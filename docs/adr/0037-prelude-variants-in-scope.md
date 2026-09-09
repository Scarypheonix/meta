# ADR-0037: A prelude enum's variants are in scope unqualified

**Status:** accepted · **Date:** 2026-09-09 · **Decided by:** implementer (user delegated)

## Context

`Option`, `Result`, `Ordering` and `IoError` are declared in the prelude, so their *names*
are in scope in every module with no `use`. Their *variants* are not: every occurrence is
written `Option::Some(x)`, `Result::Ok(v)`, `Ordering::Less`. The repository contains
**1,386 such occurrences** — 1,093 in `stage1/src`, 195 in `tests/e2e/cases`, 98 in the
prelude itself.

The qualifier carries no information. There is exactly one `Some` in the language, and in
a `match` the scrutinee's type has already determined which enum is being matched. It is
the most repeated noise in Origin source, and it falls hardest on `match`, which is the
only way to take an `Option` apart.

Three pieces of machinery for this already exist and are unreached:

- **spec/02-grammar.md** specifies the resolution rule — "if the identifier resolves in
  scope to a unit variant or a constant, the pattern matches that value; otherwise it
  introduces a binding" — and says in as many words that it is written down now "so that
  adding them does not silently change the meaning of existing patterns."
- **`internal/resolve`** implements it: `bindPattern`'s `BindPat` case consults
  `isConstructorLike`, which is true for `Variant` and `Const`. `resolve_test.go` records
  that "in 0.1 only the constant half is reachable, because a variant always needs a path."
- **`docs/spec/codes.md`** reserves `W0003`, "binding pattern shadows a similarly-named
  unit variant", which nothing emits.

The decision left open was which enums this applies to, and it had to be made before the
reserved rule could be switched on.

## Options considered

- **Glob imports (`use std::prelude::*`).** The general mechanism, and what the reserved
  rule was originally written against. It is deferred (`docs/deferred.md`, Phase 7) because
  it needs ambiguity rules and rename bookkeeping that nothing else in 0.1 wants, and it
  answers the wrong question here: the enclosing type is already in scope automatically, so
  making its variants opt-in per file is an asymmetry a reader has to learn rather than one
  the language needs. It remains the right mechanism for a *user* package's variants.

- **Every enum in scope puts its variants in scope.** Uniform and needs no notion of "the
  prelude" in the resolver. Rejected on the hazard, which scales with the number of
  variants visible: the reserved resolution rule turns a name that *would have bound* into
  a name that *matches*, silently, and a user enum declared three modules away entering the
  global namespace on declaration is exactly the surprise the rule's own warning exists to
  catch. Under this option the warning would be load-bearing; under the one chosen it is a
  backstop.

- **Every enum declared in the prelude.** Chosen first, and **wrong** — see the correction
  below. The rule reads as a restatement of one that already holds (prelude items are in
  scope everywhere), extended from the enum to the variants it declares, and it needs no
  list in the compiler.

- **The enums the language's own constructs produce and consume.** Chosen after the corpus
  rejected the option above. `Option`, `Result` and `Ordering` are the three the compiler
  itself names — `?` unwraps `Option` and `Result`, `for` drives `Iterator::next` which
  returns an `Option`, `Ord::cmp` returns an `Ordering` — and `internal/check` and
  `internal/compile` refer to them by name 8, 4 and 1 times respectively while never naming
  `IoError`. A program cannot avoid writing these variants; it can easily avoid writing an
  ordinary library enum's.

## Correction, found by running the corpus

The first decision here was "every enum declared in the prelude", on the argument that a
list of special enums is a second thing to maintain. **`tests/e2e` rejected it**, and the
failure is worth recording because nothing in the reasoning above predicted it:
`expression_language.origin` writes

```origin
other => Result::Err(Fault::BadToken(other.to_str())),
```

`other` as the name of a catch-all binding is idiomatic in any language with pattern
matching — it appears four times in this repository — and `IoError::Other` is a unit
variant, so `W0003` fired on correct code. The same collision waits in `NotFound`.

Scoping `W0003` to refutable positions had already been necessary to keep it off
`Ord::cmp(self, other: Self)`. That should have been the tell: a name whose collision has
to be worked around twice is not a name that belongs in the global scope. The rule is not
"declared in the prelude" but "so ubiquitous that the qualifier is pure noise", and that is
a judgment the language makes once — writing it down as a list *is* writing the judgment
down, which is what process rule 6 asks for rather than what it warns against.

## Decision

`Option`, `Result` and `Ordering` — the three enums the language's own constructs produce
and consume — put their variants into the global scope, under their own names, alongside
the enum's name. The qualified form remains legal and denotes the same variant.

Every other enum's variants, `IoError`'s included, are written `Enum::Variant`.

`W0003` is emitted only for a binding pattern in a **refutable** position — a `match` arm
or an `if let`/`while let` pattern — whose name differs from an in-scope unit variant only
by case.

## Consequences

- **The resolver is the only pass that changes.** A single-segment path already resolves by
  scope lookup (`resolvePathInto`), and a bare name in a pattern already consults
  `isConstructorLike`, so both `Some(5)` as an expression and `Some(x)` as a pattern work
  the moment the name is in the scope. The checker, the monomorphizer, the bytecode
  compiler and all three engines see the same `Ref{Kind: Variant}` they saw before and
  learn nothing.

- **Nothing existing is rewritten and no golden file changes.** Both spellings resolve
  identically, so every snapshot, bytecode dump, IR dump and selfhost trace over the
  existing corpus is byte-identical. This is what makes the change affordable at 208s of a
  300s suite budget.

- **Shadowing precedence keeps every existing program's meaning.** A module's own
  declaration lives in the module scope and a local binding in a block scope, both layered
  over the globals, so either shadows a prelude variant. A program that today declares
  `struct Other` or binds `let Ok = ...` continues to mean what it meant.

- **`W0003` stays scoped to refutable positions**, which is where a variant and a binding
  are both legal. In a `let`, a parameter or a `for`, a name that resolved to a unit variant
  would make the pattern refutable and `E0005` already rejects it, so the silent-capture
  confusion the warning guards cannot arise there. With `IoError` out of the global scope
  the pressure is off — `None`, `Less`, `Equal` and `Greater` are the only unit variants in
  scope, and none is a plausible binding name — but the scoping is right on its own terms
  and stays.

- **The three enums may not share a variant name.** The second declaration is a duplicate
  in the global scope and is rejected as one. No two share a name; the alternative — last
  writer wins — would make one enum's variant unreachable with no diagnostic.

- **The list is short because the criterion is narrow.** A fourth enum earns its place by
  becoming something the language itself produces, not by being useful or by living in the
  prelude. That is a higher bar than "someone would like the shorthand", and it is the bar
  that keeps the global namespace from accumulating English words.

- **A user enum's variants still need `Enum::Variant`.** This asymmetry is the price of not
  doing glob imports, and it is the reversal point: if glob imports land, the prelude
  becomes an ordinary module that every file globs, and this ADR's special case can be
  deleted rather than amended.
