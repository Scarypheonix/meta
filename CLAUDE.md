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
- **Every value the register allocator will ever give a home to now has a known kind
  (ADR-0021)**, the groundwork native heap collection needs. `bytecode.Instr` gained a
  `Kind` field for the handful of instructions whose result the backend cannot otherwise
  tell is a reference at all — a field/payload/tuple-element read, and a call — and
  `bytecode.Fn` gained `ParamKinds`/`CaptureKinds`; `internal/compile` populates all of
  it from the checker's own already-computed types, at the same points ADR-0019's
  object-layout code already reads them (a method's receiver kind needed one small new
  checker export, `check.Result.SelfTypes`, mirroring the checker's existing internal
  `FnSig.Self`). `internal/backend/kinds.go`'s `propagateKinds`, a new native-only IR
  pass run right after `closures.go`'s `resolveClosureCalls` in the same `buildIR` step,
  carries that seed to everything else structurally — arithmetic is always raw, an
  aggregate construction is always a reference, a `phi` is whatever its operands already
  are (a small fixed point, since an operand can itself be another `phi` on a loop back
  edge) — including the two ops `resolveClosureCalls` itself produces (`OpBoxFn` is
  always a reference; a repointed `OpCallClosure` keeps the kind it had as `OpCall`).
  Nothing consumes any of this yet — no engine's behavior changed, verified both directly
  (`internal/compile/kind_test.go` for the seed data; `internal/backend/kinds_test.go`
  for the completed kind, including a boxed bare function's `OpBoxFn`/`OpCallClosure`
  pair specifically) and by the full differential suite being unchanged.
