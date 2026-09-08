# ADR-0032: The playground runs the virtual machine, and ships the interpreter beside it

**Status:** accepted · **Date:** 2026-09-08 · **Decided by:** implementer (user delegated)

## Context

Phase 10 puts Origin in a browser tab. Three engines exist and one of them is immediately
out: `internal/backend` emits x86-64 machine code for a host kernel (ADR-0017), which a
WebAssembly sandbox cannot execute and has no reason to want. That leaves the tree-walking
interpreter (Phase 1) and the bytecode virtual machine (Phase 3).

The two are not interchangeable in cost. The VM needs the bytecode compiler, the optimizer
and `internal/gc`; the interpreter walks the checked AST and needs none of them. Against
that, the VM is the faster engine and the more heavily exercised one — every `optsnap`
case, every optimization level of the end-to-end corpus, and Phase 9's own differentials
run through it.

A browser adds a constraint the project has not had before: **the binary is a download**.
On Linux nobody counts the bytes of `originc`. Here every visitor pays for them before the
first program runs, so "which engine" is partly a question about size, and it deserved a
measurement rather than an assumption.

Measured, `GOOS=js GOARCH=wasm`, `-ldflags="-s -w"`, one trivial program compiled and run:

| Build | Raw | gzip -9 |
|---|---|---|
| interpreter only | 4,982,757 | 1,327,646 |
| VM only | 5,626,805 | 1,469,901 |
| both | 5,972,696 | 1,554,879 |

The shape of that table is the decision. The compiler front end — lex, parse, resolve,
check, mono, and the prelude it all runs over — dominates every column; it is present
whichever engine runs, because a program has to be compiled before anything can interpret
or execute it. Against a 1.33 MB floor that neither engine can avoid, the VM's extra
machinery costs 142 KB gzipped, and adding the *second* engine on top of the first costs
**85 KB gzipped — 5.8%**.

## Options considered

1. **Interpreter only.** The smallest build, and the engine with the fewest moving parts
   between source and behaviour. Slower on anything with a loop in it, and the engine the
   project's own optimization work does not touch, so a visitor would be running the one
   engine whose performance nobody has been improving.
2. **VM only.** The faster engine and the better-tested one. 142 KB gzipped more than the
   interpreter, which against a 1.33 MB floor is not a number worth optimizing.
3. **Both, VM by default, the interpreter selectable.** 85 KB gzipped over option 2.

## Decision

**Option 3.** The virtual machine is the default engine; the interpreter is in the same
module and the boundary takes the engine as a parameter.

The VM is default because it is faster and because it is what the project's own suite
leans on hardest — a visitor should meet the engine that has been under the most scrutiny,
not the one that is merely simplest.

The interpreter ships beside it for a reason that is about testing before it is about
features. §6 of `docs/spec/playground-runtime.md` requires the browser build to agree with
native output for the whole end-to-end corpus, and the corpus is run natively on *both*
engines. A browser build with only one engine can only be held to half of that. With both
present, one `run(...)` boundary and one differential cover the same ground the native
suite covers, and the third artefact the project has always relied on — two engines
agreeing with each other — exists in the browser too.

Whether the engine choice is surfaced in the visible UI is a separate and reversible
question, and this ADR does not settle it. What it settles is that the capability is in
the build, because that is the part that cannot be added later without another download.

## Consequences

- **The download is ~1.5 MB gzipped**, dominated by the compiler front end rather than by
  either engine. Any future work on load time should go after the front end and the
  prelude, or after the Go runtime's own contribution — not after the choice made here,
  which is worth 85 KB.
- **`internal/gc` is in the browser build**, because the VM uses it. It is pure Go with no
  `os` or `syscall` reference of any kind, which is why this costs nothing beyond bytes.
- **The optimizer runs in the browser**, so `-O0`, `-O1` and `-O2` are all reachable and
  all three must produce identical output — the same pass-verification harness Phase 4
  established, now with a fourth engine under it.
- **The native backend's absence is structural.** Nothing in `cmd/originwasm` imports
  `internal/backend`, `internal/obj` or `internal/x86`, so a change that made the browser
  build depend on them would fail to build rather than quietly grow the download.
- **A second engine is a second thing to keep honest.** If the interpreter is never
  surfaced and never diverges, it is 85 KB of insurance; if it diverges, the differential
  says so, which is the point.

## Reversing it

Dropping to one engine is a one-line change to the boundary and a rebuild. Nothing in the
page's contract depends on there being two, and no Origin program can observe which engine
ran it — that is what §6 requires. The reason to reverse would be a load-time budget that
85 KB actually threatens, which is not the situation the table above describes.
