# ADR-0030: A file is read and written whole, with no handle

**Status:** accepted · **Date:** 2026-09-02 · **Decided by:** implementer (user delegated)

## Context

Phase 9's exit criterion is Origin compiling itself, and a compiler reads a source file and
writes an object file. Phase 7 had given the language collections, hashing and strings, and
`docs/deferred.md` did not so much as mention file I/O — process rule 4's own failure mode,
an item silently dropped rather than recorded.

Three earlier decisions constrain what the interface can be:

- **ADR-0006** — errors are values. `Result` + `?`, no exceptions, no unwinding. A file
  operation that fails returns a value saying so.
- **ADR-0008** — the collector owns object lifetimes, and there are no destructors. Nothing
  in the language runs at a deterministic point when a value becomes unreachable.
- **ADR-0025 / spec/12-concurrency.md** — a compiler-provided operation returns a primitive
  or a `String`, never a prelude type, because the native backend has no way to construct
  one. `recv` splits into `chan::await_value` and `chan::taken_value` for exactly this.

## Options considered

1. **A `File` type with `open`, `read`, `write` and `close`.** What most languages have.
   Streaming, seekable, and the shape a real standard library eventually wants.
2. **A `File` type whose descriptor is closed by the collector.** Removes the `close` call
   by making it the collector's job.
3. **Whole files only: `read_to_string`, `write_string`, `file_exists`.** No handle exists,
   so there is nothing to close and nothing to leak.

## Decision

**Option 3.**

A handle would be a resource Origin has no way to release automatically (ADR-0008) and no
syntax to release by hand at the right moment — no destructors, no `defer`, no `with`. So
`close` would be a call a program could forget, and forgetting it leaks a descriptor: a
failure that appears far from its cause, under load, as a different error entirely. That is
the exact class of quiet failure this language's other decisions exist to remove, and
adding it for an interface a compiler does not need would be a poor trade.

Option 2 is worse rather than better: it makes the descriptor's lifetime depend on when a
collection happens, so a program that opens files in a loop either works or exhausts the
descriptor table depending on the heap size. "Sometimes, later" is not a schedule.

The compiler provides four operations, and the prelude is the interface:

| Compiler-provided | Prelude |
|---|---|
| `fs::read_file(path) -> i64`, `fs::taken_text() -> String` | `read_to_string(path) -> Result[String, IoError]` |
| `fs::write_file(path, contents) -> i64` | `write_string(path, contents) -> Result[(), IoError]` |
| `fs::file_exists(path) -> bool` | `file_exists(path) -> bool` |

The split of a read into two calls is ADR-0025's, unchanged: the operation returns a
status, and the bytes wait in the running thread's own TCB slot — the one
`chan::taken_value` uses, which the collector already scans — until the prelude takes them.
Two threads reading two files at once therefore cannot take each other's.

`IoError` has three cases (`NotFound`, `PermissionDenied`, `Other`) because those are the
three a program acts on differently. There is no errno: a number the system happened to
pick is not something a program should branch on, and the three engines would have to agree
on the numbering to boot.

## Consequences

- A file larger than a `String` can hold — `layout.MaxStringBytes`, the object header's own
  24-bit size field — is `Err(Other)` on every engine. That limit is 128 MiB, which is
  larger than any source file and smaller than the point at which reading a whole file at
  once stops being reasonable, so the interface's shape and the header's shape agree about
  where the line is.
- A directory is `Err(Other)` and not a trap, which took work: `open` on a directory
  succeeds on Linux, and seeking to its end answers `i64::MAX`. The size check that refuses
  a file too large for a `String` is what catches it before it becomes an allocation.
- UTF-8 validation is in the **prelude**, in Origin, over `byte_at` — not three times over
  in three runtimes. `read_to_string` is where "a `String` is valid UTF-8" (§14) is made
  true of a file's bytes rather than assumed, and one implementation is how three engines
  are made to agree rather than checked to.
- The native runtime gets a file's size from `lseek` to the end and back, not `fstat`,
  whose result struct is one of the few things Linux and macOS genuinely disagree about.
  Two extra system calls buy one code path.
- Streaming, directories, metadata, rename, delete and a `Path` type are all still missing.
  Each is additive — a new operation beside the four, with the same status-to-`IoError`
  shape — and each is recorded in `docs/deferred.md` rather than designed in advance.

## Reversing it

Adding a `File` type later is additive and does not invalidate this: `read_to_string` stays
what it is, written over the handle interface instead of beside it. What would have to
change first is the language, not the library — a way to release a resource at a point the
program names.
