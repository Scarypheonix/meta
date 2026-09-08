# Phase 10 — Complete

**Exit criteria:** rule 7 put the scope with the user, and what they asked for was a public
page where anyone can write and run real Origin in their own browser — the category
`play.rust-lang.org` and the Go Playground are in. Not a teaching subset; the full language,
exactly as specified.

**Status:** met. Origin compiles and runs client-side, and the corpus says it runs the same:

```
98 of the 102 end-to-end cases, on both engines, through the WebAssembly module
   -> byte-identical stdout, stderr and exit status to the native run
```

The four it does not cover are the four that use `std::fs`, named individually with their
reasons (ADR-0033). `./check` passes in 230s at 1,868 MiB against budgets of 300s and
3,072 MiB. **36 ADRs, 20 specification documents**, one new engine, and 720 lines of
hand-written JavaScript.

## What was built

`cmd/originwasm` is the whole pipeline — lex, parse, resolve, check, monomorphize, compile,
optimize, run — as a `GOOS=js GOARCH=wasm` module exporting **one function**. `web/` is the
page around it: an editor, the worked examples, a run button, and a footer that says where
the code goes.

| Piece | What it is |
|---|---|
| `cmd/originwasm` | the boundary: a request in, captured output and an exit status out |
| `web/worker.js` | holds the module; one program per message (ADR-0036) |
| `web/app.js` | the shell: editor, running, sharing, remembering |
| `web/origin-mode.js` | Origin highlighting, written against §01 |
| `web/share.js` | the fragment codec (ADR-0035) |
| `web/examples.js` | generated from `tests/e2e/cases` |
| `tests/wasm` | 196 runs against the corpus's golden files, under Node |
| `tests/web` | the page, driven in Chromium |

Five decisions, each an ADR: **0032** ships both engines (the VM by default) because
measurement made the second one cost 85 KB gzipped against a 1.33 MB floor the compiler front
end imposes either way; **0033** makes every file operation fail as the `Err(IoError::Other)`
§15 already defines, and puts the `os` calls behind a build tag so the module *cannot* reach a
filesystem rather than being trusted not to; **0034** takes CodeMirror over Monaco;
**0035** puts a shared program in the URL fragment, the only one of the three places it could
go that a browser never transmits; **0036** runs programs in a Web Worker, because a
non-terminating program on the main thread freezes the tab and `terminate()` is a stop button
that works without the engines carrying a browser's concern into their hot loop.

The whole download is **1.5 MB gzipped**, of which the editor is 94 KB.

## The thing that had to be found rather than designed

**Reading the code first changed the shape of the problem.** The phase brief named five
OS-dependent runtime features to resolve, and three of them did not exist to resolve:

- **FFI into libc.** ADR-0017's consequences say it outright — *"No FFI. Calling C from Origin
  is not possible in a binary that does not link one."* No syntax, no runtime, no corpus case.
- **kqueue-backed async I/O.** §08 scoped it to Phase 6 and §12 amended that scope away —
  *"Origin has no I/O to be asynchronous about."* It was never built.
- **stdout and stderr.** Never file descriptors in these two engines; `driver.RunAt`'s
  signature has ended in `stdout, stderr io.Writer` since Phase 1. Redirecting them into a
  page is passing a different writer, not a port.

That left `std::fs`: two files, four operations, the only place in either engine's path that
names the host. **A phase that looked like a port was a phase about one interface.**

## What it found

**§08's preemption does not survive the browser, and nothing below a full corpus run says
so.** §08 promises scheduling is *"preemptive at safepoints: a green thread that runs a loop
containing a back-edge can always be descheduled."* Both engines got that from the host and
neither survived losing it. The VM's safepoint releases the world lock and re-takes it, which
is a scheduling point only where a waiting thread runs on another processor; the interpreter
had no safepoint at all, and its own comment recorded why it did not need one — *"Go's own
scheduler is M:N, and it preempts, which is what §08 asks for"* — which is true on every host
that preempts and false on the one that does not, because preemption is delivered by signals
and a browser has none. `preemption_at_a_back_edge` **hung forever** on both engines. Both now
yield explicitly at a back edge, in `js`-tagged files that are empty everywhere else.

A program that hangs is the worst available way to differ: nothing reports it, and a
differential that sampled the corpus would have sampled around it.

**Two defects older than this phase, both surfaced by running the corpus harder, both
confirmed against Phase 9's own HEAD in a clean worktree before anything was changed.**

