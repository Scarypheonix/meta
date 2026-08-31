package check

import (
	"fmt"

	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/types"
)

// `Send` is derived, not implemented (spec/08-memory-model.md, ADR-0014). No program
// writes `impl Send for T`; a type either has the structure that makes concurrent sharing
// safe or it does not, and the compiler works that out.
//
// The rule, and the whole reason Origin has no data races without a borrow checker: a
// type is `Send` when it carries no mutable state reachable from another thread. Since
// ADR-0004 makes mutability a syntactic, field-level property, that is decidable by
// looking at declarations:
//
//   - every primitive, and `String`, which is immutable;
//   - a struct with no `mut` field, all of whose field types are `Send`;
//   - an enum, all of whose payload types are `Send` (a variant's payload is never `mut`,
//     since a variant is rebound rather than mutated);
//   - a tuple whose elements are all `Send`;
//   - a function value whose captures are all `Send` -- checked at the closure, since a
//     function *type* does not record what a particular closure captured.
//
// An aggregate with a `mut` field is not `Send` at any instantiation, which is what makes
// `Mutex[T]` the only route to shared mutation.
//
// Recursion is possible (a list whose tail is a list), so the walk carries the set of
// definitions already being judged and treats a cycle as satisfied: a cycle adds no new
// mutable field, so it cannot be what makes a type unsendable.
func (c *Checker) isSend(t types.Type) bool {
	return c.sendReason(t, map[*types.Def]bool{}) == nil
}

// sendFailure explains why a type is not `Send`. It is nil when the type is.
type sendFailure struct {
	// Type is the specific type that failed, which may be nested inside the one asked
	// about: `Outer` is not `Send` *because* `Inner.n` is `mut`.
	Type types.Type
	// Field names the offending field, empty when the type itself is the problem.
	Field string
	// Reason is the phrase completing "because ...".
	Reason string
	// Variant marks Field as an enum variant rather than a struct field, since calling a
	// variant a "field" in a diagnostic is simply wrong.
	Variant bool
	// Cause is the nested failure this one is explained by, when the offending field's
	// own type is what is unsendable. Rendered as its own note rather than folded into
	// this one's text, so a chain reads as a chain instead of one run-on sentence.
	Cause *sendFailure
}

// chain renders the failure and everything that explains it, outermost first.
func (f *sendFailure) chain() []string {
	var out []string
	for cur := f; cur != nil; cur = cur.Cause {
		out = append(out, cur.String())
	}
	return out
}

func (f *sendFailure) String() string {
	if f.Field != "" {
		kind := "field"
		if f.Variant {
			kind = "variant"
		}
		return fmt.Sprintf("`%s` is not `Send` because its %s `%s` %s", f.Type, kind, f.Field, f.Reason)
	}
	return fmt.Sprintf("`%s` is not `Send` because it %s", f.Type, f.Reason)
}

// sendReason returns nil when t is `Send`, or the first reason it is not.
//
// "First" is by declaration order, so the diagnostic names the field a programmer would
// find first when reading the type, rather than whichever the map iteration reached.
func (c *Checker) sendReason(t types.Type, visiting map[*types.Def]bool) *sendFailure {
	t = types.Prune(t)

	switch v := t.(type) {
	case *types.ErrorT:
		return nil // already reported; one mistake, one diagnostic
	case *types.Prim:
		return nil // every primitive is a value, and `String` is immutable
	case *types.TupleT:
		for _, e := range v.Elems {
			if f := c.sendReason(e, visiting); f != nil {
				return f
			}
		}
		return nil
	case *types.FnT:
		// A function *type* says nothing about captures; the closure that produced the
		// value does. checkSpawnCaptures does that part, at the expression.
		return nil
	case *types.Var:
		return nil // unsolved: the unsolved-type check reports it instead
	case *types.Param:
		// A rigid parameter is `Send` only if it was declared so. satisfies() handles
		// bound lookup, so reaching here means no such bound exists.
		return &sendFailure{Type: t, Reason: "is a type parameter with no `Send` bound"}
	case *types.Named:
		return c.namedSendReason(v, visiting)
	}
	return &sendFailure{Type: t, Reason: "is not a type that can be sent"}
}

