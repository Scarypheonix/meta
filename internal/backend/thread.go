package backend

import "github.com/scarypheonix/meta/internal/x86"

// Green threads in native code (spec/12-concurrency.md).
//
// The interpreter got threads from goroutines and the virtual machine from goroutines
// plus a world lock. Native code has neither, and no libc to ask: a green thread here is
// a stack this runtime mapped itself, a saved register set, and a routine that swaps one
// for another. §08 already fixed the shape -- M:N over a scheduler, preemption at
// safepoints, a stack that grows on demand to 8 MiB -- so what follows is that, minus the
// growth (a fixed reservation, see below).
//
// The order matters. Threads cannot land before the collector can see them: ADR-0022's
// `rt_collect` walks the rbp chain of the thread that allocated, and with more than one
// stack every *other* thread's chain holds live references too. A scheduler that ran
// before the collector learned about it would free reachable objects, silently, which is
// the failure mode process rule 8 exists to refuse. So a thread control block records
// enough for the collector to walk a parked thread's stack, and it is wired into the root
// walk in the same commit that first spawns anything.
const (
	// tcbSize is a thread control block, kept in raw mmap'd memory rather than the
	// collected heap: the collector moves objects, and a structure the scheduler holds a
	// raw pointer to across a switch cannot move underneath it.
	//
	// The block sits at the base of the thread's own mapping, below its stack, so one
	// mmap gives both and freeing is one munmap.
	tcbStateOff = 0
	// tcbRSPOff is the parked thread's stack pointer -- everything else it saved is on
	// that stack, which is what makes a switch a handful of pushes.
	tcbRSPOff = 8
	// tcbRBPOff is its frame pointer at the moment it parked, which is where the
	// collector starts walking this thread's chain (ADR-0022's own walk, per thread).
	tcbRBPOff = 16
	// tcbResultOff holds what the thread's closure returned, read by `join`.
	tcbResultOff = 24
	// tcbClosureOff is the closure the thread runs. It is a reference into the collected
	// heap, so it is a root the collector must visit and rewrite: the thread has not
	// called it yet, and nothing else refers to it.
	tcbClosureOff = 32
	// tcbNextOff chains every thread the program has created, so the collector can find
	// all of their stacks and the scheduler can find the next runnable one.
	tcbNextOff = 40
	// tcbJoinedOff records that a handle has been joined, so joining twice traps rather
	// than blocking forever on a thread that already finished.
	tcbJoinedOff = 48
	// tcbStackBaseOff is the mapping's own address, for munmap when the thread is done.
	tcbStackBaseOff = 56
	// tcbBlockedOff is 1 while this thread is waiting for a condition it cannot check for
	// itself -- a value on a channel, room in one, a thread to finish, a lock to be
	// dropped. The scheduler will not run a blocked thread, so "no thread is runnable" is
	// exactly §12's total deadlock (sched.go).
	//
	// It is deliberately separate from tcbStateOff, which the collector reads for a
	// different question: whether this thread has a stack worth walking. A thread parked
	// on a channel and a thread that merely yielded have the same state and different
	// blockedness.
	tcbBlockedOff = 64
	// tcbResultIsRefOff records whether the result this thread will leave in tcbResultOff
	// is a reference the collector must evacuate, or a raw value it must not touch. The
	// runtime cannot tell them apart -- that is what the stack map exists for elsewhere --
	// so the compiler, which knows `spawn`'s own T, writes the answer here.
	tcbResultIsRefOff = 72
	// tcbTakenOff holds the value a receive took off a channel, between the two halves of
	// the prelude's own `recv` (chan.go). It is a root the collector must visit when
	// tcbTakenIsRefOff says the channel's element type is a reference, for the same reason
	// tcbResultOff is: raw memory, no stack map over it.
	tcbTakenOff      = 80
	tcbTakenIsRefOff = 88
	tcbSize          = 96 // a multiple of 16, so a stack primed above it stays aligned

	// Thread states.
	//
	// The collector reads these: a `ready` thread has never run, so its primed stack is
	// not a frame chain and walking it would misread the six zeroed slots as frames --
	// only its closure is a root. A `parked` one stopped inside rt_switch, so its chain
	// is real and starts at the rbp that switch saved. A `done` one has no stack worth
	// walking; only the result it left behind is a root.
	threadReady   = 0
	threadRunning = 1
	threadParked  = 2
	threadDone    = 3

	// switchSavedBytes is what rt_switch pushes before it swaps stacks: six registers,
	// then the caller's own return address above them. A parked thread's tracked
	// registers are therefore at known offsets from its saved rsp, which is what lets the
	// collector recover them without the thread running.
	switchR15Off = 0
	switchR14Off = 8
	switchR13Off = 16
	switchR12Off = 24
	switchRBPOff = 32
	switchRBXOff = 40
	switchRetOff = 48

	// threadStackSize is the reservation for one green thread, mapped anonymously and
	// therefore committed by the kernel only as pages are touched. §08 specifies a stack
	// that starts small and grows on demand to 8 MiB; reserving 8 MiB of address space
	// and letting demand paging supply the pages achieves the same observable behaviour
	// -- an untouched page costs nothing -- without a growth check on every function
	// entry. The difference shows only in address space, of which x86-64 has plenty.
	//
	// What it does not yet do is *trap* on exhaustion the way §08 requires: running off
	// the end of this mapping faults. A guard page turns that into a trap and is the next
	// piece (docs/deferred.md).
	threadStackSize = 8 << 20
)

