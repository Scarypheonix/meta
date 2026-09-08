# ADR-0036: A program runs in a Web Worker, and its output is chunked and bounded

**Status:** accepted · **Date:** 2026-09-08 · **Decided by:** implementer (user delegated)

## Context

Origin programs do not have to terminate, and a playground's visitors will write ones that
don't — a `while true` with the increment forgotten is the second program anybody writes by
accident. Natively that is a process to `^C`. In a browser it is different in kind:
JavaScript and WebAssembly on the main thread are not preemptible, so a non-terminating
program freezes the tab it runs in. Not "makes the page slow" — the run button, the editor,
the stop button and the tab's own close animation all stop, until the browser offers to
kill the page.

This is the browser-specific consequence of §5 of `docs/spec/playground-runtime.md`, and it
is the one the phase brief did not anticipate, because it is not a mismatch between Origin's
runtime and the host. Nothing about Origin's green threads is wrong here: they are
goroutines, Go's js/wasm port schedules goroutines, and a program that yields behaves
exactly as it does natively. The problem is that the *host* has one thread that is also its
user interface.

The second question is a consequence of the first. Output has to get from the module to the
page, and the two obvious disciplines are both wrong:

- **Post every write.** A program printing in a loop generates a `postMessage` per line.
  The main thread spends its time in the message queue, which reintroduces the
  unresponsiveness a worker was supposed to remove.
- **Post once at the end.** A program that prints and then never terminates shows nothing,
  ever — the exact program this ADR exists because of.

And output can be unbounded: a `while true { io::println("x"); }` accumulates until the tab
runs out of memory, which is the freeze again by a slower route.

## Options considered

For where the module runs:

1. **Main thread.** Simplest, and freezes on the first runaway program with no recovery
   short of killing the tab.
2. **Main thread with an instruction-count budget.** The engines could count and abort. It
   makes every engine carry a browser's concern into its hot loop, it changes what a program
   observes (a program that legitimately runs a long time is killed), and the count would
   have to be a language-visible limit or an arbitrary one. It also does not exist and would
   have to be built into two engines.
3. **A Web Worker.** The module runs off the main thread. The page stays responsive by
   construction, and `worker.terminate()` stops a runaway program immediately, at any point,
   with no cooperation from the program, the engine or Go's scheduler.

For output:

**A.** post per write · **B.** post at the end · **C.** buffer in the worker, flush on an
interval and at every terminal event.

## Decision

**Option 3 and option C.**

The module runs in a dedicated Web Worker. The page holds a handle to it and nothing else;
the worker holds the module and nothing about the DOM. `terminate()` is the stop button,
and it works on a program that has stopped yielding, which is precisely the case that
matters and the only one option 2 could not have handled without changing what programs
observe.

Output accumulates in the worker and is posted on a flush interval, and unconditionally on
every terminal event — normal return, `process::exit`, a trap, or the output ceiling being
reached. A program that prints and then loops forever therefore shows what it printed,
within one interval, which is what option B could not do and what makes this a correctness
property rather than a performance tuning.

Output is bounded by a byte ceiling. On reaching it the run stops accumulating and the page
reports the truncation.

Three properties are load-bearing and are stated in §4 and §5 of
`docs/spec/playground-runtime.md` rather than only here:

- **Termination is not a language event.** A terminated program produces no trap, no exit
  status and no diagnostic. It did not do anything wrong; it was stopped from outside. The
  page reports that *it* stopped the program, in the page's own words.
- **The truncation notice is the host speaking.** It never enters the program's captured
  stdout or stderr, because a run whose output was cut must not be confusable with a program
  that printed a notice about being cut.
- **Ordering within a stream is exact.** Chunking is about when bytes are delivered, never
  about which order they are in, and never about their content. §6's byte-for-byte agreement
  with native output is unaffected by any of this — the differential compares the
  concatenation, and it must be identical.

## Consequences

- **The stop button is real**, and works on the worst case rather than the polite one.
  A playground whose stop button only works on programs that were going to stop anyway
  would be a stub that lies.
- **The engines are untouched.** No instruction budget, no yield point, no browser concern
  in a hot loop, and nothing about a program's observable behaviour differs from native
  because of this ADR. That is the main reason option 3 beats option 2.
- **A terminated worker is a discarded module**, so the next run starts a fresh one and
  pays the module's instantiation cost again. Correct rather than fast: reusing a worker
  whose program was killed mid-collection would inherit whatever state it was in.
- **Two numbers are tuning, not semantics** — the flush interval and the output ceiling.
  They live in `cmd/originwasm` with their reasoning, and changing them changes no
  program's meaning.
- **The page must handle a worker that never reports.** A module can fail to instantiate,
  and a browser can refuse to start a worker. Both are the page's own error channel, and
  neither may be reported as though the program failed.
- **Long-running-but-finite programs are not penalised.** There is no deadline. A program
  that takes ten seconds takes ten seconds and then prints its answer; only a visitor
  pressing stop ends a run early.

## Reversing it

Running on the main thread again would be a small change to the page and a large regression
in what the site can survive, and the stop button would have to go with it. The interesting
reversal is the opposite direction: multiple workers, to run the same program on both
engines at once and show that they agree. Nothing here forecloses that — the worker holds
no singleton state and the boundary is already a pure function of its request.
