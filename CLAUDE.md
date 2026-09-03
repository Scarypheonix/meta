# Origin — project instructions

Origin is a statically typed, garbage-collected language and its complete toolchain,
built from nothing until the compiler compiles itself. This file is the source of truth
for how to work in this repository. The project origin prompt is superseded by it.

**Phases 0 through 8 are complete** (see `docs/phases/`). Phase 9 has not started, and
its scope is the user's to set.

---

## Commands

```bash
./check              # THE gate: gofmt + go vet + go build + full test suite
go run ./cmd/originc run  path.origin     # run a file, or a package directory
go run ./cmd/originc run --vm path.origin # run it on the bytecode VM instead
go run ./cmd/originc dump-bytecode p.origin
go run ./cmd/originc check path.origin    # diagnostics only
go run ./cmd/originc dump-ast path.origin # print the syntax tree
go test -run xxx -fuzz FuzzParse ./tests/fuzz/   # fuzz the parser
./check fmt          # formatting only
./check test         # tests only
gofmt -w cmd internal tests
UPDATE_GOLDEN=1 go test ./...   # rewrite golden files (never hand-edit one)
go run ./cmd/originc version
```

`./check` must exit 0 before any commit, and before any phase is declared complete. It
enforces the 5-minute suite ceiling and the 3 GB RSS ceiling and fails the run when
either is breached.

## Layout

```
cmd/originc/          stage0 compiler driver (Go)
internal/source/      file identity, byte offset -> line/column (one implementation)
internal/diag/        spans, codes, diagnostic rendering
internal/lex/         tokens
internal/ast/         the AST (sealed interface, ADR-0015) and its dumper
internal/parse/       the grammar, with multi-error recovery
internal/resolve/     name resolution, modules, visibility; owns the name side tables
internal/types/       the shared type language: terms, unification, generalization
internal/check/       the type checker: inference, exhaustiveness, traits, coherence
internal/interp/      tree-walking interpreter (Phase 1)
internal/arith/       one definition of integer arithmetic per declared width (ADR-0021)
internal/layout/      object layout and headers: the GC/backend shared contract
internal/gc/          precise generational moving collector
internal/bytecode/    the stack instruction set and its disassembler
internal/ir/          the SSA intermediate representation, built from bytecode (ADR-0016)
internal/opt/         the optimizer: folding, CSE, LICM, escape analysis, inlining, DCE
internal/mono/        monomorphization of call dispatch (ADR-0010)
internal/compile/     AST -> bytecode, per monomorphized instance
internal/vm/          the bytecode virtual machine
internal/prelude/     the standard library, written in Origin: Option and Result and their
                      methods, Ordering, Cell, the concurrency handles, List, Map, the Str
                      trait, IoError and files, and the shortest-decimal float rendering
internal/x86/         a hand-written encoder for the instructions the backend emits
internal/dwarf/       the DWARF4 line table and its compile-unit DIE (ADR-0023)
internal/codesign/    the ad-hoc Mach-O code signature, without which macOS will not
                      run an executable at all (ADR-0024)
internal/obj/         ELF and Mach-O executable writers; no linker (ADR-0017)
internal/backend/     SSA IR -> machine code, register allocation, the native runtime
internal/driver/      pass ordering and error suppression
tests/conformance/    type-system accept/reject corpus, one file per case
tests/e2e/            programs + exact expected stdout/stderr/exit
tests/docs/           documentation invariants (ADR numbering, code registry, lints)
tests/selfhost/       stage1 against the Go compiler it replaces, over this repo's own
                      Origin source
tests/floats/         the float rendering against Go's strconv, over 14,000 bit patterns
tests/debuginfo/      lldb/llvm-dwarfdump on both formats; skips if they are absent
tests/fuzz/           fuzz targets for the lexer and parser
docs/spec/            THE language specification — normative; §13 collections, §14
                      strings, §15 files were added in Phase 7, §16 floats in Phase 8
docs/adr/             architecture decision records — every irreversible choice
docs/phases/          N-complete.md, written at each phase gate
docs/deferred.md      everything deliberately left out, each tagged with a phase
stage1/src/           from Phase 9: the compiler for Origin, written in Origin
bootstrap/            from Phase 9: the last known-good stage1 binary
site/                 pre-existing static website; unrelated to Origin (ADR-0002)
```

