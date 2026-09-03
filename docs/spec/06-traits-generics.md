# 06 — Traits, Generics and Coherence

## Traits

A trait is a named set of required methods and associated types.

```origin
pub trait Iterator {
    type Item;
    fn next(mut self) -> Option[Self::Item];

    fn count(mut self) -> u64 {          // default method
        let mut n = 0u64;
        loop {
            match self.next() {
                Option::None => break,
                Option::Some(_) => { n = n + 1; }
            }
        }
        n
    }
}
```

- Traits have exactly one implementing type, referred to as `Self`. Multi-parameter
  traits are DEFERRED (Phase 7).
- **Associated types** are supported and are the reason `Iterator` above needs no type
  parameter. They are projected as `Self::Item` inside the trait and `T::Item` (or
  `<T as Iterator>::Item` when ambiguous) outside it.
- **Supertraits**: `trait Ord: Eq { ... }` requires every implementor of `Ord` to
  implement `Eq`, and makes `Eq`'s methods callable on a generic `T: Ord`.
- **Default methods** may call required methods. A default method body is type-checked
  once against the trait's own bounds, not per impl.
- There are no trait objects in 0.1. `dyn` is a reserved word; dynamic dispatch is
  DEFERRED (Phase 7).

## Impls

```origin
impl Iterator for Counter {
    type Item = u64;
    fn next(mut self) -> Option[u64] { ... }
}

impl[T] Stack[T] {                  // inherent impl
    pub fn push(mut self, v: T) { ... }
}
```

An impl MUST define every required method and every associated type, with signatures
that match after substituting `Self`. A mismatch is REJECTED showing both signatures.
An impl MUST NOT define a method the trait does not declare.

### Bounds on an associated type

An associated type declaration takes bounds, and they work in both directions like every
other bound:

```origin
pub trait IntoIterator {
    type Item;
    type Iter: Iterator;
    fn into_iter(self) -> Self::Iter;
}
```

- **Inside the trait**, `Self::Iter: Iterator` is in scope in every default body, so a
  default method may call `next` on one.
- **At each impl**, whatever the impl defines the associated type to be MUST satisfy the
  bound, or the impl is REJECTED (`E0277`) naming the type it chose and the bound it
  missed. Checking it there is what lets the default bodies rely on it.

### An impl's own bounds

An impl's generic parameters take bounds, inline or in a `where` clause, and the two
spellings mean the same thing:

```origin
impl[T: Show] Show for Box[T] { ... }
impl[T] Show for Box[T] where T: Show { ... }
```

Those bounds work in both directions, and both are REQUIRED:

- **Inside**, they are in scope in every method body, so `self.item.to_str()` is a
  method call the checker can find.
- **Outside**, they are obligations of every call that selects the impl. `Box[Opaque]`
  gets a `to_str` only if `Opaque` has one; a call where it does not is REJECTED
  (`E0277`), naming the type that fails the bound rather than the impl.

## Coherence

Two rules, both checked globally at link time over the whole program:

1. **Orphan rule.** `impl Trait for Type` is permitted only in the module tree of the
   package that declares `Trait` or the package that declares `Type`. This is what makes
   "there is at most one impl" a property a package author can rely on.
2. **Overlap rule.** No two impls of the same trait may apply to the same type. Because
   0.1 has no specialization and no blanket-impl subtleties beyond bounded generics,
   overlap is decided by unifying the two impls' self types under their generic
   parameters; if they unify, both impls are REJECTED, each with a span pointing at the
   other.

Coherence violations name both impls and the type at which they conflict.

## Method resolution

For `receiver.name(args)`:

1. Collect **inherent** methods named `name` on the receiver's type. If exactly one, use
   it. If more than one, REJECTED as ambiguous.
2. Otherwise collect trait methods named `name` from traits that are (a) in scope via
   `use` and (b) implemented for the receiver's type or implied by its bounds. If
   exactly one, use it. If more than one, REJECTED with a note listing the candidates
   and the fully-qualified syntax `Trait::name(receiver, args)` for each.
