package backend

import "github.com/scarypheonix/meta/internal/x86"

// Channels in native code (spec/12-concurrency.md).
//
// A channel is a ring of `capacity` slots with a length and a head, plus the two bits every
// operation turns on: whether it has been closed, and how many receivers are waiting on it
// right now. The waiting count exists only for rendezvous, where a send must not complete
// until a receive is there to take the value -- with a queue there is nothing to
// synchronise, and the length alone decides who blocks.
//
// Every blocking operation is the same loop: check the condition, and if it does not hold,
// mark this thread blocked and park (sched.go). A wake is a broadcast, so coming back
// proves nothing and the loop re-checks. That is exactly the shape the virtual machine's
// own `w.wait(ready)` has, deliberately: the two engines must agree on which programs
// deadlock and which do not, and the surest way to get that is to make the conditions
// literally the same expressions.
//
// The channel lives in memory the collector does not own -- one mmap per channel, like a
// thread control block -- because the collector moves objects and the scheduler holds a raw
// pointer to a channel across a switch. What it holds *for* the program is another matter:
// a queued value may be a reference, and then the collector must find and rewrite it, which
// is what the channel list (rtChannelsOff) and the compiler-supplied `elemIsRef` are for.
const (
	chanCapOff    = 0
	chanLenOff    = 8
	chanHeadOff   = 16
	chanClosedOff = 24
	// chanElemIsRefOff is the compiler's answer to a question the runtime cannot ask: are
	// the words in this queue references? Raw memory carries no stack map, and only the
	// checker knew the channel's T (internal/compile's concurrencyElemIsRef).
	chanElemIsRefOff = 32
	// chanRecvWaitOff counts receivers parked on this channel, which is what makes a
	// rendezvous send wait for one rather than queueing and returning.
	chanRecvWaitOff = 40
	// chanNextOff chains every channel the program has made, so the collector can visit
	// what is sitting in their queues.
	chanNextOff = 48
	// chanSlotsOff is the ring's length: the capacity, or one for a rendezvous channel,
	// which needs somewhere to put the value it is handing over.
	chanSlotsOff = 56
	chanQueueOff = 64

	// chanMaxSlots bounds what a program may ask for, so that the size arithmetic below
	// cannot overflow into a small mapping that later writes past its end. A capacity this
	// large is out of memory in any case, and saying so is better than wrapping.
	chanMaxSlots = 1 << 28
)

// emitChanNew writes `rt_chan_new(capacity rdi, elemIsRef rsi) -> status rax, channel rdx`.
//
// A negative capacity is refused rather than trapped here (§12 names the user's line for
// it, and only the caller has that).
func (e *emitter) emitChanNew() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.chanNew)

	e.runtimePrologue()

	refused := a.NewLabel("chan_new_refused")
	sized := a.NewLabel("chan_new_sized")
	ok := a.NewLabel("chan_new_mapped")

	a.CmpRI(x86.RDI, 0)
	a.Jcc(x86.Less, refused)
	a.MovRI(x86.RAX, chanMaxSlots)
	a.CmpRR(x86.RDI, x86.RAX)
	a.Jcc(x86.AboveEqual, refused)

	a.MovRR(x86.RBX, x86.RDI) // capacity
	a.MovRR(x86.R12, x86.RSI) // elemIsRef

	// A rendezvous channel still needs one slot: the value crosses through the ring even
	// though it is never queued in the sense of waiting there.
	a.MovRR(x86.R13, x86.RBX)
	a.TestRR(x86.R13, x86.R13)
	a.Jcc(x86.NotEqual, sized)
	a.MovRI(x86.R13, 1)
	a.Bind(sized)

	a.MovRR(x86.RSI, x86.R13)
	a.ShlI(x86.RSI, 3)
	a.AddRI(x86.RSI, chanQueueOff)
	a.MovRI(x86.RAX, e.target.SysMmap)
	a.XorRR(x86.RDI, x86.RDI)
	a.MovRI(x86.RDX, 3)
	a.MovRI(x86.R10, e.target.MapAnonPrivate)
	a.MovRI(x86.R8, ^uint64(0))
	a.XorRR(x86.R9, x86.R9)
	a.Syscall()
	a.CmpRI(x86.RAX, 0x1000)
	a.Jcc(x86.AboveEqual, ok)
	e.trapWith(e.outOfMemoryMsg)
	a.Bind(ok)

	a.MovMR(x86.At(x86.RAX, chanCapOff), x86.RBX)
	a.MovMI(x86.At(x86.RAX, chanLenOff), 0)
	a.MovMI(x86.At(x86.RAX, chanHeadOff), 0)
	a.MovMI(x86.At(x86.RAX, chanClosedOff), 0)
	a.MovMR(x86.At(x86.RAX, chanElemIsRefOff), x86.R12)
	a.MovMI(x86.At(x86.RAX, chanRecvWaitOff), 0)
	a.MovMR(x86.At(x86.RAX, chanSlotsOff), x86.R13)

	a.MovRM(x86.RCX, x86.At(x86.R15, rtChannelsOff))
	a.MovMR(x86.At(x86.RAX, chanNextOff), x86.RCX)
	a.MovMR(x86.At(x86.R15, rtChannelsOff), x86.RAX)

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

