package selfhost

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/driver"
	"github.com/scarypheonix/meta/internal/opt"
	"github.com/scarypheonix/meta/internal/parse"
	"github.com/scarypheonix/meta/internal/source"
	"github.com/scarypheonix/meta/internal/testutil"
)

// The syntax tree stage1 builds is held to the one internal/parse builds, through
// internal/ast's own dumper -- which is a complete description of the tree, so two files
// that dump identically parsed identically.
//
// The corpus includes tests/conformance's deliberate syntax errors, so this compares the
// *recovered* tree as well as the well-formed one. spec/02-grammar.md calls the recovery
// strategy normative, and this is what holds the second parser to it.

// parseDriver lexes and parses each file the manifest names, and prints the dump.
const parseDriver = `use std::io;
use std::list;
use lex;
use ast;
use parse;

fn main() {
    match read_to_string("%s") {
        Result::Err(e) => { io::println("cannot read the manifest: \(e.to_str())"); }
        Result::Ok(manifest) => {
            let paths = manifest.split("\n");
            let mut i = 0;
            while i < paths.len() {
                let path = paths.at(i);
                if !path.is_empty() {
                    match read_to_string(path) {
                        Result::Err(e) => { io::println("cannot read \(path): \(e.to_str())"); }
                        Result::Ok(src) => {
                            io::println("== \(path)");
                            let toks = lex::lex(src).tokens;
                            let lines = ast::dump(parse::parse(toks).file);
                            let mut j = 0;
                            while j < lines.len() {
                                io::println(lines.at(j));
                                j = j + 1;
                            }
                        }
                    }
                }
                i = i + 1;
            }
        }
    }
}
`

// writeParsePackage lays out stage1's three modules with a generated driver beside them.
func writeParsePackage(t *testing.T, dir string, files []string) {
	t.Helper()
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	root := testutil.RepoRoot(t)
	for _, name := range []string{"lex.origin", "ast.origin", "parse.origin"} {
		data, err := os.ReadFile(filepath.Join(root, "stage1", "src", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(src, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	manifest := filepath.Join(dir, "manifest.txt")
	if err := os.WriteFile(manifest, []byte(strings.Join(files, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "main.origin"),
		[]byte(fmt.Sprintf(parseDriver, manifest)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStage1ParserMatchesTheGoParser(t *testing.T) {
	files := corpus(t)
	dir := t.TempDir()
	writeParsePackage(t, dir, files)

	var want []string
	for _, path := range files {
		want = append(want, "== "+path)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		bag := diag.New()
		f := parse.FileWith(source.NewFile(path, string(data)), bag, ast.NewIDGen())
		lines := strings.Split(strings.TrimSuffix(ast.Dump(f), "\n"), "\n")
		want = append(want, lines...)
	}

	// Every engine: the parser is Origin, so where the three engines disagree it will show
	// here. Native code takes the whole corpus; the two hosted engines take a sample of it
	// spread across the whole, since what they are for here is agreement rather than
	// coverage.
	engines := []struct {
		name   string
		engine driver.Engine
		level  opt.Level
		stride int
	}{
		{"native-O2", driver.Native, opt.O2, 1},
		{"native-O0", driver.Native, opt.O0, 1},
		{"vm-O2", driver.VM, opt.O2, 6},
		{"interpreter", driver.Interpreter, opt.O0, 6},
	}
	for _, e := range engines {
		t.Run(e.name, func(t *testing.T) {
			files, want := files, want
			pkg := dir
			if e.stride > 1 {
				files = stride(files, e.stride)
				want = nil
				for _, path := range files {
					want = append(want, "== "+path)
					data, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					bag := diag.New()
					f := parse.FileWith(source.NewFile(path, string(data)), bag, ast.NewIDGen())
					want = append(want, strings.Split(strings.TrimSuffix(ast.Dump(f), "\n"), "\n")...)
				}
				pkg = filepath.Join(dir, e.name)
				writeParsePackage(t, pkg, files)
			}
			var stdout, stderr bytes.Buffer
			if code := runStage1(t, pkg, e.engine, e.level, &stdout, &stderr); code != 0 {
				t.Fatalf("exit %d\n%s", code, stderr.String())
			}
			got := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
			if len(got) != len(want) {
				t.Errorf("stage1 printed %d lines, the Go parser %d", len(got), len(want))
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
						t.Errorf("%s, dump line %d:\n  stage1: %q\n  go:     %q", file, i, got[i], want[i])
					}
				}
			}
			if bad > 8 {
				t.Errorf("... and %d more lines differ", bad-8)
			}
			if bad == 0 && len(got) == len(want) {
				t.Logf("%d files, %d dump lines identical", len(files), len(want))
			}
		})
	}
}

// stride takes every nth file, so a sample spans the corpus rather than its first pages.
func stride(files []string, n int) []string {
	var out []string
	for i := 0; i < len(files); i += n {
		out = append(out, files[i])
	}
	return out
}
