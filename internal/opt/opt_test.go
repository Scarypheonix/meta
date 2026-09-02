package opt

import (
	"math"
	"strings"
	"testing"

	"github.com/scarypheonix/meta/internal/bytecode"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/ir"
)

// The unit tests build IR by hand. Going through the front end would test the whole
// pipeline instead of one pass, and it cannot produce the shapes that matter here —
// an unused trapping operation, or two identical allocations — because the compiler
// does not emit them.

func prog(consts ...int64) *bytecode.Program {
	p := &bytecode.Program{}
	for _, c := range consts {
		p.Consts = append(p.Consts, bytecode.Const{Kind: bytecode.ConstInt, Bits: uint64(c)})
	}
	return p
}

// intOpAt builds an integer operation the way internal/compile does: with the operand kind
// on the instruction. Arithmetic without one is not a well-formed instruction -- the
// backend refuses it and the folder declines it -- because `255u8 + 1` and `255u32 + 1`
// are different programs (spec/04-expressions.md).
func intOpAt(f *ir.Func, b *ir.Block, op ir.Op, args ...*ir.Value) *ir.Value {
	v := f.NewValue(op, diag.Span{}, args...)
	v.Const = int(bytecode.KindI64)
	return b.Append(v)
}

// konst appends an integer to the pool and returns the instruction reading it.
func konst(f *ir.Func, b *ir.Block, p *bytecode.Program, val int64) *ir.Value {
	idx := len(p.Consts)
	p.Consts = append(p.Consts, bytecode.Const{Kind: bytecode.ConstInt, Bits: uint64(val)})
	v := f.NewValue(ir.OpConst, diag.Span{})
	v.Const = idx
	return b.Append(v)
}

func intOf(t *testing.T, p *bytecode.Program, v *ir.Value) int64 {
	t.Helper()
	if v.Op != ir.OpConst {
		t.Fatalf("%s is %s, not a constant", v, v.Op)
	}
	if v.Const < 0 || v.Const >= len(p.Consts) {
		t.Fatalf("%s names constant %d, which is not in a pool of %d", v, v.Const, len(p.Consts))
	}
	return int64(p.Consts[v.Const].Bits)
}

// straight returns a function with a single block ending in `return unit`.
func straight(name string) (*ir.Func, *ir.Block) {
	f := ir.NewFunc(name, 0, 0)
	return f, f.Entry
}

func finish(f *ir.Func, b *ir.Block, result *ir.Value) {
	if result == nil {
		result = b.Append(f.NewValue(ir.OpUnit, diag.Span{}))
	}
	b.Append(f.NewValue(ir.OpReturn, diag.Span{}, result))
	f.RecomputeUses()
}

func TestConstantFoldEvaluatesArithmetic(t *testing.T) {
	p := prog()
	f, b := straight("fold")
	sum := intOpAt(f, b, ir.OpAdd, konst(f, b, p, 2), konst(f, b, p, 40))
	finish(f, b, sum)

	if !ConstantFold(f, p) {
		t.Fatal("folding 2 + 40 reported no change")
	}
	if got := intOf(t, p, sum); got != 42 {
		t.Errorf("2 + 40 folded to %d, wants 42", got)
	}
}

// TestConstantFoldFoldsToTheTrap is ADR-0005 as a test: an operation that would trap
// folds to the trap, not around it. Folding `i64::MAX + 1` to a wrapped constant would
// make `-O1` produce output that `-O0` never reaches.
func TestConstantFoldFoldsToTheTrap(t *testing.T) {
	for _, tc := range []struct {
		name string
		op   ir.Op
		x, y int64
		msg  string
	}{
		{"overflow", ir.OpAdd, math.MaxInt64, 1, "arithmetic overflow"},
		{"divide by zero", ir.OpDiv, 1, 0, "divide by zero"},
		{"remainder by zero", ir.OpRem, 1, 0, "remainder by zero"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := prog()
			f, b := straight("trap")
			bad := intOpAt(f, b, tc.op, konst(f, b, p, tc.x), konst(f, b, p, tc.y))
			finish(f, b, bad)

			if !ConstantFold(f, p) {
				t.Fatal("folding reported no change")
			}
			if bad.Op != ir.OpTrap {
				t.Fatalf("folded to %s, want a trap", bad.Op)
			}
			if msg := p.Consts[bad.Const].Str; !strings.Contains(msg, tc.msg) {
				t.Errorf("the trap says %q, want it to mention %q", msg, tc.msg)
			}
		})
	}
}

