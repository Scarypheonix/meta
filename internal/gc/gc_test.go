package gc

import (
	"math/rand"
	"testing"

	"github.com/scarypheonix/meta/internal/layout"
)

// testHeap builds a heap with three shapes: a pair of references, a leaf holding one
// integer, and a byte array.
func testHeap(t *testing.T, cfg Config) (*Heap, layout.TypeID, layout.TypeID, layout.TypeID) {
	t.Helper()
	reg := layout.NewRegistry()
	pair := reg.Add(layout.FixedDescriptor("Pair", layout.RefsOnly([]bool{true, true})))
	leaf := reg.Add(layout.FixedDescriptor("Leaf", layout.RefsOnly([]bool{false})))
	bytes := reg.Add(&layout.Descriptor{Name: "Bytes", Shape: layout.ByteArray})
	return New(cfg, reg), pair, leaf, bytes
}

// rootSet is a mutator's roots: a slice the collector rewrites in place.
type rootSet struct{ refs []layout.Ref }

func (r *rootSet) visitor() RootVisitor {
	return func(visit func(*layout.Ref)) {
		for i := range r.refs {
			visit(&r.refs[i])
		}
	}
}

func TestAllocateAndRead(t *testing.T) {
	h, _, leaf, _ := testHeap(t, DefaultConfig())
	var roots rootSet
	h.SetRoots(roots.visitor())

	r := h.Alloc(leaf, 1)
	if r == layout.Nil {
		t.Fatal("allocation failed on an empty heap")
	}
	h.Set(r, 0, 42)
	if got := h.Get(r, 0); got != 42 {
		t.Errorf("read back %d, want 42", got)
	}
	if h.TypeOf(r) != leaf {
		t.Error("the object's type was not preserved")
	}
	if h.Words(r) != 1 {
		t.Error("the object's size was not preserved")
	}
}

func TestPayloadIsZeroed(t *testing.T) {
	// spec/08: Origin has no uninitialized values. Zeroing means a collection during
	// construction sees Nil rather than a stale word from a dead object.
	h, pair, _, _ := testHeap(t, Config{NurseryWords: 64, OldWords: 1 << 12, CardWords: 8})
	var roots rootSet
	h.SetRoots(roots.visitor())

	for i := 0; i < 200; i++ {
		r := h.Alloc(pair, 2)
		if r == layout.Nil {
			t.Fatalf("allocation %d failed", i)
		}
		if h.Get(r, 0) != 0 || h.Get(r, 1) != 0 {
			t.Fatalf("allocation %d came back with a dirty payload", i)
		}
		h.SetRef(r, 0, layout.Nil)
	}
}

func TestLiveObjectSurvivesCollection(t *testing.T) {
	h, _, leaf, _ := testHeap(t, Config{NurseryWords: 128, OldWords: 1 << 14, CardWords: 8})
	roots := &rootSet{}
	h.SetRoots(roots.visitor())

	kept := h.Alloc(leaf, 1)
	h.Set(kept, 0, 99)
	roots.refs = append(roots.refs, kept)

	for i := 0; i < 500; i++ {
		h.Alloc(leaf, 1) // garbage
	}
	if h.Stats().MinorCollections == 0 {
		t.Fatal("the test allocated nothing like enough to force a collection")
	}
	if got := h.Get(roots.refs[0], 0); got != 99 {
		t.Errorf("the live object's contents are %d, want 99: it was collected or corrupted", got)
	}
}

func TestReferenceIsRewrittenInPlace(t *testing.T) {
	h, _, leaf, _ := testHeap(t, Config{NurseryWords: 64, OldWords: 1 << 12, CardWords: 8})
	roots := &rootSet{}
	h.SetRoots(roots.visitor())

	r := h.Alloc(leaf, 1)
	h.Set(r, 0, 7)
	roots.refs = append(roots.refs, r)
	before := roots.refs[0]

	h.MinorCollect()

	if roots.refs[0] == before {
		t.Error("the object did not move; this test is not exercising the copy")
	}
	if got := h.Get(roots.refs[0], 0); got != 7 {
		t.Errorf("after moving, the object reads %d, want 7", got)
	}
}

