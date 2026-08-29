package opt

import (
	"github.com/scarypheonix/meta/internal/bytecode"
	"github.com/scarypheonix/meta/internal/ir"
)

// DeadCodeElimination removes instructions whose value nothing uses.
//
// What it may *not* remove is the interesting part. An operation that can trap on its
// operands stays even when its result is ignored, because ADR-0005 makes overflow a
// trap at every optimization level: deleting an unused `a + b` that overflows would make
// `-O1` print output that `-O0` never reaches. Anything with an effect stays for the
// obvious reason. Everything else — including an unused allocation, which is what escape
// analysis exists to remove — goes.
func DeadCodeElimination(f *ir.Func, prog *bytecode.Program) bool {
	changed := false
	for again := true; again; {
		again = false
		f.RecomputeUses()
		for _, b := range f.Blocks {
			if kept := keepLive(b.Phis); len(kept) != len(b.Phis) {
				b.Phis, again, changed = kept, true, true
			}
			if kept := keepLive(b.Instr); len(kept) != len(b.Instr) {
				b.Instr, again, changed = kept, true, true
			}
		}
	}
	return changed
}

func keepLive(list []*ir.Value) []*ir.Value {
	kept := make([]*ir.Value, 0, len(list))
	for _, v := range list {
		if v.Uses() == 0 && v.Op.Removable() {
			continue
		}
		kept = append(kept, v)
	}
	return kept
}

// RemoveUnreachableBlocks drops blocks the entry cannot reach, which is what constant
// branch folding leaves behind.
func RemoveUnreachableBlocks(f *ir.Func, prog *bytecode.Program) bool {
	return f.DropBlocks(f.Reachable())
}
