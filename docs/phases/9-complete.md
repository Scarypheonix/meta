# Phase 9 — Complete

**Exit criteria:** rule 7 put the scope with the user, and the project's own arc pointed at
the one thing every phase so far had been building toward: **self-hosting**. A compiler for
Origin, written in Origin, that compiles itself.

**Status:** met. The compiler compiles itself, and the result is a fixed point:

```
originc build -O1 stage1/src            -> 4,541,555 bytes
that binary, over the same source       -> the same 4,541,555 bytes
and again                               -> the same bytes
```

`bootstrap/stage1-linux-amd64` is that binary. `./check` passes in 265s at 1,644 MiB,
against budgets of 300s and 3,072 MiB. 102 end-to-end cases, 280 conformance cases, 32 ADRs,
19 specification documents, 53,192 lines of Go, a 1,821-line prelude, and **34,035 lines of
Origin in `stage1/src`** — which is now the largest body of Origin in existence and the
program the whole suite is pointed at.

## What was built

Thirty-six modules, one per Go package it replaces, each held to that package by a
differential over this repository's own ~430 `.origin` files:

| stage1 | replaces | held by |
| --- | --- | --- |
| `lex`, `source` | `internal/lex`, `internal/source` | the token stream, the position mapping |
| `ast`, `parse` | `internal/ast`, `internal/parse` | the dumped tree; where syntax errors are reported |
| `resolve` | `internal/resolve` | 397 packages, 744,896 trace lines |
| `types`, `check` | `internal/types`, `internal/check` | 399 packages, 1,647,768 trace lines |
| `mono` | `internal/mono` | the instantiation set, and which copy each call site reaches |
| `layout`, `bytecode`, `compile` | the same three | the bytecode, instruction for instruction |
| `ir`, `irbuild`, `dom` | `internal/ir` | the SSA, at all three levels |
| `arith`, `opt` | `internal/arith`, `internal/opt` | the optimized bytecode, re-emitted |
| `x86` | `internal/x86` | 5,118 bytes of encoded instructions |
| `obj`, `dwarf`, `codesign`, `sha256` | `internal/obj`, `internal/dwarf`, `internal/codesign` | eight executables in two formats; 137 digests against Go's `crypto/sha256` |
| `emitter`, `runtime`, `array`, `sched`, `thread`, `equal`, `hash`, `strings`, `process`, `chan`, `mutex`, `files`, `collect`, `spans`, `stackmap`, `kinds`, `closures`, `regalloc`, `lower`, `float`, `backend` | `internal/backend` | **the executable itself** |

## The one thing that had to be invented

**The trace oracle**, and only for the front end. Side tables keyed by node id cannot be
compared against a tree that has no node ids, so both compilers emit one line per event, in
order, and the *sequence* is what is compared. A line's position identifies the node; what it
says identifies what the node became, naming a declaration by the `<file>:<offset>` of its own
name, so that two things called `x` are the same only if they were written in the same place.

Two of the three differ from the first in ways worth knowing before writing a fourth. The
checker's entries are rendered at the **end** of the run, not when they are made, because a
type recorded mid-body is usually an unsolved variable that later unification binds and
end-of-body defaulting resolves — printing at record time compares the checker's intermediate
state instead of its answer. The monomorphizer's names a copy by the **number** it was created
at rather than by its name, because a name is not an identity: two declarations in different
modules may share one, and what the trace has to pin is that two call sites reaching the same
copy say the same thing.

It stops being needed at the bytecode. From there on the artefact itself is the oracle, and
each one subsumes the last: bytecode, SSA, re-emitted bytecode, encoded instructions, written
executable. **Two files that are byte-identical are the same program.**

## What it found

Every bug in this phase came from *running Origin*, not from reading Go — the same tell Phase
8 recorded. The ones worth carrying forward:

