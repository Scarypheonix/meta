package vm

import (
	"fmt"
	"math"

	"github.com/scarypheonix/meta/internal/bytecode"
	"github.com/scarypheonix/meta/internal/compile"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/layout"
)

// run is the interpreter loop.
func (v *VM) run() {
	for len(v.frames) > 0 {
		f := &v.frames[len(v.frames)-1]
		if f.pc >= len(f.fn.Code) {
			// A function whose code runs off the end returns unit; the compiler always
			// emits a return, so reaching this is a compiler bug.
			panic(fmt.Sprintf("vm: fell off the end of %s", f.fn.Name))
		}
		in := f.fn.Code[f.pc]
		f.pc++

		switch in.Op {
		case bytecode.OpNop:

		case bytecode.OpConst:
			v.push(v.constValue(int(in.A), in.Span))
		case bytecode.OpUnit:
			v.push(unitVal())
		case bytecode.OpTrue:
			v.push(boolVal(true))
		case bytecode.OpFalse:
			v.push(boolVal(false))
		case bytecode.OpPop:
			v.pop()

		case bytecode.OpLoad:
			v.push(v.stack[f.base+int(in.A)])
		case bytecode.OpStore:
			v.stack[f.base+int(in.A)] = v.pop()
		case bytecode.OpLoadCapture:
			if f.closure == layout.Nil {
				panic("vm: capture read outside a closure")
			}
			tag, bits := v.heap.GetSlot(f.closure, int(in.A)+1)
			v.push(slotValue(tag, bits))

		case bytecode.OpAdd, bytecode.OpSub, bytecode.OpMul, bytecode.OpDiv, bytecode.OpRem,
			bytecode.OpAnd, bytecode.OpOr, bytecode.OpXor, bytecode.OpShl, bytecode.OpShr,
			bytecode.OpWrapAdd, bytecode.OpWrapSub, bytecode.OpWrapMul:
			b := v.pop()
			a := v.pop()
			v.push(v.intOp(in.Op, a.Int(), b.Int(), in.Span))

		case bytecode.OpNeg:
			a := v.pop()
			if a.Int() == math.MinInt64 {
				v.trap(in.Span, "arithmetic overflow")
			}
			v.push(intVal(-a.Int()))

		case bytecode.OpAddF, bytecode.OpSubF, bytecode.OpMulF, bytecode.OpDivF, bytecode.OpRemF:
			b := v.pop()
			a := v.pop()
			v.push(floatVal(floatOp(in.Op, a.Float(), b.Float())))

		case bytecode.OpNegF:
			v.push(floatVal(-v.pop().Float()))

		case bytecode.OpNot:
			v.push(boolVal(!v.pop().Bool()))

		case bytecode.OpEq, bytecode.OpNe:
			b := v.pop()
			a := v.pop()
			eq := v.equal(a, b, in.Span)
			v.push(boolVal(eq == (in.Op == bytecode.OpEq)))

		case bytecode.OpLt, bytecode.OpLe, bytecode.OpGt, bytecode.OpGe:
			b := v.pop()
			a := v.pop()
			v.push(boolVal(v.compareOp(in.Op, a, b, in.Span)))

		case bytecode.OpJump:
			f.pc = int(in.A)
		case bytecode.OpJumpIfFalse:
			if !v.pop().Bool() {
				f.pc = int(in.A)
			}
		case bytecode.OpJumpIfTrue:
			if v.pop().Bool() {
				f.pc = int(in.A)
			}

		case bytecode.OpReturn:
			result := v.pop()
			v.stack = v.stack[:f.base]
			v.frames = v.frames[:len(v.frames)-1]
			if len(v.frames) == 0 {
				return
			}
			v.push(result)

		case bytecode.OpFunc:
			v.push(Value{Tag: layout.TagFn, N: uint64(in.A)})

		case bytecode.OpCall:
			v.doCall(int(in.A), in.Span)

		case bytecode.OpCallBuiltin:
			v.callBuiltin(int(in.A), int(in.B), in.Span)

		case bytecode.OpClosure:
			v.makeClosure(int(in.A), int(in.B), in.Span)

		case bytecode.OpStruct:
			v.makeObject(v.prog.Structs[in.A], int(in.B), in.Span)

		case bytecode.OpTuple:
			v.makeObject(layout.TypeID(in.A), int(in.B), in.Span)

		case bytecode.OpVariant:
			info := v.prog.Variants[in.A]
			v.makeObject(info.Type, int(in.B), in.Span)

		case bytecode.OpGetField, bytecode.OpGetPayload, bytecode.OpGetTupleElem:
			obj := v.pop()
			if obj.Tag != layout.TagRef {
				panic("vm: field read on a value that is not an object")
			}
			tag, bits := v.heap.GetSlot(obj.R, int(in.A))
			v.push(slotValue(tag, bits))

		case bytecode.OpSetField:
			val := v.pop()
			obj := v.pop()
			if obj.Tag != layout.TagRef {
				panic("vm: field write on a value that is not an object")
			}
			v.heap.SetSlot(obj.R, int(in.A), val.Tag, slotBits(val))

		case bytecode.OpIsVariant:
			obj := v.pop()
			want := v.prog.Variants[in.A].Type
			v.push(boolVal(obj.Tag == layout.TagRef && v.heap.TypeOf(obj.R) == want))

		case bytecode.OpCast:
			v.push(v.cast(bytecode.CastKind(in.A), int(in.B), v.pop(), in.Span))

		case bytecode.OpToStr:
			s := v.display(v.pop())
			v.push(refVal(v.newString(s, in.Span)))

		case bytecode.OpTrap:
			v.trap(in.Span, "%s", v.prog.Consts[in.A].Str)

		case bytecode.OpHalt:
			v.frames = v.frames[:0]
			return

		default:
			panic(fmt.Sprintf("vm: unimplemented opcode %s", in.Op))
		}
	}
}

