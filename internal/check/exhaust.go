package check

import (
	"strconv"
	"strings"

	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/resolve"
	"github.com/scarypheonix/meta/internal/types"
)

// This file implements Maranget's usefulness algorithm ("Warnings for pattern matching",
// JFP 2007), which spec/05-patterns.md names as the required algorithm.
//
// Two questions fall out of one procedure. A `match` is exhaustive when the all-wildcard
// row is *not* useful against its arms; an arm is reachable when it *is* useful against
// the arms above it. Both are errors in Origin, not warnings, which is why the trap
// table in spec/04-expressions.md has no "match failed" entry and why the code generator
// may emit a jump table with no default arm.

// ctorKind identifies the shape of a pattern's head constructor.
type ctorKind int

const (
	ctorVariant ctorKind = iota
	ctorTuple
	ctorStruct
	ctorBool
	// ctorLit covers integer, char and String literals, whose constructor space is too
	// large to enumerate: a column of them is never a complete signature, so only a
	// wildcard can make it exhaustive.
	ctorLit
)

// ctor is a pattern's head constructor, comparable so it can be used as a map key.
type ctor struct {
	kind    ctorKind
	variant int
	boolean bool
	lit     string
}

// pat is a pattern reduced to what the algorithm needs: a head constructor and its
// arguments, or a wildcard.
type pat struct {
	wild bool
	c    ctor
	args []pat
}

func wildcard() pat { return pat{wild: true} }

// row is one row of the pattern matrix.
type row []pat

// maxRows bounds or-pattern expansion. A pattern that needs more rows than this is
// pathological; the checker says so rather than hanging.
const maxRows = 4096

// checkExhaustive reports a non-exhaustive match and any unreachable arm.
func (c *Checker) checkExhaustive(m *ast.Match, scrut types.Type) {
	scrut = c.normalize(scrut)
	if types.IsError(scrut) {
		return
	}
	types.ApplyDefaults(scrut)

	var covered []row
	truncated := false

	for _, arm := range m.Arms {
		alts := c.toPats(arm.Pat, scrut)

		// Usefulness: an arm is unreachable when nothing it matches is left uncovered.
		if !truncated && len(alts) > 0 {
			reachable := false
			for _, a := range alts {
				if c.useful(covered, row{a}, []types.Type{scrut}) != nil {
					reachable = true
					break
				}
			}
			if !reachable {
				c.bag.Errorf("E0006", arm.Pat.Span(), "unreachable pattern").
					Label("this arm can never match").
					Note("every value it would match is already handled above")
			}
		}

		// Coverage: a guarded arm contributes nothing, because its guard may fail
		// (spec/05-patterns.md).
		if arm.Guard != nil {
			continue
		}
		for _, a := range alts {
			covered = append(covered, row{a})
			if len(covered) > maxRows {
				truncated = true
				break
			}
		}
		if truncated {
			break
		}
	}

	if truncated {
		c.bag.Errorf("E0004", m.Span(), "this `match` has too many pattern combinations to check").
			Label("exhaustiveness could not be decided").
			Note("or-patterns expand combinatorially; this one exceeds %d rows", maxRows).
			Help("split the match, or replace some alternatives with a binding and a guard")
		return
	}

	witness := c.useful(covered, row{wildcard()}, []types.Type{scrut})
	if witness == nil {
		return
	}
	c.bag.Errorf("E0004", m.Span(), "non-exhaustive match: `%s` is not covered", c.renderWitness(witness[0], scrut)).
		Label("this match does not handle every value of `%s`", scrut).
		Help("add an arm: `%s => ...`", c.renderWitness(witness[0], scrut))
}

