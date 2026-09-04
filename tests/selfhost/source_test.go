package selfhost

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scarypheonix/meta/internal/driver"
	"github.com/scarypheonix/meta/internal/opt"
	"github.com/scarypheonix/meta/internal/source"
	"github.com/scarypheonix/meta/internal/testutil"
)

// stage1/src/source.origin is held to internal/source over this repository's own Origin
// files, the same way the lexer and the parser are held to theirs.
//
// What is compared is every rule spec/01-lexical.md states about positions: where each
// line begins, what a line's text is once its terminator is removed, and the one-based
// line and scalar-value column of a spread of byte offsets -- including offsets that fall
// inside a multi-byte character and past the end of the file, which are the two places a
// mapping written twice tends to diverge.

// positionStride is how far apart the sampled offsets are. Every offset would be a
// quarter-million lines of output for no more confidence than a prime stride gives:
// nothing about the mapping is periodic, so a stride co-prime with every line length
// still lands inside multi-byte characters, at line starts, and at ends of files.
const positionStride = 97

// synthetic is a fixed input the corpus does not contain: CRLF, a lone CR, a multi-byte
// character, and an empty final line. Both sides map it, so the terminator rules are
// tested even though every file in this repository ends its lines with a bare \n.
const synthetic = "a\r\nb\rc\nhéllo wörld\n\n"

// sourceDriver prints one file's whole position mapping. The paths come from the command
// line (spec/17-process.md), so the corpus travels as arguments rather than as a file the
// program has to be told where to find.
const sourceDriver = `use std::io;
use source;

fn report(f: source::File) {
    io::println("lines \(f.line_count())");
    let mut ln = 1;
    while ln <= f.line_count() {
        io::println("\(ln) @\(f.line_start(ln)) [\(f.line_text(ln))]");
        ln = ln + 1;
    }
    let mut o = 0 - 3;
    while o <= f.src.len() + 3 {
        let p = f.position(o);
        io::println("\(o) -> \(p.line):\(p.col)");
        o = o + %d;
    }
}

fn main() {
    let argv = args();
    io::println("== <synthetic>");
    report(source::file("<synthetic>", %s));
    let mut i = 1;
    while i < argv.len() {
        let path = argv.at(i);
        match read_to_string(path) {
            Result::Err(e) => { io::println("cannot read \(path): \(e.to_str())"); }
            Result::Ok(src) => {
                io::println("== \(path)");
                report(source::file(path, src));
            }
        }
        i = i + 1;
    }
}
`

// originString renders a Go string as an Origin string literal.
func originString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteByte(s[i])
		}
	}
	b.WriteByte('"')
	return b.String()
}

// writeSourcePackage lays out source.origin with the generated driver beside it.
func writeSourcePackage(t *testing.T, dir string) string {
	t.Helper()
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	root := testutil.RepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "stage1", "src", "source.origin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "source.origin"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	driver := fmt.Sprintf(sourceDriver, positionStride, originString(synthetic))
	if err := os.WriteFile(filepath.Join(src, "main.origin"), []byte(driver), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// reportGo is the Go side of the driver's `report`, line for line.
func reportGo(f *source.File) []string {
	out := []string{fmt.Sprintf("lines %d", f.LineCount())}
	for ln := 1; ln <= f.LineCount(); ln++ {
		out = append(out, fmt.Sprintf("%d @%d [%s]", ln, f.LineStart(ln), f.LineText(ln)))
	}
	for o := -3; o <= len(f.Src)+3; o += positionStride {
		line, col := f.Position(o)
		out = append(out, fmt.Sprintf("%d -> %d:%d", o, line, col))
	}
	return out
}

func wantPositions(t *testing.T, files []string) []string {
	t.Helper()
	want := []string{"== <synthetic>"}
	want = append(want, reportGo(source.NewFile("<synthetic>", synthetic))...)
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, "== "+path)
		want = append(want, reportGo(source.NewFile(path, string(data)))...)
	}
	return want
}

func TestStage1PositionsMatchTheGoSource(t *testing.T) {
	all := corpus(t)
	dir := writeSourcePackage(t, t.TempDir())

	engines := []struct {
		name   string
		engine driver.Engine
		level  opt.Level
		stride int
	}{
		{"native-O2", driver.Native, opt.O2, 1},
		{"vm-O2", driver.VM, opt.O2, 9},
		{"interpreter", driver.Interpreter, opt.O0, 9},
	}
	for _, e := range engines {
		t.Run(e.name, func(t *testing.T) {
			files := all
			if e.stride > 1 {
				files = stride(files, e.stride)
			}
			want := wantPositions(t, files)

			var stdout, stderr bytes.Buffer
			if code := runStage1(t, dir, e.engine, e.level, &stdout, &stderr, files...); code != 0 {
				t.Fatalf("exit %d\n%s", code, stderr.String())
			}
			got := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
			if len(got) != len(want) {
				t.Errorf("stage1 printed %d lines, internal/source %d", len(got), len(want))
			}
			file := ""
			bad := 0
			for i := 0; i < len(got) && i < len(want); i++ {
				if strings.HasPrefix(want[i], "== ") {
					file = want[i][3:]
				}
				if got[i] != want[i] {
					bad++
					if bad <= 8 {
						t.Errorf("%s, line %d:\n  stage1: %q\n  go:     %q", file, i, got[i], want[i])
					}
				}
			}
			if bad > 8 {
				t.Errorf("... and %d more lines differ", bad-8)
			}
			if bad == 0 && len(got) == len(want) {
				t.Logf("%d files, %d mapping lines identical", len(files), len(want))
			}
		})
	}
}
