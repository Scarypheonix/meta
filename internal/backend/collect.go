package backend

import (
	"github.com/scarypheonix/meta/internal/layout"
	"github.com/scarypheonix/meta/internal/x86"
)

// The native collector (ADR-0022): a single-space, stop-the-world, semispace copy.
//
// Four routines, each a leaf-ish subroutine callable in isolation:
//
//   - rt_lookup_stack_map: binary search over the stack-map table (mirrors
//     internal/layout.LookupStackMap exactly, machine code standing in for Go).
//   - rt_evacuate: Cheney's per-object step -- copy once, forward every reference to it
//     afterward.
//   - rt_scan_object: evacuate every reference an already-copied object holds, reading
//     its shape from the same per-TypeID table equal.go built (emitTypeTable).
//   - rt_collect: the root walk up the rbp chain (ADR-0022's "The collector's root
//     walk") followed by Cheney's scan loop over the destination space.
//
// None of the first three ever touches rbx/r12/r13/r14: rt_collect uses those four
// physical registers to carry a live root through for as long as no frame between it and
// the register's true owner has spilled it (ADR-0022's SavedMask), and a routine it
// calls must leave them exactly as it found them for that to hold. rt_scan_object is the
// one exception with a real reason to use them anyway -- it pushes and pops all four in
// a standard callee-saved prologue/epilogue, which preserves rt_collect's tracked values
// across the call exactly as the ABI already guarantees for any properly-behaved callee.

// gcLocal offsets rt_collect keeps in its own frame, relative to its own rbp.
const (
	gcWalkRbpOff = -8  // the frame currently being walked, one of rbp0, rbp1, ...
	gcWalkRetOff = -16 // that frame's return address -- the stack map's search key
	gcEntryOff   = -24 // the looked-up StackMapEntry's address in the table
	// gcLocOff0..3 track where each of rbx/r12/r13/r14's *original owner's* value
	// currently lives: 0 means "still the physical register", any other value is the
	// address of the frame save-slot it was found in (ADR-0022's `SavedMask` case).
	gcLocOff0 = -32 // rbx
	gcLocOff1 = -40 // r12
	gcLocOff2 = -48 // r13
	gcLocOff3 = -56 // r14
	gcScanOff = -64 // the Cheney scan pointer, into the destination space
	// gcPendingRbpOff and gcPendingRetOff hold the next frame's rbp and return address
	// once read from the current frame, but not yet installed as gcWalkRbpOff/
	// gcWalkRetOff: gcTransitionLocs still needs the *current* frame's rbp (to compute
	// its own save-slot addresses) and clobbers rcx/rdx/r8/r9 doing so, so the pending
	// values cannot simply ride in registers across that call the way a real callee's
	// return value would.
	gcPendingRbpOff = -72
	gcPendingRetOff = -80
	// gcThreadOff is the thread whose stack the walk is currently on, or 0 while it is on
	// the running thread's own. With more than one green thread every other thread's
	// frames hold live references too, so the walk runs once per thread rather than once
	// (spec/12-concurrency.md, thread.go).
	gcThreadOff = -88
	// gcRawCursorOff and gcRawIndexOff are the cursor the walks over raw runtime memory
	// share -- the channel list and then the mutex list, one after the other: which block,
	// and how far into it. Both live in the frame rather than in registers because every
	// step of such a walk is a call to rt_evacuate.
	gcRawCursorOff = -96
	gcRawIndexOff  = -104
	gcLocalsSz     = 112 // a multiple of 16: the ABI wants rsp aligned at every call
)

// gcTrackedRegs is rt_collect's own root-tracking registers, in RegMask/SavedMask's bit
// order, paired with the frame slot that tracks where each one's current value lives.
var gcTrackedRegs = []struct {
	reg    x86.Reg
	bit    uint8
	locOff int32
}{
	{x86.RBX, 0, gcLocOff0},
	{x86.R12, 1, gcLocOff1},
	{x86.R13, 2, gcLocOff2},
	{x86.R14, 3, gcLocOff3},
}

// allCalleeSavedMask is every gcTrackedRegs bit set: SavedMask's value for a frame that
// unconditionally pushes all four callee-saved registers, which every hand-written
// runtime routine with a full prologue does (emitPanic, emitIntToStr, emitEqualObjects,
// emitScanObject), unlike a compiled Origin function's own prologue (only the callee-
// saved registers its own register allocator actually assigned, ADR-0021).
const allCalleeSavedMask = 0b1111

// emitGCRuntimeFrameEntry lays out the synthetic StackMapEntry the root walk substitutes
// when rt_lookup_stack_map finds nothing (collect.go's emitCollect): RegMask 0 (a
// runtime routine never holds an Origin-level reference of its own -- ADR-0021's own
// documented scope for recordCall), SavedMask allCalleeSavedMask (every runtime routine
// with a full prologue always pushes all four, in the fixed calleeSaved order, so the
// walk can still correctly recover *that frame's caller's* original register values from
// its known save-slot layout), RefCount 0 (no Origin-level spill-slot convention
// applies to a hand-written routine's own locals). Only the fields gcProcessRegRoot,
// gcProcessRefSlots and gcTransitionLocs actually read are meaningful; ReturnAddr is
// never looked up for this row, so it is left zero.
func (e *emitter) emitGCRuntimeFrameEntry() {
	e.gcRuntimeEntryAddr = e.roDataAddr + uint64(len(e.roData))
	row := make([]byte, layout.StackMapEntrySize)
	row[17] = allCalleeSavedMask
	e.roData = append(e.roData, row...)
}

