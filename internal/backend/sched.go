package backend

import "github.com/scarypheonix/meta/internal/x86"

// The run queue (spec/12-concurrency.md).
//
// `join` alone needed no scheduler. There was exactly one thing a thread could wait for,
// and it could name it: switch into the thread being joined, run it to completion, take
// the result. A channel breaks that. A thread parked on a receive waits for whichever
// thread eventually sends, which nobody knows at the moment it parks -- so parking has to
// mean "hand the processor to whoever can use it" rather than "run this one".
//
// The scheduler is cooperative and single-processor: every switch is explicit, at a park,
// a wake, or a thread's end. That is not a limitation the specification objects to (§12's
// determinism clause says a program whose output depends on which thread wins a race is
// not a valid case) and it is what keeps ADR-0022's stop-the-world collector correct
// without a single lock: no thread is ever halfway through an instruction while another
// runs. Preemption at back edges, which §08 does specify, needs a safepoint check in
// compiled code and is recorded in docs/deferred.md rather than pretended at here.
//
// Waking is a broadcast, exactly as it is in the virtual machine: a state change clears
// every thread's blocked flag, each re-checks its own condition on the way out, and one
// that still cannot proceed parks again. Nothing tracks *which* thread a change could
// possibly satisfy, because the condition a thread is waiting for lives in its own code
// (the loop around its park) and not in any structure the waker can read. A thread woken
// for nothing costs one switch and one re-check; getting this wrong in the other direction
// -- failing to wake a thread that could proceed -- is a hang.
//
// Every routine here that can be on the stack when a collection happens sets up the full
// canonical frame (rbp, then rbx/r12/r13/r14), because the collector describes a frame it
// finds no stack-map entry for as exactly that shape (collect.go's
// emitGCRuntimeFrameEntry). A parked thread's chain starts inside these routines.

// Statuses the blocking runtime routines return in rax. The caller -- the lowering of the
// builtin, in lower.go -- turns a non-zero one into a trap with its own span, rather than
// the runtime trapping with a span it does not have.
const (
	schedOK       = 0
	schedDeadlock = 1
	// schedRefused is the operation's own error: sending on a closed channel, closing a
	// closed channel, joining a joined handle, re-entering a held mutex. Which one it
	// means is decided by which routine returned it, so one status serves them all.
	schedRefused = 2
)

// emitSchedNext writes `rt_sched_next() -> rax`: the next runnable thread after the
// current one in list order, or 0 when there is none.
//
// Round-robin over the one list every thread is already on (thread.go's rtThreadsOff, the
// same list the collector walks) rather than a queue of its own: with a handful of green
// threads a scan is shorter than the code to keep a queue correct, and the list is the
// only structure that is guaranteed to hold every thread whatever state it is in.
//
// A leaf. It calls nothing and cannot allocate, so it needs no frame for the collector to
// find, and it uses only rax/rcx/rdx/r8, which the private runtime convention leaves free.
func (e *emitter) emitSchedNext() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.schedNext)

	found := a.NewLabel("sched_found")
	none := a.NewLabel("sched_none")
	afterLoop := a.NewLabel("sched_after_loop")
	fromHead := a.NewLabel("sched_from_head")
	headLoop := a.NewLabel("sched_head_loop")

	// Pass one: the first runnable thread strictly after the current one, so that a thread
	// yielding repeatedly does not always hand back to the same neighbour.
	a.MovRM(x86.RCX, x86.At(x86.R15, rtCurrentOff))
	a.MovRM(x86.RDX, x86.At(x86.RCX, tcbNextOff))
	a.Bind(afterLoop)
	a.TestRR(x86.RDX, x86.RDX)
	a.Jcc(x86.Equal, fromHead)
	e.schedRunnable(x86.RDX, found)
	a.MovRM(x86.RDX, x86.At(x86.RDX, tcbNextOff))
	a.Jmp(afterLoop)

	// Pass two: from the head, which may reach the current thread itself -- correct for a
	// yield (nothing else wants the processor) and unreachable for a park (the parking
	// thread has already marked itself blocked).
	a.Bind(fromHead)
	a.MovRM(x86.RDX, x86.At(x86.R15, rtThreadsOff))
	a.Bind(headLoop)
	a.TestRR(x86.RDX, x86.RDX)
	a.Jcc(x86.Equal, none)
	e.schedRunnable(x86.RDX, found)
	a.MovRM(x86.RDX, x86.At(x86.RDX, tcbNextOff))
	a.Jmp(headLoop)

	a.Bind(found)
	a.MovRR(x86.RAX, x86.RDX)
	a.Ret()

	a.Bind(none)
	a.XorRR(x86.RAX, x86.RAX)
	a.Ret()
}

