// Package compile lowers the checked AST to the stack bytecode of internal/bytecode.
//
// It runs after the checker, so it may assume the program is well typed: a match is
// exhaustive, a field exists, an argument count is right. Where it still emits a check,
// that check is one the type system does not make — an arithmetic overflow, a division
// by zero — and it is emitted as a trap with the source span the specification requires.
//
// A struct, tuple, enum variant or closure gets one exact layout (internal/layout's
// Fixed shape) per distinct tuple of concrete field types actually constructed, keyed
// the same way internal/mono keys a function instance (ADR-0019). Every construction
// site is compiled inside some mono.Instance's body, and that instance's own
// substitution is what turns a field's declared type -- possibly still in terms of the
// struct's or the instance's own generic parameters -- into something concrete.
package compile

import (
	"fmt"
	"math"

	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/bytecode"
	"github.com/scarypheonix/meta/internal/check"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/layout"
	"github.com/scarypheonix/meta/internal/mono"
	"github.com/scarypheonix/meta/internal/resolve"
	"github.com/scarypheonix/meta/internal/types"
)

// Builtin indices, shared with the VM. They are small and fixed rather than looked up
// by name at run time.
const (
	BuiltinPrint = iota
	BuiltinPrintln
	BuiltinPanic
	BuiltinRefEq
	// BuiltinCmpInt, BuiltinCmpUint, BuiltinCmpFloat and BuiltinCmpString are one
	// builtin per receiver kind rather than one `cmp` shared by all of them: the VM
	// dispatches on its own value's runtime tag regardless and treats all four
	// identically, but native code cannot ask a register what it holds, and
	// OpCallBuiltin carries no operand-kind operand the way OpToStr does (there is
	// nowhere to put one without widening bytecode.Instr). Picking the builtin index
	// itself by kind, at compile time, answers the same question through the one
	// channel that already exists.
	BuiltinCmpInt
	BuiltinCmpUint
	BuiltinCmpFloat
	BuiltinCmpString
	BuiltinCheckedAdd
	BuiltinCheckedSub
	BuiltinCheckedMul
	BuiltinSaturatingAdd
	BuiltinSaturatingSub
	BuiltinSaturatingMul
)

// Compiler holds the state of one program's lowering.
type Compiler struct {
	res  *resolve.Result
	tys  *check.Result
	mono *mono.Result
	prog *bytecode.Program

	// instIndex maps a monomorphized instance to its bytecode function. A non-generic
	// function has exactly one instance, so it behaves exactly as before ADR-0010's
	// instances existed; a generic one gets one bytecode function per distinct tuple of
	// type arguments actually used (docs/spec/06-traits-generics.md).
	instIndex map[*mono.Instance]int
	// rootIndex maps a declaration with no generic parameters of its own to its single
	// instance, for a reference that mono did not resolve because the call itself is
	// not generic (an ordinary function value or a plain call).
	rootIndex map[*ast.FnDecl]*mono.Instance
	constIdx  map[constKey]int

	// structCache, variantCache, tupleCache and closureCache memoize exact layouts by
	// a key built from the concrete type-argument tuple, so two construction sites
	// that end up at the same concrete shape share one descriptor (ADR-0019).
	structCache  map[string]int
	variantCache map[string]int
	tupleCache   map[string]layout.TypeID
	closureCache map[string]layout.TypeID

	// Per-function state.
	fn       *bytecode.Fn
	inst     *mono.Instance
	slots    map[*resolve.Local]int
	nextSlot int
	captures map[*resolve.Local]int
	loops    []loopCtx

	// optionDef and optionSome name the prelude's `Option` enum and its `Some` variant,
	// found once alongside forcePreludeVariants' own lookup. forExpr reuses them to test
	// the `Option[Item]` a `for` loop's desugared `next()` call returns, at whatever
	// concrete `Item` the loop's pattern needs (ADR-0019: every instantiation gets its
	// own exact layout, so this cannot be precomputed the way forcePreludeVariants
	// precomputes Option[i64]).
	optionDef  *types.Def
	optionSome *ast.Variant
}

type constKey struct {
	kind bytecode.ConstKind
	bits uint64
	str  string
}

