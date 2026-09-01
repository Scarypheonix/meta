package backend

import (
	"encoding/binary"
	"sort"

	"github.com/scarypheonix/meta/internal/ir"
	"github.com/scarypheonix/meta/internal/layout"
	"github.com/scarypheonix/meta/internal/x86"
)

// pendingSafepoint is one call site recorded during instruction lowering. Its return
// address is not known yet -- x86.Label only resolves once every instruction that could
// come after it has been emitted -- so the label travels until buildStackMap resolves
// every one of them at once, after the whole program's code exists.
type pendingSafepoint struct {
	label x86.Label
	entry layout.StackMapEntry
}

// recordCall marks the address immediately after a call instruction as a safepoint,
// naming which callee-saved registers hold a live reference at that exact point
// (callSiteRegs, ADR-0021) and where the current function's own reference-kind spill
// slots are (regalloc.go's contiguous placement). v is the IR value whose lowering just
// emitted the call; every call site in this file's lowering functions has one in scope
// already.
//
// Runtime routines' own internal calls -- write, trap, panic, and everything inside
// runtime.go itself -- are deliberately not covered here. None of them ever holds an
// Origin-level reference of its own to report, and trap/panic never return to Origin
// code at all, so a collection can never resume at their return address. Reaching one of
// those frames while walking the call stack is the future collector's own problem to
// solve (skip it structurally via its saved rbp), not a gap in this table.
func (e *emitter) recordCall(v *ir.Value) {
	label := e.a.NewLabel("safepoint")
	e.a.Bind(label)
	e.recordUserSpan(v, label)

	var mask uint8
	for _, r := range e.regs.callSiteRegs[v] {
		for i, c := range calleeSaved {
			if c == r {
				mask |= 1 << uint(i)
			}
		}
	}
	e.safepoints = append(e.safepoints, pendingSafepoint{
		label: label,
		entry: layout.StackMapEntry{
			RefOffset: e.slotBase + wordSize,
			RefCount:  int32(e.regs.refSlots),
			RegMask:   mask,
			SavedMask: e.savedMask(),
		},
	})
}

// savedMask names, in RegMask's own bit order, which callee-saved registers the current
// function's own prologue pushed (e.saved, backend.go's `function`) -- a per-function
// fact recorded on every one of that function's call sites, since the stack map has no
// other place to name "the current function" from. A future collector walking the rbp
// chain (ADR-0022) uses it to tell a live-but-untouched register (still the physical
// register, unchanged since the call) from one this function itself spilled to a fixed
// rbp-relative slot in its own prologue (the caller's original value, recovered from
// there instead): for register i, StackMapEntry's own doc comment gives the slot.
func (e *emitter) savedMask() uint8 {
	var mask uint8
	for _, r := range e.saved {
		for i, c := range calleeSaved {
			if c == r {
				mask |= 1 << uint(i)
			}
		}
	}
	return mask
}

// buildStackMap resolves every recorded safepoint's return address (every label now
// bound, since every function's code has been emitted), sorts the table the way
// spec/11-codegen.md requires for a binary search, and appends it to read-only data.
// It returns the table's address and entry count, for the runtime block.
func (e *emitter) buildStackMap() (addr uint64, count int) {
	entries := make([]layout.StackMapEntry, len(e.safepoints))
	for i, sp := range e.safepoints {
		sp.entry.ReturnAddr = e.a.Addr(sp.label)
		entries[i] = sp.entry
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ReturnAddr < entries[j].ReturnAddr })

	table := layout.EncodeStackMap(entries)
	addr = e.roDataAddr + uint64(len(e.roData))
	e.roData = append(e.roData, table...)
	return addr, len(entries)
}

// writeStackMapFields pokes the table's address and entry count directly into the
// initial data segment. Both are compile-time constants once buildStackMap has run, so
// unlike the bump pointer and heap limit (only mmap's return value supplies those) they
// need no runtime instruction to set up.
func writeStackMapFields(data []byte, addr uint64, count int) {
	binary.LittleEndian.PutUint64(data[rtStackMapOff:], addr)
	binary.LittleEndian.PutUint64(data[rtStackMapCountOff:], uint64(count))
}
