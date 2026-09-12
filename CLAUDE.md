# Origin — project instructions

Origin is a statically typed, garbage-collected language and its complete toolchain,
built from nothing until the compiler compiles itself. This file is the source of truth
for how to work in this repository. The project origin prompt is superseded by it.

**Phases 0 through 15 are complete** (see `docs/phases/`). **The compiler compiles itself**,
and it runs in a browser. See Status, at the bottom of this file, for what exists and what is
next.

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
./build-web          # the playground's build output: pins the toolchain, builds the module
UPDATE_GOLDEN=1 go test ./tests/web/    # the playground's examples, and the tutorial's
                                        # code pictures and programs
```

`./check` must exit 0 before any commit, and before any phase is declared complete. It
enforces the 5-minute suite ceiling and the 3 GB RSS ceiling and fails the run when
either is breached.

## Layout

```
cmd/originc/          stage0 compiler driver (Go)
cmd/originwasm/       the browser host: the pipeline and both engines, GOOS=js GOARCH=wasm,
                      exporting one function (docs/spec/playground-runtime.md)
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
internal/stdin/       where a line of standard input ends: one answer the interpreter and
                      the VM share (spec/18-input.md)
internal/driver/      pass ordering and error suppression
tests/conformance/    type-system accept/reject corpus, one file per case
tests/e2e/            programs + exact expected stdout/stderr/exit
tests/docs/           documentation invariants (ADR numbering, code registry, lints)
tests/selfhost/       stage1 against the Go compiler it replaces, over this repo's own
                      Origin source
tests/floats/         the float rendering against Go's strconv, over 14,000 bit patterns
tests/debuginfo/      lldb/llvm-dwarfdump on both formats; skips if they are absent
tests/fuzz/           fuzz targets for the lexer and parser
tests/wasm/           the browser build against the end-to-end corpus, under Node: 98 cases
                      on both engines, byte for byte against the same golden files
tests/web/            the playground page, driven in Chromium; skips without playwright
docs/spec/            THE language specification — normative; §13 collections, §14
                      strings, §15 files were added in Phase 7, §16 floats in Phase 8,
                      §17 the process in Phase 9, §18 standard input in Phase 15
docs/adr/             architecture decision records — every irreversible choice
docs/phases/          N-complete.md, written at each phase gate
docs/deferred.md      everything deliberately left out, each tagged with a phase
stage1/src/           the compiler for Origin, written in Origin: 45 modules, roughly one
                      per Go file the backend needs and one per package elsewhere, plus
                      main.origin (its own command line). 34,700 lines, and the largest
                      body of Origin there is
bootstrap/            the last known-good stage1 binary, and what it builds from
                      stage1/src -- the same bytes (process rule 9)
web/                  the Origin playground and the tutorial: static pages that compile and
                      run Origin in the visitor's browser, with no backend of any kind.
                      `./build-web` produces its one uncommitted artefact; `vercel.json`
                      deploys it. `web/pictures/*.svg` and `web/tutorial-programs.js` are
                      generated by `tests/web` from programs that suite compiles
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

**Phase 15 is complete** (`docs/phases/15-complete.md`). **Origin can read what you type**
(**ADR-0043**, `docs/spec/18-input.md`), and the site now teaches the language rather than only
offering a playground.

```origin
let name = ask("What is your name? ")
println("Hello, \(name)!")
```

`read_line() -> Option[String]`, `input() -> String` and `ask(prompt)` are prelude Origin over
`io::read_line`/`io::taken_line`, which split exactly as `fs::read_file`/`fs::taken_text` do and
share the same held-text slot -- `io::taken_line` *is* `rt_fs_taken` in native code.
**Standard input is a stream and a file is not**, so ADR-0030's handle-free shape had to be
re-earned: a whole-input read would have been the smaller change and cannot be interactive, so the
granularity is the one a person types at. It costs **one `read` syscall per byte** natively (a
buffered read would consume past the newline and a stateless operation has nowhere to keep it) and
a **64 KiB line limit** (`layout.MaxInputLine`, the same refusal on all three engines).

**The browser is the one place ADR-0033 does not extend**: a page cannot have a filesystem but it
can have text in a box, so the playground has an input box and `tests/wasm` compares it byte for
byte against the native runs.

