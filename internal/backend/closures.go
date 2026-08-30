package backend

import "github.com/scarypheonix/meta/internal/ir"

// resolveClosureCalls is the one native-only pass that runs over every function's IR
// before backend lowering ever sees it (buildIR calls it right after ir.Build). It
// exists to close a soundness gap ir.OpFunc's own lowering comment used to admit
// directly: a bare function value is its entry address, a raw word with no heap object
// and no runtime tag, which is fine as long as the only thing that ever happens to it
// is an immediate call -- but once such a value can flow anywhere else (a return, an
// argument, a phi, a struct field), an indirect call site has no way to tell "this
// register holds a code address" apart from "this register holds a closure object" by
// looking at the value alone, the way the VM can from a value's own runtime tag
// (internal/vm/exec.go's doCall switches on Tag; native values carry none, per ADR-0008
// keeping primitives unboxed).
//
// The fix makes the answer static instead of dynamic: after this pass, a function-typed
// SSA value is either an OpFunc value every one of whose uses is being the immediate
// callee of an OpCall (left alone: still just an address, and OpCall's existing direct
// lowering still calls it that way), or it is provably a real object -- a genuine
// OpClosure, or an OpFunc value this pass has wrapped in a one-word closure box
// (OpBoxFn) because some use of it was not an immediate call. Every OpCall whose callee
// is not (after boxing) a bare OpFunc value is then repointed at OpCallClosure, so the
// backend's own dispatch on v.Op needs no further reasoning about where the value came
// from.
//
// A boxed value and a real closure share one calling convention (lower.go's
// callClosure): the object's field 0 is always the code address to call, and the
// object reference itself travels to the callee on the stack, in the fixed spot a call
// always leaves just above the return address, never in an argument register. An
// ordinary function boxed this way was compiled with no knowledge that it might be
// called this way and simply never reads that spot (it has no OpCapture instructions);
// a real closure's body does, via OpCapture. This is what lets one convention serve
// both without the call site ever needing to know, at compile time or at run time,
// which of the two a given closure-shaped value actually is.
func resolveClosureCalls(f *ir.Func) {
	boxEscapingFuncValues(f)
	classifyCalls(f)
}

// use records one place a value is read: by, at argument position index.
type use struct {
	by    *ir.Value
	index int
}

// boxEscapingFuncValues finds every OpFunc value with a use that is not being the
// immediate callee of an OpCall, and wraps each in an OpBoxFn right after its
// definition, repointing every one of its uses (including its call uses, if it has any
// left) at the box. A value with no such use is untouched, so a function called only
// directly keeps paying nothing for this pass.
func boxEscapingFuncValues(f *ir.Func) {
	uses := map[*ir.Value][]use{}
	f.Values(func(u *ir.Value) {
		for i, a := range u.Args {
			if a != nil {
				uses[a] = append(uses[a], use{u, i})
			}
		}
	})

	var toBox []*ir.Value
	f.Values(func(v *ir.Value) {
		if v.Op != ir.OpFunc {
			return
		}
		for _, u := range uses[v] {
			if u.by.Op != ir.OpCall || u.index != 0 {
				toBox = append(toBox, v)
				return
			}
		}
	})

	for _, v := range toBox {
		boxed := f.NewValue(ir.OpBoxFn, v.Span, v)
		// ReplaceAllUses must run before boxed is spliced into the block: it rewrites
		// every argument list currently reachable through f.Blocks that names v, and
		// boxed's own Args[0] names v too. Inserting first would make it rewrite
		// boxed's own argument into itself.
		f.ReplaceAllUses(v, boxed)
		insertAfter(v.Block, v, boxed)
	}
}

// classifyCalls repoints an OpCall whose callee is not a bare OpFunc value at
// OpCallClosure. It runs after boxEscapingFuncValues, so every remaining OpCall's
// callee is either an untouched OpFunc value (every use of it was an immediate call, so
// it never got boxed) or something this function correctly recognizes needs the
// closure-object calling convention: a boxed value, a real OpClosure, or anything
// derived from one (a parameter, a phi, another call's result).
func classifyCalls(f *ir.Func) {
	f.Values(func(v *ir.Value) {
		if v.Op == ir.OpCall && len(v.Args) > 0 && v.Args[0] != nil && v.Args[0].Op != ir.OpFunc {
			v.Op = ir.OpCallClosure
		}
	})
}

// insertAfter splices v into b's own instruction list immediately following after,
// which must already be there (true of every OpFunc value: bytecode.OpFunc always
// becomes a plain instruction, never a phi or a terminator). Def-before-use only needs
// v to sit somewhere after after within the same block; immediately after is simplest
// and always correct.
func insertAfter(b *ir.Block, after, v *ir.Value) {
	v.Block = b
	for i, ins := range b.Instr {
		if ins == after {
			b.Instr = append(b.Instr, nil)
			copy(b.Instr[i+2:], b.Instr[i+1:])
			b.Instr[i+1] = v
			return
		}
	}
	b.Instr = append(b.Instr, v)
}
