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

// MaxStringBytes is the longest run of bytes one String object can hold: the header's own
// 24-bit size field, less the length word, times eight bytes to the word.
//
// It is here rather than in whichever subsystem happened to need it first because it is a
// fact about the header this package owns, and because three engines have to agree on it:
// spec/15-files.md makes a file too large for a `String` an `Err(Other)` rather than a
// trap on one engine and a success on another.
const MaxStringBytes = (int64(maxWordSize) - 1) * 8

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
	// RawArray is a length-prefixed run of words that are not references: an `Array[i64]`
	// or an `Array[f64]` (spec/13-collections.md, ADR-0028). Payload word 0 is the count,
	// exactly as RefArray's is, and the collector skips the rest as it skips a ByteArray's
	// bytes. Which of the two an array instantiation gets is decided while compiling, from
	// its element type, so the collector never has to guess whether a word is a pointer.
	RawArray
)

// ArrayDescriptor builds the descriptor for one array instantiation: a length-prefixed run
// whose shape says, once and for all, whether its elements are references (ADR-0028).
//
// The count in payload word 0 is the array's *length*, not its capacity: the object is as
// large as its header says, and everything above the length is room a push has not used
// yet. The collector traces exactly the length, which is what makes those spare slots cost
// nothing and hold nothing.
func ArrayDescriptor(name string, elem WordKind) *Descriptor {
	shape := RawArray
	if elem == WordRef {
		shape = RefArray
	}
	return &Descriptor{Name: name, Shape: shape, Kind: ObjArray, Elem: elem}
}

// ValueTag identifies a VM stack value's runtime kind. It has no bearing on heap object
// layout since ADR-0019 retired the `Tagged` shape; it lives here only because the VM
// and the collector both need a shared vocabulary for a value that is not yet in an
// object (a local, a temporary, an argument).
type ValueTag uint64

const (
	// TagUnit is the unit value.
	TagUnit ValueTag = iota
	// TagInt is an integer, held as its two's-complement bits.
	TagInt
	// TagFloat is a float, held as its IEEE bits.
	TagFloat
	// TagBool is a boolean, 0 or 1.
	TagBool
	// TagChar is a Unicode scalar value.
	TagChar
	// TagRef is a heap reference.
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
	// ObjArray is an `Array[T]` (spec/13-collections.md).
	ObjArray
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
	// WordRaw is an opaque raw word, compared by bits. Used directly only by tests;
	// real descriptors use one of the more specific kinds below so that Show can
	// render a field without a runtime tag to consult (ADR-0017).
	WordRaw WordKind = iota
	// WordRef is a reference the collector must trace and rewrite.
	WordRef
	// WordFloat holds IEEE bits and is compared as a float.
	WordFloat
	// WordInt is a signed or unsigned integer, compared by bits.
	WordInt
	// WordBool is 0 or 1.
	WordBool
	// WordChar is a Unicode scalar value.
	WordChar
	// WordUnit is the unit value; its word is unused.
	WordUnit
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
	// Elem is the kind of every element for a RefArray or a RawArray, which have one
	// kind for the whole run rather than one per word (ADR-0028). The collector reads
	// Shape and never this; the virtual machine reads this to compare and render an
	// element without a per-value tag.
	Elem WordKind
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
		// A name identifies a shape. Two descriptors sharing a name must therefore be
		// the same shape; returning the existing id for a *different* one silently
		// aliases two layouts, and every read through the loser's id then interprets
		// the wrong words -- an object's field read as a reference because some other
		// type of the same name had a reference there. That is precisely the failure
		// this check was added for: closure descriptors were named by their capture
		// count alone, so a closure capturing an `i64` and one capturing a reference
		// collapsed into whichever was registered first (process rule 8).
		if prev := r.descs[id]; !sameShape(prev, d) {
			panic(fmt.Sprintf(
				"two different layouts registered as %q: %v (%d words) and %v (%d words); "+
					"a descriptor's name must identify its shape",
				d.Name, prev.Kinds, prev.Words, d.Kinds, d.Words))
		}
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

// sameShape reports whether two descriptors lay memory out identically. Names, field
// names and the rest are documentation; the kinds and the word count are the contract
// the collector and the code generator both read.
func sameShape(a, b *Descriptor) bool {
	if a.Words != b.Words || a.Kind != b.Kind || len(a.Kinds) != len(b.Kinds) {
		return false
	}
	for i := range a.Kinds {
		if a.Kinds[i] != b.Kinds[i] {
			return false
		}
	}
	return true
}
