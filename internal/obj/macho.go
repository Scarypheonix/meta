package obj

import (
	"encoding/binary"
	"fmt"
	"io"

	"github.com/scarypheonix/meta/internal/codesign"
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

	lcSegment64     = 0x19
	lcUnixThread    = 0x5
	lcSymtab        = 0x2
	lcCodeSignature = 0x1d

	machoSymtabCmdSize  = 24 // symtab_command: cmd, cmdsize, symoff, nsyms, stroff, strsize
	machoSymSize        = 16 // nlist_64: n_strx, n_type, n_sect, n_desc, n_value
	machoCodeSigCmdSize = 16 // linkedit_data_command: cmd, cmdsize, dataoff, datasize

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
// Every Mach-O also carries a `__LINKEDIT` segment, and it MUST be the file's final
// segment: macOS's own loader and every signing implementation assume it (ADR-0024).
// It holds the symbol table, the string table, and the ad-hoc code signature — which is
// not optional either, since macOS 11 the kernel SIGKILLs any executable with no valid
// signature at all, and a hand-written executable gets none for free the way one built
// through `ld` does.
//
// When the image carries ADR-0023's debug information, a `__DWARF` segment holds the
// three sections internal/dwarf produced. Unlike a `.o` file's own `__DWARF`, this one
// takes a real, non-overlapping address range like every other segment here: a second
// segment claiming vmaddr 0 collides with `__PAGEZERO`'s own [0, 4 GiB) claim, which
// macOS's `codesign` rejects outright ("internal error in Code Signing subsystem").
func (img *Image) writeMachO(w io.Writer) error {
	t := img.Target
	textOff := img.TextAddr - t.Base
	roDataOff := img.RoDataAddr - t.Base
	dataOff := img.DataAddr - t.Base

	textSegFileSize := align(roDataOff+uint64(len(img.RoData)), t.PageSize)
	dataSegFileSize := uint64(len(img.Data))
	dataSegVMSize := align(dataSegFileSize+img.Bss, t.PageSize)

	trailer := buildMachOTrailer(img, dataOff+dataSegFileSize)

	f := &writer{}

	ncmds := uint32(6)                         // __PAGEZERO, __TEXT, __DATA, __LINKEDIT, LC_SYMTAB, LC_UNIXTHREAD
	sizeofcmds := uint32(machoSegmentCmdSize + // __PAGEZERO
		machoSegmentCmdSize + machoSectionSize + // __TEXT
		machoSegmentCmdSize + machoSectionSize + // __DATA
		machoSegmentCmdSize + // __LINKEDIT, which carries no sections
		machoSymtabCmdSize +
		machoUnixThreadCmdSize)
	if trailer.dwarfSize > 0 {
		ncmds++
		sizeofcmds += machoSegmentCmdSize + 3*machoSectionSize
	}
	if trailer.sigSize > 0 {
		ncmds++
		sizeofcmds += machoCodeSigCmdSize
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

	if trailer.dwarfSize > 0 {
		machoSegment(f, "__DWARF", t.Base+trailer.dwarfOff, align(trailer.dwarfSize, t.PageSize),
			trailer.dwarfOff, trailer.dwarfSize, vmProtRead, vmProtRead, 3)
		machoSection(f, "__debug_abbrev", "__DWARF", t.Base+trailer.abbrevOff,
			uint64(len(img.DebugAbbrev)), uint32(trailer.abbrevOff), 0, sSectionRegular)
		machoSection(f, "__debug_info", "__DWARF", t.Base+trailer.infoOff,
			uint64(len(img.DebugInfo)), uint32(trailer.infoOff), 0, sSectionRegular)
		machoSection(f, "__debug_line", "__DWARF", t.Base+trailer.lineOff,
			uint64(len(img.DebugLine)), uint32(trailer.lineOff), 0, sSectionRegular)
	}

	// __LINKEDIT must be the final segment of any Mach-O (ADR-0024). It carries no
	// sections: the symbol table and the code signature inside it are described by their
	// own load commands' file offsets, not by section headers.
	machoSegment(f, "__LINKEDIT", t.Base+trailer.linkeditOff, align(trailer.linkeditSize, t.PageSize),
		trailer.linkeditOff, trailer.linkeditSize, vmProtRead, vmProtRead, 0)

	f.u32(lcSymtab)
	f.u32(machoSymtabCmdSize)
	f.u32(uint32(trailer.symOff))
	f.u32(uint32(len(img.Funcs)))
	f.u32(uint32(trailer.strOff))
	f.u32(uint32(trailer.strSize))

	if trailer.sigSize > 0 {
		// LC_CODE_SIGNATURE, a linkedit_data_command: where inside __LINKEDIT the ad-hoc
		// signature blob sits.
		f.u32(lcCodeSignature)
		f.u32(machoCodeSigCmdSize)
		f.u32(uint32(trailer.sigOff))
		f.u32(uint32(trailer.sigSize))
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
	}
	if trailer.dwarfSize > 0 {
		f.padTo(trailer.dwarfOff)
		f.bytes(trailer.dwarfContent)
	}
	f.padTo(trailer.linkeditOff)
	f.bytes(trailer.linkeditContent)

	// The signature goes last, over a file whose every other byte -- including the header
	// fields naming this signature's own offset and size -- is already final.
	f.padTo(trailer.sigOff)
	sig := codesign.Build(f.buf, img.identifier(), trailer.sigOff, 0, textSegFileSize)
	if uint64(len(sig)) != trailer.sigSize {
		return fmt.Errorf(
			"this is a compiler bug: the code signature is %d bytes but the layout reserved %d; "+
				"codesign.Size must agree with codesign.Build exactly, since the reservation is "+
				"baked into the header the signature itself covers",
			len(sig), trailer.sigSize)
	}
	f.bytes(sig)

	_, err := w.Write(f.buf)
	return err
}

// machoTrailer is the file layout past __DATA: the optional __DWARF segment (ADR-0023)
// and the mandatory __LINKEDIT one (ADR-0024). It is computed up front, before a single
// byte of the file is written, the same way textOff/roDataOff/dataOff already are — every
// size here is known from img alone, and the load commands naming these offsets are
// written long before the bytes they point at exist.
//
// Each region is page-aligned, and each segment's vmaddr keeps this file's own convention
// that a segment's address is `Base + its file offset`: monotonic offsets therefore give
// monotonic, non-overlapping addresses for free.
type machoTrailer struct {
	// __DWARF, absent (all zero) when the image carries no debug information.
	dwarfOff, dwarfSize         uint64
	abbrevOff, infoOff, lineOff uint64
	dwarfContent                []byte

	// __LINKEDIT: the symbol table, the string table, then the code signature.
	linkeditOff, linkeditSize uint64
	symOff, strOff, strSize   uint64
	sigOff, sigSize           uint64
	linkeditContent           []byte
}

// buildMachOTrailer lays out the nlist_64 symbol table (one global per img.Funcs entry,
// naming the same addresses the ELF symtab does), the three sections internal/dwarf built,
// and the ad-hoc code signature — everything that follows the two loadable segments.
func buildMachOTrailer(img *Image, after uint64) *machoTrailer {
	t := img.Target
	tr := &machoTrailer{}

	if len(img.DebugAbbrev) > 0 {
		tr.dwarfOff = align(after, t.PageSize)
		tr.abbrevOff = tr.dwarfOff
		tr.infoOff = tr.abbrevOff + uint64(len(img.DebugAbbrev))
		tr.lineOff = tr.infoOff + uint64(len(img.DebugInfo))
		tr.dwarfSize = uint64(len(img.DebugAbbrev) + len(img.DebugInfo) + len(img.DebugLine))

		tr.dwarfContent = append(tr.dwarfContent, img.DebugAbbrev...)
		tr.dwarfContent = append(tr.dwarfContent, img.DebugInfo...)
		tr.dwarfContent = append(tr.dwarfContent, img.DebugLine...)
		after = tr.dwarfOff + tr.dwarfSize
	}

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
		symtab = append(symtab, 1)                           // n_sect: __text is always section 1
		symtab = binary.LittleEndian.AppendUint16(symtab, 0) // n_desc: unused
		symtab = binary.LittleEndian.AppendUint64(symtab, fn.Address)
	}

	tr.linkeditOff = align(after, t.PageSize)
	tr.symOff = tr.linkeditOff
	tr.strOff = tr.symOff + uint64(len(symtab))
	tr.strSize = uint64(len(strtab))
	tr.linkeditContent = append(tr.linkeditContent, symtab...)
	tr.linkeditContent = append(tr.linkeditContent, strtab...)

	// The signature covers everything before it and is the last thing in the file
	// (ADR-0024), so its own start is its codeLimit. Its size follows from that and the
	// identifier alone, which is what lets __LINKEDIT's size and LC_CODE_SIGNATURE -- both
	// written into the header, inside the region the signature hashes -- be final before
	// any hashing happens.
	tr.sigOff = align(tr.linkeditOff+uint64(len(tr.linkeditContent)), 16)
	tr.sigSize = codesign.Size(img.identifier(), tr.sigOff)
	tr.linkeditSize = tr.sigOff + tr.sigSize - tr.linkeditOff

	return tr
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
