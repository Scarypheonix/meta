package backend

import (
	"github.com/scarypheonix/meta/internal/bytecode"
	"github.com/scarypheonix/meta/internal/layout"
	"github.com/scarypheonix/meta/internal/x86"
)

// The specified hash, in machine code (spec/13-collections.md's "Hashing").
//
// 64-bit FNV-1a over an encoding the specification fixes, because `hash::of` returns a
// number a program can print and the three engines have to agree on it down to the bit. The
// walk is `rt_equal_objects`' walk with one operand instead of two: the same per-TypeID
// table, the same shape dispatch, the same recursion into a reference.
//
// A composite folds each part's *own* hash in as eight little-endian bytes, so a value
// hashes the same wherever it sits. That is why this is two routines rather than one:
// rt_hash_word takes a raw word and its kind, and rt_hash_object takes a reference.

const (
	fnvOffsetBasis = 14695981039346656037
	fnvPrime       = 1099511628211
)

// emitHashMix writes `rt_hash_mix(h rdi, word rsi) -> rax`: fold eight little-endian bytes
// into a running hash. A leaf.
func (e *emitter) emitHashMix() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.hashMix)

	loop := a.NewLabel("hash_mix_loop")
	done := a.NewLabel("hash_mix_done")

	a.MovRR(x86.RAX, x86.RDI)
	a.XorRR(x86.R8, x86.R8) // byte index
	a.Bind(loop)
	a.CmpRI(x86.R8, 8)
	a.Jcc(x86.AboveEqual, done)

	// The low byte of the word shifted down by the index, xored in and multiplied.
	a.MovRR(x86.RCX, x86.R8)
	a.ShlI(x86.RCX, 3) // bit offset
	a.MovRR(x86.RDX, x86.RSI)
	a.ShrCL(x86.RDX)
	a.AndRI(x86.RDX, 0xFF)
	a.XorRR(x86.RAX, x86.RDX)
	a.MovRI(x86.R9, fnvPrime)
	a.ImulRR(x86.RAX, x86.R9)

	a.AddRI(x86.R8, 1)
	a.Jmp(loop)
	a.Bind(done)
	a.Ret()
}

// emitHashWord writes `rt_hash_word(word rdi, kind rsi) -> rax`: one value's own hash, given
// its raw word and its WordKind.
//
// A float normalizes -0.0 to 0.0 before hashing, because the two compare equal and a hash
// may not disagree with `==` (spec/13-collections.md). Unit hashes to the bare offset basis:
// nothing at all is fed in.
func (e *emitter) emitHashWord() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.hashWord)

	e.runtimePrologue()

	isRef := a.NewLabel("hash_word_ref")
	isFloat := a.NewLabel("hash_word_float")
	isUnit := a.NewLabel("hash_word_unit")
	mix := a.NewLabel("hash_word_mix")

	a.MovRR(x86.RBX, x86.RDI) // the word
	a.CmpRI(x86.RSI, int32(layout.WordRef))
	a.Jcc(x86.Equal, isRef)
	a.CmpRI(x86.RSI, int32(layout.WordFloat))
	a.Jcc(x86.Equal, isFloat)
	a.CmpRI(x86.RSI, int32(layout.WordUnit))
	a.Jcc(x86.Equal, isUnit)

	a.Bind(mix)
	a.MovRI(x86.RDI, fnvOffsetBasis)
	a.MovRR(x86.RSI, x86.RBX)
	a.Call(e.rt.hashMix)
	e.runtimeEpilogue()
	a.Ret()

	a.Bind(isRef)
	a.MovRR(x86.RDI, x86.RBX)
	a.Call(e.rt.hashObject)
	e.runtimeEpilogue()
	a.Ret()

	a.Bind(isFloat)
	// Zero and negative zero compare equal, so they hash equally. A float is one of the
	// two exactly when every bit but the sign is clear, which makes the normalization a
	// test on the bits rather than a comparison -- and keeps NaN, whose bits are its own
	// business, out of it.
	notZero := a.NewLabel("hash_word_not_zero")
	a.MovRR(x86.RAX, x86.RBX)
	a.MovRI(x86.R9, 0x7FFFFFFFFFFFFFFF)
	a.AndRR(x86.RAX, x86.R9)
	a.TestRR(x86.RAX, x86.RAX)
	a.Jcc(x86.NotEqual, notZero)
	a.XorRR(x86.RBX, x86.RBX)
	a.Bind(notZero)
	a.Jmp(mix)

	a.Bind(isUnit)
	a.MovRI(x86.RAX, fnvOffsetBasis)
	e.runtimeEpilogue()
	a.Ret()
}

