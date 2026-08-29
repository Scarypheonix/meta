package backend

import (
	"fmt"

	"github.com/scarypheonix/meta/internal/bytecode"
	"github.com/scarypheonix/meta/internal/compile"
	"github.com/scarypheonix/meta/internal/ir"
	"github.com/scarypheonix/meta/internal/x86"
)

// Lowering is one method per IR operation, working through two scratch registers.
//
// An operand may be in a register or in a frame slot, and an instruction cannot take two
// memory operands, so the pattern throughout is: bring the operands into scratch
// registers, compute, and store the result wherever the allocator put it. That costs a
// move the allocator's assignment sometimes makes unnecessary; it is also why no
// lowering rule has to reason about whether a register happens to be free.

// load brings a value into the given register.
func (e *emitter) load(dst x86.Reg, v *ir.Value) {
	l, ok := e.regs.where[v]
	if !ok {
		// A value nothing uses was never allocated. Reaching it means the lowering rule
		// read an operand the allocator did not see, which is a bug here, not a
		// recoverable condition.
		panic(fmt.Sprintf("this is a compiler bug: %s has no location", v))
	}
	if l.spilled() {
		e.a.MovRM(dst, e.mem(l))
		return
	}
	e.a.MovRR(dst, l.reg)
}

// store puts a register's contents where a value lives.
func (e *emitter) store(src x86.Reg, l loc) {
	if l.spilled() {
		e.a.MovMR(e.mem(l), src)
		return
	}
	e.a.MovRR(l.reg, src)
}

// def stores a computed result into the value's location, if anything reads it.
func (e *emitter) def(v *ir.Value, src x86.Reg) {
	l, ok := e.regs.where[v]
	if !ok {
		return // dead value: the allocator gave it nowhere, so there is nothing to keep
	}
	e.store(src, l)
}

// instr lowers one instruction.
func (e *emitter) instr(v *ir.Value) error {
	switch v.Op {
	case ir.OpConst:
		return e.constant(v)

	case ir.OpUnit:
		// Unit carries no information; zero is as good a bit pattern as any, and
		// spec/11-codegen.md says a caller must not read it.
		e.a.XorRR(scratchA, scratchA)
		e.def(v, scratchA)

	case ir.OpTrue:
		e.a.MovRI(scratchA, 1)
		e.def(v, scratchA)

	case ir.OpFalse:
		e.a.XorRR(scratchA, scratchA)
		e.def(v, scratchA)

	case ir.OpParam:
		// Parameters were moved into place in the prologue.

	case ir.OpAdd, ir.OpSub, ir.OpMul:
		return e.trappingArith(v)

	case ir.OpWrapAdd, ir.OpWrapSub, ir.OpWrapMul:
		return e.wrappingArith(v)

	case ir.OpDiv, ir.OpRem:
		return e.division(v)

	case ir.OpNeg:
		e.load(scratchA, v.Args[0])
		e.a.Neg(scratchA)
		e.trapIf(x86.Overflow, "arithmetic overflow", v)
		e.def(v, scratchA)

	case ir.OpNot:
		// `!` on a bool: flip the low bit rather than every bit, so the result stays 0
		// or 1 and can be compared and stored like any other bool.
		e.load(scratchA, v.Args[0])
		e.a.XorRI(scratchA, 1)
		e.def(v, scratchA)

	case ir.OpAnd, ir.OpOr, ir.OpXor:
		return e.bitwise(v)

	case ir.OpShl, ir.OpShr:
		return e.shift(v)

	case ir.OpEq, ir.OpNe, ir.OpLt, ir.OpLe, ir.OpGt, ir.OpGe:
		return e.compare(v)

	case ir.OpFunc:
		// A function value is its entry address. Nothing but a direct call consumes one
		// yet; a closure needs an object, which is Phase 5's remaining aggregate work.
		if v.Const < 0 || v.Const >= len(e.fnLabels) {
			return fmt.Errorf("this is a compiler bug: function index %d is out of range", v.Const)
		}
		e.a.LeaLabel(scratchA, e.fnLabels[v.Const])
		e.def(v, scratchA)

	case ir.OpCall:
		return e.call(v)

	case ir.OpCallBuiltin:
		return e.builtin(v)

	case ir.OpToStr:
		return e.toStr(v)

	case ir.OpTrap:
		msg := e.prog.Consts[v.Const].Str
		e.trapWith(e.trapMessage(msg, v.Span))

	default:
		return fmt.Errorf("unimplemented: the native backend does not lower %s yet", v.Op)
	}
	return nil
}

