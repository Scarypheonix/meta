package backend

import (
	"github.com/scarypheonix/meta/internal/compile"
	"github.com/scarypheonix/meta/internal/layout"
	"github.com/scarypheonix/meta/internal/x86"
)

// The file operations, in machine code (spec/15-files.md).
//
// Four routines, each a system call or two around a String. What makes them longer than
// they sound is the two things a system call needs that Origin values are not: a path has
// to be a NUL-terminated run of bytes somewhere the kernel can read, and a file's size has
// to be known before a String can be allocated to hold it.
//
// The size comes from `lseek` to the end and back rather than from `fstat`, whose result
// struct is one of the few things Linux and macOS genuinely disagree about. Seeking is
// portable arithmetic, and it costs two extra system calls on a path that already makes
// three.
//
// A read's bytes are handed to the caller through the running thread's own TCB slot -- the
// one `rt_chan_taken` uses -- rather than returned, because the operation has to return a
// *status* and there is nowhere else to put a second value that the collector already
// scans. §12's `recv` splits itself the same way, for the same reason.

// pathMax is how long a path this compiles for. It is a stack buffer in each routine's own
// frame, so the cost of the limit is one frame rather than a fixed allocation.
const pathMax = 4096

// The `open` flags and `lseek` whences that are the same on both systems.
const (
	openReadOnly = 0
	seekSet      = 0
	seekEnd      = 2
	writeMode    = 0o644
)

// emitFsCopyPath writes `rt_fs_copy_path(path rdi, dst rsi) -> status rax`: the String's
// bytes into a NUL-terminated buffer. A leaf.
//
// A path holding a NUL byte is refused rather than silently truncated, which is what the
// kernel would otherwise do with it -- and a truncated path names a different file.
func (e *emitter) emitFsCopyPath() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.fsCopyPath)

	tooLong := a.NewLabel("path_refused")
	loop := a.NewLabel("path_loop")
	done := a.NewLabel("path_done")

	a.MovRM(x86.RCX, x86.At(x86.RDI, strLenOff))
	a.CmpRI(x86.RCX, pathMax-1)
	a.Jcc(x86.AboveEqual, tooLong)

	a.AddRI(x86.RDI, strBytesOff)
	a.XorRR(x86.R8, x86.R8) // i
	a.Bind(loop)
	a.CmpRR(x86.R8, x86.RCX)
	a.Jcc(x86.AboveEqual, done)
	a.MovRR(x86.RAX, x86.RDI)
	a.AddRR(x86.RAX, x86.R8)
	a.XorRR(x86.RDX, x86.RDX)
	a.MovRM8(x86.RDX, x86.At(x86.RAX, 0))
	a.TestRR(x86.RDX, x86.RDX)
	a.Jcc(x86.Equal, tooLong) // an interior NUL
	a.MovRR(x86.RAX, x86.RSI)
	a.AddRR(x86.RAX, x86.R8)
	a.MovMR8(x86.At(x86.RAX, 0), x86.RDX)
	a.AddRI(x86.R8, 1)
	a.Jmp(loop)

	a.Bind(done)
	a.MovRR(x86.RAX, x86.RSI)
	a.AddRR(x86.RAX, x86.RCX)
	a.XorRR(x86.RDX, x86.RDX)
	a.MovMR8(x86.At(x86.RAX, 0), x86.RDX) // the terminating NUL
	a.XorRR(x86.RAX, x86.RAX)
	a.Ret()

	a.Bind(tooLong)
	a.MovRI(x86.RAX, compile.IOOther)
	a.Ret()
}

