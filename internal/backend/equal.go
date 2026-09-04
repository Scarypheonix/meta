package backend

import (
	"github.com/scarypheonix/meta/internal/layout"
	"github.com/scarypheonix/meta/internal/x86"
)

// Structural `==` on an aggregate or a String (ADR-0019's exact layouts made this
// possible: every object's shape is now a small, uniform per-word Kind array, whether
// the comparison is emitted here or read by internal/vm's equalObjects).
//
// bytecode.Kind widens OpEq/OpNe enough to say "this is a reference" (KindRef) but not
// which concrete type -- unlike OpToStr and the comparison operators, it carries no
// further identity, and giving it one would mean threading a TypeID through the IR the
// way spec/11-codegen.md's own DEFERRED note already says the bytecode needs widening
// for (stack maps). Rather than duplicate that work here, structural equality resolves
// the concrete shape at run time, from the operands' own header, against a table this
// file emits once per program: one shared recursive routine instead of one generated
// comparison per type, and the "which type" question answered exactly where
// internal/gc's own scanning already answers it -- the object's header.

// typeTableEntryWords is the table's per-TypeID record size, in words:
// [shape, objKind, kindsAddr, elem]. A power-of-two size keeps the row address a shift
// rather than a multiply. The fourth word is an array's single element kind (ADR-0028),
// which is what a run of elements has instead of one kind per word; it is zero for every
// other shape, and nothing reads it for one.
const typeTableEntryWords = 4

// typeTableRowShift is log2(typeTableEntryWords*8): a row is 32 bytes, so a TypeID's
// row address is the table base plus the id shifted left by 5.
const typeTableRowShift = 5

// emitTypeTable lays out a per-TypeID row equal_objects reads: the object's Shape and
// ObjKind (from the static Descriptor -- constant for a given TypeID), and the address
// of its Kinds byte array for a Fixed shape (nil otherwise). A Fixed object's actual
// field count and a ByteArray's actual byte length both live in the object's own
// header, not this table, since only the header varies per instance.
func (e *emitter) emitTypeTable() {
	n := e.prog.Types.Len()
	kindsAddr := make([]uint64, n)
	for id := 1; id < n; id++ {
		d := e.prog.Types.Get(layout.TypeID(id))
		if d.Shape != layout.Fixed || len(d.Kinds) == 0 {
			continue
		}
		addr := e.roDataAddr + uint64(len(e.roData))
		for _, k := range d.Kinds {
			e.roData = append(e.roData, byte(k))
		}
		kindsAddr[id] = addr
	}
	for len(e.roData)%wordSize != 0 {
		e.roData = append(e.roData, 0)
	}
	e.typeTableAddr = e.roDataAddr + uint64(len(e.roData))

	// TypeID 0 is reserved (layout.Registry.Get panics on it) and never looked up, but
	// the table is indexed by id * entry size from one shared base, so id 0 still
	// needs a placeholder row to keep every real id's offset correct.
	for i := 0; i < typeTableEntryWords; i++ {
		e.roData = appendU64(e.roData, 0)
	}
	for id := 1; id < n; id++ {
		d := e.prog.Types.Get(layout.TypeID(id))
		e.roData = appendU64(e.roData, uint64(d.Shape))
		e.roData = appendU64(e.roData, uint64(d.Kind))
		e.roData = appendU64(e.roData, kindsAddr[id])
		e.roData = appendU64(e.roData, uint64(d.Elem))
	}
}

// maskLow32 clears the high 32 bits of r in place. AND with a 32-bit immediate
// sign-extends over a 64-bit operand (x86.Asm.AndRI), which can express "clear the low
// bits" but not "clear the high half, keep the low one" -- exactly what reading a
// layout.Header's low-32-bit TypeID out of a 64-bit word needs -- so the mask is built
// in a register instead.
func (e *emitter) maskLow32(r, scratch x86.Reg) {
	e.a.MovRI(scratch, 0xFFFFFFFF)
	e.a.AndRR(r, scratch)
}

