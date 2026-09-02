package backend

import (
	"fmt"

	"github.com/scarypheonix/meta/internal/arith"
	"github.com/scarypheonix/meta/internal/bytecode"
	"github.com/scarypheonix/meta/internal/compile"
	"github.com/scarypheonix/meta/internal/ir"
	"github.com/scarypheonix/meta/internal/layout"
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
		// Negation overflows on exactly one operand: the type's most negative value.
		// `neg`'s own OF answers that at sixty-four bits and not below it, so a narrow
		// kind checks the result instead -- `-(-128i8)` is 128, which is not an `i8`.
		k := bytecode.Kind(v.Const)
		e.load(scratchA, v.Args[0])
		e.a.Neg(scratchA)
		if k.IsInteger() && k.Bits() < 64 {
			e.trapUnlessFits(k, scratchA, v)
		} else {
			e.trapIf(x86.Overflow, "arithmetic overflow", v)
		}
		e.def(v, scratchA)

	case ir.OpNot:
		// `!` on a bool: flip the low bit rather than every bit, so the result stays 0
		// or 1 and can be compared and stored like any other bool.
		e.load(scratchA, v.Args[0])
		e.a.XorRI(scratchA, 1)
		e.def(v, scratchA)

	case ir.OpAddF, ir.OpSubF, ir.OpMulF, ir.OpDivF:
		return e.floatArith(v)

	case ir.OpRemF:
		// Float remainder is not an SSE instruction; x87's `fprem` is the only hardware
		// form and pulling in the x87 stack for one operation is not worth it.
		return fmt.Errorf("unimplemented: float remainder in native code")

	case ir.OpNegF:
		// Negating a float flips its sign bit, which is a raw-word operation: -0.0 and
		// NaN keep their payloads, which arithmetic negation would not guarantee.
		e.load(scratchA, v.Args[0])
		e.a.MovRI(x86.RAX, 1<<63)
		e.a.XorRR(scratchA, x86.RAX)
		e.def(v, scratchA)

	case ir.OpCast:
		return e.cast(v)

	case ir.OpAnd, ir.OpOr, ir.OpXor:
		return e.bitwise(v)

	case ir.OpShl, ir.OpShr:
		return e.shift(v)

	case ir.OpEq, ir.OpNe, ir.OpLt, ir.OpLe, ir.OpGt, ir.OpGe:
		return e.compare(v)

	case ir.OpFunc:
		// A function value is its entry address. resolveClosureCalls (closures.go) has
		// already guaranteed that if this value is ever used as anything other than an
		// OpCall's own immediate callee, it was wrapped in an OpBoxFn instead -- so
		// every use reaching this case still wants exactly the raw address.
		if v.Const < 0 || v.Const >= len(e.fnLabels) {
			return fmt.Errorf("this is a compiler bug: function index %d is out of range", v.Const)
		}
		e.a.LeaLabel(scratchA, e.fnLabels[v.Const])
		e.def(v, scratchA)

	case ir.OpBoxFn:
		// A one-word closure object whose only field is Args[0]'s raw address: the same
		// shape internal/vm/fields.go's boxIfFn gives a bare function value written into
		// a reference-shaped field, built the same way any other single-field object is.
		return e.construct(v, e.prog.FnBoxType)

	case ir.OpClosure:
		return e.closure(v)

	case ir.OpCapture:
		return e.capture(v)

	case ir.OpCall:
		return e.call(v)

	case ir.OpCallClosure:
		return e.callClosure(v)

	case ir.OpCallBuiltin:
		return e.builtin(v)

	case ir.OpToStr:
		return e.toStr(v)

	case ir.OpTrap:
		msg := e.prog.Consts[v.Const].Str
		e.trapWith(e.trapMessage(msg, v.Span))

	case ir.OpStruct:
		if v.Const < 0 || v.Const >= len(e.prog.Structs) {
			return fmt.Errorf("this is a compiler bug: struct index %d is out of range", v.Const)
		}
		return e.construct(v, e.prog.Structs[v.Const])

	case ir.OpTuple:
		return e.construct(v, layout.TypeID(v.Const))

	case ir.OpVariant:
		if v.Const < 0 || v.Const >= len(e.prog.Variants) {
			return fmt.Errorf("this is a compiler bug: variant index %d is out of range", v.Const)
		}
		return e.construct(v, e.prog.Variants[v.Const].Type)

	case ir.OpGetField:
		e.load(scratchA, v.Args[0])
		e.a.MovRM(scratchB, x86.At(scratchA, fieldOffset(v.Const)))
		e.def(v, scratchB)

	case ir.OpSetField:
		e.load(scratchA, v.Args[0])
		e.load(scratchB, v.Args[1])
		e.a.MovMR(x86.At(scratchA, fieldOffset(v.Const)), scratchB)

	case ir.OpIsVariant:
		if v.Const < 0 || v.Const >= len(e.prog.Variants) {
			return fmt.Errorf("this is a compiler bug: variant index %d is out of range", v.Const)
		}
		e.load(scratchA, v.Args[0])
		e.a.MovRM(scratchB, x86.At(scratchA, 0)) // the header word
		// The low 32 bits are the object's TypeID (internal/layout.Header); AndRI's
		// 32-bit immediate sign-extends over a 64-bit operand, which would leave the
		// high bits untouched for a mask like this one, so the mask is built in a
		// register instead.
		e.a.MovRI(x86.RAX, 0xFFFFFFFF)
		e.a.AndRR(scratchB, x86.RAX)
		e.a.MovRI(x86.RAX, uint64(e.prog.Variants[v.Const].Type))
		e.a.CmpRR(scratchB, x86.RAX)
		e.a.Setcc(x86.Equal, x86.RAX)
		e.a.Movzx8(x86.RAX, x86.RAX)
		e.def(v, x86.RAX)

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
// optimization level (ADR-0005), at the operand type's own width.
//
// At sixty-four bits the hardware answers directly: OF for signed, CF for unsigned. Below
// that it does not -- `255u8 + 1` sets neither flag, because 256 fits a register perfectly
// well -- so the check is on the *result*: every operand is in range by the invariant
// spec/04-expressions.md states, so a narrow operation cannot overflow the register it is
// computed in, and "did it fit" is the only question left.
func (e *emitter) trappingArith(v *ir.Value) error {
	k := bytecode.Kind(v.Const)
	if !k.IsInteger() {
		return fmt.Errorf("this is a compiler bug: %s reached the backend with no integer kind", v.Op)
	}
	bad := e.a.NewLabel("arith_overflow")
	past := e.a.NewLabel("arith_ok")
	e.load(scratchA, v.Args[0])
	e.load(scratchB, v.Args[1])
	e.arithInto(k, arithOpOf(v.Op), bad)
	e.a.Jmp(past)
	e.a.Bind(bad)
	e.trapWith(e.trapMessage("arithmetic overflow", v.Span))
	e.a.Bind(past)
	e.def(v, scratchA)
	return nil
}

// arithInto computes `scratchA op scratchB` at kind k, leaving the result in scratchA and
// jumping to bad when it overflows. It is the one place the three forms of `+ - *` --
// trapping, checked and saturating -- agree about what overflow means.
//
// At sixty-four bits the hardware answers: OF for signed, CF for an unsigned add or
// subtract, and a non-zero high half for an unsigned multiply (`imul`'s OF answers the
// signed question instead). Below that the flags say nothing useful -- `255u8 + 1` sets
// neither, because 256 fits a register perfectly well -- so the check is on the result:
// every operand is in range by spec/04-expressions.md's invariant, so a narrow operation
// cannot overflow the register it is computed in, and "did it fit" is all that is left.
func (e *emitter) arithInto(k bytecode.Kind, op arith.Op, bad x86.Label) {
	if k.Bits() == 64 && op == arith.OpMul && !k.IsSigned() {
		e.a.MovRR(x86.RAX, scratchA)
		e.a.Mul(scratchB)
		e.a.MovRR(scratchA, x86.RAX)
		e.a.TestRR(x86.RDX, x86.RDX)
		e.a.Jcc(x86.NotEqual, bad)
		return
	}
	switch op {
	case arith.OpAdd:
		e.a.AddRR(scratchA, scratchB)
	case arith.OpSub:
		e.a.SubRR(scratchA, scratchB)
	default:
		e.a.ImulRR(scratchA, scratchB)
	}
	if k.Bits() < 64 {
		e.jumpUnlessFits(k, scratchA, bad)
		return
	}
	if k.IsSigned() {
		e.a.Jcc(x86.Overflow, bad)
		return
	}
	e.a.Jcc(x86.Carry, bad)
}

// arithOpOf names which of the three operations an IR opcode is.
func arithOpOf(op ir.Op) arith.Op {
	switch op {
	case ir.OpAdd, ir.OpWrapAdd:
		return arith.OpAdd
	case ir.OpSub, ir.OpWrapSub:
		return arith.OpSub
	}
	return arith.OpMul
}

// jumpUnlessFits jumps to bad unless the value in reg is representable in k.
func (e *emitter) jumpUnlessFits(k bytecode.Kind, reg x86.Reg, bad x86.Label) {
	e.a.MovRR(x86.RAX, reg)
	e.wrapTo(k, x86.RAX)
	e.a.CmpRR(x86.RAX, reg)
	e.a.Jcc(x86.NotEqual, bad)
}

// trapUnlessFits traps unless the value in reg is representable in k, which is only asked
// of a kind narrower than the machine word.
//
// It is `wrap and compare`: re-reading the low bits as a value of k and checking that
// nothing changed is the definition of "it fits", and it is two instructions plus a
// comparison whichever signedness k has. rax is the scratch, which the register allocator
// never hands out and nothing here holds across a call.
func (e *emitter) trapUnlessFits(k bytecode.Kind, reg x86.Reg, v *ir.Value) {
	e.a.MovRR(x86.RAX, reg)
	e.wrapTo(k, x86.RAX)
	e.a.CmpRR(x86.RAX, reg)
	e.trapIf(x86.NotEqual, "arithmetic overflow", v)
}

// wrapTo truncates reg to k's width and extends it back, which is two's-complement
// wraparound (internal/arith's Wrap, in machine code).
func (e *emitter) wrapTo(k bytecode.Kind, reg x86.Reg) {
	w := k.Bits()
	if w == 64 {
		return
	}
	// Shift the unwanted bits off the top and back down: arithmetically for a signed
	// kind, so the sign comes back, and logically for an unsigned one, so it does not.
	e.a.ShlI(reg, uint8(64-w))
	if k.IsSigned() {
		e.a.SarI(reg, uint8(64-w))
	} else {
		e.a.ShrI(reg, uint8(64-w))
	}
}

// wrappingArith lowers the explicit `wrapping_*` methods, which are the same
// instructions with no check.
func (e *emitter) wrappingArith(v *ir.Value) error {
	k := bytecode.Kind(v.Const)
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
	// Wrapping at the operand's own width: `255u8.wrapping_add(1)` is 0, not 256. At
	// sixty-four bits the register has already done it, and wrapTo is a no-op.
	if k.IsInteger() {
		e.wrapTo(k, scratchA)
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
	k := bytecode.Kind(v.Const)
	if !k.IsInteger() {
		return fmt.Errorf("this is a compiler bug: %s reached the backend with no integer kind", v.Op)
	}
	msg := "divide by zero"
	if v.Op == ir.OpRem {
		msg = "remainder by zero"
	}

	e.load(scratchB, v.Args[1]) // the divisor
	e.a.TestRR(scratchB, scratchB)
	e.trapIf(x86.Equal, msg, v)
	e.load(scratchA, v.Args[0])

	if !k.IsSigned() {
		// Unsigned division cannot overflow, and `div` is the instruction for it: `idiv`
		// on a `u64` above i64::MAX would read both operands as negative.
		e.a.MovRR(x86.RAX, scratchA)
		e.a.XorRR(x86.RDX, x86.RDX)
		e.a.Div(scratchB)
		if v.Op == ir.OpRem {
			e.def(v, x86.RDX)
			return nil
		}
		e.def(v, x86.RAX)
		return nil
	}

	// The one overflowing division: the type's most negative value divided by -1 has no
	// representation in it.
	notMin := e.a.NewLabel("div_not_min")
	e.a.CmpRI(scratchB, -1)
	e.a.Jcc(x86.NotEqual, notMin)
	e.a.MovRI(x86.RAX, arith.Min(k))
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
	k := bytecode.Kind(v.Const)
	if !k.IsInteger() {
		return fmt.Errorf("this is a compiler bug: %s reached the backend with no integer kind", v.Op)
	}
	e.load(scratchA, v.Args[0])
	e.load(scratchB, v.Args[1])

	// The amount is compared against the *operand's* width, not the register's:
	// `1u8 << 8` is out of range even though the register has plenty of room.
	e.a.CmpRI(scratchB, int32(k.Bits()))
	e.trapIf(x86.AboveEqual, "shift amount out of range", v)

	// The count has to be in cl, and cl may be holding something the allocator put
	// there, so it is saved around the shift.
	e.a.Push(x86.RCX)
	e.a.MovRR(x86.RCX, scratchB)
	switch {
	case v.Op == ir.OpShl:
		e.a.ShlCL(scratchA)
	case k.IsSigned():
		e.a.SarCL(scratchA) // arithmetic: the sign comes back
	default:
		e.a.ShrCL(scratchA) // logical: it does not
	}
	e.a.Pop(x86.RCX)
	if v.Op == ir.OpShl {
		// A left shift overflows when the bits it pushed out mattered, which at any
		// width is "the result does not fit".
		e.trapUnlessFits64(k, scratchA, scratchB, v)
	}
	e.def(v, scratchA)
	return nil
}

// trapUnlessFits64 is trapUnlessFits for a left shift, which at full width has no spare
// bits to check against: there, shifting the result back must recover the operand, and the
// amount is still in `amount` for that.
func (e *emitter) trapUnlessFits64(k bytecode.Kind, reg, amount x86.Reg, v *ir.Value) {
	if k.Bits() < 64 {
		e.trapUnlessFits(k, reg, v)
		return
	}
	e.a.MovRR(x86.RAX, reg)
	e.a.Push(x86.RCX)
	e.a.MovRR(x86.RCX, amount)
	if k.IsSigned() {
		e.a.SarCL(x86.RAX)
	} else {
		e.a.ShrCL(x86.RAX)
	}
	e.a.Pop(x86.RCX)
	e.load(scratchB, v.Args[0])
	e.a.CmpRR(x86.RAX, scratchB)
	e.trapIf(x86.NotEqual, "arithmetic overflow", v)
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
	switch {
	case kind.IsInteger():
		// A narrow integer is stored in range and extended to sixty-four bits, so the
		// width does not enter into the comparison -- only the signedness does.
		if kind.IsSigned() {
			cond = signedCond(v.Op)
		} else {
			cond = unsignedCond(v.Op)
		}
		return e.finishCompare(v, cond)
	}
	switch kind {
	case bytecode.KindChar, bytecode.KindBool, bytecode.KindUnit:
		cond = signedCond(v.Op)
	case bytecode.KindFloat:
		return e.floatCompare(v)
	case bytecode.KindRef:
		if v.Op == ir.OpEq || v.Op == ir.OpNe {
			return e.structuralCompare(v)
		}
		// `<` and friends are built in only for integers, floats, char and String
		// (spec/04-expressions.md); a struct, tuple or enum reaching here is a
		// checker bug, not a missing lowering.
		return fmt.Errorf("this is a compiler bug: ordering a %s value in native code", kind)
	case bytecode.KindString:
		if v.Op == ir.OpEq || v.Op == ir.OpNe {
			return e.structuralCompare(v)
		}
		return e.stringOrder(v)
	case bytecode.KindUnknown:
		return fmt.Errorf("this is a compiler bug: a comparison reached the backend with no operand kind")
	default:
		return fmt.Errorf("unimplemented: comparing %s values in native code", kind)
	}
	return e.finishCompare(v, cond)
}

// finishCompare emits the comparison itself, once the condition code is decided.
func (e *emitter) finishCompare(v *ir.Value, cond x86.Cond) error {
	e.load(scratchA, v.Args[0])
	e.load(scratchB, v.Args[1])
	e.a.CmpRR(scratchA, scratchB)
	e.a.Setcc(cond, x86.RAX)
	e.a.Movzx8(x86.RAX, x86.RAX)
	e.def(v, x86.RAX)
	return nil
}

// structuralCompare lowers `==` and `!=` on an aggregate or a String by calling the
// runtime's recursive equal_objects (equal.go). Both operands are pushed before the
// call, matching e.call and e.construct: the call clobbers the caller-saved registers,
// so nothing here may assume an operand's allocated location survives it.
func (e *emitter) structuralCompare(v *ir.Value) error {
	e.load(scratchA, v.Args[0])
	e.a.Push(scratchA)
	e.load(scratchA, v.Args[1])
	e.a.Push(scratchA)
	e.a.Pop(x86.RSI)
	e.a.Pop(x86.RDI)
	e.a.Call(e.rt.equalObjects)
	e.recordCall(v)
	if v.Op == ir.OpNe {
		e.a.XorRI(x86.RAX, 1)
	}
	e.def(v, x86.RAX)
	return nil
}

// stringOrder lowers `<`, `<=`, `>` and `>=` on String by calling the runtime's
// lexicographic compare_bytes (equal.go), reused rather than duplicated: it already
// returns the three-way sign `cmp`'s ordering needs (lower.go's buildOrdering), and a
// sign compared against zero with the ordinary signed condition is exactly the bool an
// operator needs.
func (e *emitter) stringOrder(v *ir.Value) error {
	e.load(scratchA, v.Args[0])
	e.a.Push(scratchA)
	e.load(scratchA, v.Args[1])
	e.a.Push(scratchA)
	e.a.Pop(x86.RSI)
	e.a.Pop(x86.RDI)
	e.a.Call(e.rt.compareBytes)
	e.recordCall(v)
	e.a.CmpRI(x86.RAX, 0)
	e.a.Setcc(signedCond(v.Op), x86.RAX)
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
//
// callee.Op is always OpFunc here: closures.go's classifyCalls repoints any OpCall
// whose callee is not a bare OpFunc value at OpCallClosure before this ever runs, so a
// callee that fails this check is this function's own caller's bug, not a program this
// backend cannot yet handle.
func (e *emitter) call(v *ir.Value) error {
	callee := v.Args[0]
	if callee == nil || callee.Op != ir.OpFunc {
		return fmt.Errorf("this is a compiler bug: a direct call's callee is not a bare function value")
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
	e.recordCall(v)
	e.def(v, x86.RAX)
	return nil
}

// closure lowers OpClosure: allocate a closure object and fill it. Field 0 is always
// the underlying function's entry address -- computed the same way OpFunc's own
// lowering computes it, not read from Args, since bytecode.OpClosure never pushes it --
// and fields 1.. are the captures, in Args order. The captures are read after the
// allocator call for the same reason construct's fields are: the call can collect, and a
// capture parked on the raw stack across it is not in the collector's root set.
func (e *emitter) closure(v *ir.Value) error {
	if v.Const < 0 || v.Const >= len(e.prog.Closures) {
		return fmt.Errorf("this is a compiler bug: closure index %d is out of range", v.Const)
	}
	ci := e.prog.Closures[v.Const]
	desc := e.prog.Types.Get(ci.Type)
	e.a.MovRI(x86.RDI, desc.Words)
	e.a.MovRI(x86.RSI, uint64(ci.Type))
	e.a.Call(e.rt.alloc)
	e.recordCall(v)
	e.a.MovRR(scratchB, x86.RAX) // the new object's reference, held while the fields land
	for i, a := range v.Args {
		e.load(scratchA, a)
		e.a.MovMR(x86.At(scratchB, fieldOffset(i+1)), scratchA)
	}
	e.a.LeaLabel(scratchA, e.fnLabels[ci.FnIndex])
	e.a.MovMR(x86.At(scratchB, fieldOffset(0)), scratchA)
	e.def(v, scratchB)
	return nil
}

// capture lowers OpCapture: read one of the current closure's fields. The closure
// reference itself comes from the frame slot regalloc.go reserved for it and the
// prologue filled from [rbp+16], never from [rbp+16] again: a collection during the body
// moves the object, and only the slot -- inside the reference area the stack map
// describes -- is updated to say where it went. Field 0 is the function's own entry
// address, so a capture at index i sits at field i+1 (bytecode.OpLoadCapture's own VM
// lowering, internal/vm/exec.go, reads the same offset).
func (e *emitter) capture(v *ir.Value) error {
	if e.regs.closureSlot == 0 {
		return fmt.Errorf("this is a compiler bug: %s reads a capture in a function declaring none", e.fn.Name)
	}
	e.a.MovRM(scratchA, e.mem(inSlot(e.regs.closureSlot)))
	e.a.MovRM(scratchB, x86.At(scratchA, fieldOffset(int(v.Aux)+1)))
	e.def(v, scratchB)
	return nil
}

// callClosure lowers OpCallClosure: Args[0] is a closure-object reference -- never a
// bare code pointer, closures.go's resolveClosureCalls established that as an invariant
// before this ever runs -- and Args[1:] are the real arguments, passed exactly the way
// call passes them.
//
// The callee's code address comes from the object's field 0. The object reference
// itself also has to reach the callee, since its own body may read captures from it,
// but not through an argument register: every one may already be carrying a real
// parameter (a function is capped at 6, docs/spec/11-codegen.md, exactly how many
// argRegs has), and shifting them over would give a direct call and an indirect one to
// the same function two different parameter conventions. It goes on the stack instead,
// in the fixed spot a call always leaves just above the return address -- [rbp+16] in
// the callee's own frame, which is exactly where capture above reads it back. An
// ordinary function reached this way (Captures == 0, boxed rather than a real closure
// literal) never reads that spot at all, so leaving it there costs it nothing: one
// calling convention serves both, and the call site never has to know, at compile time
// or run time, which of the two a given closure-shaped value actually is.
func (e *emitter) callClosure(v *ir.Value) error {
	closure := v.Args[0]
	args := v.Args[1:]
	if len(args) > len(argRegs) {
		return fmt.Errorf("unimplemented: calling a closure with %d arguments, past the %d the registers carry",
			len(args), len(argRegs))
	}
	e.load(scratchA, closure)
	e.a.MovRM(scratchB, x86.At(scratchA, fieldOffset(0)))
	e.a.Push(scratchB) // the code address, saved across the argument pushes below
	e.a.Push(scratchA) // the closure reference, saved the same way
	for _, arg := range args {
		e.load(scratchA, arg)
		e.a.Push(scratchA)
	}
	for i := len(args) - 1; i >= 0; i-- {
		e.a.Pop(argRegs[i])
	}
	e.a.Pop(scratchA) // the closure reference
	e.a.Pop(scratchB) // the code address
	// rsp must be 16-byte aligned at `call`, and the closure reference is one word --
	// eight bytes short on its own -- so it goes on the stack twice. Which of the two
	// the callee reads at [rbp+16] does not matter: they are the same value, and the
	// second exists only to pay for the first.
	e.a.Push(scratchA)
	e.a.Push(scratchA)
	e.a.CallReg(scratchB)
	e.recordCall(v)
	e.a.AddRI(x86.RSP, 16)
	e.def(v, x86.RAX)
	return nil
}

// builtin lowers a call to one of the compiler-provided functions.
func (e *emitter) builtin(v *ir.Value) error {
	switch v.Const {
	case compile.BuiltinSpawn:
		// rt_spawn(closure, resultIsRef) -> handle. The handle is an integer as far as
		// the rest of the program is concerned; internal/compile wraps it in the
		// `JoinHandle[T]` the checker gave the call (spec/12-concurrency.md).
		// Two arguments: the closure, and whether its result is a reference. The second
		// is a constant internal/compile supplies, because a thread's result lives in a
		// control block that no stack map covers and only the compiler knows its T
		// (spec/12-concurrency.md, thread.go's tcbResultIsRefOff).
		if len(v.Args) != 2 {
			return fmt.Errorf("this is a compiler bug: spawn takes two arguments, got %d", len(v.Args))
		}
		e.load(x86.RDI, v.Args[0])
		e.load(x86.RSI, v.Args[1])
		e.a.Call(e.rt.threadSpawn)
		e.recordCall(v)
		e.def(v, x86.RAX)
		return nil

	case compile.BuiltinJoin:
		if len(v.Args) != 1 {
			return fmt.Errorf("this is a compiler bug: join takes one argument, got %d", len(v.Args))
		}
		// The argument is the `JoinHandle[T]` object, not the handle: the prelude's own
		// method passes `self`. The control block's address is its first field.
		e.load(x86.RDI, v.Args[0])
		e.a.MovRM(x86.RDI, x86.At(x86.RDI, objHeaderSize))
		e.a.Call(e.rt.threadJoin)
		e.recordCall(v)
		e.schedStatus(v, "handle already joined")
		e.def(v, x86.RDX)
		return nil

	case compile.BuiltinChannel:
		// rt_chan_new(capacity, elemIsRef) -> handle, with the second argument the one
		// internal/compile adds (spec/12-concurrency.md): a queued value may be a
		// reference, and a channel is raw memory the collector reads from the outside.
		if len(v.Args) != 2 {
			return fmt.Errorf("this is a compiler bug: channel takes two arguments, got %d", len(v.Args))
		}
		e.load(x86.RDI, v.Args[0])
		e.load(x86.RSI, v.Args[1])
		e.a.Call(e.rt.chanNew)
		e.recordCall(v)
		e.schedStatus(v, "channel capacity is negative")
		e.def(v, x86.RDX)
		return nil

	case compile.BuiltinSend:
		// The argument is the `Sender[T]` object; the channel is its first field, exactly
		// as `join`'s handle is.
		if len(v.Args) != 2 {
			return fmt.Errorf("this is a compiler bug: send takes two arguments, got %d", len(v.Args))
		}
		e.load(x86.RDI, v.Args[0])
		e.a.MovRM(x86.RDI, x86.At(x86.RDI, objHeaderSize))
		e.load(x86.RSI, v.Args[1])
		e.a.Call(e.rt.chanSend)
		e.recordCall(v)
		e.schedStatus(v, "send on a closed channel")
		e.a.XorRR(scratchA, scratchA)
		e.def(v, scratchA)
		return nil

	case compile.BuiltinAwait:
		if len(v.Args) != 1 {
			return fmt.Errorf("this is a compiler bug: await takes one argument, got %d", len(v.Args))
		}
		e.load(x86.RDI, v.Args[0])
		e.a.MovRM(x86.RDI, x86.At(x86.RDI, objHeaderSize))
		e.a.Call(e.rt.chanRecv)
		e.recordCall(v)
		e.schedStatus(v, "receive on a channel that does not exist")
		e.def(v, x86.RDX)
		return nil

	case compile.BuiltinTaken:
		// The value the receive above took, held on this thread until now so that asking
		// "is there one?" and taking it are one step (the prelude's own `recv`).
		if len(v.Args) != 1 {
			return fmt.Errorf("this is a compiler bug: taken takes one argument, got %d", len(v.Args))
		}
		e.a.Call(e.rt.chanTaken)
		e.recordCall(v)
		e.schedStatus(v, "no value was taken from this channel")
		e.def(v, x86.RDX)
		return nil

	case compile.BuiltinCloseChan:
		if len(v.Args) != 1 {
			return fmt.Errorf("this is a compiler bug: close takes one argument, got %d", len(v.Args))
		}
		e.load(x86.RDI, v.Args[0])
		e.a.MovRM(x86.RDI, x86.At(x86.RDI, objHeaderSize))
		e.a.Call(e.rt.chanClose)
		e.recordCall(v)
		e.schedStatus(v, "channel already closed")
		e.a.XorRR(scratchA, scratchA)
		e.def(v, scratchA)
		return nil

	case compile.BuiltinMutex:
		if len(v.Args) != 2 {
			return fmt.Errorf("this is a compiler bug: mutex takes two arguments, got %d", len(v.Args))
		}
		e.load(x86.RDI, v.Args[0])
		e.load(x86.RSI, v.Args[1])
		e.a.Call(e.rt.mutexNew)
		e.recordCall(v)
		e.def(v, x86.RAX)
		return nil

	case compile.BuiltinWithLock:
		if len(v.Args) != 2 {
			return fmt.Errorf("this is a compiler bug: with_lock takes two arguments, got %d", len(v.Args))
		}
		return e.withLock(v)

	case compile.BuiltinHash:
		// The specified hash (spec/13-collections.md): the same number on all three
		// engines, which is why the algorithm and the encoding are in the specification
		// rather than in whichever runtime got there first.
		if len(v.Args) != 1 {
			return fmt.Errorf("this is a compiler bug: hash::of takes one argument, got %d", len(v.Args))
		}
		e.load(x86.RDI, v.Args[0])
		e.a.MovRI(x86.RSI, uint64(hashKindOf(v.Args[0].Kind)))
		e.a.Call(e.rt.hashWord)
		e.recordCall(v)
		e.def(v, x86.RAX)
		return nil

	case compile.BuiltinStrLen:
		if len(v.Args) != 1 {
			return fmt.Errorf("this is a compiler bug: str::len takes one argument, got %d", len(v.Args))
		}
		e.load(x86.RDI, v.Args[0])
		e.a.Call(e.rt.strLen)
		e.def(v, x86.RAX)
		return nil

	case compile.BuiltinStrByteAt, compile.BuiltinStrCharAt, compile.BuiltinStrCharWidth:
		if len(v.Args) != 2 {
			return fmt.Errorf("this is a compiler bug: a string index takes two arguments, got %d", len(v.Args))
		}
		e.load(x86.RDI, v.Args[0])
		e.load(x86.RSI, v.Args[1])
		switch v.Const {
		case compile.BuiltinStrByteAt:
			e.a.Call(e.rt.strByteAt)
		case compile.BuiltinStrCharAt:
			e.a.Call(e.rt.strCharAt)
		default:
			e.a.Call(e.rt.strCharWidth)
		}
		e.strStatus(v)
		e.def(v, x86.RDX)
		return nil

	case compile.BuiltinStrSlice:
		if len(v.Args) != 3 {
			return fmt.Errorf("this is a compiler bug: str::slice takes three arguments, got %d", len(v.Args))
		}
		return e.strSlice(v)

	case compile.BuiltinStrConcat:
		if len(v.Args) != 2 {
			return fmt.Errorf("this is a compiler bug: str::concat takes two arguments, got %d", len(v.Args))
		}
		return e.strConcat(v)

	case compile.BuiltinReadFile, compile.BuiltinFileExists:
		if len(v.Args) != 1 {
			return fmt.Errorf("this is a compiler bug: a path operation takes one argument, got %d", len(v.Args))
		}
		e.load(x86.RDI, v.Args[0])
		if v.Const == compile.BuiltinReadFile {
			e.a.Call(e.rt.fsRead)
			// It allocates the String it reads into, so the call site is a safepoint
			// like any other allocation in user code (ADR-0021).
			e.recordCall(v)
		} else {
			e.a.Call(e.rt.fsExists)
		}
		e.def(v, x86.RAX)
		return nil

	case compile.BuiltinWriteFile:
		if len(v.Args) != 2 {
			return fmt.Errorf("this is a compiler bug: fs::write_file takes two arguments, got %d", len(v.Args))
		}
		e.load(x86.RDI, v.Args[0])
		e.load(x86.RSI, v.Args[1])
		e.a.Call(e.rt.fsWrite)
		e.def(v, x86.RAX)
		return nil

	case compile.BuiltinTakenText:
		if len(v.Args) != 0 {
			return fmt.Errorf("this is a compiler bug: fs::taken_text takes no arguments, got %d", len(v.Args))
		}
		e.a.Call(e.rt.fsTaken)
		e.def(v, x86.RAX)
		return nil

	case compile.BuiltinNewArray:
		// rt_array_new(capacity, typeid): the second argument is the layout
		// internal/compile chose for this instantiation, which is what tells the
		// collector whether the elements are references (ADR-0028).
		if len(v.Args) != 2 {
			return fmt.Errorf("this is a compiler bug: array::new takes two arguments, got %d", len(v.Args))
		}
		e.load(x86.RDI, v.Args[0])
		e.load(x86.RSI, v.Args[1])
		e.a.Call(e.rt.arrayNew)
		e.recordCall(v)
		e.refusedStatus(v, "array capacity is negative")
		e.def(v, x86.RDX)
		return nil

	case compile.BuiltinArrayLen, compile.BuiltinArrayCap:
		if len(v.Args) != 1 {
			return fmt.Errorf("this is a compiler bug: an array length takes one argument, got %d", len(v.Args))
		}
		e.load(x86.RDI, v.Args[0])
		if v.Const == compile.BuiltinArrayLen {
			e.a.Call(e.rt.arrayLen)
		} else {
			e.a.Call(e.rt.arrayCap)
		}
		e.def(v, x86.RAX)
		return nil

	case compile.BuiltinArrayAt:
		if len(v.Args) != 2 {
			return fmt.Errorf("this is a compiler bug: array::at takes two arguments, got %d", len(v.Args))
		}
		e.load(x86.RDI, v.Args[0])
		e.load(x86.RSI, v.Args[1])
		e.a.Call(e.rt.arrayAt)
		e.refusedStatus(v, "index out of range")
		e.def(v, x86.RDX)
		return nil

	case compile.BuiltinArraySet:
		if len(v.Args) != 3 {
			return fmt.Errorf("this is a compiler bug: array::set takes three arguments, got %d", len(v.Args))
		}
		e.load(x86.RDI, v.Args[0])
		e.load(x86.RSI, v.Args[1])
		e.load(x86.RDX, v.Args[2])
		e.a.Call(e.rt.arraySet)
		e.refusedStatus(v, "index out of range")
		e.a.XorRR(scratchA, scratchA)
		e.def(v, scratchA)
		return nil

	case compile.BuiltinArrayPush:
		if len(v.Args) != 2 {
			return fmt.Errorf("this is a compiler bug: array::push takes two arguments, got %d", len(v.Args))
		}
		e.load(x86.RDI, v.Args[0])
		e.load(x86.RSI, v.Args[1])
		e.a.Call(e.rt.arrayPush)
		e.def(v, x86.RAX)
		return nil

	case compile.BuiltinArrayTruncate:
		if len(v.Args) != 2 {
			return fmt.Errorf("this is a compiler bug: array::truncate takes two arguments, got %d", len(v.Args))
		}
		e.load(x86.RDI, v.Args[0])
		e.load(x86.RSI, v.Args[1])
		e.a.Call(e.rt.arrayTruncate)
		e.a.XorRR(scratchA, scratchA)
		e.def(v, scratchA)
		return nil

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
		e.recordCall(v)
		e.a.XorRR(scratchA, scratchA)
		e.def(v, scratchA)
		return nil

	case compile.BuiltinPanic:
		// `panic` traps with a message the program computed, so unlike every other trap
		// the text is not known while compiling and the runtime writes the String.
		if len(v.Args) != 1 {
			return fmt.Errorf("this is a compiler bug: panic takes one argument, got %d", len(v.Args))
		}
		suffix := e.rawString(fmt.Sprintf(" at %s\n", v.Span))
		e.load(x86.RDI, v.Args[0])
		e.a.MovRI(x86.RSI, suffix.addr)
		e.a.MovRI(x86.RDX, uint64(suffix.length))
		e.a.Call(e.rt.panic)
		e.a.Ud2()
		return nil

	case compile.BuiltinRefEq:
		// Identity, not equality: two references compare equal exactly when they name
		// the same object, which for a reference is bit-for-bit register equality —
		// no runtime call, unlike the aggregate builtins below.
		if len(v.Args) != 2 {
			return fmt.Errorf("this is a compiler bug: ref_eq takes two arguments, got %d", len(v.Args))
		}
		e.load(scratchA, v.Args[0])
		e.load(scratchB, v.Args[1])
		e.a.CmpRR(scratchA, scratchB)
		e.a.Setcc(x86.Equal, x86.RAX)
		e.a.Movzx8(x86.RAX, x86.RAX)
		e.def(v, x86.RAX)
		return nil

	case compile.BuiltinCmpInt, compile.BuiltinCmpUint, compile.BuiltinCmpFloat, compile.BuiltinCmpString:
		if len(v.Args) != 2 {
			return fmt.Errorf("this is a compiler bug: cmp takes two arguments, got %d", len(v.Args))
		}
		return e.buildOrdering(v)

	case compile.BuiltinFitsAdd, compile.BuiltinFitsSub, compile.BuiltinFitsMul:
		if len(v.Args) != 2 {
			return fmt.Errorf("this is a compiler bug: a fits-check takes two arguments, got %d", len(v.Args))
		}
		return e.fitsCheck(v)

	case compile.BuiltinSaturatingAdd, compile.BuiltinSaturatingSub, compile.BuiltinSaturatingMul:
		if len(v.Args) != 2 {
			return fmt.Errorf("this is a compiler bug: a saturating operation takes two arguments, got %d", len(v.Args))
		}
		return e.saturatingArith(v)
	}
	return fmt.Errorf("unimplemented: builtin %d in native code", v.Const)
}

// withLock lowers `Mutex::with`: take the lock, call the body with the guarded value,
// release the lock, and yield what the body returned.
//
// All three land at this one call site rather than in a runtime routine, because the middle
// one is a call into Origin code -- with its own frame, its own stack map and its own
// closure convention -- and because there is then no path out of the body that skips the
// release. A panic inside it ends the process (ADR-0026), which is the only other exit and
// needs no lock dropped.
//
// The body's call gets its own safepoint entry. It is a call in the middle of an operation
// the register allocator sees as one instruction, so the live references at it are exactly
// the ones it recorded for that instruction -- but the collector still has to find an entry
// for its return address, or a collection inside the body walks this frame as though it were
// a runtime routine's, with no roots of its own.
func (e *emitter) withLock(v *ir.Value) error {
	a := e.a

	e.load(x86.RDI, v.Args[0])
	a.MovRM(x86.RDI, x86.At(x86.RDI, objHeaderSize)) // the mutex, out of `Mutex[T]`
	a.Call(e.rt.mutexLock)
	e.recordCall(v)
	e.schedStatus(v, "mutex re-entered by the same thread")

	// The guarded value came back in rdx and is the body's only argument. The closure goes
	// on the stack where a closure's own body reads it (callClosure above), twice for the
	// alignment a call wants.
	a.MovRR(x86.RDI, x86.RDX)
	e.load(scratchA, v.Args[1])
	a.MovRM(scratchB, x86.At(scratchA, fieldOffset(0)))
	a.Push(scratchA)
	a.Push(scratchA)
	a.CallReg(scratchB)
	e.recordCall(v)
	a.AddRI(x86.RSP, 16)

	// The result rides the stack across the release, which is sound only because
	// rt_mutex_unlock cannot allocate: it drops the owner and wakes the waiters. A
	// reference parked on the raw stack across anything that *can* collect is exactly the
	// bug construct above was fixed for.
	a.Push(x86.RAX)
	a.Push(x86.RAX)
	e.load(x86.RDI, v.Args[0])
	a.MovRM(x86.RDI, x86.At(x86.RDI, objHeaderSize))
	a.Call(e.rt.mutexUnlock)
	a.Pop(x86.RAX)
	a.Pop(x86.RAX)
	e.def(v, x86.RAX)
	return nil
}

// refusedStatus traps when a runtime routine reported the one error it can have, and falls
// through when it did not. The message names the user's own line, which spans.go finds when
// the call being lowered is one the prelude makes.
func (e *emitter) refusedStatus(v *ir.Value, refused string) {
	a := e.a
	ok := a.NewLabel("refused_status_ok")
	a.TestRR(x86.RAX, x86.RAX)
	a.Jcc(x86.Equal, ok)
	e.trapAtUserSpan(v, refused)
	a.Bind(ok)
}

// strSlice and strConcat lower the two string operations that allocate.
//
// The allocation is emitted *here*, in user code, rather than inside a runtime routine --
// which is the whole reason the runtime side of each is split into a check and a leaf copy
// (strings.go's emitStrSliceCheck explains). A routine of its own would have to hold the
// source string across its own allocation, and the collector's root walk substitutes a
// no-roots-of-its-own entry for a frame with no stack map, so nothing would update it when
// a collection moved the object. At a call site in user code, `recordCall` writes a real
// entry and the register allocator has already given the operands homes the collector
// walks -- the same thing `construct` relies on for a struct's fields, and the reason both
// read their operands again *after* the allocation rather than before.
func (e *emitter) strSlice(v *ir.Value) error {
	a := e.a
	e.load(x86.RDI, v.Args[0])
	e.load(x86.RSI, v.Args[1])
	e.load(x86.RDX, v.Args[2])
	a.Call(e.rt.strSliceCheck)
	e.strStatus(v)

	a.MovRR(x86.RDI, x86.RDX) // the result's length in bytes
	a.Call(e.rt.strAlloc)
	e.recordCall(v)

	a.MovRR(scratchA, x86.RAX) // the new String, held while its bytes land
	e.load(x86.RSI, v.Args[0]) // wherever the source is *now*
	e.load(x86.RDX, v.Args[1])
	a.MovRR(x86.RDI, scratchA)
	a.Call(e.rt.strSliceInto)
	e.def(v, scratchA)
	return nil
}

func (e *emitter) strConcat(v *ir.Value) error {
	a := e.a
	e.load(scratchA, v.Args[0])
	a.MovRM(x86.RDI, x86.At(scratchA, strLenOff))
	e.load(scratchA, v.Args[1])
	a.MovRM(scratchB, x86.At(scratchA, strLenOff))
	a.AddRR(x86.RDI, scratchB)
	a.Call(e.rt.strAlloc)
	e.recordCall(v)

	a.MovRR(scratchA, x86.RAX)
	e.load(x86.RSI, v.Args[0])
	e.load(x86.RDX, v.Args[1])
	a.MovRR(x86.RDI, scratchA)
	a.Call(e.rt.strConcatInto)
	e.def(v, scratchA)
	return nil
}

// strStatus turns a string routine's status (strings.go) into one of the two traps
// spec/14-strings.md distinguishes, and falls through when the index was legal.
//
// Two messages rather than one, because a string index fails in two ways that mean
// different things: an index outside the string is arithmetic the caller got wrong, and one
// inside it that splits a character is a byte index used where a character index was meant.
func (e *emitter) strStatus(v *ir.Value) {
	a := e.a
	ok := a.NewLabel("str_status_ok")
	notBoundary := a.NewLabel("str_status_not_boundary")

	a.TestRR(x86.RAX, x86.RAX)
	a.Jcc(x86.Equal, ok)
	a.CmpRI(x86.RAX, strNotBoundary)
	a.Jcc(x86.Equal, notBoundary)
	e.trapAtUserSpan(v, "index out of range")
	a.Bind(notBoundary)
	e.trapAtUserSpan(v, "string index is not a character boundary")
	a.Bind(ok)
}

// schedStatus turns a blocking runtime routine's status (sched.go) into a trap, and falls
// through when the operation succeeded.
//
// The runtime returns a status rather than trapping itself because a trap's text names a
// source location and a runtime routine has none of its own -- the line §12's messages want
// is the programmer's, which spans.go resolves by walking the stack when the call being
// lowered is one the prelude makes.
func (e *emitter) schedStatus(v *ir.Value, refused string) {
	a := e.a
	ok := a.NewLabel("sched_status_ok")
	notRefused := a.NewLabel("sched_status_not_refused")

	a.TestRR(x86.RAX, x86.RAX)
	a.Jcc(x86.Equal, ok)
	a.CmpRI(x86.RAX, schedRefused)
	a.Jcc(x86.NotEqual, notRefused)
	e.trapAtUserSpan(v, refused)
	a.Bind(notRefused)
	e.trapAtUserSpan(v, "all threads are blocked")
	a.Bind(ok)
}

// buildOrdering lowers a `cmp` builtin: decide Less, Equal or Greater by the operand
// kind compile.cmpBuiltinFor already baked into which of the four builtins this is,
// then allocate the corresponding zero-payload Ordering variant -- the same
// zero-argument allocation internal/vm's ordering() does, just chosen by three jumps
// here instead of a Go switch there.
func (e *emitter) buildOrdering(v *ir.Value) error {
	e.load(scratchA, v.Args[0])
	e.a.Push(scratchA)
	e.load(scratchA, v.Args[1])
	e.a.Push(scratchA)
	e.a.Pop(scratchB) // b
	e.a.Pop(scratchA) // a

	less := e.a.NewLabel("cmp_less")
	greater := e.a.NewLabel("cmp_greater")
	equal := e.a.NewLabel("cmp_equal")
	done := e.a.NewLabel("cmp_done")

	switch v.Const {
	case compile.BuiltinCmpInt:
		e.a.CmpRR(scratchA, scratchB)
		e.a.Jcc(x86.Less, less)
		e.a.Jcc(x86.Greater, greater)
		e.a.Jmp(equal)

	case compile.BuiltinCmpUint:
		e.a.CmpRR(scratchA, scratchB)
		e.a.Jcc(x86.Below, less)
		e.a.Jcc(x86.Above, greater)
		e.a.Jmp(equal)

	case compile.BuiltinCmpFloat:
		e.a.MovqXR(x86.XMM0, scratchA)
		e.a.MovqXR(x86.XMM1, scratchB)
		e.a.UcomisdXX(x86.XMM0, x86.XMM1)
		// Unordered (either operand NaN): internal/vm's ordering() finds both `<` and
		// `>` false and falls through to Equal, so this does too, rather than
		// inventing a fourth outcome the VM does not have.
		e.a.Jcc(x86.Parity, equal)
		e.a.Jcc(x86.Below, less)
		e.a.Jcc(x86.Above, greater)
		e.a.Jmp(equal)

	case compile.BuiltinCmpString:
		e.a.MovRR(x86.RDI, scratchA)
		e.a.MovRR(x86.RSI, scratchB)
		e.a.Call(e.rt.compareBytes)
		e.recordCall(v)
		e.a.CmpRI(x86.RAX, 0)
		e.a.Jcc(x86.Less, less)
		e.a.Jcc(x86.Greater, greater)
		e.a.Jmp(equal)

	default:
		return fmt.Errorf("this is a compiler bug: builtin %d is not a `cmp`", v.Const)
	}

	allocVariant := func(idx int) {
		e.a.XorRR(x86.RDI, x86.RDI) // Less/Equal/Greater all carry no payload
		e.a.MovRI(x86.RSI, uint64(e.prog.Variants[idx].Type))
		e.a.Call(e.rt.alloc)
		e.recordCall(v)
	}

	e.a.Bind(less)
	allocVariant(e.prog.Prelude.Less)
	e.a.Jmp(done)
	e.a.Bind(greater)
	allocVariant(e.prog.Prelude.Greater)
	e.a.Jmp(done)
	e.a.Bind(equal)
	allocVariant(e.prog.Prelude.Equal)

	e.a.Bind(done)
	e.def(v, x86.RAX)
	return nil
}

// fieldOffset is where payload word i sits, relative to an object reference (which
// points at the header, spec/11-codegen.md's "Object layout in native code").
func fieldOffset(i int) int32 { return int32(objHeaderSize + wordSize*i) }

// construct lowers OpStruct, OpTuple and OpVariant: allocate an object of the given
// exact layout (ADR-0019) and fill its fields from the operands, in order.
//
// Every field value is read from its own home *after* the allocator call, never parked
// on the stack across it. The allocator can collect (ADR-0022), and a collection moves
// objects: the collector's root set is this frame's tracked registers and its
// reference-kind spill slots (ADR-0021's stack map), and a reference pushed to the raw
// stack is in neither, so it would come back pointing into the space just vacated.
// Reading afterwards is also what makes the clobbering harmless: an operand of a
// call-clobbering operation is live across that call by construction, so regalloc.go
// already gave it a callee-saved register or a spill slot, both of which the collector
// updates in place.
func (e *emitter) construct(v *ir.Value, t layout.TypeID) error {
	desc := e.prog.Types.Get(t)
	for i, a := range v.Args {
		if desc.Kinds[i] == layout.WordRef && a.Op == ir.OpFunc {
			// A bare top-level function reference is TagFn at the VM's level: an index,
			// not a heap object, and writing one into a reference-shaped field needs the
			// same boxing into a captureless closure the VM does (ADR-0019). Reaching
			// this branch means closures.go's resolveClosureCalls failed to box it
			// first -- every use of an OpFunc value that is not being an OpCall's own
			// immediate callee is supposed to be boxed (into exactly this shape, an
			// OpBoxFn one field wide) before construct ever sees it, field values of a
			// struct/tuple/variant construction included.
			return fmt.Errorf("this is a compiler bug: an unboxed function value reached a reference-shaped field")
		}
	}
	e.a.MovRI(x86.RDI, desc.Words)
	e.a.MovRI(x86.RSI, uint64(t))
	e.a.Call(e.rt.alloc)
	e.recordCall(v)
	e.a.MovRR(scratchB, x86.RAX) // the new object's reference, held while the fields land
	for i, a := range v.Args {
		e.load(scratchA, a)
		e.a.MovMR(x86.At(scratchB, fieldOffset(i)), scratchA)
	}
	e.def(v, scratchB)
	return nil
}

// fitsCheck lowers the predicate behind `checked_add` and its siblings: does `a op b` fit
// in the operand type? It answers a bool and builds nothing.
//
// The `Option` is internal/compile's to build, because it is the call's own instantiation:
// `Option[u8]` and `Option[i64]` are different types with different layouts (ADR-0019),
// and a runtime has one recorded variant index rather than one per instantiation.
func (e *emitter) fitsCheck(v *ir.Value) error {
	a := e.a
	k := checkedKind(v)
	e.load(scratchA, v.Args[0])
	e.load(scratchB, v.Args[1])

	no := a.NewLabel("fits_no")
	done := a.NewLabel("fits_done")
	e.arithInto(k, checkedOpOf(v.Const), no)
	a.MovRI(x86.RAX, 1)
	a.Jmp(done)
	a.Bind(no)
	a.XorRR(x86.RAX, x86.RAX)
	a.Bind(done)
	e.def(v, x86.RAX)
	return nil
}

// allocPreludeVariant allocates one variant of a prelude enum, by the index the compiler
// recorded. The payload, if any, is the caller's to write.
func (e *emitter) allocPreludeVariant(v *ir.Value, idx int) {
	info := e.prog.Types.Get(e.prog.Variants[idx].Type)
	e.a.MovRI(x86.RDI, info.Words)
	e.a.MovRI(x86.RSI, uint64(e.prog.Variants[idx].Type))
	e.a.Call(e.rt.alloc)
	e.recordCall(v)
}

// saturatingArith lowers `saturating_add`, `saturating_sub` and `saturating_mul`: the same
// instruction again, with overflow clamped to the end of the range it ran off.
//
// Which end that is follows from the sign of the *true* result, and the wrapped answer is
// exactly the thing that does not say. The operands do:
//
//   - `a + b` overflows only when the operands share a sign, and the true sum has it.
//   - `a - b` overflows only when they differ, and the true difference has `a`'s.
//   - `a * b` is negative exactly when the operands' signs differ, which is the sign of
//     their xor -- and that stays true when the product wraps.
//
// So the deciding value is computed *before* the operation, from operands the operation is
// about to consume, and read after the flag it must not disturb.
func (e *emitter) saturatingArith(v *ir.Value) error {
	a := e.a
	k := checkedKind(v)
	op := checkedOpOf(v.Const)
	e.load(scratchA, v.Args[0])
	e.load(scratchB, v.Args[1])

	// Which end an overflow ran off follows from the *operands*, which is exactly what
	// the wrapped answer cannot say -- so it is computed before the operation consumes
	// them. r9 rather than rcx: rcx is in the allocator's pool and would need saving,
	// and nothing here calls anything, so a caller-saved scratch is enough.
	//
	// For an unsigned type there is only one end to run off downwards, and only `-` can:
	// `negativeOverflow` in internal/arith says the same thing in Go.
	end := x86.R9
	if k.IsSigned() {
		a.MovRR(end, scratchA)
		if op == arith.OpMul {
			a.XorRR(end, scratchB) // a product is negative when the signs differ
		}
	} else if op == arith.OpSub {
		a.MovRI(end, ^uint64(0)) // any negative value: an unsigned subtraction clamps to 0
	} else {
		a.XorRR(end, end)
	}

	over := a.NewLabel("saturating_over")
	done := a.NewLabel("saturating_done")
	e.arithInto(k, op, over)
	a.Jmp(done)

	a.Bind(over)
	a.MovRI(scratchA, arith.Max(k))
	a.MovRI(scratchB, arith.Min(k))
	a.CmpRI(end, 0)
	a.Cmov(x86.Less, scratchA, scratchB)
	a.Bind(done)
	e.def(v, scratchA)
	return nil
}

// checkedKind is the width a checked or saturating operation answers at, which
// internal/compile put on the instruction from the receiver's own type.
func checkedKind(v *ir.Value) bytecode.Kind {
	if v.OperandKind.IsInteger() {
		return v.OperandKind
	}
	return bytecode.KindI64
}

// checkedOpOf names which operation a checked or saturating builtin index performs.
func checkedOpOf(idx int) arith.Op {
	switch idx {
	case compile.BuiltinFitsAdd, compile.BuiltinSaturatingAdd:
		return arith.OpAdd
	case compile.BuiltinFitsSub, compile.BuiltinSaturatingSub:
		return arith.OpSub
	}
	return arith.OpMul
}

// toStr renders a value as a String.
func (e *emitter) toStr(v *ir.Value) error {
	kind := bytecode.Kind(v.Const)
	if kind.IsInteger() {
		e.load(x86.RDI, v.Args[0])
		// The routine cannot ask the register whether its bits are signed, so the
		// static type tells it.
		if kind.IsSigned() {
			e.a.MovRI(x86.RSI, 1)
		} else {
			e.a.XorRR(x86.RSI, x86.RSI)
		}
		e.a.Call(e.rt.intToStr)
		e.recordCall(v)
		e.def(v, x86.RAX)
		return nil
	}
	switch kind {
	case bytecode.KindString:
		// A String renders as itself.
		e.load(scratchA, v.Args[0])
		e.def(v, scratchA)
		return nil
	case bytecode.KindBool:
		// Both results are String objects already in read-only data, so rendering a
		// bool is a conditional move between two pointers and allocates nothing.
		e.load(scratchA, v.Args[0])
		e.a.MovRI(x86.RAX, e.stringLiteral("true").addr)
		e.a.MovRI(scratchB, e.stringLiteral("false").addr)
		e.a.TestRR(scratchA, scratchA)
		e.a.Cmov(x86.Equal, x86.RAX, scratchB)
		e.def(v, x86.RAX)
		return nil
	case bytecode.KindChar:
		// A char renders as its UTF-8 encoding: one to four bytes in a fresh String
		// (spec/14-strings.md's table, read the other way round).
		e.load(x86.RDI, v.Args[0])
		e.a.Call(e.rt.charToStr)
		e.recordCall(v)
		e.def(v, x86.RAX)
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
		e.preemptCheck(b)
		e.phiCopies(b, b.Succs[0])
		e.a.Jmp(e.blockLbl[b.Succs[0]])
		return nil

	case ir.OpBranch:
		e.preemptCheck(b)
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

// preemptCheck emits the back edge's own safepoint: count down, and when the budget runs
// out let another thread run (sched.go).
//
// Nothing at all for a program that never spawns, and two instructions plus an untaken
// branch for one that does. It goes before the φ copies deliberately: the copies write into
// the loop's own carried values, and the call must not land between writing one and reading
// another.
func (e *emitter) preemptCheck(b *ir.Block) {
	if !e.preempts || !e.regs.backEdges[b] {
		return
	}
	a := e.a
	skip := a.NewLabel("preempt_skip")
	a.MovRM(x86.RAX, x86.At(x86.R15, rtBudgetOff))
	a.SubRI(x86.RAX, 1)
	a.MovMR(x86.At(x86.R15, rtBudgetOff), x86.RAX)
	a.Jcc(x86.NotEqual, skip)
	a.Call(e.rt.preempt)
	e.recordCall(b.Term)
	a.Bind(skip)
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

// floatArith lowers `+`, `-`, `*` and `/` on floats, which never trap
// (spec/04-expressions.md): overflow is an infinity and 0.0/0.0 is a NaN.
//
// Floats live in general-purpose registers like everything else and move to SSE only for
// the arithmetic itself. That costs two moves per operation and buys one register class
// in the allocator instead of two — a trade worth revisiting once anything float-heavy
// exists to measure.
func (e *emitter) floatArith(v *ir.Value) error {
	e.load(scratchA, v.Args[0])
	e.load(scratchB, v.Args[1])
	e.a.MovqXR(x86.XMM0, scratchA)
	e.a.MovqXR(x86.XMM1, scratchB)
	switch v.Op {
	case ir.OpAddF:
		e.a.AddsdXX(x86.XMM0, x86.XMM1)
	case ir.OpSubF:
		e.a.SubsdXX(x86.XMM0, x86.XMM1)
	case ir.OpMulF:
		e.a.MulsdXX(x86.XMM0, x86.XMM1)
	case ir.OpDivF:
		e.a.DivsdXX(x86.XMM0, x86.XMM1)
	}
	e.a.MovqRX(x86.RAX, x86.XMM0)
	e.def(v, x86.RAX)
	return nil
}

// floatCompare lowers a comparison on floats.
//
// IEEE comparison is not the integer one with different registers. `ucomisd` sets the
// parity flag when either operand is NaN, and every ordered comparison must be false
// then — including `==`, which is why `NaN == NaN` is false, and `!=`, which must be
// *true* for a NaN. Getting this wrong is the classic float bug, and
// spec/04-expressions.md pins the behaviour that the three engines have to agree on.
func (e *emitter) floatCompare(v *ir.Value) error {
	e.load(scratchA, v.Args[0])
	e.load(scratchB, v.Args[1])
	e.a.MovqXR(x86.XMM0, scratchA)
	e.a.MovqXR(x86.XMM1, scratchB)

	// The comparison is arranged so that the unsigned conditions read correctly:
	// ucomisd sets CF and ZF as an unsigned compare would, with PF for unordered.
	swapped := false
	switch v.Op {
	case ir.OpGt, ir.OpGe:
		// `a > b` is `b < a` with the operands exchanged, which keeps every case on the
		// Below/BelowEqual side where NaN clears the flags rather than setting them.
		e.a.UcomisdXX(x86.XMM1, x86.XMM0)
		swapped = true
	default:
		e.a.UcomisdXX(x86.XMM0, x86.XMM1)
	}

	var cond x86.Cond
	switch v.Op {
	case ir.OpEq:
		cond = x86.Equal
	case ir.OpNe:
		cond = x86.NotEqual
	case ir.OpLt, ir.OpGt:
		cond = x86.Below
	default:
		cond = x86.BelowEqual
	}
	_ = swapped

	e.a.Setcc(cond, x86.RAX)
	e.a.Movzx8(x86.RAX, x86.RAX)

	// NaN makes ucomisd set ZF, PF and CF together, so `setbe` and `sete` would report
	// true for an unordered pair. Every comparison but `!=` must be false there, and
	// `!=` must be true, so the parity flag is folded in explicitly.
	if v.Op == ir.OpNe {
		e.a.Setcc(x86.Parity, scratchA)
		e.a.Movzx8(scratchA, scratchA)
		e.a.OrRR(x86.RAX, scratchA)
	} else {
		e.a.Setcc(x86.NoParity, scratchA)
		e.a.Movzx8(scratchA, scratchA)
		e.a.AndRR(x86.RAX, scratchA)
	}
	e.def(v, x86.RAX)
	return nil
}

// cast lowers a conversion between primitive types.
func (e *emitter) cast(v *ir.Value) error {
	kind := bytecode.CastKind(v.Const)
	width := v.Aux
	bits, signed := castShape(width)

	switch kind {
	case bytecode.CastIntTrunc, bytecode.CastCharToInt:
		e.load(scratchA, v.Args[0])
		e.truncate(scratchA, bits, signed)
		e.def(v, scratchA)
		return nil

	case bytecode.CastBoolToInt:
		// A bool is already 0 or 1.
		e.load(scratchA, v.Args[0])
		e.def(v, scratchA)
		return nil

	case bytecode.CastIntToFloat:
		e.load(scratchA, v.Args[0])
		e.a.Cvtsi2sd(x86.XMM0, scratchA)
		e.a.MovqRX(x86.RAX, x86.XMM0)
		e.def(v, x86.RAX)
		return nil

	case bytecode.CastFloatWiden:
		// f32 is carried as an f64 already, so widening is the identity.
		e.load(scratchA, v.Args[0])
		e.def(v, scratchA)
		return nil
	}
	return fmt.Errorf("unimplemented: cast %d in native code", kind)
}

// truncate narrows an integer to a width, sign-extending or zero-extending to fill the
// register, which is what keeps the one-word value model consistent.
func (e *emitter) truncate(r x86.Reg, bits uint, signed bool) {
	if bits >= 64 || bits == 0 {
		return
	}
	shift := uint8(64 - bits)
	e.a.ShlI(r, shift)
	if signed {
		e.a.SarI(r, shift)
		return
	}
	e.a.ShrI(r, shift)
}

// castShape unpacks the width operand the compiler packed: the bit width, with bit 8 set
// when the target type is signed.
func castShape(width int) (bits uint, signed bool) {
	return uint(width & 0xFF), width&(1<<8) != 0
}

// stringLiteral puts a String the backend itself needs — "true", "false" — into
// read-only data, built exactly like a program's own literal.
func (e *emitter) stringLiteral(s string) staticStr {
	if got, ok := e.literals[s]; ok {
		return got
	}
	for len(e.roData)%wordSize != 0 {
		e.roData = append(e.roData, 0)
	}
	out := staticStr{addr: e.roDataAddr + uint64(len(e.roData)), length: len(s)}
	e.roData = append(e.roData, stringObject(s, e.stringType)...)
	e.literals[s] = out
	return out
}