// emitLookupStackMap writes rt_lookup_stack_map: rdi = a return address, result in rax
// is the matching entry's address in the table, or 0 if none matches (a defensive case:
// every user-code call site has an entry, so reaching it is a compiler bug, not a
// program the language rejects).
//
// A leaf: it calls nothing, uses only caller-saved scratch, and never touches
// rdi (the search key stays valid throughout without needing to be re-loaded).
func (e *emitter) emitLookupStackMap() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.lookupStackMap)

	a.MovRM(x86.R8, x86.At(x86.R15, rtStackMapOff))
	a.MovRM(x86.R9, x86.At(x86.R15, rtStackMapCountOff))
	a.XorRR(x86.RCX, x86.RCX) // lo = 0
	a.MovRR(x86.RDX, x86.R9)  // hi = count

	loop := a.NewLabel("lookup_loop")
	notFound := a.NewLabel("lookup_not_found")
	found := a.NewLabel("lookup_found")
	a.Bind(loop)
	a.CmpRR(x86.RCX, x86.RDX)
	a.Jcc(x86.AboveEqual, notFound)

	a.MovRR(x86.RAX, x86.RDX)
	a.AddRR(x86.RAX, x86.RCX)
	a.ShrI(x86.RAX, 1) // mid = (lo+hi)/2

	a.MovRR(x86.R10, x86.RAX)
	a.MovRI(x86.R11, layout.StackMapEntrySize)
	a.ImulRR(x86.R10, x86.R11)
	a.AddRR(x86.R10, x86.R8) // r10 = &table[mid]

	a.MovRM(x86.R11, x86.At(x86.R10, 0)) // entry.ReturnAddr
	a.CmpRR(x86.R11, x86.RDI)
	a.Jcc(x86.Equal, found)

	goRight := a.NewLabel("lookup_go_right")
	a.Jcc(x86.Below, goRight)
	// entry.ReturnAddr > target: hi = mid
	a.MovRR(x86.RDX, x86.RAX)
	a.Jmp(loop)
	a.Bind(goRight)
	// entry.ReturnAddr < target: lo = mid+1
	a.MovRR(x86.RCX, x86.RAX)
	a.AddRI(x86.RCX, 1)
	a.Jmp(loop)

	a.Bind(found)
	a.MovRR(x86.RAX, x86.R10)
	a.Ret()

	a.Bind(notFound)
	a.XorRR(x86.RAX, x86.RAX)
	a.Ret()
}

// emitEvacuate writes rt_evacuate: rdi = a reference (possibly Nil), result in rax is
// where it now lives.
//
// Cheney's per-object step, mirroring internal/gc/collect.go's evacuateMajor: Nil passes
// through; an object already forwarded (its header's top bit set -- layout.Header)
// returns the forwarding address; anything else is copied word for word to the
// destination space's current bump position (rtGcNextOff, which this advances), and the
// source's header is overwritten with a forwarding header pointing at the copy. A single
// generation needs no "already in the destination space" check the way
// internal/gc's own evacuateMajor does for its nursery-or-old-from-space test: every
// live reference, at the start of a native collection, is necessarily still in the one
// space being collected out of, so the forwarding check alone is exactly the "already
// evacuated this cycle" test.
//
// A leaf: it calls nothing and uses only rax, rcx, rdx, rdi, r8, r9, r10, r11 -- never
// rbx/r12/r13/r14, which is what lets rt_collect call it freely while using those four
// to carry a live root through untouched.
func (e *emitter) emitEvacuate() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.evacuate)

	isNil := a.NewLabel("evac_nil")
	notForwarded := a.NewLabel("evac_not_forwarded")
	copyDone := a.NewLabel("evac_copy_done")
	copyLoop := a.NewLabel("evac_copy_loop")

	a.TestRR(x86.RDI, x86.RDI)
	a.Jcc(x86.Equal, isNil)

	a.MovRM(x86.RAX, x86.At(x86.RDI, 0)) // header
	a.MovRR(x86.RCX, x86.RAX)
	a.ShrI(x86.RCX, 63)
	a.TestRR(x86.RCX, x86.RCX)
	a.Jcc(x86.Equal, notForwarded)

	// Forwarded: rax is the header with the forward bit set; clear it to get the
	// destination address layout.Header.Forwarded already decodes the same way.
	a.MovRI(x86.RCX, 0x7FFFFFFFFFFFFFFF)
	a.AndRR(x86.RAX, x86.RCX)
	a.Ret()

	a.Bind(notForwarded)
	// rax is still the (unforwarded) header. words = header >> 32; size = (words+1)*8.
	a.MovRR(x86.RCX, x86.RAX)
	a.ShrI(x86.RCX, 32)
	a.AddRI(x86.RCX, 1)
	a.ShlI(x86.RCX, 3) // rcx = byte size

	a.MovRM(x86.R9, x86.At(x86.R15, rtGcNextOff)) // dst
	a.XorRR(x86.R10, x86.R10)                     // i = 0
	a.Bind(copyLoop)
	a.CmpRR(x86.R10, x86.RCX)
	a.Jcc(x86.AboveEqual, copyDone)
	a.MovRR(x86.RAX, x86.RDI)
	a.AddRR(x86.RAX, x86.R10)
	a.MovRM(x86.R11, x86.At(x86.RAX, 0))
	a.MovRR(x86.RAX, x86.R9)
	a.AddRR(x86.RAX, x86.R10)
	a.MovMR(x86.At(x86.RAX, 0), x86.R11)
	a.AddRI(x86.R10, 8)
	a.Jmp(copyLoop)

	a.Bind(copyDone)
	a.MovRR(x86.RAX, x86.R9)
	a.AddRR(x86.RAX, x86.RCX)
	a.MovMR(x86.At(x86.R15, rtGcNextOff), x86.RAX) // next += size

	// Forward the source: MakeForward(dst) = dst | forwardBit.
	a.MovRR(x86.RAX, x86.R9)
	a.MovRI(x86.RCX, 0x8000000000000000)
	a.OrRR(x86.RAX, x86.RCX)
	a.MovMR(x86.At(x86.RDI, 0), x86.RAX)

	a.MovRR(x86.RAX, x86.R9)
	a.Ret()

	a.Bind(isNil)
	a.XorRR(x86.RAX, x86.RAX)
	a.Ret()
}

