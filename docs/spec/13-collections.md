# 13 — Collections

Phase 7. This document is normative. It specifies Origin's growable sequence and keyed
lookup: what they are, what they guarantee, and what the compiler provides underneath them.

Nothing here is syntax. Phase 7 adds **no keywords, no operators and no grammar
productions** (ADR-0028), exactly as Phase 6 added none: `List` and `Map` are ordinary
generic types with ordinary methods, `for` walks them through the `Iterator` trait §06
already specifies, and there is no index operator and no collection literal. A program that
never names a collection compiles exactly as it did before Phase 7.

## Array

`Array[T]` is a **fixed-capacity, growable-length** run of elements. It is a built-in type
like `String` — nameable anywhere, with no declaration and no literal syntax — and it is the
one thing the compiler provides; `List` and `Map` are written in Origin on top of it.

An array is created empty with room for `capacity` elements, and `push` is what makes a slot
exist. There is no operation that reads a slot nothing has written — which is what keeps
§04's "no uninitialized bindings" true for a structure that grows, without inventing an
"empty" value for a slot to hold (ADR-0007 rules that out).

```origin
use std::array;

fn array::new[T](capacity: i64) -> Array[T]        // TRAPS `array capacity is negative`
fn array::len[T](a: Array[T]) -> i64
fn array::cap[T](a: Array[T]) -> i64
fn array::at[T](a: Array[T], i: i64) -> T          // TRAPS `index out of range`
fn array::set[T](a: Array[T], i: i64, v: T) -> ()  // TRAPS `index out of range`
fn array::push[T](a: Array[T], v: T) -> bool       // false when the array is full
fn array::truncate[T](a: Array[T], n: i64) -> ()   // no-op when n >= len
```

They are free functions rather than methods because a built-in type has no `impl` block to
put a method in, and every one of them returns a primitive: nothing here builds an `Option`,
because the runtime has no way to construct one and no business knowing what one is (the
same rule Phase 6 followed). `get`, which does return an `Option`, is `List`'s and is written
in Origin.

- `len` is how many elements have been pushed; `cap` is how many the object has room for.
  `len <= cap` always.
- `push` returns `false` rather than growing: an array never reallocates, and a caller that
  wants growth is `List`.
- `truncate` shortens the length. The elements above it become unreachable through this
  array — the collector traces exactly `len` of them — and a later `push` overwrites them.
- Equality is element-wise: two arrays are `==` when their lengths are equal and their
  elements are pairwise `==`. Capacity is not compared, which is what makes `==` on a list
  mean what it should.

An array's elements live in the object itself, so an `Array[T]` of *n* elements is one
allocation and one indirection, not *n* of either.

## List

```origin
pub struct List[T]
```

A growable sequence. The capacity doubles when it runs out, so *n* pushes cost O(*n*).

The constructor is a free function rather than `List::new`, because Origin has no
associated functions: `impl` blocks hold methods, and a path like `List::new` resolves to
nothing (`docs/deferred.md`, Phase 8). `list::new` is the same thing under a name the
language can express, and it is compiler-provided only in the sense that the compiler
writes out the two operations it stands for -- an empty array, and the prelude's own struct
around it. No engine implements it.

```origin
use std::list;

fn list::new[T]() -> List[T]
fn (l: List[T]) len() -> i64
fn (l: List[T]) is_empty() -> bool
fn (l: List[T]) push(v: T) -> ()
fn (l: List[T]) pop() -> Option[T]
fn (l: List[T]) get(i: i64) -> Option[T]
fn (l: List[T]) at(i: i64) -> T               // TRAPS `index out of range`
fn (l: List[T]) set(i: i64, v: T) -> ()       // TRAPS `index out of range`
fn (l: List[T]) clear() -> ()
```

`List[T]` implements `IntoIterator`, so `for x in xs` walks it in order, front to back. The
iterator yields each element once and does not observe a `push` that happens during the walk.

A `List` has `mut` fields, so it is **not `Send`** (§08, ADR-0014): a list cannot cross a
channel, because two threads sharing a growable buffer is exactly the race `Send` refuses.
`Mutex[List[T]]` is how a list is shared.

## Map

```origin
pub struct Map[K, V]
```

A keyed lookup. Keys are compared with `==` — structural equality, which every type has
(§04, ADR-0011) — so any type may be a key.

```origin
use std::map;

fn map::new[K, V]() -> Map[K, V]
fn (m: Map[K, V]) len() -> i64
fn (m: Map[K, V]) is_empty() -> bool
fn (m: Map[K, V]) insert(k: K, v: V) -> Option[V]   // the value it replaced, if any
fn (m: Map[K, V]) get(k: K) -> Option[V]
fn (m: Map[K, V]) contains(k: K) -> bool
fn (m: Map[K, V]) remove(k: K) -> Option[V]
fn (m: Map[K, V]) keys() -> List[K]
fn (m: Map[K, V]) values() -> List[V]
```

Lookup is a hash table; iteration is a separate, insertion-ordered list of entries. A
`remove` rebuilds both, so it costs O(*n*) where the rest are O(1) amortized — removals are
rare in the programs this phase is for, and the alternative is a tombstone in every probe.

