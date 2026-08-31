package e2e

import (
	"bytes"
	"crypto/sha256"
	stddwarf "debug/dwarf"
	"debug/macho"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/scarypheonix/meta/internal/codesign"
	"github.com/scarypheonix/meta/internal/driver"
	"github.com/scarypheonix/meta/internal/obj"
	"github.com/scarypheonix/meta/internal/opt"
	"github.com/scarypheonix/meta/internal/testutil"
)

// Mach-O is the format Origin ships (ADR-0003), and the one this suite cannot execute:
// the differential above runs every case as an ELF, so a defect that exists only in the
// Mach-O writer passes everything else here unnoticed. That is not hypothetical. Until
// ADR-0024, *no Mach-O this project produced could run at all* — there was no __LINKEDIT
// segment and no code signature — and it went undetected through the whole phase because
// exactly one program had ever been built in that format and none had ever been run.
//
// So every case is built as a Mach-O too, and checked for the properties that make one
// loadable, at every optimization level. It is not execution and does not pretend to be;
// it is the largest part of the claim that can be made without the target machine.
func TestEveryCaseBuildsALoadableMachO(t *testing.T) {
	cases := loadCases(t)
	if len(cases) == 0 {
		t.Fatal("no end-to-end cases found; the check would pass vacuously")
	}

	root := testutil.RepoRoot(t)
	levels := []struct {
		name  string
		level opt.Level
	}{{"O0", opt.O0}, {"O1", opt.O1}, {"O2", opt.O2}}

	for _, c := range cases {
		for _, l := range levels {
			t.Run(c.Name+"/"+l.name, func(t *testing.T) {
				if concurrencyCases[c.Name] {
					t.Skip("Phase 6: the native backend has no scheduler yet")
				}
				raw := buildMachO(t, root, c, l.level)
				checkSegments(t, raw)
				checkSignature(t, raw)
			})
		}
	}
}

// buildMachO compiles one case for macOS through the driver -- the same path
// `originc build --target macos` takes -- and returns the file's bytes.
func buildMachO(t *testing.T, root string, c caseFile, level opt.Level) []byte {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(wd) }()

	out := filepath.Join(t.TempDir(), c.Name)
	var stdout, stderr bytes.Buffer
	if code := driver.Build(c.RelPath, out, "macos", level, &stdout, &stderr); code != 0 {
		t.Fatalf("originc build --target macos exited %d\n%s%s", code, stdout.String(), stderr.String())
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("reading the built executable: %v", err)
	}
	return raw
}

// checkSegments is what the kernel's own loader needs to be true: __LINKEDIT last, no
// segment overlapping another, and every one of them above __PAGEZERO.
func checkSegments(t *testing.T, raw []byte) {
	t.Helper()
	f, err := macho.NewFile(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("debug/macho could not read the file: %v", err)
	}
	defer f.Close()

	var segs []*macho.Segment
	for _, l := range f.Loads {
		if s, ok := l.(*macho.Segment); ok {
			segs = append(segs, s)
		}
	}
	if len(segs) < 2 {
		t.Fatalf("only %d segments", len(segs))
	}
	if last := segs[len(segs)-1]; last.Name != "__LINKEDIT" {
		t.Errorf("the last segment is %q, want __LINKEDIT", last.Name)
	}
	for i, s := range segs {
		if s.Name == "__PAGEZERO" {
			continue
		}
		if s.Addr < obj.MacOS.Base {
			t.Errorf("%s is at %#x, inside __PAGEZERO's [0, %#x)", s.Name, s.Addr, obj.MacOS.Base)
		}
		if i > 0 && segs[i-1].Name != "__PAGEZERO" {
			if prev := segs[i-1]; s.Addr < prev.Addr+prev.Memsz {
				t.Errorf("%s starts at %#x, inside %s's [%#x, %#x)",
					s.Name, s.Addr, prev.Name, prev.Addr, prev.Addr+prev.Memsz)
			}
		}
		if s.Filesz > 0 && uint64(len(raw)) < s.Offset+s.Filesz {
			t.Errorf("%s claims file bytes [%d, %d) but the file is %d bytes",
				s.Name, s.Offset, s.Offset+s.Filesz, len(raw))
		}
	}

	// The debug information a debugger resolves a breakpoint through.
	d, err := f.DWARF()
	if err != nil {
		t.Fatalf("debug/macho could not open DWARF: %v", err)
	}
	r := d.Reader()
	cu, err := r.Next()
	if err != nil || cu == nil || cu.Tag != stddwarf.TagCompileUnit {
		t.Fatalf("reading the compile-unit DIE: %+v, err %v", cu, err)
	}
	if f.Symtab == nil || len(f.Symtab.Syms) == 0 {
		t.Error("no symbols, so `bt` would name no frame")
	}
}

// checkSignature re-derives every page digest from the file. macOS kills a process whose
// signature does not match its own bytes, so a signature that merely parses is not enough
// (ADR-0024).
func checkSignature(t *testing.T, raw []byte) {
	t.Helper()
	ncmds := binary.LittleEndian.Uint32(raw[16:])
	off := uint64(32)
	var sigOff, sigSize uint64
	for i := uint32(0); i < ncmds; i++ {
		cmd := binary.LittleEndian.Uint32(raw[off:])
		size := uint64(binary.LittleEndian.Uint32(raw[off+4:]))
		if size == 0 {
			t.Fatalf("load command %d has zero size", i)
		}
		if cmd == 0x1d { // LC_CODE_SIGNATURE
			sigOff = uint64(binary.LittleEndian.Uint32(raw[off+8:]))
			sigSize = uint64(binary.LittleEndian.Uint32(raw[off+12:]))
		}
		off += size
	}
	if sigOff == 0 {
		t.Fatal("no LC_CODE_SIGNATURE: macOS will not run an unsigned executable")
	}
	if sigOff+sigSize != uint64(len(raw)) {
		t.Errorf("the signature ends at %d but the file is %d bytes; it must be last",
			sigOff+sigSize, len(raw))
	}

	sig := raw[sigOff : sigOff+sigSize]
	count := binary.BigEndian.Uint32(sig[8:])
	checked := 0
	for i := uint32(0); i < count; i++ {
		blob := sig[binary.BigEndian.Uint32(sig[12+i*8+4:]):]
		if binary.BigEndian.Uint32(blob) != 0xfade0c02 { // CSMAGIC_CODEDIRECTORY
			continue
		}
		hashOffset := binary.BigEndian.Uint32(blob[16:])
		nCode := binary.BigEndian.Uint32(blob[28:])
		codeLimit := uint64(binary.BigEndian.Uint32(blob[32:]))
		hashSize := uint32(blob[36])

		if codeLimit != sigOff {
			t.Errorf("codeLimit is %d, want %d: the signature must cover everything before it",
				codeLimit, sigOff)
		}
		for p := uint32(0); p < nCode; p++ {
			start := uint64(p) * codesign.PageSize
			end := start + codesign.PageSize
			if end > codeLimit {
				end = codeLimit
			}
			want := sha256.Sum256(raw[start:end])
			at := hashOffset + p*hashSize
			if string(blob[at:at+hashSize]) != string(want[:]) {
				t.Errorf("page %d's digest does not match the file's own bytes", p)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Error("the signature has no CodeDirectory, so nothing was checked")
	}
}
