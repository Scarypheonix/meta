# Phase 0 — Complete

**Exit criteria:** `./check` passes on an empty implementation; the specification is
complete enough that someone else could implement from it.

**Status:** met. `./check` exits 0 (gofmt, `go vet`, `go build`, full test suite),
1 second elapsed, 149 MiB peak RSS against budgets of 300 s and 3072 MiB.

## What was built

**Specification** — `docs/spec/`, 12 documents, normative:

| Document | Contents |
|---|---|
| `00-overview.md` | design pillars, conformance vocabulary, the explicit not-in-0.1 list |
| `01-lexical.md` | UTF-8 source, spans, nesting block comments, every literal form, escapes, lexer error recovery, 12 worked token examples |
| `02-grammar.md` | complete EBNF, 14-level precedence table, two documented parser restrictions, normative parser error-recovery strategy, 13 worked parse examples |
| `03-types.md` | primitives, composites, equality, inference with its three deviations, no subtyping, well-formedness, 14 worked verdicts |
| `04-expressions.md` | evaluation order, the complete trap table, integer and float semantics, structural equality, the full `as` cast matrix, places, `for` desugaring, capture rules, 14 worked results |
| `05-patterns.md` | pattern forms, binding rules, guards, Maranget exhaustiveness with witnesses, constructor spaces, decision-tree lowering, 15 worked verdicts |
| `06-traits-generics.md` | associated types, supertraits, coherence, method resolution, monomorphization and its termination rule, prelude traits, 12 worked verdicts |
| `07-modules.md` | filesystem-as-module-tree, paths, `use`, two-level visibility, permitted module cycles, resolver output contract, 12 worked verdicts |
| `08-memory-model.md` | value categories, aliasing, GC guarantees, safepoints, write barriers, stacks, the `Send` concurrency model, 12 worked behaviours |
| `09-errors.md` | `Result`/`?`, traps, the diagnostic contract with seven normative rules, phase ordering and error suppression, 12 worked outcomes |
| `10-examples.md` | 15 complete programs with exact stdout, stderr and exit status — 12 accepted, 3 rejected |
| `codes.md` | the permanent diagnostic code registry, 22 codes |

**Decisions** — `docs/adr/`, 15 ADRs plus a template. Every one names the options
rejected and the cost of reversal. `docs/design-questions.md` records which of them the
user decided and which were taken under the standing delegation, so the two are never
confused later.

**Repository and harness:**

- `check` — the gate. gofmt, `go vet`, `go build`, `go test`, with the 5-minute and 3 GB
  budgets enforced as failures, not comments. Portable across macOS and Linux with
  nothing but bash, awk and ps (no GNU `time`, no python).
- `cmd/originc` — driver skeleton. `version` and `help` work; `check`, `build` and `run`
  panic with `unimplemented:` and the phase that will implement them.
- `internal/testutil` — the shared harness: case discovery, the `// EXPECT:` header
  parser, golden-file comparison. 15 unit tests covering every malformed-header case.
- `tests/conformance` — 4 seeded cases; the harness validates every expectation header
  and cross-checks every named diagnostic code against `codes.md` **today**.
- `tests/e2e` — 3 seeded cases from the spec examples; the harness validates that every
  case has its `.out` and `.exit` companions and that the exit status is possible.
- `tests/docs` — documentation invariants: required spec files exist, ADRs are uniquely
  numbered and structurally complete, every deferred item carries a phase, and no
  diagnostic anywhere contains an internal identifier.

## What was deferred

`docs/deferred.md` — 33 items across language, runtime and toolchain, each with a
reason and a phase. The three largest:

- **Per-green-thread panic isolation** (Phase 6). ADR-0006 buys a backend with no
  unwinder by making a panic kill the process. Phase 6 has to pay that back with either
  unwinding or a supervisor model. This is the single largest deferred item.
- **Trait objects** (Phase 7). Monomorphization (ADR-0010) is incompatible with dynamic
  dispatch without the uniform representation it exists to avoid.
- **Index syntax and collection literals** (Phase 7). ADR-0013 dropped them to make the
  grammar context-free; adding them back must not reintroduce the ambiguity.

## What surprised me

1. **The repository was not empty.** It held a static website. Recorded and resolved in
   ADR-0002 by moving it to `site/` rather than deleting anything.
2. **The development environment is not the target machine.** Linux x86-64, ELF `clang`,
   no `lldb`, no ability to execute Mach-O. Under a literal reading of the mandate,
   every end-to-end test and the entire Phase 9 bootstrap would be unrunnable where the
   code is written. ADR-0003 resolves it by sharing one instruction stream between two
   object-file writers — but **Phase 5 cannot be declared complete by the implementer
   alone**: that a compiled binary runs, and that `lldb` breaks on an Origin source
   line, are checklist items for the user on the MacBook.
3. **Dropping the index operator paid for itself immediately.** ADR-0013 chose `[]` for
   type application to avoid the `<`/`>>` ambiguity. The consequence — that `[` after an
   expression *always* starts type arguments — means `f[i64](x)` needs no turbofish at
   all. A restriction bought a feature.
4. **ADR-0004 turned out to decide ADR-0014.** Making mutability a property of field
   declarations was chosen to avoid a borrow checker. It then made "does this type have
   a `mut` field?" a purely syntactic, type-level question — which is exactly the
   predicate `Send` needs. Data-race freedom fell out of an unrelated decision.
5. **The trapping-overflow choice is forced, not preferred.** Rust's debug-trap/
   release-wrap split is the popular answer, and it is incompatible with Phase 4's own
   exit criterion. Two of the project's rules constrained a language decision that
   looked free.
6. **`==` being structural removed the need for attributes.** With compiler-generated
   equality (spec §04) there is no `derive(Eq)` to write, and with no other pressing use
   for attributes, 0.1 ships with no attribute syntax at all — a whole lexical and
   parsing subsystem that does not need to exist yet.

## Verification

```
$ ./check
==> gofmt
    ok   gofmt
==> go vet
    ok   go vet (0s, 15 MiB)
==> go build
    ok   go build (0s, 10 MiB)
==> go test
ok  github.com/scarypheonix/meta/cmd/originc          0.006s
ok  github.com/scarypheonix/meta/internal/testutil    0.004s
ok  github.com/scarypheonix/meta/tests/conformance    0.005s
ok  github.com/scarypheonix/meta/tests/docs           0.007s
ok  github.com/scarypheonix/meta/tests/e2e            0.003s
    ok   go test (1s, 149 MiB)

elapsed:  1s (budget 300s)
peak rss: 149 MiB (budget 3072 MiB)

PASS
```

## Gate

Phase 0 is complete and Phase 1 may begin. The next action is recorded in `CLAUDE.md`.
