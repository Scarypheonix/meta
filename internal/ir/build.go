package ir

import (
	"fmt"
	"sort"

	"github.com/scarypheonix/meta/internal/bytecode"
	"github.com/scarypheonix/meta/internal/diag"
)

// stackEffect reports how many values an instruction pops and pushes.
//
// The table has to be exact: the whole construction rests on knowing the operand stack's
// depth at every program point, because a stack slot live across a branch is a variable
// and has to be given a φ. A wrong entry here does not produce slightly worse code, it
// produces a wrong program, so the builder checks the depths it derives for consistency
// and reports a compiler bug rather than continuing.
func stackEffect(in bytecode.Instr) (pops, pushes int) {
	switch in.Op {
	case bytecode.OpNop, bytecode.OpJump, bytecode.OpTrap, bytecode.OpHalt:
		return 0, 0
	case bytecode.OpConst, bytecode.OpUnit, bytecode.OpTrue, bytecode.OpFalse,
		bytecode.OpLoad, bytecode.OpLoadCapture, bytecode.OpFunc:
		return 0, 1
	case bytecode.OpStore, bytecode.OpPop, bytecode.OpJumpIfFalse, bytecode.OpJumpIfTrue,
		bytecode.OpReturn:
		return 1, 0
	case bytecode.OpNeg, bytecode.OpNegF, bytecode.OpNot, bytecode.OpToStr,
		bytecode.OpGetField, bytecode.OpGetPayload, bytecode.OpGetTupleElem,
		bytecode.OpIsVariant, bytecode.OpCast:
		return 1, 1
	case bytecode.OpSetField:
		return 2, 0
	case bytecode.OpCall:
		return int(in.A) + 1, 1
	case bytecode.OpCallBuiltin, bytecode.OpClosure, bytecode.OpStruct,
		bytecode.OpTuple, bytecode.OpVariant:
		return int(in.B), 1
	default:
		// Everything else is a binary operator.
		return 2, 1
	}
}

// irOps maps a bytecode operation to its IR counterpart, for the instructions that
// translate one to one.
var irOps = map[bytecode.Op]Op{
	bytecode.OpAdd: OpAdd, bytecode.OpSub: OpSub, bytecode.OpMul: OpMul,
	bytecode.OpDiv: OpDiv, bytecode.OpRem: OpRem, bytecode.OpNeg: OpNeg,
	bytecode.OpWrapAdd: OpWrapAdd, bytecode.OpWrapSub: OpWrapSub, bytecode.OpWrapMul: OpWrapMul,
	bytecode.OpAnd: OpAnd, bytecode.OpOr: OpOr, bytecode.OpXor: OpXor,
	bytecode.OpShl: OpShl, bytecode.OpShr: OpShr,
	bytecode.OpAddF: OpAddF, bytecode.OpSubF: OpSubF, bytecode.OpMulF: OpMulF,
	bytecode.OpDivF: OpDivF, bytecode.OpRemF: OpRemF, bytecode.OpNegF: OpNegF,
	bytecode.OpEq: OpEq, bytecode.OpNe: OpNe, bytecode.OpLt: OpLt,
	bytecode.OpLe: OpLe, bytecode.OpGt: OpGt, bytecode.OpGe: OpGe, bytecode.OpNot: OpNot,
	bytecode.OpToStr:    OpToStr,
	bytecode.OpGetField: OpGetField, bytecode.OpGetPayload: OpGetField,
	bytecode.OpGetTupleElem: OpGetField, bytecode.OpIsVariant: OpIsVariant,
	bytecode.OpCast: OpCast,
}

// builder turns one bytecode function into SSA.
type builder struct {
	src     *bytecode.Fn
	f       *Func
	leaders map[int]*Block
	// depth[pc] is the operand-stack depth on entry to the instruction at pc.
	depth []int
	// reachable[pc] marks instructions reachable from the entry.
	reachable []bool
	nLocals   int
	// stack is the abstract operand stack while translating a block.
	stack []*Value
}

