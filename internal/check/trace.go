package check

import (
	"fmt"

	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/resolve"
	"github.com/scarypheonix/meta/internal/types"
)

// The inference trace: one entry per checking event, in the order the checker produces
// them, rendered at the very end.
//
// It is internal/resolve/trace.go's idea carried one pass further, and for the same
// reason: internal/check keys its side tables by ast.NodeID and stage1's syntax tree has
// no node ids, so the tables cannot be compared but the *sequence* can. Both checkers
// visit the same nodes in the same order, so an entry's position identifies the node.
//
// Rendering is deferred to the end rather than done as each entry is made, because a type
// recorded mid-body is often an unsolved variable that later unification binds and
// end-of-body defaulting resolves. Printing at record time would compare the checker's
// intermediate state, which is both less informative and needlessly brittle; printing at
// the end compares the answer.

// traceEntry is one recorded event: a label and, for most of them, a type.
type traceEntry struct {
	what string
	ty   types.Type
	// text is used instead of ty for entries that name a declaration rather than a type.
	text string
}

// noteType records an inferred type under a label.
func (c *Checker) noteType(what string, t types.Type) {
	if c.trace == nil {
		return
	}
	*c.trace = append(*c.trace, traceEntry{what: what, ty: t})
}

// noteText records an event that names a declaration rather than a type.
func (c *Checker) noteText(what, text string) {
	if c.trace == nil {
		return
	}
	*c.trace = append(*c.trace, traceEntry{what: what, text: text})
}

// noteLocal records the type a binding was given.
func (c *Checker) noteLocal(l *resolve.Local, t types.Type) {
	if c.trace == nil {
		return
	}
	*c.trace = append(*c.trace, traceEntry{what: "local " + l.Name, ty: t})
}

// noteInst records a call site's instantiation: which declaration, and the type each of
// its generic parameters was given here.
func (c *Checker) noteInst(what string, inst *Inst) {
	if c.trace == nil {
		return
	}
	*c.trace = append(*c.trace, traceEntry{what: what, text: declSite(inst.Decl), ty: instArgs(inst)})
}

// instArgs packs an instantiation's arguments into a tuple so that they render, and are
// pruned, the way any other type is. A non-generic instantiation has none, which prints
// as `()`.
func instArgs(inst *Inst) types.Type {
	return &types.TupleT{Elems: append([]types.Type{}, inst.Args...)}
}

// declSite names a declaration by where its own name was written, which is the identity
// the resolution trace already uses.
func declSite(fn *ast.FnDecl) string {
	if fn == nil {
		return "?"
	}
	s := fn.Name.Loc
	if s.File == nil {
		return fn.Name.Name + " ?:0"
	}
	return fmt.Sprintf("%s %s:%d", fn.Name.Name, s.File.Name, s.Start)
}

// renderTrace turns the recorded entries into lines, after every type in them has been
// solved as far as it is going to be.
func renderTrace(entries []traceEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		switch {
		case e.ty == nil:
			out = append(out, e.what+" "+e.text)
		case e.text != "":
			out = append(out, fmt.Sprintf("%s %s %s", e.what, e.text, e.ty.String()))
		default:
			out = append(out, e.what+" "+e.ty.String())
		}
	}
	return out
}
