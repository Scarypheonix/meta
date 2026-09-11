# Phase 15 — Complete

**Exit criteria:** the user asked for a tutorial, with pictures, that shows how to write
Origin — **and how to take user input.** Origin could not take user input. `docs/deferred.md`
had carried standard input since Phase 0 with the reason attached: *"it is a stream, and §15
has so far avoided handles entirely (ADR-0030)."* So the phase is the feature, and then the
pages that teach it.

**Status:** met. `./check` passes in **205s at 1,917 MiB** against budgets of 300s and
3,072 MiB. **43 ADRs, 21 specification documents.**

## What was built

| | |
|---|---|
| **ADR-0043**, `docs/spec/18-input.md` | standard input, a line at a time, with no handle |
| `read_line()` `input()` `ask(prompt)` | prelude Origin over two compiler-provided operations |
| `internal/stdin` | where a line ends, shared by the interpreter and the VM |
| `internal/backend/input.go`, `stage1/src/input.origin` | `rt_stdin_read`, in machine code, twice |
| the playground's **input box** | a browser has no standard input, so the page supplies it |
| `web/tutorial.html`, `web/pictures/*.svg` | nine steps, each a picture of a program that compiles |
| `README.md` | the repository had none |

## The design question, and what answered it

ADR-0030 chose whole-file reads over handles: `read_to_string(path)`, no `open`, no cursor.
That was possible because **a file has a size and can be read twice.** Standard input has
neither property, so the handle-free shape does not transfer for free — it has to be
re-earned, and the three candidates each fail somewhere:

- **A `Stdin` handle** costs the precedent, not the type. One handle is a whole category.
- **`read_to_string()` with no argument** is literally ADR-0030's shape with the path
  removed, and it has *no* size limit — but it cannot be interactive. A program that prints
  `What is your name? ` and then reads everything blocks until end of input. It reads a pipe
  well and a person badly.
- **A line at a time** is the granularity a person types at *and* the granularity a
  pipeline's producer flushes at, and it needs no new type.

**The audience decided it.** A whole-input read would have been the smaller change and the
cleaner spec, and the tutorial's second program would not work.

## Three things this cost that the shape did not advertise

- **One `read` system call per byte, in native code.** A buffered read consumes bytes past
  the newline, and a stateless operation has nowhere to keep them — the only place would be
  runtime-global state every green thread would then share and lock. So the routine reads
  one byte at a time. That is a real cost, taken deliberately, and it is written at the top
  of `internal/backend/input.go` so nobody "fixes" it later.

- **A maximum line length**, `layout.MaxInputLine` = 64 KiB, because the native routine
  accumulates into its own frame. It is `internal/layout`'s number so that all three engines
  refuse the same input (process rule 5), and it is a trap rather than a truncation. The
  boundary is tested: a line of exactly 65,536 bytes succeeds on all three engines and
  65,537 traps on all three.

- **Two new statuses, not one.** `IOEndOfInput` and `IOTooLong` sit beside §15's four,
  because the two traps say different things and a status that could mean either could not
  choose which to say.

**What it did not cost: a second held-text slot.** `io::taken_line` *is* `fs::taken_text` —
the same field in both Go engines and literally the same call, `rt_fs_taken`, in native
code. The two are never live at once, and one slot means one thing for the collector to
scan.

## `input()` lies about nothing, and still flattens `Option`

`read_line()` returns `Option[String]` and `input()` returns `String`, so `input()` cannot
tell an empty line from the end of the input. That is deliberate and it is written down:
`Option` is the fourth thing that blocks a beginner (`docs/phases/14-complete.md` prices all
four), and the program a beginner writes does not care. A filter that reads until its input
ends does care, and `read_line` gives it the honest answer. **Both exist; neither returns
something that is not true.**

## The browser is the one place ADR-0033 does not extend

ADR-0033 fails every file operation in the browser because there is no file and nothing on
the page could be one. **A page can hold text in a box.** So the playground has an input
box, its contents cross the boundary with the program as one string, and `read_line` walks
them a line at a time and then reports the end — the same lines, the same end, the same
limit. `tests/wasm` runs the corpus's `.in` files through it and compares byte for byte
against the native runs, so this is a differential claim rather than an assertion.

## What the corpus found, again

The phase rewrote every program the playground shows into the syntax of Phases 11–14 — no
`use std::io`, no `fn main`, `Some`/`Ok`/`Less` unqualified, `[a, b]` for a list, and a
semicolon only where a line break does not end the statement. Six of those files then ended
with a `}`, and **`tests/selfhost` immediately reported a Phase 14 defect**:

```
tests/e2e/cases/fib.origin
  stage1: "expr-stmt ;"
  go:     "expr-stmt"
```

`stage1/src/parse.origin` recorded every top-level tail expression as having a semicolon.
Phase 14 wrote that line and nothing caught it, because the one case Phase 14 added ends in
`)` — where automatic semicolon insertion supplies a real one — and **no file in the corpus
had a top-level statement that ended in `}` until this phase wrote one.** The fix is one
`false`.

The lesson is Phase 11's and Phase 13's again, and it keeps arriving in the same shape: a
differential oracle is only as wide as the corpus it runs on, and **the cheapest way to widen
it is to write the programs you would show someone.** Three phases of syntax work were
validated against a corpus that did not use the syntax.

## Two things ADR-0040 will be asked about

Writing the examples surfaced the two places a reader reaches for a line break and does not
get one, because neither `}` nor `?` is a statement terminator:

```origin
let cell = Cell { value: 0 };     // the `;` is required
let x = parse_digit(a)?;          // and here
```

`}` is settled: ADR-0040 measured it and a trigger there would have made **805
value-returning functions quietly return `()`**. `?` was not measured, because nothing in
the corpus ended a line with one until `result_and_try` was rewritten. It is left alone
rather than added quietly — the trigger set is ADR-0040's decision and widening it is a
decision, not a patch — and it is named here so that whoever revisits it starts from the
count rather than from the annoyance.

## The pages

`web/tutorial.html` is nine steps. Every step shows a **picture** of a program, and the
picture is generated rather than drawn: `tests/web/picture_test.go` renders it from source
with the project's **own lexer** (`internal/lex`) and compares it against the committed SVG
like any other golden file. Six of the nine programs are corpus cases, named rather than
copied; the other three are compiled by that same test. So a tutorial program that stopped
compiling would fail the build before anyone saw it.

The programs behind the page's **Copy** and **Run it** buttons come from the same sources as
the pictures, through one generated file — because a picture cannot be copied, and writing
the program twice is how the two come to disagree.

## Carrying forward

- **The corpus is the oracle's width.** Widen it by writing what you would show someone.
- **`input()`/`read_line()` is the shape to reuse** when a beginner-facing convenience and
  an honest answer disagree: ship both, and say in the spec which is which.
- **Phase 16's scope is the user's** (rule 7). The three barriers Phase 14 priced —
  `xs[0]` (ADR-0013), mandatory signatures (ADR-0009), `Option` on every index (ADR-0007) —
  are unchanged and are still the user's call.
