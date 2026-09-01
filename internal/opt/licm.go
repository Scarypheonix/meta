package opt

import (
	"github.com/scarypheonix/meta/internal/bytecode"
	"github.com/scarypheonix/meta/internal/ir"
)

// LoopInvariantCodeMotion hoists computations that do not change across a loop's
// iterations into the block before it.
//
// Two conditions are load-bearing:
//
//   - The value may not trap. Hoisting `a / b` out of a loop that never executes turns
//     a program that ran fine into one that traps on a zero divisor, which is a
//     different program at `-O2` than at `-O0`. ADR-0005 makes that a correctness
//     failure, not a quality-of-implementation choice.
//   - The loop needs a preheader: a single entry from outside, so a hoisted value runs
//     exactly once before the loop rather than on some paths and not others.
func LoopInvariantCodeMotion(f *ir.Func, prog *bytecode.Program) bool {
	d := ir.ComputeDominators(f)
	loops := ir.Loops(f, d)
	if len(loops) == 0 {
		return false
	}

	changed := false
	for _, loop := range loops {
		if loop.Preheader == nil {
			continue
		}
		// Repeat until nothing more moves: hoisting one value can make another
		// invariant, because its operand is now defined outside.
		for again := true; again; {
			again = false
			for _, b := range f.Blocks {
				if !loop.Blocks[b] {
					continue
				}
				kept := b.Instr[:0]
				for _, v := range b.Instr {
					if invariant(v, loop) {
						loop.Preheader.Instr = append(loop.Preheader.Instr, v)
						v.Block = loop.Preheader
						again, changed = true, true
						continue
					}
					kept = append(kept, v)
				}
				b.Instr = kept
			}
		}
	}
	if changed {
		f.RecomputeUses()
	}
	return changed
}

// invariant reports whether a value can be computed once before the loop instead of on
// every iteration.
func invariant(v *ir.Value, loop *ir.Loop) bool {
	if !v.Movable() {
		return false
	}
	if len(v.Args) == 0 {
		// A constant inside a loop is invariant, but moving it buys nothing and churns
		// the output of every snapshot; leave it where it is.
		return false
	}
	for _, a := range v.Args {
		if a == nil || a.Block == nil {
			return false
		}
		if loop.Blocks[a.Block] {
			return false // defined inside the loop, so it may change each iteration
		}
	}
	return true
}
