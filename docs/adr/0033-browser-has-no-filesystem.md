# ADR-0033: The browser is a host whose every file operation fails, and the code that could succeed is not linked

**Status:** accepted · **Date:** 2026-09-08 · **Decided by:** implementer (user delegated)

## Context

Phase 10's brief asked for an explicit resolution of every OS-dependent runtime feature in
the browser build, and named five: stdout/stderr, file I/O, green threads, FFI into libc,
and panics. Reading the code first changed the shape of the problem, and the finding is
worth recording because it is most of this ADR's context:

- **stdout and stderr were never file descriptors** in the interpreter or the VM. Both
  engines take `io.Writer` parameters and always have — `driver.RunAt`'s signature ends in
  `stdout, stderr io.Writer`. Redirecting them into a page is not a port; it is passing a
  different writer.
- **FFI does not exist in Origin.** ADR-0017's consequences say so outright: *"No FFI.
  Calling C from Origin is not possible in a binary that does not link one."* There is no
  syntax, no runtime, no corpus case. Nothing to disable.
- **kqueue-backed async I/O was never built.** `docs/spec/08-memory-model.md` scoped it to
  Phase 6 and `docs/spec/12-concurrency.md` amends that scope away — *"Origin has no I/O to
  be asynchronous about"* — with the item still sitting in `docs/deferred.md`. Nothing to
  port.
- **Green threads are goroutines** in both engines, not the native runtime's M:N scheduler,
  and Go's js/wasm port runs goroutines. Nothing to replace. (ADR-0036 covers the real
  browser-specific consequence, which is about the event loop and not about scheduling.)
- **Panics, traps, `process::exit` and allocation** touch no system call on these two
  engines. `process::exit` is a Go panic carrying a status, recovered in `Run()`; the
  VM's collector is `internal/gc`, which contains no `os` or `syscall` reference.

That leaves exactly one: **`std::fs`**. `internal/interp/files.go` and
`internal/vm/files.go` call `os.ReadFile`, `os.WriteFile` and `os.Open`, and they are the
only place in either engine's path that names the host. Four operations, one file per
engine.

So the decision is not "how do we port the runtime". It is "what does a file operation do
on a host that has no files", plus a build-level question about whether the code that
performs them should be present at all.

## Options considered

1. **Compile the operations out so a program using them fails to compile.** `fs::read_file`
   becomes an unknown name in the browser. Cheap, and wrong: the playground would reject
   programs that are valid Origin, which makes it a different language wearing the same
   name. §15 is part of the language whether or not a given host can satisfy it.
2. **Trap on the first file operation.** Loud and honest about the host, but a trap is
   `panic`'s vocabulary — it means the program did something the language forbids
   (ADR-0005, ADR-0026). Reading a file that is not there is not a defect; §15 already has
   a way to say it failed, and that way is a value.
3. **Return the existing failure status, and add a fourth `IoError` case for "unsupported".**
   Precise, and it changes the language to describe a host — a new case every program's
   `match` would have to consider, on every platform, because of one platform.
4. **Return the existing failure status, mapping to the `Other` case §15 already has.**
   No language change. A program compiles, runs, and observes a failure it already had to
   handle.

Orthogonally, and independently of which of those is chosen:

**A.** Let the browser build carry the `os`-backed implementation and simply never succeed,
or **B.** select a `js`-tagged implementation so the `os` file calls are not linked at all.

## Decision

**Option 4, plus B.**

`fs::read_file` and `fs::write_file` return `IOOther`; `fs::file_exists` returns `false`;
`fs::taken_text` returns `""` and is unreachable on a successful read because there are no
successful reads. Through the prelude a program sees `Err(IoError::Other)` — a value §15
already defines, that every program handling file errors already handles.

`IoError` does not grow a fourth case. ADR-0030 chose three because they are the three a
program acts on differently, and "this host has no filesystem" is not a fourth thing to act
on — there is no recovery specific to it. It is `Other`, which is what `Other` is for.

Nothing here is a stub that lies (process rule 8). `file_exists` answering `false` is the
true answer: no file exists. `read_file` answering `IOOther` says the read did not happen,
which it did not. The distinction that matters is between *reporting a real failure* and
*fabricating a plausible success*, and every operation here does the first.

**B** is the half that is about security rather than semantics. The browser build selects
`files_js.go` behind a `//go:build js` constraint, and the existing implementations take
`//go:build !js`. Go's `os` file calls are therefore absent from the WebAssembly module.
The phase's "no filesystem access" constraint becomes a property of what was linked rather
than a promise about which branch runs — and Go's js/wasm port *does* ship an `fs` shim
that a Node host backs with the real filesystem, so this is not a theoretical distinction.
Under Node, option A would genuinely read files.

## Consequences

- **The language is unchanged.** No spec document describing Origin is edited by this
  phase. `docs/spec/playground-runtime.md` describes a host, and this ADR is the reason it
  is able to stay that short.
- **Four end-to-end cases diverge and are named.** `file_read_write_round_trip`,
  `file_large_and_threaded`, `word_frequency` and `option_and_result_methods` use
  `std::fs`. The WASM differential excludes exactly those four, by name, with the reason in
  the exclusion — not by a pattern that could silently grow to cover a case that started
  failing for an unrelated reason. The other 98 must match byte for byte.
- **The two `files_js.go` implementations are not covered by the native suite**, because
  the native suite does not build them. They are covered by the differential, which runs
  the browser build for real — and they are eleven lines apiece with no branches, which is
  the right amount of code to have behind a build tag.
- **A future host with storage has a place to go.** Nothing here forecloses the browser
  eventually backing `std::fs` with something real — the Origin Private File System, or an
  in-memory overlay the page seeds. That would be a new implementation of the same four
  operations, returning the same status vocabulary, and no language change either.
- **`docs/spec/15-files.md` now has a host on which its error path is the only path**,
  which is a better test of that path than any of the three had.

## Reversing it

Backing the operations with real storage is additive: replace `files_js.go` and the
statuses start varying. Reversing the *build tag* — letting the `os` implementation into
the browser build — is the part that should not happen quietly, because it is the
difference between a module that cannot reach a filesystem and one that is trusted not to.