// emitScanObject writes rt_scan_object: rdi = an object already copied into the
// destination space, result in rax is its total size in bytes (header included), which
// is also how far rt_collect's own Cheney loop advances its scan pointer.
//
// Mirrors internal/gc/collect.go's scanObject: a ByteArray (String) holds no references
// and is skipped entirely; a Fixed object's Kinds array (equal.go's per-TypeID table,
// e.typeTableAddr -- the same table structural equality reads) says which payload words
// are WordRef, and each one is evacuated in place. RefArray never occurs (no collection
// literals before Phase 7), so, like equal.go, this does not special-case it.
func (e *emitter) emitScanObject() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.scanObject)

	epilogue := a.NewLabel("scan_epilogue")

	a.Push(x86.RBP)
	a.MovRR(x86.RBP, x86.RSP)
	a.Push(x86.RBX)
	a.Push(x86.R12)
	a.Push(x86.R13)
	a.Push(x86.R14)

	a.MovRR(x86.RBX, x86.RDI) // rbx = the object

	a.MovRM(x86.RAX, x86.At(x86.RBX, 0)) // header
	e.maskLow32(x86.RAX, x86.R8)         // rax = typeid
	a.MovRR(x86.RCX, x86.RAX)
	a.ShlI(x86.RCX, typeTableRowShift)
	a.MovRI(x86.R9, e.typeTableAddr)
	a.AddRR(x86.RCX, x86.R9) // rcx = this TypeID's table row

	a.MovRM(x86.RAX, x86.At(x86.RCX, 0)) // shape
	bytesCase := a.NewLabel("scan_bytes")
	refArrayCase := a.NewLabel("scan_ref_array")
	a.CmpRI(x86.RAX, int32(layout.ByteArray))
	a.Jcc(x86.Equal, bytesCase)
	a.CmpRI(x86.RAX, int32(layout.RawArray))
	a.Jcc(x86.Equal, bytesCase) // an array of non-references holds none, like a String
	a.CmpRI(x86.RAX, int32(layout.RefArray))
	a.Jcc(x86.Equal, refArrayCase)

	// Fixed shape: recurse over each reference-kind word.
	a.MovRM(x86.RAX, x86.At(x86.RBX, 0)) // header again (rax was clobbered)
	a.ShrI(x86.RAX, 32)
	a.MovRR(x86.R13, x86.RAX)             // r13 = words
	a.MovRM(x86.R14, x86.At(x86.RCX, 16)) // r14 = kindsAddr

	a.XorRR(x86.R12, x86.R12) // i = 0
	fieldLoop := a.NewLabel("scan_field_loop")
	fieldsDone := a.NewLabel("scan_fields_done")
	notRef := a.NewLabel("scan_not_ref")
	a.Bind(fieldLoop)
	a.CmpRR(x86.R12, x86.R13)
	a.Jcc(x86.GreaterEqual, fieldsDone)

	a.MovRR(x86.RAX, x86.R14)
	a.AddRR(x86.RAX, x86.R12)
	a.XorRR(x86.RCX, x86.RCX)
	a.MovRM8(x86.RCX, x86.At(x86.RAX, 0)) // this word's WordKind
	a.CmpRI(x86.RCX, int32(layout.WordRef))
	a.Jcc(x86.NotEqual, notRef)

	a.MovRR(x86.RAX, x86.R12)
	a.ShlI(x86.RAX, 3)
	a.AddRI(x86.RAX, objHeaderSize)
	a.AddRR(x86.RAX, x86.RBX) // rax = the field's address
	a.MovRM(x86.RDI, x86.At(x86.RAX, 0))
	a.Push(x86.RAX) // preserve the field's address across the call
	a.Call(e.rt.evacuate)
	a.Pop(x86.RCX)
	a.MovMR(x86.At(x86.RCX, 0), x86.RAX)

	a.Bind(notRef)
	a.AddRI(x86.R12, 1)
	a.Jmp(fieldLoop)

	a.Bind(fieldsDone)
	a.MovRR(x86.RAX, x86.R13)
	a.AddRI(x86.RAX, 1)
	a.ShlI(x86.RAX, 3) // (words+1)*8
	a.Jmp(epilogue)

	// A RefArray (ADR-0028): payload word 0 is the length, and that many references
	// follow it. The room above the length holds nothing -- a push writes the element
	// before it grows the length -- so tracing exactly the length is both correct and all
	// that is correct: a word above it may still hold a reference an earlier truncate
	// abandoned, and evacuating that would resurrect it.
	a.Bind(refArrayCase)
	a.MovRM(x86.RAX, x86.At(x86.RBX, 0))
	a.ShrI(x86.RAX, 32)
	a.MovRR(x86.R13, x86.RAX)                        // r13 = words, for the size below
	a.MovRM(x86.R14, x86.At(x86.RBX, objHeaderSize)) // r14 = the length
	a.XorRR(x86.R12, x86.R12)                        // i = 0

	elemLoop := a.NewLabel("scan_elem_loop")
	a.Bind(elemLoop)
	a.CmpRR(x86.R12, x86.R14)
	a.Jcc(x86.GreaterEqual, fieldsDone)
	a.MovRR(x86.RAX, x86.R12)
	a.ShlI(x86.RAX, 3)
	a.AddRI(x86.RAX, objHeaderSize+wordSize) // past the header and the length word
	a.AddRR(x86.RAX, x86.RBX)
	a.MovRM(x86.RDI, x86.At(x86.RAX, 0))
	a.Push(x86.RAX)
	a.Call(e.rt.evacuate)
	a.Pop(x86.RCX)
	a.MovMR(x86.At(x86.RCX, 0), x86.RAX)
	a.AddRI(x86.R12, 1)
	a.Jmp(elemLoop)

	a.Bind(bytesCase)
	// A String holds no references; its total size is (header.Words()+1)*8 exactly like
	// a Fixed object's, since stringObject and rt_int_to_str both set the header's own
	// words field to 1 (the length word) plus the byte payload's word count.
	a.MovRM(x86.RAX, x86.At(x86.RBX, 0))
	a.ShrI(x86.RAX, 32)
	a.AddRI(x86.RAX, 1)
	a.ShlI(x86.RAX, 3)

	a.Bind(epilogue)
	a.Pop(x86.R14)
	a.Pop(x86.R13)
	a.Pop(x86.R12)
	a.Pop(x86.RBX)
	a.Pop(x86.RBP)
	a.Ret()
}

