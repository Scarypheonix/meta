package backend

import (
	"github.com/scarypheonix/meta/internal/layout"
	"github.com/scarypheonix/meta/internal/x86"
)

// The runtime is machine code this file emits. ADR-0017 leaves no libc and no dynamic
// loader, so a compiled program starts at its own `_start`, gets its heap from `mmap`,
// writes with `write`, and ends with `exit` — all through raw syscalls.
//
// The routines below use a private convention: arguments in the System V registers,
// result in rax, and rcx, rdx, r10 and r11 free to clobber. Anything a routine needs
// beyond that it pushes. Origin code never calls them directly; lowering does.

const (
	// heapSize is what the runtime asks mmap for at start-up.
	//
	// Nothing reclaims it yet — a collector in native code needs precise stack maps,
	// which need reference-typed IR (docs/spec/11-codegen.md, docs/deferred.md) — so a
	// program's allocation is bounded by this number and `out of memory` is a real trap
	// rather than a formality.
	heapSize = 64 << 20

	// rtBumpOff and rtEndOff are the runtime block's fields, addressed through r15.
	rtBumpOff = 0
	rtEndOff  = 8
	// rtBlockSize is the block's size in bytes; it lives in the writable segment.
	rtBlockSize = 16

	// wordSize is the size of everything the machine holds in a register.
	wordSize = 8
	// headerSize is the object header that precedes every payload (internal/layout).
	objHeaderSize = 8
	// strBytesOff is where a String's bytes begin, past the header and the length word.
	strBytesOff = objHeaderSize + wordSize
)

// runtimeLabels names the emitted routines so lowering can call them.
type runtimeLabels struct {
	start    x86.Label
	alloc    x86.Label
	write    x86.Label
	trap     x86.Label
	intToStr x86.Label
	print    x86.Label
	println  x86.Label
	panic    x86.Label
	// outOfMemory is the trap message the allocator jumps to; it lives in read-only
	// data like every other trap message.
	outOfMemoryAddr uint64
}

// emitRuntime writes every runtime routine and returns their labels. `_start` is emitted
// first so that the entry point is the first byte of the text segment, which is what a
// disassembler and a debugger both expect to find there.
func (e *emitter) emitRuntime(mainLabel x86.Label) {
	e.rt.start = e.a.NewLabel("_start")
	e.rt.alloc = e.a.NewLabel("rt_alloc")
	e.rt.write = e.a.NewLabel("rt_write")
	e.rt.trap = e.a.NewLabel("rt_trap")
	e.rt.intToStr = e.a.NewLabel("rt_int_to_str")
	e.rt.print = e.a.NewLabel("rt_print")
	e.rt.println = e.a.NewLabel("rt_println")
	e.rt.panic = e.a.NewLabel("rt_panic")

	e.emitStart(mainLabel)
	e.emitAlloc()
	e.emitWrite()
	e.emitTrap()
	e.emitIntToStr()
	e.emitPrint()
	e.emitPanic()
}

// emitPanic reports a `panic` and exits 101: rdi = the message String, rsi = the address
// of the site's " at file:line:col\n" suffix, rdx = its length.
//
// Unlike every other trap, a panic's text is not a constant: the message is a String the
// program computed. Only the two ends are known while compiling, so the runtime writes
// three pieces and the result reads exactly as the interpreter's does — which is what
// the end-to-end suite compares.
func (e *emitter) emitPanic() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.panic)

	a.Push(x86.RBP)
	a.MovRR(x86.RBP, x86.RSP)
	a.Push(x86.RBX)
	a.Push(x86.R12)
	a.Push(x86.R13)
	a.Push(x86.R14)

	a.MovRR(x86.RBX, x86.RDI) // the message
	a.MovRR(x86.R12, x86.RSI) // the suffix
	a.MovRR(x86.R13, x86.RDX)

	a.MovRI(x86.RDI, 2)
	a.MovRI(x86.RSI, e.panicPrefix.addr)
	a.MovRI(x86.RDX, uint64(e.panicPrefix.length))
	a.Call(e.rt.write)

	a.MovRI(x86.RDI, 2)
	a.MovRR(x86.RSI, x86.RBX)
	a.AddRI(x86.RSI, strBytesOff)
	a.MovRM(x86.RDX, x86.At(x86.RBX, objHeaderSize))
	a.Call(e.rt.write)

	a.MovRI(x86.RDI, 2)
	a.MovRR(x86.RSI, x86.R12)
	a.MovRR(x86.RDX, x86.R13)
	a.Call(e.rt.write)

	a.MovRI(x86.RAX, e.target.SysExit)
	a.MovRI(x86.RDI, 101)
	a.Syscall()
	a.Ud2()
}

