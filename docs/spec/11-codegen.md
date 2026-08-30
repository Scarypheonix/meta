# 11 — Code Generation, Object Files and the Native Runtime

Status: **normative** for the native backend (Phase 5).

This document specifies what `originc build` produces and how the generated program
behaves. It is the contract between the optimizer's SSA output (`internal/ir`), the
object layout the collector already owns (`internal/layout`, §08), and the bytes that
end up in an executable file.

The governing constraint is in `CLAUDE.md` and is not negotiable: **no LLVM, no
Cranelift, no libgccjit, no C backend, and no external assembler or linker.** The
compiler emits machine-code bytes itself and writes a complete, directly executable
object file. Nothing on the target machine is required to run the output — no toolchain,
no dynamic loader, no system library.

## The target

| Property | Value |
|---|---|
| Architecture | x86-64 |
| Baseline ISA | SSE2, `cmov`, `syscall` — everything a Broadwell i5-5350U has, and nothing it lacks |
| Word size | 8 bytes; every Origin value that lives in a register occupies one word |
| Endianness | little |
| Shipping format | Mach-O 64 executable (`MH_EXECUTE`), for macOS Monterey on the target MacBook Air |
| Verification format | ELF64 executable (`ET_EXEC`), static, for the Linux container the compiler is developed in |

The two formats share **one** instruction stream. A byte emitted into the text section
is identical in both files; only the file's headers, segment tables and entry
declaration differ (ADR-0003). This is not cross-compilation: there is one target
architecture, one instruction encoder, and one code generator. There are two ways to
wrap the same bytes so that two operating systems will load them.

Extensions above the baseline (AVX, BMI, FMA) MUST NOT be emitted. The target machine
has some of them; a compiler that emits them cannot be tested by running its own output
on any other machine, and the project's differential testing depends on running it here.

## Registers

The generated code uses the integer registers with their System V AMD64 roles, and the
SSE registers for floats:

| Register | Role |
|---|---|
| `rax` | return value; scratch; `idiv` dividend low half |
| `rdi rsi rdx rcx r8 r9` | integer arguments 1–6, in that order |
| `r10 r11` | scratch, caller-saved |
| `rbx r12 r13 r14 r15` | callee-saved, available to the allocator |
| `rbp` | frame pointer, always maintained (see below) |
| `rsp` | stack pointer |
| `r15` | RESERVED: the runtime block pointer (see "The runtime") |
| `xmm0`–`xmm7` | float arguments and return; scratch |
| `xmm8`–`xmm15` | scratch |

`r15` is reserved and is therefore **not** available to the register allocator, which
leaves `rbx r12 r13 r14` as the callee-saved pool.

Origin calls nothing written in another language, so it is not bound to the platform
ABI. It follows it anyway, in argument order, return register, callee-saved set and
stack alignment, for one reason: `lldb` and `gdb` can then walk a backtrace and read
arguments with no plugin. A debugger that understands the frame is worth more than the
registers a bespoke convention would save.

## Calling convention

- The first six integer or reference arguments are passed in `rdi rsi rdx rcx r8 r9`;
  the first eight float arguments in `xmm0`–`xmm7`. Further arguments are pushed right
  to left, so argument *n* is at `[rbp + 16 + 8*(n-7)]` in the callee.
- The integer or reference result is returned in `rax`; a float result in `xmm0`; `()`
  returns an unspecified `rax` that the caller MUST NOT read.
- `rsp` is 16-byte aligned at the point of every `call`, so `rsp % 16 == 8` on entry to
  a function body (the return address is on the stack).
- Every function maintains a frame pointer, with no omission at any optimization level:

  ```
  push rbp
  mov  rbp, rsp
  sub  rsp, <frame size, rounded up to 16>
  ```

  Omitting it would save one register and one instruction pair and would break
  `lldb`'s backtrace on a machine where that backtrace is one of two acceptance
  criteria the implementer cannot check.
- A callee saves and restores `rbx r12 r13 r14` if it uses them, in its prologue and
  epilogue, below the saved `rbp`.

## Stack frames

A frame is laid out downward from `rbp`:

```
   [rbp + 16 + 8k]   argument 7 and beyond (caller-pushed)
   [rbp + 8]         return address
   [rbp + 0]         saved rbp                      <- rbp
   [rbp - 8 ...]     saved callee-saved registers
   [rbp - ...]       spill slots and reference slots
   [rsp]                                            <- rsp
```

Every slot holding a **reference** is allocated below every slot holding a raw value,
so that a frame's reference area is one contiguous run described by an offset and a
count. §08 requires that a stack map at a safepoint say exactly which slots hold
references; a contiguous run makes that map two integers rather than a bitmap, and makes
the collector's frame scan a loop rather than a decode.

A value that the register allocator spills keeps its slot for the whole function. Slots
are not reused between values of different reference-ness, because a slot whose meaning
changes mid-function cannot be described by one stack map entry.

## Safepoints and stack maps

Per §08 a collection may begin only at a safepoint, and the compiler inserts one at
every function entry, every loop back edge, and every allocation.

A **stack map** describes a frame at one safepoint: the frame size, the offset of the
reference area, and the number of reference slots live there. Stack maps are emitted
into a table in the read-only data section, sorted by the code address of the safepoint,
so the runtime finds the entry for a return address by binary search.

`internal/layout` owns the stack map's representation, because §08 makes it a thing the
collector and the code generator must agree on, and process rule 5 puts such an
agreement in exactly one module. Neither the collector nor the backend may derive the
frame's shape independently.