**Every program the site shows was rewritten in the current syntax** -- no `use std::io`, no
`fn main`, `Some`/`Ok`/`Less` unqualified, `[a, b]`, semicolons only where a line break does not
end a statement -- and `tests/selfhost` immediately reported a **Phase 14 defect**: stage1 recorded
every top-level tail expression as having a semicolon, and nothing caught it because no corpus file
had a top-level statement ending in `}` until this phase wrote one. **A differential oracle is only
as wide as its corpus, and the cheapest way to widen it is to write the programs you would show
someone.**

**The suite has no headroom left, and the next phase must cut before it adds.** `./check`
measured 192s, 205s and 211s during this phase and then **301s -- one second over the ceiling**
-- before passing again at 292s. Measured rather than re-run until green: `originc` checks all
34,700 lines of `stage1/src` in **0.40s** and builds it in **3.8s**, and the slowdown scales with
how long a package runs (floats, 26s of work: +8%; wasm, 55s: +63%; selfhost, 280s: +50%) rather
than with anything in the diff. That is a CPU quota with burst credit on this host, not a
regression -- but the honest number is the slow one. Phase 9's rule applies: look for the question
being asked more than once, *before* adding.

**The tutorial's pictures are generated, not drawn** (`tests/web/picture_test.go`): rendered from
source with the project's own lexer and golden-checked, six of nine from corpus cases and the rest
compiled by that test. The buttons' programs come from the same sources, so a picture and what it
copies cannot disagree.

**ADR-0040 gained a trigger.** `?` is a statement terminator now, so `let x = f(a)?` needs no
semicolon. It had never been measured, because nothing in the corpus ended a line with one until
this phase rewrote `result_and_try`; measured, it is free -- **26 semicolons become optional, 0
lines could continue the expression, 0 programs change meaning**, because `?` is postfix and the
character has no other use in the grammar. `let cell = Cell { value: 0 };` still needs its
semicolon and always will: `}` is settled at 805 value-returning functions. **The reusable part is
the shape**: "a rule with no evidence behind it is how the trigger set grows without anyone
deciding to grow it" is a standing instruction to go and get the evidence, not to leave the
question open. It took four minutes.

**Phase 14 is complete** (`docs/phases/14-complete.md`). Two things Phase 13 left on the floor.

**The checker defect was a reorder, not the trail it was priced as.** `lookupMethod` unified the
receiver against each candidate impl and asked whether that impl declared the method *afterwards*;
`Unify` binds with no rollback, so probing a candidate that was going to be rejected bound the
receiver anyway. Moving the `info.Methods[name]` lookup *above* the `Unify` call means a rejected
candidate never touches the receiver -- and there is nothing to roll back. `sum`, `max`, `min` and
`join` are methods again. **The lesson is about pricing**: a defect report that names a mechanism
will quote you the mechanism's price; re-derive what the fix has to *achieve* first. Still open, and
in `docs/deferred.md`: two impls that both declare the method and both unify are not reported as
ambiguous. That one does need the trail.

**`println("hi")` is a complete program** (**ADR-0042**). A file's top-level statements become the
body of a synthesized `main`; a file with both top-level statements and a declared `main` is
rejected. It is a wrapper rather than a script, because items hoist (§07) and "the file runs top to
bottom" would be false about half of it. Additive: every file in the repository declares `main`, so
nothing changed and no golden file moved. `./check` runs in 197s at 1,949 MiB.

**The audience changed the arithmetic**: teenagers, not software engineers. `fn main` was the last
piece of ceremony that was not load-bearing. The next three barriers -- **`xs[0]` (ADR-0013)**,
**mandatory signatures (ADR-0009)** and **`Option` on every index (ADR-0007)** -- are all listed
language invariants, so the next simplification is a *design* phase and needs the user in it.
`docs/phases/14-complete.md` prices all three.

**Phase 13 is complete** (`docs/phases/13-complete.md`). Origin has an iteration library:
`range`, thirteen methods on `List[T]`, and four free functions -- **all ordinary Origin in the
prelude, with no compiler change at all.** Every shape was verified expressible against the real
checker before a line was written. stage1's driver had hand-rolled its own stable insertion sort
and now calls the prelude's, 22 lines lighter. `./check` runs in 209s at 1,974 MiB, *down* from
239s: Phase 7's unreached-prelude-dropping means a generous library costs no program anything.

