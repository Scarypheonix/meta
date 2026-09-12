# Phase 16 — Complete

**Exit criteria:** the user's, and it reframed who the compiler is for: *"most people will be
using the web version of Origin, not the native."* Compare the web build against Python 3 and
Go, then make it faster — without touching syntax or grammar.

That is a different target from every phase before it. Phases 5 through 9 made the *native*
backend good. Nobody visiting a playground runs the native backend.

**Status:** met. `./check` passes in **211s at 1,918 MiB** against budgets of 300s and
3,072 MiB. **43 ADRs, 21 specification documents** — both unchanged, and that is the point:
nothing about the language moved.

## What the comparison found

`fib(30)`, 2,692,537 calls, on the same machine. Origin through the real `web/origin.wasm`
under V8, against Python and Go running natively — which is the comparison a visitor actually
makes, because their alternative is running Python on their own machine:

| | fib(30) | 10M int loop | 1M allocations |
|---|---|---|---|
| Go | 6 ms | 5 ms | 20 ms |
| Origin native `-O2` | 14 ms | 24 ms | 14 ms |
| Python 3.11 | 102 ms | 1,000 ms | 155 ms |
| **Origin web, VM `-O2`** | **1,956 ms** | **6,097 ms** | **689 ms** |
| Origin web, interpreter | 8,004 ms | — | — |

The Python number was checked rather than trusted: CPython 3.11's specialized adaptive
interpreter really does 2.69M calls in ~100 ms, about 38 ns a call. **We were being measured
against a fast interpreter, and losing to it by 19×.**

## The decomposition is the finding

Running the same three programs down all three execution paths says where the cost is:

| | native → **VM** | VM → **wasm** |
|---|---|---|
| fib(30) | **32×** | 4.3× |
| 10M loop | **55×** | 4.5× |
| 1M alloc | **11×** | 4.7× |

**The web build is not slow because it is WebAssembly. It is slow because it interprets
bytecode.** WebAssembly costs a flat ~4.5×; the bytecode VM costs 11–55× on top of the native
backend. That killed the obvious idea before it was started — the native backend cannot help,
it emits x86-64 machine code, and `docs/spec/playground-runtime.md` excludes it from the build
deliberately. The interpreter was the whole target.

## What was built

Four changes, all inside `internal/vm`:

| | |
|---|---|
| the instruction is taken **by pointer** | `bytecode.Instr` is 40 bytes and was copied on every dispatch |
| **`pc` and `code` hoisted into locals** | for as long as a frame runs uninterrupted |
| **`world.spawned`**, an `atomic.Bool` | `safepoint` was taking a mutex on every loop back edge |
| `frame` caches `fn.Code` | two fewer dereferences per instruction |

Result, through the same wasm module:

| | before | after | |
|---|---|---|---|
| fib(30) | 1,956 ms | **1,182 ms** | −40% |
| 10M int loop | 6,097 ms | **3,172 ms** | −48% |
| 1M allocations | 689 ms | **418 ms** | −39% |

Against CPython 3.11 that is 19× → **11×** on fib, 6.1× → **3.1×** on the loop, and 4.4× →
**2.5×** on allocation. The native VM gained too — the loop went 1,337 → 770 ms — so
`originc run --vm` is faster as well.

**No ADR.** Rule 6 asks for one on an irreversible choice, and none of these is: every one is
an implementation detail of a single package, reversible by deleting it, and observable only
as a stopwatch reading. `bytecode.Instr` — the thing that *is* a contract, because
`tests/selfhost` holds stage1 to it instruction for instruction — was not touched.

## The mutex nobody was supposed to be paying for

`safepoint` ran on every loop back edge and called `singleThreaded()`, which took `w.mu` with a
`defer`. Its own doc comment said the opposite of what it did:

> *"The overwhelming majority of programs never spawn at all, and they should not pay for
> this."*

