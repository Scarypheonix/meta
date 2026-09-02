package opt

import (
	"math"

	"github.com/scarypheonix/meta/internal/arith"
	"github.com/scarypheonix/meta/internal/bytecode"
	"github.com/scarypheonix/meta/internal/ir"
)

// ConstantFold evaluates operations whose operands are all constants, and folds a
// branch on a constant condition into a jump.
//
// The rule that shapes it is ADR-0005: an operation that would trap folds *to the trap*,
// not around it and not away. `let x = i64::MAX + 1;` must stop the program at every
// optimization level, so folding it produces an unconditional trap in the same place
// rather than a wrapped constant or nothing at all.
func ConstantFold(f *ir.Func, prog *bytecode.Program) bool {
	changed := false
	for _, b := range f.Blocks {
		for _, v := range b.Instr {
			if foldValue(f, b, v, prog) {
				changed = true
			}
		}
		if foldBranch(f, b) {
			changed = true
		}
	}
	return changed
}

// constOf reads a value's compile-time constant, if it has one.
func constOf(v *ir.Value, prog *bytecode.Program) (bytecode.Const, bool) {
	switch v.Op {
	case ir.OpConst:
		if v.Const < 0 || v.Const >= len(prog.Consts) {
			return bytecode.Const{}, false
		}
		return prog.Consts[v.Const], true
	case ir.OpTrue:
		return bytecode.Const{Kind: bytecode.ConstInt, Bits: 1}, true
	case ir.OpFalse:
		return bytecode.Const{Kind: bytecode.ConstInt, Bits: 0}, true
	}
	return bytecode.Const{}, false
}

func isBoolConst(v *ir.Value) (bool, bool) {
	switch v.Op {
	case ir.OpTrue:
		return true, true
	case ir.OpFalse:
		return false, true
	}
	return false, false
}

// intConst adds an integer to the pool, reusing an existing entry.
func intConst(prog *bytecode.Program, val int64) int {
	for i, c := range prog.Consts {
		if c.Kind == bytecode.ConstInt && c.Bits == uint64(val) {
			return i
		}
	}
	prog.Consts = append(prog.Consts, bytecode.Const{Kind: bytecode.ConstInt, Bits: uint64(val)})
	return len(prog.Consts) - 1
}

func floatConst(prog *bytecode.Program, val float64) int {
	bits := math.Float64bits(val)
	for i, c := range prog.Consts {
		if c.Kind == bytecode.ConstFloat && c.Bits == bits {
			return i
		}
	}
	prog.Consts = append(prog.Consts, bytecode.Const{Kind: bytecode.ConstFloat, Bits: bits})
	return len(prog.Consts) - 1
}

func stringConst(prog *bytecode.Program, s string) int {
	for i, c := range prog.Consts {
		if c.Kind == bytecode.ConstString && c.Str == s {
			return i
		}
	}
	prog.Consts = append(prog.Consts, bytecode.Const{Kind: bytecode.ConstString, Str: s})
	return len(prog.Consts) - 1
}

// foldValue rewrites one instruction in place when its operands are constant.
func foldValue(f *ir.Func, b *ir.Block, v *ir.Value, prog *bytecode.Program) bool {
	if len(v.Args) == 0 || v.Op == ir.OpPhi {
		return false
	}
	for _, a := range v.Args {
		if a == nil {
			return false
		}
	}

	if len(v.Args) == 2 {
		a, aok := constOf(v.Args[0], prog)
		c, cok := constOf(v.Args[1], prog)
		if !aok || !cok {
			return false
		}
		return foldBinary(f, b, v, a, c, prog)
	}
	if len(v.Args) == 1 {
		a, aok := constOf(v.Args[0], prog)
		if !aok {
			return false
		}
		return foldUnary(f, b, v, a, prog)
	}
	return false
}

