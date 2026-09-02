package ir

import (
	"fmt"

	"github.com/scarypheonix/meta/internal/bytecode"
	"github.com/scarypheonix/meta/internal/diag"
)

// Emit lowers an SSA function back to stack bytecode.
//
// SSA is destroyed the classic way: every value gets a frame slot, and a φ becomes
// copies at the end of each predecessor. The copies are performed through the operand
// stack — all sources pushed, then all destinations popped in reverse — which makes a
// parallel copy correct without a swap analysis, because the stack holds the old values
// while the new ones are written.
func Emit(f *Func, name string, span diag.Span) (*bytecode.Fn, error) {
	e := &emitter{
		f:     f,
		slots: map[*Value]int{},
		fn: &bytecode.Fn{
			Name: name, Params: f.Params, Captures: f.Captures, Span: span,
			ParamKinds: paramKinds(f), CaptureKinds: captureKinds(f),
		},
		blockStart: map[*Block]int{},
	}
	if err := e.assignSlots(); err != nil {
		return nil, err
	}
	e.layout()
	if err := e.emitBlocks(); err != nil {
		return nil, err
	}
	e.patchJumps()
	e.fn.Locals = e.nextSlot
	return e.fn, nil
}

// paramKinds and captureKinds rebuild bytecode.Fn's ParamKinds/CaptureKinds (ADR-0021)
// from f's own OpParam/OpCapture values, which already carry them (Build seeded them
// from the Fn being re-optimized). Emit must not simply drop this data: -O1/-O2 build a
// fresh bytecode.Fn here, and internal/backend's own re-Build of the result (buildIR)
// needs it exactly where ir.Build originally read it from.
func paramKinds(f *Func) []bytecode.Kind {
	out := make([]bytecode.Kind, f.Params)
	f.Values(func(v *Value) {
		if v.Op == OpParam && v.Aux >= 0 && v.Aux < len(out) {
			out[v.Aux] = v.Kind
		}
	})
	return out
}

func captureKinds(f *Func) []bytecode.Kind {
	out := make([]bytecode.Kind, f.Captures)
	f.Values(func(v *Value) {
		if v.Op == OpCapture && v.Aux >= 0 && v.Aux < len(out) {
			out[v.Aux] = v.Kind
		}
	})
	return out
}

type pendingJump struct {
	at    int
	block *Block
}

type emitter struct {
	f     *Func
	fn    *bytecode.Fn
	slots map[*Value]int

	nextSlot   int
	order      []*Block
	blockStart map[*Block]int
	jumps      []pendingJump
}

// assignSlots gives every value a frame slot. Parameters keep the slots the calling
// convention put them in, so slot i is argument i.
func (e *emitter) assignSlots() error {
	e.nextSlot = e.f.Params
	for _, b := range e.f.Blocks {
		for _, list := range [][]*Value{b.Phis, b.Instr} {
			for _, v := range list {
				if v.Op == OpParam {
					if v.Aux >= e.f.Params {
						return fmt.Errorf("this is a compiler bug: parameter %d in a function taking %d",
							v.Aux, e.f.Params)
					}
					e.slots[v] = v.Aux
					continue
				}
				e.slots[v] = e.nextSlot
				e.nextSlot++
			}
		}
	}
	return nil
}

// layout orders blocks in reverse postorder, which puts a block's predecessors before it
// wherever the control-flow graph allows and turns most jumps into fallthroughs.
func (e *emitter) layout() {
	seen := map[*Block]bool{}
	var post []*Block
	var visit func(*Block)
	visit = func(b *Block) {
		if b == nil || seen[b] {
			return
		}
		seen[b] = true
		for _, s := range b.Succs {
			visit(s)
		}
		post = append(post, b)
	}
	visit(e.f.Entry)
	for i := len(post) - 1; i >= 0; i-- {
		e.order = append(e.order, post[i])
	}
}

func (e *emitter) emit(op bytecode.Op, span diag.Span) int {
	e.fn.Code = append(e.fn.Code, bytecode.Instr{Op: op, Span: span})
	return len(e.fn.Code) - 1
}

func (e *emitter) emitA(op bytecode.Op, a int, span diag.Span) int {
	e.fn.Code = append(e.fn.Code, bytecode.Instr{Op: op, A: int32(a), Span: span})
	return len(e.fn.Code) - 1
}

func (e *emitter) emitAB(op bytecode.Op, a, b int, span diag.Span) int {
	e.fn.Code = append(e.fn.Code, bytecode.Instr{Op: op, A: int32(a), B: int32(b), Span: span})
	return len(e.fn.Code) - 1
}

