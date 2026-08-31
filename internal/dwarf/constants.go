package dwarf

// The DWARF4 constants this package actually uses. Named exactly as the standard names
// them (DWARF Debugging Information Format, Version 4, §7), so a reader cross-checking
// against the spec or against `llvm-dwarfdump`'s own output finds the same names.
const (
	dwVersion4 = 4

	// Tags (§7.5.3). Only one DIE kind exists in this phase's scope (ADR-0023).
	dwTagCompileUnit = 0x11

	// Children flag (§7.5.3): the compile-unit DIE has none — no subprogram DIEs yet.
	dwChildrenNo = 0x00

	// Attributes (§7.5.4).
	dwAtName     = 0x03
	dwAtLowPC    = 0x11
	dwAtHighPC   = 0x12
	dwAtStmtList = 0x10
	dwAtProducer = 0x25

	// Forms (§7.5.6).
	dwFormAddr   = 0x01 // an address, target-pointer-sized (8 bytes here)
	dwFormData8  = 0x07 // a fixed 8-byte constant
	dwFormString = 0x08 // an inline null-terminated string
	dwFormSecOff = 0x17 // an offset into another debug section (4 bytes, 32-bit DWARF)

	// lineBase and lineRange are the line-program header's own tuning parameters for
	// *special* opcodes (§6.2.5.1), which this package never emits (line.go). Their
	// values are only required to be present and valid, never actually consulted.
	lineBase  = 0xFB // -5 as a byte: line_base is nominally signed
	lineRange = 14

	// Standard line-number opcodes (§6.2.5.2). opcodeBase names the first free opcode
	// number for (unused, here) vendor extensions, and is also the length of the
	// standard_opcode_lengths table the line-program header carries.
	dwLnsCopy        = 0x01
	dwLnsAdvancePC   = 0x02
	dwLnsAdvanceLine = 0x03
	dwLnsSetFile     = 0x04
	dwLnsSetColumn   = 0x05
	dwLnsNegateStmt  = 0x06
	opcodeBase       = 13

	// Extended line-number opcodes (§6.2.5.3): a zero opcode byte, a ULEB128 length,
	// then the real opcode byte and its own operand.
	dwLneEndSequence = 0x01
	dwLneSetAddress  = 0x02
)

// standardOpcodeLengths is how many ULEB128 operands each standard opcode 1..opcodeBase-1
// takes, in order — required by the line-program header regardless of which opcodes this
// package actually emits, so a reader can skip an opcode it does not recognize.
var standardOpcodeLengths = []byte{0, 1, 1, 1, 1, 0, 0, 0, 1, 0, 0, 1}
