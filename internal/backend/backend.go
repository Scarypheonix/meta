// Package backend turns the optimized SSA IR into an executable.
//
// It is the last stage: `internal/ir` decides what the program computes, `internal/x86`
// decides what bytes an instruction is, `internal/obj` decides what a file looks like,
// and this decides which instructions to emit and where everything lives.
//
// Two passes over the whole program, not one. ADR-0017's freestanding executables have
// no relocations, so an instruction naming an address must know it while being encoded —
// but the addresses depend on how much code there is. The first pass emits against a
// provisional layout to measure; the second emits against the real one. Every
// instruction the encoder produces has a length independent of its operands' values, so
// the two passes agree byte for byte on size, and the build fails loudly if they ever
// do not.
package backend

import (
	"fmt"

	"github.com/scarypheonix/meta/internal/bytecode"
	"github.com/scarypheonix/meta/internal/ir"
	"github.com/scarypheonix/meta/internal/layout"
	"github.com/scarypheonix/meta/internal/obj"
	"github.com/scarypheonix/meta/internal/x86"
)

// Build compiles a program to an executable image for a target.
//
// The bytecode it is given has already been through the optimizer at whatever level was
// asked for, so the native path gets the optimizer for free and shares its lowering with
// the virtual machine — which is what makes comparing the two a real test rather than a
// comparison of two unrelated compilers.
func Build(prog *bytecode.Program, target obj.Target) (*obj.Image, error) {
	funcs, err := buildIR(prog)
	if err != nil {
		return nil, err
	}

	// Pass one measures. Its addresses are wrong and its code is thrown away.
	probe := obj.Plan(target, 0, 0, rtBlockSize)
	first, err := emitProgram(prog, funcs, target, probe)
	if err != nil {
		return nil, err
	}

	// Pass two emits against the layout the measurement implies.
	plan := obj.Plan(target, uint64(len(first.text)), uint64(len(first.roData)), rtBlockSize)
	final, err := emitProgram(prog, funcs, target, plan)
	if err != nil {
		return nil, err
	}
	if len(final.text) != len(first.text) || len(final.roData) != len(first.roData) {
		return nil, fmt.Errorf(
			"this is a compiler bug: the two layout passes disagree (%d/%d bytes of code, %d/%d of data); "+
				"an instruction's length must not depend on the value of an address",
			len(first.text), len(final.text), len(first.roData), len(final.roData))
	}

	img := plan.Image(final.text, final.roData, final.data, 0, final.entry)
	if err := img.Validate(); err != nil {
		return nil, err
	}
	return img, nil
}

// buildIR lowers every function to SSA. A function with no code is a declaration the
// program never defined — a compiler-provided impl, say — and has nothing to emit.
func buildIR(prog *bytecode.Program) ([]*ir.Func, error) {
	out := make([]*ir.Func, len(prog.Fns))
	for i, fn := range prog.Fns {
		if len(fn.Code) == 0 {
			continue
		}
		f, err := ir.Build(fn)
		if err != nil {
			return nil, fmt.Errorf("building %s: %w", fn.Name, err)
		}
		resolveClosureCalls(f)
		propagateKinds(f, prog)
		out[i] = f
	}
	return out, nil
}

// staticStr is a constant already sitting in read-only data: its address and its length.
type staticStr struct {
	addr   uint64
	length int
}

// result is one pass's output.
type result struct {
	text   []byte
	roData []byte
	data   []byte
	entry  uint64
}

// emitter holds one pass's state.
type emitter struct {
	prog   *bytecode.Program
	funcs  []*ir.Func
	target obj.Target
	a      *x86.Asm
	rt     runtimeLabels

	roDataAddr uint64
	dataAddr   uint64
	roData     []byte

	stringType     layout.TypeID
	newlineAddr    uint64
	outOfMemoryMsg staticStr
	// panicPrefix is the "origin: " a panic's message is wrapped in, so that the three
	// engines print the same line.
	panicPrefix staticStr
	// constStr maps a constant-pool index to the String object built for it, so a
	// literal costs no allocation at run time.
	constStr map[int]staticStr
	// trapMsg deduplicates the fully formatted trap messages.
	trapMsg map[string]staticStr
	// literals holds the Strings the backend itself needs, such as the two a bool
	// renders as.
	literals map[string]staticStr

	// typeTableAddr is the base of the per-TypeID layout table equal_objects reads
	// (equal.go). `bytecode.Kind` widens OpEq/OpNe enough to say "this is some
	// aggregate" (KindRef) but not which one -- unlike OpToStr and the comparison
	// operators, it carries no further type identity -- so structural `==` resolves
	// the concrete shape at run time from the operand's own header, the same way
	// internal/vm's equalObjects does, rather than needing compile.go widened again.
	typeTableAddr uint64

	fnLabels []x86.Label

	// Per-function state.
	fn       *ir.Func
	regs     *alloc
	slotBase int32
	frame    int32
	saved    []x86.Reg
	blockLbl map[*ir.Block]x86.Label
	epilogue x86.Label
	// pending are the φ landing pads a conditional branch created, emitted after the
	// blocks so that no block falls through into one.
	pending []edge
}

