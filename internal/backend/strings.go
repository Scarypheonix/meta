package backend

import "github.com/scarypheonix/meta/internal/x86"

// The string operations, in machine code (spec/14-strings.md).
//
// A `String` is one object of internal/layout's ByteArray shape: a header, then the length
// in bytes, then the bytes themselves. That layout has been the same since Phase 2 -- it is
// what `io::println`, `==`, ordering and the specified hash already read -- so nothing here
// introduces a representation, only operations over the one that exists.
//
// Two of the six allocate (`slice` and `concat`) and therefore have frames the collector can
// walk; the other four are leaves. Every one of them returns a *status* rather than trapping
// itself, because a trap's text names a source location and a runtime routine has none of
// its own: the line a programmer wants is the one that called the prelude's method, and
// spans.go resolves it by walking the stack (the same arrangement §12 and §13 use).
//
// The status codes are the difference from the array operations, which need only one: a
// string index can fail in two distinguishable ways, and spec/14-strings.md gives them two
// different messages.

const (
	strOK = 0
	// strOutOfRange is an index outside the string, and strNotBoundary is one inside it
	// that would split a character. They are separate because §14 gives them separate
	// messages, and because they mean different things: one is arithmetic the caller got
	// wrong, the other is a byte index used where a character index was meant.
	strOutOfRange  = 1
	strNotBoundary = 2
)

// strLenOff is the payload word holding the length in bytes. The bytes begin at
// strBytesOff, one word past it.
const strLenOff = objHeaderSize

// emitStrLen writes `rt_str_len(s rdi) -> rax`. A leaf.
func (e *emitter) emitStrLen() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.strLen)
	a.MovRM(x86.RAX, x86.At(x86.RDI, strLenOff))
	a.Ret()
}

// strByteAddr leaves the address of byte rsi of the string in rdi in dst. It assumes the
// index has already been checked.
func (e *emitter) strByteAddr(dst x86.Reg) {
	a := e.a
	a.MovRR(dst, x86.RDI)
	a.AddRI(dst, strBytesOff)
	a.AddRR(dst, x86.RSI)
}

// strReadIndexCheck jumps to refused unless rsi names a byte that exists in the string in
// rdi. The comparison is unsigned, so a negative index is a very large one and fails the
// same test. `len` itself is not a byte that exists: a slice may end there, nothing can be
// read there.
func (e *emitter) strReadIndexCheck(refused x86.Label) {
	a := e.a
	a.MovRM(x86.RCX, x86.At(x86.RDI, strLenOff))
	a.CmpRR(x86.RSI, x86.RCX)
	a.Jcc(x86.AboveEqual, refused)
}

// strBoundaryCheck jumps to refused when byte rsi of the string in rdi is a UTF-8
// continuation byte (10xxxxxx), which is the only thing that is not a character boundary.
// An index equal to the length is a boundary and is left alone, which is what makes the
// empty slice at the end legal.
func (e *emitter) strBoundaryCheck(refused x86.Label) {
	a := e.a
	atEnd := a.NewLabel("str_boundary_at_end")
	a.MovRM(x86.RCX, x86.At(x86.RDI, strLenOff))
	a.CmpRR(x86.RSI, x86.RCX)
	a.Jcc(x86.Equal, atEnd)
	e.strByteAddr(x86.RCX)
	a.XorRR(x86.RDX, x86.RDX)
	a.MovRM8(x86.RDX, x86.At(x86.RCX, 0))
	a.AndRI(x86.RDX, 0xC0)
	a.CmpRI(x86.RDX, 0x80)
	a.Jcc(x86.Equal, refused)
	a.Bind(atEnd)
}

// emitStrByteAt writes `rt_str_byte_at(s rdi, i rsi) -> status rax, byte rdx`. A leaf.
func (e *emitter) emitStrByteAt() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.strByteAt)

	refused := a.NewLabel("str_byte_at_refused")
	e.strReadIndexCheck(refused)
	e.strByteAddr(x86.RCX)
	a.XorRR(x86.RDX, x86.RDX)
	a.MovRM8(x86.RDX, x86.At(x86.RCX, 0))
	a.MovRI(x86.RAX, strOK)
	a.Ret()

	a.Bind(refused)
	a.MovRI(x86.RAX, strOutOfRange)
	a.XorRR(x86.RDX, x86.RDX)
	a.Ret()
}

