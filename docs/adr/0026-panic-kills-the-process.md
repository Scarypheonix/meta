# ADR-0026: A panic kills the process; per-thread isolation stays deferred

**Status:** accepted · **Date:** 2026-08-31 · **Decided by:** implementer (user delegated)

## Context

`docs/deferred.md` calls per-green-thread panic isolation "the single largest deferred
item", and §09 defers it *to Phase 6*. This is Phase 6, so it is decided here rather than
deferred again by default.

Today a panic — and every trap in §04's table — writes `origin: <msg>` and the source
location to stderr and exits 101, killing every green thread with it.

## Options considered

- **Unwinding.** A panicking thread runs cleanup as its frames unwind, releasing locks;
  the scheduler reaps it and other threads continue. This is Rust's model. ADR-0006 ruled
  out unwinding for the language, and reversing that decision is not a Phase 6 change: it
  changes the calling convention, the backend's frame layout, every trap site, and the
  meaning of "errors are values", which is a spec pillar.
- **A supervisor model.** A panicking thread dies; a supervisor observes it and restarts
  the work. This is Erlang's model, and Erlang can offer it because processes share
  *nothing*: a dead process's state is unreachable by definition. Origin shares the heap
  (ADR-0008) and shares mutable state through `Mutex` (ADR-0014). A thread that panics
  inside `with` dies holding a lock, over a value it was part-way through mutating.
- **Poisoning without unwinding.** Mark a mutex whose holder died, and make later
  acquisitions fail. This is coherent, but with no unwinding there is no moment at which
  the dying thread can mark anything: the process is already exiting. It would have to be
  the scheduler's job, and the scheduler would have to know which locks a thread held —
  which means lock bookkeeping on every acquire, for a case that ends the process anyway.
- **Keep killing the process.**

## Decision

A panic kills the process, including every green thread. Per-thread isolation remains
deferred, now with a reason rather than a deadline.

The reason is not effort. It is that isolation and a shared mutable heap are in tension,
and Origin has already chosen the shared heap. A surviving thread that acquires a lock
released by a thread that died mid-mutation reads a value that satisfies no invariant its
own code was written against. That is not a crash — it is a wrong answer, produced by a
program that contains no error at the point it goes wrong, which is precisely what §04's
"every operation produces a defined value or traps" exists to prevent. Between a process
that stops and a survivor that silently computes nonsense, a language with no undefined
behaviour has to choose the former.

## Consequences

- **Phase 6 ships with the failure model Phase 5 had.** `panic` and every trap behave
  identically whether one thread is running or eight. §09 needs no amendment.
- A long-running server in Origin 0.1 cannot survive a panic in one request handler. That
  is a real limitation and belongs in `docs/deferred.md` as one, not in a footnote.
- **Isolation becomes reachable if unwinding ever arrives**, and only then. If ADR-0006
  is ever revisited — for `?` on `Option`, or for FFI, or for anything else — this ADR
  should be revisited in the same breath, because the option it rejected was rejected on
  ADR-0006's grounds and not on its own.
- Deadlock detection matters more because of this. A process that cannot lose a thread to
  a panic can still lose every thread to a wait, so the scheduler traps
  `all threads are blocked` (§12) rather than hanging — the one failure mode this
  decision does not already turn into an exit.
