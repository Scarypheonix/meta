// Package ir is Origin's SSA intermediate representation.
//
// A function is a control-flow graph of basic blocks; a block is a list of instructions
// ending in a terminator; every instruction defines at most one value, and every value
// is defined exactly once. Uses point at definitions directly, so def-use information is
// the representation rather than an analysis over it.
//
// The IR is built from bytecode and emitted back to bytecode (ADR-0016), which is what
// makes the `-O0` / `-O1` / `-O2` differential an oracle: the levels share a lowering
// and differ only by the optimizer's edits.
package ir

import (
	"fmt"
	"sort"
	"strings"

	"github.com/scarypheonix/meta/internal/bytecode"
	"github.com/scarypheonix/meta/internal/diag"
)

// Op is an IR operation.
type Op uint8

// The IR operations. Arithmetic keeps the trapping and non-trapping forms distinct,
// because ADR-0005 makes that a semantic difference and not an implementation detail:
// the optimizer may fold a wrapping add and must fold a trapping one *to the trap*.
const (
	// OpConst is a constant: Const holds its index in the program's pool.
	OpConst Op = iota
	// OpUnit, OpTrue and OpFalse are the constants with no pool entry.
	OpUnit
	OpTrue
	OpFalse
	// OpPhi merges values from predecessors; Args are parallel to the block's Preds.
	OpPhi
	// OpParam is a function parameter; Index is its position.
	OpParam
	// OpCapture reads a closure's capture; Index is its position.
	OpCapture

	// Trapping integer arithmetic.
	OpAdd
	OpSub
	OpMul
	OpDiv
	OpRem
	OpNeg
	// Non-trapping integer arithmetic.
	OpWrapAdd
	OpWrapSub
	OpWrapMul
	// Bitwise and shifts; shifts trap on an out-of-range amount.
	OpAnd
	OpOr
	OpXor
	OpShl
	OpShr
	// Float arithmetic, which never traps.
	OpAddF
	OpSubF
	OpMulF
	OpDivF
	OpRemF
	OpNegF

	// Comparison and logic.
	OpEq
	OpNe
	OpLt
	OpLe
	OpGt
	OpGe
	OpNot

	// Calls.
	OpCall
	OpCallClosure
	OpCallBuiltin
	OpFunc
	OpClosure
	// OpBoxFn wraps Args[0], a bare OpFunc value, into a one-word captureless closure
	// object -- the native backend's own pass (internal/backend's resolveClosureCalls)
	// inserts it wherever a function reference does not provably stay a direct call's
	// own callee, so that every other consumer of a function-typed value (an
	// OpCallClosure, a return, an argument, a phi) sees the same object shape a real
	// OpClosure produces. Never built from bytecode directly: the VM's own value model
	// already distinguishes a bare function from a closure dynamically (a runtime tag),
	// which native code has no room for, so this exists to make that distinction static
	// instead, only where native lowering actually needs it.
	OpBoxFn

	// Aggregates.
	OpStruct
	OpTuple
	OpVariant
	OpGetField
	OpSetField
	OpIsVariant

	// Conversions.
	OpCast
	OpToStr

	// OpTrap stops with the message in Const.
	OpTrap

	// Terminators.
	// OpJump transfers to Succs[0].
	OpJump
	// OpBranch tests Args[0] and transfers to Succs[0] when true, Succs[1] otherwise.
	OpBranch
	// OpReturn returns Args[0].
	OpReturn

	numOps
)

var opNames = [...]string{
	OpConst: "const", OpUnit: "unit", OpTrue: "true", OpFalse: "false",
	OpPhi: "phi", OpParam: "param", OpCapture: "capture",
	OpAdd: "add", OpSub: "sub", OpMul: "mul", OpDiv: "div", OpRem: "rem", OpNeg: "neg",
	OpWrapAdd: "wrap_add", OpWrapSub: "wrap_sub", OpWrapMul: "wrap_mul",
	OpAnd: "and", OpOr: "or", OpXor: "xor", OpShl: "shl", OpShr: "shr",
	OpAddF: "addf", OpSubF: "subf", OpMulF: "mulf", OpDivF: "divf", OpRemF: "remf", OpNegF: "negf",
	OpEq: "eq", OpNe: "ne", OpLt: "lt", OpLe: "le", OpGt: "gt", OpGe: "ge", OpNot: "not",
	OpCall: "call", OpCallClosure: "call_closure", OpCallBuiltin: "call_builtin",
	OpFunc: "func", OpClosure: "closure", OpBoxFn: "box_fn",
	OpStruct: "struct", OpTuple: "tuple", OpVariant: "variant",
	OpGetField: "get_field", OpSetField: "set_field", OpIsVariant: "is_variant",
	OpCast: "cast", OpToStr: "to_str", OpTrap: "trap",
	OpJump: "jump", OpBranch: "branch", OpReturn: "return",
}

