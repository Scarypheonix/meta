package selfhost

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scarypheonix/meta/internal/driver"
	"github.com/scarypheonix/meta/internal/dwarf"
	"github.com/scarypheonix/meta/internal/obj"
	"github.com/scarypheonix/meta/internal/opt"
	"github.com/scarypheonix/meta/internal/testutil"
)

// stage1/src/obj.origin is held to internal/obj, and the oracle is the artefact: a writer
// is right when the bytes are right. Both sides lay out the same images, fill their
// segments with the same position-dependent pattern, and the files are compared.
//
// Four images rather than one, because the shapes that differ are the ones with something
// missing. An image with no debug information writes two program headers and nothing past
// them; one with functions grows a section header table, a symbol table and two string
// tables. An image whose writable data is empty must still pad up to the data offset before
// the appended sections begin, which is a case the writer has a branch for and nothing else
// would reach.
//
// Mach-O is not part of this yet: stage1 does not write one (docs/deferred.md), and its
// `write` says so rather than producing something that is not a Mach-O.

// goImages is the Go compiler's answer: the four executables, as the driver prints them.
func goImages(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, c := range []struct {
		text, ro, data, bss int
		withFuncs           bool
	}{
		{64, 32, 16, 0, false},
		{64, 32, 16, 4096, true},
		{1, 1, 0, 0, true},
		{4000, 200, 300, 65536, true},
	} {
		target := obj.Linux
		l := obj.Plan(target, uint64(c.text), uint64(c.ro), uint64(c.data))
		img := l.Image(pattern(c.text, 1), pattern(c.ro, 2), pattern(c.data, 3),
			uint64(c.bss), l.TextAddr)
		if c.withFuncs {
			img.Funcs = []dwarf.Func{
				{Name: "main", Address: l.TextAddr, Size: 16},
				{Name: "a_rather_longer_function_name", Address: l.TextAddr + 16, Size: 24},
				{Name: "f", Address: l.TextAddr + 40, Size: 1},
			}
			// Stand-ins for the three DWARF sections: this differential is about where
			// the bytes land, not what they mean, and internal/dwarf is not translated
			// yet.
			img.DebugAbbrev = counted(11, 1)
			img.DebugInfo = counted(23, 100)
			img.DebugLine = counted(37, 200)
		}
		var buf bytes.Buffer
		if err := img.Write(&buf); err != nil {
			t.Fatalf("the Go writer refused an image of %d/%d/%d bytes: %v",
				c.text, c.ro, c.data, err)
		}
		out = append(out, fmt.Sprintf("== %d bytes", buf.Len()))
		out = append(out, hexLines(buf.Bytes())...)
	}
	return out
}

// pattern is bytes that depend on their position, so that a writer putting a segment at the
// wrong offset shows up as content rather than only as a length.
func pattern(n, seed int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte((i*7 + seed*31) % 251)
	}
	return out
}

func counted(n, from int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte(from + i)
	}
	return out
}

// TestStage1WritesTheSameExecutableAsTheGoWriter builds a package of stage1's writer plus
// the driver beside it, runs it on every engine, and compares the files.
func TestStage1WritesTheSameExecutableAsTheGoWriter(t *testing.T) {
	want := goImages(t)

	dir := t.TempDir()
	writeObjPackage(t, dir)

	engines := []struct {
		name   string
		engine driver.Engine
		level  opt.Level
	}{
		{"native-O2", driver.Native, opt.O2},
		{"native-O0", driver.Native, opt.O0},
		{"vm-O2", driver.VM, opt.O2},
		{"interpreter", driver.Interpreter, opt.O0},
	}
	for _, e := range engines {
		t.Run(e.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := runStage1(t, dir, e.engine, e.level, &stdout, &stderr); code != 0 {
				t.Fatalf("the writer driver exited %d\nstderr:\n%s", code, stderr.String())
			}
			if stderr.Len() > 0 {
				t.Fatalf("the writer driver wrote to stderr:\n%s", stderr.String())
			}
			got := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
			if len(got) != len(want) {
				t.Errorf("stage1 printed %d lines, the Go writer %d", len(got), len(want))
			}
			bad := 0
			for i := 0; i < len(got) && i < len(want); i++ {
				if got[i] != want[i] {
					bad++
					if bad <= 8 {
						t.Errorf("line %d:\n  stage1: %s\n  go:     %s", i, got[i], want[i])
					}
				}
			}
			if bad > 8 {
				t.Errorf("... and %d more lines differ", bad-8)
			}
			if bad == 0 && len(got) == len(want) {
				t.Logf("4 executables, %d lines of bytes identical", len(want))
			}
		})
	}
}

// writeObjPackage assembles a package of stage1's own obj.origin and the driver that
// exercises it.
func writeObjPackage(t *testing.T, dir string) {
	t.Helper()
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	root := testutil.RepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "stage1", "src", "obj.origin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "obj.origin"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	main, err := os.ReadFile(filepath.Join(root, "tests", "selfhost", "testdata", "obj_driver.origin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "main.origin"), main, 0o644); err != nil {
		t.Fatal(err)
	}
}
