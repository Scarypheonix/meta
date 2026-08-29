package check

import (
	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/resolve"
	"github.com/scarypheonix/meta/internal/types"
)

// bindPattern checks a pattern against the type it matches and records the types of the
// bindings it introduces.
func (c *Checker) bindPattern(p ast.Pattern, want types.Type, irrefutable bool) {
	want = c.normalize(want)
	if p != nil {
		c.out.PatTypes[p.NodeID()] = want
	}

	switch v := p.(type) {
	case nil, *ast.ErrorPat:
		return

	case *ast.WildcardPat:
		return

	case *ast.LitPat:
		c.bindLitPattern(v, want)

	case *ast.BindPat:
		ref, _ := c.res.Ref(v.NodeID())
		switch ref.Kind {
		case resolve.Variant:
			c.bindVariantPattern(v.Span(), ref, want, ast.UnitVariant, nil, nil, false)
		case resolve.Const:
			got := c.constType(ref.Const)
			c.unify(want, got, v.Span(), "this constant pattern")
		default:
			if local, ok := c.res.Bindings[v.NodeID()]; ok {
				c.out.LocalTypes[local] = want
			}
		}
		if v.Sub != nil {
			c.bindPattern(v.Sub, want, irrefutable)
		}

	case *ast.TuplePat:
		elems := make([]types.Type, 0, len(v.Elems))
		for range v.Elems {
			elems = append(elems, c.freshFor(v.Span()))
		}
		tup := &types.TupleT{Elems: elems}
		if !c.unify(want, tup, v.Span(), "this tuple pattern") {
			for _, e := range v.Elems {
				c.bindPattern(e, types.Error, irrefutable)
			}
			return
		}
		for i, e := range v.Elems {
			c.bindPattern(e, elems[i], irrefutable)
		}

	case *ast.OrPat:
		for _, alt := range v.Alts {
			c.bindPattern(alt, want, irrefutable)
		}
		c.checkOrPatternBindings(v)

	case *ast.PathPat:
		c.bindPathPattern(v, want, irrefutable)
	}

	if irrefutable && isRefutable(c, p) {
		c.bag.Errorf("E0005", p.Span(), "refutable pattern where an irrefutable one is required").
			Label("this pattern can fail to match").
			Note("`let`, `for` and function parameters must match every possible value").
			Help("use `match` to handle the other cases")
	}
}

func (c *Checker) bindLitPattern(v *ast.LitPat, want types.Type) {
	// spec/05-patterns.md: float literal patterns are rejected, because NaN and -0.0
	// make equality the wrong primitive for matching.
	if _, isFloat := v.Lit.(*ast.FloatLit); isFloat {
		c.bag.Errorf("E0658", v.Span(), "a float literal cannot be a pattern").
			Label("floats have no usable equality for matching").
			Note("`NaN` never equals itself, and `0.0 == -0.0`").
			Help("use a guard: `x if x == 1.0 => ...`")
		return
	}
	got := c.infer(v.Lit)
	if v.Neg {
		if p, ok := types.AsPrim(got); ok && !p.Kind.IsSigned() && p.Kind.IsInteger() {
			c.bag.Errorf("E0600", v.Span(), "cannot negate `%s`", p.Kind).
				Label("unsigned types have no negative values")
		}
	}
	c.unify(want, got, v.Span(), "this literal pattern")
}

func (c *Checker) bindPathPattern(v *ast.PathPat, want types.Type, irrefutable bool) {
	ref, ok := c.res.Ref(v.NodeID())
	if !ok || ref.Kind == resolve.Unresolved {
		return
	}
	switch ref.Kind {
	case resolve.Variant:
		c.bindVariantPattern(v.Span(), ref, want, v.Kind, v.Elems, v.Fields, v.Rest)
	case resolve.Struct:
		c.bindStructPattern(v, ref, want, irrefutable)
	default:
		c.bag.Errorf("E0532", v.Span(), "`%s` cannot be matched as a pattern", v.Path).
			Label("expected a struct or an enum variant")
	}
}

func (c *Checker) bindVariantPattern(span diag.Span, ref resolve.Ref, want types.Type, kind ast.VariantKind, elems []ast.Pattern, fields []*ast.FieldPat, rest bool) {
	def := c.defs[ast.Item(ref.Enum)]
	if def == nil {
		return
	}
	subst, args := c.freshArgs(def, nil, span)
	if !c.unify(want, &types.Named{Def: def, Args: args}, span, "this pattern") {
		return
	}
	idx := variantIndex(def, ref.Variant)
	if idx < 0 {
		return
	}
	payload := def.VariantTypes[idx]

	switch kind {
	case ast.UnitVariant:
		if ref.Variant.Kind != ast.UnitVariant {
			c.bag.Errorf("E0532", span, "`%s` carries a payload", ref.Variant.Name.Name).
				Label("this pattern ignores it").
				Help("write `%s(..)` or `%s { .. }`", ref.Variant.Name.Name, ref.Variant.Name.Name)
		}
	case ast.TupleVariant:
		if len(elems) != len(payload) {
			c.bag.Errorf("E0023", span,
				"`%s` carries %d value%s but this pattern has %d",
				ref.Variant.Name.Name, len(payload), plural(len(payload)), len(elems)).
				Label("wrong number of values")
			return
		}
		for i, e := range elems {
			c.bindPattern(e, types.Substitute(payload[i], subst), false)
		}
	default:
		c.bindNamedFieldPatterns(span, ref.Variant.Fields, payload, subst, fields, rest, ref.Variant.Name.Name)
	}
}

