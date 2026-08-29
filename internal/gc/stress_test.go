package gc

import (
	"math/rand"
	"testing"

	"github.com/scarypheonix/meta/internal/layout"
)

// TestTenMillionShortLivedObjects is Phase 3's exit criterion for the collector: it
// survives ten million short-lived allocations with a live set held across collections,
// with no leak, no premature collection and no crash.
//
// The live set is checked, not just kept: every object in it carries a value that is
// verified at the end, so an object that was collected early or copied wrongly fails
// here rather than corrupting something later. The heap is deliberately small, so the
// ten million objects force thousands of collections rather than fitting in memory.
func TestTenMillionShortLivedObjects(t *testing.T) {
	if testing.Short() {
		t.Skip("the ten-million-object stress test is skipped in -short mode")
	}

	reg := layout.NewRegistry()
	pair := reg.Add(layout.FixedDescriptor("Pair", layout.RefsOnly([]bool{true, true})))
	leaf := reg.Add(layout.FixedDescriptor("Leaf", layout.RefsOnly([]bool{false})))

	// The old generation is deliberately small: promotions from the live set and the
	// barrier writes below must overflow it, so that major collections are exercised
	// too rather than only the nursery path.
	h := New(Config{NurseryWords: 1 << 12, OldWords: 1 << 14, CardWords: 128}, reg)
	roots := &rootSet{}
	h.SetRoots(roots.visitor())

	// A live set of a thousand objects, held for the whole run.
	const liveSet = 1000
	want := make([]uint64, liveSet)
	for i := 0; i < liveSet; i++ {
		r := h.Alloc(leaf, 1)
		if r == layout.Nil {
			t.Fatalf("could not build the live set: allocation %d failed", i)
		}
		want[i] = uint64(i) * 2654435761
		h.Set(r, 0, want[i])
		roots.refs = append(roots.refs, r)
	}

	// A chain through the live set, so the collector must trace and not just copy
	// roots: each pair points at two live-set members.
	for i := 0; i < 64; i++ {
		p := h.Alloc(pair, 2)
		if p == layout.Nil {
			t.Fatalf("could not build the chain: allocation %d failed", i)
		}
		roots.refs = append(roots.refs, p)
		h.SetRef(p, 0, roots.refs[i%liveSet])
		h.SetRef(p, 1, roots.refs[(i*7)%liveSet])
	}
	chainStart := liveSet
	ringStart := len(roots.refs)
	var ring []layout.Ref

	const total = 10_000_000
	for i := 0; i < total; i++ {
		r := h.Alloc(leaf, 1)
		if r == layout.Nil {
			t.Fatalf("allocation %d of %d failed: heap exhausted after %d collections",
				i, total, h.Stats().MinorCollections+h.Stats().MajorCollections)
		}
		h.Set(r, 0, uint64(i))
		// The object is dropped immediately: it is never rooted, so it is garbage by
		// the next collection.

		// Periodically write a young object into an old one, which is what makes the
		// write barrier and the card table do real work under the stress.
		//
		// Only the most recent few are kept rooted. The rest are promoted and then
		// become garbage *in the old generation*, which is what forces the semispace
		// flip: a test where every promoted object stays live never exercises it.
		if i%1024 == 0 {
			old := roots.refs[chainStart+(i/1024)%64]
			h.SetRef(old, 0, r)
			ring = append(ring, r)
			if len(ring) > 16 {
				ring = ring[1:]
			}
			roots.refs = append(roots.refs[:ringStart], ring...)
		}
	}

	// Collect twice with the live set still rooted. Process rule 3's property is that
	// no garbage survives two full collections, so what is left is the live set and
	// nothing else — this is the leak check, and it is an assertion rather than a
	// number in a log.
	h.MajorCollect()
	h.MajorCollect()

	liveObjects := liveSet + 64 + len(ring)
	maxLiveWords := uint64(liveObjects) * 4 // every shape here is 3 words or fewer
	if got := h.LiveWords(); got > maxLiveWords {
		t.Errorf("%d words survived two full collections, but the live set needs at most %d: garbage was retained",
			got, maxLiveWords)
	}

	stats := h.Stats()
	if stats.MinorCollections < 100 {
		t.Fatalf("only %d minor collections; the heap was too large to exercise the collector",
			stats.MinorCollections)
	}
	if stats.MajorCollections == 0 {
		t.Fatal("no major collection ran; the old-generation semispace flip went untested")
	}
	if h.OutOfMemory() {
		t.Fatal("the heap ran out: garbage was retained across collections")
	}

	// Every live-set object still reads back what it was given.
	for i := 0; i < liveSet; i++ {
		if got := h.Get(roots.refs[i], 0); got != want[i] {
			t.Fatalf("live-set object %d reads %d, want %d: it was collected early or corrupted",
				i, got, want[i])
		}
	}
	// The chain still points into the live set.
	for i := 0; i < 64; i++ {
		p := roots.refs[chainStart+i]
		if h.GetRef(p, 0) == layout.Nil || h.GetRef(p, 1) == layout.Nil {
			t.Fatalf("chain object %d lost a reference", i)
		}
	}

	t.Logf("%d allocations, %d minor and %d major collections, %d objects promoted, live set %d words",
		stats.ObjectsAllocated, stats.MinorCollections, stats.MajorCollections,
		stats.ObjectsPromoted, h.LiveWords())
}

