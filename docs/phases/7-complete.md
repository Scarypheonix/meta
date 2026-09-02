# Phase 7 — Complete

**Exit criteria:** the user delegated Phase 7's scope, so its exit criteria are the ones
this phase set for itself: Origin gets the standard library a program cannot be written
without — collections, a hash three engines agree on, strings, and files — each specified
before it was built, and every program in `docs/spec/13-collections.md`, `14-strings.md`
and `15-files.md`'s worked-examples tables runs identically on the interpreter, the virtual
machine and native code, at every optimization level.

**Status:** met. `./check` passes in 42s at 198 MiB, against budgets of 300s and 3072 MiB.
33 packages report `ok`; 44,994 lines of Go including tests; 78 end-to-end cases × 7
engine/level combinations, 267 conformance cases, 31 ADRs, 17 specification documents, and
a 908-line prelude — which is now the largest body of Origin in the project, and the point.

The phase's own measure is `tests/e2e/cases/word_frequency.origin` (spec/10-examples.md
#17): a program that reads a file, counts words in a `Map`, sorts the result with a routine
written in Origin, and reports it with string interpolation. Nothing in it is a feature
demonstration, and every part of it needed something the language did not have when the
phase began.

## What was built

**`docs/spec/13-collections.md` and ADR-0028** — collections are a library, not syntax.
`[]` is type application (ADR-0013) and there is no index operator, so `xs.get(i)` returns
`Option[T]` and `xs.at(i)` traps. The compiler provides `Array[T]`: a run of elements with
a length and a capacity, where a slot exists only once something has been pushed to it,
which is what lets a growable structure hold no value that nothing wrote (ADR-0007). `List`
and `Map` are Origin source over it. The specified hash is 64-bit FNV-1a over an encoding
the specification fixes, because `hash::of` returns a number a program can print.

**`docs/spec/14-strings.md`** — `String` could be written, compared, ordered and printed,
and could not be asked how long it was. Six operations the compiler provides, because their
bodies read or allocate raw bytes, and a `Str` trait whose default method bodies are Origin
source over exactly those six. Byte indices and characters stay visibly distinct; `slice`
and `char_at` TRAP on an index that would split a character rather than producing a
`String` that is not valid UTF-8, because `==`, ordering, hashing and printing all assume
it is.

**String interpolation** — `"hello, \(name)!"`, desugared in the parser into
`"hello, ".concat(name.to_str())`. That is the whole definition: it adds no node to the
syntax tree, no rule to the checker and nothing at all to the three engines. No formatting
trait was needed, because `Show` already renders a value and `Str::concat` already joins
two strings. The escape is where it lives because `\(` was a *rejected* escape before this
existed, so no literal that used to be valid changes meaning, and there is no `{{` doubling
rule to remember.

**`docs/spec/15-files.md` and ADR-0030** — whole files, and no handle. A handle would be a
resource Origin has no way to release automatically (ADR-0008: no destructors, no `defer`),
so `close` would be a call a program could forget, and forgetting it leaks a descriptor.
Letting the collector close it is worse: "sometimes, later" is not a schedule. A read
splits into a status and a `taken_text`, which is ADR-0025's split unchanged — the bytes
wait in the running thread's own TCB slot, the one `chan::taken_value` uses and the
collector already scans.

**`?` on `Option`** — no shared "try" abstraction was needed, only a second arm in the one
rule. The two forms do not mix, because there is nothing `?` could build out of an `Option`
to satisfy a `Result`.

**Default trait methods, and impls on primitive types** (ADR-0029) — both were in the
specification and neither ran. Fixing them is what made `impl Str for String` possible, and
therefore what made the whole string surface Origin source rather than compiler
special-cases.

## The decision that shaped this phase

**ADR-0028: collections are a library, not syntax.** Every later choice followed its shape.
`Str` is a trait over four compiler-provided operations because `Array` was a type over
seven; `read_to_string` is Origin over four system-call operations for the same reason.
What the compiler provides is always the part whose body needs something Origin cannot say
— raw bytes, a syscall, a layout — and never the part a programmer reads.

The test of that is whether the two halves are distinguishable from outside. `slice` is
machine code and `split` is twenty lines of Origin above it; `array::push` is machine code
and `List::push` is Origin; `fs::read_file` is three system calls and `read_to_string` is
six lines. A program cannot tell which is which, and neither can a diagnostic.

## What surprised me

**Six bugs, and only one of them was Phase 7's.** Writing the tests for a new feature is
how the old ones surfaced, every time.

1. **A string literal is not in the heap, and the collector tried to move it.**
   `backend.go`'s `stringConst` puts a literal in read-only data as a complete `String`
   object, so a literal is a pointer rather than an allocation. `rt_evacuate` assumed every
   reference it met was in the semispace being collected out of: it copied the object and
   overwrote its header with a forwarding pointer — a write to a page mapped read-only,
   which is a SIGSEGV rather than a wrong answer. It needs no string operation to
   reproduce: a struct with a `String` field held live across enough allocations does it,
   and it has been there since Phase 5.

