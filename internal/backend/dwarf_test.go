package backend

import (
	"bytes"
	stddwarf "debug/dwarf"
	stdelf "debug/elf"
	stdmacho "debug/macho"
	"os"
	"path/filepath"
	"testing"

	"github.com/scarypheonix/meta/internal/obj"
	"github.com/scarypheonix/meta/internal/prelude"
)

// TestNativeBuildCarriesValidDebugInfo compiles a real program through the whole pipeline
// (parse, resolve, check, mono, compile, Build), writes it as an ELF file and reads the
// result back with Go's own independent debug/elf and debug/dwarf readers -- the same
// oracle internal/dwarf's own tests use, now exercised end to end through the two-pass
// emitter rather than by calling internal/dwarf directly (ADR-0023).
func TestNativeBuildCarriesValidDebugInfo(t *testing.T) {
	img := buildStackMapTestImage(t, `
use std::io;

fn add(a: i64, b: i64) -> i64 {
    a + b
}

fn main() {
    let sum = add(1, 2);
    io::println(sum.to_str());
}
`)
	checkELFDebugInfo(t, img)
}

func checkELFDebugInfo(t *testing.T, img *obj.Image) {
	t.Helper()
	if len(img.DebugAbbrev) == 0 || len(img.DebugInfo) == 0 || len(img.DebugLine) == 0 {
		t.Fatal("the image carries no debug sections at all")
	}
	if len(img.Funcs) == 0 {
		t.Fatal("the image carries no function symbols")
	}

	path := filepath.Join(t.TempDir(), "prog")
	var buf bytes.Buffer
	if err := img.Write(&buf); err != nil {
		t.Fatalf("writing image: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o755); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}

	ef, err := stdelf.Open(path)
	if err != nil {
		t.Fatalf("debug/elf could not read the file we wrote: %v", err)
	}
	defer ef.Close()

	syms, err := ef.Symbols()
	if err != nil {
		t.Fatalf("reading .symtab: %v", err)
	}
	names := map[string]stdelf.Symbol{}
	for _, s := range syms {
		names[s.Name] = s
	}
	for _, want := range []string{"add", "main"} {
		s, ok := names[want]
		if !ok {
			t.Errorf(".symtab has no entry named %q; got %v", want, names)
			continue
		}
		if s.Value < img.TextAddr || s.Value >= img.TextAddr+uint64(len(img.Text)) {
			t.Errorf("symbol %q is at %#x, outside .text [%#x, %#x)",
				want, s.Value, img.TextAddr, img.TextAddr+uint64(len(img.Text)))
		}
	}

	d, err := ef.DWARF()
	if err != nil {
		t.Fatalf("debug/elf could not open DWARF through the section table: %v", err)
	}
	r := d.Reader()
	cu, err := r.Next()
	if err != nil || cu == nil || cu.Tag != stddwarf.TagCompileUnit {
		t.Fatalf("reading the compile-unit DIE: entry %+v, err %v", cu, err)
	}
	if name, _ := cu.Val(stddwarf.AttrName).(string); name != "case.origin" {
		t.Errorf("DW_AT_name = %q, want %q", name, "case.origin")
	}
	low, _ := cu.Val(stddwarf.AttrLowpc).(uint64)
	if low != img.TextAddr {
		t.Errorf("DW_AT_low_pc = %#x, want %#x (the start of .text)", low, img.TextAddr)
	}
	ranges, err := d.Ranges(cu)
	if err != nil || len(ranges) != 1 || ranges[0][1] != img.TextAddr+uint64(len(img.Text)) {
		t.Errorf("d.Ranges(cu) = %v, err %v, want a single range ending at %#x",
			ranges, err, img.TextAddr+uint64(len(img.Text)))
	}

	lr, err := d.LineReader(cu)
	if err != nil {
		t.Fatalf("opening the line reader: %v", err)
	}
	var rows []stddwarf.LineEntry
	for {
		var e stddwarf.LineEntry
		if err := lr.Next(&e); err != nil {
			break
		}
		rows = append(rows, e)
	}
	if len(rows) < 2 {
		t.Fatalf("only %d line rows, want at least one real row plus the end-of-sequence row", len(rows))
	}
	// Every row names one of the program's two source files, and the user's own file is
	// among them. Both files are real: the prelude is Origin source and is compiled into
	// the program like any other, so a debugger stepping into one of its methods should
	// be told so rather than shown a line of the user's file that has nothing to do with
	// where it stopped.
	sawUserFile := false
	for i, e := range rows {
		if e.EndSequence {
			continue
		}
		if e.Address < img.TextAddr || e.Address >= img.TextAddr+uint64(len(img.Text)) {
			t.Errorf("row %d's address %#x is outside .text [%#x, %#x)",
				i, e.Address, img.TextAddr, img.TextAddr+uint64(len(img.Text)))
		}
		switch {
		case e.File == nil:
			t.Errorf("row %d has no file at all", i)
		case e.File.Name == "case.origin":
			sawUserFile = true
		case e.File.Name == prelude.Name:
		default:
			t.Errorf("row %d's file is %v, want case.origin or %s", i, e.File, prelude.Name)
		}
	}
	if !sawUserFile {
		t.Error("no line row names the program's own source file")
	}
	last := rows[len(rows)-1]
	if !last.EndSequence || last.Address != img.TextAddr+uint64(len(img.Text)) {
		t.Errorf("last row = {endSeq:%v addr:%#x}, want the end-of-sequence row at %#x",
			last.EndSequence, last.Address, img.TextAddr+uint64(len(img.Text)))
	}
}

// TestMachOBuildCarriesValidDebugInfo is the same end-to-end check for the shipping
// format, which had none: ADR-0023's Mach-O path was only ever verified by attaching
// synthetic sections to a hand-written image (internal/obj), never through the real
// compiler. Since ADR-0024 found that no Mach-O this project produced could even run,
// "the Mach-O path was checked structurally" is worth being precise about.
//
// `lldb` itself resolves `breakpoint set --file <name> --line N` against a Mach-O built
// this way -- verified by hand in the development container, where lldb reads the file
// cross-platform even though it cannot execute it. That is the whole acceptance criterion
// minus the live process, which only the target machine can supply (ADR-0003).
func TestMachOBuildCarriesValidDebugInfo(t *testing.T) {
	img := buildStackMapTestImageFor(t, obj.MacOS, `
use std::io;

fn add(a: i64, b: i64) -> i64 {
    a + b
}

fn main() {
    let sum = add(1, 2);
    io::println(sum.to_str());
}
`)
	if len(img.DebugLine) == 0 || len(img.Funcs) == 0 {
		t.Fatal("the Mach-O image carries no debug information")
	}

	path := filepath.Join(t.TempDir(), "prog")
	var buf bytes.Buffer
	if err := img.Write(&buf); err != nil {
		t.Fatalf("writing image: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o755); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}

	mf, err := stdmacho.Open(path)
	if err != nil {
		t.Fatalf("debug/macho could not read the file we wrote: %v", err)
	}
	defer mf.Close()

	// The symbol table `bt` names a frame from.
	if mf.Symtab == nil {
		t.Fatal("no LC_SYMTAB")
	}
	named := map[string]bool{}
	for _, s := range mf.Symtab.Syms {
		named[s.Name] = true
	}
	for _, want := range []string{"add", "main"} {
		if !named[want] {
			t.Errorf("no symbol named %q; lldb's `bt` would not name that frame", want)
		}
	}

	d, err := mf.DWARF()
	if err != nil {
		t.Fatalf("debug/macho could not open DWARF from __DWARF: %v", err)
	}
	r := d.Reader()
	cu, err := r.Next()
	if err != nil || cu == nil || cu.Tag != stddwarf.TagCompileUnit {
		t.Fatalf("reading the compile-unit DIE: entry %+v, err %v", cu, err)
	}

	// Every line row must name an address inside __TEXT: a breakpoint resolves through
	// these, so a row pointing anywhere else is a breakpoint that lands nowhere.
	lr, err := d.LineReader(cu)
	if err != nil {
		t.Fatalf("opening the line reader: %v", err)
	}
	rows := 0
	for {
		var e stddwarf.LineEntry
		if err := lr.Next(&e); err != nil {
			break
		}
		if e.EndSequence {
			continue
		}
		if e.Address < img.TextAddr || e.Address >= img.TextAddr+uint64(len(img.Text)) {
			t.Errorf("row %d's address %#x is outside __text [%#x, %#x)",
				rows, e.Address, img.TextAddr, img.TextAddr+uint64(len(img.Text)))
		}
		rows++
	}
	if rows == 0 {
		t.Error("the line table has no rows, so no breakpoint could resolve")
	}
}