func TestConstantFoldResolvesABranch(t *testing.T) {
	f := ir.NewFunc("branch", 0, 0)
	then, els := f.NewBlock(), f.NewBlock()
	cond := f.Entry.Append(f.NewValue(ir.OpTrue, diag.Span{}))
	f.Entry.SetTerminator(f.NewValue(ir.OpBranch, diag.Span{}, cond), then, els)
	for _, b := range []*ir.Block{then, els} {
		u := b.Append(f.NewValue(ir.OpUnit, diag.Span{}))
		b.SetTerminator(f.NewValue(ir.OpReturn, diag.Span{}, u))
	}
	f.RecomputeUses()

	if !ConstantFold(f, prog()) {
		t.Fatal("folding a branch on `true` reported no change")
	}
	if f.Entry.Term.Op != ir.OpJump {
		t.Fatalf("the terminator is %s, want a jump", f.Entry.Term.Op)
	}
	if len(f.Entry.Succs) != 1 || f.Entry.Succs[0] != then {
		t.Errorf("the jump goes to %v, want the `then` block", f.Entry.Succs)
	}
	if !RemoveUnreachableBlocks(f, prog()) {
		t.Fatal("the orphaned `else` block was not removed")
	}
	for _, b := range f.Blocks {
		if b == els {
			t.Error("the unreachable block is still in the function")
		}
	}
}

func TestDeadCodeRemovesAnUnusedValue(t *testing.T) {
	p := prog()
	f, b := straight("dead")
	konst(f, b, p, 7) // nothing reads it
	finish(f, b, nil)

	if !DeadCodeElimination(f, p) {
		t.Fatal("an unused constant was not removed")
	}
	f.Values(func(v *ir.Value) {
		if v.Op == ir.OpConst {
			t.Error("the unused constant survived")
		}
	})
}

// TestDeadCodeKeepsAnUnusedTrappingOperation is the other half of ADR-0005: the result
// is ignored, but the overflow it would raise is observable, so the operation stays.
func TestDeadCodeKeepsAnUnusedTrappingOperation(t *testing.T) {
	p := prog()
	f, b := straight("keep")
	sum := intOpAt(f, b, ir.OpAdd, konst(f, b, p, 1), konst(f, b, p, 2))
	finish(f, b, nil)

	DeadCodeElimination(f, p)
	found := false
	f.Values(func(v *ir.Value) {
		if v == sum {
			found = true
		}
	})
	if !found {
		t.Error("an unused trapping add was removed; an overflow it raises would be lost")
	}
}

func TestCopyPropagateRemovesATrivialPhi(t *testing.T) {
	f := ir.NewFunc("copy", 0, 0)
	join := f.NewBlock()
	src := f.Entry.Append(f.NewValue(ir.OpUnit, diag.Span{}))
	f.Entry.SetTerminator(f.NewValue(ir.OpJump, diag.Span{}), join)
	phi := join.Append(f.NewValue(ir.OpPhi, diag.Span{}, src))
	join.SetTerminator(f.NewValue(ir.OpReturn, diag.Span{}, phi))
	f.RecomputeUses()

	if !CopyPropagate(f, prog()) {
		t.Fatal("a φ with one operand was not removed")
	}
	if len(join.Phis) != 0 {
		t.Errorf("the block still has %d φs", len(join.Phis))
	}
	if join.Term.Args[0] != src {
		t.Errorf("the return reads %v, want the φ's only operand", join.Term.Args[0])
	}
}

func TestCommonSubexpressionsMergesEqualComputations(t *testing.T) {
	p := prog()
	f, b := straight("cse")
	x, y := konst(f, b, p, 3), konst(f, b, p, 4)
	first := intOpAt(f, b, ir.OpWrapAdd, x, y)
	second := intOpAt(f, b, ir.OpWrapAdd, x, y)
	use := intOpAt(f, b, ir.OpWrapMul, first, second)
	finish(f, b, use)

	if !CommonSubexpressions(f, p) {
		t.Fatal("two identical additions were not merged")
	}
	if use.Args[0] != first || use.Args[1] != first {
		t.Errorf("the multiply reads %v and %v, want the first add twice", use.Args[0], use.Args[1])
	}
}