// useful implements Maranget's I: it returns a witness vector when q matches some value
// no row of matrix does, and nil when matrix already covers everything q matches.
func (c *Checker) useful(matrix []row, q row, colTypes []types.Type) row {
	if len(q) == 0 {
		if len(matrix) == 0 {
			return row{} // nothing covers it: useful, and the witness is empty
		}
		return nil
	}

	head, rest := q[0], q[1:]
	headType := colTypes[0]

	if !head.wild {
		return c.usefulSpecialized(matrix, head.c, head.args, rest, colTypes)
	}

	// A wildcard head: if the column's constructors form a complete signature, the
	// wildcard is only useful when it is useful for some constructor.
	sig := c.columnCtors(matrix)
	if complete, all := c.completeSignature(headType, sig); complete {
		for _, cc := range all {
			arity := c.ctorArgTypes(headType, cc)
			args := make([]pat, len(arity))
			for i := range args {
				args[i] = wildcard()
			}
			if w := c.usefulSpecialized(matrix, cc, args, rest, colTypes); w != nil {
				return w
			}
		}
		return nil
	}

	// Incomplete: recurse on the default matrix, then name a missing constructor.
	def := defaultMatrix(matrix)
	w := c.useful(def, rest, colTypes[1:])
	if w == nil {
		return nil
	}
	missing := c.missingCtor(headType, sig)
	return append(row{missing}, w...)
}

// usefulSpecialized is Maranget's S composed with I for a known head constructor.
func (c *Checker) usefulSpecialized(matrix []row, cc ctor, args []pat, rest row, colTypes []types.Type) row {
	argTypes := c.ctorArgTypes(colTypes[0], cc)
	newCols := append(append([]types.Type{}, argTypes...), colTypes[1:]...)

	spec := specialize(matrix, cc, len(argTypes))
	q := append(append(row{}, args...), rest...)

	w := c.useful(spec, q, newCols)
	if w == nil {
		return nil
	}
	// Rebuild the witness: fold the first len(argTypes) entries back into cc.
	head := pat{c: cc, args: append([]pat{}, w[:len(argTypes)]...)}
	return append(row{head}, w[len(argTypes):]...)
}

// specialize is Maranget's S(c, P): keep rows whose head is c or a wildcard, replacing
// the head with its arguments.
func specialize(matrix []row, cc ctor, arity int) []row {
	var out []row
	for _, r := range matrix {
		if len(r) == 0 {
			continue
		}
		head := r[0]
		switch {
		case head.wild:
			expanded := make(row, 0, arity+len(r)-1)
			for i := 0; i < arity; i++ {
				expanded = append(expanded, wildcard())
			}
			out = append(out, append(expanded, r[1:]...))
		case head.c == cc:
			expanded := make(row, 0, arity+len(r)-1)
			expanded = append(expanded, head.args...)
			out = append(out, append(expanded, r[1:]...))
		}
	}
	return out
}

// defaultMatrix is Maranget's D(P): keep only the rows whose head is a wildcard.
func defaultMatrix(matrix []row) []row {
	var out []row
	for _, r := range matrix {
		if len(r) > 0 && r[0].wild {
			out = append(out, append(row{}, r[1:]...))
		}
	}
	return out
}

// columnCtors collects the constructors appearing in the first column.
func (c *Checker) columnCtors(matrix []row) map[ctor]bool {
	out := map[ctor]bool{}
	for _, r := range matrix {
		if len(r) > 0 && !r[0].wild {
			out[r[0].c] = true
		}
	}
	return out
}

// completeSignature reports whether the constructors seen cover the column's type, and
// returns the full signature when the type has an enumerable one.
func (c *Checker) completeSignature(t types.Type, seen map[ctor]bool) (bool, []ctor) {
	switch v := types.Prune(t).(type) {
	case *types.Prim:
		if v.Kind == types.Bool {
			all := []ctor{{kind: ctorBool, boolean: false}, {kind: ctorBool, boolean: true}}
			return len(seen) >= 2 && seen[all[0]] && seen[all[1]], all
		}
		if v.Kind == types.UnitKind {
			all := []ctor{{kind: ctorTuple}}
			return seen[all[0]], all
		}
		// Integers, floats, char and String have no enumerable signature; only a
		// wildcard makes such a column exhaustive (spec/05-patterns.md).
		return false, nil

	case *types.TupleT:
		all := []ctor{{kind: ctorTuple}}
		return seen[all[0]], all

	case *types.Named:
		if v.Def.Kind == types.StructDef {
			all := []ctor{{kind: ctorStruct}}
			return seen[all[0]], all
		}
		all := make([]ctor, 0, len(v.Def.Enum.Variants))
		complete := true
		for i := range v.Def.Enum.Variants {
			cc := ctor{kind: ctorVariant, variant: i}
			all = append(all, cc)
			if !seen[cc] {
				complete = false
			}
		}
		return complete, all
	}
	return false, nil
}

