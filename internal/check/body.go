package check

import (
	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/resolve"
	"github.com/scarypheonix/meta/internal/types"
)

// checkBodies type-checks every function body in a file. Signatures are already known
// (ADR-0009), so each body is checked in isolation.
func (c *Checker) checkBodies(f *ast.File) {
	for _, it := range f.Items {
		switch v := it.(type) {
		case *ast.FnDecl:
			c.checkFn(v, nil, nil, nil, nil)

		case *ast.ConstDecl:
			c.checkConst(v)

		case *ast.ImplDecl:
			info := c.implFor(v)
			if info == nil {
				continue
			}
			for _, m := range info.Methods {
				c.checkFn(m, info.Params, info.Self, nil, info.Bounds)
			}

		case *ast.TraitDecl:
			ti := c.traits[v]
			if ti == nil {
				continue
			}
			for name, m := range ti.Methods {
				if m.Body == nil {
					continue // a required method declares a signature only
				}
				// A default body is checked once against the trait's own bounds, not
				// per impl (spec/06-traits-generics.md).
				selfBounds := []Bound{{Type: ti.SelfParam, Trait: ti}}
				selfBounds = append(selfBounds, ti.Supertraits...)
				c.checkFn(m, append(ti.Params, ti.SelfParam), ti.SelfParam, ti, selfBounds)
				_ = name
			}
		}
	}
}

func (c *Checker) implFor(decl *ast.ImplDecl) *ImplInfo {
	for _, info := range c.impls {
		if info.Decl == decl {
			return info
		}
	}
	return nil
}

func (c *Checker) checkConst(v *ast.ConstDecl) {
	c.env = typeEnv{}
	want := c.toType(v.Type)
	if v.Value == nil {
		return
	}
	c.beginBody(types.Unit())
	got := c.infer(v.Value)
	c.unify(want, got, v.Value.Span(), "in this constant's value")
	c.endBody()
}

// beginBody prepares the per-body inference state.
func (c *Checker) beginBody(ret types.Type) {
	c.ret = ret
	c.obligations = nil
	c.bodyVars = nil
	c.loopValues = nil
	c.intLits = nil
	c.opChecks = nil
}

// endBody finishes a body: solve bounds, apply literal defaults, then report anything
// still unsolved. The order matters — defaulting happens once, after all constraints
// are solved (spec/03-types.md).
func (c *Checker) endBody() {
	// Defaulting first: a bound on an integer literal's type cannot be judged while the
	// type is still an unsolved variable, and `needs_ord(1)` must know it means i64.
	for _, lit := range c.intLits {
		types.ApplyDefaults(lit.ty)
	}
	c.solveObligations()
	for _, v := range c.bodyVars {
		if pv, isVar := types.Prune(v.ty).(*types.Var); isVar && c.generalized[pv] {
			continue
		}
		if !types.ApplyDefaults(v.ty) {
			c.bag.Errorf("E0309", v.span, "cannot infer this type").
				Label("the type of this expression is not determined by anything").
				Help("add an annotation, such as `let x: i64 = ...`")
		}
	}
	c.checkLiteralRanges()
	c.runOperandChecks()
	c.bodyVars = nil
	c.intLits = nil
	c.opChecks = nil
}

// checkFn checks one function body against its declared signature.
func (c *Checker) checkFn(fn *ast.FnDecl, outerParams []*types.Param, self types.Type, trait *TraitInfo, outerBounds []Bound) {
	sig := c.fnSigs[fn]
	if sig == nil {
		if trait != nil {
			sig = trait.Sigs[fn.Name.Name]
		}
		if sig == nil {
			return
		}
	}
	if fn.Body == nil {
		return
	}

	all := append(append([]*types.Param{}, outerParams...), sig.Params...)
	c.env = envFor(all, self, trait)
	c.env.bounds = append(append([]Bound{}, outerBounds...), sig.Bounds...)

	c.beginBody(sig.Ret)

	if fn.Self != nil && self == nil {
		c.bag.Errorf("E0424", fn.Self.Span(), "`self` is not available here").
			Label("only a method may take `self`").
			Note("a method is declared inside an `impl` or a `trait`")
	}
	for i, p := range fn.Params {
		if i < len(sig.ParamTypes) {
			c.bindPattern(p.Pat, sig.ParamTypes[i], true)
		}
	}

	got := c.checkBlock(fn.Body)
	// A body that always diverges satisfies any return type.
	if !types.IsNever(got) {
		c.unify(sig.Ret, got, c.tailSpan(fn), "in this function's body")
	}
	c.endBody()
	c.env = typeEnv{}
}