## Hard environment constraints

The target machine is a 2017 MacBook Air: Broadwell i5-5350U, 2 cores / 4 threads, 8 GB
RAM, macOS Monterey 12.7.6, x86-64. Development happens in a Linux x86-64 container.

- **No LLVM, no Cranelift, no libgccjit, no C backend.** Machine code bytes are emitted
  directly and object files are written by hand. This is the point of the project.
- Host language is Go (ADR-0001). The decision is closed.
- Incremental build < 60s. A package that exceeds it is too large — split it.
- Full test suite < 5 minutes. When it crosses, stop feature work and fix it.
- Peak RSS < 3 GB during build or test.
- Target is native x86-64. There is no cross-compilation layer. Two object-file
  writers (Mach-O for shipping, ELF for container-side verification) share one
  instruction stream — ADR-0003 explains why that is not cross-compilation.

## Process rules

1. **Specification before implementation.** No code for a subsystem until its spec
   exists in `docs/spec/` with syntax, semantics, error conditions, and worked examples
   with expected output. Ambiguity discovered while coding means the spec is wrong —
   fix the spec first, in the same commit.
2. **Tests are ground truth, not reasoning.** Every language feature lands with unit
   tests, a snapshot test of the generated IR/assembly, and an end-to-end test asserting
   exact stdout and exit code. A feature without an end-to-end test does not exist.
3. **Differential testing.** Lexer/parser: fuzz, assert structured errors and no panics.
   Type checker: `tests/conformance/` verdicts. Codegen: generate the equivalent C,
   compile with clang, run both, assert identical output — a divergence is a codegen bug
   and is never suppressed. GC: property tests over random object graphs.
4. **One phase at a time, hard gates.** Phase N+1 does not start until every exit
   criterion of phase N passes and `docs/phases/N-complete.md` records what was built,
   what was deferred, and what surprised you.
5. **Module boundaries are contracts.** Narrow documented interfaces; no reaching into
   another subsystem's internals. When two subsystems must agree on a representation,
   that agreement lives in **one** shared module with its own tests — never duplicated.
   Current shared-agreement modules: `internal/layout` owns object layout and headers
   for the GC and, from Phase 5, the backend; stack maps and safepoint placement join
   it there (spec §08), and from Phase 7 the longest run of bytes a `String` can hold
   (§15). `internal/compile` owns the builtin indices and the file-operation statuses
   every engine reads; `internal/obj` owns the per-target syscall numbers.
6. **ADRs for irreversible choices.** Numbered file in `docs/adr/`: context, options
   considered, decision, consequences. If you cannot remember why something is the way
   it is, the ADR was missing — write it retroactively and say so in the file.
7. **Stop and ask on semantics.** Language design decisions belong to the user;
   implementation decisions belong to you. The user delegated the Phase 0 design
   questions (`docs/design-questions.md`); every answer taken under that delegation is
   an ADR, and any of them can be overturned by reading one file.
8. **No stubs that lie.** A function returning a plausible wrong answer is worse than
   one that stops. Unimplemented paths `panic("unimplemented: <what>")`. A test that
   cannot yet run `t.Skip`s with the phase named — it never passes silently.
9. **Never break the bootstrap.** From Phase 9, the last known-good stage1 binary is
   committed in `bootstrap/`. If a change breaks self-compilation, revert to green
   before doing anything else.

## Language invariants

These are load-bearing across the whole compiler. Changing one means changing a spec
document, an ADR, and probably a phase's worth of code.

- **No undefined behaviour.** Every operation produces a defined value or traps.
- **Optimization is unobservable.** Identical stdout and exit code at `-O0`, `-O1`,
  `-O2`. This is why evaluation order is fully specified and why overflow traps at every
  level (ADR-0005, ADR-0012).