func (v *VM) constValue(i int, span diag.Span) Value {
	c := v.prog.Consts[i]
	switch c.Kind {
	case bytecode.ConstInt:
		return intVal(int64(c.Bits))
	case bytecode.ConstFloat:
		return Value{Tag: layout.TagFloat, N: c.Bits}
	case bytecode.ConstChar:
		return charVal(rune(c.Bits))
	default:
		return refVal(v.stringConst(i, span))
	}
}

// slotValue turns a heap slot back into a stack value.
func slotValue(tag layout.ValueTag, bits uint64) Value {
	if tag == layout.TagRef {
		return Value{Tag: tag, R: layout.Ref(bits)}
	}
	return Value{Tag: tag, N: bits}
}

// slotBits is the word to store beside a value's tag.
func slotBits(val Value) uint64 {
	if val.Tag == layout.TagRef {
		return uint64(val.R)
	}
	return val.N
}

// makeObject pops n values and builds an object from them.
func (v *VM) makeObject(t layout.TypeID, n int, span diag.Span) {
	r := v.alloc(t, uint64(n)*2, span)
	// The values are written back to front so the stack is unwound in one pass, and the
	// object is only reachable after every slot is filled -- a collection triggered by
	// a later allocation must never see a half-built object.
	for i := n - 1; i >= 0; i-- {
		val := v.pop()
		v.heap.SetSlot(r, i, val.Tag, slotBits(val))
	}
	v.push(refVal(r))
}

func (v *VM) makeClosure(fnIndex, captures int, span diag.Span) {
	t := v.prog.ClosureTypes[captures]
	r := v.alloc(t, uint64(captures+1)*2, span)
	for i := captures - 1; i >= 0; i-- {
		val := v.pop()
		v.heap.SetSlot(r, i+1, val.Tag, slotBits(val))
	}
	v.heap.SetSlot(r, 0, layout.TagFn, uint64(fnIndex))
	v.push(refVal(r))
}

