package check

import (
	"math"

	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/resolve"
	"github.com/scarypheonix/meta/internal/types"
)

// literalUse remembers an integer literal and the type it ended up with, so its range
// can be checked once inference has settled (spec/01-lexical.md: a literal whose value
// does not fit its resolved type is rejected at compile time).
type literalUse struct {
	lit *ast.IntLit
	ty  types.Type
}

// infer computes an expression's type, records it, and returns it.
func (c *Checker) infer(e ast.Expr) types.Type {
	t := c.inferUncached(e)
	if e != nil {
		c.record(e.NodeID(), t)
	}
	return t
}

func (c *Checker) inferUncached(e ast.Expr) types.Type {
	switch v := e.(type) {
	case nil:
		return types.Unit()

	case *ast.ErrorExpr:
		return types.Error

	case *ast.IntLit:
		return c.inferIntLit(v)

	case *ast.FloatLit:
		switch v.Suffix {
		case "f32":
			return types.P(types.F32)
		case "f64":
			return types.P(types.F64)
		}
		return c.ctx.FreshDefaulting(types.FloatDefault)

	case *ast.StrLit:
		return types.P(types.String)

	case *ast.CharLit:
		return types.P(types.Char)

	case *ast.BoolLit:
		return types.P(types.Bool)

	case *ast.SelfExpr:
		if c.env.self == nil {
			c.bag.Errorf("E0424", v.Span(), "`self` is not available here").
				Label("only a method's body may use `self`").
				Note("a method is a function declared inside an `impl` or a `trait` that takes `self`").
				Help("add a `self` parameter, or take the value as an ordinary parameter")
			return types.Error
		}
		return c.env.self

	case *ast.PathExpr:
		return c.inferPath(v)

	case *ast.StructLit:
		return c.inferStructLit(v)

	case *ast.TupleExpr:
		if len(v.Elems) == 0 {
			return types.Unit()
		}
		elems := make([]types.Type, 0, len(v.Elems))
		for _, el := range v.Elems {
			elems = append(elems, c.infer(el))
		}
		return &types.TupleT{Elems: elems}

	case *ast.Lambda:
		return c.inferLambda(v)

	case *ast.Block:
		return c.checkBlock(v)

	case *ast.If:
		return c.inferIf(v)

	case *ast.Match:
		return c.inferMatch(v)

	case *ast.While:
		c.expectBool(v.Cond, "a `while` condition")
		c.expectUnitBlock(v.Body, "a `while` body")
		return types.Unit()

	case *ast.For:
		return c.inferFor(v)

	case *ast.Loop:
		return c.inferLoop(v)

	case *ast.Break:
		return c.inferBreak(v)

	case *ast.Continue:
		return types.P(types.Never)

	case *ast.Return:
		var got types.Type = types.Unit()
		if v.Value != nil {
			got = c.infer(v.Value)
		}
		c.unify(c.ret, got, v.Span(), "in this `return`")
		return types.P(types.Never)

	case *ast.Unary:
		return c.inferUnary(v)

	case *ast.Binary:
		return c.inferBinary(v)

	case *ast.Assign:
		return c.inferAssign(v)

	case *ast.Cast:
		return c.inferCast(v)

	case *ast.Call:
		return c.inferCall(v)

	case *ast.MethodCall:
		return c.inferMethodCall(v)

	case *ast.FieldAccess:
		return c.inferField(v)

	case *ast.Try:
		return c.inferTry(v)
	}
	return types.Error
}

func (c *Checker) inferIntLit(v *ast.IntLit) types.Type {
	var t types.Type
	if k, ok := primKinds[v.Suffix]; ok && v.Suffix != "" {
		t = types.P(k)
	} else {
		t = c.ctx.FreshDefaulting(types.IntDefault)
	}
	c.intLits = append(c.intLits, literalUse{lit: v, ty: t})
	return t
}

// checkLiteralRanges verifies every integer literal fits the type inference gave it.
func (c *Checker) checkLiteralRanges() {
	for _, use := range c.intLits {
		p, ok := types.AsPrim(use.ty)
		if !ok || !p.Kind.IsInteger() {
			continue
		}
		if use.lit.Overflow || !fitsIn(use.lit.Value, p.Kind) {
			d := c.bag.Errorf("E0308", use.lit.Span(),
				"literal out of range for `%s`", p.Kind).
				Label("this value does not fit").
				Note("`%s` holds %s", p.Kind, rangeText(p.Kind))
			if p.Kind.IsSigned() && !use.lit.Overflow && use.lit.Value == uint64(1)<<(p.Kind.Bits()-1) {
				d.Help("the most negative value of `%s` is written `%s::MIN`", p.Kind, p.Kind)
			}
		}
	}
}

func fitsIn(v uint64, k types.PrimKind) bool {
	bits := k.Bits()
	if bits == 0 {
		return true
	}
	if k.IsSigned() {
		// Negation is a separate operator, so the literal itself must fit the positive
		// range: `-128i8` is `-(128i8)`, and `128` does not fit `i8`. The minimum is
		// written `i8::MIN` (spec/01-lexical.md).
		return v <= uint64(1)<<(bits-1)-1
	}
	if bits >= 64 {
		return true
	}
	return v < uint64(1)<<bits
}

func rangeText(k types.PrimKind) string {
	bits := k.Bits()
	if k.IsSigned() {
		hi := uint64(1)<<(bits-1) - 1
		return "values from -" + u64s(uint64(1)<<(bits-1)) + " to " + u64s(hi)
	}
	if bits >= 64 {
		return "values from 0 to 18446744073709551615"
	}
	return "values from 0 to " + u64s(uint64(1)<<bits-1)
}

