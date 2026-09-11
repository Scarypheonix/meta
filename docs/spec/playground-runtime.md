# 18 — The playground runtime

Origin runs in a browser tab. The same compiler front end, the same bytecode compiler, the
same optimizer and the same virtual machine that `originc run --vm` uses, compiled to
WebAssembly and handed a program as a string instead of a path.

This document is normative for the **browser build** and for nothing else. It does not
change the language. Every other document in `docs/spec/` describes Origin; this one
describes a *host*, in the same sense that Linux and macOS are hosts, and its whole job is
to say exactly where that host differs from the two the project already targets and what an
Origin program observes at each of those points.

The rule the rest of this document is an elaboration of:

> **The browser is a host without a filesystem.** It is not a smaller language, a subset,
> or a sandboxed dialect. A program whose behaviour does not depend on the filesystem
> behaves in the browser exactly as it behaves natively — the same output, byte for byte,
> and the same exit status.

## 1. What the browser build is

`cmd/originwasm` is a `GOOS=js GOARCH=wasm` binary containing the whole pipeline from
source text to a running program:

```
source string
  -> lex, parse, resolve, check        internal/{lex,parse,resolve,check}
  -> monomorphize                      internal/mono
  -> bytecode                          internal/compile
  -> optimize                          internal/opt
  -> run                               internal/vm     (default; ADR-0032)
                                       internal/interp (selectable)
```

It exports one function to JavaScript. Nothing else on the page can reach the engines, and
the engines can reach nothing on the page: the only values that cross the boundary are the
source string and arguments going in, and captured output, an exit status and diagnostics
coming out.

The native x86-64 backend (`internal/backend`, `internal/obj`, `internal/x86`, ADR-0017) is
**not** part of this build. It emits machine code for a host kernel, which is meaningless
in a browser sandbox, and `originc build` has no counterpart here.

### 1.1 The boundary

```
run(request) -> response
```

`request` carries the program's text, the engine to run it on, the optimization level, the
argument vector, and the program's standard input as one string (§18). `response` carries
captured stdout, captured stderr, the exit status, and whether the run ended by returning,
by trapping, by `process::exit`, or by exhausting a limit this document sets.

The exact JavaScript shape is `cmd/originwasm`'s contract and is documented there. What is
normative here is that the boundary is **data only**. No callback, no host object and no
JavaScript function is reachable from an Origin program. There is no `eval`, no dynamic
import driven by program text, and no way for a program to name a capability it was not
given. A program is a string that produces bytes.

## 2. The OS surface, feature by feature

This is the section the phase exists for. Each row is a thing Origin can do that has, on
Linux and macOS, a system call underneath it, and each says what the browser does instead.

| Feature | Native | Browser | Divergence |
|---|---|---|---|
| `io::println`, `io::eprintln` | `write(1)`, `write(2)` | captured into a buffer, delivered to the page (§4) | none observable |
| `std::fs` (§15) | `openat`, `read`, `write` | every operation fails; see §3 | **yes, and it is the only one** |
| `process::exit` (§17) | `exit_group` | unwinds to the entry point, which reports the status | none observable |
| `args()` (§17) | the kernel's argument vector | supplied by the caller across the boundary | none observable |
| `read_line`, `input`, `ask` (§18) | `read(0)` | the caller's `stdin` string, a line at a time | none observable |
| Green threads, channels, `Mutex` (§12) | goroutines, preempted by the host | goroutines, yielding explicitly at a back edge | **§08's preemption had to be asked for**; see §5 |
| Traps and panics (§08, ADR-0005, ADR-0026) | a message on stderr, exit 101 | the same message on captured stderr, exit 101 | none |
| Allocation and collection (§08) | Go's heap (interpreter), `internal/gc` (VM) | identical — both are Go | none |
| Integer overflow (ADR-0005) | traps | traps | none |
| Float rendering (§16, ADR-0031) | prelude Origin code | prelude Origin code | none |

Two entries that a reader of the phase brief may expect are absent because **they do not
exist in Origin**:

- **FFI into libc.** There is none, and there never has been. ADR-0017 makes a freestanding
  binary with no linker and no libc, and records under its consequences: *"No FFI. Calling
  C from Origin is not possible in a binary that does not link one."* There is no syntax
  for it, no runtime support for it, and nothing in the corpus that uses it. The browser
  build therefore has nothing to remove and nothing to reject.
- **kqueue-backed async I/O.** `docs/spec/08-memory-model.md` scoped it to Phase 6;
  `docs/spec/12-concurrency.md` §"Recorded in `docs/deferred.md`" amends that and says why:
  *"Origin has no I/O to be asynchronous about — `io::println` is the entire surface."* It
  was never built. `docs/deferred.md` still carries it. There is no event loop to port.