// fsClassify turns a failed system call's result in rax -- which both systems report as
// -errno -- into one of spec/15-files.md's statuses, leaving it in rax.
func (e *emitter) fsClassify() {
	a := e.a
	notFound := a.NewLabel("errno_not_found")
	denied := a.NewLabel("errno_denied")
	done := a.NewLabel("errno_done")

	a.Neg(x86.RAX)
	a.CmpRI(x86.RAX, int32(e.target.ErrNotFound))
	a.Jcc(x86.Equal, notFound)
	a.CmpRI(x86.RAX, int32(e.target.ErrPermission))
	a.Jcc(x86.Equal, denied)
	a.MovRI(x86.RAX, compile.IOOther)
	a.Jmp(done)
	a.Bind(notFound)
	a.MovRI(x86.RAX, compile.IONotFound)
	a.Jmp(done)
	a.Bind(denied)
	a.MovRI(x86.RAX, compile.IOPermission)
	a.Bind(done)
}

// pathBufOff is where each routine's path buffer sits in its own frame: below the four
// callee-saved registers runtimePrologue pushes.
const pathBufOff = -40 - pathMax

// emitFsRead writes `rt_fs_read(path rdi) -> status rax`, leaving the bytes in this
// thread's TCB slot for `rt_fs_taken` to collect.
func (e *emitter) emitFsRead() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.fsRead)

	e.runtimePrologue()
	a.SubRI(x86.RSP, pathMax+8) // +8 keeps rsp 16-byte aligned for the call below

	badPath := a.NewLabel("read_bad_path")
	openFailed := a.NewLabel("read_open_failed")
	readLoop := a.NewLabel("read_loop")
	readDone := a.NewLabel("read_done")
	fail := a.NewLabel("read_fail")

	a.Lea(x86.RSI, x86.At(x86.RBP, pathBufOff))
	a.Call(e.rt.fsCopyPath)
	a.TestRR(x86.RAX, x86.RAX)
	a.Jcc(x86.NotEqual, badPath)

	// open(path, O_RDONLY, 0)
	a.MovRI(x86.RAX, e.target.SysOpen)
	a.Lea(x86.RDI, x86.At(x86.RBP, pathBufOff))
	a.MovRI(x86.RSI, openReadOnly)
	a.XorRR(x86.RDX, x86.RDX)
	a.Syscall()
	a.CmpRI(x86.RAX, 0)
	a.Jcc(x86.Less, openFailed)
	a.MovRR(x86.RBX, x86.RAX) // fd

	// size = lseek(fd, 0, SEEK_END), then rewind.
	a.MovRI(x86.RAX, e.target.SysLseek)
	a.MovRR(x86.RDI, x86.RBX)
	a.XorRR(x86.RSI, x86.RSI)
	a.MovRI(x86.RDX, seekEnd)
	a.Syscall()
	a.CmpRI(x86.RAX, 0)
	a.Jcc(x86.Less, fail)
	// A size no String can hold is refused rather than allocated for. It is also how a
	// *directory* is caught: `open` on one succeeds on Linux, and seeking to its end
	// answers i64::MAX -- which would otherwise become an allocation that traps `out of
	// memory` where the other two engines return an error value.
	a.MovRI(x86.RCX, uint64(layout.MaxStringBytes))
	a.CmpRR(x86.RAX, x86.RCX)
	a.Jcc(x86.Greater, fail)
	a.MovRR(x86.R13, x86.RAX) // size

	a.MovRI(x86.RAX, e.target.SysLseek)
	a.MovRR(x86.RDI, x86.RBX)
	a.XorRR(x86.RSI, x86.RSI)
	a.MovRI(x86.RDX, seekSet)
	a.Syscall()

	// The String to read into. Nothing after this allocates, so holding it in r14 --
	// which the collector does not scan for a frame with no stack map -- is safe.
	a.MovRR(x86.RDI, x86.R13)
	a.Call(e.rt.strAlloc)
	a.MovRR(x86.R14, x86.RAX)
	a.XorRR(x86.R12, x86.R12) // bytes read so far

	a.Bind(readLoop)
	a.MovRR(x86.RDX, x86.R13)
	a.SubRR(x86.RDX, x86.R12)
	a.TestRR(x86.RDX, x86.RDX)
	a.Jcc(x86.Equal, readDone)
	a.MovRI(x86.RAX, e.target.SysRead)
	a.MovRR(x86.RDI, x86.RBX)
	a.MovRR(x86.RSI, x86.R14)
	a.AddRI(x86.RSI, strBytesOff)
	a.AddRR(x86.RSI, x86.R12)
	a.Syscall()
	a.CmpRI(x86.RAX, 0)
	a.Jcc(x86.Less, fail)      // the read itself failed, which is not end of file
	a.Jcc(x86.Equal, readDone) // end of file, before the size the seek promised
	a.AddRR(x86.R12, x86.RAX)
	a.Jmp(readLoop)

	a.Bind(readDone)
	// A file that grew shorter between the seek and the read leaves the object longer
	// than what was actually read, so the length is what came back rather than what the
	// seek promised. The room above it is free of meaning, exactly as an array's is.
	a.MovMR(x86.At(x86.R14, strLenOff), x86.R12)

	a.MovRI(x86.RAX, e.target.SysClose)
	a.MovRR(x86.RDI, x86.RBX)
	a.Syscall()

	a.MovRM(x86.RCX, x86.At(x86.R15, rtCurrentOff))
	a.MovMR(x86.At(x86.RCX, tcbTakenOff), x86.R14)
	a.MovMI(x86.At(x86.RCX, tcbTakenIsRefOff), 1)
	a.XorRR(x86.RAX, x86.RAX)
	a.AddRI(x86.RSP, pathMax+8)
	e.runtimeEpilogue()
	a.Ret()

	a.Bind(fail)
	// The file opened and something after it did not. Close it before giving up: a
	// descriptor Origin cannot close by hand is exactly what §15 has no handles for.
	a.MovRI(x86.RAX, e.target.SysClose)
	a.MovRR(x86.RDI, x86.RBX)
	a.Syscall()
	a.MovRI(x86.RAX, compile.IOOther)
	a.AddRI(x86.RSP, pathMax+8)
	e.runtimeEpilogue()
	a.Ret()

	a.Bind(openFailed)
	e.fsClassify()
	a.AddRI(x86.RSP, pathMax+8)
	e.runtimeEpilogue()
	a.Ret()

	a.Bind(badPath)
	a.MovRI(x86.RAX, compile.IOOther)
	a.AddRI(x86.RSP, pathMax+8)
	e.runtimeEpilogue()
	a.Ret()
}