// schedRunnable jumps to yes when the thread in reg can run: it has not finished and is
// not blocked on a condition. Clobbers rax and r8.
func (e *emitter) schedRunnable(reg x86.Reg, yes x86.Label) {
	a := e.a
	no := a.NewLabel("sched_not_runnable")
	a.MovRM(x86.RAX, x86.At(reg, tcbStateOff))
	a.CmpRI(x86.RAX, threadDone)
	a.Jcc(x86.Equal, no)
	a.MovRM(x86.R8, x86.At(reg, tcbBlockedOff))
	a.TestRR(x86.R8, x86.R8)
	a.Jcc(x86.Equal, yes)
	a.Bind(no)
}

// emitSchedWakeAll writes `rt_wake_all()`: clear every thread's blocked flag.
//
// A leaf, and deliberately unconditional -- it does not skip the running thread, whose
// flag is zero anyway, and it does not skip finished threads, whose flag nothing reads.
func (e *emitter) emitSchedWakeAll() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.schedWakeAll)

	loop := a.NewLabel("wake_loop")
	done := a.NewLabel("wake_done")
	a.MovRM(x86.RCX, x86.At(x86.R15, rtThreadsOff))
	a.Bind(loop)
	a.TestRR(x86.RCX, x86.RCX)
	a.Jcc(x86.Equal, done)
	a.MovMI(x86.At(x86.RCX, tcbBlockedOff), 0)
	a.MovRM(x86.RCX, x86.At(x86.RCX, tcbNextOff))
	a.Jmp(loop)
	a.Bind(done)
	a.Ret()
}

// emitSchedPark writes `rt_park() -> rax`: give up the processor, and come back when
// something has woken this thread.
//
// The caller sets its own blocked flag first and re-checks its own condition after,
// because the condition lives in the caller's code and nothing here can evaluate it. So
// this returns for two reasons that look the same from the outside -- a real wake, and a
// broadcast that did not help -- and the caller's loop tells them apart.
//
// `schedDeadlock` means no other thread can run: every one of them has finished or is
// blocked, and no code that could clear a blocked flag will ever execute. That is §12's
// total deadlock exactly, detected rather than hung on. The blocked flag is cleared on the
// way out so that the thread about to trap is not itself counted as stuck by anything that
// looks afterwards.
func (e *emitter) emitSchedPark() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.schedPark)

	e.runtimePrologue()

	stuck := a.NewLabel("park_stuck")

	a.MovRM(x86.RBX, x86.At(x86.R15, rtCurrentOff))
	a.Call(e.rt.schedNext)
	a.TestRR(x86.RAX, x86.RAX)
	a.Jcc(x86.Equal, stuck)

	// Park this one and adopt the other. The state is what the collector reads to decide
	// that this stack has a real frame chain to walk (thread.go), so it is set before the
	// switch and restored after it, not around the wake.
	a.MovMI(x86.At(x86.RBX, tcbStateOff), threadParked)
	a.MovMR(x86.At(x86.R15, rtCurrentOff), x86.RAX)
	a.MovRR(x86.RSI, x86.RAX)
	a.MovRR(x86.RDI, x86.RBX)
	a.Call(e.rt.threadSwitch)

	// Resumed: this thread is the current one again, whoever switched back into it.
	a.MovMR(x86.At(x86.R15, rtCurrentOff), x86.RBX)
	a.MovMI(x86.At(x86.RBX, tcbStateOff), threadRunning)
	a.MovMI(x86.At(x86.RBX, tcbBlockedOff), 0)
	a.XorRR(x86.RAX, x86.RAX)
	e.runtimeEpilogue()
	a.Ret()

	a.Bind(stuck)
	a.MovMI(x86.At(x86.RBX, tcbBlockedOff), 0)
	a.MovRI(x86.RAX, schedDeadlock)
	e.runtimeEpilogue()
	a.Ret()
}

