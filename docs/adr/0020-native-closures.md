# ADR-0020: Native closures are a static-boxing pass plus one calling convention shared by every closure-shaped value

**Status:** accepted · **Date:** 2026-08-30 · **Decided by:** implementer (user delegated)

## Context
`internal/backend` lowered every other Phase 5 construct — arithmetic, control flow,
struct/tuple/enum-variant construction and field access, structural `==`/ordering — but
rejected any function with `Captures > 0` outright, and its lowering of `OpFunc`
required the callee to be, syntactically, the immediate operand of the call: an
`OpCall` whose callee is anything else (a parameter, a phi, another call's return value)
was `unimplemented: an indirect call in native code`. Most realistic Origin programs use
a closure or pass a function as a value somewhere, so this was Phase 5's last real gap.

The VM already runs every closure program correctly. It does so dynamically: a value
carries a runtime tag (`layout.TagFn` for a bare function, `layout.TagRef` for a heap
object), and `doCall` (`internal/vm/exec.go`) switches on it at every call site. Native
values carry no such tag — ADR-0008 keeps primitives unboxed specifically so the
collector and the backend never need one — so the question a native indirect call site
must answer (is this register a raw code address or a closure object's reference?)
cannot be asked of the value at run time. It has to be decided statically, once, in a
way every call site can then rely on unconditionally.

## Options considered
- **A trampoline per boxed function.** Give a bare top-level function reference,
  wherever it escapes being an immediate call, its own tiny generated stub that adapts
  between "ordinary function" and "closure" calling conventions, and box the stub's
  address instead of the function's own. Sound, and it would let an ordinary function's
  own calling convention stay untouched even in the closure case. Rejected for needing a
  second code-generation path (one per escaping function, deduplicated) for no
  observable benefit over the option below, which gets the same result by leaving the
  closure reference off to the side instead of adapting register positions to match it.
- **Shift every parameter register to make room for an implicit closure-reference
  argument, uniformly, on every function.** Fully sound and needs no per-value analysis
  at all: every function, closure or not, is called the same way. Rejected on cost: it
  reduces the six-register argument budget every function gets
  (`docs/spec/11-codegen.md`) to five for every call in the program, not just the rare
  ones that go through a closure, to solve a problem only those rare ones have.
- **A runtime tag bit on a function-typed value**, mirroring the VM's `Tag`, so a call
  site can branch dynamically the way `doCall` does. Rejected as a representation
  change bigger than the problem: it means stealing a bit from every value that could
  ever hold a function, undoing the "primitives unboxed" property ADR-0008 chose
  specifically to keep the collector precise and values plain machine words.
- **Static boxing plus one shared, unshifted calling convention.** Decided below.

## Decision
A new native-only, unconditional IR pass (`internal/backend/closures.go`'s
`resolveClosureCalls`, run once per function immediately after `ir.Build`, before any
optimization or lowering sees the function) makes the answer to "is this a raw address
or a closure object" a static property of the IR rather than a runtime one:

- An `OpFunc` value (a bare top-level function reference) is left alone, still lowered
  to a raw code address, exactly when every one of its uses is being the immediate
  callee of an `OpCall` — the one case that was already sound and already fast.
- Every other `OpFunc` value — returned, stored, passed as an argument, merged by a
  phi, written into a struct/tuple/variant field — is wrapped, right after its
  definition, in a new IR op (`OpBoxFn`) that allocates the same one-word captureless
  closure object `internal/vm/fields.go`'s `boxIfFn` already builds dynamically for the
  identical situation on the VM (`compile.Program`'s `FnBoxType`, ADR-0019). Every use
  of the original value is repointed at the box.
- Every `OpCall` whose callee is, after that, not a bare `OpFunc` value — a box, a real
  `OpClosure`, or anything derived from either — is repointed at a new op,
  `OpCallClosure`. `internal/ir`'s `HasEffect`/`clobbersCallerSaved`-style
  classification tables already had a slot reserved for it from earlier IR work; this
  is what fills it in.

A closure-shaped object's field 0 is always its underlying function's entry address
(`internal/backend/lower.go`'s `closure` builds a real `OpClosure` object this way;
`OpBoxFn` lowers through the same `construct` a struct field uses, since a one-word,
non-reference-kind field needs nothing special). `OpCallClosure`'s lowering
(`callClosure`) reads that field through a register and calls it, and passes the
closure reference itself to the callee — not shifted into an argument register, since
every one may already carry a real parameter, but on the stack, in the fixed spot a
`call` instruction always leaves just above the return address (`[rbp+16]` once the
callee's own prologue has run). `OpCapture`'s lowering reads a capture from that same
spot, at `[rbp+16]`, with no persistent register or frame slot needed for it at all: the
value sits in the *caller's* frame, untouched by anything the callee does with its own
stack, for the whole call.

An ordinary function reached through a box was compiled with no idea it might be called
this way and simply never reads `[rbp+16]` — it has no `OpCapture` instructions — so
leaving the closure reference there costs it nothing. This is what lets one calling
convention, and one `OpCallClosure` lowering, serve a real closure and a boxed bare
function alike, with the call site never needing to know, at compile time or run time,
which of the two a particular closure-shaped value actually is.

## Consequences
- **A function called only directly still costs nothing new.** `resolveClosureCalls`
  only touches an `OpFunc` value with an escaping use; the common case (call it by name,
  nowhere else) keeps the exact direct-call lowering Phase 5 already had, register-free
  and label-addressed.
- **Six argument registers stay six**, for a direct call and an indirect one alike. The
  closure reference's stack-passing costs one push and one pop per indirect call site,
  not a permanently narrowed argument budget.
- **One box per escaping value, not one per call site.** `resolveClosureCalls` boxes an
  `OpFunc` value once, at its own definition; every use downstream — including a
  function used both as a direct callee and as an escaping value in the same body —
  shares that box rather than allocating separately per occurrence. The one thing this
  gives up, deliberately, is the direct-call fast path for a use that used to be a
  simple immediate call but now shares an escaping value's box: correctness first, and a
  documented opportunity rather than a gap (see `docs/deferred.md`).
- **Native heap collection remains a separate, still-open problem.** A box is one more
  kind of allocation `alloc` makes; nothing here changes when or whether a collection
  can safely run (spec/11-codegen.md's stack-map DEFERRED note, unchanged by this ADR).
- **The IR gained two ops with no bytecode counterpart.** `OpBoxFn` and (in the sense
  that it is only ever *produced* here) `OpCallClosure` exist solely inside
  `internal/backend`'s own IR build: `internal/opt`'s optimizer and `internal/ir/emit.go`
  never see them, because the VM's O1/O2 path builds and emits its own, separate IR from
  the same bytecode and never needs the distinction this ADR exists to make.
