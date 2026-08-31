package obj

import (
	"encoding/binary"
	"io"
)

// Mach-O constants, spelled out for the same reason the ELF ones are: the test reads the
// result back with an independently written parser, and a constant wrong in both the
// writer and the reader would cancel out.
const (
	machoHeaderSize        = 32
	machoSegmentCmdSize    = 72
	machoSectionSize       = 80
	machoUnixThreadCmdSize = 8 + 4 + 4 + 21*8

	mhMagic64  = 0xFEEDFACF
	mhExecute  = 2
	mhNoUndefs = 0x1

	cpuTypeX8664    = 0x01000007
	cpuSubtypeX8664 = 3

	lcSegment64  = 0x19
	lcUnixThread = 0x5
	lcSymtab     = 0x2

	machoSymtabCmdSize = 24 // symtab_command: cmd, cmdsize, symoff, nsyms, stroff, strsize
	machoSymSize       = 16 // nlist_64: n_strx, n_type, n_sect, n_desc, n_value

	// nSect marks a symbol as defined in a section (n_sect names which one, 1-based
	// across every section in the file); nExt marks it externally visible, the nlist_64
	// convention `nm`/lldb expect for a symbol naming a function (§ "Symbol Table
	// Formats" in Apple's Mach-O reference).
	nSect = 0x0e
	nExt  = 0x01

	vmProtNone    = 0
	vmProtRead    = 1
	vmProtWrite   = 2
	vmProtExecute = 4

	// x86_THREAD_STATE64, counted in 32-bit words as the kernel counts it.
	x86ThreadState64      = 4
	x86ThreadState64Count = 42
	// ripIndex is where the instruction pointer sits in x86_thread_state64_t, counted in
	// 64-bit fields: rax rbx rcx rdx rdi rsi rbp rsp r8..r15 rip.
	ripIndex = 16

	sSectionRegular = 0x0
	sAttrPureInstr  = 0x80000000
	sAttrSomeInstr  = 0x00000400
)

// writeMachO writes a static MH_EXECUTE.
//
// The entry point is declared with LC_UNIXTHREAD rather than LC_MAIN. LC_MAIN is an
// offset that the dynamic loader jumps to after it has set a program up; this executable
// has no dynamic loader (ADR-0017), so it declares the initial thread state directly and
// the kernel starts it with rip already pointing at the entry stub.
//
// When the image carries ADR-0023's debug information, a third segment, __DWARF, and an
// LC_SYMTAB load command are appended: __DWARF is a real LC_SEGMENT_64 with three
// sections, one per DWARF section this package's internal/dwarf produced, but with
// vmaddr and vmsize both zero -- DWARF is read directly from the file by every consumer
// (lldb included), never from the running process's memory, so unlike __TEXT and __DATA
// this segment occupies no address-space range at all. Execution cannot be verified in
// this container (ADR-0003's own scope: the shipping target is macOS on a real Mac), so
// this is checked structurally, by reading it back with debug/macho.
func (img *Image) writeMachO(w io.Writer) error {
	t := img.Target
	textOff := img.TextAddr - t.Base
	roDataOff := img.RoDataAddr - t.Base
	dataOff := img.DataAddr - t.Base

	textSegFileSize := align(roDataOff+uint64(len(img.RoData)), t.PageSize)
	dataSegFileSize := uint64(len(img.Data))
	dataSegVMSize := align(dataSegFileSize+img.Bss, t.PageSize)

	hasDebug := len(img.DebugAbbrev) > 0 || len(img.Funcs) > 0
	var debug *machoDebugPlan
	if hasDebug {
		debug = buildMachODebug(img, dataOff+dataSegFileSize)
	}

	f := &writer{}

	ncmds := uint32(4)
	sizeofcmds := uint32(machoSegmentCmdSize + // __PAGEZERO
		machoSegmentCmdSize + machoSectionSize + // __TEXT
		machoSegmentCmdSize + machoSectionSize + // __DATA
		machoUnixThreadCmdSize)
	if debug != nil {
		ncmds += 2 // __DWARF, LC_SYMTAB
		sizeofcmds += machoSegmentCmdSize + 3*machoSectionSize + machoSymtabCmdSize
	}

	f.u32(mhMagic64)
	f.u32(cpuTypeX8664)
	f.u32(cpuSubtypeX8664)
	f.u32(mhExecute)
	f.u32(ncmds)
	f.u32(sizeofcmds)
	f.u32(mhNoUndefs)
	f.u32(0) // reserved

	// __PAGEZERO: macOS requires the whole low 4 GiB to be unmapped in a 64-bit
	// executable, which is why a Mach-O build is based at 0x100000000 and an ELF one at
	// 0x400000. It is the one place the two files' *addresses* differ, and the reason
	// the code generator must know its target before emitting a byte.
	machoSegment(f, "__PAGEZERO", 0, t.Base, 0, 0, vmProtNone, vmProtNone, 0)

	// __TEXT covers the headers, the code and the constants, mapped read+execute.
	machoSegment(f, "__TEXT", t.Base, textSegFileSize, 0, textSegFileSize,
		vmProtRead|vmProtExecute, vmProtRead|vmProtExecute, 1)
	machoSection(f, "__text", "__TEXT", img.TextAddr, uint64(len(img.Text)), uint32(textOff),
		4, sSectionRegular|sAttrPureInstr|sAttrSomeInstr)

	// __DATA is the runtime block and the static buffers.
	machoSegment(f, "__DATA", img.DataAddr, dataSegVMSize, dataOff, dataSegFileSize,
		vmProtRead|vmProtWrite, vmProtRead|vmProtWrite, 1)
	machoSection(f, "__data", "__DATA", img.DataAddr, dataSegFileSize, uint32(dataOff),
		3, sSectionRegular)

	if debug != nil {
		// vmaddr and vmsize are both zero: see the doc comment above for why this segment
		// claims no address-space range at all.
		machoSegment(f, "__DWARF", 0, 0, debug.contentOff, debug.dwarfSize, vmProtNone, vmProtNone, 3)
		machoSection(f, "__debug_abbrev", "__DWARF", 0, uint64(len(img.DebugAbbrev)), uint32(debug.abbrevOff), 0, sSectionRegular)
		machoSection(f, "__debug_info", "__DWARF", 0, uint64(len(img.DebugInfo)), uint32(debug.infoOff), 0, sSectionRegular)
		machoSection(f, "__debug_line", "__DWARF", 0, uint64(len(img.DebugLine)), uint32(debug.lineOff), 0, sSectionRegular)

		f.u32(lcSymtab)
		f.u32(machoSymtabCmdSize)
		f.u32(uint32(debug.symOff))
		f.u32(uint32(len(img.Funcs)))
		f.u32(uint32(debug.strOff))
		f.u32(uint32(len(debug.strtab)))
	}

	// LC_UNIXTHREAD: the initial register state. Everything is zero except rip.
	f.u32(lcUnixThread)
	f.u32(machoUnixThreadCmdSize)
	f.u32(x86ThreadState64)
	f.u32(x86ThreadState64Count)
	for i := 0; i < 21; i++ {
		if i == ripIndex {
			f.u64(img.Entry)
			continue
		}
		f.u64(0)
	}

	f.padTo(textOff)
	f.bytes(img.Text)
	f.padTo(roDataOff)
	f.bytes(img.RoData)
	if dataSegFileSize > 0 {
		f.padTo(dataOff)
		f.bytes(img.Data)
	} else if debug != nil {
		// buildMachODebug computed every appended section's offset starting at
		// dataOff + len(img.Data); with no data bytes to write, the file must still
		// reach dataOff before the debug content begins, or the two disagree.
		f.padTo(dataOff)
	}
	if debug != nil {
		f.bytes(debug.content)
	}

	_, err := w.Write(f.buf)
	return err
}

