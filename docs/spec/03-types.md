# 03 — Type System

## Primitive types

| Type | Values | Notes |
|---|---|---|
| `i8` `i16` `i32` `i64` | two's-complement signed | overflow TRAPS (§04) |
| `u8` `u16` `u32` `u64` | unsigned | overflow TRAPS |
| `f32` `f64` | IEEE-754 binary32 / binary64 | round-to-nearest-ties-to-even |
| `bool` | `true`, `false` | 1 byte, values 0 and 1 only |
| `char` | one Unicode scalar value | 4 bytes, surrogates excluded |
| `()` | the single value `()` | zero-sized |
| `String` | immutable UTF-8 text | heap object, reference semantics (§08) |

There is no `isize`/`usize` in 0.1; sizes and indices are `u64`. DEFERRED: pointer-sized
integers (Phase 5, if the Mach-O writer needs them in the source language).

## Composite types

- **Tuples** `(A, B, ...)` — value types. Arity 0 is `()`; arity 1 requires `(A,)`.
  Maximum arity is 12 (a limit, not a technical constraint; larger tuples should be
  structs). Tuples are compared and copied element-wise.
- **Structs** — nominal, heap-allocated, reference semantics (§08, ADR-0008).
- **Enums** — nominal algebraic data types, heap-allocated when they carry a payload.
  A fieldless enum (all variants are unit variants) is a value type represented as a
  `u32` tag.
- **Function types** `fn(A, B) -> C` — a code pointer plus an optional captured
  environment. Lambdas that capture nothing are representationally identical to
  top-level functions.

`Option[T]` and `Result[T, E]` are ordinary enums defined in the prelude, not built-in:

```origin
pub enum Option[T] { None, Some(T) }
pub enum Result[T, E] { Ok(T), Err(E) }
```

## Type equality

Types are compared **structurally for tuples, functions and type application**, and
**nominally for structs, enums and traits**. Two struct types are equal iff they are the
same declaration instantiated with equal type arguments. A `type` alias is transparent:
it is expanded before comparison and never appears in a diagnostic without its
expansion also being shown.

## Inference

Origin uses Hindley–Milner inference with these three deviations, each of which buys a
concrete property (ADR-0009):

1. **Signatures are mandatory.** Every `fn` at item level, in a `trait`, or in an `impl`
   MUST annotate every parameter and its return type (a missing `->` means `-> ()`).
   Inference is therefore **intra-procedural**: each function body is checked in
   isolation against a known signature. This buys separate compilation, stable error
   spans, and the ability to report "expected `i64`, found `String`" rather than an
   unhelpful chain of unification variables.
2. **Value restriction.** A `let` binding generalizes its type only when its
   right-hand side is a *syntactic value*: a literal, a path, a lambda, a tuple of
   syntactic values, or a constructor applied to syntactic values. Anything else —
   notably a call — is monomorphic. This is required for soundness because aggregates
   have mutable fields and reference semantics; without it,
   `let v = Vec::new();` would generalize to `forall T. Vec[T]` and permit pushing an
   `i64` and reading back a `String`.
3. **Defaulting.** An unsolved integer variable defaults to `i64`; an unsolved float
   variable defaults to `f64`. Any other unsolved variable at the end of a function
   body is REJECTED with "cannot infer type; add an annotation" and a span pointing at
   the expression that introduced it. Defaulting happens once, after all constraints in
   the body are solved, never mid-inference.

Local `let` bindings may omit their type; lambda parameters may omit theirs when the
expected type is known from context.

### Unification

Unification is first-order, with the occurs check enabled. Attempting to unify a
variable with a type containing it is REJECTED with "infinite type" and both spans.
Unification never crosses a function boundary.

Associated type projections `Self::Item` are normalized before unification by looking
up the impl; an unresolved projection blocks unification and is retried after further
constraints are solved. If a projection remains unresolved at the end of the body, it is
REJECTED with "cannot determine `X::Item`; add a bound or an annotation".

## Subtyping and coercion

**There is none.** Origin has no subtyping, no variance, and no implicit conversion of
any kind — not integer widening, not `i32` to `i64`, not `&T` to `T`, not literal
adaptation beyond the defaulting rule above. Every conversion is written with `as`
(§04) and every `as` is a place a reviewer can see (ADR-0012).

The single exception is that a diverging expression (`return`, `break`, `continue`, a
call to a function returning the never type, or a trap) has type `!` and unifies with
any type. `!` is not writable in source in 0.1.

## Type parameters and bounds

```origin
fn largest[T: Ord](items: Vec[T]) -> Option[T] { ... }

fn print_all[T](items: Vec[T]) where T: Show { ... }
```

`[T: Ord]` and a `where` clause are equivalent; both may be used in one signature and
the bounds are unioned. A type parameter with no bounds supports only what every type
supports: being moved, being stored, and `==` (§04's structural equality is total).

## Well-formedness

A type is well-formed iff: every path resolves; every type application supplies exactly
the declared number of arguments; every argument satisfies the corresponding
declaration's bounds; and no recursive struct or enum has infinite size. Since
aggregates are heap-allocated with reference semantics (ADR-0008), *all* direct
recursion is finite:

```origin
enum List[T] { Nil, Cons(T, List[T]) }   // accepted: Cons's payload is a reference
```

This is a genuine simplification over value-semantics languages, which need an explicit
indirection (`Box`) here.

## Constants

`const` initializers are evaluated at compile time by a restricted evaluator that
supports literals, arithmetic on primitives, comparisons, `if`/`else`, and references to
other `const`s. A `const` initializer that would TRAP is REJECTED at compile time with
the trap's message. Function calls in `const` initializers are DEFERRED (Phase 4).

## Worked examples

| Program fragment | Verdict |
|---|---|
| `let x = 1;` | `x: i64` by defaulting |
| `let x: u8 = 1;` | `x: u8` |
| `let x: u8 = 300;` | REJECTED — literal out of range for `u8` |
| `let x = 1; let y: i32 = x;` | REJECTED — expected `i32`, found `i64`; no widening |
| `let x = 1i32; let y = x as i64;` | accepted |
| `let f = \|x\| x;` | `f: forall T. fn(T) -> T` — RHS is a syntactic value, generalized |
| `let v = Vec::new();` | monomorphic; `Vec[?T]`, must be pinned by later use |
| `let v = Vec::new(); v.push(1); v.push("a");` | REJECTED — second push, expected `i64` |
| `fn id[T](x: T) -> T { x }` | accepted |
| `fn bad(x) { x }` | REJECTED — parameter needs a type annotation |
| `fn f[T](x: T) -> T { x + x }` | REJECTED — `T` has no `Add` bound and `+` is not overloadable |
| `enum List[T] { Nil, Cons(T, List[T]) }` | accepted — reference semantics |
| `let x = if c { 1 } else { return 0 };` | `x: i64` — `!` unifies |
| `let x = if c { 1 } else { "a" };` | REJECTED — branches have different types |
