package obj

import (
	"encoding/binary"
	"io"
)

// ELF64 constants. They are spelled out rather than imported from debug/elf so that the
// writer and the test that reads the result back do not share a definition: a constant
// wrong in both places would cancel out and the test would pass.
const (
	elfHeaderSize     = 64
	elfProgHeaderSize = 56
	elfShdrSize       = 64
	elfSymSize        = 24

	etExec      = 2  // a complete executable, not a relocatable object (ADR-0017)
	emX8664     = 62 // EM_X86_64
	ptLoad      = 1
	pfExec      = 1
	pfWrite     = 2
	pfRead      = 4
	elfClass64  = 2
	elfData2LSB = 1
	elfVersion  = 1

	shtProgbits = 1
	shtSymtab   = 2
	shtStrtab   = 3

	shfWrite     = 0x1
	shfAlloc     = 0x2
	shfExecInstr = 0x4

	sttFunc   = 2
	stbGlobal = 1
)

// writeELF writes a static ET_EXEC with two loadable segments, plus -- when the image
// carries ADR-0023's debug information -- a section header table, a symbol table, and the
// three DWARF sections, all appended after the loadable segments and reachable only
// through the section table (never mapped by a program header: the kernel loads this
// executable by its program headers alone, exactly as before; a debugger reads sections
// directly from the file). An image with no debug information gets exactly the file this
// function always wrote: two PT_LOAD segments and nothing past them.
func (img *Image) writeELF(w io.Writer) error {
	t := img.Target
	textOff := img.TextAddr - t.Base
	roDataOff := img.RoDataAddr - t.Base
	dataOff := img.DataAddr - t.Base

	// The first segment covers the headers as well as the code: a mapping is
	// page-granular and starts at file offset zero anyway, so the headers ride along
	// rather than costing a page of their own.
	textSegFileSize := roDataOff + uint64(len(img.RoData))
	dataSegFileSize := uint64(len(img.Data))

	hasDebug := len(img.DebugAbbrev) > 0 || len(img.Funcs) > 0
	var sections *elfSectionPlan
	if hasDebug {
		sections = buildELFSections(img, textOff, roDataOff, dataOff)
	}

	f := &writer{}

	// ELF identification.
	f.bytes([]byte{0x7F, 'E', 'L', 'F'})
	f.u8(elfClass64)
	f.u8(elfData2LSB)
	f.u8(elfVersion)
	f.u8(0) // System V ABI
	f.u8(0) // ABI version
	f.pad(7)

	f.u16(etExec)
	f.u16(emX8664)
	f.u32(elfVersion)
	f.u64(img.Entry)
	f.u64(elfHeaderSize) // program headers follow the ELF header
	if sections != nil {
		f.u64(sections.tableOff)
	} else {
		f.u64(0) // no section header table
	}
	f.u32(0) // no flags on x86-64
	f.u16(elfHeaderSize)
	f.u16(elfProgHeaderSize)
	f.u16(2) // two PT_LOAD segments
	if sections != nil {
		f.u16(elfShdrSize)
		f.u16(sections.numSections)
		f.u16(sections.shstrndx)
	} else {
		f.u16(0) // no sections, so their size is irrelevant
		f.u16(0)
		f.u16(0)
	}

	// The read+execute segment: headers, code, and constants.
	elfProgHeader(f, ptLoad, pfRead|pfExec, 0, t.Base, textSegFileSize, textSegFileSize, t.PageSize)
	// The read+write segment: the runtime block and static buffers, plus the zero-filled
	// tail that costs nothing in the file.
	elfProgHeader(f, ptLoad, pfRead|pfWrite, dataOff, img.DataAddr,
		dataSegFileSize, dataSegFileSize+img.Bss, t.PageSize)

	f.padTo(textOff)
	f.bytes(img.Text)
	f.padTo(roDataOff)
	f.bytes(img.RoData)
	if dataSegFileSize > 0 {
		f.padTo(dataOff)
		f.bytes(img.Data)
	} else if sections != nil {
		// buildELFSections computed every appended section's offset starting at
		// dataOff + len(img.Data); with no data bytes to write, the file must still
		// reach dataOff before the debug content begins, or the two disagree.
		f.padTo(dataOff)
	}

	if sections != nil {
		f.bytes(sections.content)
		f.bytes(sections.table)
	}

	_, err := w.Write(f.buf)
	return err
}

func elfProgHeader(f *writer, typ, flags uint32, off, vaddr, filesz, memsz, align uint64) {
	f.u32(typ)
	f.u32(flags)
	f.u64(off)
	f.u64(vaddr)
	f.u64(vaddr) // physical address: unused by a user-space loader, conventionally equal
	f.u64(filesz)
	f.u64(memsz)
	f.u64(align)
}

// elfSectionPlan is everything writeELF needs about the section header table before it can
// write the ELF header (which names the table's own file offset and size) and after the
// two loadable segments have been written (where the table's content actually lives).
// Computing it in one pass, before any byte of the file is written, is the same trick
// textOff/roDataOff/dataOff already use: every size here is known from img alone.
type elfSectionPlan struct {
	tableOff    uint64
	numSections uint16
	shstrndx    uint16
	// content is the DWARF sections, the symbol table and the two string tables, back to
	// back, in exactly the order their section headers claim; table is the Elf64_Shdr
	// array itself. Both are appended to the file after the two loadable segments.
	content []byte
	table   []byte
}

// strTable is an ELF string table under construction: SHT_STRTAB's own format, a run of
// null-terminated strings with a mandatory empty string at offset 0.
type strTable struct{ buf []byte }

