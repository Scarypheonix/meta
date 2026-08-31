package interp

import (
	"sync"

	"github.com/scarypheonix/meta/internal/diag"
)

// The interpreter's concurrency runtime (spec/12-concurrency.md).
//
// Green threads are goroutines. That is not a shortcut around §08's M:N scheduler --
// Go's own scheduler *is* M:N, and it preempts, which is what §08 asks for. What the
// interpreter must supply on top is the part that is Origin's semantics rather than the
// host's: rendezvous channels, a mutex that traps rather than deadlocking on re-entry,
// total-deadlock detection, and ADR-0026's rule that a trap anywhere ends the process.
//
// One mutex and one condition variable serve every blocking operation. A broadcast on
// every state change wakes more threads than strictly necessary, which for programs of
// this size costs nothing and buys the thing that matters: every wait and every wake
// happens under one lock, so "are all threads blocked?" is a question with an exact
// answer rather than a racy estimate.
type runtime struct {
	mu   sync.Mutex
	cond *sync.Cond

	// live counts threads that have not finished, main included. blocked counts those
	// parked in wait. When they are equal, nothing can ever make progress again.
	live    int
	blocked int

	// trap is the first trap raised by any thread. Once set the process is ending, so
	// every parked thread is woken to unwind (ADR-0026).
	trap *Trap

	threads  map[int64]*threadState
	channels map[int64]*channelState
	mutexes  map[int64]*mutexState
	next     int64

	// waiters holds each parked thread's own condition. Counting parked threads is not
	// enough to recognize deadlock: a thread that has been signalled but has not yet
	// re-acquired the lock is still parked, so a perfectly ordinary handover -- a
	// rendezvous sender waiting for its value to be taken while the receiver has already
	// been woken to take it -- would look exactly like every thread being stuck. What
	// distinguishes the two is whether any parked thread's condition already holds.
	waiters    map[int64]func() bool
	nextWaiter int64

	// nextTid numbers threads. Ids are their own space rather than shared with handles,
	// and main is 1, so that a mutex's `owner == 0` unambiguously means free -- with main
	// at 0 it would read as main holding every unlocked lock.
	nextTid int64

	// out serializes writes to stdout, so two threads printing cannot interleave within
	// one line.
	out sync.Mutex

	// wg tracks spawned threads, so the process can outlive `main` returning exactly as
	// long as a thread is still running (spec/12-concurrency.md).
	wg sync.WaitGroup
}

type threadState struct {
	done   bool
	result Value
	joined bool
}

type channelState struct {
	capacity int
	queue    []Value
	closed   bool
	// waitingReceivers is what a rendezvous send waits for: with no buffer, a value may
	// not be handed over until someone is there to take it.
	waitingReceivers int
}

type mutexState struct {
	value Value
	// owner is the thread holding the lock, or 0 when free. Thread ids start at 1 so
	// that zero is unambiguous.
	owner int64
}

// dying unwinds a thread whose process is already ending because another thread trapped.
// It is not a trap itself and produces no output: the trap that started it has already
// been recorded, and reporting a second one would invent a failure that did not happen.
type dying struct{}

func newRuntime() *runtime {
	r := &runtime{
		threads:  map[int64]*threadState{},
		channels: map[int64]*channelState{},
		mutexes:  map[int64]*mutexState{},
		waiters:  map[int64]func() bool{},
		live:     1, // main
		next:     1,
		nextTid:  2, // main is 1
	}
	r.cond = sync.NewCond(&r.mu)
	return r
}

func (r *runtime) handle() int64 {
	h := r.next
	r.next++
	return h
}

// fail records the first trap and wakes every parked thread so it can unwind. Called with
// r.mu held.
func (r *runtime) fail(t *Trap) {
	if r.trap == nil {
		r.trap = t
	}
	r.cond.Broadcast()
}

