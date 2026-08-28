# ADR-0008: Aggregates are heap objects with reference semantics; primitives are unboxed

**Status:** accepted · **Date:** 2026-08-28 · **Decided by:** implementer (user delegated)

## Context
Whether `struct` values are copied on assignment (Go, C, Rust) or shared by reference
(Java, OCaml, Python). This decision propagates into the GC, the calling convention,
struct layout, monomorphization and the optimizer.

## Options considered
- **Value semantics with explicit pointers (Go).** Predictable copies, no allocation for
  small structs; requires a pointer type in the source language, makes recursive types
  need explicit indirection, and gives the moving GC interior pointers to track.
- **Uniform boxing of everything, including integers.** Simplest possible GC; slow
  enough to be a problem for a compiler that must compile itself in a bounded time.
- **Split: primitives unboxed, aggregates boxed.** Aggregate identity is a machine word;
  the GC only ever tracks whole-object references; recursive types are free; argument
  passing is uniform.

## Decision
Primitives (`iN`, `uN`, `fN`, `bool`, `char`, `()`, fieldless enums, and tuples of
those) are unboxed values. Structs, payload-carrying enums, `String`, capturing lambdas
and tuples containing an aggregate are GC-allocated and referred to by reference.

## Consequences
- **The moving GC never sees an interior pointer.** Root scanning is per-word and
  precise; a moved object needs exactly one forwarding word. This is the reason the
  Phase 3 collector is tractable at all.
- Recursive `enum List[T] { Nil, Cons(T, List[T]) }` needs no `Box` (spec §03).
- Aliasing is observable and specified (§08, example 10) rather than accidental.
- The cost is allocation pressure: a two-field `Point` costs a heap object. **This is
  precisely what Phase 4's escape analysis is for** — a non-escaping aggregate whose
  fields are provably not aliased past its scope can be exploded into registers or a
  stack slot. Boxed-by-default with escape analysis is a coherent arc; value-semantics-
  by-default would have needed the same analysis in reverse.
- Reversing this after Phase 3 would mean rewriting the collector.
