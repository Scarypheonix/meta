package obj

import "io"

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
func (img *Image) writeMachO(w io.Writer) error {
	t := img.Target
	textOff := img.TextAddr - t.Base
	roDataOff := img.RoDataAddr - t.Base
	dataOff := img.DataAddr - t.Base

	textSegFileSize := align(roDataOff+uint64(len(img.RoData)), t.PageSize)
	dataSegFileSize := uint64(len(img.Data))
	dataSegVMSize := align(dataSegFileSize+img.Bss, t.PageSize)

	f := &writer{}

	const ncmds = 4
	sizeofcmds := uint32(machoSegmentCmdSize + // __PAGEZERO
		machoSegmentCmdSize + machoSectionSize + // __TEXT
		machoSegmentCmdSize + machoSectionSize + // __DATA
		machoUnixThreadCmdSize)

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

	_, err := w.Write(f.buf)
	return err
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