func TestSharedObjectStaysShared(t *testing.T) {
	// spec/08: object identity is preserved across collections, which is what makes
	// `ref_eq` stable under a moving collector.
	h, pair, leaf, _ := testHeap(t, Config{NurseryWords: 128, OldWords: 1 << 12, CardWords: 8})
	roots := &rootSet{}
	h.SetRoots(roots.visitor())

	shared := h.Alloc(leaf, 1)
	h.Set(shared, 0, 5)
	a := h.Alloc(pair, 2)
	b := h.Alloc(pair, 2)
	h.SetRef(a, 0, shared)
	h.SetRef(b, 0, shared)
	roots.refs = append(roots.refs, a, b)

	h.MinorCollect()

	fromA := h.GetRef(roots.refs[0], 0)
	fromB := h.GetRef(roots.refs[1], 0)
	if fromA != fromB {
		t.Error("a shared object was copied twice; identity was not preserved")
	}
	if h.Get(fromA, 0) != 5 {
		t.Error("the shared object's contents were lost")
	}
}

func TestCyclicGraphTerminatesAndSurvives(t *testing.T) {
	h, pair, _, _ := testHeap(t, Config{NurseryWords: 128, OldWords: 1 << 12, CardWords: 8})
	roots := &rootSet{}
	h.SetRoots(roots.visitor())

	a := h.Alloc(pair, 2)
	b := h.Alloc(pair, 2)
	h.SetRef(a, 0, b)
	h.SetRef(b, 0, a) // a cycle
	roots.refs = append(roots.refs, a)

	h.MinorCollect()

	newA := roots.refs[0]
	newB := h.GetRef(newA, 0)
	if back := h.GetRef(newB, 0); back != newA {
		t.Error("the cycle was not preserved: b no longer points back at a")
	}
}

func TestGarbageDoesNotSurviveTwoCycles(t *testing.T) {
	// Process rule 3's property: no garbage survives two full collections.
	h, _, leaf, _ := testHeap(t, Config{NurseryWords: 1 << 12, OldWords: 1 << 14, CardWords: 8})
	roots := &rootSet{}
	h.SetRoots(roots.visitor())

	for i := 0; i < 200; i++ {
		h.Alloc(leaf, 1)
	}
	h.MinorCollect()
	h.MajorCollect()

	if live := h.LiveWords(); live != 0 {
		t.Errorf("%d words survived with no roots; garbage was retained", live)
	}
}

func TestWriteBarrierKeepsAYoungObjectAliveFromAnOldOne(t *testing.T) {
	// This is the property the card table exists for. Without the barrier, `young` is
	// reachable only from an old object, the minor collection does not scan the old
	// generation, and the object is collected while still live.
	h, pair, leaf, _ := testHeap(t, Config{NurseryWords: 256, OldWords: 1 << 14, CardWords: 8})
	roots := &rootSet{}
	h.SetRoots(roots.visitor())

	old := h.Alloc(pair, 2)
	roots.refs = append(roots.refs, old)
	h.MinorCollect() // promote `old` to the old generation
	old = roots.refs[0]

	young := h.Alloc(leaf, 1)
	h.Set(young, 0, 1234)
	h.SetRef(old, 0, young) // the barrier fires here

	h.MinorCollect()

	promoted := h.GetRef(roots.refs[0], 0)
	if promoted == layout.Nil {
		t.Fatal("the young object was lost: the write barrier did not record the store")
	}
	if got := h.Get(promoted, 0); got != 1234 {
		t.Errorf("the young object reads %d, want 1234", got)
	}
}

func TestByteArraysRoundTrip(t *testing.T) {
	h, _, _, bytes := testHeap(t, Config{NurseryWords: 1 << 12, OldWords: 1 << 14, CardWords: 8})
	roots := &rootSet{}
	h.SetRoots(roots.visitor())

	for _, s := range []string{"", "a", "hello", "a longer string that spans several words", "üñíçø∂é"} {
		r := h.AllocBytes(bytes, s)
		roots.refs = append(roots.refs, r)
	}
	h.MinorCollect()
	h.MajorCollect()

	want := []string{"", "a", "hello", "a longer string that spans several words", "üñíçø∂é"}
	for i, w := range want {
		if got := h.Bytes(roots.refs[i]); got != w {
			t.Errorf("byte array %d round-tripped as %q, want %q", i, got, w)
		}
	}
}

