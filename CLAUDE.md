# Origin — project instructions

Origin is a statically typed, garbage-collected language and its complete toolchain,
built from nothing until the compiler compiles itself. This file is the source of truth
for how to work in this repository. The project origin prompt is superseded by it.

**Current phase: 5 — native x86-64 backend. Phases 0 through 4 are complete**
(see `docs/phases/`).

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
internal/layout/      object layout and headers: the GC/backend shared contract
internal/gc/          precise generational moving collector
internal/bytecode/    the stack instruction set and its disassembler
internal/ir/          the SSA intermediate representation, built from bytecode (ADR-0016)
internal/opt/         the optimizer: folding, CSE, LICM, escape analysis, inlining, DCE
internal/mono/        monomorphization of call dispatch (ADR-0010)
internal/compile/     AST -> bytecode, per monomorphized instance
internal/vm/          the bytecode virtual machine
internal/prelude/     Option, Result, Ordering — written in Origin
internal/driver/      pass ordering and error suppression
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
   Current shared-agreement modules: `internal/layout` owns object layout and headers
   for the GC and, from Phase 5, the backend; stack maps and safepoint placement join
   it there (spec §08).
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

**In flight:** Phase 5, the native x86-64 backend. Landed so far:

- `docs/spec/11-codegen.md` and ADR-0017 through ADR-0020 specify and decide the
  backend's shape: no linker, no libc, freestanding executables (ADR-0017); linear-scan
  register allocation (ADR-0018); every struct, tuple, enum-variant and closure
  instantiation now gets its own exact `Fixed` object layout, keyed the same way
  `internal/mono` keys a function instance — `layout.Tagged` is retired (ADR-0019).
- `internal/x86`: a hand-written instruction encoder for the subset the backend needs.
- `internal/obj`: complete Mach-O and ELF executable writers sharing one instruction
  stream (ADR-0003), no linker involved (ADR-0017).
- `internal/backend`: lowers the SSA IR to machine code for integer/float/bool
  arithmetic with trapping semantics, comparisons, control flow, direct/indirect calls
  under the System V calling convention, and — now that exact layouts exist —
  `OpStruct`/`OpTuple`/`OpVariant` construction and `OpGetField`/`OpSetField`/
  `OpIsVariant` field access, allocating through the runtime's bump allocator
  (`alloc`). `originc build` is wired up and native output sits in the same
  differential the other two engines run; the `nativeSkips` list in `tests/e2e` is
  the live, honest record of what native still cannot do, and it is shrinking rather
  than growing.
- `internal/layout` now has its second reader: with exact layouts in place, the backend
  reads a field's offset and pointer-ness directly from its `Descriptor` — no
  descriptor lookup at run time, since the offset is baked into the instruction.
- Found and fixed a real register-allocator bug while extending the differential: a
  value used only as a successor block's φ argument for the edge leaving its own
  defining block got an interval that ended at its own definition, so the allocator
  could recycle its register before the back-edge's φ copy read it back. Any loop with
  two or more live loop-carried values (a counter and an accumulator, say) could
  silently collapse them onto one. Regression-tested directly against `intervals()` and
  `allocate()` in `internal/backend/regalloc_test.go`.
- Structural `==`/`!=` on a struct, tuple, enum or `String` now works in native code
  (`internal/backend/equal.go`): a hand-written recursive routine reads a per-`TypeID`
  layout table this file emits into read-only data (Shape, ObjKind and, for a `Fixed`
  shape, the address of its `Kinds` byte array — an object's own header still supplies
  its actual field count or byte length, since only the table's *shape* is constant per
  `TypeID`), mirroring `internal/vm`'s `equalObjects` field for field, including IEEE
  float equality inside an aggregate and the trap on comparing two closures.
- `<`/`<=`/`>`/`>=` on `String`, and the `cmp` builtin on every primitive kind, now work
  in native code too. `compare_bytes` (`equal.go`) is the byte-lexicographic walk
  `equal_objects`'s `ByteArray` case already needed, returning a three-way sign both
  `stringOrder` and `cmp`'s `buildOrdering` read. `cmp` itself never got `OpCallBuiltin`
  widened with an operand-kind operand the way `OpToStr` was — instead
  `compile.cmpBuiltinFor` picks one of four kind-specific builtins (`BuiltinCmpInt`/
  `Uint`/`Float`/`String`) at compile time, so the backend already knows which
  comparison a call needs without reading anything from the instruction beyond which
  builtin it is. Finding this also caught a latent bug in `kindOf` shared with
  `OpToStr`: inside a generic instance's body a receiver's checked type can still name
  that instance's own type parameter, and `kindOf` was reading it unsubstituted —
  invisible for `to_str` (the VM ignores its Kind operand and dispatches on the value's
  own runtime tag) but fatal for `cmp`'s new compile-time builtin choice, which fixed
  both at once. Verified by hand against nested structs, `String` content and ordering
  (including prefix and empty-string cases), `NaN`/`-0.0` inside a struct, tuples, and
  `cmp` across every primitive kind, byte-identical across all five engine/level
  combinations; landed permanently as `tests/e2e/cases/structural_equality` and
  `tests/e2e/cases/cmp_and_ordering`.
