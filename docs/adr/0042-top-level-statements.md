# ADR-0042: A file's top-level statements are the body of `main`

**Status:** accepted · **Date:** 2026-09-11 · **Decided by:** implementer (user delegated)

## Context

The user reframed the audience: **teenagers, not software engineers.** That changes which
costs matter. An engineer reads `fn main() { }` as the entry point and moves on; someone
writing their first program reads it as two lines of incantation before anything happens.

After ADR-0041 the smallest program was three lines, two of which were ceremony:

```origin
fn main() {
    println("hi")
}
```

Python's is `print("hi")`, and that is the comparison the audience actually makes. Go and
Java both require an entry point, but neither is the language a fifteen-year-old is choosing
between.

The four things that block a beginner, in the order they hit them, are: this wrapper;
`xs[0]` not existing (ADR-0013); a function signature needing types (ADR-0009); and `Option`
turning up the first time they index anything (ADR-0007). **Only the first is not a pillar.**
The other three are listed as language invariants and reversing one is the user's call, not
something to take under a standing "simplify the syntax" delegation. This ADR takes the one
that is free.

## Options considered

- **Leave it.** Every compiled language has an entry point and nobody has ever failed to
  learn `fn main`. Rejected because "nobody fails to learn it" is not the same as "it costs
  nothing" — it is the first thing on the screen, and it is unexplainable to someone who has
  not yet been taught what a function is.

- **A magic file name or a `#!`-style directive.** Rejected: a second mechanism to learn, and
  it puts the answer somewhere other than the code.

- **Run top-level statements as the program, with no `main` at all** — the Python model, where
  a module *is* a script. Rejected because Origin has items with forward references: `fn` and
  `struct` declarations are visible before they are written (§07's four-pass resolution), so
  "the file executes top to bottom" would be false about half the file. Wrapping the
  statements keeps the existing rule exactly as it is.

- **Collect the top-level statements into a synthesized `fn main`.** Chosen. The statements
  keep their source order, items keep their hoisting, and nothing downstream learns a new
  form — the same move `if let` and list literals make (ADR-0038, ADR-0039).

## Decision

Statements may appear at a file's top level. They become the body of a synthesized
`fn main()`, in source order. A file that contains top-level statements **and** declares
`main` is REJECTED.

## Consequences

- **The smallest program is one line**, and it is the line that does the work:

  ```origin
  println("hi")
  ```

- **Additive: no existing program changes.** Every file in this repository declares `main`
  and has no top-level statements, so none of them is affected and no golden file moves. A
  file that wants an explicit `main` keeps writing one — this is a second way to spell an
  entry point, not a replacement.

- **Items still hoist, and that is why this is a wrapper rather than a script.** A top-level
  statement may call a function declared below it, because resolution sees every item in the
  file before any body is resolved. A reader who assumes strict top-to-bottom execution is
  right about the statements and wrong about the declarations, which is the same rule every
  Origin block already follows.

- **Two entry points is an error rather than a silent choice.** Picking the declared `main`
  would make the loose statements dead code; picking the loose statements would make the
  declaration dead. Both are mistakes about what the program does, so the compiler says so.

- **The parser is the only pass that changes.** It builds an ordinary `ast.FnDecl` with an
  ordinary `ast.Block`, so the resolver, the checker, monomorphization and all three engines
  see a `main` they cannot distinguish from a written one.

- **It does not make Origin a scripting language.** Types are still mandatory on function
  signatures, `Option` is still what indexing returns, and there is still no `xs[0]`. Those
  are the next three barriers and each is a pillar; this ADR deliberately does not touch
  them, and `docs/phases/14-complete.md` prices them for whoever decides.

- **Reversing it is deleting the collection loop.** Programs that declared `main` are
  unaffected; programs written without one would stop parsing.
