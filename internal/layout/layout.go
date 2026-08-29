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
	// Tagged is a run of slots, each two words: a ValueTag and its payload.
	//
	// It exists because Origin 0.1 does not monomorphize yet (ADR-0010, deferred to
	// Phase 4), so the field of a generic struct has no statically known shape: the
	// payload of `Option[T]` is a reference for `T = String` and a raw word for
	// `T = i64`. A tag written beside each slot answers that at run time while keeping
	// the collector precise -- it reads the tag, it does not guess from the bits.
	//
	// The cost is two words per slot. Phase 4 replaces it with exact layouts once
	// monomorphization gives every instantiation its own descriptor.
	Tagged
)

// ValueTag identifies what a Tagged slot holds. The collector reads it to decide
// whether the word beside it is a reference, so this is part of the shared contract and
// not a detail of the VM.
type ValueTag uint64

const (
	// TagUnit is the unit value; its payload word is unused.
	TagUnit ValueTag = iota
	// TagInt is an integer, held as its two's-complement bits.
	TagInt
	// TagFloat is a float, held as its IEEE bits.
	TagFloat
	// TagBool is a boolean, 0 or 1.
	TagBool
	// TagChar is a Unicode scalar value.
	TagChar
	// TagRef is a heap reference: the collector traces and rewrites the payload word.
	TagRef
	// TagFn is a top-level function index.
	TagFn
	// TagBuiltin is a compiler-provided function index.
	TagBuiltin
)

// ObjKind says what an object is, for structural equality and for rendering. The
// collector ignores it; the VM cannot, because `Point { x: 1 }` and a two-element tuple
// have the same shape and must not print or compare the same way.
type ObjKind uint8

const (
	// ObjStruct is a struct instance.
	ObjStruct ObjKind = iota
	// ObjEnum is one variant of an enum. Each variant has its own descriptor, so a
	// variant test is a type-id comparison and needs no tag slot.
	ObjEnum
	// ObjTuple is a tuple.
	ObjTuple
	// ObjClosure is a function value: slot 0 is the function index, the rest are its
	// captures.
	ObjClosure
	// ObjBytes is a String.
	ObjBytes
)

// WordKind says how one payload word is to be read.
//
// The collector only needs to know which words are references. The VM needs more: it
// compares aggregates structurally (spec/04-expressions.md), and a float compares by
// IEEE rules rather than by bits, so `NaN != NaN` and `0.0 == -0.0` hold inside a struct
// exactly as they do outside one. Both readings come from this one table, so the two
// cannot drift apart.
type WordKind uint8

const (
	// WordRaw is an integer, bool, char or unit: compared by bits.
	WordRaw WordKind = iota
	// WordRef is a reference the collector must trace and rewrite.
	WordRef
	// WordFloat holds IEEE bits and is compared as a float.
	WordFloat
)

// Descriptor describes one object shape.
type Descriptor struct {
	// Name is used in diagnostics and heap dumps.
	Name string
	// Shape selects how the payload words are interpreted.
	Shape Shape
	// Words is the payload size for a Fixed shape, in words.
	Words uint64
	// Kinds gives the kind of each payload word for a Fixed shape.
	Kinds []WordKind
	// Slots is the number of tagged slots for a Tagged shape.
	Slots int
	// Kind says what the object is, for equality and rendering.
	Kind ObjKind
	// FieldNames names a struct's or a struct variant's slots, in declaration order.
	// It is empty for a tuple variant, a tuple or a closure.
	FieldNames []string
	// TypeName is the enum's or struct's declared name; VariantName is the variant's,
	// for an ObjEnum descriptor.
	TypeName    string
	VariantName string
}

// TaggedDescriptor builds a descriptor for an object of n tagged slots.
func TaggedDescriptor(name string, kind ObjKind, slots int) *Descriptor {
	return &Descriptor{Name: name, Shape: Tagged, Words: uint64(slots) * 2, Slots: slots, Kind: kind}
}

// IsRef reports whether payload word i holds a reference.
func (d *Descriptor) IsRef(i uint64) bool {
	return d.Shape == Fixed && i < uint64(len(d.Kinds)) && d.Kinds[i] == WordRef
}

// IsFloat reports whether payload word i holds IEEE float bits.
func (d *Descriptor) IsFloat(i uint64) bool {
	return d.Shape == Fixed && i < uint64(len(d.Kinds)) && d.Kinds[i] == WordFloat
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

// FixedDescriptor builds a Fixed descriptor from the kind of each field, in declaration
// order.
func FixedDescriptor(name string, kinds []WordKind) *Descriptor {
	return &Descriptor{Name: name, Shape: Fixed, Words: uint64(len(kinds)), Kinds: kinds}
}

// RefsOnly builds the kind list for an object whose fields are all references, or all
// raw, according to isRef.
func RefsOnly(isRef []bool) []WordKind {
	kinds := make([]WordKind, len(isRef))
	for i, r := range isRef {
		if r {
			kinds[i] = WordRef
		}
	}
	return kinds
}
