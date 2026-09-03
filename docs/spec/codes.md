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
| E0595 | error | cannot assign to a binding a lambda captured | 0.1 |
| E0599 | error | no method found | 0.1 |
| E0034 | error | ambiguous method call | 0.1 |
| E0055 | error | monomorphization depth exceeded (polymorphic recursion) | 0.1 |
| E0023 | error | enum variant pattern has the wrong number of values | 0.1 |
| E0026 | error | pattern names a field the type does not have | 0.1 |
| E0027 | error | pattern does not mention every field | 0.1 |
| E0046 | error | impl is missing a required method or associated type | 0.1 |
| E0061 | error | wrong number of arguments | 0.1 |
| E0062 | error | struct literal initializes a field twice | 0.1 |
| E0063 | error | struct literal is missing a field | 0.1 |
| E0107 | error | wrong number of type arguments | 0.1 |
| E0109 | error | type arguments supplied to something that is not generic | 0.1 |
| E0220 | error | associated type cannot be determined from the bounds in scope | 0.1 |
| E0369 | error | operator is not defined on this type | 0.1 |
| E0404 | error | expected a trait, found something else | 0.1 |
| E0407 | error | impl defines a method the trait does not declare | 0.1 |
| E0411 | error | `Self` used outside a trait or impl | 0.1 |
| E0412 | error | type parameter not in scope | 0.1 |
| E0423 | error | expected a value, found a type | 0.1 |
| E0424 | error | `self` used outside a method | 0.1 |
| E0532 | error | pattern names something that cannot be matched | 0.1 |
| E0533 | error | variant constructed with the wrong syntax | 0.1 |
| E0560 | error | struct literal names a field the type does not have | 0.1 |
| E0571 | error | `break` with a value outside `loop` | 0.1 |
| E0573 | error | expected a type, found something else | 0.1 |
| E0574 | error | struct literal used for something that is not a struct | 0.1 |
| E0600 | error | cannot apply unary `-` to this type | 0.1 |
| E0605 | error | invalid cast | 0.1 |
| E0609 | error | no such field | 0.1 |
| E0618 | error | called something that is not a function | 0.1 |
| E0658 | error | float literal used as a pattern | 0.1 |
| W0001 | warning | unused `Result` | 0.1 |
| E0700 | error | a value crossing a channel is not `Send` | 0.1 |
| E0701 | error | a spawned closure captures a value that is not `Send` | 0.1 |
| E0702 | error | `JoinHandle[T]` where `T` is not `Send` | 0.1 |
| W0002 | warning | unused item | 0.1 |
| W0003 | warning | binding pattern shadows a similarly-named unit variant | 0.1 |

Codes in the `E03xx`/`E01xx`/`E05xx` ranges intentionally echo familiar numbering where
the meaning matches, so that error text is searchable by people arriving from other
languages. This is a convenience, not a compatibility claim.
