# 01 — Lexical Structure

## Source encoding

A source file MUST be well-formed UTF-8. A file that is not well-formed UTF-8 is
REJECTED with a diagnostic naming the byte offset of the first invalid sequence. A
leading U+FEFF byte-order mark is skipped if present; a U+FEFF anywhere else is
REJECTED.

Source files use the extension `.origin`.

## Positions and spans

Every token carries a `Span`: a half-open byte range `[start, end)` into the source
file, plus the file identity. Line and column are derived, never stored:

- **Line** is 1-based, counting `\n`. A `\r\n` pair counts as one line terminator; a
  lone `\r` is whitespace and does NOT terminate a line.
- **Column** is 1-based and counted in **Unicode scalar values**, not bytes and not
  grapheme clusters. A tab advances the column by exactly 1.

Every diagnostic MUST carry at least one span. A diagnostic with no span is an
implementation bug (§09).

## Whitespace and comments

Whitespace is U+0009, U+000A, U+000B, U+000C, U+000D, U+0020. A newline is whitespace
everywhere except for the one rule below: it may cause a semicolon to be inserted. Nothing
else about the language depends on where a line ends, and indentation is never significant.

```
LineComment  = "//" { any character except "\n" } ;
BlockComment = "/*" { BlockComment | any character } "*/" ;
```

Block comments nest. An unterminated block comment is REJECTED with a span pointing at
the opening `/*` of the outermost unterminated comment.

Doc comments (`///` before an item) are lexed as ordinary line comments in 0.1 and
attached to nothing. DEFERRED: doc comment capture (Phase 8, for the LSP hover).

## Statement terminators

A semicolon is **inserted** at a line break when all three of the following hold
(ADR-0040). The token it inserts is an ordinary `;`: the grammar in §02 is unchanged, and
the parser cannot tell an inserted semicolon from a written one.

1. The last token of the line is one that can end a statement:

   ```
   Ident   IntLit   FloatLit   StringLit   CharLit
   true    false    self       )           ]
   break   continue return
   ```

   **`}` is deliberately not in this set** — see below.

2. The lexer is not inside an unclosed `(` or `[`. A call, a type argument list or a list
   literal may therefore be split across lines freely, with or without a trailing comma.

3. The next token that is not whitespace or a comment is not `}`.

Written semicolons remain legal wherever they are legal today; insertion only ever supplies
one where the source could have written it.

### Why `}` does not terminate a line

Two constructs would break if it did, and both are consequences of Origin being
expression-oriented rather than statement-oriented:

- **A block's value is its trailing expression, written without a semicolon** (§02). Rule 3
  is what protects it: in `fn f() -> u64 { 1u64 << w }` the line `1u64 << w` ends in a
  trigger token, but the next token is `}`, so nothing is inserted and the expression stays
  the block's value. Without rule 3 every value-returning function in the language would
  quietly begin returning `()`.
- **A brace-bodied `match` arm may omit its comma.** If `}` terminated a line, a semicolon
  would land between two arms, where the grammar allows only a comma. The same exclusion is
  what lets `}` and `else` sit on separate lines.

