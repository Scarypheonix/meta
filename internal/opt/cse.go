package opt

import (
	"fmt"

	"github.com/scarypheonix/meta/internal/bytecode"
	"github.com/scarypheonix/meta/internal/ir"
)

// CopyPropagate removes the values that merely forward another.
//
// SSA construction leaves two kinds behind: a φ whose operands are all the same value,
// and a φ in a block with one predecessor. Braun's algorithm removes most of them as it
// builds, but folding a branch can make a φ trivial afterwards, so the pass runs in the
// pipeline rather than only at construction.
func CopyPropagate(f *ir.Func, prog *bytecode.Program) bool {
	changed := false
	for again := true; again; {
		again = false
		for _, b := range f.Blocks {
			kept := b.Phis[:0]
			for _, phi := range b.Phis {
				if same, trivial := trivialPhi(phi); trivial && same != nil {
					f.ReplaceAllUses(phi, same)
					again, changed = true, true
					continue
				}
				kept = append(kept, phi)
			}
			b.Phis = kept
		}
		f.RecomputeUses()
	}
	return changed
}

// trivialPhi reports the single distinct operand of a φ, if it has one.
func trivialPhi(phi *ir.Value) (*ir.Value, bool) {
	var same *ir.Value
	for _, op := range phi.Args {
		if op == phi || op == same {
			continue
		}
		if same != nil {
			return nil, false
		}
		same = op
	}
	return same, true
}

// CommonSubexpressions replaces a value with an earlier one computing the same thing.
//
// Two restrictions keep it correct. The earlier value must *dominate* the later one, or
// the replacement would read a value that may not have been computed. And an operation
// that allocates is never shared: two `Point { x: 1 }` literals are distinct objects,
// and `ref_eq` can tell them apart (spec/04-expressions.md), so merging them would
// change what a program observes.
func CommonSubexpressions(f *ir.Func, prog *bytecode.Program) bool {
	d := ir.ComputeDominators(f)
	// Candidates are grouped by a key describing what they compute; within a group the
	// list is scanned for one that dominates.
	groups := map[string][]*ir.Value{}
	changed := false

	for _, b := range f.Blocks {
		for _, v := range b.Instr {
			if !v.Op.Shareable() {
				continue
			}
			key := valueKey(v)
			if key == "" {
				continue
			}
			replaced := false
			for _, earlier := range groups[key] {
				if earlier == v || !d.ValueDominates(earlier, v) {
					continue
				}
				replaced = true
				// A trapping op's own instruction survives DCE even with no uses left
				// (DeadCodeElimination keeps it precisely because it can trap), so a
				// value already fully replaced in an earlier round is still here to be
				// found again -- rediscovering it is not new work. Reporting `changed`
				// only when there was a use left to replace is what makes this pass
				// idempotent once nothing is actually being rewritten any more; without
				// it, the pipeline never reaches runPasses's fixed point on a function
				// with an unused-but-still-present duplicate of a trapping expression.
				if v.Uses() > 0 {
					f.ReplaceAllUses(v, earlier)
					changed = true
				}
				break
			}
			if !replaced {
				groups[key] = append(groups[key], v)
			}
		}
	}
	if changed {
		f.RecomputeUses()
	}
	return changed
}

// valueKey describes what an instruction computes, for grouping. It returns "" for
// anything whose identity is not determined by its operation and operands.
func valueKey(v *ir.Value) string {
	key := fmt.Sprintf("%s/%d/%d", v.Op, v.Const, v.Aux)
	for _, a := range v.Args {
		if a == nil {
			return ""
		}
		key += fmt.Sprintf("/%d", a.ID)
	}
	return key
}