func emitProgram(prog *bytecode.Program, funcs []*ir.Func, target obj.Target, plan *obj.Layout) (*result, error) {
	e := &emitter{
		prog: prog, funcs: funcs, target: target,
		a:          x86.New(plan.TextAddr),
		roDataAddr: plan.RoDataAddr,
		dataAddr:   plan.DataAddr,
		stringType: prog.StringType,
		constStr:   map[int]staticStr{},
		trapMsg:    map[string]staticStr{},
		literals:   map[string]staticStr{},
	}

	// The two constants the runtime itself needs come first, so their addresses do not
	// move when a program's own strings change.
	e.newlineAddr = e.roDataAddr + uint64(len(e.roData))
	e.roData = append(e.roData, '\n')
	e.outOfMemoryMsg = e.rawString("origin: out of memory at <runtime>\n")
	e.panicPrefix = e.rawString("origin: ")
	e.emitTypeTable()

	if prog.Entry < 0 || prog.Entry >= len(funcs) || funcs[prog.Entry] == nil {
		return nil, fmt.Errorf("the program has no `main` to start at")
	}

	e.fnLabels = make([]x86.Label, len(funcs))
	for i, f := range funcs {
		name := prog.Fns[i].Name
		if f == nil {
			name += " (no body)"
		}
		e.fnLabels[i] = e.a.NewLabel(fmt.Sprintf("fn%d %s", i, name))
	}

	e.emitRuntime(e.fnLabels[prog.Entry])

	for i, f := range funcs {
		if f == nil {
			continue
		}
		if err := e.function(i, f); err != nil {
			return nil, err
		}
	}

	data := make([]byte, rtBlockSize)
	return &result{
		text:   e.a.Code(),
		roData: e.roData,
		data:   data,
		entry:  e.a.Addr(e.rt.start),
	}, nil
}

// rawString puts a bare run of bytes in read-only data: a trap message, which the
// runtime writes with `write` and never treats as an Origin value.
func (e *emitter) rawString(s string) staticStr {
	out := staticStr{addr: e.roDataAddr + uint64(len(e.roData)), length: len(s)}
	e.roData = append(e.roData, s...)
	return out
}

// stringConst puts a constant-pool string in read-only data as a complete String object,
// header and all, so that a literal is a pointer rather than an allocation.
func (e *emitter) stringConst(idx int) staticStr {
	if s, ok := e.constStr[idx]; ok {
		return s
	}
	// Objects are word-aligned, because the header packs a size into bits 32..55 and
	// the collector walks by words.
	for len(e.roData)%wordSize != 0 {
		e.roData = append(e.roData, 0)
	}
	text := e.prog.Consts[idx].Str
	out := staticStr{addr: e.roDataAddr + uint64(len(e.roData)), length: len(text)}
	e.roData = append(e.roData, stringObject(text, e.stringType)...)
	e.constStr[idx] = out
	return out
}

// trapMessage formats a trap exactly as the interpreter and the virtual machine print
// it, and puts it in read-only data.
//
// Every span is known while compiling, so the whole line is a constant and the runtime
// does no formatting at all. It has to match the other two engines character for
// character: the end-to-end suite compares their stderr byte for byte.
func (e *emitter) trapMessage(msg string, span interface{ String() string }) staticStr {
	text := fmt.Sprintf("origin: %s at %s\n", msg, span)
	if s, ok := e.trapMsg[text]; ok {
		return s
	}
	s := e.rawString(text)
	e.trapMsg[text] = s
	return s
}

