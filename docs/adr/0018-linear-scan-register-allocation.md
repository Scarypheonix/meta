# ADR-0018: Register allocation is linear scan over the SSA IR, with the platform ABI's register roles

**Status:** accepted · **Date:** 2026-08-29 · **Decided by:** implementer (user delegated)

## Context
Phase 5 lowers the optimized SSA IR to x86-64. The IR has an unbounded number of
values; the machine has fourteen usable general-purpose registers. Something has to
decide which values live in registers and which live in the frame.

Two constraints from `CLAUDE.md` bear on the choice directly. The compiler must
eventually compile *itself* inside a 60-second incremental build on two cores, so the
allocator's own running time is a cost the project pays repeatedly. And Phase 5's
acceptance includes `lldb` breaking on an Origin source line and showing a usable
backtrace on a machine the implementer cannot test on.

## Options considered
- **Graph colouring (Chaitin–Briggs).** The best code of the three, and the textbook
  answer. Build an interference graph, colour it, spill on failure, iterate. The cost is
  quadratic-ish behaviour in the graph size and a build-spill-rebuild loop, in a
  compiler that must stay fast on a two-core laptop; and the implementation is large
  enough that its bugs are hard to localize.
- **Linear scan over live intervals (Poletto–Sarkar).** Sort values by the start of
  their live interval, walk once, keep an active set ordered by interval end, and spill
  the interval that ends furthest away when the pool is empty. Near-linear, small enough
  to read in one sitting, and the standard choice for compilers that value compile time
  — which is exactly this compiler's position.
- **No allocation: every value in a frame slot.** Trivially correct, trivially fast to
  compile, and the generated code is memory traffic from end to end. Tempting as a first
  step, and rejected as a destination: it would make every later measurement meaningless
  because the baseline would be absurd.

## Decision
Linear scan over live intervals computed on the SSA IR, in the IR's reverse-postorder
block layout so an interval is a contiguous range of instruction indices.

The register roles are the platform's (System V AMD64): arguments in
`rdi rsi rdx rcx r8 r9`, integer result in `rax`, callee-saved `rbx r12 r13 r14 r15`,
floats in `xmm0`–`xmm15`. `r15` is reserved for the runtime block (ADR-0017), leaving
`rbx r12 r13 r14` callee-saved and the caller-saved set for short-lived values. The
frame pointer is maintained in every function at every optimization level.

φ-functions are resolved before allocation, by inserting parallel copies on the
predecessor edges, so the allocator never sees one.

## Consequences
- **Debuggers work with no help from us.** A `lldb` backtrace, `bt`, and reading an
  argument register all behave as they would for a C program, because the frame and the
  registers mean what the platform says they mean. Given that two of Phase 5's
  acceptance criteria are things only the user can check on their machine, "behaves like
  everything else the debugger has ever seen" is worth more than the register a bespoke
  convention would win back.
- **Compile time stays linear in the function's size**, which is what the 60-second
  self-compilation budget needs.
- **The generated code is worse than a colouring allocator's**, mostly at high register
  pressure where linear scan spills a value a colouring allocator would have kept. This
  is accepted, and it is measurable: if a future phase wants the difference, the
  interface — live intervals in, an assignment out — does not change.
- **A call clobbers the caller-saved registers**, so any live interval crossing a call
  either lives in a callee-saved register or is spilled. The allocator prefers
  callee-saved registers for intervals that span a call, which is the one heuristic it
  has.
- **Reserving `r15` costs a register.** It buys every emitted runtime routine a
  one-instruction path to global state in a file with no relocations (ADR-0017), which
  is a straight trade of one register for the whole relocation machinery.
- Spill slots are permanent for the life of a function and are never shared between a
  reference-typed value and a raw one, because a stack map at a safepoint must be able to
  say what a slot holds (`docs/spec/11-codegen.md`, §08).