// emitThreadSwitch writes `rt_switch(from_tcb rdi, to_tcb rsi)`.
//
// A context switch is only as big as the calling convention says it must be. The System V
// ABI makes rbx, rbp and r12..r15 callee-saved, so a routine that pushes them, swaps
// stacks, and pops them has preserved everything the *caller* was entitled to keep; the
// caller-saved registers were already the caller's problem at any call, and a switch is a
// call. Everything else -- the return address, the locals -- is on the stack that just
// got swapped.
//
// r15 is deliberately saved and restored with the rest even though every thread shares
// one runtime block: ADR-0018 reserves it, and a switch that assumed its value would be
// a rule the next reader has to know rather than one the code states.
func (e *emitter) emitThreadSwitch() {
	a := e.a
	a.Bind(e.rt.threadSwitch)

	a.Push(x86.RBX)
	a.Push(x86.RBP)
	a.Push(x86.R12)
	a.Push(x86.R13)
	a.Push(x86.R14)
	a.Push(x86.R15)

	// Park the outgoing thread: its stack pointer is all the collector and the scheduler
	// need, since the saved registers are now on that stack and rbp is among them.
	a.MovMR(x86.At(x86.RDI, tcbRSPOff), x86.RSP)
	a.MovMR(x86.At(x86.RDI, tcbRBPOff), x86.RBP)

	// Adopt the incoming one.
	a.MovRM(x86.RSP, x86.At(x86.RSI, tcbRSPOff))

	a.Pop(x86.R15)
	a.Pop(x86.R14)
	a.Pop(x86.R13)
	a.Pop(x86.R12)
	a.Pop(x86.RBP)
	a.Pop(x86.RBX)
	// Returns onto the incoming thread's own stack, into whatever it was doing when it
	// parked -- or, for a thread that has never run, into the trampoline its stack was
	// primed with (emitThreadNew).
	a.Ret()
}

