package vm

import (
	"sync"

	"github.com/scarypheonix/meta/internal/compile"

	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/layout"
)

// The virtual machine's concurrency runtime (spec/12-concurrency.md).
//
// The interpreter could hand this problem to the host: its values are Go values and Go's
// collector finds them wherever they are. The virtual machine cannot. It owns a precise,
// *moving* collector (internal/gc), and that changes what threads mean:
//
//   - every thread's operand stack, frames and temporaries are a root set, so the
//     collector must see all of them, not just the one that happened to allocate;
//   - an object's address changes when it is evacuated, so no thread may be halfway
//     through reading one while another triggers a collection.
//
// So threads here interleave rather than run in parallel: a thread holds the world lock
// while it executes bytecode and releases it at safepoints -- which is exactly the
// scheduling §08 specifies ("preemptive at safepoints"), and exactly what makes a
// collection safe, since a thread that is not holding the lock is by definition between
// instructions with a consistent stack.
//
// Parallelism is not what the specification promises, and it is not what the differential
// requires: a program whose output depends on which thread wins a race is not a valid
// case in the first place (spec/12-concurrency.md's determinism clause). Real parallelism
// is recorded in docs/deferred.md rather than pretended at.
type world struct {
	// exec is the world lock. Held while a thread executes; released at every safepoint.
	exec sync.Mutex

	mu   sync.Mutex
	cond *sync.Cond

	live    int
	blocked int
	trap    *Trap

	// exiting and exitCode are `process::exit` from any thread. It ends the process the
	// way a trap does -- every parked thread is woken to unwind -- and differs only in
	// what is reported at the end: a status, and nothing on stderr (spec/17-process.md).
	exiting  bool
	exitCode int

	threads  map[int64]*vmThread
	channels map[int64]*vmChannel
	mutexes  map[int64]*vmMutex
	next     int64

	// waiters holds each parked thread's condition, so deadlock means "every thread is
	// parked and not one of their conditions holds" rather than merely "every thread is
	// parked" -- a thread signalled but not yet rescheduled is still parked, and
	// counting alone mistakes an ordinary handover for a stuck program.
	waiters    map[int64]func() bool
	nextWaiter int64
	nextTid    int64

	// wg tracks spawned threads, so `main` returning does not end the program while one
	// is still running (spec/12-concurrency.md).
	wg sync.WaitGroup

	// vms is every live thread's machine, so the collector can visit all of their roots.
	// A thread that has finished is removed: its stack holds nothing the program can
	// still reach.
	vms map[int64]*VM
}

type vmThread struct {
	done   bool
	result Value
	joined bool
}

type vmChannel struct {
	capacity         int
	queue            []Value
	closed           bool
	waitingReceivers int
}

type vmMutex struct {
	value Value
	owner int64
}

// dying unwinds a thread whose process is ending because another thread trapped
// (ADR-0026). It carries no message: the trap that caused it is already recorded.
type dying struct{}

func newWorld() *world {
	w := &world{
		threads:  map[int64]*vmThread{},
		channels: map[int64]*vmChannel{},
		mutexes:  map[int64]*vmMutex{},
		waiters:  map[int64]func() bool{},
		vms:      map[int64]*VM{},
		live:     1,
		next:     1,
		nextTid:  2, // main is 1, and 0 means "no owner" for a mutex
	}
	w.cond = sync.NewCond(&w.mu)
	return w
}

func (w *world) handle() int64 {
	h := w.next
	w.next++
	return h
}

func (w *world) fail(t *Trap) {
	if w.trap == nil {
		w.trap = t
	}
	w.cond.Broadcast()
}

// requestExit records the first `process::exit` and wakes every parked thread, exactly as
// fail does for a trap (spec/17-process.md).
func (w *world) requestExit(code int) {
	if !w.exiting && w.trap == nil {
		w.exiting, w.exitCode = true, code
	}
	w.cond.Broadcast()
}

// ending reports whether the process is already on its way out, by a trap in some thread
// or by `process::exit`. Called with w.mu held.
func (w *world) ending() bool { return w.trap != nil || w.exiting }