func (e *emitter) constant(v *ir.Value) error {
	if v.Const < 0 || v.Const >= len(e.prog.Consts) {
		return fmt.Errorf("this is a compiler bug: constant index %d is out of range", v.Const)
	}
	c := e.prog.Consts[v.Const]
	switch c.Kind {
	case bytecode.ConstInt, bytecode.ConstChar, bytecode.ConstFloat:
		// Integers, chars and floats are all one word: the float's IEEE bits travel in a
		// general-purpose register and move to SSE only for arithmetic.
		e.a.MovRI(scratchA, c.Bits)
		e.def(v, scratchA)
	case bytecode.ConstString:
		// A literal is a String object already built in read-only data, so it costs a
		// pointer rather than an allocation.
		e.a.MovRI(scratchA, e.stringConst(v.Const).addr)
		e.def(v, scratchA)
	default:
		return fmt.Errorf("unimplemented: a constant of kind %d in native code", c.Kind)
	}
	return nil
}

// trappingArith lowers `+`, `-` and `*`, which stop the program on overflow at every
// optimization level (ADR-0005). The hardware sets OF for exactly these cases, so the
// check is one conditional jump per operation.
func (e *emitter) trappingArith(v *ir.Value) error {
	e.load(scratchA, v.Args[0])
	e.load(scratchB, v.Args[1])
	switch v.Op {
	case ir.OpAdd:
		e.a.AddRR(scratchA, scratchB)
	case ir.OpSub:
		e.a.SubRR(scratchA, scratchB)
	case ir.OpMul:
		e.a.ImulRR(scratchA, scratchB)
	}
	e.trapIf(x86.Overflow, "arithmetic overflow", v)
	e.def(v, scratchA)
	return nil
}

// wrappingArith lowers the explicit `wrapping_*` methods, which are the same
// instructions with no check.
func (e *emitter) wrappingArith(v *ir.Value) error {
	e.load(scratchA, v.Args[0])
	e.load(scratchB, v.Args[1])
	switch v.Op {
	case ir.OpWrapAdd:
		e.a.AddRR(scratchA, scratchB)
	case ir.OpWrapSub:
		e.a.SubRR(scratchA, scratchB)
	case ir.OpWrapMul:
		e.a.ImulRR(scratchA, scratchB)
	}
	e.def(v, scratchA)
	return nil
}

// division lowers `/` and `%`.
//
// Both checks are mandatory and neither is the hardware's: x86 raises a processor
// exception for a zero divisor and for `MIN / -1`, and a processor exception is not a
// trap with a message and exit 101. Testing first is what turns them into the behaviour
// spec/04-expressions.md requires.
func (e *emitter) division(v *ir.Value) error {
	msg := "divide by zero"
	if v.Op == ir.OpRem {
		msg = "remainder by zero"
	}

	e.load(scratchB, v.Args[1]) // the divisor
	e.a.TestRR(scratchB, scratchB)
	e.trapIf(x86.Equal, msg, v)

	// The one overflowing division: the most negative integer divided by -1 has no
	// representation.
	e.load(scratchA, v.Args[0])
	notMin := e.a.NewLabel("div_not_min")
	e.a.CmpRI(scratchB, -1)
	e.a.Jcc(x86.NotEqual, notMin)
	e.a.MovRI(x86.RAX, 1<<63)
	e.a.CmpRR(scratchA, x86.RAX)
	e.trapIf(x86.Equal, "arithmetic overflow", v)
	e.a.Bind(notMin)

	e.a.MovRR(x86.RAX, scratchA)
	e.a.Cqo()
	e.a.Idiv(scratchB)
	if v.Op == ir.OpRem {
		e.def(v, x86.RDX)
		return nil
	}
	e.def(v, x86.RAX)
	return nil
}

func (e *emitter) bitwise(v *ir.Value) error {
	e.load(scratchA, v.Args[0])
	e.load(scratchB, v.Args[1])
	switch v.Op {
	case ir.OpAnd:
		e.a.AndRR(scratchA, scratchB)
	case ir.OpOr:
		e.a.OrRR(scratchA, scratchB)
	case ir.OpXor:
		e.a.XorRR(scratchA, scratchB)
	}
	e.def(v, scratchA)
	return nil
}