**Two limits found by writing it**, both now in `docs/deferred.md`. **Lazy adapters cannot be
expressed**: typing a lazy `map` needs `I::Item` to *be* the function's argument type, a bound
names a trait and its type arguments rather than a projection, and it fails with `expected A,
found I::Item` -- so every transform is eager and §13 says so. **Candidate-impl probing is not
speculative**: `lookupMethod` unifies the receiver against each candidate and checks whether
that impl has the method *afterwards*, and `Unify` binds with no rollback, so an
`impl List[i64]` beside `impl[T] List[T]` made `let w = ["a", "b"]` fail with `expected i64,
found String`. That defect predates the library -- this is just the first prelude impl on a
concrete instantiation to expose it. `sum`, `max`, `min` and `join` are free functions until it
is fixed, which needs a trail in `internal/types` mirrored in stage1: a phase, not a patch.

**The phase amended the one before it.** Using the library produced code ADR-0040 rejected --
a method chain wrapped with the dot leading -- so rule 3 now suppresses insertion before `.` as
well as `}`, on a *stronger* argument: nothing in the grammar can begin with a dot, so it trades
away nothing. **The lesson: a grammar rule and a library are not independent.** ADR-0040's
migration cost looked like 93 sites because the corpus had no fluent API in it, because the
standard library had none to use. A rule validated against today's code can be wrong about
tomorrow's.

**Next action: the user's to set** (rule 7). Still held and untouched: **associated functions**
(`List::new()` -- which would also make `sum` and `join` methods without the checker fix) and
**tuple element access** (`t.0`). Still unbuilt: the **formatter**, referenced twice in the spec
and never scheduled. And **18% of code lines are still nothing but a closing brace** -- only
indentation sensitivity changes that, and it remains priced as a front-end redesign.

**Phase 12 is complete** (`docs/phases/12-complete.md`). Semicolons are inserted at a line break
(**ADR-0040**): 14,173 of the repository's 31,771 code lines end in one, and all of them are now
optional. `./check` runs in 217s at 1,954 MiB.

**Go's rule does not transfer, and that is the finding.** Origin is expression-oriented and Go is
not, so two carve-outs are load-bearing rather than cosmetic: a block's value is its trailing
expression written *without* a semicolon, so inserting one before `}` would have made **805
value-returning functions quietly return `()`**; and `}` is not a trigger, because Origin's
dominant `match` idiom is a brace-bodied arm with the comma omitted -- **442 sites** -- where a
semicolon is not grammatical at all. With `}` in the trigger set: 6,149 insertions and 442 arms
to rewrite. With it out: **129 insertions and 93 breaks.**

**The migration wrote a bug and the corpus caught it.** The 93 wrapped expressions were migrated
mechanically; where a line ended in a trailing `//` comment the operator was appended *inside* it
and vanished, so `obj.origin`'s `header_size` and `sizeofcmds` silently lost terms and stage1's
Mach-O writer could not pad its own header. It compiled and type-checked. What made it safe was
asserting the invariant the migration claims -- *a pure operator move changes nothing but line
breaks* -- by stripping comments and whitespace from both versions and comparing; only
`lex.origin` diverges. **Write that check before running the tool, not after.**

**This does not make Origin stop looking like C**, and was not meant to. The Phase 11 audit
measured where that comes from: 18% of code lines are nothing but a closing brace. Semicolons are
character texture; brace lines are whole lines.

**Next action: the user's to set** (rule 7). Still held explicitly, and outside the Phase 12
delegation: **associated functions** (`List::new()`, 568 sites) and **tuple element access**
(`t.0`). Still open and not syntax questions at all: **`std::iter` is a normative §10 example that
does not compile**, and the **formatter** is referenced twice in the spec but neither built nor
scheduled -- the iterator gap is what the audit ranked first for changing how Origin looks, since
852 `while` loops against 28 `for..in` and 731 manual increments each cost a nesting level and a
closing brace.

**Phase 11 is complete** (`docs/phases/11-complete.md`). Three syntax simplifications, chosen
from an audit of every candidate and ranked by cost, each of which leaves every existing program
meaning exactly what it meant:

```
Option::Some(x)  ->  Some(x)              ADR-0037, the resolver and nothing else
match o { Some(n) => a, None => {} }
                 ->  if let Some(n) = o { a }    ADR-0038, a parser desugaring
list::new(); push; push  ->  [a, b]       ADR-0039, a parser desugaring
```

**No golden file moved and no existing source was rewritten**, which is what made three features
affordable at 208s of a 300s budget: both spellings of a variant resolve to the same `Ref`, and
the two new forms were syntax errors before. `./check` runs in 176s at 1,929 MiB.

