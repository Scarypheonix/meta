package obj

import "io"

// ELF64 constants. They are spelled out rather than imported from debug/elf so that the
// writer and the test that reads the result back do not share a definition: a constant
// wrong in both places would cancel out and the test would pass.
const (
	elfHeaderSize     = 64
	elfProgHeaderSize = 56

	etExec      = 2  // a complete executable, not a relocatable object (ADR-0017)
	emX8664     = 62 // EM_X86_64
	ptLoad      = 1
	pfExec      = 1
	pfWrite     = 2
	pfRead      = 4
	elfClass64  = 2
	elfData2LSB = 1
	elfVersion  = 1
)

// writeELF writes a static ET_EXEC with two loadable segments.
//
// There is no section header table. Sections are what a linker consumes, and there is no
// linker; the kernel loads an executable by its program headers alone. A section table
// arrives with the DWARF line information, which is the first thing that needs one.
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
	f.u64(0)             // no section header table
	f.u32(0)             // no flags on x86-64
	f.u16(elfHeaderSize)
	f.u16(elfProgHeaderSize)
	f.u16(2) // two PT_LOAD segments
	f.u16(0) // no sections, so their size is irrelevant
	f.u16(0)
	f.u16(0)

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
