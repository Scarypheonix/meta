# Phase 6 — Complete

**Exit criteria:** Origin has threads, channels and a mutex, specified before they were
built; `Send` is a compiler rule and not a convention; and every program in
`docs/spec/12-concurrency.md`'s worked-examples table runs identically on the interpreter,
the virtual machine and native code, at every optimization level.

**Status:** met. `./check` passes in 24s at 207 MiB, against budgets of 300s and 3072 MiB.
29 packages report `ok`; 40,590 lines including tests; 46 end-to-end cases × 7 engine/level
combinations, 258 conformance cases, 27 ADRs.

The last criterion is the one that took the phase. `concurrencyCases` — the ledger of cases
an engine could not yet run, kept the way Phase 5 kept `nativeSkips` — is empty: sixteen
concurrent programs, including four that trap, produce byte-identical stdout, stderr and
exit status on all three engines. The trap messages agree down to the source span, which
turned out to be a harder claim than it reads.

## What was built

**`docs/spec/12-concurrency.md`, ADR-0025 and ADR-0026** — the specification first, as
process rule 1 requires. Concurrency adds no keywords, no operators and no grammar
productions (ADR-0025): `spawn` is a function, a channel is a generic type, `Mutex` is a
struct with a method. Origin already had closures as values and monomorphized generics,
which is everything a library form needs, and a keyword would have had to be threaded
through the lexer, the grammar, the AST, resolution, the checker and three engines. A panic
kills the process (ADR-0026), and that is a decision with a reason rather than an omission:
per-thread isolation needs a dead thread to leave shared state usable, and under a shared
heap with `Mutex` as the sharing mechanism, a thread that dies inside `with` holds a lock
over a half-mutated value. Rust survives that with unwinding plus poisoning and Erlang by
sharing nothing; ADR-0006 ruled out the first and ADR-0008 the second.

**`internal/check/send.go`** — `Send` derived structurally, which is what makes ADR-0014's
"no data races in safe Origin" a compiler rule rather than a document. A type is `Send` when
it has no `mut` field and its fields are all `Send`. E0700/E0701/E0702 name the offending
field and chain through nesting, and E0701 checks a spawned closure's *captures* — which no
type can express, and without which a mutable aggregate reaches another thread by capture
instead of by channel, entirely legally.

**The prelude surface** — `JoinHandle[T]`, `Sender[T]`, `Receiver[T]`, `Mutex[T]`, with
methods calling compiler-provided operations. Those operations return a bare handle; the
wrapping into a prelude type happens in Origin or in `internal/compile`, where the call's
own checked type names the instantiation to build. That is what lets three engines share one
design: the native backend has no notion of "the prelude's `Option`" to build from machine
code, and now nothing asks it to.

**Three runtimes.** The interpreter got threads from goroutines, and is genuinely parallel
because Go's collector finds its values wherever they are. The virtual machine owns a
precise moving collector, so its threads interleave under one world lock released at
safepoints — which is exactly the scheduling §08 specifies, and exactly what makes a
collection safe. Native code has neither a host runtime nor a lock: a green thread there is
a stack the runtime mapped itself, a saved register set, and a routine that swaps one for
another.

**The native scheduler** (`internal/backend/sched.go`) — `join` alone needed none: there was
one thing a thread could wait for and it could name it. A channel breaks that, because a
thread parked on a receive waits for whichever thread eventually sends, which nobody knows
when it parks. So parking means "hand the processor to whoever can use it", waking is a
broadcast and each thread re-checks its own condition, and "no thread is runnable" is §12's
total deadlock — detected rather than hung on. `main` returning drains the threads nobody
joined, because §12 says the process waits for them.

**Channels and `Mutex` in machine code** (`chan.go`, `mutex.go`) — a ring with a length, a
head, a closed bit and a count of parked receivers; a lock that is one word naming its owner.
The blocking conditions are written as the same expressions the virtual machine tests, on
purpose: the two engines have to agree on which programs deadlock, and copying the predicate
is a surer way to get that than deriving it twice.

**Preemption at back edges** — §08 asks for it, and a cooperative scheduler does not give it:
a loop that calls nothing runs to its end however long another thread has been ready. It is a
countdown rather than a clock, because a timer means a signal handler; and it is emitted only
in a program that contains a `spawn`, because the check makes every back edge a call site and
so pushes every loop-carried value out of the caller-saved registers. A single-threaded
program's loops are exactly what they were before this phase.

