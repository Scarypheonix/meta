# ADR-0010: Generics are monomorphized

**Status:** accepted · **Date:** 2026-08-28 · **Decided by:** implementer (user delegated)

## Context
Generic code must either be specialized per type or compiled once against a uniform
representation with runtime dictionaries.

## Options considered
- **Dictionary passing (Haskell, Swift).** One copy of the code; small binaries; fast
  compiles. Requires a uniform value representation — every type parameter boxed, or a
  value-witness table describing size, alignment, copy and GC layout at runtime. Every
  trait method call becomes an indirect call the optimizer cannot see through.
- **Monomorphization (Rust, C++).** One copy per instantiation; all trait calls become
  direct calls; exact layout known statically. Code size and compile time grow with the
  instantiation count.
- **Hybrid.** Both mechanisms and the rules for choosing between them; twice the work.

## Decision
Monomorphize. Emit one specialized copy per distinct type-argument tuple, resolving
every trait method call to a direct call.

## Consequences
- **The backend never sees a type variable.** Register allocation, struct layout and
  calling convention are entirely static — which is what makes a hand-written x86-64
  backend feasible at this scale.
- **The GC gets an exact layout per instantiation**, so precise root scanning needs no
  runtime type information at all. Combined with ADR-0008, the collector's view of the
  heap is fully static.
- No trait objects in 0.1 — dynamic dispatch needs the uniform representation
  monomorphization avoids. Deferred to Phase 7.
- Compile time and binary size are the risk, and they hit hardest in Phase 9 when the
  compiler compiles itself. Mitigations, in order: hash-based deduplication of
  structurally identical instantiations (Phase 5), cross-build instantiation caching
  (Phase 8). If the 60-second incremental budget is threatened, this ADR gets revisited
  rather than the budget.
- Polymorphic recursion is rejected at instantiation depth 64 (spec §06), because it
  makes the instantiation set infinite.
