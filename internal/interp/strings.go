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
	case "env::arg_count":
		return Int(len(in.args)), true
	case "env::arg_at":
		i, ok := args[0].(Int)
		if !ok || int(i) < 0 || int(i) >= len(in.args) {
			in.trap(span, "index out of range")
		}
		return &Str{S: in.args[int(i)]}, true
	case "process::exit":
		code, ok := args[0].(Int)
		if !ok {
			in.trap(span, "`process::exit` takes an integer")
		}
		panic(exitRequest{code: int(code) & 0xFF})

	case "char::from_u32":
		n, ok := args[0].(Int)
		if !ok {
			in.trap(span, "`char::from_u32` takes an integer, found %s", TypeName(args[0]))
		}
		// The interpreter builds the `Option` itself. The bytecode compiler emits the
		// construction instead, out of a predicate the runtimes provide -- ADR-0025's
		// split, which exists because the native backend cannot make a prelude type.
		if !isScalarValue(uint64(n)) {
			return in.optionNone(span), true
		}
		return in.optionSome(Char(rune(n)), span), true

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

// isScalarValue reports whether a number is a Unicode scalar value: below 0x110000 and
// not a surrogate. It is the whole content of `char::from_u32`; the conversion itself is
// nothing, since a `char` and its scalar value are the same bits.
func isScalarValue(v uint64) bool {
	return v <= 0x10FFFF && !(v >= 0xD800 && v <= 0xDFFF)
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