// emitEqualObjects writes the structural-equality runtime routine: rdi = a, rsi = b,
// result in rax (0 or 1). It recurses on a WordRef field, the same way
// internal/vm.equalObjects does, and traps on comparing two closures -- unreachable
// today since native code does not lower OpClosure yet, but a definitive answer rather
// than a silently wrong one the day it does.
//
// Registers: rbx = a, r13 = b, r12 = the loop index, kept across the recursive call
// (they are callee-saved, so the call's own prologue/epilogue preserve them for free).
// [rbp-40] and [rbp-48] hold the current type's kindsAddr and word count -- read once
// per call, not per iteration, and cheap to keep on the stack rather than spend a
// fourth callee-saved register on.
func (e *emitter) emitEqualObjects() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.equalObjects)

	a.Push(x86.RBP)
	a.MovRR(x86.RBP, x86.RSP)
	a.Push(x86.RBX)
	a.Push(x86.R12)
	a.Push(x86.R13)
	a.Push(x86.R14)
	a.SubRI(x86.RSP, 32)

	kindsSlot := x86.At(x86.RBP, -40)
	wordsSlot := x86.At(x86.RBP, -48)

	epilogue := a.NewLabel("eq_epilogue")
	isTrue := a.NewLabel("eq_true")
	isFalse := a.NewLabel("eq_false")

	a.MovRR(x86.RBX, x86.RDI)
	a.MovRR(x86.R13, x86.RSI)

	a.CmpRR(x86.RBX, x86.R13)
	a.Jcc(x86.Equal, isTrue)

	a.MovRM(x86.RAX, x86.At(x86.RBX, 0)) // header a
	a.MovRM(x86.RCX, x86.At(x86.R13, 0)) // header b
	e.maskLow32(x86.RAX, x86.R8)
	e.maskLow32(x86.RCX, x86.R8)
	a.CmpRR(x86.RAX, x86.RCX)
	a.Jcc(x86.NotEqual, isFalse)
	// rax = the shared TypeID.

	a.MovRR(x86.RCX, x86.RAX)
	a.ShlI(x86.RCX, typeTableRowShift) // * the row size in bytes (typeTableEntryWords*8)
	a.MovRI(x86.R9, e.typeTableAddr)
	a.AddRR(x86.RCX, x86.R9) // rcx = this TypeID's table row

	a.MovRM(x86.RAX, x86.At(x86.RCX, 0)) // shape
	bytesCase := a.NewLabel("eq_bytes")
	arrayCase := a.NewLabel("eq_array")
	a.CmpRI(x86.RAX, int32(layout.ByteArray))
	a.Jcc(x86.Equal, bytesCase)
	a.CmpRI(x86.RAX, int32(layout.RefArray))
	a.Jcc(x86.Equal, arrayCase)
	a.CmpRI(x86.RAX, int32(layout.RawArray))
	a.Jcc(x86.Equal, arrayCase)

	a.MovRM(x86.RAX, x86.At(x86.RCX, 8)) // objKind
	trapFn := a.NewLabel("eq_trap_fn")
	a.CmpRI(x86.RAX, int32(layout.ObjClosure))
	a.Jcc(x86.Equal, trapFn)

	// Fixed shape: recurse over each word by its static Kind.
	a.MovRM(x86.RAX, x86.At(x86.RBX, 0)) // header a again (rax was clobbered)
	a.ShrI(x86.RAX, 32)                  // words: the header's forward bit and unused
	a.MovMR(wordsSlot, x86.RAX)          // bits above it are 0 for any live object.
	a.MovRM(x86.RAX, x86.At(x86.RCX, 16))
	a.MovMR(kindsSlot, x86.RAX) // kindsAddr

	a.XorRR(x86.R12, x86.R12) // i = 0
	fixedLoop := a.NewLabel("eq_fixed_loop")
	a.Bind(fixedLoop)
	a.MovRM(x86.RAX, wordsSlot)
	a.CmpRR(x86.R12, x86.RAX)
	a.Jcc(x86.GreaterEqual, isTrue)

	a.MovRM(x86.RAX, kindsSlot)
	a.AddRR(x86.RAX, x86.R12)
	a.XorRR(x86.R14, x86.R14)
	a.MovRM8(x86.R14, x86.At(x86.RAX, 0)) // this word's WordKind

	a.MovRR(x86.RAX, x86.R12)
	a.ShlI(x86.RAX, 3)
	a.AddRI(x86.RAX, objHeaderSize)
	a.MovRR(x86.RCX, x86.RAX) // rcx = the field's byte offset, kept for both operands
	a.AddRR(x86.RAX, x86.RBX)
	a.MovRM(x86.R8, x86.At(x86.RAX, 0)) // field word from a
	a.MovRR(x86.RAX, x86.RCX)
	a.AddRR(x86.RAX, x86.R13)
	a.MovRM(x86.R9, x86.At(x86.RAX, 0)) // field word from b

	e.eqWordPair(isFalse)
	a.AddRI(x86.R12, 1)
	a.Jmp(fixedLoop)

	// ByteArray shape: a String. Its length is payload word 0; equal length and equal
	// bytes is equal content.
	//
	// The whole words are compared a word at a time and the trailing bytes one at a time,
	// which is not an optimization but the only correct reading. The bytes above a
	// partial final word's last meaningful one are NOT part of the value and are NOT
	// zero: `rt_str_alloc` writes only the bytes it was given, and after the first
	// collection the space it bump-allocates from is a semispace that has been used
	// before, so those bytes hold whatever the previous cycle left there. Comparing them
	// made two strings of the same text unequal -- but only in native code, only after a
	// collection, and only for a length that is not a multiple of eight.
	a.Bind(bytesCase)
	a.MovRM(x86.RAX, x86.At(x86.RBX, objHeaderSize)) // len a
	a.MovRM(x86.RCX, x86.At(x86.R13, objHeaderSize)) // len b
	a.CmpRR(x86.RAX, x86.RCX)
	a.Jcc(x86.NotEqual, isFalse)
	a.MovMR(wordsSlot, x86.RAX) // the length in bytes, for the tail
	a.ShrI(x86.RAX, 3)          // whole words = len / 8
	a.MovMR(kindsSlot, x86.RAX) // reused here as the whole-word count

	a.XorRR(x86.R12, x86.R12)
	bytesLoop := a.NewLabel("eq_bytes_loop")
	bytesTail := a.NewLabel("eq_bytes_tail")
	a.Bind(bytesLoop)
	a.MovRM(x86.RAX, kindsSlot)
	a.CmpRR(x86.R12, x86.RAX)
	a.Jcc(x86.GreaterEqual, bytesTail)

	a.MovRR(x86.RCX, x86.R12)
	a.ShlI(x86.RCX, 3)
	a.AddRI(x86.RCX, strBytesOff)
	a.MovRR(x86.RAX, x86.RCX)
	a.AddRR(x86.RAX, x86.RBX)
	a.MovRM(x86.R8, x86.At(x86.RAX, 0))
	a.MovRR(x86.RAX, x86.RCX)
	a.AddRR(x86.RAX, x86.R13)
	a.MovRM(x86.R9, x86.At(x86.RAX, 0))
	a.CmpRR(x86.R8, x86.R9)
	a.Jcc(x86.NotEqual, isFalse)
	a.AddRI(x86.R12, 1)
	a.Jmp(bytesLoop)

	// The bytes past the last whole word: at most seven, one at a time.
	a.Bind(bytesTail)
	a.MovRR(x86.R12, x86.RAX)
	a.ShlI(x86.R12, 3) // i = whole words * 8, the first byte of the tail
	tailLoop := a.NewLabel("eq_bytes_tail_loop")
	a.Bind(tailLoop)
	a.MovRM(x86.RAX, wordsSlot)
	a.CmpRR(x86.R12, x86.RAX)
	a.Jcc(x86.GreaterEqual, isTrue)

	a.MovRR(x86.RCX, x86.R12)
	a.AddRI(x86.RCX, strBytesOff)
	a.MovRR(x86.RAX, x86.RCX)
	a.AddRR(x86.RAX, x86.RBX)
	a.XorRR(x86.R8, x86.R8)
	a.MovRM8(x86.R8, x86.At(x86.RAX, 0))
	a.MovRR(x86.RAX, x86.RCX)
	a.AddRR(x86.RAX, x86.R13)
	a.XorRR(x86.R9, x86.R9)
	a.MovRM8(x86.R9, x86.At(x86.RAX, 0))
	a.CmpRR(x86.R8, x86.R9)
	a.Jcc(x86.NotEqual, isFalse)
	a.AddRI(x86.R12, 1)
	a.Jmp(tailLoop)

	// An array (ADR-0028): equal lengths and pairwise-equal elements, with the room above
	// the length no part of the value. One kind for the whole run, from the table's fourth
	// word, rather than one per word.
	a.Bind(arrayCase)
	a.MovRM(x86.RAX, x86.At(x86.RCX, 24))
	a.MovMR(kindsSlot, x86.RAX) // the element kind, for every element
	a.MovRM(x86.RAX, x86.At(x86.RBX, objHeaderSize))
	a.MovRM(x86.RDX, x86.At(x86.R13, objHeaderSize))
	a.CmpRR(x86.RAX, x86.RDX)
	a.Jcc(x86.NotEqual, isFalse)
	a.MovMR(wordsSlot, x86.RAX)

	a.XorRR(x86.R12, x86.R12)
	arrayLoop := a.NewLabel("eq_array_loop")
	a.Bind(arrayLoop)
	a.MovRM(x86.RAX, wordsSlot)
	a.CmpRR(x86.R12, x86.RAX)
	a.Jcc(x86.GreaterEqual, isTrue)

	a.MovRR(x86.RCX, x86.R12)
	a.ShlI(x86.RCX, 3)
	a.AddRI(x86.RCX, objHeaderSize+wordSize) // past the header and the length word
	a.MovRR(x86.RAX, x86.RCX)
	a.AddRR(x86.RAX, x86.RBX)
	a.MovRM(x86.R8, x86.At(x86.RAX, 0))
	a.MovRR(x86.RAX, x86.RCX)
	a.AddRR(x86.RAX, x86.R13)
	a.MovRM(x86.R9, x86.At(x86.RAX, 0))
	a.MovRM(x86.R14, kindsSlot)

	e.eqWordPair(isFalse)
	a.AddRI(x86.R12, 1)
	a.Jmp(arrayLoop)

	a.Bind(trapFn)
	// No native OpClosure lowering exists yet (internal/backend/lower.go), so no
	// closure object can reach here; the message still matches the other two
	// engines' exactly, for the day one can.
	e.trapWith(e.rawString("origin: function values cannot be compared with `==` at <runtime>\n"))

	a.Bind(isTrue)
	a.MovRI(x86.RAX, 1)
	a.Jmp(epilogue)
	a.Bind(isFalse)
	a.XorRR(x86.RAX, x86.RAX)

	a.Bind(epilogue)
	a.AddRI(x86.RSP, 32)
	a.Pop(x86.R14)
	a.Pop(x86.R13)
	a.Pop(x86.R12)
	a.Pop(x86.RBX)
	a.Pop(x86.RBP)
	a.Ret()
}

