# Origin

A small scripting language, and an interpreter for it that runs anywhere
Python 3.8+ runs. Read [LANGUAGE.md](LANGUAGE.md) for the syntax;
this page is about getting it working on your machine.

```origin
fn greet(who) {
    return "hello, " + who
}

for name in ["world", "Origin"] {
    print(greet(name))
}
```

## Requirements

Python 3.8 or newer, and nothing else — no packages to install.

```bash
python3 --version
```

## Run a program

Put your code in a file ending in `.ori` and run it:

```bash
python3 origin.py examples/01_basics.ori
```

Or use the wrapper script, which does the same thing:

```bash
./ori examples/01_basics.ori
```

## Use the REPL

Run it with no file to get an interactive prompt. Expressions print their
value; a line ending in an unclosed `{` keeps reading until you close it.

```
$ ./ori
Origin REPL -- type an expression, or Ctrl-D to leave.
ori> 2 + 3 * 4
14
ori> let xs = [1, 2, 3]
ori> for x in xs { print(x * x) }
1
4
9
ori> ^D
```

## Run `ori` from anywhere

Add this directory to your `PATH` so you can type `ori` in any folder:

```bash
# bash
echo 'export PATH="$PATH:'"$PWD"'"' >> ~/.bashrc && source ~/.bashrc
# zsh
echo 'export PATH="$PATH:'"$PWD"'"' >> ~/.zshrc && source ~/.zshrc
```

On Windows, use `python origin.py program.ori` (the `ori` shell script needs
WSL or Git Bash).

You can also make a program self-running on macOS/Linux by starting it with a
shebang and marking it executable:

```origin
#!/usr/bin/env ori
print("runs as ./hello.ori")
```

```bash
chmod +x hello.ori && ./hello.ori
```

(The `#!` line is a comment to Origin, so it is simply ignored.)

## Layout

```
origin.py          the whole interpreter: lexer, parser, evaluator, REPL
ori                shell wrapper so you can type ./ori instead
LANGUAGE.md        the language reference
examples/*.ori     runnable programs, in the order worth reading them
tests/             the interpreter's test suite
```

## Tests

```bash
python3 tests/test_origin.py
```

19 tests cover the expression grammar, control flow, closures, the built-ins,
error messages, and every example program.

## How the interpreter works

`origin.py` is a straightforward tree-walking interpreter in four stages, and
it is meant to be edited:

1. **`tokenize(src)`** turns text into a flat list of tokens, tracking line
   numbers so errors can point at them.
2. **`Parser`** builds a syntax tree by precedence climbing — one method per
   precedence level (`or_expr` → `and_expr` → `equality` → … → `primary`).
   Nodes are plain tuples, `(kind, line, ...payload)`.
3. **`Interpreter.execute` / `.eval`** walk that tree. `Env` is a chain of
   scopes; a `Function` holds its defining `Env`, which is what makes closures
   work.
4. **`main`** runs a file or starts the REPL.

To add a built-in function, add one entry to the `table` list in
`install_builtins`. To add syntax, add a token in `tokenize`, a case in
`Parser.statement` or the precedence chain, and a case in `execute`/`eval`.
Then add a test.

## Where to take it next

Good next features, roughly easiest first: string interpolation, `elif`-free
`match`, a `try`/`catch` for the errors in §9 of the reference, an import
system for multi-file programs, and a standard library module for math and
file I/O.