// emitCollect writes rt_collect: r9 = the return address of the call in `alloc`'s own
// caller (the first frame to walk), rbp = that same caller's own rbp -- both exactly as
// emitAlloc left them, since neither it nor anything before this call touched either.
//
// See collect.go's package doc for the root-walk algorithm and ADR-0022 for why it is
// correct: SavedMask tells a live-but-untouched register (still the physical register)
// from one a frame spilled to its own fixed prologue slot (the frame that called it, and
// so needs recovering from there instead).
func (e *emitter) emitCollect() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.collect)

	a.Push(x86.RBP)
	a.MovRR(x86.RBP, x86.RSP)
	a.SubRI(x86.RSP, gcLocalsSz)

	// walkRbp = the value our own `push rbp` just saved -- frame 0's rbp, since alloc
	// touched neither rbp nor rdi/rsi's stack cell before this call.
	a.MovRM(x86.RAX, x86.At(x86.RBP, 0))
	a.MovMR(x86.At(x86.RBP, gcWalkRbpOff), x86.RAX)
	a.MovMR(x86.At(x86.RBP, gcWalkRetOff), x86.R9)

	for _, tr := range gcTrackedRegs {
		a.MovMI(x86.At(x86.RBP, tr.locOff), 0) // 0 = "still the physical register"
	}
	// The walk starts on the running thread's own stack; gcThreadOff advances through
	// every other thread once that one is exhausted (spec/12-concurrency.md).
	a.MovMI(x86.At(x86.RBP, gcThreadOff), 0)

	// The destination space is the one alloc's own bump/end did not just fail out of.
	a.MovRM(x86.RAX, x86.At(x86.R15, rtOtherStartOff))
	a.MovMR(x86.At(x86.R15, rtGcNextOff), x86.RAX)
	a.MovMR(x86.At(x86.RBP, gcScanOff), x86.RAX)

	walkLoop := a.NewLabel("gc_walk_loop")
	walkDone := a.NewLabel("gc_walk_done")
	missingEntry := a.NewLabel("gc_missing_entry")
	a.Bind(walkLoop)

	haveEntry := a.NewLabel("gc_have_entry")
	a.MovRM(x86.RDI, x86.At(x86.RBP, gcWalkRetOff))
	a.Call(e.rt.lookupStackMap)
	a.TestRR(x86.RAX, x86.RAX)
	a.Jcc(x86.NotEqual, haveEntry)
	a.Bind(missingEntry)
	// No real entry: this frame is one of runtime.go's own routines, allocating
	// internally but never wired into recordCall (ADR-0021's own documented scope).
	// Substitute the synthetic all-saved, no-roots-of-its-own entry
	// (emitGCRuntimeFrameEntry) so the walk still correctly recovers this frame's
	// *caller's* original register values, rather than treating a routine the compiler
	// itself wrote as a stack-map gap.
	a.MovRI(x86.RAX, e.gcRuntimeEntryAddr)
	a.Bind(haveEntry)
	a.MovMR(x86.At(x86.RBP, gcEntryOff), x86.RAX)

	for _, tr := range gcTrackedRegs {
		e.gcProcessRegRoot(tr.reg, tr.bit, tr.locOff)
	}
	e.gcProcessRefSlots()

	// Whether there is a caller frame to walk to next must be decided before touching
	// any tracked register's loc: gcTransitionLocs's whole point is recovering *that
	// caller's* original register values from this frame's own save slots, and running
	// it when this is the outermost frame (main's own, its own caller-rbp zero) would
	// overwrite a still-physical, still-correct loc with an address that names nothing
	// this walk will ever use again -- corrupting a physical register frame 0 itself
	// still needs untouched when it resumes.
	//
	// The caller's rbp and return address are stashed in memory (gcPendingRbpOff/
	// gcPendingRetOff), not left riding in rcx/rdx, because gcTransitionLocs needs the
	// *current* frame's rbp -- still in gcWalkRbpOff at this point -- to compute its own
	// save-slot addresses, and its own body freely clobbers rcx/rdx/r8/r9 doing so; only
	// after it returns are gcWalkRbpOff/gcWalkRetOff themselves overwritten.
	a.MovRM(x86.RAX, x86.At(x86.RBP, gcWalkRbpOff))
	a.MovRM(x86.RCX, x86.At(x86.RAX, 0)) // caller's rbp
	a.MovRM(x86.RDX, x86.At(x86.RAX, 8)) // caller's return address
	a.TestRR(x86.RCX, x86.RCX)
	a.Jcc(x86.Equal, walkDone)
	a.MovMR(x86.At(x86.RBP, gcPendingRbpOff), x86.RCX)
	a.MovMR(x86.At(x86.RBP, gcPendingRetOff), x86.RDX)

	e.gcTransitionLocs()
	a.MovRM(x86.RCX, x86.At(x86.RBP, gcPendingRbpOff))
	a.MovRM(x86.RDX, x86.At(x86.RBP, gcPendingRetOff))
	a.MovMR(x86.At(x86.RBP, gcWalkRbpOff), x86.RCX)
	a.MovMR(x86.At(x86.RBP, gcWalkRetOff), x86.RDX)
	a.Jmp(walkLoop)

	a.Bind(walkDone)
	e.gcNextThreadStack(walkLoop)
	e.gcEvacuateChannels()
	e.gcEvacuateMutexes()

	// Cheney's scan: walk the destination space from its own start to its own (growing)
	// bump position, evacuating every reference each already-copied object holds.
	scanLoop := a.NewLabel("gc_scan_loop")
	scanDone := a.NewLabel("gc_scan_done")
	a.Bind(scanLoop)
	a.MovRM(x86.RAX, x86.At(x86.RBP, gcScanOff))
	a.MovRM(x86.RCX, x86.At(x86.R15, rtGcNextOff))
	a.CmpRR(x86.RAX, x86.RCX)
	a.Jcc(x86.AboveEqual, scanDone)
	a.MovRR(x86.RDI, x86.RAX)
	a.Call(e.rt.scanObject) // rax = this object's size in bytes
	a.MovRM(x86.RCX, x86.At(x86.RBP, gcScanOff))
	a.AddRR(x86.RCX, x86.RAX)
	a.MovMR(x86.At(x86.RBP, gcScanOff), x86.RCX)
	a.Jmp(scanLoop)
	a.Bind(scanDone)

	// The collection is over: the destination space is now current, with its bump
	// pointer wherever the Cheney scan's own copying left rtGcNextOff. The space just
	// vacated -- its start recovered from the old rtEndOff, still intact here -- becomes
	// the destination for next time.
	a.MovRM(x86.RCX, x86.At(x86.R15, rtEndOff))
	a.SubRI(x86.RCX, heapSize) // rcx = the space just collected out of, its own start

	a.MovRM(x86.RAX, x86.At(x86.R15, rtOtherStartOff)) // the space just collected into
	a.MovRR(x86.RDX, x86.RAX)
	a.AddRI(x86.RDX, heapSize)

	a.MovRM(x86.R8, x86.At(x86.R15, rtGcNextOff)) // the new bump pointer

	a.MovMR(x86.At(x86.R15, rtBumpOff), x86.R8)
	a.MovMR(x86.At(x86.R15, rtEndOff), x86.RDX)
	a.MovMR(x86.At(x86.R15, rtOtherStartOff), x86.RCX)

	// Nothing further restores the physical registers here: whichever of them frame 0's
	// own RegMask claimed as a root was already evacuated and written directly back into
	// the physical register during iteration 0's own gcProcessRegRoot call, at that point
	// unconditionally still "physical" (nothing has transitioned yet in iteration 0 of
	// any walk). A register frame 0 also happens to have saved (SavedMask) but never
	// claimed as its own root is deliberately never touched again: that save slot holds
	// frame 0's *caller's* value, already correctly updated in place -- by whichever
	// later frame's own gcProcessRegRoot found it there via the transitioned loc -- and
	// frame 0's own ordinary epilogue `pop` is what will carry it back into the register
	// when frame 0 itself eventually returns, not this collector. Reading a transitioned
	// slot back into the physical register here would overwrite frame 0's own live use of
	// that register (its own local, however unrelated to any reference) with a value
	// that was never frame 0's to see in the first place.

	a.AddRI(x86.RSP, gcLocalsSz)
	a.Pop(x86.RBP)
	a.Ret()
}

