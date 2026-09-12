# 10 — Worked Examples

Every example below is normative: the program text, its exact stdout, its exact stderr,
and its exit status. As each phase lands, these become files in `tests/e2e/cases/` with
`.out`, `.err` and `.exit` companions, and the harness asserts byte equality. An example
here that the implementation cannot reproduce is a bug in the implementation or a bug in
this document — never a reason to edit the expected output.

`src/main.origin` is assumed throughout; column numbers in expected stderr refer to it.

Every program here is written the way Origin is written now: no `use std::io`, because
printing needs no import (ADR-0041); no `fn main` wrapper, because a file's top-level
statements *are* `main` (ADR-0042); `Some`, `Ok` and `Less` unqualified (ADR-0037); `[a, b]`
for a list (ADR-0039); and a semicolon only where a line break does not end the statement
(ADR-0040) — after a value that ends in `}` or `?`, which are not statement terminators.

---

## 1. Hello

```origin
println("Hello, Origin.")
```

stdout: `Hello, Origin.\n` · stderr: empty · exit: `0`

## 2. Recursive fibonacci

```origin
fn fib(n: i64) -> i64 {
    if n < 2 { n } else { fib(n - 1) + fib(n - 2) }
}

for i in range(0, 11) {
    println(fib(i).to_str())
}
```

stdout: `0\n1\n1\n2\n3\n5\n8\n13\n21\n34\n55\n` · exit: `0`

## 3. Closure counter (capture-by-value, shared cell)

Demonstrates §04's capture rule: the lambda captures `cell` by value, and `cell` is a
reference to a heap object, so the mutation is shared. Two counters do not interfere.

```origin
struct Cell { mut value: i64 }

fn make_counter() -> fn() -> i64 {
    // A `let` whose value ends in `}` keeps its semicolon: `}` is not a statement
    // terminator, because a block's last expression is its value (ADR-0040).
    let cell = Cell { value: 0 };
    || { cell.value = cell.value + 1; cell.value }
}

let c = make_counter()
let d = make_counter()
println(c().to_str())
println(c().to_str())
println(d().to_str())
```

stdout: `1\n2\n1\n` · exit: `0`

## 4. Recursive enum (linked list)

No `Box` is needed: enum payloads are references (§08).

```origin
enum Chain { Nil, Cons(i64, Chain) }

fn total(c: Chain) -> i64 {
    match c {
        Chain::Nil => 0,
        Chain::Cons(head, tail) => head + total(tail),
    }
}

let c = Chain::Cons(1, Chain::Cons(2, Chain::Cons(3, Chain::Nil)))
println(total(c).to_str())
```

stdout: `6\n` · exit: `0`

## 5. Mutual recursion

```origin
fn is_even(n: i64) -> bool { if n == 0 { true } else { is_odd(n - 1) } }
fn is_odd(n: i64) -> bool { if n == 0 { false } else { is_even(n - 1) } }

println(is_even(10).to_str())
println(is_odd(10).to_str())
```

stdout: `true\nfalse\n` · exit: `0`

## 6. Generics with a trait bound

```origin
fn max2[T: Ord](a: T, b: T) -> T {
    match a.cmp(b) {
        Less => b,
        Equal => a,
        Greater => a,
    }
}

println(max2(3, 7).to_str())
println(max2("apple", "banana"))
```

stdout: `7\nbanana\n` · exit: `0`

Monomorphization emits two copies of `max2`: one at `i64`, one at `String`.

## 7. Integer overflow traps

```origin
let x = 9223372036854775807
println("before")
let y = x + 1
println(y.to_str())
```

stdout: `before\n` · stderr: `origin: arithmetic overflow at src/main.origin:3:9\n`
· exit: `101`

Identical at `-O0`, `-O1` and `-O2`. An optimizer that constant-folds `x + 1` must fold
it to the trap, not to a wrapped value.

## 8. Explicit wrapping

```origin
let x = 9223372036854775807
println(x.wrapping_add(1).to_str())
println((300 as u8).to_str())
println(((-7) / 2).to_str())
println(((-7) % 2).to_str())
```

stdout: `-9223372036854775808\n44\n-3\n-1\n` · exit: `0`

## 9. `Result` and `?`

