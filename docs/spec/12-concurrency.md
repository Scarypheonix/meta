# 12 — Concurrency

Phase 6. This document is normative. It specifies the surface of Origin's concurrency:
what a thread is, how one starts, how two communicate, and what happens when one fails.

The *memory model* is not specified here — §08 already fixes it, and ADR-0014 decided it.
This document must not contradict either. In one sentence: threads share the heap, a
value crossing a channel must be `Send`, and `Mutex[T]` is the only shared mutable thing.

## Nothing here is syntax

Concurrency adds **no keywords, no operators and no grammar productions**. `spawn` is a
function, a channel is a generic type, and `Mutex` is a struct with methods. A program
that never imports `std::thread` parses and compiles exactly as it did before Phase 6.

This is ADR-0025, and it is a deliberate departure from Go, whose `go` and `select` are
statements. Origin already has closures as first-class values (ADR-0020) and
monomorphized generics (ADR-0010), which is everything a library form needs; a keyword
would buy nothing and would have to be threaded through the lexer, the grammar, the AST,
resolution, the checker and three engines.

## Threads

A **green thread** is a unit of execution scheduled by Origin's own M:N scheduler onto a
pool of OS threads. §08 fixes the rest: preemption at safepoints, a stack that grows on
demand to 8 MiB, and `stack overflow` as a trap rather than corruption.

```origin
use std::thread;

fn spawn[T: Send](body: fn() -> T) -> JoinHandle[T]
```

`spawn` starts `body` on the scheduler and returns immediately. The returned handle is
the only way to observe the thread's result.

```origin
fn (h: JoinHandle[T]) join() -> T
```

`join` blocks the calling thread until the spawned one returns, and yields its value.
Joining is not required: a program may spawn and never join. The process does not exit
while any green thread is still runnable — `main` returning ends the program only after
every spawned thread has finished. This differs from Go deliberately: a Go program's exit
racing its goroutines is a common bug, and §04's "no undefined behaviour" is easier to
keep if the answer is never "whichever happened first".

`JoinHandle[T]` is `Send` when `T` is, so a handle may itself be sent to another thread.
Joining the same handle twice TRAPS with `handle already joined`.

**`body` must be `Send`.** A closure is `Send` when all of its captures are (§08), so
this is the rule that stops a mutable aggregate from reaching another thread by capture
rather than by channel. The bound is on the closure, not only on the values it names.

## Channels

```origin
use std::chan;

fn channel[T: Send](capacity: i64) -> (Sender[T], Receiver[T])
```

A channel is a queue of `capacity` values. `capacity` 0 means **rendezvous**: a send
completes only when a receive is ready for it, and vice versa. A negative capacity TRAPS
with `channel capacity is negative`.

Both halves are `Send` and both may be copied freely; a channel is a shared object like
any other (ADR-0008), so copying a `Sender` does not copy the queue.

```origin
fn (s: Sender[T]) send(value: T) -> ()
fn (r: Receiver[T]) recv() -> Option[T]
fn (s: Sender[T]) close() -> ()
```

- `send` blocks while the queue is full. Sending on a **closed** channel TRAPS with
  `send on a closed channel`. That is a programming error in the same sense division by
  zero is — the sender has lost track of who owns the channel's lifetime — and §04's
  rule is that such an error stops the program rather than returning a value nobody
  checks.
- `recv` blocks while the queue is empty. It returns `Some(v)` for each value in send
  order, and `None` once the channel is closed **and** drained. `None` is therefore
  permanent: a receiver that sees it will never see `Some` again.
- `close` marks the channel closed. Values already queued remain receivable. Closing an
  already-closed channel TRAPS with `channel already closed`.

Only a sender closes. There is no receiver-side close, because the receiving end cannot
know whether another sender still exists.

Values cross a channel **by reference**, like every aggregate (ADR-0008). Nothing is
copied and nothing is deep-cloned; `Send` is what makes that safe, since a `Send`
aggregate has no `mut` field to race on.

## Mutex

```origin
use std::sync;

fn Mutex::new[T](value: T) -> Mutex[T]
fn (m: Mutex[T]) with[R](body: fn(T) -> R) -> R
```

`with` acquires the lock, calls `body` with the protected value, releases the lock, and
returns what `body` returned. There is no `lock()` returning a guard, because a guard
would have to be released by a destructor and Origin has none (ADR-0006 has no
unwinding, so there is no place to hang one). A closure has an end; that is the whole
mechanism.

`Mutex[T]` is `Send` when `T` is. The protected value may have `mut` fields — that is the
point — and mutating it inside `body` is exactly how shared mutable state works.

Re-entering the same mutex from the thread already holding it TRAPS with
`mutex re-entered by the same thread`, rather than deadlocking. A hang is defined
behaviour but a useless one; a trap names the bug at the moment it happens.

Sending the value out of `body` and using it afterwards is not prevented by the type
system in 0.1. It is a real hole, and it is recorded in `docs/deferred.md`: closing it
needs a lifetime or region system, which ADR-0004 declined for the language as a whole.

## Deadlock