Three things to carry forward. **The corpus corrected ADR-0037 within a minute of the first
run**: the rule was "every enum the prelude declares", which put `IoError::Other` in the global
scope, and `other` is the idiomatic name for a catch-all match arm — four sites here. The tell
had already been misread, since W0003 had needed scoping away from `Ord::cmp(self, other: Self)`
for the same collision. **A desugaring is invisible until a diagnostic points at it**: an
irrefutable `if let` made the synthesized `_` arm unreachable and E0006 named syntax that is not
in the source, so `ast.Match` carries the construct it came from and E0008 names the
programmer's pattern instead. And **`resolveWithPrelude` had been passing the prelude as an
ordinary file** rather than with `Prelude: true` for ten phases — the helper's mistake and every
assertion's expectation were the same mistake, so no test could have caught it.

**Next action: the user's to set** (rule 7). Held explicitly for a separate decision:
**associated functions** (`List::new()`, 568 corpus sites, the only audited candidate that
reaches the type checker) and **tuple element access** (`t.0`, whose `x.0.1` lexing is the one
place any of this would reintroduce the context-sensitivity ADR-0013 removed).

**Phase 10 is complete** (`docs/phases/10-complete.md`). **Origin runs in a browser**, entirely
client-side, with no server executing user code and no backend at all:

```
98 of the 102 end-to-end cases, on both engines, through the WebAssembly module
   -> byte-identical stdout, stderr and exit status to the native run
```

`cmd/originwasm` is the whole pipeline as a `GOOS=js GOARCH=wasm` module exporting one
function; `web/` is the page around it. 1.5 MB gzipped, of which the CodeMirror editor is
94 KB. Five ADRs: **0032** (both engines, the VM by default), **0033** (a host with no
filesystem: every file operation fails as `Err(IoError::Other)`, and the `os` calls are behind
a build tag so the module cannot reach one), **0034** (CodeMirror, vendored), **0035** (a
shared program travels in the URL fragment, which browsers never transmit) and **0036** (a Web
Worker, so a non-terminating program does not freeze the tab and `terminate()` is a real stop
button).

Two things to carry forward. **Running the corpus is what found everything**: §08's back-edge
preemption does not survive a host with no signals, and `preemption_at_a_back_edge` hung
forever on both engines until they were made to yield explicitly. And **two defects older than
the phase** — a stale reference in the VM's `spawn` (a moving collector rewrites its roots, and
a Go local captured by a goroutine is not one) and a data race in `types.Prune` (path
compression made a write out of a read; `-race` reported 18, and reports 0 now). Both have
regression tests that fail on the code they replace.

**Phase 9 is complete** (`docs/phases/9-complete.md`). **The compiler compiles itself**, and the
result is a fixed point:

```
originc build -O1 stage1/src        -> 4,541,555 bytes
that binary, over the same source   -> the same bytes
and again                           -> the same bytes
```

`bootstrap/stage1-linux-amd64` is that binary. `stage1/src` is thirty-six modules and 34,000
lines of Origin -- one module per Go package under `internal/` -- and it is the largest body of
Origin there is. It also writes the signed Mach-O the target machine runs, identical to
`originc build --target macos`'s.

**Every component is held to the Go one it replaces**, over this repository's own ~430 `.origin`
files, in `tests/selfhost`: the token stream against `internal/lex`, the dumped tree against
`internal/ast`, the position mapping against `internal/source`, the *places* syntax errors are
reported against `internal/lex` + `internal/parse`, resolution against `internal/resolve` (397
packages, 744,896 trace lines), inference against `internal/check` (399 packages, 1,647,768
trace lines), the instantiation set against `internal/mono`, the bytecode against
`internal/compile`, the SSA against `internal/ir` at all three levels, the re-emitted bytecode
against `internal/opt`, 5,118 bytes of encoded instructions against `internal/x86`, eight
executables in two formats against `internal/obj`, 137 digests against Go's `crypto/sha256` --
and, above all of them, **the executable itself**.

**The oracle above the front end needed no invention, and each one subsumes the last.** Two
files that are byte-identical are the same program. `tests/selfhost`'s build differential
compares four end-to-end programs in two formats at three levels; the whole 102-case corpus
agrees by hand sweep. Below the bytecode the *trace oracle* is what had to be invented, and
`docs/phases/9-complete.md` records how it works and the two ways the checker's and the
monomorphizer's differ from the resolver's.

