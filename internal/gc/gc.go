// Package gc is Origin's garbage collector: precise, generational and moving.
//
// The design is the one spec/08-memory-model.md specifies:
//
//   - Objects are allocated by bumping a pointer in the nursery.
//   - A minor collection copies the nursery's live objects into the old generation.
//     Nothing is left behind, so the nursery is empty again afterwards and allocation
//     stays a pointer bump.
//   - A major collection flips the old generation between two semispaces, copying live
//     objects from one to the other.
//   - Old-to-young references are found through a card table, written by a barrier on
//     every reference store into an old object.
//   - Roots come from the mutator, precisely: it hands the collector every live
//     reference as a pointer to the slot holding it, so a moved object's address is
//     rewritten in place.
//
// The collector never scans conservatively and never guesses whether a word is a
// pointer: every object's shape comes from internal/layout, which is the one module the
// collector and the code generator share (process rule 5). Addresses are not observable
// from Origin, which is what makes moving sound (ADR-0008).
package gc

import (
	"fmt"

	"github.com/scarypheonix/meta/internal/layout"
)

// Config sizes the heap. Sizes are in words; the defaults are small enough to force
// many collections in tests and large enough to run real programs.
type Config struct {
	// NurseryWords is the size of the young generation.
	NurseryWords uint64
	// OldWords is the size of each of the two old-generation semispaces.
	OldWords uint64
	// CardWords is how many words one card covers.
	CardWords uint64
}

// DefaultConfig is a heap that collects often enough to be interesting.
func DefaultConfig() Config {
	return Config{NurseryWords: 1 << 16, OldWords: 1 << 18, CardWords: 128}
}

// RootVisitor is handed every root slot so the collector can rewrite it in place.
type RootVisitor func(visit func(*layout.Ref))

// Stats records what the collector has done, for tests and for reporting.
type Stats struct {
	MinorCollections uint64
	MajorCollections uint64
	BytesAllocated   uint64
	ObjectsAllocated uint64
	ObjectsPromoted  uint64
	ObjectsCopied    uint64
}

// Heap is one program's heap.
type Heap struct {
	cfg   Config
	types *layout.Registry

	// mem holds every space. A Ref is a word index into it, so a reference identifies
	// its space implicitly and no reference needs a tag.
	mem []uint64

	nurseryStart, nurseryEnd uint64
	nurseryNext              uint64

	// The old generation is two semispaces; oldFrom is the live one.
	oldFromStart, oldFromEnd uint64
	oldToStart, oldToEnd     uint64
	oldNext                  uint64

	// cards[i] is set when the old-generation region it covers may hold a reference
	// into the nursery.
	cards []bool

	roots RootVisitor
	stats Stats

	// outOfMemory is reported to the mutator rather than panicking here, so the trap
	// carries a source span (spec/04-expressions.md).
	outOfMemory bool
}

// New builds a heap. The root visitor may be set later with SetRoots, but must be set
// before the first collection.
func New(cfg Config, types *layout.Registry) *Heap {
	if cfg.NurseryWords == 0 {
		cfg.NurseryWords = DefaultConfig().NurseryWords
	}
	if cfg.OldWords == 0 {
		cfg.OldWords = DefaultConfig().OldWords
	}
	if cfg.CardWords == 0 {
		cfg.CardWords = DefaultConfig().CardWords
	}
	// A minor collection must be able to promote the entire nursery, because the
	// shortfall cannot be discovered halfway through a Cheney scan. That makes
	// "an old semispace is at least as large as the nursery" an invariant of the
	// design, not a tuning preference, so a configuration that breaks it is corrected
	// here rather than failing as a mysterious exhaustion later.
	if cfg.OldWords < cfg.NurseryWords {
		cfg.OldWords = cfg.NurseryWords
	}

	total := cfg.NurseryWords + 2*cfg.OldWords
	h := &Heap{cfg: cfg, types: types, mem: make([]uint64, total+1)}

	// Word 0 is reserved so that layout.Nil is never a real object.
	h.nurseryStart = 1
	h.nurseryEnd = h.nurseryStart + cfg.NurseryWords
	h.oldFromStart = h.nurseryEnd
	h.oldFromEnd = h.oldFromStart + cfg.OldWords
	h.oldToStart = h.oldFromEnd
	h.oldToEnd = h.oldToStart + cfg.OldWords

	h.nurseryNext = h.nurseryStart
	h.oldNext = h.oldFromStart
	h.cards = make([]bool, (2*cfg.OldWords)/cfg.CardWords+1)
	return h
}

// SetRoots installs the mutator's root visitor.
func (h *Heap) SetRoots(v RootVisitor) { h.roots = v }

// Stats returns a copy of the collector's counters.
func (h *Heap) Stats() Stats { return h.stats }

