# 09 — Error Handling and Diagnostics

Two distinct mechanisms, kept distinct on purpose:

| | Recoverable failure | Bug |
|---|---|---|
| Mechanism | `Result[T, E]` returned as a value | trap / `panic` |
| Visible in signature | yes | no |
| Catchable | yes, by pattern matching | no (0.1) |
| Example | file not found, parse failure | index out of bounds, integer overflow |

There are no exceptions and no stack unwinding in the language semantics (ADR-0006).

## `Result` and `?`

```origin
pub enum Result[T, E] { Ok(T), Err(E) }

fn read_config(path: String) -> Result[Config, IoError] {
    let text = read_to_string(path)?;       // returns early on Err
    let cfg = parse(text)?;
    Result::Ok(cfg)
}
```

`e?` is valid only where:

- `e` has type `Result[T, E]`, and
- the enclosing function's return type is `Result[U, E]` with **the same** `E`.

It evaluates to `T` on `Ok`, and on `Err(err)` it immediately returns `Result::Err(err)`
from the enclosing function. Exactly:

```origin
match e {
    Result::Ok(v)  => v,
    Result::Err(x) => return Result::Err(x),
}
```

`?` inside a lambda applies to the lambda's return type, not the enclosing function's.
Automatic error conversion via an `Into` bound on `E` is DEFERRED; until then, convert
explicitly with `map_err`, which the prelude provides:

```origin
let text = read_to_string(path).map_err(|e| Fault::Io(e))?;
```

Using `?` in a function that does not return `Result` is REJECTED with a note showing
the signature change that would fix it.

### `?` on `Option`

`?` applies to `Option[T]` as well, in a function returning `Option[U]`:

```origin
fn initial(s: String) -> Option[String] {
    let c = s.chars().get(0)?;      // returns early on None
    Option::Some(c.to_str())
}
```

It evaluates to `T` on `Some`, and on `None` it immediately returns `Option::None` from
the enclosing function. Exactly:

```origin
match e {
    Option::Some(v) => v,
    Option::None    => return Option::None,
}
```

The two forms do not mix: `?` on an `Option` in a function returning `Result` is REJECTED,
and so is the other way round. There is nothing `?` could build out of an `Option` to
satisfy a `Result`, and inventing one — a `None` becoming `Err` of some default — would be
a conversion the signature does not mention.

### What the early return returns

The value `?` returns is built at the **enclosing function's** return type, not carried
over from the value it was given.

That is not a detail. `Result[i64, E]::Err(e)` and `Result[String, E]::Err(e)` are
different types with different object layouts (ADR-0019), even though both carry one `E`:

```origin
fn inner() -> Result[i64, String] { Result::Err("boom") }

fn outer() -> Result[String, String] {
    let n = inner()?;              // the Err returned here is `Result[String, String]`'s
    Result::Ok("fine")
}
```

Handing `outer`'s caller an object of `inner`'s type would give it a value whose variant
tag belongs to a different instantiation, and its own `match` would match no arm at all.
So `?` unwraps the payload and rebuilds — except when the two instantiations coincide, in
which case there is nothing to rebuild.

## The methods on `Option` and `Result`

Both are ordinary Origin in the prelude, with ordinary methods — the same arrangement §13
made for collections and §14 for strings, for the same reason (ADR-0028): nothing here
needs the compiler.

```origin
impl[T] Option[T] {
    pub fn is_some(self) -> bool
    pub fn is_none(self) -> bool
    pub fn unwrap_or(self, fallback: T) -> T
    pub fn unwrap_or_else(self, fallback: fn() -> T) -> T
    pub fn expect(self, msg: String) -> T          // TRAPS with msg on None
    pub fn map[U](self, f: fn(T) -> U) -> Option[U]
    pub fn and_then[U](self, f: fn(T) -> Option[U]) -> Option[U]
    pub fn filter(self, keep: fn(T) -> bool) -> Option[T]
    pub fn ok_or[E](self, err: E) -> Result[T, E]
}

impl[T, E] Result[T, E] {
    pub fn is_ok(self) -> bool
    pub fn is_err(self) -> bool
    pub fn ok(self) -> Option[T]
    pub fn err(self) -> Option[E]
    pub fn unwrap_or(self, fallback: T) -> T
    pub fn expect(self, msg: String) -> T          // TRAPS with msg on Err
    pub fn map[U](self, f: fn(T) -> U) -> Result[U, E]
    pub fn map_err[F](self, f: fn(E) -> F) -> Result[T, F]
    pub fn and_then[U](self, f: fn(T) -> Result[U, E]) -> Result[U, E]
}
```

