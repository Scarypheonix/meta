# Phase 13 — Complete

**Exit criteria:** the Phase 11 audit measured why Origin reads heavily and found the answer
was not grammar: **852 `while` loops against 28 `for..in`, 731 manual index increments, and
zero iterator combinators.** Every one of those loops costs four lines and a nesting level
where `for x in xs` costs one. The user's standing Phase 12 delegation covers it, and
"continue" chose it over the two held items.

**Status:** met. `./check` passes in **209s at 1,974 MiB** against budgets of 300s and
3,072 MiB. **41 ADRs, 20 specification documents.**

## What was built

`range`, thirteen methods on `List[T]`, and four free functions — **all of it ordinary
Origin in the prelude, with no compiler change of any kind.** Every shape was verified
expressible against the real checker before a line of library code was written, which is
what kept the phase from becoming a type-system phase by accident.

| | |
|---|---|
| Lazy source | `range(lo, hi)` — a struct with a `next`, allocating nothing |
| On `List[T]` | `map` `filter` `fold` `any` `all` `sort_by` `reverse` `contains` `index_of` `take` `skip` `first` `last` |
| Free functions | `sum` `max` `min` `join` |

**stage1's driver had hand-rolled its own stable insertion sort.** It calls the prelude's
now — 22 lines deleted from `main.origin`. The compiler's own source using the library is
the only proof that matters that the library is good enough.

## Two limits found by writing it

Neither was known before this phase, and both are now in `docs/deferred.md` rather than
worked around in silence.

**Lazy adapters cannot be expressed at all.** A lazy `map` wraps an iterator `I` and an
`fn(A) -> B`, and typing its `next` requires saying that `I`'s associated `Item` **is** `A`.
A bound names a trait and its *type arguments* (§06); `Item` is an associated type, not a
parameter; there is no syntax for the constraint. The body fails with `expected A, found
I::Item`. So every transform here is eager — it builds a new list — and §13 says so plainly
instead of implying a laziness the language cannot deliver.

**Candidate-impl probing is not speculative.** `sum`, `max`, `min` and `join` each apply to
one instantiation, so they wanted `impl List[i64]` and `impl List[String]`. Adding them
broke inference for *every* list literal of another element type:

```
let w = ["a", "b"]      ->  error: expected `i64`, found `String`
```

`lookupMethod` tries each candidate impl with `types.Unify(self, recv)` and checks whether
that impl actually has the method **afterwards**. `Unify` binds, and nothing rolls it back,
so probing the rejected `impl List[i64]` against a receiver whose element type was still a
variable bound it to `i64` permanently. `selfKey` returns just `"List"`, which is what puts
the generic impl and the instantiation impls in one bucket and brings them into contact.

The defect is the checker's and predates this library — **this phase is simply the first
time the prelude has had an impl on a concrete instantiation to expose it.** The fix is a
trail or a non-binding `CanUnify` in `internal/types`, mirrored in stage1 and re-earned
against 1.6M selfhost trace lines: a phase, not a patch. Until then the four are free
functions, which never enter the probe, and §13 explains the inconsistency rather than
hiding it.

## The phase amended the phase before it

Using the library produced, immediately, code that Phase 12 rejected:

```origin
let words = text.split("\n")
    .filter(|w| !w.is_empty())     // REJECTED under ADR-0040 as first written
```

Putting the operator last is the migration ADR-0040 asked of 93 sites, and for arithmetic it
is fine. For a method chain it is not — every language with fluent chains wraps with the dot
leading. Rule 3 now suppresses insertion before a leading `.` as well as before `}`, and the
justification is *stronger* than the brace's: nothing in the grammar can begin with a dot, so
a line break followed by one is always mid-expression and the rule trades away nothing. The
trailing dots ADR-0040 forced on `arith.origin` are back to leading ones.

**That is the finding worth carrying:** a grammar rule and a library are not independent. The
cost of ADR-0040's migration looked like 93 sites when measured against the corpus that
existed, and the corpus that existed had no fluent API in it because the standard library had
no fluent API to use. A rule validated against today's code can be wrong about tomorrow's.

## The budget went down

**239s → 201s → 209s**, against a 300s ceiling. Prelude growth cost nothing measurable, and
the reason is Phase 7's: declarations the program does not reach are not roots, so an
unused method is never monomorphized and never reaches any engine. A library can be
generous without every program paying for it.

## What this does and does not change

It is the lever the Phase 11 audit ranked first for how Origin *looks*, and the word-frequency
program is the evidence: the corpus version is 85 lines, and the same program over this
library is 16. What it does not touch is braces — **18% of code lines are still nothing but a
closing brace**, and only indentation sensitivity changes that.

## Still open

**`std::iter` is gone as a drift item**: §10's example 11 had not compiled since Phase 7,
having been written against a module nobody built with nothing running it. It is
`tests/e2e/cases/range_and_iteration.origin` now, so the next divergence fails a test rather
than sitting in a document. **The formatter** is still referenced twice in the spec and
neither built nor scheduled.

**Held by the user, and untouched:** associated functions (`List::new()`) and tuple element
access (`t.0`). Associated functions would also let `sum` and `join` be methods without the
checker fix, which is a connection worth noticing when that decision is made.