- `for` loops now compile on the VM and native backend, not only the interpreter.
  `check.forElementType` records the desugaring's two implicit calls (`into_iter()`,
  `next()`, spec/04-expressions.md's normative form) as instantiations keyed to
  existing AST nodes that `ast.Inspect` already visits (`v.Iter` standing in for
  `e.into_iter()`, `v` itself standing in for `__it.next()`), so `internal/mono` and
  `internal/compile`'s new `forExpr` find a concrete callee for each exactly as an
  explicit method call would. The backend needed no changes at all: a desugared `for`
  lowers to ops (calls, jumps, `is_variant`, `get_field`) it already handled. Landed
  permanently as `tests/e2e/cases/for_loop`, byte-identical across all seven
  engine/level combinations.
- **Closures now compile natively (ADR-0020)**, closing what had been Phase 5's last
  real gap. `internal/backend/closures.go`'s `resolveClosureCalls`, a new native-only
  IR pass run unconditionally right after `ir.Build`, makes static what the VM decides
  dynamically from a runtime tag (native values carry none, ADR-0008): an `OpFunc`
  value stays a bare code address exactly when every one of its uses is being an
  `OpCall`'s own immediate callee; every other use — a return, an argument, a phi, a
  struct field — gets it wrapped in a new `OpBoxFn` op first, allocating the same
  one-word closure shape `internal/vm/fields.go`'s `boxIfFn` already builds dynamically
  for the identical situation. Every `OpCall` left with a non-bare-`OpFunc` callee is
  then repointed at a new `OpCallClosure` op (a slot `internal/ir`'s classification
  tables had reserved since earlier IR work, never filled in until now). A real closure
  and a boxed bare function share one calling convention: the callee's code address
  comes from the object's field 0, and the object reference itself travels to the
  callee on the stack — in the fixed spot a call always leaves just above the return
  address, never shifted into an argument register — where `OpCapture` reads it back
  directly, with no persistent register or frame slot needed at all. An ordinary
  function reached this way simply never reads that spot, so one convention serves
  both without the call site ever needing to know which kind of callable a given value
  actually is. Found and fixed a real bug in the pass itself while testing it against
  an argument-passing case: `ReplaceAllUses` run *after* splicing the new box into its
  block rewrote the box's own reference to the value it was boxing into a reference to
  itself, corrupting every downstream read into a zero — caught immediately by the
  first cross-function test case, fixed by reordering the two steps. Verified against
  direct closures with multiple params and multiple captures, closures rebuilt fresh
  each loop iteration with different captured values, a bare function stored in a
  variable, passed as an argument, returned through an `if`/`else` (a phi), and read
  back out of a struct field — byte-identical across the interpreter, VM, and native at
  every optimization level; landed permanently as `tests/e2e/cases/fn_value_escapes`
  alongside the pre-existing `closure_counter`, now off the native skip list entirely.

**Known-broken / explicitly out of scope for this slice**, recorded in
`docs/deferred.md`: native heap allocation never triggers a collection (no stack maps
yet — spec/11-codegen.md's own DEFERRED note explains why); integer arithmetic is
still 64-bit only in both engines; `u64::MAX` has no run-time representation; `match`
compiles to a linear chain of arm tests rather than a decision tree; a struct or enum
declared inside a function body (never checked, per Phase 2's own documented scope) now
fails loudly in `internal/compile` instead of silently compiling against the wrong
descriptor, which is safer but still not a fix; and a function used both as a direct
callee and as an escaping value in the same body loses the direct-call fast path for
every use once it is boxed at all (ADR-0020's own documented, deliberate simplification
— correct, just not maximally fast in that one mixed pattern).

**Next action:** native heap collection is now the only large piece of Phase 5 left.
The backend's `alloc` bump-allocates forever; making it actually collect needs precise
stack maps, which need the bytecode widened to carry a value's kind at a safepoint the
way `OpToStr` and the comparison ops already carry one for their own purposes
(spec/11-codegen.md's own DEFERRED note is the starting point). Phase 5 is not closed
until every phase 4 exit criterion holds under `-O0`/`-O1`/`-O2` on native output too,
and collection is what that still needs.

**Awaiting the user:** the two things only the target machine can confirm — that a
compiled Mach-O binary runs, and that `lldb` breaks on an Origin source line — are
closer now that `originc build` produces one, but still untested outside this
container. A prebuilt `darwin/amd64` binary of `originc` itself (cross-compiled here,
no Go install needed on the Mac) has been handed to the user for exactly this.