// ctorArgTypes returns the types a constructor's arguments have in a column of type t.
func (c *Checker) ctorArgTypes(t types.Type, cc ctor) []types.Type {
	switch v := types.Prune(t).(type) {
	case *types.TupleT:
		return v.Elems
	case *types.Named:
		subst := map[*types.Param]types.Type{}
		for i, p := range v.Def.Params {
			if i < len(v.Args) {
				subst[p] = v.Args[i]
			}
		}
		if v.Def.Kind == types.StructDef {
			out := make([]types.Type, 0, len(v.Def.FieldTypes))
			for _, ft := range v.Def.FieldTypes {
				out = append(out, types.Substitute(ft, subst))
			}
			return out
		}
		if cc.kind == ctorVariant && cc.variant < len(v.Def.VariantTypes) {
			out := make([]types.Type, 0, len(v.Def.VariantTypes[cc.variant]))
			for _, ft := range v.Def.VariantTypes[cc.variant] {
				out = append(out, types.Substitute(ft, subst))
			}
			return out
		}
	}
	return nil
}

// missingCtor picks a constructor absent from seen, to name in the witness.
func (c *Checker) missingCtor(t types.Type, seen map[ctor]bool) pat {
	switch v := types.Prune(t).(type) {
	case *types.Prim:
		if v.Kind == types.Bool {
			for _, b := range []bool{false, true} {
				cc := ctor{kind: ctorBool, boolean: b}
				if !seen[cc] {
					return pat{c: cc}
				}
			}
		}
		// For a literal column, name a value nobody matched, which is far more useful
		// than printing `_`.
		if v.Kind.IsInteger() || v.Kind == types.Char || v.Kind == types.String {
			return pat{c: ctor{kind: ctorLit, lit: freshLiteral(v.Kind, seen)}}
		}
	case *types.Named:
		if v.Def.Kind == types.EnumDef {
			for i := range v.Def.Enum.Variants {
				cc := ctor{kind: ctorVariant, variant: i}
				if !seen[cc] {
					return pat{c: cc, args: wildcardsFor(c.ctorArgTypes(t, cc))}
				}
			}
		}
	}
	return wildcard()
}

// freshLiteral invents a value of the given kind that no pattern in the column matched.
func freshLiteral(k types.PrimKind, seen map[ctor]bool) string {
	switch k {
	case types.Char:
		for r := 'a'; r <= 'z'; r++ {
			s := "'" + string(r) + "'"
			if !seen[ctor{kind: ctorLit, lit: s}] {
				return s
			}
		}
		return "'_'"
	case types.String:
		for i := 0; i < 100; i++ {
			s := strconv.Quote(strings.Repeat("x", i+1))
			if !seen[ctor{kind: ctorLit, lit: s}] {
				return s
			}
		}
		return `"..."`
	default:
		for i := 0; i < 1000; i++ {
			s := strconv.Itoa(i)
			if !seen[ctor{kind: ctorLit, lit: s}] {
				return s
			}
		}
		return "_"
	}
}

func wildcardsFor(ts []types.Type) []pat {
	out := make([]pat, len(ts))
	for i := range out {
		out[i] = wildcard()
	}
	return out
}

// ---------------------------------------------------------------------------
// Converting the AST to the algorithm's patterns
// ---------------------------------------------------------------------------

