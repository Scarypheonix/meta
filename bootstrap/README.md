# bootstrap

The last known-good `stage1` binary: the Origin compiler, written in Origin, compiled to
native x86-64 with no linker and no libc (ADR-0017).

`stage1-linux-amd64` is `originc build -O1 --target linux stage1/src` — and it is also what
*it* produces from the same source, byte for byte. That fixed point is what Phase 9 was for.

## What it is for

Process rule 9: **never break the bootstrap.** A compiler that can only be built by the
compiler it replaces is not self-hosting, and a binary nobody can reproduce is not a
bootstrap. Two tests hold both halves:

- `tests/selfhost`'s `TestTheBootstrapBinaryIsWhatTheCompilerBuilds` rebuilds `stage1/src`
  with `originc` and compares the bytes against this file. It fails the moment a change to
  the Go compiler, to `stage1/src`, or to the prelude would produce a different binary —
  which is the signal to regenerate this one deliberately rather than to discover the drift
  later.
- `TestStage1CompilesItself` runs *this file* over `stage1/src` and checks that what comes
  out is this file again.

## Regenerating it

```
go run ./cmd/originc build -O1 --target linux -o bootstrap/stage1-linux-amd64 stage1/src
```

Only when the difference is intended. The commit that changes this binary should say what
about the compiler changed, because the diff itself says nothing a reader can use.

## What it is not

It is not a release artefact and not the macOS build. `originc build --target macos` is what
the target machine runs (ADR-0024: macOS will not execute an unsigned executable, so the
compiler signs its own output); stage1 does not write a Mach-O yet, so the binary that
reproduces itself is the Linux one the container verifies.
