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
	"github.com/scarypheonix/meta/internal/dwarf"
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
	// The debug-info sections are built entirely from addresses inside .text (ADR-0023),
	// which pass one and pass two already agree on byte for byte -- text's own address
	// space starts at the same place and every instruction's length is pass-invariant by
	// construction (the check above). This asserts that invariant held rather than
	// silently trusting it, the same way the text/roData check does.
	if len(final.debugLine) != len(first.debugLine) {
		return nil, fmt.Errorf(
			"this is a compiler bug: the two layout passes' debug line tables disagree (%d/%d bytes)",
			len(first.debugLine), len(final.debugLine))
	}

	img := plan.Image(final.text, final.roData, final.data, 0, final.entry)
	img.DebugAbbrev = final.debugAbbrev
	img.DebugInfo = final.debugInfo
	img.DebugLine = final.debugLine
	img.Funcs = final.funcs
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

	// debugAbbrev, debugInfo and debugLine are ADR-0023's DWARF sections; funcs is the
	// symbol-table data `bt` names a frame from.
	debugAbbrev, debugInfo, debugLine []byte
	funcs                             []dwarf.Func
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
	// joinedTwiceMsg is §12's `handle already joined`.
	joinedTwiceMsg staticStr
	// deadlockMsg is §12's `all threads are blocked`, for the one place that detects it
	// with no user code left on the stack to name: the drain loop `_start` runs after
	// `main` has returned (sched.go).
	deadlockMsg staticStr
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

	// gcRuntimeEntryAddr is a synthetic StackMapEntry the collector's own root walk
	// (ADR-0022) substitutes when it reaches a frame with no real entry -- one of
	// runtime.go's own routines (rt_int_to_str, say), which allocates internally but,
	// like every runtime routine, was deliberately never wired into recordCall
	// (ADR-0021's own documented scope). See collect.go's emitGCRuntimeFrameEntry.
	gcRuntimeEntryAddr uint64

	fnLabels []x86.Label

	// safepoints accumulates one entry per call site across every function in the
	// program, in whatever order lowering visits them; buildStackMap sorts and encodes
	// them once every function's code exists (ADR-0021, spec/11-codegen.md's
	// "Safepoints and stack maps").
	safepoints []pendingSafepoint

	// debugLines accumulates one dwarf.Line per source-line boundary crossed while
	// emitting, across every function (ADR-0023); debugFuncs accumulates one dwarf.Func
	// per emitted function, for the symbol table `bt` names a frame from. lastDebugFile
	// and lastDebugLine are recordLine's own dedupe state -- a value's span produces a
	// new row only when it names a different line than the previous instruction's did.
	debugLines    []dwarf.Line
	debugFuncs    []dwarf.Func
	lastDebugFile string
	lastDebugLine int

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
	// A concurrency trap raised by the runtime rather than by a lowered instruction has
	// no span of its own to name, the same way `out of memory` has none. The other two
	// engines resolve such a trap to the user's own line by walking out of the prelude
	// (interp/vm's userSpan); native code has no frame table to do that with yet, so it
	// says `<runtime>` rather than inventing a location (docs/deferred.md).
	e.joinedTwiceMsg = e.rawString("origin: handle already joined at <runtime>\n")
	e.deadlockMsg = e.rawString("origin: all threads are blocked at <runtime>\n")
	e.panicPrefix = e.rawString("origin: ")
	e.emitTypeTable()
	e.emitGCRuntimeFrameEntry()

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

	mapAddr, mapCount := e.buildStackMap()

	data := make([]byte, rtBlockSize)
	writeStackMapFields(data, mapAddr, mapCount)

	text := e.a.Code()
	// The compile-unit DIE's own address range covers the whole of .text (there is
	// exactly one compile unit, ADR-0023), not just where user code happens to start --
	// plan.TextAddr is where e.a itself was seeded (emitProgram's own `x86.New(plan.TextAddr)`
	// above), so this needs nothing e.a doesn't already expose.
	lowPC := plan.TextAddr
	highPC := lowPC + uint64(len(text))
	abbrev, info, line := dwarf.Build(e.debugLines, lowPC, highPC)

	return &result{
		text:   text,
		roData: e.roData,
		data:   data,
		entry:  e.a.Addr(e.rt.start),

		debugAbbrev: abbrev,
		debugInfo:   info,
		debugLine:   line,
		funcs:       e.debugFuncs,
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
	// A fresh function always gets a debug-line row for its own first instruction, even if
	// it happens to share a (file, line) with whatever the previous function's last row
	// named -- recordLine's dedupe is otherwise global across the whole program.
	e.lastDebugFile, e.lastDebugLine = "", 0

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
	startAddr := e.a.PC()
	e.a.Push(x86.RBP)
	e.a.MovRR(x86.RBP, x86.RSP)
	for _, r := range e.saved {
		e.a.Push(r)
	}
	if e.frame > 0 {
		e.a.SubRI(x86.RSP, e.frame)
	}

	// A reference-kind spill slot is reserved for the whole function body the moment its
	// owning value is ever spilled (regalloc.go gives every spilled interval a permanent
	// slot, never reused across non-overlapping ones), but a stack map's RefCount --
	// fixed per function, not narrowed to what is actually live at each call site --
	// covers every one of them at every safepoint, including a call that executes before
	// the slot's own value is ever computed (a branch that skips its definition, or a
	// call textually earlier in the function). Zeroing every reference slot here, before
	// anything can be spilled into one, means a future collector (ADR-0022) that visits
	// one before its value exists reads Nil rather than whatever an earlier stack frame
	// happened to leave there -- a defensive read, not a value real Origin code can ever
	// observe (ADR-0007: no null).
	for i := 1; i <= e.regs.refSlots; i++ {
		e.a.MovMI(e.mem(inSlot(i)), 0)
	}

	// A closure body's own object arrives just above the return address (lower.go's
	// callClosure). Copying it into the slot regalloc.go reserved is what makes it a root
	// the collector can find and update; every OpCapture reads it from there afterwards.
	if e.regs.closureSlot != 0 {
		e.a.MovRM(scratchA, x86.At(x86.RBP, 16))
		e.a.MovMR(e.mem(inSlot(e.regs.closureSlot)), scratchA)
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
			e.recordLine(v)
			if err := e.instr(v); err != nil {
				return fmt.Errorf("%s: %w", e.prog.Fns[index].Name, err)
			}
		}
		if b.Term != nil {
			e.recordLine(b.Term)
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

	e.debugFuncs = append(e.debugFuncs, dwarf.Func{
		Name:    e.prog.Fns[index].Name,
		Address: startAddr,
		Size:    e.a.PC() - startAddr,
	})
	return nil
}

// recordLine appends a dwarf.Line row for v's own source position, but only when it names
// a different (file, line) than the previous instruction's did (ADR-0023): a row exists to
// mark where a line *begins*, not one per instruction, and DW_LNS_copy's own row is what a
// breakpoint or a `step` resolves against, not every address in between.
func (e *emitter) recordLine(v *ir.Value) {
	span := v.Span
	if !span.Valid() {
		return
	}
	line, col := span.Position()
	file := span.File.Name
	if file == e.lastDebugFile && line == e.lastDebugLine {
		return
	}
	e.lastDebugFile, e.lastDebugLine = file, line
	e.debugLines = append(e.debugLines, dwarf.Line{
		Address: e.a.PC(),
		File:    file,
		Line:    line,
		Col:     col,
	})
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
