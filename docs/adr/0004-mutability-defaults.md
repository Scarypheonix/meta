# ADR-0004: Immutable by default; mutability is a property of field declarations

**Status:** accepted · **Date:** 2026-08-28 · **Decided by:** implementer (user delegated)

## Context
Two orthogonal questions: may a binding be reassigned, and may an object's contents
change? Languages answer these together (Rust: `mut` on the binding governs both,
enforced by borrowing) or separately (OCaml: `mutable` on the field).

## Options considered
- **Rust's model** — `mut` on bindings, mutation permitted only through a `mut` path,
  enforced by a borrow checker. Excellent guarantees; requires lifetimes and borrow
  checking, which is a project of its own and is redundant under a GC.
- **Mutable by default (Go/C).** Least ceremony, weakest guarantees, and it makes
  `Send` inference (ADR-0014) useless because everything is mutable.
- **OCaml's model** — immutable bindings by default with `let mut` to opt in to
  reassignment, and `mut` on individual struct fields to opt in to mutation. No aliasing
  discipline; a `mut` field is mutable through every reference.

## Decision
OCaml's model. `let x` binds immutably, `let mut x` permits reassignment. A struct field
is immutable unless declared `mut`, and a `mut` field is mutable through any reference.

## Consequences
- No borrow checker, no lifetimes, no `&`/`&mut` distinction in the type system. This is
  the single largest reduction in project scope in the whole design.
- Aliasing is observable (spec §08); the specification says so explicitly rather than
  pretending otherwise, and example 10 in `10-examples.md` pins the behaviour.
- Immutability becomes a *type-level* property (does this type have any `mut` field?),
  which ADR-0014 uses to derive `Send` automatically and thereby eliminate data races.
- The optimizer gets a real guarantee: a non-`mut` field, once written at construction,
  never changes, so loads from it are freely CSE-able and hoistable.