// wait parks the calling thread until ready() holds.
//
// The world lock is released for the duration: a parked thread is not executing, so it
// must not keep every other thread -- or a collection -- waiting on it. Called with w.mu
// held, and returns with it held.
func (w *world) wait(v *VM, span diag.Span, ready func() bool) {
	id := w.nextWaiter
	w.nextWaiter++
	defer delete(w.waiters, id)

	for !ready() {
		if w.ending() {
			panic(dying{})
		}
		w.waiters[id] = ready
		w.blocked++
		if w.blocked == w.live && !w.anyReady() {
			w.blocked--
			w.fail(&Trap{Msg: "all threads are blocked", Span: span})
			panic(dying{})
		}

		// Leave the world while parked, and take it back on the way out. The order
		// matters: the world lock is released before sleeping and re-acquired after
		// waking, and w.mu is what makes the pair atomic with respect to the wake.
		w.exec.Unlock()
		w.cond.Wait()
		w.blocked--
		delete(w.waiters, id)
		w.mu.Unlock()
		w.exec.Lock()
		w.mu.Lock()

		if w.ending() {
			panic(dying{})
		}
	}
}

func (w *world) anyReady() bool {
	for _, ready := range w.waiters {
		if ready() {
			return true
		}
	}
	return false
}

// safepoint yields the world to any thread waiting for it. §08 places one at every
// function entry, loop back edge and allocation, which is what stops a compute loop
// from starving the scheduler.
func (v *VM) safepoint() {
	if v.w == nil || v.w.singleThreaded() {
		return
	}
	v.w.exec.Unlock()
	v.w.exec.Lock()
	v.w.mu.Lock()
	fatal := v.w.ending()
	v.w.mu.Unlock()
	if fatal {
		panic(dying{})
	}
}

// singleThreaded reports whether the program has never spawned, in which case every
// safepoint is a pair of uncontended lock operations that buys nothing. The overwhelming
// majority of programs never spawn at all, and they should not pay for this.
func (w *world) singleThreaded() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.nextTid == 2 && w.live == 1
}

// visitWorldRoots hands the collector every thread's roots, not just the one that
// allocated. A missed root is an object freed while it is still reachable; a stale one is
// a reference the collector fails to rewrite when the object moves.
func (w *world) visitWorldRoots(visit func(*layout.Ref)) {
	for _, vm := range w.vms {
		vm.visitOwnRoots(visit)
		// The string cache is shared between threads, so it is visited once here rather
		// than once per machine; visiting it twice would be harmless but pointless.
	}
	if main := w.vms[1]; main != nil {
		for k := range main.strings {
			r := main.strings[k]
			if r != layout.Nil {
				visit(&r)
				main.strings[k] = r
			}
		}
	}
	for h := range w.channels {
		ch := w.channels[h]
		for i := range ch.queue {
			if ch.queue[i].Tag == layout.TagRef && ch.queue[i].R != layout.Nil {
				visit(&ch.queue[i].R)
			}
		}
	}
	for h := range w.mutexes {
		m := w.mutexes[h]
		if m.value.Tag == layout.TagRef && m.value.R != layout.Nil {
			visit(&m.value.R)
		}
	}
	for h := range w.threads {
		t := w.threads[h]
		if t.result.Tag == layout.TagRef && t.result.R != layout.Nil {
			visit(&t.result.R)
		}
	}
}

// ---------------------------------------------------------------------------
// Operations
// ---------------------------------------------------------------------------