// strDecodeWidth leaves in dst how many bytes the character at byte rsi of the string in
// rdi occupies, from the lead byte alone. The index must already be a checked boundary.
//
// The widths are spec/14-strings.md's table: 0xxxxxxx is 1, 110xxxxx is 2, 1110xxxx is 3,
// 11110xxx is 4. A continuation byte cannot reach here, because a boundary check rejected
// it first.
//
// dst may not be rcx or r8, which are the scratch this uses.
func (e *emitter) strDecodeWidth(dst x86.Reg) {
	a := e.a
	done := a.NewLabel("str_width_done")
	two := a.NewLabel("str_width_two")
	three := a.NewLabel("str_width_three")

	e.strByteAddr(x86.RCX)
	a.XorRR(x86.R8, x86.R8)
	a.MovRM8(x86.R8, x86.At(x86.RCX, 0))

	a.MovRI(dst, 1)
	a.MovRR(x86.RCX, x86.R8)
	a.AndRI(x86.RCX, 0x80)
	a.CmpRI(x86.RCX, 0)
	a.Jcc(x86.Equal, done)

	a.MovRR(x86.RCX, x86.R8)
	a.AndRI(x86.RCX, 0xE0)
	a.CmpRI(x86.RCX, 0xC0)
	a.Jcc(x86.Equal, two)

	a.MovRR(x86.RCX, x86.R8)
	a.AndRI(x86.RCX, 0xF0)
	a.CmpRI(x86.RCX, 0xE0)
	a.Jcc(x86.Equal, three)

	a.MovRI(dst, 4)
	a.Jmp(done)
	a.Bind(three)
	a.MovRI(dst, 3)
	a.Jmp(done)
	a.Bind(two)
	a.MovRI(dst, 2)
	a.Bind(done)
}

// timesSix multiplies dst by six in place, which is how many bits one UTF-8 continuation
// byte carries. It clobbers r8.
func (e *emitter) timesSix(dst x86.Reg) {
	e.a.MovRI(x86.R8, 6)
	e.a.ImulRR(dst, x86.R8)
}

// emitStrCharWidth writes `rt_str_char_width(s rdi, i rsi) -> status rax, width rdx`. A leaf.
func (e *emitter) emitStrCharWidth() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.strCharWidth)

	outOfRange := a.NewLabel("str_char_width_range")
	notBoundary := a.NewLabel("str_char_width_boundary")

	e.strReadIndexCheck(outOfRange)
	e.strBoundaryCheck(notBoundary)
	e.strDecodeWidth(x86.RDX)
	a.MovRI(x86.RAX, strOK)
	a.Ret()

	a.Bind(outOfRange)
	a.MovRI(x86.RAX, strOutOfRange)
	a.XorRR(x86.RDX, x86.RDX)
	a.Ret()

	a.Bind(notBoundary)
	a.MovRI(x86.RAX, strNotBoundary)
	a.XorRR(x86.RDX, x86.RDX)
	a.Ret()
}