// emitStart is the program's entry point: claim a heap, call `main`, exit cleanly.
func (e *emitter) emitStart(mainLabel x86.Label) {
	a := e.a
	a.Bind(e.rt.start)

	// r15 holds the runtime block for the rest of the program's life (ADR-0018 reserves
	// it). The address is an immediate: the data segment's address was decided before a
	// byte of code was emitted, so there is nothing to relocate.
	a.MovRI(x86.R15, e.dataAddr)

	// mmap(NULL, heapSize, PROT_READ|PROT_WRITE, MAP_PRIVATE|MAP_ANONYMOUS, -1, 0)
	a.MovRI(x86.RAX, e.target.SysMmap)
	a.XorRR(x86.RDI, x86.RDI)
	a.MovRI(x86.RSI, heapSize)
	a.MovRI(x86.RDX, 3)
	a.MovRI(x86.R10, e.target.MapAnonPrivate)
	a.MovRI(x86.R8, ^uint64(0))
	a.XorRR(x86.R9, x86.R9)
	a.Syscall()

	// A failed mmap returns a small negative value rather than a pointer. Treating that
	// as a heap would corrupt the first page of memory silently, so it traps.
	ok := a.NewLabel("heap_ok")
	a.CmpRI(x86.RAX, 0x1000)
	a.Jcc(x86.AboveEqual, ok)
	e.trapWith(e.outOfMemoryMsg)
	a.Bind(ok)

	a.MovMR(x86.At(x86.R15, rtBumpOff), x86.RAX)
	a.MovRI(x86.RCX, heapSize)
	a.AddRR(x86.RAX, x86.RCX)
	a.MovMR(x86.At(x86.R15, rtEndOff), x86.RAX)

	// The kernel hands over a 16-byte aligned stack with argc on top. Origin's `main`
	// takes no arguments, and every call site keeps the alignment the ABI requires, so
	// aligning once here is enough.
	a.AndRI(x86.RSP, -16)

	a.Call(mainLabel)

	a.MovRI(x86.RAX, e.target.SysExit)
	a.XorRR(x86.RDI, x86.RDI)
	a.Syscall()
	a.Ud2()
}

// emitAlloc is the bump allocator: rdi = payload words, rsi = type id, result in rax.
//
// There is no collector behind it. When the bump pointer reaches the end of the mapping
// the program traps with `out of memory`, which spec/08-memory-model.md allows an
// allocation to do; what it does not yet do is reclaim, and that is recorded as deferred
// rather than pretended away.
func (e *emitter) emitAlloc() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.alloc)

	// total = header + words*8
	a.MovRR(x86.RCX, x86.RDI)
	a.ShlI(x86.RCX, 3)
	a.AddRI(x86.RCX, objHeaderSize)

	a.MovRM(x86.RAX, x86.At(x86.R15, rtBumpOff)) // rax = the object's address
	a.MovRR(x86.RDX, x86.RAX)
	a.AddRR(x86.RDX, x86.RCX) // rdx = the new bump pointer

	fits := a.NewLabel("alloc_fits")
	a.MovRM(x86.RCX, x86.At(x86.R15, rtEndOff))
	a.CmpRR(x86.RDX, x86.RCX)
	a.Jcc(x86.BelowEqual, fits)
	e.trapWith(e.outOfMemoryMsg)
	a.Bind(fits)

	a.MovMR(x86.At(x86.R15, rtBumpOff), x86.RDX)

	// The header is the type id in the low 32 bits and the payload size in words at bit
	// 32, exactly as internal/layout packs it for the collector.
	a.MovRR(x86.RCX, x86.RDI)
	a.ShlI(x86.RCX, 32)
	a.OrRR(x86.RCX, x86.RSI)
	a.MovMR(x86.At(x86.RAX, 0), x86.RCX)
	a.Ret()
}