**A lost root, and a lesson about tools.** The φ that stands in for an inlined call took its
kind from its first operand, and after inlining that operand can be a *placeholder*: a `return`
that carries nothing — the arm of the callee that diverges — gets a synthetic `OpUnit`, because
a φ needs an operand for every predecessor whether or not that predecessor can arrive. Letting
that unit answer made a `Block` reference raw to the stack map: spilled to a raw slot, never
updated when a collection moved the object. Only at `-O2`, only through inlining, only when a
collection landed while the φ was live — which is why the heap window was so narrow, correct at
48 MiB, wrong at 64, correct at 80.

Three days of reading the code got the characterization wrong twice; three tools got it in
twenty minutes, and all three are still in the tree:

- `debugCollectEvery` forces a collection every N allocations. A lost root is a coincidence
  between one collection and one moment; collecting constantly removes the coincidence.
- `debugStaleRefs` makes the array primitives check that the reference they were handed is
  inside the semispace being allocated out of. It turned `index out of range`, seven thousand
  lines into a dump, into `stale reference (forwarded) in rt_array_push`.
- `opt.DebugSkip` and `opt.DebugInlineLimit` bisect the optimizer, down to `inline 1843`.

Two smaller lessons: a sweep that printed `FAIL` without separating `index out of range` from
an honest `out of memory` produced a wrong bisection, so **a bisection is only as good as its
predicate**; and both wrong characterizations came from reasoning about which pass *could* be
at fault instead of asking the program.

**A register-allocator bug four phases old, found by writing an encoder.** Liveness counted a
parameter and a capture as *definitions* of the block they appear in. They are not: a parameter
arrives with the frame. That is wrong exactly when the entry block is also a loop header —
`while c.len() < n { c.push(0); }` is that shape — and the allocator then hands a live
parameter's register away. Native only, `-O0` and `-O1` only, because at `-O2` inlining
reshapes the function and it comes out right by accident. Nothing in twenty thousand lines of
Origin had happened to write a loop whose condition is a function's first statement until an
encoder needed to pad to an alignment boundary.

**The virtual machine was eight times slower than the tree-walking interpreter.** `str::slice`
read the whole String and then took the range out of the copy, so a slice cost the *source's*
length rather than the result's — quadratic in a lexer, which cuts one token at a time out of a
whole file. Invisible wherever strings are short. The regression test asserts the complexity
rather than a deadline, because only a ratio can state it: the same 4,096 slices out of a 1 KiB
string and out of a 128 KiB one should cost the same, and the code it replaces makes the second
a hundred times dearer.

**Three span bugs, all found by the executable differential and by nothing below it.** The
inliner treated an empty callee span as invalid where the Go compiler treats it as valid; a
function's recorded span began at its name rather than at `fn`; a `match` arm's began at its
body rather than at its pattern. The bytecode dump prints an instruction's operands and not
where it came from, so all three were invisible to every differential below the executable —
and all three are visible in the DWARF line table's column, which is the only artefact in the
project that renders a span. **When the oracle has a hole, the thing it is checking inherits
it**, recorded for `ast.Dump` in this same phase and true one level down as well.

**A constant's file has to travel with its value.** An instruction records a span as a byte
offset and a file name separately, and the lowering stamped every instruction with the *unit
being compiled* — so a constant declared in one module and used in another got a valid-looking
location in the wrong file. `internal/compile` cannot have this bug: its span carries a
`*source.File`, so the pair can never come apart. It was 17,843 bytes of `.debug_line` between
the two compilers on stage1's own source, and twelve lines reproduce it.

**A defect one inlining decision from firing everywhere.** Loading two operands straight into
rdi and rsi is wrong when the allocator put the *second* one in rdi. Every multi-operand runtime
call had that shape and none could reach it, because they all arrive through a prelude method
whose parameters the prologue has already put in argument order — `%` on floats is the only one
lowered inline over two ordinary local values, and it is what found it. At `-O1` that prelude
method gets inlined and its parameters become values the allocator places freely.