// emitABK is emitAB carrying a value's kind, for the same reason emitAK exists: a round
// trip through bytecode must not lose what only the checker knew (ADR-0021).
func (e *emitter) emitABK(op bytecode.Op, a, b int, kind bytecode.Kind, span diag.Span) int {
	e.fn.Code = append(e.fn.Code, bytecode.Instr{Op: op, A: int32(a), B: int32(b), Kind: kind, Span: span})
	return len(e.fn.Code) - 1
}

// emitAK is emitA plus a value's kind (ADR-0021): re-emitting v as bytecode must not
// drop what internal/backend needs from it, even though this package's other consumer
// (the VM, at -O1/-O2) never reads it.
func (e *emitter) emitAK(op bytecode.Op, a int, kind bytecode.Kind, span diag.Span) int {
	e.fn.Code = append(e.fn.Code, bytecode.Instr{Op: op, A: int32(a), Kind: kind, Span: span})
	return len(e.fn.Code) - 1
}

// load pushes a value from its slot.
func (e *emitter) load(v *Value, span diag.Span) {
	e.emitA(bytecode.OpLoad, e.slots[v], span)
}

func (e *emitter) emitBlocks() error {
	for i, b := range e.order {
		e.blockStart[b] = len(e.fn.Code)
		for _, v := range b.Instr {
			if err := e.emitValue(v); err != nil {
				return err
			}
		}
		var next *Block
		if i+1 < len(e.order) {
			next = e.order[i+1]
		}
		if err := e.emitTerm(b, next); err != nil {
			return err
		}
	}
	return nil
}

// emitValue computes a value and stores it in its slot.
func (e *emitter) emitValue(v *Value) error {
	span := v.Span
	switch v.Op {
	case OpParam:
		return nil // already in its slot

	case OpConst:
		e.emitA(bytecode.OpConst, v.Const, span)
	case OpUnit:
		e.emit(bytecode.OpUnit, span)
	case OpTrue:
		e.emit(bytecode.OpTrue, span)
	case OpFalse:
		e.emit(bytecode.OpFalse, span)
	case OpCapture:
		e.emitA(bytecode.OpLoadCapture, v.Aux, span)
	case OpFunc:
		e.emitA(bytecode.OpFunc, v.Const, span)

	case OpCall:
		for _, a := range v.Args {
			e.load(a, span)
		}
		e.emitAK(bytecode.OpCall, v.Aux, v.Kind, span)

	case OpCallBuiltin:
		for _, a := range v.Args {
			e.load(a, span)
		}
		// OperandKind is what Build read off the instruction; Kind may since have been
		// rewritten by a backend pass that does not run on this path, so the round trip
		// writes back what came in.
		k := v.OperandKind
		if k == bytecode.KindUnknown {
			k = v.Kind
		}
		e.emitABK(bytecode.OpCallBuiltin, v.Const, v.Aux, k, span)

	case OpClosure:
		for _, a := range v.Args {
			e.load(a, span)
		}
		e.emitAB(bytecode.OpClosure, v.Const, v.Aux, span)

	case OpStruct, OpTuple, OpVariant:
		for _, a := range v.Args {
			e.load(a, span)
		}
		e.emitAB(structOps[v.Op], v.Const, v.Aux, span)

	case OpGetField:
		e.load(v.Args[0], span)
		e.emitAK(bytecode.OpGetField, v.Const, v.Kind, span)

	case OpSetField:
		e.load(v.Args[0], span)
		e.load(v.Args[1], span)
		e.emitA(bytecode.OpSetField, v.Const, span)
		return nil // no value produced

	case OpIsVariant:
		e.load(v.Args[0], span)
		e.emitA(bytecode.OpIsVariant, v.Const, span)

	case OpCast:
		e.load(v.Args[0], span)
		e.emitAB(bytecode.OpCast, v.Const, v.Aux, span)

	case OpToStr:
		e.load(v.Args[0], span)
		e.emitA(bytecode.OpToStr, v.Const, span)

	case OpTrap:
		e.emitA(bytecode.OpTrap, v.Const, span)
		return nil

	default:
		op, ok := backOps[v.Op]
		if !ok {
			return fmt.Errorf("this is a compiler bug: no bytecode form for IR %s", v.Op)
		}
		for _, a := range v.Args {
			e.load(a, span)
		}
		// Const travels back out even for the operations that mostly do not have one.
		// A comparison carries the static kind of its operands (bytecode.Kind), and
		// dropping it here would leave the round trip lossy in a way only the native
		// backend notices -- the virtual machine reads a tag off the value instead, so
		// it would keep working while compiled code got it wrong.
		e.emitA(op, v.Const, span)
	}

	e.emitA(bytecode.OpStore, e.slots[v], span)
	return nil
}

