package selfhost

import (
	"bytes"
	"fmt"
	"os"
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

// stage1 is run as the program it is -- `stage1 parse <file>...`, its own driver, its own
// command line -- and the places it reports a syntax error are held to the places
// internal/lex and internal/parse report one, over this repository's whole Origin corpus.
//
// What is compared is the whole line: the file, the line, the column and the message.
// Position and count say the two parsers agree about the *file*; the wording says they
// agree about how to describe it, which matters because stage1 is meant to replace the Go
// compiler for somebody's diagnostics and not merely to fail in the same places.
//
// It found a real bug on its first run: stage1's interpolation sub-parser used a
// diagnostic list of its own and threw it away, so every syntax error inside `\(...)`
// vanished, and it never checked that the interpolation held only one expression.
//
// The `dump-ast` command's own output is what the parse differential compares; this one is
// about what the parser *rejects*, which no tree can show.

func TestStage1ReportsSyntaxErrorsWhereTheGoParserDoes(t *testing.T) {
	root := testutil.RepoRoot(t)
	all := corpus(t)

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

			var want []string
			for _, path := range files {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				f := source.NewFile(path, string(data))
				bag := diag.New()
				parse.FileWith(f, bag, ast.NewIDGen())
				for _, d := range bag.All() {
					line, col := f.Position(d.Primary.Span.Start)
					want = append(want, fmt.Sprintf("%s:%d:%d: %s", path, line, col, d.Msg))
				}
			}

			wd, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chdir(root); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = os.Chdir(wd) }()

			var stdout, stderr bytes.Buffer
			args := append([]string{"parse"}, files...)
			code := runStage1(t, stage1Root, e.engine, e.level, &stdout, &stderr, args...)
			if stderr.Len() > 0 {
				t.Fatalf("stage1 wrote to stderr:\n%s", stderr.String())
			}
			// 1 when any file was rejected, 0 when none was: the corpus contains
			// deliberate syntax errors, so which one it is depends on the sample.
			wantCode := 0
			if len(want) > 0 {
				wantCode = 1
			}
			if code != wantCode {
				t.Errorf("stage1 exited %d, want %d", code, wantCode)
			}

			var got []string
			for _, line := range strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n") {
				if line != "" {
					got = append(got, line)
				}
			}

			if len(got) != len(want) {
				t.Errorf("stage1 reported %d syntax errors, the Go parser %d", len(got), len(want))
			}
			bad := 0
			for i := 0; i < len(got) && i < len(want); i++ {
				if got[i] != want[i] {
					bad++
					if bad <= 8 {
						t.Errorf("error %d:\n  stage1: %s\n  go:     %s", i, got[i], want[i])
					}
				}
			}
			if bad > 8 {
				t.Errorf("... and %d more differ", bad-8)
			}
			if bad == 0 && len(got) == len(want) {
				t.Logf("%d files, %d syntax errors, same places and same words", len(files), len(want))
			}
		})
	}
}