// toPats converts one AST pattern to the alternatives it stands for. An or-pattern
// becomes several; everything else becomes one.
func (c *Checker) toPats(p ast.Pattern, t types.Type) []pat {
	t = c.normalize(t)
	switch v := p.(type) {
	case nil, *ast.ErrorPat, *ast.WildcardPat:
		return []pat{wildcard()}

	case *ast.BindPat:
		ref, _ := c.res.Ref(v.NodeID())
		switch ref.Kind {
		case resolve.Variant:
			return c.variantPats(ref, nil, nil, false, t)
		case resolve.Const:
			// A constant pattern matches one value; treat it as an opaque literal so it
			// never completes a signature.
			return []pat{{c: ctor{kind: ctorLit, lit: ref.Const.Name.Name}}}
		}
		if v.Sub != nil {
			return c.toPats(v.Sub, t)
		}
		return []pat{wildcard()}

	case *ast.LitPat:
		return []pat{c.literalPat(v, t)}

	case *ast.OrPat:
		var out []pat
		for _, alt := range v.Alts {
			out = append(out, c.toPats(alt, t)...)
			if len(out) > maxRows {
				return out
			}
		}
		return out

	case *ast.TuplePat:
		elems := tupleElems(t, len(v.Elems))
		return c.productPats(ctor{kind: ctorTuple}, v.Elems, elems)

	case *ast.PathPat:
		ref, _ := c.res.Ref(v.NodeID())
		switch ref.Kind {
		case resolve.Variant:
			return c.variantPats(ref, v.Elems, v.Fields, v.Rest, t)
		case resolve.Struct:
			n, ok := types.AsNamed(t)
			if !ok || n.Def.Struct == nil {
				return []pat{wildcard()}
			}
			argTypes := c.ctorArgTypes(t, ctor{kind: ctorStruct})
			sub := c.fieldPats(n.Def.Struct.Fields, v.Fields, argTypes)
			return c.productPats(ctor{kind: ctorStruct}, nil, nil, sub...)
		}
		return []pat{wildcard()}
	}
	return []pat{wildcard()}
}

func (c *Checker) literalPat(v *ast.LitPat, t types.Type) pat {
	switch lit := v.Lit.(type) {
	case *ast.BoolLit:
		return pat{c: ctor{kind: ctorBool, boolean: lit.Value}}
	case *ast.IntLit:
		text := strconv.FormatUint(lit.Value, 10)
		if v.Neg {
			text = "-" + text
		}
		return pat{c: ctor{kind: ctorLit, lit: text}}
	case *ast.CharLit:
		return pat{c: ctor{kind: ctorLit, lit: strconv.QuoteRune(lit.Value)}}
	case *ast.StrLit:
		return pat{c: ctor{kind: ctorLit, lit: strconv.Quote(lit.Value)}}
	}
	return wildcard()
}

// variantPats builds the alternatives for an enum-variant pattern.
func (c *Checker) variantPats(ref resolve.Ref, elems []ast.Pattern, fields []*ast.FieldPat, rest bool, t types.Type) []pat {
	n, ok := types.AsNamed(t)
	if !ok || n.Def.Enum == nil {
		return []pat{wildcard()}
	}
	idx := variantIndex(n.Def, ref.Variant)
	if idx < 0 {
		return []pat{wildcard()}
	}
	cc := ctor{kind: ctorVariant, variant: idx}
	argTypes := c.ctorArgTypes(t, cc)

	switch ref.Variant.Kind {
	case ast.UnitVariant:
		return []pat{{c: cc}}
	case ast.TupleVariant:
		if len(elems) != len(argTypes) {
			return []pat{{c: cc, args: wildcardsFor(argTypes)}}
		}
		return c.productPats(cc, elems, argTypes)
	default:
		sub := c.fieldPats(ref.Variant.Fields, fields, argTypes)
		return c.productPats(cc, nil, nil, sub...)
	}
}

// fieldPats orders named field patterns by declaration, filling omitted fields with
// wildcards, which is what `..` and shorthand both reduce to.
func (c *Checker) fieldPats(decls []*ast.Field, given []*ast.FieldPat, argTypes []types.Type) [][]pat {
	out := make([][]pat, len(decls))
	for i, d := range decls {
		out[i] = []pat{wildcard()}
		for _, fp := range given {
			if fp.Name.Name != d.Name.Name {
				continue
			}
			var ft types.Type
			if i < len(argTypes) {
				ft = argTypes[i]
			}
			out[i] = c.toPats(fp.Pat, ft)
		}
	}
	return out
}

