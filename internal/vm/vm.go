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
	}
	v.heap.SetRoots(v.visitRoots)
	return v
}

// visitRoots hands the collector every live reference. It is precise because every
// stack slot is tagged: nothing is scanned conservatively and nothing is guessed.
func (v *VM) visitRoots(visit func(*layout.Ref)) {
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
	for k := range v.strings {
		r := v.strings[k]
		if r != layout.Nil {
			visit(&r)
			v.strings[k] = r
		}
	}
}

// Stats reports what the collector did during the run, so a test can assert that a
// workload actually exercised it rather than fitting in the nursery.
func (v *VM) Stats() gc.Stats { return v.heap.Stats() }

func (v *VM) trap(span diag.Span, format string, args ...any) {
	panic(&Trap{Msg: fmt.Sprintf(format, args...), Span: span})
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
		if r := recover(); r != nil {
			t, isTrap := r.(*Trap)
			if !isTrap {
				panic(r)
			}
			fmt.Fprintln(v.stderr, t.Error())
			exitCode = TrapExitCode
		}
	}()

	entry := v.prog.Fns[v.prog.Entry]
	v.pushFrame(v.prog.Entry, entry, layout.Nil, 0, entry.Span)
	v.run()
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
