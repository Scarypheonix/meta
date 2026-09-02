package backend

import (
	"testing"

	"github.com/scarypheonix/meta/internal/bytecode"
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
	n := number(f, false)
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
	a := allocate(f, false)

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

// TestAllocateGivesEveryPhiOfABlockADistinctLocation is the same requirement one step
// further, and the bug it caught was silent for three phases.
//
// A merge block's phis are all written by one parallel copy on the edge into it, so no two
// may share a location however short one's own life is. Numbering them one after another
// did not say that: a phi nothing reads -- the unit-typed one a `let mut` assigned in both
// arms of a *nested* `if` leaves behind -- was a one-point interval, expired before the
// next phi's began, and handed its register to a later phi of the same block. Both were
// then written through that register and the first one won, so the later phi's value was
// whatever the dead one carried: `unit`, which is zero, which is a null reference.
//
// The shape below is the smallest one that produced it: four values merged at a block one
// of whose phis is dead.
func TestAllocateGivesEveryPhiOfABlockADistinctLocation(t *testing.T) {
	f := ir.NewFunc("merge", 1, 0)
	entry := f.Entry
	left := f.NewBlock()
	right := f.NewBlock()
	join := f.NewBlock()

	cond := entry.Append(f.NewValue(ir.OpParam, diag.Span{}))
	entry.SetTerminator(f.NewValue(ir.OpBranch, diag.Span{}, cond), left, right)

	const arms = 4
	var lefts, rights []*ir.Value
	for i := 0; i < arms; i++ {
		lefts = append(lefts, left.Append(f.NewValue(ir.OpConst, diag.Span{})))
		rights = append(rights, right.Append(f.NewValue(ir.OpConst, diag.Span{})))
	}
	left.SetTerminator(f.NewValue(ir.OpJump, diag.Span{}), join)
	right.SetTerminator(f.NewValue(ir.OpJump, diag.Span{}), join)

	var phis []*ir.Value
	for i := 0; i < arms; i++ {
		p := f.NewValue(ir.OpPhi, diag.Span{})
		p.Args = []*ir.Value{lefts[i], rights[i]}
		join.Append(p)
		phis = append(phis, p)
	}
	// Only the last phi is read. The first three are dead, which is exactly the case that
	// used to collapse their intervals to a point.
	join.SetTerminator(f.NewValue(ir.OpReturn, diag.Span{}, phis[arms-1]))

	// The invariant is on the intervals, not on which registers a particular free list
	// happened to hand out: two phis of one block must overlap, so that no linear scan,
	// under no amount of register pressure, can ever give them the same location.
	n := number(f, false)
	byVal := map[*ir.Value]*interval{}
	for _, iv := range intervals(f, n) {
		byVal[iv.val] = iv
	}
	for i, p := range phis {
		for j := i + 1; j < len(phis); j++ {
			a, b := byVal[p], byVal[phis[j]]
			if a == nil || b == nil {
				t.Fatalf("phi %d or %d has no interval at all", i, j)
			}
			if a.end < b.start || b.end < a.start {
				t.Errorf("phi %d [%d,%d] and phi %d [%d,%d] do not overlap: "+
					"the allocator may give them one location, and the parallel copy "+
					"into the block writes both through it",
					i, a.start, a.end, j, b.start, b.end)
			}
		}
	}

	al := allocate(f, false)
	seen := map[loc]int{}
	for i, p := range phis {
		l, ok := al.where[p]
		if !ok {
			continue // no location at all is fine: nothing is written to it
		}
		if prev, dup := seen[l]; dup {
			t.Errorf("phi %d and phi %d share location %+v: one parallel copy writes "+
				"both, so whichever lands first is lost", prev, i, l)
		}
		seen[l] = i
	}
}

// TestReferenceSpillSlotsAreContiguousAndBelowRawOnes is ADR-0021's own regression
// test: spec/11-codegen.md's "Stack frames" requires every reference-kind spill slot to
// sit below every raw one, so a stack map can describe the reference area as one offset
// and one count instead of a bitmap. Twelve values (six raw, six reference), all kept
// live to one final instruction that uses every one of them, force more spilling than
// the four callee-saved registers can absorb -- each value crosses the call the tuple
// construction itself lowers to (clobbersCallerSaved), so every one of them prefers a
// callee-saved register or a spill slot, never a caller-saved register.
func TestReferenceSpillSlotsAreContiguousAndBelowRawOnes(t *testing.T) {
	f := ir.NewFunc("many", 0, 0)
	entry := f.Entry

	const nRaw, nRef = 6, 6
	var raws, refs []*ir.Value
	for i := 0; i < nRaw; i++ {
		// OpFunc needs no operands and staticKind gives it KindInt unconditionally, so
		// it is a raw value with nothing else to set up.
		raws = append(raws, entry.Append(f.NewValue(ir.OpFunc, diag.Span{})))
	}
	for i := 0; i < nRef; i++ {
		// OpStruct's Const would normally be a real layout.TypeID, but staticKind
		// reports KindRef for it unconditionally, so no registry is needed here.
		refs = append(refs, entry.Append(f.NewValue(ir.OpStruct, diag.Span{})))
	}
	all := append(append([]*ir.Value{}, raws...), refs...)
	tup := entry.Append(f.NewValue(ir.OpTuple, diag.Span{}, all...))
	entry.SetTerminator(f.NewValue(ir.OpReturn, diag.Span{}, tup))
	f.RecomputeUses()

	propagateKinds(f, &bytecode.Program{})
	a := allocate(f, false)

	if a.slots == 0 {
		t.Fatal("expected at least some spilling: twelve values share four callee-saved registers")
	}

	var refSpills, rawSpills int
	for _, v := range refs {
		if l, ok := a.where[v]; ok && l.spilled() {
			refSpills++
			if l.slot > a.refSlots {
				t.Errorf("a reference-kind value spilled to slot %d, past refSlots=%d", l.slot, a.refSlots)
			}
		}
	}
	for _, v := range raws {
		if l, ok := a.where[v]; ok && l.spilled() {
			rawSpills++
			if l.slot <= a.refSlots {
				t.Errorf("a raw value spilled to slot %d, at or below refSlots=%d", l.slot, a.refSlots)
			}
		}
	}
	if refSpills == 0 || rawSpills == 0 {
		t.Fatalf("test did not force both groups to spill (refSpills=%d rawSpills=%d); "+
			"widen it rather than trusting a result that never exercised the boundary",
			refSpills, rawSpills)
	}
	if a.refSlots != refSpills {
		t.Errorf("refSlots = %d, want exactly %d (the number of spilled reference values)", a.refSlots, refSpills)
	}
}

// TestCallSiteRegsNamesOnlyTheLiveReferenceInACalleeSavedRegister is a stack map's
// register-root set, computed directly: of two values both live across the same call
// (a reference and a raw one, both necessarily in callee-saved registers per ADR-0018's
// own allocation invariant), callSiteRegs at that call must name the reference's
// register and never the raw one's.
func TestCallSiteRegsNamesOnlyTheLiveReferenceInACalleeSavedRegister(t *testing.T) {
	f := ir.NewFunc("regroot", 0, 0)
	entry := f.Entry
	ref := entry.Append(f.NewValue(ir.OpStruct, diag.Span{})) // KindRef
	raw := entry.Append(f.NewValue(ir.OpFunc, diag.Span{}))   // KindInt
	call := entry.Append(f.NewValue(ir.OpToStr, diag.Span{}, raw))
	tup := entry.Append(f.NewValue(ir.OpTuple, diag.Span{}, ref, raw, call))
	entry.SetTerminator(f.NewValue(ir.OpReturn, diag.Span{}, tup))
	f.RecomputeUses()

	propagateKinds(f, &bytecode.Program{})
	a := allocate(f, false)

	refLoc, ok := a.where[ref]
	if !ok || refLoc.spilled() {
		t.Fatalf("ref did not get a register (loc=%+v, ok=%v); the test needs it to, to mean anything", refLoc, ok)
	}
	rawLoc, ok := a.where[raw]
	if !ok || rawLoc.spilled() {
		t.Fatalf("raw did not get a register (loc=%+v, ok=%v); the test needs it to, to mean anything", rawLoc, ok)
	}

	regs, ok := a.callSiteRegs[call]
	if !ok {
		t.Fatal("call has no entry in callSiteRegs at all")
	}
	if len(regs) != 1 || regs[0] != refLoc.reg {
		t.Errorf("callSiteRegs[call] = %v, want exactly [%v] (ref's register, not raw's %v)",
			regs, refLoc.reg, rawLoc.reg)
	}
}

// TestAllocatePanicsRatherThanGuessAnUnknownKindAcrossACall is the other side of
// ADR-0021's soundness argument: a value whose kind propagateKinds could not resolve,
// forced to spill while live across a call, must never be silently grouped as "raw" --
// wrong for an actual reference, it would make a future collection corrupt memory
// instead of merely computing a worse stack map. This constructs exactly that gap by
// hand (a parameter with no seed data, i.e. built from a Fn with an empty ParamKinds)
// rather than relying on one still existing somewhere in kinds.go's real coverage.
func TestAllocatePanicsRatherThanGuessAnUnknownKindAcrossACall(t *testing.T) {
	f := ir.NewFunc("gap", 1, 0)
	entry := f.Entry
	// A real ir.Build would seed this from bytecode.Fn.ParamKinds[0]; built by hand and
	// left unset, its Kind is the zero value, KindUnknown.
	p := entry.Append(f.NewValue(ir.OpParam, diag.Span{}))

	// Six more live values, plus p, all used by the tuple construction below -- which
	// itself lowers to a call (clobbersCallerSaved) -- forces p to spill while live
	// across it, without needing a second function to call.
	var others []*ir.Value
	for i := 0; i < 6; i++ {
		others = append(others, entry.Append(f.NewValue(ir.OpFunc, diag.Span{})))
	}
	tup := entry.Append(f.NewValue(ir.OpTuple, diag.Span{}, append([]*ir.Value{p}, others...)...))
	entry.SetTerminator(f.NewValue(ir.OpReturn, diag.Span{}, tup))
	f.RecomputeUses()

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("allocate did not panic on a call-spanning value with no known kind")
		}
	}()
	allocate(f, false)
	t.Fatal("unreachable: allocate should have panicked")
}

