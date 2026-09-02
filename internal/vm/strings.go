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
// length in bytes, and the bytes follow it, eight to a word.
//
// The operations that read one byte read *one word* and shift, rather than asking the heap
// for the whole object as a Go string. That is not a micro-optimization: `heap.Bytes`
// copies the entire object, so a `byte_at` written over it makes any loop across a string
// quadratic, and the prelude is full of such loops -- `is_valid_utf8` over a 128 KiB file
// took longer than the whole end-to-end suite. The interpreter never had the problem (its
// String is a Go string) and neither does native code (`rt_str_byte_at` is two loads), so
// this is also what keeps the three engines the same shape of fast.
//
// `slice` and `concat` are O(n) by nature and use `Bytes`. Their arguments are in `temps`,
// which callBuiltin keeps as a root set, so a collection during either one cannot leave the
// operands dangling.

func (v *VM) strBuiltin(index int, args []Value, span diag.Span) (Value, bool) {
	span = v.userSpan(span)
	switch index {
	case compile.BuiltinStrLen:
		r := v.strRef(args[0], span)
		return intVal(int64(v.strLen(r))), true

	case compile.BuiltinStrByteAt:
		r := v.strRef(args[0], span)
		return intVal(int64(v.strByte(r, v.byteIndex(r, args[1], span)))), true

	case compile.BuiltinStrSlice:
		r := v.strRef(args[0], span)
		n := int64(v.strLen(r))
		a, b := int64(args[1].N), int64(args[2].N)
		if a < 0 || b > n || a > b {
			v.trap(span, "index out of range")
		}
		v.mustBeBoundary(r, a, span)
		v.mustBeBoundary(r, b, span)
		return refVal(v.newString(v.heap.Bytes(r)[a:b], span)), true

	case compile.BuiltinStrConcat:
		a := v.strRef(args[0], span)
		b := v.strRef(args[1], span)
		return refVal(v.newString(v.heap.Bytes(a)+v.heap.Bytes(b), span)), true

	case compile.BuiltinStrCharAt:
		r := v.strRef(args[0], span)
		i := v.byteIndex(r, args[1], span)
		v.mustBeBoundary(r, i, span)
		ch, _ := utf8.DecodeRuneInString(v.strTail(r, i))
		return Value{Tag: layout.TagChar, N: uint64(ch)}, true

	case compile.BuiltinStrCharWidth:
		r := v.strRef(args[0], span)
		i := v.byteIndex(r, args[1], span)
		v.mustBeBoundary(r, i, span)
		_, n := utf8.DecodeRuneInString(v.strTail(r, i))
		return intVal(int64(n)), true
	}
	return Value{}, false
}

func (v *VM) strRef(val Value, span diag.Span) layout.Ref {
	if val.Tag != layout.TagRef || val.R == layout.Nil {
		v.trap(span, "this operation expects a String")
	}
	return val.R
}

// strLen is the length in bytes: payload word 0, one load.
func (v *VM) strLen(r layout.Ref) int64 { return int64(v.heap.Get(r, 0)) }

// strByte is byte i, from the word holding it. The caller has already bounds-checked it.
func (v *VM) strByte(r layout.Ref, i int64) byte {
	w := v.heap.Get(r, uint64(1+i/8))
	return byte(w >> uint((i%8)*8))
}

// strTail is up to four bytes starting at i, which is all a UTF-8 decode ever needs. It is
// a fixed-size read rather than a slice of the whole string, for the reason at the top of
// this file.
func (v *VM) strTail(r layout.Ref, i int64) string {
	n := v.strLen(r)
	var buf [4]byte
	k := 0
	for ; k < len(buf) && i+int64(k) < n; k++ {
		buf[k] = v.strByte(r, i+int64(k))
	}
	return string(buf[:k])
}

// byteIndex bounds-checks an index that has to name a byte that exists. `len` itself does
// not: a slice may end there, but nothing can be read there.
func (v *VM) byteIndex(r layout.Ref, val Value, span diag.Span) int64 {
	if int64(val.N) < 0 || int64(val.N) >= v.strLen(r) {
		v.trap(span, "index out of range")
	}
	return int64(val.N)
}

// mustBeBoundary enforces spec/14-strings.md's rule that no operation may produce a String
// which is not valid UTF-8. A continuation byte (10xxxxxx) is the only thing that is not a
// boundary; `len` itself always is, which is what makes the empty slice at the end legal.
func (v *VM) mustBeBoundary(r layout.Ref, i int64, span diag.Span) {
	if i == v.strLen(r) {
		return
	}
	if v.strByte(r, i)&0xC0 == 0x80 {
		v.trap(span, "string index is not a character boundary")
	}
}