// mem is a spilled value's address. Slots sit below the saved callee-saved registers,
// which is why this is a method rather than a property of the location.
func (e *emitter) mem(l loc) x86.Mem {
	return x86.At(x86.RBP, -(e.slotBase + int32(8*l.slot)))
}

// argRegs is the integer argument sequence of the platform ABI (docs/spec/11-codegen.md).
var argRegs = []x86.Reg{x86.RDI, x86.RSI, x86.RDX, x86.RCX, x86.R8, x86.R9}

// function emits one function: prologue, blocks in reverse postorder, epilogue.
func (e *emitter) function(index int, f *ir.Func) error {
	e.fn = f
	e.regs = allocate(f)
	e.saved = e.regs.used
	e.slotBase = int32(8 * len(e.saved))
	e.blockLbl = map[*ir.Block]x86.Label{}
	e.epilogue = e.a.NewLabel(fmt.Sprintf("fn%d epilogue", index))

	// The frame keeps rsp 16-byte aligned at every call. Entering the body, rsp is
	// aligned; `push rbp` and the saved registers move it, and this makes up the
	// difference so that a `call` lands on a boundary.
	slots := int32(8 * e.regs.slots)
	e.frame = align32(slots, 16) + (e.slotBase % 16)

	if f.Params > len(argRegs) {
		return fmt.Errorf("unimplemented: a function of %d parameters, past the %d the "+
			"registers carry (docs/spec/11-codegen.md leaves the rest on the stack)",
			f.Params, len(argRegs))
	}
	e.a.Align(16)
	e.a.Bind(e.fnLabels[index])
	e.a.Push(x86.RBP)
	e.a.MovRR(x86.RBP, x86.RSP)
	for _, r := range e.saved {
		e.a.Push(r)
	}
	if e.frame > 0 {
		e.a.SubRI(x86.RSP, e.frame)
	}

	blocks := order(f)
	for _, b := range blocks {
		e.blockLbl[b] = e.a.NewLabel(fmt.Sprintf("fn%d %s", index, b))
	}

	// Parameters arrive in argument registers and have to reach wherever the allocator
	// put them — which may be another argument register still holding a parameter not
	// moved yet. Pushing every incoming value and popping it into its destination
	// performs the whole permutation without needing to know which moves conflict.
	var params []*ir.Value
	for i := 0; i < f.Params; i++ {
		p := e.paramValue(f, i)
		params = append(params, p)
		if p != nil {
			e.a.Push(argRegs[i])
		}
	}
	for i := len(params) - 1; i >= 0; i-- {
		if params[i] == nil {
			continue // the parameter is never read, so it needs no home
		}
		e.a.Pop(scratchA)
		e.store(scratchA, e.regs.where[params[i]])
	}

	for _, b := range blocks {
		e.a.Bind(e.blockLbl[b])
		for _, v := range b.Instr {
			if err := e.instr(v); err != nil {
				return fmt.Errorf("%s: %w", e.prog.Fns[index].Name, err)
			}
		}
		if err := e.terminator(b); err != nil {
			return fmt.Errorf("%s: %w", e.prog.Fns[index].Name, err)
		}
	}

	// The φ landing pads a branch needed. Emitting one can create another, because a pad
	// ends in a jump to a block whose own edges were already resolved, so this drains
	// rather than iterating once.
	for len(e.pending) > 0 {
		batch := e.pending
		e.pending = nil
		for _, ed := range batch {
			e.a.Bind(ed.label)
			e.phiCopies(ed.from, ed.to)
			e.a.Jmp(e.blockLbl[ed.to])
		}
	}

	e.a.Bind(e.epilogue)
	if e.frame > 0 {
		e.a.AddRI(x86.RSP, e.frame)
	}
	for i := len(e.saved) - 1; i >= 0; i-- {
		e.a.Pop(e.saved[i])
	}
	e.a.Pop(x86.RBP)
	e.a.Ret()
	return nil
}

// paramValue finds the OpParam value for one parameter index.
func (e *emitter) paramValue(f *ir.Func, index int) *ir.Value {
	var found *ir.Value
	f.Values(func(v *ir.Value) {
		if v.Op == ir.OpParam && v.Aux == index {
			found = v
		}
	})
	if found == nil {
		return nil
	}
	if _, ok := e.regs.where[found]; !ok {
		return nil
	}
	return found
}

func align32(v, to int32) int32 {
	if to == 0 {
		return v
	}
	return (v + to - 1) / to * to
}
