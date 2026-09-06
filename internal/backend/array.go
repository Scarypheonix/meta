package backend

import "github.com/scarypheonix/meta/internal/x86"

// The array operations, in machine code (spec/13-collections.md, ADR-0028).
//
// An `Array[T]` is one object: payload word 0 is the length, and the elements follow it.
// Capacity is the object's own size, which the header already records, so an array carries
// no field for it and `cap` is arithmetic on the header rather than a load.
//
// Only `rt_array_new` allocates, and it is therefore the only one of these with a frame the
// collector can walk. The rest are leaves: they read and write words in an object the caller
// already holds, and nothing they do can move it.
//
// The bounds checks are against the *length*, never the capacity. A slot nothing has pushed
// to is not a slot, and reading one would be exactly the undefined behaviour §04 leaves no
// room for -- which is also why there is no operation that lengthens the array without
// writing the element first.

// debugStaleRefs turns on a run-time assertion: every array operation checks that the
// reference it was handed points inside the semispace the program is currently allocating
// out of. A reference that does not is one a collection moved and something failed to
// update -- a lost root, which otherwise surfaces much later, somewhere else, as a wrong
// length, a wrong capacity, or an index the caller had every reason to believe was in
// range. Turning it on is what named the failure in Phase 9's lost root: not "index out of
// range seven thousand lines into a dump" but "a stale, already-forwarded array reached
// rt_array_push", which says in one line that the object was copied and one reference to
// it was not updated.
//
// Off by default -- it costs four instructions on the hottest primitive in the language --
// and on in internal/backend/collect_test.go, which is where it has to keep working. Every
// array is heap-allocated (ADR-0028), so "outside the current semispace" and "stale" are
// the same thing here; a String literal in read-only data, which is neither, never reaches
// these routines.
var debugStaleRefs = false

// checkInSpace traps if the reference in reg is outside [end-heapSize, end), the
// semispace alloc is currently handing out of. Two messages: a stale reference whose
// old header carries a forwarding pointer is one the collector *did* copy and something
// failed to update (a missed root), and one without is an object the walk never reached
// at all. Clobbers rax and rcx.
func (e *emitter) checkInSpace(reg x86.Reg, name string) {
	if !debugStaleRefs {
		return
	}
	a := e.a
	ok := a.NewLabel("in_space")
	stale := a.NewLabel("stale_ref")
	fwd := a.NewLabel("stale_fwd")
	a.MovRM(x86.RAX, x86.At(x86.R15, rtEndOff))
	a.CmpRR(reg, x86.RAX)
	a.Jcc(x86.AboveEqual, stale)
	a.SubRI(x86.RAX, heapSize)
	a.CmpRR(reg, x86.RAX)
	a.Jcc(x86.AboveEqual, ok)

	a.Bind(stale)
	a.MovRM(x86.RAX, x86.At(reg, 0))
	a.ShrI(x86.RAX, 63)
	a.TestRR(x86.RAX, x86.RAX)
	a.Jcc(x86.NotEqual, fwd)
	e.trapWith(e.rawString("origin: stale reference (never copied) in " + name + "\n"))
	a.Bind(fwd)
	e.trapWith(e.rawString("origin: stale reference (forwarded) in " + name + "\n"))
	a.Bind(ok)
}

// arrayLenOff is the payload word holding the length; arrayFirstOff is where element 0
// begins, one word past it.
const (
	arrayLenOff   = objHeaderSize
	arrayFirstOff = objHeaderSize + wordSize
)

// emitArrayNew writes `rt_array_new(capacity rdi, typeid rsi) -> status rax, array rdx`.
//
// The capacity comes from the program and the type id from the compiler, which is the
// division ADR-0028 draws: how much room to leave is a run-time question, and whether the
// elements are references is not.
func (e *emitter) emitArrayNew() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.arrayNew)

	e.runtimePrologue()
	refused := a.NewLabel("array_new_refused")

	a.CmpRI(x86.RDI, 0)
	a.Jcc(x86.Less, refused)

	a.MovRR(x86.RBX, x86.RDI) // capacity, across the allocation
	a.AddRI(x86.RDI, 1)       // the length word sits below the elements
	a.Call(e.rt.alloc)
	a.MovMI(x86.At(x86.RAX, arrayLenOff), 0) // empty until something is pushed
	a.MovRR(x86.RDX, x86.RAX)
	a.XorRR(x86.RAX, x86.RAX)
	e.runtimeEpilogue()
	a.Ret()

	a.Bind(refused)
	a.MovRI(x86.RAX, schedRefused)
	a.XorRR(x86.RDX, x86.RDX)
	e.runtimeEpilogue()
	a.Ret()
}

// arrayWords leaves the object's payload word count in dst: the header's own size field.
func (e *emitter) arrayWords(dst x86.Reg, obj x86.Reg) {
	e.a.MovRM(dst, x86.At(obj, 0))
	e.a.ShrI(dst, 32)
	e.a.MovRI(x86.R10, 0xFFFFFF)
	e.a.AndRR(dst, x86.R10)
}

// emitArrayLen writes `rt_array_len(array rdi) -> rax`. A leaf.
func (e *emitter) emitArrayLen() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.arrayLen)
	e.checkInSpace(x86.RDI, "rt_array_len")
	a.MovRM(x86.RAX, x86.At(x86.RDI, arrayLenOff))
	a.Ret()
}