**`internal/backend/spans.go`** — the machinery for a trap that knows its message but not its
line. `send on a closed channel` is discovered inside a runtime routine, and the call that
reached it was written in the prelude, so the span the backend holds names a file the
programmer never opened. Both other engines resolve this at run time by walking out of the
prelude; native code now does the same, against a table of user call sites keyed by return
address — the same shape as the collector's own stack walk.

## The decision that shaped this phase

ADR-0014, made in Phase 0 and cashed in here: a value crossing a channel must be `Send`,
`Send` is the absence of `mut`, and `Mutex` is the only shared mutable thing. Every part of
this phase leans on it. It is why `spawn` needs a bound on the *closure* and not only on the
values it names; why a channel can hand a value over by reference and copy nothing; and why
the collector can move objects between threads at all — no thread is ever halfway through
mutating one another thread can see.

## What surprised me

**The collector's root set had holes in it that nothing single-threaded could show.** Five
of them, found one at a time, each by pushing on the next piece of the phase:

- A constructor pushed its field values to the raw stack across its own allocator call. The
  stack is not in the root set, so a `Node { value: i, tail: out }` whose allocation
  triggered a collection got a `tail` pointing into the space just vacated.
- A closure body read its captures from `[rbp + 16]` every time — the word the caller left
  above the return address, which no stack map can name and no collection updates.
- `main` was deliberately left off the runtime's thread list, on the reasoning that the
  running thread's stack is walked from its live registers. True only while `main` is the
  thread running, and it is not while it waits inside `join`.
- `rt_join` set up no frame pointer, so the walk that reached a parked thread started one
  frame too high and described ordinary Origin code with the synthetic entry a runtime frame
  gets — no roots of its own.
- The thread trampoline called a spawned closure with the object in `rdi`, which works right
  up until the closure captures something.

The first two predate this phase entirely: they were reachable in Phase 5 by any program
that allocated 64 MiB, and no test did. The lesson is in `internal/backend/collect_test.go`,
which shrinks the heap around a single build so that a collection that actually moves objects
is something a five-minute suite can reach. Every one of those bugs has a test there that
fails on the code it replaced.

**A trap's line moved when the optimizer inlined.** `-O2` reported a `divide by zero` inside
a small helper at the line that *called* the helper, because inlining stamped the call site's
span onto every instruction it cloned. spec/11-codegen.md requires a trap's text to be
identical across all three engines at every optimization level, so this was an optimization
the program could read back. The fix has one wrinkle worth the ADR it did not get: the
prelude is the exception, and for the same reason rather than a special case — an operation
the prelude performs on the caller's behalf already reports the caller's line when it is
*not* inlined, because the other engines walk out of the prelude at run time.

**A specification can be wrong in a way tests do not catch.** §12's worked-examples table
ended with a claim that every row was an end-to-end case. Nine of sixteen were. One row was
not runnable Origin at all — `r.recv().unwrap()`, where `Option` has no `unwrap`, the only
mention of one anywhere in the specification. Two others were compile-time verdicts and
belonged in `tests/conformance/` rather than `tests/e2e/`. The table now says what is true,
and the seven missing cases exist.

## What was deferred

Recorded in `docs/deferred.md` with the phase each is scheduled against. The ones that matter
most for reading this code later:

- **Per-thread panic isolation** stays deferred, now blocked on ADR-0006 rather than
  scheduled: it needs unwinding or a supervisor model, and ADR-0006 closed the door on the
  first.
- **`select` over several channels** — every program in this phase's scope is expressible
  with one channel per thread or a `Mutex`, and `select` needs a multi-way blocking primitive
  and a fairness policy. It should land as a function over a list of cases, or ADR-0025 is
  being revisited by accident.
- **Real parallelism**, in the virtual machine and in native code alike. Both run one thread
  at a time for the same reason: a precise moving collector needs every stack to be a root
  set and no thread to be mid-read of an object another is about to move. §12's determinism
  clause already says no valid differential case can observe the difference.
- **The native runtime reclaims nothing** it maps for a channel, a mutex or a finished
  thread's stack. Nothing dangles — that is what makes the leak safe — but a program that
  makes channels in a loop grows without bound. Freeing one needs the runtime to know when
  the last handle is gone, and a handle is an integer inside a prelude struct rather than a
  reference the collector can trace.
- **A green thread's stack has no guard page**, so running off the end of its 8 MiB faults
  instead of trapping with §08's `stack overflow`. The page is easy; turning the fault into a
  named trap needs a signal handler.
