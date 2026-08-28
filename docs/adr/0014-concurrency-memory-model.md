# ADR-0014: Shared heap, `Send`-gated channels, `Mutex` as the only shared mutable thing

**Status:** accepted · **Date:** 2026-08-28 · **Decided by:** implementer (user delegated)

## Context
Phase 6 adds M:N green threads. With ADR-0004's aliasing-permissive mutation and
ADR-0008's shared heap, unrestricted sharing means data races — and Origin has no
ownership tracking with which to prevent them.

## Options considered
- **Go's model: share anything, races are the programmer's problem.** Simple to
  implement, and it means a language with no undefined behaviour elsewhere has undefined
  behaviour here, contradicting spec pillar 1.
- **Full isolation: deep-copy or move everything across channels (Erlang).** No races by
  construction; expensive, and it makes a shared cache or graph impossible.
- **`Send` as a derived marker.** ADR-0004 already makes "has a `mut` field" a
  syntactic, type-level property. So: a type is `Send` iff it is unboxed, or `String`, or
  an aggregate with no `mut` fields whose fields are all `Send`. Mutable aggregates
  simply cannot cross a channel. Shared mutable state goes through `Mutex[T]`, which is
  `Send` and whose only accessor holds the lock.

## Decision
Threads share the heap. Channel sends require `Send`, derived automatically by the rule
above. `Mutex[T]` is the sole route to cross-thread mutation.

## Consequences
- **There are no data races in safe Origin 0.1**, with no borrow checker and no
  ownership types — the guarantee falls out of ADR-0004's field-level mutability.
- `Mutex` acquire/release are the only cross-thread ordering primitives; atomics are
  deferred to Phase 6 and only if the scheduler needs them exposed.
- Immutable structures become the natural way to share data, which also suits the
  generational GC (no old-to-young write barriers for immutable fields).
- The cost: a shared mutable graph needs explicit locking, and a lock-free structure
  cannot be written in safe Origin at all.
- Scheduling is preemptive at safepoints (function entry, loop back-edges, allocation),
  so a compute loop cannot starve the scheduler.
