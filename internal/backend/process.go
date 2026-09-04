package backend

import "github.com/scarypheonix/meta/internal/x86"

// emitArgAt writes `rt_arg_at(i rdi) -> status rax, string rdx`: the i'th word of the
// argument vector the kernel left on the stack, copied into a fresh String, and
// schedRefused when i is not one the process was given (spec/17-process.md).
//
// The vector itself is at [r15+rtArgvOff], saved by `_start` before it aligned the stack
// away from it. Its first word is the count and the words after it are NUL-terminated C
// strings -- there is no libc to have measured them, so this does.
//
// It allocates, and it holds a pointer across that allocation. That pointer is into the
// kernel's own stack region, which the collector neither scans nor moves, so the frame
// needs no stack map of its own: there is nothing of anybody's here for a collection to
// invalidate.
func (e *emitter) emitArgAt() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.argAt)

	e.runtimePrologue()
	refused := a.NewLabel("arg_at_refused")

	a.MovRM(x86.R12, x86.At(x86.R15, rtArgvOff)) // the vector: argc, then the pointers
	a.MovRM(x86.RAX, x86.At(x86.R12, 0))         // argc
	a.CmpRI(x86.RDI, 0)
	a.Jcc(x86.Less, refused)
	a.CmpRR(x86.RDI, x86.RAX)
	a.Jcc(x86.GreaterEqual, refused)

	// r13 = argv[i], the C string itself.
	a.MovRR(x86.R13, x86.RDI)
	a.ShlI(x86.R13, 3)
	a.AddRR(x86.R13, x86.R12)
	a.MovRM(x86.R13, x86.At(x86.R13, wordSize))

	// r14 = its length, found by walking to the NUL.
	a.XorRR(x86.R14, x86.R14)
	a.MovRR(x86.RAX, x86.R13)
	measure := a.NewLabel("arg_at_measure")
	measured := a.NewLabel("arg_at_measured")
	a.Bind(measure)
	a.XorRR(x86.RDX, x86.RDX)
	a.MovRM8(x86.RDX, x86.At(x86.RAX, 0))
	a.TestRR(x86.RDX, x86.RDX)
	a.Jcc(x86.Equal, measured)
	a.AddRI(x86.RAX, 1)
	a.AddRI(x86.R14, 1)
	a.Jmp(measure)
	a.Bind(measured)

	a.MovRR(x86.RDI, x86.R14)
	a.Call(e.rt.strAlloc)
	a.MovRR(x86.RBX, x86.RAX) // the String, across the copy: strCopyBytes clobbers rdx

	a.MovRR(x86.RDI, x86.RAX)
	a.AddRI(x86.RDI, strBytesOff)
	a.MovRR(x86.RSI, x86.R13)
	a.MovRR(x86.RCX, x86.R14)
	e.strCopyBytes()

	a.MovRR(x86.RDX, x86.RBX)
	a.XorRR(x86.RAX, x86.RAX)
	e.runtimeEpilogue()
	a.Ret()

	a.Bind(refused)
	a.MovRI(x86.RAX, schedRefused)
	a.XorRR(x86.RDX, x86.RDX)
	e.runtimeEpilogue()
	a.Ret()
}
