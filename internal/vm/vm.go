// Package vm executes Origin bytecode against the garbage-collected heap.
//
// Its contract with the tree-walking interpreter is exact: the same program must produce
// byte-identical stdout, byte-identical stderr and the same exit status. Phase 3's exit
// criterion is that differential, and `tests/e2e` runs the whole corpus through both.
//
// Every value on the stack carries a tag, so the collector finds roots precisely: a
// frame is a slice of tagged values, and the root visitor hands the collector the
// address of each reference slot to rewrite in place.
package vm

import (
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/scarypheonix/meta/internal/bytecode"
	"github.com/scarypheonix/meta/internal/compile"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/gc"
	"github.com/scarypheonix/meta/internal/layout"
	"github.com/scarypheonix/meta/internal/prelude"
)

// TrapExitCode is the process exit status after a trap (spec/04-expressions.md).
const TrapExitCode = 101

// Trap is a runtime trap. It matches the interpreter's exactly, because the two must
// print the same thing.
type Trap struct {
	Msg  string
	Span diag.Span
}

func (t *Trap) Error() string { return fmt.Sprintf("origin: %s at %s", t.Msg, t.Span) }

// Value is a tagged runtime value.
type Value struct {
	Tag layout.ValueTag
	// N holds a primitive: an integer's two's-complement bits, a float's IEEE bits, a
	// bool's 0 or 1, a char's scalar value, or a function index.
	N uint64
	// R is the heap reference when Tag is TagRef.
	R layout.Ref
}

func intVal(v int64) Value     { return Value{Tag: layout.TagInt, N: uint64(v)} }
func floatVal(f float64) Value { return Value{Tag: layout.TagFloat, N: math.Float64bits(f)} }
func boolVal(b bool) Value {
	var n uint64
	if b {
		n = 1
	}
	return Value{Tag: layout.TagBool, N: n}
}
func charVal(r rune) Value      { return Value{Tag: layout.TagChar, N: uint64(r)} }
func refVal(r layout.Ref) Value { return Value{Tag: layout.TagRef, R: r} }
func unitVal() Value            { return Value{Tag: layout.TagUnit} }

func (v Value) Int() int64     { return int64(v.N) }
func (v Value) Float() float64 { return math.Float64frombits(v.N) }
func (v Value) Bool() bool     { return v.N != 0 }

// frame is one function activation.
type frame struct {
	fn      *bytecode.Fn
	fnIndex int
	pc      int
	// base is where this frame's locals start in the value stack.
	base int
	// closure is the object holding this call's captures, or Nil for a plain function.
	closure layout.Ref
	// retSpan is where the call came from, for a trap inside a builtin.
	retSpan diag.Span
}

// VM executes a program.
type VM struct {
	prog   *bytecode.Program
	heap   *gc.Heap
	stdout io.Writer
	stderr io.Writer

	stack  []Value
	frames []frame
	// strings caches the heap object for each string constant, so a literal is
	// allocated once rather than on every evaluation.
	strings map[int]layout.Ref

	// temps holds values that have left the stack but are still in use -- a builtin's
	// arguments, for instance. They are roots: a value the collector cannot see is a
	// value whose address goes stale the moment anything allocates, and holding one
	// across an allocation is the classic use-after-move.
	temps []Value

	// maxFrames bounds recursion, so deep recursion traps as a stack overflow rather
	// than exhausting the host (spec/08-memory-model.md).
	maxFrames int

	// w is the shared world -- the scheduler, the channels, and every other thread's
	// machine (spec/12-concurrency.md). tid identifies this thread, main being 1.
	w   *world
	tid int64
	// taken holds what BuiltinAwait dequeued, until BuiltinTaken reads it. Per-thread,
	// which is what makes the pair atomic with respect to another receiver.
	taken Value
	// takenText is the same arrangement for `fs::read_file` (spec/15-files.md). It is a
	// Go string rather than a heap object, so unlike `taken` it is not a collection root:
	// nothing in the heap points at it until `fs::taken_text` allocates the String.
	takenText string
	hasTaken  bool
}

// Config sizes the VM.
type Config struct {
	Heap      gc.Config
	MaxFrames int
	MaxStack  int
}

// New builds a VM for a program.
func New(prog *bytecode.Program, cfg Config, stdout, stderr io.Writer) *VM {
	if cfg.MaxFrames == 0 {
		cfg.MaxFrames = 4096
	}
	if cfg.MaxStack == 0 {
		cfg.MaxStack = 1 << 20
	}
	v := &VM{
		prog:      prog,
		heap:      gc.New(cfg.Heap, prog.Types),
		stdout:    stdout,
		stderr:    stderr,
		stack:     make([]Value, 0, 256),
		strings:   map[int]layout.Ref{},
		maxFrames: cfg.MaxFrames,
		w:         newWorld(),
		tid:       1,
	}
	v.w.vms[v.tid] = v
	// The collector sees every thread's roots, not only this one's: with more than one
	// thread each has its own stack, and a root missed is an object collected while it
	// is still reachable.
	v.heap.SetRoots(v.w.visitWorldRoots)
	return v
}

// visitRoots hands the collector every live reference. It is precise because every
// stack slot is tagged: nothing is scanned conservatively and nothing is guessed.
func (v *VM) visitRoots(visit func(*layout.Ref)) {
	v.w.visitWorldRoots(visit)
}