// emitChanSend writes `rt_chan_send(channel rdi, value rsi) -> status rax`.
//
// Two shapes, exactly as the virtual machine has them. A buffered send waits for room and
// leaves the value in the queue. A rendezvous send waits for a receiver to be parked *and*
// the ring to be empty, hands the value over, and then waits again until it has been taken
// -- which is what makes the send complete only when the receive does, rather than merely
// when a receiver exists.
//
// A closed channel refuses at every one of those points, including after a wait: closing
// while a sender is blocked is a real ordering, and returning as though the value had been
// delivered would lose it silently (§12 makes sending on a closed channel a programming
// error, like dividing by zero).
func (e *emitter) emitChanSend() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.chanSend)

	e.runtimePrologue()
	a.MovRR(x86.RBX, x86.RDI)                       // the channel
	a.MovRR(x86.R12, x86.RSI)                       // the value
	a.MovRM(x86.R13, x86.At(x86.R15, rtCurrentOff)) // this thread

	refused := a.NewLabel("send_refused")
	stuck := a.NewLabel("send_stuck")
	buffered := a.NewLabel("send_buffered")
	rendezvous := a.NewLabel("send_rendezvous")
	handOver := a.NewLabel("send_hand_over")
	waitTaken := a.NewLabel("send_wait_taken")
	done := a.NewLabel("send_done")

	a.MovRM(x86.RAX, x86.At(x86.RBX, chanCapOff))
	a.TestRR(x86.RAX, x86.RAX)
	a.Jcc(x86.Equal, rendezvous)

	// Buffered: wait for room.
	a.Bind(buffered)
	a.MovRM(x86.RAX, x86.At(x86.RBX, chanClosedOff))
	a.TestRR(x86.RAX, x86.RAX)
	a.Jcc(x86.NotEqual, refused)
	a.MovRM(x86.RAX, x86.At(x86.RBX, chanLenOff))
	a.MovRM(x86.RCX, x86.At(x86.RBX, chanCapOff))
	a.CmpRR(x86.RAX, x86.RCX)
	a.Jcc(x86.Less, handOver)
	e.blockAndPark(x86.R13, stuck)
	a.Jmp(buffered)

	// Rendezvous: wait for a receiver that has nothing to take yet.
	waitRecv := a.NewLabel("send_wait_receiver")
	a.Bind(rendezvous)
	a.MovRM(x86.RAX, x86.At(x86.RBX, chanClosedOff))
	a.TestRR(x86.RAX, x86.RAX)
	a.Jcc(x86.NotEqual, refused)
	a.MovRM(x86.RAX, x86.At(x86.RBX, chanRecvWaitOff))
	a.TestRR(x86.RAX, x86.RAX)
	a.Jcc(x86.Equal, waitRecv)
	a.MovRM(x86.RAX, x86.At(x86.RBX, chanLenOff))
	a.TestRR(x86.RAX, x86.RAX)
	a.Jcc(x86.Equal, handOver)
	a.Bind(waitRecv)
	e.blockAndPark(x86.R13, stuck)
	a.Jmp(rendezvous)

	a.Bind(handOver)
	e.chanEnqueue(x86.RBX, x86.R12)
	a.Call(e.rt.schedWakeAll)
	a.MovRM(x86.RAX, x86.At(x86.RBX, chanCapOff))
	a.TestRR(x86.RAX, x86.RAX)
	a.Jcc(x86.NotEqual, done)

	// Rendezvous only: not delivered until the receiver has taken it. A close counts as
	// delivered-or-gone; the value stays queued for whoever drains the channel.
	a.Bind(waitTaken)
	a.MovRM(x86.RAX, x86.At(x86.RBX, chanLenOff))
	a.TestRR(x86.RAX, x86.RAX)
	a.Jcc(x86.Equal, done)
	a.MovRM(x86.RAX, x86.At(x86.RBX, chanClosedOff))
	a.TestRR(x86.RAX, x86.RAX)
	a.Jcc(x86.NotEqual, done)
	e.blockAndPark(x86.R13, stuck)
	a.Jmp(waitTaken)

	a.Bind(done)
	a.XorRR(x86.RAX, x86.RAX)
	e.runtimeEpilogue()
	a.Ret()

	a.Bind(refused)
	a.MovRI(x86.RAX, schedRefused)
	e.runtimeEpilogue()
	a.Ret()

	a.Bind(stuck)
	a.MovRI(x86.RAX, schedDeadlock)
	e.runtimeEpilogue()
	a.Ret()
}

