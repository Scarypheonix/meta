package vm

import (
	"fmt"
	"math"

	"github.com/scarypheonix/meta/internal/arith"
	"github.com/scarypheonix/meta/internal/bytecode"
	"github.com/scarypheonix/meta/internal/compile"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/layout"
)

// run is the interpreter loop.
// run executes until the frame stack drops back to floor.
//
// A floor above zero is a nested call: the runtime itself invoking a closure the program
// gave it -- a spawned thread's body, or the function passed to `Mutex.with` -- which has
// to run to completion and yield its value rather than being left for the outer loop to
// finish (spec/12-concurrency.md).
func (v *VM) run(floor int) {
	for len(v.frames) > floor {
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
			desc := v.prog.Types.Get(v.heap.TypeOf(f.closure))
			v.push(v.readField(desc, f.closure, int(in.A)+1))

		case bytecode.OpAdd, bytecode.OpSub, bytecode.OpMul, bytecode.OpDiv, bytecode.OpRem,
			bytecode.OpAnd, bytecode.OpOr, bytecode.OpXor, bytecode.OpShl, bytecode.OpShr,
			bytecode.OpWrapAdd, bytecode.OpWrapSub, bytecode.OpWrapMul:
			b := v.pop()
			a := v.pop()
			v.push(v.intOp(in.Op, bytecode.Kind(in.A), a.N, b.N, in.Span))

		case bytecode.OpNeg:
			a := v.pop()
			r, trap := arith.Neg(bytecode.Kind(in.A), a.N)
			if trap != "" {
				v.trap(in.Span, "%s", trap)
			}
			v.push(Value{Tag: layout.TagInt, N: r})

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
			// §08 puts a safepoint on every loop back edge, so a compute loop cannot
			// starve the scheduler. A backward jump is what a back edge compiles to.
			if int(in.A) <= f.pc {
				v.safepoint()
			}
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
			v.push(result)
			if len(v.frames) == floor {
				return
			}

		case bytecode.OpFunc:
			v.push(Value{Tag: layout.TagFn, N: uint64(in.A)})

		case bytecode.OpCall:
			v.doCall(int(in.A), in.Span)

		case bytecode.OpCallBuiltin:
			v.callBuiltin(int(in.A), int(in.B), in.Kind, in.Span)

		case bytecode.OpClosure:
			v.makeClosure(int(in.A), in.Span)

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
			desc := v.prog.Types.Get(v.heap.TypeOf(obj.R))
			v.push(v.readField(desc, obj.R, int(in.A)))

		case bytecode.OpSetField:
			val := v.pop()
			obj := v.pop()
			if obj.Tag != layout.TagRef {
				panic("vm: field write on a value that is not an object")
			}
			desc := v.prog.Types.Get(v.heap.TypeOf(obj.R))
			v.writeField(desc, obj.R, int(in.A), val, in.Span)

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

// makeObject pops n values and builds an object from them.
func (v *VM) makeObject(t layout.TypeID, n int, span diag.Span) {
	desc := v.prog.Types.Get(t)
	r := v.alloc(t, desc.Words, span)
	// The values are written back to front so the stack is unwound in one pass, and the
	// object is only reachable after every slot is filled -- a collection triggered by
	// a later allocation must never see a half-built object.
	for i := n - 1; i >= 0; i-- {
		val := v.pop()
		v.writeField(desc, r, i, val, span)
	}
	v.push(refVal(r))
}

func (v *VM) makeClosure(ci int, span diag.Span) {
	info := v.prog.Closures[ci]
	desc := v.prog.Types.Get(info.Type)
	captures := len(desc.Kinds) - 1 // slot 0 is the function index
	r := v.alloc(info.Type, desc.Words, span)
	for i := captures - 1; i >= 0; i-- {
		val := v.pop()
		v.writeField(desc, r, i+1, val, span)
	}
	v.heap.Set(r, 0, uint64(info.FnIndex))
	v.push(refVal(r))
}

// doCall invokes the callee sitting beneath argCount arguments.
// callValue calls a function value from the runtime's own code and returns its result.
//
// The closure and its arguments go on the operand stack exactly as a compiled call would
// leave them, so nothing about the calling convention is special-cased; only the loop's
// stopping point is.
func (v *VM) callValue(callee Value, span diag.Span, args ...Value) Value {
	floor := len(v.frames)
	v.push(callee)
	for _, a := range args {
		v.push(a)
	}
	v.doCall(len(args), span)
	v.run(floor)
	return v.pop()
}

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
		if v.prog.Types.Get(v.heap.TypeOf(callee.R)).Kind != layout.ObjClosure {
			panic("vm: called an object that is not a closure")
		}
		idx := int(v.heap.Get(callee.R, 0))
		fn := v.prog.Fns[idx]
		closure := callee.R
		copy(v.stack[len(v.stack)-argCount-1:], v.stack[len(v.stack)-argCount:])
		v.stack = v.stack[:len(v.stack)-1]
		v.pushFrame(idx, fn, closure, argCount, span)

	default:
		panic("vm: called something that is not a function")
	}
}

// intOp performs one integer operation at the width the instruction carries.
//
// The rules are internal/arith's, not this file's: three engines have to agree on what
// `255u8 + 1` does, and the two written in Go agree by sharing the code rather than by
// being reviewed against each other (spec/04-expressions.md).
func (v *VM) intOp(op bytecode.Op, k bytecode.Kind, a, b uint64, span diag.Span) Value {
	if !k.IsInteger() {
		panic("vm: an integer operation with no integer kind")
	}
	var r uint64
	var trap string
	switch op {
	case bytecode.OpAdd:
		r, trap = arith.Add(k, a, b)
	case bytecode.OpSub:
		r, trap = arith.Sub(k, a, b)
	case bytecode.OpMul:
		r, trap = arith.Mul(k, a, b)
	case bytecode.OpDiv:
		r, trap = arith.Div(k, a, b)
	case bytecode.OpRem:
		r, trap = arith.Rem(k, a, b)
	case bytecode.OpAnd:
		r = arith.And(k, a, b)
	case bytecode.OpOr:
		r = arith.Or(k, a, b)
	case bytecode.OpXor:
		r = arith.Xor(k, a, b)
	case bytecode.OpShl:
		r, trap = arith.Shl(k, a, b)
	case bytecode.OpShr:
		r, trap = arith.Shr(k, a, b)
	case bytecode.OpWrapAdd:
		r = arith.Wrap(k, a+b)
	case bytecode.OpWrapSub:
		r = arith.Wrap(k, a-b)
	case bytecode.OpWrapMul:
		r = arith.Wrap(k, a*b)
	default:
		panic("vm: not an integer operation")
	}
	if trap != "" {
		v.trap(span, "%s", trap)
	}
	return Value{Tag: layout.TagInt, N: r}
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