type loopCtx struct {
	// breaks and continues are jump positions to patch once the loop's extent is known.
	breaks    []int
	continues []int
	// isValueLoop marks a `loop`, whose breaks carry a value.
	isValueLoop bool
}

// Program lowers a checked program.
//
// mo is the instantiation set from internal/mono: which specialized copy of each
// generic function exists, and which copy every call site reaches. A non-generic
// program has exactly one instance per declaration and this behaves as it did before
// ADR-0010 landed.
func Program(res *resolve.Result, tys *check.Result, mo *mono.Result, files ...*ast.File) (*bytecode.Program, error) {
	c := &Compiler{
		res: res, tys: tys, mono: mo,
		prog: &bytecode.Program{
			Types: layout.NewRegistry(),
			Entry: -1,
		},
		instIndex:    map[*mono.Instance]int{},
		rootIndex:    map[*ast.FnDecl]*mono.Instance{},
		constIdx:     map[constKey]int{},
		structCache:  map[string]int{},
		variantCache: map[string]int{},
		tupleCache:   map[string]layout.TypeID{},
		closureCache: map[string]layout.TypeID{},
	}
	c.prog.StringType = c.prog.Types.Add(&layout.Descriptor{
		Name: "String", Shape: layout.ByteArray, Kind: layout.ObjBytes, TypeName: "String",
	})
	// The canonical captureless-closure shape: slot 0 only, the function index. Every
	// zero-capture lambda shares it, and it is also what a bare top-level function
	// reference is boxed into when it is written into a reference-shaped field
	// (ADR-0019) -- guaranteed to exist even if the program never writes one there.
	c.prog.FnBoxType = c.prog.Types.Add(&layout.Descriptor{
		Name: "(fn)", Shape: layout.Fixed, Words: 1,
		Kinds: []layout.WordKind{layout.WordInt}, Kind: layout.ObjClosure,
	})
	c.closureCache[kindsKey([]layout.WordKind{layout.WordInt})] = c.prog.FnBoxType

	// Every instance mono found gets a bytecode function, indexed in instantiation
	// order so the result is deterministic. instIndex is filled first, in full, so
	// that a call from one instance to another compiled later still resolves.
	for _, inst := range mo.Instances {
		idx := len(c.prog.Fns)
		params := len(inst.Decl.Params)
		if inst.Decl.Self != nil {
			params++
		}
		c.prog.Fns = append(c.prog.Fns, &bytecode.Fn{Name: inst.Name, Params: params, Span: inst.Decl.Span()})
		c.instIndex[inst] = idx
		if len(inst.Args) == 0 {
			c.rootIndex[inst.Decl] = inst
		}
	}
	if mo.Entry == nil {
		return nil, fmt.Errorf("no `main` function")
	}
	c.prog.Entry = c.instIndex[mo.Entry]

	if err := c.forcePreludeVariants(files[0]); err != nil {
		return nil, err
	}
	for _, inst := range mo.Instances {
		if err := c.compileInstance(inst); err != nil {
			return nil, err
		}
	}
	return c.prog, nil
}

// forcePreludeVariants builds the specific instantiations the VM constructs directly,
// with no compiled call site to attach a concrete type to: Option::Some[i64] and
// Option::None for the checked-arithmetic builtins, Ordering's three variants for
// `cmp`. Their indices must be known before any user code runs.
func (c *Compiler) forcePreludeVariants(prelude *ast.File) error {
	option, ordering := findEnum(prelude, "Option"), findEnum(prelude, "Ordering")
	if option == nil || ordering == nil {
		return nil // the prelude does not declare them; nothing to force
	}
	optionDef, orderingDef := c.tys.Defs[ast.Item(option)], c.tys.Defs[ast.Item(ordering)]
	if optionDef == nil || orderingDef == nil {
		return nil
	}
	optionI64 := &types.Named{Def: optionDef, Args: []types.Type{types.P(types.I64)}}
	orderingT := &types.Named{Def: orderingDef}

	some, none := findVariant(option, "Some"), findVariant(option, "None")
	less, equal, greater := findVariant(ordering, "Less"), findVariant(ordering, "Equal"), findVariant(ordering, "Greater")
	if some == nil || none == nil || less == nil || equal == nil || greater == nil {
		return nil
	}
	c.optionDef, c.optionSome = optionDef, some

	var p bytecode.PreludeVariants
	var err error
	if p.OptionSome, err = c.variantInst(some, optionI64); err != nil {
		return err
	}
	if p.OptionNone, err = c.variantInst(none, optionI64); err != nil {
		return err
	}
	if p.Less, err = c.variantInst(less, orderingT); err != nil {
		return err
	}
	if p.Equal, err = c.variantInst(equal, orderingT); err != nil {
		return err
	}
	if p.Greater, err = c.variantInst(greater, orderingT); err != nil {
		return err
	}
	p.Found = true
	c.prog.Prelude = p
	return nil
}

