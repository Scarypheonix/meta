// Package compile lowers the checked AST to the stack bytecode of internal/bytecode.
//
// It runs after the checker, so it may assume the program is well typed: a match is
// exhaustive, a field exists, an argument count is right. Where it still emits a check,
// that check is one the type system does not make — an arithmetic overflow, a division
// by zero — and it is emitted as a trap with the source span the specification requires.
//
// Objects use the tagged-slot layout of internal/layout. Origin 0.1 does not
// monomorphize yet (ADR-0010, Phase 4), so a generic field has no statically known
// shape and the tag beside each slot supplies it at run time.
package compile

import (
	"fmt"
	"math"

	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/bytecode"
	"github.com/scarypheonix/meta/internal/check"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/layout"
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
	BuiltinCmp
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
	prog *bytecode.Program

	fnIndex    map[*ast.FnDecl]int
	structIdx  map[*ast.StructDecl]int
	variantIdx map[*ast.Variant]int
	constIdx   map[constKey]int

	// Per-function state.
	fn       *bytecode.Fn
	slots    map[*resolve.Local]int
	nextSlot int
	captures map[*resolve.Local]int
	loops    []loopCtx
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
func Program(res *resolve.Result, tys *check.Result, files ...*ast.File) (*bytecode.Program, error) {
	c := &Compiler{
		res: res, tys: tys,
		prog: &bytecode.Program{
			Types:        layout.NewRegistry(),
			TupleTypes:   map[int]layout.TypeID{},
			ClosureTypes: map[int]layout.TypeID{},
			Entry:        -1,
		},
		fnIndex:    map[*ast.FnDecl]int{},
		structIdx:  map[*ast.StructDecl]int{},
		variantIdx: map[*ast.Variant]int{},
		constIdx:   map[constKey]int{},
	}
	c.prog.StringType = c.prog.Types.Add(&layout.Descriptor{
		Name: "String", Shape: layout.ByteArray, Kind: layout.ObjBytes, TypeName: "String",
	})

	// Declarations first, so a call can name a function that appears later and a
	// constructor can name a type declared in another file.
	for _, f := range files {
		c.declare(f)
	}
	if c.prog.Entry < 0 {
		return nil, fmt.Errorf("no `main` function")
	}
	c.recordPreludeVariants()
	for _, f := range files {
		if err := c.compileFile(f); err != nil {
			return nil, err
		}
	}
	return c.prog, nil
}

// recordPreludeVariants finds the variants the VM builds on its own. They are looked up
// once, here, so that a checked-arithmetic call in the VM does not search by name.
func (c *Compiler) recordPreludeVariants() {
	want := map[string]*int{
		"Option::Some":      &c.prog.Prelude.OptionSome,
		"Option::None":      &c.prog.Prelude.OptionNone,
		"Ordering::Less":    &c.prog.Prelude.Less,
		"Ordering::Equal":   &c.prog.Prelude.Equal,
		"Ordering::Greater": &c.prog.Prelude.Greater,
	}
	found := 0
	for i, v := range c.prog.Variants {
		if slot, ok := want[v.Name]; ok {
			*slot = i
			found++
		}
	}
	c.prog.Prelude.Found = found == len(want)
}

func (c *Compiler) declare(f *ast.File) {
	for _, it := range f.Items {
		switch v := it.(type) {
		case *ast.FnDecl:
			c.declareFn(v)
		case *ast.StructDecl:
			c.declareStruct(v)
		case *ast.EnumDecl:
			c.declareEnum(v)
		case *ast.ImplDecl:
			for _, m := range v.Methods {
				c.declareFn(m)
			}
		case *ast.TraitDecl:
			for _, m := range v.Methods {
				if m.Body != nil {
					c.declareFn(m)
				}
			}
		}
	}
}

func (c *Compiler) declareFn(fn *ast.FnDecl) {
	if _, ok := c.fnIndex[fn]; ok {
		return
	}
	idx := len(c.prog.Fns)
	params := len(fn.Params)
	if fn.Self != nil {
		params++
	}
	c.prog.Fns = append(c.prog.Fns, &bytecode.Fn{Name: fn.Name.Name, Params: params, Span: fn.Span()})
	c.fnIndex[fn] = idx
	if fn.Name.Name == "main" && fn.Self == nil {
		c.prog.Entry = idx
	}
}

func (c *Compiler) declareStruct(s *ast.StructDecl) {
	d := layout.TaggedDescriptor(s.Name.Name, layout.ObjStruct, len(s.Fields))
	d.TypeName = s.Name.Name
	for _, f := range s.Fields {
		d.FieldNames = append(d.FieldNames, f.Name.Name)
	}
	id := c.prog.Types.Add(d)
	c.structIdx[s] = len(c.prog.Structs)
	c.prog.Structs = append(c.prog.Structs, id)
}

func (c *Compiler) declareEnum(e *ast.EnumDecl) {
	for _, v := range e.Variants {
		payload := 0
		switch v.Kind {
		case ast.TupleVariant:
			payload = len(v.Types)
		case ast.StructVariant:
			payload = len(v.Fields)
		}
		// Each variant gets its own descriptor, so testing a variant is a type-id
		// comparison and no tag slot is needed.
		d := layout.TaggedDescriptor(e.Name.Name+"::"+v.Name.Name, layout.ObjEnum, payload)
		d.TypeName = e.Name.Name
		d.VariantName = v.Name.Name
		for _, f := range v.Fields {
			d.FieldNames = append(d.FieldNames, f.Name.Name)
		}
		id := c.prog.Types.Add(d)

		c.variantIdx[v] = len(c.prog.Variants)
		c.prog.Variants = append(c.prog.Variants, bytecode.VariantInfo{
			Type: id, Tag: len(c.prog.Variants), Payload: payload,
			Name: e.Name.Name + "::" + v.Name.Name,
		})
	}
}

// tupleType returns the descriptor for a tuple of the given arity, creating it once.
func (c *Compiler) tupleType(n int) layout.TypeID {
	if id, ok := c.prog.TupleTypes[n]; ok {
		return id
	}
	d := layout.TaggedDescriptor(fmt.Sprintf("(tuple/%d)", n), layout.ObjTuple, n)
	id := c.prog.Types.Add(d)
	c.prog.TupleTypes[n] = id
	return id
}

// closureType returns the descriptor for a closure with n captures. Slot 0 holds the
// function index, so the object has n+1 slots.
func (c *Compiler) closureType(n int) layout.TypeID {
	if id, ok := c.prog.ClosureTypes[n]; ok {
		return id
	}
	d := layout.TaggedDescriptor(fmt.Sprintf("(closure/%d)", n), layout.ObjClosure, n+1)
	id := c.prog.Types.Add(d)
	c.prog.ClosureTypes[n] = id
	return id
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
