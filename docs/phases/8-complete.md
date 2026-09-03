# Phase 8 — Complete

**Exit criteria:** the user delegated Phase 8's scope, so its exit criteria are the ones
this phase set for itself. They came in two halves, and the second was not planned.

The first half was the three places where `docs/spec/` and the implementation had drifted
apart, each of which had been carried in `docs/deferred.md` since Phase 5: arithmetic at
the width a type declares, a run-time representation for `u64`, and a decimal rendering for
a float — the last `unimplemented:` in native code.

The second half was a method rather than a list. Phase 7 recorded that five of its six bugs
were found by *writing a library in Origin* rather than by testing the compiler, and that
four were invisible to a differential suite with no case exercising them. So before closing
the gate: seven real programs, about 1,400 lines of Origin, each run on the interpreter, the
virtual machine and native code at `-O0`, `-O1` and `-O2` and required to agree byte for
byte. They found nine more holes.

**Status:** met. `./check` passes in 43s at 312 MiB, against budgets of 300s and 3072 MiB.
90 end-to-end cases × 7 engine/level combinations, 275 conformance cases, 32 ADRs, 18
specification documents, 46,903 lines of Go, and a 1,545-line prelude.

## What was built

**`internal/arith`, and arithmetic at the declared width.** Every integer operation now
happens at its operand type's own width, and "overflow" means *not representable in that
type* rather than *not representable in 64 bits*. `255u8 + 1` traps and `255u32 + 1` is
256, on all three engines, at every optimization level. One package holds the rules,
carrying `uint64` bit patterns interpreted per `bytecode.Kind`; the interpreter and the VM
call it and the backend mirrors it against the same tests. `bytecode.Kind` grew from one
integer kind to eight, because a register holds sixty-four bits and nothing in it says
which of the eight types they are — ADR-0021's rule, applied again.

**`u64` has a run-time representation.** A value holds the bit pattern and the static type
says how to read it, so `u64::MAX` is every bit set. What had to be *told* the signedness
rather than reading it off the value: division, remainder, right shift, the ordering
operators, `cmp` (which already had a separate `BuiltinCmpUint` for native code and now
uses it in the VM too), and `to_str` in all three engines — `rt_int_to_str` takes it as an
argument.

**`docs/spec/16-floats.md` and ADR-0031 — the shortest decimal for a float, written in
Origin.** This was the last `unimplemented:` in the project and the hardest thing in it:
the shortest decimal string that reads back as the same float is a claim about mathematics,
not a translation, and computing it needs exact arithmetic on integers far wider than
sixty-four bits. With no linker and no libc (ADR-0017) there was nothing to borrow, so the
options were to hand-write Ryu or Dragon4 in machine code or to find somewhere better to
put it.

It went in the prelude. The compiler contributes `float::bits` and `float::from_bits`,
which compute nothing — on every engine a float and its bits are the same sixty-four bits
in the same place — and everything above them is Origin that all three engines run: Steele
& White's free-format conversion in Burger & Dybvig's form, over base-10⁹ bignums on the
prelude's own `List[i64]`. The specification fixes the *result* — the shortest digits, both
tie rules, the layout table — so an implementation is tested against the answer rather than
against an algorithm.

**`Option` and `Result` have methods.** Both were two enum declarations and nothing else;
`match` and `?` were the entire surface. The diagnostic for a `?` whose error type does not
match told the writer to call `.map_err(|e| ...)`, which did not exist, and §09 said the
same. Ordinary Origin in the prelude now, the way `List` and `Str` already were. No
`unwrap()`: `expect(msg)` costs a message and buys a diagnostic worth reading.

**`Cell[T]`.** §04 has always ended its capture rule with "share an aggregate with a `mut`
field — that is what `Cell[T]` in the prelude is for", and there was no such type.

## The nine holes, and how each was found

Every one of these came from running a program, not from reading code. They are listed in
the order they turned up.

1. **A merge block's φs could share a register** (`float_rendering` at `-O0`). All the φs of
   a block are written by one parallel copy on the edge into it, so no two may share a
   location — but a φ nothing reads, the unit-typed one a `let mut` assigned in both arms of
   a *nested* `if` leaves behind, was a one-point interval, expired before the next φ's
   began, and handed its register to a later φ of the same block. Both were written through
   it and the first landing won, so the later φ held `unit`: zero, a null reference, a
   segfault. Only at `-O0`, because the optimizer removes the dead φ. Live three phases.

2. **`u64` literals were rejected at run time.** The checker had accepted
   `18446744073709551615u64` per width and signedness since the first half of this phase;
   the interpreter still trapped on anything above `i64::MAX`.

3. **An impl's inline bounds were dropped.** `impl[T: Show] Box[T]` parsed and
   `collectBounds` was called with a nil generic list, so only the `where` spelling
   survived. A method body could not use the bound it had just been given.

4. **An impl's bounds were never required at a call.** Only the *method's* bounds became
   obligations, so `Box[Opaque].to_str()` type-checked against `impl[T: Show] Show for
   Box[T]` with no `Show for Opaque` anywhere. What happened next depended on the engine:
   the interpreter and the VM fell back on a debug rendering, native code failed to build.

5. **A lambda could assign what it captured.** §04 has always said it cannot — a capture is
   a copy, there is no outer binding there. The interpreter assigned the copy and lost it at
   the next call, so a counter written that way returned the same number every time, and the
   bytecode compiler stopped with an `unimplemented:`. Now `E0595`, from the resolver, which
   is the only pass that knows a name crossed a lambda boundary.