// Build converts a bytecode function to SSA form.
func Build(src *bytecode.Fn) (*Func, error) {
	b := &builder{
		src:     src,
		f:       NewFunc(src.Name, src.Params, src.Captures),
		leaders: map[int]*Block{},
		nLocals: src.Locals,
	}
	if err := b.computeDepths(); err != nil {
		return nil, err
	}
	b.findLeaders()
	if err := b.translate(); err != nil {
		return nil, err
	}
	removeTrivialPhis(b.f)
	b.f.RecomputeUses()
	return b.f, nil
}

// computeDepths derives the operand-stack depth at every instruction and reports which
// instructions are reachable.
func (b *builder) computeDepths() error {
	n := len(b.src.Code)
	b.depth = make([]int, n+1)
	b.reachable = make([]bool, n+1)
	seen := make([]bool, n+1)

	type item struct{ pc, depth int }
	work := []item{{0, 0}}
	if n == 0 {
		return nil
	}

	for len(work) > 0 {
		it := work[len(work)-1]
		work = work[:len(work)-1]
		if it.pc > n {
			return fmt.Errorf("this is a compiler bug: jump to %d, past the end of %s", it.pc, b.src.Name)
		}
		if seen[it.pc] {
			if b.depth[it.pc] != it.depth {
				return fmt.Errorf(
					"this is a compiler bug: %s reaches instruction %d with stack depth %d and %d",
					b.src.Name, it.pc, b.depth[it.pc], it.depth)
			}
			continue
		}
		seen[it.pc] = true
		b.reachable[it.pc] = true
		b.depth[it.pc] = it.depth
		if it.pc == n {
			continue
		}

		in := b.src.Code[it.pc]
		pops, pushes := stackEffect(in)
		if it.depth < pops {
			return fmt.Errorf("this is a compiler bug: %s underflows the stack at instruction %d",
				b.src.Name, it.pc)
		}
		out := it.depth - pops + pushes

		switch in.Op {
		case bytecode.OpJump:
			work = append(work, item{int(in.A), out})
		case bytecode.OpJumpIfFalse, bytecode.OpJumpIfTrue:
			work = append(work, item{int(in.A), out}, item{it.pc + 1, out})
		case bytecode.OpReturn, bytecode.OpHalt, bytecode.OpTrap:
			// These do not fall through.
		default:
			work = append(work, item{it.pc + 1, out})
		}
	}
	return nil
}

// findLeaders marks the instructions that begin a basic block: the entry, every jump
// target, and every instruction after a control transfer.
func (b *builder) findLeaders() {
	isLeader := make([]bool, len(b.src.Code)+1)
	if len(b.src.Code) > 0 {
		isLeader[0] = true
	}
	for pc, in := range b.src.Code {
		switch in.Op {
		case bytecode.OpJump:
			isLeader[int(in.A)] = true
			if pc+1 < len(isLeader) {
				isLeader[pc+1] = true
			}
		case bytecode.OpJumpIfFalse, bytecode.OpJumpIfTrue:
			isLeader[int(in.A)] = true
			if pc+1 < len(isLeader) {
				isLeader[pc+1] = true
			}
		case bytecode.OpReturn, bytecode.OpHalt, bytecode.OpTrap:
			if pc+1 < len(isLeader) {
				isLeader[pc+1] = true
			}
		}
	}
	for pc := range isLeader {
		if !isLeader[pc] || !b.reachable[pc] {
			continue
		}
		if pc == 0 {
			b.leaders[pc] = b.f.Entry
			continue
		}
		b.leaders[pc] = b.f.NewBlock()
	}
}

// blockAt returns the block a jump target begins.
func (b *builder) blockAt(pc int) *Block { return b.leaders[pc] }

