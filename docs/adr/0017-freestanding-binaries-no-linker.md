# ADR-0017: `originc build` writes a complete freestanding executable, with no linker and no libc

**Status:** accepted · **Date:** 2026-08-29 · **Decided by:** implementer (user delegated)

## Context
`CLAUDE.md` forbids LLVM, Cranelift, libgccjit and a C backend: machine-code bytes are
emitted directly and object files are written by hand. It does not, on its face, say
anything about the *linker*. Almost every compiler that emits its own object files still
hands them to the system linker, which resolves relocations, lays out segments, and
links a C runtime that provides `_start`, `malloc`, `write` and process exit.

Taking that path would mean the output of `originc build` is a `.o` file, and running an
Origin program requires `ld` (or `clang` as a linker driver) and a system libc on the
machine. It would also mean Phase 9's bootstrap — the compiler compiling itself to a
byte-identical binary — depends on a third-party linker's output being byte-identical,
which is not something this project controls.

The programs Origin 0.1 produces need very little from an operating system: map some
memory for the heap, write bytes to a file descriptor, and exit.

## Options considered
- **Emit relocatable objects and call the system linker.** Standard, well-trodden, and
  every symbol the runtime needs comes free from libc. Costs: the toolchain is no longer
  self-contained, the bootstrap's byte-identity depends on someone else's linker, and
  the project's central claim — built from nothing until it compiles itself — acquires
  an asterisk. It also front-loads relocation machinery (`R_X86_64_*`, Mach-O `X86_64_RELOC_*`)
  that a single-unit compiler never actually needs.
- **Emit relocatable objects and write our own linker.** Self-contained, but it is a
  whole subsystem built to resolve references between translation units when there is
  exactly one translation unit. It solves a problem the compiler does not have.
- **Emit a complete, statically laid out executable directly.** The compiler knows every
  function's address because it assigns them; there is nothing to relocate. It writes
  the file's headers, one text segment, one read-only data segment, one data segment,
  and an entry point. The runtime — heap, output, traps, process exit — is machine code
  the backend emits, calling the kernel directly through `syscall`.

## Decision
`originc build` writes a complete freestanding executable. No linker, no libc, no
`crt0`, no dynamic loader. The runtime is emitted machine code and reaches the operating
system through raw syscalls, per `docs/spec/11-codegen.md`.

A reserved register (`r15`) holds the address of a runtime block in the data section, so
emitted runtime code reaches global state in one instruction and the file needs no
relocations at all.

## Consequences
- **The output runs on a machine with no toolchain.** A Mach-O built here runs on the
  target MacBook with nothing installed. That is the acceptance criterion Phase 5 hands
  to the user, and it is now a property of the file rather than of their machine.
- **Phase 9's byte-identity is ours to guarantee.** Nothing between the compiler's
  output and the file on disk belongs to anyone else.
- **Segment layout is fixed at compile time**, so addresses are known while emitting.
  Jump and call targets are patched by the assembler itself; there is no relocation
  table in either format.
- **Position-independent execution is given up.** The executable is loaded at a fixed
  address, so it cannot be ASLR-relocated. For macOS, `MH_PIE` is not set. This is a
  real loss of a hardening feature and is recorded in `docs/deferred.md`.
- **Every syscall is a per-OS constant.** The two targets differ in syscall numbers and
  in the executable's headers, and nowhere else — the instruction bytes are the same,
  which is exactly what ADR-0003 requires of the two writers.
- **No FFI.** Calling C from Origin is not possible in a binary that does not link one.
  It was already out of scope for 0.1, and this decision makes it a later phase's
  problem rather than a thing that half-works.
- If Origin later needs a library ecosystem, separate compilation, or FFI, this decision
  is where that has to be revisited: relocations and a linker (ours or the system's)
  become unavoidable at the point where a program is more than one compilation unit.
