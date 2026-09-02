package opt

import (
	"github.com/scarypheonix/meta/internal/bytecode"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/ir"
	"github.com/scarypheonix/meta/internal/prelude"
)

// inlineBudget is the cost model: a callee is inlined when it is no larger than this,
// counted in IR instructions.
//
// The number is deliberately small. Inlining pays for itself by exposing constants and
// removing call overhead, and stops paying when it multiplies code size — and this
// compiler must eventually compile itself inside a 60-second budget on two cores, so
// growth is a cost that lands on the project directly.
const inlineBudget = 24

// maxInlinesPerFunction bounds how much one function can grow, so that a chain of small
// callees cannot compound into something enormous.
const maxInlinesPerFunction = 8

// Inline replaces direct calls to small functions with their bodies.
//
// Only a call whose callee is a named function is considered: a closure is called
// through a heap object whose target is not known statically, and devirtualizing that
// needs information the bytecode has thrown away. A function is never inlined into
// itself, which is what keeps recursion from expanding forever.
func Inline(f *ir.Func, selfIndex int, funcs []*ir.Func, prog *bytecode.Program, level Level) bool {
	changed := false
	for count := 0; count < maxInlinesPerFunction; count++ {
		site, callee, calleeIndex := findInlineSite(f, selfIndex, funcs)
		if site == nil {
			break
		}
		inlineOne(f, site, callee, calleeIndex)
		changed = true
	}
	if changed {
		f.RecomputeUses()
	}
	return changed
}

// findInlineSite picks the first call worth inlining.
func findInlineSite(f *ir.Func, selfIndex int, funcs []*ir.Func) (*ir.Value, *ir.Func, int) {
	for _, b := range f.Blocks {
		for _, v := range b.Instr {
			if v.Op != ir.OpCall || len(v.Args) == 0 || v.Args[0] == nil {
				continue
			}
			target := v.Args[0]
			if target.Op != ir.OpFunc {
				continue // an indirect call: the callee is not known here
			}
			idx := target.Const
			if idx < 0 || idx >= len(funcs) || idx == selfIndex {
				continue
			}
			callee := funcs[idx]
			if callee == nil || callee.Captures > 0 {
				continue
			}
			if len(v.Args)-1 != callee.Params {
				continue // arity disagreement: leave it to the call
			}
			if instructionCount(callee) > inlineBudget || hasInlineBlocker(callee) {
				continue
			}
			return v, callee, idx
		}
	}
	return nil, nil, 0
}

func instructionCount(f *ir.Func) int {
	n := 0
	for _, b := range f.Blocks {
		n += len(b.Phis) + len(b.Instr) + 1
	}
	return n
}

// hasInlineBlocker reports whether a callee contains something the splice cannot handle.
func hasInlineBlocker(f *ir.Func) bool {
	blocked := false
	f.Values(func(v *ir.Value) {
		if v.Op == ir.OpCapture {
			blocked = true
		}
	})
	return blocked
}

// inlineOne splices a callee's body into the caller at one call site.
//
// The caller's block is split at the call: what came before stays, what came after moves
// to a continuation block. The callee's blocks are cloned in, its parameters are replaced
// by the call's arguments, and each of its returns becomes a jump to the continuation.
// The call's value becomes a φ over the returned values.
func inlineOne(f *ir.Func, call *ir.Value, callee *ir.Func, calleeIndex int) {
	site := call.Block
	pos := -1
	for i, v := range site.Instr {
		if v == call {
			pos = i
			break
		}
	}
	if pos < 0 {
		return
	}

	// The continuation takes everything after the call, and the site's terminator.
	cont := f.NewBlock()
	cont.Seal()
	cont.Instr = append(cont.Instr, site.Instr[pos+1:]...)
	for _, v := range cont.Instr {
		v.Block = cont
	}
	site.Instr = site.Instr[:pos]

	// The continuation inherits the site's terminator and its outgoing edges. The
	// successors' φ operands must stay where they are, so the predecessor is replaced
	// in place rather than removed and re-added: a φ's operands are positional, and
	// re-adding would shift every one after it onto the wrong edge.
	oldTerm := site.Term
	oldSuccs := append([]*ir.Block{}, site.Succs...)
	cont.Term = oldTerm
	if oldTerm != nil {
		oldTerm.Block = cont
	}
	cont.Succs = oldSuccs
	for _, s := range oldSuccs {
		s.ReplacePred(site, cont)
	}
	site.Succs = nil

	// Clone the callee.
	clone := newCloner(f, call)
	for i := 1; i < len(call.Args); i++ {
		clone.params[i-1] = call.Args[i]
	}
	entry := clone.cloneBlocks(callee)

	// Control enters the callee, and each of its returns lands in the continuation.
	site.SetTerminator(f.NewValue(ir.OpJump, call.Span), entry)

	result := f.NewValue(ir.OpPhi, call.Span)
	// The φ stands in for the call, so it holds what the call held -- including the
	// static kind only the checker knew (ADR-0021). Losing it here leaves a value the
	// register allocator cannot place, which is a compiler-bug panic at -O2 for a
	// program that builds at -O1.
	result.Kind = call.Kind
	for _, ret := range clone.returns {
		var val *ir.Value
		if len(ret.value.Args) > 0 {
			val = ret.value.Args[0]
		}
		if val == nil {
			val = f.NewValue(ir.OpUnit, call.Span)
			val.Block = ret.block
			ret.block.Instr = append(ret.block.Instr, val)
		}
		ret.block.SetTerminator(f.NewValue(ir.OpJump, call.Span), cont)
		result.Args = append(result.Args, val)
	}

	if len(result.Args) == 1 {
		f.ReplaceAllUses(call, result.Args[0])
	} else {
		cont.Phis = append([]*ir.Value{result}, cont.Phis...)
		result.Block = cont
		f.ReplaceAllUses(call, result)
	}
	f.RecomputeUses()
}