func findEnum(f *ast.File, name string) *ast.EnumDecl {
	for _, it := range f.Items {
		if e, ok := it.(*ast.EnumDecl); ok && e.Name.Name == name {
			return e
		}
	}
	return nil
}

func findVariant(e *ast.EnumDecl, name string) *ast.Variant {
	for _, v := range e.Variants {
		if v.Name.Name == name {
			return v
		}
	}
	return nil
}

// wordKindOf translates the backend's operand kind (internal/compile/ops.go) into the
// GC- and Show-facing kind an exact-layout word is recorded with. It is total over
// every kind a field can concretely have; KindUnknown means the checker left something
// unresolved, which is a compiler bug by the time a construction site is compiled.
func wordKindOf(k bytecode.Kind) (layout.WordKind, bool) {
	switch k {
	case bytecode.KindFloat:
		return layout.WordFloat, true
	case bytecode.KindBool:
		return layout.WordBool, true
	case bytecode.KindChar:
		return layout.WordChar, true
	case bytecode.KindUnit:
		return layout.WordUnit, true
	case bytecode.KindInt, bytecode.KindUint:
		return layout.WordInt, true
	case bytecode.KindString, bytecode.KindRef:
		return layout.WordRef, true
	}
	return layout.WordRaw, false
}

// wordKindFor derives a field's exact word kind from its concrete type, falling back
// to WordInt for a type that is still an unresolved inference variable. concreteType
// only ever leaves one of those behind when the checker's own value-restriction
// analysis already proved the value can never be observably read (see its doc
// comment), so any kind is sound here and WordInt is simply the cheapest one that
// keeps the collector from tracing it.
func wordKindFor(t types.Type) (layout.WordKind, bool) {
	if _, stillVar := t.(*types.Var); stillVar {
		return layout.WordInt, true
	}
	return wordKindOf(kindOfType(t))
}

func kindsKey(kinds []layout.WordKind) string {
	buf := make([]byte, len(kinds))
	for i, k := range kinds {
		buf[i] = byte(k)
	}
	return string(buf)
}

// concreteType substitutes the instance currently compiling into t, then applies the
// same end-of-body defaulting the checker itself would have (an integer or float
// literal's variable defaults to i64/f64).
//
// A checked type inside a generic function's body may still name that function's own
// parameters (or, for a trait method, the enclosing impl's, or Self); Instance.Subst is
// what mono resolved them to for this particular specialization, which is what object
// layout needs the field's *actual* type to be (ADR-0019).
//
// Defaulting here catches what the checker's own end-of-body sweep provably could not:
// a value the value restriction generalized and that is never instantiated again --
// `let used = 1; let ignored = (used, used);` with `ignored` itself unread -- is left
// with a free variable on purpose, because nothing observable depends on what its type
// would have been. But internal/compile must still emit *something* for that dead
// construction before a later pass deletes it, and a Fixed-shape word needs a concrete
// kind to be written with the right one. types.ApplyDefaults is exactly the fallback
// the checker itself uses for the one case that does need a real answer (an
// under-constrained literal); a variable with no default left at this point is,
// by the checker's own analysis, provably never read, so which concrete kind it gets
// cannot be observed either way -- WordInt's zero value, wordKindOf's fallback, is
// exactly as sound as any other choice.
func (c *Compiler) concreteType(t types.Type) types.Type {
	if c.inst != nil && len(c.inst.Subst) > 0 {
		t = types.Substitute(t, c.inst.Subst)
	}
	types.ApplyDefaults(t)
	return types.Prune(t)
}

