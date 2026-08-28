# ADR-0006: Errors are `Result` values; there is no unwinding

**Status:** accepted · **Date:** 2026-08-28 · **Decided by:** implementer (user delegated)

## Context
The error model determines whether the backend needs an unwinder, whether every call
site needs landing pads, and how a green thread can fail without killing its neighbours.

## Options considered
- **Exceptions with unwinding.** Ergonomic at the call site. Requires DWARF CFI, a
  personality routine, landing pads at every call that owns cleanup state, and an
  unwinder — all hand-written here, with no LLVM to supply them. It is plausibly a
  phase of work on its own, and it makes every function call a potential control-flow
  edge for the optimizer.
- **`Result` values only.** No unwinder, no landing pads; every failure edge is an
  ordinary branch the optimizer already understands. Verbose without a propagation
  operator, hence `?`.
- **Both.** The usual answer (Rust, Swift). It still needs the unwinder.

## Decision
`Result[T, E]` with the `?` operator for recoverable failure. Bugs — overflow, bad
index, explicit `panic` — trap: print to stderr, exit 101, no unwinding, not catchable.

## Consequences
- Phase 5 needs no unwinder and no landing pads. Calls stay simple edges.
- **The cost is real and is the largest deferred item in the project:** in Phase 6, one
  green thread's panic kills the process, because isolating it requires either unwinding
  or a supervisor-restart model. Recorded in `docs/deferred.md` against Phase 6.
- `?` requires the error types to match exactly in 0.1; automatic conversion needs an
  `Into` bound and is deferred to Phase 7.
- Reversal is expensive after Phase 5 but not impossible: adding unwinding later is
  additive, since `Result` code keeps working.