- **A stale reference in the VM's `spawn`.** The thread's closure was registered as a root
  before the goroutine started, which keeps it from being *freed* and is all that comment
  claimed. It does not keep it *correct*: the collector moves objects and rewrites the roots
  it was handed, and a Go local captured by the goroutine's own closure is not a root. A
  collection landing in the few instructions between registration and the thread's first
  instruction left it calling the address the closure used to be at. One defect, three
  symptoms, depending on what now lived there: `called an object that is not a closure`,
  `field read on a value that is not an object`, and `unknown TypeID 519: the heap and the
  descriptor table disagree`. It hit the full suite about once in three runs; with a nursery
  small enough to collect constantly — Phase 9's own technique — it reproduces in under a
  second.
- **A data race in `types.Prune`.** Path compression made a *write* to shared state out of
  what all of its callers perform as a read. Harmless while only the single-threaded checker
  runs; not harmless once a solved graph is read concurrently, which is what the interpreter
  does — `kindOfType` prunes on the arithmetic path and green threads are goroutines.
  `go test -race ./tests/e2e` reported **18 races, every one of them here**, and reports 0
  now. Compression was what had to go rather than a lock: by the time the graph is read
  concurrently it is immutable, so there is nothing left to compress that inference has not
  already shortened. On `originc check stage1/src`, 34,000 lines, the difference between the
  two versions is smaller than the variance between runs of either.

Both have regression tests, and both tests were checked against the code they replace and
fail on it. The `Prune` one asserts the no-mutation invariant directly rather than by running
threads, so it is deterministic and needs no `-race` — which the suite cannot afford: the race
run alone is 492s.

**`go tool nm` cannot read a wasm binary.** ADR-0033's structural half — the file operations
are not linked — was first "verified" by a command that answers `unrecognized object file`,
which greps to an empty symbol list and passes every assertion. The test searches the module's
bytes instead, and carries a **positive control**: it fails if the module contains no symbol
names at all, because without that a stripped build would satisfy it by containing nothing.
The search was checked both ways before being relied on. **A verification that cannot fail is
not one**, and the tell was that it passed the first time.

## The budget, again

It crossed 241s of the 300s ceiling before this phase closed, and the fix was the shape Phase
9 recorded: **find the question being asked more than once.** `tests/web` was running all
thirteen examples on both engines — twenty-six programs that `tests/wasm` already runs through
the same module against the same golden files. It runs three now, chosen for what a browser
uniquely answers: that output reaches the DOM, that a non-zero exit is reported, and that the
engine control reaches a second engine. 23.7s of work removed, ~11s of wall clock and 500 MiB
of peak RSS — the gap being that `tests/web` was never on the critical path.

`TestEveryExampleIsCoveredByTheWasmDifferential` is what keeps that cut honest: it fails if an
example ever uses `std::fs`, because that is exactly the class `tests/wasm` excludes, and the
argument for running three programs instead of twenty-six rests on it.

**The critical path is `tests/selfhost` at 212s.** Any future budget crisis is there, and
nothing in this phase moved it.

## What is deferred

`docs/deferred.md` has a Phase 10 section: one source file at a time, `originc build` in the
browser (a disassembly view is the reachable version), real storage behind `std::fs`, clicking
a diagnostic to jump to it, a server-backed short link (not planned — that is the point at
which this acquires a backend, and the brief says stop and ask), and deploying the page, which
is blocked on the user choosing a host.

## Read before Phase 11

- **The browser is a host, and `docs/spec/playground-runtime.md` is about a host.** It changes
  no language document, and ADR-0033 is why it is able to stay that short. Anything that would
  make the playground reject a program the compiler accepts, or accept one it rejects, is
  wrong at this level rather than a trade-off.
- **The editor is not on the critical path, and that is tested by taking it away.**
  `index.html` ships a real `<textarea>`; `app.js` upgrades it and hides which one the rest of
  the file got. `tests/web` blocks `codemirror.js` at the network and asserts the page still
  compiles and runs a program. The only way to know a fallback works is to remove the thing it
  falls back from.
- **`web/origin-mode.js` is a highlighter, not a second opinion about the grammar.** The
  compiler on the same page decides what is valid; where the two disagree the mode is wrong by
  definition. The three things it has to get right are all in §01 and all bite a naive
  tokenizer: block comments nest, string literals span lines, and `\(expr)` holds a real
  expression ending at the `)` matching its own `(` — so the state is a stack, not a flag.
- **Nothing the page offers is written twice.** The examples are generated from
  `tests/e2e/cases`, which came from `docs/spec/10-examples.md`. A hand-maintained copy of
  thirteen programs is a thing that drifts, and the generated one has a test that says when.
