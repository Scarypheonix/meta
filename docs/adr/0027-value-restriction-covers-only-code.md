# ADR-0027: The value restriction generalizes code, never data

**Status:** accepted · **Date:** 2026-09-01 · **Decided by:** implementer (user delegated)

## Context

This program printed `10` on the interpreter and trapped on the virtual machine with
`no match arm matched`:

```origin
fn build() -> Option[i64] { let out = Option::None; out }
fn main() {
    match build() {
        Option::Some(n) => { io::println(n.to_str()); },
        Option::None => { io::println("none"); },
    }
}
```

Two engines, two answers, and the wrong one was not a crash — it was a `match` over a
value of the right type finding no arm that fit.

The cause is a collision between two decisions that were each correct alone.
spec/03-types.md's value restriction generalizes a `let` whose right-hand side is a
*syntactic value*, which is the classical rule and includes a constructor. So
`let out = Option::None;` was given `forall T. Option[T]`. Meanwhile ADR-0010
monomorphizes and ADR-0019 gives every instantiation its own exact object descriptor, so
`Option[i64]::None` and `Option[String]::None` are *different runtime types*.

A generalized data binding therefore names no single runtime shape. `internal/compile`
resolved the construction site's still-open type to a distinct descriptor keyed on the
unresolved variable, built the object at that one, and `match` at the use site compared
against the real `Option[i64]` descriptor. Nothing matched.

The interpreter was unaffected, and that is the important part of how this survived: its
values are uniform Go values with no per-instantiation layout, so it cannot observe the
difference. No case in the corpus generalized a data constructor, so the differential
never had the chance to disagree.

## Options considered

- **Monomorphize the binding per use.** Build one object per instantiation the binding is
  used at. This makes `ref_eq(n, n)` capable of returning false — one binding, two
  objects — which contradicts §04's object identity.
- **A uniform representation for generic data.** Boxing would make one `None` serve every
  instantiation. That is precisely what ADR-0010 declined for the whole language, and
  reversing it for one case is worse than either choice alone.
- **Reject a construction whose type is still open.** Correct and safe, but it rejects the
  program above, which is ordinary Origin that any programmer would expect to work.
- **Narrow the value restriction to code.**

## Decision

A `let` binding generalizes only when its right-hand side is a lambda or a path naming a
function. A literal, a constructor, a struct or tuple expression, and a call are all
monomorphic; the binding's type is settled by unification with its uses.

The principle is that **generalization is sound exactly when the value's representation
does not depend on the type it is used at.** Code satisfies that: a lambda is
monomorphized per call site, so a generalized function is compiled afresh at each use.
Data does not: an object is built once, at one descriptor, and carries it for its
lifetime.

Literals are excluded for the same reason rather than as an oversight — `i64` and `u8`
are not one representation, so a generalized `let x = 1;` would have the identical defect.
Rule 3's defaulting is what settles a literal's type, and it already did.

`internal/compile` also now refuses outright to instantiate a struct or variant at a type
that still contains an inference variable (`requireConcrete`). The narrowed restriction
should mean nothing ever reaches it, which is the point: process rule 8 says an
unimplemented path stops rather than returning a plausible wrong answer, and this failure
mode's whole character was that it returned one.

## Consequences

- **A binding used at two different types is now an error** rather than silent
  corruption: `let n = Option::None;` used at both `Option[i64]` and `Option[String]`
  fails to unify. That is the correct answer under monomorphization — one binding cannot
  have two layouts — and it is reported at the second use.
- **Nothing in the existing corpus depended on generalizing data.** The whole conformance
  and end-to-end suite passes unchanged, which is some evidence the classical rule was
  buying nothing here beyond the bug.
- **Lambda polymorphism is untouched**, including spec/03-types.md's own worked example
  `let f = |x| x;`.
- **This is a specification change, not only an implementation fix.** §03's rule 2 said
  what the implementation did; both were wrong together, in the sense that neither was
  consistent with ADR-0010. The rule is amended in the same commit (process rule 1).
- The interpreter's immunity is worth remembering: an engine with a uniform value
  representation cannot catch a layout bug, so agreement between it and one other engine
  is weaker evidence than agreement between the virtual machine and native code.