// TestRandomObjectGraphs is the property test process rule 3 requires: build random
// object graphs, drop parts of them, collect repeatedly, and assert that everything
// still reachable holds the value it was given.
//
// Every reference the test holds is a root. That is not a convenience — it is the
// mutator's contract with a moving collector, and the first version of this test broke
// it by caching references across an allocation that triggered a collection. The
// collector rewrote the roots and the cached copies became addresses of nothing. A
// mutator that holds a reference the collector cannot see is a mutator with a
// use-after-move bug, which is exactly what the VM's frame layout has to avoid.
func TestRandomObjectGraphs(t *testing.T) {
	for seed := int64(0); seed < 50; seed++ {
		t.Run("", func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			h, pair, leaf, bytes := testHeap(t, Config{
				NurseryWords: uint64(64 + rng.Intn(512)),
				OldWords:     1 << 16,
				CardWords:    uint64(4 + rng.Intn(60)),
			})
			roots := &rootSet{}
			h.SetRoots(roots.visitor())

			// values[i] is what roots.refs[i] should still read back as; a pair has no
			// scalar of its own, so it records nothing.
			var values []uint64
			var isLeaf []bool

			push := func(r layout.Ref, v uint64, leafy bool) {
				roots.refs = append(roots.refs, r)
				values = append(values, v)
				isLeaf = append(isLeaf, leafy)
			}
			drop := func(i int) {
				roots.refs = append(roots.refs[:i], roots.refs[i+1:]...)
				values = append(values[:i], values[i+1:]...)
				isLeaf = append(isLeaf[:i], isLeaf[i+1:]...)
			}

			for step := 0; step < 400; step++ {
				switch {
				case len(roots.refs) < 2 || rng.Intn(3) == 0:
					r := h.Alloc(leaf, 1)
					if r == layout.Nil {
						t.Fatalf("step %d: allocation failed", step)
					}
					v := rng.Uint64() &^ (1 << 63)
					h.Set(r, 0, v)
					push(r, v, true)

				case rng.Intn(8) == 0:
					s := "s" + string(rune('a'+rng.Intn(26)))
					r := h.AllocBytes(bytes, s)
					if r == layout.Nil {
						t.Fatalf("step %d: byte allocation failed", step)
					}
					push(r, 0, false)

				default:
					// Allocate first, then link: `Alloc` may collect, and the operands
					// must be roots across it.
					r := h.Alloc(pair, 2)
					if r == layout.Nil {
						t.Fatalf("step %d: allocation failed", step)
					}
					push(r, 0, false)
					h.SetRef(r, 0, roots.refs[rng.Intn(len(roots.refs))])
					h.SetRef(r, 1, roots.refs[rng.Intn(len(roots.refs))])
				}

				// Drop roots to create garbage, including objects other objects still
				// point at, so the collector has to keep those alive transitively.
				if len(roots.refs) > 8 && rng.Intn(4) == 0 {
					drop(rng.Intn(len(roots.refs)))
				}
				if rng.Intn(40) == 0 {
					h.MinorCollect()
				}
				if rng.Intn(150) == 0 {
					h.MajorCollect()
				}
			}

			h.MinorCollect()
			h.MajorCollect()
			h.MajorCollect()

			if h.OutOfMemory() {
				t.Fatal("the heap reported exhaustion; the test's sizes are wrong")
			}

			// Every root still reads back what it was given, and the graph beneath it
			// is walkable — a collected or corrupted object shows up as either.
			for i, r := range roots.refs {
				if isLeaf[i] {
					if got := h.Get(r, 0); got != values[i] {
						t.Fatalf("root %d reads %d, want %d", i, got, values[i])
					}
				}
			}
			seen := map[layout.Ref]bool{}
			var walk func(layout.Ref, int)
			walk = func(r layout.Ref, depth int) {
				if r == layout.Nil || seen[r] || depth > 10000 {
					return
				}
				seen[r] = true
				if h.TypeOf(r) == pair {
					walk(h.GetRef(r, 0), depth+1)
					walk(h.GetRef(r, 1), depth+1)
				}
			}
			for _, r := range roots.refs {
				walk(r, 0)
			}
		})
	}
}

func TestOutOfMemoryIsReportedNotPanicked(t *testing.T) {
	// spec/08: allocation may fail; it never returns an invalid reference, and the
	// mutator turns the failure into the `out of memory` trap.
	h, _, leaf, _ := testHeap(t, Config{NurseryWords: 16, OldWords: 32, CardWords: 8})
	roots := &rootSet{}
	h.SetRoots(roots.visitor())

	failed := false
	for i := 0; i < 1000; i++ {
		r := h.Alloc(leaf, 1)
		if r == layout.Nil {
			failed = true
			break
		}
		roots.refs = append(roots.refs, r) // keep everything alive
	}
	if !failed {
		t.Fatal("a tiny heap holding every object should have run out")
	}
	if !h.OutOfMemory() {
		t.Error("the heap did not report exhaustion")
	}
}

func TestCollectionWithoutRootsPanics(t *testing.T) {
	h, _, _, _ := testHeap(t, DefaultConfig())
	defer func() {
		if recover() == nil {
			t.Error("collecting with no root visitor must fail loudly, not silently collect everything")
		}
	}()
	h.MinorCollect()
}
