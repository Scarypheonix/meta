package backend

import (
	"encoding/binary"
	"sort"

	"github.com/scarypheonix/meta/internal/ir"
	"github.com/scarypheonix/meta/internal/prelude"
	"github.com/scarypheonix/meta/internal/x86"
)

// Which line a runtime-raised trap names.
//
// Most traps know their line while compiling: `divide by zero` is emitted at the division,
// and lower.go bakes the whole message, span and all, into read-only data. The concurrency
// traps do not. `send on a closed channel` is discovered inside a runtime routine, and the
// call that reached it was written in the *prelude* -- `Sender::send` calling
// `chan::send_value` -- so the span the backend has at that point names a file the
// programmer never opened.
//
// The other two engines answer this at run time: a trap whose span is in the prelude is
// re-reported at the innermost frame whose call site is not (interp/vm's `userSpan`). §12's
// worked examples are written that way, so the three engines only agree if native code does
// the same thing. It has the machinery already -- the collector walks the rbp chain and
// binary-searches a table keyed by return address -- so this is that walk against a second
// table: one entry per call site in user code, holding the text of its own span.
//
// -O1 and above mostly settle this before the backend sees it: inlining a prelude method
// rewrites its spans to the call site (opt/inline.go), which turns the trap back into one
// with a known line. -O0 inlines nothing, and the suite runs all three levels.

// spanEntryWords is one row: the return address, and the address and length of the span's
// own text (`file.origin:12:5`, no message and no newline around it).
const spanEntryWords = 3

// pendingUserSpan is one call site recorded while lowering. Like a safepoint, its return
// address is a label that resolves only once every instruction after it exists.
type pendingUserSpan struct {
	label x86.Label
	text  staticStr
}

// recordUserSpan notes the call being lowered as one a trap may be re-reported at, unless
// it was written in the prelude -- the whole point of the walk is to skip those.
//
// The label must be bound by the caller (recordCall already binds one at the same address
// for the stack map, and both tables are keyed by exactly that address).
func (e *emitter) recordUserSpan(v *ir.Value, label x86.Label) {
	if isPreludeSpan(v) {
		return
	}
	e.userSpans = append(e.userSpans, pendingUserSpan{label: label, text: e.rawString(v.Span.String())})
}

// isPreludeSpan reports whether a value's span names the prelude rather than a file the
// programmer wrote, mirroring interp/vm's own test.
func isPreludeSpan(v *ir.Value) bool {
	return v.Span.Valid() && v.Span.File != nil && v.Span.File.Name == prelude.Name
}

// buildSpanTable resolves every recorded call site's return address and appends the sorted
// table to read-only data, exactly as buildStackMap does for safepoints.
func (e *emitter) buildSpanTable() (addr uint64, count int) {
	type row struct{ ret, text, length uint64 }
	rows := make([]row, 0, len(e.userSpans))
	for _, s := range e.userSpans {
		rows = append(rows, row{e.a.Addr(s.label), s.text.addr, uint64(s.text.length)})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ret < rows[j].ret })

	addr = e.roDataAddr + uint64(len(e.roData))
	for _, r := range rows {
		e.roData = appendU64(e.roData, r.ret)
		e.roData = appendU64(e.roData, r.text)
		e.roData = appendU64(e.roData, r.length)
	}
	return addr, len(rows)
}

// writeSpanTableFields pokes the table's address and row count into the initial data
// segment, the same compile-time constants the stack map's own fields are.
func writeSpanTableFields(data []byte, addr uint64, count int) {
	binary.LittleEndian.PutUint64(data[rtSpanTableOff:], addr)
	binary.LittleEndian.PutUint64(data[rtSpanCountOff:], uint64(count))
}

