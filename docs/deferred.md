# Deferred Items

Everything deliberately left out of Origin 0.1, with the phase it is scheduled against
and why it was deferred. Process rule 4: deferred items are recorded here, never
silently dropped. An item may move phases; it may not vanish without a line in
`docs/phases/N-complete.md` saying so.

## Language

| Item | Why deferred | Phase |
|---|---|---|
| `if let` / `while let` | pure sugar over a two-arm `match`; needs the match lowering first | Phase 3 |
| `?` on `Option[T]` | needs a shared "try" abstraction or a second desugaring rule | Phase 7 |
| Error conversion in `?` via `Into` | needs a blanket-impl story that interacts with coherence (ADR-0011) | Phase 7 |
| Trait objects / `dyn` | needs the uniform value representation monomorphization avoids (ADR-0010) | Phase 7 |
| Multi-parameter traits | inference ambiguity; not needed for the stdlib as designed | Phase 7 |
| Generic associated types | needs higher-kinded machinery | — (not planned) |
| Specialization | interacts badly with coherence; open problem elsewhere | — (not planned) |
| Operator overloading | `+` on user types needs traits in the operator path; `==` is already structural | Phase 7 |
| Index syntax `v[i]` | must not reintroduce parse ambiguity (ADR-0013); likely spelled `v.[i]` | Phase 7 |
| Collection literals | same ambiguity constraint as index syntax | Phase 7 |
| Attributes and `derive` | mostly unnecessary because `==` is compiler-generated (ADR-0011) | Phase 7 |
| Range patterns (`1..=9`) | exhaustiveness over integer ranges needs interval splitting in Maranget | Phase 7 |
| Labelled `break` / `continue` | needs loop labels in the resolver | Phase 7 |
| Glob imports, `use ... as` | ambiguity rules and rename bookkeeping | Phase 7 |
| String interpolation | needs a formatting trait and a lexer mode | Phase 7 |
| Raw string literals | trivial, but no consumer yet | Phase 7 |
| `isize` / `usize` | add only if the Mach-O writer needs them in Origin source | Phase 5 |
| Fixed-size arrays `[T; N]` | needs const generics | Phase 7 |
| `const fn` / calls in const initializers | needs the const evaluator to run real code | Phase 5 |
| The never type `!` written in source | exists in the checker, no syntax for it | Phase 7 |
| Doc comment capture | needed by LSP hover, not before | Phase 8 |
| Effect checking for `match` guards | closes the one "unspecified" clause in the spec (§05) | Phase 5 |
| Cycle detection in structural `==` | a cyclic graph makes `==` diverge (§04) | Phase 7 |
| Tuple element access (`t.0`) | found in Phase 1: the grammar's `.` takes an identifier, so a tuple can only be read by pattern. Needs either integer field syntax or a lexer rule for `.0` | Phase 7 |
| Float formatting in `to_str` | the spec does not fix a rendering; the interpreter uses the shortest round-tripping form and the e2e suite avoids depending on it | Phase 7 |
| Per-width integer overflow | both engines carry every integer as a single i64 `Value`; a field or local now has an exact declared width in its layout (ADR-0019), but arithmetic itself does not yet trap or wrap at anything narrower than 64 bits | Phase 5 |
| `u64::MAX` at run time | the same limitation: it has no representation in an i64 value model, so it is rejected rather than returning a wrong number | Phase 5 |
| Exact object layouts from monomorphization | delivered in Phase 5: every struct, tuple, enum-variant and closure instantiation gets its own exact `Fixed` layout, keyed the same way `internal/mono` keys a function instance (ADR-0019). `layout.Tagged` is retired | done |
| `for` loops in compiled code | delivered in Phase 5: `internal/check`'s `forElementType` records the desugaring's two implicit calls (`into_iter()`, `next()`) as instantiations keyed to existing AST nodes (`v.Iter`, `v` itself), so `internal/mono` and `internal/compile`'s `forExpr` find a concrete callee for each exactly as an explicit method call would; the backend needed no changes since a desugared `for` lowers to ops (calls, jumps, `is_variant`, `get_field`) it already handled | done |
| Decision-tree lowering for `match` | the compiler emits a linear chain of arm tests; the tree belongs on the control-flow graph | Phase 5 |
| Instantiation depth diagnostic truncation | E0055's instantiation chain is not shortened past the list level, so a deep chain's note is long: each entry's type name has already grown by the time truncation would apply | Phase 5 |
| Orphan rule (`E0117`) | cannot be violated while a program is one package; it becomes checkable with the package manager | Phase 8 |
| Compiler-provided impls in Origin | `Show`, `Ord` and `Int` for the primitives are registered by the checker because their bodies need operations the language does not expose yet | Phase 7 |
| Static field-mutability check | delivered in Phase 2: assigning a non-`mut` field is now `E0594` at compile time | done |
| Block-level (nested) item declarations | `check/body.go` documents the choice explicitly: "Phase 2 checks items at file level only... left to Phase 8's incremental machinery." A struct declared inside a function body resolves but is never checked, so before Phase 5's exact layouts a use of it silently compiled against whichever descriptor happened to be at index 0; it now fails loudly instead (`internal/compile`'s per-instantiation lookup has nothing to find), which is correct per process rule 8 but not a fix | Phase 8 |