// shift lowers `<<` and `>>`, which trap when the amount is not less than the width
// (spec/04-expressions.md). x86 would silently mask the count to six bits instead, which
// is a different program.
func (e *emitter) shift(v *ir.Value) error {
	e.load(scratchA, v.Args[0])
	e.load(scratchB, v.Args[1])

	e.a.CmpRI(scratchB, 64)
	e.trapIf(x86.AboveEqual, "shift amount out of range", v)

	// The count has to be in cl, and cl may be holding something the allocator put
	// there, so it is saved around the shift.
	e.a.Push(x86.RCX)
	e.a.MovRR(x86.RCX, scratchB)
	if v.Op == ir.OpShl {
		e.a.ShlCL(scratchA)
	} else {
		e.a.SarCL(scratchA)
	}
	e.a.Pop(x86.RCX)
	e.def(v, scratchA)
	return nil
}

// compare lowers `==`, `!=`, `<`, `<=`, `>` and `>=`.
//
// Which machine comparison to use depends on the operand's static type, which the IR
// carries because the bytecode was widened to carry it (bytecode.Kind): a register holds
// sixty-four bits and nothing in it says whether they are a signed integer, an unsigned
// one, or a pointer to a String.
func (e *emitter) compare(v *ir.Value) error {
	kind := bytecode.Kind(v.Const)
	var cond x86.Cond
	switch kind {
	case bytecode.KindInt, bytecode.KindChar, bytecode.KindBool, bytecode.KindUnit:
		cond = signedCond(v.Op)
	case bytecode.KindUint:
		cond = unsignedCond(v.Op)
	case bytecode.KindUnknown:
		return fmt.Errorf("this is a compiler bug: a comparison reached the backend with no operand kind")
	default:
		return fmt.Errorf("unimplemented: comparing %s values in native code", kind)
	}

	e.load(scratchA, v.Args[0])
	e.load(scratchB, v.Args[1])
	e.a.CmpRR(scratchA, scratchB)
	e.a.Setcc(cond, x86.RAX)
	e.a.Movzx8(x86.RAX, x86.RAX)
	e.def(v, x86.RAX)
	return nil
}

func signedCond(op ir.Op) x86.Cond {
	switch op {
	case ir.OpEq:
		return x86.Equal
	case ir.OpNe:
		return x86.NotEqual
	case ir.OpLt:
		return x86.Less
	case ir.OpLe:
		return x86.LessEqual
	case ir.OpGt:
		return x86.Greater
	default:
		return x86.GreaterEqual
	}
}

func unsignedCond(op ir.Op) x86.Cond {
	switch op {
	case ir.OpEq:
		return x86.Equal
	case ir.OpNe:
		return x86.NotEqual
	case ir.OpLt:
		return x86.Below
	case ir.OpLe:
		return x86.BelowEqual
	case ir.OpGt:
		return x86.Above
	default:
		return x86.AboveEqual
	}
}

// call lowers a direct call. Args[0] is the callee; the rest are the arguments.
func (e *emitter) call(v *ir.Value) error {
	callee := v.Args[0]
	if callee == nil || callee.Op != ir.OpFunc {
		return fmt.Errorf("unimplemented: an indirect call in native code")
	}
	args := v.Args[1:]
	if len(args) > len(argRegs) {
		return fmt.Errorf("unimplemented: passing %d arguments, past the %d the registers carry",
			len(args), len(argRegs))
	}
	// An argument may currently live in the register a *later* argument has to go into,
	// so the values are pushed in order and popped into place. The pushes are balanced
	// before the call, which is what keeps rsp 16-byte aligned there.
	for _, arg := range args {
		e.load(scratchA, arg)
		e.a.Push(scratchA)
	}
	for i := len(args) - 1; i >= 0; i-- {
		e.a.Pop(argRegs[i])
	}
	e.a.Call(e.fnLabels[callee.Const])
	e.def(v, x86.RAX)
	return nil
}

// builtin lowers a call to one of the compiler-provided functions.
func (e *emitter) builtin(v *ir.Value) error {
	switch v.Const {
	case compile.BuiltinPrint, compile.BuiltinPrintln:
		if len(v.Args) != 1 {
			return fmt.Errorf("this is a compiler bug: print takes one argument, got %d", len(v.Args))
		}
		e.load(x86.RDI, v.Args[0])
		if v.Const == compile.BuiltinPrintln {
			e.a.Call(e.rt.println)
		} else {
			e.a.Call(e.rt.print)
		}
		e.a.XorRR(scratchA, scratchA)
		e.def(v, scratchA)
		return nil

	case compile.BuiltinPanic:
		// `panic` traps with a message the program computed, so unlike every other trap
		// the text is not known while compiling and the runtime writes the String.
		if len(v.Args) != 1 {
			return fmt.Errorf("this is a compiler bug: panic takes one argument, got %d", len(v.Args))
		}
		e.load(x86.RDI, v.Args[0])
		e.a.MovRM(x86.RSI, x86.At(x86.RDI, objHeaderSize))
		e.a.AddRI(x86.RDI, strBytesOff)
		e.a.Call(e.rt.trap)
		e.a.Ud2()
		return nil
	}
	return fmt.Errorf("unimplemented: builtin %d in native code", v.Const)
}

