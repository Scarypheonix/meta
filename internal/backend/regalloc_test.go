package backend

import (
	"testing"

	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/ir"
)

// twoPhiLoop builds, by hand, the shape that exposed a real register-allocator bug:
// a loop header with two live-across-the-back-edge phis (a counter and an
// accumulator). b0 -> b1(header, 2 phis) -> b2(body) -> b1 (back edge), b1 -> b3(exit).
//
//	fn(n) { let mut i = 0; let mut acc = 0; while i < n { acc = acc + 10; i = i + 1 }; acc }
func twoPhiLoop(t *testing.T) (f *ir.Func, header, body *ir.Block, iPhi, accPhi, accNext *ir.Value) {
	t.Helper()
	f = ir.NewFunc("total", 1, 0)
	entry := f.Entry
	header = f.NewBlock()
	body = f.NewBlock()
	exit := f.NewBlock()

	n := entry.Append(f.NewValue(ir.OpParam, diag.Span{}))
	zero1 := entry.Append(f.NewValue(ir.OpConst, diag.Span{}))
	zero2 := entry.Append(f.NewValue(ir.OpConst, diag.Span{}))
	entry.SetTerminator(f.NewValue(ir.OpJump, diag.Span{}), header)

	iPhi = f.NewValue(ir.OpPhi, diag.Span{})
	accPhi = f.NewValue(ir.OpPhi, diag.Span{})
	header.Append(iPhi)
	header.Append(accPhi)
	cond := header.Append(f.NewValue(ir.OpLt, diag.Span{}, iPhi, n))
	header.SetTerminator(f.NewValue(ir.OpBranch, diag.Span{}, cond), body, exit)

	ten := body.Append(f.NewValue(ir.OpConst, diag.Span{}))
	accNext = body.Append(f.NewValue(ir.OpWrapAdd, diag.Span{}, accPhi, ten))
	one := body.Append(f.NewValue(ir.OpConst, diag.Span{}))
	iNext := body.Append(f.NewValue(ir.OpWrapAdd, diag.Span{}, iPhi, one))
	body.SetTerminator(f.NewValue(ir.OpJump, diag.Span{}), header)

	iPhi.Args = []*ir.Value{zero1, iNext}
	accPhi.Args = []*ir.Value{zero2, accNext}

	exit.SetTerminator(f.NewValue(ir.OpReturn, diag.Span{}, accPhi))

	f.RecomputeUses()
	return f, header, body, iPhi, accPhi, accNext
}

// TestIntervalsKeepAPhiSourceAliveToTheBlocksEnd is a regression test for a real bug:
// a value defined in a block and used only as a successor's phi argument for the edge
// leaving that block (the common shape of a loop-carried variable on the back edge) got
// an interval that ended at its own defining instruction, because liveOut never carried
// it -- liveness's uses/defs dataflow deliberately excludes a same-block definition
// from that block's "uses" set, which is correct for propagating liveness across block
// boundaries but left this purely-local def-then-phi-use invisible to it. The allocator
// then freely reused that value's register for whatever came next in the block, and by
// the time phiCopies read it back for the edge, it held something else -- silently: two
// loop-carried values (a counter and an accumulator) collapsed onto one, and the loop
// returned the wrong one.
func TestIntervalsKeepAPhiSourceAliveToTheBlocksEnd(t *testing.T) {
	f, _, body, _, _, accNext := twoPhiLoop(t)
	n := number(f)
	ivs := intervals(f, n)

	var got *interval
	for _, iv := range ivs {
		if iv.val == accNext {
			got = iv
		}
	}
	if got == nil {
		t.Fatal("accNext has no computed interval at all")
	}
	wantEnd := n.end[body]
	if got.end < wantEnd {
		t.Errorf("accNext's interval ends at %d, want it to reach the block's end (%d): "+
			"the allocator would free its register before the back edge's phi copy reads it",
			got.end, wantEnd)
	}
}

// TestAllocateGivesLoopCarriedPhisDistinctLocations is the same bug from the
// allocator's side: with the interval fix in place, the two loop-carried values (the
// counter and the accumulator) must never be assigned the same register or slot, or
// one silently overwrites the other on every iteration.
func TestAllocateGivesLoopCarriedPhisDistinctLocations(t *testing.T) {
	f, _, _, iPhi, accPhi, _ := twoPhiLoop(t)
	a := allocate(f)

	li, ok := a.where[iPhi]
	if !ok {
		t.Fatal("the loop counter phi has no location")
	}
	la, ok := a.where[accPhi]
	if !ok {
		t.Fatal("the accumulator phi has no location")
	}
	if li == la {
		t.Errorf("the counter and the accumulator share one location (%+v): "+
			"one will silently overwrite the other on every iteration", li)
	}
}
