// Package layout owns the representation the garbage collector and the code generator
// must agree on: object headers, field offsets, which words hold references, and (from
// Phase 5) stack maps and safepoint placement.
//
// Process rule 5 requires that agreement to live in exactly one module with its own
// tests. Everything the collector knows about an object's shape comes from here, and so
// will everything the backend knows. Neither may derive it independently: a heap where
// the two disagree about which word is a pointer is a heap that corrupts silently.
//
// The format is specified by docs/spec/08-memory-model.md.
package layout

import "fmt"

// Ref is a reference to a heap object: a word index into the collector's arena.
//
// A moving collector rewrites references, so a Ref is only valid until the next
// collection unless it is reachable from a root. Zero is not a valid object and is used
// as the "no object" marker inside the collector; Origin itself has no null (ADR-0007).
type Ref uint64

// Nil is the reference that names no object.
const Nil Ref = 0

// TypeID identifies an object's descriptor.
type TypeID uint32

// Header is the first word of every heap object.
//
//	bit  63     forwarded: the rest of the word is the object's new address
//	bits 32..55 payload size in words
//	bits 0..31  TypeID
//
// A forwarding pointer overwrites the header during a copy, which is why the size and
// type must be read before an object is moved, and why the to-space copy is the only
// authority afterwards.
type Header uint64

const (
	forwardBit  = uint64(1) << 63
	sizeShift   = 32
	sizeMask    = uint64(0xFFFFFF) // 24 bits: up to 16Mi words in one object
	typeIDMask  = uint64(0xFFFFFFFF)
	maxWordSize = sizeMask
)

// MakeHeader builds a header for an object of the given type and payload size.
func MakeHeader(t TypeID, words uint64) Header {
	if words > maxWordSize {
		panic(fmt.Sprintf("object of %d words exceeds the %d-word limit", words, maxWordSize))
	}
	return Header(uint64(t) | (words << sizeShift))
}

// Forwarded reports whether the object has been moved, and to where.
func (h Header) Forwarded() (Ref, bool) {
	if uint64(h)&forwardBit == 0 {
		return Nil, false
	}
	return Ref(uint64(h) &^ forwardBit), true
}

// MakeForward builds a header that points at an object's new address.
func MakeForward(to Ref) Header { return Header(uint64(to) | forwardBit) }

// TypeID returns the object's type, which is meaningless once it is forwarded.
func (h Header) TypeID() TypeID { return TypeID(uint64(h) & typeIDMask) }

// Words returns the payload size, which is meaningless once the object is forwarded.
func (h Header) Words() uint64 { return (uint64(h) >> sizeShift) & sizeMask }

// Shape says how a descriptor describes an object's payload.
type Shape uint8

const (
	// Fixed is a struct-like object: a fixed number of payload words, with RefBits
	// saying which of them hold references.
	Fixed Shape = iota
	// RefArray is a length-prefixed run of references: payload word 0 is the count,
	// and every word after it is a reference.
	RefArray
	// ByteArray is a length-prefixed run of raw bytes: payload word 0 is the length in
	// bytes, and the bytes follow. It holds no references, which is what makes a
	// `String` cheap for the collector to skip.
	ByteArray
)

// Descriptor describes one object shape to the collector.
type Descriptor struct {
	// Name is used in diagnostics and heap dumps.
	Name string
	// Shape selects how Payload words are interpreted.
	Shape Shape
	// Words is the payload size for a Fixed shape, in words.
	Words uint64
	// RefBits is a bitmap over payload words for a Fixed shape: bit i is set when
	// payload word i holds a reference.
	RefBits []uint64
}

// IsRef reports whether payload word i of a Fixed object holds a reference.
func (d *Descriptor) IsRef(i uint64) bool {
	if d.Shape != Fixed {
		return false
	}
	word := i / 64
	if word >= uint64(len(d.RefBits)) {
		return false
	}
	return d.RefBits[word]&(1<<(i%64)) != 0
}

// SetRef marks payload word i of a Fixed object as holding a reference.
func (d *Descriptor) SetRef(i uint64) {
	word := i / 64
	for uint64(len(d.RefBits)) <= word {
		d.RefBits = append(d.RefBits, 0)
	}
	d.RefBits[word] |= 1 << (i % 64)
}

// Registry maps TypeIDs to descriptors. One registry serves one program: descriptors
// are created while lowering and read by the collector, and neither side may invent one
// the other has not seen.
type Registry struct {
	descs  []*Descriptor
	byName map[string]TypeID
}

// NewRegistry returns an empty registry. TypeID 0 is reserved so that a zeroed word is
// never a valid descriptor.
func NewRegistry() *Registry {
	return &Registry{
		descs:  []*Descriptor{{Name: "<invalid>"}},
		byName: map[string]TypeID{},
	}
}

// Add registers a descriptor and returns its id. Registering the same name twice returns
// the existing id, so lowering may declare a type wherever it first needs it.
func (r *Registry) Add(d *Descriptor) TypeID {
	if id, ok := r.byName[d.Name]; ok {
		return id
	}
	id := TypeID(len(r.descs))
	r.descs = append(r.descs, d)
	r.byName[d.Name] = id
	return id
}

// Get returns a descriptor by id. An unknown id is a bug in whichever side wrote it, so
// it panics rather than returning a plausible shape and corrupting the heap.
func (r *Registry) Get(t TypeID) *Descriptor {
	if int(t) >= len(r.descs) || t == 0 {
		panic(fmt.Sprintf("unknown TypeID %d: the heap and the descriptor table disagree", t))
	}
	return r.descs[t]
}

// Lookup finds a descriptor's id by name.
func (r *Registry) Lookup(name string) (TypeID, bool) {
	id, ok := r.byName[name]
	return id, ok
}

// Len reports how many descriptors are registered, including the reserved zero.
func (r *Registry) Len() int { return len(r.descs) }

// FixedDescriptor builds a Fixed descriptor from a list saying which fields are
// references, in declaration order.
func FixedDescriptor(name string, isRef []bool) *Descriptor {
	d := &Descriptor{Name: name, Shape: Fixed, Words: uint64(len(isRef))}
	for i, r := range isRef {
		if r {
			d.SetRef(uint64(i))
		}
	}
	return d
}