**Standard input is not a second gap.** ADR-0033 fails every file operation because there
is no file and nothing on the page could be one; a page *can* hold text in a box, so the
caller hands it over and §18 reads it exactly as it reads a pipe -- the same lines, the same
end, the same limit. The playground's input box is what fills it. A caller that supplies
nothing gives the program an empty stream, which is what a program run with its input closed
gets anywhere.

The filesystem is the only gap in what a program can *observe*, and §3 is the whole of it.
One thing in the table cost implementation work to keep in the "none" column rather than
being free: §08's back-edge preemption, which this host does not provide and had to be
asked for. §5 records what that was and how the corpus found it.

## 3. `std::fs` in the browser

A browser tab has no filesystem. Origin's file interface (§15, ADR-0030) is four
compiler-provided operations underneath a prelude that turns their status into an
`IoError`:

| Operation | Native | Browser |
|---|---|---|
| `fs::read_file(path) -> i64` | a status from `errno` | `IOOther` (3), always |
| `fs::taken_text() -> String` | the bytes just read | `""` — unreachable on a successful read, because there are none |
| `fs::write_file(path, contents) -> i64` | a status from `errno` | `IOOther` (3), always |
| `fs::file_exists(path) -> bool` | `open` succeeded | `false`, always |

Through the prelude, a program observes:

```origin
match read_to_string("anything") {
    Result::Ok(text) => io::println(text),        // never taken in the browser
    Result::Err(e)   => io::println(e.to_str()),  // always taken; prints "I/O error"
}
```

`file_exists` answering `false` is not a stub and not a convenient lie: no file exists, so
`false` is the true answer. `read_file` and `write_file` answering `IOOther` is the same
kind of statement — the operation genuinely did not happen — routed through the status
vocabulary §15 already defines.

**`IoError` does not grow a fourth case.** ADR-0030 gives it exactly three — `NotFound`,
`PermissionDenied`, `Other` — chosen because they are the three a program acts on
differently. "This host has no filesystem" is not a fourth thing to act on; it is `Other`,
which is what `Other` is for. Adding a case would change the language to describe a host,
and this document changes no language.

**The operations are absent from the binary, not merely failing in it.** The browser build
selects a `js`-tagged implementation of the four operations, so Go's `os` file calls are
not linked into the WebAssembly module at all (ADR-0033). The guarantee is structural: the
playground cannot touch a filesystem because there is no code in it that could, not because
a branch declines to. This is the phase's "no filesystem access" constraint made a property
of the build rather than a promise about behaviour.

**Consequence for the corpus.** Four of the 102 end-to-end cases use `std::fs`
(`file_read_write_round_trip`, `file_large_and_threaded`, `word_frequency`,
`option_and_result_methods`). They are the four cases whose browser output is *expected* to
differ, they are named individually in the differential, and they are excluded by name and
not by a pattern that could quietly grow. The other 98 must match natively byte for byte.

## 4. Output

Output is **captured, chunked and bounded**.

- **Captured.** `io::println` reaches an `io.Writer` the entry point supplies, exactly as
  `originc run` supplies `os.Stdout`. Nothing is written to a file descriptor.
- **Chunked, not withheld.** A program that prints and then loops forever must show what it
  printed. Output is delivered to the page as it accumulates, on a flush interval and at
  every terminal event, rather than only when the program ends (ADR-0036). Ordering within
  a stream is preserved exactly; stdout and stderr are separate streams and their relative
  interleaving is not specified, which matches every other engine — natively they are two
  file descriptors and the shell decides.
- **Bounded.** A program may print without limit. The runtime stops accumulating at a
  documented byte ceiling and appends a truncation notice naming the limit. The notice is
  the playground speaking, not the program: it goes to the page's own channel, never into
  the program's captured stdout, so a truncated run cannot be mistaken for a program that
  printed the notice itself.

The ceiling and the flush interval are `cmd/originwasm` constants, recorded there with the
reasoning. They are the only two numbers in this document that are tuning rather than
semantics.

## 5. Threads

§12's green threads are, in both the interpreter and the virtual machine, **goroutines** —
`internal/interp/concurrent.go` says so in its opening comment, and
`internal/vm/concurrent.go` does the same. They are not a hand-written M:N scheduler in
either engine; the M:N scheduler is the *native* runtime's, and the native runtime is out of
scope here.

This matters because it changes what has to be argued. The question is not whether a
user-space scheduler survives having no OS threads; it is whether **Go's** scheduler works
under `GOOS=js`. Mostly it does: the js/wasm port runs goroutines, channels, `select` and
the race of a program's exit against its threads on a single thread, and
`internal/vm/concurrent.go`'s collector registration is unaffected because it is ordinary Go
synchronization.

