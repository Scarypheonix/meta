# Phase 2 — Complete

**Exit criteria:** `tests/conformance/` has 200+ files with expected verdicts, all
correct; type errors name the source span and explain the conflict in plain language —
not `cannot unify t17 with t42`. <!-- allow-internal-identifier: quoted as the anti-pattern -->

**Status:** met on both halves.

- **243 conformance cases**, 105 accept and 138 reject, exercising **39 distinct
  diagnostic codes**. All verdicts correct; no case is skipped.
- **`TestDiagnosticQuality` enforces the second half as a test**, over the whole reject
  corpus rather than by inspection. Every error must carry a valid span, a label saying
  what is wrong, no internal identifier anywhere in its text, and — unless it is a
  lexical or syntax error, whose message already quotes the offending token — at least
  one note, help or second span. A diagnostic that is merely correct now fails the suite.

`./check` passes in 2 seconds at 164 MiB, against budgets of 300 s and 3072 MiB.
714 tests across 17 packages; 12,705 lines of implementation.

## What was built

**`internal/types`** — the shared type language and the single module the checker and,
later, the backend agree on. Primitives; nominal types with generic parameters; tuples;
function types; rigid parameters; associated-type projections; and inference variables
carrying a Rémy rank, so `let` generalization quantifies exactly the variables no
enclosing binding constrains. Unification has the occurs check, and an unsolved variable
prints as `_` — spec §09 rule 2 forbids internal identifiers in messages, so the type
printer is part of the diagnostic contract, not a debugging aid.

**`internal/check`** — ADR-0009's strategy, implemented:

- Signatures are mandatory, so every body is checked in isolation against a known type
  and every error is attributable to one function.
- Inference runs inside a body. `let` generalizes **only syntactic values** — the value
  restriction — which is what keeps `let v = Vec::new();` monomorphic and stops pushing
  an `i64` and reading back a `String`.
- Defaulting happens once, after the body's constraints are solved. Integer and float
  literals carry a constraint as well as a default: an integer literal's variable will
  not unify with `f64`, which is what stops the implicit int-to-float conversion
  ADR-0012 rules out from creeping in through the back door.
- **Maranget's usefulness algorithm** decides both exhaustiveness and reachability, and
  prints a concrete witness: `non-exhaustive match: `Shape::Rect { .. }` is not covered`,
  with the missing arm offered as a help.
- Trait bounds with supertraits; the overlap half of coherence, including conflicts with
  the compiler's own impls; method resolution in spec §06's order, with no autoref and
  no autoderef.

**`internal/resolve`** — extended from one file to a package. The filesystem is the
module tree: a file's path under the source root is its module path, `main.origin` and
`lib.origin` are the root module, and nothing is registered anywhere. Resolution runs in
four passes — build the tree, declare items, process imports, resolve bodies — which is
what makes module cycles legal. Visibility is the specification's two levels, and
`std::io` is a real module rather than a special case in path resolution.

**Prelude** — `Ord`, `Show`, `Iterator`, `IntoIterator`, `Send`, and `Int` for the
non-trapping arithmetic ADR-0005 makes explicit. Their impls for primitives are
compiler-provided but registered as ordinary impls, so `i64: Show` is solved by the same
code path as `MyType: Show` rather than by a special case.

## What was deferred

`docs/deferred.md` gained five entries and closed one:

- **Monomorphization** moves to Phase 4. ADR-0010 is a *lowering* strategy; Phase 2's
  job was to make generics type-check, and there is no IR to instantiate into yet.
- **The orphan rule (`E0117`)** moves to Phase 8. It cannot be violated while a program
  is a single package, so implementing it now would be untestable.
- **Per-width integer overflow** and **`u64::MAX`** move to Phase 3. The checker knows
  the widths; the tree-walking interpreter's value model still carries every integer as
  an `i64`. `u64::MAX` traps rather than returning a plausible wrong number.
- **Compiler-provided impls** stay until Phase 7, when a standard library written in
  Origin can express them.
- **Static field-mutability checking is done**, moved out of the deferred list: it is
  `E0594` at compile time now, where the specification always wanted it.

## What surprised me

1. **Node ids were being allocated per file.** The prelude and the user's file both
   started at 1, so every side table silently shared entries between them. It was
   invisible in Phase 1 because the interpreter only ever walked user code. The fix was
   one generator per compilation; the lesson was that a key which is only unique within
   a file is not a key.

2. **The resolver walked types in declarations but not in bodies.** So `let x: i64 = ...`
   recorded no resolution, the checker read the missing entry as the error type, and
   *every annotated binding type-checked against anything*. One forgotten call site
   disabled a whole class of checking, silently, while the suite stayed green. The fix
   was three lines; the response was `ast.Inspect` plus a structural test that walks a
   program and asserts every type path resolves. That test fails for any type position
   added later and not walked — which is the only durable answer to "did I remember
   every case?"

3. **Ordering inside the checker turned out to be load-bearing three separate times.**
   Trait obligations were being solved before literal defaulting, so `needs_ord(1)` saw
   an unsolved variable and passed. Operand checks ran before defaulting, so
   `1.0 & 2.0` was accepted. Bounds were collected *after* a signature's return type was
   converted, so `-> Option[T::Item]` could not find the trait declaring `Item`. Each
   was a one-line move and each had been silently wrong.

4. **The specification was right about `-128i8` and I was wrong.** I wrote a conformance
   case asserting the magnitude fits, because that is what Rust does. Spec §01 says a
   literal must fit its type before negation applies, and directs the reader to
   `i8::MIN`. Following the specification meant implementing associated constants on the
   primitives — which §07 had already promised in a worked example, and which nothing
   had implemented. The rule that a spec disagreement is resolved by reading the spec,
   not by adjusting the test, paid for itself here.

5. **A test caught a diagnostic that contradicted itself.** `1.to_str()` on an
   uninferred receiver produced "no method `to_str` on `_`" followed by the note
   "trait `Show` declares `to_str`, and `_` implements it". Both halves were generated
   correctly by code that had never been asked to agree with itself. It is now an
   `E0309` that says the type is not known yet.

6. **Writing 243 conformance cases found more bugs than writing the checker did.** The
   corpus is not documentation of what works; it is the thing that decides what works.
   Nine defects came out of it, including `continue` outside a loop being accepted —
   the resolver checked `break` and nobody had noticed the other half.

## Verification

```
$ ./check
==> gofmt
    ok   gofmt
==> go vet
==> go build
==> go test        (714 tests, 17 packages)
elapsed:  2s (budget 300s)
peak rss: 164 MiB (budget 3072 MiB)
PASS
```

## Gate

Phase 2 is complete and Phase 3 may begin. The next action is recorded in `CLAUDE.md`.