// TestPreemptionMakesABackEdgeACallSite pins down what the preemption check costs and who
// pays it (sched.go, spec/11-codegen.md).
//
// A back edge in a program that can spawn is a call site: the check calls into the
// scheduler, so a value carried around the loop has to survive a call, which means a
// callee-saved register or a frame slot and never a caller-saved one. A program with no
// `spawn` in it has nothing that could want the processor, so its loops keep the
// caller-saved registers they had before Phase 6 -- which is the whole reason the flag
// exists rather than the check being unconditional.
func TestPreemptionMakesABackEdgeACallSite(t *testing.T) {
	f, _, body, iPhi, accPhi, _ := twoPhiLoop(t)

	plain := allocate(f, false)
	if len(plain.backEdges) != 0 {
		t.Errorf("a program that cannot spawn marked %d back edges as call sites", len(plain.backEdges))
	}

	preemptible := allocate(f, true)
	if !preemptible.backEdges[body] {
		t.Fatalf("the loop body's back edge is not a call site: %v", preemptible.backEdges)
	}
	for name, v := range map[string]*ir.Value{"counter": iPhi, "accumulator": accPhi} {
		l, ok := preemptible.where[v]
		if !ok {
			t.Fatalf("the %s phi has no location", name)
		}
		if !l.spilled() && !isCalleeSaved(l.reg) {
			t.Errorf("the %s is carried around the loop in %v, which the preemption check clobbers",
				name, l.reg)
		}
	}
}