// emitStrCharAt writes `rt_str_char_at(s rdi, i rsi) -> status rax, scalar rdx`: the
// Unicode scalar value whose encoding begins at byte i. A leaf.
//
// The decode is spec/14-strings.md's table read the other way: the lead byte contributes
// its low bits, and each continuation byte six more.
func (e *emitter) emitStrCharAt() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.strCharAt)

	outOfRange := a.NewLabel("str_char_at_range")
	notBoundary := a.NewLabel("str_char_at_boundary")
	loop := a.NewLabel("str_char_at_loop")
	done := a.NewLabel("str_char_at_done")

	e.strReadIndexCheck(outOfRange)
	e.strBoundaryCheck(notBoundary)
	e.strDecodeWidth(x86.R9) // r9 = the width

	// The lead byte's payload: all 7 bits for a 1-byte character, and 7 - width bits
	// otherwise -- 5 for two bytes, 4 for three, 3 for four. Masking with
	// 0x7F >> (width - 1) produces exactly that in every case, and leaves a 1-byte
	// character's own value untouched.
	e.strByteAddr(x86.RCX)
	a.XorRR(x86.RDX, x86.RDX)
	a.MovRM8(x86.RDX, x86.At(x86.RCX, 0))
	a.MovRI(x86.R8, 0x7F)
	a.MovRR(x86.RAX, x86.R9)
	a.SubRI(x86.RAX, 1)
	e.shiftRightBy(x86.R8, x86.RAX)
	a.AndRR(x86.RDX, x86.R8)

	// Each continuation byte contributes its low six bits, most significant first.
	a.MovRI(x86.R10, 1) // r10 = which byte of the character
	a.Bind(loop)
	a.CmpRR(x86.R10, x86.R9)
	a.Jcc(x86.AboveEqual, done)
	a.MovRR(x86.RCX, x86.RDI)
	a.AddRI(x86.RCX, strBytesOff)
	a.AddRR(x86.RCX, x86.RSI)
	a.AddRR(x86.RCX, x86.R10)
	a.XorRR(x86.RAX, x86.RAX)
	a.MovRM8(x86.RAX, x86.At(x86.RCX, 0))
	a.AndRI(x86.RAX, 0x3F)
	a.ShlI(x86.RDX, 6)
	a.OrRR(x86.RDX, x86.RAX)
	a.AddRI(x86.R10, 1)
	a.Jmp(loop)

	a.Bind(done)
	a.MovRI(x86.RAX, strOK)
	a.Ret()

	a.Bind(outOfRange)
	a.MovRI(x86.RAX, strOutOfRange)
	a.XorRR(x86.RDX, x86.RDX)
	a.Ret()

	a.Bind(notBoundary)
	a.MovRI(x86.RAX, strNotBoundary)
	a.XorRR(x86.RDX, x86.RDX)
	a.Ret()
}

// shiftRightBy shifts dst right by the count in amount, which may be any register: the
// instruction itself only takes cl, so the count moves there first.
func (e *emitter) shiftRightBy(dst x86.Reg, amount x86.Reg) {
	a := e.a
	a.MovRR(x86.RCX, amount)
	a.ShrCL(dst)
}

// emitStrAlloc writes `rt_str_alloc(len rdi) -> rax`: a String object with room for len
// bytes and its length word already set. The bytes are whatever the allocator left there,
// so every caller writes all of them.
//
// It allocates, so it has a frame the collector can walk. It takes no reference argument,
// which is what makes it safe to call with a string operand parked anywhere: there is
// nothing of the caller's for a collection to move out from under it.
func (e *emitter) emitStrAlloc() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.strAlloc)

	e.runtimePrologue()
	a.MovRR(x86.RBX, x86.RDI) // the length in bytes, across the allocation

	// words = 1 length word + ceil(len / 8) byte words.
	a.AddRI(x86.RDI, 7)
	a.ShrI(x86.RDI, 3)
	a.AddRI(x86.RDI, 1)
	a.MovRI(x86.RSI, uint64(e.stringType))
	a.Call(e.rt.alloc)
	a.MovMR(x86.At(x86.RAX, strLenOff), x86.RBX)

	e.runtimeEpilogue()
	a.Ret()
}

// strCopyBytes copies rcx bytes from the address in rsi to the address in rdi, one at a
// time. There is no alignment to exploit: either end can start mid-word.
func (e *emitter) strCopyBytes() {
	a := e.a
	loop := a.NewLabel("str_copy_loop")
	done := a.NewLabel("str_copy_done")
	a.Bind(loop)
	a.TestRR(x86.RCX, x86.RCX)
	a.Jcc(x86.Equal, done)
	a.XorRR(x86.RDX, x86.RDX)
	a.MovRM8(x86.RDX, x86.At(x86.RSI, 0))
	a.MovMR8(x86.At(x86.RDI, 0), x86.RDX)
	a.AddRI(x86.RSI, 1)
	a.AddRI(x86.RDI, 1)
	a.SubRI(x86.RCX, 1)
	a.Jmp(loop)
	a.Bind(done)
}

