package layout

import "encoding/binary"

// StackMapEntry describes one call site's frame for spec/11-codegen.md's "Safepoints
// and stack maps": which frame slots and which registers hold a live reference at the
// instant this call returns. This is the one place the shape lives (process rule 5):
// internal/backend builds a program's table with EncodeStackMap, and the collector
// (once it exists) will read one back with LookupStackMap; neither derives the frame's
// shape independently.
type StackMapEntry struct {
	// ReturnAddr is the call's own return address -- the table's search key.
	ReturnAddr uint64
	// RefOffset is the byte offset, subtracted from rbp, of the first (lowest-addressed)
	// reference-kind spill slot. A function's own linear-scan register allocator
	// (internal/backend/regalloc.go) places every one of them in one contiguous run
	// below the raw slots, so RefCount consecutive words starting at rbp-RefOffset are
	// every reference this frame's own spill slots hold.
	RefOffset int32
	// RefCount is how many consecutive reference-kind spill slots start at RefOffset.
	RefCount int32
	// RegMask names which of the four callee-saved registers hold a live reference at
	// this call site: bit 0 = rbx, bit 1 = r12, bit 2 = r13, bit 3 = r14 -- the fixed
	// order internal/backend/regalloc.go's own calleeSaved slice never varies. A live
	// reference is never in a caller-saved register at a call site (ADR-0018's own
	// allocation invariant: an interval spanning a call always gets a callee-saved
	// register or spills), which is why four bits are enough.
	RegMask uint8
}

// StackMapEntrySize is one encoded entry's width: ReturnAddr(8) + RefOffset(4) +
// RefCount(4) + RegMask(1), padded to keep every entry's ReturnAddr 8-byte aligned so a
// binary search can index the table with a plain multiply. Exported so a caller that
// must compute a table's byte extent before decoding it (internal/backend's own tests,
// reading the table out of a raw image) has one place to read the width from rather than
// hand-duplicating it.
const StackMapEntrySize = 8 + 4 + 4 + 1 + 7

const stackMapEntrySize = StackMapEntrySize

// EncodeStackMap serializes entries into the fixed-width, binary-searchable table
// spec/11-codegen.md describes: sorted by ReturnAddr, in read-only data, reachable from
// the runtime block. entries must already be sorted; this does not re-sort them, so
// that the caller's own sort (over *ir.Value-keyed data with no natural order until
// addresses exist) is the one place order is decided.
func EncodeStackMap(entries []StackMapEntry) []byte {
	out := make([]byte, len(entries)*stackMapEntrySize)
	for i, e := range entries {
		b := out[i*stackMapEntrySize:]
		binary.LittleEndian.PutUint64(b[0:8], e.ReturnAddr)
		binary.LittleEndian.PutUint32(b[8:12], uint32(e.RefOffset))
		binary.LittleEndian.PutUint32(b[12:16], uint32(e.RefCount))
		b[16] = e.RegMask
	}
	return out
}

// LookupStackMap finds the entry for exactly addr in a table EncodeStackMap built, or
// reports false. addr not landing on an entry exactly is a compiler bug upstream of
// this function (every call site gets one), not a case this function resolves any other
// way.
func LookupStackMap(table []byte, addr uint64) (StackMapEntry, bool) {
	n := len(table) / stackMapEntrySize
	lo, hi := 0, n
	for lo < hi {
		mid := (lo + hi) / 2
		e := decodeStackMapEntry(table[mid*stackMapEntrySize:])
		switch {
		case e.ReturnAddr == addr:
			return e, true
		case e.ReturnAddr < addr:
			lo = mid + 1
		default:
			hi = mid
		}
	}
	return StackMapEntry{}, false
}

// DecodeStackMap returns every entry in a table EncodeStackMap built, in table order
// (by ReturnAddr, ascending). It exists for tests and tooling that need to inspect a
// whole table rather than look up one address.
func DecodeStackMap(table []byte) []StackMapEntry {
	n := len(table) / stackMapEntrySize
	out := make([]StackMapEntry, n)
	for i := range out {
		out[i] = decodeStackMapEntry(table[i*stackMapEntrySize:])
	}
	return out
}

func decodeStackMapEntry(b []byte) StackMapEntry {
	return StackMapEntry{
		ReturnAddr: binary.LittleEndian.Uint64(b[0:8]),
		RefOffset:  int32(binary.LittleEndian.Uint32(b[8:12])),
		RefCount:   int32(binary.LittleEndian.Uint32(b[12:16])),
		RegMask:    b[16],
	}
}