// emitThreadNew writes `rt_thread_new(closure rdi) -> tcb rax`.
//
// It maps one region for the control block and the stack together, primes the stack so
// that the first switch into this thread lands in the trampoline, and links it into the
// list every thread is on.
func (e *emitter) emitThreadNew() {
	a := e.a
	a.Bind(e.rt.threadNew)

	a.Push(x86.RBX)
	a.MovRR(x86.RBX, x86.RDI) // the closure, across the mmap call

	// mmap(NULL, tcbSize+threadStackSize, PROT_READ|PROT_WRITE, MAP_PRIVATE|MAP_ANONYMOUS, -1, 0)
	a.MovRI(x86.RAX, e.target.SysMmap)
	a.XorRR(x86.RDI, x86.RDI)
	a.MovRI(x86.RSI, tcbSize+threadStackSize)
	a.MovRI(x86.RDX, 3)
	a.MovRI(x86.R10, e.target.MapAnonPrivate)
	a.MovRI(x86.R8, ^uint64(0))
	a.XorRR(x86.R9, x86.R9)
	a.Syscall()

	ok := a.NewLabel("thread_stack_ok")
	a.CmpRI(x86.RAX, 0x1000)
	a.Jcc(x86.AboveEqual, ok)
	e.trapWith(e.outOfMemoryMsg)
	a.Bind(ok)

	// rax is the mapping: the control block first, the stack above it growing down from
	// the far end.
	a.MovMI(x86.At(x86.RAX, tcbStateOff), threadReady)
	a.MovMI(x86.At(x86.RAX, tcbJoinedOff), 0)
	a.MovMR(x86.At(x86.RAX, tcbClosureOff), x86.RBX)
	a.MovMR(x86.At(x86.RAX, tcbStackBaseOff), x86.RAX)
	a.MovMI(x86.At(x86.RAX, tcbResultOff), 0)
	a.MovMI(x86.At(x86.RAX, tcbTakenOff), 0)
	a.MovMI(x86.At(x86.RAX, tcbTakenIsRefOff), 0)
	a.MovMI(x86.At(x86.RAX, tcbBlockedOff), 0)

	// The stack pointer starts at the top of the mapping, 16-byte aligned, and is then
	// primed with what `rt_switch`'s tail expects to find: six saved registers and a
	// return address it will `ret` into.
	a.MovRR(x86.RCX, x86.RAX)
	a.AddRI(x86.RCX, tcbSize+threadStackSize)
	a.AndRI(x86.RCX, -16)

	// The trampoline's address, as the address `ret` will jump to.
	a.LeaLabel(x86.RDX, e.rt.threadEntry)
	a.SubRI(x86.RCX, wordSize)
	a.MovMR(x86.At(x86.RCX, 0), x86.RDX)

	// Six callee-saved slots beneath it, in the order rt_switch pops them: rbx, rbp,
	// r12, r13, r14 and finally r15, which is popped first and therefore sits lowest.
	//
	// Five of them may hold anything -- the trampoline reads its thread from the runtime
	// block, not from a register -- but the *slots* must exist, because the switch pops
	// six words before it returns. r15 is the exception and must be primed with the
	// runtime block itself (ADR-0018 reserves it): a new thread whose r15 popped as zero
	// would dereference null in the trampoline's first instruction.
	for i := 0; i < 6; i++ {
		a.SubRI(x86.RCX, wordSize)
		a.MovMI(x86.At(x86.RCX, 0), 0)
	}
	a.MovMR(x86.At(x86.RCX, 0), x86.R15)
	a.MovMR(x86.At(x86.RAX, tcbRSPOff), x86.RCX)
	a.MovMR(x86.At(x86.RAX, tcbRBPOff), x86.RCX)

	// Link it onto the list of every thread, which the collector walks for roots.
	a.MovRM(x86.RDX, x86.At(x86.R15, rtThreadsOff))
	a.MovMR(x86.At(x86.RAX, tcbNextOff), x86.RDX)
	a.MovMR(x86.At(x86.R15, rtThreadsOff), x86.RAX)

	a.Pop(x86.RBX)
	a.Ret()
}