// cloner copies a callee's blocks and values into the caller.
type cloner struct {
	f       *ir.Func
	call    *ir.Value
	params  map[int]*ir.Value
	values  map[*ir.Value]*ir.Value
	blocks  map[*ir.Block]*ir.Block
	returns []returnSite
}

type returnSite struct {
	block *ir.Block
	value *ir.Value
}

func newCloner(f *ir.Func, call *ir.Value) *cloner {
	return &cloner{
		f: f, call: call,
		params: map[int]*ir.Value{},
		values: map[*ir.Value]*ir.Value{},
		blocks: map[*ir.Block]*ir.Block{},
	}
}

func (c *cloner) cloneBlocks(callee *ir.Func) *ir.Block {
	for _, b := range callee.Blocks {
		nb := c.f.NewBlock()
		nb.Seal()
		c.blocks[b] = nb
	}
	// Values are created before edges, so a φ can refer to a value defined later.
	for _, b := range callee.Blocks {
		nb := c.blocks[b]
		for _, v := range b.Phis {
			nv := c.cloneValue(v)
			if nv != nil {
				nb.Phis = append(nb.Phis, nv)
				nv.Block = nb
			}
		}
		for _, v := range b.Instr {
			nv := c.cloneValue(v)
			if nv != nil {
				nb.Instr = append(nb.Instr, nv)
				nv.Block = nb
			}
		}
	}
	// Now the operands and the edges.
	for _, b := range callee.Blocks {
		nb := c.blocks[b]
		for _, list := range [][]*ir.Value{nb.Phis, nb.Instr} {
			for _, v := range list {
				c.rewriteArgs(v)
			}
		}
		if b.Term == nil {
			continue
		}
		switch b.Term.Op {
		case ir.OpReturn:
			nt := c.f.NewValue(ir.OpReturn, c.call.Span)
			for _, a := range b.Term.Args {
				nt.Args = append(nt.Args, c.mapValue(a))
			}
			nb.Term = nt
			nt.Block = nb
			c.returns = append(c.returns, returnSite{block: nb, value: nt})
		case ir.OpJump:
			nb.SetTerminator(c.f.NewValue(ir.OpJump, c.call.Span), c.blocks[b.Succs[0]])
		case ir.OpBranch:
			nt := c.f.NewValue(ir.OpBranch, c.call.Span, c.mapValue(b.Term.Args[0]))
			nb.SetTerminator(nt, c.blocks[b.Succs[0]], c.blocks[b.Succs[1]])
		}
	}

	// A φ's operands are positional: operand i is the value arriving from Preds[i]
	// (internal/ir's OpPhi). The φs above were cloned with their operands in the
	// *callee's* pred order, but the edges were just created by walking the callee's
	// blocks and appending to each successor's Preds -- which is a different order
	// whenever a block's predecessors do not happen to appear in that same sequence.
	//
	// So the pred lists are rebuilt from the originals, in the originals' order. The two
	// lists hold the same edges either way, which is why nothing that only counts them
	// ever noticed: a merge block whose two arms arrived the other way round silently
	// swapped its two values.
	for _, b := range callee.Blocks {
		nb := c.blocks[b]
		nb.Preds = nb.Preds[:0]
		for _, p := range b.Preds {
			nb.Preds = append(nb.Preds, c.blocks[p])
		}
	}
	return c.blocks[callee.Entry]
}

// inlinedSpan is which line a cloned instruction reports if it traps.
//
// The callee's own, because that is the line the trap is on and inlining is not allowed to
// change what a program prints (spec/11-codegen.md: identical output at every level).
// Stamping the call site onto every cloned instruction, as this used to, moved a `divide by
// zero` inside a small helper onto the line that called the helper as soon as -O2 inlined
// it, which is an optimization the program can see.
//
// A span in the prelude is the exception, and not a special case so much as the same rule:
// an operation the prelude performs on the caller's behalf already reports the caller's
// line when it is *not* inlined, because both other engines walk out of the prelude to find
// one (interp/vm's userSpan). Keeping the prelude's own span here would make -O2 the level
// that names `<prelude>` in a message about the user's program.
func inlinedSpan(callee, site diag.Span) diag.Span {
	if callee.Valid() && callee.File != nil && callee.File.Name != prelude.Name {
		return callee
	}
	return site
}

// cloneValue copies one value, or returns nil for a parameter, which is replaced by the
// call's argument rather than cloned.
func (c *cloner) cloneValue(v *ir.Value) *ir.Value {
	if v.Op == ir.OpParam {
		if arg, ok := c.params[v.Aux]; ok {
			c.values[v] = arg
			return nil
		}
	}
	nv := c.f.NewValue(v.Op, inlinedSpan(v.Span, c.call.Span))
	nv.Const = v.Const
	nv.Aux = v.Aux
	nv.Kind = v.Kind
	nv.OperandKind = v.OperandKind
	// The clone starts out pointing at the callee's values; rewriteArgs then maps each
	// to its caller-side counterpart. Starting from an empty slice instead loses every
	// operand, which shows up as an instruction with no inputs rather than as a crash.
	nv.Args = append([]*ir.Value{}, v.Args...)
	c.values[v] = nv
	return nv
}

func (c *cloner) rewriteArgs(v *ir.Value) {
	for i := range v.Args {
		v.Args[i] = c.mapValue(v.Args[i])
	}
}

// mapValue finds the caller-side value standing for a callee-side one.
func (c *cloner) mapValue(v *ir.Value) *ir.Value {
	if v == nil {
		return nil
	}
	if nv, ok := c.values[v]; ok {
		return nv
	}
	return v
}