func (o Op) String() string {
	if int(o) < len(opNames) && opNames[o] != "" {
		return opNames[o]
	}
	return fmt.Sprintf("op(%d)", int(o))
}

// IsTerminator reports whether o ends a block.
func (o Op) IsTerminator() bool { return o == OpJump || o == OpBranch || o == OpReturn }

// MayTrapOnValues reports whether an operation can trap because of its operands.
//
// Such an operation is never removed for being unused: ADR-0005 makes overflow a trap at
// every optimization level, so an ignored `a + b` that would overflow must still stop the
// program. Deleting it would make `-O1` produce output `-O0` does not, which is exactly
// what the phase's differential is built to catch.
func (o Op) MayTrapOnValues() bool {
	switch o {
	case OpAdd, OpSub, OpMul, OpDiv, OpRem, OpNeg, OpShl, OpShr, OpCast:
		return true
	}
	return false
}

// Allocates reports whether an operation creates a heap object.
//
// Allocation matters twice. An unused allocation may be removed — that is what escape
// analysis is for — but two allocations may never be shared, because they produce
// distinct objects and `ref_eq` can tell them apart (spec/04-expressions.md).
func (o Op) Allocates() bool {
	switch o {
	case OpStruct, OpTuple, OpVariant, OpClosure, OpBoxFn, OpToStr:
		return true
	}
	return false
}

// HasEffect reports whether an operation does something beyond producing its value.
// An instruction with an effect is never removed and never reordered past another.
func (o Op) HasEffect() bool {
	switch o {
	case OpSetField, OpCall, OpCallClosure, OpCallBuiltin, OpTrap:
		return true
	}
	return false
}

// Removable reports whether an unused instance of an operation may be deleted.
func (o Op) Removable() bool {
	return !o.HasEffect() && !o.MayTrapOnValues() && !o.IsTerminator()
}

// Shareable reports whether two instances computing the same thing may be merged.
//
// Reading a field is excluded: a `mut` field can change between two reads, and proving
// it does not needs an alias analysis this phase does not have. That costs some
// optimization and keeps every case correct.
func (o Op) Shareable() bool {
	switch o {
	case OpPhi, OpParam, OpGetField:
		return false
	}
	return !o.HasEffect() && !o.Allocates()
}

// Movable reports whether an instruction may be hoisted out of a loop. A trapping
// operation may not: hoisting it makes a program that never entered the loop trap.
func (o Op) Movable() bool {
	return o.Shareable() && !o.MayTrapOnValues()
}

// Shareable reports whether two occurrences of this value necessarily compute the same
// thing, which is what a common-subexpression pass replaces one with the other on.
//
// The opcode alone almost decides it, and `Op.Shareable` is that part. What it cannot see
// is a structural `==` on an aggregate: the operands are two references, and the answer
// depends on the *fields* they point at, which an assignment between the two occurrences
// can change (spec/04-expressions.md makes `==` structural, ADR-0011). That makes it a heap
// read, exactly like OpGetField, which is already excluded for the same reason. On anything
// else -- an integer, a float, a String, which is immutable -- it is a pure function of its
// operands and stays shareable.
func (v *Value) Shareable() bool {
	if !v.Op.Shareable() {
		return false
	}
	if v.Op == OpEq || v.Op == OpNe {
		return bytecode.Kind(v.Const) != bytecode.KindRef
	}
	return true
}

// Movable reports whether this value may be hoisted out of a loop.
func (v *Value) Movable() bool {
	return v.Shareable() && !v.Op.MayTrapOnValues()
}

