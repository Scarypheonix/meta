// Package obj writes executable files.
//
// ADR-0017 makes these *complete* executables, not relocatable objects: there is no
// linker, so what this package writes is what the kernel maps and jumps into. Every
// address is already decided by the time an Image reaches here — the code generator knew
// where its own bytes would live — so there is no relocation table in either format.
//
// Two formats, one instruction stream (ADR-0003). A byte in Image.Text is the same byte
// in both files; the writers differ in headers, segment tables and how the entry point is
// declared, and in nothing else. That is why this is not cross-compilation: there is one
// target architecture and one encoder, and two ways to wrap the result so that two
// operating systems will load it.
package obj

import (
	"encoding/binary"
	"fmt"
	"io"

	"github.com/scarypheonix/meta/internal/dwarf"
)

// Format is an executable file format.
type Format int

const (
	// ELF is the Linux format. It is the verification path: the container the compiler
	// is developed in can run one, so the end-to-end differential can compare native
	// output against the interpreter's.
	ELF Format = iota
	// MachO is the macOS format, and the one Origin ships: the target machine is a
	// 2017 MacBook Air running Monterey.
	MachO
)

func (f Format) String() string {
	switch f {
	case ELF:
		return "elf"
	case MachO:
		return "macho"
	}
	return fmt.Sprintf("format(%d)", int(f))
}

// Target is everything about a platform that the compiler must know before it emits a
// single byte, because ADR-0017's freestanding executables bake addresses in.
//
// It is deliberately tiny. docs/spec/11-codegen.md claims the two builds differ only in
// where they are loaded, in their headers, and in syscall numbers; this struct is that
// claim written down, and anything that has to be added here later is a place the claim
// was too strong.
type Target struct {
	Format Format
	// Base is the virtual address the file is mapped at. macOS reserves the whole first
	// 4 GiB for __PAGEZERO on 64-bit, so a Mach-O executable cannot live where an ELF
	// one does.
	Base uint64
	// PageSize is the mapping granularity that segment boundaries must respect.
	PageSize uint64
	// SysWrite, SysExit and SysMmap are the syscall numbers. macOS ORs the BSD class
	// 0x2000000 into every one of them.
	SysWrite uint64
	SysExit  uint64
	SysMmap  uint64
	// The file operations (spec/15-files.md). `lseek` is what gives a file's size without
	// an `fstat`, whose struct layout is one of the few things the two systems genuinely
	// disagree about -- seeking to the end and back is portable arithmetic instead.
	SysOpen  uint64
	SysRead  uint64
	SysClose uint64
	SysLseek uint64
	// OpenWriteFlags is O_WRONLY|O_CREAT|O_TRUNC, which the two systems number
	// differently for O_CREAT and O_TRUNC.
	OpenWriteFlags uint64
	// ErrNotFound and ErrPermission are the errno values spec/15-files.md's first two
	// statuses correspond to. A syscall reports failure as -errno in rax on both systems.
	ErrNotFound   uint64
	ErrPermission uint64
	// MapAnonPrivate is the mmap flag pair for a private anonymous mapping, which is
	// how the runtime asks for a heap. The two systems number MAP_ANONYMOUS
	// differently, so it joins the syscall numbers as a per-target constant.
	MapAnonPrivate uint64
}

// Linux is the x86-64 Linux target.
var Linux = Target{
	Format:   ELF,
	Base:     0x400000,
	PageSize: 0x1000,
	SysWrite: 1,
	SysExit:  231, // exit_group: exits every thread, which is what a program's end means
	SysMmap:  9,
	SysOpen:  2,
	SysRead:  0,
	SysClose: 3,
	SysLseek: 8,
	// O_WRONLY | O_CREAT | O_TRUNC
	OpenWriteFlags: 0o1 | 0o100 | 0o1000,
	ErrNotFound:    2,  // ENOENT
	ErrPermission:  13, // EACCES
	// MAP_PRIVATE | MAP_ANONYMOUS
	MapAnonPrivate: 0x02 | 0x20,
}

// MacOS is the x86-64 macOS target.
var MacOS = Target{
	Format:   MachO,
	Base:     0x100000000, // above __PAGEZERO, which macOS requires to cover the first 4 GiB
	PageSize: 0x1000,
	SysWrite: 0x2000004,
	SysExit:  0x2000001,
	SysMmap:  0x20000C5,
	SysOpen:  0x2000005,
	SysRead:  0x2000003,
	SysClose: 0x2000006,
	SysLseek: 0x20000C7,
	// O_WRONLY | O_CREAT | O_TRUNC, which BSD numbers differently from Linux
	OpenWriteFlags: 0x0001 | 0x0200 | 0x0400,
	ErrNotFound:    2,  // ENOENT
	ErrPermission:  13, // EACCES
	// MAP_PRIVATE | MAP_ANON
	MapAnonPrivate: 0x0002 | 0x1000,
}