// emitWrite is write(fd = rdi, buf = rsi, len = rdx), looping until the whole buffer is
// gone.
//
// The loop is the point. A single `write` may transfer less than it was given — a large
// program's output down a pipe does exactly that — and a runtime that ignores the return
// value truncates output that the interpreter would have printed in full. The end-to-end
// differential compares the three engines byte for byte, so a short write is a
// miscompilation like any other.
func (e *emitter) emitWrite() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.write)

	loop := a.NewLabel("write_loop")
	done := a.NewLabel("write_done")

	a.Bind(loop)
	a.TestRR(x86.RDX, x86.RDX)
	a.Jcc(x86.Equal, done)

	// The syscall clobbers rcx and r11, so fd is kept in r8 across it.
	a.MovRR(x86.R8, x86.RDI)
	a.MovRI(x86.RAX, e.target.SysWrite)
	a.Syscall()

	// A negative result is an error. There is nowhere useful to report it — stderr may
	// be what just failed — so the program exits non-zero rather than looping forever.
	failed := a.NewLabel("write_failed")
	a.CmpRI(x86.RAX, 0)
	a.Jcc(x86.LessEqual, failed)

	a.AddRR(x86.RSI, x86.RAX)
	a.SubRR(x86.RDX, x86.RAX)
	a.MovRR(x86.RDI, x86.R8)
	a.Jmp(loop)

	a.Bind(failed)
	a.MovRI(x86.RAX, e.target.SysExit)
	a.MovRI(x86.RDI, 1)
	a.Syscall()
	a.Ud2()

	a.Bind(done)
	a.Ret()
}

// emitTrap writes a message to stderr and exits 101: rdi = message, rsi = length.
//
// The message is already complete in read-only data — "origin: divide by zero at
// file.origin:6:13\n" — because every trap site's span is known while compiling. The
// runtime does no formatting, and the text matches what the interpreter and the virtual
// machine print, which is what lets the end-to-end suite compare all three.
func (e *emitter) emitTrap() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.trap)

	a.MovRR(x86.RDX, x86.RSI)
	a.MovRR(x86.RSI, x86.RDI)
	a.MovRI(x86.RDI, 2)
	a.Call(e.rt.write)

	a.MovRI(x86.RAX, e.target.SysExit)
	a.MovRI(x86.RDI, 101)
	a.Syscall()
	a.Ud2()
}

// emitIntToStr renders a signed 64-bit integer as decimal into a fresh String: rdi = the
// integer, result in rax.
func (e *emitter) emitIntToStr() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.intToStr)

	a.Push(x86.RBP)
	a.MovRR(x86.RBP, x86.RSP)
	a.Push(x86.RBX)
	a.Push(x86.R12)
	a.Push(x86.R13)
	a.Push(x86.R14)
	a.SubRI(x86.RSP, 32) // the digit buffer: 20 digits and a sign fit in 21 bytes

	// r12 walks backwards from the end of the buffer; r13 remembers the end.
	a.MovRR(x86.R12, x86.RSP)
	a.AddRI(x86.R12, 32)
	a.MovRR(x86.R13, x86.R12)

	// r14 records whether a minus sign is owed. The magnitude is produced by negating
	// into *unsigned* bits: negating the most negative integer leaves its own bit
	// pattern, which read as unsigned is exactly its magnitude, so the digits come out
	// right with no special case.
	a.XorRR(x86.R14, x86.R14)
	a.MovRR(x86.RAX, x86.RDI)
	notNeg := a.NewLabel("not_negative")
	a.CmpRI(x86.RAX, 0)
	a.Jcc(x86.GreaterEqual, notNeg)
	a.Neg(x86.RAX)
	a.MovRI(x86.R14, 1)
	a.Bind(notNeg)

	digits := a.NewLabel("digit_loop")
	a.MovRI(x86.RBX, 10)
	a.Bind(digits)
	a.XorRR(x86.RDX, x86.RDX)
	a.Div(x86.RBX) // unsigned: rax = quotient, rdx = remainder
	a.AddRI(x86.RDX, '0')
	a.SubRI(x86.R12, 1)
	a.MovMR8(x86.At(x86.R12, 0), x86.RDX)
	a.TestRR(x86.RAX, x86.RAX)
	a.Jcc(x86.NotEqual, digits)

	noSign := a.NewLabel("no_sign")
	a.TestRR(x86.R14, x86.R14)
	a.Jcc(x86.Equal, noSign)
	a.SubRI(x86.R12, 1)
	a.MovRI(x86.RDX, '-')
	a.MovMR8(x86.At(x86.R12, 0), x86.RDX)
	a.Bind(noSign)

	// rbx = length in bytes.
	a.MovRR(x86.RBX, x86.R13)
	a.SubRR(x86.RBX, x86.R12)

	// words = 1 length word + ceil(len/8) byte words.
	a.MovRR(x86.RDI, x86.RBX)
	a.AddRI(x86.RDI, 7)
	a.ShrI(x86.RDI, 3)
	a.AddRI(x86.RDI, 1)
	a.MovRI(x86.RSI, uint64(e.stringType))
	a.Call(e.rt.alloc)

	a.MovMR(x86.At(x86.RAX, objHeaderSize), x86.RBX) // the length word

	// Copy the digits into place, one byte at a time: the source is somewhere in the
	// middle of a stack buffer, so there is no alignment to exploit.
	a.MovRR(x86.RCX, x86.RAX)
	a.AddRI(x86.RCX, strBytesOff)
	copyLoop := a.NewLabel("copy_loop")
	copyDone := a.NewLabel("copy_done")
	a.Bind(copyLoop)
	a.CmpRR(x86.R12, x86.R13)
	a.Jcc(x86.AboveEqual, copyDone)
	a.MovRM8(x86.RDX, x86.At(x86.R12, 0))
	a.MovMR8(x86.At(x86.RCX, 0), x86.RDX)
	a.AddRI(x86.R12, 1)
	a.AddRI(x86.RCX, 1)
	a.Jmp(copyLoop)
	a.Bind(copyDone)

	a.AddRI(x86.RSP, 32)
	a.Pop(x86.R14)
	a.Pop(x86.R13)
	a.Pop(x86.R12)
	a.Pop(x86.RBX)
	a.Pop(x86.RBP)
	a.Ret()
}

