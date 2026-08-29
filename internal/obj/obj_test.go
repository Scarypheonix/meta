package obj

import (
	"bytes"
	"debug/elf"
	"debug/macho"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/scarypheonix/meta/internal/x86"
)

// helloProgram builds a complete program by hand: write a string to stdout, then exit
// with a status. It is the smallest thing that exercises everything an executable needs
// — an entry point the kernel can find, code that runs, constant data addressed without
// a relocation, and a clean exit — and on Linux the test runs it.
func helloProgram(t *testing.T, target Target, msg string, status int32) *Image {
	t.Helper()

	// Two passes are not needed: Plan fixes the addresses first, and the code is
	// emitted against them. That is the whole shape of ADR-0017 in one function.
	const textEstimate = 0x100
	plan := Plan(target, textEstimate, uint64(len(msg)), 0)

	a := x86.New(plan.TextAddr)
	entry := a.NewLabel("_start")
	a.Bind(entry)

	// write(1, msg, len(msg))
	a.MovRI(x86.RAX, target.SysWrite)
	a.MovRI(x86.RDI, 1)
	a.MovRI(x86.RSI, plan.RoDataAddr)
	a.MovRI(x86.RDX, uint64(len(msg)))
	a.Syscall()

	// exit(status)
	a.MovRI(x86.RAX, target.SysExit)
	a.MovRI(x86.RDI, uint64(uint32(status)))
	a.Syscall()

	// The kernel does not return from exit; if it somehow did, fault rather than run
	// whatever bytes follow.
	a.Ud2()

	code := a.Code()
	if uint64(len(code)) > textEstimate {
		t.Fatalf("the hand-written program grew to %d bytes, past the %d it was laid out for",
			len(code), textEstimate)
	}
	return plan.Image(code, []byte(msg), nil, 0, a.Addr(entry))
}

func writeImage(t *testing.T, img *Image, path string) {
	t.Helper()
	var buf bytes.Buffer
	if err := img.Write(&buf); err != nil {
		t.Fatalf("writing image: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o755); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// TestELFRuns is the only test in this package that proves the format is right rather
// than merely self-consistent: the kernel is the authority on whether an ELF file is one,
// and it says so by running it.
func TestELFRuns(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("an x86-64 Linux ELF can only be executed on x86-64 Linux")
	}
	img := helloProgram(t, Linux, "hello from a hand-written executable\n", 7)
	path := filepath.Join(t.TempDir(), "hello")
	writeImage(t, img, path)

	out, err := exec.Command(path).Output()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("running the executable: %v", err)
	}
	if got, want := string(out), "hello from a hand-written executable\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if code != 7 {
		t.Errorf("exit status = %d, want 7", code)
	}
}

// TestELFParses reads the file back with the standard library's ELF reader — an
// independent implementation, which is the point. Constants spelled out in elf.go and
// re-derived in a test written beside it could be wrong together; debug/elf was written
// by someone with no access to this file.
func TestELFParses(t *testing.T) {
	img := helloProgram(t, Linux, "hi\n", 0)
	path := filepath.Join(t.TempDir(), "hello")
	writeImage(t, img, path)

	f, err := elf.Open(path)
	if err != nil {
		t.Fatalf("debug/elf could not read the file we wrote: %v", err)
	}
	defer f.Close()

	if f.Class != elf.ELFCLASS64 {
		t.Errorf("class is %v, want 64-bit", f.Class)
	}
	if f.Data != elf.ELFDATA2LSB {
		t.Errorf("byte order is %v, want little-endian", f.Data)
	}
	if f.Type != elf.ET_EXEC {
		t.Errorf("type is %v, want ET_EXEC: ADR-0017 makes this a complete executable", f.Type)
	}
	if f.Machine != elf.EM_X86_64 {
		t.Errorf("machine is %v, want x86-64", f.Machine)
	}
	if f.Entry != img.Entry {
		t.Errorf("entry is %#x, want %#x", f.Entry, img.Entry)
	}

	var loads []*elf.Prog
	for _, p := range f.Progs {
		if p.Type == elf.PT_LOAD {
			loads = append(loads, p)
		}
	}
	if len(loads) != 2 {
		t.Fatalf("found %d loadable segments, want 2", len(loads))
	}
	if loads[0].Flags != elf.PF_R|elf.PF_X {
		t.Errorf("the first segment is %v, want read+execute", loads[0].Flags)
	}
	if img.Entry < loads[0].Vaddr || img.Entry >= loads[0].Vaddr+loads[0].Memsz {
		t.Error("the entry point is not inside the executable segment")
	}
	// The relocation-free claim of ADR-0017, checked rather than asserted.
	for _, s := range f.Sections {
		if s.Type == elf.SHT_REL || s.Type == elf.SHT_RELA {
			t.Errorf("the file has a relocation section %q, but nothing is meant to need relocating", s.Name)
		}
	}
}