func u64s(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

func (c *Checker) inferPath(v *ast.PathExpr) types.Type {
	ref, ok := c.res.Ref(v.NodeID())
	if !ok || ref.Kind == resolve.Unresolved {
		return types.Error
	}
	switch ref.Kind {
	case resolve.LocalVar:
		return c.instantiateLocal(c.localScheme(ref.Local), v.Span())

	case resolve.Fn:
		return c.instantiateFn(v, ref.Fn)

	case resolve.Const:
		return c.constType(ref.Const)

	case resolve.Variant:
		return c.variantValue(v, ref)

	case resolve.Builtin:
		return c.builtinType(ref.Builtin, v.Args, v.Span())

	case resolve.PrimConst:
		k, ok := primKinds[ref.Name]
		if !ok || !k.IsInteger() {
			c.bag.Errorf("E0599", v.Span(), "`%s` has no associated constant `%s`", ref.Name, ref.Member).
				Label("associated constants exist on the integer types").
				Note("`MIN`, `MAX` and `BITS` are declared for `i8` through `u64`")
			return types.Error
		}
		if ref.Member == "BITS" {
			return types.P(types.U32)
		}
		return types.P(k)

	case resolve.Struct, resolve.Enum, resolve.Trait, resolve.TypeAlias, resolve.Prim:
		d := c.bag.Errorf("E0423", v.Span(), "`%s` is a type, not a value", v.Path).
			Label("expected a value here")
		if ref.Kind == resolve.Struct {
			d.Note("a struct is built with a literal, not called")
			d.Help("write `%s { field: value, .. }`", v.Path)
		} else {
			d.Note("a type names a set of values; it is not one of them")
		}
		return types.Error
	}
	return types.Error
}

// constType returns a constant's declared type. Constants are checked separately, so
// this only reads the annotation.
func (c *Checker) constType(decl *ast.ConstDecl) types.Type {
	saved := c.env
	c.env = typeEnv{}
	t := c.toType(decl.Type)
	c.env = saved
	return t
}

// instantiateFn gives a function reference its type at this use site, with fresh
// variables for its generic parameters and its bounds recorded as obligations.
func (c *Checker) instantiateFn(v *ast.PathExpr, fn *ast.FnDecl) types.Type {
	sig := c.fnSigs[fn]
	if sig == nil {
		return types.Error
	}
	subst := c.instantiateParams(sig.Params, v.Args, v.Span(), fn.Name.Name)
	for _, b := range sig.Bounds {
		c.requireBound(types.Substitute(b.Type, subst), b.Trait, v.Span())
	}
	// The arguments are recorded unresolved: inference has not run yet. They are pruned
	// when monomorphization reads them, which is after the whole program has been checked.
	//
	// A *non-generic* callee is recorded too, with no arguments. It has exactly one
	// instance either way, so this says nothing new about which copy runs -- what it says
	// is that this call site reaches it at all, which is how monomorphization knows a
	// prelude function is used and every other one is not (internal/mono's Program).
	inst := &Inst{Decl: fn, Params: sig.Params}
	for _, p := range sig.Params {
		inst.Args = append(inst.Args, subst[p])
	}
	c.out.Insts[v.NodeID()] = inst
	params := make([]types.Type, 0, len(sig.ParamTypes))
	for _, p := range sig.ParamTypes {
		params = append(params, types.Substitute(p, subst))
	}
	return &types.FnT{Params: params, Ret: types.Substitute(sig.Ret, subst)}
}

// instantiateParams builds the substitution for a generic use site, honouring explicit
// type arguments when they are written.
func (c *Checker) instantiateParams(params []*types.Param, args []ast.Type, span diag.Span, what string) map[*types.Param]types.Type {
	subst := make(map[*types.Param]types.Type, len(params))
	if len(args) > 0 && len(args) != len(params) {
		c.bag.Errorf("E0107", span,
			"`%s` takes %d type argument(s) but %d were supplied", what, len(params), len(args)).
			Label("wrong number of type arguments").
			Note("`%s` declares %s", what, paramNames(params)).
			Help("supply every argument, or omit them all and let inference choose")
		args = nil
	}
	for i, p := range params {
		if i < len(args) {
			subst[p] = c.toType(args[i])
		} else {
			subst[p] = c.freshFor(span)
		}
	}
	return subst
}

// variantValue types an enum variant used as an expression: a unit variant is a value,
// a tuple variant is its constructor function.
func (c *Checker) variantValue(v *ast.PathExpr, ref resolve.Ref) types.Type {
	def := c.defs[ast.Item(ref.Enum)]
	if def == nil {
		return types.Error
	}
	subst, args := c.freshArgs(def, v.Args, v.Span())
	result := &types.Named{Def: def, Args: args}

	idx := variantIndex(def, ref.Variant)
	if idx < 0 {
		return types.Error
	}
	switch ref.Variant.Kind {
	case ast.UnitVariant:
		return result
	case ast.TupleVariant:
		params := make([]types.Type, 0, len(def.VariantTypes[idx]))
		for _, p := range def.VariantTypes[idx] {
			params = append(params, types.Substitute(p, subst))
		}
		return &types.FnT{Params: params, Ret: result}
	default:
		c.bag.Errorf("E0533", v.Span(), "`%s` is a struct variant", ref.Variant.Name.Name).
			Label("build it with braces").
			Help("write `%s { .. }`", v.Path)
		return types.Error
	}
}

// freshArgs instantiates a definition's generic parameters with fresh variables, or with
// the explicit type arguments if any were written.
func (c *Checker) freshArgs(def *types.Def, explicit []ast.Type, span diag.Span) (map[*types.Param]types.Type, []types.Type) {
	subst := c.instantiateParams(def.Params, explicit, span, def.Name)
	args := make([]types.Type, 0, len(def.Params))
	for _, p := range def.Params {
		args = append(args, subst[p])
	}
	return subst, args
}

func variantIndex(def *types.Def, va *ast.Variant) int {
	if def.Enum == nil {
		return -1
	}
	for i, v := range def.Enum.Variants {
		if v == va {
			return i
		}
	}
	return -1
}

// builtinType gives the compiler-provided functions their signatures.
func (c *Checker) builtinType(name string, targs []ast.Type, span diag.Span) types.Type {
	if t := c.concurrencyBuiltinType(name, targs, span); t != nil {
		return t
	}
	if t := c.arrayBuiltinType(name, targs, span); t != nil {
		return t
	}
	if t := c.strBuiltinType(name); t != nil {
		return t
	}
	str := types.P(types.String)
	switch name {
	case "io::print", "io::println":
		return &types.FnT{Params: []types.Type{str}, Ret: types.Unit()}
	case "panic":
		return &types.FnT{Params: []types.Type{str}, Ret: types.P(types.Never)}
	case "ref_eq":
		// `ref_eq` compares two aggregates of the same type for identity.
		v := c.freshFor(span)
		return &types.FnT{Params: []types.Type{v, v}, Ret: types.P(types.Bool)}
	}
	return types.Error
}

func (c *Checker) inferStructLit(v *ast.StructLit) types.Type {
	ref, ok := c.res.Ref(v.NodeID())
	if !ok || ref.Kind == resolve.Unresolved {
		for _, f := range v.Fields {
			c.infer(f.Value)
		}
		return types.Error
	}

	switch ref.Kind {
	case resolve.Struct:
		def := c.defs[ast.Item(ref.Struct)]
		if def == nil {
			return types.Error
		}
		subst, args := c.freshArgs(def, v.Args, v.Span())
		c.checkFieldInits(v, ref.Struct.Fields, def.FieldTypes, subst, def.Name)
		return &types.Named{Def: def, Args: args}

	case resolve.Variant:
		def := c.defs[ast.Item(ref.Enum)]
		if def == nil {
			return types.Error
		}
		if ref.Variant.Kind != ast.StructVariant {
			c.bag.Errorf("E0533", v.Span(), "`%s` is not a struct variant", ref.Variant.Name.Name).
				Label("this variant does not have named fields")
			return types.Error
		}
		subst, args := c.freshArgs(def, v.Args, v.Span())
		idx := variantIndex(def, ref.Variant)
		if idx >= 0 {
			c.checkFieldInits(v, ref.Variant.Fields, def.VariantTypes[idx], subst, ref.Variant.Name.Name)
		}
		return &types.Named{Def: def, Args: args}
	}

	c.bag.Errorf("E0574", v.Span(), "`%s` cannot be built with a struct literal", v.Path).
		Label("expected a struct or a struct variant")
	return types.Error
}

// checkFieldInits verifies a struct literal supplies exactly the declared fields, with
// the right types. Initializers are checked in source order, matching the evaluation
// order spec/04-expressions.md specifies.
func (c *Checker) checkFieldInits(lit *ast.StructLit, decls []*ast.Field, fieldTypes []types.Type, subst map[*types.Param]types.Type, name string) {
	seen := map[string]diag.Span{}
	for _, init := range lit.Fields {
		idx := -1
		for i, d := range decls {
			if d.Name.Name == init.Name.Name {
				idx = i
				break
			}
		}
		if idx < 0 {
			c.infer(init.Value)
			c.bag.Errorf("E0560", init.Name.Loc, "`%s` has no field `%s`", name, init.Name.Name).
				Label("unknown field").
				Note("`%s` has %s", name, fieldList(decls))
			continue
		}
		if prev, dup := seen[init.Name.Name]; dup {
			c.bag.Errorf("E0062", init.Name.Loc, "field `%s` is initialized twice", init.Name.Name).
				Label("duplicate initializer").
				Secondary(prev, "first given here")
		}
		seen[init.Name.Name] = init.Name.Loc

		got := c.infer(init.Value)
		if idx < len(fieldTypes) {
			want := types.Substitute(fieldTypes[idx], subst)
			c.unify(want, got, init.Value.Span(), "in this field's value")
		}
	}
	var missing []string
	for _, d := range decls {
		if _, ok := seen[d.Name.Name]; !ok {
			missing = append(missing, "`"+d.Name.Name+"`")
		}
	}
	if len(missing) > 0 {
		c.bag.Errorf("E0063", lit.Span(), "missing field%s in `%s`", plural(len(missing)), name).
			Label("%s not given", joinNames(missing)).
			Note("Origin has no default values, so every field must be supplied")
	}
}

func fieldList(decls []*ast.Field) string {
	var names []string
	for _, d := range decls {
		names = append(names, "`"+d.Name.Name+"`")
	}
	if len(names) == 0 {
		return "no fields"
	}
	return joinNames(names)
}

func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	case 2:
		return names[0] + " and " + names[1]
	}
	out := ""
	for i, n := range names[:len(names)-1] {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out + " and " + names[len(names)-1]
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func (c *Checker) inferLambda(v *ast.Lambda) types.Type {
	return c.inferLambdaExpecting(v, nil)
}

// inferLambdaExpecting checks a lambda against the function type its context wants, when
// there is one. An unannotated parameter takes its type from `want` rather than from a
// fresh variable, which is what makes `m.with(|v| v.get())` work: a method call inside the
// body is resolved against the receiver's exact type (there is no autoref), so that type
// has to be known while the body is being checked, not merely by the end of the call.
func (c *Checker) inferLambdaExpecting(v *ast.Lambda, want *types.FnT) types.Type {
	if want != nil && len(want.Params) != len(v.Params) {
		want = nil // the arity error is reported by the caller's unification
	}
	params := make([]types.Type, 0, len(v.Params))
	for i, p := range v.Params {
		var t types.Type
		if p.Type != nil {
			t = c.toType(p.Type)
		} else if want != nil {
			t = want.Params[i]
		} else {
			t = c.freshFor(p.Span())
		}
		params = append(params, t)
		// A lambda parameter binds every value it is given, so its pattern must be
		// irrefutable, exactly like a function parameter.
		c.bindPattern(p.Pat, t, true)
	}

	savedRet := c.ret
	var declared types.Type
	if v.Ret != nil {
		declared = c.toType(v.Ret)
		c.ret = declared
	} else {
		c.ret = c.freshFor(v.Span())
	}
	got := c.infer(v.Body)
	if declared != nil && !types.IsNever(got) {
		c.unify(declared, got, v.Body.Span(), "in this lambda's body")
		got = declared
	} else if declared == nil {
		// `return` inside a lambda targets the lambda's return type, so reconcile.
		c.unify(c.ret, got, v.Body.Span(), "in this lambda's body")
		got = c.ret
	}
	c.ret = savedRet
	return &types.FnT{Params: params, Ret: got}
}

func (c *Checker) inferIf(v *ast.If) types.Type {
	c.expectBool(v.Cond, "an `if` condition")
	thenTy := c.checkBlock(v.Then)
	if v.Else == nil {
		// spec/04-expressions.md: an `if` without `else` has type `()`.
		if !types.IsNever(thenTy) {
			c.unify(types.Unit(), thenTy, blockTailSpan(v.Then), "in an `if` with no `else`")
		}
		return types.Unit()
	}
	elseTy := c.infer(v.Else)
	switch {
	case types.IsNever(thenTy):
		return elseTy
	case types.IsNever(elseTy):
		return thenTy
	}
	c.unify(thenTy, elseTy, elseSpan(v), "this branch")
	return thenTy
}

func blockTailSpan(b *ast.Block) diag.Span {
	if b != nil && b.Tail != nil {
		return b.Tail.Span()
	}
	if b != nil {
		return b.Span()
	}
	return diag.Span{}
}

func elseSpan(v *ast.If) diag.Span {
	if b, ok := v.Else.(*ast.Block); ok {
		return blockTailSpan(b)
	}
	return v.Else.Span()
}

func (c *Checker) expectBool(e ast.Expr, what string) {
	got := c.infer(e)
	if types.IsNever(got) {
		return
	}
	c.unify(types.P(types.Bool), got, e.Span(), what+" must be a `bool`")
}

func (c *Checker) expectUnitBlock(b *ast.Block, what string) {
	got := c.checkBlock(b)
	if types.IsNever(got) {
		return
	}
	c.unify(types.Unit(), got, blockTailSpan(b), what+" produces no value")
}

func (c *Checker) inferMatch(v *ast.Match) types.Type {
	scrut := c.infer(v.Scrutinee)
	if len(v.Arms) == 0 {
		c.bag.Errorf("E0004", v.Span(), "`match` with no arms").
			Label("a match must handle every case").
			Note("`%s` has values this match does not cover", scrut)
		return types.Error
	}

	var result types.Type
	for _, arm := range v.Arms {
		c.bindPattern(arm.Pat, scrut, false)
		if arm.Guard != nil {
			c.expectBool(arm.Guard, "a match guard")
		}
		got := c.infer(arm.Body)
		switch {
		case types.IsNever(got):
			continue
		case result == nil:
			result = got
		default:
			c.unify(result, got, arm.Body.Span(), "this arm")
		}
	}
	c.checkExhaustive(v, scrut)
	if result == nil {
		return types.P(types.Never)
	}
	return result
}

func (c *Checker) inferFor(v *ast.For) types.Type {
	iter := c.infer(v.Iter)
	elem := c.forElementType(v, iter)
	c.bindPattern(v.Pat, elem, true)
	c.expectUnitBlock(v.Body, "a `for` body")
	return types.Unit()
}

// forElementType applies the desugaring in spec/04-expressions.md: `e.into_iter()`, then
// `next()` yielding `Option[Item]`.
//
// The desugaring never becomes AST nodes of its own — there is no `ast.MethodCall` for
// `into_iter()` or `next()` to hang an instantiation off — so both implicit calls are
// recorded against the `for` node itself, in two tables: `next` in Insts, where every
// other call site lives, and `into_iter` in IterInsts. Two tables rather than two nodes,
// because the only other node the desugaring could name is the iterable expression, and
// that already has an entry of its own whenever it is itself a call (`for k in m.keys()`).
func (c *Checker) forElementType(v *ast.For, iter types.Type) types.Type {
	if types.IsError(iter) {
		return types.Error
	}
	intoIter := c.traitByName("IntoIterator")
	if intoIter == nil {
		return types.Error
	}
	c.requireBound(iter, intoIter, v.Iter.Span())
	elem := c.normalize(&types.AssocT{Trait: intoIter.Decl, Member: "Item", Self: iter})

	iterType, ok := c.recordForCall(c.out.IterInsts, v.NodeID(), iter, "into_iter", v.Iter.Span())
	if !ok {
		// `iter` does not actually implement `IntoIterator`: requireBound above already
		// reports it once checking the body finishes.
		return elem
	}
	if iteratorTrait := c.traitByName("Iterator"); iteratorTrait != nil {
		c.requireBound(iterType, iteratorTrait, v.Span())
	}
	c.recordForCall(c.out.Insts, v.NodeID(), iterType, "next", v.Span())
	return elem
}

// recordForCall resolves and records the instantiation of a trait method called with no
// explicit arguments beyond `self`, the shape both halves of the `for` desugaring have.
// It mirrors the instantiation-recording half of inferMethodCall, without the argument
// unification an explicit call site needs. ok is false when no candidate exists for
// name on recv, which the caller treats as an error already reported elsewhere.
func (c *Checker) recordForCall(into map[ast.NodeID]*Inst, nodeID ast.NodeID, recv types.Type, name string, span diag.Span) (ret types.Type, ok bool) {
	cand, _ := c.lookupMethod(recv, name)
	if cand == nil {
		return types.Error, false
	}
	subst := map[*types.Param]types.Type{}
	for k, val := range cand.Subst {
		subst[k] = val
	}
	for _, p := range cand.Sig.Params {
		subst[p] = c.freshFor(span)
	}
	if cand.Trait != nil {
		subst[cand.Trait.SelfParam] = recv
	}
	c.recordMethodInst(into, nodeID, name, cand, recv, subst)
	for _, b := range cand.Sig.Bounds {
		c.requireBound(types.Substitute(b.Type, subst), b.Trait, span)
	}
	return c.normalize(types.Substitute(cand.Sig.Ret, subst)), true
}

func (c *Checker) inferLoop(v *ast.Loop) types.Type {
	c.loopValues = append(c.loopValues, nil)
	c.checkBlock(v.Body)
	result := c.loopValues[len(c.loopValues)-1]
	c.loopValues = c.loopValues[:len(c.loopValues)-1]
	if result == nil {
		// A `loop` with no `break` never produces a value.
		return types.Unit()
	}
	return result
}

func (c *Checker) inferBreak(v *ast.Break) types.Type {
	var got types.Type = types.Unit()
	if v.Value != nil {
		got = c.infer(v.Value)
	}
	if len(c.loopValues) == 0 {
		// `break` in a `while` or `for`, or outside a loop entirely; the resolver
		// already rejected the latter.
		if v.Value != nil {
			c.bag.Errorf("E0571", v.Span(), "`break` with a value is only allowed in `loop`").
				Label("`while` and `for` produce no value").
				Help("use `loop { ... break value; }`")
		}
		return types.P(types.Never)
	}
	top := len(c.loopValues) - 1
	if c.loopValues[top] == nil {
		c.loopValues[top] = got
	} else {
		c.unify(c.loopValues[top], got, v.Span(), "this `break`")
	}
	return types.P(types.Never)
}

func (c *Checker) inferUnary(v *ast.Unary) types.Type {
	got := c.infer(v.X)
	switch v.Op {
	case ast.Not:
		c.unify(types.P(types.Bool), got, v.X.Span(), "`!` applies to `bool`")
		return types.P(types.Bool)
	default:
		if p, ok := types.AsPrim(got); ok {
			if p.Kind.IsSigned() || p.Kind.IsFloat() {
				return got
			}
			if p.Kind.IsInteger() {
				c.bag.Errorf("E0600", v.Span(), "cannot negate `%s`", p.Kind).
					Label("unsigned types have no negative values").
					Help("use a signed type, or `0 - x` if wrapping is intended")
				return got
			}
		}
		if types.IsError(got) || types.IsNever(got) {
			return got
		}
		if _, unsolved := types.Prune(got).(*types.Var); unsolved {
			return got // defaulting will settle it
		}
		c.bag.Errorf("E0600", v.Span(), "cannot negate `%s`", got).
			Label("`-` applies to signed integers and floats").
			Note("Origin 0.1 has no operator overloading, so `-` is defined on primitives only")
		return types.Error
	}
}

// inferBinary types an infix operator. Operators are built in on primitives only:
// Origin 0.1 has no operator overloading (docs/deferred.md, Phase 7).
func (c *Checker) inferBinary(v *ast.Binary) types.Type {
	left := c.infer(v.L)
	right := c.infer(v.R)

	switch v.Op {
	case ast.AndAnd, ast.OrOr:
		c.unify(types.P(types.Bool), left, v.L.Span(), "`"+v.Op.String()+"` applies to `bool`")
		c.unify(types.P(types.Bool), right, v.R.Span(), "`"+v.Op.String()+"` applies to `bool`")
		return types.P(types.Bool)

	case ast.Eq, ast.Ne:
		// `==` is structural and total for every non-function type
		// (spec/04-expressions.md), so the operands need only agree.
		c.unify(left, right, v.R.Span(), "both operands of `"+v.Op.String()+"` must have the same type")
		c.rejectFunctionEquality(v, left)
		return types.P(types.Bool)
	}

	if !c.unify(left, right, v.R.Span(), "both operands of `"+v.Op.String()+"` must have the same type") {
		return c.binaryResult(v.Op, types.Error)
	}
	c.checkOperand(v, left)
	return c.binaryResult(v.Op, left)
}

func (c *Checker) rejectFunctionEquality(v *ast.Binary, t types.Type) {
	if _, isFn := types.Prune(t).(*types.FnT); isFn {
		c.bag.Errorf("E0369", v.Span(), "function values cannot be compared").
			Label("`%s` is not defined on functions", v.Op).
			Note("two functions have no meaningful equality in Origin")
	}
}

// operandCheck is an operator application waiting for its operand type to settle.
type operandCheck struct {
	expr *ast.Binary
	ty   types.Type
}

// checkOperand records an operator application for verification after defaulting.
//
// It cannot run immediately: `1.0 & 2.0` has an unsolved float variable at this point,
// and `let x: u8 = 1 + 2;` would be broken by defaulting the literals early.
func (c *Checker) checkOperand(v *ast.Binary, t types.Type) {
	c.opChecks = append(c.opChecks, operandCheck{expr: v, ty: t})
}

func (c *Checker) runOperandChecks() {
	for _, oc := range c.opChecks {
		c.verifyOperand(oc.expr, oc.ty)
	}
}

func (c *Checker) verifyOperand(v *ast.Binary, t types.Type) {
	if types.IsError(t) || types.IsNever(t) {
		return
	}
	types.ApplyDefaults(t)
	if _, unsolved := types.Prune(t).(*types.Var); unsolved {
		return // reported as an uninferred type instead
	}
	p, isPrim := types.AsPrim(t)

	switch v.Op {
	case ast.Add, ast.Sub, ast.Mul, ast.Div, ast.Rem:
		if isPrim && p.Kind.IsNumeric() {
			return
		}
		c.rejectOperator(v, t, "arithmetic operators apply to integers and floats")

	case ast.BitAnd, ast.BitOr, ast.BitXor:
		if isPrim && p.Kind.IsInteger() {
			return
		}
		c.rejectOperator(v, t, "bitwise operators apply to integers")

	case ast.Shl, ast.Shr:
		if isPrim && p.Kind.IsInteger() {
			return
		}
		c.rejectOperator(v, t, "shift operators apply to integers")

	case ast.Lt, ast.Le, ast.Gt, ast.Ge:
		if isPrim && p.Kind.IsOrdered() {
			return
		}
		d := c.bag.Errorf("E0369", v.Span(), "`%s` is not defined on `%s`", v.Op, t).
			Label("cannot compare these").
			Note("`<` and friends are built in for integers, floats, `char` and `String`")
		d.Help("implement `Ord` for `%s` and write `a.cmp(b)`", t)
	}
}

func (c *Checker) rejectOperator(v *ast.Binary, t types.Type, note string) {
	d := c.bag.Errorf("E0369", v.Span(), "`%s` is not defined on `%s`", v.Op, t).
		Label("this operator does not apply here").
		Note("%s", note)
	if _, isNamed := types.AsNamed(t); isNamed {
		d.Help("Origin 0.1 has no operator overloading; write a method instead")
	}
}

func (c *Checker) binaryResult(op ast.BinaryOp, operand types.Type) types.Type {
	if op.IsComparison() {
		return types.P(types.Bool)
	}
	return operand
}

// inferAssign types an assignment. Its value is always `()`, which is why
// `a = b = c` is rejected unless `a: ()` (spec/04-expressions.md).
func (c *Checker) inferAssign(v *ast.Assign) types.Type {
	place := c.infer(v.Place)
	c.checkPlace(v.Place)
	value := c.infer(v.Value)

	if op, compound := v.Op.BinaryOp(); compound {
		c.unify(place, value, v.Value.Span(), "both sides of `"+v.Op.String()+"` must have the same type")
		c.checkOperand(&ast.Binary{Base: ast.Base{ID: v.NodeID(), Loc: v.Loc}, Op: op}, place)
	} else {
		c.unify(place, value, v.Value.Span(), "in this assignment")
	}
	return types.Unit()
}

// checkPlace enforces the field half of the place rule (spec/04-expressions.md). The
// binding half is checked by the resolver, which knows about `mut` without needing
// types; this half needs the receiver's type, so it lands here.
func (c *Checker) checkPlace(e ast.Expr) {
	fa, ok := e.(*ast.FieldAccess)
	if !ok {
		return // the resolver handles paths and rejects everything else
	}
	recv := c.out.ExprTypes[fa.Recv.NodeID()]
	if recv == nil {
		return
	}
	decl, owner := c.fieldDecl(recv, fa.Name.Name)
	if decl == nil {
		return // the field lookup already reported
	}
	if decl.Mut {
		return
	}
	c.bag.Errorf("E0594", fa.Span(), "field `%s` of `%s` is not declared `mut`", fa.Name.Name, owner).
		Label("cannot assign to an immutable field").
		Secondary(decl.Name.Loc, "declare it as `mut %s`", decl.Name.Name).
		Note("a field is fixed at construction unless it is declared `mut` (spec/08-memory-model.md)")
}

// fieldDecl finds a named field on a struct or single-variant enum type.
func (c *Checker) fieldDecl(t types.Type, name string) (*ast.Field, string) {
	n, ok := types.AsNamed(t)
	if !ok {
		return nil, ""
	}
	if n.Def.Struct != nil {
		for _, f := range n.Def.Struct.Fields {
			if f.Name.Name == name {
				return f, n.Def.Name
			}
		}
	}
	return nil, n.Def.Name
}

// inferCast implements the `as` matrix of spec/04-expressions.md. Every conversion in
// Origin is written, so this is the only place a type changes without unification.
func (c *Checker) inferCast(v *ast.Cast) types.Type {
	from := c.infer(v.X)
	to := c.toType(v.Type)
	if types.IsError(from) || types.IsError(to) {
		return to
	}
	// An unsolved source defaults now: `1 as u8` must decide what `1` is first.
	types.ApplyDefaults(from)

	fp, fok := types.AsPrim(from)
	tp, tok := types.AsPrim(to)
	if !fok || !tok {
		c.rejectCast(v, from, to)
		return to
	}

	switch {
	case fp.Kind.IsNumeric() && tp.Kind.IsNumeric():
		return to
	case fp.Kind == types.Bool && tp.Kind.IsInteger():
		return to
	case fp.Kind == types.Char && tp.Kind.IsInteger():
		return to
	case fp.Kind.IsInteger() && tp.Kind == types.Char:
		c.bag.Errorf("E0605", v.Span(), "cannot cast `%s` to `char`", fp.Kind).
			Label("not every integer is a Unicode scalar value").
			Help("use `char::from_u32(x)`, which returns `Option[char]`")
		return to
	}
	c.rejectCast(v, from, to)
	return to
}

func (c *Checker) rejectCast(v *ast.Cast, from, to types.Type) {
	d := c.bag.Errorf("E0605", v.Span(), "cannot cast `%s` to `%s`", from, to).
		Label("this conversion is not defined").
		Note("`as` converts between numeric types, and from `bool` or `char` to an integer")
	if _, isNamed := types.AsNamed(from); isNamed {
		d.Help("write a method that builds the value you want")
	}
}

func (c *Checker) inferCall(v *ast.Call) types.Type {
	callee := c.infer(v.Fn)
	if types.IsError(callee) {
		for _, a := range v.Args {
			c.infer(a) // still check them, so one mistake is one diagnostic
		}
		return types.Error
	}

	// Settle a literal's type first, so that calling a number reports "not callable"
	// rather than a mismatch against an invented function type.
	types.ApplyDefaults(callee)

	fn, ok := types.Prune(callee).(*types.FnT)
	if ok && len(v.Args) == len(fn.Params) {
		args := c.inferArgs(v.Args, fn.Params, ordinalArg)
		_ = args
		// The concurrency builtins carry obligations their signatures cannot: the `Send`
		// bounds ADR-0014 rests on, and a spawned closure's captures
		// (spec/12-concurrency.md).
		c.checkConcurrencyCall(v, fn, fn.Ret)
		return fn.Ret
	}

	args := make([]types.Type, 0, len(v.Args))
	for _, a := range v.Args {
		args = append(args, c.infer(a))
	}
	if !ok {
		if _, unsolved := types.Prune(callee).(*types.Var); unsolved {
			// The callee's type is still open: constrain it to a function rather than
			// guessing. The result variable is only registered once that succeeds, so a
			// failure produces one diagnostic and not a second about an unsolved type.
			ret := c.ctx.Fresh()
			if !c.unify(&types.FnT{Params: args, Ret: ret}, callee, v.Fn.Span(), "this is called as a function") {
				return types.Error
			}
			c.bodyVars = append(c.bodyVars, pendingVar{ty: ret, span: v.Span()})
			return ret
		}
		c.bag.Errorf("E0618", v.Fn.Span(), "`%s` is not callable", callee).
			Label("expected a function").
			Note("only a function value can be called")
		return types.Error
	}

	c.bag.Errorf("E0061", v.Span(),
		"this function takes %d argument%s but %d %s supplied",
		len(fn.Params), plural(len(fn.Params)), len(args), wereOrWas(len(args))).
		Label("wrong number of arguments").
		Note("its type is `%s`", fn)
	return fn.Ret
}

// inferArgs checks a call's arguments against the parameter types the callee declares,
// and unifies each one.
//
// Lambdas go last, and that is the whole point: unifying the ordinary arguments first can
// solve the type variables a generic signature leaves in the *lambda's* parameter types,
// so that by the time the lambda's body is checked its parameters have real types. Before
// this, `sort_by(nums, |a, b| a < b)` worked only because `<` on two unknowns could wait,
// and `m.with(|v| v.get())` did not work at all -- method lookup needs the receiver's
// exact type at the moment it runs.
func (c *Checker) inferArgs(argExprs []ast.Expr, params []types.Type, kind func(int) string) []types.Type {
	args := make([]types.Type, len(argExprs))
	var lambdas []int
	for i, a := range argExprs {
		if _, isLambda := a.(*ast.Lambda); isLambda {
			lambdas = append(lambdas, i)
			continue
		}
		args[i] = c.infer(a)
		c.unify(params[i], args[i], a.Span(), kind(i))
	}
	for _, i := range lambdas {
		lam := argExprs[i].(*ast.Lambda)
		want, _ := types.Prune(params[i]).(*types.FnT)
		args[i] = c.inferLambdaExpecting(lam, want)
		c.unify(params[i], args[i], lam.Span(), kind(i))
	}
	return args
}

func wereOrWas(n int) string {
	if n == 1 {
		return "was"
	}
	return "were"
}

func ordinalArg(i int) string {
	switch i {
	case 0:
		return "the first argument"
	case 1:
		return "the second argument"
	case 2:
		return "the third argument"
	}
	return "argument " + u64s(uint64(i+1))
}

func (c *Checker) inferMethodCall(v *ast.MethodCall) types.Type {
	recv := c.normalize(c.infer(v.Recv))
	if types.IsError(recv) {
		for _, a := range v.Args {
			c.infer(a) // still check them, so one mistake is one diagnostic
		}
		return types.Error
	}
	// An unsolved receiver has no methods to look up yet; defaulting it here is what
	// makes `1.to_str()` work without an annotation.
	types.ApplyDefaults(recv)

	// A receiver whose type is still unknown has no methods to search. Saying "no
	// method `to_str` on `_`" would be both unhelpful and, with the note that follows
	// it, self-contradictory.
	if _, unsolved := types.Prune(recv).(*types.Var); unsolved {
		c.bag.Errorf("E0309", v.Recv.Span(), "cannot infer the type of this value").
			Label("its type must be known to look up `%s`", v.Name.Name).
			Note("Origin has no autoref or autoderef, so a method is found on one exact type").
			Help("annotate the binding or the parameter that produces it")
		return types.Error
	}

	cand, ambiguous := c.lookupMethod(recv, v.Name.Name)
	if cand == nil {
		c.reportNoMethod(v, recv, ambiguous)
		for _, a := range v.Args {
			c.infer(a)
		}
		return types.Error
	}
	c.out.Methods[v.NodeID()] = cand.Decl

	subst := map[*types.Param]types.Type{}
	for k, val := range cand.Subst {
		subst[k] = val
	}
	for _, p := range cand.Sig.Params {
		subst[p] = c.freshFor(v.Span())
	}
	// `Self` is one of the generic parameters of a trait's own declaration (decl.go's
	// `signature` puts it there), so it has to be in the substitution before the
	// instantiation is recorded -- otherwise a default method body, whose declaration
	// *is* the trait's, is recorded with `Self` unknown and reaches no instance at all.
	if cand.Trait != nil {
		subst[cand.Trait.SelfParam] = recv
	}
	c.recordMethodInst(c.out.Insts, v.NodeID(), v.Name.Name, cand, recv, subst)
	// The impl's own bounds are obligations of the call as much as the method's are:
	// `impl[T: Show] Show for Box[T]` gives `Box[Opaque]` a `to_str` only if `Opaque` has
	// one. Requiring only the method's bounds let the call through, and what happened next
	// depended on the engine -- the interpreter and the VM fell back on a debug rendering,
	// native code failed to build -- which is the shape of divergence the whole project is
	// arranged to prevent.
	if cand.Impl != nil {
		for _, b := range cand.Impl.Bounds {
			c.requireBound(types.Substitute(b.Type, subst), b.Trait, v.Span())
		}
	}
	for _, b := range cand.Sig.Bounds {
		c.requireBound(types.Substitute(b.Type, subst), b.Trait, v.Span())
	}

	if cand.Sig.Decl.Self == nil {
		c.bag.Errorf("E0599", v.Span(), "`%s` is an associated function, not a method", v.Name.Name).
			Label("it does not take `self`").
			Help("call it as `%s::%s(...)`", recv, v.Name.Name)
		for _, a := range v.Args {
			c.infer(a)
		}
		return types.Error
	}

	params := make([]types.Type, 0, len(cand.Sig.ParamTypes))
	for _, p := range cand.Sig.ParamTypes {
		params = append(params, c.normalize(types.Substitute(p, subst)))
	}
	if len(v.Args) != len(params) {
		for _, a := range v.Args {
			c.infer(a)
		}
		c.bag.Errorf("E0061", v.Span(),
			"`%s` takes %d argument%s but %d %s supplied",
			v.Name.Name, len(params), plural(len(params)), len(v.Args), wereOrWas(len(v.Args))).
			Label("wrong number of arguments")
		return c.normalize(types.Substitute(cand.Sig.Ret, subst))
	}
	// Arguments are checked against the parameter types the method declares, lambdas last,
	// so that `m.with(|v| v.get())` knows what `v` is while its body is being checked.
	c.inferArgs(v.Args, params, ordinalArg)
	return c.normalize(types.Substitute(cand.Sig.Ret, subst))
}

// reportNoMethod produces the diagnostic for a failed method lookup, including the note
// that names a trait which would supply it (spec/06-traits-generics.md).
func (c *Checker) reportNoMethod(v *ast.MethodCall, recv types.Type, ambiguous []string) {
	if len(ambiguous) > 0 {
		d := c.bag.Errorf("E0034", v.Span(), "`%s` is ambiguous on `%s`", v.Name.Name, recv).
			Label("more than one candidate applies")
		for _, name := range ambiguous {
			d.Help("call it as `%s::%s(receiver, ...)`", name, v.Name.Name)
		}
		return
	}
	d := c.bag.Errorf("E0599", v.Span(), "no method `%s` on `%s`", v.Name.Name, recv).
		Label("method not found")
	for _, ti := range c.traits {
		if _, has := ti.Methods[v.Name.Name]; !has {
			continue
		}
		if c.satisfies(recv, ti) {
			d.Note("trait `%s` declares `%s`, and `%s` implements it",
				ti.Decl.Name.Name, v.Name.Name, recv)
		} else {
			d.Note("trait `%s` declares `%s`, but `%s` does not implement it",
				ti.Decl.Name.Name, v.Name.Name, recv)
		}
		return
	}
	if _, isPrim := types.AsPrim(recv); isPrim {
		d.Note("Origin has no autoref or autoderef: the receiver's type must match exactly")
		return
	}
	if p, isParam := types.Prune(recv).(*types.Param); isParam {
		d.Note("`%s` is a type parameter, so it has only the methods its bounds give it", p.Name)
		d.Help("add a bound that declares `%s`", v.Name.Name)
		return
	}
	d.Note("a method comes from an inherent `impl` on the type, or from a trait it implements")
}

func (c *Checker) inferField(v *ast.FieldAccess) types.Type {
	recv := c.normalize(c.infer(v.Recv))
	if types.IsError(recv) {
		return types.Error
	}
	// Settle a literal's type before looking for the field, so that `1.field` reports
	// "`i64` has no fields" rather than "cannot infer this type".
	types.ApplyDefaults(recv)
	n, ok := types.AsNamed(recv)
	if !ok {
		if _, unsolved := types.Prune(recv).(*types.Var); unsolved {
			c.bag.Errorf("E0309", v.Span(), "cannot infer the type of this value").
				Label("its type must be known before a field can be read").
				Help("add an annotation on the binding")
			return types.Error
		}
		c.bag.Errorf("E0609", v.Span(), "`%s` has no fields", recv).
			Label("not a struct").
			Note("only a struct has named fields; a tuple is read by destructuring it in a pattern")
		return types.Error
	}
	if n.Def.Struct == nil {
		c.bag.Errorf("E0609", v.Span(), "`%s` is an enum, so it has no fields to read directly", n.Def.Name).
			Label("no field `%s`", v.Name.Name).
			Help("match on it to reach a variant's payload")
		return types.Error
	}
	for i, f := range n.Def.Struct.Fields {
		if f.Name.Name != v.Name.Name {
			continue
		}
		subst := map[*types.Param]types.Type{}
		for j, p := range n.Def.Params {
			if j < len(n.Args) {
				subst[p] = n.Args[j]
			}
		}
		return types.Substitute(n.Def.FieldTypes[i], subst)
	}
	c.bag.Errorf("E0609", v.Span(), "`%s` has no field `%s`", n.Def.Name, v.Name.Name).
		Label("unknown field").
		Note("`%s` has %s", n.Def.Name, fieldList(n.Def.Struct.Fields))
	return types.Error
}

// inferTry types the `?` operator (spec/09-errors.md).
//
// It applies to `Result[T, E]` and to `Option[T]`, and in both cases the *enclosing*
// function must return the same enum: `?` returns early with a value of the function's own
// return type, and there is nothing it could build out of an `Option` to satisfy a
// `Result` or the other way round.
//
// For a `Result` the error types must match exactly. Automatic conversion via `Into` is
// still deferred (docs/deferred.md): it needs a blanket-impl story that interacts with
// coherence (ADR-0011).
func (c *Checker) inferTry(v *ast.Try) types.Type {
	got := c.normalize(c.infer(v.X))
	if types.IsError(got) {
		return types.Error
	}
	inner, ok := types.AsNamed(got)
	if !ok || (inner.Def.Name != "Result" && inner.Def.Name != "Option") {
		c.bag.Errorf("E0277", v.Span(), "`?` applies to a `Result` or an `Option`, found `%s`", got).
			Label("neither a `Result` nor an `Option`").
			Note("`?` returns early on `Err` or `None`, and unwraps `Ok` or `Some`")
		return types.Error
	}
	if inner.Def.Name == "Option" {
		return c.inferTryOption(v, inner)
	}
	if len(inner.Args) != 2 {
		return types.Error
	}

	outer, ok := types.AsNamed(c.ret)
	if !ok || outer.Def.Name != "Result" || len(outer.Args) != 2 {
		c.bag.Errorf("E0277", v.Span(), "`?` on a `Result` is only allowed in a function returning `Result`").
			Label("this function returns `%s`", c.ret).
			Help("change the return type to `Result[%s, %s]`", tryOkType(c.ret), inner.Args[1])
		return inner.Args[0]
	}
	if !c.unify(outer.Args[1], inner.Args[1], v.Span(), "the error types must match") {
		c.bag.Errorf("E0277", v.Span(), "`?` cannot convert between error types").
			Label("this fails with `%s`, but the function returns `%s`", inner.Args[1], outer.Args[1]).
			Note("automatic error conversion needs an `Into` bound, which Origin 0.1 does not have").
			Help("convert explicitly: `.map_err(|e| ...)?`")
	}
	return inner.Args[0]
}

// inferTryOption is the `Option[T]` half: the enclosing function must return an `Option`,
// and there is no second type argument for the two to disagree about.
func (c *Checker) inferTryOption(v *ast.Try, inner *types.Named) types.Type {
	if len(inner.Args) != 1 {
		return types.Error
	}
	outer, ok := types.AsNamed(c.ret)
	if !ok || outer.Def.Name != "Option" || len(outer.Args) != 1 {
		c.bag.Errorf("E0277", v.Span(), "`?` on an `Option` is only allowed in a function returning `Option`").
			Label("this function returns `%s`", c.ret).
			Note("`?` returns early with `None`, which is not a value of `%s`", c.ret).
			Help("change the return type to `Option[%s]`, or `match` on it instead", tryOkType(c.ret))
	}
	return inner.Args[0]
}

// tryOkType renders the current return type's payload for the `?` help text.
func tryOkType(t types.Type) string {
	if n, ok := types.AsNamed(t); ok && len(n.Args) > 0 &&
		(n.Def.Name == "Result" || n.Def.Name == "Option") {
		return n.Args[0].String()
	}
	return t.String()
}

var _ = math.MaxInt64

// recordMethodInst notes which copy of a method a call site needs.
//
// The receiver is recorded along with the parameters, because it is what
// monomorphization resolves against: a trait method called on a type parameter names
// the trait's own declaration here, and only the substituted receiver says which impl
// actually runs.
//
// nodeID and name are passed separately from an *ast.MethodCall so that a call the
// desugaring in spec/04-expressions.md implies, but that never becomes an AST node of
// its own — `for`'s `into_iter()`/`next()` — can be recorded too, keyed to whichever
// existing node stands in for it (see forElementType).
func (c *Checker) recordMethodInst(into map[ast.NodeID]*Inst, nodeID ast.NodeID, name string, cand *methodCandidate, recv types.Type, subst map[*types.Param]types.Type) {
	inst := &Inst{Decl: cand.Decl, Recv: recv, Method: name}
	for _, p := range c.out.Generics[cand.Decl] {
		arg, known := subst[p]
		if !known {
			return
		}
		inst.Params = append(inst.Params, p)
		inst.Args = append(inst.Args, arg)
	}
	into[nodeID] = inst
}