// runtimePrologue and runtimeEpilogue are the frame shape collect.go's synthetic stack-map
// entry describes: a frame pointer, then all four callee-saved registers in the order
// gcTrackedRegs names them, at [rbp-8] through [rbp-32]. Every runtime routine that can be
// on the stack while a collection walks it must have exactly this shape, whether or not it
// needs the registers, because the walk recovers the *caller's* register values from these
// slots.
func (e *emitter) runtimePrologue() {
	a := e.a
	a.Push(x86.RBP)
	a.MovRR(x86.RBP, x86.RSP)
	a.Push(x86.RBX)
	a.Push(x86.R12)
	a.Push(x86.R13)
	a.Push(x86.R14)
}

func (e *emitter) runtimeEpilogue() {
	a := e.a
	a.Pop(x86.R14)
	a.Pop(x86.R13)
	a.Pop(x86.R12)
	a.Pop(x86.RBX)
	a.Pop(x86.RBP)
}

// emitSchedDrain writes `rt_drain()`: run every spawned thread that has not finished, and
// return once they all have.
//
// §12 says the process does not exit while a green thread is still runnable -- `main`
// returning ends the program only after every spawned thread is done, deliberately unlike
// Go, where the race between exit and a goroutine is a familiar bug. So `_start` calls
// this between `main` and `exit`.
//
// `main` is never marked done: it is the thread every finished thread's wake brings back,
// and marking it done would leave a program whose threads all finish with nobody to switch
// to. The loop below is what ends instead.
func (e *emitter) emitSchedDrain() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.schedDrain)

	e.runtimePrologue()

	loop := a.NewLabel("drain_loop")
	scan := a.NewLabel("drain_scan")
	scanNext := a.NewLabel("drain_scan_next")
	waitOne := a.NewLabel("drain_wait")
	done := a.NewLabel("drain_done")

	a.MovRM(x86.RBX, x86.At(x86.R15, rtCurrentOff)) // main

	a.Bind(loop)
	// Anything left that is not this thread and not finished?
	a.MovRM(x86.R12, x86.At(x86.R15, rtThreadsOff))
	a.Bind(scan)
	a.TestRR(x86.R12, x86.R12)
	a.Jcc(x86.Equal, done)
	a.CmpRR(x86.R12, x86.RBX)
	a.Jcc(x86.Equal, scanNext)
	a.MovRM(x86.RAX, x86.At(x86.R12, tcbStateOff))
	a.CmpRI(x86.RAX, threadDone)
	a.Jcc(x86.NotEqual, waitOne)
	a.Bind(scanNext)
	a.MovRM(x86.R12, x86.At(x86.R12, tcbNextOff))
	a.Jmp(scan)

	a.Bind(waitOne)
	// Wait for it, exactly the way `join` waits: block, and let the scheduler run whoever
	// can. A finishing thread wakes everybody, which is what brings this loop back.
	a.MovMI(x86.At(x86.RBX, tcbBlockedOff), 1)
	a.Call(e.rt.schedPark)
	a.TestRR(x86.RAX, x86.RAX)
	a.Jcc(x86.Equal, loop)

	// Nothing runnable and something unfinished: every remaining thread is blocked on a
	// condition no thread will ever satisfy (§12). `main` has left its own code, so there
	// is no user span to name -- unlike a deadlock a thread's own `recv` detects.
	e.trapWith(e.deadlockMsg)

	a.Bind(done)
	e.runtimeEpilogue()
	a.Ret()
}