// translate walks the bytecode block by block, building SSA as it goes.
func (b *builder) translate() error {
	// Parameters and captures are the entry block's initial definitions.
	for i := 0; i < b.src.Params && i < b.nLocals; i++ {
		v := b.f.NewValue(OpParam, b.src.Span)
		v.Aux = i
		if i < len(b.src.ParamKinds) {
			v.Kind = b.src.ParamKinds[i]
		}
		b.f.Entry.Append(v)
		b.f.Entry.defs[i] = v
	}
	// Every other local starts as unit: Origin has no uninitialized bindings
	// (ADR-0007), and the lowering writes each before it reads it.
	for i := b.src.Params; i < b.nLocals; i++ {
		v := b.f.NewValue(OpUnit, b.src.Span)
		b.f.Entry.Append(v)
		b.f.Entry.defs[i] = v
	}
	b.f.Entry.sealed = true

	starts := make([]int, 0, len(b.leaders))
	for pc := range b.leaders {
		starts = append(starts, pc)
	}
	sort.Ints(starts)

	// Every block's predecessors are known once the whole function has been walked, so
	// sealing happens in a second pass. Until then a read of a variable not defined
	// locally creates an incomplete φ.
	for _, pc := range starts {
		if pc >= len(b.src.Code) {
			continue
		}
		if err := b.translateBlock(pc); err != nil {
			return err
		}
	}
	for _, pc := range starts {
		b.sealBlock(b.leaders[pc])
	}
	return nil
}

// translateBlock converts the straight-line run of instructions starting at pc.
func (b *builder) translateBlock(pc int) error {
	blk := b.blockAt(pc)
	b.stack = b.stack[:0]

	// The block's entry stack becomes variables numbered above the locals, read on
	// demand so that a value produced in one block and consumed in another gets a φ.
	for i := 0; i < b.depth[pc]; i++ {
		b.stack = append(b.stack, b.readVar(b.nLocals+i, blk, b.src.Span))
	}

	for i := pc; i < len(b.src.Code); i++ {
		in := b.src.Code[i]
		if i > pc && b.leaders[i] != nil {
			// The next instruction begins a block: fall through to it.
			b.writeStack(blk)
			t := b.f.NewValue(OpJump, in.Span)
			blk.Append(t)
			blk.SetSuccs(b.leaders[i])
			return nil
		}
		done, err := b.translateInstr(blk, in, i)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
	}
	// Ran off the end without a terminator: the lowering always emits a return, so this
	// is a compiler bug rather than something to paper over.
	return fmt.Errorf("this is a compiler bug: %s has a block with no terminator", b.src.Name)
}

// writeStack records the block's outgoing operand stack as variable definitions, so a
// successor can read them.
func (b *builder) writeStack(blk *Block) {
	for i, v := range b.stack {
		blk.defs[b.nLocals+i] = v
	}
}

func (b *builder) push(v *Value) { b.stack = append(b.stack, v) }

func (b *builder) pop() *Value {
	v := b.stack[len(b.stack)-1]
	b.stack = b.stack[:len(b.stack)-1]
	return v
}

// popN removes n values and returns them in stack order.
func (b *builder) popN(n int) []*Value {
	out := make([]*Value, n)
	copy(out, b.stack[len(b.stack)-n:])
	b.stack = b.stack[:len(b.stack)-n]
	return out
}

