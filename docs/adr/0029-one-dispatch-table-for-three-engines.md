# ADR-0029: Every engine resolves a method through monomorphization

**Status:** accepted · **Date:** 2026-09-01 · **Decided by:** implementer (user delegated)

## Context

Phase 7 needed `impl Str for String` — a trait implemented for a primitive type, with
default method bodies in Origin over a handful of compiler-provided operations. Two
programs written to try it out disagreed between engines:

```origin
trait Loud { fn shout(self) -> String; }
impl Loud for i64 { fn shout(self) -> String { "i64" } }
impl Loud for u8  { fn shout(self) -> String { "u8" } }
fn main() { io::println(1i64.shout()); }
```

The VM and native code printed `i64`. The interpreter trapped: *no method `shout` on
integer*. A second program, using a trait's **default method body**, failed on every
engine — the checker accepted it and nothing ran it, though `docs/spec/06-traits-generics.md`
has promised default methods since Phase 2 and shows one in its first example.

Both had the same root. The interpreter resolved a method by looking the name up in a table
`internal/resolve` built, keyed by the impl's type name. That table cannot answer either
question:

- **`impl Loud for i64` beside `impl Loud for u8`.** Which one a call reaches is a fact
  about the receiver's *static* type. An interpreted integer is a Go `int64` with no width
  — `internal/interp`'s own package comment has said so since Phase 1 — so the two impls
  are indistinguishable from the value in front of it.
- **A default method body.** It is declared on the trait, not on any impl, so it appears
  under no type's name at all. And it is generic in `Self` exactly the way a generic
  function is generic in its parameters: one body, one instance per implementing type.

The checker had the answer both times and was not being asked. `check.Result.Methods` has
carried the comment "so the interpreter and later the backend do not repeat the search"
since Phase 2, and the interpreter never read it. But the checker's per-call-site record is
not enough on its own: inside `fn go[T: Loud](x: T)` there is *one* call site reaching a
different impl per instantiation, and one node cannot hold two answers.

## Options considered

1. **Give interpreted values their width.** Make `Int` carry `i8`…`u64`. Fixes the numeric
   case and nothing else — a default body still lives under no type name — and it makes
   every arithmetic path in the interpreter carry a tag the specification says is static.
2. **Read `check.Result.Methods`, keep name lookup as a fallback.** Cheap. Fixes the
   monomorphic cases. Leaves `x.shout()` inside a generic body wrong, because that site's
   record is the *trait's* declaration, and silently so: the trait's default body would run
   in place of an impl that overrides it.
3. **Resolve through `mono.Result`, as the bytecode compiler and the backend already do.**
   The instantiation set already maps (instance, call site) → instance. It is the answer to
   exactly this question, computed once, for the whole program.

## Decision

**Option 3.** The interpreter takes `*mono.Result` and runs each frame *as an instance*:
`frame.inst` is the monomorphized instance whose body it is evaluating, and every call made
from that frame is `inst.Calls[node]` — the same table, the same lookup, the same answer
the other two engines get. A function value carries the instance it stands for, so a call
through a `*Closure` resolves the same way; a lambda carries the instance that created it.

Two supporting changes fall out:

- `internal/check`'s `inferMethodCall` substituted `Self` one line *after* recording the
  call site's instantiation, so a default body — whose declaration is the trait's, and
  whose generic parameters therefore include `Self` — was recorded with `Self` unknown and
  reached no instance at all. That is why default methods had never run.
- `resolve.Result.Methods`, the name-keyed table, is deleted. Nothing reads it now, and
  leaving a second way to answer a question this ADR gives one answer to is the invariant
  smell `CLAUDE.md` warns about.

## Consequences

- The interpreter is no longer "the engine that runs without types". It never really was:
  its package comment listed the places it approximates, and method dispatch was one it had
  not noticed. It still evaluates the syntax tree, and it still keeps no types *of its own*
  — it reads the ones the checker already computed.
- Default methods work, on all three engines, including through a bound and including when
  an impl overrides one. `tests/e2e/cases/trait_default_methods.origin` is the case.
- `impl Trait for i64` works on all three engines, and so does the same call inside a
  generic body. `tests/e2e/cases/impl_on_a_primitive.origin` covers every primitive that
  can carry an impl.
- A trait implemented for a primitive is now a thing the prelude can write. That is what
  Phase 7's `Str` is: four compiler-provided operations under a surface of ordinary Origin.
- `internal/driver` must monomorphize before interpreting. It already did — ADR-0010 made
  instantiation part of checking a program's legality, so `originc check` and `originc run`
  reach the same verdict — so this costs nothing new.
- One divergence class is gone rather than narrowed: there is no longer a dispatch question
  one engine answers from a table another engine does not have.

## Reversing it

Nothing about this is visible in the language, so it reverses by changing code. The
interpreter could go back to name lookup only by giving up default methods and impls on
primitives, which the specification promises and the prelude now uses.
