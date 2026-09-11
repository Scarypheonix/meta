# Phase 14 — Complete

**Exit criteria:** two things the phase before it left on the floor. Phase 13 found a
checker defect and priced its fix as "a phase, not a patch"; that estimate was wrong and
this phase says why. And the user reframed the audience mid-phase — **teenagers, not
software engineers** — which changes which costs count and made the `fn main` wrapper the
next thing to go.

**Status:** met. `./check` passes in **197s at 1,949 MiB** against budgets of 300s and
3,072 MiB. **42 ADRs, 20 specification documents.**

## What was built

**A reorder, and a wrapper.**

| | |
|---|---|
| `internal/check/traits.go` | ask whether a candidate impl *has* the method before unifying the receiver against it |
| `internal/parse/parse.go` | a file's top-level statements become the body of a synthesized `main` (**ADR-0042**) |

Both are mirrored in `stage1/src`, both are covered by conformance and end-to-end cases,
and neither moved a golden file.

## The Phase 13 defect was four lines, not a trail

Phase 13 recorded this and priced it as a type-system phase:

```
let w = ["a", "b"]      ->  error: expected `i64`, found `String`
```

with `impl List[i64]` (for `sum`) sitting beside `impl[T] List[T]` in the prelude. The
diagnosis was right: `lookupMethod` calls `types.Unify(self, recv)` against each candidate
impl and asks whether that impl declares the method **afterwards**, and `Unify` binds with
no rollback, so probing a candidate that was going to be rejected anyway permanently bound
the receiver's element type to `i64`.

The *fix* was read wrong. "Unify has no trail" led straight to "so write one", and a trail
in `internal/types` is genuinely a phase: it has to be mirrored in `stage1/src/types.origin`
and re-earned against 1,647,768 inference trace lines. But the question the probe is asking
is `does this impl have a method called push`, and the answer does not depend on the
receiver at all. Moving the `info.Methods[name]` lookup above the `Unify` call means a
candidate about to be rejected never touches the receiver, and there is nothing to roll
back.

`sum`, `max`, `min` and `join` are methods again — `xs.sum()`, `words.join(", ")`.

**The lesson is about how a defect gets priced.** The report named the mechanism
accurately and then the cost came from the mechanism rather than from the requirement.
*What has to be true* was "a rejected candidate must not bind the receiver", and the
cheapest way to make that true is not to probe it. **Ask what the fix has to achieve before
asking what the machinery would cost** — the two questions have different answers more often
than they look like they should.

**What this does not fix**, now in `docs/deferred.md`: two impls that *both* declare the
method and *both* unify. The first probed still binds and the second then fails rather than
being reported as ambiguous. That one does need the trail. No shape in the prelude reaches
it, and the error when something does is a type mismatch rather than a wrong answer.

## `println("hi")` is a complete program

ADR-0042. Statements may appear at a file's top level and become the body of a synthesized
`fn main()`, in source order; a file with both top-level statements and a declared `main` is
rejected.

**It is a wrapper, not a script.** Origin's items hoist — §07 resolves every item in a
package before any body — so "the file executes top to bottom" would be false about half of
a file that mixes declarations and statements. Wrapping keeps the existing rule exactly as
written: the statements run in order, the declarations are visible regardless of where they
sit.

**Additive.** Every `.origin` file in this repository declares `main` and has no top-level
statements, so nothing in the corpus changed and no golden file moved — the same property
that made three features affordable in Phase 11.

## What a beginner hits next, and why this phase stopped

The four things that block someone writing their first Origin program, in the order they
hit them:

| | | |
|---|---|---|
| 1 | `fn main() { }` around everything | **removed, ADR-0042** |
| 2 | `xs[0]` does not exist | ADR-0013 — `[]` is type application |
| 3 | a function signature needs types | ADR-0009 |
| 4 | `Option` turns up the first time you index anything | ADR-0007 |

**Only the first is not a pillar.** The other three are listed in `CLAUDE.md` under
*Language invariants*, each has an ADR behind it, and reversing one is the user's call —
not something to take under a standing "simplify the syntax" delegation (rule 7). They are
priced here so that whoever decides is deciding with numbers:

- **`xs[0]`** is the one that recurs: it is hit on every list access, and ADR-0013's
  argument is specifically that `[]` must not need parser feedback to disambiguate from
  type application. A distinct token (`xs.[0]`, `xs@0`) costs nothing in the lexer and
  costs a new thing to learn; reusing `[` costs the lexer/parser separation the ADR exists
  to protect. **This is a real decision with a real trade, and it is the biggest of the
  three.**
- **Mandatory signatures** are what make monomorphization and the whole-program error
  story work; inferring them is not a syntax change.
- **`Option`** is the no-null pillar and reversing it is reversing the language.

## Carrying forward

- **The reorder is the shape of the lesson**: a defect report that names a mechanism will
  quote you the mechanism's price. Re-derive what the fix must *achieve* first.
- **`fn main` was the last piece of ceremony that was not load-bearing.** Everything left
  between Origin and a fifteen-year-old's first program is a language invariant, which
  means the next simplification phase is a *design* phase and needs the user in it.
- **Phase 15's scope is the user's** (rule 7).