// emitHashObject writes `rt_hash_object(ref rdi) -> rax`: one heap object's own hash.
//
// A String hashes over its bytes; an array over its elements' own hashes; anything else
// over its fields' own hashes, by the per-word kinds the type table holds. Nil hashes as a
// zero word, which is what the other two engines do with a reference that is not there.
func (e *emitter) emitHashObject() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.hashObject)

	e.runtimePrologue()
	a.SubRI(x86.RSP, 16) // one slot for the running hash, one to keep rsp aligned

	hashSlot := x86.At(x86.RBP, -40)

	isNil := a.NewLabel("hash_obj_nil")
	bytesCase := a.NewLabel("hash_obj_bytes")
	arrayCase := a.NewLabel("hash_obj_array")
	fixedCase := a.NewLabel("hash_obj_fixed")
	epilogue := a.NewLabel("hash_obj_epilogue")

	a.TestRR(x86.RDI, x86.RDI)
	a.Jcc(x86.Equal, isNil)

	a.MovRR(x86.RBX, x86.RDI) // the object
	a.MovRI(x86.RAX, fnvOffsetBasis)
	a.MovMR(hashSlot, x86.RAX)

	// The type table row, exactly as equal.go and collect.go read it.
	a.MovRM(x86.RAX, x86.At(x86.RBX, 0))
	e.maskLow32(x86.RAX, x86.R8)
	a.ShlI(x86.RAX, typeTableRowShift)
	a.MovRI(x86.R9, e.typeTableAddr)
	a.AddRR(x86.RAX, x86.R9)
	a.MovRR(x86.R12, x86.RAX) // r12 = the row

	a.MovRM(x86.RAX, x86.At(x86.R12, 0)) // shape
	a.CmpRI(x86.RAX, int32(layout.ByteArray))
	a.Jcc(x86.Equal, bytesCase)
	a.CmpRI(x86.RAX, int32(layout.RefArray))
	a.Jcc(x86.Equal, arrayCase)
	a.CmpRI(x86.RAX, int32(layout.RawArray))
	a.Jcc(x86.Equal, arrayCase)
	a.Jmp(fixedCase)

	// A struct, tuple, variant or closure: each field's own hash, in order.
	a.Bind(fixedCase)
	a.MovRM(x86.RAX, x86.At(x86.RBX, 0))
	a.ShrI(x86.RAX, 32)
	a.MovRR(x86.R13, x86.RAX)             // words
	a.MovRM(x86.R14, x86.At(x86.R12, 16)) // kindsAddr
	a.XorRR(x86.R12, x86.R12)             // i = 0

	fieldLoop := a.NewLabel("hash_obj_field_loop")
	a.Bind(fieldLoop)
	a.CmpRR(x86.R12, x86.R13)
	a.Jcc(x86.GreaterEqual, epilogue)

	a.MovRR(x86.RAX, x86.R14)
	a.AddRR(x86.RAX, x86.R12)
	a.XorRR(x86.RSI, x86.RSI)
	a.MovRM8(x86.RSI, x86.At(x86.RAX, 0)) // this word's kind
	a.MovRR(x86.RAX, x86.R12)
	a.ShlI(x86.RAX, 3)
	a.AddRI(x86.RAX, objHeaderSize)
	a.AddRR(x86.RAX, x86.RBX)
	a.MovRM(x86.RDI, x86.At(x86.RAX, 0))
	a.Call(e.rt.hashWord)

	a.MovRM(x86.RDI, hashSlot)
	a.MovRR(x86.RSI, x86.RAX)
	a.Call(e.rt.hashMix)
	a.MovMR(hashSlot, x86.RAX)

	a.AddRI(x86.R12, 1)
	a.Jmp(fieldLoop)

	// An array: its length's worth of elements, each by the row's single element kind.
	a.Bind(arrayCase)
	a.MovRM(x86.R13, x86.At(x86.RBX, objHeaderSize)) // the length
	a.MovRM(x86.R14, x86.At(x86.R12, 24))            // the element kind
	a.XorRR(x86.R12, x86.R12)

	elemLoop := a.NewLabel("hash_obj_elem_loop")
	a.Bind(elemLoop)
	a.CmpRR(x86.R12, x86.R13)
	a.Jcc(x86.GreaterEqual, epilogue)

	a.MovRR(x86.RAX, x86.R12)
	a.ShlI(x86.RAX, 3)
	a.AddRI(x86.RAX, objHeaderSize+wordSize)
	a.AddRR(x86.RAX, x86.RBX)
	a.MovRM(x86.RDI, x86.At(x86.RAX, 0))
	a.MovRR(x86.RSI, x86.R14)
	a.Call(e.rt.hashWord)

	a.MovRM(x86.RDI, hashSlot)
	a.MovRR(x86.RSI, x86.RAX)
	a.Call(e.rt.hashMix)
	a.MovMR(hashSlot, x86.RAX)

	a.AddRI(x86.R12, 1)
	a.Jmp(elemLoop)

	// A String: its bytes, one at a time, in order.
	a.Bind(bytesCase)
	a.MovRM(x86.R13, x86.At(x86.RBX, objHeaderSize)) // the length in bytes
	a.XorRR(x86.R12, x86.R12)

	byteLoop := a.NewLabel("hash_obj_byte_loop")
	a.Bind(byteLoop)
	a.CmpRR(x86.R12, x86.R13)
	a.Jcc(x86.GreaterEqual, epilogue)

	a.MovRR(x86.RAX, x86.RBX)
	a.AddRI(x86.RAX, strBytesOff)
	a.AddRR(x86.RAX, x86.R12)
	a.XorRR(x86.RCX, x86.RCX)
	a.MovRM8(x86.RCX, x86.At(x86.RAX, 0))
	a.MovRM(x86.RAX, hashSlot)
	a.XorRR(x86.RAX, x86.RCX)
	a.MovRI(x86.R9, fnvPrime)
	a.ImulRR(x86.RAX, x86.R9)
	a.MovMR(hashSlot, x86.RAX)

	a.AddRI(x86.R12, 1)
	a.Jmp(byteLoop)

	a.Bind(epilogue)
	a.MovRM(x86.RAX, hashSlot)
	a.AddRI(x86.RSP, 16)
	e.runtimeEpilogue()
	a.Ret()

	a.Bind(isNil)
	a.MovRI(x86.RDI, fnvOffsetBasis)
	a.XorRR(x86.RSI, x86.RSI)
	a.Call(e.rt.hashMix)
	a.AddRI(x86.RSP, 16)
	e.runtimeEpilogue()
	a.Ret()
}

// hashKindOf maps a value's static kind to the WordKind rt_hash_word dispatches on. The
// two vocabularies say the same thing about a word -- what it holds -- from either side of
// the bytecode boundary (ADR-0021).
func hashKindOf(k bytecode.Kind) layout.WordKind {
	switch k {
	case bytecode.KindRef, bytecode.KindString:
		return layout.WordRef
	case bytecode.KindFloat:
		return layout.WordFloat
	case bytecode.KindBool:
		return layout.WordBool
	case bytecode.KindChar:
		return layout.WordChar
	case bytecode.KindUnit:
		return layout.WordUnit
	default:
		return layout.WordInt
	}
}