// tailSpan points at the expression that produces a function's value, so a return-type
// mismatch is reported where the value is, not on the whole body.
func (c *Checker) tailSpan(fn *ast.FnDecl) diag.Span {
	if fn.Body != nil && fn.Body.Tail != nil {
		return fn.Body.Tail.Span()
	}
	if fn.Ret != nil {
		return fn.Ret.Span()
	}
	return fn.Name.Loc
}

// ---------------------------------------------------------------------------
// Unification with diagnostics
// ---------------------------------------------------------------------------

// unify makes want and got equal, reporting a type mismatch if they cannot be.
//
// This is the only place a type mismatch is phrased, which is how spec/09-errors.md
// rule 3 stays enforceable: types always print in source syntax, and the message always
// says which side was expected and why.
func (c *Checker) unify(want, got types.Type, span diag.Span, context string) bool {
	want, got = c.normalize(want), c.normalize(got)
	err := types.Unify(want, got)
	if err == nil {
		return true
	}
	if err.Infinite {
		c.bag.Errorf("E0310", span, "infinite type").
			Label("this expression's type would contain itself").
			Note("`%s` cannot be a part of itself", got).
			Help("a recursive value needs an enum, such as `enum List { Nil, Cons(i64, List) }`")
		return false
	}
	if err.Detail != "" {
		d := c.bag.Errorf("E0308", span, "%s", err.Detail).
			Label("%s", context)
		if err.Help != "" {
			d.Help("%s", err.Help)
		} else {
			c.explainMismatch(d, err.Want, err.Got)
		}
		return false
	}
	d := c.bag.Errorf("E0308", span, "expected `%s`, found `%s`", err.Want, err.Got).
		Label("%s", context)
	c.explainMismatch(d, err.Want, err.Got)
	return false
}

// explainMismatch adds the note that turns "expected X, found Y" into something
// actionable. Phase 2's exit criterion is that a type error explains the conflict in
// plain language, so every mismatch ends with a sentence a reader can act on — the
// common confusions get a specific one, and everything else gets the rule it broke.
func (c *Checker) explainMismatch(d *diag.Builder, want, got types.Type) {
	wp, wok := types.AsPrim(want)
	gp, gok := types.AsPrim(got)

	switch {
	case wok && gok && wp.Kind.IsInteger() && gp.Kind.IsInteger():
		d.Note("Origin has no implicit numeric conversion, not even widening")
		d.Help("write the conversion: `x as %s`", wp.Kind)
		return
	case wok && gok && wp.Kind.IsFloat() && gp.Kind.IsInteger():
		d.Note("an integer is not a float in Origin")
		d.Help("write the conversion: `x as %s`", wp.Kind)
		return
	case wok && gok && wp.Kind.IsInteger() && gp.Kind.IsFloat():
		d.Note("a float is not an integer in Origin")
		d.Help("write the conversion: `x as %s`, which truncates toward zero", wp.Kind)
		return
	case wok && wp.Kind == types.String && gok && gp.Kind.IsNumeric():
		d.Note("Origin does not render values as text implicitly")
		d.Help("call `.to_str()` on it")
		return
	case wok && wp.Kind == types.String:
		d.Note("`String` is a distinct type; nothing converts to it implicitly")
		d.Help("implement `Show` for it and call `.to_str()`")
		return
	case wok && wp.Kind == types.Bool:
		d.Note("a condition and a logical operator both need a `bool`")
		d.Help("compare instead, as in `x != 0`")
		return
	case wok && wp.Kind == types.UnitKind:
		d.Note("this expression produces a value, but nothing here uses it")
		d.Help("end the statement with `;` to discard the value")
		return
	case gok && gp.Kind == types.UnitKind:
		d.Note("this expression produces no value")
		d.Help("a block's value is its last expression, written without a `;`")
		return
	}

	if p, ok := types.Prune(want).(*types.Param); ok {
		d.Note("`%s` is a type parameter: the caller chooses it, so the body must work for every type", p.Name)
		d.Help("return a value built from the parameters, or add a bound that produces one")
		return
	}
	if p, ok := types.Prune(got).(*types.Param); ok {
		d.Note("`%s` is a type parameter and is not known to be `%s` here", p.Name, want)
		d.Help("add a bound on `%s`, or accept `%s` directly", p.Name, want)
		return
	}

	wn, wIsNamed := types.AsNamed(want)
	gn, gIsNamed := types.AsNamed(got)
	if wIsNamed && gIsNamed && wn.Def == gn.Def {
		d.Note("both are `%s`, but at different type arguments", wn.Def.Name)
		return
	}
	if wIsNamed && gIsNamed {
		d.Note("`%s` and `%s` are different declarations, so they are different types", wn.Def.Name, gn.Def.Name)
		return
	}
	if _, ok := types.Prune(want).(*types.FnT); ok {
		d.Note("function types are structural: the parameters and the result must all match")
		return
	}

	// The general rule, which is what every remaining case comes down to.
	d.Note("Origin has no implicit conversions: a value of one type is never silently a value of another")
}