**One part of it does not, and it had to be fixed rather than documented.** §08 says
scheduling is *"preemptive at safepoints: a green thread that runs a loop containing a
back-edge can always be descheduled, so a compute loop cannot starve the scheduler."* Both
engines got that from the host and neither survived losing it:

- The **virtual machine** has an explicit safepoint on every backward jump, and its body is
  to release the world lock and re-take it. On a host with real threads that is enough — a
  thread blocked on the lock is running on another processor and takes it the instant it is
  free. Under `GOOS=js` there is one thread and no asynchronous preemption, so `Unlock`
  followed immediately by `Lock` re-acquires the lock every time and the waiting thread
  never runs.
- The **interpreter** had no safepoint at all. Its own comment recorded why it did not need
  one: *"Green threads are goroutines. That is not a shortcut around §08's M:N scheduler —
  Go's own scheduler is M:N, and it preempts, which is what §08 asks for."* That reasoning
  is correct on every host that preempts and false on the one that does not.

Preemption in Go's runtime is delivered by signals, and a browser has none. So both engines
now yield explicitly at a back edge — `runtime.Gosched`, in a `js`-tagged file, a no-op
everywhere else so that no other host pays for it. The guarantee §08 states is therefore
kept on this host too, by asking for it rather than by inheriting it.

This was found by running the corpus, not by reading the code: without it,
`tests/e2e/cases/preemption_at_a_back_edge.origin` — a spin loop that ends only when
another thread sets its flag — **hangs forever** on both engines. Every other engine runs it.
A program that hangs is the worst available way to differ, because nothing reports it, and
it is the reason §6's differential covers the whole corpus rather than a sample.

What genuinely *is* absent, and is not a divergence, is real parallelism: no engine ever had
it. `CLAUDE.md` records it under known-deferred — *"no engine runs threads in parallel."* A
program that depends on two Origin threads running at the same instant was already
unsupported on Linux and macOS.

What *is* new is that a program which never yields never returns, and in a browser that
freezes the tab it runs in. The runtime answers this by construction rather than by
detection: the module runs in a Web Worker, so the page stays responsive and the worker can
be terminated (ADR-0036). Termination is not a language event — the program does not observe
it, no trap is raised, and no exit status is produced. The page reports that it stopped the
program, and says so in its own words rather than the program's.

## 6. Determinism

The browser build is a **fourth engine** in the sense Phase 5's native backend was a third,
and it is held to the same standard: the same program, the same output, the same exit
status.

Specifically, for every program that does not use `std::fs`:

```
originwasm(src, VM, -O)  ==  originc run --vm -O src      stdout, stderr and exit status
originwasm(src, interp)  ==  originc run src              stdout, stderr and exit status
```

This is checked, not asserted, over the whole end-to-end corpus (§3's four exclusions
aside) at every optimization level, in `tests/wasm`. A divergence is a bug in the browser
build and is never fixed by editing an expectation.

Nothing in the pipeline is permitted to consult the host. Two properties make that
checkable rather than hoped for: no engine reads a clock, a random source or an environment
variable, and the four filesystem operations are the only ones that ever did consult the
host. §3 removes those from the build.

## 7. What the playground itself may report

The page can produce three kinds of text, and they must not be confusable:

1. **The program's output** — captured stdout and stderr, byte for byte, unannotated.
2. **The compiler's diagnostics** — `internal/diag`'s rendering, unchanged. §09's rules and
   `docs/spec/codes.md`'s registry apply here exactly as they do at a terminal. The browser
   build renders the same text with the same codes; it does not paraphrase, soften or
   re-word them.
3. **The playground's own messages** — "the program was stopped", "output truncated at N
   bytes", "the module failed to load". These are the host speaking about the run. They are
   visually distinct from both of the above and are never written into either stream.

Rule 2 of §09 — a diagnostic never names an internal identifier — is enforced over this
build by the same test that enforces it everywhere else.

## 8. What is not here

Recorded so that a reader does not go looking:

- **`originc build`.** No native executable, no object file, no disassembly. The browser
  build stops at running the program.
- **Multiple files and modules.** The boundary takes one source string, so a program is one
  file plus the prelude. §07's module system is unchanged and untouched; the playground
  simply has no second file to give it. `driver.CompilePackage` already takes a `[]Unit`,
  so this is a boundary decision and not a language one, and widening it later costs a field.
- **A network.** Origin has no socket API to disable.
- **Persistence.** Whatever the page remembers about a visitor's program is the page's, in
  that browser, and is not visible to any Origin program. There is no Origin-level storage
  interface, and this document does not add one.