**The suite is at 265s of its 300s ceiling**, and this phase spent that budget twice over
before learning the rule: when it crosses, look for the question being asked more than once.
Engine agreement was being asked once per pass, in seven differentials, and is asked twice now
-- at `check` and at `dump-ir -O2`, which between them sit downstream of every pass. The corpus
differentials run natively only, at full breadth, against the Go compiler. Cutting *coverage*
to fit the budget is the wrong move and was not made.

**Next action: Phase 10, whose scope is the user's to set** (rule 7). What this phase left
behind, in the order it will be missed:

- **stage1's `build` prints hex, not a file.** A `String` is UTF-8 by construction
  (spec/14-strings.md) and an executable is not text, so there is nothing to hand
  `fs::write_file`. The oracle is unaffected -- two programs that print the same bytes wrote the
  same executable -- but a self-hosted toolchain that can run `originc build` end to end wants
  binary output, which wants a byte-oriented file API §15 does not have.
- **No directory listing**, so `--package` takes its files on the command line.
- Everything else in `docs/deferred.md` still tagged for a later phase: `match` as a linear
  chain of arm tests, a struct or enum declared inside a function body, no associated functions,
  a function that both escapes and is called directly losing its fast path (ADR-0020),
  single-space non-generational collection (ADR-0022), `frame variable` (ADR-0023), no parallel
  threads, unreclaimed channel/mutex/thread memory, and `?`'s error conversion via `Into`.

**Before touching anything, read `docs/phases/9-complete.md`.** The three things most likely to
matter:

- **The three debugging tools, before reading any code about a miscompilation.**
  `debugCollectEvery` (`internal/backend/runtime.go`) forces a collection every N allocations, so
  a lost root stops being a coincidence between one collection and one moment. `debugStaleRefs`
  (`internal/backend/array.go`) makes the array primitives check that a reference is inside the
  live semispace. `opt.DebugSkip` and `opt.DebugInlineLimit` bisect the optimizer. Three days of
  reading the code got Phase 9's lost root wrong twice; these got it in twenty minutes.
- **A slot on the AST arrives when a reader does.** The tree grew five this phase (`Expr.inst`,
  `Expr.iter_inst`, `FnDecl.span`, `Arm.span`, and `Stmt::Let`'s own), each in the same commit as
  the code that reads it, because a field nothing reads is the failure mode Phase 8 kept finding.
  Three of the five were span bugs the *executable* differential found and nothing below it
  could: the bytecode dump prints an instruction's operands and not where it came from, and the
  DWARF line table is the only artefact in the project that renders a span.
- **Process rule 9 now has something to protect.** `bootstrap/` holds a binary;
  `tests/selfhost` rebuilds it from source and compares, and runs it over its own source to
  check the fixed point still holds. When a change to the Go compiler, to `stage1/src` or to the
  prelude alters it, regenerate it deliberately -- the command is in `bootstrap/README.md` -- and
  say in the commit what about the compiler changed, because the diff itself says nothing a
  reader can use.

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

**Known-broken / deferred**, all recorded in `docs/deferred.md` with a phase, and nothing in
the project is known-*wrong*: `match` compiles to a linear chain of arm tests; a struct or enum
declared inside a function body fails loudly in `internal/compile` rather than being checked;
there are no associated functions, so a constructor is a free function in a `std::` module
(`list::new`, `sync::mutex`); a function used both as a direct callee and as an escaping value
loses its direct-call fast path for every use (ADR-0020); native collection is single-space,
non-generational, with no write barrier (ADR-0022); DWARF is a line table and a symbol table
only, so `frame variable` does not work (ADR-0023); no engine runs threads in parallel; the
native runtime never reclaims what it maps for a channel, a mutex or a finished thread's stack;
`std::fs` has no directory listing, metadata, rename, delete or streaming, and there is no
`Path` type (ADR-0030); and error conversion in `?` via `Into` needs a blanket-impl story
(`map_err` now exists, so the explicit form is real).

**On Phase 9's scope:** rule 7 put the choice with the user, and the project's own arc pointed
at self-hosting — what every phase so far had been building toward and what `bootstrap/` was
reserved for. It is done; the top of this section says what that means.

**A note on the history:** the VM's concurrency runtime landed inside commit `6b073de`,
whose message describes only ADR-0027 — the bug the VM work uncovered. The commit is
accurate about the bug and silent about the 500 lines of `internal/vm/concurrent.go`
beside it. Read that commit for both.
