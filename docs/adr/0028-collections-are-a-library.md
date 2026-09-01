# ADR-0028: Collections are a library, not syntax

**Status:** accepted · **Date:** 2026-09-01 · **Decided by:** implementer (user delegated)

## Context

Phase 7 has to give Origin the collections a program cannot be written without: a growable
sequence and a keyed lookup. Phase 9 makes that concrete rather than aspirational — the
compiler has to be written in Origin, and no compiler is writable without a list and a map.

Three things were already decided and constrain this:

- **ADR-0013** — `[]` is type application. There is no index operator, and the reason is
  the lexer: `Vec[i64]` and `v[i]` are indistinguishable without parser feedback, and a
  lexer that needs feedback was rejected in Phase 1. `docs/deferred.md` carried index
  syntax as a Phase 7 item, "likely spelled `v.[i]`".
- **ADR-0010** — generics are monomorphized, so `List[i64]` and `List[Node]` are different
  types with different layouts by the time anything runs.
- **ADR-0019** — every instantiation has an exact object layout, and the collector reads a
  field's reference-ness from a per-TypeID table rather than from a tag.

`internal/layout` has carried a `RefArray` shape since Phase 2 — "a length-prefixed run of
references" — used by nothing, waiting for this.

## Options considered

1. **Collection literals and an index operator.** `[1, 2, 3]` and `v.[i]`, with `Vec` and
   `Map` as compiler-known types. Familiar, and what most languages do.
2. **An index operator only**, with construction through functions.
3. **Neither: collections are ordinary library types with ordinary methods.** `List::new()`,
   `xs.push(v)`, `xs.get(i)`, and `for x in xs` through the `Iterator` trait the language
   already has.

## Decision

**Option 3.** Collections are a library. Phase 7 adds no keywords, no operators and no
grammar productions, exactly as ADR-0025 decided for concurrency, and for the same reason:
the language already has what a library form needs — monomorphized generics, methods,
`Iterator`/`IntoIterator` driving `for`, and structural equality.

What the language *does* gain is a small set of compiler-provided operations on one new
primitive type, `Array[T]`, which is a fixed-capacity run of elements. Everything else —
`List[T]`, `Map[K, V]` — is written in Origin, in the prelude, on top of it. The runtime
knows nothing about a list.

`Array[T]` carries both a **length** and a **capacity**: the object is allocated with room
for `capacity` elements and starts empty, and `push` is what makes a slot readable. That is
not an optimization, it is what keeps ADR-0007 (no null, no uninitialized bindings) true for
a growable structure: there is no operation that reads a slot nothing has written, so no
"empty slot" value has to exist. It is also what makes structural equality right on a list —
`==` compares the elements that are there, and two lists with the same elements compare
equal whatever room they happen to have left.

`internal/layout` gains one shape, `RawArray`, beside the `RefArray` that was already
specified: an array whose elements are not references. Which one an instantiation gets is
decided at compile time from its element type, exactly as a struct's `Kinds` are.

## Consequences

- No lexer, parser or AST change in Phase 7. ADR-0013 stands untouched, and the deferred
  "index syntax" item is closed rather than done: `xs.get(i)` returns `Option[T]` and
  `xs.at(i)` traps, which is more than `v[i]` says.
- Reading an element costs a method call the optimizer must inline to make cheap. That is
  the price, and it is measurable rather than structural.
- A collection literal remains impossible to write, so building a list is a sequence of
  `push` calls. Awkward in tests, and the reason to revisit this is ergonomics rather than
  capability.
- `List` and `Map` have `mut` fields, so neither is `Send` (ADR-0014). A list cannot cross a
  channel. That falls straight out of the memory model and is correct: two threads sharing a
  growable buffer is exactly the race `Send` exists to refuse. A `Mutex[List[T]]` is how a
  list is shared.
- The prelude becomes the largest body of Origin in the project, which is the point: Phase 9
  needs the language to carry a real program, and the standard library is the first one.

## Reversing it

Adding an index operator later is additive: `v.[i]` is a new postfix production that
desugars to the same method call, and nothing about the layout or the library changes.
Collection literals are the same shape of change. Neither would invalidate this ADR; both
would be written as successors to it.