// doCall invokes the callee sitting beneath argCount arguments.
func (v *VM) doCall(argCount int, span diag.Span) {
	callee := v.stack[len(v.stack)-argCount-1]

	switch callee.Tag {
	case layout.TagFn:
		idx := int(callee.N)
		fn := v.prog.Fns[idx]
		// The callee slot is overwritten by the first argument, so the frame's base is
		// where the callee was and the arguments become locals 0..n-1.
		copy(v.stack[len(v.stack)-argCount-1:], v.stack[len(v.stack)-argCount:])
		v.stack = v.stack[:len(v.stack)-1]
		v.pushFrame(idx, fn, layout.Nil, argCount, span)

	case layout.TagRef:
		tag, bits := v.heap.GetSlot(callee.R, 0)
		if tag != layout.TagFn {
			panic("vm: called an object that is not a closure")
		}
		idx := int(bits)
		fn := v.prog.Fns[idx]
		closure := callee.R
		copy(v.stack[len(v.stack)-argCount-1:], v.stack[len(v.stack)-argCount:])
		v.stack = v.stack[:len(v.stack)-1]
		v.pushFrame(idx, fn, closure, argCount, span)

	default:
		panic("vm: called something that is not a function")
	}
}

func (v *VM) intOp(op bytecode.Op, a, b int64, span diag.Span) Value {
	switch op {
	case bytecode.OpAdd:
		s := a + b
		if (a > 0 && b > 0 && s < 0) || (a < 0 && b < 0 && s >= 0) {
			v.trap(span, "arithmetic overflow")
		}
		return intVal(s)
	case bytecode.OpSub:
		d := a - b
		if (a >= 0 && b < 0 && d < 0) || (a < 0 && b > 0 && d >= 0) {
			v.trap(span, "arithmetic overflow")
		}
		return intVal(d)
	case bytecode.OpMul:
		if a == 0 || b == 0 {
			return intVal(0)
		}
		p := a * b
		if p/b != a || (a == math.MinInt64 && b == -1) || (b == math.MinInt64 && a == -1) {
			v.trap(span, "arithmetic overflow")
		}
		return intVal(p)
	case bytecode.OpDiv:
		if b == 0 {
			v.trap(span, "divide by zero")
		}
		if a == math.MinInt64 && b == -1 {
			v.trap(span, "arithmetic overflow")
		}
		return intVal(a / b)
	case bytecode.OpRem:
		if b == 0 {
			v.trap(span, "remainder by zero")
		}
		if a == math.MinInt64 && b == -1 {
			v.trap(span, "arithmetic overflow")
		}
		return intVal(a % b)
	case bytecode.OpAnd:
		return intVal(a & b)
	case bytecode.OpOr:
		return intVal(a | b)
	case bytecode.OpXor:
		return intVal(a ^ b)
	case bytecode.OpShl:
		if b < 0 || b >= 64 {
			v.trap(span, "shift amount out of range")
		}
		return intVal(a << uint(b))
	case bytecode.OpShr:
		if b < 0 || b >= 64 {
			v.trap(span, "shift amount out of range")
		}
		return intVal(a >> uint(b))
	case bytecode.OpWrapAdd:
		return intVal(a + b)
	case bytecode.OpWrapSub:
		return intVal(a - b)
	case bytecode.OpWrapMul:
		return intVal(a * b)
	}
	panic("vm: not an integer operation")
}

func floatOp(op bytecode.Op, a, b float64) float64 {
	switch op {
	case bytecode.OpAddF:
		return a + b
	case bytecode.OpSubF:
		return a - b
	case bytecode.OpMulF:
		return a * b
	case bytecode.OpDivF:
		return a / b
	default:
		return math.Mod(a, b)
	}
}

var _ = compile.BuiltinPrint
