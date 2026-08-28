# ADR-0011: Single-parameter traits with associated types; coherence by orphan + overlap

**Status:** accepted · **Date:** 2026-08-28 · **Decided by:** implementer (user delegated)

## Context
How much of a trait system to build. Each increment (multi-parameter traits, associated
types, GATs, specialization, trait objects) costs checker complexity and inference
ambiguity.

## Options considered
- **Type-parameter traits only** (`trait Iterator[Item]`). Simpler unification; every
  use site must mention `Item`, and nothing stops one type implementing
  `Iterator[i64]` and `Iterator[String]`, which destroys inference for `for` loops.
- **Associated types.** `Iterator` has one type parameter fewer at every use site, and
  the output type is *functionally determined* by the implementing type, which is what
  makes `for x in it` infer. Costs projection normalization in the unifier.
- **Everything** (multi-param + GATs + specialization). Specialization interacts badly
  with coherence and is an open research area in Rust; GATs need higher-kinded
  machinery.

## Decision
Traits have exactly one implementing type (`Self`), may declare associated types and
supertraits, and may provide default methods. Bounds are written inline or in `where`.
Coherence is enforced globally by the orphan rule (implement a foreign trait only for a
local type, or a local trait for any type) plus an overlap check by unification. No
multi-parameter traits, no GATs, no specialization, no trait objects in 0.1.

## Consequences
- `for` loops and iterator chains infer cleanly (`Self::Item` is determined by `Self`).
- Coherence is checkable with one unification per impl pair per trait, and the error
  names both impls.
- `Eq` is deliberately not a trait: `==` is compiler-generated and structural (spec §04),
  which removes the single most common motivation for `derive` and lets 0.1 ship with no
  attribute syntax at all.
- No dynamic dispatch means no heterogeneous collections in 0.1; the stdlib is designed
  around generics instead. Deferred to Phase 7.
