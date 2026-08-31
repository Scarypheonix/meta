package dwarf

// buildAbbrev encodes .debug_abbrev: one abbreviation declaration, for the single
// DW_TAG_compile_unit DIE .debug_info ever contains (ADR-0023 — no subprogram, variable
// or type DIEs yet). An abbreviation table entry is: a ULEB128 code (how .debug_info's
// own DIE refers back to this declaration), the tag, the children flag, then
// (attribute, form) ULEB128 pairs terminated by (0, 0); the whole table is terminated by
// a single zero byte where the next entry's code would be.
func buildAbbrev() []byte {
	var b []byte
	b = uleb128(b, 1) // abbreviation code 1 — the only one .debug_info's DIE ever names
	b = uleb128(b, dwTagCompileUnit)
	b = append(b, dwChildrenNo)

	b = attr(b, dwAtName, dwFormString)
	b = attr(b, dwAtLowPC, dwFormAddr)
	b = attr(b, dwAtHighPC, dwFormData8) // an 8-byte length, added to low_pc (DWARF4 §2.17.2)
	b = attr(b, dwAtStmtList, dwFormSecOff)
	b = attr(b, dwAtProducer, dwFormString)
	b = uleb128(b, 0) // (attribute, form) terminator
	b = uleb128(b, 0)

	b = uleb128(b, 0) // table terminator: the next abbreviation code, if any, is 0
	return b
}

func attr(b []byte, at, form uint64) []byte {
	b = uleb128(b, at)
	return uleb128(b, form)
}
