package backend

import (
	"github.com/scarypheonix/meta/internal/compile"
	"github.com/scarypheonix/meta/internal/layout"
	"github.com/scarypheonix/meta/internal/x86"
)

// Standard input, in machine code (spec/18-input.md).
//
// One routine, and what makes it longer than "read until newline" is the two things a
// system call is not: the bytes have to land somewhere before their count is known, and
// the count is what decides how large a String to allocate.
//
// It reads **one byte per `read`**, which is the part worth defending. A buffered read
// consumes bytes past the newline, and `io::read_line` is stateless by construction --
// there is no handle in the language to hold the remainder in (ADR-0043), so the only
// place to keep it would be runtime-global state that every green thread would then have
// to share and lock. Standard input is not a throughput path; a program that wants one
// reads a file (§15), which does use a sized read.
//
// The line travels to the caller through the running thread's TCB slot -- the same slot
// `rt_fs_read` fills and `rt_fs_taken` empties -- rather than being returned, because the
// routine has to return a *status* and that slot is the one place the collector already
// scans. `io::taken_line` lowers to `rt_fs_taken`: one held-text slot, so the two engines
// that hold it in a Go field hold it in one field too.

// inputLineMax is the longest line this will read. It is internal/layout's number because
// all three engines must refuse the same input (process rule 5); it is a *stack* buffer
// here, so the cost of the limit is one frame while the routine runs.
const inputLineMax = int32(layout.MaxInputLine)

// lineBufOff is where the accumulated line sits in the routine's own frame, below the four
// callee-saved registers runtimePrologue pushes; lineCharOff is the single byte each
// `read` lands in, below that.
const (
	lineBufOff  = -40 - inputLineMax
	lineCharOff = lineBufOff - 8
)

// newlineByte is the one byte that ends a line. A `\r` before it is left on: the prelude
// strips it, so the rule is written once rather than once per engine (spec/18-input.md).
const newlineByte = 10

// emitStdinRead writes `rt_stdin_read() -> status rax`, leaving the line in this thread's
// TCB slot for `rt_fs_taken` to collect.
func (e *emitter) emitStdinRead() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.stdinRead)

	e.runtimePrologue()
	// rsp is rbp-40 here, which is 8 mod 16; the frame is the line buffer, the one-byte
	// landing place, and eight more bytes to put rsp back on a 16-byte boundary for the
	// call to rt_str_alloc below.
	a.SubRI(x86.RSP, inputLineMax+24)

	loop := a.NewLabel("stdin_loop")
	endOfLine := a.NewLabel("stdin_end_of_line")
	atEOF := a.NewLabel("stdin_eof")
	tooLong := a.NewLabel("stdin_too_long")
	failed := a.NewLabel("stdin_failed")
	endOfInput := a.NewLabel("stdin_end_of_input")

	a.XorRR(x86.R12, x86.R12) // bytes accumulated

	a.Bind(loop)
	// read(0, &byte, 1)
	a.MovRI(x86.RAX, e.target.SysRead)
	a.XorRR(x86.RDI, x86.RDI)
	a.Lea(x86.RSI, x86.At(x86.RBP, lineCharOff))
	a.MovRI(x86.RDX, 1)
	a.Syscall()
	a.CmpRI(x86.RAX, 0)
	a.Jcc(x86.Less, failed)
	a.Jcc(x86.Equal, atEOF)

	a.XorRR(x86.RDX, x86.RDX)
	a.MovRM8(x86.RDX, x86.At(x86.RBP, lineCharOff))
	a.CmpRI(x86.RDX, newlineByte)
	a.Jcc(x86.Equal, endOfLine)

	// The capacity check is *after* the byte is read, not before: a line of exactly
	// inputLineMax bytes followed by a newline is a legal line, and there is no way to
	// know which one this is without having the byte in hand.
	a.CmpRI(x86.R12, inputLineMax)
	a.Jcc(x86.GreaterEqual, tooLong)
	a.Lea(x86.RCX, x86.At(x86.RBP, lineBufOff))
	a.AddRR(x86.RCX, x86.R12)
	a.MovMR8(x86.At(x86.RCX, 0), x86.RDX)
	a.AddRI(x86.R12, 1)
	a.Jmp(loop)

	// End of file with nothing accumulated is the end of the input; with something
	// accumulated it is a last line that had no terminator, which is a line
	// (spec/18-input.md's table).
	a.Bind(atEOF)
	a.TestRR(x86.R12, x86.R12)
	a.Jcc(x86.Equal, endOfInput)

	a.Bind(endOfLine)
	// The String to hand back. Nothing of the caller's is in a register across this, and
	// the line itself is on the stack, so a collection here has nothing to move.
	a.MovRR(x86.RDI, x86.R12)
	a.Call(e.rt.strAlloc)
	a.MovRR(x86.RBX, x86.RAX) // the String, across the copy: strCopyBytes clobbers rdx

	a.MovRR(x86.RDI, x86.RAX)
	a.AddRI(x86.RDI, strBytesOff)
	a.Lea(x86.RSI, x86.At(x86.RBP, lineBufOff))
	a.MovRR(x86.RCX, x86.R12)
	e.strCopyBytes()

	a.MovRM(x86.RCX, x86.At(x86.R15, rtCurrentOff))
	a.MovMR(x86.At(x86.RCX, tcbTakenOff), x86.RBX)
	a.MovMI(x86.At(x86.RCX, tcbTakenIsRefOff), 1)
	a.MovRI(x86.RAX, compile.IOOk)
	e.stdinReturn()

	a.Bind(endOfInput)
	a.MovRI(x86.RAX, compile.IOEndOfInput)
	e.stdinReturn()

	a.Bind(tooLong)
	a.MovRI(x86.RAX, compile.IOTooLong)
	e.stdinReturn()

	a.Bind(failed)
	a.MovRI(x86.RAX, compile.IOOther)
	e.stdinReturn()
}

// stdinReturn unwinds the frame emitStdinRead's four exits share.
func (e *emitter) stdinReturn() {
	e.a.AddRI(x86.RSP, inputLineMax+24)
	e.runtimeEpilogue()
	e.a.Ret()
}
