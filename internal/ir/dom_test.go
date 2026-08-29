package ir

import (
	"testing"

	"github.com/scarypheonix/meta/internal/diag"
)

// cfg builds a control-flow graph by hand. Each block gets a terminator matching its
// successor count, because dominance is computed over the edges and the rest of the
// optimizer expects every block to end in one.
func cfg(t *testing.T, name string, n int, edges map[int][]int) (*Func, []*Block) {
	t.Helper()
	f := NewFunc(name, 0, 0)
	blocks := []*Block{f.Entry}
	for i := 1; i < n; i++ {
		blocks = append(blocks, f.NewBlock())
	}
	for i, b := range blocks {
		var succs []*Block
		for _, s := range edges[i] {
			succs = append(succs, blocks[s])
		}
		b.SetSuccs(succs...)
		switch len(succs) {
		case 0:
			b.Append(f.NewValue(OpReturn, diag.Span{}, b.Append(f.NewValue(OpUnit, diag.Span{}))))
		case 1:
			b.Append(f.NewValue(OpJump, diag.Span{}))
		default:
			cond := b.Append(f.NewValue(OpTrue, diag.Span{}))
			b.Append(f.NewValue(OpBranch, diag.Span{}, cond))
		}
	}
	return f, blocks
}

func TestDominatorsOverADiamond(t *testing.T) {
	// 0 -> 1, 2; 1 -> 3; 2 -> 3
	f, b := cfg(t, "diamond", 4, map[int][]int{0: {1, 2}, 1: {3}, 2: {3}})
	d := ComputeDominators(f)

	if got := d.Idom[b[0]]; got != nil {
		t.Errorf("the entry's immediate dominator is %v, want nil", got)
	}
	for _, i := range []int{1, 2, 3} {
		if d.Idom[b[i]] != b[0] {
			t.Errorf("idom(b%d) = %v, want b0", i, d.Idom[b[i]])
		}
	}
	if d.Dominates(b[1], b[3]) {
		t.Error("b1 dominates b3, but control can reach b3 through b2")
	}
	if !d.Dominates(b[0], b[3]) || !d.Dominates(b[3], b[3]) {
		t.Error("dominance must be reflexive and hold from the entry")
	}
}

func TestDominatorsOverAChainWithABypass(t *testing.T) {
	// 0 -> 1, 3; 1 -> 2; 2 -> 3: b1 and b2 are on one path only.
	f, b := cfg(t, "bypass", 4, map[int][]int{0: {1, 3}, 1: {2}, 2: {3}})
	d := ComputeDominators(f)

	if d.Idom[b[2]] != b[1] {
		t.Errorf("idom(b2) = %v, want b1", d.Idom[b[2]])
	}
	if d.Idom[b[3]] != b[0] {
		t.Errorf("idom(b3) = %v, want b0", d.Idom[b[3]])
	}
	if d.Dominates(b[2], b[3]) {
		t.Error("b2 dominates b3, but the edge b0 -> b3 skips it")
	}
}

func TestLoopsFindsNestedLoops(t *testing.T) {
	// 0 -> 1; 1 -> 2; 2 -> 2 (inner back edge), 2 -> 3; 3 -> 1 (outer back edge), 3 -> 4
	f, b := cfg(t, "nested", 5, map[int][]int{
		0: {1}, 1: {2}, 2: {2, 3}, 3: {1, 4},
	})
	d := ComputeDominators(f)
	loops := Loops(f, d)
	if len(loops) != 2 {
		t.Fatalf("found %d loops, want 2", len(loops))
	}

	byHeader := map[*Block]*Loop{}
	for _, l := range loops {
		byHeader[l.Header] = l
	}
	inner, ok := byHeader[b[2]]
	if !ok {
		t.Fatal("no loop is headed by b2")
	}
	outer, ok := byHeader[b[1]]
	if !ok {
		t.Fatal("no loop is headed by b1")
	}
	if len(inner.Blocks) != 1 || !inner.Blocks[b[2]] {
		t.Errorf("the inner loop contains %d blocks, want just b2", len(inner.Blocks))
	}
	for _, i := range []int{1, 2, 3} {
		if !outer.Blocks[b[i]] {
			t.Errorf("the outer loop does not contain b%d", i)
		}
	}
	if outer.Blocks[b[4]] {
		t.Error("the outer loop contains b4, which is after the loop")
	}
	if outer.Preheader != b[0] {
		t.Errorf("the outer loop's preheader is %v, want b0", outer.Preheader)
	}
}

// TestLoopWithTwoEntriesHasNoPreheader is the condition that keeps hoisting honest: with
// two edges into the header from outside, no single block runs exactly once before the
// loop, so there is nowhere safe to put a hoisted value.
func TestLoopWithTwoEntriesHasNoPreheader(t *testing.T) {
	// 0 -> 1, 2; 1 -> 3; 2 -> 3; 3 -> 3
	f, b := cfg(t, "twoentry", 4, map[int][]int{0: {1, 2}, 1: {3}, 2: {3}, 3: {3}})
	d := ComputeDominators(f)
	loops := Loops(f, d)
	if len(loops) != 1 {
		t.Fatalf("found %d loops, want 1", len(loops))
	}
	if loops[0].Header != b[3] {
		t.Fatalf("the loop is headed by %v, want b3", loops[0].Header)
	}
	if loops[0].Preheader != nil {
		t.Errorf("a loop entered from two blocks was given preheader %v", loops[0].Preheader)
	}
}

func TestValueDominanceWithinABlock(t *testing.T) {
	f := NewFunc("order", 0, 0)
	entry := f.Entry
	phi := entry.Append(f.NewValue(OpPhi, diag.Span{}))
	first := entry.Append(f.NewValue(OpUnit, diag.Span{}))
	second := entry.Append(f.NewValue(OpUnit, diag.Span{}))
	entry.Append(f.NewValue(OpReturn, diag.Span{}, second))

	d := ComputeDominators(f)
	if !d.ValueDominates(first, second) {
		t.Error("an earlier instruction does not dominate a later one in the same block")
	}
	if d.ValueDominates(second, first) {
		t.Error("a later instruction dominates an earlier one")
	}
	if !d.ValueDominates(phi, first) {
		t.Error("a φ does not dominate the instructions of its own block")
	}
	if d.ValueDominates(first, phi) {
		t.Error("an instruction dominates a φ of its own block, which runs on entry")
	}
}
