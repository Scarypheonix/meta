package interp

import (
	"math"
	"unicode/utf8"

	"github.com/scarypheonix/meta/internal/diag"
)

// The string operations, interpreted (spec/14-strings.md).
//
// A `String` is a Go string here, so `len` is a field read and `concat` is a `+`. What is
// not free is the part that has to be identical on three engines: which indices are legal,
// which of the two trap messages an illegal one produces, and what a character boundary is.
// Those are written out below in the specification's own terms rather than borrowed from
// Go's, because Go would silently do something reasonable -- `s[a:b]` on a non-boundary
// yields invalid UTF-8, and `utf8.DecodeRuneInString` returns RuneError -- and "reasonable"
// is not the same answer as "the same answer".

// strBuiltin dispatches the std::str operations, reporting whether it handled the name.
// floatBuiltin dispatches the std::float operations, reporting whether it handled the name.
//
// Neither one computes anything: a `f64` and a `u64` are the same sixty-four bits, and what
// these say is only which of the two ways to read them applies from here on. They are what
// makes the decimal rendering of a float Origin source in the prelude (spec/16-floats.md)
// rather than a shortest-round-trip algorithm written once per engine.
func (in *Interp) floatBuiltin(name string, args []Value, span diag.Span) (Value, bool) {
	switch name {
	case "float::bits":
		f, ok := args[0].(Float)
		if !ok {
			in.trap(span, "`float::bits` takes a float, found %s", TypeName(args[0]))
		}
		return Int(int64(math.Float64bits(float64(f)))), true
	case "float::from_bits":
		n, ok := args[0].(Int)
		if !ok {
			in.trap(span, "`float::from_bits` takes an integer, found %s", TypeName(args[0]))
		}
		return Float(math.Float64frombits(uint64(n))), true
	}
	return nil, false
}

func (in *Interp) strBuiltin(name string, args []Value, span diag.Span) (Value, bool) {
	switch name {
	case "str::len":
		return Int(len(in.strArg(args[0], span))), true

	case "str::byte_at":
		s := in.strArg(args[0], span)
		return Int(s[in.byteIndex(s, args[1], span)]), true

	case "str::slice":
		s := in.strArg(args[0], span)
		a, b := in.sliceIndex(args[1], span), in.sliceIndex(args[2], span)
		if a < 0 || b > len(s) || a > b {
			in.trap(span, "index out of range")
		}
		in.mustBeBoundary(s, a, span)
		in.mustBeBoundary(s, b, span)
		return &Str{S: s[a:b]}, true

	case "str::concat":
		return &Str{S: in.strArg(args[0], span) + in.strArg(args[1], span)}, true

	case "str::char_at":
		s := in.strArg(args[0], span)
		i := in.byteIndex(s, args[1], span)
		in.mustBeBoundary(s, i, span)
		r, _ := utf8.DecodeRuneInString(s[i:])
		return Char(r), true

	case "str::char_width":
		s := in.strArg(args[0], span)
		i := in.byteIndex(s, args[1], span)
		in.mustBeBoundary(s, i, span)
		_, n := utf8.DecodeRuneInString(s[i:])
		return Int(n), true
	}
	return nil, false
}

func (in *Interp) strArg(v Value, span diag.Span) string {
	s, ok := v.(*Str)
	if !ok {
		in.trap(span, "this operation expects a String")
	}
	return s.S
}

// byteIndex bounds-checks an index that has to name a byte that exists, which `len` itself
// does not: a slice may end there, but nothing can be read there.
func (in *Interp) byteIndex(s string, v Value, span diag.Span) int {
	i, ok := v.(Int)
	if !ok {
		in.trap(span, "a string index must be an integer")
	}
	if i < 0 || int(i) >= len(s) {
		in.trap(span, "index out of range")
	}
	return int(i)
}

func (in *Interp) sliceIndex(v Value, span diag.Span) int {
	i, ok := v.(Int)
	if !ok {
		in.trap(span, "a string index must be an integer")
	}
	return int(i)
}

// mustBeBoundary enforces spec/14-strings.md's rule that no operation may produce a String
// which is not valid UTF-8. A continuation byte (10xxxxxx) is the only thing that is not a
// boundary; `len` itself always is, which is what makes the empty slice at the end legal.
func (in *Interp) mustBeBoundary(s string, i int, span diag.Span) {
	if i == len(s) {
		return
	}
	if s[i]&0xC0 == 0x80 {
		in.trap(span, "string index is not a character boundary")
	}
}