// ---------------------------------------------------------------------------
// Blocks and statements
// ---------------------------------------------------------------------------

func (c *Checker) checkBlock(b *ast.Block) types.Type {
	if b == nil {
		return types.Unit()
	}
	for _, s := range b.Stmts {
		c.checkStmt(s)
	}
	if b.Tail == nil {
		return types.Unit()
	}
	t := c.infer(b.Tail)
	c.record(b.NodeID(), t)
	return t
}

func (c *Checker) checkStmt(s ast.Stmt) {
	switch v := s.(type) {
	case *ast.LetStmt:
		c.checkLet(v)

	case *ast.ExprStmt:
		t := c.infer(v.X)
		// spec/09-errors.md: an ignored Result is a warning, not an error.
		if v.Semi {
			c.warnUnusedResult(v.X, t)
		}

	case *ast.ItemStmt:
		// A nested item is checked where it is declared; Phase 2 checks items at file
		// level only, and a block-level item is rare enough to be left to Phase 8's
		// incremental machinery.
	}
}

func (c *Checker) warnUnusedResult(e ast.Expr, t types.Type) {
	n, ok := types.AsNamed(t)
	if !ok || n.Def.Name != "Result" {
		return
	}
	// Only a call produces a Result worth warning about; reading a variable does not.
	switch e.(type) {
	case *ast.Call, *ast.MethodCall:
	default:
		return
	}
	c.bag.Warnf("W0001", e.Span(), "unused `Result`").
		Label("this call's failure is discarded").
		Help("handle it with `match`, propagate it with `?`, or bind it to `_`")
}

func (c *Checker) checkLet(v *ast.LetStmt) {
	var want types.Type
	if v.Type != nil {
		want = c.toType(v.Type)
	}

	// The right-hand side is inferred one level deeper, so the variables it creates are
	// candidates for generalization (spec/03-types.md's value restriction).
	c.ctx.EnterLevel()
	var got types.Type
	if v.Value != nil {
		got = c.infer(v.Value)
	} else {
		got = types.Error
	}
	c.ctx.ExitLevel()

	if want != nil {
		c.unify(want, got, v.Value.Span(), "in this binding's value")
		got = want
	}

	scheme := types.Mono(got)
	if v.Value != nil && isSyntacticValue(v.Value) {
		// Only a syntactic value generalizes. `let v = Vec::new();` must stay
		// monomorphic, or pushing an i64 and reading back a String would type-check.
		scheme = c.ctx.Generalize(got)
		for _, v := range scheme.Vars {
			c.generalized[v] = true
		}
	}
	c.bindPatternScheme(v.Pat, scheme, true)
}