// TestRandomisedAllocationPatterns runs the same idea with an allocation mix that
// changes shape as it goes, which is what "no crashes under randomized allocation
// patterns" means in the phase's exit criteria.
func TestRandomisedAllocationPatterns(t *testing.T) {
	reg := layout.NewRegistry()
	pair := reg.Add(layout.FixedDescriptor("Pair", layout.RefsOnly([]bool{true, true})))
	leaf := reg.Add(layout.FixedDescriptor("Leaf", layout.RefsOnly([]bool{false})))
	big := reg.Add(layout.FixedDescriptor("Big", make([]layout.WordKind, 64)))
	bytes := reg.Add(&layout.Descriptor{Name: "Bytes", Shape: layout.ByteArray})

	for seed := int64(0); seed < 8; seed++ {
		rng := rand.New(rand.NewSource(seed))
		h := New(Config{
			NurseryWords: uint64(256 + rng.Intn(4096)),
			OldWords:     1 << 17,
			CardWords:    uint64(8 + rng.Intn(120)),
		}, reg)
		roots := &rootSet{}
		h.SetRoots(roots.visitor())

		for i := 0; i < 200_000; i++ {
			var r layout.Ref
			switch rng.Intn(10) {
			case 0, 1, 2, 3, 4:
				r = h.Alloc(leaf, 1)
				if r != layout.Nil {
					h.Set(r, 0, rng.Uint64()&^(1<<63))
				}
			case 5, 6, 7:
				r = h.Alloc(pair, 2)
				if r != layout.Nil && len(roots.refs) > 0 {
					h.SetRef(r, 0, roots.refs[rng.Intn(len(roots.refs))])
				}
			case 8:
				r = h.Alloc(big, 64)
			default:
				r = h.AllocBytes(bytes, "abcdefghijklmnopqrstuvwxyz"[:1+rng.Intn(25)])
			}
			if r == layout.Nil {
				t.Fatalf("seed %d: allocation %d failed", seed, i)
			}
			// Keep a bounded live set that churns, so objects are promoted and then
			// become garbage in the old generation, forcing major collections.
			if rng.Intn(20) == 0 {
				roots.refs = append(roots.refs, r)
			}
			if len(roots.refs) > 2000 {
				roots.refs = roots.refs[len(roots.refs)/2:]
			}
		}
		if h.OutOfMemory() {
			t.Errorf("seed %d: the heap ran out", seed)
		}
	}
}
