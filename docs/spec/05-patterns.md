# 05 — Patterns, Exhaustiveness and Usefulness

## Pattern forms

| Form | Matches | Binds |
|---|---|---|
| `_` | anything | nothing |
| `x` | anything (unless `x` resolves to a unit variant or `const`) | `x` |
| `mut x` | anything | `x`, reassignable |
| `x @ p` | whatever `p` matches | `x` to the whole value, plus `p`'s bindings |
| `1`, `'a'`, `"s"`, `true` | that literal value | nothing |
| `(p1, p2)` | a tuple, element-wise | union |
| `Variant` | that unit variant | nothing |
| `Variant(p1, p2)` | that tuple variant | union |
| `Variant { f: p, g }` | that struct variant | union; `g` is shorthand for `g: g` |
| `Struct { f: p, .. }` | a struct, ignoring unlisted fields | union |
| `p1 \| p2` | either | must be identical in both |

`..` is only valid as the final element of a struct pattern's field list. Struct
patterns without `..` MUST list every field, including private ones — which means a
struct with private fields cannot be destructured outside its module (§07).

## Binding rules

- Every alternative of an or-pattern MUST bind exactly the same set of names, each at
  the same type. `Some(x) | None` is REJECTED with "variable `x` is not bound in all
  patterns" and a span on the alternative that lacks it.
- A pattern MUST NOT bind the same name twice. `(x, x)` is REJECTED.
- Bindings introduced by a `match` arm's pattern are in scope in that arm's guard and
  body only.
- Bindings are by value, following §04's capture rule: a binding to an aggregate binds
  a reference to the same object.

## Guards

`p if g => body` matches when `p` matches and `g` evaluates to `true`. Guards may read
the pattern's bindings. A guard MUST NOT mutate anything reachable from the scrutinee;
this is not checked in 0.1 (DEFERRED: effect checking, Phase 4) but a guard that does so
has unspecified interaction with subsequent arm testing, which is the only "unspecified"
in the specification and the reason it is scheduled for removal.

**Guards are invisible to exhaustiveness.** An arm with a guard never contributes
coverage. `match b { true if c => 1, false => 2 }` is REJECTED as non-exhaustive.

## Exhaustiveness and usefulness

The checker implements Maranget's algorithm ("Warnings for pattern matching", JFP 2007)
over the matrix of arm patterns:

- **Exhaustiveness**: the pattern matrix must be exhaustive for the scrutinee type. A
  non-exhaustive `match` is REJECTED (an error, not a warning) with a diagnostic that
  prints a concrete **witness** — a smallest value not covered:
  `non-exhaustive match: `Shape::Rect { .. }` is not covered`.
- **Usefulness**: an arm whose pattern is subsumed by the arms above it is REJECTED as
  "unreachable pattern", with a span on the redundant arm and a note pointing at the
  arm that subsumes it.

Consequences of these being errors rather than warnings: there is no runtime match
failure, so §04's trap table has no "match failed" entry, and codegen may emit a jump
table with no default arm.

**Constructor spaces** used by the algorithm:

| Type | Constructors |
|---|---|
| `bool` | `true`, `false` — finite, so `true`/`false` is exhaustive |
| enum | its variants — finite |
| tuple / struct | one constructor, recurse into fields |
| integers, `char` | ranges over the full value space; only `_` or a binding is exhaustive in 0.1 (no range patterns) |
| `f32`/`f64` | never exhaustive without `_`; literal float patterns are REJECTED, since `NaN` and `-0.0` make equality a bad match primitive |
| `String` | never exhaustive without `_` |

`let p = e;` and `for p in e` require `p` to be **irrefutable** — exhaustive by itself
for the scrutinee's type. `let Some(x) = opt;` is REJECTED with a note suggesting
`match` or `if let`. (`if let` is DEFERRED, Phase 2 — it is sugar over a two-arm
`match`.)

## Decision tree lowering

`match` lowers to a decision tree, not a linear chain of tests, so no subexpression of
the scrutinee is tested twice on any path. The lowering is required to preserve:

1. arm order for overlapping patterns (first match wins),
2. guard evaluation order (a guard runs only after its pattern has fully matched, and
   only if no earlier arm matched),
3. the trap-freedom of testing itself — testing a pattern never traps.

Snapshot tests in `tests/snapshot/match/` pin the generated tree for a fixed corpus.

## Worked examples

| Match | Verdict |
|---|---|
| `match b { true => 1, false => 2 }` | accepted, exhaustive |
| `match b { true => 1 }` | REJECTED — witness `false` |
| `match b { true if c => 1, false => 2 }` | REJECTED — guarded arm gives no coverage |
| `match o { Some(x) => x, None => 0 }` | accepted |
| `match o { Some(x) => x, _ => 0, None => 1 }` | REJECTED — arm 3 unreachable |
| `match p { (0, y) => y, (x, 0) => x, (x, y) => x + y }` | accepted |
| `match n { 0 => "z", _ => "n" }` | accepted — `_` required for integers |
| `match n { 0 => "z" }` | REJECTED — witness `1` (or any non-zero) |
| `match s { Circle(r) => r, Rect { w, h } => w * h }` | accepted |
| `match s { Circle(r) => r }` | REJECTED — witness `Rect { .. }` |
| `match x { 1.0 => 1, _ => 0 }` | REJECTED — float literal patterns not permitted |
| `match e { A \| B => 1, C => 2 }` | accepted |
| `match e { A(x) \| B => 1 }` | REJECTED — `x` not bound in all alternatives |
| `let (a, b) = pair;` | accepted — irrefutable |
| `let Some(x) = opt;` | REJECTED — refutable pattern in `let` |
