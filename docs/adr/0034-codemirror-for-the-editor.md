# ADR-0034: The editor is CodeMirror 6, vendored and pinned

**Status:** accepted · **Date:** 2026-09-08 · **Decided by:** implementer (user delegated)

## Context

The playground needs a text editor with Origin syntax highlighting. The two candidates are
the two everything in this category uses: **Monaco**, the editor extracted from VS Code and
what Compiler Explorer runs, and **CodeMirror 6**, what the Rust and Go playgrounds' modern
rebuilds and most embedded editors run.

Both are MIT licensed, so licence does not separate them.

Neither has an Origin mode, and neither can: Origin is this repository's language and
nothing outside it has heard of it. So "how good is the language support" is not a
criterion — a mode has to be written either way, and what matters is how much work that is.

The constraint that does separate them is the one ADR-0032 introduced: **the page is a
download, and the WebAssembly module is already ~1.5 MB gzipped of it.** The editor's
bytes are added to a budget that is already mostly spent, and unlike the module's they buy
no capability that is the point of the site — a visitor comes to run Origin, not to have
an IDE.

The second constraint is ADR-0035's and the phase brief's: static hosting, no backend, and
nothing about a visitor's code leaving their browser. An editor loaded from a third-party
CDN at runtime is a third party on the page, and this site's whole claim is that there
isn't one.

## Options considered

1. **Monaco.** The most capable option by a wide margin: a real language-server protocol
   client, multi-file models, a diff view, the whole VS Code editing surface. Its cost is
   an order of magnitude more JavaScript than CodeMirror, delivered as multiple chunks with
   its own worker for tokenization, and a bundling story that is genuinely awkward to
   vendor without adopting its build assumptions. Almost none of what it is good at is
   reachable here: there is no language server for Origin, no second file (§8 of
   `docs/spec/playground-runtime.md`), and no diff.
2. **CodeMirror 6.** An order of magnitude smaller, modular enough that a build includes
   only the extensions used, good on touch devices, and with `StreamLanguage` — a
   tokenizer interface that takes a function over a character stream, which is exactly the
   shape Origin's lexical grammar (§01) already has. Fewer features, essentially all of
   which are ones this page has no use for.
3. **A `<textarea>` and no library.** Zero bytes, and what the phase's step 4 proof uses
   deliberately. Sufficient to run programs; insufficient as the finished thing, because
   an unhighlighted, un-indented editor is a worse place to read a language than the spec
   is.

## Decision

**Option 2: CodeMirror 6, vendored into the repository as a pinned pre-built bundle and
served from the same origin as everything else.**

The deciding argument is that Monaco's advantage is entirely in capabilities this page
cannot use, while its cost lands on the one budget that is already tight. That is not a
close trade. `StreamLanguage` over §01's token rules is a small amount of code with an
existing normative description to write it against, and highlighting is the only editor
feature the page actually needs.

**Vendored and pinned**, not loaded from a CDN, for three reasons that are all the same
reason: a CDN is a third party who can see every visitor, is a dependency that can go away,
and would make the privacy claim in the UI ("your code stays in your browser") require an
asterisk. A checked-in bundle is auditable, works offline, deploys to static hosting with
no build step at deploy time, and keeps the site to one origin.

This ADR does **not** decide the editor's visual design, its keybindings, or whether it
offers completion. Those are reversible and belong to the phase step that builds the UI.

## Consequences

- **A vendored bundle is a dependency the repository now carries**, with a version to
  record and a provenance to state. It is checked in with its version pinned and its
  licence file beside it, and updating it is a deliberate commit — the same discipline
  `bootstrap/` gets, for the same reason: a binary nobody can reproduce is not a
  dependency, it is a mystery.
- **The Origin mode is ours to write and ours to keep correct.** It is a highlighter, not a
  parser, and it must never become a second opinion about the grammar: the compiler in the
  same page is the authority on whether a program is valid, and the mode's only job is
  colour. Where they disagree, the mode is wrong by definition.
- **No npm at deploy time.** The static site is files in a directory. Whether a build step
  exists in development to produce the bundle is separate from what is served, and what is
  served is committed.
- **The editor is not on the critical path to running a program.** The module and the run
  button work without it; the proof in this phase's step 4 is a `<textarea>`. If the editor
  fails to load, the page can still run Origin, and it should be built so that this stays
  true.
- **Monaco's absence closes off a language server later.** If Origin ever grows one, this
  decision is where that gets revisited — and by then the argument would have changed,
  because the capability would be reachable rather than theoretical.

## Reversing it

The editor is one component behind a small interface: give it text, get text back, tell it
about a diagnostic's span. Swapping it is a contained change as long as nothing else on the
page reaches into it, which is the constraint to hold. The reason to reverse would be
Origin acquiring the kind of tooling Monaco exists to host.