2. **A `-O2` inliner bug that swapped a merge block's two values.** A φ's operands are
   positional — operand *i* is the value arriving along `Preds[i]` — and the cloner copied
   a callee's φs with their operands in the *callee's* predecessor order, then rebuilt the
   edges by walking the callee's blocks and appending. Those are different orders, and the
   two lists hold the same edges either way, so nothing that counted them noticed. `-O0`
   and `-O1` printed the right answer and `-O2` printed the other arm's. `ir.CheckPhis` now
   runs after every pass, which also found a latent index bug in unreachable-block removal.

3. **`?` returned an `Err` of the wrong instantiation.** `Result[i64, E]::Err` and
   `Result[String, E]::Err` are different types with different layouts (ADR-0019) even
   though both carry one `E`. The early return handed back the value it was given, so a
   caller's `match` saw a variant tag from another instantiation and matched no arm. The VM
   and native code both got it wrong; the interpreter, which has no layouts, got it right —
   which is the shape of every divergence this project's differential suite exists for.

4. **`match` on an integer literal could not be compiled to native code at all.** An arm
   test is a comparison the programmer never wrote, and `internal/compile` emitted it with
   no operand kind — the one thing a comparison must carry (ADR-0021). It went unnoticed
   because the suite's literal patterns all matched enum variants, which lower to
   `is_variant` rather than a comparison, and because at `-O1` and above a match on a
   constant scrutinee folds away before the backend sees it.

5. **`checked_*` and `saturating_*` were unimplemented in native code entirely**, and in no
   deferred list. §06's `Int` promised nine methods; native code had three.

6. **The VM materialized a whole heap object as a Go string for every string operation.**
   `byte_at` in a loop was therefore quadratic, and validating a 128 KiB file took longer
   than the entire end-to-end suite. Native code had always read one word and shifted; the
   interpreter never needed to, since its `String` is a Go string. Only the VM was in
   between, and only a file large enough to matter revealed it.

**The prelude stopped being free.** `internal/mono` compiled every function with no generic
parameters whether or not anything called it, which cost nothing while the prelude's
non-generic items were a handful. `impl Str for String` was the first non-generic impl, and
suddenly every program carried six methods it never called — which renamed every binary's
DWARF compile unit `<prelude>`, added 1546 lines to the golden files, and broke a
constant-folding test that counts arithmetic across a whole program. The prelude is a
library, not a program: its declarations are no longer roots, and `internal/check` records
a non-generic call site so that monomorphization can walk from `main`.

**Writing the library in Origin found the language's own rough edges faster than writing
tests for it did.** `Str::repeat` appending one piece at a time was quadratic because
`concat` copies both operands — obvious in retrospect, invisible until a 128 KiB string
took a minute. `split` testing for its separator by slicing TRAPPED on any non-ASCII input,
because a slice whose end falls inside a character is exactly what §14 forbids. Both are
bugs in Origin code, found by running Origin code, which is what Phase 9 is going to be
made of.

**The interpreter needed types after all.** It had been "the engine that runs without
them", and its own package comment listed the two places it approximates. Method dispatch
was a third it had not noticed: `impl Loud for i64` and `impl Loud for u8` are one Go
`int64` at run time, and a trait's default method body is declared under no type's name at
all. It resolves through `mono.Result` now, the same table the bytecode compiler and the
backend read (ADR-0029) — so one dispatch question has one answer instead of three.

## What was deferred

Recorded in `docs/deferred.md`, each with a phase:

- **Float formatting in `to_str`**, still. The specification does not fix a rendering, and
  native code has no implementation; a real one is a full shortest-round-trip decimal
  algorithm, and doing it without a spec to build against would be inventing the answer.
- **Directory listing, metadata, rename, delete, the current directory, streaming**, and a
  `Path` type. §15 scoped a compiler's needs; each of these is additive, with the same
  status-to-`IoError` shape, and none is needed before something asks for it.
- **Error conversion in `?` via `Into`**, moved to Phase 8: it needs a blanket-impl story
  that interacts with coherence (ADR-0011).
- **A decision-tree lowering for `match`**, which is still a linear chain of arm tests.
- **Trait objects, multi-parameter traits, operator overloading, range patterns, tuple
  element access, labelled break** — every one of them still absent, and every one of them
  still recorded rather than forgotten.

## What to read first, next time

`docs/adr/0029-one-dispatch-table-for-three-engines.md` before touching method resolution
in any engine, and `docs/adr/0030-files-without-handles.md` before adding anything to
`std::fs`.

The thing most likely to matter later is the shape of the six bugs above: five of the six
were found by *writing a library in the language* rather than by testing the compiler, and
four of them were invisible to a differential suite that had no case exercising them. The
suite is only as good as its corpus, and the corpus grows by writing programs. Phase 9 will
be one very large program.