func (c *Checker) bindStructPattern(v *ast.PathPat, ref resolve.Ref, want types.Type, irrefutable bool) {
	def := c.defs[ast.Item(ref.Struct)]
	if def == nil {
		return
	}
	subst, args := c.freshArgs(def, nil, v.Span())
	if !c.unify(want, &types.Named{Def: def, Args: args}, v.Span(), "this pattern") {
		return
	}
	if v.Kind != ast.StructVariant {
		c.bag.Errorf("E0532", v.Span(), "`%s` is a struct", def.Name).
			Label("match it with braces").
			Help("write `%s { .. }`", def.Name)
		return
	}
	c.bindNamedFieldPatterns(v.Span(), ref.Struct.Fields, def.FieldTypes, subst, v.Fields, v.Rest, def.Name)
}

// spanLike is anything that can report a span, which lets the variant and struct paths
// share one implementation.
type spanLike interface{ Span() diag.Span }

func (c *Checker) bindNamedFieldPatterns(span diag.Span, decls []*ast.Field, fieldTypes []types.Type, subst map[*types.Param]types.Type, pats []*ast.FieldPat, rest bool, name string) {
	seen := map[string]bool{}
	for _, fp := range pats {
		idx := -1
		for i, d := range decls {
			if d.Name.Name == fp.Name.Name {
				idx = i
				break
			}
		}
		if idx < 0 {
			c.bag.Errorf("E0026", fp.Name.Loc, "`%s` has no field `%s`", name, fp.Name.Name).
				Label("unknown field").
				Note("`%s` has %s", name, fieldList(decls))
			c.bindPattern(fp.Pat, types.Error, false)
			continue
		}
		seen[fp.Name.Name] = true
		c.bindPattern(fp.Pat, types.Substitute(fieldTypes[idx], subst), false)
	}
	if rest {
		return
	}
	var missing []string
	for _, d := range decls {
		if !seen[d.Name.Name] {
			missing = append(missing, "`"+d.Name.Name+"`")
		}
	}
	if len(missing) > 0 {
		c.bag.Errorf("E0027", span, "pattern does not mention %s", joinNames(missing)).
			Label("missing field%s", plural(len(missing))).
			Help("list them, or end the pattern with `..`")
	}
}

// checkOrPatternBindings enforces that every alternative binds the same names at the
// same types (spec/05-patterns.md, E0007).
func (c *Checker) checkOrPatternBindings(v *ast.OrPat) {
	if len(v.Alts) < 2 {
		return
	}
	first := c.patternBindings(v.Alts[0])
	for _, alt := range v.Alts[1:] {
		other := c.patternBindings(alt)
		for name, l := range first {
			o, ok := other[name]
			if !ok {
				c.bag.Errorf("E0007", alt.Span(), "`%s` is not bound in all alternatives", name).
					Label("this alternative does not bind `%s`", name).
					Secondary(l.Decl, "bound here").
					Note("every alternative of an or-pattern must bind the same names")
				continue
			}
			wt, ok1 := c.out.LocalTypes[l]
			gt, ok2 := c.out.LocalTypes[o]
			if ok1 && ok2 {
				c.unify(wt, gt, alt.Span(), "`"+name+"` must have the same type in every alternative")
			}
		}
		for name, l := range other {
			if _, ok := first[name]; !ok {
				c.bag.Errorf("E0007", v.Alts[0].Span(), "`%s` is not bound in all alternatives", name).
					Label("this alternative does not bind `%s`", name).
					Secondary(l.Decl, "bound here")
			}
		}
	}
}

func (c *Checker) patternBindings(p ast.Pattern) map[string]*resolve.Local {
	out := map[string]*resolve.Local{}
	c.collectBindings(p, out)
	return out
}

func (c *Checker) collectBindings(p ast.Pattern, out map[string]*resolve.Local) {
	switch v := p.(type) {
	case *ast.BindPat:
		if l, ok := c.res.Bindings[v.NodeID()]; ok {
			out[l.Name] = l
		}
		c.collectBindings(v.Sub, out)
	case *ast.TuplePat:
		for _, e := range v.Elems {
			c.collectBindings(e, out)
		}
	case *ast.OrPat:
		for _, a := range v.Alts {
			c.collectBindings(a, out)
		}
	case *ast.PathPat:
		for _, e := range v.Elems {
			c.collectBindings(e, out)
		}
		for _, f := range v.Fields {
			c.collectBindings(f.Pat, out)
		}
	}
}

// isRefutable reports whether a pattern can fail to match some value of its type.
func isRefutable(c *Checker, p ast.Pattern) bool {
	switch v := p.(type) {
	case nil, *ast.WildcardPat, *ast.ErrorPat:
		return false
	case *ast.LitPat:
		return true
	case *ast.BindPat:
		ref, _ := c.res.Ref(v.NodeID())
		if ref.Kind == resolve.Variant || ref.Kind == resolve.Const {
			return true
		}
		return v.Sub != nil && isRefutable(c, v.Sub)
	case *ast.TuplePat:
		for _, e := range v.Elems {
			if isRefutable(c, e) {
				return true
			}
		}
		return false
	case *ast.OrPat:
		for _, a := range v.Alts {
			if isRefutable(c, a) {
				return true
			}
		}
		return false
	case *ast.PathPat:
		ref, _ := c.res.Ref(v.NodeID())
		if ref.Kind == resolve.Variant {
			// A single-variant enum is irrefutable; anything else can fail.
			if ref.Enum != nil && len(ref.Enum.Variants) > 1 {
				return true
			}
		}
		for _, e := range v.Elems {
			if isRefutable(c, e) {
				return true
			}
		}
		for _, f := range v.Fields {
			if isRefutable(c, f.Pat) {
				return true
			}
		}
		return false
	}
	return false
}