// blockAndPark marks this thread as waiting and gives up the processor, jumping to stuck
// when the scheduler reports that nothing else can run (§12's total deadlock).
func (e *emitter) blockAndPark(thread x86.Reg, stuck x86.Label) {
	a := e.a
	a.MovMI(x86.At(thread, tcbBlockedOff), 1)
	a.Call(e.rt.schedPark)
	a.TestRR(x86.RAX, x86.RAX)
	a.Jcc(x86.NotEqual, stuck)
}

// chanEnqueue writes one value into the ring at head+len and lengthens it. The index wraps
// by subtraction rather than division: head is below slots and len is at most slots, so
// their sum is below twice it.
func (e *emitter) chanEnqueue(ch, value x86.Reg) {
	a := e.a
	noWrap := a.NewLabel("enqueue_no_wrap")

	a.MovRM(x86.RAX, x86.At(ch, chanHeadOff))
	a.MovRM(x86.RCX, x86.At(ch, chanLenOff))
	a.AddRR(x86.RAX, x86.RCX)
	a.MovRM(x86.RCX, x86.At(ch, chanSlotsOff))
	a.CmpRR(x86.RAX, x86.RCX)
	a.Jcc(x86.Below, noWrap)
	a.SubRR(x86.RAX, x86.RCX)
	a.Bind(noWrap)

	a.ShlI(x86.RAX, 3)
	a.AddRI(x86.RAX, chanQueueOff)
	a.AddRR(x86.RAX, ch)
	a.MovMR(x86.At(x86.RAX, 0), value)

	a.MovRM(x86.RCX, x86.At(ch, chanLenOff))
	a.AddRI(x86.RCX, 1)
	a.MovMR(x86.At(ch, chanLenOff), x86.RCX)
}

// chanDequeue takes the oldest value out of the ring, leaving it in rax.
func (e *emitter) chanDequeue(ch x86.Reg) {
	a := e.a
	noWrap := a.NewLabel("dequeue_no_wrap")

	a.MovRM(x86.RCX, x86.At(ch, chanHeadOff))
	a.MovRR(x86.RAX, x86.RCX)
	a.ShlI(x86.RAX, 3)
	a.AddRI(x86.RAX, chanQueueOff)
	a.AddRR(x86.RAX, ch)
	a.MovRM(x86.RAX, x86.At(x86.RAX, 0)) // the value

	a.AddRI(x86.RCX, 1)
	a.MovRM(x86.RDX, x86.At(ch, chanSlotsOff))
	a.CmpRR(x86.RCX, x86.RDX)
	a.Jcc(x86.Below, noWrap)
	a.XorRR(x86.RCX, x86.RCX)
	a.Bind(noWrap)
	a.MovMR(x86.At(ch, chanHeadOff), x86.RCX)

	a.MovRM(x86.RCX, x86.At(ch, chanLenOff))
	a.SubRI(x86.RCX, 1)
	a.MovMR(x86.At(ch, chanLenOff), x86.RCX)
}