// TestCommonSubexpressionsIsIdempotentOnAnUnusedTrappingDuplicate guards a real bug: a
// trapping duplicate (ADR-0005's own reason DeadCodeElimination keeps it) stays in its
// block with no uses once CSE has merged it once, so a second pass finds the very same
// dominating pair again. Reporting a change that second time -- as an earlier version
// did, unconditionally on any match -- means runPasses (opt.go) never reaches a fixed
// point on any function shaped like this one, which is exactly what happened building
// the native collector's own stress test (docs/deferred.md, `-O2` fixed-point bug).
func TestCommonSubexpressionsIsIdempotentOnAnUnusedTrappingDuplicate(t *testing.T) {
	p := prog()
	f, b := straight("idempotent")
	x, y := konst(f, b, p, 3), konst(f, b, p, 4)
	first := intOpAt(f, b, ir.OpAdd, x, y)
	second := intOpAt(f, b, ir.OpAdd, x, y)
	finish(f, b, second)

	if !CommonSubexpressions(f, p) {
		t.Fatal("two identical trapping additions were not merged on the first pass")
	}
	if f.Entry.Term.Args[0] != first {
		t.Fatalf("the return reads %v, want the first add", f.Entry.Term.Args[0])
	}
	// DeadCodeElimination would ordinarily run next in the real pipeline and, correctly
	// per ADR-0005, leaves `second` in place -- it can still trap, so an unused instance
	// is never removed just because CSE redirected its uses elsewhere. `first` and
	// `second` are therefore still both here, still an exact dominating match.
	if CommonSubexpressions(f, p) {
		t.Fatal("a second pass over an already-merged, still-present trapping duplicate " +
			"reported a change; the pipeline would never converge")
	}
}

// TestCommonSubexpressionsNeverMergesAllocations: two struct literals are distinct
// objects and `ref_eq` can tell them apart (spec/04-expressions.md), so merging them
// would change what a program observes.
func TestCommonSubexpressionsNeverMergesAllocations(t *testing.T) {
	p := prog()
	f, b := straight("alloc")
	x := konst(f, b, p, 1)
	first := b.Append(f.NewValue(ir.OpStruct, diag.Span{}, x))
	second := b.Append(f.NewValue(ir.OpStruct, diag.Span{}, x))
	use := b.Append(f.NewValue(ir.OpEq, diag.Span{}, first, second))
	finish(f, b, use)

	CommonSubexpressions(f, p)
	if use.Args[0] == use.Args[1] {
		t.Error("two allocations were merged into one object")
	}
}

// TestCommonSubexpressionsRequiresDominance: the earlier computation must be guaranteed
// to have run, or the replacement reads a value that was never computed.
func TestCommonSubexpressionsRequiresDominance(t *testing.T) {
	p := prog()
	f := ir.NewFunc("dom", 0, 0)
	then, els := f.NewBlock(), f.NewBlock()
	x := konst(f, f.Entry, p, 5)
	cond := f.Entry.Append(f.NewValue(ir.OpUnit, diag.Span{}))
	f.Entry.SetTerminator(f.NewValue(ir.OpBranch, diag.Span{}, cond), then, els)

	inThen := intOpAt(f, then, ir.OpWrapAdd, x, x)
	then.SetTerminator(f.NewValue(ir.OpReturn, diag.Span{}, inThen))
	inElse := intOpAt(f, els, ir.OpWrapAdd, x, x)
	els.SetTerminator(f.NewValue(ir.OpReturn, diag.Span{}, inElse))
	f.RecomputeUses()

	CommonSubexpressions(f, p)
	if els.Term.Args[0] != inElse {
		t.Error("a value from a sibling branch was reused, but neither branch dominates the other")
	}
}

// loopFunc builds `entry -> header -> body -> header, header -> exit` and returns the
// pieces a hoisting test needs.
func loopFunc(name string) (f *ir.Func, pre, header, body, exit *ir.Block) {
	f = ir.NewFunc(name, 2, 0)
	pre = f.Entry
	header, body, exit = f.NewBlock(), f.NewBlock(), f.NewBlock()
	pre.SetTerminator(f.NewValue(ir.OpJump, diag.Span{}), header)
	cond := header.Append(f.NewValue(ir.OpTrue, diag.Span{}))
	header.SetTerminator(f.NewValue(ir.OpBranch, diag.Span{}, cond), body, exit)
	body.SetTerminator(f.NewValue(ir.OpJump, diag.Span{}), header)
	u := exit.Append(f.NewValue(ir.OpUnit, diag.Span{}))
	exit.SetTerminator(f.NewValue(ir.OpReturn, diag.Span{}, u))
	return
}