// emitFsWrite writes `rt_fs_write(path rdi, contents rsi) -> status rax`.
func (e *emitter) emitFsWrite() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.fsWrite)

	e.runtimePrologue()
	a.SubRI(x86.RSP, pathMax+8)

	badPath := a.NewLabel("write_bad_path")
	openFailed := a.NewLabel("write_open_failed")
	writeLoop := a.NewLabel("write_loop")
	writeDone := a.NewLabel("write_done")
	fail := a.NewLabel("write_fail")

	a.MovRR(x86.R14, x86.RSI) // the contents, across everything below
	a.Lea(x86.RSI, x86.At(x86.RBP, pathBufOff))
	a.Call(e.rt.fsCopyPath)
	a.TestRR(x86.RAX, x86.RAX)
	a.Jcc(x86.NotEqual, badPath)

	// open(path, O_WRONLY|O_CREAT|O_TRUNC, 0644)
	a.MovRI(x86.RAX, e.target.SysOpen)
	a.Lea(x86.RDI, x86.At(x86.RBP, pathBufOff))
	a.MovRI(x86.RSI, e.target.OpenWriteFlags)
	a.MovRI(x86.RDX, writeMode)
	a.Syscall()
	a.CmpRI(x86.RAX, 0)
	a.Jcc(x86.Less, openFailed)
	a.MovRR(x86.RBX, x86.RAX) // fd

	a.MovRM(x86.R13, x86.At(x86.R14, strLenOff)) // how many bytes to write
	a.XorRR(x86.R12, x86.R12)                    // written so far

	a.Bind(writeLoop)
	a.MovRR(x86.RDX, x86.R13)
	a.SubRR(x86.RDX, x86.R12)
	a.TestRR(x86.RDX, x86.RDX)
	a.Jcc(x86.Equal, writeDone)
	a.MovRI(x86.RAX, e.target.SysWrite)
	a.MovRR(x86.RDI, x86.RBX)
	a.MovRR(x86.RSI, x86.R14)
	a.AddRI(x86.RSI, strBytesOff)
	a.AddRR(x86.RSI, x86.R12)
	a.Syscall()
	a.CmpRI(x86.RAX, 0)
	a.Jcc(x86.LessEqual, fail) // a short write that made no progress is a failure
	a.AddRR(x86.R12, x86.RAX)
	a.Jmp(writeLoop)

	a.Bind(writeDone)
	a.MovRI(x86.RAX, e.target.SysClose)
	a.MovRR(x86.RDI, x86.RBX)
	a.Syscall()
	a.XorRR(x86.RAX, x86.RAX)
	a.AddRI(x86.RSP, pathMax+8)
	e.runtimeEpilogue()
	a.Ret()

	a.Bind(fail)
	a.MovRI(x86.RAX, e.target.SysClose)
	a.MovRR(x86.RDI, x86.RBX)
	a.Syscall()
	a.MovRI(x86.RAX, compile.IOOther)
	a.AddRI(x86.RSP, pathMax+8)
	e.runtimeEpilogue()
	a.Ret()

	a.Bind(openFailed)
	e.fsClassify()
	a.AddRI(x86.RSP, pathMax+8)
	e.runtimeEpilogue()
	a.Ret()

	a.Bind(badPath)
	a.MovRI(x86.RAX, compile.IOOther)
	a.AddRI(x86.RSP, pathMax+8)
	e.runtimeEpilogue()
	a.Ret()
}

