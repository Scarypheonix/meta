# ADR-0001: Host language is Go

**Status:** accepted · **Date:** 2026-08-28 · **Decided by:** user (project mandate)

## Context
The stage0 compiler must be written in something other than Origin. The target machine
is a 2017 MacBook Air (Broadwell i5, 2 cores / 4 threads, 8 GB). The project mandate
allowed one written argument against Go before the end of Phase 0.

## Options considered
- **Go.** Sub-second incremental compiles on two cores, single static binary, a GC so
  the compiler's own memory management is not a second project, good `testing` and
  fuzzing built in. Weak sum types: ADTs must be encoded (see ADR-0015).
- **Rust.** Excellent ADTs and pattern matching for an AST; cargo builds on a 2-core
  Broadwell with 8 GB routinely exceed the mandated 60-second incremental budget, and
  peak RSS during codegen threatens the 3 GB ceiling.
- **OCaml.** The best language for writing a compiler in; the smallest ecosystem for the
  Mach-O writing, kqueue and FFI work in later phases, and an unfamiliar build story.

## Decision
Use Go. No counter-argument is filed; the decision window closes with Phase 0.

## Consequences
- The 60s incremental and 3 GB budgets are comfortably met.
- The AST needs an explicit encoding for sum types — ADR-0015.
- Go's GC means the compiler's allocation behaviour is not a proxy for Origin's; the
  Origin GC (Phase 3) is written from scratch and cannot borrow anything from the host.
- Phase 9 deletes Go from the build path entirely, so no Go idiom may leak into
  Origin's semantics.
