package backend

import "github.com/scarypheonix/meta/internal/x86"

// Mutex in native code (spec/12-concurrency.md, ADR-0014).
//
// The lock is one word naming the thread that holds it, and the loop that waits for it is
// the same one every channel operation uses. What makes it a mutex rather than a flag is
// where the unlock happens: §12 gives no `lock()` returning a guard, because a guard would
// have to be released by a destructor and Origin has none (ADR-0006 left no unwinding to
// hang one on). `with` takes a closure, and a closure has an end -- so the acquire, the
// call and the release are all emitted at the one call site (lower.go), and there is no
// path between them a program can leave by. A panic inside the body ends the process
// (ADR-0026), which is the only other way out and needs no lock released.
//
// Re-entering traps rather than deadlocking. The thread that already holds the lock is the
// one asking for it again, so no thread can ever release it, and a hang would be a defined
// but useless answer where a trap names the bug at the moment it happens.
const (
	mutexValueOff = 0
	// mutexOwnerOff is the control block of the thread holding the lock, or 0.
	mutexOwnerOff = 8
	// mutexIsRefOff is the compiler's answer, as a channel's is: raw memory carries no
	// stack map, so the collector is told whether the guarded value is a reference.
	mutexIsRefOff = 16
	mutexNextOff  = 24
	mutexSize     = 32
)

// emitMutexNew writes `rt_mutex_new(value rdi, isRef rsi) -> mutex rax`.
//
// One mapping per mutex, in memory the collector does not move, for the same reason a
// channel gets one: the guarded value is reachable through this block while no thread holds
// the lock, and the block cannot move underneath a thread parked on it.
func (e *emitter) emitMutexNew() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.mutexNew)

	e.runtimePrologue()
	a.MovRR(x86.RBX, x86.RDI)
	a.MovRR(x86.R12, x86.RSI)

	ok := a.NewLabel("mutex_new_mapped")
	a.MovRI(x86.RAX, e.target.SysMmap)
	a.XorRR(x86.RDI, x86.RDI)
	a.MovRI(x86.RSI, mutexSize)
	a.MovRI(x86.RDX, 3)
	a.MovRI(x86.R10, e.target.MapAnonPrivate)
	a.MovRI(x86.R8, ^uint64(0))
	a.XorRR(x86.R9, x86.R9)
	a.Syscall()
	a.CmpRI(x86.RAX, 0x1000)
	a.Jcc(x86.AboveEqual, ok)
	e.trapWith(e.outOfMemoryMsg)
	a.Bind(ok)

	a.MovMR(x86.At(x86.RAX, mutexValueOff), x86.RBX)
	a.MovMI(x86.At(x86.RAX, mutexOwnerOff), 0)
	a.MovMR(x86.At(x86.RAX, mutexIsRefOff), x86.R12)

	a.MovRM(x86.RCX, x86.At(x86.R15, rtMutexesOff))
	a.MovMR(x86.At(x86.RAX, mutexNextOff), x86.RCX)
	a.MovMR(x86.At(x86.R15, rtMutexesOff), x86.RAX)

	e.runtimeEpilogue()
	a.Ret()
}

// emitMutexLock writes `rt_mutex_lock(mutex rdi) -> status rax, guarded rdx`.
//
// The guarded value is read after the lock is taken, never before: a collection while this
// thread was parked will have moved it, and the collector rewrites the mutex's own slot
// rather than any copy this routine made on the way in.
func (e *emitter) emitMutexLock() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.mutexLock)

	e.runtimePrologue()
	a.MovRR(x86.RBX, x86.RDI)
	a.MovRM(x86.R13, x86.At(x86.R15, rtCurrentOff))

	refused := a.NewLabel("mutex_refused")
	stuck := a.NewLabel("mutex_stuck")
	wait := a.NewLabel("mutex_wait")
	acquire := a.NewLabel("mutex_acquire")

	a.MovRM(x86.RAX, x86.At(x86.RBX, mutexOwnerOff))
	a.CmpRR(x86.RAX, x86.R13)
	a.Jcc(x86.Equal, refused)

	a.Bind(wait)
	a.MovRM(x86.RAX, x86.At(x86.RBX, mutexOwnerOff))
	a.TestRR(x86.RAX, x86.RAX)
	a.Jcc(x86.Equal, acquire)
	e.blockAndPark(x86.R13, stuck)
	a.Jmp(wait)

	a.Bind(acquire)
	a.MovMR(x86.At(x86.RBX, mutexOwnerOff), x86.R13)
	a.MovRM(x86.RDX, x86.At(x86.RBX, mutexValueOff))
	a.XorRR(x86.RAX, x86.RAX)
	e.runtimeEpilogue()
	a.Ret()

	a.Bind(refused)
	a.MovRI(x86.RAX, schedRefused)
	a.XorRR(x86.RDX, x86.RDX)
	e.runtimeEpilogue()
	a.Ret()

	a.Bind(stuck)
	a.MovRI(x86.RAX, schedDeadlock)
	a.XorRR(x86.RDX, x86.RDX)
	e.runtimeEpilogue()
	a.Ret()
}

// emitMutexUnlock writes `rt_mutex_unlock(mutex rdi)`.
//
// It cannot allocate and cannot park -- it drops the owner and broadcasts -- which is what
// lets the call site hold the body's result on the stack across it (lower.go).
func (e *emitter) emitMutexUnlock() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.mutexUnlock)

	e.runtimePrologue()
	a.MovMI(x86.At(x86.RDI, mutexOwnerOff), 0)
	a.Call(e.rt.schedWakeAll)
	e.runtimeEpilogue()
	a.Ret()
}

// gcEvacuateMutexes visits the value each mutex guards.
//
// While a thread holds the lock the value is also its body's argument, and rooted there
// like any other parameter; between calls this block is the only thing that names it.
// Whether it is a reference at all is the compiler's answer (mutexIsRefOff), for the same
// reason a channel's queue needs one: no header, no stack map, nothing to read a shape from.
func (e *emitter) gcEvacuateMutexes() {
	a := e.a
	loop := a.NewLabel("gc_mutex_loop")
	next := a.NewLabel("gc_mutex_next")
	done := a.NewLabel("gc_mutex_done")

	a.MovRM(x86.RAX, x86.At(x86.R15, rtMutexesOff))
	a.MovMR(x86.At(x86.RBP, gcRawCursorOff), x86.RAX)

	a.Bind(loop)
	a.MovRM(x86.RCX, x86.At(x86.RBP, gcRawCursorOff))
	a.TestRR(x86.RCX, x86.RCX)
	a.Jcc(x86.Equal, done)
	a.MovRM(x86.RAX, x86.At(x86.RCX, mutexIsRefOff))
	a.TestRR(x86.RAX, x86.RAX)
	a.Jcc(x86.Equal, next)

	a.MovRM(x86.RDI, x86.At(x86.RCX, mutexValueOff))
	a.TestRR(x86.RDI, x86.RDI)
	a.Jcc(x86.Equal, next)
	a.Call(e.rt.evacuate)
	a.MovRM(x86.RCX, x86.At(x86.RBP, gcRawCursorOff))
	a.MovMR(x86.At(x86.RCX, mutexValueOff), x86.RAX)

	a.Bind(next)
	a.MovRM(x86.RCX, x86.At(x86.RBP, gcRawCursorOff))
	a.MovRM(x86.RCX, x86.At(x86.RCX, mutexNextOff))
	a.MovMR(x86.At(x86.RBP, gcRawCursorOff), x86.RCX)
	a.Jmp(loop)

	a.Bind(done)
}
