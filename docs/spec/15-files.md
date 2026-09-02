# 15 — Files

A program that cannot read a file cannot be a compiler, and Phase 9's exit criterion is
Origin compiling itself. This section gives Origin the smallest file interface that is
enough for that, and no more.

```origin
use std::io;

fn main() {
    match read_to_string("input.txt") {
        Result::Ok(text) => io::println("\(text.len()) bytes"),
        Result::Err(e) => io::println("cannot read it: \(e.to_str())"),
    }
}
```

## What it is

Three operations, in the prelude, written in Origin over four the compiler provides:

```origin
pub enum IoError {
    NotFound,
    PermissionDenied,
    Other,
}

pub fn read_to_string(path: String) -> Result[String, IoError];
pub fn write_string(path: String, contents: String) -> Result[(), IoError];
pub fn file_exists(path: String) -> bool;
```

They are free functions in the prelude rather than a `File` type with `open`, `read` and
`close`, and that is the whole design:

- **A whole file at a time.** A compiler reads a source file and writes an object file.
  Neither is streamed, and a handle that has to be closed is a resource Origin has no way
  to close automatically — there are no destructors and no `defer` (§08 leaves ownership
  to the collector, which is not a schedule). A leaked descriptor is the kind of quiet
  failure this language exists to not have.
- **Errors are values** (ADR-0006). Every operation returns a `Result`, and `IoError`
  names the three cases a program can act on differently. There is no errno.
- **Text, not bytes.** `read_to_string` returns a `String`, and a `String` is valid UTF-8
  (§14). A file that is not is an error, not a `String` that lies.

## Errors

| Condition | Result |
|---|---|
| the path does not exist | `Err(IoError::NotFound)` |
| the path exists but cannot be opened for the requested access | `Err(IoError::PermissionDenied)` |
| the file's bytes are not valid UTF-8 | `Err(IoError::Other)` |
| anything else the system refuses | `Err(IoError::Other)` |
| `file_exists` on a path that cannot be read | `false` — not an error |

None of these traps. A missing file is the ordinary case a program is written to handle,
which is exactly what §09 separates from a bug.

`write_string` creates the file if it does not exist and truncates it if it does. A
partial write — the system accepting some bytes and then failing — is
`Err(IoError::Other)`, and the file is left with whatever was written: there is no
rollback, and pretending otherwise would be a promise the system does not make.

## What the compiler provides

`std::fs`'s four operations. They exist for the reason `std::str`'s and `std::array`'s do
(ADR-0028): their bodies are system calls, which Origin has no way to say.

| Operation | Meaning |
|---|---|
| `fs::read_file(path) -> i64` | reads the whole file; returns a status, and leaves the bytes where `fs::taken_text` will find them |
| `fs::taken_text() -> String` | the bytes the last successful `fs::read_file` on **this thread** produced |
| `fs::write_file(path, contents) -> i64` | writes the whole file; returns a status |
| `fs::file_exists(path) -> bool` | whether the path can be opened for reading |

The status is `0` for success, `1` for not found, `2` for permission denied and `3` for
anything else. A program is not expected to see these numbers: the prelude turns them into
an `IoError` in the one place that knows what they mean.

The split between `read_file` and `taken_text` is the same one §12's `recv` uses, and for
the same reason: a compiler-provided operation returns a primitive or a `String`, never a
prelude type, because the native backend has no way to construct one. The value is held on
the calling thread until the prelude reads it, which is what makes the pair atomic with
respect to another thread doing the same thing. The slot is a garbage-collection root, the
same one `chan::taken_value` uses.

## Paths

A path is a `String`, passed to the system unchanged. Origin does not parse it, normalize
it, or have an opinion about separators: there is no `Path` type in 0.1, and a program that
needs to join two path components concatenates them (§14). A path containing a NUL byte is
`Err(IoError::Other)` rather than a truncated path, since that is what the system would
otherwise silently do.

There is no current-directory query, no directory listing, no metadata beyond
`file_exists`, no rename, no delete. Every one is recorded in `docs/deferred.md`.

## Worked examples

| Program | Output |
|---|---|
| write a file, then read it back | the same contents |
| `read_to_string` of a path that does not exist | `Err(NotFound)` |
| `file_exists` on a written file, then on a missing one | `true`, then `false` |
| write the empty string, then read it | `Ok("")` |
| write 100 KiB, then read it back and compare | equal |
| write, overwrite with something shorter, read | only the second contents |
| `read_to_string` of a path with a NUL byte | `Err(Other)` |
| a file written and read on a spawned thread while another thread does the same | each thread sees its own |

Every row is a case in `tests/e2e/cases/`, asserting exact stdout, stderr and exit status on
all three engines at every optimization level. They run in a directory the harness makes
and removes, so the suite writes nothing it does not clean up and nothing outside it.