// concreteTypeOf is concreteType over an expression's checked type.
func (c *Compiler) concreteTypeOf(e ast.Expr) types.Type {
	return c.concreteType(c.typeOf(e))
}

// patTypeOf is concreteType over the type a pattern node matches against.
func (c *Compiler) patTypeOf(id ast.NodeID) types.Type {
	if t, ok := c.tys.PatTypes[id]; ok {
		return c.concreteType(t)
	}
	return types.Error
}

// fieldKinds computes the exact per-word kind of a struct's or a variant's payload,
// substituting the definition's own generic parameters with the concrete arguments
// this instantiation supplies, then the instance currently compiling on top of that
// (a field's declared type can itself mention the instance's parameters, e.g. a
// generic struct built inside a generic function).
func (c *Compiler) fieldKinds(declared []types.Type, params []*types.Param, args []types.Type, span diag.Span) ([]layout.WordKind, error) {
	subst := make(map[*types.Param]types.Type, len(params))
	for i, p := range params {
		if i < len(args) {
			subst[p] = args[i]
		}
	}
	kinds := make([]layout.WordKind, len(declared))
	for i, ft := range declared {
		concrete := c.concreteType(types.Substitute(ft, subst))
		k, ok := wordKindFor(concrete)
		if !ok {
			return nil, unsupported(fmt.Sprintf("a field of type %s has no object layout yet", concrete), span)
		}
		kinds[i] = k
	}
	return kinds, nil
}

// structInst returns the bytecode struct index for one concrete instantiation of t,
// building its exact layout on first use and reusing it for every later construction
// at the same concrete field types.
func (c *Compiler) structInst(t types.Type, span diag.Span) (int, error) {
	named, ok := types.AsNamed(c.concreteType(t))
	if !ok || named.Def == nil || named.Def.Struct == nil {
		return 0, unsupported(fmt.Sprintf("constructing %s, which is not a concrete struct type", t), span)
	}
	key := "struct:" + instKey(named)
	if idx, ok := c.structCache[key]; ok {
		return idx, nil
	}
	kinds, err := c.fieldKinds(named.Def.FieldTypes, named.Def.Params, named.Args, span)
	if err != nil {
		return 0, err
	}
	d := layout.FixedDescriptor(key, kinds)
	d.Kind, d.TypeName = layout.ObjStruct, named.Def.Name
	for _, f := range named.Def.Struct.Fields {
		d.FieldNames = append(d.FieldNames, f.Name.Name)
	}
	id := c.prog.Types.Add(d)
	idx := len(c.prog.Structs)
	c.prog.Structs = append(c.prog.Structs, id)
	c.structCache[key] = idx
	return idx, nil
}

// variantInst returns the bytecode variant index for one concrete instantiation of a
// variant, building its exact layout on first use.
func (c *Compiler) variantInst(variant *ast.Variant, enumT types.Type) (int, error) {
	named, ok := types.AsNamed(c.concreteType(enumT))
	if !ok || named.Def == nil || named.Def.Enum == nil {
		return 0, unsupported(fmt.Sprintf("matching or building %s, which is not a concrete enum type", enumT), variant.Span())
	}
	pos := -1
	for i, v := range named.Def.Enum.Variants {
		if v == variant {
			pos = i
			break
		}
	}
	if pos < 0 || pos >= len(named.Def.VariantTypes) {
		return 0, unsupported("an unknown variant", variant.Span())
	}
	key := "variant:" + instKey(named) + "::" + variant.Name.Name
	if idx, ok := c.variantCache[key]; ok {
		return idx, nil
	}
	kinds, err := c.fieldKinds(named.Def.VariantTypes[pos], named.Def.Params, named.Args, variant.Span())
	if err != nil {
		return 0, err
	}
	d := layout.FixedDescriptor(key, kinds)
	d.Kind, d.TypeName, d.VariantName = layout.ObjEnum, named.Def.Name, variant.Name.Name
	for _, f := range variant.Fields {
		d.FieldNames = append(d.FieldNames, f.Name.Name)
	}
	id := c.prog.Types.Add(d)
	idx := len(c.prog.Variants)
	c.prog.Variants = append(c.prog.Variants, bytecode.VariantInfo{
		Type: id, Tag: idx, Payload: len(kinds), Name: named.Def.Name + "::" + variant.Name.Name,
	})
	c.variantCache[key] = idx
	return idx, nil
}

