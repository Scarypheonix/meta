# 10 — Worked Examples

Every example below is normative: the program text, its exact stdout, its exact stderr,
and its exit status. As each phase lands, these become files in `tests/e2e/cases/` with
`.out`, `.err` and `.exit` companions, and the harness asserts byte equality. An example
here that the implementation cannot reproduce is a bug in the implementation or a bug in
this document — never a reason to edit the expected output.

`src/main.origin` is assumed throughout; column numbers in expected stderr refer to it.

---

## 1. Hello

```origin
use std::io;

fn main() {
    io::println("Hello, Origin.");
}
```

stdout: `Hello, Origin.\n` · stderr: empty · exit: `0`

## 2. Recursive fibonacci

```origin
use std::io;

fn fib(n: i64) -> i64 {
    if n < 2 { n } else { fib(n - 1) + fib(n - 2) }
}

fn main() {
    let mut i = 0;
    while i <= 10 {
        io::println(fib(i).to_str());
        i = i + 1;
    }
}
```

stdout: `0\n1\n1\n2\n3\n5\n8\n13\n21\n34\n55\n` · exit: `0`

## 3. Closure counter (capture-by-value, shared cell)

Demonstrates §04's capture rule: the lambda captures `cell` by value, and `cell` is a
reference to a heap object, so the mutation is shared. Two counters do not interfere.

```origin
use std::io;

struct Cell { mut value: i64 }

fn make_counter() -> fn() -> i64 {
    let cell = Cell { value: 0 };
    || { cell.value = cell.value + 1; cell.value }
}

fn main() {
    let c = make_counter();
    let d = make_counter();
    io::println(c().to_str());
    io::println(c().to_str());
    io::println(d().to_str());
}
```

stdout: `1\n2\n1\n` · exit: `0`

## 4. Recursive enum (linked list)

No `Box` is needed: enum payloads are references (§08).

```origin
use std::io;

enum List { Nil, Cons(i64, List) }

fn sum(l: List) -> i64 {
    match l {
        List::Nil => 0,
        List::Cons(head, tail) => head + sum(tail),
    }
}

fn main() {
    let l = List::Cons(1, List::Cons(2, List::Cons(3, List::Nil)));
    io::println(sum(l).to_str());
}
```

stdout: `6\n` · exit: `0`

## 5. Mutual recursion

```origin
use std::io;

fn is_even(n: u64) -> bool { if n == 0 { true } else { is_odd(n - 1) } }
fn is_odd(n: u64) -> bool { if n == 0 { false } else { is_even(n - 1) } }

fn main() {
    io::println(is_even(10).to_str());
    io::println(is_odd(10).to_str());
}
```

stdout: `true\nfalse\n` · exit: `0`

## 6. Generics with a trait bound

```origin
use std::io;

fn max2[T: Ord](a: T, b: T) -> T {
    match a.cmp(b) {
        Ordering::Less => b,
        Ordering::Equal => a,
        Ordering::Greater => a,
    }
}

fn main() {
    io::println(max2(3, 7).to_str());
    io::println(max2("apple", "banana"));
}
```

stdout: `7\nbanana\n` · exit: `0`

Monomorphization emits two copies of `max2`: one at `i64`, one at `String`.

## 7. Integer overflow traps

```origin
use std::io;

fn main() {
    let x = 9223372036854775807;
    io::println("before");
    let y = x + 1;
    io::println(y.to_str());
}
```

stdout: `before\n` · stderr: `origin: arithmetic overflow at src/main.origin:6:13\n`
· exit: `101`

Identical at `-O0`, `-O1` and `-O2`. An optimizer that constant-folds `x + 1` must fold
it to the trap, not to a wrapped value.

## 8. Explicit wrapping

```origin
use std::io;

fn main() {
    let x = 9223372036854775807;
    io::println(x.wrapping_add(1).to_str());
    io::println((300 as u8).to_str());
    io::println(((-7) / 2).to_str());
    io::println(((-7) % 2).to_str());
}
```

stdout: `-9223372036854775808\n44\n-3\n-1\n` · exit: `0`

## 9. `Result` and `?`

