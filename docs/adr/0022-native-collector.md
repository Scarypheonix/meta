# ADR-0022: The native collector is a single-space, stop-the-world, semispace copy

**Status:** accepted · **Date:** 2026-08-30 · **Decided by:** implementer (user delegated)

## Context

ADR-0021 landed every piece a stack map needs — per-value kind data, the register
allocator's contiguous reference-slot placement, and the stack-map table itself — but
left the collector that reads the table entirely unimplemented: `emitAlloc` still only
bump-allocates and traps on `out of memory`. This ADR is what actually reclaims memory in
native code, closing spec/11-codegen.md's "Safepoints and stack maps" DEFERRED note and
spec/08-memory-model.md's collector guarantees for the native backend.

Two things the stack-map table does not yet supply turned out to be necessary while
designing the collector itself, both closed here rather than deferred further:

1. **Recovering a caller's register roots.** A stack-map entry's `RegMask` says which of
   `rbx`/`r12`/`r13`/`r14` hold a live reference *at one call site*, for *that call
   site's own function*. Walking further out — from the function that called `alloc` to
   *its* caller, and so on up the `rbp` chain — needs to know the *value* each of those
   four registers held at the outer call, and by the time a collection runs, the
   physical registers may no longer hold it: an intervening function may have pushed one
   in its own prologue to use as scratch, in which case the outer caller's original
   value is sitting in that intervening function's own save area, not in the CPU
   register at all. Telling "still live in the physical register" from "spilled to a
   specific frame's save slot" needs one more fact per call site: which callee-saved
   registers *this function's own prologue* pushed. `StackMapEntry` gains `SavedMask`
   for exactly this (Decision, below).
2. **A slot the collector visits before it is ever written.** `RefCount` is a
   per-*function* constant (every reference-kind spill slot the function ever uses),
   not narrowed to what is actually live at each individual call site — computing the
   narrower answer would need per-call-site liveness the register allocator does not
   currently track this precisely, and RefCount is deliberately the cheap two-integer
   answer spec/11-codegen.md's "Stack frames" already commits to. A call site that
   executes before some later-defined value's spill slot is ever written (a branch that
   skips the value's definition, or a call textually earlier in the function than the
   value's first store) would have the collector read uninitialized stack memory as a
   reference. `backend.go`'s `function` now zeroes every reference-kind slot in the
   prologue, before anything can spill into one (Decision, below).

## Decision

### Scope: single-space, stop-the-world, non-generational, to start

Phase 3's collector (`internal/gc`, serving the interpreter and the VM) is precise,
generational and moving. ADR-0021's "Consequences" already scoped the *native* collector
down for its first landing: no write barrier, no card table, no generational split — a
collection walks every live root every time, which is sound (spec/08-memory-model.md's
five guarantees do not require generations, only that an unreachable object is collected
by the second full collection at the latest, which a single always-full collection
trivially satisfies) and is what a hand-written freestanding runtime can afford to get
right first. Going generational is Phase 6+ work, unblocked by anything here.

The algorithm is Cheney's, the same one `internal/gc/collect.go`'s `MajorCollect`
already implements for the VM's old generation: two same-size regions ("semispaces"),
bump-allocate in one, and on exhaustion copy every reachable object into the other,
rewriting every reference as it goes, then swap which space is "current". `internal/gc`
is the existing, tested reference for the algorithm's shape; this ADR gives it a
hand-assembled implementation with no descriptor lookups beyond the same per-`TypeID`
table `equal.go` already emits (`emitTypeTable`, `e.typeTableAddr`) — Shape, `ObjKind`,
and a `Fixed` shape's `Kinds` address, exactly what `scanObject` needs to know which
payload words are references.

`emitStart` now `mmap`s two `heapSize` regions instead of one. The runtime block grows
by one field, `rtOtherStartOff`, naming the address of the currently-unused space (its
end is always `+ heapSize`, a compile-time constant, so no separate field for it).
`rtBumpOff`/`rtEndOff` describe the current space exactly as before. Total mapped memory
doubles to 128 MiB — still trivial against the 8 GiB target machine — rather than
halving `heapSize`'s existing meaning.

