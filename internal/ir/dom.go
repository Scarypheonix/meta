package ir

// Dominators is a function's dominator tree: block A dominates block B when every path
// from the entry to B passes through A.
//
// CSE needs it — a value may only be replaced by one that is guaranteed to have been
// computed already — and so does loop-invariant code motion, which needs to know which
// blocks a loop contains.
type Dominators struct {
	// Idom maps a block to its immediate dominator; the entry maps to nil.
	Idom map[*Block]*Block
	// rpo is the reverse-postorder numbering the algorithm walks in.
	rpo   []*Block
	order map[*Block]int
}

// ComputeDominators builds the dominator tree with the iterative algorithm of Cooper,
// Harvey and Kennedy ("A Simple, Fast Dominance Algorithm"). It is chosen over
// Lengauer-Tarjan for the usual reason: on graphs this size it is as fast in practice
// and it fits on a page, which matters for something the whole optimizer trusts.
func ComputeDominators(f *Func) *Dominators {
	d := &Dominators{Idom: map[*Block]*Block{}, order: map[*Block]int{}}

	var post []*Block
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
		post = append(post, b)
	}
	visit(f.Entry)

	for i := len(post) - 1; i >= 0; i-- {
		d.rpo = append(d.rpo, post[i])
	}
	for i, b := range d.rpo {
		d.order[b] = i
	}

	d.Idom[f.Entry] = f.Entry
	for changed := true; changed; {
		changed = false
		for _, b := range d.rpo[1:] {
			var newIdom *Block
			for _, p := range b.Preds {
				if _, known := d.Idom[p]; !known {
					continue
				}
				if newIdom == nil {
					newIdom = p
					continue
				}
				newIdom = d.intersect(p, newIdom)
			}
			if newIdom != nil && d.Idom[b] != newIdom {
				d.Idom[b] = newIdom
				changed = true
			}
		}
	}
	d.Idom[f.Entry] = nil
	return d
}

func (d *Dominators) intersect(a, b *Block) *Block {
	for a != b {
		for d.order[a] > d.order[b] {
			a = d.Idom[a]
			if a == nil {
				return b
			}
		}
		for d.order[b] > d.order[a] {
			b = d.Idom[b]
			if b == nil {
				return a
			}
		}
	}
	return a
}

// Dominates reports whether a dominates b. A block dominates itself.
func (d *Dominators) Dominates(a, b *Block) bool {
	for cur := b; cur != nil; cur = d.Idom[cur] {
		if cur == a {
			return true
		}
	}
	return false
}

// ValueDominates reports whether the definition of a is guaranteed to have run before
// the definition of b. Within one block, position decides; across blocks, dominance does.
func (d *Dominators) ValueDominates(a, b *Value) bool {
	if a.Block == nil || b.Block == nil {
		return false
	}
	if a.Block != b.Block {
		return d.Dominates(a.Block, b.Block)
	}
	// A φ is defined on entry to its block, so it precedes every instruction there.
	if a.Op == OpPhi {
		return b.Op != OpPhi || indexOf(a.Block.Phis, a) < indexOf(a.Block.Phis, b)
	}
	if b.Op == OpPhi {
		return false
	}
	return indexOf(a.Block.Instr, a) < indexOf(a.Block.Instr, b)
}

func indexOf(list []*Value, v *Value) int {
	for i, x := range list {
		if x == v {
			return i
		}
	}
	return -1
}

// Loop is a natural loop: a header block and the blocks that can reach its back edge
// without leaving the loop.
type Loop struct {
	Header *Block
	Blocks map[*Block]bool
	// Preheader is a block that dominates the header and is not in the loop, if there is
	// exactly one such predecessor. Hoisting needs somewhere to hoist to.
	Preheader *Block
}

// Loops finds the natural loops of a function: one per back edge, where a back edge is
// an edge whose target dominates its source.
func Loops(f *Func, d *Dominators) []*Loop {
	var out []*Loop
	for _, b := range f.Blocks {
		for _, s := range b.Succs {
			if !d.Dominates(s, b) {
				continue
			}
			out = append(out, buildLoop(s, b, d))
		}
	}
	return out
}

func buildLoop(header, latch *Block, d *Dominators) *Loop {
	l := &Loop{Header: header, Blocks: map[*Block]bool{header: true}}
	stack := []*Block{latch}
	for len(stack) > 0 {
		b := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if l.Blocks[b] {
			continue
		}
		l.Blocks[b] = true
		stack = append(stack, b.Preds...)
	}

	// The preheader is the loop's single entry from outside. Without one, there is
	// nowhere a hoisted value would be guaranteed to run exactly once.
	var outside []*Block
	for _, p := range header.Preds {
		if !l.Blocks[p] {
			outside = append(outside, p)
		}
	}
	if len(outside) == 1 && len(outside[0].Succs) == 1 {
		l.Preheader = outside[0]
	}
	return l
}
