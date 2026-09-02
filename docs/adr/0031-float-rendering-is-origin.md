# ADR-0031: The shortest decimal for a float is written in Origin

**Status:** accepted · **Date:** 2026-09-02 · **Decided by:** implementer (user delegated)

## Context

`to_str` on a float was the last `unimplemented:` in the project. The interpreter and the
virtual machine borrowed Go's `strconv.FormatFloat(f, 'g', -1, 64)`; native code had
nothing at all, and `docs/deferred.md` had carried the item since Phase 5 with the reason
written out: "a real one is a full shortest-round-trip decimal algorithm with no spec to
build it against yet — Grisu/Ryu/Dragon4-class work."

Two things make this harder than the rest of the runtime:

- **It is arithmetic, not translation.** Every other native routine does what the
  interpreter does, in registers. This one has to compute the shortest decimal string that
  reads back as the same float, which is a fact about numbers far wider than sixty-four
  bits. Getting it wrong is not a crash; it is a program that prints a plausible number.
- **There is no linker and no libc** (ADR-0017). Nothing can be borrowed. Whatever the
  native backend does, it does in bytes this project emits.

The three engines must agree byte for byte at every optimization level (process rule 3), so
whatever the answer is, it has to be one answer.

## Options considered

1. **Hand-write Ryu or Dragon4 in x86-64 machine code.** The obvious reading of "no LLVM,
   no C backend". Ryu needs 128-bit multiplies against a generated table of ~1200 constants;
   Dragon4 needs multi-word integers, which in hand-encoded assembly means loops over word
   arrays with carry handling — several hundred lines of machine code whose only test is
   the output of the whole program.
2. **Write it in Go, once, in a shared module, and have native code call it.** Impossible:
   there is no Go at run time in a native executable. It would mean two implementations —
   Go for the two hosted engines, machine code for the third — which is option 1 plus a
   second copy to disagree with.
3. **Write it in Origin, in the prelude, over a compiler-provided reinterpretation of a
   float's bits.** The compiler provides `float::bits` and `float::from_bits`, which
   compute nothing; everything above them — decomposing the significand, the exact
   arithmetic, the layout — is Origin source that all three engines run.

## Decision

**Option 3.** `Show for f64` and `Show for f32` are Origin source in
`internal/prelude/prelude.origin`, and the floats are removed from `internal/check`'s
builtin-impl table. The compiler's contribution is two operations that reinterpret
sixty-four bits, because a float's encoding is the one thing Origin cannot otherwise see.

The algorithm is Steele & White's free-format conversion in Burger & Dybvig's form, over
base-10⁹ bignums built on the prelude's own `List[i64]`. `spec/16-floats.md` specifies the
result — the shortest decimal that reads back, both tie rules, and the layout — so an
implementation is tested against the answer rather than against an algorithm.

## Consequences

- **One implementation, not three.** This is the same trade ADR-0028 made for collections
  and Phase 7 made for strings, and it buys the most here: the alternative was the hardest
  code in the project written in the least testable language available.
- **The prelude grew by about three hundred lines of Origin**, which is the point twice
  over: Phase 9 has to compile this compiler, and the prelude is where Origin proves it can
  carry real code. Bignum arithmetic over `List[i64]` is the most demanding Origin written
  so far, and it found nothing — the language held.
- **It is slower than a native routine would be**, and the difference is visible: about
  0.16 ms per float in native code, 2 ms on the virtual machine and 20 ms in the
  tree-walking interpreter, against roughly 100 ns for Go's own Ryu. Nothing in the project
  prints floats in a loop, and the honest trade is a correct conversion that is slow in the
  interpreter over a fast one nobody can check. If it ever matters, the fix is a Grisu fast
  path *in Origin*, with this as the fallback — not a second implementation.
- **`tests/floats` is the oracle**, and it had to be one: the first version of the tie rule
  rounded halves up, which is wrong in about one case in two thousand and which no
  amount of reading the code would have found. Go's `strconv` decides it, the way clang
  decides the codegen suite.
- **The interpreter no longer renders a float through `Display`** for `to_str`; that path
  now exists only for diagnostics. A float printed by an Origin program goes through the
  prelude on every engine.
