package opt

import (
	"github.com/scarypheonix/meta/internal/bytecode"
	"github.com/scarypheonix/meta/internal/ir"
)

// EscapeAnalysis removes heap allocations whose object never leaves the function.
//
// ADR-0008 made every aggregate a heap object so that a moving collector never sees an
// interior pointer. This pass is what claws the cost back: an object that is built,
// read, and forgotten within one function does not need to exist at all, and each of its
// fields can be the SSA value that was stored into it.
//
// The version here handles the case that is both common and unambiguous: an allocation
// whose only uses are field reads at constant indices. Anything else counts as an
// escape — passing the object to a call, storing it into another object, returning it,
// comparing it, or writing a field after construction — because proving those safe needs
// an alias analysis this phase does not have. `ref_eq` is a builtin call, so an object
// whose identity is observed escapes automatically, which is what keeps
// spec/04-expressions.md's identity rules intact.
func EscapeAnalysis(f *ir.Func, prog *bytecode.Program) bool {
	changed := false
	for _, b := range f.Blocks {
		kept := b.Instr[:0]
		for _, v := range b.Instr {
			if !v.Op.Allocates() || v.Op == ir.OpToStr || v.Op == ir.OpClosure {
				kept = append(kept, v)
				continue
			}
			reads, ok := nonEscapingReads(f, v)
			if !ok {
				kept = append(kept, v)
				continue
			}
			// Every read becomes the value that was stored at construction.
			for _, r := range reads {
				if r.Const < 0 || r.Const >= len(v.Args) {
					ok = false
					break
				}
			}
			if !ok {
				kept = append(kept, v)
				continue
			}
			for _, r := range reads {
				f.ReplaceAllUses(r, v.Args[r.Const])
			}
			changed = true
			// The allocation itself is now unused; dead-code elimination removes it on
			// the next pass rather than this one guessing about its other uses.
			kept = append(kept, v)
		}
		b.Instr = kept
	}
	if changed {
		f.RecomputeUses()
	}
	return changed
}

// nonEscapingReads collects the field reads of an allocation, reporting false when the
// object is used in any other way.
func nonEscapingReads(f *ir.Func, alloc *ir.Value) ([]*ir.Value, bool) {
	var reads []*ir.Value
	escapes := false

	f.Values(func(u *ir.Value) {
		if u == alloc || escapes {
			return
		}
		for _, a := range u.Args {
			if a != alloc {
				continue
			}
			if u.Op == ir.OpGetField {
				reads = append(reads, u)
				continue
			}
			escapes = true
			return
		}
	})
	if escapes {
		return nil, false
	}
	return reads, true
}