They did pay for it — a lock, a deferred unlock and an unlock, on every back edge of every loop
in every single-threaded program. `internal/interp` has carried an `atomic.Bool` for exactly
this since Phase 10; the VM never got one. **The two engines are supposed to be the same
answer twice, and here one of them had a fix the other did not.** That is the class of bug
Phase 7's ADR-0029 already warned about — the interpreter is not "the engine without types",
and neither is the VM "the interpreter with bytecode".

## The experiment that failed, and why it is recorded

The obvious next move was a **VM-private decoded instruction stream**: 24 of `Instr`'s 40 bytes
are the `diag.Span` only a trap needs, so split spans into a parallel array and walk 16-byte
instructions instead of 40-byte ones. `tests/selfhost` compares the *bytecode program*, not how
the machine decodes it, so this was free of any contract.

**It was 4–8% slower.** fib 1,199 → 1,243 ms, loop 3,134 → 3,382 ms. It was reverted.

The reasoning error is the part worth keeping: **struct size only matters when you copy the
struct.** Change one had already made `in` a pointer, so the loop never copied 40 bytes — it
loaded the two or three fields each opcode uses. Shrinking the struct therefore saved nothing
on loads, while splitting one array into two added a second memory stream and an extra
bounds-checked index on `OpConst` and every arithmetic op, which are the hottest opcodes there
are. **The optimization was already obsolete when it was proposed, by the optimization before
it.**

This is `docs/phases/14-complete.md`'s lesson wearing different clothes. There it was pricing a
fix from the mechanism instead of the requirement; here it was carrying a mental model of the
hot loop that had been true forty minutes earlier. **Re-profile after every change, not after
every batch.**

## What was deliberately not done

**An i64 fast path for arithmetic**, worth about 4%. `intOp` plus `arith.Add` are 7.8% of a
compute program, and the fast path is easy to write. It would also be **a second definition of
integer overflow semantics**, which is exactly what `internal/arith` exists to prevent and what
process rule 5 forbids: *"When two subsystems must agree on a representation, that agreement
lives in one shared module."* Overflow trapping at every optimization level is a listed
language invariant. Four percent does not buy a second copy of it.

## How this was measured, which is most of the work

Nothing here was guessed. Both profiles were taken against the thing being optimized:

- **Go's pprof** for the native VM, through a throwaway `main` inside the module (deleted).
- **V8's `--cpu-prof`** for the wasm build, driven through the real `web/origin.wasm` under
  Node — the same engine Chrome runs.

Both said the same thing: 75% of a compute program is the dispatch loop's own body. That is
what made the four changes the right four, and it is what caught the fifth being a regression
before it shipped rather than after.

The harness lived in `/tmp` and every measurement was best-of-N against a baseline captured on
an unmodified tree first. **Capture the baseline before the first edit** — by the third change
there is no way to reconstruct it.

## Carrying forward

- **The web build is the one most people run.** It had never been profiled. Four hours of
  measurement found a mutex on every back edge and a 40-byte copy on every instruction, both
  years old in project time.
- **Re-profile after every change.** An optimization can be made obsolete by the one before it,
  and the profile is the only thing that says so.
- **The two Go engines drift.** The VM missed a fix `internal/interp` had carried for five
  phases. Whatever is true of one is worth checking against the other.
- **Phase 17's scope is the user's** (rule 7). What is left, with honest confidence:
  **superinstruction fusion** is the one real remaining lever — it removes dispatches and
  push/pop pairs outright rather than rearranging memory, plausibly another 25–40%, and it
  needs a decoded stream (which costs ~4% on its own, so it has to earn that back), a
  pc-remapping pass so jump targets and trap spans still line up, and new VM-internal opcodes.
  Beyond it is a **WebAssembly backend** — reuse `internal/ir`, `internal/opt` and
  `internal/mono`, replace the x86 encoder, and solve SSA to structured control flow with a
  relooper. That is the step change; everything short of it is still a bytecode interpreter.
