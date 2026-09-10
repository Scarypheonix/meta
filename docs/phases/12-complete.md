# Phase 12 — Complete

**Exit criteria:** rule 7 put the scope with the user, who delegated it
(`docs/design-questions.md`, Phase 12): *"Just do whatever you want in order to simplify the
syntax and grammar of Origin."* Two constraints came with it and were not delegated —
nothing is pushed, and the website's style is not touched.

**Status:** met. Semicolons are inserted at a line break (**ADR-0040**), `./check` passes in
**217s at 1,954 MiB** against budgets of 300s and 3,072 MiB, and `tests/selfhost` holds the
two lexers to each other, token for token, over every `.origin` file in the repository.
**40 ADRs, 20 specification documents.**

```
14,173 of 31,771 code lines end in a semicolon   ->  all of them now optional
93 sites wrapped an expression the wrong way     ->  migrated, operator trailing
```

## What was built

One rule, in one module, in two languages. `internal/lex` and `stage1/src/lex.origin` each
gained the same three-part test and emit the same token; the parser, the grammar in §02 and
its brace-depth error recovery are untouched, which is what kept the change affordable.

## Go's rule does not transfer, and that is the finding

The obvious plan was Go's rule verbatim. It is wrong for Origin in two independent ways, and
both trace to the same root: **Origin is expression-oriented and Go is not.**

- **A block's value is its trailing expression, written without a semicolon.** Go has no
  such form — its `return` is a keyword statement. Under Go's rule, the line before a closing
  brace takes an inserted semicolon, and **805 value-returning functions in this corpus would
  have quietly started returning `()`**. Not a migration cost; a silent change of meaning.
  Rule 3 — never insert before `}` — exists entirely for this.
- **`}` as a trigger costs more than it earns.** Go inserts after `}` and pays with the rule
  that `} else {` shares a line. Origin's dominant `match` idiom is a brace-bodied arm with
  the comma omitted — **442 sites** — and a semicolon between two arms is not grammatical at
  all. Dropping `}` from the trigger set removes that whole class, and the 7 `}`-then-`else`
  sites with it.

The measurement that settled the design is worth keeping: with `}` in the trigger set, 6,149
insertions and a rewrite of 442 match arms; with it out, **129 insertions and 93 breaks.**

## The migration wrote a bug, and the corpus caught it

The 93 wrapped expressions were migrated mechanically, Go-style: operator moved to the end
of the previous line. The script appended it to the *stripped* line — and where a line ended
in a trailing `//` comment, the operator landed **inside the comment** and vanished.

`obj.origin`'s `header_size` and `sizeofcmds` are sums written one term per line with a
comment naming each load command. Both silently lost terms. stage1's Mach-O writer then
could not pad its own header:

```
stage1: this is a compiler bug: the file is already 1016 bytes, cannot pad back to 112
```

The code compiled. It type-checked. It was a plausible wrong answer of exactly the kind
process rule 8 forbids, and it was produced by a tool rather than by a person — which is the
part to carry forward, because a tool makes that mistake uniformly and silently across every
file it touches.

**What made the migration safe was not re-reading the diff.** It was asserting the invariant
the migration claims: *a pure operator move changes nothing but line breaks.* Stripping
comments and whitespace from both versions and comparing the character streams proves it in
one pass over every file. Only `lex.origin` diverges, which is where ASI itself was written.
That check took a minute to write and would have caught the bug before the suite did.

## What this does not do

**It does not make Origin stop looking like C**, and nobody should read this phase as having
tried. The Phase 11 audit measured where that impression comes from: **18% of code lines are
nothing but a closing brace.** Semicolons are character-level texture; brace lines are whole
lines. This phase removes the texture.

**The corpus does not visibly change.** Of the lines that do not already end in a semicolon,
129 receive an inserted one. The 14,173 is what *may now be omitted* — a gain to code written
after this, not one that arrives by itself. Collecting it would mean reformatting 453 files,
moving every byte offset in the repository and churning every span, trace, line table and
golden: the same total-churn migration the audit priced for significant indentation and
declined. It is declined again here, and the reasoning is in ADR-0040 rather than in a
commit message.

## The suite is at 217s of 300s, up from 173s

Some is variance on two cores; a run that failed mid-phase took 237s. Some is real: every
trace the selfhost differential compares now carries the inserted tokens, and both lexers do
slightly more work per token. Phase 9's rule when this crosses is to look for the question
being asked more than once, not to cut coverage. It has not crossed, and nothing was cut.

## Deferred, and where it stands

**Significant indentation** is where the audit left it: a front-end redesign touching §01,
§02's recovery contract, both lexers, and every `.origin` file. This phase is the cheap half
of the same idea and does not bring it closer.

**Associated functions** (`List::new()`, 568 sites) and **tuple element access** (`t.0`) were
held by the user in Phase 11 and are still held. "Simplify the syntax" was not read as
consent to either; both are recorded in `docs/design-questions.md` as outside the delegation.

**The two pieces of drift the Phase 11 audit turned up are still open**, and neither is a
syntax question: `std::iter` is a normative §10 example that does not compile, and the
formatter is referenced twice in the spec but is neither built nor scheduled. The iterator
gap is the one the audit ranked first for changing how Origin *looks* — 852 `while` loops
against 28 `for..in`, and 731 manual index increments, each costing a nesting level and a
closing brace.