```origin
enum ParseError { Empty, BadDigit(char) }

fn parse_digit(c: char) -> Result[i64, ParseError] {
    if c >= '0' && c <= '9' {
        Ok(c as u32 as i64 - 48)
    } else {
        Err(ParseError::BadDigit(c))
    }
}

fn parse_two(a: char, b: char) -> Result[i64, ParseError] {
    // `?` ends a statement, so neither of these needs a semicolon (ADR-0040, amended in
    // Phase 15). The last line has none either, and that is what makes it the value.
    let x = parse_digit(a)?
    let y = parse_digit(b)?
    Ok(x * 10 + y)
}

match parse_two('4', '2') {
    Ok(n) => println(n.to_str()),
    Err(_) => println("error"),
}
match parse_two('4', 'z') {
    Ok(n) => println(n.to_str()),
    Err(ParseError::BadDigit(_)) => println("bad digit"),
    Err(ParseError::Empty) => println("empty"),
}
```

stdout: `42\nbad digit\n` · exit: `0`

## 10. Mutation through an alias

```origin
struct Counter { mut n: i64 }

fn bump(c: Counter) { c.n = c.n + 1 }

let a = Counter { n: 0 };
let b = a
bump(b)
bump(a)
println(a.n.to_str())
println(ref_eq(a, b).to_str())
println((Counter { n: 2 } == a).to_str())
println(ref_eq(Counter { n: 2 }, a).to_str())
```

stdout: `2\ntrue\ntrue\nfalse\n` · exit: `0`

## 11. `for` over an iterator

```origin
struct Counter { mut n: i64 }

impl IntoIterator for Counter {
    type Item = i64
    type Iter = Counter
    fn into_iter(self) -> Counter { self }
}

impl Iterator for Counter {
    type Item = i64
    fn next(mut self) -> Option[i64] {
        if self.n > 0 {
            self.n = self.n - 1
            Some(self.n)
        } else {
            None
        }
    }
}

let mut total = 0
for v in (Counter { n: 5 }) {
    total = total + v
}
println(total.to_str())

let mut count = 0
for v in (Counter { n: 10 }) {
    if v == 6 {
        break
    }
    count = count + 1
}
println(count.to_str())

let mut odds = 0
for v in (Counter { n: 6 }) {
    if v % 2 == 0 {
        continue
    }
    odds = odds + 1
}
println(odds.to_str())

let mut empty_runs = 0
for v in (Counter { n: 0 }) {
    empty_runs = empty_runs + v
}
println(empty_runs.to_str())

for v in (Counter { n: 3 }) {
    println(v.to_str())
}
```

stdout: `10\n` · exit: `0` — `range` is half-open, so this sums 1..=4.

This example spent Phases 7 to 12 not compiling: it was written against a `std::iter` module
that was never built, and nothing checked it. `range` is a prelude function now (§13), so it
needs no `use` at all, and the program is `tests/e2e/cases/range_and_iteration.origin` so
that the next divergence fails a test instead of sitting in the specification.

## 12. Green threads and channels *(Phase 6)*

```origin
use std::thread;

let h = thread::spawn(|| -> i64 { 40 + 2 })
println(h.join().to_str())

// Joining in a fixed order makes the output independent of scheduling, which is what
// lets this be a differential case at all (spec/12-concurrency.md).
let a = thread::spawn(|| -> i64 { 1 })
let b = thread::spawn(|| -> i64 { 2 })
let c = thread::spawn(|| -> i64 { 3 })
println((a.join() + b.join() + c.join()).to_str())
```

stdout: `6\n` · exit: `0`

The lambda captures `tx: Sender[i64]` and `n: i64`, both `Send`, so the lambda is `Send`
and may be spawned (§08).

## 13. A discarded allocation loop is reclaimed

```origin
struct Pair { a: i64, b: i64 }

let mut last = Pair { a: 0, b: 0 };
let mut i: i64 = 0
while i < 5000000 {
    last = Pair { a: i, b: i };
    i = i + 1
}
println(last.a.to_str())
println(last.b.to_str())
```

stdout: `4999999\n4999999\n` · exit: `0`

Every iteration but the last allocates a `Pair` that becomes unreachable the moment the
next one is assigned, so the loop's total allocation (~120 MB of `Pair`s) is many times
over any one collector's heap, and only the final `Pair` is ever live. A collector that
leaks would exhaust its heap and TRAP with `out of memory` before `i` reaches 5000000; one
that collects a live object would corrupt `last` or crash. Getting `4999999` twice is
therefore a collection stress test in its own right (spec/08-memory-model.md's
worked-examples table), landing here as a normal program rather than a `tests/gc/`-only
property test because every engine — including native, whose own single-space
stop-the-world collector (ADR-0022) this is what actually exercises — must reclaim
correctly to reach this output at all. Both fields read `i` rather than `i` and `i + 1`
deliberately: the latter trips a pre-existing `-O2` fixed-point bug unrelated to
collection at all (`docs/deferred.md`), and this example's job is the collector, not that
bug.

