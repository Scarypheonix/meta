# Phase 11 — Complete

**Exit criteria:** rule 7 put the scope with the user. What they asked for was three specific
simplifications to Origin's surface syntax, chosen from an audit of every candidate and
ranked by what they cost: a prelude enum's variants in scope unqualified, `if let` and
`while let`, and list literals. Not a redesign — three shorthands, each of which had to leave
every existing program meaning exactly what it meant.

**Status:** met. All three land, `./check` passes in **176s at 1,929 MiB** against budgets of
300s and 3,072 MiB, and `tests/selfhost` holds the two front ends to each other over the
repository's own source with no divergence. **39 ADRs, 20 specification documents.**

```
Option::Some(x)         ->  Some(x)                     1,386 sites could shorten
match o { Some(n) => a, None => {} }
                        ->  if let Some(n) = o { a }     147 of stage1's 534 matches
list::new(); push; push ->  [a, b]
```

## What was built

| Feature | ADR | Where the change lives |
|---|---|---|
| Unqualified prelude variants | 0037 | the resolver, and nothing else |
| `if let` / `while let` | 0038 | the parser, as a desugaring |
| List literals | 0039 | the parser, as a desugaring |

Each landed twice — `internal/` in Go and `stage1/src` in Origin — because a syntax change in
this project is a change to two implementations of the same compiler, held to each other
trace-for-trace.

**Nothing downstream learned a new form.** The resolver, the checker, monomorphization, the
bytecode compiler and all three engines are untouched by two of the three features and see
only a new scope entry for the third. That is what kept the cost down, and it is the same
move `internal/parse/interp.go` documents for string interpolation and `check.forElementType`
for `for`.

**No golden file moved.** Both spellings of a variant resolve to the same `Ref`, the
qualified name included, and the two new forms were syntax errors before, so no existing
source changed shape. Every snapshot, bytecode dump, IR dump and selfhost trace over the
existing corpus is byte-identical. This is what made three features affordable inside a
budget that was already at 208s of 300s.

## The three things worth carrying forward

**The corpus corrected an ADR that reasoning had got wrong.** ADR-0037's first decision was
"every enum the prelude declares", argued from a rule that sounds right — prelude items are
in scope, so their variants should be too — and needing no list in the compiler.
`tests/e2e` rejected it inside a minute: `expression_language.origin` writes
`other => Result::Err(...)`, `other` as a catch-all binding appears four times in this
repository, and `IoError::Other` is a unit variant, so W0003 fired on correct code. The tell
had already been there and was misread: scoping W0003 away from irrefutable positions had
been *necessary* to keep it off `Ord::cmp(self, other: Self)`, which is 12 sites in the
prelude alone. A name whose collision has to be worked around twice does not belong in the
global scope. The rule is now the three enums the language's own constructs produce and
consume — which is the line `internal/check` and `internal/compile` had already drawn by
naming `Option`, `Result` and `Ordering` 8, 4 and 1 times, and `IoError` never.

**A desugaring is invisible until a diagnostic points at it.** `if let` reduces to a two-arm
`match`, and every pass downstream is happy — until the pattern is irrefutable, when the
usefulness check reports "unreachable pattern" against a `_` arm that exists nowhere in the
source. That is a diagnostic that lies about where the problem is, which process rule 8 rules
out as firmly as a stub that lies. `ast.Match` therefore carries one field naming the
construct it came from, with exactly one reader added in the same commit (Phase 9's rule
about slots), and `E0008` names the programmer's own pattern. The same class of gap, found
the same way: a `while` body must produce no value and a desugared `while let` body would
have been allowed to produce one and have it silently discarded — two loops differing for a
reason no reader could guess.

**A test helper had been quietly not testing the thing.** `resolveWithPrelude` passed the
prelude as an ordinary file rather than with `Prelude: true`, so its items went into the root
module's scope instead of the globals. Close enough for everything the tests had asked so
far, and it meant the first unqualified-variant test failed with `cannot find Some in this
scope` — correctly, because nothing had been put where the real compiler puts it. Anything
depending on that distinction had been untestable for ten phases, and no test could have
reported it, because the helper's mistake and the assertion's expectation were the same
mistake.

## What the audit rejected, and why it is written down

The phase began as an audit of every simplification available, not these three. The ones
turned down are recorded so that they are not re-proposed as new ideas:

- **Index syntax `v[i]`** — ADR-0013 ruled out the production and ADR-0028 declined the
  `v.[i]` escape hatch. The method form carries what the bracket erases: `get` returns an
  `Option`, `at` traps, and `v[i]` would have to silently pick one.
- **`io::println` taking `T: Show`** — 349 `.to_str()` calls in the corpus want it, but it is
  a compiler builtin, so every call site would gain a monomorphized instance and a `to_str`
  call, churning every bytecode and IR snapshot and every selfhost comparison. String
  interpolation already banked most of the ergonomic gain in Phase 7.
- **Angle brackets for generics** — reverting ADR-0013 buys familiarity and pays for it with
  permanent lexer/parser entanglement, in a compiler that has already been rewritten in its
  own language once.
- **Removing the struct-literal-in-condition restriction** — the restriction is what makes
  `if x {` unambiguous.
- **Making comparison associative** — rejecting `a < b < c` catches a real bug class at no
  cost to correct programs.
- **Unifying `mut` on bindings and on fields** — ADR-0004, and load-bearing past syntax:
  field-level `mut` is what ADR-0014 derives `Send` from.

## Deferred, and where it stands

**Associated functions (`List::new()`)** and **tuple element access (`t.0`)** were the fourth
and fifth recommendations of the audit and the user held both for a separate decision. Both
remain in `docs/deferred.md` with their costs now measured rather than estimated: associated
functions are 568 corpus sites and the only candidate that reaches the type checker (where do
an impl's type arguments come from with no receiver?), and `t.0` needs a lexer rule for the
`x.0.1` case, which is the one place any of this would reintroduce the context-sensitivity
ADR-0013 spent its budget removing.

**`W0003` is emitted for the first time** since it was registered in 0.1, which also gave
stage1's resolver its first warning severity — `ResolveError` gained the `warning` flag
`check::CheckError` already had, because counting a warning as fatal would have made
`stage1 resolve` reject what `originc` accepts.

**Case folding is ASCII on both sides, deliberately.** `strings.EqualFold` folds all of
Unicode and stage1 has no case tables — the same gap its lexer records for `XID_Start` — so
the Go side folds only ASCII too. The two now agree by construction rather than because the
corpus happens to hold no non-ASCII identifier, and for a heuristic warning a missed one is
the safe direction.

**The six-argument native limit bit for the third time** (`docs/deferred.md`): `let_match` as
a method is seven arguments with `self`, so it is a free function taking a span. Working
within it is still easy and it is still a compiler people work around.