// wait parks the calling thread until ready() holds, and is where deadlock is detected.
//
// Called with r.mu held; returns with it held. It panics with dying{} if the process is
// ending, and traps if every thread in the program is parked -- at which point no state
// can change again, so waiting longer is waiting forever.
func (r *runtime) wait(span diag.Span, ready func() bool) {
	id := r.nextWaiter
	r.nextWaiter++
	defer delete(r.waiters, id)

	for !ready() {
		if r.trap != nil {
			panic(dying{})
		}
		r.waiters[id] = ready
		r.blocked++
		if r.blocked == r.live && !r.anyReady() {
			// Every thread is parked and not one of their conditions holds, so nothing
			// can change and no wake is coming. Reported against the operation that
			// completed the cycle, which is as good a place as any.
			r.blocked--
			t := &Trap{Msg: "all threads are blocked", Span: span}
			r.fail(t)
			panic(dying{})
		}
		r.cond.Wait()
		r.blocked--
		delete(r.waiters, id)
		if r.trap != nil {
			panic(dying{})
		}
	}
}

// anyReady reports whether some parked thread's condition already holds, meaning a wake
// is on its way and the program is not stuck.
func (r *runtime) anyReady() bool {
	for _, ready := range r.waiters {
		if ready() {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Threads
// ---------------------------------------------------------------------------

// spawn starts body on a new green thread and returns its handle.
func (in *Interp) spawn(body *Closure, span diag.Span) int64 {
	r := in.rt
	r.mu.Lock()
	h := r.handle()
	tid := r.nextTid
	r.nextTid++
	r.threads[h] = &threadState{}
	r.live++
	r.mu.Unlock()

	// A thread gets its own frames and its own depth -- the call stack is the one piece
	// of interpreter state that is per-thread. Everything else, the heap included, is
	// shared by construction, which is exactly what `Send` exists to make safe.
	child := &Interp{
		res: in.res, stdout: in.stdout, stderr: in.stderr,
		maxDepth: in.maxDepth, rt: r, tid: tid,
	}

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer func() {
			if rec := recover(); rec != nil {
				r.mu.Lock()
				if t, isTrap := rec.(*Trap); isTrap {
					// ADR-0026: a trap in any thread ends the process. Recording it here
					// and letting the main thread report it keeps one output path.
					r.fail(t)
				} else if _, isDying := rec.(dying); !isDying {
					r.mu.Unlock()
					panic(rec) // a real bug in the interpreter, not a program's trap
				}
				r.live--
				r.cond.Broadcast()
				r.mu.Unlock()
				return
			}
		}()

		result := child.callClosure(body, nil, span)

		r.mu.Lock()
		st := r.threads[h]
		st.done, st.result = true, result
		r.live--
		r.cond.Broadcast()
		r.mu.Unlock()
	}()
	return h
}

// join blocks until the thread finishes and yields its result.
func (in *Interp) join(h int64, span diag.Span) Value {
	span = in.userSpan(span)
	r := in.rt
	r.mu.Lock()
	defer r.mu.Unlock()

	st := r.threads[h]
	if st == nil {
		in.trap(span, "join of a thread that does not exist")
	}
	if st.joined {
		in.trap(span, "handle already joined")
	}
	r.wait(span, func() bool { return st.done })
	st.joined = true
	return st.result
}

// ---------------------------------------------------------------------------
// Channels
// ---------------------------------------------------------------------------

func (in *Interp) makeChannel(capacity int64, span diag.Span) int64 {
	span = in.userSpan(span)
	if capacity < 0 {
		in.trap(span, "channel capacity is negative")
	}
	r := in.rt
	r.mu.Lock()
	defer r.mu.Unlock()
	h := r.handle()
	r.channels[h] = &channelState{capacity: int(capacity)}
	return h
}

func (in *Interp) channelSend(h int64, v Value, span diag.Span) {
	span = in.userSpan(span)
	r := in.rt
	r.mu.Lock()
	defer r.mu.Unlock()

	ch := r.channels[h]
	if ch == nil {
		in.trap(span, "send on a channel that does not exist")
	}
	if ch.closed {
		in.trap(span, "send on a closed channel")
	}

	if ch.capacity == 0 {
		// Rendezvous: hand the value over only when someone is waiting to take it, and
		// do not return until they have.
		r.wait(span, func() bool { return ch.closed || (ch.waitingReceivers > 0 && len(ch.queue) == 0) })
		if ch.closed {
			in.trap(span, "send on a closed channel")
		}
		ch.queue = append(ch.queue, v)
		r.cond.Broadcast()
		r.wait(span, func() bool { return ch.closed || len(ch.queue) == 0 })
		return
	}

	r.wait(span, func() bool { return ch.closed || len(ch.queue) < ch.capacity })
	if ch.closed {
		in.trap(span, "send on a closed channel")
	}
	ch.queue = append(ch.queue, v)
	r.cond.Broadcast()
}

// channelRecv returns the value received and whether the channel yielded one at all. A
// false second result is `None`: the channel is closed and drained, permanently.
func (in *Interp) channelRecv(h int64, span diag.Span) (Value, bool) {
	span = in.userSpan(span)
	r := in.rt
	r.mu.Lock()
	defer r.mu.Unlock()

	ch := r.channels[h]
	if ch == nil {
		in.trap(span, "receive on a channel that does not exist")
	}

	ch.waitingReceivers++
	r.cond.Broadcast() // a rendezvous sender is waiting for exactly this
	r.wait(span, func() bool { return len(ch.queue) > 0 || ch.closed })
	ch.waitingReceivers--

	if len(ch.queue) > 0 {
		v := ch.queue[0]
		ch.queue = ch.queue[1:]
		r.cond.Broadcast() // a blocked sender may now fit, or its handover completed
		return v, true
	}
	return nil, false
}

func (in *Interp) channelClose(h int64, span diag.Span) {
	span = in.userSpan(span)
	r := in.rt
	r.mu.Lock()
	defer r.mu.Unlock()

	ch := r.channels[h]
	if ch == nil {
		in.trap(span, "close of a channel that does not exist")
	}
	if ch.closed {
		in.trap(span, "channel already closed")
	}
	ch.closed = true
	r.cond.Broadcast()
}

// ---------------------------------------------------------------------------
// Mutex
// ---------------------------------------------------------------------------

func (in *Interp) makeMutex(v Value) int64 {
	r := in.rt
	r.mu.Lock()
	defer r.mu.Unlock()
	h := r.handle()
	r.mutexes[h] = &mutexState{value: v}
	return h
}

// withLock acquires the lock, runs body on the guarded value, and releases it.
//
// The lock is released even when body traps, which matters less than it looks: a trap
// ends the process (ADR-0026), so there is no survivor to hand a corrupted value to. It
// is done anyway because leaving the lock held would turn one trap into a deadlock
// report from another thread, and the first trap is the true one.
func (in *Interp) withLock(h int64, body *Closure, span diag.Span) Value {
	span = in.userSpan(span)
	r := in.rt

	// Acquire under the lock, then release it: Origin code must never run while the
	// runtime's own mutex is held, or a thread calling `send` from inside `with` would
	// block every other thread rather than just itself.
	m := func() *mutexState {
		r.mu.Lock()
		defer r.mu.Unlock()
		m := r.mutexes[h]
		if m == nil {
			in.trap(span, "lock of a mutex that does not exist")
		}
		if m.owner == in.tid {
			in.trap(span, "mutex re-entered by the same thread")
		}
		r.wait(span, func() bool { return m.owner == 0 })
		m.owner = in.tid
		return m
	}()

	defer func() {
		r.mu.Lock()
		m.owner = 0
		r.cond.Broadcast()
		r.mu.Unlock()
	}()

	return in.callClosure(body, []Value{m.value}, span)
}

// ---------------------------------------------------------------------------
// The builtins the prelude's methods are written in terms of
// ---------------------------------------------------------------------------

// preludeStruct wraps a runtime handle in one of the prelude's handle types.
//
// The interpreter walks the AST, so unlike the compiled engines it never passes through
// internal/compile's wrapConcurrencyHandle and has to do this itself. It can: it has the
// declarations in hand. What matters for the design is the compiled path, where the
// runtime must not invent a type -- the native backend could not.
func (in *Interp) preludeStruct(name string, h int64, span diag.Span) Value {
	def := in.res.Structs[name]
	if def == nil {
		in.trap(span, "the prelude does not define `%s`", name)
	}
	return &Struct{Def: def, Vals: []Value{Int(h)}}
}

// handleOf reads the runtime handle out of one of the prelude's handle structs.
//
// A program can forge one -- field visibility does not currently prevent writing
// `Sender[i64] { handle: 99 }` -- so every operation looks the handle up rather than
// trusting it, and traps when the runtime does not know it. That keeps a forged handle a
// defined error instead of undefined behaviour (§04).
func (in *Interp) handleOf(v Value, what string, span diag.Span) int64 {
	s, ok := v.(*Struct)
	if !ok || len(s.Vals) != 1 {
		in.trap(span, "`%s` expects a %s", what, what)
	}
	n, ok := s.Vals[0].(Int)
	if !ok {
		in.trap(span, "`%s` expects a %s", what, what)
	}
	return int64(n)
}

// concurrencyBuiltin dispatches the std::thread, std::chan and std::sync operations. It
// reports whether it handled the name, so the ordinary builtin path can carry on.
//
// Each returns a bare handle or a bare value. Wrapping one into `JoinHandle[T]` or
// `Option[T]` is the compiler's job or the prelude's, never the runtime's -- see
// internal/compile's wrapConcurrencyHandle for why.
func (in *Interp) concurrencyBuiltin(name string, args []Value, span diag.Span) (Value, bool) {
	switch name {
	case "thread::spawn":
		body, ok := args[0].(*Closure)
		if !ok {
			in.trap(span, "`spawn` takes a function value")
		}
		return in.preludeStruct("JoinHandle", in.spawn(body, span), span), true

	case "thread::join_thread":
		return in.join(in.handleOf(args[0], "JoinHandle", span), span), true

	case "chan::channel":
		n, ok := args[0].(Int)
		if !ok {
			in.trap(span, "`channel` takes an `i64` capacity")
		}
		h := in.makeChannel(int64(n), span)
		// Both ends name the same queue; they are separate types so that a receiver
		// cannot send and a sender cannot close what it does not own.
		return &Tuple{Elems: []Value{
			in.preludeStruct("Sender", h, span),
			in.preludeStruct("Receiver", h, span),
		}}, true

	case "chan::send_value":
		in.channelSend(in.handleOf(args[0], "Sender", span), args[1], span)
		return Unit{}, true

	case "chan::await_value":
		v, got := in.channelRecv(in.handleOf(args[0], "Receiver", span), span)
		// The value is held for this thread until `taken_value` reads it, so that the
		// prelude's `recv` can build `Option` in Origin without another receiver taking
		// what this one dequeued.
		in.taken = v
		return Bool(got), true

	case "chan::taken_value":
		v := in.taken
		if v == nil {
			in.trap(span, "no value was taken from this channel")
		}
		in.taken = nil
		return v, true

	case "chan::close_sender":
		in.channelClose(in.handleOf(args[0], "Sender", span), span)
		return Unit{}, true

	case "sync::mutex":
		return in.preludeStruct("Mutex", in.makeMutex(args[0]), span), true

	case "sync::with_lock":
		body, ok := args[1].(*Closure)
		if !ok {
			in.trap(span, "`with` takes a function value")
		}
		return in.withLock(in.handleOf(args[0], "Mutex", span), body, span), true
	}
	return nil, false
}