var structOps = map[Op]bytecode.Op{
	OpStruct: bytecode.OpStruct, OpTuple: bytecode.OpTuple, OpVariant: bytecode.OpVariant,
}

// backOps maps the one-to-one IR operations to bytecode.
var backOps = map[Op]bytecode.Op{
	OpAdd: bytecode.OpAdd, OpSub: bytecode.OpSub, OpMul: bytecode.OpMul,
	OpDiv: bytecode.OpDiv, OpRem: bytecode.OpRem, OpNeg: bytecode.OpNeg,
	OpWrapAdd: bytecode.OpWrapAdd, OpWrapSub: bytecode.OpWrapSub, OpWrapMul: bytecode.OpWrapMul,
	OpAnd: bytecode.OpAnd, OpOr: bytecode.OpOr, OpXor: bytecode.OpXor,
	OpShl: bytecode.OpShl, OpShr: bytecode.OpShr,
	OpAddF: bytecode.OpAddF, OpSubF: bytecode.OpSubF, OpMulF: bytecode.OpMulF,
	OpDivF: bytecode.OpDivF, OpRemF: bytecode.OpRemF, OpNegF: bytecode.OpNegF,
	OpEq: bytecode.OpEq, OpNe: bytecode.OpNe, OpLt: bytecode.OpLt,
	OpLe: bytecode.OpLe, OpGt: bytecode.OpGt, OpGe: bytecode.OpGe, OpNot: bytecode.OpNot,
}

// emitPhiCopies writes the values a successor's φs expect, at the end of a predecessor.
//
// Every source is pushed before any destination is written, so a cycle among the copies
// — `x, y = y, x` at a loop back-edge — comes out right without special handling.
func (e *emitter) emitPhiCopies(from, to *Block, span diag.Span) {
	if to == nil || len(to.Phis) == 0 {
		return
	}
	idx := -1
	for i, p := range to.Preds {
		if p == from {
			idx = i
			break
		}
	}
	if idx < 0 {
		return
	}
	var dests []int
	for _, phi := range to.Phis {
		if idx >= len(phi.Args) || phi.Args[idx] == nil {
			continue
		}
		e.load(phi.Args[idx], span)
		dests = append(dests, e.slots[phi])
	}
	for i := len(dests) - 1; i >= 0; i-- {
		e.emitA(bytecode.OpStore, dests[i], span)
	}
}

func (e *emitter) emitTerm(b, next *Block) error {
	t := b.Term
	if t == nil {
		return fmt.Errorf("this is a compiler bug: block %s has no terminator", b)
	}
	span := t.Span

	switch t.Op {
	case OpReturn:
		if len(t.Args) > 0 && t.Args[0] != nil {
			e.load(t.Args[0], span)
		} else {
			e.emit(bytecode.OpUnit, span)
		}
		e.emit(bytecode.OpReturn, span)
		return nil

	case OpJump:
		target := b.Succs[0]
		e.emitPhiCopies(b, target, span)
		if target != next {
			e.jumps = append(e.jumps, pendingJump{at: e.emitA(bytecode.OpJump, 0, span), block: target})
		}
		return nil

	case OpBranch:
		trueB, falseB := b.Succs[0], b.Succs[1]
		// φ copies differ per edge, so a branch whose successors both have φs needs a
		// landing pad on at least one edge. Emitting the copies for the taken edge
		// before the jump only works when the other edge needs none; otherwise the
		// false edge gets its own block of copies after the branch.
		e.load(t.Args[0], span)
		jf := e.emitA(bytecode.OpJumpIfFalse, 0, span)

		e.emitPhiCopies(b, trueB, span)
		if trueB != next {
			e.jumps = append(e.jumps, pendingJump{at: e.emitA(bytecode.OpJump, 0, span), block: trueB})
		} else {
			e.jumps = append(e.jumps, pendingJump{at: e.emitA(bytecode.OpJump, 0, span), block: trueB})
		}

		// The false edge lands here.
		e.fn.Code[jf].A = int32(len(e.fn.Code))
		e.emitPhiCopies(b, falseB, span)
		e.jumps = append(e.jumps, pendingJump{at: e.emitA(bytecode.OpJump, 0, span), block: falseB})
		return nil
	}
	return fmt.Errorf("this is a compiler bug: %s is not a terminator", t.Op)
}

func (e *emitter) patchJumps() {
	for _, j := range e.jumps {
		e.fn.Code[j.at].A = int32(e.blockStart[j.block])
	}
}
