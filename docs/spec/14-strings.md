# 14 — Strings

`String` is immutable UTF-8 text (§03). It is an aggregate with reference semantics (§08),
so passing one copies a reference and never the bytes.

Until Phase 7 a program could write a string literal, compare two strings, order them, and
print one. It could not ask how long one was. This section gives `String` the operations
that make it a type rather than a token, and it does it the way §12 and §13 did: a handful
of operations the compiler provides, under a surface written in Origin.

```origin
let greeting = "hello, world";
io::println(greeting.len().to_str());              // 12
io::println(greeting.slice(0, 5));                 // hello
io::println(greeting.starts_with("hello").to_str()); // true

match "42".parse_int() {
    Option::Some(n) => io::println((n * 2).to_str()),  // 84
    Option::None => io::println("not a number"),
}
```

## Bytes and characters

A `String` is a sequence of bytes that is always valid UTF-8. Two ways of indexing it both
matter, and Origin keeps them visibly distinct rather than picking one:

- **Byte indices.** `len`, `byte_at`, `slice` and `find` are all in bytes. Byte indexing is
  O(1) and is what a parser or a tokenizer wants.
- **Characters.** A *character* is a Unicode scalar value — Origin's `char` (§03). `char_at`
  decodes the one starting at a byte index; `chars` walks them all.

A byte index that would split a character is never allowed to produce anything. `slice` and
`char_at` TRAP on one rather than returning replacement characters or invalid text, because
"a `String` is valid UTF-8" is an invariant the whole language leans on — `==`, ordering,
hashing (§13) and `io::println` all assume it — and an operation that could break it would
make every one of those undefined.

```origin
let s = "héllo";            // 6 bytes: h, 0xC3, 0xA9, l, l, o
io::println(s.len().to_str());          // 6
io::println(s.char_count().to_str());   // 5
io::println(s.slice(0, 1));             // h
io::println(s.char_at(1).to_str());     // é
let bad = s.slice(0, 2);                // TRAPS: string index is not a character boundary
```

Byte index 0 and byte index `len` are always boundaries; `len` is the empty slice's end.

## The `Str` trait

The operations live on a trait the prelude declares and implements for `String`:

```origin
pub trait Str {
    // Provided by the compiler, through `std::str`.
    fn len(self) -> i64;
    fn byte_at(self, i: i64) -> i64;
    fn slice(self, start: i64, end: i64) -> String;
    fn concat(self, other: String) -> String;
    fn char_at(self, i: i64) -> char;
    fn char_width(self, i: i64) -> i64;

    // Written in Origin, as default method bodies over the six above.
    fn is_empty(self) -> bool { ... }
    fn as_string(self) -> String { ... }
    fn char_count(self) -> i64 { ... }
    fn matches_at(self, at: i64, needle: String) -> bool { ... }
    fn starts_with(self, prefix: String) -> bool { ... }
    fn ends_with(self, suffix: String) -> bool { ... }
    fn find(self, needle: String) -> Option[i64] { ... }
    fn contains(self, needle: String) -> bool { ... }
    fn split(self, sep: String) -> List[String] { ... }
    fn chars(self) -> List[char] { ... }
    fn repeat(self, n: i64) -> String { ... }
    fn trim(self) -> String { ... }
    fn trim_start(self) -> String { ... }
    fn trim_end(self) -> String { ... }
    fn parse_int(self) -> Option[i64] { ... }
}
```

`Str` is a trait rather than a set of free functions because `String` is a primitive: there
is no struct to hang an inherent impl on, and `s.len()` is what a program wants to write.
It is implemented for `String` and for nothing else. A generic function may take `T: Str`,
which today means it takes a `String`; the bound is what will let a future string type join
without changing a call site.

### The six the compiler provides

| Operation | Meaning |
|---|---|
| `s.len()` | length in **bytes** |
| `s.byte_at(i)` | the byte at index `i`, as an `i64` in `0..=255` |
| `s.slice(a, b)` | the bytes in `[a, b)`, as a new `String` |
| `s.concat(t)` | `s` followed by `t`, as a new `String` |
| `s.char_at(i)` | the character whose encoding starts at byte `i` |
| `s.char_width(i)` | how many bytes that character occupies: 1, 2, 3 or 4 |