// trapHere replaces an instruction with an unconditional trap and cuts the block short.
// Everything after it is unreachable, so the block returns and its successors lose an
// edge; the unreachable-block pass then removes whatever that orphaned.
func trapHere(f *ir.Func, b *ir.Block, v *ir.Value, prog *bytecode.Program, msg string) bool {
	v.Op = ir.OpTrap
	v.Const = stringConst(prog, msg)
	for _, a := range v.Args {
		_ = a
	}
	v.Args = nil
	b.Truncate(v)

	unit := f.NewValue(ir.OpUnit, v.Span)
	unit.Block = b
	b.Instr = append(b.Instr, unit)
	b.SetTerminator(f.NewValue(ir.OpReturn, v.Span, unit))
	return true
}

func replaceWithConst(f *ir.Func, v *ir.Value, idx int) bool {
	v.Op = ir.OpConst
	v.Const = idx
	v.Args = nil
	return true
}

func replaceWithBool(v *ir.Value, val bool) bool {
	if val {
		v.Op = ir.OpTrue
	} else {
		v.Op = ir.OpFalse
	}
	v.Args = nil
	return true
}

func foldBinary(f *ir.Func, b *ir.Block, v *ir.Value, x, y bytecode.Const, prog *bytecode.Program) bool {
	// Comparison and equality work on any constant kind.
	switch v.Op {
	case ir.OpEq, ir.OpNe, ir.OpLt, ir.OpLe, ir.OpGt, ir.OpGe:
		res, ok := foldCompare(v.Op, bytecode.Kind(v.Const), x, y)
		if !ok {
			return false
		}
		return replaceWithBool(v, res)
	}

	if x.Kind == bytecode.ConstFloat && y.Kind == bytecode.ConstFloat {
		return foldFloat(f, v, math.Float64frombits(x.Bits), math.Float64frombits(y.Bits), prog)
	}
	if x.Kind != bytecode.ConstInt || y.Kind != bytecode.ConstInt {
		return false
	}
	return foldInt(f, b, v, x.Bits, y.Bits, prog)
}

// foldInt evaluates an integer operation on two constants, at the width the instruction
// carries.
//
// The rules are internal/arith's, which is not a convenience: folding is only allowed
// because it is unobservable (spec/11-codegen.md), and a folder with its own arithmetic is
// a second definition of what `255u8 + 1` means. This one used to be 64-bit only, so `-O1`
// folded that to 256 while `-O0` trapped -- a program whose answer depended on the
// optimization level, which is the one thing the level is not allowed to change.
func foldInt(f *ir.Func, b *ir.Block, v *ir.Value, x, y uint64, prog *bytecode.Program) bool {
	k := bytecode.Kind(v.Const)
	if !k.IsInteger() {
		// An instruction with no kind is one no engine could run either; leave it for
		// the backend to reject rather than inventing a width here.
		return false
	}
	var r uint64
	var trap string
	switch v.Op {
	case ir.OpAdd:
		r, trap = arith.Add(k, x, y)
	case ir.OpSub:
		r, trap = arith.Sub(k, x, y)
	case ir.OpMul:
		r, trap = arith.Mul(k, x, y)
	case ir.OpDiv:
		r, trap = arith.Div(k, x, y)
	case ir.OpRem:
		r, trap = arith.Rem(k, x, y)
	case ir.OpAnd:
		r = arith.And(k, x, y)
	case ir.OpOr:
		r = arith.Or(k, x, y)
	case ir.OpXor:
		r = arith.Xor(k, x, y)
	case ir.OpShl:
		r, trap = arith.Shl(k, x, y)
	case ir.OpShr:
		r, trap = arith.Shr(k, x, y)
	case ir.OpWrapAdd:
		r = arith.Wrap(k, x+y)
	case ir.OpWrapSub:
		r = arith.Wrap(k, x-y)
	case ir.OpWrapMul:
		r = arith.Wrap(k, x*y)
	default:
		return false
	}
	if trap != "" {
		return trapHere(f, b, v, prog, trap)
	}
	return replaceWithConst(f, v, intConst(prog, int64(r)))
}