// Value is a single SSA definition.
type Value struct {
	// ID is unique within a function and stable for the life of the value.
	ID    int
	Op    Op
	Args  []*Value
	Block *Block

	// Const is the constant-pool index for OpConst, the callee index for OpFunc and
	// OpCall, the builtin index for OpCallBuiltin, the type index for OpStruct and
	// OpTuple, the variant index for OpVariant and OpIsVariant, the field index for
	// OpGetField and OpSetField, the cast kind for OpCast, and the message index for
	// OpTrap.
	Const int
	// Aux is a second operand: the argument count for a call, the capture count for a
	// closure, the cast's target for OpCast, the slot for OpParam and OpCapture.
	Aux int

	// Kind is bytecode.KindUnknown for most values: it is populated here only where
	// Build can read it straight off the bytecode with no extra context (OpParam and
	// OpCapture from the function's ParamKinds/CaptureKinds, and a field/payload/
	// tuple-element read or a call from the instruction's own Kind, ADR-0021). It is
	// not a general property of the IR — internal/opt and the VM path never read it —
	// and it is not, on its own, every value's final kind: internal/backend's own
	// kind-propagation pass is what extends this seed to values Build cannot label
	// (OpConst, which needs the constant pool Build does not have; every value whose
	// kind is fixed by its operation alone; and anything derived, like a φ, from ones
	// that are already known).
	Kind bytecode.Kind

	// Span is where the value came from, so a trap names a source location.
	Span diag.Span

	// uses is maintained by the block editing helpers so that dead-code elimination and
	// replacement do not need a separate analysis.
	uses int
}

// String renders a value's name.
func (v *Value) String() string { return fmt.Sprintf("v%d", v.ID) }

// Block is a basic block: a straight-line run of instructions ending in a terminator.
type Block struct {
	ID    int
	Func  *Func
	Phis  []*Value
	Instr []*Value
	// Term is the block's terminator.
	Term *Value
	// Preds and Succs are the control-flow edges. A phi's Args are parallel to Preds.
	Preds []*Block
	Succs []*Block

	// sealed marks a block whose predecessors are all known, which is what lets
	// on-demand phi insertion decide whether a variable's definition can be resolved.
	sealed bool
	// defs maps a variable to its definition in this block, during construction.
	defs map[int]*Value
	// incomplete holds phis created before the block was sealed.
	incomplete map[int]*Value
}

func (b *Block) String() string { return fmt.Sprintf("b%d", b.ID) }

// Func is one function in SSA form.
type Func struct {
	Name     string
	Params   int
	Captures int
	Blocks   []*Block
	Entry    *Block

	nextValue int
	nextBlock int
}

// NewFunc returns an empty function with an entry block.
func NewFunc(name string, params, captures int) *Func {
	f := &Func{Name: name, Params: params, Captures: captures}
	f.Entry = f.NewBlock()
	return f
}

// NewBlock adds a block. A block created after construction should be sealed with Seal.
func (f *Func) NewBlock() *Block {
	b := &Block{
		ID: f.nextBlock, Func: f,
		defs: map[int]*Value{}, incomplete: map[int]*Value{},
	}
	f.nextBlock++
	f.Blocks = append(f.Blocks, b)
	return b
}

// NewValue allocates a value without placing it in a block.
func (f *Func) NewValue(op Op, span diag.Span, args ...*Value) *Value {
	v := &Value{ID: f.nextValue, Op: op, Args: args, Span: span}
	f.nextValue++
	for _, a := range args {
		if a != nil {
			a.uses++
		}
	}
	return v
}

// Append places a value at the end of a block's instruction list.
func (b *Block) Append(v *Value) *Value {
	v.Block = b
	if v.Op == OpPhi {
		b.Phis = append(b.Phis, v)
		return v
	}
	if v.Op.IsTerminator() {
		b.Term = v
		return v
	}
	b.Instr = append(b.Instr, v)
	return v
}

// SetSuccs replaces a block's successors, maintaining the predecessor lists.
func (b *Block) SetSuccs(succs ...*Block) {
	for _, s := range b.Succs {
		s.removePred(b)
	}
	b.Succs = succs
	for _, s := range succs {
		s.Preds = append(s.Preds, b)
	}
}

func (b *Block) removePred(p *Block) {
	for i, q := range b.Preds {
		if q == p {
			b.Preds = append(b.Preds[:i], b.Preds[i+1:]...)
			return
		}
	}
}

// Uses reports how many other values name this one.
func (v *Value) Uses() int { return v.uses }

