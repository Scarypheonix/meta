# Origin Language Specification — Overview

Status: **normative**. Version 0.1 (Phase 0).

Origin is a statically typed, garbage-collected, expression-oriented systems-adjacent
language. It compiles to native x86-64 machine code with no external code generator.

This directory is the source of truth for language behaviour. If the implementation
disagrees with this specification, the implementation is wrong — unless a numbered ADR
in `docs/adr/` supersedes a clause here, in which case this specification is stale and
must be corrected in the same commit that changes behaviour.

## Document map

| File | Covers |
|---|---|
| `00-overview.md` | This document: design pillars, conformance vocabulary |
| `01-lexical.md` | Source encoding, tokens, literals, comments, positions |
| `02-grammar.md` | Complete EBNF surface grammar |
| `03-types.md` | Type system, inference, generalization rules |
| `04-expressions.md` | Evaluation semantics, operators, precedence, traps |
| `05-patterns.md` | Patterns, exhaustiveness, usefulness |
| `06-traits-generics.md` | Traits, associated types, coherence, monomorphization |
| `07-modules.md` | Modules, paths, visibility, name resolution |
| `08-memory-model.md` | Object model, GC guarantees, concurrency, `Send` |
| `09-errors.md` | `Result`, `?`, panics, diagnostics requirements |
| `10-examples.md` | Worked examples with exact expected output |
| `11-codegen.md` | Native code generation, object files, the runtime, debug info |
| `12-concurrency.md` | Threads, channels, `Mutex`, deadlock, panics under concurrency |
| `13-collections.md` | `Array`, `List`, `Map`, and the specified hash |
| `14-strings.md` | `String`'s operations, character boundaries, interpolation |
| `15-files.md` | Reading and writing whole files |
| `16-floats.md` | What `to_str` on a float produces, exactly |

## Design pillars

1. **No undefined behaviour, ever.** Every operation either produces a defined value or
   traps with a diagnostic and a non-zero exit code. There is no clause in this
   specification that reads "the result is unspecified".
2. **Optimization is unobservable.** A conforming program produces byte-identical stdout
   and the same exit code at every optimization level. This is a hard constraint on the
   optimizer, not an aspiration, and it is why evaluation order is fully specified
   (§04) and why integer overflow traps at every optimization level (ADR-0005).
3. **No null.** Absence is `Option[T]`. There is no null pointer, no zero value, and no
   uninitialized binding (ADR-0007).
4. **Errors are values.** No exceptions, no unwinding in the language semantics
   (ADR-0006).
5. **Immutable by default.** Bindings and struct fields are immutable unless declared
   `mut` (ADR-0004).
6. **The parser is context-free.** Type application uses `[]`, there is no index
   operator, and no construct requires the parser to know whether a name is a type or a
   value (ADR-0013).

## Conformance vocabulary

- **MUST / MUST NOT** — a conforming implementation is incorrect if it violates this.
- **TRAPS** — execution stops, a message is written to stderr, and the process exits
  with status `101`. Traps are not catchable in Origin 0.1.
- **REJECTED** — the compiler MUST emit at least one error diagnostic and MUST NOT
  produce an output binary.
- **DEFERRED** — deliberately not in 0.1; see `docs/deferred.md` for the phase it is
  scheduled against.

## What Origin 0.1 deliberately does not have

Operator overloading, index syntax `a[i]`, collection literals, attributes/`derive`,
macros, closures capturing by reference distinct from by value, lifetimes, borrow
checking, const generics, higher-kinded types, generic associated types,
specialization, weak references, finalizers, unwinding, `unsafe` blocks, variadics,
default trait method type parameters, a `Path` type, file handles.

Each of these is listed in `docs/deferred.md` with a rationale and a target phase.
Absence here is a decision, not an oversight.
