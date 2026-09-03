# 07 — Modules, Paths and Visibility

## Packages and modules

A **package** is a directory containing `origin.toml` and a `src/` tree. A **module** is
a single `.origin` file. A module's path is its file path under `src/`, with separators
replaced by `::` and the `.origin` extension removed:

```
mypkg/
  origin.toml           name = "mypkg"
  src/
    main.origin         module mypkg
    lex/
      token.origin      module mypkg::lex::token
```

`src/main.origin` (for an executable) or `src/lib.origin` (for a library) is the package
root module. There is no `mod` declaration and no `mod.rs` equivalent: the filesystem is
the module tree, and no file needs to be registered anywhere to be compiled. Every
`.origin` file under `src/` is part of the package.

## Paths

```
mypkg::lex::token::Token      absolute, from a package name
std::io::println              absolute, into a dependency
lex::token::Token             relative, resolved against the current module's parent
Token                         a name brought into scope by `use`, or declared locally
lex::token::Token::Ident      a variant of another module's enum
Self::Item                    an associated type of the impl's self type
i64::MAX                      an associated const of a primitive
```

A path's last segment names an item in the module the segments before it name — except
for a variant, where the last *two* segments are the enum and the variant and everything
before them is the module. `lex::token::Token::Ident` is therefore module `lex::token`,
enum `Token`, variant `Ident`, and it means the same thing in an expression and in a
pattern. The enum must be visible from here; the variant itself carries no separate
visibility.

Resolution order for a bare identifier, first hit wins:

1. local bindings (innermost scope outward),
2. items declared in the current module,
3. names imported by `use` in the current module,
4. the prelude.

A name that resolves at two different steps is not an error — the earlier step shadows.
A name that resolves ambiguously *within* one step (two `use`s importing the same name)
is REJECTED, naming both imports.

## `use`

```origin
use std::io;
use std::collections::{Vec, Map};
```

`use` imports a single name or a braced list of names from one path. There are no glob
imports, no renaming (`as`), and no nested groups in 0.1 — each is DEFERRED (Phase 7).
All `use` declarations MUST precede all items in a file; the formatter sorts them.

Importing a name that is not `pub` in its defining module is REJECTED, with a span on
the `use` and a note on the private declaration.

## Visibility

Two levels:

- **private** (the default): visible in the declaring module only, including its
  descendants? **No** — visible in the declaring module *only*, not in child modules.
  A child module must `use` a `pub` item like anyone else.
- **`pub`**: visible wherever the module is reachable, including from other packages.

Fields carry their own visibility independent of the struct's: `struct P { pub x: f64, y: f64 }`
is a public type with one public and one private field. Consequences worth stating
because they surprise people:

- A struct with any private field cannot be constructed by a struct literal outside its
  module. Provide a constructor function.
- A struct with any private field cannot be destructured by a pattern outside its
  module unless the pattern ends in `..` and omits the private fields.
- `==` still compares private fields (it is compiler-generated, not user code).

Enum variants are as visible as their enum; there is no per-variant visibility.

Trait methods are as visible as their trait. An `impl` block's `pub` markers on methods
apply only to inherent impls; on a trait impl they are REJECTED as redundant.

## Cycles

Module-level cycles are **permitted**. Origin resolves names across the whole package
before type checking, so `a.origin` and `b.origin` may refer to each other. Cyclic
*package* dependencies are REJECTED by the package manager (Phase 8). Cyclic `const`
initializers are REJECTED with the cycle printed.

## Name resolution output

Name resolution produces, for every identifier occurrence, exactly one of: a binding id,
an item id, or an `Error` marker (already reported). Later phases MUST NOT re-resolve
names or consult scopes; the resolver's output is the single source of truth. This is
the module-boundary contract from process rule 5 — `internal/resolve` owns it and
`internal/types` consumes it.

## Worked examples

| Situation | Verdict |
|---|---|
| `src/a.origin` uses `mypkg::b::f` where `b.origin` declares `pub fn f` | accepted |
| same, but `f` is not `pub` | REJECTED — private, note on the declaration |
| `a.origin` and `b.origin` `use` each other | accepted — module cycles are fine |
| two `use` lines importing `Token` from different modules | REJECTED — ambiguous |
| `use` after an `fn` item | REJECTED — imports must precede items |
| `use std::collections::*` | REJECTED — glob imports not in 0.1 |
| `P { x: 1.0, y: 2.0 }` outside `P`'s module with `y` private | REJECTED |
| `match p { P { x, .. } => x }` outside `P`'s module | accepted |
| `match p { P { x, y } => x }` outside `P`'s module, `y` private | REJECTED |
| a file under `src/` never mentioned by any `use` | compiled anyway; unused-item warning |
| `const A: i64 = B; const B: i64 = A;` | REJECTED — cyclic const |
| `i64::MAX` | accepted — associated const on a primitive |
| `b::Enum::Variant` where `b.origin` declares `pub enum Enum` | accepted — in an expression and in a pattern |
| same, but `Enum` is not `pub` | REJECTED `E0603` — private, note on the declaration |
| `b::Struct::Variant` where `Struct` is not an enum | REJECTED `E0433` — only an enum has variants |