// gcProcessRegRoot emits the code for one tracked register's root at the current walk
// frame: if the looked-up entry's RegMask names it, evacuate its current value (read
// from wherever locOff says it lives) and write the result back to that same place.
func (e *emitter) gcProcessRegRoot(reg x86.Reg, bit uint8, locOff int32) {
	a := e.a
	skip := a.NewLabel("gc_root_skip")
	inMem := a.NewLabel("gc_root_mem")

	a.MovRM(x86.RAX, x86.At(x86.RBP, gcEntryOff))
	a.XorRR(x86.RCX, x86.RCX)
	a.MovRM8(x86.RCX, x86.At(x86.RAX, 16)) // RegMask
	a.AndRI(x86.RCX, int32(1<<bit))
	a.TestRR(x86.RCX, x86.RCX)
	a.Jcc(x86.Equal, skip)

	a.MovRM(x86.RAX, x86.At(x86.RBP, locOff))
	a.TestRR(x86.RAX, x86.RAX)
	a.Jcc(x86.NotEqual, inMem)

	a.MovRR(x86.RDI, reg)
	a.Call(e.rt.evacuate)
	a.MovRR(reg, x86.RAX)
	a.Jmp(skip)

	a.Bind(inMem)
	a.MovRM(x86.RDI, x86.At(x86.RAX, 0))
	a.Push(x86.RAX) // preserve the slot's address across the call
	a.Call(e.rt.evacuate)
	a.Pop(x86.RCX)
	a.MovMR(x86.At(x86.RCX, 0), x86.RAX)

	a.Bind(skip)
}

