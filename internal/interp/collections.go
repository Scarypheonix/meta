package interp

import "github.com/scarypheonix/meta/internal/diag"

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