// translateInstr converts the instruction at pc, reporting whether it terminated the
// block.
func (b *builder) translateInstr(blk *Block, in bytecode.Instr, pc int) (bool, error) {
	switch in.Op {
	case bytecode.OpNop:
		return false, nil

	case bytecode.OpConst:
		v := b.f.NewValue(OpConst, in.Span)
		v.Const = int(in.A)
		b.push(blk.Append(v))

	case bytecode.OpUnit:
		b.push(blk.Append(b.f.NewValue(OpUnit, in.Span)))
	case bytecode.OpTrue:
		b.push(blk.Append(b.f.NewValue(OpTrue, in.Span)))
	case bytecode.OpFalse:
		b.push(blk.Append(b.f.NewValue(OpFalse, in.Span)))

	case bytecode.OpLoad:
		b.push(b.readVar(int(in.A), blk, in.Span))
	case bytecode.OpStore:
		blk.defs[int(in.A)] = b.pop()
	case bytecode.OpLoadCapture:
		v := b.f.NewValue(OpCapture, in.Span)
		v.Aux = int(in.A)
		if i := int(in.A); i >= 0 && i < len(b.src.CaptureKinds) {
			v.Kind = b.src.CaptureKinds[i]
		}
		b.push(blk.Append(v))
	case bytecode.OpPop:
		b.pop()

	case bytecode.OpFunc:
		v := b.f.NewValue(OpFunc, in.Span)
		v.Const = int(in.A)
		b.push(blk.Append(v))

	case bytecode.OpJump:
		b.writeStack(blk)
		blk.Append(b.f.NewValue(OpJump, in.Span))
		blk.SetSuccs(b.blockAt(int(in.A)))
		return true, nil

	case bytecode.OpJumpIfFalse, bytecode.OpJumpIfTrue:
		cond := b.pop()
		b.writeStack(blk)
		t := b.f.NewValue(OpBranch, in.Span, cond)
		blk.Append(t)
		target := b.blockAt(int(in.A))
		fallthroughBlk := b.blockAt(pc + 1)
		if in.Op == bytecode.OpJumpIfTrue {
			blk.SetSuccs(target, fallthroughBlk)
		} else {
			blk.SetSuccs(fallthroughBlk, target)
		}
		return true, nil

	case bytecode.OpReturn:
		val := b.pop()
		blk.Append(b.f.NewValue(OpReturn, in.Span, val))
		return true, nil

	case bytecode.OpHalt:
		blk.Append(b.f.NewValue(OpReturn, in.Span, blk.Append(b.f.NewValue(OpUnit, in.Span))))
		return true, nil

	case bytecode.OpTrap:
		v := b.f.NewValue(OpTrap, in.Span)
		v.Const = int(in.A)
		blk.Append(v)
		blk.Append(b.f.NewValue(OpReturn, in.Span, blk.Append(b.f.NewValue(OpUnit, in.Span))))
		return true, nil

	case bytecode.OpCall:
		args := b.popN(int(in.A) + 1)
		v := b.f.NewValue(OpCall, in.Span, args...)
		v.Aux = int(in.A)
		v.Kind = in.Kind
		b.push(blk.Append(v))

	case bytecode.OpCallBuiltin:
		args := b.popN(int(in.B))
		v := b.f.NewValue(OpCallBuiltin, in.Span, args...)
		v.Const = int(in.A)
		v.Aux = int(in.B)
		b.push(blk.Append(v))

	case bytecode.OpClosure:
		args := b.popN(int(in.B))
		v := b.f.NewValue(OpClosure, in.Span, args...)
		v.Const = int(in.A)
		v.Aux = int(in.B)
		b.push(blk.Append(v))

	case bytecode.OpStruct, bytecode.OpTuple, bytecode.OpVariant:
		args := b.popN(int(in.B))
		op := OpStruct
		switch in.Op {
		case bytecode.OpTuple:
			op = OpTuple
		case bytecode.OpVariant:
			op = OpVariant
		}
		v := b.f.NewValue(op, in.Span, args...)
		v.Const = int(in.A)
		v.Aux = int(in.B)
		b.push(blk.Append(v))

	case bytecode.OpSetField:
		val := b.pop()
		obj := b.pop()
		v := b.f.NewValue(OpSetField, in.Span, obj, val)
		v.Const = int(in.A)
		blk.Append(v)

	case bytecode.OpCast:
		v := b.f.NewValue(OpCast, in.Span, b.pop())
		v.Const = int(in.A)
		v.Aux = int(in.B)
		b.push(blk.Append(v))

	default:
		op, ok := irOps[in.Op]
		if !ok {
			return false, fmt.Errorf("this is a compiler bug: no IR form for %s", in.Op)
		}
		pops, _ := stackEffect(in)
		args := b.popN(pops)
		v := b.f.NewValue(op, in.Span, args...)
		v.Const = int(in.A)
		// Only OpGetField, OpGetPayload and OpGetTupleElem (all mapped to ir.OpGetField)
		// carry a meaningful Kind here; every other op reaching default leaves in.Kind at
		// its zero value, KindUnknown, which is exactly what a value whose kind is not
		// yet known here should have.
		v.Kind = in.Kind
		b.push(blk.Append(v))
	}
	return false, nil
}