There is no `unwrap()`. `expect(msg)` takes the message that says why the writer believed
there was a value, because "the program stopped, and here is what was assumed" is worth
strictly more than "the program stopped".

`ok_or` is the bridge between the two: "there is nothing" becomes "here is why".

## Panics and traps

`panic(msg: String) -> !` terminates the process: it writes `origin: <msg>` plus the
source location to stderr and exits **101**. Every trap in §04's table behaves
identically.

A panic in Origin 0.1 kills the whole process, including every green thread. Per-thread
panic isolation requires unwinding or a supervisor-and-restart model; it is DEFERRED to
Phase 6 and is recorded in `docs/deferred.md` as the largest single deferred item.

A `Result` value that is dropped without being inspected is a warning, not an error:
`unused Result; handle it or bind it to _`.

## Compiler diagnostics

Diagnostics are a specified, tested artifact, not incidental output. Phase 2's exit
criterion — "type errors explain the conflict in plain language, not `cannot unify t17
with t42`" — is enforced by a lint over the diagnostic corpus. <!-- allow-internal-identifier: quoted above as the anti-pattern -->

### Structure

Every diagnostic has: a severity (`error` / `warning`), a stable machine-readable code
(`E0042`), a primary span with a message, zero or more secondary spans with messages,
and zero or more notes or helps. Rendering:

```
error[E0308]: type mismatch in `if` branches
  --> src/main.origin:7:22
   |
 7 |     let x = if c { 1 } else { "no" };
   |                    ^          ---- this branch has type `String`
   |                    |
   |                    this branch has type `i64`
   |
   = note: both branches of an `if` expression must have the same type
   = help: convert one branch, or use a `match` returning a common enum
```

### Rules

1. Every diagnostic MUST have at least one span. A spanless diagnostic is an
   implementation bug and fails `./check`.
2. A diagnostic MUST NOT contain an internal identifier: no inference variable numbers,
   no node ids, no Go type names. `./check` greps the diagnostic corpus for `t[0-9]+`,
   `?[0-9]+`, and `0x` and fails on a hit.
3. Types in messages are printed in **source syntax**, with aliases expanded and the
   alias named: ``expected `Bytes` (= `Vec[u8]`), found `String` ``.
4. Cascading is forbidden. Once an expression is marked `Error`, no further diagnostic
   may be emitted for it or for anything whose type derives from it. The corpus test
   asserts a one-error program produces exactly one error.
5. Codes are permanent. A code is never reused for a different meaning; retired codes
   stay retired. `docs/spec/codes.md` is the registry, generated by `./check`.
6. Errors are reported in source order across the whole compilation, deterministically,
   regardless of parallelism.
7. The compiler exits `0` with no errors, `1` with errors, `101` if the compiler itself
   traps. A compiler trap MUST print `this is a compiler bug` and the phase name.

### Ordering of phases and error suppression

A compilation runs lex → parse → resolve → typecheck → exhaustiveness → lower → optimize
→ codegen. A phase runs only if the previous phase produced no errors, with one
exception: parse always runs after lex, so a file with lexical errors still reports its
syntax errors. This keeps error counts high on a first run without cascading nonsense
from a broken parse into the type checker.

## Worked examples

| Program | Diagnostics |
|---|---|
| `fn f() -> i64 { g()? }` where `g: () -> Result[i64, E]` | REJECTED — `?` in a non-`Result` function, with a `-> Result[i64, E]` help |
| `fn f() -> Result[i64, A] { g()? }` where `g` errs with `B` | REJECTED — mismatched error types `A` vs `B` |
| `fn f() -> Result[i64, E] { Result::Ok(g()?) }` | accepted |
| `let x = if c { 1 } else { "no" };` | one `E0308`, two spans, zero cascades |
| `let x: i32 = 1;` used as `i64` later | one error at the use site, not two |
| `g();` where `g -> Result[..]` | warning: unused `Result` |
| `panic("boom")` | stderr `origin: boom at src/main.origin:3:5`, exit 101 |
| `Option::None.expect("no config")` | stderr `origin: no config at ...`, exit 101 |
| `read_to_string(p).map_err(\|e\| Fault::Io(e))?` | converts the error type explicitly |
| `text.parse_int().ok_or(Fault::Parse(text))` | `Option` becomes `Result` |
| `v.at(99)` on a 3-element `Vec` | stderr `origin: index out of bounds at ...`, exit 101 |
| a program with 5 syntax errors | all 5 reported in one run, source-ordered |
| a program with a syntax error and a type error | only the syntax error (typecheck did not run) |
| a lexical error and a syntax error | both (parse runs after lex) |
| compiler internal panic | `this is a compiler bug` + phase, exit 101 |
