# ADR-0003: Two object-file writers (Mach-O and ELF) behind one interface

**Status:** accepted · **Date:** 2026-08-28 · **Decided by:** implementer (user delegated)

## Context
The mandate is native x86-64 Mach-O executables on a 2017 MacBook Air, with no LLVM and
no external codegen library. Development, however, happens in a Linux x86-64 container:
Mach-O binaries produced there cannot be executed, `lldb` is unavailable, and the local
`clang` targets ELF. Under a Mach-O-only rule, every end-to-end test, the entire
clang-differential suite, and the Phase 9 three-stage bootstrap become unrunnable in the
environment where the code is actually written.

## Options considered
- **Mach-O only.** Honours the mandate literally; ships codegen that has never executed
  a single program in the environment that produced it, and moves all verification to
  manual runs on the laptop. The Phase 5 exit criteria could not be checked before being
  claimed.
- **ELF only, Mach-O deferred.** Fastest to a verified compiler; abandons the stated
  deliverable.
- **Both, sharing everything above the container format.** Instruction selection,
  register allocation, stack-frame layout, System V AMD64 calling convention and machine
  code emission are written once. Only the object-file writer and the platform's symbol
  conventions differ (leading underscore on Mach-O, section and relocation encodings,
  `__TEXT`/`__DATA` segments vs. `.text`/`.data`).

## Decision
Emit one instruction stream; write it out through an `ObjectWriter` interface with two
implementations, `macho` and `elf`. Mach-O is the shipping target. ELF is the
continuous-verification target and is what `./check` exercises.

## Consequences
- This is **not** a cross-compilation layer as the mandate forbids: one ISA, one ABI,
  one register allocator, one encoder. The divergence is confined to a file format and a
  symbol-naming rule, and is under a hundred lines of interface.
- Every codegen change is verified by execution on every commit, in the container.
- Mach-O output is verified structurally in the container (a hand-written reader in the
  test suite parses back what the writer emitted) and behaviourally by the user on the
  MacBook. Phase 5 is not complete until the user confirms a compiled binary runs and
  `lldb` breaks on a source line — that confirmation is a checklist item in
  `docs/phases/5-complete.md`, not something the implementer can claim.
- Reversing this means deleting `elf` and losing container-side verification.
