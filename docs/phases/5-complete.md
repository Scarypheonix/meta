# Phase 5 — Complete

**Exit criteria:** `originc build` produces a native x86-64 executable with no linker and
no libc; the end-to-end suite produces identical output on the interpreter, the virtual
machine and native code at every optimization level; and, on the target machine, a
compiled Mach-O runs and `lldb` breaks on an Origin source line.

**Status:** met. `./check` passes in 45s at 208 MiB, against budgets of 300s and 3072 MiB.
33 packages, 35,796 lines including tests, 27 end-to-end cases × 7 engine/level
combinations, 244 conformance cases, 25 ADRs.

The last criterion was confirmed on the actual 2017 MacBook Air (macOS 12.7.6, x86-64):

```
(lldb) breakpoint set --file hello.origin --line 4
Breakpoint 1: where = hello2`add + 22, address = 0x0000000100000fe6
(lldb) run
Process 28941 stopped
* thread #1, stop reason = breakpoint 1.1
    frame #0: 0x0000000100000fe6 hello2`add at hello.origin:4:18
-> 4       let result = a + b;
(lldb) bt
  * frame #0: 0x0000000100000fe6 hello2`add at hello.origin:4:18
    frame #1: 0x000000010000108a hello2`main at hello.origin:9:15
(lldb) continue
3
```

That binary was compiled by `originc` running on the Mac, from a compiler this container
cross-built, and it ran with no linker, no libc, and no `codesign` — nothing but the
compiler's own output.

## What was built

**`internal/x86`** — a hand-written encoder for the instruction subset the backend emits.
Every instruction's encoded length is independent of its operands' *values*, which is the
property the whole two-pass layout rests on (ADR-0017).

**`internal/obj`** — complete ELF and Mach-O executable writers. Not relocatable objects:
there is no linker, so what these write is what the kernel maps and jumps into. Addresses
are decided by `Plan` before a byte of code exists, because with no relocations an
instruction naming an address must know it while being encoded. One instruction stream
feeds both (ADR-0003), which `TestBothTargetsShareOneInstructionStream` now checks on real
compiler output rather than a hand-written stub: identical code lengths, differing only in
baked-in 64-bit immediates, about 2% of bytes.

**`internal/backend`** — SSA IR to machine code. Linear-scan register allocation
(ADR-0018), the System V calling convention, trapping arithmetic, structural equality and
ordering, closures, `for` loops, and the freestanding runtime (`_start`, `alloc`, `write`,
`int_to_str`, `trap`) emitted as machine code with syscalls made directly.

**`internal/layout`** — gained its second reader. Every struct, tuple, enum-variant and
closure instantiation gets its own exact `Fixed` layout, keyed the way `internal/mono`
keys a function instance (ADR-0019); `layout.Tagged` is retired. The backend reads a
field's offset and pointer-ness from the descriptor at compile time, so no lookup happens
at run time.

**Native heap collection (ADR-0022)** — a single-space, stop-the-world semispace copy.
`rt_collect` walks the `rbp` chain from `alloc`'s caller, resolves each frame through the
stack-map table, and evacuates every register and stack-slot root by Cheney's algorithm.
Getting there needed three things first, each landed and tested on its own: a static kind
for every value the register allocator will ever place (ADR-0021), reference-kind spill
slots grouped into one contiguous run below the raw ones, and the stack-map table itself.

**`internal/dwarf` (ADR-0023)** — a DWARF4 line table and one compile-unit DIE, built
entirely from `.text` addresses, which is what makes it byte-identical across both
emission passes for free. Function names for `bt` come from a plain symbol table, not a
`DW_TAG_subprogram` DIE: the native calling convention already uses standard
`push rbp; mov rbp, rsp` prologues, so frame-pointer unwinding needs nothing else.

**`internal/codesign` (ADR-0024)** — the ad-hoc Mach-O code signature, without which
macOS will not run an executable at all. See below.

## The decision that shaped this phase

ADR-0017: freestanding executables, no linker, no libc. It is the reason `internal/obj`
writes complete executables rather than object files, the reason addresses are planned
before code is emitted, the reason there are two emission passes, and the reason the
runtime is machine code the backend emits rather than a library it calls.

It is also what made ADR-0024 necessary rather than optional. Since macOS 11 the kernel
SIGKILLs any executable carrying no valid code signature. Every normal toolchain satisfies
this without anyone noticing, because Apple's linker ad-hoc signs its output by default —
and Origin has no linker. A compiler whose output needs a second, proprietary tool before
it can execute has ADR-0017's defect in a different place, and Phase 9's bootstrap would
inherit it. So the compiler signs its own output.

## What surprised me

**Every Mach-O this project had ever produced was unrunnable, and nothing in the container
could have revealed it.** The first time one was executed on the target machine it died
with `zsh: killed` — SIGKILL from the kernel, for the missing signature. The binary was
otherwise correct: the same instruction stream ran fine as an ELF and the differential
agreed across all three engines. Apple's own `codesign` refused to sign it after the fact,
reporting only "internal error in Code Signing subsystem"; `rcodesign`, an independent
cross-platform implementation that *does* run in the container, named the real defect in
one line — `__LINKEDIT isn't final Mach-O segment`. Two structural bugs were behind it:
there was no `__LINKEDIT` segment at all, and ADR-0023's own `__DWARF` segment claimed
`vmaddr 0`, colliding with `__PAGEZERO` — the unmapped-segment convention of a `.o` file,
not valid in a linked executable.