// emitStrSliceCheck writes `rt_str_slice_check(s rdi, start rsi, end rdx) -> status rax,
// length rdx`: everything spec/14-strings.md says a slice's two indices must satisfy, and
// how many bytes the result will hold. A leaf.
//
// It is separate from the copy because of where the allocation between them has to happen.
// A runtime routine that held the source string across its own allocation would be holding
// it somewhere the collector does not look: the root walk substitutes a synthetic
// all-saved, no-roots-of-its-own entry for any frame with no stack map (collect.go's
// emitGCRuntimeFrameEntry), so a reference parked in one of its callee-saved registers is
// never updated when a collection moves the object. Splitting the work leaves the
// allocation at an ordinary call site in user code, where `recordCall` writes a real entry
// and the register allocator has already given every operand of that call a home the
// collector walks -- which is exactly what `construct` relies on for a struct's fields.
func (e *emitter) emitStrSliceCheck() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.strSliceCheck)

	outOfRange := a.NewLabel("str_slice_range")
	notBoundary := a.NewLabel("str_slice_boundary")

	// start <= end <= len, with start >= 0. Signed comparisons, because a negative index
	// must fail rather than wrap into a very large one -- `end` is compared against the
	// length, which an unsigned test would let a negative `start` slip past.
	a.CmpRI(x86.RSI, 0)
	a.Jcc(x86.Less, outOfRange)
	a.CmpRR(x86.RSI, x86.RDX)
	a.Jcc(x86.Greater, outOfRange)
	a.MovRM(x86.R9, x86.At(x86.RDI, strLenOff))
	a.CmpRR(x86.RDX, x86.R9)
	a.Jcc(x86.Greater, outOfRange)

	// Both endpoints must fall between characters, not inside one. strBoundaryCheck reads
	// the index from rsi, so `end` is moved there and `start` put back afterwards.
	a.MovRR(x86.R9, x86.RDX) // end
	e.strBoundaryCheck(notBoundary)
	a.MovRR(x86.R10, x86.RSI) // start
	a.MovRR(x86.RSI, x86.R9)
	e.strBoundaryCheck(notBoundary)

	a.MovRR(x86.RDX, x86.R9)
	a.SubRR(x86.RDX, x86.R10) // end - start
	a.MovRI(x86.RAX, strOK)
	a.Ret()

	a.Bind(outOfRange)
	a.MovRI(x86.RAX, strOutOfRange)
	a.XorRR(x86.RDX, x86.RDX)
	a.Ret()

	a.Bind(notBoundary)
	a.MovRI(x86.RAX, strNotBoundary)
	a.XorRR(x86.RDX, x86.RDX)
	a.Ret()
}

// emitStrSliceInto writes `rt_str_slice_into(dst rdi, s rsi, start rdx)`: copy the bytes
// into a String the caller has already allocated, taking the count from the destination's
// own length word. A leaf -- it allocates nothing, so neither operand can move under it.
func (e *emitter) emitStrSliceInto() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.strSliceInto)

	a.MovRM(x86.RCX, x86.At(x86.RDI, strLenOff))
	a.AddRI(x86.RDI, strBytesOff)
	a.AddRI(x86.RSI, strBytesOff)
	a.AddRR(x86.RSI, x86.RDX)
	e.strCopyBytes()
	a.Ret()
}

// emitStrConcatInto writes `rt_str_concat_into(dst rdi, a rsi, b rdx)`: both operands'
// bytes, in order, into a String the caller has already allocated. A leaf, for the reason
// emitStrSliceCheck explains.
func (e *emitter) emitStrConcatInto() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.strConcatInto)

	a.MovRM(x86.R8, x86.At(x86.RSI, strLenOff)) // len(a)
	a.MovRM(x86.R9, x86.At(x86.RDX, strLenOff)) // len(b)

	a.MovRR(x86.R10, x86.RDI) // the destination's base, kept for the second copy
	a.MovRR(x86.R11, x86.RDX) // b

	a.AddRI(x86.RDI, strBytesOff)
	a.AddRI(x86.RSI, strBytesOff)
	a.MovRR(x86.RCX, x86.R8)
	e.strCopyBytes()

	a.MovRR(x86.RDI, x86.R10)
	a.AddRI(x86.RDI, strBytesOff)
	a.AddRR(x86.RDI, x86.R8)
	a.MovRR(x86.RSI, x86.R11)
	a.AddRI(x86.RSI, strBytesOff)
	a.MovRR(x86.RCX, x86.R9)
	e.strCopyBytes()
	a.Ret()
}