3. Otherwise REJECTED: "no method `name` on `T`", plus a note if a trait providing it
   exists but is not in scope, naming the `use` that would fix it.

There is no autoref/autoderef: the receiver's type must match exactly. This is a real
ergonomic cost paid for a resolution algorithm that fits on this page.

## Generics: monomorphization

Generic functions and impls are **monomorphized** (ADR-0010): the compiler emits one
specialized copy per distinct tuple of type arguments actually used, with all type
parameters substituted and all trait method calls resolved statically to direct calls.

Consequences the rest of the compiler depends on:

- No runtime dictionaries, no vtables, no boxing of type parameters. The backend
  (Phase 5) never sees a type variable, which is why register allocation and struct
  layout can be entirely static.
- The GC (Phase 3) gets an exact layout per instantiation, so root scanning is precise
  without runtime type reflection.
- Cost: code size, and compile time proportional to instantiation count. Mitigations,
  in order of when they land: deduplicate structurally identical instantiations by
  hashing the substituted signature (Phase 5); cache instantiations across incremental
  builds (Phase 8).
- **Monomorphization must terminate.** Polymorphic recursion — a generic function that
  calls itself at a strictly larger type, e.g. `fn f[T](x: T) { f[Pair[T, T]](...) }` —
  produces an infinite instantiation set and is REJECTED when the instantiation depth
  exceeds 64, with a diagnostic showing the instantiation chain.

## Prelude traits

Defined in `std::prelude` and in scope in every module without a `use`:

| Trait | Required | Notes |
|---|---|---|
| `Ord` | `fn cmp(self, other: Self) -> Ordering` | `<` on user types goes through `.cmp`, not the operator |
| `Show` | `fn to_str(self) -> String` | used by `io::println` |
| ~~`Hash`~~ | — | not a trait: hashing is structural and total, exactly as `==` is (§13) |
| `Iterator` | `type Item; fn next(mut self) -> Option[Self::Item]` | drives `for` |
| `IntoIterator` | `type Item; type Iter: Iterator; fn into_iter(self) -> Self::Iter` | drives `for` |
| `Send` | (marker, no methods) | §08; required to cross a channel |

`Eq` is **not** a trait: `==` is structural and total for every non-function type (§04). Nor
is `Hash`, for the same reason and by the same argument ADR-0011 makes about `==`: Phase 7
found that `Map` needs one function over any key at all, not one trait implemented per key
type, so `hash::of` is compiler-provided and specified (§13's "Hashing"). A `Hasher` to write
into, and a trait for a type that wants its own hash, would both be additions rather than
corrections — and neither is needed while hashing agrees with equality by construction.
This removes the most common reason to want `derive` and is why 0.1 has no attribute
syntax.

## Worked examples

| Code | Verdict |
|---|---|
| `trait T { type A; fn f(self) -> Self::A; }` | accepted |
| `impl T for i64 { type A = i64; fn f(self) -> i64 { self } }` | accepted |
| `impl T for i64 { fn f(self) -> i64 { self } }` | REJECTED — missing `type A` |
| `impl T for i64 { type A = i64; fn f(self) -> u64 { 0 } }` | REJECTED — signature mismatch |
| two `impl T for i64` in one package | REJECTED — overlapping impls |
| `impl Iterator for i64` in a package owning neither | REJECTED — orphan rule |
| `fn f[T: Ord](a: T, b: T) -> T { if a.cmp(b) == Ordering::Less { b } else { a } }` | accepted |
| `fn f[T](a: T, b: T) -> T { if a.cmp(b) == ... }` | REJECTED — `T: Ord` not satisfied |
| `fn f[T](x: T) -> bool { x == x }` | accepted — `==` is total |
| `fn f[T](x: T) -> T { x + x }` | REJECTED — `+` is not a trait method |
| `x.next()` with `Iterator` not `use`d | REJECTED, with a note naming the `use` |
| `fn f[T](x: T) { f[(T, T)](( x, x )) }` | REJECTED at depth 64 — polymorphic recursion |