// tupleInst returns the descriptor for one concrete tuple shape, building it on first
// use. A tuple's element types are already concrete at every construction site (they
// come straight from inference, not from a declaration's own parameters), so only the
// instance substitution applies.
func (c *Compiler) tupleInst(t types.Type, span diag.Span) (layout.TypeID, error) {
	tup, ok := c.concreteType(t).(*types.TupleT)
	if !ok {
		return 0, unsupported(fmt.Sprintf("constructing %s, which is not a concrete tuple type", t), span)
	}
	kinds := make([]layout.WordKind, len(tup.Elems))
	for i, e := range tup.Elems {
		k, ok := wordKindFor(c.concreteType(e))
		if !ok {
			return 0, unsupported(fmt.Sprintf("a tuple element of type %s has no object layout yet", e), span)
		}
		kinds[i] = k
	}
	key := "tuple:" + kindsKey(kinds)
	if id, ok := c.tupleCache[key]; ok {
		return id, nil
	}
	d := layout.FixedDescriptor(fmt.Sprintf("(tuple/%d)", len(kinds)), kinds)
	d.Kind = layout.ObjTuple
	id := c.prog.Types.Add(d)
	c.tupleCache[key] = id
	return id, nil
}

// closureInst returns the descriptor for a closure over exactly these captures, in
// order. Slot 0 is always the function index; two closures that capture the same
// types, even from different lambda literals, share one descriptor.
func (c *Compiler) closureInst(caps []*resolve.Local, span diag.Span) (layout.TypeID, error) {
	kinds := make([]layout.WordKind, 0, len(caps)+1)
	kinds = append(kinds, layout.WordInt)
	for _, l := range caps {
		k, ok := wordKindFor(c.concreteType(c.tys.LocalTypes[l]))
		if !ok {
			return 0, unsupported("a capture whose type has no object layout yet", span)
		}
		kinds = append(kinds, k)
	}
	key := kindsKey(kinds)
	if id, ok := c.closureCache[key]; ok {
		return id, nil
	}
	d := layout.FixedDescriptor(fmt.Sprintf("(closure/%d)", len(caps)), kinds)
	d.Kind = layout.ObjClosure
	id := c.prog.Types.Add(d)
	c.closureCache[key] = id
	return id, nil
}

// instKey identifies a named type's concrete instantiation for the layout caches. It is
// built from pointer identity for the definition, like internal/mono's own instance
// key, so that two types sharing a name in different modules never collide.
func instKey(n *types.Named) string {
	var sb []byte
	sb = fmt.Appendf(sb, "%p", n.Def)
	for _, a := range n.Args {
		sb = append(sb, '|')
		sb = append(sb, typeKey(a)...)
	}
	return string(sb)
}

func typeKey(t types.Type) string {
	switch v := types.Prune(t).(type) {
	case *types.Prim:
		return v.Kind.String()
	case *types.Named:
		return "N" + instKey(v)
	case *types.TupleT:
		s := "T("
		for _, e := range v.Elems {
			s += typeKey(e) + ","
		}
		return s + ")"
	case *types.FnT:
		s := "F("
		for _, p := range v.Params {
			s += typeKey(p) + ","
		}
		return s + ")->" + typeKey(v.Ret)
	default:
		return fmt.Sprintf("%s", t)
	}
}

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

func (c *Compiler) constant(k bytecode.ConstKind, bits uint64, str string) int {
	key := constKey{kind: k, bits: bits, str: str}
	if i, ok := c.constIdx[key]; ok {
		return i
	}
	i := len(c.prog.Consts)
	c.prog.Consts = append(c.prog.Consts, bytecode.Const{Kind: k, Bits: bits, Str: str})
	c.constIdx[key] = i
	return i
}