// TestMachOParses is the same check for the shipping format. The implementer cannot run a
// Mach-O here, so reading it back with an independent parser is as far as verification
// goes in the container; that a compiled binary runs is the user's checklist item on the
// target machine (docs/phases/0-complete.md).
func TestMachOParses(t *testing.T) {
	img := helloProgram(t, MacOS, "hi\n", 0)
	path := filepath.Join(t.TempDir(), "hello")
	writeImage(t, img, path)

	f, err := macho.Open(path)
	if err != nil {
		t.Fatalf("debug/macho could not read the file we wrote: %v", err)
	}
	defer f.Close()

	if f.Magic != macho.Magic64 {
		t.Errorf("magic is %#x, want a 64-bit Mach-O", f.Magic)
	}
	if f.Cpu != macho.CpuAmd64 {
		t.Errorf("cpu is %v, want x86-64", f.Cpu)
	}
	if f.Type != macho.TypeExec {
		t.Errorf("type is %v, want MH_EXECUTE", f.Type)
	}

	segs := map[string]*macho.Segment{}
	for _, l := range f.Loads {
		if s, ok := l.(*macho.Segment); ok {
			segs[s.Name] = s
		}
	}
	pagezero, ok := segs["__PAGEZERO"]
	if !ok {
		t.Fatal("no __PAGEZERO: macOS requires the low 4 GiB of a 64-bit executable to be unmapped")
	}
	if pagezero.Addr != 0 || pagezero.Memsz != MacOS.Base {
		t.Errorf("__PAGEZERO covers [%#x, %#x), want [0, %#x)",
			pagezero.Addr, pagezero.Addr+pagezero.Memsz, MacOS.Base)
	}
	text, ok := segs["__TEXT"]
	if !ok {
		t.Fatal("no __TEXT segment")
	}
	if text.Addr != MacOS.Base {
		t.Errorf("__TEXT is at %#x, want %#x", text.Addr, MacOS.Base)
	}
	if img.Entry < text.Addr || img.Entry >= text.Addr+text.Memsz {
		t.Error("the entry point is not inside __TEXT")
	}
	if sect := f.Section("__text"); sect == nil {
		t.Error("no __text section, so a debugger has nothing to name the code")
	} else if sect.Addr != img.TextAddr {
		t.Errorf("__text starts at %#x, want %#x", sect.Addr, img.TextAddr)
	}
}

// TestMachOCarriesTheEntryInThreadState checks the one field debug/macho does not expose
// as a typed load command. LC_UNIXTHREAD is how a program with no dynamic loader says
// where to start, and if rip lands in the wrong slot the kernel starts at zero.
func TestMachOCarriesTheEntryInThreadState(t *testing.T) {
	img := helloProgram(t, MacOS, "hi\n", 0)
	var buf bytes.Buffer
	if err := img.Write(&buf); err != nil {
		t.Fatalf("writing image: %v", err)
	}
	raw := buf.Bytes()

	cmd, ok := findLoadCommand(raw, lcUnixThread)
	if !ok {
		t.Fatal("no LC_UNIXTHREAD load command")
	}
	if got := le32(cmd[8:]); got != x86ThreadState64 {
		t.Errorf("thread state flavor is %d, want x86_THREAD_STATE64 (%d)", got, x86ThreadState64)
	}
	if got := le32(cmd[12:]); got != x86ThreadState64Count {
		t.Errorf("thread state count is %d, want %d", got, x86ThreadState64Count)
	}
	if got := le64(cmd[16+ripIndex*8:]); got != img.Entry {
		t.Errorf("rip in the initial thread state is %#x, want the entry %#x", got, img.Entry)
	}
	for i := 0; i < 21; i++ {
		if i == ripIndex {
			continue
		}
		if got := le64(cmd[16+i*8:]); got != 0 {
			t.Errorf("register %d of the initial thread state is %#x, want zero", i, got)
		}
	}
}

// findLoadCommand walks the Mach-O load commands looking for one by type. It is a
// deliberately separate reader from the writer: the writer computes sizes, and this walks
// by them, so a wrong cmdsize desynchronizes the walk and the test fails.
func findLoadCommand(raw []byte, want uint32) ([]byte, bool) {
	ncmds := le32(raw[16:])
	off := uint64(machoHeaderSize)
	for i := uint32(0); i < ncmds; i++ {
		cmd := le32(raw[off:])
		size := uint64(le32(raw[off+4:]))
		if size == 0 || off+size > uint64(len(raw)) {
			return nil, false
		}
		if cmd == want {
			return raw[off : off+size], true
		}
		off += size
	}
	return nil, false
}