// TargetFor returns the target for a format name, as `originc build --target` spells it.
func TargetFor(name string) (Target, error) {
	switch name {
	case "linux", "elf":
		return Linux, nil
	case "macos", "darwin", "macho":
		return MacOS, nil
	}
	return Target{}, fmt.Errorf("unknown target %q: the targets are `linux` and `macos`", name)
}

// Image is a laid-out program: the bytes of each segment and the addresses they were
// generated for.
//
// The code generator produces one by asking Layout where things will go, emitting into
// that layout, and filling in the segments. Nothing here recomputes an address; a
// mismatch between this and what the code assumed is a bug in the caller, and Validate
// catches the ones that are visible from here.
type Image struct {
	Target Target
	// Text is executable code, mapped read+execute.
	Text []byte
	// RoData is constant data — string literals, stack maps — mapped read-only. It
	// shares a segment with the text, because a segment is a page-granular mapping and
	// two of them for a few kilobytes of constants is a page wasted per program.
	RoData []byte
	// Data is writable data: the runtime block and any static buffers.
	Data []byte
	// Bss is writable space that is zero at start-up and takes no room in the file.
	Bss uint64
	// Entry is the virtual address the kernel jumps to.
	Entry uint64

	// The addresses Layout assigned, which the code generator baked into the code.
	TextAddr   uint64
	RoDataAddr uint64
	DataAddr   uint64

	// DebugAbbrev, DebugInfo and DebugLine are ADR-0023's DWARF4 sections -- a line-number
	// program built entirely from addresses inside Text, plus the one compile-unit DIE
	// that anchors it. Nil (the zero value) is valid: a build that never asked for debug
	// info just omits the sections and the debugging directories/segments that describe
	// them, the same way Bss being zero omits nothing about a normal build.
	DebugAbbrev, DebugInfo, DebugLine []byte
	// Funcs is one entry per compiled function, for the plain symbol table `bt` names a
	// frame from (never a DWARF subprogram DIE -- see internal/dwarf's package doc).
	Funcs []dwarf.Func

	// Identifier names the code a Mach-O's ad-hoc signature covers (ADR-0024).
	// Conventionally the output file's base name, which is what `codesign` itself
	// defaults to; identifier() supplies a fallback when it is empty, since a signature
	// with no identifier at all is not valid.
	Identifier string
}

// identifier is the name the code signature gives this image, never empty.
func (img *Image) identifier() string {
	if img.Identifier == "" {
		return "origin"
	}
	return img.Identifier
}

// Layout decides where each segment will be mapped, before any code exists.
//
// The code generator calls this first with the sizes it is about to produce, emits
// against the addresses it returns, and then fills in the bytes. The order is forced by
// ADR-0017: with no relocations, an instruction that names an address must know it while
// being encoded.
type Layout struct {
	Target     Target
	TextAddr   uint64
	RoDataAddr uint64
	DataAddr   uint64

	textOff   uint64
	roDataOff uint64
	dataOff   uint64
}

// headerSize is how much room the file's own headers need before the first byte of code.
// The headers are part of the first mapped segment — the standard trick that saves a
// page — so the text cannot start at file offset zero.
//
// Both formats must return the same value regardless of whether the image being built
// will end up carrying ADR-0023's debug information: Plan decides TextAddr before a
// single instruction is emitted (and, for the native backend, identically on both of
// Build's two passes), so this cannot depend on anything only known afterward. ELF's own
// program-header region never grows for debug info — the section header table, symbol
// table and DWARF sections all live past the loadable segments (elf.go's own doc comment)
// — but Mach-O's do, because a load command is how a Mach-O file points a reader at
// anything at all: __DWARF and LC_SYMTAB, when writeMachO emits them, sit in the same
// header region __TEXT and __DATA's own commands do. So the Mach-O case always reserves
// room for them, whether or not this particular image turns out to carry debug
// information — the reservation only needs to be an upper bound, and any slack is zero-
// padded (writeELF and writeMachO already pad up to TextAddr regardless).
func headerSize(t Target) uint64 {
	switch t.Format {
	case ELF:
		return elfHeaderSize + 2*elfProgHeaderSize
	case MachO:
		return machoHeaderSize +
			machoSegmentCmdSize + // __PAGEZERO
			machoSegmentCmdSize + machoSectionSize + // __TEXT with __text
			machoSegmentCmdSize + machoSectionSize + // __DATA with __data
			machoUnixThreadCmdSize +
			machoSegmentCmdSize + 3*machoSectionSize + // __DWARF (ADR-0023), reserved unconditionally
			machoSymtabCmdSize + // LC_SYMTAB, reserved unconditionally
			machoSegmentCmdSize + // __LINKEDIT, mandatory and always emitted (ADR-0024)
			machoCodeSigCmdSize // LC_CODE_SIGNATURE (ADR-0024)
	}
	panic(fmt.Sprintf("unimplemented: header size for %s", t.Format))
}