// machoDebugPlan is buildMachODebug's output: the file layout for everything past the two
// loadable segments, computed up front the same way elf.go's elfSectionPlan is -- every
// size here is known from img alone, so the load commands (which name these offsets) can
// be written before any of these bytes exist.
type machoDebugPlan struct {
	contentOff                  uint64
	dwarfSize                   uint64 // __debug_abbrev + __debug_info + __debug_line combined
	abbrevOff, infoOff, lineOff uint64
	symOff, strOff              uint64
	// content is the three DWARF sections, the nlist_64 symbol table and the string
	// table, back to back, in exactly the order their offsets above claim.
	content []byte
	strtab  []byte
}

// buildMachODebug lays out the nlist_64 symbol table (one STT_FUNC-equivalent global
// per img.Funcs entry, naming the same address range the ELF symtab does) and the three
// sections internal/dwarf built (ADR-0023).
func buildMachODebug(img *Image, contentOff uint64) *machoDebugPlan {
	abbrevOff := contentOff
	infoOff := abbrevOff + uint64(len(img.DebugAbbrev))
	lineOff := infoOff + uint64(len(img.DebugInfo))
	symOff := lineOff + uint64(len(img.DebugLine))

	// nlist_64's string table uses the same convention ELF's SHT_STRTAB does: index 0 is
	// the mandatory empty string.
	strtab := []byte{0}
	var symtab []byte
	for _, fn := range img.Funcs {
		nameOff := uint32(len(strtab))
		strtab = append(strtab, fn.Name...)
		strtab = append(strtab, 0)

		symtab = binary.LittleEndian.AppendUint32(symtab, nameOff)
		symtab = append(symtab, nSect|nExt)                  // n_type
		symtab = append(symtab, 1)                           // n_sect: __text is always section 1 (the first section in load-command order)
		symtab = binary.LittleEndian.AppendUint16(symtab, 0) // n_desc: unused
		symtab = binary.LittleEndian.AppendUint64(symtab, fn.Address)
	}
	strOff := symOff + uint64(len(symtab))

	var content []byte
	content = append(content, img.DebugAbbrev...)
	content = append(content, img.DebugInfo...)
	content = append(content, img.DebugLine...)
	content = append(content, symtab...)
	content = append(content, strtab...)

	return &machoDebugPlan{
		contentOff: contentOff,
		dwarfSize:  uint64(len(img.DebugAbbrev) + len(img.DebugInfo) + len(img.DebugLine)),
		abbrevOff:  abbrevOff, infoOff: infoOff, lineOff: lineOff,
		symOff: symOff, strOff: strOff,
		content: content, strtab: strtab,
	}
}

func machoSegment(f *writer, name string, vmaddr, vmsize, fileoff, filesize uint64, maxprot, initprot uint32, nsects uint32) {
	f.u32(lcSegment64)
	f.u32(uint32(machoSegmentCmdSize + machoSectionSize*nsects))
	f.name(name, 16)
	f.u64(vmaddr)
	f.u64(vmsize)
	f.u64(fileoff)
	f.u64(filesize)
	f.u32(maxprot)
	f.u32(initprot)
	f.u32(nsects)
	f.u32(0) // no segment flags
}

func machoSection(f *writer, sect, seg string, addr, size uint64, offset, alignPow uint32, flags uint32) {
	f.name(sect, 16)
	f.name(seg, 16)
	f.u64(addr)
	f.u64(size)
	f.u32(offset)
	f.u32(alignPow)
	f.u32(0) // no relocations: every address is already resolved (ADR-0017)
	f.u32(0)
	f.u32(flags)
	f.u32(0)
	f.u32(0)
	f.u32(0)
}