func foldFloat(f *ir.Func, v *ir.Value, x, y float64, prog *bytecode.Program) bool {
	// Float arithmetic is folded, but never reassociated and never contracted: ADR-0012
	// forbids both, because they would change results between optimization levels.
	switch v.Op {
	case ir.OpAddF:
		return replaceWithConst(f, v, floatConst(prog, x+y))
	case ir.OpSubF:
		return replaceWithConst(f, v, floatConst(prog, x-y))
	case ir.OpMulF:
		return replaceWithConst(f, v, floatConst(prog, x*y))
	case ir.OpDivF:
		return replaceWithConst(f, v, floatConst(prog, x/y))
	case ir.OpRemF:
		return replaceWithConst(f, v, floatConst(prog, math.Mod(x, y)))
	}
	return false
}

// foldCompare evaluates a comparison of two constants, reporting false when the pair
// cannot be compared at compile time.
func foldCompare(op ir.Op, k bytecode.Kind, x, y bytecode.Const) (bool, bool) {
	if x.Kind != y.Kind {
		return false, false
	}
	var cmp int
	switch x.Kind {
	case bytecode.ConstInt, bytecode.ConstChar:
		// Signed or unsigned as the operand kind says, which is the same question the
		// backend asks: the bits of `u64::MAX` and `-1i64` are identical and their order
		// against 1 is not.
		if x.Kind == bytecode.ConstInt && k.IsInteger() {
			switch {
			case arith.Less(k, x.Bits, y.Bits):
				cmp = -1
			case arith.Less(k, y.Bits, x.Bits):
				cmp = 1
			}
			break
		}
		a, b := int64(x.Bits), int64(y.Bits)
		switch {
		case a < b:
			cmp = -1
		case a > b:
			cmp = 1
		}
	case bytecode.ConstFloat:
		a, b := math.Float64frombits(x.Bits), math.Float64frombits(y.Bits)
		// NaN compares false against everything, including itself, so a comparison
		// involving one is only foldable for `!=`.
		if math.IsNaN(a) || math.IsNaN(b) {
			switch op {
			case ir.OpNe:
				return true, true
			case ir.OpEq, ir.OpLt, ir.OpLe, ir.OpGt, ir.OpGe:
				return false, true
			}
			return false, false
		}
		switch {
		case a < b:
			cmp = -1
		case a > b:
			cmp = 1
		}
	case bytecode.ConstString:
		switch {
		case x.Str < y.Str:
			cmp = -1
		case x.Str > y.Str:
			cmp = 1
		}
	default:
		return false, false
	}

	switch op {
	case ir.OpEq:
		return cmp == 0, true
	case ir.OpNe:
		return cmp != 0, true
	case ir.OpLt:
		return cmp < 0, true
	case ir.OpLe:
		return cmp <= 0, true
	case ir.OpGt:
		return cmp > 0, true
	case ir.OpGe:
		return cmp >= 0, true
	}
	return false, false
}

func foldUnary(f *ir.Func, b *ir.Block, v *ir.Value, x bytecode.Const, prog *bytecode.Program) bool {
	switch v.Op {
	case ir.OpNot:
		if x.Kind != bytecode.ConstInt {
			return false
		}
		return replaceWithBool(v, x.Bits == 0)
	case ir.OpNeg:
		if x.Kind != bytecode.ConstInt {
			return false
		}
		k := bytecode.Kind(v.Const)
		if !k.IsInteger() {
			return false
		}
		r, trap := arith.Neg(k, x.Bits)
		if trap != "" {
			return trapHere(f, b, v, prog, trap)
		}
		return replaceWithConst(f, v, intConst(prog, int64(r)))
	case ir.OpNegF:
		if x.Kind != bytecode.ConstFloat {
			return false
		}
		return replaceWithConst(f, v, floatConst(prog, -math.Float64frombits(x.Bits)))
	}
	return false
}

// foldBranch turns a branch on a constant condition into an unconditional jump, which
// is what makes the unreachable arm removable.
func foldBranch(f *ir.Func, b *ir.Block) bool {
	if b.Term == nil || b.Term.Op != ir.OpBranch || len(b.Succs) != 2 {
		return false
	}
	val, known := isBoolConst(b.Term.Args[0])
	if !known {
		return false
	}
	taken, dropped := b.Succs[0], b.Succs[1]
	if !val {
		taken, dropped = dropped, taken
	}
	_ = dropped
	b.SetTerminator(f.NewValue(ir.OpJump, b.Term.Span), taken)
	return true
}
