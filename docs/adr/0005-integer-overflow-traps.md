# ADR-0005: Integer overflow traps at every optimization level

**Status:** accepted · **Date:** 2026-08-28 · **Decided by:** implementer (user delegated)

## Context
Overflow behaviour must be chosen before any arithmetic is specified or emitted.

## Options considered
- **Wrap always.** Fastest, fully defined, and silently wrong: the most common
  arithmetic bug produces a plausible number instead of a stop.
- **Undefined.** Not available — spec pillar 1 forbids undefined behaviour.
- **Trap in debug, wrap in release (Rust).** Two semantics for one program. It directly
  contradicts Phase 4's exit criterion, which requires byte-identical output at `-O0`
  through `-O2`; a program overflowing at `-O0` would trap and at `-O2` would print a
  number, and the harness could not tell that from a miscompilation.
- **Trap always, with explicit non-trapping alternatives.** One semantics. Costs a
  conditional branch on the overflow flag after each `add`/`sub`/`imul` — on Broadwell,
  a correctly-predicted not-taken branch, i.e. close to free in the common case, and
  the branch is a natural constant-folding opportunity when a range is known.

## Decision
`+ - *`, unary `-`, and shifts trap on overflow or out-of-range shift amount, at every
optimization level. Wrapping, checked and saturating operations are explicit methods
(`wrapping_add`, `checked_add`, `saturating_add`) that never trap. Narrowing `as` casts
truncate and never trap.

## Consequences
- The `-O0`/`-O1`/`-O2` identical-output invariant is checkable and meaningful.
- Constant folding must fold to *the trap*, not around it. The optimizer may not
  eliminate an overflowing operation as dead unless it can prove it does not trap.
  This is a real constraint on DCE and is tested in Phase 4.
- Codegen emits `jo`/`jc` after arithmetic, and explicit compares before `div`, `%`
  and shifts. Machine-code size grows; measured, not assumed, in Phase 5.
- Reversing to wrapping would silently change the meaning of every existing Origin
  program, so this decision is effectively permanent after Phase 1.