## Runtime

| Item | Why deferred | Phase |
|---|---|---|
| Per-green-thread panic isolation | needs unwinding or a supervisor model; the single largest deferred item (ADR-0006) | Phase 6 |
| Weak references | no consumer; complicates the moving collector | Phase 7 |
| Finalizers | resurrect-and-order problems for no current benefit | Phase 7 |
| Exposed atomics | add only if the scheduler needs them in Origin source (ADR-0014) | Phase 6 |
| Lock-free data structures in safe Origin | impossible under ADR-0014; would need an `unsafe` escape hatch | — (not planned) |
| `unsafe` blocks | nothing in the design needs one yet; adding one is a spec-pillar change | — (not planned) |

## Toolchain

| Item | Why deferred | Phase |
|---|---|---|
| Instantiation deduplication by signature hash | mitigation for monomorphization code size (ADR-0010) | Phase 5 |
| Cross-build instantiation caching | mitigation for monomorphization compile time (ADR-0010) | Phase 8 |
| Cross-compilation | explicitly out of scope; two object writers is not cross-compilation (ADR-0003) | — (not planned) |
| Incremental compilation in the LSP | needs the query architecture; full recheck first | Phase 8 |
| Closures in native code | `originc build` rejects any function with `Captures > 0`; struct/tuple/enum-variant construction and field access are lowered (this phase), but building a closure object and reading a capture are not | Phase 5 |
| Native heap collection | the backend's `alloc` bump-allocates but never triggers a collection: that needs precise stack maps, which need the bytecode widened to carry a value's kind the way `OpToStr` and the comparison ops already were (docs/spec/11-codegen.md's own DEFERRED note) | Phase 5 |
| Structural `==`/`!=` on aggregates and `String` in native code | delivered in Phase 5: `equal_objects` reads an embedded per-`TypeID` layout table and recurses on a `WordRef` field, mirroring `internal/vm`'s `equalObjects` exactly (`internal/backend/equal.go`) | done |
| Structural `<`/`<=`/`>`/`>=` on `String` in native code | delivered in Phase 5: `compare_bytes` (`internal/backend/equal.go`) does the byte-lexicographic walk `equal_objects`' `ByteArray` case already needed, returning a three-way sign both `stringOrder` and `cmp` (`buildOrdering`) read | done |
| The `cmp` builtin in native code | delivered in Phase 5: rather than widen `OpCallBuiltin` with an operand-kind operand, `compile.cmpBuiltinFor` picks one of four kind-specific builtins (`BuiltinCmpInt`/`Uint`/`Float`/`String`) at compile time, so the backend already knows which comparison a given call needs without reading anything from the instruction beyond which builtin it is | done |
