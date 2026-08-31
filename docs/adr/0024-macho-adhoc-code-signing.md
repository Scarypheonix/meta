# ADR-0024: Origin signs its own Mach-O executables, ad hoc

**Status:** accepted · **Date:** 2026-08-31 · **Decided by:** implementer (user delegated)

## Context

The first time a Mach-O build produced by this project was ever executed on the target
machine — a 2017 MacBook Air running macOS 12.7.6 — it did not run:

```
$ ./hello
zsh: killed     ./hello
```

`zsh: killed` is SIGKILL from the kernel, not a fault in the program. The binary was
correct: the same instruction stream, byte for byte (ADR-0003), runs correctly as an ELF
in the development container, and the differential suite agrees across all three engines.
What was missing was a **code signature**.

Since macOS 11, AMFI kills any executable that carries no valid signature. This is not
Gatekeeper and not the quarantine bit — removing the quarantine attribute changes
nothing. It is unconditional: every executable macOS runs is signed, and the reason
nobody normally notices is that Apple's linker has ad-hoc signed its own output by
default since Xcode 8. **Origin has no linker (ADR-0017), so its output was never
signed, and could never have run.** This had gone undetected for the whole phase
because the container cannot execute a Mach-O; every prior verification was structural.

Trying to sign it after the fact failed too:

```
$ codesign --sign - ./hello
./hello: internal error in Code Signing subsystem
```

That error is Apple's tooling declining to say what is wrong. `rcodesign` — an
independent, cross-platform implementation of Apple code signing, which *can* be run in
the development container — names it precisely:

```
Error: __LINKEDIT isn't final Mach-O segment
```

Two structural defects, fixed in the commit preceding this ADR, were behind it: there was
no `__LINKEDIT` segment at all (macOS requires one, as the last segment, because that is
where a signature is appended), and the `__DWARF` segment ADR-0023 added claimed
`vmaddr 0`, colliding with `__PAGEZERO`'s own `[0, 4 GiB)` claim — the unmapped-debug-
segment convention of a `.o` file, which is not valid in a linked executable. With both
fixed, `rcodesign` signs the file and **the signed binary runs on the target machine**,
which is what makes the rest of this decision a question of *how* to sign rather than
whether signing is the missing piece.

## Decision

### Origin emits its own ad-hoc signature; it does not shell out to `codesign`

`originc build --target macos` produces a binary that runs. It does not produce one that
the user must then pass through Apple's `codesign`, or through `rcodesign`, or through
anything else.

This follows directly from ADR-0017's reasoning rather than extending it. That ADR
refused a linker because "the compiler emits the bytes of its own executables" is the
point of the project; a compiler whose output cannot be executed without a second,
external, platform-proprietary tool has the same defect in a different place. It would
also break the toolchain's own future: Phase 9 self-compiles, and a stage1 binary that
cannot run without a tool that does not exist on the machine that built it is not a
bootstrap.

The counter-argument — that code signing is cryptographic infrastructure, not code
generation, and that hand-writing it is scope creep — is real but does not survive the
specifics. An **ad-hoc** signature involves no keys, no certificates, and no
cryptography beyond SHA-256, which the host language's standard library already
provides. What it requires is the same thing `internal/x86`, `internal/obj` and
`internal/dwarf` already do: writing a documented byte format exactly. The whole of it
is a few hundred lines.

### The signature is ad hoc, and nothing more

Ad hoc means the CodeDirectory's `adhoc` flag is set and there is no CMS signature: the
binary asserts its own identity by content hash and claims no signing authority. It is
what `codesign --sign -` produces, and it is exactly enough for the kernel to run the
program. Deliberately out of scope, all of it Phase 7 or later and none of it needed to
execute a binary:

- **Real (Developer ID) signing, notarization, and stapling.** These are for
  *distributing* software to other people's Macs. They need an Apple developer account,
  a private key, and a network round trip to Apple's notary service. Origin is not
  distributing binaries to third parties.
- **Entitlements, hardened runtime, library validation.** Nothing the language can
  currently express needs a privileged capability.
- **A resource envelope / `_CodeSignature` bundle.** That is for `.app` bundles; Origin
  emits a single freestanding executable file.

### SHA-256 only, one CodeDirectory

Apple's own tooling emits two CodeDirectories — one hashed with SHA-1, one with SHA-256 —
so a binary runs on macOS versions predating SHA-256 support. Origin's target is macOS 12,
where SHA-256 is what the kernel actually uses; the SHA-1 directory would be emitted only
to be ignored. One directory, `hashType = 2` (SHA-256), is emitted. This is trivially
reversible if a target older than SHA-256 support ever matters, which it does not: the
project's stated target machine is fixed (§ "Hard environment constraints").

The signature superblob carries three blobs, matching what a real ad-hoc signature
contains: the CodeDirectory, an empty requirement set, and an empty CMS wrapper. The two
empty ones cost 20 bytes together and keep the blob shape the one every consumer expects
rather than a minimal variant nothing else produces.

### The layout is computed before any byte is written, like every other address here

The signature covers the file from offset 0 up to where the signature itself begins
(`codeLimit`), hashed in 4 KiB pages. That creates an apparent circularity — the
signature's own size appears in `__LINKEDIT`'s `filesize` and in `LC_CODE_SIGNATURE`,
both of which sit in the header, which is inside the hashed region — but it is only
apparent. The signature's *size* depends on the identifier's length and the number of
page hashes; the number of page hashes depends on `codeLimit`; and `codeLimit` is where
the signature starts, which is fixed by the sizes of everything before it. So the size is
computable up front, the header is written with the final values, and only then is
anything hashed. This is the same discipline `internal/obj.Plan` already imposes on
segment addresses and ADR-0023 on the DWARF line table: **nothing is patched after the
fact**, because a freestanding executable has no relocations to patch through.

### `internal/codesign` owns the format

Process rule 5, and the same shape as `internal/dwarf` and `internal/x86`: the blob
encoding is a documented external format with its own tests, not something
`internal/obj` should grow inline. `internal/obj` decides where the signature sits in
the file; `internal/codesign` decides what its bytes are.

## Consequences

- **Every Mach-O this project produced before this change was unrunnable**, and every
  claim that the Mach-O path was "verified" was a claim about structure only. ADR-0003
  scoped that honestly — "the implementer cannot run a Mach-O here" — and the checklist
  item existed precisely to catch what it caught. It is worth recording that the item
  was not a formality: it found a defect that no amount of in-container verification
  would have.
- **The container can now verify Mach-O signing without a Mac.** `rcodesign` reads and
  reports on signatures in the same way Apple's tooling does, so `internal/codesign`'s
  output is checked against an independent implementation, exactly as `internal/dwarf`'s
  is checked against Go's `debug/dwarf`. This does not make the Mac confirmation
  redundant — only execution proves execution — but it moves most of the loop in-house.
- **`Image` grows an identifier.** A signature names the code it covers; the name used
  is the output file's base name, matching what `codesign` defaults to.
- **Signing is not optional and not a flag.** An unsigned Mach-O cannot run, so there is
  no configuration in which emitting one is useful. ELF is untouched: Linux has no
  equivalent requirement, and the ELF path already runs.
