# The Origin Language

Origin is a small, dynamically typed scripting language. If you know a little
JavaScript or Python, you can read Origin already. This page is the whole
language — there is nothing hidden past the end of it.

## 1. Shape of a program

A program is a list of statements. There are no semicolons; a newline ends a
statement. Blocks are always wrapped in `{ }`.

```origin
# comments run from '#' to the end of the line

let name = "world"
print("hello, " + name)
```

## 2. Values

Origin has six types. `type(v)` tells you which one you have.

| Type   | Examples                          | Notes                                   |
| ------ | --------------------------------- | --------------------------------------- |
| `num`  | `42`, `-7`, `3.14`                | one number type; `6 / 3` gives `2`      |
| `str`  | `"hi"`, `"a\nb"`                  | double quotes only; `\n \t \" \\` escape |
| `bool` | `true`, `false`                   |                                          |
| `nil`  | `nil`                             | "no value"; the default return value    |
| `list` | `[1, "two", [3]]`                 | ordered, mixed types, grows with `push` |
| `map`  | `{"a": 1, "b": 2}`                | keys are `str` or `num`                 |

Only `false` and `nil` are falsy. `0` and `""` are **true** — this is a
deliberate choice so that `if count` never surprises you.

## 3. Variables

`let` introduces a name. Plain `=` reassigns one that already exists.

```origin
let count = 0
count = count + 1     # fine
total = 1             # error: cannot assign to undefined name 'total'; use 'let'
```

Assigning to a name that was never declared is an error, so a typo can't
silently create a new variable.

Scope is lexical, and every block gets its own scope:

```origin
let x = 1
if true {
    let x = 2         # a different x, living only inside the block
}
print(x)              # 1
```

## 4. Operators

From loosest to tightest binding:

```
or
and
==  !=
<   >   <=  >=
+   -
*   /   %
not x   -x          (unary)
f(x)  a[i]  a.k     (call, index, field)
```

- `+` adds nums, joins strs, and concatenates lists. Mixing types is an error:
  write `"n = " + str(n)`, not `"n = " + n`.
- `/` on two nums that divide evenly gives a whole num, otherwise a decimal.
- `==` compares by value, and values of different types are never equal
  (`1 == "1"` is `false`).
- `and` and `or` short-circuit and return one of their operands, so
  `let port = given or 8080` works as a default.

## 5. Control flow

```origin
if score >= 90 {
    print("A")
} else if score >= 80 {
    print("B")
} else {
    print("C")
}

while n > 1 {
    n = n - 1
}

for item in ["a", "b"] { print(item) }   # over a list
for ch in "hi"          { print(ch) }    # over a string's characters
for key in user         { print(key) }   # over a map's keys
```

Parentheses around the condition are optional and usually omitted. `break`
leaves the nearest loop; `continue` jumps to its next round. `range(n)`,
`range(start, stop)` and `range(start, stop, step)` build the list to count
over.

## 6. Functions

```origin
fn add(a, b) {
    return a + b
}
```

A function with no `return` gives back `nil`. Functions are ordinary values:
assign them, pass them, return them.

```origin
let double = fn(x) { return x * 2 }        # anonymous function

fn apply_twice(f, x) { return f(f(x)) }
print(apply_twice(double, 5))              # 20
```

Functions close over the scope they were written in, which is how you get
private state:

```origin
fn counter() {
    let n = 0
    return fn() {
        n = n + 1
        return n
    }
}
let next = counter()
print(next(), next())                      # 1 2
```

Calling with the wrong number of arguments is an error — Origin does not pad
missing arguments with `nil`.

## 7. Lists and maps

```origin
let xs = [3, 1, 2]
print(xs[0], xs[-1])       # 3 2   -- negative indexes count from the end
xs[0] = 99
push(xs, 4)                # append
let last = pop(xs)         # remove and return the last item

let user = {"name": "ada", "age": 36}
print(user["name"], user.name)     # both read a key
user.age = 37                      # both write one
user["city"] = "london"            # a new key is created on assignment
```

Reading a key that does not exist is an error, not `nil` — check first with
`has(user, "email")`. Indexing past the end of a list is an error too.

## 8. Built-in functions

| Function | Meaning |
| --- | --- |
| `print(a, ...)` | write the values, space separated, then a newline |
| `input(prompt?)` | read one line from the terminal as a `str` |
| `len(v)` | length of a `str`, `list` or `map` |
| `str(v)` / `num(v)` | convert to text / parse text as a number |
| `type(v)` | `"num"`, `"str"`, `"bool"`, `"nil"`, `"list"`, `"map"` or `"fn"` |
| `push(list, v)` / `pop(list)` | append / remove-and-return the last item |
| `keys(map)` / `has(map, k)` | list of keys / whether a key exists |
| `range(stop)` … `range(start, stop, step)` | build a list of nums |
| `split(str, sep)` / `join(list, sep)` | text to list / list to text |

## 9. Errors

Every error names the line and says what went wrong:

```
origin: cannot add a num and a str (line 12)
```

There is no `try`/`catch` yet — an error stops the program. That is the first
thing worth adding when you extend the language.

## 10. Grammar

```
program     = statement*
statement   = "let" NAME "=" expression
            | "fn" NAME params block
            | "if" expression block ("else" (block | ifStatement))?
            | "while" expression block
            | "for" NAME "in" expression block
            | "return" expression?
            | "break" | "continue"
            | expression ("=" expression)?

block       = "{" statement* "}"
params      = "(" (NAME ("," NAME)*)? ")"

expression  = or
or          = and ("or" and)*
and         = equality ("and" equality)*
equality    = comparison (("==" | "!=") comparison)*
comparison  = additive (("<" | ">" | "<=" | ">=") additive)*
additive    = multiplicative (("+" | "-") multiplicative)*
multiplicative = unary (("*" | "/" | "%") unary)*
unary       = ("-" | "not") unary | postfix
postfix     = primary (call | "[" expression "]" | "." NAME)*
call        = "(" (expression ("," expression)*)? ")"
primary     = NUMBER | STRING | "true" | "false" | "nil" | NAME
            | "fn" params block
            | "(" expression ")"
            | "[" (expression ("," expression)*)? "]"
            | "{" (expression ":" expression ("," ...)*)? "}"
```
