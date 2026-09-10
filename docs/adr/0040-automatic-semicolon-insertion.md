# ADR-0040: Semicolons are inserted at a line break, with three rules

**Status:** accepted · **Date:** 2026-09-10 · **Decided by:** implementer (user delegated)

## Context

**14,173 lines of the 31,771 in this repository's Origin source end in a semicolon.** It is
the most-typed character in the language after the space, and every one of them sits where a
line break already says the same thing.

The Phase 11 audit put the surface question in front of the user and measured every
candidate. Its finding about this one was that automatic semicolon insertion is the only
*grammar* simplification with a good ratio — the rest were either cosmetic (`->`, `::`) or a
front-end redesign (significant indentation). The user then delegated the choice
(`docs/design-questions.md`, Phase 12).

**§01 asserts the opposite of this ADR** — "Origin is **not** newline-sensitive: there is no
automatic semicolon insertion and newlines are indistinguishable from spaces to the parser" —
as a bare statement of fact with no rationale and no ADR behind it. Searching every ADR turns
up no discussion of semicolons, newlines or block delimiters: the Phase 0 delegation asked
fourteen language questions and surface syntax was not among them. So there is nothing here
to reverse. The assertion was a default that was never examined, and this is the examination.

## Options considered

- **Leave it.** Costs nothing and keeps §01's one-line rule, which is genuinely the simplest
  rule a lexer can have. Rejected because the user asked, and because 14,173 is not a
  rounding error.

- **Go's rule, transferred unmodified.** Insert a semicolon when a line's last token is an
  identifier, a literal, `)`, `]`, `}`, or one of a few keywords. **This does not work**, and
  finding out why is the substance of this ADR:

  - **Origin is expression-oriented and Go is not.** A block's value is its trailing
    expression written *without* a semicolon (§02), so `fn f() -> u64 { 1u64 << w }` returns
    a `u64` and `{ 1u64 << w; }` returns `()`. Go has no such form — its `return` is a
    keyword statement. Under Go's rule the line `1u64 << w` before a closing brace would take
    an inserted semicolon and **every value-returning function in the language would silently
    start returning `()`**. Not a migration cost: a change of meaning, applied invisibly, to
    805 sites in this corpus.
  - **`}` as a trigger costs more than it earns.** Go inserts after `}` and pays for it with
    the rule that `} else {` must share a line. Origin's dominant `match` idiom is a
    brace-bodied arm with the comma omitted — **442 sites here** — and a semicolon between
    two arms is not grammatical at all. Add 7 `}`-then-`else` sites.

- **Go's rule, with `}` removed from the trigger set and insertion suppressed before a
  closing brace.** Chosen. The two carve-outs are exactly the two places Origin's
  expression-orientation shows through, and each removes an entire failure class rather than
  a list of sites.

## Decision

A semicolon is inserted at a line break when **all three** hold:

1. the last token of the line is one that can end a statement — an identifier, an integer,
   float, string or character literal, `true`, `false`, `self`, `)`, `]`, or one of `break`,
   `continue`, `return`. **`}` is deliberately not in this set;**
2. the lexer is not inside an unclosed `(` or `[`;
3. the next token that is not whitespace or a comment is not `}`.

Written semicolons remain legal everywhere they are legal today. Nothing in the corpus is
reformatted to drop them.

## Consequences

- **The parser does not change.** The lexer emits the same `Semi` token the source would have
  written, so the grammar in §02, its LL(2) property, and its brace-depth error recovery are
  all untouched. This is what keeps the change inside one module.

- **93 sites in the repository break, and they are one shape.** An expression wrapped across
  lines with the operator beginning the continuation line — 64 `||`, 14 `+`, 10 `&&`, 5
  method chains. The fix is Go's: put the operator at the end of the previous line. These are
  the only source edits the phase makes, so golden churn is confined to their files.

- **The corpus does not visibly change, and that is the honest shape of the benefit.** Of the
  lines that do not already end in a semicolon, only 129 would receive an inserted one. The
  14,173 is what *may now be omitted*, not what disappears today — the gain is to code written
  after this, and it does not arrive by itself. Reformatting the corpus to collect it would
  move every byte offset in the repository and churn every span, trace, line table and
  golden; that is the same total-churn migration the audit priced for significant indentation
  and declined, and it is declined again here.

- **This does not address what the audit found was actually making Origin look like C.**
  That is braces and nesting: **18% of code lines are nothing but a closing brace.**
  Semicolons are character-level texture; brace lines are whole lines. Anyone reading this
  ADR expecting the language to look different afterwards should read
  `docs/phases/11-complete.md`'s bottom line instead — the lever there is the standard
  library's missing iterators, not the grammar.

- **Rule 3 needs one token of lookahead past whitespace and comments**, which the lexer can
  do because it already scans both. It is the only place in the lexer where a decision
  depends on what comes *after*, and it is why this rule lives in the lexer rather than the
  scanner loop.

- **Reversing it is deleting the insertion and restoring §01's sentence.** Every program
  written before this ADR still parses identically, because insertion only ever adds a token
  where the source could legally have written one. Programs written *after* it, with the
  semicolons omitted, would not — which is the asymmetry that makes this worth an ADR rather
  than a note.
