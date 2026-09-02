# 04 — Expressions and Evaluation Semantics

This is the most load-bearing document in the specification. Phase 4's exit criterion —
identical program output at `-O0`, `-O1` and `-O2` — is only meaningful because
everything here is fully determined.

## Evaluation order

Evaluation is **strictly left-to-right, innermost-first**, everywhere and at every
optimization level:

- Binary operator: left operand fully, then right operand, then the operation.
- Call: the callee expression, then arguments left to right, then the call.
- Method call: the receiver, then arguments left to right.
- Struct literal: field initializers in **source order**, not declaration order.
- Tuple: elements left to right.
- Assignment `place = value`: the **place** subexpressions first (e.g. the receiver in
  `a.b.c = e`), then `value`, then the store.
- Compound assignment `p += v`: place subexpressions, then the current value is read,
  then `v`, then the operation, then the store.

`&&` and `||` short-circuit: the right operand is not evaluated when the result is
determined by the left. `if`/`match` evaluate only the selected branch; `match` tests
arms top to bottom and guards are evaluated only for arms whose pattern matched.

An optimizer MAY reorder operations only when it can prove the reordering is
unobservable, which for operations that can TRAP means proving they cannot trap. This
is the rule the pass-verification harness enforces empirically.

## Traps

A trap writes a single line to stderr and exits with status **101**:

```
origin: <message> at <file>:<line>:<col>
```

The complete list of traps in Origin 0.1:

| Trap | Message |
|---|---|
| Integer overflow (`+ - * << as`, unary `-`) | `arithmetic overflow` |
| Integer division by zero | `divide by zero` |
| Integer remainder by zero | `remainder by zero` |
| `INT_MIN / -1`, `INT_MIN % -1` | `arithmetic overflow` |
| Shift amount `>=` bit width | `shift amount out of range` |
| Float→int cast, value out of range or NaN | `invalid float to integer cast` |
| `.at(i)` out of bounds | `index out of bounds` |
| Non-exhaustive `match` reached at runtime | (impossible; §05 makes it a compile error) |
| Explicit `panic(msg)` | the supplied message |
| Stack exhaustion | `stack overflow` |

Traps are not catchable in 0.1. DEFERRED: per-green-thread trap isolation (Phase 6).

## Arithmetic

All arithmetic is on operands of **identical** type; there is no promotion. Mixed-type
arithmetic is REJECTED.

### Integers

Arithmetic happens at the operand type's own width. `i8` addition is 8-bit addition, `u32`
multiplication is 32-bit multiplication, and "overflow" always means *not representable in
that type* — never "not representable in 64 bits". So `255u8 + 1` TRAPS and `255u32 + 1`
is `256`, and the two are different programs even though the values are the same bits.

That is not a detail an implementation may round off. A machine register holds sixty-four
bits and nothing in it says which of the eight integer types they are, so the width travels
with the operation, and every engine performs the operation at it.

A value of an integer type is always representable in that type: an operation that would
leave one that is not, traps instead. So a narrow integer needs no separate "is this in
range" state — being in range is an invariant, and `as`, which truncates rather than
trapping, re-establishes it by discarding the bits above the target width.

`+ - *` TRAP on overflow, at every optimization level, in debug and release alike
(ADR-0005). Explicit alternatives live in the prelude and never trap:

```origin
a.wrapping_add(b)      // two's-complement wraparound, at a's own width
a.checked_add(b)       // Option[T]: None when the result does not fit in T
a.saturating_add(b)    // clamps to T::MIN / T::MAX
```

All three are at the receiver's width too: `255u8.wrapping_add(1)` is `0`,
`255u8.saturating_add(1)` is `255`, and `255u8.checked_add(1)` is `None`.

`/` truncates toward zero. `%` takes the sign of the dividend, so
`a == (a / b) * b + (a % b)` holds whenever neither side traps. Division and remainder
by zero TRAP. `i64::MIN / -1` TRAPS (the mathematical result is not representable).

`<<` and `>>` require the shift amount to be strictly less than the bit width of the
left operand; otherwise they TRAP. The shift amount has type `u32` regardless of the
left operand's type. `>>` on a signed type is arithmetic (sign-extending); on an
unsigned type it is logical.

`& | ^` are bitwise on integers only. Unary `-` on an integer TRAPS when the operand is
that type's minimum value; it is REJECTED on unsigned types.

### Floats

IEEE-754 binary32/binary64 with round-to-nearest-ties-to-even. Overflow produces an
infinity; `0.0 / 0.0` produces NaN. Floats never trap. The compiler MUST NOT contract
`a * b + c` into a fused multiply-add, MUST NOT reassociate float arithmetic, and MUST
NOT assume the absence of NaN — all three would break the identical-output-across-`-O`
requirement. Signalling NaNs are not distinguished; the sign bit of a produced NaN is
unspecified but a NaN is never compared equal to anything, including itself.

## Comparison

`==` and `!=` are **structural and total**, defined by the compiler for every type:

- primitives compare by value (`f32`/`f64` by IEEE semantics, so `NaN != NaN` and
  `0.0 == -0.0`);
- tuples, structs and enums compare field-wise and variant-wise, recursively, in
  declaration order, short-circuiting on the first difference;
- `String` compares by byte sequence;
- function values are REJECTED as operands of `==`.

Because `==` traverses structure, comparing a cyclic object graph does not terminate.
This is the one place Origin 0.1 permits non-termination without a trap; DEFERRED:
cycle detection or an opt-in `Eq` trait (Phase 7).