// gcProcessRefSlots evacuates every one of the current frame's own reference-kind spill
// slots: RefCount consecutive words starting at walkRbp - RefOffset (regalloc.go's own
// contiguous placement, ADR-0021), each read fresh from the looked-up entry.
func (e *emitter) gcProcessRefSlots() {
	a := e.a
	loop := a.NewLabel("gc_refslot_loop")
	done := a.NewLabel("gc_refslot_done")

	a.MovRM(x86.RAX, x86.At(x86.RBP, gcEntryOff))
	a.MovRM(x86.RCX, x86.At(x86.RAX, 8)) // packs RefOffset (low32) and RefCount (high32)
	a.MovRR(x86.RDX, x86.RCX)
	e.maskLow32(x86.RDX, x86.R8) // rdx = RefOffset
	a.ShrI(x86.RCX, 32)          // rcx = RefCount

	a.XorRR(x86.R8, x86.R8) // j = 0
	a.Bind(loop)
	a.CmpRR(x86.R8, x86.RCX)
	a.Jcc(x86.AboveEqual, done)

	a.MovRM(x86.RAX, x86.At(x86.RBP, gcWalkRbpOff))
	a.SubRR(x86.RAX, x86.RDX)
	a.MovRR(x86.R9, x86.R8)
	a.ShlI(x86.R9, 3)
	a.SubRR(x86.RAX, x86.R9) // rax = the slot's address

	a.Push(x86.RCX)
	a.Push(x86.RDX)
	a.Push(x86.R8)
	a.Push(x86.RAX)
	a.MovRM(x86.RDI, x86.At(x86.RAX, 0))
	a.Call(e.rt.evacuate)
	a.Pop(x86.R9) // the slot's address, popped back
	a.MovMR(x86.At(x86.R9, 0), x86.RAX)
	a.Pop(x86.R8)
	a.Pop(x86.RDX)
	a.Pop(x86.RCX)

	a.AddRI(x86.R8, 1)
	a.Jmp(loop)
	a.Bind(done)
}

// gcTransitionLocs updates every tracked register's loc for the next frame the walk
// visits, per the current entry's SavedMask: a register this frame's own prologue
// pushed has its next owner's original value sitting at a fixed, computable slot in
// this frame (ADR-0022); one it never touched keeps whatever loc already says (still
// physical, or an even earlier frame's slot).
func (e *emitter) gcTransitionLocs() {
	a := e.a
	a.MovRM(x86.RAX, x86.At(x86.RBP, gcEntryOff))
	a.XorRR(x86.RCX, x86.RCX)
	a.MovRM8(x86.RCX, x86.At(x86.RAX, 17)) // SavedMask
	a.XorRR(x86.RDX, x86.RDX)              // rank = 0

	for _, tr := range gcTrackedRegs {
		skip := a.NewLabel("gc_saved_skip")
		a.MovRR(x86.R9, x86.RCX)
		a.AndRI(x86.R9, int32(1<<tr.bit))
		a.TestRR(x86.R9, x86.R9)
		a.Jcc(x86.Equal, skip)

		// slot = walkRbp - 8*(rank+1)
		a.MovRM(x86.RAX, x86.At(x86.RBP, gcWalkRbpOff))
		a.MovRR(x86.R8, x86.RDX)
		a.AddRI(x86.R8, 1)
		a.ShlI(x86.R8, 3)
		a.SubRR(x86.RAX, x86.R8)
		a.MovMR(x86.At(x86.RBP, tr.locOff), x86.RAX)
		a.AddRI(x86.RDX, 1)

		a.Bind(skip)
	}
}