// spawn starts a closure on a new thread. The child gets its own stack, frames and
// temporaries -- the execution state -- and shares everything else, the heap included.
func (v *VM) spawn(body Value, span diag.Span) int64 {
	w := v.w
	if body.Tag != layout.TagRef && body.Tag != layout.TagFn {
		v.trap(span, "`spawn` takes a function value")
	}

	w.mu.Lock()
	h := w.handle()
	tid := w.nextTid
	w.nextTid++
	w.threads[h] = &vmThread{}
	w.live++

	child := &VM{
		prog: v.prog, heap: v.heap, stdout: v.stdout, stderr: v.stderr,
		stack: make([]Value, 0, 256), strings: v.strings,
		maxFrames: v.maxFrames, args: v.args, w: w, tid: tid,
	}
	// Registered before the goroutine starts, so a collection triggered by any thread
	// between now and its first instruction still sees the closure it is about to call.
	child.temps = append(child.temps, body)
	w.vms[tid] = child
	w.mu.Unlock()

	w.wg.Add(1)
	go func() {
		defer w.wg.Done()

		// A new thread must hold the world before it touches anything.
		w.exec.Lock()
		defer func() {
			rec := recover()
			w.mu.Lock()
			if rec != nil {
				if t, isTrap := rec.(*Trap); isTrap {
					w.fail(t) // ADR-0026: any thread's trap ends the process
				} else if e, requested := rec.(exitRequest); requested {
					// `process::exit` ends the process, not the thread that called it
					// (spec/17-process.md): the same road as a trap, and only what main
					// reports at the end differs.
					w.requestExit(e.code)
				} else if _, unwinding := rec.(dying); !unwinding {
					w.mu.Unlock()
					w.exec.Unlock()
					panic(rec) // a bug in the VM, not a program's trap
				}
			}
			delete(w.vms, tid)
			w.live--
			w.cond.Broadcast()
			w.mu.Unlock()
			w.exec.Unlock()
		}()

		result := child.callValue(body, span)

		w.mu.Lock()
		st := w.threads[h]
		st.done, st.result = true, result
		w.mu.Unlock()
	}()
	return h
}

func (v *VM) join(h int64, span diag.Span) Value {
	span = v.userSpan(span)
	w := v.w
	w.mu.Lock()
	defer w.mu.Unlock()

	st := w.threads[h]
	if st == nil {
		v.trap(span, "join of a thread that does not exist")
	}
	if st.joined {
		v.trap(span, "handle already joined")
	}
	w.wait(v, span, func() bool { return st.done })
	st.joined = true
	return st.result
}

func (v *VM) makeChannel(capacity int64, span diag.Span) int64 {
	span = v.userSpan(span)
	if capacity < 0 {
		v.trap(span, "channel capacity is negative")
	}
	w := v.w
	w.mu.Lock()
	defer w.mu.Unlock()
	h := w.handle()
	w.channels[h] = &vmChannel{capacity: int(capacity)}
	return h
}

func (v *VM) channelSend(h int64, val Value, span diag.Span) {
	span = v.userSpan(span)
	w := v.w
	w.mu.Lock()
	defer w.mu.Unlock()

	ch := w.channels[h]
	if ch == nil {
		v.trap(span, "send on a channel that does not exist")
	}
	if ch.closed {
		v.trap(span, "send on a closed channel")
	}

	if ch.capacity == 0 {
		// Rendezvous: hand over only when a receiver is there to take it, and do not
		// return until it has been taken.
		w.wait(v, span, func() bool {
			return ch.closed || (ch.waitingReceivers > 0 && len(ch.queue) == 0)
		})
		if ch.closed {
			v.trap(span, "send on a closed channel")
		}
		ch.queue = append(ch.queue, val)
		w.cond.Broadcast()
		w.wait(v, span, func() bool { return ch.closed || len(ch.queue) == 0 })
		return
	}

	w.wait(v, span, func() bool { return ch.closed || len(ch.queue) < ch.capacity })
	if ch.closed {
		v.trap(span, "send on a closed channel")
	}
	ch.queue = append(ch.queue, val)
	w.cond.Broadcast()
}

func (v *VM) channelRecv(h int64, span diag.Span) (Value, bool) {
	span = v.userSpan(span)
	w := v.w
	w.mu.Lock()
	defer w.mu.Unlock()

	ch := w.channels[h]
	if ch == nil {
		v.trap(span, "receive on a channel that does not exist")
	}

	ch.waitingReceivers++
	w.cond.Broadcast()
	w.wait(v, span, func() bool { return len(ch.queue) > 0 || ch.closed })
	ch.waitingReceivers--

	if len(ch.queue) > 0 {
		val := ch.queue[0]
		ch.queue = ch.queue[1:]
		w.cond.Broadcast()
		return val, true
	}
	return Value{}, false
}