- **The register allocator now places every reference-kind spill slot in one contiguous
  run below the raw ones**, exactly what spec/11-codegen.md's "Stack frames" specifies
  and the last piece a stack map needs before the table itself. `regalloc.go`'s
  `allocate` sorts a spilled value by `isRefKind(iv.val.Kind)` and numbers the two
  groups afterward; a call-spanning value that still carries `KindUnknown` at that point
  panics rather than guessing, since a wrong guess would let a future collection corrupt
  memory instead of merely computing a worse layout. That panic fired immediately, on
  the first real program compiled with it: `internal/opt`'s `-O1`/`-O2` path was
  silently dropping `Kind` on its own bytecode round-trip (`ir.Emit`'s `OpCall`/
  `OpGetField` emission, a fresh `bytecode.Fn`'s `ParamKinds`/`CaptureKinds`, and
  inlining's value cloner never touched it) — a real, previously-invisible gap, found
  and fixed the same day `Kind` got its first actual reader. Verified by the panic's own
  regression test (`internal/backend/regalloc_test.go`), a new test for the grouping
  itself, and the full differential suite passing clean at every level again.
- **The stack-map table is now built and reachable from every native binary**, closing
  everything ADR-0021 scoped ahead of the collector itself. `internal/layout/stackmap.go`
  owns the encoded shape (process rule 5): `StackMapEntry`, `EncodeStackMap`,
  `LookupStackMap` (binary search), `DecodeStackMap`. `regalloc.go`'s `allocate` now also
  computes `callSiteRegs` — for every call-clobbering value, which of the four
  callee-saved registers hold a live reference-kind interval at that exact point, never a
  caller-saved one (ADR-0018's own allocation invariant, which is what bounds a register
  root set to four bits). `internal/backend/stackmap.go`'s `recordCall` is called at
  every user-code call site `lower.go` lowers — direct and indirect calls, `alloc`,
  `equalObjects`, `compareBytes`, and the comparison-builtin allocation sites — binding a
  label at the call's own return address and recording an entry from `callSiteRegs` and
  the allocator's own contiguous reference-slot numbering (`RefOffset`/`RefCount`, read
  directly, no separate computation). `runtime.go`'s own internal call sites (`write`,
  `print`, `intToStr`, and the trap/panic paths that never return to Origin code)
  deliberately get no entry — none holds an Origin-level reference, and the future
  collector's `rbp`-chain walk can skip over them structurally. `buildStackMap` resolves
  every recorded label only after codegen's real-address pass runs, sorts by address, and
  encodes into read-only data; `writeStackMapFields` pokes the table's address and count
  into the runtime block (`rtStackMapOff`, `rtStackMapCountOff`) as compile-time
  constants, the same way other fixed values are already poked into the data segment.
  Verified end to end through the real compiler (`internal/backend/stackmap_test.go`:
  every entry's return address lands inside the text segment, the table is address-sorted,
  a live reference genuinely survives a real allocation in a callee-saved register, and
  the runtime block's own entry count matches what decodes), plus the register-root
  computation directly (`regalloc_test.go`) and the encoding round-trip directly
  (`internal/layout/stackmap_test.go`). Nothing consumes the table yet — no engine's
  behavior changed, and the full differential suite is unchanged.
- **Native heap collection is landed (ADR-0022)** — a single-space, stop-the-world
  semispace copy, closing the collector gap the six bullets above were all building
  toward. `emitStart` now `mmap`s two `heapSize` regions instead of one; `emitAlloc`
  collects and retries once on an out-of-space bump, trapping `out of memory` only if the
  retry still does not fit. `internal/backend/collect.go`'s `rt_collect` walks the `rbp`
  chain from the immediate caller of `alloc`, resolving each frame via a new
  `rt_lookup_stack_map` (machine code standing in for `layout.LookupStackMap`) and
  evacuating every register and stack-slot root through `rt_evacuate` (Cheney's
  algorithm) and `rt_scan_object` (reading the same per-`TypeID` table `equal.go`
  built). `StackMapEntry` gained `SavedMask` — which callee-saved registers a call
  site's own function pushed in its prologue — the one fact the table did not yet carry:
  without it, recovering an *outer* frame's original register values from an *inner*
  frame's save slots while walking outward is not possible. A frame with no real entry
  (one of `runtime.go`'s own routines, e.g. `rt_int_to_str`, which allocates internally
  but was never wired into `recordCall`) gets a synthetic all-saved, no-roots-of-its-own
  entry rather than being treated as a stack-map gap. `backend.go`'s `function` now also
  zeroes every reference-kind spill slot at entry, so a slot the collector visits before
  its value's first store reads `Nil` rather than another frame's leftover bytes. Two
  real bugs surfaced only once the walk actually ran on real programs (both now
  regression-covered): `gcTransitionLocs` clobbering `rcx`/`rdx` while computing a
  frame's own save-slot addresses corrupted the *next* frame's rbp/return-address the
  moment they were computed into those same registers just before calling it, fixed by
  stashing them in dedicated frame slots across the call; and an early draft wrote every
  tracked register's final location back into the physical register once the whole walk
  finished, which is correct for a register a frame never touched but corrupts one a
  frame saved for its own unrelated local state (`rt_int_to_str`'s own buffer pointer,
  concretely) — fixed by never doing a separate end-of-walk restore pass at all, since
  each register's one correct write-back already happens inline, exactly once, at the
  specific frame whose `RegMask` claims it. Verified against real, executed collections:
  `tests/e2e/cases/gc_reclaims_discarded_allocations` (spec/10-examples.md #13: ~120 MB
  into one loop-carried live struct, several real collections in one run) and
  `gc_survives_across_a_call` (a live struct held in `main`'s own frame while a called
  function allocates heavily — genuine cross-frame register recovery, not just the
  frame-0 case) — both byte-identical across every engine and optimization level.
  Building the first of those also surfaced an unrelated, pre-existing `internal/opt`
  bug (a `-O2` fixed-point non-convergence on a loop assigning two variables from the
  loop counter two different ways each iteration, confirmed to reproduce with no
  allocation involved at all) — recorded in `docs/deferred.md` rather than chased here.
- **`tests/e2e`'s `nativeSkips` list is now empty.** Its one remaining entry,
  `opt_float_semantics_survive_folding`, was never actually a native-codegen gap in the
  sense the other five (now-closed) skips were — it was the *test* depending on
  behavior `docs/deferred.md` already documents as unspecified and Phase-7 work: float
  `to_str`'s exact rendering is "the implementation's choice," and the interpreter/VM's
  shared `formatFloat` (`strconv.FormatFloat(f, 'g', -1, 64)`, plus a trailing `.0` for
  an integral result) is that choice, not something the spec fixes yet. Implementing a
  full shortest-round-trip float-to-decimal algorithm in hand-assembled x86 (Grisu/
  Ryu/Dragon4-class work, needing real bounded-precision big-integer arithmetic with no
  spec to build it against) would have been exactly the "code before its spec exists"
  process rule 1 forbids. The right fix was the one `docs/deferred.md` already
  prescribed: stop depending on it. The test's last line changed from printing
  `(1.0 / 3.0).to_str()` to printing `(1.0 / 3.0 == 0.3333333333333333).to_str()` —
  Go's `ParseFloat` (what the lexer's own float literals already go through) is the
  exact inverse of `FormatFloat`'s shortest-round-trip mode, so the literal parses back
  to bit-for-bit the same `f64` a correctly-computed `1.0/3.0` produces, and the test
  still verifies exactly what its name says (constant folding doesn't change a float
  division's result) without rendering a float at all. Native `to_str` on a `Float`
  still hits `unimplemented:` — that gap is real and unchanged — but nothing in the
  differential suite depends on it anymore, which is the state `docs/deferred.md` always
  called correct for Phase 5.
- **Root-caused and fixed the `-O2` fixed-point bug** the collector's own stress test
  had surfaced. `CommonSubexpressions` (`internal/opt/cse.go`) reported `changed`
  whenever it found a dominating duplicate expression, even when that duplicate already
  had zero uses left to replace. A trapping operation's own duplicate instruction
  survives `DeadCodeElimination` forever once CSE has merged it (ADR-0005: it can still
  trap, so an unused instance is never removed just because its uses were redirected
  elsewhere), so every subsequent round rediscovered the exact same already-merged,
  still-present pair and reported a change anyway — the pipeline (`runPasses`, `opt.go`)
  could never reach the fixed point it was looking for on any function shaped like that.
  Fixed by reporting `changed` only when a use was actually replaced, which is what
  makes a second pass over an unchanged pair genuinely a no-op. Verified directly
  (`internal/opt/opt_test.go`'s `TestCommonSubexpressionsIsIdempotentOnAnUnusedTrappingDuplicate`,
  confirmed to fail without the fix) and end to end
  (`tests/e2e/cases/opt_cse_converges_on_a_trapping_duplicate`, the minimal repro: two
  loop-carried variables each assigned from the loop counter a different way, one
  iteration apart).
- **A DWARF4 line table is now emitted, following a new decision, ADR-0023.** Found while
  preparing the Mac confirmation materials below: spec/11-codegen.md's "Debug
  information" section was normative and unimplemented — `lldb breakpoint set --file f
  --line N` had nothing to resolve against, on any native build, ever. A new
  `internal/dwarf` package owns the byte-level encoding (process rule 5, the same shape
  as `internal/x86`'s instruction encoder): `.debug_abbrev`/`.debug_info` carry the one
  `DW_TAG_compile_unit` DIE the phase's own scope calls for (no subprogram, variable or
  type DIEs — deliberately out of scope, `docs/deferred.md`), and `.debug_line`'s
  line-number program is built entirely from `.text` addresses, using only standard and
  extended opcodes (never a *special* opcode, so `line_base`/`line_range` need never be
  tuned for correctness). Built entirely from `.text` addresses, it is automatically
  byte-identical on both of `backend.Build`'s two emission passes — the same
  pass-invariance ADR-0017 already gives every instruction's length — which `Build` now
  also asserts explicitly, alongside its existing text/roData check.
  `internal/backend`'s `function()` gained `recordLine`, called once per instruction and
  once per terminator: it appends a `dwarf.Line` row only when a value's span names a
  different (file, line) than the previous instruction's did, and resets per function so
  a function's own first row is never accidentally deduped against the previous
  function's last one. Each function's own `(name, address, size)` is captured the same
  way, into `dwarf.Func`. "`bt` names the function" comes from a plain symbol table on
  both formats, never a `DW_TAG_subprogram` DIE — Origin's native calling convention
  already uses standard `push rbp; mov rbp, rsp` prologues, so frame-pointer unwinding
  needs nothing else. `internal/obj.Image` gained `DebugAbbrev`/`DebugInfo`/`DebugLine`/
  `Funcs`; ELF (`elf.go`'s `buildELFSections`) gets a real section header table —
  `.text`/`.rodata`/`.data` "shadow" sections over the bytes already in the two loaded
  segments, plus `.debug_abbrev`/`.debug_info`/`.debug_line`/`.symtab`/`.strtab`/
  `.shstrtab` appended after them — and Mach-O (`macho.go`'s `buildMachODebug`) gets a
  `__DWARF` segment (three `__debug_*` sections, vmaddr/vmsize both zero: DWARF is read
  from the file, never from the running process, so unlike `__TEXT`/`__DATA` this segment
  claims no address-space range at all) plus an `LC_SYMTAB` command. Found and fixed two
  real bugs while wiring this up: Mach-O's `headerSize` hadn't grown to reserve room for
  the two new load commands, so the real header overran the space `Plan` had already
  fixed `TextAddr` against, corrupting the file (fixed by making Mach-O's `headerSize`
  always reserve that room, whether or not a given image ends up carrying debug info — an
  upper bound costs nothing, since any slack is already zero-padded); and both writers'
  "skip padding to `dataOff` when there is no data" optimization left the file's actual
  write position short of where the debug section's offsets assumed it would be whenever
  an image had debug info but a genuinely empty data segment (fixed by padding to
  `dataOff` regardless, whenever debug sections are present). Verified against Go's own
  independent `debug/dwarf`/`debug/elf`/`debug/macho` readers at three layers
  (`internal/dwarf/dwarf_test.go` calling `dwarf.Build` directly;
  `internal/obj/obj_test.go`'s new `TestELFCarriesDebugInfo`/`TestMachOCarriesDebugInfo`
  attaching synthetic sections to a hand-written image; `internal/backend/dwarf_test.go`'s
  `TestNativeBuildCarriesValidDebugInfo` through the real compiler end to end) — and,
  going further than any other native-backend milestone this phase, against `lldb`
  itself, running in this container: `originc build` a real program, then `lldb -b`
  `breakpoint set --file dbg.origin --line N`, `run`, `bt` — it stops at the right
  address, prints the right source line, and names both frames (`add`, `main`) with
  correct `file:line:col`, executed for real rather than only claimed. `frame variable`
  correctly reports no variable info, exactly the scope ADR-0023 committed to. Mach-O's
  own correctness is structural-only here (`debug/macho`, no macOS available); a compiled
  Mach-O binary actually breaking in `lldb` on the target machine remains the user's own
  confirmation step below, now against code that has debug info at all for the first
  time.

- **Mach-O executables are signed, and now actually run (ADR-0024).** The user ran the
  first Mach-O this project ever produced on the real target machine and it did not run:
  `zsh: killed`, SIGKILL from the kernel. The binary was correct — the same instruction
  stream runs as an ELF, and the differential agrees across all three engines — but since
  macOS 11 the kernel kills any executable carrying no valid code signature. Nothing
  about that is Gatekeeper or the quarantine bit (removing it changes nothing); it is
  unconditional, and goes unnoticed everywhere else only because Apple's linker ad-hoc
  signs its own output by default. **Origin has no linker (ADR-0017), so its Mach-O
  output was never signed and could never have run** — undetected for the whole phase
  because the container cannot execute a Mach-O, so every previous "verification" of that
  path was structural. Signing it after the fact failed too, with `codesign` saying only
  "internal error in Code Signing subsystem". Installing `rcodesign` (an independent,
  cross-platform implementation of Apple code signing that *does* run in the container)
  named the real defect immediately: `__LINKEDIT isn't final Mach-O segment`. Two
  structural bugs were behind it, both now fixed and regression-tested: there was no
  `__LINKEDIT` segment at all (macOS requires one, last, because that is where a
  signature is appended — the symbol and string tables moved into it, where they belong,
  instead of sitting past every declared segment), and ADR-0023's own `__DWARF` segment
  claimed `vmaddr 0`, colliding with `__PAGEZERO`'s `[0, 4 GiB)` claim — the
  unmapped-debug-segment convention of a `.o` file, not valid in a linked executable.
  With both fixed, an `rcodesign`-signed build **ran on the target machine and printed
  the right answer**, which is what made the rest a question of how to sign rather than
  what was wrong. `internal/codesign` now emits the signature itself, so
  `originc build --target macos` produces a binary that runs with no external tool —
  the same reasoning ADR-0017 used to refuse a linker, since a compiler whose output
  needs a second proprietary tool to execute has the same defect in a different place,
  and Phase 9's bootstrap would inherit it. Ad hoc means identity by content hash alone:
  no key, no certificate, nothing cryptographic beyond SHA-256. The signature's size is
  computed before any byte is written, because `__LINKEDIT`'s size and
  `LC_CODE_SIGNATURE` both name it and both sit in the header the signature then hashes —
  the same no-patching-after-the-fact discipline `Plan` imposes on addresses. Verified
  three ways: `internal/codesign`'s own tests re-derive every page digest from the file
  (including a short final page, the off-by-one case) rather than trusting the builder;
  `internal/obj` walks `LC_CODE_SIGNATURE` the way the kernel does and re-hashes the real
  written file; and, as a genuine cross-implementation check, the same hashing logic was
  run against **`rcodesign`'s own signature** and validates it — identical `codeLimit`,
  page boundaries and special-slot layout — with our emitted requirement-set digest
  byte-identical to theirs.

**Known-broken / explicitly out of scope for this slice**, recorded in
`docs/deferred.md`: integer arithmetic is still 64-bit only in both engines; `u64::MAX`
has no run-time representation; `match` compiles to a linear chain of arm tests rather
than a decision tree; a struct or enum declared inside a function body (never checked,
per Phase 2's own documented scope) now fails loudly in `internal/compile` instead of
silently compiling against the wrong descriptor, which is safer but still not a fix; a
function used both as a direct callee and as an escaping value in the same body loses the
direct-call fast path for every use once it is boxed at all (ADR-0020's own documented,
deliberate simplification); native heap collection is single-space and non-generational,
with no write barrier (ADR-0022's own deliberate scope, satisfying the same language-
level guarantees as the VM/interpreter's generational collector, see spec/08-memory-
model.md); `to_str` on a `Float` is unimplemented in native code (no spec fixes a
rendering yet — Phase 7, per `docs/deferred.md` — and nothing in the differential suite
needs it now); and DWARF is a line table and a symbol table only — no
`DW_TAG_subprogram`/`DW_TAG_variable`/`DW_TAG_base_type` DIEs and no `.debug_frame`, so
`frame variable` and expression evaluation under `lldb` do not work (ADR-0023's own
deliberate scope; `docs/deferred.md`, Phase 7).

**Next action:** `./check` passes clean, `nativeSkips` is empty, the full differential
suite agrees byte for byte including exit status, a compiled ELF's DWARF line table has
been verified against `lldb` itself in this container, and — the thing the whole phase
was gated on — **a Mach-O build has now run on the actual target machine and printed the
right answer**, after ADR-0024 found and fixed the reason no Mach-O this project produced
had ever been runnable.

What is outstanding is the confirmation of *self-signed* output specifically: the binary
the user ran was signed by `rcodesign`, which proved the structure and the approach;
`internal/codesign` now does that signing itself, verified in-container against an
independent implementation but not yet executed on a Mac. So the remaining checklist item
for `docs/phases/5-complete.md` is unchanged in shape and much smaller in risk: **the
user confirms, on the target machine, that a binary built by current `originc` runs
unaided, and that `lldb` breaks on an Origin source line** (ADR-0003's "Consequences" —
not something to write up preemptively). A rebuilt `darwin/amd64` `originc` carrying both
ADR-0023 and ADR-0024 has been sent for exactly that.

The lesson worth keeping: ADR-0003's target-machine checklist item was not a formality.
It found a defect that made every Mach-O this project ever emitted unrunnable, and no
amount of in-container structural verification would have caught it. Treat "verified
structurally" as what it says and nothing more.
