# ADR-0015: AST as a sealed interface with per-node structs; ids in side tables

**Status:** accepted · **Date:** 2026-08-28 · **Decided by:** implementer (user delegated)

## Context
Go has no sum types. An AST is a sum type. Every Go compiler picks an encoding, and the
choice determines how exhaustive the compiler's own `switch`es are and how much of the
type checker's output has to be threaded through the tree.

## Options considered
- **One struct with a `Kind` tag and a union of fields.** Cache-friendly and compact;
  every access is unchecked, and a node's valid fields are documented in comments
  instead of in the type system. The class of bug it invites — reading `Lhs` on a node
  that has none — is exactly what this project cannot afford in a self-hosted rewrite.
- **Interface with a marker method, one struct per node kind.** Type switches are
  checked by the compiler; each node carries only the fields it has; `go vet` and
  exhaustiveness linting can reason about it. Costs a pointer indirection per node and
  an allocation per node.
- **Arena of indices (`NodeID` into flat slices).** Best cache behaviour and trivially
  serializable; every traversal reads like assembly, which harms the readability of the
  code that must later be transliterated into Origin.

## Decision
Sealed interface `ast.Node` with an unexported marker method, one struct per node kind,
each embedding `diag.Span`. Semantic results — resolved names, inferred types,
instantiations — live in **side tables keyed by node id**, never in mutable fields on
the node. Nodes are immutable after parsing.

## Consequences
- The type checker cannot corrupt the AST, and re-checking a function is idempotent —
  which the LSP (Phase 8) needs.
- Every pass's output is a separate, independently testable table, matching process
  rule 5: `internal/resolve` owns the name table, `internal/types` owns the type table,
  neither reaches into the other's.
- The transliteration to Origin in Phase 9 is direct: a Go sealed interface becomes an
  Origin `enum`, and the side tables become `Map[NodeId, _]`. Choosing the arena
  encoding would have made that rewrite a redesign.
- Costs an allocation per AST node in the Go host. Measured against the 60-second
  incremental budget in Phase 1; if it becomes the bottleneck, node structs move into
  per-kind slab allocators without changing the interface.
