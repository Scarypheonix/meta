# 18 — Standard input

A program that cannot be told anything at run time can only compute one answer. §17 gave
it the command line; this section gives it the other channel, the one a person types into.

```origin
let name = ask("What is your name? ")
println("Hello, \(name)!")
```

That is the whole program. There is no handle to open, nothing to close, and no import
(ADR-0043, ADR-0041, ADR-0042).

## The three functions

```origin
pub fn read_line() -> Option[String]
pub fn input() -> String
pub fn ask(prompt: String) -> String
```

`read_line()` returns the next line of standard input, or `None` when there is no more
input. `input()` is the same thing with `None` flattened to `""`. `ask(prompt)` prints the
prompt — with no newline after it, so the cursor stays on the same line — and then calls
`input()`.

All three are prelude functions, in scope everywhere with no `use`, exactly as `println`
and `args` are.

**`input()` cannot tell an empty line from the end of input and `read_line()` can.** That
is the trade each one is for. A program that asks a person a question does not care; a
program that reads until its input ends does, and must use `read_line`:

```origin
let mut lines = 0
while let Some(line) = read_line() {
    lines = lines + 1
}
println("\(lines) lines")
```

## What a line is

A line is the bytes up to and including the next `\n`, with the `\n` removed. If a `\r`
comes immediately before it, that is removed too, so text typed on Windows reads the same
as text typed anywhere else.

The final line of the input does not need a terminator. Input ending in `a\nb` is two
lines, `a` and `b`; input ending in `a\nb\n` is the same two lines. Input that is empty is
zero lines, and the first `read_line()` is `None`.

| Standard input | `read_line()` returns, in order |
|---|---|
| *(empty)* | `None` |
| `\n` | `Some("")`, `None` |
| `hi` | `Some("hi")`, `None` |
| `hi\n` | `Some("hi")`, `None` |
| `hi\r\n` | `Some("hi")`, `None` |
| `a\n\nb\n` | `Some("a")`, `Some("")`, `Some("b")`, `None` |

Once a line has been read it is gone: standard input is consumed, not seeked. `read_line`
holds no cursor a program can see, because there is nothing to hold — the position belongs
to the process.

## Two failures, and both trap

| | |
|---|---|
| the line is not valid UTF-8 | TRAPS with `standard input is not valid UTF-8` |
| the line is longer than 65,536 bytes | TRAPS with `a line of standard input is too long` |

A `String` is valid UTF-8 by construction (§14), so there is no `String` to return for the
first, and truncating the second would hand back text that is not what was typed. Both are
traps rather than `Err` for the reason §17 gives for `args()`: a *missing file* is an
ordinary thing a program plans for and can report a path for, and neither of these is.

The maximum line length is `internal/layout`'s, shared by the three engines so that a
program refuses the same input wherever it runs (process rule 5).

## Reading numbers

`parse_int` and `parse_float` are methods on `Str` (§14) and return `Option`, so the
reading and the parsing compose:

```origin
let age = ask("How old are you? ").parse_int()
if let Some(n) = age {
    println("In ten years you will be \(n + 10).")
} else {
    println("That was not a number.")
}
```

## What the compiler provides

Two operations, and the reason is ADR-0025's — a runtime returns a primitive or a `String`,
never a prelude type:

```origin
io::read_line() -> i64        // 0 a line is held, 4 end of input, 5 too long, 3 it failed
io::taken_line() -> String    // the held line, without its terminator
```

The status codes are `internal/compile`'s, the same set §15's file operations use, with
`IOEndOfInput` (4) and `IOTooLong` (5) added for the two conditions a file read does not
have. They are two statuses rather than one because the two traps say different things and
a status that could mean either could not choose. The held line travels the way a file's
text does: on the running thread, until the next call takes it — the *same* slot, in fact,
so `io::taken_line` and `fs::taken_text` are one operation under two names.

`read_line`, `input` and `ask` are ordinary Origin in the prelude over those two, which is
where the UTF-8 check and the `\r` stripping live — one implementation rather than the same
rule written three times and agreeing by luck.

## Where the bytes come from

On the interpreter and the virtual machine, standard input is the driver's own: `originc
run prog.origin < data` reads `data`.

In native code it is file descriptor 0, read **one byte per `read` system call**. That is
not an oversight. A buffered read consumes bytes past the newline, and a stateless
operation has nowhere to keep them; buffering would mean runtime-global state shared by
every green thread. Standard input is not a throughput path, and a program that wants one
reads a file (§15).

In the browser there is no standard input at all, so **the playground supplies it**: the
page has an input box, its contents are handed to the module with the program, and
`read_line` walks them a line at a time and then reports the end. This is the one place
ADR-0033's "a host with no files fails every file operation" does not extend, and the
reason is that a page can have text in a box and cannot have a filesystem.
