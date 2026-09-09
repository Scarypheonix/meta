# ADR-0038: `if let` and `while let` are desugared in the parser

**Status:** accepted · **Date:** 2026-09-09 · **Decided by:** implementer (user delegated)

## Context

`match` is the only way to take an `Option` apart, so a test for one variant is written as
a two-arm `match` whose second arm exists only to satisfy exhaustiveness. **147 of the 534
`match` expressions in `stage1/src` are that shape**, many with a literally empty arm:

```origin
match ty {
    Option::Some(t) => dump_type(out, t, depth + 2),
    Option::None => {}
}
```

The construct advertises a choice where there is only a test, and the reader pays for the
arm that says nothing.

`docs/deferred.md` has carried `if let` / `while let` since Phase 3 — "pure sugar over a
two-arm `match`; needs the match lowering first". That lowering landed in Phase 5.

## Options considered

- **An `ast.IfLet` node carried through the pipeline.** Honest to the source, and it is what
  a formatter and an LSP would eventually want. Rejected for now on cost: the resolver, the
  checker, the exhaustiveness pass, the monomorphizer, the bytecode compiler and all three
  engines would each need a rule saying "this means the same as a `match`" — in two
  languages, since `stage1/src` is a second implementation of the front end. Six duplicated
  statements of one fact is exactly the duplication process rule 5 exists to prevent.

- **Desugar in the parser to a `match`.** The precedent is string interpolation, whose own
  note in `internal/parse/interp.go` makes the argument: "Desugaring to nodes that already
  exist is the whole point. Nothing downstream learns a new form." The `for` loop is the
  same move at one remove — it desugars to `into_iter`/`next` calls the checker records as
  ordinary instantiations.

- **Desugar, and additionally reject a syntactically irrefutable pattern in the parser.**
  Catches the common mistake (`if let x = e`) with no AST change, but only that one:
  `if let Wrapper(x) = e`, where `Wrapper` is a single-variant enum, is irrefutable too and
  needs types to know it. A check that catches the easy half and lets the other half reach
  a confusing diagnostic is worse than either whole answer.

## Decision

Parse `if let` and `while let`, and desugar both in the parser into nodes that already
exist:

```
if let p = e { a } else { b }   ->   match e { p => a, _ => b }
if let p = e { a }              ->   match e { p => a, _ => () }
while let p = e { body }        ->   loop { match e { p => body, _ => break } }
```

`ast.Match` gains one field naming the construct it was desugared from, read only by the
usefulness check, which reports `E0008` — "irrefutable pattern in `if let` or `while let`" —
instead of `E0006` against a `_` arm the programmer did not write.

## Consequences

- **Nothing downstream learns a new form.** The resolver, checker, monomorphizer, bytecode
  compiler and all three engines see a `Match` and a `Loop` they already handle. `break` and
  `continue` inside a `while let` body bind correctly with no resolver change, because
  `loopDepth` counts `while`, `for` and `loop` and not `match` — so the desugared `loop` is
  the innermost one, which is the loop the programmer meant. `continue` re-evaluates the
  scrutinee, which is what `while let` means.

- **Additive; no existing program changes and no golden file moves.** `if let` is a syntax
  error today, so no corpus file uses it and every snapshot, bytecode dump and selfhost
  trace over existing source is byte-identical.

- **The one new AST field has exactly one reader, added in the same commit.** Phase 8's
  recorded failure mode is a field nothing reads, and Phase 9's rule is that a slot arrives
  when a reader does. Here the reader is the usefulness check, and without the field its
  diagnostic would point at synthesized syntax — a diagnostic that lies about where the
  problem is, which process rule 8 rules out as firmly as a stub that lies.

- **An irrefutable `if let` is an error, not a warning.** `E0005` already makes the mirror
  case — a refutable pattern in `let` — an error, and the two rules should not disagree
  about how much a wrong pattern costs. The diagnostic names `let` as the fix.

- **The desugaring is not visible in a diagnostic's span.** Every synthesized node carries
  the span of the source construct it came from, so an error inside the body underlines
  what the programmer wrote. This is the same discipline `subExpr` keeps for interpolation.

- **Reversing this means writing the `ast.IfLet` node after all**, which is what a formatter
  that must reproduce the source, or an LSP that must offer "convert to `match`", will
  eventually force. That is a additive change to the parser and one new node, not a
  correction: the desugaring's *meaning* is what the spec fixes, and a node that carries the
  same meaning further down the pipeline does not change any program's behaviour.
