# ADR-0009: Mandatory function signatures; inference is intra-procedural

**Status:** accepted · **Date:** 2026-08-28 · **Decided by:** implementer (user delegated)

## Context
Full Hindley–Milner infers top-level signatures. That is elegant and produces famously
bad error messages, because a mistake in one function is reported as a unification
failure in another.

## Options considered
- **Full HM with top-level inference.** No annotations required; errors point at
  arbitrary places; separate compilation needs interface files or a whole-program pass;
  traits plus inferred signatures brings the ambiguity problems ML avoids by not having
  traits.
- **Annotations required everywhere, no inference.** Simple; unusable for local
  bindings.
- **Mandatory signatures at item level, full inference inside bodies.** Each body is
  checked against a known signature, so every error is attributable to one function.

## Decision
Every `fn` at item, trait or impl level annotates all parameters and its return type.
Inference runs within a body: `let` types, lambda parameter types, and generic
instantiations are inferred. Generalization at `let` is restricted to syntactic values
(the value restriction), which is required for soundness given mutable fields.

## Consequences
- Error messages can say "expected `i64`, found `String`" with a span in the function
  the mistake is in. Phase 2's exit criterion depends on this.
- Separate compilation and incremental type checking are straightforward: a module's
  interface is its signatures, computable without checking bodies.
- The LSP (Phase 8) can type a single function without a whole-program solve.
- The cost is annotation burden on the author, and it is the reason Origin's own
  compiler source will be more verbose than an ML equivalent.
