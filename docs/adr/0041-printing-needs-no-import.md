# ADR-0041: `print` and `println` are in scope everywhere

**Status:** accepted · **Date:** 2026-09-10 · **Decided by:** implementer (user delegated)

## Context

The smallest program Origin can express costs four lines, two of which are ceremony:

```origin
use std::io

fn main() {
    io::println("hi")
}
```

Python's is `print("hi")`. Go's needs an import too, but Go is not the language a beginner is
compared against — Python is, and the user's complaint was specifically that this is "still
very complicated".

**The language is already inconsistent about it.** These names are in scope in every module
with no `use`, because the prelude declares them: `args`, `read_to_string`, `write_string`,
`file_exists`, `float_to_str`. So today:

```origin
let text = read_to_string("f.txt")   // no import
io::println(text)                    // needs `use std::io`
```

Reading an entire file off the disk needs nothing. Printing a line needs an import. There is
no principle behind that split — it is an accident of `read_to_string` being written in
Origin in the prelude while `println` is a compiler builtin reached through a module path.

`std::io` is also not what the other `std::` modules are. Reading `stdModules`' own comments,
`std::array`, `std::str`, `std::hash`, `std::float`, `std::fs`, `std::chan` and `std::sync`
exist so that *the prelude* can be written — "the operations the prelude's own methods are
written in terms of, since a method body cannot otherwise reach an operation the runtime
provides." A user program is not supposed to call them. `std::io` is the one module in that
table whose contents are aimed at the person writing the program.

## Options considered

- **Leave it.** Costs nothing, and `use std::io` is one line. Rejected: it is the first line
  of the first program anyone writes, and it teaches that Origin makes you ask permission to
  print. The inconsistency with `read_to_string` makes it indefensible rather than merely
  verbose.

- **Make every `std::` module global.** Removes all imports. Rejected outright: it would put
  `new`, `len`, `at`, `set`, `push`, `of`, `bits`, `send_value`, `with_lock` and thirty more
  into the global namespace, where they would shadow nothing today and collide with
  everything a user might name tomorrow. Most of those names are plumbing the prelude needs
  and a program should never see.

- **Move `println` into the prelude as Origin source.** Would make it global by the same
  route `read_to_string` takes, and needs no resolver change. Rejected because there is
  nothing to write: `println` *is* the runtime operation, so a prelude wrapper would be a
  function whose entire body is the builtin it wraps, added only to reach a scoping rule.

- **Put `print` and `println` in the global scope directly.** Chosen. Two names, both verbs,
  both universal, neither plausible as a user's own top-level function in a way that matters
  (a module's own declaration shadows them, as it does for every global).

## Decision

`print` and `println` resolve without a `use`, in every module. `std::io` remains, and
`io::println` continues to mean exactly the same builtin, so no existing program changes.

## Consequences

- **Hello world loses half its lines**, and the two it loses are the two that carry no
  information:

  ```origin
  fn main() {
      println("hi")
  }
  ```

- **The resolver is the only pass that changes.** `globalBuiltins` becomes a map from the
  global name to the builtin it names, rather than a set where the two coincide — `panic`
  and `ref_eq` name themselves, `println` names `io::println`. Everything downstream sees
  the `Ref` it already saw, so no golden file moves and the corpus is not rewritten.

- **Shadowing behaves as it does for every other global.** A module that declares its own
  `println` gets its own, because module scope is layered over the globals (§07). Nothing
  that compiles today stops compiling.

- **This does not start a slide toward a flat namespace.** The criterion is the one that
  distinguished `std::io` from the rest of the table: a name is global when it is aimed at
  the person writing the program *and* is universal enough that its absence is what surprises
  people. That admits `print` and `println` and, on the evidence of the table, nothing else
  currently in `std::`. A future candidate has to clear the same bar rather than cite this
  ADR as precedent.

- **`list::new` and `map::new` are pointedly not included**, though they are the next most
  common imports a program needs. They are not a scoping problem: they are the shape
  associated functions would fix (`List::new()`), which is held for a separate decision
  (`docs/deferred.md`). Making them global would spend the namespace on the wrong fix.

- **Reversing it is deleting two map entries.** Every program written before this ADR still
  compiles, since `io::println` is untouched; programs written after it, with the bare name,
  would not.
