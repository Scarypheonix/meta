# 16 — Rendering a float

`1.0 / 3.0` has no exact decimal spelling, and `0.1` is not the number `0.1`. Printing a
float is therefore a decision, not a formatting detail, and this section makes it one the
specification owns rather than one each engine reaches on its own.

```origin
use std::io;

fn main() {
    io::println((1.0 / 3.0).to_str());   // 0.3333333333333333
    io::println((0.1 + 0.2).to_str());   // 0.30000000000000004
    io::println(1000000.0.to_str());     // 1e+06
    io::println((0.0 / 0.0).to_str());   // NaN
}
```

## The rule

`to_str` on a float produces the **shortest decimal digit string that reads back as
exactly the same value**, laid out by the rules below. "Reads back" means through Origin's
own float literals (§01), so the rendering and the lexer are inverses: every finite float
printed by `to_str` and pasted back into a program is the same float.

Shortest is a property of the value, not of how it was written. `1.0`, `1.00` and
`0.5 + 0.5` all render as `1.0`. Two different floats never render the same, and one float
never renders two ways.

### The digits

Every finite non-zero float `v` is `d₁d₂…dₙ × 10^(k−n)` for exactly one shortest digit
string with `d₁ ≠ 0`. Write it as `0.d₁d₂…dₙ × 10^k`; `k` is the **decimal point
position** and `n` the **digit count**.

`d₁…dₙ` is the shortest string of digits such that some decimal of that length lies
strictly inside the interval of reals that round to `v` — and, when more than one does,
the one nearest `v`. Two ties are decided, both the way parsing decides one:

- Whether the interval's *endpoints* count as inside: they do exactly when `v`'s
  significand is even, because a decimal exactly halfway between two floats parses to the
  one with the even significand.
- When `v` is exactly halfway between the two candidates of length `n`, the one whose last
  digit is even wins. `2⁻²⁵` is `0.000000029802322387695312**5**` exactly, so its
  seventeenth digit is a tie, and it renders as `2.9802322387695312e-08`.

This is a fact about `v`, so an implementation may compute it any way it likes and is
tested against the answer, never against an algorithm.

### The layout

Let `e = k − 1`, the exponent when the digits are written as `d₁.d₂…dₙ`.

| Condition | Form |
|---|---|
| `e < −4` or `e ≥ 6` | scientific: `d₁`, then `.d₂…dₙ` when `n > 1`, then `e`, then `+` or `−`, then \|`e`\| in decimal, at least two digits |
| `k ≤ 0` | `0.`, then `−k` zeros, then the digits |
| `k ≥ n` | the digits, then `k − n` zeros |
| otherwise | the first `k` digits, `.`, then the rest |

If the result so far contains neither `.` nor `e`, `.0` is appended — so an integral value
renders as `1.0`, not `1`, and a float is never mistaken for an integer in output.

A negative value is the rendering of its magnitude with `-` in front. This includes
negative zero, which renders as `-0.0`: `0.0 == -0.0` is `true` (§04) but they are
different floats, and `to_str` shows what a value is.

### The three that are not numbers

| Value | Rendering |
|---|---|
| positive infinity | `+Inf` |
| negative infinity | `-Inf` |
| NaN | `NaN` |

These have no digits and no sign rule; `NaN` is one spelling whatever the payload or sign
bit, because §04 makes every NaN indistinguishable from every other by comparison.

## Worked examples

| Expression | Renders as |
|---|---|
| `0.0` | `0.0` |
| `-0.0` | `-0.0` |
| `1.0` | `1.0` |
| `0.5` | `0.5` |
| `100000.0` | `100000.0` — `e` is 5, still positional |
| `1000000.0` | `1e+06` — `e` is 6 |
| `999999.0` | `999999.0` |
| `0.0001` | `0.0001` — `e` is −4 |
| `0.00001` | `1e-05` — `e` is −5 |
| `1.0 / 3.0` | `0.3333333333333333` |
| `0.1 + 0.2` | `0.30000000000000004` — 17 digits, because 16 do not read back |
| `1e20` | `1e+20` |
| `1e100` | `1e+100` — three exponent digits when three are needed |
| `f64::MAX` | `1.7976931348623157e+308` |
| the smallest subnormal | `5e-324` — one digit is enough to name it |
| `2.0 / 0.0` | `+Inf` |
| `0.0 / 0.0` | `NaN` |

## Where it lives

The rendering is **Origin source in the prelude** (ADR-0031), written over two
compiler-provided operations:

```origin
float::bits(f: f64) -> u64          // the IEEE-754 binary64 encoding
float::from_bits(b: u64) -> f64     // and back
```

Neither computes anything: on every engine a float and its bits are the same sixty-four
bits in the same place, and what these say is only which of the two ways to read them
applies from here on. Everything else — decomposing the significand and exponent, the
exact arithmetic that finds the shortest digits, the layout above — is Origin, in
`internal/prelude/prelude.origin`, and therefore one implementation that all three engines
run rather than three that have to be argued into agreement.

That is the same trade §13 made for collections and §14 made for strings, and here it buys
the most: a shortest-round-trip conversion needs exact arithmetic on numbers far wider than
sixty-four bits, and the alternative was writing that by hand in x86-64 machine code.

`f32` renders through the same path. Origin computes in binary64 (§04), so an `f32` value
is a `f64` value and renders as one.