// ---------------------------------------------------------------------------
// Braun et al.: on-demand SSA construction
// ---------------------------------------------------------------------------

// readVar returns the value of a variable at the end of a block, inserting φ-functions
// as needed.
func (b *builder) readVar(v int, blk *Block, span diag.Span) *Value {
	if val, ok := blk.defs[v]; ok {
		return val
	}
	return b.readVarRecursive(v, blk, span)
}

func (b *builder) readVarRecursive(v int, blk *Block, span diag.Span) *Value {
	var val *Value
	switch {
	case !blk.sealed:
		// The block's predecessors are not all known yet, so the φ's operands cannot be
		// filled in; it is recorded and completed when the block is sealed.
		val = b.f.NewValue(OpPhi, span)
		val.Aux = v
		blk.Append(val)
		blk.incomplete[v] = val
	case len(blk.Preds) == 1:
		return b.readVar(v, blk.Preds[0], span)
	default:
		val = b.f.NewValue(OpPhi, span)
		val.Aux = v
		blk.Append(val)
		// The φ is recorded before its operands are read, which is what breaks the
		// cycle when a loop's back-edge reads the variable the φ defines.
		blk.defs[v] = val
		b.addPhiOperands(v, val, span)
	}
	blk.defs[v] = val
	return val
}

func (b *builder) addPhiOperands(v int, phi *Value, span diag.Span) {
	for _, p := range phi.Block.Preds {
		phi.Args = append(phi.Args, b.readVar(v, p, span))
	}
}

// sealBlock records that a block's predecessors are all known and completes the φs that
// were created before that was true.
func (b *builder) sealBlock(blk *Block) {
	if blk == nil || blk.sealed {
		return
	}
	blk.sealed = true
	// In local-slot order, not map order: completing a φ reads its variable in every
	// predecessor, which can create further φs, so the iteration order decides the
	// order values are created and therefore how they are numbered. Snapshots compare
	// printed IR, so a run-to-run renumbering is a spurious diff.
	vars := make([]int, 0, len(blk.incomplete))
	for v := range blk.incomplete {
		vars = append(vars, v)
	}
	sort.Ints(vars)
	for _, v := range vars {
		b.addPhiOperands(v, blk.incomplete[v], blk.incomplete[v].Span)
	}
	blk.incomplete = map[int]*Value{}
}

// removeTrivialPhis deletes φs whose operands are all the same value (or the φ itself),
// to a fixed point. Braun's algorithm removes them as it goes; doing it afterwards is
// simpler and reaches the same result, because triviality is monotone.
func removeTrivialPhis(f *Func) {
	for changed := true; changed; {
		changed = false
		for _, blk := range f.Blocks {
			kept := blk.Phis[:0]
			for _, phi := range blk.Phis {
				if same, trivial := trivialOperand(phi); trivial {
					if same == nil {
						// No operand at all: the block is unreachable, so any value
						// will do and unit is the one with no other meaning.
						same = f.NewValue(OpUnit, phi.Span)
						blk.Instr = append([]*Value{same}, blk.Instr...)
						same.Block = blk
					}
					f.ReplaceAllUses(phi, same)
					changed = true
					continue
				}
				kept = append(kept, phi)
			}
			blk.Phis = kept
		}
	}
	f.RecomputeUses()
}

// trivialOperand reports the single distinct operand of a φ, if it has one.
func trivialOperand(phi *Value) (*Value, bool) {
	var same *Value
	for _, op := range phi.Args {
		if op == phi || op == same {
			continue
		}
		if same != nil {
			return nil, false
		}
		same = op
	}
	return same, true
}
