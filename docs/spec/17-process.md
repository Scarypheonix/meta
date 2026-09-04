# 17 — The process

A compiler is told which file to compile and reports whether it succeeded. Neither is
expressible in Origin 0.1: a program has no way to read its arguments and no way to choose
its exit status. This section gives it both, and nothing else.

```origin
use std::process;

fn main() {
    let args = args();
    if args.len() != 2 {
        io::println("usage: \(args.at(0)) <file>");
        process::exit(2);
    }
    match read_to_string(args.at(1)) {
        Result::Ok(text) => io::println("\(text.len()) bytes"),
        Result::Err(e) => {
            io::println("cannot read it: \(e.to_str())");
            process::exit(1);
        }
    }
}
```

## Arguments

```origin
pub fn args() -> List[String]
```

`args()` is a prelude function, in scope everywhere without a `use`, for the same reason
`read_to_string` is: the prelude is Origin source and Origin source cannot declare a
module, so a name that wants to be `env::args` has to be either a compiler builtin or a
free function. It is the whole command line, **including the program's own name at index 0**,
in the order the process was given them. It is a fresh `List` each call: the arguments are
the process's, and a program that sorts or truncates the list it got has changed nothing
for the next caller.

An argument is a `String`, so it is valid UTF-8 (§14). `args()` checks, and TRAPS with
`argument is not valid UTF-8` on one that is not — not silently replaced, and not returned
as something that claims to be text and is not. The check is in the prelude rather than in
each runtime, which is where §15 already puts the same check for a file's bytes.

The list is never empty: index 0 exists even when it is empty text, because a process
always has an argument vector and the kernel always writes its length.

## Exit status

```origin
pub fn exit(code: i64) -> !
```

`process::exit` ends the process immediately with `code & 0xFF` as its status. It does not
return — its type is `!` (§03), so the code after it is unreachable and the checker knows
it.

It ends the **process**, not the thread: every green thread stops where it is, exactly as
`panic` does (§09, ADR-0026). Nothing is flushed that was not already written, because
`io::print` writes through (§11) and there is nothing buffered to lose.

The three statuses a program produces without asking are unchanged: `0` when `main`
returns, `101` on a trap or a `panic` (§09), and whatever the kernel reports for a signal.
`exit` is how a program says anything else.

| Expression | Status |
|---|---|
| `main` returns | `0` |
| `process::exit(0)` | `0` |
| `process::exit(2)` | `2` |
| `process::exit(-1)` | `255` — the low byte, which is all a status has |
| `process::exit(256)` | `0` — the same rule |
| `panic("x")` | `101` |

## What the compiler provides

Three operations, and the reason is ADR-0025's: a runtime returns a primitive or a
`String`, never a prelude type, because the native backend has no way to construct one.

```origin
env::arg_count() -> i64          // how many arguments the process was given
env::arg_at(i: i64) -> String    // one of them; TRAPS out of range
process::exit(code: i64) -> !
```

`args()` is Origin in the prelude over the first two, and is where the UTF-8 check happens.
`exit` is the one that has to be provided: a program cannot end a process by computing
anything.

## Where they come from

On the interpreter and the virtual machine, the arguments are the ones the driver was
given after the program's path — `originc run prog.origin a b c` passes `a b c`, with
`prog.origin` at index 0 so that a program sees the same shape wherever it runs.

In native code they are the kernel's own: `_start` receives a stack whose top word is the
count and whose following words are the pointers, and the runtime keeps that address
before it aligns the stack away. There is no libc to have parsed them first.