// productPats forms the cartesian product of sub-pattern alternatives under a
// constructor. Either elems+types or pre-converted alternatives may be supplied.
func (c *Checker) productPats(cc ctor, elems []ast.Pattern, elemTypes []types.Type, pre ...[]pat) []pat {
	subs := pre
	if subs == nil {
		subs = make([][]pat, len(elems))
		for i, e := range elems {
			var t types.Type
			if i < len(elemTypes) {
				t = elemTypes[i]
			}
			subs[i] = c.toPats(e, t)
		}
	}

	out := []pat{{c: cc, args: []pat{}}}
	for _, alts := range subs {
		next := make([]pat, 0, len(out)*len(alts))
		for _, base := range out {
			for _, a := range alts {
				args := append(append([]pat{}, base.args...), a)
				next = append(next, pat{c: cc, args: args})
			}
		}
		out = next
		if len(out) > maxRows {
			return out
		}
	}
	return out
}

func tupleElems(t types.Type, n int) []types.Type {
	if tup, ok := types.Prune(t).(*types.TupleT); ok {
		return tup.Elems
	}
	out := make([]types.Type, n)
	for i := range out {
		out[i] = types.Error
	}
	return out
}

// ---------------------------------------------------------------------------
// Witness rendering
// ---------------------------------------------------------------------------

// renderWitness prints a witness pattern the way a programmer would write it, so the
// diagnostic's help text can be pasted into the match.
func (c *Checker) renderWitness(p pat, t types.Type) string {
	if p.wild {
		return "_"
	}
	switch p.c.kind {
	case ctorBool:
		if p.c.boolean {
			return "true"
		}
		return "false"

	case ctorLit:
		return p.c.lit

	case ctorTuple:
		elems := tupleElems(t, len(p.args))
		if len(p.args) == 0 {
			return "()"
		}
		var parts []string
		for i, a := range p.args {
			var et types.Type
			if i < len(elems) {
				et = elems[i]
			}
			parts = append(parts, c.renderWitness(a, et))
		}
		if len(parts) == 1 {
			return "(" + parts[0] + ",)"
		}
		return "(" + strings.Join(parts, ", ") + ")"

	case ctorStruct:
		n, ok := types.AsNamed(t)
		if !ok {
			return "_"
		}
		return n.Def.Name + " { " + renderNamed(c, n.Def.Struct.Fields, p.args, c.ctorArgTypes(t, p.c)) + " }"

	case ctorVariant:
		n, ok := types.AsNamed(t)
		if !ok || n.Def.Enum == nil || p.c.variant >= len(n.Def.Enum.Variants) {
			return "_"
		}
		va := n.Def.Enum.Variants[p.c.variant]
		qualified := n.Def.Name + "::" + va.Name.Name
		switch va.Kind {
		case ast.UnitVariant:
			return qualified
		case ast.TupleVariant:
			argTypes := c.ctorArgTypes(t, p.c)
			var parts []string
			for i, a := range p.args {
				var at types.Type
				if i < len(argTypes) {
					at = argTypes[i]
				}
				parts = append(parts, c.renderWitness(a, at))
			}
			if len(parts) == 0 {
				return qualified
			}
			return qualified + "(" + strings.Join(parts, ", ") + ")"
		default:
			return qualified + " { " + renderNamed(c, va.Fields, p.args, c.ctorArgTypes(t, p.c)) + " }"
		}
	}
	return "_"
}

// renderNamed prints named fields, collapsing an all-wildcard list to `..`.
func renderNamed(c *Checker, decls []*ast.Field, args []pat, argTypes []types.Type) string {
	allWild := true
	for _, a := range args {
		if !a.wild {
			allWild = false
			break
		}
	}
	if allWild || len(decls) == 0 {
		return ".."
	}
	var parts []string
	for i, d := range decls {
		if i >= len(args) {
			break
		}
		var ft types.Type
		if i < len(argTypes) {
			ft = argTypes[i]
		}
		parts = append(parts, d.Name.Name+": "+c.renderWitness(args[i], ft))
	}
	return strings.Join(parts, ", ")
}
