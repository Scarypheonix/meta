package layout

import "testing"

func TestHeaderRoundTrip(t *testing.T) {
	for _, tt := range []struct {
		id    TypeID
		words uint64
	}{{1, 0}, {1, 1}, {42, 7}, {0xFFFFFFFF, 0xFFFFFF}} {
		h := MakeHeader(tt.id, tt.words)
		if got := h.TypeID(); got != tt.id {
			t.Errorf("TypeID round-tripped as %d, want %d", got, tt.id)
		}
		if got := h.Words(); got != tt.words {
			t.Errorf("Words round-tripped as %d, want %d", got, tt.words)
		}
		if _, moved := h.Forwarded(); moved {
			t.Error("a fresh header must not look forwarded")
		}
	}
}

func TestForwardingPointer(t *testing.T) {
	// The forwarding bit overwrites the header, so a forwarded object's type and size
	// are gone. Anything that needs them must read them before the copy.
	h := MakeForward(Ref(12345))
	to, moved := h.Forwarded()
	if !moved {
		t.Fatal("a forwarding header must report itself as forwarded")
	}
	if to != Ref(12345) {
		t.Errorf("forwarded to %d, want 12345", to)
	}
}

func TestOversizedObjectPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("an object too large for the header's size field must fail loudly")
		}
	}()
	MakeHeader(1, 1<<24)
}

func TestWordKinds(t *testing.T) {
	d := FixedDescriptor("Mixed", []WordKind{WordRaw, WordRef, WordFloat})
	for i, want := range []struct{ isRef, isFloat bool }{
		{false, false}, {true, false}, {false, true},
	} {
		if got := d.IsRef(uint64(i)); got != want.isRef {
			t.Errorf("IsRef(%d) = %v, want %v", i, got, want.isRef)
		}
		if got := d.IsFloat(uint64(i)); got != want.isFloat {
			t.Errorf("IsFloat(%d) = %v, want %v", i, got, want.isFloat)
		}
	}
	if d.IsRef(99) || d.IsFloat(99) {
		t.Error("a word past the end must read as neither")
	}
}

func TestNonFixedShapesHaveNoWordKinds(t *testing.T) {
	// A ByteArray holds no references, which is what lets the collector skip a String
	// entirely; asking about its words must not accidentally say otherwise.
	d := &Descriptor{Name: "Bytes", Shape: ByteArray}
	if d.IsRef(0) || d.IsFloat(0) {
		t.Error("a ByteArray's words are raw bytes")
	}
}

func TestRegistry(t *testing.T) {
	r := NewRegistry()
	if r.Len() != 1 {
		t.Errorf("a new registry has %d entries, want 1 (the reserved zero)", r.Len())
	}
	a := r.Add(FixedDescriptor("A", []WordKind{WordRef}))
	b := r.Add(FixedDescriptor("B", []WordKind{WordRaw}))
	if a == 0 || b == 0 || a == b {
		t.Errorf("ids must be distinct and non-zero, got %d and %d", a, b)
	}
	if again := r.Add(FixedDescriptor("A", []WordKind{WordRef})); again != a {
		t.Errorf("registering the same name twice gave %d then %d", a, again)
	}
	if got, ok := r.Lookup("B"); !ok || got != b {
		t.Error("Lookup did not find a registered descriptor")
	}
	if _, ok := r.Lookup("nope"); ok {
		t.Error("Lookup found a descriptor that was never registered")
	}
}

func TestUnknownTypeIDPanics(t *testing.T) {
	// A heap word naming a descriptor nobody registered means the two sides disagree
	// about layout, which corrupts silently if it is allowed to continue.
	r := NewRegistry()
	defer func() {
		if recover() == nil {
			t.Error("an unknown TypeID must fail loudly")
		}
	}()
	r.Get(99)
}

func TestReservedZeroTypeIDPanics(t *testing.T) {
	r := NewRegistry()
	defer func() {
		if recover() == nil {
			t.Error("TypeID 0 is reserved so that a zeroed word is never a valid descriptor")
		}
	}()
	r.Get(0)
}