The cost of the exclusion is that a statement ending in a brace needs its own semicolon when
one is required: `let p = Point { x: 1 };` keeps it. A block *expression* used as a
statement never needed one (§02's `ExprStmt`), so `if c { a }` on its own line is unaffected.

### Continuing an expression across lines

An expression that wraps must not leave a trigger token at the end of a line. Put the
operator there instead:

```origin
let big = a_long_condition ||
    another_condition;          // fine: the line ends in `||`

let big = a_long_condition      // REJECTED: a semicolon is inserted here
    || another_condition;
```

Inside `(` or `[` this does not apply, by rule 2. There is no line-continuation escape.

### Worked examples

| Source (two lines) | Inserted? | Why |
|---|---|---|
| `let x = 1` then `let y = 2` | yes | ends in a literal, next token is `let` |
| `use std::io` then `fn main() {` | yes | ends in an identifier |
| `1u64 << w` then `}` | **no** | rule 3: the block's value |
| `Point { x: 1 }` then `let y = 2` | **no** | `}` is not a trigger |
| `foo(a,` then `b)` | no | ends in `,`, and rule 2 applies |
| `xs.get(0)` then `.unwrap_or(3)` | yes — REJECTED | move `.` to the previous line |
| `x` then `+ y` | yes — REJECTED | move `+` to the previous line |
| `f(x` then `+ y)` | no | rule 2: inside `(` |
| `A => { g() }` then `B => 1,` | **no** | `}` is not a trigger; arms stay comma-separated |
| `return` then `}` | no | rule 3 |
| `break` then `let x = 1` | yes | `break` ends a statement |

## Keywords

Reserved, and never usable as identifiers:

```
as      break    const    continue else    enum    false   fn
for     if       impl     in       let     loop    match   mut
pub     return   self     Self     struct  trait   true    type
use     where    while
```

Reserved for future use, REJECTED as identifiers with a "reserved word" diagnostic:

```
async   await    box      dyn      extern  macro   move    ref
static  super    unsafe   yield
```

## Identifiers

```
Ident      = IdentStart { IdentContinue } ;
IdentStart = "_" | XID_Start ;
IdentContinue = "_" | XID_Continue ;
```

`_` alone is not an identifier; it is the wildcard token. Identifiers are compared by
exact code point sequence — no NFC normalization is applied, so two visually identical
identifiers with different encodings are different identifiers. An identifier
containing a character with `XID_Start`/`XID_Continue` outside ASCII is accepted; the
formatter (Phase 8) MUST NOT rewrite it.

**Naming convention** (enforced by the linter, not the compiler): types, traits and
enum variants are `UpperCamelCase`; functions, methods, fields, bindings and modules are
`lower_snake_case`; constants are `SCREAMING_SNAKE_CASE`.

## Literals

### Integer literals

```
IntLit    = ( DecLit | HexLit | OctLit | BinLit ) [ IntSuffix ] ;
DecLit    = DecDigit { DecDigit | "_" } ;
HexLit    = "0x" HexDigit { HexDigit | "_" } ;
OctLit    = "0o" OctDigit { OctDigit | "_" } ;
BinLit    = "0b" BinDigit { BinDigit | "_" } ;
IntSuffix = "i8" | "i16" | "i32" | "i64" | "u8" | "u16" | "u32" | "u64" ;
```

An underscore MUST NOT be the first character after the base prefix and MUST NOT be the
final character. A literal with no suffix has its type inferred; if inference leaves it
unconstrained it defaults to `i64` (§03). A literal whose value does not fit its
resolved type is REJECTED at compile time — this includes `let x: u8 = 256;` and
`-128i8` (which is negation applied to `128i8`, and `128` does not fit `i8`; write
`i8::MIN` instead).

### Float literals

```
FloatLit  = DecLit "." DecLit [ Exp ] [ FloatSuffix ]
          | DecLit Exp [ FloatSuffix ] ;
Exp       = ( "e" | "E" ) [ "+" | "-" ] DecDigit { DecDigit | "_" } ;
FloatSuffix = "f32" | "f64" ;
```

`1.` and `.5` are REJECTED: a float literal MUST have digits on both sides of the
point. Unsuffixed float literals default to `f64`. Literals are rounded to the target
type using round-to-nearest-ties-to-even; a literal that rounds to infinity is
REJECTED.

### Boolean and character literals

`true` and `false` are keywords of type `bool`.

```
CharLit = "'" ( Escape | any scalar value except "'" or "\\" ) "'" ;
```

A `char` is a Unicode scalar value: `0..=0x10FFFF` excluding the surrogate range
`0xD800..=0xDFFF`. `'\u{D800}'` is REJECTED.

Unlike a string literal, a character literal must be closed on the line it opens: a
literal newline before the closing `'` is REJECTED as an unterminated character literal.
One scalar value never needs a line break, and an apostrophe is common enough in ordinary
text that swallowing the rest of the file for one helps nobody.

### String literals

```
StringLit = '"' { Escape | Interpolation | any scalar value except '"' or "\\" } '"' ;
```

String literals are `String` values (§08: heap-allocated, immutable, UTF-8). A literal
may span lines; the newline is part of the value, exactly as `\n` would be. There are no
raw strings. Only the end of the file leaves a literal unterminated.

### Escapes

```
Escape = "\\" ( "n" | "r" | "t" | "0" | "\\" | "'" | '"'
              | "x" HexDigit HexDigit
              | "u" "{" HexDigit { HexDigit } "}" ) ;
```

`\xNN` denotes a scalar value in `0x00..=0x7F` only; `\x80` or above is REJECTED
(it would be ambiguous between a byte and a scalar value). `\u{...}` accepts 1 to 6 hex
digits and MUST denote a Unicode scalar value. Any other escape is REJECTED with a
diagnostic naming the offending character.

### Interpolation

```
Interpolation = "\\" "(" Expr ")" ;
```

A string literal may embed an expression, which is rendered with `to_str` and joined to
the text around it. What that means is §14's; what it *is* is lexical, and the escape is
where it lives for a reason: `\(` was a rejected escape before this existed, so no literal
that used to be valid changes meaning, and there is no doubling rule to remember — `{`,
`}` and `$` are ordinary characters in a string.

```origin
let name = "world";
io::println("hello, \(name)!");        // hello, world!
io::println("\(1 + 2) items");         // 3 items
io::println("nested: \("inner \(1)")"); // nested: inner 1
```

The expression runs to the `)` that matches its own `(`, counted in **tokens**: a `)`
inside a nested string literal or a character literal closes nothing. An interpolation
holds exactly one expression; `\()` and `\(1 2)` are REJECTED.

## Punctuation and operators

```
( ) { } [ ] , ; : :: . -> => _ @
+ - * / % & | ^ ! << >>
= += -= *= /= %=
== != < <= > >=
&& || ?
```

The lexer uses maximal munch: `>>` lexes as one shift token, never two closes. Because
type application uses `[...]` (ADR-0013), Origin has no `>>` ambiguity to resolve and
the lexer never needs parser feedback.

## Lexer error recovery

The lexer MUST NOT stop at the first error. On encountering an invalid character,
unterminated literal, or malformed escape it MUST emit a diagnostic, skip to a
plausible resynchronization point (end of line for unterminated string/char literals;
one scalar value for an invalid character), and continue. A single `origin check` run
MUST be able to report every lexical error in a file. The fuzz target
`tests/fuzz/lex` asserts that no input causes a panic, a hang, or a token with an
out-of-range span.

## Worked examples

| Input | Tokens (kind:text) | Note |
|---|---|---|
| `1_000` | `IntLit:1_000` | value 1000 |
| `0xFF_u8` | REJECTED | `_` immediately before suffix is not allowed; write `0xFFu8` |
| `0b1010i32` | `IntLit:0b1010i32` | value 10, type `i32` |
| `1.5e-3` | `FloatLit:1.5e-3` | type `f64` |
| `1.` | REJECTED | digits required after `.` |
| `a>>b` | `Ident:a` `Shr:>>` `Ident:b` | maximal munch |
| `Map[K, V]` | `Ident:Map` `LBracket` `Ident:K` `Comma` `Ident:V` `RBracket` | type application |
| `/* /* */ */` | (no tokens) | comments nest |
| `/* unterminated` | REJECTED | span points at the outermost `/*` |
| `'\u{1F600}'` | `CharLit` | valid scalar value |
| `'\u{D800}'` | REJECTED | surrogate |
| `"line\nbreak"` | `StringLit` | 10 scalar values |
| a `"` , a newline, a `"` | `StringLit` | one scalar value: a literal may span lines |
| `'` , a newline, `'` | REJECTED | a character literal may not |