**Two tuples of the same arity could not coexist in one compiled program.** A descriptor's
*name* is its identity in the layout registry, and a tuple's was its arity alone, so
`(i64, bool)` and `(bool, String)` were the same type: the second registration lost, and every
read through it interpreted the wrong words. The Go compiler's own closure descriptors had been
fixed for exactly this in Phase 5, with a comment explaining why; tuples were never given the
same treatment. It survived because nothing executed it — `tests/conformance` compiles a tuple
case only as far as the checker's verdict, and `originc run` defaults to the interpreter, which
builds no descriptor at all. Running a second compiler over the whole corpus found it on the
third file.

**Three lookups in the Go checker depended on a Go map's iteration order.** Every one is a scan
for a *name*, and every one is deterministic right up until a name is declared twice — which
the corpus does exactly once, in the entry where the prelude is checked with itself as the
prelude. Five runs of that file gave three different outputs. **The degenerate input is the
most valuable file in the corpus.**

**Dead code lies.** `allocPreludeVariant` had no callers, and translating it instead of the
live closure beside it made every `cmp` seven bytes longer. A method nothing calls is the same
tell as a field nothing reads, and Go vet does not flag it.

## What the language grew

- **The command line and the exit status** (`docs/spec/17-process.md`): `args()` over
  `env::arg_count` and `env::arg_at`, and `process::exit`, which ends the *process* and not the
  thread on all three engines. A compiler cannot be written without them. In native code the
  vector is the kernel's own: `_start` saves the address the stack came in at before aligning it
  away, because there is no libc to have parsed it.
- **Reading a decimal into a float**, the inverse of Phase 8's rendering and the other half of a
  pair a language should not have only one of.
- **A string literal may span lines**, which `docs/spec/01-lexical.md` had always said and both
  lexers had always denied — the Go lexer's own error note said so beside the condition
  contradicting it.
- **`%` on floats in native code**, the last `unimplemented:` in the project.
- **SHA-256**, in Origin, because macOS will not run an unsigned executable and a compiler that
  signs its own output needs a hash of its own.

## The suite, and what it cost

The ceiling is five minutes and this phase spent it twice. Both times the fix was the same
shape, and it is the thing to reach for the third time: **find the question being asked more
than once.**

The first was `driver.RunAt` compiling stage1 from source on every call — twenty compilations
of the same unchanged program across the differentials. The second was engine agreement, asked
once per pass: seven differentials each running stage1 on the interpreter and the virtual
machine, 125s of a 246s package, all establishing that the three engines agree. They do not
need to be asked separately. If stage1 on the interpreter produces the same artefact as stage1
in native code at a *downstream* stage, the two agreed at every stage before it — so it is
asked twice now, at `check` (downstream of lexing, parsing and resolution, and where a rejected
program produces its diagnostics) and at `dump-ir -O2` (downstream of everything after).

The same argument retired the self-source rows below the executable. `TestStage1CompilesItself`
compares the *bytes of the binary* built from stage1's own source; the tests that compared its
bytecode, its SSA and its instantiation set were asking a question that one already answers
more strongly.

What was deliberately **not** cut: corpus breadth on the native rows, and every diagnostic
comparison. A program that fails to compile has no executable, so the traces are the only
oracle those paths have.

## What is deferred

Everything in `docs/deferred.md` still tagged for a later phase, and one thing this phase
added: stage1's `build` prints its executable as hex rather than writing the file, because a
`String` is UTF-8 by construction (spec/14-strings.md) and an executable is not text. The
oracle is unaffected — two programs that print the same bytes wrote the same executable — but a
self-hosted toolchain that can run `originc build` end to end wants binary output, which wants
a byte-oriented file API that `docs/spec/15-files.md` does not have.

## Read before Phase 10

The three tools in the lost-root section, before reading any code about a miscompilation. The
span story, before adding a field to the AST: the tree grew four slots this phase
(`Expr.inst`, `Expr.iter_inst`, `FnDecl.span`, `Arm.span`, `Stmt::Let`'s own), and every one
arrived in the same commit as its reader, because a field nothing reads is the failure mode
Phase 8 kept finding. And process rule 9, which now has something to protect: `bootstrap/`
holds a binary, `tests/selfhost` rebuilds it from source and compares, and the same tests run
that binary over its own source and check that what comes out is the binary.
