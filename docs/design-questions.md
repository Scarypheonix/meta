# Phase 0 Design Questions

Process rule 7 reserves language design decisions for the user. This file records what
was asked, what came back, and where each answer now lives — so that "the user decided"
and "the implementer decided under delegation" are never confused later.

## What was asked

Two logistics questions were put first because they gated where files could go:

1. **Repo layout** — the repository was not empty; it contained a static website.
2. **Object format** — the mandate is Mach-O on a 2017 MacBook Air, but development
   happens in a Linux container where Mach-O binaries cannot be executed, `lldb` is
   unavailable, and the local `clang` targets ELF.

Ten language questions were queued behind them: mutability defaults, integer overflow,
error handling model, nullability, traits and associated types, generics strategy,
evaluation order, numeric coercion, the concurrency memory model, and struct value vs.
reference semantics.

## What the user answered

> "whatever you want" · "your wish dont ask me anything. i dont know shit."

The user delegated all of it, including the queued language questions, which were
therefore never put. This is a standing delegation for Phase 0's design decisions. It is
**not** a delegation of the phase gates: `docs/phases/N-complete.md` still goes to the
user for review, and the Phase 5 criteria that only the target machine can verify still
need the user to run them.

## What was decided under that delegation

Every answer is an ADR. Each ADR states the options that were rejected and what it would
cost to reverse it, so any of these can be overturned by reading one file and writing a
successor ADR.

| Question | Decision | ADR |
|---|---|---|
| Repo layout | site moved to `site/`, Origin owns the root | ADR-0002 |
| Object format | Mach-O + ELF writers behind one interface | ADR-0003 |
| Mutability defaults | immutable by default; `mut` on fields, no borrow checker | ADR-0004 |
| Integer overflow | traps at every optimization level; explicit wrapping methods | ADR-0005 |
| Error handling | `Result` + `?`; traps for bugs; no unwinding | ADR-0006 |
| Nullability | no null; `Option[T]` | ADR-0007 |
| Value vs. reference semantics | primitives unboxed, aggregates by reference | ADR-0008 |
| Inference strategy | mandatory signatures, intra-procedural HM, value restriction | ADR-0009 |
| Generics | monomorphization | ADR-0010 |
| Traits | single-parameter with associated types; orphan + overlap coherence | ADR-0011 |
| Evaluation order and coercion | strict left-to-right; zero implicit coercions | ADR-0012 |
| Generic syntax | `[]` for type application; no index operator | ADR-0013 |
| Concurrency memory model | shared heap, `Send`-gated channels, `Mutex` only | ADR-0014 |
| AST representation (implementation) | sealed interface, side tables | ADR-0015 |

## The decisions most worth a second look

If the user reads only three of these, these are the three with the largest blast radius
and the least obvious answer:

- **ADR-0005 (overflow traps everywhere).** Costs a branch per arithmetic op and
  constrains dead-code elimination. Chosen because Rust's debug-trap/release-wrap split
  would make Phase 4's `-O` differential harness unable to distinguish a legal semantic
  difference from a miscompilation.
- **ADR-0008 (aggregates by reference).** Makes `let b = a; b.n = 5` visible through
  `a`. Chosen because a moving collector that never sees an interior pointer is the
  difference between Phase 3 being hard and being intractable.
- **ADR-0004 + ADR-0014 (field-level mutability, `Send` derived from it).** Together
  these give a data-race-free language with no borrow checker and no ownership types.
  The cost is that a lock-free structure cannot be written in safe Origin at all.

---

## Phase 6 (concurrency)

Asked, at the Phase 5 gate: what a spawned task *is* in the language, and what happens
when one panics — the two questions the concurrency specification could not be written
without.

The user answered:

> "your wish is my wish."

Delegated, on the Phase 0 pattern. The answers live in ADR-0025 (concurrency is a
library, not syntax) and ADR-0026 (a panic kills the process; per-thread isolation stays
deferred, now with a reason rather than a deadline), and the surface they imply is
`docs/spec/12-concurrency.md`. Both are overturnable by reading one file, which is the
point of recording them this way.

Note that most of Phase 6's *semantics* were not open questions at all: ADR-0014 fixed
the memory model and §08 fixed green threads, `Send`, safepoint preemption and stack
limits, all under the Phase 0 delegation. What was genuinely undecided was the surface
and the failure model.

The phase gates remain the user's: `docs/phases/6-complete.md` goes to them for review,
as Phase 5's did.