// toStr renders a value as a String.
func (e *emitter) toStr(v *ir.Value) error {
	kind := bytecode.Kind(v.Const)
	switch kind {
	case bytecode.KindInt, bytecode.KindUint:
		e.load(x86.RDI, v.Args[0])
		e.a.Call(e.rt.intToStr)
		e.def(v, x86.RAX)
		return nil
	case bytecode.KindString:
		// A String renders as itself.
		e.load(scratchA, v.Args[0])
		e.def(v, scratchA)
		return nil
	case bytecode.KindUnknown:
		return fmt.Errorf("this is a compiler bug: `to_str` reached the backend with no operand kind")
	}
	return fmt.Errorf("unimplemented: `to_str` on %s in native code", kind)
}

// trapIf jumps to a trap when a condition holds, and falls through when it does not.
//
// The stub is emitted inline, jumped over, because a trap ends the program: the cost of
// the extra jump is paid once per trapping operation on the path that does not trap, and
// there is no need for an out-of-line section.
func (e *emitter) trapIf(cond x86.Cond, msg string, v *ir.Value) {
	past := e.a.NewLabel("no_trap")
	e.a.Jcc(cond.Invert(), past)
	e.trapWith(e.trapMessage(msg, v.Span))
	e.a.Bind(past)
}

// terminator lowers a block's exit, after emitting the copies its successors' φs need.
func (e *emitter) terminator(b *ir.Block) error {
	switch b.Term.Op {
	case ir.OpReturn:
		if len(b.Term.Args) > 0 && b.Term.Args[0] != nil {
			e.load(x86.RAX, b.Term.Args[0])
		}
		e.a.Jmp(e.epilogue)
		return nil

	case ir.OpJump:
		e.phiCopies(b, b.Succs[0])
		e.a.Jmp(e.blockLbl[b.Succs[0]])
		return nil

	case ir.OpBranch:
		// The φ copies differ per edge, so each edge gets its own landing pad when it
		// has any: jumping straight to a shared successor would run the wrong copies.
		e.load(scratchA, b.Term.Args[0])
		e.a.TestRR(scratchA, scratchA)

		taken := e.edgeTarget(b, b.Succs[0])
		notTaken := e.edgeTarget(b, b.Succs[1])
		e.a.Jcc(x86.NotEqual, taken)
		e.a.Jmp(notTaken)
		return nil
	}
	return fmt.Errorf("unimplemented: terminator %s in native code", b.Term.Op)
}

// edgeTarget returns a label to jump to for one edge: the successor itself when the edge
// carries no φ copies, or a small pad that performs them and then jumps.
func (e *emitter) edgeTarget(from, to *ir.Block) x86.Label {
	if len(to.Phis) == 0 {
		return e.blockLbl[to]
	}
	pad := e.a.NewLabel(fmt.Sprintf("edge %s->%s", from, to))
	e.pending = append(e.pending, edge{label: pad, from: from, to: to})
	return pad
}

// edge is a φ landing pad still to be emitted.
type edge struct {
	label    x86.Label
	from, to *ir.Block
}

// phiCopies moves each φ's incoming value into the φ's own location.
//
// The copies happen simultaneously in SSA, so they cannot be emitted as a sequence of
// moves: a φ whose source is another φ of the same block would read a value that has
// already been overwritten. Pushing every source and popping into every destination in
// reverse performs an arbitrary permutation correctly, at the cost of some stack
// traffic, and is what the bytecode emitter already does for the same reason.
func (e *emitter) phiCopies(from, to *ir.Block) {
	idx := predIndex(to, from)
	if idx < 0 || len(to.Phis) == 0 {
		return
	}
	live := make([]*ir.Value, 0, len(to.Phis))
	for _, p := range to.Phis {
		if _, ok := e.regs.where[p]; !ok {
			continue // the φ is dead, so nothing needs its value
		}
		live = append(live, p)
	}
	for _, p := range live {
		e.load(scratchA, p.Args[idx])
		e.a.Push(scratchA)
	}
	for i := len(live) - 1; i >= 0; i-- {
		e.a.Pop(scratchA)
		e.def(live[i], scratchA)
	}
}