When every green thread is blocked — on `recv`, on `send`, on `join`, or on a mutex — and
none can be woken, the scheduler TRAPS the process with `all threads are blocked`,
exiting 101 like every other trap.

A program that hangs forever is not a wrong answer, but it is not a useful one, and the
condition is exactly detectable: the scheduler knows how many threads exist and how many
are parked. Diagnosing it is the runtime's job, not the programmer's.

This detects *total* deadlock only. A subset of threads deadlocked while others run is
not detected, and is not a trap.

## Panics

**A panic kills the process, including every green thread**, exactly as in §09. Phase 6
does *not* introduce per-thread panic isolation, and this is a decision rather than an
omission: ADR-0026 records why.

The short form: isolation requires that a thread's failure leave shared state usable by
the survivors. Under a shared heap (ADR-0008) with `Mutex` as the sharing mechanism
(ADR-0014), a thread that panics inside `with` holds a lock over a value it was halfway
through modifying. Rust survives this with unwinding plus lock poisoning; Erlang survives
it by sharing nothing. Origin has neither — ADR-0006 ruled out unwinding — so a surviving
thread would acquire a lock over a half-written value with no way to know. Killing the
process is the only choice that keeps §04's promise that no operation produces an
undefined result.

## What is deferred

Recorded in `docs/deferred.md`:

- **`select` over several channels.** It needs a multi-way blocking primitive and a
  fairness policy, and every program in this phase's scope is expressible with one
  channel per thread or a `Mutex`. Phase 7.
- **Async I/O.** §08 previously said Phase 6 delivered "kqueue-backed async I/O"; that
  is amended, because Origin has no I/O to be asynchronous about — `io::println` is the
  entire surface. Async I/O follows the file and socket APIs, in Phase 7.
- **Exposed atomics.** ADR-0014 already made these conditional on the scheduler needing
  them in Origin source. It does not; the scheduler is Go, not Origin.
- **Per-thread panic isolation**, per ADR-0026 above.
- **Preventing a protected value from escaping `with`**, as described under Mutex.

## Diagnostics

| Code | Condition |
|---|---|
| `E0700` | a value crossing a channel is not `Send` |
| `E0701` | a spawned closure captures a value that is not `Send` |
| `E0702` | `JoinHandle[T]` where `T` is not `Send` |

Each names the offending type and the field or capture that makes it non-`Send`, since
"`C` is not `Send`" without "because field `n` is `mut`" is not actionable.

## Worked examples

| Program | Output |
|---|---|
| `let h = thread::spawn(\|\| -> i64 { 40 + 2 }); io::println(h.join().to_str());` | `42` |
| spawn 4 threads each returning their index, join all in order | `0 1 2 3` |
| `let (s, r) = chan::channel[i64](0); thread::spawn(\|\| { s.send(7); }); match r.recv() { ... }` | `7` |
| send 3 values then `close`, receive until `None` | the 3 values, in send order |
| `recv` on a closed, drained channel | `None`, every time |
| `send` after `close` | TRAPS `send on a closed channel`, exit 101 |
| `close` twice | TRAPS `channel already closed`, exit 101 |
| `chan::channel[i64](-1)` | TRAPS `channel capacity is negative`, exit 101 |
| 8 threads each incrementing a `Mutex[Counter]` 1000 times | `8000` — no lost updates |
| `m.with(\|c\| { m.with(\|c2\| { 0 }) })` | TRAPS `mutex re-entered by the same thread` |
| spawning a closure capturing `C { mut n: i64 }` | REJECTED at compile time — `E0701` |
| sending a `Mutex[C]` where `C` has `mut` fields | accepted — `Mutex[T]` is `Send` for `Send` `T` |
| `h.join()` twice on one handle | TRAPS `handle already joined` |
| two threads each waiting on the other's channel | TRAPS `all threads are blocked`, exit 101 |
| a thread panicking while another runs | the whole process exits 101 (§09) |
| `main` returns while a spawned thread still runs | the program waits for it, then exits |
| a compute loop with no calls, another thread ready | the other thread runs — safepoint on the back-edge (§08) |

Every row is a case in the corpus, which is what makes this table normative rather than
aspirational. The two rows that are verdicts rather than runs — the rejected capture and the
accepted `Mutex[C]` — are `tests/conformance/` cases, asserting the diagnostic and its code;
every other row is a `tests/e2e/cases/` case asserting exact stdout, stderr and exit status
on all three engines at every optimization level.

A row's program is written here in the shortest form that shows the point, and in the corpus
in whatever form the language actually has: the receive above is a `match` and not an
`unwrap`, because `Option` has no `unwrap` — opening one is what `match` is for, and adding
a method that traps on `None` is a decision for the prelude and not for this document.

## Determinism and the differential

The three engines must agree on every program in this document's scope, which constrains
what the suite may contain: a program whose output depends on *which* thread wins a race
is not a valid end-to-end case, because the interpreter, the VM and native code schedule
differently. Cases either impose an order (join in sequence, one channel per thread) or
assert an order-independent property (a total, a set, a count).

This is a real restriction on the tests, not on the language. Programs whose output is
scheduling-dependent are legal Origin; they are simply not usable as differential cases.
