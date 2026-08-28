# ADR-0007: No null; absence is `Option[T]`

**Status:** accepted · **Date:** 2026-08-28 · **Decided by:** implementer (user delegated)

## Context
Whether a reference type has an inhabitant meaning "nothing".

## Options considered
- **Null references.** Every dereference becomes a potential trap and every type's
  domain silently gains a member.
- **Nullable types `T?` with flow-sensitive narrowing.** Ergonomic; adds flow analysis
  to the type checker and a second, parallel notion of "optional" alongside `Option`.
- **No null; `Option[T]` as an ordinary enum.** Zero language machinery — it falls out
  of ADTs and pattern matching, which exist anyway.

## Decision
No null, no zero values, no uninitialized bindings. `Option[T]` is a prelude enum.

## Consequences
- Every binding has an initializer and every struct literal supplies every field
  (spec §08). There is no `Default`.
- The GC never sees a null reference, so root scanning has no null case.
- Ergonomics depend on `Option` combinators in the stdlib (Phase 7) and on `?` for
  `Option`, which is deferred to Phase 7.
- Nullable-pointer optimization (representing `Option[T]` for boxed `T` as a possibly-
  null machine word) remains available as a pure representation choice in Phase 5
  without exposing null in the language.