// OutOfMemory reports whether the last allocation failed because the heap is full.
func (h *Heap) OutOfMemory() bool { return h.outOfMemory }

// ---------------------------------------------------------------------------
// Allocation
// ---------------------------------------------------------------------------

// Alloc reserves an object of the given type with payloadWords of payload, collecting
// if the nursery is full. It returns layout.Nil when the heap is exhausted, and the
// caller turns that into the `out of memory` trap.
//
// The returned object's payload is zeroed. Origin has no uninitialized bindings
// (ADR-0007), so the mutator always overwrites every word before the object escapes;
// zeroing means a collection that happens mid-construction sees Nil rather than garbage.
func (h *Heap) Alloc(t layout.TypeID, payloadWords uint64) layout.Ref {
	if h.outOfMemory {
		return layout.Nil
	}
	need := payloadWords + 1 // header

	// An object too large for the nursery goes straight to the old generation, so that
	// a minor collection is never asked to copy something that cannot fit.
	if need > h.cfg.NurseryWords {
		return h.allocOld(t, payloadWords)
	}

	if h.nurseryNext+need > h.nurseryEnd {
		h.collectForNursery()
		if h.outOfMemory || h.nurseryNext+need > h.nurseryEnd {
			h.outOfMemory = true
			return layout.Nil
		}
	}

	ref := layout.Ref(h.nurseryNext)
	h.mem[h.nurseryNext] = uint64(layout.MakeHeader(t, payloadWords))
	for i := uint64(1); i < need; i++ {
		h.mem[h.nurseryNext+i] = 0
	}
	h.nurseryNext += need

	h.stats.ObjectsAllocated++
	h.stats.BytesAllocated += need * 8
	return ref
}

// collectForNursery frees the nursery, choosing which collection can actually do it.
//
// A minor collection copies survivors into the old generation, so it is only safe when
// the old generation has room for the nursery's worst case: every object surviving.
// Discovering the shortfall halfway through a Cheney scan is unrecoverable — some
// objects have already moved and their originals are forwarding pointers — so the
// choice is made before any object is touched.
func (h *Heap) collectForNursery() {
	worstCase := h.nurseryNext - h.nurseryStart
	if h.oldFromEnd-h.oldNext >= worstCase {
		h.MinorCollect()
		return
	}
	h.majorCollectWithRoom(worstCase)
	if h.oldFromEnd-h.oldNext < worstCase {
		h.outOfMemory = true
	}
}

// majorCollectWithRoom runs a major collection whose destination is large enough to take
// everything live, plus `extra` words of room to allocate into afterwards.
//
// Sizing the destination *before* the copy is the whole point. A Cheney scan discovers a
// shortfall halfway through, with some objects moved and some not and forwarding pointers
// written into a space it is about to abandon; there is no recovery from that, and the old
// code's answer was to set the out-of-memory flag and hand the mutator a reference that had
// not been updated. A copying collector's live set is bounded before it starts -- it cannot
// exceed what is in the nursery plus what is in the from-space -- so the destination can
// simply be made big enough first.
func (h *Heap) majorCollectWithRoom(extra uint64) {
	live := (h.nurseryNext - h.nurseryStart) + (h.oldNext - h.oldFromStart)
	// Half again as much as could possibly survive, and never below the configured size:
	// a semispace exactly as large as the live set collects on every allocation and never
	// makes progress.
	want := h.cfg.OldWords
	for want < live+live/2+extra && want < maxOldWords {
		want *= 2
	}
	h.ensureToSpace(want)
	h.MajorCollect()
}

// maxOldWords caps a semispace at 4 GiB. Past there, a program that keeps growing is a
// program with a leak rather than one that needs more room.
//
// It is a variable so that the test for what happens at the ceiling can lower it. Nothing
// else writes it.
var maxOldWords uint64 = 1 << 29

// ensureToSpace makes the destination of the next major collection at least `want` words.
//
// The to-space holds nothing -- that is what makes this safe. It can be moved and resized
// freely, while the nursery and the from-space never move, so every reference the mutator
// holds stays valid. Growing the heap therefore needs no relocation pass and no second
// collection: the next major collection does the moving, as it was going to anyway.
func (h *Heap) ensureToSpace(want uint64) {
	if want > maxOldWords {
		want = maxOldWords
	}
	if h.oldToEnd-h.oldToStart >= want {
		return
	}
	// Extend it in place when it already sits at the top of the heap; otherwise put the
	// new one there, abandoning the old one to the next collection's reuse.
	start := h.oldToStart
	if h.oldToEnd != uint64(len(h.mem)) {
		start = uint64(len(h.mem))
	}
	end := start + want
	if end > uint64(len(h.mem)) {
		mem := make([]uint64, end)
		copy(mem, h.mem)
		h.mem = mem
	}
	h.oldToStart, h.oldToEnd = start, end
	if want > h.cfg.OldWords {
		h.cfg.OldWords = want
	}
	if n := want/h.cfg.CardWords + 1; n > uint64(len(h.cards)) {
		h.cards = make([]bool, n)
	}
}

