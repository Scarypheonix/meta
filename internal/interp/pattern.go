package interp

import (
	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/resolve"
)

// bindPattern binds an irrefutable pattern, trapping if it turns out to be refutable.
// Phase 2's checker rejects a refutable pattern in `let`, `for` or a parameter at
// compile time (E0005); until then this is where it is caught.
func (in *Interp) bindPattern(f *frame, p ast.Pattern, v Value, span diag.Span) {
	if !in.matchPattern(f, p, v) {
		in.trap(span, "refutable pattern did not match %s", Display(v))
	}
}

// matchPattern tests v against p, binding into f as it goes, and reports whether the
// match succeeded. On failure some bindings may already have been written; the caller
// restores them (see saveVars).
func (in *Interp) matchPattern(f *frame, p ast.Pattern, v Value) bool {
	switch pat := p.(type) {
	case nil:
		return true

	case *ast.WildcardPat:
		return true

	case *ast.ErrorPat:
		return false

	case *ast.LitPat:
		return in.matchLiteral(pat, v)

	case *ast.BindPat:
		// The resolver decided whether this name binds or names a unit variant or a
		// constant (spec/02-grammar.md).
		if ref, ok := in.res.Ref(pat.NodeID()); ok {
			switch ref.Kind {
			case resolve.Variant:
				e, isEnum := v.(*Enum)
				if !isEnum || e.Variant != ref.Variant {
					return false
				}
				return pat.Sub == nil || in.matchPattern(f, pat.Sub, v)
			case resolve.Const:
				want, c := in.evalExpr(ref.Const.Value)
				if c.stops() {
					return false
				}
				eq, err := equal(want, v)
				if err != nil {
					in.trap(pat.Span(), "%s", err.Error())
				}
				if !eq {
					return false
				}
				return pat.Sub == nil || in.matchPattern(f, pat.Sub, v)
			}
		}
		local, ok := in.res.Bindings[pat.NodeID()]
		if !ok {
			return false
		}
		if pat.Sub != nil && !in.matchPattern(f, pat.Sub, v) {
			return false
		}
		f.vars[local] = v
		return true

	case *ast.TuplePat:
		t, ok := v.(*Tuple)
		if !ok || len(t.Elems) != len(pat.Elems) {
			return false
		}
		for i, sub := range pat.Elems {
			if !in.matchPattern(f, sub, t.Elems[i]) {
				return false
			}
		}
		return true

	case *ast.OrPat:
		for _, alt := range pat.Alts {
			saved := saveVars(f)
			if in.matchPattern(f, alt, v) {
				return true
			}
			restoreVars(f, saved)
		}
		return false

	case *ast.PathPat:
		return in.matchPathPattern(f, pat, v)
	}
	return false
}

func (in *Interp) matchLiteral(pat *ast.LitPat, v Value) bool {
	lit, c := in.evalExpr(pat.Lit)
	if c.stops() {
		return false
	}
	if pat.Neg {
		switch n := lit.(type) {
		case Int:
			lit = Int(-int64(n))
		case Float:
			lit = Float(-float64(n))
		}
	}
	eq, err := equal(lit, v)
	if err != nil {
		in.trap(pat.Span(), "%s", err.Error())
	}
	return eq
}

func (in *Interp) matchPathPattern(f *frame, pat *ast.PathPat, v Value) bool {
	ref, ok := in.res.Ref(pat.NodeID())
	if !ok {
		return false
	}

	switch ref.Kind {
	case resolve.Variant:
		e, isEnum := v.(*Enum)
		if !isEnum || e.Variant != ref.Variant {
			return false
		}
		switch pat.Kind {
		case ast.UnitVariant:
			return true
		case ast.TupleVariant:
			if len(pat.Elems) != len(e.Vals) {
				in.trap(pat.Span(), "`%s` takes %d value(s) but the pattern has %d",
					ref.Variant.Name.Name, len(e.Vals), len(pat.Elems))
			}
			for i, sub := range pat.Elems {
				if !in.matchPattern(f, sub, e.Vals[i]) {
					return false
				}
			}
			return true
		default:
			return in.matchNamedFields(f, pat, e.FieldIndex, e.Vals)
		}

	case resolve.Struct:
		s, isStruct := v.(*Struct)
		if !isStruct || s.Def != ref.Struct {
			return false
		}
		return in.matchNamedFields(f, pat, s.FieldIndex, s.Vals)
	}
	return false
}

func (in *Interp) matchNamedFields(f *frame, pat *ast.PathPat, index func(string) int, vals []Value) bool {
	for _, fp := range pat.Fields {
		i := index(fp.Name.Name)
		if i < 0 {
			in.trap(fp.Span(), "no field `%s` to match", fp.Name.Name)
		}
		if !in.matchPattern(f, fp.Pat, vals[i]) {
			return false
		}
	}
	return true
}