// emitFsExists writes `rt_fs_exists(path rdi) -> rax`: 1 when the path can be opened for
// reading, 0 otherwise. Never an error, because a path that cannot be read is exactly what
// the answer `false` means (spec/15-files.md).
func (e *emitter) emitFsExists() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.fsExists)

	e.runtimePrologue()
	a.SubRI(x86.RSP, pathMax+8)

	no := a.NewLabel("exists_no")

	a.Lea(x86.RSI, x86.At(x86.RBP, pathBufOff))
	a.Call(e.rt.fsCopyPath)
	a.TestRR(x86.RAX, x86.RAX)
	a.Jcc(x86.NotEqual, no)

	a.MovRI(x86.RAX, e.target.SysOpen)
	a.Lea(x86.RDI, x86.At(x86.RBP, pathBufOff))
	a.MovRI(x86.RSI, openReadOnly)
	a.XorRR(x86.RDX, x86.RDX)
	a.Syscall()
	a.CmpRI(x86.RAX, 0)
	a.Jcc(x86.Less, no)

	a.MovRR(x86.RBX, x86.RAX)
	a.MovRI(x86.RAX, e.target.SysClose)
	a.MovRR(x86.RDI, x86.RBX)
	a.Syscall()
	a.MovRI(x86.RAX, 1)
	a.AddRI(x86.RSP, pathMax+8)
	e.runtimeEpilogue()
	a.Ret()

	a.Bind(no)
	a.XorRR(x86.RAX, x86.RAX)
	a.AddRI(x86.RSP, pathMax+8)
	e.runtimeEpilogue()
	a.Ret()
}

// emitFsTaken writes `rt_fs_taken() -> rax`: the String the last `rt_fs_read` on this
// thread produced, and the empty string when there is none. A leaf.
func (e *emitter) emitFsTaken() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.fsTaken)

	have := a.NewLabel("taken_have")

	a.MovRM(x86.RCX, x86.At(x86.R15, rtCurrentOff))
	a.MovRM(x86.RAX, x86.At(x86.RCX, tcbTakenOff))
	a.TestRR(x86.RAX, x86.RAX)
	a.Jcc(x86.NotEqual, have)
	a.MovRI(x86.RAX, e.stringLiteral("").addr)
	a.Ret()

	a.Bind(have)
	a.MovMI(x86.At(x86.RCX, tcbTakenOff), 0)
	a.MovMI(x86.At(x86.RCX, tcbTakenIsRefOff), 0)
	a.Ret()
}
