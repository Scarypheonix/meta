package interp

import (
	"encoding/binary"
	"math"

	"github.com/scarypheonix/meta/internal/diag"
)

// The array operations, interpreted (spec/13-collections.md).
//
// The interpreter's values are Go values, so an `Array[T]` is a Go slice and a capacity,
// and none of the layout work the other two engines need applies here: the host collector
// finds an element wherever it is. What does apply is the *semantics*, which all three
// engines have to agree on to the trap message -- so the bounds checks and the full-array
// answer are written here in the same terms the specification uses, and the differential
// suite is what keeps them the same terms.

// Array is a fixed-capacity, growable-length run of elements. `Elems` holds exactly the
// elements that have been pushed; `Cap` is how many the array has room for. Nothing here
// ever reads past `len(Elems)`, which is the property that keeps ADR-0007's "no
// uninitialized bindings" true for a structure that grows.
type Array struct {
	Elems []Value
	Cap   int
}

func (*Array) isValue() {}

// arrayBuiltin dispatches the std::array operations, reporting whether it handled the
// name. The trailing type-layout argument `array::new` carries for the other two engines
// is ignored: the interpreter has no object layouts to be told about.
func (in *Interp) arrayBuiltin(name string, args []Value, span diag.Span) (Value, bool) {
	switch name {
	case "hash::of":
		return Int(int64(in.hashOf(args[0], span))), true

	case "map::new":
		// The other two engines never reach this: internal/compile writes an empty entry
		// list and a zeroed index out of operations that already exist. The interpreter
		// walks the syntax tree, so it builds the same two fields here.
		def := in.res.Structs["Map"]
		if def == nil {
			in.trap(span, "the prelude does not define `Map`")
		}
		entries := in.res.Structs["List"]
		if entries == nil {
			in.trap(span, "the prelude does not define `List`")
		}
		index := &Array{Elems: make([]Value, mapInitialSlots), Cap: mapInitialSlots}
		for i := range index.Elems {
			index.Elems[i] = Int(0)
		}
		return &Struct{Def: def, Vals: []Value{
			&Struct{Def: entries, Vals: []Value{&Array{}}},
			index,
		}}, true

	case "list::new":
		// The other two engines never reach a `list::new` at all: internal/compile
		// writes it out as an empty array wrapped in the prelude's own struct. The
		// interpreter walks the syntax tree, so it builds the same thing here.
		def := in.res.Structs["List"]
		if def == nil {
			in.trap(span, "the prelude does not define `List`")
		}
		return &Struct{Def: def, Vals: []Value{&Array{}}}, true

	case "array::new":
		n, ok := args[0].(Int)
		if !ok {
			in.trap(span, "`array::new` takes an integer capacity")
		}
		if n < 0 {
			in.trap(span, "array capacity is negative")
		}
		return &Array{Elems: make([]Value, 0, int(n)), Cap: int(n)}, true

	case "array::len":
		return Int(len(in.arrayArg(args[0], span).Elems)), true

	case "array::cap":
		return Int(in.arrayArg(args[0], span).Cap), true

	case "array::at":
		a := in.arrayArg(args[0], span)
		return a.Elems[in.arrayIndex(a, args[1], span)], true

	case "array::set":
		a := in.arrayArg(args[0], span)
		a.Elems[in.arrayIndex(a, args[1], span)] = args[2]
		return Unit{}, true

	case "array::push":
		a := in.arrayArg(args[0], span)
		if len(a.Elems) >= a.Cap {
			return Bool(false), true
		}
		a.Elems = append(a.Elems, args[1])
		return Bool(true), true

	case "array::truncate":
		a := in.arrayArg(args[0], span)
		n, ok := args[1].(Int)
		if !ok {
			in.trap(span, "`array::truncate` takes an integer length")
		}
		if n < 0 {
			n = 0
		}
		if int(n) < len(a.Elems) {
			a.Elems = a.Elems[:int(n)]
		}
		return Unit{}, true
	}
	return nil, false
}

// mapInitialSlots is how much room a fresh map's index has, and must agree with
// internal/compile's own constant: the prelude divides by the capacity, so a map that
// started with a different one would probe differently on this engine.
const mapInitialSlots = 8

func (in *Interp) arrayArg(v Value, span diag.Span) *Array {
	a, ok := v.(*Array)
	if !ok {
		in.trap(span, "this operation expects an Array")
	}
	return a
}

// arrayIndex bounds-checks an index against the array's *length*, not its capacity: a slot
// nothing has pushed to is not a slot, and reading one would be exactly the undefined
// behaviour §04 leaves no room for.
func (in *Interp) arrayIndex(a *Array, v Value, span diag.Span) int {
	i, ok := v.(Int)
	if !ok {
		in.trap(span, "an array index must be an integer")
	}
	if i < 0 || int(i) >= len(a.Elems) {
		in.trap(span, "index out of range")
	}
	return int(i)
}

// The specified hash (spec/13-collections.md's "Hashing").
//
// 64-bit FNV-1a over an encoding fixed by the specification rather than by whichever engine
// got there first, because `hash::of` returns a number a program can print and the three
// engines have to agree on it. Every arm below is one row of that document's table.
const (
	fnvOffset = 14695981039346656037
	fnvPrime  = 1099511628211
)

func fnvBytes(h uint64, bs []byte) uint64 {
	for _, b := range bs {
		h = (h ^ uint64(b)) * fnvPrime
	}
	return h
}

func fnvWord(h uint64, w uint64) uint64 {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], w)
	return fnvBytes(h, buf[:])
}

// hashOf is a value's own hash: FNV-1a from the offset basis over the encoding
// spec/13-collections.md's table gives it. A composite folds each part's own hash in as a
// word, so what a value hashes to never depends on where it sits.
func (in *Interp) hashOf(v Value, span diag.Span) uint64 {
	switch t := v.(type) {
	case Int:
		return fnvWord(fnvOffset, uint64(t))
	case Float:
		f := float64(t)
		if f == 0 { // -0.0 hashes as 0.0, because they compare equal
			f = 0
		}
		return fnvWord(fnvOffset, math.Float64bits(f))
	case Bool:
		if t {
			return fnvWord(fnvOffset, 1)
		}
		return fnvWord(fnvOffset, 0)
	case Char:
		return fnvWord(fnvOffset, uint64(t))
	case Unit:
		return fnvOffset // nothing at all is fed in
	case *Str:
		return fnvBytes(fnvOffset, []byte(t.S))
	case *Tuple:
		return in.hashParts(t.Elems, span)
	case *Struct:
		return in.hashParts(t.Vals, span)
	case *Enum:
		return in.hashParts(t.Vals, span)
	case *Array:
		return in.hashParts(t.Elems, span)
	}
	in.trap(span, "a function value cannot be hashed")
	return fnvOffset
}

func (in *Interp) hashParts(parts []Value, span diag.Span) uint64 {
	h := uint64(fnvOffset)
	for _, p := range parts {
		h = fnvWord(h, in.hashOf(p, span))
	}
	return h
}