// emitArrayCap writes `rt_array_cap(array rdi) -> rax`: the object's size, less the word
// the length lives in. A leaf.
func (e *emitter) emitArrayCap() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.arrayCap)
	e.checkInSpace(x86.RDI, "rt_array_cap")
	e.arrayWords(x86.RAX, x86.RDI)
	a.SubRI(x86.RAX, 1)
	a.Ret()
}

// arrayBoundsCheck jumps to refused unless rsi is an index into the array in rdi. The
// comparison is unsigned, so a negative index is a very large one and fails the same test.
func (e *emitter) arrayBoundsCheck(refused x86.Label) {
	a := e.a
	a.MovRM(x86.RCX, x86.At(x86.RDI, arrayLenOff))
	a.CmpRR(x86.RSI, x86.RCX)
	a.Jcc(x86.AboveEqual, refused)
}

// arrayElemAddr leaves the address of element rsi of the array in rdi in dst.
func (e *emitter) arrayElemAddr(dst x86.Reg) {
	a := e.a
	a.MovRR(dst, x86.RSI)
	a.ShlI(dst, 3)
	a.AddRI(dst, arrayFirstOff)
	a.AddRR(dst, x86.RDI)
}

// emitArrayAt writes `rt_array_at(array rdi, index rsi) -> status rax, value rdx`. A leaf.
func (e *emitter) emitArrayAt() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.arrayAt)
	e.checkInSpace(x86.RDI, "rt_array_at")

	refused := a.NewLabel("array_at_refused")
	e.arrayBoundsCheck(refused)
	e.arrayElemAddr(x86.RAX)
	a.MovRM(x86.RDX, x86.At(x86.RAX, 0))
	a.XorRR(x86.RAX, x86.RAX)
	a.Ret()

	a.Bind(refused)
	a.MovRI(x86.RAX, schedRefused)
	a.XorRR(x86.RDX, x86.RDX)
	a.Ret()
}

// emitArraySet writes `rt_array_set(array rdi, index rsi, value rdx) -> status rax`. A leaf.
func (e *emitter) emitArraySet() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.arraySet)
	e.checkInSpace(x86.RDI, "rt_array_set")

	refused := a.NewLabel("array_set_refused")
	e.arrayBoundsCheck(refused)
	e.arrayElemAddr(x86.RAX)
	a.MovMR(x86.At(x86.RAX, 0), x86.RDX)
	a.XorRR(x86.RAX, x86.RAX)
	a.Ret()

	a.Bind(refused)
	a.MovRI(x86.RAX, schedRefused)
	a.Ret()
}

// emitArrayPush writes `rt_array_push(array rdi, value rsi) -> rax`: 1 when the element
// went in, 0 when the array was full. A leaf, and not a trap: a full array is an answer
// rather than an error, and growing one is `List`'s business (spec/13-collections.md).
//
// The element is written before the length grows. Nothing between the two can collect --
// this allocates nothing -- but the order is what makes the invariant readable: at every
// point at which anything else could look, the length describes only words that hold a
// value.
func (e *emitter) emitArrayPush() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.arrayPush)
	e.checkInSpace(x86.RDI, "rt_array_push")

	full := a.NewLabel("array_push_full")
	e.arrayWords(x86.RAX, x86.RDI)
	a.SubRI(x86.RAX, 1) // capacity
	a.MovRM(x86.RCX, x86.At(x86.RDI, arrayLenOff))
	a.CmpRR(x86.RCX, x86.RAX)
	a.Jcc(x86.AboveEqual, full)

	a.MovRR(x86.RAX, x86.RCX)
	a.ShlI(x86.RAX, 3)
	a.AddRI(x86.RAX, arrayFirstOff)
	a.AddRR(x86.RAX, x86.RDI)
	a.MovMR(x86.At(x86.RAX, 0), x86.RSI)
	a.AddRI(x86.RCX, 1)
	a.MovMR(x86.At(x86.RDI, arrayLenOff), x86.RCX)
	a.MovRI(x86.RAX, 1)
	a.Ret()

	a.Bind(full)
	a.XorRR(x86.RAX, x86.RAX)
	a.Ret()
}

// emitArrayTruncate writes `rt_array_truncate(array rdi, length rsi)`: shorten, or do
// nothing. A leaf.
//
// The elements above the new length stay in the object and stop being traced, which is what
// makes them collectable; a later push overwrites them.
func (e *emitter) emitArrayTruncate() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.arrayTruncate)
	e.checkInSpace(x86.RDI, "rt_array_truncate")

	done := a.NewLabel("array_truncate_done")
	clamp := a.NewLabel("array_truncate_clamp")

	a.CmpRI(x86.RSI, 0)
	a.Jcc(x86.Less, clamp)
	a.MovRM(x86.RCX, x86.At(x86.RDI, arrayLenOff))
	a.CmpRR(x86.RSI, x86.RCX)
	a.Jcc(x86.AboveEqual, done) // already shorter than that
	a.MovMR(x86.At(x86.RDI, arrayLenOff), x86.RSI)
	a.Ret()

	a.Bind(clamp)
	a.MovMI(x86.At(x86.RDI, arrayLenOff), 0)

	a.Bind(done)
	a.Ret()
}