`emitAlloc`'s failure path changes from "trap" to "collect, then retry the same
allocation, and trap only if it *still* does not fit" — the collector runs, `rtBumpOff`/
`rtEndOff`/`rtOtherStartOff` are updated to reflect the flip, and the bump-and-check
sequence repeats once. A program whose live set alone exceeds one semispace still traps
with `out of memory`, honestly, exactly as spec/08-memory-model.md's fifth guarantee
requires.

### `StackMapEntry.SavedMask`

Added alongside `RegMask`, in the same four-bit, same-order convention (bit 0 = `rbx`,
bit 1 = `r12`, bit 2 = `r13`, bit 3 = `r14`): which of the four callee-saved registers
*this call site's own function* pushed in its prologue (`e.saved`, computed once per
function by `regalloc.go`'s `allocate` and already used to emit the actual `push`/`pop`
sequence in `backend.go`'s `function`). It costs nothing in the table's size — the entry
already had seven padding bytes to keep `ReturnAddr` 8-byte aligned; one of them is
`SavedMask` now, six remain.

`RegMask` bit *i* set implies `SavedMask` bit *i* set: a register can only be a live
root at a call site if the function's own register allocator assigned it to some
interval at all, which is exactly what causes `SavedMask` to have that bit. This
invariant is checked directly (`internal/backend/stackmap_test.go`) against every entry
a real program's build produces, not just asserted.

**Why this is the fact the collector needs**, walking outward from the function that
called `alloc` (call it frame 0, whose live register values are still literally the
current CPU registers — `alloc` touches none of `rbx`/`r12`/`r13`/`r14`) to frame 1 (frame
0's caller), frame 2, and so on: at each step, register *i*'s value *as frame K+1 had
it* is recoverable in one of exactly two ways, and `SavedMask` at frame K says which:

- **`SavedMask` bit *i* is set at frame K**: frame K's own prologue pushed register *i*,
  at `frame K's rbp - 8 * (1 + rank)`, where *rank* is how many lower-indexed bits of
  `SavedMask` are also set (the same fixed push order `calleeSaved` and `e.saved` always
  use). That slot has held frame K+1's original value, untouched, for the whole of frame
  K's execution — it is read from there.
- **`SavedMask` bit *i* is clear at frame K**: frame K's own register allocator never
  touched register *i* at all, so its value has passed through frame K completely
  unchanged — physically, whatever is currently in the CPU register (or, further out,
  whatever an even-earlier frame's own save slot already redirected it to) is frame K+1's
  value too.

The two cases are also exactly where an evacuated (possibly relocated) root gets written
back so that the program sees the update when it actually resumes: a value read from a
frame's own save slot is written back to that same slot (the frame's own epilogue will
`pop` it from there); a value still tracked as "the physical register" is written to the
physical register directly. `SavedMask` bit *i* clear at frame K also implies `RegMask`
bit *i* is clear at every one of frame K's own call sites (frame K cannot claim a root in
a register its own allocator never assigned), so the two cases never conflict: a register
whose slot is being read for frame K+1's sake was never simultaneously a live root frame
K itself needed evacuated at that exact step.

### Zeroing reference-kind spill slots at function entry