The lesson is about the word "verified." ADR-0003's target-machine checklist item was not
a formality, and "verified structurally" meant exactly what it said and nothing more. The
correction was not just to fix the bug but to stop making the claim so cheaply: every
end-to-end case is now built as a Mach-O at every optimization level (81 binaries, where
one program had ever gone down that path) and checked for what makes one loadable, and
`tests/debuginfo` puts `lldb` and `llvm-dwarfdump` on both formats. Across those 81
binaries, 805 of 805 `(file, line)` pairs resolve under `lldb` to an address the compiler's
own line table gives that line.

**The verification boundary was drawn too pessimistically.** ADR-0023 recorded that Mach-O
debug information could only be checked structurally in this container. That was wrong,
and the ADR now says so: `lldb` and `llvm-dwarfdump` read Mach-O cross-platform, and
resolving a breakpoint by file and line is a static DWARF lookup rather than execution. The
entire static half of the phase's last criterion was verifiable here all along. Only the
live process genuinely needed the Mac.

**Two real bugs surfaced only when something finally read the data.** Wiring `Kind` up as
the register allocator's first consumer immediately found that `internal/opt` had been
silently dropping it on its own bytecode round-trip — invisible until something depended
on it. Building the collector's stress test surfaced a `-O2` fixed-point non-convergence
that turned out to be a Phase 4 bug: `CommonSubexpressions` reported "changed" whenever it
found a dominating duplicate, even one with zero uses left to replace, and a trapping
operation's duplicate survives dead-code elimination forever (ADR-0005), so the pipeline
rediscovered the same merged pair every round and never converged.

**A register-allocator bug that could silently collapse two variables into one.** A value
used only as a successor block's φ argument, on the edge leaving its own defining block,
got an interval ending at its own definition — so the allocator could recycle its register
before the back-edge's φ copy read it. Any loop carrying two live values, a counter and an
accumulator say, could merge them. Found by extending the differential, not by reasoning.

## What was deferred

Recorded in `docs/deferred.md` with the phase each is scheduled against. The ones that
matter most for reading this code later: integer arithmetic is 64-bit only in every
engine; `match` compiles to a linear chain of arm tests rather than a decision tree;
`to_str` on a `Float` is unimplemented in native code, since no specification fixes a
rendering yet and a correct one is a shortest-round-trip decimal algorithm; native
collection is single-space and non-generational with no write barrier (ADR-0022's own
scope, satisfying the same language-level guarantees as the generational collector the VM
and interpreter share); and DWARF carries a line table and a symbol table only — no
subprogram, variable or base-type DIEs and no call-frame information, so `frame variable`
and expression evaluation do not work under `lldb`.

A function used both as a direct callee and as an escaping value in the same body loses
its direct-call fast path for every use once it is boxed at all — ADR-0020's own
deliberate simplification, correct and measurably slower only in that one mixed pattern.