// isSyntacticValue implements the value restriction of spec/03-types.md: a literal, a
// path, a lambda, a tuple of syntactic values, or a constructor applied to them.
func isSyntacticValue(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.IntLit, *ast.FloatLit, *ast.StrLit, *ast.CharLit, *ast.BoolLit,
		*ast.PathExpr, *ast.Lambda, *ast.SelfExpr:
		return true
	case *ast.TupleExpr:
		for _, el := range v.Elems {
			if !isSyntacticValue(el) {
				return false
			}
		}
		return true
	case *ast.StructLit:
		for _, f := range v.Fields {
			if !isSyntacticValue(f.Value) {
				return false
			}
		}
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Bookkeeping
// ---------------------------------------------------------------------------

// pendingVar is an inference variable whose origin span is remembered, so that an
// unsolved one can be reported where it came from.
type pendingVar struct {
	ty   types.Type
	span diag.Span
}

// record stores an expression's type in the side table.
func (c *Checker) record(id ast.NodeID, t types.Type) types.Type {
	c.out.ExprTypes[id] = t
	return t
}

// freshFor allocates an inference variable and remembers where it came from.
func (c *Checker) freshFor(span diag.Span) types.Type {
	v := c.ctx.Fresh()
	c.bodyVars = append(c.bodyVars, pendingVar{ty: v, span: span})
	return v
}

// instantiateLocal instantiates a let-generalized local's scheme at one use site and
// registers every fresh variable the instantiation created for end-of-body defaulting.
//
// types.Ctx.Instantiate lives in internal/types and has no access to a Checker's
// pending-variable list, so a fresh variable it creates was previously invisible to
// endBody: nothing registered it, so a single use of a generalized binding with
// nothing else pinning its type down -- `let used = 1; io::println(used.to_str());`,
// where the method call itself never forces a concrete type -- left a Var unresolved
// with no defaulting and no error.
//
// This does not reach every case: if the fresh variable flows straight into *another*
// generalized `let` that is itself never used again (`let t = (used, used);` with `t`
// never read), that second `let` re-quantifies over it, endBody skips it as
// generalized, and it stays unresolved -- correctly, since nothing observable depends
// on what it would have been. internal/compile's field-layout code applies the same
// defaulting itself for exactly that residual case (ADR-0019): a var still free at a
// construction site is, by construction, one the checker proved unobservable.
func (c *Checker) instantiateLocal(s *types.Scheme, span diag.Span) types.Type {
	t := c.ctx.Instantiate(s)
	if len(s.Vars) == 0 {
		return t
	}
	for _, v := range types.FreeVars(t, nil) {
		c.bodyVars = append(c.bodyVars, pendingVar{ty: v, span: span})
	}
	return t
}

// bindPatternScheme binds a pattern to a possibly-polymorphic type. `let` patterns must
// be irrefutable (spec/05-patterns.md), which is what irrefutable records.
func (c *Checker) bindPatternScheme(p ast.Pattern, s *types.Scheme, irrefutable bool) {
	if b, ok := p.(*ast.BindPat); ok && len(s.Vars) > 0 {
		if local, isBinding := c.res.Bindings[b.NodeID()]; isBinding {
			c.schemes[local] = s
			c.out.LocalTypes[local] = s.Type
			c.out.PatTypes[b.NodeID()] = s.Type
			return
		}
	}
	c.bindPattern(p, s.Type, irrefutable)
}

// localScheme returns the scheme a binding was given, or a monomorphic one.
func (c *Checker) localScheme(l *resolve.Local) *types.Scheme {
	if s, ok := c.schemes[l]; ok {
		return s
	}
	if t, ok := c.out.LocalTypes[l]; ok {
		return types.Mono(t)
	}
	return types.Mono(types.Error)
}
