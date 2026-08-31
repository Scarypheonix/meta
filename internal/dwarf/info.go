package dwarf

import "encoding/binary"

// buildInfo encodes .debug_info: a compilation-unit header followed by the single
// compile-unit DIE that names the program's own address range and points at
// .debug_line (DW_AT_stmt_list) — the one fact `lldb` needs to find the right line
// program for a `--file` breakpoint at all.
//
// 32-bit DWARF (a 4-byte unit_length): the whole program is nowhere near 4 GiB of debug
// info, so the 64-bit DWARF escape (0xffffffff plus an 8-byte length) buys nothing.
func buildInfo(files []string, lowPC, highPC uint64) []byte {
	var body []byte
	body = binary.LittleEndian.AppendUint16(body, dwVersion4)
	body = binary.LittleEndian.AppendUint32(body, 0) // debug_abbrev_offset: the only table
	body = append(body, 8)                           // address_size: 8 bytes (x86-64)

	// The DIE itself: abbreviation code 1 (abbrev.go's only entry), then its attributes
	// in the exact order the abbreviation declared them.
	body = uleb128(body, 1)
	body = cString(body, files[0]) // DW_AT_name: the program's primary source file
	body = binary.LittleEndian.AppendUint64(body, lowPC)
	body = binary.LittleEndian.AppendUint64(body, highPC-lowPC) // DW_AT_high_pc as an offset (§2.17.2)
	body = binary.LittleEndian.AppendUint32(body, 0)            // DW_AT_stmt_list: offset 0 into .debug_line
	body = cString(body, "origin")                              // DW_AT_producer

	out := binary.LittleEndian.AppendUint32(nil, uint32(len(body)))
	return append(out, body...)
}

// cString appends s followed by a terminating zero byte, DW_FORM_string's own encoding.
func cString(b []byte, s string) []byte {
	b = append(b, s...)
	return append(b, 0)
}