// emitCompareBytes writes the lexicographic byte comparison String's `cmp` needs: rdi
// = a, rsi = b, result in rax as a three-way sign (-1, 0, 1), the same convention
// strings.Compare uses -- internal/vm's ordering() gets there through two separate
// bool comparisons instead, but a sign is exactly as informative and cheaper to turn
// into one of Less/Equal/Greater at the call site (lower.go's buildOrdering).
//
// A leaf routine: it calls nothing, so unlike equal_objects it needs no 16-byte-aligned
// sub-frame, just the four callee-saved registers it uses.
func (e *emitter) emitCompareBytes() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.compareBytes)

	a.Push(x86.RBX)
	a.Push(x86.R12)
	a.Push(x86.R13)
	a.Push(x86.R14)

	a.MovRR(x86.RBX, x86.RDI) // a
	a.MovRR(x86.R13, x86.RSI) // b

	a.MovRM(x86.RAX, x86.At(x86.RBX, objHeaderSize)) // len a
	a.MovRM(x86.RCX, x86.At(x86.R13, objHeaderSize)) // len b
	a.MovRR(x86.R14, x86.RAX)
	a.CmpRR(x86.RCX, x86.R14)
	a.Cmov(x86.Less, x86.R14, x86.RCX) // r14 = min(len a, len b)

	neg1 := a.NewLabel("cb_neg1")
	pos1 := a.NewLabel("cb_pos1")
	done := a.NewLabel("cb_done")

	a.XorRR(x86.R12, x86.R12) // i = 0
	loop := a.NewLabel("cb_loop")
	a.Bind(loop)
	a.CmpRR(x86.R12, x86.R14)
	afterCommon := a.NewLabel("cb_after_common")
	a.Jcc(x86.GreaterEqual, afterCommon)

	a.MovRR(x86.R8, x86.R12)
	a.AddRI(x86.R8, strBytesOff)
	a.MovRR(x86.R9, x86.R8)
	a.AddRR(x86.R9, x86.RBX)
	a.XorRR(x86.RAX, x86.RAX)
	a.MovRM8(x86.RAX, x86.At(x86.R9, 0)) // byte a
	a.MovRR(x86.R9, x86.R8)
	a.AddRR(x86.R9, x86.R13)
	a.XorRR(x86.RCX, x86.RCX)
	a.MovRM8(x86.RCX, x86.At(x86.R9, 0)) // byte b

	a.CmpRR(x86.RAX, x86.RCX)
	a.Jcc(x86.Less, neg1)
	a.Jcc(x86.Greater, pos1)
	a.AddRI(x86.R12, 1)
	a.Jmp(loop)

	// Every byte of the shorter's length matched: the shorter string sorts first,
	// unless they are the same length, in which case they are equal.
	a.Bind(afterCommon)
	a.MovRM(x86.RAX, x86.At(x86.RBX, objHeaderSize)) // len a again (rax clobbered)
	a.MovRM(x86.RCX, x86.At(x86.R13, objHeaderSize)) // len b
	a.CmpRR(x86.RAX, x86.RCX)
	a.Jcc(x86.Less, neg1)
	a.Jcc(x86.Greater, pos1)
	a.XorRR(x86.RAX, x86.RAX)
	a.Jmp(done)

	a.Bind(neg1)
	a.MovRI(x86.RAX, ^uint64(0)) // -1
	a.Jmp(done)
	a.Bind(pos1)
	a.MovRI(x86.RAX, 1)

	a.Bind(done)
	a.Pop(x86.R14)
	a.Pop(x86.R13)
	a.Pop(x86.R12)
	a.Pop(x86.RBX)
	a.Ret()
}