- **No null.** `Option[T]`. No zero values, no uninitialized bindings (ADR-0007).
- **Errors are values.** `Result` + `?`. No exceptions, no unwinding (ADR-0006).
- **Immutable by default.** `mut` on bindings for reassignment, `mut` on fields for
  mutation; no borrow checker (ADR-0004).
- **Primitives unboxed, aggregates heap-allocated by reference** (ADR-0008). This is
  what makes a precise moving GC tractable and what escape analysis exists to claw back.
- **Generics are monomorphized** (ADR-0010), so the backend never sees a type variable.
- **`[]` is type application; there is no index operator** (ADR-0013), so the lexer
  never needs parser feedback.
- **No data races in safe Origin**: channel sends require `Send`, derived from the
  absence of `mut` fields; `Mutex` is the only shared mutable thing (ADR-0014).

## Conventions

- Go: standard `gofmt`; packages under `internal/` are named for what they own, not for
  what they contain. AST is a sealed interface with one struct per node kind and
  semantic results in side tables keyed by node id (ADR-0015).
- Origin: types/traits/variants `UpperCamelCase`; everything else `lower_snake_case`;
  constants `SCREAMING_SNAKE_CASE`.
- Diagnostics: every one has a span and a registered code from `docs/spec/codes.md`;
  none contains an internal identifier. `tests/docs` enforces both.
- Golden files change only via `UPDATE_GOLDEN=1`, never by hand.
- Commit messages: imperative mood, phase-tagged, e.g. `phase1: lex integer literals`.

## Context management

This project outlasts any single context window.

- Update this file at the end of every working session: current phase, what is in
  flight, what is known-broken, the next action.
- Never rely on remembering a decision. Look it up in `docs/adr/`. If it is not written
  down, it is undecided — write the ADR.
- Pattern-matching to a familiar design instead of reading the spec is the failure mode
  to watch for. When you notice it, stop and read the spec.
- An invariant spread across more than three files is a design smell. Refactor so it
  lives in one place with one test suite guarding it.

## Status

**Phase 8 is complete** (`docs/phases/8-complete.md`). Two halves. The first closed the
three places `docs/spec/` and the implementation had drifted apart since Phase 5:
arithmetic happens at the width the operand type declares (`internal/arith`, one definition
the three engines share), `u64` has a run-time representation, and a float renders as the
shortest decimal that reads back as the same value — the last `unimplemented:` in the
project, written in **Origin, in the prelude** (**ADR-0031**, `docs/spec/16-floats.md`)
because a shortest-round-trip conversion needs exact arithmetic wider than sixty-four bits
and the alternative was hand-encoding Dragon4 in machine code.

The second half was a method rather than a list, and it is the thing to carry forward:
**seven real programs, ~1,400 lines of Origin, run on all three engines at every
optimization level.** They found nine more holes, none of which any existing test caught.
Seven of the nine were *accepted syntax that nothing verified* — an inline bound on an
impl, a bound on an associated type, a lambda assigning what it captured, a qualified
variant path, a struct literal in condition position. That failure mode has a tell: a field
on an AST node that no other package reads. The other two were a register-allocator bug
live since Phase 5 (two φs of one block could share a register, visible only at `-O0`) and
`panic` from the prelude naming the prelude in native code.

Read `docs/phases/8-complete.md` before starting Phase 9, and before touching the register
allocator or anything that decides how a value is printed.

**Phase 7 is complete** (`docs/phases/7-complete.md`). Origin has the standard library a
program cannot be written without: `List` and `Map` over one compiler-provided `Array[T]`
(ADR-0028), a hash the three engines agree on to the bit, a `String` with a real surface,
string interpolation, and whole-file reading and writing with no handle (ADR-0030). `?`
applies to `Option` as well as `Result`. The prelude is 908 lines of Origin and is now the
largest body of Origin in the project.