// gcNextThreadStack advances the root walk to the next green thread's stack, jumping back
// to walkLoop when it finds one and falling through when every thread has been walked.
//
// A single-threaded program never enters the loop below: rtThreadsOff is empty, so this
// is one load and one branch, which is what a program that never spawns should pay.
//
// The cursor lives in memory rather than a register throughout. rt_collect deliberately
// does not preserve rbx/r12/r13/r14 -- they hold frame 0's own live roots, which
// gcProcessRegRoot updates in place -- so holding anything in one of them here would
// destroy a root the collector is in the middle of forwarding. The caller-saved registers
// are no better, since every evacuation below is a call.
//
// The three states mean three different things, and confusing them corrupts the heap
// rather than merely missing an object:
//
//   - `ready`: never run, so its stack is six primed zero words and a trampoline address,
//     not a frame chain. Walking it would read those zeros as frames. Only its closure is
//     a root.
//   - `parked`: stopped inside rt_switch, so its chain is real and begins at the rbp that
//     switch saved -- and its four tracked registers are in switch's own pushed slots,
//     which is where the locs must start rather than "physical" (a parked thread owns no
//     physical register).
//   - `done`: no stack worth walking; the result it left behind is the root, and only when
//     the compiler said that result is a reference.
func (e *emitter) gcNextThreadStack(walkLoop x86.Label) {
	a := e.a
	advance := a.NewLabel("gc_thread_advance")
	consider := a.NewLabel("gc_thread_consider")
	finished := a.NewLabel("gc_threads_done")

	// The first pass starts at the head of the list; later ones continue from the cursor.
	a.MovRM(x86.RCX, x86.At(x86.RBP, gcThreadOff))
	a.TestRR(x86.RCX, x86.RCX)
	a.Jcc(x86.NotEqual, advance)
	a.MovRM(x86.RCX, x86.At(x86.R15, rtThreadsOff))
	a.Jmp(consider)

	a.Bind(advance)
	a.MovRM(x86.RCX, x86.At(x86.RBP, gcThreadOff))
	a.MovRM(x86.RCX, x86.At(x86.RCX, tcbNextOff))

	a.Bind(consider)
	a.TestRR(x86.RCX, x86.RCX)
	a.Jcc(x86.Equal, finished)
	a.MovMR(x86.At(x86.RBP, gcThreadOff), x86.RCX)

	// Whatever its state, the closure it was handed is reachable from the control block
	// alone until the thread has run it.
	e.gcEvacuateTCBSlot(tcbClosureOff)

	// A result, if the compiler said it is a reference. The runtime cannot tell a
	// reference from an integer -- that is what the stack map does elsewhere -- so
	// `spawn` records the answer for its own T.
	skipResult := a.NewLabel("gc_thread_no_result")
	a.MovRM(x86.RCX, x86.At(x86.RBP, gcThreadOff))
	a.MovRM(x86.RAX, x86.At(x86.RCX, tcbResultIsRefOff))
	a.TestRR(x86.RAX, x86.RAX)
	a.Jcc(x86.Equal, skipResult)
	e.gcEvacuateTCBSlot(tcbResultOff)
	a.Bind(skipResult)

	// A value a receive took but the program has not read back yet (chan.go): 2 means it
	// is a reference, 1 that it is raw, 0 that the slot holds nothing at all.
	skipTaken := a.NewLabel("gc_thread_no_taken")
	a.MovRM(x86.RCX, x86.At(x86.RBP, gcThreadOff))
	a.MovRM(x86.RAX, x86.At(x86.RCX, tcbTakenIsRefOff))
	a.CmpRI(x86.RAX, 2)
	a.Jcc(x86.NotEqual, skipTaken)
	e.gcEvacuateTCBSlot(tcbTakenOff)
	a.Bind(skipTaken)

	// The running thread's stack was walked first, from its live registers. Walking it
	// again through a control block that describes where it last parked would evacuate
	// stale copies of roots this collection has already moved. Its control block's own
	// slots above are visited all the same: they are roots wherever the thread is, and
	// evacuating one twice is what the forwarding pointer is for.
	a.MovRM(x86.RCX, x86.At(x86.RBP, gcThreadOff))
	a.MovRM(x86.RAX, x86.At(x86.R15, rtCurrentOff))
	a.CmpRR(x86.RCX, x86.RAX)
	a.Jcc(x86.Equal, advance)

	// Only a parked thread has a frame chain to walk.
	a.MovRM(x86.RCX, x86.At(x86.RBP, gcThreadOff))
	a.MovRM(x86.RAX, x86.At(x86.RCX, tcbStateOff))
	a.CmpRI(x86.RAX, threadParked)
	a.Jcc(x86.NotEqual, advance)

	// Its tracked registers live in rt_switch's own pushed slots, addressed from the
	// stack pointer switch saved.
	a.MovRM(x86.RDX, x86.At(x86.RCX, tcbRSPOff))
	for _, tr := range gcTrackedRegs {
		var off int32
		switch tr.reg {
		case x86.RBX:
			off = switchRBXOff
		case x86.R12:
			off = switchR12Off
		case x86.R13:
			off = switchR13Off
		case x86.R14:
			off = switchR14Off
		}
		a.MovRR(x86.RAX, x86.RDX)
		a.AddRI(x86.RAX, off)
		a.MovMR(x86.At(x86.RBP, tr.locOff), x86.RAX)
	}

	// Frame 0 of this thread is whoever called rt_switch: the rbp switch saved, and the
	// return address sitting above its six pushed registers.
	a.MovRM(x86.RAX, x86.At(x86.RCX, tcbRBPOff))
	a.MovMR(x86.At(x86.RBP, gcWalkRbpOff), x86.RAX)
	a.MovRM(x86.RAX, x86.At(x86.RDX, switchRetOff))
	a.MovMR(x86.At(x86.RBP, gcWalkRetOff), x86.RAX)
	a.Jmp(walkLoop)

	a.Bind(finished)
}