// eqWordPair compares one pair of words -- a's in r8, b's in r9 -- by the WordKind in r14,
// jumping to isFalse when they differ and falling through when they do not.
//
// Both loops in emitEqualObjects need exactly this, over a struct's per-word kinds and over
// an array's single element kind, and the three cases have to stay identical to the virtual
// machine's own `equal` for the differential to hold. So it is written once here and emitted
// at both sites rather than being two pieces of Go that must be kept in step.
func (e *emitter) eqWordPair(isFalse x86.Label) {
	a := e.a
	wordRef := a.NewLabel("eq_word_ref")
	wordFloat := a.NewLabel("eq_word_float")
	done := a.NewLabel("eq_word_done")

	a.CmpRI(x86.R14, int32(layout.WordRef))
	a.Jcc(x86.Equal, wordRef)
	a.CmpRI(x86.R14, int32(layout.WordFloat))
	a.Jcc(x86.Equal, wordFloat)

	// Every other Kind (int, bool, char, unit, or a test's bare "raw") compares as raw
	// bits, exactly as internal/vm's equal() treats them.
	a.CmpRR(x86.R8, x86.R9)
	a.Jcc(x86.NotEqual, isFalse)
	a.Jmp(done)

	a.Bind(wordRef)
	a.MovRR(x86.RDI, x86.R8)
	a.MovRR(x86.RSI, x86.R9)
	a.Call(e.rt.equalObjects)
	a.TestRR(x86.RAX, x86.RAX)
	a.Jcc(x86.Equal, isFalse)
	a.Jmp(done)

	a.Bind(wordFloat)
	// IEEE equality: unordered (either operand NaN) is never equal, which
	// spec/04-expressions.md pins for a float at top level and inside an aggregate alike
	// (floatCompare's comment explains the parity-flag reasoning in full).
	a.MovqXR(x86.XMM0, x86.R8)
	a.MovqXR(x86.XMM1, x86.R9)
	a.UcomisdXX(x86.XMM0, x86.XMM1)
	a.Jcc(x86.Parity, isFalse)
	a.Jcc(x86.NotEqual, isFalse)

	a.Bind(done)
}
