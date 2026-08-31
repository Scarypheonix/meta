# ADR-0025: Concurrency is a library, not syntax

**Status:** accepted · **Date:** 2026-08-31 · **Decided by:** implementer (user delegated)

## Context

Phase 6 adds green threads, channels and mutexes. Go — the closest reference point, and
the source of the M:N model §08 already commits to — makes two of these *statements*:
`go f()` and `select { ... }`. Rust makes all of them library items. Origin has to pick
before any of it is specified, because the choice decides whether the lexer, grammar,
AST, resolver and checker all grow.

## Options considered

- **Keywords, as in Go.** `spawn f()` reads well and makes the concurrency model visible
  in the grammar. It also means new tokens, new AST nodes, new resolution and checking
  paths, and new lowering in three engines — for constructs whose semantics are entirely
  ordinary function calls.
- **A library, as in Rust.** `thread::spawn(f)` takes a closure; a channel is a generic
  type; a mutex is a struct with a method. Nothing new below the standard library.

## Decision

Concurrency adds no keywords, no operators and no grammar productions. `spawn` is a
generic function taking a `Send` closure, `Sender[T]`/`Receiver[T]` are generic types,
and `Mutex[T]` is a struct whose only accessor takes a closure.

The language already has everything this needs: closures are first-class values with a
working native representation (ADR-0020), generics are monomorphized so `Sender[i64]` is
a concrete type by the time the backend sees it (ADR-0010), and `[]` is already type
application (ADR-0013), so `channel[i64](0)` needs no new parsing rule.

A keyword would buy visibility and cost a pass through every layer of the compiler. The
one thing it could buy that a function cannot — special evaluation order, as `go`'s
argument evaluation has — is not wanted: `spawn(f)` evaluating `f` before starting a
thread is exactly what §04's evaluation order already says.

## Consequences

- A program that never imports `std::thread` is unaffected by Phase 6 in every stage of
  the compiler. That is worth a great deal for a project that must keep three engines in
  agreement.
- `select` cannot be a statement later without becoming an exception to this. It is
  deferred (`docs/deferred.md`, Phase 7), and when it lands it should be a function over
  a list of cases rather than a control-flow construct, or this ADR should be revisited
  deliberately rather than by accident.
- The `Send` bound lands on the closure passed to `spawn`, not only on the values it
  names. A closure is `Send` when its captures are (§08), so capture is checked by the
  same rule as a channel send, in one place.
- Error messages are library-shaped: "the closure passed to `spawn` is not `Send`"
  rather than a syntax error. Diagnostics `E0700`–`E0702` name the offending field or
  capture, because the trait bound alone does not tell a programmer what to change.