func le32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

func le64(b []byte) uint64 {
	return uint64(le32(b)) | uint64(le32(b[4:]))<<32
}

// TestBothFormatsShareOneInstructionStream is ADR-0003 as a test. The two files differ in
// their headers and in where they are loaded; the instructions between them differ only
// where an address or a syscall number is baked in, and nowhere else.
func TestBothFormatsShareOneInstructionStream(t *testing.T) {
	linux := helloProgram(t, Linux, "hi\n", 0)
	mac := helloProgram(t, MacOS, "hi\n", 0)

	if len(linux.Text) != len(mac.Text) {
		t.Fatalf("the two builds emitted %d and %d bytes of code; one instruction stream means one length",
			len(linux.Text), len(mac.Text))
	}
	// Every difference must be inside a `movabs` immediate: the load address of the
	// message, or a syscall number. Counting them is what makes "differ only there" a
	// claim a test can check.
	diffs := 0
	for i := range linux.Text {
		if linux.Text[i] != mac.Text[i] {
			diffs++
		}
	}
	if diffs == 0 {
		t.Fatal("the two builds are byte-identical, so the test is not exercising the two targets")
	}
	if diffs > 3*8 {
		t.Errorf("%d bytes differ between the targets, more than the three baked-in 64-bit values "+
			"(message address, write number, exit number) can account for", diffs)
	}
}

func TestPlanKeepsSegmentsApart(t *testing.T) {
	plan := Plan(Linux, 0x1234, 0x50, 0x10)
	if plan.TextAddr < Linux.Base+headerSize(Linux) {
		t.Error("the text overlaps the file headers, which share its mapping")
	}
	if plan.RoDataAddr < plan.TextAddr+0x1234 {
		t.Error("the read-only data overlaps the text")
	}
	if plan.DataAddr%Linux.PageSize != 0 {
		t.Errorf("the writable segment starts at %#x, which is not page-aligned: its "+
			"protection would have to be shared with the code before it", plan.DataAddr)
	}
	if plan.DataAddr < plan.RoDataAddr+0x50 {
		t.Error("the writable data overlaps the read-only data")
	}
}

// TestValidateCatchesAnOvergrownSegment: the code generator lays out addresses before it
// knows exactly how much code there will be, so emitting more than planned is a real
// failure mode. It must be a build error, not a program that jumps into its own strings.
func TestValidateCatchesAnOvergrownSegment(t *testing.T) {
	plan := Plan(Linux, 16, 16, 0)
	img := plan.Image(make([]byte, 4096), []byte("x"), nil, 0, plan.TextAddr)
	if err := img.Validate(); err == nil {
		t.Error("a text segment that outgrew its layout was accepted")
	}
}

func TestValidateCatchesABadEntry(t *testing.T) {
	plan := Plan(Linux, 16, 0, 0)
	img := plan.Image(make([]byte, 16), nil, nil, 0, plan.TextAddr+1000)
	if err := img.Validate(); err == nil {
		t.Error("an entry point outside the text segment was accepted")
	}
}

func TestTargetFor(t *testing.T) {
	for _, name := range []string{"linux", "elf"} {
		if got, err := TargetFor(name); err != nil || got.Format != ELF {
			t.Errorf("TargetFor(%q) = %v, %v", name, got.Format, err)
		}
	}
	for _, name := range []string{"macos", "darwin", "macho"} {
		if got, err := TargetFor(name); err != nil || got.Format != MachO {
			t.Errorf("TargetFor(%q) = %v, %v", name, got.Format, err)
		}
	}
	if _, err := TargetFor("wasm"); err == nil {
		t.Error("an unknown target was accepted")
	}
}

// TestWritesAreDeterministic is spec/11-codegen.md's determinism clause, which Phase 9's
// bootstrap depends on: the same image must produce the same bytes every time.
func TestWritesAreDeterministic(t *testing.T) {
	for _, target := range []Target{Linux, MacOS} {
		img := helloProgram(t, target, "hi\n", 0)
		var first, second bytes.Buffer
		if err := img.Write(&first); err != nil {
			t.Fatal(err)
		}
		if err := img.Write(&second); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first.Bytes(), second.Bytes()) {
			t.Errorf("%s: two writes of one image differ", target.Format)
		}
	}
}