func newStrTable() *strTable { return &strTable{buf: []byte{0}} }

func (s *strTable) add(str string) uint32 {
	off := uint32(len(s.buf))
	s.buf = append(s.buf, str...)
	s.buf = append(s.buf, 0)
	return off
}

// buildELFSections lays out the symbol table (one STT_FUNC per img.Funcs entry, naming the
// address range `bt` should resolve to that function), the three sections
// internal/dwarf built (ADR-0023), and the section header table that describes all nine
// real sections plus the mandatory SHT_NULL at index 0. The three segment-backed sections
// (.text, .rodata, .data) point back at bytes writeELF already wrote at textOff/roDataOff/
// dataOff; nothing here duplicates them.
func buildELFSections(img *Image, textOff, roDataOff, dataOff uint64) *elfSectionPlan {
	const (
		secText = 1 + iota
		secRoData
		secData
		secDebugAbbrev
		secDebugInfo
		secDebugLine
		secSymtab
		secStrtab
		secShstrtab
		numSections
	)

	shstrtab := newStrTable()
	nameText := shstrtab.add(".text")
	nameRoData := shstrtab.add(".rodata")
	nameData := shstrtab.add(".data")
	nameDebugAbbrev := shstrtab.add(".debug_abbrev")
	nameDebugInfo := shstrtab.add(".debug_info")
	nameDebugLine := shstrtab.add(".debug_line")
	nameSymtab := shstrtab.add(".symtab")
	nameStrtab := shstrtab.add(".strtab")
	nameShstrtab := shstrtab.add(".shstrtab")

	strtab := newStrTable()
	var symtab []byte
	symtab = append(symtab, make([]byte, elfSymSize)...) // index 0: the mandatory null symbol
	for _, fn := range img.Funcs {
		nameOff := strtab.add(fn.Name)
		symtab = binary.LittleEndian.AppendUint32(symtab, nameOff)
		symtab = append(symtab, byte(stbGlobal<<4|sttFunc), 0)
		symtab = binary.LittleEndian.AppendUint16(symtab, secText)
		symtab = binary.LittleEndian.AppendUint64(symtab, fn.Address)
		symtab = binary.LittleEndian.AppendUint64(symtab, fn.Size)
	}

	contentOff := dataOff + uint64(len(img.Data))
	debugAbbrevOff := contentOff
	debugInfoOff := debugAbbrevOff + uint64(len(img.DebugAbbrev))
	debugLineOff := debugInfoOff + uint64(len(img.DebugInfo))
	symtabOff := debugLineOff + uint64(len(img.DebugLine))
	strtabOff := symtabOff + uint64(len(symtab))
	shstrtabOff := strtabOff + uint64(len(strtab.buf))
	tableOff := shstrtabOff + uint64(len(shstrtab.buf))

	type shdr struct {
		name         uint32
		typ          uint32
		flags        uint64
		addr         uint64
		off          uint64
		size         uint64
		link, info   uint32
		align, entsz uint64
	}
	shdrs := make([]shdr, numSections)
	shdrs[secText] = shdr{name: nameText, typ: shtProgbits, flags: shfAlloc | shfExecInstr,
		addr: img.TextAddr, off: textOff, size: uint64(len(img.Text)), align: 16}
	shdrs[secRoData] = shdr{name: nameRoData, typ: shtProgbits, flags: shfAlloc,
		addr: img.RoDataAddr, off: roDataOff, size: uint64(len(img.RoData)), align: 1}
	shdrs[secData] = shdr{name: nameData, typ: shtProgbits, flags: shfAlloc | shfWrite,
		addr: img.DataAddr, off: dataOff, size: uint64(len(img.Data)), align: 8}
	shdrs[secDebugAbbrev] = shdr{name: nameDebugAbbrev, typ: shtProgbits,
		off: debugAbbrevOff, size: uint64(len(img.DebugAbbrev)), align: 1}
	shdrs[secDebugInfo] = shdr{name: nameDebugInfo, typ: shtProgbits,
		off: debugInfoOff, size: uint64(len(img.DebugInfo)), align: 1}
	shdrs[secDebugLine] = shdr{name: nameDebugLine, typ: shtProgbits,
		off: debugLineOff, size: uint64(len(img.DebugLine)), align: 1}
	shdrs[secSymtab] = shdr{name: nameSymtab, typ: shtSymtab,
		off: symtabOff, size: uint64(len(symtab)), link: uint32(secStrtab), info: 1,
		align: 8, entsz: elfSymSize}
	shdrs[secStrtab] = shdr{name: nameStrtab, typ: shtStrtab,
		off: strtabOff, size: uint64(len(strtab.buf)), align: 1}
	shdrs[secShstrtab] = shdr{name: nameShstrtab, typ: shtStrtab,
		off: shstrtabOff, size: uint64(len(shstrtab.buf)), align: 1}

	var content []byte
	content = append(content, img.DebugAbbrev...)
	content = append(content, img.DebugInfo...)
	content = append(content, img.DebugLine...)
	content = append(content, symtab...)
	content = append(content, strtab.buf...)
	content = append(content, shstrtab.buf...)

	table := &writer{}
	for _, s := range shdrs {
		table.u32(s.name)
		table.u32(s.typ)
		table.u64(s.flags)
		table.u64(s.addr)
		table.u64(s.off)
		table.u64(s.size)
		table.u32(s.link)
		table.u32(s.info)
		table.u64(s.align)
		table.u64(s.entsz)
	}

	return &elfSectionPlan{
		tableOff: tableOff, numSections: numSections, shstrndx: secShstrtab,
		content: content, table: table.buf,
	}
}