func (c *Compiler) intConst(v int64) int {
	return c.constant(bytecode.ConstInt, uint64(v), "")
}

func (c *Compiler) strConst(s string) int {
	return c.constant(bytecode.ConstString, 0, s)
}

// ---------------------------------------------------------------------------
// Emitting
// ---------------------------------------------------------------------------

func (c *Compiler) emit(op bytecode.Op, span diag.Span) int {
	c.fn.Code = append(c.fn.Code, bytecode.Instr{Op: op, Span: span})
	return len(c.fn.Code) - 1
}

func (c *Compiler) emitA(op bytecode.Op, a int, span diag.Span) int {
	c.fn.Code = append(c.fn.Code, bytecode.Instr{Op: op, A: int32(a), Span: span})
	return len(c.fn.Code) - 1
}

func (c *Compiler) emitAB(op bytecode.Op, a, b int, span diag.Span) int {
	c.fn.Code = append(c.fn.Code, bytecode.Instr{Op: op, A: int32(a), B: int32(b), Span: span})
	return len(c.fn.Code) - 1
}

// patch points a previously emitted jump at the current end of the code.
func (c *Compiler) patch(at int) {
	c.fn.Code[at].A = int32(len(c.fn.Code))
}

func (c *Compiler) here() int { return len(c.fn.Code) }

// slotOf returns the frame slot for a binding, allocating one on first use.
func (c *Compiler) slotOf(l *resolve.Local) int {
	if s, ok := c.slots[l]; ok {
		return s
	}
	s := c.nextSlot
	c.nextSlot++
	c.slots[l] = s
	if c.nextSlot > c.fn.Locals {
		c.fn.Locals = c.nextSlot
	}
	return s
}

// temp allocates an anonymous frame slot, used for a match scrutinee or an extracted
// payload.
func (c *Compiler) temp() int {
	s := c.nextSlot
	c.nextSlot++
	if c.nextSlot > c.fn.Locals {
		c.fn.Locals = c.nextSlot
	}
	return s
}

// typeOf returns an expression's checked type.
func (c *Compiler) typeOf(e ast.Expr) types.Type {
	if t, ok := c.tys.ExprTypes[e.NodeID()]; ok {
		return types.Prune(t)
	}
	return types.Error
}

// isFloat reports whether an expression's type is a float, which selects the
// non-trapping arithmetic opcodes.
func (c *Compiler) isFloat(e ast.Expr) bool {
	p, ok := types.AsPrim(c.typeOf(e))
	return ok && p.Kind.IsFloat()
}

func unsupported(what string, span diag.Span) error {
	return fmt.Errorf("unimplemented: %s at %s", what, span)
}

var _ = math.MaxInt64

// mathFloat64bits is math.Float64bits, named locally so the import stays obvious at the
// one place floats cross into the constant pool.
func mathFloat64bits(f float64) uint64 { return math.Float64bits(f) }

// primConstValue evaluates `i64::MAX` and friends at compile time.
func primConstValue(prim, member string) (int64, error) {
	info, ok := map[string]struct {
		bits   uint
		signed bool
	}{
		"i8": {8, true}, "i16": {16, true}, "i32": {32, true}, "i64": {64, true},
		"u8": {8, false}, "u16": {16, false}, "u32": {32, false}, "u64": {64, false},
	}[prim]
	if !ok {
		return 0, fmt.Errorf("`%s` has no associated constants", prim)
	}
	switch member {
	case "BITS":
		return int64(info.bits), nil
	case "MIN":
		if !info.signed {
			return 0, nil
		}
		return -(int64(1) << (info.bits - 1)), nil
	case "MAX":
		if info.signed {
			return int64(1)<<(info.bits-1) - 1, nil
		}
		if info.bits >= 64 {
			// The VM's value model is one machine integer, so u64::MAX has no
			// representation yet (docs/deferred.md, Phase 3).
			return 0, fmt.Errorf("unimplemented: `u64::MAX` needs real integer widths")
		}
		return int64(1)<<info.bits - 1, nil
	}
	return 0, fmt.Errorf("`%s` has no associated constant `%s`", prim, member)
}