Read `docs/phases/7-complete.md` before adding anything to the prelude or to `std::`. The
thing most likely to matter later is the shape of the six bugs recorded there: five of the
six were found by *writing a library in Origin* rather than by testing the compiler, and
four were invisible to a differential suite that had no case exercising them. Two are worth
knowing before touching their subsystems — **ADR-0029** (every engine resolves a method
through monomorphization; the interpreter is not "the engine without types") and the
collector's sixth root-set hole, a string literal in read-only data that `rt_evacuate` tried
to move.

**Phase 6 is complete** (`docs/phases/6-complete.md`). Origin has green threads, channels
and a mutex; `Send` is derived structurally by the checker rather than promised by a
document; and every program in §12's worked-examples table runs byte-identically on the
interpreter, the virtual machine and native code at every optimization level, trap messages
and their spans included. `concurrencyCases` is empty, the way `nativeSkips` emptied in
Phase 5.

Read `docs/phases/6-complete.md` before touching the scheduler or the collector's root walk.
The thing most likely to matter later is the list of five root-set holes recorded there:
every one was invisible until a collection actually moved objects, and two of them predate
the phase entirely. `internal/backend/collect_test.go` is where that lesson lives — it
shrinks `heapSize` around a single build so a real collection is something a five-minute
suite can reach, and every one of those bugs has a test there that fails on the code it
replaced.

**Phase 5 is complete** (`docs/phases/5-complete.md`). `originc build` produces native
x86-64 executables for Linux and macOS with no linker and no libc; the differential suite
agrees byte for byte across the interpreter, the VM and native code at every optimization
level, including exit status; `nativeSkips` is empty. On the target machine, a Mach-O
compiled by `originc` running there executes unaided and `lldb` breaks on an Origin source
line with `bt` naming the frames.

Read `docs/phases/5-complete.md` before touching the backend. The two things most likely
to matter later: **ADR-0024** (macOS kills unsigned executables, so the compiler signs its
own output — there is no linker to do it), and the lesson recorded there about "verified
structurally", which had concealed the fact that every Mach-O the project produced was
unrunnable.

**Known-broken / deferred**, all recorded in `docs/deferred.md` with a phase: `match`
compiles to a linear chain of arm tests; `%` on floats works on the interpreter and the VM
and fails to build natively (SSE has no remainder instruction — Phase 9); a struct or enum
declared inside a function body fails loudly in `internal/compile` rather than being
checked; there are no associated functions, so a constructor is a free function in a `std::`
module (`list::new`, `sync::mutex`); a function used both as a direct callee and as an
escaping value loses its direct-call fast path for every use (ADR-0020); native collection
is single-space, non-generational, with no write barrier (ADR-0022); DWARF is a line table
and a symbol table only, so `frame variable` does not work (ADR-0023); no engine runs
threads in parallel; the native runtime never reclaims what it maps for a channel, a mutex
or a finished thread's stack; `std::fs` has no directory listing, metadata, rename, delete
or streaming, and there is no `Path` type (ADR-0030); and error conversion in `?` via `Into`
needs a blanket-impl story (`map_err` now exists, so the explicit form is real).

**Next action:** Phase 9's scope is not decided, and rule 7 puts that choice with the user.
The project's own arc points at **self-hosting**: a compiler for Origin, written in Origin,
which is what every phase so far has been building toward and what `bootstrap/` is reserved
for. Multi-file packages, recursive enums, maps, strings, files, `?` and now `Option`/
`Result` methods all work and are covered by end-to-end cases; what a self-hosting compiler
would still miss is recorded above. The cheapest next thing that is not that: keep writing
Origin. Nine of nine bugs this phase came from running programs, none from reading Go.
Nothing is in flight.

**A note on the history:** the VM's concurrency runtime landed inside commit `6b073de`,
whose message describes only ADR-0027 — the bug the VM work uncovered. The commit is
accurate about the bug and silent about the 500 lines of `internal/vm/concurrent.go`
beside it. Read that commit for both.
