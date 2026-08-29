# Phase 3 — Complete

**Exit criteria:** the GC survives a stress test allocating 10 million short-lived
objects with a live set held across collections — no leaks, no premature collection, no
crashes under randomized allocation patterns; the interpreter and the VM produce
identical output on every Phase 1 and 2 test.

**Status:** met, and both halves are enforced as assertions rather than observations.

- **`TestTenMillionShortLivedObjects`** runs ten million allocations through a
  deliberately small heap: 4,882 minor and 3 major collections in 0.08 s. The live set is
  *verified*, not merely kept — every object carries a value that is read back at the
  end — and two full collections afterwards bring the heap down to 2,320 words, the live
  set and nothing else. That last assertion is the leak check.
- **`TestEnginesAgree`** runs the whole end-to-end corpus through both engines and
  compares stdout, stderr and exit status byte for byte, separately from the comparison
  against the expected files. A case whose expectations were wrong would still report
  "the engines disagree" rather than two identical-looking failures.

`./check` passes in 2 seconds at 253 MiB, against budgets of 300 s and 3072 MiB.
845 tests across 23 packages; 16,539 lines of implementation.

## What was built

**`internal/layout`** — the representation the collector and, from Phase 5, the code
generator must agree on. The object header packs a forwarding bit, a payload size and a
type id into one word; descriptors say what each payload word is. Process rule 5 requires
this to live in one module, because a heap where the two sides disagree about which word
is a pointer corrupts silently.

**`internal/gc`** — Cheney copying, generationally. Allocation is a pointer bump in the
nursery; a minor collection copies survivors into the old generation and leaves the
nursery empty; a major collection flips the old generation between two semispaces.
Old-to-young references are found through a card table written by a barrier on every
reference store into an old object.

**`internal/bytecode`, `internal/compile`, `internal/vm`** — a stack bytecode, a compiler
from the checked AST, and a VM that executes it against the real heap. A stack machine
was chosen because Phase 4 lowers to an SSA IR anyway: solving register allocation twice,
once on a representation not designed for it, would be work thrown away.

Every value on the VM's stack carries a tag, so root scanning is precise — the collector
is handed the address of each reference slot and rewrites it in place. Nothing is scanned
conservatively and nothing is inferred from bit patterns.

## The decision that shaped this phase

Origin does not monomorphize yet (ADR-0010, deferred to Phase 4), so **a generic field
has no statically known shape**: the payload of `Option[T]` is a reference when `T` is
`String` and a raw word when it is `i64`. Three answers were possible — box everything,
defer generics in the VM, or write the shape down at run time. The third is what
`layout.Tagged` is: two words per slot, a tag beside each value.

It keeps the collector precise (the tag is written by the mutator, not guessed), it costs
two words per field, and it disappears in Phase 4 when monomorphization gives every
instantiation its own exact descriptor. Recording it in `docs/deferred.md` rather than
leaving it as a quiet inefficiency is the difference between a staging decision and a
mistake.

## What surprised me

1. **The first GC property test was the buggy one, not the GC.** It cached references
   across an allocation that triggered a collection; the collector rewrote the roots and
   the cached copies became addresses of nothing. That is the mutator's contract with a
   moving collector, and the test now models the discipline instead of violating it —
   which mattered, because the VM had to obey exactly the same rule.

2. **The same bug then appeared in the VM, in the one place I had not thought about.** A
   builtin popped its arguments into a Go slice and then allocated. The arguments were no
   longer on the stack, so the collector could not see them, so their addresses went
   stale. The fix was a `temps` list that root scanning also visits. Writing the property
   test first is what made this one recognisable on sight.

3. **Evacuation could fail halfway through a Cheney scan.** The old generation filling up
   mid-copy leaves half the heap forwarded and half not, and `Alloc` did not notice — it
   just carried on into a corrupted heap. A minor collection now checks up front that the
   old generation can hold the *entire* nursery and upgrades itself to a major collection
   when it cannot. The old failure path is a panic now: reaching it means the check is
   wrong.

4. **That check turned a tuning knob into an invariant.** "An old semispace is at least as
   large as the nursery" is not a preference; violating it makes minor collection
   impossible. `New` corrects a configuration that breaks it rather than letting it fail
   later as a mysterious exhaustion.

5. **The whole end-to-end corpus passed on the VM the first time it ran.** That is not a
   claim about the code being right; it is a claim about the expectations being precise.
   Twelve cases pinning exact stdout, exact stderr and exact exit status left the compiler
   and the VM almost nowhere to be subtly wrong, and the two bugs that did exist —
   both in the collector — had already been found by the property tests.

## What was deferred

Five entries in `docs/deferred.md`, all against Phase 4, and all consequences of not
having an IR yet: tagged slots, per-width integer overflow, `u64::MAX` at run time,
`for` loops in compiled code, and decision-tree lowering for `match` (the compiler emits
a linear chain of arm tests, which is observably correct — arms in order, guards after
their pattern matches — but tests some subexpressions twice).

## Gate

Phase 3 is complete and Phase 4 may begin. The next action is recorded in `CLAUDE.md`.