// visitOwnRoots hands the collector this one thread's roots.
func (v *VM) visitOwnRoots(visit func(*layout.Ref)) {
	for i := range v.stack {
		if v.stack[i].Tag == layout.TagRef && v.stack[i].R != layout.Nil {
			visit(&v.stack[i].R)
		}
	}
	for i := range v.temps {
		if v.temps[i].Tag == layout.TagRef && v.temps[i].R != layout.Nil {
			visit(&v.temps[i].R)
		}
	}
	for i := range v.frames {
		if v.frames[i].closure != layout.Nil {
			visit(&v.frames[i].closure)
		}
	}
	// The value dequeued by `await_value` and not yet read by `taken_value` is live and
	// belongs to no stack: it has left the channel's queue and has not reached the
	// program. Missing it means a collection between the two frees it, or moves it
	// without rewriting this reference.
	if v.hasTaken && v.taken.Tag == layout.TagRef && v.taken.R != layout.Nil {
		visit(&v.taken.R)
	}
}

// Stats reports what the collector did during the run, so a test can assert that a
// workload actually exercised it rather than fitting in the nursery.
func (v *VM) Stats() gc.Stats { return v.heap.Stats() }

func (v *VM) trap(span diag.Span, format string, args ...any) {
	panic(&Trap{Msg: fmt.Sprintf(format, args...), Span: v.userSpan(span)})
}

// userSpan resolves a span to the innermost location in the program's own source.
//
// The prelude's methods are written in terms of compiler-provided operations, so a trap
// raised by one -- "send on a closed channel" -- naturally carries a span inside the
// prelude, which tells a programmer nothing they can act on. Walking out through the
// return spans names the line they wrote. The interpreter does the same, which is also
// what keeps the two engines' stderr byte-identical for the differential.
func (v *VM) userSpan(span diag.Span) diag.Span {
	if !isPreludeSpan(span) {
		return span
	}
	for i := len(v.frames) - 1; i >= 0; i-- {
		if s := v.frames[i].retSpan; s.Valid() && !isPreludeSpan(s) {
			return s
		}
	}
	return span
}

func isPreludeSpan(s diag.Span) bool {
	return s.Valid() && s.File != nil && s.File.Name == prelude.Name
}

func (v *VM) push(val Value) { v.stack = append(v.stack, val) }

func (v *VM) pop() Value {
	val := v.stack[len(v.stack)-1]
	v.stack = v.stack[:len(v.stack)-1]
	return val
}

func (v *VM) peek(n int) Value { return v.stack[len(v.stack)-1-n] }

// alloc allocates, turning exhaustion into the trap the specification names.
func (v *VM) alloc(t layout.TypeID, words uint64, span diag.Span) layout.Ref {
	r := v.heap.Alloc(t, words)
	if r == layout.Nil {
		v.trap(span, "out of memory")
	}
	return r
}

// Run executes `main` and returns the process exit status.
func (v *VM) Run() (exitCode int) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		// `dying` means another thread trapped first and this one was woken to unwind;
		// the trap to report is that one (ADR-0026).
		if _, unwinding := r.(dying); unwinding {
			v.w.mu.Lock()
			t := v.w.trap
			v.w.mu.Unlock()
			if t != nil {
				fmt.Fprintln(v.stderr, t.Error())
			}
			exitCode = TrapExitCode
			return
		}
		t, isTrap := r.(*Trap)
		if !isTrap {
			panic(r)
		}
		// Recorded before reporting, so a thread parked on a channel or a lock wakes and
		// unwinds rather than running past the end of the program.
		v.w.mu.Lock()
		v.w.fail(t)
		v.w.live--
		v.w.mu.Unlock()

		fmt.Fprintln(v.stderr, t.Error())
		exitCode = TrapExitCode
	}()

	// Main holds the world while it executes, exactly as a spawned thread does: the
	// lock is what serializes mutators against each other and against a collection
	// (concurrent.go).
	v.w.exec.Lock()

	entry := v.prog.Fns[v.prog.Entry]
	v.pushFrame(v.prog.Entry, entry, layout.Nil, 0, entry.Span)
	v.run(0)

	// `main` returning does not end the program while a spawned thread is still running
	// (spec/12-concurrency.md). Main stops being a thread that can make progress first,
	// so a deadlock among the survivors is still recognized.
	v.w.mu.Lock()
	v.w.live--
	delete(v.w.vms, v.tid)
	v.w.cond.Broadcast()
	v.w.mu.Unlock()
	v.w.exec.Unlock()
	v.w.wg.Wait()

	v.w.mu.Lock()
	t := v.w.trap
	v.w.mu.Unlock()
	if t != nil {
		fmt.Fprintln(v.stderr, t.Error())
		return TrapExitCode
	}
	return 0
}

func (v *VM) pushFrame(index int, fn *bytecode.Fn, closure layout.Ref, argCount int, span diag.Span) {
	if len(v.frames) >= v.maxFrames {
		v.trap(span, "stack overflow")
	}
	base := len(v.stack) - argCount
	// Locals beyond the arguments start as unit; every binding is written before it is
	// read, because Origin has no uninitialized values (ADR-0007).
	for i := argCount; i < fn.Locals; i++ {
		v.push(unitVal())
	}
	v.frames = append(v.frames, frame{fn: fn, fnIndex: index, base: base, closure: closure, retSpan: span})
}

// stringConst interns a string constant into the heap.
func (v *VM) stringConst(i int, span diag.Span) layout.Ref {
	if r, ok := v.strings[i]; ok && r != layout.Nil {
		return r
	}
	r := v.heap.AllocBytes(v.prog.StringType, v.prog.Consts[i].Str)
	if r == layout.Nil {
		v.trap(span, "out of memory")
	}
	v.strings[i] = r
	return r
}

// newString allocates a fresh String object.
func (v *VM) newString(s string, span diag.Span) layout.Ref {
	r := v.heap.AllocBytes(v.prog.StringType, s)
	if r == layout.Nil {
		v.trap(span, "out of memory")
	}
	return r
}

var _ = strconv.Itoa
var _ = strings.Builder{}
var _ = compile.BuiltinPrint
