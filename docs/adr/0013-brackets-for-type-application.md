# ADR-0013: `[]` for type application; no index operator

**Status:** accepted · **Date:** 2026-08-28 · **Decided by:** implementer (user delegated)

## Context
`Vec<T>` forces every hand-written parser to resolve `<` between "less than" and "start
of type arguments", and `>>` between "shift" and "two closing angle brackets". The usual
fixes are lexer feedback from the parser, or a turbofish (`::<>`) in expressions. Both
are ongoing costs in a compiler that must eventually be rewritten in its own language.

## Options considered
- **Angle brackets + turbofish.** Familiar; permanently entangles lexer and parser.
- **Brackets for types, brackets also for indexing.** `a[i]` and `Vec[T]` are the same
  shape; disambiguation moves to name resolution, which works but reintroduces a
  context-sensitivity — just later in the pipeline.
- **Brackets for types, no index operator at all.** `[` after an expression *always*
  begins type arguments, so `f[i64](x)` is an unambiguous explicit instantiation with no
  turbofish. Indexing becomes `v.get(i) -> Option[T]` and `v.at(i) -> T` (trapping).

## Decision
`[...]` is type application, in types and in expressions. Origin has no index operator
and no collection literals in 0.1.

## Consequences
- The lexer never needs parser feedback; `>>` is always a shift; the grammar in §02 is
  LL(2) with two documented restrictions.
- `v.get(i)` returning `Option[T]` is the ergonomic default, which pairs with ADR-0007:
  the bounds check is in the type, not in a trap.
- Numeric code that indexes heavily is more verbose. Index syntax and collection
  literals are deferred (Phase 7) and, if added, must be sugar that does not reintroduce
  parse ambiguity — most likely `v.[i]`.
