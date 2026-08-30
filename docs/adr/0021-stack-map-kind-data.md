# ADR-0021: A value's kind travels through bytecode only where the backend cannot recover it any other way

**Status:** accepted · **Date:** 2026-08-30 · **Decided by:** implementer (user delegated)

## Context
Native heap collection is Phase 5's last large piece (spec/11-codegen.md's "Safepoints
and stack maps" DEFERRED note). A stack map at a safepoint has to say which frame slots
and registers hold references, and that requires knowing, for every value the register
allocator gives a home to, whether it is a reference at all. The SSA IR does not carry
this: it is built from bytecode (ADR-0016), and bytecode is untyped — a stack machine
whose instructions say nothing about what a word means once it has been computed.

The naive fix is to widen every bytecode instruction with an operand-kind field. That is
far more than a stack map needs, and ADR-0016's own precedent (`bytecode.Kind`, added
first for `to_str` and the comparison operators) already points at the cheaper answer:
widen exactly the instructions whose *result's* kind nothing downstream can otherwise
recover, and derive everything else structurally.

## What is, and is not, recoverable without widening anything
Tracing every way a value can arise in the IR:

- **`OpConst`**: already knows its kind from the constant pool's own `ConstKind`
  (int/float/char/string). Nothing to add.
- **Arithmetic, bitwise, shift, comparison, `OpNot`, `OpIsVariant`, `OpCast`**: every one
  of these has a result kind fixed by the operation itself (comparisons and `OpIsVariant`
  always produce `bool`; a cast's target is named by its own `CastKind`, and `as` only
  converts between primitives, spec/04-expressions.md). Nothing to add.
- **`OpStruct`/`OpTuple`/`OpVariant`/`OpClosure`/`OpBoxFn`/`OpToStr`**: always a
  reference (a heap object; `OpToStr` always builds a `String`). Nothing to add.
- **`OpPhi`**: a well-typed program's static type does not change across branches, so a
  φ's kind is whatever its operands' kind already is. Nothing to add.
- **`OpParam`/`OpCapture`**: these are entry values — a parameter or a capture arrives
  with no producing instruction of its own for a kind to have traveled with. This is one
  genuine gap.
- **`OpGetField`/`OpGetPayload`/`OpGetTupleElem`** (bytecode; all three become IR's
  `OpGetField`): the offset baked into the instruction (ADR-0019, no descriptor lookup at
  run time) does not say what is *at* that offset. `internal/compile` knows — it computed
  the field's exact kind when it built the containing struct/variant/tuple's own object
  layout — but nothing carries that answer to where the IR reads it back. The second gap.
- **`OpCall`/`OpCallClosure`**: the result's kind is the call expression's own checked
  type, which `internal/compile` has in hand at the call site but which is not, in
  general, recoverable from the callee alone — an indirect call's callee is not
  statically known, only its declared signature is (every value that could reach a given
  `OpCallClosure` site shares one `fn(...) -> T` type, Origin being statically typed, but
  there is no one function to look the return type up on). The third gap.

## Decision
Widen only what closes those three gaps, all in `internal/compile`, which already has
every answer on hand from the checker's own type tables:

- `bytecode.Instr` gains a `Kind` field, populated for `OpGetField`, `OpSetField`,
  `OpGetPayload`, `OpGetTupleElem` (the field's own kind — `OpSetField` gets one too, for
  symmetry, though nothing reads it yet: no write barrier exists in the non-generational
  collector this phase is scoped to, see "Consequences") and `OpCall` (the call
  expression's own checked kind, computed the same way at every one of `internal/compile`'s
  four `OpCall`-emitting sites, direct and indirect alike, so `internal/backend` never
  needs to ask which kind of call it turned out to be).
- `bytecode.Fn` gains `ParamKinds` and `CaptureKinds`, parallel to `Params` and
  `Captures`, populated once per function (`compileInstance` for an ordinary function or
  method, `lambda` for a closure body) from the same substituted, concrete types
  `internal/compile` already computes for object layout (ADR-0019's `concreteType`). A
  method's receiver kind needed one new piece of plumbing: `check.Result.SelfTypes`,
  mirroring the checker's already-existing (but internal) `FnSig.Self`, since nothing
  outside the checker could otherwise learn a method's own receiver type.

`internal/backend`'s own pass (not yet written; see "Consequences") is what will
propagate these seed values across the rest of the IR — every op above with a
structurally-derivable kind — the same way `internal/backend/closures.go`'s
`resolveClosureCalls` already computes a native-only property purely from the IR its own
`buildIR` builds, entirely apart from `internal/ir`, `internal/opt` or the VM. This ADR
covers only the seed data; the propagation pass, the register allocator's placement of
reference-kind spill slots as one contiguous run (already specified,
spec/11-codegen.md's "Stack frames"), the stack-map table itself, and the collector that
consumes it are the work `docs/deferred.md` still lists as open.

## Consequences
- **Nothing observable changes.** This lands the seed data and nothing that reads it
  yet, verified directly (`internal/compile/kind_test.go` asserts the exact `Kind` on
  hand-picked programs covering a struct field, a variant payload, a tuple element, a
  call result, a method's `self`, and a closure's captures) and indirectly (the full
  differential suite is unchanged, because no engine's behavior depends on this data).
- **No write barrier, no card table, and no generational split are part of this phase's
  design.** `OpSetField` gets a `Kind` for symmetry with the other three field-access
  ops, but a barrier is what a *generational* collector needs to find old-to-young
  references cheaply; a single-space, stop-the-world collector (the shape Phase 5 is
  scoped to — see the still-open stack-map/collector work) does not need one at all,
  since every collection walks every live root regardless of generation. Going
  generational later is Phase 6+ work, not blocked by anything here.
- **A future collector's soundness rests on this data being exact, not conservative.**
  Unlike `internal/opt`'s escape analysis (where "may allocate" is a safe fallback), a
  stack map claiming a raw integer is a reference would make the eventual collector chase
  it as a pointer — memory corruption, not merely a missed optimization. That is why this
  ADR insists on deriving each `Kind` from the checker's own recorded type at the exact
  point `internal/compile` already has it in hand, rather than approximating.
