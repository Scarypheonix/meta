package gc

import "github.com/scarypheonix/meta/internal/layout"

// MinorCollect copies the nursery's live objects into the old generation.
//
// Cheney's algorithm: roots are copied first, then the collector scans what it has
// copied, copying anything those objects reference, until the scan pointer catches the
// allocation pointer. Nothing is left in the nursery afterwards, so allocation goes back
// to a pointer bump.
//
// The old generation is not scanned. That is the whole point of a generational
// collector, and it is only sound because every old-to-young reference has been recorded
// in the card table by the write barrier.
func (h *Heap) MinorCollect() {
	if h.roots == nil {
		panic("gc: collection with no root visitor; the mutator must call SetRoots first")
	}
	h.stats.MinorCollections++

	scanStart := h.oldNext

	// Roots first.
	h.roots(func(slot *layout.Ref) {
		*slot = h.evacuate(*slot)
	})

	// Then the recorded old-to-young references. A card covers a range of words, so the
	// whole range is scanned; that is the cost of a coarse card table and the reason
	// the cards are cleared as they are processed.
	h.scanDirtyCards()

	// Then everything the copies reference, until the scan pointer catches up.
	h.scanFrom(scanStart)

	// The nursery is empty again.
	h.nurseryNext = h.nurseryStart
}

// scanDirtyCards visits the old-generation words a card marks as possibly pointing into
// the nursery, evacuating what they reference.
func (h *Heap) scanDirtyCards() {
	for i := range h.cards {
		if !h.cards[i] {
			continue
		}
		h.cards[i] = false

		start := h.oldFromStart + uint64(i)*h.cfg.CardWords
		end := start + h.cfg.CardWords
		if end > h.oldNext {
			end = h.oldNext
		}
		// The card records word addresses, but a word is only a reference if its
		// object's descriptor says so, so the range is walked object by object.
		h.scanRange(start, end)
	}
}

// scanRange evacuates the references held by every object overlapping [start, end).
//
// It walks the old generation from its beginning to find object boundaries: a card
// records where a store happened, not where the object containing it starts.
func (h *Heap) scanRange(start, end uint64) {
	w := h.oldFromStart
	for w < h.oldNext {
		hdr := layout.Header(h.mem[w])
		size := hdr.Words() + 1
		objEnd := w + size
		if objEnd > start && w < end {
			h.scanObject(layout.Ref(w))
		}
		if w >= end {
			return
		}
		w = objEnd
	}
}

// scanFrom is Cheney's scan loop over newly copied objects.
func (h *Heap) scanFrom(scan uint64) {
	for scan < h.oldNext {
		r := layout.Ref(scan)
		size := h.header(r).Words() + 1
		h.scanObject(r)
		scan += size
	}
}

// scanObject evacuates every reference an object holds, rewriting each in place.
func (h *Heap) scanObject(r layout.Ref) {
	desc := h.types.Get(h.header(r).TypeID())
	words := h.header(r).Words()
	base := uint64(r) + 1

	switch desc.Shape {
	case layout.ByteArray, layout.RawArray:
		return // no references: a String's bytes, or an array of a non-reference type

	case layout.RefArray:
		n := h.mem[base]
		for i := uint64(1); i <= n && i < words; i++ {
			h.mem[base+i] = uint64(h.evacuate(layout.Ref(h.mem[base+i])))
		}

	default: // layout.Fixed: exact per-instantiation layout (ADR-0019)
		for i := uint64(0); i < words; i++ {
			if desc.IsRef(i) {
				h.mem[base+i] = uint64(h.evacuate(layout.Ref(h.mem[base+i])))
			}
		}
	}
}

