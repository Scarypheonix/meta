package dwarf

// LEB128 (Little Endian Base 128) is how DWARF encodes every variable-width integer:
// counts, offsets, signed line deltas. Both forms are self-terminating (the high bit of
// each byte says whether another follows), which is what lets a consumer walk a stream
// of them with no separate length field.

// uleb128 appends v as an unsigned LEB128.
func uleb128(b []byte, v uint64) []byte {
	for {
		c := byte(v & 0x7F)
		v >>= 7
		if v != 0 {
			c |= 0x80
		}
		b = append(b, c)
		if v == 0 {
			return b
		}
	}
}

// sleb128 appends v as a signed LEB128 — DWARF's own line-advance deltas need this,
// since a line number can move backward (a loop body textually below its own header).
func sleb128(b []byte, v int64) []byte {
	for {
		c := byte(v & 0x7F)
		v >>= 7 // arithmetic shift: sign-extends, which is exactly what this needs
		signBitSet := c&0x40 != 0
		done := (v == 0 && !signBitSet) || (v == -1 && signBitSet)
		if !done {
			c |= 0x80
		}
		b = append(b, c)
		if done {
			return b
		}
	}
}
