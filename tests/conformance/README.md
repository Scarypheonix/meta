# Conformance suite

One file per case. Every file begins with an expectation header:

```
// EXPECT: accept
// EXPECT: reject E0308
// EXPECT: reject E0308 E0004
```

A `reject` case must name every diagnostic code it expects; "it fails somehow" is not an
expectation. Every code named here must exist in `docs/spec/codes.md` — `./check`
enforces that today, before the compiler that emits the codes exists.

Phase 2's exit criterion is 200+ cases here with correct verdicts. The four seeded cases
exercise the harness, not the compiler.