// emitThreadEntry writes the trampoline a new thread's primed stack returns into.
//
// It is never called: `rt_switch` pops six saved registers and executes `ret`, and the
// address this label names is what was placed where that `ret` looks. So it begins with
// no arguments, no return address of its own, and a stack that is otherwise empty --
// everything it needs comes from the runtime block.
func (e *emitter) emitThreadEntry() {
	a := e.a
	a.Bind(e.rt.threadEntry)

	// The thread that was switched into is the current one, by definition of the switch.
	a.MovRM(x86.RBX, x86.At(x86.R15, rtCurrentOff))
	a.MovMI(x86.At(x86.RBX, tcbStateOff), threadRunning)

	// Call the closure exactly as lower.go's callClosure does, because the body was
	// compiled expecting that and nothing else: the code address comes out of the object's
	// first field, and the object itself goes on the stack just above the return address,
	// where OpCapture reads it. Twice, for the alignment a call wants.
	//
	// Passing it in rdi instead -- which this did, on the strength of a stale reading of
	// ADR-0020 -- works for exactly as long as the spawned closure captures nothing. The
	// first one that captured a value read its capture from whatever the stack happened to
	// hold at [rbp + 16], which was zero.
	a.MovRM(x86.RDI, x86.At(x86.RBX, tcbClosureOff))
	a.MovRM(x86.RAX, x86.At(x86.RDI, objHeaderSize))
	a.Push(x86.RDI)
	a.Push(x86.RDI)
	a.CallReg(x86.RAX)
	a.AddRI(x86.RSP, 16)

	// Publish the result before the state, so that a thread observing `done` never reads
	// a result that has not been written. Only one thread runs at a time here, so this is
	// ordering for the reader's sake rather than against a race.
	a.MovRM(x86.RBX, x86.At(x86.R15, rtCurrentOff))
	a.MovMR(x86.At(x86.RBX, tcbResultOff), x86.RAX)
	a.MovMI(x86.At(x86.RBX, tcbStateOff), threadDone)

	// Finishing is a state change like any other, so everyone waiting gets to re-check:
	// whoever joined this thread, and `main`'s own drain loop, which is waiting for exactly
	// this (sched.go).
	a.Call(e.rt.schedWakeAll)

	// Hand the processor to whoever can use it. A finished thread never runs again, so this
	// switch does not return and the outgoing save is only bookkeeping.
	stuck := a.NewLabel("entry_stuck")
	a.Call(e.rt.schedNext)
	a.TestRR(x86.RAX, x86.RAX)
	a.Jcc(x86.Equal, stuck)
	a.MovRR(x86.RSI, x86.RAX)
	a.MovMR(x86.At(x86.R15, rtCurrentOff), x86.RSI)
	a.MovRR(x86.RDI, x86.RBX)
	a.Call(e.rt.threadSwitch)

	// Unreachable: nothing ever switches back into a finished thread. If the scheduler
	// ever did, stopping is better than running off the end of the trampoline into
	// whatever follows it (process rule 8).
	a.Ud2()

	// The wake above makes `main` runnable whatever it was waiting for, and `main` is never
	// marked done, so a finished thread always has somewhere to go. Reaching this would
	// mean the thread list itself is wrong rather than that the program deadlocked.
	a.Bind(stuck)
	e.trapWith(e.deadlockMsg)
}

// emitThreadSpawn writes `rt_spawn(closure rdi, resultIsRef rsi) -> tcb rax`.
//
// Creating a thread does not start it. §12 says `spawn` returns immediately and the
// handle is the only way to observe the result, so the new thread sits `ready` until
// something switches into it -- which today is `join` and, when channels land, the
// scheduler.
func (e *emitter) emitThreadSpawn() {
	a := e.a
	a.Bind(e.rt.threadSpawn)

	a.Push(x86.RBX)
	a.MovRR(x86.RBX, x86.RSI) // resultIsRef, across the call
	a.Call(e.rt.threadNew)
	a.MovMR(x86.At(x86.RAX, tcbResultIsRefOff), x86.RBX)
	a.Pop(x86.RBX)
	a.Ret()
}