They are compiler-provided for the same reason `Array`'s operations are (§13, ADR-0028):
their bodies need to read and allocate raw bytes, which Origin has no way to say. Everything
else is Origin source in the prelude, and the specification's claim is that the two halves
are indistinguishable from outside.

There is no `+` on strings. `s.concat(t)` is the concatenation, and `+` stays arithmetic
(§04): an operator that means "add" for every other type and "join" for one is exactly the
kind of quiet special case ADR-0005's trapping arithmetic exists to avoid.

### The rest

| Operation | Meaning |
|---|---|
| `s.is_empty()` | `s.len() == 0` |
| `s.as_string()` | `s` as a `String` |
| `s.char_count()` | how many characters, which is `len()` only for ASCII |
| `s.matches_at(i, n)` | whether `n`'s bytes occur at byte index `i` |
| `s.starts_with(p)` | whether `s` begins with the bytes of `p` |
| `s.ends_with(p)` | whether `s` ends with the bytes of `p` |
| `s.find(n)` | the **byte** index where `n` first occurs, or `None` |
| `s.contains(n)` | `s.find(n)` is `Some` |
| `s.split(sep)` | the pieces between occurrences of `sep`, as a `List[String]` |
| `s.chars()` | the characters, as a `List[char]` |
| `s.repeat(n)` | `s` joined to itself `n` times; `n <= 0` gives `""` |
| `s.trim()` | `s` without leading or trailing ASCII whitespace |
| `s.trim_start()`, `s.trim_end()` | one end only |
| `s.parse_int()` | the decimal integer `s` denotes, or `None` |

`find` and `split` search by bytes, not by characters. That is the same answer either way:
UTF-8 is self-synchronizing, so a valid encoding never occurs at a non-boundary inside
another one, and a byte-wise match is therefore always a character-wise match. They search
with `matches_at` rather than by slicing, because a slice whose end fell inside a character
would TRAP, and a search has to be able to ask about a position before it knows whether the
answer is yes.

`as_string` is there because inside a default method body `self` has type `Self`, not
`String` — so `split`, which builds a `List[String]`, and `repeat`, which concatenates, both
need a conversion the trait itself supplies. For `String` it is a copy of the whole thing,
which is what makes it total for a future implementor whose representation is not a
`String` at all.

`split` on the empty separator is `None`-free but degenerate, and is defined: it returns a
one-element list holding `s`. `split` of the empty string on any separator returns a
one-element list holding `""`. Every other case follows the rule "one more piece than
separators found", so `"a,b,".split(",")` is three pieces, the last empty.

