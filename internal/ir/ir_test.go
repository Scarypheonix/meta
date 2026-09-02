package ir

import (
	"testing"

	"github.com/scarypheonix/meta/internal/diag"
)

// CheckPhis is the invariant every optimizer pass has to preserve: a φ's operands are
// positional, so there must be exactly one per predecessor. It runs after every pass
// (internal/opt's runPasses), where it names the pass that broke it rather than leaving a
// wrong value for an engine to trip over three passes later.
func TestCheckPhisCatchesAMismatchedOperandCount(t *testing.T) {
	f := NewFunc("f", 0, 0)
	entry := f.Entry
	left, right, merge := f.NewBlock(), f.NewBlock(), f.NewBlock()
	left.Seal()
	right.Seal()
	merge.Seal()

	cond := entry.Append(f.NewValue(OpTrue, diag.Span{}))
	entry.SetTerminator(f.NewValue(OpBranch, diag.Span{}, cond), left, right)
	a := left.Append(f.NewValue(OpTrue, diag.Span{}))
	b := right.Append(f.NewValue(OpFalse, diag.Span{}))
	left.SetTerminator(f.NewValue(OpJump, diag.Span{}), merge)
	right.SetTerminator(f.NewValue(OpJump, diag.Span{}), merge)

	phi := f.NewValue(OpPhi, diag.Span{}, a, b)
	phi.Block = merge
	merge.Phis = append(merge.Phis, phi)
	merge.SetTerminator(f.NewValue(OpReturn, diag.Span{}, phi))

	if err := CheckPhis(f); err != nil {
		t.Fatalf("a well-formed function was rejected: %v", err)
	}

	// One operand too few: exactly what a pass that adds an edge without touching the φ
	// leaves behind.
	phi.Args = phi.Args[:1]
	if err := CheckPhis(f); err == nil {
		t.Error("a φ with one operand for two predecessors was accepted")
	}

	// And a predecessor with no edge back, which is what a half-applied rewrite leaves.
	phi.Args = append(phi.Args, b)
	merge.Preds = append(merge.Preds, entry)
	phi.Args = append(phi.Args, a)
	if err := CheckPhis(f); err == nil {
		t.Error("a predecessor with no matching successor edge was accepted")
	}
}