// emitThreadJoin writes `rt_join(tcb rdi) -> status rax, result rdx`.
//
// Waiting for a thread is now the same shape as waiting for anything else: block, hand the
// processor to the scheduler, and re-check on the way back (sched.go). It no longer runs
// the thread it is waiting for -- a joined thread may itself park on a channel, and then
// the joiner running it would be waiting for a thread that is waiting for a third.
//
// The status says what the caller must trap on, rather than trapping here: a runtime
// routine has no span, and §12's messages name the user's own line. `schedRefused` is
// `handle already joined`, `schedDeadlock` is `all threads are blocked`.
//
// The full prologue is load-bearing, not habit. This frame is what a parked joining
// thread's stack chain starts at (the rbp rt_switch saved is this one), and the collector
// reads a frame it finds no stack-map entry for as the synthetic runtime entry
// (collect.go's emitGCRuntimeFrameEntry): no roots of its own, all four callee-saved
// registers pushed. Without the `push rbp` this frame would not exist in the chain at all,
// the saved rbp would be the *caller's*, and that caller -- ordinary Origin code, holding
// its own live references -- would be described by the synthetic entry instead of by its
// own. Its roots would then never be evacuated, silently, exactly while another thread is
// allocating hard enough to move them.
func (e *emitter) emitThreadJoin() {
	a := e.a
	a.Bind(e.rt.threadJoin)

	e.runtimePrologue()
	a.MovRR(x86.RBX, x86.RDI)                       // the thread being joined
	a.MovRM(x86.R12, x86.At(x86.R15, rtCurrentOff)) // this one

	// Joining twice is a programming error, like sending on a closed channel: the second
	// call would otherwise block forever on a thread that already finished (§12).
	refused := a.NewLabel("join_refused")
	stuck := a.NewLabel("join_stuck")
	a.MovRM(x86.RAX, x86.At(x86.RBX, tcbJoinedOff))
	a.TestRR(x86.RAX, x86.RAX)
	a.Jcc(x86.NotEqual, refused)
	a.MovMI(x86.At(x86.RBX, tcbJoinedOff), 1)

	loop := a.NewLabel("join_loop")
	done := a.NewLabel("join_done")
	a.Bind(loop)
	a.MovRM(x86.RAX, x86.At(x86.RBX, tcbStateOff))
	a.CmpRI(x86.RAX, threadDone)
	a.Jcc(x86.Equal, done)

	a.MovMI(x86.At(x86.R12, tcbBlockedOff), 1)
	a.Call(e.rt.schedPark)
	a.TestRR(x86.RAX, x86.RAX)
	a.Jcc(x86.NotEqual, stuck)
	a.Jmp(loop)

	a.Bind(done)
	a.MovRM(x86.RDX, x86.At(x86.RBX, tcbResultOff))
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

// emitMainThread gives the program's first thread a control block of its own.
//
// It has no stack to map -- the kernel gave it one -- but it needs the block, because
// every switch names two threads and the collector needs somewhere to record where this
// one parked while another runs. Called from `_start` before `main`.
func (e *emitter) emitMainThread() {
	a := e.a
	a.Bind(e.rt.threadMain)

	// mmap one page: a control block, no stack.
	a.MovRI(x86.RAX, e.target.SysMmap)
	a.XorRR(x86.RDI, x86.RDI)
	a.MovRI(x86.RSI, 0x1000)
	a.MovRI(x86.RDX, 3)
	a.MovRI(x86.R10, e.target.MapAnonPrivate)
	a.MovRI(x86.R8, ^uint64(0))
	a.XorRR(x86.R9, x86.R9)
	a.Syscall()

	ok := a.NewLabel("main_tcb_ok")
	a.CmpRI(x86.RAX, 0x1000)
	a.Jcc(x86.AboveEqual, ok)
	e.trapWith(e.outOfMemoryMsg)
	a.Bind(ok)

	a.MovMI(x86.At(x86.RAX, tcbStateOff), threadRunning)
	a.MovMI(x86.At(x86.RAX, tcbJoinedOff), 1) // nothing joins main
	a.MovMI(x86.At(x86.RAX, tcbClosureOff), 0)
	a.MovMI(x86.At(x86.RAX, tcbResultOff), 0)
	a.MovMI(x86.At(x86.RAX, tcbResultIsRefOff), 0)
	a.MovMI(x86.At(x86.RAX, tcbTakenOff), 0)
	a.MovMI(x86.At(x86.RAX, tcbTakenIsRefOff), 0)
	a.MovMI(x86.At(x86.RAX, tcbBlockedOff), 0)
	// Main goes on rtThreadsOff's list like every other thread. The walk skips whichever
	// thread is running (gcNextThreadStack compares each against rtCurrentOff), so listing
	// main costs nothing while main is the thread that allocated -- and while main is
	// parked inside `join`, that list is the collector's only way to reach its stack at
	// all. Leaving it off meant a spawned thread's collection never walked main's frames,
	// so a list main held across the join was not evacuated and came back corrupt.
	a.MovRM(x86.RCX, x86.At(x86.R15, rtThreadsOff))
	a.MovMR(x86.At(x86.RAX, tcbNextOff), x86.RCX)
	a.MovMR(x86.At(x86.R15, rtThreadsOff), x86.RAX)
	a.MovMR(x86.At(x86.R15, rtCurrentOff), x86.RAX)
	a.Ret()
}