`< <= > >=` are defined **only** on integers, floats, `char` (by scalar value) and
`String` (lexicographic by bytes, which for UTF-8 equals code point order). Ordering of
user types goes through the `Ord` trait (§06) and is written `a.cmp(b)`.

Comparison operators are non-associative (§02).

Reference identity is available as the prelude function `ref_eq(a, b) -> bool`, defined
only for aggregate types. It is the only observable consequence of the GC's object
identity, and a moving collector MUST preserve it.

## `as` casts

`as` is the only conversion. Its semantics are total and defined:

| From → To | Behaviour |
|---|---|
| int → wider int, same signedness | value preserved |
| int → narrower int | **truncates** (takes the low bits); never traps |
| signed ↔ unsigned, same width | reinterprets the bit pattern |
| int → float | rounds to nearest, ties to even |
| float → int | truncates toward zero; **TRAPS** if the truncated value is out of range or the input is NaN |
| `f64` → `f32` | rounds to nearest, ties to even; may become infinity |
| `f32` → `f64` | exact |
| `char` → `u32` | the scalar value |
| `u32` → `char` | **REJECTED** — use `char::from_u32(x) -> Option[char]` |
| `bool` → integer | `0` or `1` |
| anything else | REJECTED |

Integer narrowing truncating rather than trapping is deliberate: narrowing is how you
say "I want the low bits", and a trapping narrowing would have no non-trapping
counterpart. Float→int traps because there is no defensible value to produce.

## Places and assignment

A *place* is an expression that denotes storage: a `mut` local binding, or a field
access `e.f` where `f` is declared `mut`. Assigning to anything else is REJECTED, with
a diagnostic that names which of the two rules failed:

- `x = 1` where `x` is `let x` → "`x` is not declared `mut`"
- `p.x = 1` where `x: f64` → "field `x` of `Point` is not declared `mut`"

Assignment evaluates to `()`. Chained assignment `a = b = c` therefore assigns `()` to
`a` and is REJECTED unless `a: ()`.

## Control flow expressions

`if` without `else` has type `()` and its block MUST have type `()`. `if`/`else` requires
both branches to have the same type.

`loop { }` evaluates to the value of its `break`s; every `break` in a `loop` must carry
the same type, and `break` with no value means `break ()`. `while` and `for` have type
`()` and their `break` MUST NOT carry a value.

`for p in e { }` requires `e: I where I: Iterator`, and desugars exactly to:

```origin
{
    let mut __it = e.into_iter();
    loop {
        match __it.next() {
            Option::None => break,
            Option::Some(p) => { ... }
        }
    }
}
```

The desugaring is normative: a `for` loop's observable behaviour MUST equal the
desugared form's, including evaluation order and trap points.

`return e` exits the enclosing function with `e`; `return` alone means `return ()`.
`break`/`continue` bind to the innermost enclosing loop; labelled loops are DEFERRED
(Phase 7).

## Lambdas and captures

A lambda captures each free variable **by value at the point the lambda is created**.
For a primitive that means a copy of the value; for an aggregate that means a copy of
the reference, so the lambda and the enclosing scope observe the same object and see
each other's mutations to `mut` fields. A captured `mut` *binding* is captured by value:
reassigning the outer binding after the lambda is created does not affect the lambda,
and the lambda cannot reassign the outer binding. To share a mutable slot, share an
aggregate with a `mut` field — that is what `Cell[T]` in the prelude is for.

This rule is what makes the Phase 1 closure-counter test meaningful; it is spelled out
in `10-examples.md` §7 with its expected output.

## Worked examples

| Expression | Result |
|---|---|
| `2 + 3 * 4` | `14` |
| `i64::MAX + 1` | TRAPS `arithmetic overflow` |
| `255u8 + 1` | TRAPS `arithmetic overflow` — 8-bit addition |
| `255u32 + 1` | `256` — the same bits, a different type, a different program |
| `127i8 + 1`, `-128i8 - 1`, `-(-128i8)` | TRAPS `arithmetic overflow` |
| `255u8.wrapping_add(1)` | `0` |
| `255u8.saturating_add(1)` | `255` |
| `255u8.checked_add(1)` | `None` |
| `200u8 * 2` | TRAPS `arithmetic overflow` |
| `-128i8 / -1` | TRAPS `arithmetic overflow` |
| `1u8 << 7` | `128` |
| `3u8 << 7` | TRAPS `arithmetic overflow` |
| `1u8 << 8` | TRAPS `shift amount out of range` |
| `u64::MAX` | `18446744073709551615` |
| `u64::MAX + 1` | TRAPS `arithmetic overflow` |
| `u64::MAX / 2` | `9223372036854775807` — unsigned division, not signed |
| `u64::MAX > 1` | `true` — unsigned comparison |
| `i64::MAX.wrapping_add(1)` | `i64::MIN` |
| `(-7) / 2` | `-3` (truncates toward zero) |
| `(-7) % 2` | `-1` (sign of dividend) |
| `1 << 64` on `i64` | TRAPS `shift amount out of range` |
| `-1i32 >> 1` | `-1` (arithmetic shift) |
| `300 as u8` | `44` (truncation, defined) |
| `1e20 as i32` | TRAPS `invalid float to integer cast` |
| `0.0/0.0 == 0.0/0.0` | `false` |
| `0.0 == -0.0` | `true` |
| `"abc" < "abd"` | `true` |
| `f(g(), h())` | `g()` runs before `h()`, always |
| `false && trap()` | `false`; right operand not evaluated |