// emitTrapSpan writes `rt_trap_span(prefix rdi, prefixLen rsi)`: report a trap whose
// message is known but whose line is not, and exit 101.
//
// The prefix is the whole message up to and including " at ", so this appends a location
// and a newline and the result reads exactly as a compile-time trap message does.
//
// It walks out from its own caller until a return address turns up in the span table, which
// holds user-code call sites only -- so the first hit is the innermost frame the programmer
// wrote, which is what `userSpan` means. A frame with no entry is skipped rather than
// stopping the walk: the routines between here and user code are the prelude's and the
// runtime's own. When nothing matches, the location is `<runtime>`, which is honest and is
// what the message said before this existed.
func (e *emitter) emitTrapSpan() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.trapSpan)

	e.runtimePrologue()
	a.MovRR(x86.R12, x86.RDI) // the prefix, across every call below
	a.MovRR(x86.R13, x86.RSI)

	// r14 and rbx end up holding the location's text; the fallback is `<runtime>`.
	a.MovRI(x86.R14, e.runtimeLocMsg.addr)
	a.MovRI(x86.RBX, uint64(e.runtimeLocMsg.length))

	walk := a.NewLabel("trap_span_walk")
	report := a.NewLabel("trap_span_report")
	next := a.NewLabel("trap_span_next")

	a.MovRR(x86.RCX, x86.RBP) // this frame; [rcx+8] is the call that reached it
	a.Bind(walk)
	a.MovRM(x86.RDI, x86.At(x86.RCX, 8))
	a.Push(x86.RCX)
	a.Call(e.rt.spanLookup)
	a.Pop(x86.RCX)
	a.TestRR(x86.RAX, x86.RAX)
	a.Jcc(x86.Equal, next)
	a.MovRM(x86.R14, x86.At(x86.RAX, 8))
	a.MovRM(x86.RBX, x86.At(x86.RAX, 16))
	a.Jmp(report)

	a.Bind(next)
	a.MovRM(x86.RCX, x86.At(x86.RCX, 0))
	a.TestRR(x86.RCX, x86.RCX)
	a.Jcc(x86.NotEqual, walk)

	a.Bind(report)
	a.MovRI(x86.RDI, 2)
	a.MovRR(x86.RSI, x86.R12)
	a.MovRR(x86.RDX, x86.R13)
	a.Call(e.rt.write)

	a.MovRI(x86.RDI, 2)
	a.MovRR(x86.RSI, x86.R14)
	a.MovRR(x86.RDX, x86.RBX)
	a.Call(e.rt.write)

	a.MovRI(x86.RDI, 2)
	a.MovRI(x86.RSI, e.newlineAddr)
	a.MovRI(x86.RDX, 1)
	a.Call(e.rt.write)

	a.MovRI(x86.RAX, e.target.SysExit)
	a.MovRI(x86.RDI, 101)
	a.Syscall()
	a.Ud2()
}

// emitSpanLookup writes `rt_span_lookup(retAddr rdi) -> rax`: the row for that return
// address, or 0.
//
// Binary search over a table sorted by address, the same shape and the same reason as
// rt_lookup_stack_map: one row per user-code call site is a lot of rows, and a trap is
// allowed to be slow but a linear scan of every call site in the program is silly when the
// table is already sorted.
//
// A leaf: it calls nothing and uses only the scratch registers.
func (e *emitter) emitSpanLookup() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.spanLookup)

	loop := a.NewLabel("span_loop")
	found := a.NewLabel("span_found")
	notFound := a.NewLabel("span_not_found")
	goRight := a.NewLabel("span_go_right")

	a.MovRM(x86.R8, x86.At(x86.R15, rtSpanTableOff))
	a.MovRM(x86.R9, x86.At(x86.R15, rtSpanCountOff))
	a.XorRR(x86.RCX, x86.RCX) // lo = 0
	a.MovRR(x86.RDX, x86.R9)  // hi = count

	a.Bind(loop)
	a.CmpRR(x86.RCX, x86.RDX)
	a.Jcc(x86.AboveEqual, notFound)

	a.MovRR(x86.RAX, x86.RDX)
	a.AddRR(x86.RAX, x86.RCX)
	a.ShrI(x86.RAX, 1) // mid

	a.MovRR(x86.R10, x86.RAX)
	a.MovRI(x86.R11, spanEntryWords*wordSize)
	a.ImulRR(x86.R10, x86.R11)
	a.AddRR(x86.R10, x86.R8) // &table[mid]

	a.MovRM(x86.R11, x86.At(x86.R10, 0))
	a.CmpRR(x86.R11, x86.RDI)
	a.Jcc(x86.Equal, found)
	a.Jcc(x86.Below, goRight)
	a.MovRR(x86.RDX, x86.RAX) // hi = mid
	a.Jmp(loop)
	a.Bind(goRight)
	a.MovRR(x86.RCX, x86.RAX)
	a.AddRI(x86.RCX, 1) // lo = mid+1
	a.Jmp(loop)

	a.Bind(found)
	a.MovRR(x86.RAX, x86.R10)
	a.Ret()

	a.Bind(notFound)
	a.XorRR(x86.RAX, x86.RAX)
	a.Ret()
}

// trapAtUserSpan emits a trap whose message is known and whose line is decided at run time.
//
// When the call being lowered was written outside the prelude, the line is known right here
// and the message is a constant like every other trap's. It is only inside the prelude that
// the walk is needed at all.
func (e *emitter) trapAtUserSpan(v *ir.Value, msg string) {
	if !isPreludeSpan(v) {
		e.trapWith(e.trapMessage(msg, v.Span))
		return
	}
	prefix := e.trapPrefix(msg)
	e.a.MovRI(x86.RDI, prefix.addr)
	e.a.MovRI(x86.RSI, uint64(prefix.length))
	e.a.Call(e.rt.trapSpan)
	e.a.Ud2()
}

// trapPrefix is a trap message up to the location it will name: "origin: <msg> at ".
func (e *emitter) trapPrefix(msg string) staticStr {
	text := "origin: " + msg + " at "
	if s, ok := e.trapMsg[text]; ok {
		return s
	}
	s := e.rawString(text)
	e.trapMsg[text] = s
	return s
}