---

## 17. A word-frequency report *(Phase 7)*

```origin
use std::map;

// A file is read, split into words, counted in a `Map`, ranked, and reported with
// interpolation. Every part of it needed something the language did not have when Phase 7
// began -- and the sort is the prelude's now (§13), not this program's.

fn count_words(text: String) -> Map[String, i64] {
    let counts = map::new[String, i64]()
    for line in text.split("\n") {
        for word in line.split(" ") {
            let w = word.trim()
            if !w.is_empty() {
                counts.insert(w, count_of(counts, w) + 1);
            }
        }
    }
    counts
}

fn count_of(counts: Map[String, i64], word: String) -> i64 {
    match counts.get(word) {
        Some(n) => n,
        None => 0,
    }
}

/// The keys, most frequent first and ties broken by the word itself, so the report does not
/// depend on insertion order.
fn ranked(counts: Map[String, i64]) -> List[String] {
    counts.keys().sort_by(|a, b| beats(counts, a, b))
}

fn beats(counts: Map[String, i64], a: String, b: String) -> bool {
    let ca = count_of(counts, a)
    let cb = count_of(counts, b)
    if ca != cb {
        return ca > cb
    }
    match a.cmp(b) {
        Less => true,
        Equal => false,
        Greater => false,
    }
}

/// The whole report, or the reason there is none. `?` is what makes the failure one line.
fn report(path: String) -> Result[String, IoError] {
    let text = read_to_string(path)?;
    let counts = count_words(text)
    let mut out = "\(counts.len()) distinct words in \(text.len()) bytes\n"
    for word in ranked(counts) {
        out = out.concat("  \(word): \(count_of(counts, word))\n")
    }
    Ok(out)
}

let path = "tests/e2e/scratch/words.txt"
let text = "the quick brown fox\njumps over the lazy dog\nthe fox sleeps\n"
match write_string(path, text) {
    Ok(_) => {},
    Err(e) => panic("cannot write: \(e.to_str())"),
}
match report(path) {
    Ok(r) => print(r),
    Err(e) => println("no report: \(e.to_str())"),
}
match report("tests/e2e/scratch/missing.txt") {
    Ok(r) => print(r),
    Err(e) => println("no report: \(e.to_str())"),
}
```

Given `the quick brown fox / jumps over the lazy dog / the fox sleeps`, stdout begins
`9 distinct words in 59 bytes` and lists `the: 3`, `fox: 2`, then the singletons in
alphabetical order. Reading a path that is not there prints `no report: not found`. Exit:
`0`. The whole program is `tests/e2e/cases/word_frequency.origin`, including the sort,
which is written in Origin.

Nothing here is a language feature demonstration, and that is the point: every part of it
needed something Origin did not have when Phase 7 began. `Map[String, i64]` and `List`
needed collections and a hash the three engines agree on (§13); `split`, `trim` and
`is_empty` needed strings (§14); `"\(word): \(n)"` needed interpolation; `read_to_string`
needed files (§15); `?` on the `Result` it returns needed the early return to be built at
the enclosing function's own type (§09); and `counts.get(w)` returning an `Option` needed
`match` on a literal to compile at every optimization level. Reading it as ordinary code —
rather than as a list of features — is what the phase was for.

---

## Programs that must be REJECTED

Each of these exits `1` with the named diagnostic and produces no binary.

## 14. No implicit numeric coercion

```origin
let a = 1i32
let b: i64 = 2
let c = a + b
```

`error[E0308]: mismatched types in `+`: left is `i32`, right is `i64``, with a help
suggesting `a as i64 + b`. Origin never widens implicitly (§03).

## 15. Non-exhaustive match

```origin
enum Shape { Circle(f64), Rect { w: f64, h: f64 } }

fn area(s: Shape) -> f64 {
    match s {
        Shape::Circle(r) => 3.14159 * r * r,
    }
}
```

``error[E0004]: non-exhaustive match: `Shape::Rect { .. }` is not covered``, with a help
offering the missing arm. Because this is an error, `area` can never fail at runtime.

## 16. Non-`Send` value crossing a channel

```origin
struct Counter { mut n: i64 }

let ch = chan::bounded[Counter](1)
ch.send(Counter { n: 0 })
```

``error[E0277]: `Counter` is not `Send```, note: ``field `n` is declared `mut` ``, help:
``send a `Mutex[Counter]` instead``.