// emitPrint writes a String to stdout, with and without a trailing newline. The two
// entry points share a body: `println` sets a flag and falls into `print`.
func (e *emitter) emitPrint() {
	a := e.a
	a.Align(16)

	a.Bind(e.rt.println)
	a.MovRI(x86.RCX, 1)
	body := a.NewLabel("print_body")
	a.Jmp(body)

	a.Bind(e.rt.print)
	a.XorRR(x86.RCX, x86.RCX)

	a.Bind(body)
	a.Push(x86.RBP)
	a.MovRR(x86.RBP, x86.RSP)
	a.Push(x86.RBX)
	a.Push(x86.R12)
	a.SubRI(x86.RSP, 8) // keep rsp 16-byte aligned across the calls below

	a.MovRR(x86.R12, x86.RCX) // the newline flag, saved across the write

	a.MovRM(x86.RDX, x86.At(x86.RDI, objHeaderSize)) // length
	a.MovRR(x86.RSI, x86.RDI)
	a.AddRI(x86.RSI, strBytesOff)
	a.MovRI(x86.RDI, 1)
	a.Call(e.rt.write)

	done := a.NewLabel("print_done")
	a.TestRR(x86.R12, x86.R12)
	a.Jcc(x86.Equal, done)
	a.MovRI(x86.RDI, 1)
	a.MovRI(x86.RSI, e.newlineAddr)
	a.MovRI(x86.RDX, 1)
	a.Call(e.rt.write)

	a.Bind(done)
	a.AddRI(x86.RSP, 8)
	a.Pop(x86.R12)
	a.Pop(x86.RBX)
	a.Pop(x86.RBP)
	a.Ret()
}

// trapWith emits an unconditional trap with a message already in read-only data.
func (e *emitter) trapWith(msg staticStr) {
	e.a.MovRI(x86.RDI, msg.addr)
	e.a.MovRI(x86.RSI, uint64(msg.length))
	e.a.Call(e.rt.trap)
	e.a.Ud2()
}

// stringObject builds the bytes of a String as it exists on the heap, for a literal that
// can live in read-only data instead of being allocated at run time.
func stringObject(s string, typeID layout.TypeID) []byte {
	byteWords := (len(s) + wordSize - 1) / wordSize
	words := uint64(1 + byteWords)

	out := make([]byte, 0, objHeaderSize+int(words)*wordSize)
	out = appendU64(out, uint64(layout.MakeHeader(typeID, words)))
	out = appendU64(out, uint64(len(s)))
	out = append(out, s...)
	for len(out)%wordSize != 0 {
		out = append(out, 0)
	}
	return out
}

func appendU64(b []byte, v uint64) []byte {
	for i := 0; i < 8; i++ {
		b = append(b, byte(v>>(8*i)))
	}
	return b
}
