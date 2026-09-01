package vm

import (
	"math"

	"github.com/scarypheonix/meta/internal/compile"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/layout"
)

// The array operations, on the virtual machine (spec/13-collections.md).
//
// An `Array[T]` is one heap object: payload word 0 is the length, and the elements follow
// it. Capacity is the object's own size -- the header already says how many words were
// allocated -- so an array carries no field the collector would have to be told about, and
// asking for its capacity is arithmetic rather than a load.
//
// The collector traces exactly `length` elements (internal/gc's RefArray arm), which is
// what makes the room above the length free of meaning as well as free of cost: no
// reference lives there, so nothing has to be kept alive or rewritten there.

// arrayElem reads element i, tagging it by the descriptor's single element kind. An array
// has one kind for its whole run rather than one per word, so this is readField's shape
// with `Elem` in place of `Kinds[i]`.
func (v *VM) arrayElem(desc *layout.Descriptor, r layout.Ref, i uint64) Value {
	w := arrayFirst + i
	switch desc.Elem {
	case layout.WordRef:
		return refVal(v.heap.GetRef(r, w))
	case layout.WordFloat:
		return Value{Tag: layout.TagFloat, N: v.heap.Get(r, w)}
	case layout.WordBool:
		return Value{Tag: layout.TagBool, N: v.heap.Get(r, w)}
	case layout.WordChar:
		return Value{Tag: layout.TagChar, N: v.heap.Get(r, w)}
	case layout.WordUnit:
		return Value{Tag: layout.TagUnit}
	default:
		return Value{Tag: layout.TagInt, N: v.heap.Get(r, w)}
	}
}

func (v *VM) setArrayElem(desc *layout.Descriptor, r layout.Ref, i uint64, val Value, span diag.Span) {
	w := arrayFirst + i
	if desc.Elem == layout.WordRef {
		v.heap.SetRef(r, w, v.boxIfFn(val, span).R)
		return
	}
	v.heap.Set(r, w, val.N)
}

// arrayFirst is where the elements start: payload word 0 is the length.
const arrayFirst = 1

// arrayOf reads the array argument, its descriptor and its length together, since every
// operation below needs all three.
func (v *VM) arrayOf(val Value, span diag.Span) (layout.Ref, *layout.Descriptor, uint64) {
	if val.Tag != layout.TagRef || val.R == layout.Nil {
		v.trap(span, "this operation expects an Array")
	}
	desc := v.prog.Types.Get(v.heap.TypeOf(val.R))
	if desc.Shape != layout.RefArray && desc.Shape != layout.RawArray {
		v.trap(span, "this operation expects an Array")
	}
	return val.R, desc, v.heap.Get(val.R, 0)
}

// arrayIndex bounds-checks against the length, never the capacity: a slot nothing has
// pushed to is not a slot (spec/13-collections.md).
func (v *VM) arrayIndex(n uint64, val Value, span diag.Span) uint64 {
	if int64(val.N) < 0 || uint64(val.N) >= n {
		v.trap(span, "index out of range")
	}
	return val.N
}

