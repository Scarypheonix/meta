# ADR-0019: Every instantiation gets an exact object layout; tagged slots retire

**Status:** accepted · **Date:** 2026-08-29 · **Decided by:** implementer (user delegated)

## Context
ADR-0010 monomorphized *call dispatch*: each generic function is compiled once per
tuple of type arguments actually used, and a call inside its body resolves to the
concrete callee. It deliberately left object *layout* alone. A struct or enum field of
generic type still went through `internal/layout`'s `Tagged` shape — two words per slot,
a `ValueTag` written beside the payload at construction time, read back at every access.
That was the right staging decision for Phase 4: it kept the collector precise without
needing to know, ahead of time, what a generic field's word count and reference-ness
would turn out to be.

Phase 5 removes the reason that staging decision existed. A native backend cannot afford
a runtime tag dispatch on every field read, and it cannot build a stack map for a struct
whose shape isn't known until the field is read. `docs/deferred.md` already named this
the natural Phase 5 conclusion: "Fields still use the two-word tagged-slot
representation, because giving each instantiation its own exact layout is shared work
with the backend."

Every struct literal, tuple, enum-variant construction and closure capture list is
compiled inside some `mono.Instance`'s body, which carries a full substitution
(`Instance.Subst`) from that instance's own generic parameters to concrete types. That
substitution is exactly what's missing to give a construction site an exact layout: read
the field's declared type (in terms of the struct's or enum's own parameters, from
`types.Def.FieldTypes`/`VariantTypes`), substitute the struct's own type arguments, then
substitute the instance's `Subst` for anything still symbolic, and the result is fully
concrete.

## Options considered
- **Keep `Tagged` for generic fields, add `Fixed` only for non-generic ones.** Two object
  representations alive at once, decided per declaration rather than per instantiation.
  Doesn't remove the backend's problem: a call site inside a generic body still needs to
  know, concretely, what `T` turned out to be, which needs the same substitution
  machinery either way — so the "simple" option buys nothing and leaves two GC-reading
  code paths to maintain.
- **Recompute a layout key from the mangled instance name.** Piggybacks on `mono`'s
  naming instead of building a parallel cache. Rejected: a layout is keyed by the
  *fields'* concrete types, not the *function's* — two different generic functions can
  construct the same instantiation of the same struct, and a name string is a worse key
  than the type-identity key `mono` already uses internally.
- **Give every instantiation an exact `Fixed` descriptor, cached by (declaration,
  concrete type-argument tuple).** Mirrors `mono.Instance`'s own keying scheme
  one-for-one. The GC already supports this: `internal/gc/collect.go`'s scanning switch
  had a `default` branch reading `Descriptor.IsRef(i)` generically, and the property
  tests in `internal/gc` already exercise `Fixed`/`FixedDescriptor` — that scaffolding
  was added ahead of Phase 5 and had simply gone unused by the compiler until now.

## Decision
`internal/compile` builds one `layout.Fixed` descriptor per **(declaration, concrete
type-argument tuple)**, the same granularity `internal/mono` already uses for function
instances, cached the same way. `layout.Tagged` and `TaggedDescriptor` are deleted:
nothing constructs them once every call site resolves a concrete instantiation, and
process rule 8 (no stubs that lie) rules out keeping a shape nothing produces.

Three things fall out of making this precise:

1. **`WordKind` gains real granularity.** The collector only ever needed
   ref-or-not-or-float, but `Fixed` shape has no runtime tag to fall back on, and
   `to_str` needs to know a raw word is a `bool` and not an `i64` holding `1`.
   `WordKind` grows `WordInt`, `WordBool`, `WordChar`, `WordUnit` alongside the
   existing `WordRaw`, `WordRef`, `WordFloat`; `IsRef`/`IsFloat` are unchanged.
2. **Closures are keyed by their capture types, not their capture count.** Two lambdas
   that both close over one `i64` and one `String` share a descriptor; two lambdas with
   the same *count* but different capture types do not. `bytecode.Program.Closures`
   replaces the old `ClosureTypes map[int]TypeID` with one `ClosureInfo{FnIndex, Type}`
   per closure-creation site, mirroring how `Program.Variants` already works.
3. **A bare function reference is boxed into a captureless closure at the point it is
   written into a reference-shaped field.** A path naming a top-level function evaluates
   to `TagFn` (an index, not a heap object) so that calling it directly costs nothing;
   a lambda literal always evaluates to a heap closure, even with zero captures. Both are
   the same static type (`FnT`), so a struct field or tuple slot of function type must
   accept either at different times — impossible for one `Fixed` word to distinguish
   without a runtime tag. The VM normalizes: writing a `TagFn` value into a `WordRef`
   slot allocates a one-word closure object (`Program.FnBoxType`, the same shape as a
   genuine zero-capture closure) and stores that instead. Reading it back is uniform —
   always a closure object — which is sound because every existing user of a function
   value (`doCall`, `==`'s ban on comparing functions, `to_str`) already treats a
   zero-capture closure and a bare function reference identically.

## Consequences
- **The interpreter is untouched.** It never touched `internal/gc`/`internal/layout`;
  this is a VM- and compiler-only change, verified by the differential the two engines
  already run against each other (`TestEnginesAgree`).
- **Every field access instruction (`OpGetField`/`OpSetField`/`OpGetPayload`/
  `OpGetTupleElem`) is unchanged.** Only construction sites (`OpStruct`/`OpTuple`/
  `OpVariant`/`OpClosure`) needed new descriptor-resolution logic; a field index still
  means "word `i`" on both sides of the change.
- **`u64::MAX` and per-width overflow remain deferred** (`docs/deferred.md`): `WordInt`
  does not distinguish signed from unsigned, matching the VM's existing single `TagInt`
  representation. Giving every width its own runtime behavior is separate work.
- **This is the second reader `internal/layout` was always meant to gain.** The backend
  (ADR-0017/0018) can now read an object's exact field offsets and pointer-ness directly
  from its `Descriptor`, which is what a stack map needs — the collector and the backend
  agree on shape because there is exactly one table describing it (process rule 5).
