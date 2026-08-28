# Diagnostic Code Registry

Codes are permanent. A code is never reused for a different meaning; a retired code is
marked retired and never reissued. `./check` verifies that every code emitted by the
compiler appears here exactly once, and that no code appears twice.

| Code | Severity | Meaning | Since |
|---|---|---|---|
| E0001 | error | lexical error (invalid character, unterminated literal, bad escape) | 0.1 |
| E0002 | error | syntax error | 0.1 |
| E0004 | error | non-exhaustive `match` | 0.1 |
| E0005 | error | refutable pattern in `let` or `for` | 0.1 |
| E0006 | error | unreachable pattern | 0.1 |
| E0007 | error | binding not present in all or-pattern alternatives | 0.1 |
| E0308 | error | mismatched types | 0.1 |
| E0309 | error | cannot infer type; annotation required | 0.1 |
| E0310 | error | infinite type (occurs check) | 0.1 |
| E0277 | error | trait bound not satisfied | 0.1 |
| E0119 | error | overlapping impls | 0.1 |
| E0117 | error | orphan impl | 0.1 |
| E0432 | error | unresolved import | 0.1 |
| E0433 | error | unresolved path | 0.1 |
| E0603 | error | item is private | 0.1 |
| E0594 | error | cannot assign: binding or field is not `mut` | 0.1 |
| E0599 | error | no method found | 0.1 |
| E0034 | error | ambiguous method call | 0.1 |
| E0055 | error | monomorphization depth exceeded (polymorphic recursion) | 0.1 |
| W0001 | warning | unused `Result` | 0.1 |
| W0002 | warning | unused item | 0.1 |
| W0003 | warning | binding pattern shadows a similarly-named unit variant | 0.1 |

Codes in the `E03xx`/`E01xx`/`E05xx` ranges intentionally echo familiar numbering where
the meaning matches, so that error text is searchable by people arriving from other
languages. This is a convenience, not a compatibility claim.