// ReplaceAllUses points every use of v at with, throughout the function.
func (f *Func) ReplaceAllUses(v, with *Value) {
	if v == with {
		return
	}
	for _, b := range f.Blocks {
		for _, list := range [][]*Value{b.Phis, b.Instr} {
			for _, u := range list {
				replaceArgs(u, v, with)
			}
		}
		if b.Term != nil {
			replaceArgs(b.Term, v, with)
		}
	}
}

func replaceArgs(u, v, with *Value) {
	for i, a := range u.Args {
		if a == v {
			u.Args[i] = with
			v.uses--
			if with != nil {
				with.uses++
			}
		}
	}
}

// RecomputeUses rebuilds every value's use count from scratch. Passes that rewrite
// instructions in bulk call it rather than trying to keep the counts exact as they go.
func (f *Func) RecomputeUses() {
	for _, b := range f.Blocks {
		for _, list := range [][]*Value{b.Phis, b.Instr} {
			for _, v := range list {
				v.uses = 0
			}
		}
		if b.Term != nil {
			b.Term.uses = 0
		}
	}
	for _, b := range f.Blocks {
		for _, list := range [][]*Value{b.Phis, b.Instr} {
			for _, v := range list {
				for _, a := range v.Args {
					if a != nil {
						a.uses++
					}
				}
			}
		}
		if b.Term != nil {
			for _, a := range b.Term.Args {
				if a != nil {
					a.uses++
				}
			}
		}
	}
}

// Print renders a function. Snapshot tests compare its output, so its shape is stable:
// changing it means updating goldens deliberately.
func (f *Func) Print(prog *bytecode.Program) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "func %s(params=%d captures=%d)\n", f.Name, f.Params, f.Captures)
	for _, b := range f.Blocks {
		preds := make([]string, 0, len(b.Preds))
		for _, p := range b.Preds {
			preds = append(preds, p.String())
		}
		sort.Strings(preds)
		if len(preds) > 0 {
			fmt.Fprintf(&sb, "%s:  ; preds %s\n", b, strings.Join(preds, " "))
		} else {
			fmt.Fprintf(&sb, "%s:\n", b)
		}
		for _, p := range b.Phis {
			fmt.Fprintf(&sb, "  %s\n", printValue(p, prog))
		}
		for _, v := range b.Instr {
			fmt.Fprintf(&sb, "  %s\n", printValue(v, prog))
		}
		if b.Term != nil {
			fmt.Fprintf(&sb, "  %s\n", printTerm(b))
		}
	}
	return sb.String()
}

func printValue(v *Value, prog *bytecode.Program) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s = %s", v, v.Op)
	switch v.Op {
	case OpConst:
		fmt.Fprintf(&sb, " %s", constText(prog, v.Const))
	case OpParam, OpCapture:
		fmt.Fprintf(&sb, " %d", v.Aux)
	case OpFunc:
		fmt.Fprintf(&sb, " %d", v.Const)
	case OpGetField, OpSetField:
		fmt.Fprintf(&sb, " %d", v.Const)
	case OpVariant, OpIsVariant:
		if prog != nil && v.Const < len(prog.Variants) {
			fmt.Fprintf(&sb, " %s", prog.Variants[v.Const].Name)
		} else {
			fmt.Fprintf(&sb, " %d", v.Const)
		}
	case OpTrap:
		fmt.Fprintf(&sb, " %s", constText(prog, v.Const))
	case OpCall, OpCallBuiltin, OpStruct, OpTuple, OpClosure, OpCast:
		fmt.Fprintf(&sb, " %d %d", v.Const, v.Aux)
	}
	for _, a := range v.Args {
		if a == nil {
			sb.WriteString(" <nil>")
			continue
		}
		fmt.Fprintf(&sb, " %s", a)
	}
	return sb.String()
}

func printTerm(b *Block) string {
	t := b.Term
	switch t.Op {
	case OpJump:
		return fmt.Sprintf("jump %s", b.Succs[0])
	case OpBranch:
		return fmt.Sprintf("branch %s ? %s : %s", t.Args[0], b.Succs[0], b.Succs[1])
	default:
		if len(t.Args) > 0 {
			return fmt.Sprintf("return %s", t.Args[0])
		}
		return "return"
	}
}