`parse_int` accepts an optional leading `-` or `+`, then one or more ASCII digits, and
nothing else — no whitespace, no underscores, no radix prefix. A value that does not fit in
an `i64` is `None`, not a trap: overflow inside `parse_int` is a property of the input, and
the prelude uses `checked_mul` and `checked_add` (§06's `Int`) so it stays one.

## Errors

| Condition | Result |
|---|---|
| `s.byte_at(i)` with `i < 0` or `i >= s.len()` | TRAPS `index out of range` |
| `s.char_at(i)`, `s.char_width(i)` with `i < 0` or `i >= s.len()` | TRAPS `index out of range` |
| `s.slice(a, b)` with `a < 0`, `b > s.len()`, or `a > b` | TRAPS `index out of range` |
| `s.char_at(i)`, `s.char_width(i)` where `i` is not a character boundary | TRAPS `string index is not a character boundary` |
| `s.slice(a, b)` where `a` or `b` is not a character boundary | TRAPS `string index is not a character boundary` |
| `s.repeat(n)` with `n <= 0` | `""` — not a trap |
| `s.find(n)` with no occurrence | `None` — not a trap |
| `s.parse_int()` on anything but a decimal integer | `None` — not a trap |
| `s.parse_int()` on a number too large for `i64` | `None` — not a trap |

Every one is defined. §04 leaves no room for an out-of-range read to return whatever is next
in memory, and there is no operation here that can produce a `String` which is not valid
UTF-8.

The two trap messages are the same on all three engines, and so are the spans they carry: a
trap raised inside a prelude method names the *caller's* line, not the prelude's, which is
the same rule §12's channel operations follow.

## What the compiler provides

`std::str`'s six operations, and nothing else. They are addressed as `str::len(s)` and so
on, but a program is not expected to write them: `impl Str for String` in the prelude is
one line each, and the trait is the surface.

A `String` object is `internal/layout`'s `ByteArray` shape and has been since Phase 2: a
header, a length word in bytes, then the bytes. `slice` and `concat` allocate a new one;
nothing here mutates an existing one, because §03 says `String` is immutable and §08's
moving collector is free to relocate one at any allocation.

`char_at` and `char_width` decode UTF-8 from the bytes at an index. The encoding is the
standard one, and the specification names it here rather than leaving it to an engine
because all three must agree to the byte:

| Lead byte | Width | Scalar |
|---|---|---|
| `0xxxxxxx` | 1 | the byte |
| `110xxxxx` | 2 | 5 bits, then 6 |
| `1110xxxx` | 3 | 4 bits, then 6, then 6 |
| `11110xxx` | 4 | 3 bits, then 6, 6, 6 |
| `10xxxxxx` | — | not a boundary: TRAPS |

A `String` is valid UTF-8 by construction — every one is a literal, a `to_str`, a `slice` at
boundaries, or a `concat` of two valid strings — so a continuation byte at an index is a
boundary error and never a malformed-input error. There is no fifth row.

## Worked examples

| Program | Output |
|---|---|
| `"hello".len()` | `5` |
| `"".is_empty()` | `true` |
| `"héllo".len()` | `6` |
| `"héllo".char_count()` | `5` |
| `"hello".byte_at(1)` | `101` |
| `"hello".slice(1, 4)` | `ell` |
| `"hello".slice(2, 2)` | `` (empty) |
| `"hello".slice(0, 5)` | `hello` |
| `"ab".concat("cd")` | `abcd` |
| `"héllo".char_at(1)` | `é` |
| `"héllo".char_width(1)` | `2` |
| `"hello".starts_with("he")` / `.ends_with("lo")` | `true`, `true` |
| `"hello".find("ll")` | `Some(2)` |
| `"hello".find("z")` | `None` |
| `"a,b,c".split(",")` joined by spaces | `a b c` |
| `"héllo wörld".split(" ")` joined by `\|` | `héllo\|wörld` — a separator search never splits a character |
| `"a,b,".split(",")` length | `3` |
| `"abc".split("")` length | `1` |
| `"ab".repeat(3)` | `ababab` |
| `"ab".repeat(0)` | `` (empty) |
| `"  hi  ".trim()` | `hi` |
| `"42".parse_int()` | `Some(42)` |
| `"-7".parse_int()` | `Some(-7)` |
| `"4x2".parse_int()` | `None` |
| `"99999999999999999999".parse_int()` | `None` — checked, not trapped |
| `"héllo".chars()` printed one per line | `h é l l o` |
| `"hello".byte_at(9)` | TRAPS `index out of range`, exit 101 |
| `"hello".slice(3, 1)` | TRAPS `index out of range`, exit 101 |
| `"héllo".slice(0, 2)` | TRAPS `string index is not a character boundary`, exit 101 |
| `"héllo".char_at(2)` | TRAPS `string index is not a character boundary`, exit 101 |
| a string built by 1000 `concat`s, then `len()` | the same number on all three engines |
| `hash::of(s)` for a sliced and a literal string with the same bytes | equal (§13) |
| a `List[String]` surviving a collection that moves every element | the same strings |

Every row is a case in `tests/e2e/cases/`, asserting exact stdout, stderr and exit status on
all three engines at every optimization level, which is what makes this table normative
rather than aspirational. The last row is a backend test for the same reason §13's is: a
collection that actually moves live strings needs a heap smaller than any shipped program
has.