6. **A variant of another module's enum did not resolve.** The code for it existed and was
   unreachable: it sat inside the branch taken when a path's prefix names a module, and in
   `shapes::Shape::Circle` the prefix is `shapes::Shape`, which names an enum. The only way
   to reach another module's variant was to `use` the enum first — which works, which is why
   nothing noticed, and which stops being a workaround the moment two modules both have a
   `Node`.

7. **A bound on an associated type was dropped**, and then **the projection reached the
   backend**. `type Tag: Show;` is in §02's grammar and the checker kept only the name, so
   the bound did nothing in either direction — including the prelude's own `type Iter:
   Iterator`, which meant an `IntoIterator` impl whose `Iter` had no `Iterator` impl was
   accepted and the `for` loop failed later, somewhere else. With the bound working,
   `shout[Widget]` then compiled a `to_str` whose operand type was still `Self::Tag`:
   substituting `Self` does not reduce a projection, and a projection has no layout, no
   width and no kind.

8. **A lambda argument's parameter types came from nowhere.** §03 already said "lambda
   parameters may omit theirs when the expected type is known from context", and a call
   never supplied one — every argument was inferred before the callee's parameter types
   were. `sort_by(xs, |a, b| a < b)` worked, which is why nothing noticed: comparison on two
   unknowns can wait. Anything calling a *method* on a lambda parameter could not, because
   Origin has no autoref and a method is found on one exact receiver type at the moment the
   lookup runs.

9. **`panic` from the prelude named the prelude.** The interpreter and the VM re-report a
   trap raised inside the prelude at the innermost frame the programmer wrote; native code
   did so for every trap except `panic`, whose message is a String the program computed and
   whose location was baked in while compiling. `Option::expect` is the prelude's first call
   to `panic`, so this became reachable the moment `Option` got methods.

## What surprised me

**Seven of the nine were accepted syntax that nothing verified.** An inline bound on an
impl, a bound on an associated type, a lambda assigning a capture, a struct literal in
condition position, a qualified variant path: in each case the parser said yes and some
later pass silently did nothing with it. That is a specific failure mode and it has a
specific tell — a field on an AST node that no other package reads. It is worth grepping
for.

**The one that mattered most was a register allocator bug, and a float renderer found it.**
Not a stress test of the allocator: an ordinary program with four `let mut` bindings
assigned in both arms of a nested `if`. It had been there since Phase 5 and every test in
the suite passed. What made it visible was a program long enough to have a nested branch
over four references, which is to say a program somebody would actually write.

**Writing the standard library is still the best test.** Phase 7 said so and this phase is
the second data point: nine of nine holes came from running Origin, none from reading Go.
The two most productive programs were the ones with no feature-demonstration in them at all
— a lexer and Pratt parser for a small language, and a multi-file package with a supertrait
across module boundaries.

**The prelude is now 1,545 lines of Origin**, and about 300 of those are bignum arithmetic
for one function. That is the honest cost of a correct float rendering, and it is cheaper
than the alternative by an order of magnitude: the same algorithm in hand-encoded x86-64
would have been several hundred lines of machine code whose only test is the output of the
whole program.

**A specification document is a test that nothing runs.** Five things §04, §09, §12 and §15
asserted were not true of the implementation: `Cell[T]` did not exist, `map_err` did not
exist, `read_to_string` was spelled `fs::read_to_string`, `Mutex::new` needs associated
functions the language does not have, and a shift amount does not have type `u32`. Each was
found by copying an example out of the specification and running it. Nothing else would
have found them, because nothing else reads the specification.

## What was deferred

Recorded in `docs/deferred.md` with a phase, unchanged unless noted:

- **Automatic error conversion in `?`** via an `Into` bound still needs a blanket-impl
  story. `map_err` now exists, so the explicit form the diagnostic recommends is real.
- **Associated functions** (`List::new()`, `Mutex::new()`). The workaround — a free function
  in a `std::` module — is now written into §12 as the truth rather than left as a
  discrepancy.
- **Float remainder in native code**, newly recorded: `%` on floats works on the two hosted
  engines and fails to build natively, since SSE has no remainder instruction and x87's
  `fprem` was not worth the stack for one operation. It fails loudly, so nothing depends on
  it silently.
- **Block-level item declarations**, **the orphan rule**, **doc comment capture**, and the
  package manager proper (`origin.toml`, dependencies) — all still Phase 9 or later.

## What to read first, next time

`docs/spec/16-floats.md` and **ADR-0031**, before touching anything about how a value is
printed. The rendering is Origin, not Go and not machine code, and the reason is written
down.

`internal/backend/regalloc_test.go`'s `TestAllocateGivesEveryPhiOfABlockADistinctLocation`,
before touching the allocator. It asserts the *intervals* overlap rather than which
registers a free list happened to hand out, because the second is not the invariant and
passes for the wrong reasons.

`tests/floats`, before changing the float renderer. It is the only test in the project whose
oracle is a claim about mathematics rather than another engine's output, and it is what
settled the tie rule: an exact halfway case goes to the *even* digit, and rounding halves up
is wrong about once in two thousand values.

And the seven programs themselves, in `tests/e2e/cases/`: `expression_language`,
`generics_closures_and_threads`, `recursive_generic_tree`,
`associated_types_and_iterators`, `patterns_generics_and_a_mutex`,
`option_and_result_methods`, and the multi-file package in
`internal/driver/driver_test.go`. Each is a program before it is a test, which is the only
reason any of them found anything.