// emitCharToStr writes `rt_char_to_str(scalar rdi) -> rax`: the UTF-8 encoding of one
// Unicode scalar value, as a fresh String.
//
// It is the inverse of `rt_str_char_at`, and it is here rather than in runtime.go because
// it is the same table read the same way: the width comes from the value's magnitude, the
// lead byte carries the high bits under its own prefix, and every continuation byte carries
// six more under 10xxxxxx.
//
// A `char` is a Unicode scalar value by construction (§03 excludes surrogates, and the only
// ways to make one are a literal and `char_at`), so there is no invalid input to reject.
func (e *emitter) emitCharToStr() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.charToStr)

	e.runtimePrologue()

	two := a.NewLabel("char_to_str_two")
	three := a.NewLabel("char_to_str_three")
	four := a.NewLabel("char_to_str_four")
	alloc := a.NewLabel("char_to_str_alloc")
	tail := a.NewLabel("char_to_str_tail")
	tailDone := a.NewLabel("char_to_str_tail_done")

	a.MovRR(x86.RBX, x86.RDI) // the scalar
	a.MovRI(x86.R12, 1)       // the width
	a.MovRI(x86.R13, 0)       // the lead byte's prefix
	a.MovRI(x86.R14, 0x7F)    // the lead byte's payload mask

	a.CmpRI(x86.RBX, 0x80)
	a.Jcc(x86.Less, alloc)
	a.CmpRI(x86.RBX, 0x800)
	a.Jcc(x86.Less, two)
	a.CmpRI(x86.RBX, 0x10000)
	a.Jcc(x86.Less, three)

	a.Bind(four)
	a.MovRI(x86.R12, 4)
	a.MovRI(x86.R13, 0xF0)
	a.MovRI(x86.R14, 0x07)
	a.Jmp(alloc)

	a.Bind(three)
	a.MovRI(x86.R12, 3)
	a.MovRI(x86.R13, 0xE0)
	a.MovRI(x86.R14, 0x0F)
	a.Jmp(alloc)

	a.Bind(two)
	a.MovRI(x86.R12, 2)
	a.MovRI(x86.R13, 0xC0)
	a.MovRI(x86.R14, 0x1F)

	a.Bind(alloc)
	a.MovRR(x86.RDI, x86.R12)
	a.Call(e.rt.strAlloc)

	// The lead byte: the scalar's high bits, shifted down past the continuation bytes,
	// masked to what the prefix leaves room for, then the prefix itself.
	a.MovRR(x86.RDX, x86.RBX)
	a.MovRR(x86.RCX, x86.R12)
	a.SubRI(x86.RCX, 1)
	e.timesSix(x86.RCX) // the bits the continuation bytes will carry
	a.ShrCL(x86.RDX)
	a.AndRR(x86.RDX, x86.R14)
	a.OrRR(x86.RDX, x86.R13)
	a.MovRR(x86.RDI, x86.RAX)
	a.AddRI(x86.RDI, strBytesOff)
	a.MovMR8(x86.At(x86.RDI, 0), x86.RDX)

	// Then one continuation byte per remaining position, most significant first.
	a.MovRI(x86.R9, 1)
	a.Bind(tail)
	a.CmpRR(x86.R9, x86.R12)
	a.Jcc(x86.AboveEqual, tailDone)
	a.MovRR(x86.RCX, x86.R12)
	a.SubRR(x86.RCX, x86.R9)
	a.SubRI(x86.RCX, 1)
	e.timesSix(x86.RCX)
	a.MovRR(x86.RDX, x86.RBX)
	a.ShrCL(x86.RDX)
	a.AndRI(x86.RDX, 0x3F)
	a.OrRI(x86.RDX, 0x80)
	a.MovRR(x86.RDI, x86.RAX)
	a.AddRI(x86.RDI, strBytesOff)
	a.AddRR(x86.RDI, x86.R9)
	a.MovMR8(x86.At(x86.RDI, 0), x86.RDX)
	a.AddRI(x86.R9, 1)
	a.Jmp(tail)
	a.Bind(tailDone)

	e.runtimeEpilogue()
	a.Ret()
}