`backend.go`'s `function` now emits `mov qword [slot], 0` for every one of a function's
`refSlots` (`e.regs.refSlots`), immediately after `sub rsp, frame` and before the first
block's code. `layout.Nil` (zero) is what `evacuate` treats as "not a pointer, nothing to
do" — the same sentinel `internal/gc`'s own `evacuate` already special-cases for `Ref ==
Nil` — so a slot the collector visits before its owning value's first store is a safe
no-op rather than a wild pointer chase through whatever an earlier stack frame's bytes
happened to leave there. Origin code itself never observes a zero reference (ADR-0007:
no null); this zero is purely the collector's own internal bookkeeping, exactly as
`layout.Nil`'s own doc comment already describes its role.

### The collector's root walk

`internal/backend/collect.go`'s `emitCollect` is called by `emitAlloc` (a plain `call`,
with `rdi`/`rsi` — the pending allocation's words/typeID — pushed across it) once bump
would exceed end. It is not itself a leaf routine: it needs a real prologue (`push rbp;
mov rbp, rsp`) so that `[rbp+0]`, read once at entry, is exactly the value that was in
`rbp` on entry — frame 0's own `rbp`, since neither `alloc` nor anything before it in
`collect`'s own prologue has touched `rbp`. `collect` never allocates `rbx`/`r12`/`r13`/
`r14` for its own scratch (everything it needs beyond a handful of caller-saved
registers lives in its own frame slots), because it must read and, at the very end,
write those four physical registers directly as frame 0's own (possibly evacuated) root
values — the one case above where the write-back destination is "the physical register",
which only ever applies to frame 0's own roots (every other frame's resumption reads its
registers via its callers' own `pop`, never by re-reading the live CPU register mid-
collection).

The walk, starting from frame 0's `rbp` (the live `rbp` register) and its return address
into `alloc` (read once, before `collect`'s own prologue moves `rsp`):

1. `LookupStackMap` the current return address.
2. For each of the four registers whose `RegMask` bit is set, evacuate its current
   tracked value and write the result back to wherever it currently lives (the physical
   register, or a specific earlier frame's save slot — see "Why this is the fact the
   collector needs", above).
3. For each of `RefCount` consecutive words starting at `rbp - RefOffset`, evacuate in
   place.
4. For each of the four registers whose `SavedMask` bit is set, retarget that register's
   tracked value and its write-back location to this frame's own save slot (`rbp - 8 *
   (1 + rank)`), reading the value from there.
5. Read `[rbp+0]` (the caller's `rbp`) and `[rbp+8]` (the return address into it). If the
   caller's `rbp` is zero — `emitStart` now clears `rbp` before its own `call main`, so
   this is exactly "we have finished processing `main`'s own frame" — stop. Otherwise
   move to that frame and repeat from step 1.

### Runtime-internal frames get a synthetic entry, not a trap

A frame the walk reaches can have no real stack-map entry: `rt_int_to_str` allocates
internally (to build the `String` it returns) but, like every routine `runtime.go` hand-
writes, was deliberately never wired into `recordCall` (ADR-0021's own documented scope
— it holds no Origin-level reference of its own to report). When such a frame is frame 0
itself (an allocation triggers a collection from inside `rt_int_to_str`, say) or is
reached partway up the walk, `rt_lookup_stack_map` reports "not found", and the walk
substitutes a synthetic entry (`emitGCRuntimeFrameEntry`, built once into read-only data)
rather than treating the miss as the compiler bug it would be at a genuine user-code call
site: `RegMask` 0 (correct — the routine holds no reference of its own), `SavedMask`
`0b1111` (every one of these routines' prologues pushes all four callee-saved registers
unconditionally, unlike a compiled Origin function's own prologue, which pushes only
what its register allocator actually assigned), `RefCount` 0 (no Origin-level spill-slot
convention applies to a hand-written routine's own locals). This lets the walk correctly
recover *that frame's caller's* original register values from its known, fixed save-slot
layout and continue outward, exactly as if a real entry had said the same thing.

A frame 0 that turns out to be one of these routines is also why the walk's register
write-back can never simply overwrite the physical registers with whatever the walk's
`loc` tracking says once it finishes: `rt_int_to_str` keeps its own local state (a
buffer-walking pointer, say) in `r12` across its own call to `alloc`, with `RegMask` 0
at that call correctly saying "not a root" — but its `SavedMask` bit for `r12` is still
1 (it did push it), so the walk still transitions `r12`'s tracked value once a caller
frame exists. Nothing may write that transitioned value back into the physical `r12`
when the walk finishes: `r12` there is `rt_int_to_str`'s own live local, and only its own
ordinary epilogue `pop r12` (unrelated to this collector) is what correctly restores the
caller's value later, when `rt_int_to_str` itself returns. Each register's own write-back
therefore happens exactly once, inline, at the exact frame whose `RegMask` claims it as a
root (step 2 below) — never as a separate pass over the four registers once the walk
ends.

`evacuate` is Cheney's: `Nil` and an already-to-space reference return unchanged; a
`Header.Forwarded()` object returns its forwarding address; otherwise the object is
copied word-for-word into the current bump position of the destination space, a
forwarding header is written over its old location, and the new address is both the
result and what gets scanned later. The scan itself walks the destination space from
its own start to its own (growing) bump position, reading each copied object's `Shape`/
`Kinds` from `e.typeTableAddr` — the same table `equal.go` built — and evacuating every
`WordRef` field (`ByteArray` objects, i.e. `String`, hold no references and are
skipped, exactly as `internal/gc`'s own `scanObject` does).

## Consequences

- **The native and VM/interpreter collectors are now genuinely different
  implementations of the same guarantees, not the same collector.** This is a deliberate,
  documented divergence (ADR-0021's "Consequences" already flagged it as the plan): the
  five guarantees in spec/08-memory-model.md — reachability, bounded collection latency,
  timing-only observability, no finalizers, TRAP over corruption — are satisfied by a
  single-space stop-the-world collector exactly as much as by a generational one; nothing
  about "generational" is part of the *language's* contract, only of Phase 3's original
  implementation choice for two engines that do not have to hand-assemble every
  instruction.
- **A future write barrier and generational split are still out of scope**, and still
  will not need to revisit this ADR's shape when they land: going generational adds a
  nursery and a card table on top of the same semispace-copy machinery this ADR
  implements for the (then-old) generation, the same relationship `internal/gc` already
  has between `MinorCollect` and `MajorCollect`.
- **`SavedMask` and the zeroed spill slots are both inert until the collector reads
  them** — landed and verified directly first (`internal/backend/stackmap_test.go`'s
  subset check; the zeroing is a straight-line, always-executed sequence with no
  conditional to get wrong) so that the walk described above has no unverified
  assumption underneath it once it is wired up.
- **Collection is stop-the-world and single-threaded**, which Phase 5 has no scheduler to
  make otherwise: there is exactly one green thread's worth of stack to walk (`main`'s
  own call chain), and Phase 6's M:N scheduler is what will eventually need every
  thread's stack root-walked at a synchronized safepoint — a Phase 6 problem, not this
  one.
- **A program whose live set exceeds one semispace still traps honestly.** Retrying the
  allocation once after a collection and trapping only if it still does not fit is the
  same "TRAP over corruption" contract `emitAlloc` already had; a real collection now
  runs first, so the trap means what it says rather than firing on merely a full nursery.
- **Verified against real, executed collections, not just inert data.** Landed as
  `tests/e2e/cases/gc_reclaims_discarded_allocations` (spec/10-examples.md #13: ~120 MB
  of allocation into a single loop-carried live struct, forcing several real collections
  in one run) and `gc_survives_across_a_call` (a live struct held in `main`'s own frame
  while a called function allocates heavily, exercising a genuine cross-frame register
  recovery rather than only the frame-0 case) — both pass at every optimization level, on
  every engine, through the ordinary differential suite.
- **Two real bugs surfaced only once the walk actually ran**, both now regression-covered
  by the two cases above: `gcTransitionLocs` freely clobbers `rcx`/`rdx` computing a
  frame's own save-slot addresses, which corrupted the *next* frame's rbp/return-address
  the moment they were computed into those same registers just before calling it — fixed
  by stashing them in dedicated frame slots (`gcPendingRbpOff`/`gcPendingRetOff`) across
  the call instead of leaving them live in registers a callee is free to use. Separately,
  an early draft wrote every tracked register's *final* transitioned location back into
  the physical register once the whole walk finished, on the reasoning that a register a
  frame never claimed as its own root should be left alone (`loc == 0`, still physical) —
  but a register a frame *did* save for unrelated reasons (a runtime routine's own local,
  the case above) has a non-zero `loc` by the time the walk ends, and writing that back
  clobbered live, non-reference frame-0 state. Each register's write-back happens exactly
  once now, inline, at the specific frame whose `RegMask` actually claims it.
- **Found, and deliberately did not chase, an unrelated `-O2` bug.** Building the first
  stress case above with two struct fields read from the loop variable two different ways
  (`Pair { a: i, b: i + 1 }`) hit `internal/opt`'s "pipeline did not reach a fixed point"
  panic — confirmed to reproduce with no allocation or struct at all, so a pre-existing
  optimizer bug this ADR's work exposed rather than caused. Recorded in `docs/deferred.md`
  rather than fixed here, and the spec example instead uses `Pair { a: i, b: i }`.
