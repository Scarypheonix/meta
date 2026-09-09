# ADR-0039: A list literal desugars to `list::new` and pushes

**Status:** accepted · **Date:** 2026-09-09 · **Decided by:** implementer (user delegated)

## Context

A list of three elements costs four statements: `list::new[i64]()` and three `push` calls.
`docs/deferred.md` has carried collection literals since Phase 7 and concedes the point —
"The reason to revisit is ergonomics — a test that wants three elements writes three pushes
— rather than capability" — but defers them under "same ambiguity constraint as index
syntax".

**That constraint does not hold on the current grammar.** ADR-0013 ruled out `[` *after* an
expression, where it always begins type arguments. §02's `Primary` production is
`Literal | PathExpr | StructLit | TupleOrParen | Lambda | Block | IfExpr | MatchExpr |
WhileExpr | ForExpr | LoopExpr | break | continue | return | self`, and `internal/parse`'s
`parsePrimary` has no `LBracket` case: **`[` in prefix position is unreachable today** and
reaching it costs ADR-0013 nothing.

## Options considered

- **A first-class list literal carried to the checker**, with its own AST node and its own
  typing rule. Rejected for ADR-0038's reason and ADR-0028's: it would put a collection into
  the language's core, where the whole point of ADR-0028 is that collections are a library.

- **A `FromLiteral` trait, so any type can be built from a literal.** The general answer, and
  what a language with `Vec`, `HashMap` and a user's own container eventually wants.
  Rejected as premature: 0.1 has one sequence type, the trait would need a variadic or
  slice-shaped input that nothing else in the language has, and choosing its shape now
  would be deciding an interface before there is a second implementor to check it against.

- **Desugar in the parser into `list::new` plus pushes.** Hardcodes two prelude names in the
  parser — which is what the language already does twice: `for` desugars to `into_iter` and
  `next`, and string interpolation to `to_str` and `concat`. This is the same move, not a
  new kind of move, and it leaves `List` an ordinary library type.

## Decision

`[e1, e2, ..., en]` is an expression, desugared in the parser to a block that builds the
prelude's `List` and pushes each element in source order:

```
[a, b, c]   ->   { let xs = list::new(); xs.push(a); xs.push(b); xs.push(c); xs }
```

The element type is left to inference. No separate rule exists for the empty literal.

## Consequences

- **Evaluation order falls out correctly.** §04 requires strict left-to-right evaluation,
  and the pushes are emitted in source order, so `[f(), g()]` calls `f` then `g` like every
  other construct.

- **The element type needs no new inference rule.** `list::new()` is generic in `T` and each
  `push` constrains it, all within one block, which is inside what ADR-0009's local
  inference already solves. `[]` with nothing to constrain it fails as `E0309` — "cannot
  infer type; annotation required" — which is the honest answer and is fixed the ordinary
  way, by annotating: `let xs: List[i64] = [];` unifies the block's tail with the annotation
  and solves `T`. A dedicated empty-literal rule would be a second way to say what
  annotation already says.

- **The binding the desugaring introduces is not a valid identifier**, so no program can
  name it and no user binding can collide with it. It appears in an AST dump as itself,
  which is correct: it is synthesized, and a dump that disguised it as an ordinary name
  would be lying about the tree.

- **No ambiguity is introduced, and the near-miss is unreachable.** A `[` following an
  expression still begins type arguments. The one shape where the two could compete —
  an expression statement followed by a literal on the next line — cannot arise, because
  §02's `ExprStmt` requires `;` after a non-block expression: in `foo [1, 2]` the parser is
  still inside `foo`'s expression, so `[` is type application, exactly as ADR-0013 says.
  Writing `foo; [1, 2]` is unambiguous. Origin is not newline-sensitive and this decision
  does not make it so.

- **Only `List` gets a literal.** `Map` has no literal form, because a map literal needs a
  key/value syntax that would be a second decision, and `Map`'s constructor is not the thing
  the deferred entry complained about.

- **Reversing this is deleting a parser production.** Nothing downstream knows the form
  exists, so no program's meaning depends on the desugaring beyond the calls it emits —
  which are the calls the programmer would otherwise have written.