// gcEvacuateTCBSlot evacuates one reference held in the thread control block the cursor
// names, and writes the forwarded address back. The block is raw mmap'd memory rather
// than a heap object, so nothing else will ever visit it.
func (e *emitter) gcEvacuateTCBSlot(off int32) {
	a := e.a
	skip := a.NewLabel("gc_tcb_slot_skip")
	a.MovRM(x86.RCX, x86.At(x86.RBP, gcThreadOff))
	a.MovRM(x86.RDI, x86.At(x86.RCX, off))
	a.TestRR(x86.RDI, x86.RDI)
	a.Jcc(x86.Equal, skip)
	a.Call(e.rt.evacuate)
	a.MovRM(x86.RCX, x86.At(x86.RBP, gcThreadOff))
	a.MovMR(x86.At(x86.RCX, off), x86.RAX)
	a.Bind(skip)
}

// gcEvacuateChannels visits what is sitting in every channel's queue.
//
// A value crossing a channel is an ordinary reference into the collected heap
// (spec/12-concurrency.md: values cross by reference, like every aggregate), but a channel
// is raw mmap'd memory with no stack map over it and no object header to read a shape from.
// So the queue is walked from the outside: the compiler wrote down whether this channel's
// element type is a reference (chan.go's chanElemIsRefOff), and the ring's own head and
// length say which slots are occupied.
//
// A channel nothing refers to any more is still walked. Its queue would keep dead objects
// alive, which is a leak rather than a corruption, and closing that gap needs the collector
// to trace the channel objects themselves -- which it cannot, since the handle a program
// holds is an integer, not a reference (the prelude's own `Sender[T]`). docs/deferred.md.
func (e *emitter) gcEvacuateChannels() {
	a := e.a
	loop := a.NewLabel("gc_chan_loop")
	next := a.NewLabel("gc_chan_next")
	done := a.NewLabel("gc_chan_done")
	inner := a.NewLabel("gc_chan_slot_loop")
	innerDone := a.NewLabel("gc_chan_slot_done")
	noWrap := a.NewLabel("gc_chan_no_wrap")

	a.MovRM(x86.RAX, x86.At(x86.R15, rtChannelsOff))
	a.MovMR(x86.At(x86.RBP, gcRawCursorOff), x86.RAX)

	a.Bind(loop)
	a.MovRM(x86.RCX, x86.At(x86.RBP, gcRawCursorOff))
	a.TestRR(x86.RCX, x86.RCX)
	a.Jcc(x86.Equal, done)
	a.MovRM(x86.RAX, x86.At(x86.RCX, chanElemIsRefOff))
	a.TestRR(x86.RAX, x86.RAX)
	a.Jcc(x86.Equal, next)
	a.MovMI(x86.At(x86.RBP, gcRawIndexOff), 0)

	a.Bind(inner)
	a.MovRM(x86.RCX, x86.At(x86.RBP, gcRawCursorOff))
	a.MovRM(x86.RAX, x86.At(x86.RBP, gcRawIndexOff))
	a.MovRM(x86.RDX, x86.At(x86.RCX, chanLenOff))
	a.CmpRR(x86.RAX, x86.RDX)
	a.Jcc(x86.AboveEqual, innerDone)

	// The occupied slots run from head, wrapping once at the end of the ring.
	a.MovRM(x86.R8, x86.At(x86.RCX, chanHeadOff))
	a.AddRR(x86.R8, x86.RAX)
	a.MovRM(x86.R9, x86.At(x86.RCX, chanSlotsOff))
	a.CmpRR(x86.R8, x86.R9)
	a.Jcc(x86.Below, noWrap)
	a.SubRR(x86.R8, x86.R9)
	a.Bind(noWrap)
	a.ShlI(x86.R8, 3)
	a.AddRI(x86.R8, chanQueueOff)
	a.AddRR(x86.R8, x86.RCX) // the slot's address

	a.MovRM(x86.RDI, x86.At(x86.R8, 0))
	a.Push(x86.R8)
	a.Push(x86.R8) // twice, so rsp keeps the alignment the call wants
	a.Call(e.rt.evacuate)
	a.Pop(x86.RCX)
	a.Pop(x86.RCX)
	a.MovMR(x86.At(x86.RCX, 0), x86.RAX)

	a.MovRM(x86.RAX, x86.At(x86.RBP, gcRawIndexOff))
	a.AddRI(x86.RAX, 1)
	a.MovMR(x86.At(x86.RBP, gcRawIndexOff), x86.RAX)
	a.Jmp(inner)

	a.Bind(innerDone)
	a.Bind(next)
	a.MovRM(x86.RCX, x86.At(x86.RBP, gcRawCursorOff))
	a.MovRM(x86.RCX, x86.At(x86.RCX, chanNextOff))
	a.MovMR(x86.At(x86.RBP, gcRawCursorOff), x86.RCX)
	a.Jmp(loop)

	a.Bind(done)
}