// Plan assigns addresses for segments of the given sizes.
func Plan(t Target, textSize, roDataSize, dataSize uint64) *Layout {
	l := &Layout{Target: t}

	l.textOff = align(headerSize(t), 16)
	l.TextAddr = t.Base + l.textOff

	l.roDataOff = align(l.textOff+textSize, 16)
	l.RoDataAddr = t.Base + l.roDataOff

	// The writable segment starts on a fresh page: a mapping's protection is
	// page-granular, so data sharing a page with code would be either executable or
	// unwritable.
	l.dataOff = align(l.roDataOff+roDataSize, t.PageSize)
	l.DataAddr = t.Base + l.dataOff

	return l
}

// Image builds the image for a plan, with the bytes the code generator produced.
func (l *Layout) Image(text, roData, data []byte, bss, entry uint64) *Image {
	return &Image{
		Target: l.Target, Text: text, RoData: roData, Data: data, Bss: bss, Entry: entry,
		TextAddr: l.TextAddr, RoDataAddr: l.RoDataAddr, DataAddr: l.DataAddr,
	}
}

// Validate reports the inconsistencies that are visible without running the program: an
// entry point outside the text, or segments whose sizes have outgrown the addresses the
// code was generated against.
func (img *Image) Validate() error {
	if img.Entry < img.TextAddr || img.Entry >= img.TextAddr+uint64(len(img.Text)) {
		return fmt.Errorf("entry point %#x is not inside the text segment [%#x, %#x)",
			img.Entry, img.TextAddr, img.TextAddr+uint64(len(img.Text)))
	}
	if img.TextAddr+uint64(len(img.Text)) > img.RoDataAddr {
		return fmt.Errorf("the text segment overruns the read-only data it was laid out against")
	}
	if img.RoDataAddr+uint64(len(img.RoData)) > img.DataAddr {
		return fmt.Errorf("the read-only data overruns the writable data it was laid out against")
	}
	return nil
}

// Write writes the executable in its target's format.
func (img *Image) Write(w io.Writer) error {
	if err := img.Validate(); err != nil {
		return err
	}
	switch img.Target.Format {
	case ELF:
		return img.writeELF(w)
	case MachO:
		return img.writeMachO(w)
	}
	return fmt.Errorf("unimplemented: writing %s", img.Target.Format)
}

func align(v, to uint64) uint64 {
	if to == 0 {
		return v
	}
	return (v + to - 1) / to * to
}

// writer accumulates the file, so that a header written before a size is known can be
// patched rather than computed twice.
type writer struct {
	buf []byte
}

func (w *writer) u8(v uint8)   { w.buf = append(w.buf, v) }
func (w *writer) u16(v uint16) { w.buf = binary.LittleEndian.AppendUint16(w.buf, v) }
func (w *writer) u32(v uint32) { w.buf = binary.LittleEndian.AppendUint32(w.buf, v) }
func (w *writer) u64(v uint64) { w.buf = binary.LittleEndian.AppendUint64(w.buf, v) }

func (w *writer) bytes(b []byte) { w.buf = append(w.buf, b...) }

// name writes a fixed-width, zero-padded name, as both formats use for segments.
func (w *writer) name(s string, width int) {
	if len(s) > width {
		panic(fmt.Sprintf("name %q does not fit in %d bytes", s, width))
	}
	w.bytes([]byte(s))
	w.pad(uint64(width - len(s)))
}

func (w *writer) pad(n uint64) {
	for i := uint64(0); i < n; i++ {
		w.u8(0)
	}
}

// padTo zero-fills up to an absolute file offset.
func (w *writer) padTo(off uint64) {
	if uint64(len(w.buf)) > off {
		panic(fmt.Sprintf("file is already %d bytes, cannot pad back to %d", len(w.buf), off))
	}
	w.pad(off - uint64(len(w.buf)))
}

func (w *writer) len() uint64 { return uint64(len(w.buf)) }
