package backend

import (
	"fmt"
	"sort"

	"github.com/scarypheonix/meta/internal/bytecode"
	"github.com/scarypheonix/meta/internal/ir"
	"github.com/scarypheonix/meta/internal/x86"
)

// Register allocation is linear scan over live intervals (ADR-0018).
//
// The pools below are what is left after the reservations docs/spec/11-codegen.md makes.
// r15 holds the runtime block (ADR-0017); rax, rdx, r10 and r11 are scratch, kept out of
// the pool so that any instruction needing a fixed register — `idiv` wants rdx:rax, a
// variable shift wants cl, a call wants its arguments in order — can always be fixed up
// without first evicting somebody.
var (
	// calleeSaved survive a call, so an interval that spans one prefers them.
	calleeSaved = []x86.Reg{x86.RBX, x86.R12, x86.R13, x86.R14}
	// callerSaved are cheaper — no prologue cost — but are clobbered by every call.
	callerSaved = []x86.Reg{x86.RCX, x86.RSI, x86.RDI, x86.R8, x86.R9}

	// scratchA and scratchB are never allocated. Lowering uses them to hold a spilled
	// operand while it computes, so a two-operand instruction never needs a free
	// register to exist.
	scratchA = x86.R10
	scratchB = x86.R11
)

// loc is where a value lives: a register, or a slot in the frame.
type loc struct {
	reg   x86.Reg
	slot  int // 1-based; 0 means "in a register"
	inReg bool
}

func inReg(r x86.Reg) loc   { return loc{reg: r, inReg: true} }
func inSlot(n int) loc      { return loc{slot: n} }
func (l loc) spilled() bool { return !l.inReg }

// interval is one value's live range in the linearized instruction numbering.
type interval struct {
	val        *ir.Value
	start, end int
	// spansCall marks an interval live across an instruction that clobbers the
	// caller-saved registers. Such an interval gets a callee-saved register or a frame
	// slot, and never a register a call would destroy.
	spansCall bool
}

// alloc is the result: a location for every value, and how big the frame must be.
type alloc struct {
	where map[*ir.Value]loc
	slots int
	// refSlots is how many of the low-numbered spill slots (1..refSlots) hold
	// references; slots refSlots+1..slots are raw. spec/11-codegen.md's "Stack frames"
	// requires every reference-kind slot to sit below every raw one, so that a stack
	// map can describe the reference area as one offset and one count instead of a
	// bitmap (ADR-0021 is what gives every spilled value's Kind the register allocator
	// now reads to keep that grouping).
	refSlots int
	// used lists the callee-saved registers the function actually assigned, so the
	// prologue saves those and no others.
	used []x86.Reg
}

// order linearizes the blocks in reverse postorder, which is the order the code is
// emitted in and therefore the order intervals are measured against.
func order(f *ir.Func) []*ir.Block {
	var post []*ir.Block
	seen := map[*ir.Block]bool{}
	var visit func(*ir.Block)
	visit = func(b *ir.Block) {
		if b == nil || seen[b] {
			return
		}
		seen[b] = true
		for _, s := range b.Succs {
			visit(s)
		}
		post = append(post, b)
	}
	visit(f.Entry)

	out := make([]*ir.Block, 0, len(post))
	for i := len(post) - 1; i >= 0; i-- {
		out = append(out, post[i])
	}
	return out
}

// numbering assigns every value an index in the linear order: the φs of a block, then
// its instructions, then its terminator.
type numbering struct {
	blocks []*ir.Block
	index  map[*ir.Value]int
	start  map[*ir.Block]int
	end    map[*ir.Block]int
	// callAt marks the indices where the caller-saved registers are destroyed.
	callAt map[int]bool
	next   int
}

func number(f *ir.Func) *numbering {
	n := &numbering{
		blocks: order(f),
		index:  map[*ir.Value]int{},
		start:  map[*ir.Block]int{},
		end:    map[*ir.Block]int{},
		callAt: map[int]bool{},
	}
	for _, b := range n.blocks {
		n.start[b] = n.next
		for _, p := range b.Phis {
			n.index[p] = n.next
			n.next++
		}
		for _, v := range b.Instr {
			n.index[v] = n.next
			if clobbersCallerSaved(v.Op) {
				n.callAt[n.next] = true
			}
			n.next++
		}
		if b.Term != nil {
			n.index[b.Term] = n.next
			n.next++
		}
		n.end[b] = n.next
	}
	return n
}

// clobbersCallerSaved reports whether lowering an operation emits a `call`. Every
// allocation is a call to the runtime's allocator, which is why the aggregate
// constructors are in this list beside the obvious ones. The comparison operators are
// here even though most of them lower to a plain register compare: a structural `==`/
// `!=` and a String's `<`/`<=`/`>`/`>=` both call into the runtime (equal.go), and this
// function only sees the Op, not the bytecode.Kind that decides which lowering a given
// occurrence gets -- so every comparison is treated as call-clobbering, which is
// conservative for the common (primitive) case and correct for both.
func clobbersCallerSaved(op ir.Op) bool {
	switch op {
	case ir.OpCall, ir.OpCallClosure, ir.OpCallBuiltin,
		ir.OpStruct, ir.OpTuple, ir.OpVariant, ir.OpClosure, ir.OpBoxFn, ir.OpToStr,
		ir.OpEq, ir.OpNe, ir.OpLt, ir.OpLe, ir.OpGt, ir.OpGe:
		return true
	}
	return false
}

