# ADR-0016: The SSA IR is built from bytecode, not from the AST

**Status:** accepted · **Date:** 2026-08-29 · **Decided by:** implementer (user delegated)

## Context
Phase 4 needs an SSA intermediate representation with a control-flow graph: the
optimizer's passes are defined on one, and Phase 5's instruction selection reads one.
Phase 3 already delivered a working path from the checked AST to stack bytecode, and a
VM that executes it.

The IR can be built from either end of that path.

## Options considered
- **AST → SSA → bytecode**, replacing the Phase 3 lowering. The AST carries types,
  spans and structure, so building SSA from it is the easier construction: control flow
  is still nested, and a `while` loop's back-edge is where the parser put it rather than
  something to rediscover. The cost is that the Phase 3 compiler is thrown away, and
  that `-O0` and `-O1` then differ by *two* things — the optimizer and the entire
  lowering path — which makes the differential a weaker oracle.
- **Bytecode → SSA → bytecode.** The construction is harder: the control-flow graph must
  be recovered from jump targets, and the operand stack must be turned back into values,
  because a stack slot live across a branch is a variable in disguise. In exchange,
  `-O0` and `-O1` run the *same* generated code except for the optimizer's edits, so a
  divergence between them is the optimizer's fault and nothing else's. This is what
  JVMs do, for the same reason.
- **Optimize the bytecode directly**, with no SSA at all. Cheapest now; every pass then
  reinvents its own def-use analysis, and Phase 5 has to build the IR anyway.

## Decision
Build the SSA IR from bytecode, optimize it, and emit bytecode again. `-O0` skips the
round trip entirely; `-O1` and `-O2` run it with progressively more passes.

SSA construction uses Braun et al.'s algorithm ("Simple and Efficient Construction of
Static Single Assignment Form", CC 2013), which inserts φ-functions on demand and needs
no dominance-frontier computation. Locals and operand-stack slots are both treated as
variables, which is what makes the stack machine's values recoverable.

## Consequences
- **Phase 4's exit criterion becomes a real oracle.** The suite compares `-O0`, `-O1`
  and `-O2` output byte for byte; because they share a lowering, any difference is a
  miscompilation. Under the other option a difference could have been either engine
  being wrong, and the test could not say which.
- Nothing from Phase 3 is discarded. The bytecode is the interchange format between the
  lowering, the optimizer and the VM, and it stays the thing snapshot tests pin.
- The construction cost is real and lands in one module, `internal/ir/build`, with its
  own tests: stack-depth inference, leader detection, φ insertion and trivial-φ removal.
- Phase 5's backend reads the IR, so the native path gets the optimizer for free rather
  than needing its own.
- If the IR later needs information the bytecode has thrown away — a source-level type
  for a devirtualization pass, say — the answer is to widen the bytecode, not to add a
  second lowering path. Two paths from source to code is the thing this decision exists
  to avoid.