```origin
use std::io;

enum ParseError { Empty, BadDigit(char) }

fn parse_digit(c: char) -> Result[i64, ParseError] {
    if c >= '0' && c <= '9' {
        Result::Ok(c as u32 as i64 - 48)
    } else {
        Result::Err(ParseError::BadDigit(c))
    }
}

fn parse_two(a: char, b: char) -> Result[i64, ParseError] {
    let x = parse_digit(a)?;
    let y = parse_digit(b)?;
    Result::Ok(x * 10 + y)
}

fn main() {
    match parse_two('4', '2') {
        Result::Ok(n) => io::println(n.to_str()),
        Result::Err(_) => io::println("error"),
    }
    match parse_two('4', 'z') {
        Result::Ok(n) => io::println(n.to_str()),
        Result::Err(ParseError::BadDigit(_)) => io::println("bad digit"),
        Result::Err(ParseError::Empty) => io::println("empty"),
    }
}
```

stdout: `42\nbad digit\n` · exit: `0`

## 10. Mutation through an alias

```origin
use std::io;

struct Counter { mut n: i64 }

fn bump(c: Counter) { c.n = c.n + 1; }

fn main() {
    let a = Counter { n: 0 };
    let b = a;
    bump(b);
    bump(a);
    io::println(a.n.to_str());
    io::println(ref_eq(a, b).to_str());
    io::println((Counter { n: 2 } == a).to_str());
    io::println(ref_eq(Counter { n: 2 }, a).to_str());
}
```

stdout: `2\ntrue\ntrue\nfalse\n` · exit: `0`

## 11. `for` over an iterator

```origin
use std::io;
use std::iter;

fn main() {
    let mut total = 0;
    for i in iter::range(1, 5) {
        total = total + i;
    }
    io::println(total.to_str());
}
```

stdout: `10\n` · exit: `0` — `range` is half-open, so this sums 1..=4.

## 12. Green threads and channels *(Phase 6)*

```origin
use std::io;
use std::thread;
use std::chan;

fn main() {
    let ch = chan::bounded[i64](4);
    let mut i = 1;
    while i <= 3 {
        let tx = ch.sender();
        let n = i;
        thread::spawn(|| { tx.send(n); });
        i = i + 1;
    }
    let mut total = 0;
    let mut k = 0;
    while k < 3 {
        total = total + ch.recv();
        k = k + 1;
    }
    io::println(total.to_str());
}
```

stdout: `6\n` · exit: `0`

The lambda captures `tx: Sender[i64]` and `n: i64`, both `Send`, so the lambda is `Send`
and may be spawned (§08).

## 13. A discarded allocation loop is reclaimed

```origin
use std::io;

struct Pair { a: i64, b: i64 }

fn main() {
    let mut last = Pair { a: 0, b: 0 };
    let mut i: i64 = 0;
    while i < 5000000 {
        last = Pair { a: i, b: i };
        i = i + 1;
    }
    io::println(last.a.to_str());
    io::println(last.b.to_str());
}
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
use std::io;
use std::list;
use std::map;

fn count_words(text: String) -> Map[String, i64] {
    let counts = map::new[String, i64]();
    for line in text.split("\n") {
        for word in line.split(" ") {
            let w = word.trim();
            if !w.is_empty() {
                match counts.get(w) {
                    Option::Some(n) => { counts.insert(w, n + 1); },
                    Option::None => { counts.insert(w, 1); },
                }
            }
        }
    }
    counts
}

fn report(path: String) -> Result[String, IoError] {
    let text = read_to_string(path)?;
    let counts = count_words(text);
    let mut out = "\(counts.len()) distinct words in \(text.len()) bytes\n";
    for word in ranked(counts) {
        out = out.concat("  \(word): \(count_of(counts, word))\n");
    }
    Result::Ok(out)
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
fn main() {
    let a = 1i32;
    let b: i64 = 2;
    let c = a + b;
}
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

fn main() {
    let ch = chan::bounded[Counter](1);
    ch.send(Counter { n: 0 });
}
```

``error[E0277]: `Counter` is not `Send```, note: ``field `n` is declared `mut` ``, help:
``send a `Mutex[Counter]` instead``.
