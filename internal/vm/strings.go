package vm

import (
	"unicode/utf8"

	"github.com/scarypheonix/meta/internal/compile"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/layout"
)

// The string operations, on the virtual machine (spec/14-strings.md).
//
// A `String` is one heap object of internal/layout's ByteArray shape: payload word 0 is the
// length in bytes, and the bytes follow it. `Bytes` reads them back out as a Go string, so
// the bounds and boundary rules below are written over Go strings exactly as the
// interpreter's are -- one set of rules, checked the same way, which is what makes the two
// engines agree on which index traps and with which message.
//
// `slice` and `concat` allocate. Their arguments are in `temps`, which callBuiltin keeps as
// a root set, so a collection during either one cannot leave the operands dangling.

func (v *VM) strBuiltin(index int, args []Value, span diag.Span) (Value, bool) {
	span = v.userSpan(span)
	switch index {
	case compile.BuiltinStrLen:
		return intVal(int64(len(v.strArg(args[0], span)))), true

	case compile.BuiltinStrByteAt:
		s := v.strArg(args[0], span)
		return intVal(int64(s[v.byteIndex(s, args[1], span)])), true

	case compile.BuiltinStrSlice:
		s := v.strArg(args[0], span)
		a, b := int64(args[1].N), int64(args[2].N)
		if a < 0 || b > int64(len(s)) || a > b {
			v.trap(span, "index out of range")
		}
		v.mustBeBoundary(s, int(a), span)
		v.mustBeBoundary(s, int(b), span)
		return refVal(v.newString(s[a:b], span)), true

	case compile.BuiltinStrConcat:
		a := v.strArg(args[0], span)
		b := v.strArg(args[1], span)
		return refVal(v.newString(a+b, span)), true

	case compile.BuiltinStrCharAt:
		s := v.strArg(args[0], span)
		i := v.byteIndex(s, args[1], span)
		v.mustBeBoundary(s, i, span)
		r, _ := utf8.DecodeRuneInString(s[i:])
		return Value{Tag: layout.TagChar, N: uint64(r)}, true

	case compile.BuiltinStrCharWidth:
		s := v.strArg(args[0], span)
		i := v.byteIndex(s, args[1], span)
		v.mustBeBoundary(s, i, span)
		_, n := utf8.DecodeRuneInString(s[i:])
		return intVal(int64(n)), true
	}
	return Value{}, false
}

func (v *VM) strArg(val Value, span diag.Span) string {
	if val.Tag != layout.TagRef || val.R == layout.Nil {
		v.trap(span, "this operation expects a String")
	}
	return v.heap.Bytes(val.R)
}

// byteIndex bounds-checks an index that has to name a byte that exists. `len` itself does
// not: a slice may end there, but nothing can be read there.
func (v *VM) byteIndex(s string, val Value, span diag.Span) int {
	if int64(val.N) < 0 || int64(val.N) >= int64(len(s)) {
		v.trap(span, "index out of range")
	}
	return int(val.N)
}

// mustBeBoundary enforces spec/14-strings.md's rule that no operation may produce a String
// which is not valid UTF-8. A continuation byte (10xxxxxx) is the only thing that is not a
// boundary; `len` itself always is, which is what makes the empty slice at the end legal.
func (v *VM) mustBeBoundary(s string, i int, span diag.Span) {
	if i == len(s) {
		return
	}
	if s[i]&0xC0 == 0x80 {
		v.trap(span, "string index is not a character boundary")
	}
}
