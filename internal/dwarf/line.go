package dwarf

import "encoding/binary"

// buildLineProgram encodes .debug_line: one line-number program, covering the whole
// program's own .text (there is exactly one compile unit, ADR-0023). Only the standard
// and extended opcodes are used, never a *special* opcode (DWARF's single-byte combined
// address+line-advance form) — a real compactness optimization the spec allows skipping,
// so line_base and line_range below are present only because the header format requires
// some value, never actually consulted by a program that never emits opcode >= 13.
func buildLineProgram(lines []Line, files []string, endAddr uint64) []byte {
	lines = sortLines(lines)
	header := lineProgramHeader(files)
	program := lineProgramBody(lines, files, endAddr)

	var body []byte
	body = binary.LittleEndian.AppendUint16(body, dwVersion4)
	body = binary.LittleEndian.AppendUint32(body, uint32(len(header)))
	body = append(body, header...)
	body = append(body, program...)

	out := binary.LittleEndian.AppendUint32(nil, uint32(len(body)))
	return append(out, body...)
}

// lineProgramHeader is everything from minimum_instruction_length through the
// file_names table's own terminator — exactly the span header_length itself measures.
func lineProgramHeader(files []string) []byte {
	var h []byte
	h = append(h, 1)         // minimum_instruction_length
	h = append(h, 1)         // maximum_operations_per_instruction (DWARF4)
	h = append(h, 1)         // default_is_stmt
	h = append(h, lineBase)  // line_base (unused: no special opcodes)
	h = append(h, lineRange) // line_range (unused: no special opcodes)
	h = append(h, opcodeBase)
	h = append(h, standardOpcodeLengths...)
	h = append(h, 0) // include_directories: none, just the terminator

	for _, f := range files {
		h = cString(h, f)
		h = uleb128(h, 0) // directory index: 0, no directory table
		h = uleb128(h, 0) // mtime: not tracked
		h = uleb128(h, 0) // file length: not tracked
	}
	h = append(h, 0) // file_names terminator: an entry with an empty name

	return h
}

// lineProgramBody is the actual opcode stream: for every row, an absolute address (so
// each row is independent — no reliance on DW_LNS_advance_pc's own instruction-length
// scaling, which this package does not use), the file and line if either changed, the
// column, and DW_LNS_copy to commit the row. DW_LNE_end_sequence closes the program at
// endAddr, marking everything from the last row's address up to there as still part of
// the program (required: a sequence must end with this, one past the last valid byte).
func lineProgramBody(lines []Line, files []string, endAddr uint64) []byte {
	var p []byte
	curFile, curLine := 1, 1
	for _, ln := range lines {
		p = setAddress(p, ln.Address)

		if fi := fileIndex(files, ln.File); fi != curFile {
			p = append(p, dwLnsSetFile)
			p = uleb128(p, uint64(fi))
			curFile = fi
		}
		if ln.Line != curLine {
			p = append(p, dwLnsAdvanceLine)
			p = sleb128(p, int64(ln.Line-curLine))
			curLine = ln.Line
		}
		p = append(p, dwLnsSetColumn)
		p = uleb128(p, uint64(ln.Col))
		p = append(p, dwLnsCopy)
	}

	p = setAddress(p, endAddr)
	p = append(p, 0) // extended opcode marker
	p = uleb128(p, 1)
	p = append(p, dwLneEndSequence)
	return p
}

// setAddress emits DW_LNE_set_address: an extended opcode with a fixed 8-byte operand,
// never a ULEB128 — the reason a line table built from nothing but .text addresses is
// byte-identical on both of backend.Build's passes with no separate length check needed
// (ADR-0023): the operand's *encoded width* can never vary with its value.
func setAddress(p []byte, addr uint64) []byte {
	p = append(p, 0) // extended opcode marker
	p = uleb128(p, 1+8)
	p = append(p, dwLneSetAddress)
	return binary.LittleEndian.AppendUint64(p, addr)
}