func constText(prog *bytecode.Program, i int) string {
	if prog == nil || i < 0 || i >= len(prog.Consts) {
		return fmt.Sprintf("#%d", i)
	}
	c := prog.Consts[i]
	switch c.Kind {
	case bytecode.ConstString:
		return fmt.Sprintf("%q", c.Str)
	case bytecode.ConstChar:
		return fmt.Sprintf("%q", rune(c.Bits))
	case bytecode.ConstFloat:
		return "float"
	default:
		return fmt.Sprint(int64(c.Bits))
	}
}

// RemoveEdge deletes the control-flow edge from → to, dropping the φ operands that
// arrived along it. A φ's operands are positional, parallel to its block's predecessor
// list, so removing a predecessor without removing the matching operand silently
// misattributes every remaining one.
func (f *Func) RemoveEdge(from, to *Block) {
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
	to.Preds = append(to.Preds[:idx], to.Preds[idx+1:]...)
	for _, phi := range to.Phis {
		if idx < len(phi.Args) {
			phi.Args = append(phi.Args[:idx], phi.Args[idx+1:]...)
		}
	}
	for i, s := range from.Succs {
		if s == to {
			from.Succs = append(from.Succs[:i], from.Succs[i+1:]...)
			break
		}
	}
}

// SetTerminator replaces a block's terminator and its successors together, so the two
// can never disagree.
func (b *Block) SetTerminator(t *Value, succs ...*Block) {
	for _, s := range append([]*Block{}, b.Succs...) {
		b.Func.RemoveEdge(b, s)
	}
	t.Block = b
	b.Term = t
	b.Succs = nil
	for _, s := range succs {
		b.Succs = append(b.Succs, s)
		s.Preds = append(s.Preds, b)
	}
}

// Truncate drops every instruction after v in its block. It is used when an instruction
// is replaced by something that does not return — a trap folded from a constant
// overflow, for instance.
func (b *Block) Truncate(after *Value) {
	for i, v := range b.Instr {
		if v == after {
			b.Instr = b.Instr[:i+1]
			return
		}
	}
}

// Reachable returns the blocks reachable from the entry.
func (f *Func) Reachable() map[*Block]bool {
	seen := map[*Block]bool{}
	var visit func(*Block)
	visit = func(b *Block) {
		if b == nil || seen[b] {
			return
		}
		seen[b] = true
		for _, s := range b.Succs {
			visit(s)
		}
	}
	visit(f.Entry)
	return seen
}

// DropBlocks removes blocks the entry cannot reach, fixing the edges and φ operands of
// the blocks that survive.
func (f *Func) DropBlocks(keep map[*Block]bool) bool {
	var kept []*Block
	changed := false
	for _, b := range f.Blocks {
		if keep[b] {
			kept = append(kept, b)
			continue
		}
		changed = true
		for _, s := range append([]*Block{}, b.Succs...) {
			f.RemoveEdge(b, s)
		}
	}
	if !changed {
		return false
	}
	f.Blocks = kept
	for _, b := range f.Blocks {
		var preds []*Block
		for i, p := range b.Preds {
			if keep[p] {
				preds = append(preds, p)
				continue
			}
			for _, phi := range b.Phis {
				if i < len(phi.Args) {
					phi.Args = append(phi.Args[:i], phi.Args[i+1:]...)
				}
			}
		}
		b.Preds = preds
	}
	f.RecomputeUses()
	return true
}

// Values calls fn for every value in the function, φs first within each block.
func (f *Func) Values(fn func(*Value)) {
	for _, b := range f.Blocks {
		for _, v := range b.Phis {
			fn(v)
		}
		for _, v := range b.Instr {
			fn(v)
		}
		if b.Term != nil {
			fn(b.Term)
		}
	}
}

// Seal marks a block created after SSA construction as complete. The on-demand φ
// machinery only runs during construction; a block spliced in later — by inlining, say —
// has all its predecessors from the moment it exists.
func (b *Block) Seal() {
	b.sealed = true
	b.defs = map[int]*Value{}
	b.incomplete = map[int]*Value{}
}

// ReplacePred swaps one predecessor of a block for another, leaving the φ operands
// exactly where they are.
//
// This is what a block split needs, and it is not the same as removing an edge and
// adding one: a φ's operands are positional, parallel to the predecessor list, so
// removing an entry and appending a new one silently reassigns every operand after it.
func (b *Block) ReplacePred(old, with *Block) {
	for i, p := range b.Preds {
		if p == old {
			b.Preds[i] = with
			return
		}
	}
}