func (c *Checker) namedSendReason(n *types.Named, visiting map[*types.Def]bool) *sendFailure {
	def := n.Def
	if def == nil {
		return nil
	}
	// A recursive type is judged by its fields, and a cycle introduces none that are new.
	if visiting[def] {
		return nil
	}
	visiting[def] = true
	defer delete(visiting, def)

	// Field types are written in terms of the definition's own parameters, so they have
	// to be substituted at this instantiation: `Holder[C]` is unsendable exactly when
	// `C` is, and `Holder[i64]` may be fine.
	subst := map[*types.Param]types.Type{}
	for i, p := range def.Params {
		if i < len(n.Args) {
			subst[p] = n.Args[i]
		}
	}
	at := func(t types.Type) types.Type {
		if len(subst) == 0 {
			return t
		}
		return types.Substitute(t, subst)
	}

	switch def.Kind {
	case types.StructDef:
		if def.Struct == nil {
			return nil
		}
		for i, field := range def.Struct.Fields {
			if field.Mut {
				return &sendFailure{Type: n, Field: field.Name.Name, Reason: "is `mut`"}
			}
			if i >= len(def.FieldTypes) {
				continue
			}
			if f := c.sendReason(at(def.FieldTypes[i]), visiting); f != nil {
				// Name the field the programmer can see, and keep the inner failure as
				// the cause rather than splicing its sentence into this one.
				return &sendFailure{
					Type: n, Field: field.Name.Name,
					Reason: fmt.Sprintf("has type `%s`", f.Type),
					Cause:  f,
				}
			}
		}
		return nil

	case types.EnumDef:
		// A variant's payload is never `mut`: a variant is replaced, never mutated. So
		// only the payload types matter.
		for vi, payload := range def.VariantTypes {
			for _, p := range payload {
				if f := c.sendReason(at(p), visiting); f != nil {
					name := ""
					if def.Enum != nil && vi < len(def.Enum.Variants) {
						name = def.Enum.Variants[vi].Name.Name
					}
					if name != "" {
						return &sendFailure{
							Type: n, Field: name,
							Reason:  fmt.Sprintf("carries a `%s`", f.Type),
							Cause:   f,
							Variant: true,
						}
					}
					return f
				}
			}
		}
		return nil
	}
	return nil
}

// closureCaptureNotSend reports the first capture of a closure that is not `Send`.
//
// This is the half of the rule a type cannot carry: `fn() -> i64` is one type whether the
// closure behind it captured an immutable counter or a mutable one, so the check has to
// happen where the closure is written, against the captures resolution recorded for it.
func (c *Checker) closureCaptureNotSend(lam *ast.Lambda) (string, *sendFailure) {
	for _, capture := range c.res.Captures[lam.NodeID()] {
		t := c.out.LocalTypes[capture]
		if t == nil {
			continue
		}
		if f := c.sendReason(t, map[*types.Def]bool{}); f != nil {
			return capture.Name, f
		}
	}
	return "", nil
}

// reportNotSend diagnoses a failed `Send` bound (E0700).
//
// The generic "nothing implements it; write an impl" advice is wrong for a derived
// trait: no program makes a type `Send` by writing an impl, and a programmer following
// that help would waste their time and then be told the impl is not allowed. What is
// actionable is the field standing in the way, and what to do about it — which for a
// `mut` field is to remove the `mut` or to wrap the value in a `Mutex`.
func (c *Checker) reportNotSend(b Bound) {
	subject := types.Prune(b.Type)
	code, what := b.Code, b.What
	if code == "" {
		code = "E0700"
	}
	if what == "" {
		what = "cannot cross a thread boundary"
	}
	d := c.bag.Errorf(code, b.Span, "`%s` %s", subject, what).
		Label("this value is not `Send`")

	if _, isParam := subject.(*types.Param); isParam {
		d.Help("add a bound: `%s: Send`", subject)
		d.Secondary(b.Trait.Decl.Name.Loc, "trait declared here")
		return
	}

	if f := c.sendReason(subject, map[*types.Def]bool{}); f != nil {
		for _, line := range f.chain() {
			d.Note("%s", line)
		}
	}
	d.Note("`Send` is derived, never implemented by hand: a type is `Send` when it has " +
		"no `mut` field and its fields are all `Send`")
	d.Help("remove `mut`, or share the value as a `Mutex[%s]`, whose accessor holds the lock", subject)
	d.Secondary(b.Trait.Decl.Name.Loc, "trait declared here")
}