DEFERRED: stack maps are not emitted yet, and native code therefore allocates without
collecting. A precise map requires knowing which frame slots hold references, and the
SSA IR does not carry that on its own: it is built from bytecode (ADR-0016), which has no
types. ADR-0021 supplies the seed of that answer — `internal/compile` now attaches each
field/payload/tuple-element read's and each call's result kind to the bytecode
instruction itself, and each function's parameter and capture kinds to the `bytecode.Fn`
— since those are exactly the places nothing else downstream can recover a value's kind
from. What is still missing, and still scheduled in `docs/deferred.md`:

- An `internal/backend`-only pass that propagates a kind to every other IR value
  structurally from that seed data (an arithmetic op is always raw, a `phi` is whatever
  its operands already are, and so on) — the same shape as `internal/backend/closures.go`'s
  `resolveClosureCalls`, entirely apart from `internal/ir`, `internal/opt` or the VM.
- The register allocator placing every reference-kind spill slot in one contiguous run,
  as this section's "Stack frames" already specifies, which is what turns a stack map
  into two integers rather than a bitmap.
- The stack-map table itself: one entry per call site (not only a user-visible
  allocation — any call can transitively allocate and trigger a collection, so any call's
  return address must resolve to a map), sorted by code address in read-only data, and
  reachable from the runtime block. A live reference in a register at a call site is
  necessarily in one of the four callee-saved registers, never a caller-saved one
  (ADR-0018's own allocation invariant), which bounds a map's register-root set to four
  bits — the harder case (an arbitrary register, mid-loop, with no call to force
  spilling) belongs to safepoints at a function entry or a loop back edge, which exist
  for scheduler preemption (Phase 6) and are not needed to trigger a collection at all.
- The collector that walks the map: for the immediate caller of `alloc`, and then up the
  `rbp` chain this section's calling convention already always maintains, one frame at a
  time, each resolved by its own return address's map entry.

Emitting a map that claims a precision the compiler cannot supply would be a stub that
lies (process rule 8); ADR-0021 explicitly scopes what has landed to data no engine yet
reads, verified directly rather than by claiming a map that does not exist.

## Object layout in native code

Objects use the layouts of §08 and `internal/layout`, read through the same descriptor
table the collector reads. Field access compiles to a load or store at a constant offset
from the object reference: there is no descriptor lookup at run time, because
monomorphization (ADR-0010, Phase 4) has already given every instantiation a concrete
type by the time the backend sees it.

## The runtime

The generated program contains its runtime. There is no libc, no `crt0`, and no dynamic
loader involvement: the kernel maps the file, jumps to the entry point, and everything
after that is code this compiler emitted.

Reserved register `r15` holds the address of the **runtime block**, a fixed structure in
the data section holding the heap bump pointer, the heap limit, the stack-map table
address, and the output buffer. It is loaded once in the entry stub and never
reassigned, so any emitted runtime routine can reach global state in one instruction
with no relocation.

The runtime routines the backend emits, and what they compile to:

| Routine | Behaviour |
|---|---|
| `_start` | set up `r15`, `mmap` the heap, align the stack, call `main`, then exit `0` |
| `alloc(words, typeid)` | bump-allocate, write the `layout.Header`, return the reference; TRAPS `out of memory` when the heap is exhausted |
| `write(fd, ptr, len)` | the `write` syscall, retrying a short write |
| `int_to_str` | signed 64-bit to decimal, into a heap `String` |
| `trap(msg, span)` | write `<file>:<line>:<col>: <message>` to stderr, exit `101` |
| `exit(status)` | the `exit_group` (Linux) or `exit` (macOS) syscall |

Syscalls are made directly: `syscall` with the number in `rax`, arguments in
`rdi rsi rdx r10 r8 r9`. Two things differ between the operating systems — the syscall
numbers (macOS ORs the BSD class `0x2000000` into each) and the `mmap` flag for an
anonymous mapping — and nothing else does. They are a per-target constant table, not a
second code path.

## Traps

Every trap in §04 — integer overflow, division by zero, remainder by zero, a failed
cast, `panic` — compiles to a conditional jump to a trap stub. The stub loads the
message and the source span for that site and calls the runtime `trap` routine.

A trap MUST produce the same text and the same exit status as the interpreter and the
virtual machine produce for the same program. That is what makes the three engines
comparable, and the end-to-end suite asserts it for every case at every optimization
level.

## Debug information

The executable carries DWARF line-number information mapping each machine-code address
to a source file, line and column. This is what makes

```
$ lldb ./program
(lldb) breakpoint set --file hello.origin --line 4
```

resolve to an address and stop there. Full DWARF type and variable information is
DEFERRED (see `docs/deferred.md`): the line table is what the phase's acceptance
criterion needs, and emitting types by hand before anything reads them would be
speculative.

## Determinism

Two builds of the same source with the same compiler MUST produce byte-identical output
files. No timestamps, no paths outside the source's own recorded name, no addresses
derived from map iteration order. Phase 9's bootstrap compares binaries for equality;
a compiler that cannot reproduce its own output cannot prove it has converged.

## Worked examples

| Program | Native behaviour |
|---|---|
| `fn main() { }` | exits `0`, writes nothing |
| `io::println("hi")` | writes `hi\n` to fd 1, exits `0` |
| `io::println((2 + 3).to_str())` | writes `5\n` — folded to a constant at `-O1`, computed at `-O0`, same output |
| `let x = i64::MAX; let y = x + 1;` | writes `<file>:<line>:<col>: arithmetic overflow` to fd 2, exits `101` |
| `let z = 0; let y = 1 / z;` | writes `... divide by zero` to fd 2, exits `101` |
| a program whose output is 10 MiB | identical bytes to the interpreter's, no truncation, no short write |
| the same source built twice | byte-identical executables |
| `lldb`, break on a source line | stops, and `bt` names the Origin function |