// evacuate copies a nursery object into the old generation and returns its new address.
// An object already copied is recognised by its forwarding header, so a shared object
// stays shared and a cyclic graph terminates — this is where `ref_eq` stability comes
// from (spec/08-memory-model.md).
func (h *Heap) evacuate(r layout.Ref) layout.Ref {
	if r == layout.Nil || !h.isYoung(uint64(r)) {
		return r // null, or already in the old generation
	}
	if to, moved := h.header(r).Forwarded(); moved {
		return to
	}

	hdr := h.header(r)
	size := hdr.Words() + 1

	if h.oldNext+size > h.oldFromEnd {
		// Unreachable: collectForNursery guarantees the old generation can hold the
		// whole nursery before a minor collection starts. Getting here means that
		// guarantee was broken, and continuing would leave half the heap forwarded and
		// half not, so it stops loudly instead.
		panic("gc: old generation exhausted during a minor collection; collectForNursery's headroom check is wrong")
	}
	dst := h.oldNext
	copy(h.mem[dst:dst+size], h.mem[uint64(r):uint64(r)+size])
	h.oldNext += size

	h.mem[uint64(r)] = uint64(layout.MakeForward(layout.Ref(dst)))
	h.stats.ObjectsPromoted++
	h.stats.ObjectsCopied++
	return layout.Ref(dst)
}

// MajorCollect copies every live object into the other old semispace, scanning the
// nursery as well so that young objects reachable only from the old generation survive.
func (h *Heap) MajorCollect() {
	if h.roots == nil {
		panic("gc: collection with no root visitor; the mutator must call SetRoots first")
	}
	h.stats.MajorCollections++

	// Flip: allocate into what was the to-space.
	fromStart, fromEnd := h.oldFromStart, h.oldNext
	h.oldFromStart, h.oldToStart = h.oldToStart, h.oldFromStart
	h.oldFromEnd, h.oldToEnd = h.oldToEnd, h.oldFromEnd
	h.oldNext = h.oldFromStart

	scanStart := h.oldNext
	h.roots(func(slot *layout.Ref) {
		*slot = h.evacuateMajor(*slot, fromStart, fromEnd)
	})

	// Cheney scan over the new space, following references out of everything copied.
	scan := scanStart
	for scan < h.oldNext {
		r := layout.Ref(scan)
		size := h.header(r).Words() + 1
		h.scanObjectMajor(r, fromStart, fromEnd)
		scan += size
	}

	// Every live object is now in the new old space; the nursery is empty and the cards
	// describe a space that no longer exists.
	h.nurseryNext = h.nurseryStart
	for i := range h.cards {
		h.cards[i] = false
	}
}

// evacuateMajor copies an object from either the nursery or the old from-space.
func (h *Heap) evacuateMajor(r layout.Ref, fromStart, fromEnd uint64) layout.Ref {
	if r == layout.Nil {
		return r
	}
	w := uint64(r)
	inOldFrom := w >= fromStart && w < fromEnd
	if !h.isYoung(w) && !inOldFrom {
		return r // already in the destination space
	}
	if to, moved := h.header(r).Forwarded(); moved {
		return to
	}

	size := h.header(r).Words() + 1
	if h.oldNext+size > h.oldFromEnd {
		h.outOfMemory = true
		return r
	}
	dst := h.oldNext
	copy(h.mem[dst:dst+size], h.mem[w:w+size])
	h.oldNext += size
	h.mem[w] = uint64(layout.MakeForward(layout.Ref(dst)))
	h.stats.ObjectsCopied++
	return layout.Ref(dst)
}

func (h *Heap) scanObjectMajor(r layout.Ref, fromStart, fromEnd uint64) {
	desc := h.types.Get(h.header(r).TypeID())
	words := h.header(r).Words()
	base := uint64(r) + 1

	switch desc.Shape {
	case layout.ByteArray, layout.RawArray:
		return
	case layout.RefArray:
		n := h.mem[base]
		for i := uint64(1); i <= n && i < words; i++ {
			h.mem[base+i] = uint64(h.evacuateMajor(layout.Ref(h.mem[base+i]), fromStart, fromEnd))
		}
	default:
		for i := uint64(0); i < words; i++ {
			if desc.IsRef(i) {
				h.mem[base+i] = uint64(h.evacuateMajor(layout.Ref(h.mem[base+i]), fromStart, fromEnd))
			}
		}
	}
}

// Collect runs a full collection. It is what a program calls when it wants the heap
// compacted regardless of how full the nursery is; the collector itself decides when to
// run a minor collection.
func (h *Heap) Collect() { h.MajorCollect() }

// LiveWords reports how many words the old generation currently holds, which is the
// live set after a collection.
func (h *Heap) LiveWords() uint64 { return h.oldNext - h.oldFromStart }

// NurseryWords reports how many words are allocated in the nursery.
func (h *Heap) NurseryWords() uint64 { return h.nurseryNext - h.nurseryStart }