// liveness computes live-in and live-out sets per block by the usual backward dataflow.
//
// Intervals cannot be read off def and use positions alone: a value defined before a
// loop and used inside it is live across the whole loop, including the part of the
// linear order that comes *after* its last use. Doing the dataflow first and building
// intervals from block-level liveness is what makes the back edge count.
func liveness(f *ir.Func, n *numbering) (in, out map[*ir.Block]map[*ir.Value]bool) {
	in = map[*ir.Block]map[*ir.Value]bool{}
	out = map[*ir.Block]map[*ir.Value]bool{}
	uses := map[*ir.Block]map[*ir.Value]bool{}
	defs := map[*ir.Block]map[*ir.Value]bool{}

	for _, b := range n.blocks {
		u := map[*ir.Value]bool{}
		d := map[*ir.Value]bool{}
		// A φ's operands are used by the *predecessor*, not here: the copy that gives a
		// φ its value is emitted on the edge. Treating them as used here would keep a
		// value alive along paths that never carry it.
		for _, p := range b.Phis {
			d[p] = true
		}
		for _, v := range b.Instr {
			for _, a := range v.Args {
				if a != nil && !d[a] {
					u[a] = true
				}
			}
			d[v] = true
		}
		if b.Term != nil {
			for _, a := range b.Term.Args {
				if a != nil && !d[a] {
					u[a] = true
				}
			}
		}
		// The operands a block supplies to its successors' φs are used at its end.
		for _, s := range b.Succs {
			for _, p := range s.Phis {
				idx := predIndex(s, b)
				if idx >= 0 && idx < len(p.Args) && p.Args[idx] != nil && !d[p.Args[idx]] {
					u[p.Args[idx]] = true
				}
			}
		}
		uses[b], defs[b] = u, d
		in[b] = map[*ir.Value]bool{}
		out[b] = map[*ir.Value]bool{}
	}

	for changed := true; changed; {
		changed = false
		for i := len(n.blocks) - 1; i >= 0; i-- {
			b := n.blocks[i]
			newOut := map[*ir.Value]bool{}
			for _, s := range b.Succs {
				for v := range in[s] {
					newOut[v] = true
				}
			}
			newIn := map[*ir.Value]bool{}
			for v := range uses[b] {
				newIn[v] = true
			}
			for v := range newOut {
				if !defs[b][v] {
					newIn[v] = true
				}
			}
			if !sameSet(newIn, in[b]) || !sameSet(newOut, out[b]) {
				in[b], out[b] = newIn, newOut
				changed = true
			}
		}
	}
	return in, out
}

func sameSet(a, b map[*ir.Value]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for v := range a {
		if !b[v] {
			return false
		}
	}
	return true
}

func predIndex(b, pred *ir.Block) int {
	for i, p := range b.Preds {
		if p == pred {
			return i
		}
	}
	return -1
}

// intervals builds one live interval per value.
func intervals(f *ir.Func, n *numbering) []*interval {
	liveIn, liveOut := liveness(f, n)
	byVal := map[*ir.Value]*interval{}

	extend := func(v *ir.Value, at int) {
		iv, ok := byVal[v]
		if !ok {
			byVal[v] = &interval{val: v, start: at, end: at}
			return
		}
		if at < iv.start {
			iv.start = at
		}
		if at > iv.end {
			iv.end = at
		}
	}

	for _, b := range n.blocks {
		for v := range liveIn[b] {
			extend(v, n.start[b])
		}
		for v := range liveOut[b] {
			extend(v, n.end[b])
		}
		for _, p := range b.Phis {
			extend(p, n.index[p])
		}
		for _, v := range b.Instr {
			extend(v, n.index[v])
			for _, a := range v.Args {
				if a != nil {
					extend(a, n.index[v])
				}
			}
		}
		if b.Term != nil {
			for _, a := range b.Term.Args {
				if a != nil {
					extend(a, n.index[b.Term])
				}
			}
		}
		// A value b supplies to a successor's φ must survive to the physical end of b,
		// where the edge's phi-copy code (backend.go's phiCopies) reads it — even when
		// the value is defined in b itself. liveOut[b] does not carry this: liveness's
		// own uses/defs dataflow deliberately excludes a same-block definition from
		// that block's "uses" (a φ operand is used by the predecessor conceptually, not
		// literally by an instruction there), which is correct for propagating
		// liveness *across* block boundaries but leaves a purely-local def-then-φ-use
		// invisible to it. Without this, such a value's interval ends at its own
		// defining instruction, the allocator reuses its register for whatever comes
		// next in the block, and phiCopies reads back garbage -- silently, since
		// nothing here checks that a location is still the right one.
		for _, s := range b.Succs {
			idx := predIndex(s, b)
			if idx < 0 {
				continue
			}
			for _, p := range s.Phis {
				if idx < len(p.Args) && p.Args[idx] != nil {
					extend(p.Args[idx], n.end[b])
				}
			}
		}
	}

	out := make([]*interval, 0, len(byVal))
	for _, iv := range byVal {
		for at := range n.callAt {
			// A call at the interval's own definition point does not clobber it: the
			// result is produced in rax after the call has returned.
			if at > iv.start && at <= iv.end {
				iv.spansCall = true
				break
			}
		}
		out = append(out, iv)
	}
	// Sorted by start, then by value id so that two intervals starting together are
	// ordered the same way on every run: the allocator's output feeds the emitted bytes,
	// and spec/11-codegen.md requires two builds to be byte-identical.
	sort.Slice(out, func(i, j int) bool {
		if out[i].start != out[j].start {
			return out[i].start < out[j].start
		}
		return out[i].val.ID < out[j].val.ID
	})
	return out
}