// emitChanRecv writes `rt_chan_recv(channel rdi) -> status rax, got rdx`.
//
// The value itself does not come back in a register. §12's `recv` is two operations in
// Origin -- "was there one?" and "give it to me" -- so that the prelude can build the
// `Option` the caller sees without the runtime knowing what an `Option` is, and asking
// twice would be a race between two receivers. So the value is taken here, under nobody
// else's nose, and held on this thread until rt_chan_taken reads it.
//
// The waiting count goes up before the wait and down after it, and a wake follows it up:
// a rendezvous sender is waiting for exactly this.
func (e *emitter) emitChanRecv() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.chanRecv)

	e.runtimePrologue()
	a.MovRR(x86.RBX, x86.RDI)
	a.MovRM(x86.R13, x86.At(x86.R15, rtCurrentOff))

	wait := a.NewLabel("recv_wait")
	take := a.NewLabel("recv_take")
	empty := a.NewLabel("recv_empty")
	stuck := a.NewLabel("recv_stuck")

	a.MovRM(x86.RAX, x86.At(x86.RBX, chanRecvWaitOff))
	a.AddRI(x86.RAX, 1)
	a.MovMR(x86.At(x86.RBX, chanRecvWaitOff), x86.RAX)
	a.Call(e.rt.schedWakeAll)

	a.Bind(wait)
	a.MovRM(x86.RAX, x86.At(x86.RBX, chanLenOff))
	a.TestRR(x86.RAX, x86.RAX)
	a.Jcc(x86.NotEqual, take)
	a.MovRM(x86.RAX, x86.At(x86.RBX, chanClosedOff))
	a.TestRR(x86.RAX, x86.RAX)
	a.Jcc(x86.NotEqual, empty)
	e.blockAndPark(x86.R13, stuck)
	a.Jmp(wait)

	a.Bind(take)
	a.MovRM(x86.RAX, x86.At(x86.RBX, chanRecvWaitOff))
	a.SubRI(x86.RAX, 1)
	a.MovMR(x86.At(x86.RBX, chanRecvWaitOff), x86.RAX)

	e.chanDequeue(x86.RBX)
	a.MovMR(x86.At(x86.R13, tcbTakenOff), x86.RAX)
	a.MovRM(x86.RAX, x86.At(x86.RBX, chanElemIsRefOff))
	a.AddRI(x86.RAX, 1) // 1 = a raw value is held, 2 = a reference is
	a.MovMR(x86.At(x86.R13, tcbTakenIsRefOff), x86.RAX)
	a.Call(e.rt.schedWakeAll)

	a.XorRR(x86.RAX, x86.RAX)
	a.MovRI(x86.RDX, 1)
	e.runtimeEpilogue()
	a.Ret()

	a.Bind(empty)
	a.MovRM(x86.RAX, x86.At(x86.RBX, chanRecvWaitOff))
	a.SubRI(x86.RAX, 1)
	a.MovMR(x86.At(x86.RBX, chanRecvWaitOff), x86.RAX)
	a.XorRR(x86.RAX, x86.RAX)
	a.XorRR(x86.RDX, x86.RDX)
	e.runtimeEpilogue()
	a.Ret()

	a.Bind(stuck)
	a.MovRM(x86.RAX, x86.At(x86.RBX, chanRecvWaitOff))
	a.SubRI(x86.RAX, 1)
	a.MovMR(x86.At(x86.RBX, chanRecvWaitOff), x86.RAX)
	a.MovRI(x86.RAX, schedDeadlock)
	a.XorRR(x86.RDX, x86.RDX)
	e.runtimeEpilogue()
	a.Ret()
}

// emitChanTaken writes `rt_chan_taken() -> status rax, value rdx`: the value this thread's
// own last receive took. It is cleared on the way out, so the slot never keeps a reference
// alive past the moment the program reads it.
func (e *emitter) emitChanTaken() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.chanTaken)

	refused := a.NewLabel("taken_refused")
	a.MovRM(x86.RCX, x86.At(x86.R15, rtCurrentOff))
	a.MovRM(x86.RAX, x86.At(x86.RCX, tcbTakenIsRefOff))
	a.TestRR(x86.RAX, x86.RAX)
	a.Jcc(x86.Equal, refused)

	a.MovRM(x86.RDX, x86.At(x86.RCX, tcbTakenOff))
	a.MovMI(x86.At(x86.RCX, tcbTakenOff), 0)
	a.MovMI(x86.At(x86.RCX, tcbTakenIsRefOff), 0)
	a.XorRR(x86.RAX, x86.RAX)
	a.Ret()

	a.Bind(refused)
	a.MovRI(x86.RAX, schedRefused)
	a.XorRR(x86.RDX, x86.RDX)
	a.Ret()
}

// emitChanClose writes `rt_chan_close(channel rdi) -> status rax`.
//
// Queued values stay receivable; what closing ends is the possibility of more (§12). Only a
// sender may close, which the type system enforces rather than this: `close` is a method on
// `Sender[T]`, and a receiver has no way to name it.
func (e *emitter) emitChanClose() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.chanClose)

	e.runtimePrologue()
	refused := a.NewLabel("close_refused")

	a.MovRM(x86.RAX, x86.At(x86.RDI, chanClosedOff))
	a.TestRR(x86.RAX, x86.RAX)
	a.Jcc(x86.NotEqual, refused)
	a.MovMI(x86.At(x86.RDI, chanClosedOff), 1)
	a.Call(e.rt.schedWakeAll)
	a.XorRR(x86.RAX, x86.RAX)
	e.runtimeEpilogue()
	a.Ret()

	a.Bind(refused)
	a.MovRI(x86.RAX, schedRefused)
	e.runtimeEpilogue()
	a.Ret()
}
