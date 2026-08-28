# ADR-0012: Fully specified left-to-right evaluation; zero implicit coercions

**Status:** accepted · **Date:** 2026-08-28 · **Decided by:** implementer (user delegated)

## Context
Phase 4's exit criterion is that every program produces identical output at `-O0`,
`-O1` and `-O2`. That criterion is only meaningful if the language specifies enough that
"identical" is a property of the program rather than of the optimizer's mood.

## Options considered
- **Unspecified argument evaluation order (C, C++ pre-17).** Maximum optimizer freedom.
  It makes the `-O` differential harness useless: a difference between levels could be a
  legal reordering or a miscompilation, and nothing distinguishes them.
- **Specified left-to-right everywhere.** Costs the optimizer the freedom to reorder
  side-effecting or trapping operations — but only those; pure operations may still be
  reordered because the reordering is unobservable.
- **Implicit numeric widening** (`i32` → `i64`, int → float). Convenient; introduces
  silent precision and sign changes, and interacts with trapping arithmetic and literal
  defaulting in ways that need their own conformance section.

## Decision
Evaluation is strictly left-to-right, innermost-first, at every optimization level, for
binary operands, arguments, struct-literal fields in source order, tuple elements, and
the place/value halves of assignment. `&&`/`||` short-circuit. There are no implicit
conversions of any kind; every conversion is an `as` with defined, total semantics.

## Consequences
- Any output divergence across `-O` levels is unambiguously a bug, and the harness is a
  real oracle rather than a heuristic.
- Float arithmetic may not be reassociated or contracted into FMA, and NaN may not be
  assumed away. Origin gives up a class of float optimizations to keep the invariant.
- The programmer writes more `as` casts. Every one of them is a place a reviewer can
  see, which is the point.