// arrayBuiltin dispatches the std::array operations, and reports whether it handled the
// index.
func (v *VM) arrayBuiltin(index int, args []Value, span diag.Span) (Value, bool) {
	span = v.userSpan(span)
	switch index {
	case compile.BuiltinHash:
		return intVal(int64(v.hashOf(args[0], span))), true

	case compile.BuiltinNewArray:
		// args[1] is the layout internal/compile picked for this instantiation: whether
		// the elements are references, which the collector must know and the runtime
		// cannot work out (ADR-0028).
		if int64(args[0].N) < 0 {
			v.trap(span, "array capacity is negative")
		}
		capacity := args[0].N
		r := v.alloc(layout.TypeID(args[1].N), arrayFirst+capacity, span)
		v.heap.Set(r, 0, 0)
		return refVal(r), true

	case compile.BuiltinArrayLen:
		_, _, n := v.arrayOf(args[0], span)
		return intVal(int64(n)), true

	case compile.BuiltinArrayCap:
		r, _, _ := v.arrayOf(args[0], span)
		return intVal(int64(v.heap.Words(r) - arrayFirst)), true

	case compile.BuiltinArrayAt:
		r, desc, n := v.arrayOf(args[0], span)
		return v.arrayElem(desc, r, v.arrayIndex(n, args[1], span)), true

	case compile.BuiltinArraySet:
		r, desc, n := v.arrayOf(args[0], span)
		v.setArrayElem(desc, r, v.arrayIndex(n, args[1], span), args[2], span)
		return unitVal(), true

	case compile.BuiltinArrayPush:
		r, desc, n := v.arrayOf(args[0], span)
		if n >= v.heap.Words(r)-arrayFirst {
			return boolVal(false), true
		}
		// The element lands before the length grows, so a collection between the two --
		// the boxing below can allocate -- sees a length that describes only words that
		// are written.
		v.setArrayElem(desc, r, n, args[1], span)
		v.heap.Set(r, 0, n+1)
		return boolVal(true), true

	case compile.BuiltinArrayTruncate:
		r, _, n := v.arrayOf(args[0], span)
		want := int64(args[1].N)
		if want < 0 {
			want = 0
		}
		if uint64(want) < n {
			v.heap.Set(r, 0, uint64(want))
		}
		return unitVal(), true
	}
	return Value{}, false
}

// The specified hash (spec/13-collections.md's "Hashing").
//
// The same 64-bit FNV-1a over the same encoding the interpreter and native code use --
// specified rather than left to each engine, because `hash::of` returns a number a program
// can print. What differs here is only where the parts come from: a descriptor's word
// kinds, rather than Go values or a type table.
const (
	fnvOffset = 14695981039346656037
	fnvPrime  = 1099511628211
)

func fnvWord(h, w uint64) uint64 {
	for i := 0; i < 8; i++ {
		h = (h ^ (w>>(8*i))&0xFF) * fnvPrime
	}
	return h
}

func fnvBytes(h uint64, s string) uint64 {
	for i := 0; i < len(s); i++ {
		h = (h ^ uint64(s[i])) * fnvPrime
	}
	return h
}

// hashOf is a value's own hash, following the same table the other two engines do: FNV-1a
// from the offset basis, with a composite folding each part's own hash in as a word.
func (v *VM) hashOf(val Value, span diag.Span) uint64 {
	switch val.Tag {
	case layout.TagUnit:
		return fnvOffset // nothing at all is fed in
	case layout.TagFloat:
		bits := val.N
		if math.Float64frombits(bits) == 0 { // -0.0 hashes as 0.0, because they compare equal
			bits = 0
		}
		return fnvWord(fnvOffset, bits)
	case layout.TagRef:
		return v.hashObject(val.R, span)
	case layout.TagFn, layout.TagBuiltin:
		v.trap(span, "a function value cannot be hashed")
	}
	return fnvWord(fnvOffset, val.N)
}

// hashObject hashes one heap object: a String over its bytes, everything else by folding
// each part's own hash in as a word.
func (v *VM) hashObject(r layout.Ref, span diag.Span) uint64 {
	if r == layout.Nil {
		return fnvWord(fnvOffset, 0)
	}
	desc := v.prog.Types.Get(v.heap.TypeOf(r))
	h := uint64(fnvOffset)
	switch desc.Shape {
	case layout.ByteArray:
		return fnvBytes(h, v.heap.Bytes(r))
	case layout.RefArray, layout.RawArray:
		n := v.heap.Get(r, 0)
		for i := uint64(0); i < n; i++ {
			h = fnvWord(h, v.hashOf(v.arrayElem(desc, r, i), span))
		}
		return h
	default:
		for i := range desc.Kinds {
			h = fnvWord(h, v.hashOf(v.readField(desc, r, i), span))
		}
		return h
	}
}
