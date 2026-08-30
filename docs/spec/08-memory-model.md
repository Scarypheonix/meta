# 08 — Memory Model, Object Model and Concurrency

## Value categories

Every Origin type is exactly one of:

- **Unboxed**: `i8`…`u64`, `f32`, `f64`, `bool`, `char`, `()`, fieldless enums, and
  tuples all of whose elements are unboxed. Copied on assignment and on argument
  passing. Never allocated.
- **Boxed**: structs, enums with a payload, `String`, function values with captures,
  and tuples containing a boxed element. Allocated on the GC heap and referred to by a
  reference. Assignment and argument passing copy the *reference*, not the object
  (ADR-0008).

There is no syntactic distinction between the two and no `Box`/`&`/`*` in the source
language. The consequences of the split are observable in exactly two places, both
specified: mutation through an alias (below), and `ref_eq` (§04).

## Mutation and aliasing

Origin has no borrow checker and no aliasing restrictions. Two bindings may refer to the
same object, and a mutation through one is visible through the other:

```origin
struct Counter { mut n: i64 }
let a = Counter { n: 0 };
let b = a;          // b and a refer to the same object
b.n = 5;
// a.n is now 5
```

Mutability is a property of a **field declaration**, not of a reference or a path. A
field declared `mut` is mutable through every reference to that object, forever; a field
not declared `mut` is fixed at construction and can never change. There is no way to
obtain a read-only view of an object with `mut` fields. This is a deliberate trade: it
buys a language with no lifetimes, no borrow errors, and a GC that never has to reason
about uniqueness (ADR-0004, ADR-0008).

`let mut x` controls only whether the *binding* can be re-pointed, which is orthogonal.

## Initialization

There are no uninitialized values and no zero values. Every binding has an initializer,
every struct literal supplies every field, and every enum payload is supplied at
construction. Recursive data is built bottom-up or through a `mut` field assigned after
construction. DEFERRED: cyclic-structure construction helpers (Phase 7).

## The garbage collector

Phase 3 delivers a **precise, generational, moving** collector. The language-level
guarantees, which the implementation MUST satisfy and which the property tests in
`tests/gc/` enforce:

1. An object reachable from a root is never collected. Roots are: live locals and
   temporaries in every green thread's stack, globals, and the values held by the
   runtime (channel buffers, scheduler queues).
2. An object unreachable at the start of a collection is collected by the end of the
   second full collection at the latest.
3. Collection is invisible to program semantics apart from timing. Object identity is
   preserved across moves (`ref_eq` is stable). Addresses are not observable — there is
   no way to obtain one in safe Origin, which is why moving is sound.
4. There are no finalizers and no weak references in 0.1. An object's storage is
   reclaimed with no user code run. DEFERRED: both (Phase 7).
5. Allocation may TRAP with `out of memory`; it never returns a null or invalid
   reference.

These five guarantees are the language's own contract, satisfied identically by every
engine; nothing about "generational" is part of it. The interpreter and the VM share
`internal/gc`'s precise, generational, moving collector; the native backend's own
collector (ADR-0022, spec/11-codegen.md's "Safepoints and stack maps") is a single-space,
stop-the-world semispace copy — a deliberately simpler shape a hand-assembled freestanding
runtime can get right first, satisfying the same five guarantees without generations.

**Safepoints.** The compiler inserts safepoints at every function entry, every loop
back-edge, and every allocation. A collection may begin only at a safepoint, and at a
safepoint the stack map for the current call site describes exactly which stack slots
and registers hold references. Between safepoints, generated code may hold derived or
interior pointers; across a safepoint it MUST NOT. This is the single invariant that
Phase 3 (GC) and Phase 5 (backend) must agree on, so it lives in one module —
`internal/layout` owns object layout, stack maps and safepoint placement, with its own
test suite, and neither the GC nor the backend duplicates the knowledge (process rule 5).

**Write barriers.** Every store of a reference into a `mut` field of a heap object goes
through a write barrier that records old-to-young references in a card table. The
optimizer MAY elide a barrier only when it can prove the target object is in the
nursery.

## Stack

Green threads start with a small stack that grows on demand (Phase 6). Exhausting the
growth limit TRAPS with `stack overflow`. Deep recursion is therefore a trap, never
memory corruption. The default limit is 8 MiB per green thread, configurable at spawn.

## Concurrency

Phase 6 delivers green threads over an M:N scheduler with channels and kqueue-backed
async I/O. The memory model at the language level:

- **Threads share the heap**, but what may cross a channel is restricted: a value sent
  on a channel MUST implement `Send`.
- `Send` is a marker trait implemented automatically for: unboxed types, `String`,
  aggregates all of whose fields are non-`mut` and whose types are `Send`, and function
  values whose captures are all `Send`.
- An aggregate with any `mut` field is **not** `Send` and cannot cross a channel. To
  share mutable state, send a `Mutex[T]`, which is `Send` for `T: Send` and whose only
  accessor takes a closure holding the lock.

The result: **there are no data races in safe Origin 0.1**, achieved without ownership
tracking, by making the only shareable mutable thing a `Mutex`. The cost is that
patterns like a shared mutable graph across threads require explicit locking — which is
the correct cost.

Within a single green thread, execution is sequentially consistent and matches §04's
evaluation order. Across threads, `Mutex` acquire/release are the only ordering
primitives; atomics are DEFERRED (Phase 6, if the scheduler needs them exposed).

Scheduling is **preemptive at safepoints**: a green thread that runs a loop containing a
back-edge can always be descheduled, so a compute loop cannot starve the scheduler. A
green thread calling into C via FFI is not preemptible and occupies its OS thread until
it returns.

## Worked examples

| Program | Behaviour |
|---|---|
| `let a = C { n: 0 }; let b = a; b.n = 5;` | `a.n == 5` — shared object |
| `let a = (1, 2); let mut b = a; b = (3, 4);` | `a == (1, 2)` — tuples of primitives are unboxed |
| `let s = "hi"; let t = s;` | same object; `String` is immutable so it cannot matter |
| `ref_eq(a, b)` after `let b = a` | `true`, and stays `true` across collections |
| `ref_eq(C { n: 0 }, C { n: 0 })` | `false` — distinct allocations |
| `C { n: 0 } == C { n: 0 }` | `true` — `==` is structural |
| a 10M-object allocation loop with a live set | no leak, no premature collection (Phase 3 stress test) |
| sending `C { mut n: i64 }` on a channel | REJECTED — `C` is not `Send` |
| sending `Point { x: f64, y: f64 }` (no `mut`) | accepted |
| sending `Mutex[C]` | accepted |
| recursion 10M deep | TRAPS `stack overflow` |
| `for` loop with an empty body, another thread ready | the other thread runs — safepoint on the back-edge |