// allocate assigns every value a register or a frame slot.
func allocate(f *ir.Func) *alloc {
	n := number(f)
	ivs := intervals(f, n)

	a := &alloc{where: map[*ir.Value]loc{}}
	freeCallee := append([]x86.Reg{}, calleeSaved...)
	freeCaller := append([]x86.Reg{}, callerSaved...)
	usedCallee := map[x86.Reg]bool{}

	var active []*interval
	regOf := map[*interval]x86.Reg{}

	release := func(iv *interval) {
		r, ok := regOf[iv]
		if !ok {
			return
		}
		delete(regOf, iv)
		if isCalleeSaved(r) {
			freeCallee = append(freeCallee, r)
			return
		}
		freeCaller = append(freeCaller, r)
	}

	expire := func(at int) {
		kept := active[:0]
		for _, iv := range active {
			if iv.end < at {
				release(iv)
				continue
			}
			kept = append(kept, iv)
		}
		active = kept
	}

	// refSpills and rawSpills collect spilled intervals in the order they were spilled;
	// final slot numbers are assigned afterward so that every reference-kind slot ends
	// up below every raw one (spec/11-codegen.md's "Stack frames"), regardless of the
	// arrival order the linear scan itself spills them in.
	var refSpills, rawSpills []*interval
	spill := func(iv *interval) {
		if iv.spansCall && iv.val.Kind == bytecode.KindUnknown {
			// A value the register allocator has to keep alive across a call, whose
			// kind ADR-0021's propagateKinds still could not resolve, is exactly the
			// case a stack map cannot afford to guess about: grouping it as "raw" when
			// it might actually be a reference would make a future collection corrupt
			// memory instead of merely computing a worse layout. Reaching this is a gap
			// in kinds.go's coverage, not a program the language rejects -- every
			// well-typed Origin program has a fully static type for every value.
			panic(fmt.Sprintf(
				"this is a compiler bug: %s in %s spans a call with no known kind", iv.val, f.Name))
		}
		if isRefKind(iv.val.Kind) {
			refSpills = append(refSpills, iv)
		} else {
			rawSpills = append(rawSpills, iv)
		}
	}

	for _, iv := range ivs {
		expire(iv.start)

		var chosen x86.Reg
		got := false
		if iv.spansCall {
			if len(freeCallee) > 0 {
				chosen, freeCallee = freeCallee[0], freeCallee[1:]
				got = true
			}
		} else {
			if len(freeCaller) > 0 {
				chosen, freeCaller = freeCaller[0], freeCaller[1:]
				got = true
			} else if len(freeCallee) > 0 {
				chosen, freeCallee = freeCallee[0], freeCallee[1:]
				got = true
			}
		}
		if !got {
			spill(iv)
			continue
		}
		if isCalleeSaved(chosen) {
			usedCallee[chosen] = true
		}
		regOf[iv] = chosen
		a.where[iv.val] = inReg(chosen)
		active = append(active, iv)
		sort.Slice(active, func(i, j int) bool { return active[i].end < active[j].end })
	}

	for i, iv := range refSpills {
		a.where[iv.val] = inSlot(i + 1)
	}
	for i, iv := range rawSpills {
		a.where[iv.val] = inSlot(len(refSpills) + i + 1)
	}
	a.refSlots = len(refSpills)
	a.slots = len(refSpills) + len(rawSpills)

	for _, r := range calleeSaved {
		if usedCallee[r] {
			a.used = append(a.used, r)
		}
	}
	return a
}

// isRefKind reports whether a value's kind is a heap reference a collector would need
// to trace -- a struct/tuple/enum/closure (KindRef) or a String (KindString, ByteArray-
// shaped but still a heap object, layout.go).
func isRefKind(k bytecode.Kind) bool {
	return k == bytecode.KindRef || k == bytecode.KindString
}

func isCalleeSaved(r x86.Reg) bool {
	for _, c := range calleeSaved {
		if c == r {
			return true
		}
	}
	return false
}
