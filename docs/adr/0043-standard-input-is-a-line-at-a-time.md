# ADR-0043: Standard input is read a line at a time, with no handle

**Status:** accepted · **Date:** 2026-09-11 · **Decided by:** implementer (user delegated)

## Context

Origin has had no way to read standard input since Phase 0. `docs/deferred.md` recorded it
with the reason: *"Standard input is the one with a design question attached — it is a
stream, and §15 has so far avoided handles entirely (ADR-0030)."*

The question is now forced. The audience is teenagers (ADR-0042), and the second program
anyone writes is the one that asks their name and says hello. A language whose tutorial
cannot contain that program is not a language a fifteen-year-old will keep.

ADR-0030 chose whole-file reads over file handles: `read_to_string(path)` and
`write_string(path, text)`, no `open`, no `close`, no cursor. That choice was possible
because a file has a size and can be read twice. **Standard input has neither property.**
It cannot be seeked, its length is not known until it ends, and the bytes are gone once
consumed. So the handle-free shape does not transfer for free; it has to be re-earned.

## Options considered

- **A `Stdin` handle** — `let h = stdin(); h.read_line()`. Rejected. It is the first
  handle in the language, and one handle is a whole category: something to construct,
  something to pass around, something with a lifetime, and a second answer to "how do I do
  I/O" beside §15's. The cost is not the type, it is the precedent.

- **`read_to_string()` with no argument — the whole of standard input at once.** Tempting,
  because it is literally ADR-0030's shape with the path removed, and it has no size limit
  at all. Rejected as the *primary* operation because it cannot be interactive: a program
  that prints `What is your name? ` and then reads everything blocks until the user signals
  end of input, so the obvious first program does not work. It reads a pipe well and a
  person badly, and the audience is a person.

- **A line at a time, as two compiler-provided operations and a prelude function.**
  Chosen. It is the granularity a person types at, it is the granularity a pipeline's
  producer flushes at, and it needs no new type.

- **Character at a time.** Rejected: every use would rebuild lines out of it, which means
  every program contains the same loop.

## Decision

**One prelude function, over two compiler-provided operations.**

```origin
read_line() -> Option[String]   // the next line, or None at end of input
input() -> String               // the next line, or "" at end of input
ask(prompt: String) -> String   // print the prompt, then input()
```

`io::read_line() -> i64` reads one line and holds it; `io::taken_line() -> String` takes
it. That is the same split `fs::read_file`/`fs::taken_text` uses and it exists for the same
reason (ADR-0025): a compiler-provided operation returns a primitive or a `String`, never a
prelude type, because the native backend cannot construct one.

The line does **not** include its terminator: the runtimes strip a trailing `\n` and the
prelude strips a `\r` before it, so a file typed on Windows reads the same as one typed
anywhere else.

**A line has a maximum length**, `layout.MaxInputLine` = 65,536 bytes, and a longer one is
an error rather than a silent truncation. `internal/layout` owns the number because all
three engines have to refuse the same input (process rule 5).

## Consequences

- **The hello-your-name program is three lines**, and it is the program the tutorial opens
  with:

  ```origin
  let name = ask("What is your name? ")
  println("Hello, \(name)!")
  ```

- **`input()` conflates end of input with an empty line, and `read_line()` does not.**
  That is deliberate, not an oversight: `input()` is the one a beginner reaches for and
  `Option` is the fourth thing that blocks them (ADR-0007, priced in
  `docs/phases/14-complete.md`). A program that must tell the difference — a filter reading
  until its input ends — uses `read_line()` and gets the honest answer. Both exist; neither
  lies about what it returns.

- **A failure traps rather than being returned.** Invalid UTF-8 and an over-long line are
  the two, and they trap with a message naming which. §15 returns `Err` for a file because
  a *missing file* is an ordinary thing a program plans for; standard input being
  unreadable is not, and there is no path to report. This matches `args()`, which traps on
  an argument that is not valid UTF-8 (§17).

- **The native runtime reads one byte per system call.** It has to: a buffered read would
  consume bytes past the newline, and there is no handle to hold them in — the operation is
  stateless by construction. Standard input is not a throughput path, and the alternative
  is runtime-global buffer state that every green thread would have to share.

- **In the browser there is no standard input, so the playground supplies it.** ADR-0033
  made every file operation fail on a host with no filesystem; input is different, because
  a page *can* have text in a box. The playground hands the module the contents of its
  input box and `read_line` reads lines out of it, ending when they run out. A tutorial
  page that cannot run the program it is teaching is worse than no tutorial page.

- **It does not open the door to handles.** `read_line` is a free function with no
  receiver and no state a program can name. If Origin ever wants `stdout` as a value, or a
  file cursor, that is still a separate decision with its own ADR.

- **Reversing it is deleting two builtins and three prelude functions.** Nothing else in
  the language refers to them.
