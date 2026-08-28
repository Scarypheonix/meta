# Origin — project instructions

Origin is a statically typed, garbage-collected language and its complete toolchain,
built from nothing until the compiler compiles itself. This file is the source of truth
for how to work in this repository. The project origin prompt is superseded by it.

**Current phase: 1 — tree-walking interpreter. Phase 0 is complete** (see
`docs/phases/0-complete.md`).

---

## Commands

```bash
./check              # THE gate: gofmt + go vet + go build + full test suite
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
internal/             compiler packages (Go) — see "Module boundaries"
tests/conformance/    type-system accept/reject corpus, one file per case
tests/e2e/            programs + exact expected stdout/stderr/exit
tests/docs/           documentation invariants (ADR numbering, code registry, lints)
tests/fuzz/           fuzz targets for the lexer and parser
docs/spec/            THE language specification — normative
docs/adr/             architecture decision records — every irreversible choice
docs/phases/          N-complete.md, written at each phase gate
docs/deferred.md      everything deliberately left out, each tagged with a phase
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
   Current shared-agreement modules: `internal/layout` will own object layout, stack
   maps and safepoint placement for both the GC and the backend (spec §08).
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

**In flight:** nothing. Phase 0 closed.

**Known-broken:** nothing.

**Next action:** Phase 1 — lexer with source positions, then the Pratt parser with
error recovery (multiple syntax errors per pass), then the AST, name resolution with
lexical scoping and closures, then the tree-walking interpreter. Phase 1 exits when the
interpreter runs recursive fibonacci, a closure-based counter, a linked list and mutual
recursion, and parser errors point at the right line and column. The four programs are
already written with their expected output in `docs/spec/10-examples.md` §2–§5 and
seeded under `tests/e2e/cases/`.

**Awaiting the user:** nothing blocking. Two things only the target machine can
confirm, both at Phase 5: that a compiled Mach-O binary runs, and that `lldb` breaks on
an Origin source line.