// allocOld places an object directly in the old generation, for objects too large for
// the nursery.
func (h *Heap) allocOld(t layout.TypeID, payloadWords uint64) layout.Ref {
	need := payloadWords + 1
	if h.oldNext+need > h.oldFromEnd {
		h.majorCollectWithRoom(need)
		if h.oldNext+need > h.oldFromEnd {
			h.outOfMemory = true
			return layout.Nil
		}
	}
	ref := layout.Ref(h.oldNext)
	h.mem[h.oldNext] = uint64(layout.MakeHeader(t, payloadWords))
	for i := uint64(1); i < need; i++ {
		h.mem[h.oldNext+i] = 0
	}
	h.oldNext += need
	h.stats.ObjectsAllocated++
	h.stats.BytesAllocated += need * 8
	return ref
}

// ---------------------------------------------------------------------------
// Field access
// ---------------------------------------------------------------------------

// header returns an object's header word.
func (h *Heap) header(r layout.Ref) layout.Header { return layout.Header(h.mem[r]) }

// TypeOf returns an object's type.
func (h *Heap) TypeOf(r layout.Ref) layout.TypeID { return h.header(r).TypeID() }

// Words returns an object's payload size.
func (h *Heap) Words(r layout.Ref) uint64 { return h.header(r).Words() }

// Get reads payload word i as a raw word.
func (h *Heap) Get(r layout.Ref, i uint64) uint64 {
	h.checkBounds(r, i)
	return h.mem[uint64(r)+1+i]
}

// Set writes payload word i with a non-reference value. No barrier is needed: a
// primitive can never make an old object point into the nursery.
func (h *Heap) Set(r layout.Ref, i uint64, v uint64) {
	h.checkBounds(r, i)
	h.mem[uint64(r)+1+i] = v
}

// GetRef reads payload word i as a reference.
func (h *Heap) GetRef(r layout.Ref, i uint64) layout.Ref {
	return layout.Ref(h.Get(r, i))
}

// SetRef writes a reference into payload word i, through the write barrier.
//
// The barrier is the whole reason a generational collector can skip the old generation
// during a minor collection: without recording this store, an old object pointing at a
// young one would keep no root alive and the young object would be collected while
// still reachable.
func (h *Heap) SetRef(r layout.Ref, i uint64, v layout.Ref) {
	h.checkBounds(r, i)
	h.mem[uint64(r)+1+i] = uint64(v)
	if h.isOld(uint64(r)) && h.isYoung(uint64(v)) {
		h.markCard(uint64(r) + 1 + i)
	}
}

func (h *Heap) checkBounds(r layout.Ref, i uint64) {
	if r == layout.Nil {
		panic("gc: field access on the null reference; Origin has no null (ADR-0007)")
	}
	if w := h.header(r).Words(); i >= w {
		panic(fmt.Sprintf("gc: field %d is out of range for a %d-word object", i, w))
	}
}

func (h *Heap) isYoung(w uint64) bool {
	return w >= h.nurseryStart && w < h.nurseryEnd
}

func (h *Heap) isOld(w uint64) bool {
	return (w >= h.oldFromStart && w < h.oldFromEnd) || (w >= h.oldToStart && w < h.oldToEnd)
}

func (h *Heap) markCard(w uint64) {
	idx := (w - h.oldFromStart) / h.cfg.CardWords
	if idx < uint64(len(h.cards)) {
		h.cards[idx] = true
	}
}

// ---------------------------------------------------------------------------
// Bytes, for String
// ---------------------------------------------------------------------------

// AllocBytes stores a byte string as a ByteArray object.
func (h *Heap) AllocBytes(t layout.TypeID, s string) layout.Ref {
	words := 1 + (uint64(len(s))+7)/8
	r := h.Alloc(t, words)
	if r == layout.Nil {
		return r
	}
	h.Set(r, 0, uint64(len(s)))
	for i := 0; i < len(s); i++ {
		w := uint64(1 + i/8)
		shift := uint((i % 8) * 8)
		h.Set(r, w, h.Get(r, w)|uint64(s[i])<<shift)
	}
	return r
}

// Bytes reads back a ByteArray object.
func (h *Heap) Bytes(r layout.Ref) string {
	n := h.Get(r, 0)
	out := make([]byte, n)
	for i := uint64(0); i < n; i++ {
		w := 1 + i/8
		shift := uint((i % 8) * 8)
		out[i] = byte(h.Get(r, w) >> shift)
	}
	return string(out)
}