func params(f *ir.Func) (*ir.Value, *ir.Value) {
	a := f.NewValue(ir.OpParam, diag.Span{})
	a.Aux = 0
	c := f.NewValue(ir.OpParam, diag.Span{})
	c.Aux = 1
	// Parameters are prepended so they precede the entry block's own instructions.
	f.Entry.Instr = append([]*ir.Value{a, c}, f.Entry.Instr...)
	a.Block, c.Block = f.Entry, f.Entry
	return a, c
}

func TestLoopInvariantCodeMotionHoists(t *testing.T) {
	f, pre, _, body, _ := loopFunc("licm")
	a, c := params(f)
	inv := intOpAt(f, body, ir.OpWrapAdd, a, c)
	f.RecomputeUses()

	if !LoopInvariantCodeMotion(f, prog()) {
		t.Fatal("an addition of two parameters was not hoisted out of the loop")
	}
	if inv.Block != pre {
		t.Errorf("the invariant value ended up in %v, want the preheader %v", inv.Block, pre)
	}
}

// TestLoopInvariantCodeMotionNeverHoistsATrap: hoisting `a / b` out of a loop that never
// runs turns a program that completed into one that traps, which ADR-0005 makes a
// correctness failure rather than a quality-of-implementation choice.
func TestLoopInvariantCodeMotionNeverHoistsATrap(t *testing.T) {
	f, _, _, body, _ := loopFunc("licm-trap")
	a, c := params(f)
	div := intOpAt(f, body, ir.OpDiv, a, c)
	f.RecomputeUses()

	LoopInvariantCodeMotion(f, prog())
	if div.Block != body {
		t.Errorf("a trapping division was hoisted to %v", div.Block)
	}
}

func TestEscapeAnalysisReplacesFieldReads(t *testing.T) {
	p := prog()
	f, b := straight("escape")
	x, y := konst(f, b, p, 10), konst(f, b, p, 20)
	obj := b.Append(f.NewValue(ir.OpStruct, diag.Span{}, x, y))
	read := f.NewValue(ir.OpGetField, diag.Span{}, obj)
	read.Const = 1
	b.Append(read)
	use := intOpAt(f, b, ir.OpWrapAdd, read, x)
	finish(f, b, use)

	if !EscapeAnalysis(f, p) {
		t.Fatal("a struct that never leaves the function was not scalarized")
	}
	if use.Args[0] != y {
		t.Errorf("the read was replaced by %v, want the value stored in that field", use.Args[0])
	}
	DeadCodeElimination(f, p)
	f.Values(func(v *ir.Value) {
		if v == obj {
			t.Error("the now-unused allocation survived dead-code elimination")
		}
	})
}

// TestEscapeAnalysisKeepsAnObjectThatEscapes: passing the object to a call means its
// identity may be observed, and proving otherwise needs an alias analysis this phase
// does not have.
func TestEscapeAnalysisKeepsAnObjectThatEscapes(t *testing.T) {
	p := prog()
	f, b := straight("escapes")
	x := konst(f, b, p, 1)
	obj := b.Append(f.NewValue(ir.OpStruct, diag.Span{}, x))
	read := f.NewValue(ir.OpGetField, diag.Span{}, obj)
	read.Const = 0
	b.Append(read)
	callee := b.Append(f.NewValue(ir.OpFunc, diag.Span{}))
	b.Append(f.NewValue(ir.OpCall, diag.Span{}, callee, obj))
	finish(f, b, read)

	if EscapeAnalysis(f, p) {
		t.Error("an object passed to a call was scalarized")
	}
	if read.Args[0] != obj {
		t.Error("the field read was rewritten even though the object escapes")
	}
}

func TestPipelineOrderIsDeclaredConsistently(t *testing.T) {
	names := map[string]bool{}
	for _, n := range PassNames() {
		names[n] = true
	}
	for _, n := range PassNames() {
		for _, need := range Prerequisites(n) {
			if !names[need] {
				t.Errorf("pass %q needs %q, which is not a pass", n, need)
			}
		}
	}
}