func (v *VM) channelClose(h int64, span diag.Span) {
	span = v.userSpan(span)
	w := v.w
	w.mu.Lock()
	defer w.mu.Unlock()

	ch := w.channels[h]
	if ch == nil {
		v.trap(span, "close of a channel that does not exist")
	}
	if ch.closed {
		v.trap(span, "channel already closed")
	}
	ch.closed = true
	w.cond.Broadcast()
}

func (v *VM) makeMutex(val Value) int64 {
	w := v.w
	w.mu.Lock()
	defer w.mu.Unlock()
	h := w.handle()
	w.mutexes[h] = &vmMutex{value: val}
	return h
}

func (v *VM) withLock(h int64, body Value, span diag.Span) Value {
	span = v.userSpan(span)
	w := v.w

	m := func() *vmMutex {
		w.mu.Lock()
		defer w.mu.Unlock()
		m := w.mutexes[h]
		if m == nil {
			v.trap(span, "lock of a mutex that does not exist")
		}
		if m.owner == v.tid {
			v.trap(span, "mutex re-entered by the same thread")
		}
		w.wait(v, span, func() bool { return m.owner == 0 })
		m.owner = v.tid
		return m
	}()

	defer func() {
		w.mu.Lock()
		m.owner = 0
		w.cond.Broadcast()
		w.mu.Unlock()
	}()

	// The guarded value is pushed as the body's argument. It is read fresh here rather
	// than captured above, because a collection between acquiring the lock and calling
	// the body may have moved it.
	w.mu.Lock()
	guarded := m.value
	w.mu.Unlock()
	return v.callValue(body, span, guarded)
}

// ---------------------------------------------------------------------------
// Dispatch
// ---------------------------------------------------------------------------

// handleOf reads the runtime handle out of one of the prelude's handle structs. Every
// operation looks the handle up rather than trusting it, so a forged one is a defined
// error rather than undefined behaviour (§04).
func (v *VM) handleOf(val Value, what string, span diag.Span) int64 {
	if val.Tag != layout.TagRef || val.R == layout.Nil {
		v.trap(span, "this operation expects a %s", what)
	}
	desc := v.prog.Types.Get(v.heap.TypeOf(val.R))
	if desc.Words < 1 {
		v.trap(span, "this operation expects a %s", what)
	}
	return int64(v.heap.Get(val.R, 0))
}

// concurrencyBuiltin dispatches the concurrency operations, and reports whether it
// handled the index. Each yields a bare handle or a bare value; internal/compile wraps
// one into the prelude type the call's checked type names.
func (v *VM) concurrencyBuiltin(index int, args []Value, span diag.Span) (Value, bool) {
	switch index {
	case compile.BuiltinSpawn:
		return intVal(v.spawn(args[0], span)), true

	case compile.BuiltinJoin:
		return v.join(v.handleOf(args[0], "JoinHandle", span), span), true

	case compile.BuiltinChannel:
		return intVal(v.makeChannel(int64(args[0].N), span)), true

	case compile.BuiltinSend:
		v.channelSend(v.handleOf(args[0], "Sender", span), args[1], span)
		return unitVal(), true

	case compile.BuiltinAwait:
		val, got := v.channelRecv(v.handleOf(args[0], "Receiver", span), span)
		// Held for this thread until BuiltinTaken reads it, so the prelude's `recv` can
		// build `Option` in Origin without racing another receiver.
		v.taken = val
		v.hasTaken = got
		return boolVal(got), true

	case compile.BuiltinTaken:
		if !v.hasTaken {
			v.trap(span, "no value was taken from this channel")
		}
		val := v.taken
		v.taken, v.hasTaken = Value{}, false
		return val, true

	case compile.BuiltinCloseChan:
		v.channelClose(v.handleOf(args[0], "Sender", span), span)
		return unitVal(), true

	case compile.BuiltinMutex:
		return intVal(v.makeMutex(args[0])), true

	case compile.BuiltinWithLock:
		return v.withLock(v.handleOf(args[0], "Mutex", span), args[1], span), true
	}
	return Value{}, false
}