**Iteration is in insertion order.** `keys()`, `values()` and `for` yield entries in the
order they were first inserted; re-inserting an existing key updates its value and leaves its
position alone; `remove` takes an entry out of the order entirely.

That is a guarantee about the language, not an accident of the implementation, and it is
there for a specific reason: a hash order would differ between the three engines, and a
program that printed a map would then not be a valid differential case. Insertion order makes
every `Map` program deterministic and comparable, and it is the order a person expects when
they print one.

A key must not be mutated while it is in a map. Nothing prevents it — a `mut` field is a
`mut` field — and the result is defined but useless: the key hashes differently than it did,
so `get` probes elsewhere and answers `None` for an entry that is still in `keys()`. That is
a wrong answer the program asked for rather than undefined behaviour, and it is the one
place where §04's guarantee is a guarantee about the language and not about the program.

`Map[K, V]` implements `IntoIterator`, yielding `Entry[K, V]` — a struct with `key` and
`value` fields — in insertion order.

## Hashing

```origin
use std::hash;

fn hash::of[T](v: T) -> i64
```

`hash::of` is what `Map` probes with, and it is **specified rather than left to the
implementation**, because the three engines have to agree on every value a program can
print. It is 64-bit FNV-1a over a canonical encoding of the value:

```
h = 14695981039346656037
for each byte b:  h = (h XOR b) * 1099511628211      (wrapping, unsigned)
```

and the bytes are:

| Value | Bytes fed to the hash |
|---|---|
| `i8`–`i64`, `u8`–`u64`, `char`, `bool` | the value's word, sign- or zero-extended to 64 bits, little-endian |
| `f32`, `f64` | the IEEE bits of the value widened to `f64`, little-endian, with `-0.0` normalized to `0.0` first |
| `()` | nothing at all |
| `String` | its bytes, in order |
| a struct, tuple, enum variant or closure | each field's own hash in turn, as eight little-endian bytes |
| an `Array[T]` or a `List[T]` | each element's own hash in turn, as eight little-endian bytes |

The result is reinterpreted as a signed `i64`, so it may be negative; `Map` masks it before
probing.

Two rules follow, and both are what a hash is for: **equal values hash equally** — the
encoding above reads exactly what `==` compares, field for field and element for element,
which is why `-0.0` is normalized (`0.0 == -0.0`) and why a type's identity is *not* mixed
in (two different types are never equal, so a collision between them costs a comparison and
nothing else). And unequal values may hash equally, which is a collision and not a bug.

## What the compiler provides

`Array[T]`'s operations and `hash::of`. `List` and `Map` are Origin source in the prelude,
which is the point of ADR-0028: the runtime knows what an array is, how to hash a value, and
nothing at all about a list or a map.

The object layout is `internal/layout`'s, as everything else is (§08, ADR-0019): an array is
a length-prefixed run, either `RefArray` when its elements are references or `RawArray` when
they are not. Which one an instantiation gets is decided while compiling, from the element
type, exactly as a struct's per-word kinds are — so the collector reads an array's shape from
the same per-TypeID table it already reads a struct's from, and never has to guess whether a
word is a pointer.

## Errors

| Condition | Result |
|---|---|
| `array::new[T](-1)` | TRAPS `array capacity is negative` |
| `array::at(a, i)` or `array::set(a, i, v)` with `i < 0` or `i >= len` | TRAPS `index out of range` |
| `l.at(i)` or `l.set(i, v)` with `i < 0` or `i >= len` | TRAPS `index out of range` |
| `l.get(i)` out of range | `None` — not a trap |
| `array::push(a, v)` on a full array | `false` — not a trap |
| `m.get(k)` for an absent key | `None` |

Every one of these is defined: §04's "no undefined behaviour" leaves no room for an
out-of-range read to return whatever is next in memory.

## Worked examples

| Program | Output |
|---|---|
| `let xs = list::new[i64](); xs.push(1); xs.push(2); io::println(xs.len().to_str());` | `2` |
| push 0..99, then sum with `for` | `4950` |
| `xs.get(7)` on a three-element list | `None` |
| `array::at(a, 7)` on a three-element array | TRAPS `index out of range`, exit 101 |
| `xs.at(7)` on a three-element list | TRAPS `index out of range`, exit 101 |
| `xs.pop()` until `None`, printing each | the elements in reverse |
| two lists with the same elements and different capacities | `==` |
| `array::new[i64](-1)` | TRAPS `array capacity is negative`, exit 101 |
| `array::push` on a full array | `false`, and the array is unchanged |
| `m.insert("a", 1); m.insert("b", 2); m.insert("a", 3);` then `keys()` | `a b` — `a` keeps its place |
| `m.get("missing")` | `None` |
| `m.remove("a")` then `m.get("a")` | `Some(3)`, then `None` |
| 1000 insertions, then `len()` | `1000` |
| a `Map[String, List[i64]]` built, mutated through, and printed | deterministic on all three engines |
| `hash::of(0.0) == hash::of(-0.0)` | `true` — the encoding normalizes it, because `==` does |
| `hash::of` of the same value on three engines | the same number |
| a `List[Node]` surviving a collection that moves every element | the same elements |

Every row is a case in `tests/e2e/cases/`, asserting exact stdout, stderr and exit status on
all three engines at every optimization level — which is what makes this table normative
rather than aspirational.
