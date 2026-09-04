package selfhost

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/check"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/driver"
	"github.com/scarypheonix/meta/internal/opt"
	"github.com/scarypheonix/meta/internal/parse"
	"github.com/scarypheonix/meta/internal/prelude"
	"github.com/scarypheonix/meta/internal/resolve"
	"github.com/scarypheonix/meta/internal/source"
	"github.com/scarypheonix/meta/internal/testutil"
)

// stage1/src/check.origin is held to internal/check over this repository's own Origin
// source, by the oracle resolve_test.go describes: internal/check keys its side tables by
// ast.NodeID and stage1's tree has none, so the tables cannot be compared but the
// *sequence* can. One line per checking event, in order, pins every one of them.
//
// Two things make this trace different from the resolver's. Its entries are rendered at
// the very end rather than as they are made, because a type recorded mid-body is often an
// unsolved variable that later unification binds and end-of-body defaulting resolves --
// printing at record time would compare the checker's intermediate state rather than its
// answer. And its instantiation entries name a declaration the way the resolution trace
// does, by the `<file>:<offset>` where its name was written, so that a call to one of two
// same-named functions is not the same line as a call to the other.
//
// The corpus includes the prelude itself, which means one of the 399 packages is the
// prelude checked with itself as the prelude: every name in it is declared twice. That is
// not a program anyone would write, and it is the most valuable case in the corpus --
// duplicate declarations are what made three separate lookups in the Go checker depend on
// a Go map's iteration order, and none of them was visible on input that declares each
// name once.

// goCheck renders the Go checker's answer for one file, in stage1's own output format:
// the `== path` header, the diagnostics, then the trace. Resolution errors stop it, the
// way they stop stage1, because a checker handed unresolved names reports noise.
func goCheck(t *testing.T, ids *ast.IDGen, preludeTree *ast.File, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tree := parse.FileWith(source.NewFile(path, string(data)), diag.New(), ids)
	out := []string{"== " + path}

	bag := diag.New()
	res := resolve.Program(bag,
		resolve.Input{File: preludeTree, Prelude: true},
		resolve.Input{File: tree},
	)
	out = append(out, renderDiags(bag)...)
	if bag.HasErrors() {
		return out
	}

	cbag := diag.New()
	_, lines := check.Trace(cbag, res, preludeTree, tree)
	out = append(out, renderDiags(cbag)...)
	return append(out, lines...)
}

func renderDiags(bag *diag.Bag) []string {
	var out []string
	for _, d := range bag.All() {
		s := d.Primary.Span
		out = append(out, fmt.Sprintf("error %s %s:%d:%d: %s", d.Code, s.File.Name, s.Start, s.End, d.Msg))
	}
	return out
}

func TestStage1CheckerMatchesTheGoChecker(t *testing.T) {
	root := testutil.RepoRoot(t)
	all := relativeCorpus(t)

	pdata, err := os.ReadFile(filepath.Join(root, preludePath))
	if err != nil {
		t.Fatal(err)
	}

	engines := []struct {
		name   string
		engine driver.Engine
		level  opt.Level
		stride int
	}{
		{"native-O2", driver.Native, opt.O2, 1},
		{"native-O0", driver.Native, opt.O0, 1},
		{"vm-O2", driver.VM, opt.O2, 24},
		{"interpreter", driver.Interpreter, opt.O0, 40},
	}
	for _, e := range engines {
		t.Run(e.name, func(t *testing.T) {
			files := all
			if e.stride > 1 {
				files = stride(files, e.stride)
			}

			wd, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chdir(root); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = os.Chdir(wd) }()

			args := append([]string{"check", preludePath}, files...)
			var stdout, stderr bytes.Buffer
			code := driver.RunAt("stage1/src", e.engine, e.level, &stdout, &stderr, args...)
			if stderr.Len() > 0 {
				t.Fatalf("stage1 wrote to stderr:\n%s", stderr.String())
			}
			// 1 because the corpus contains files that do not check on their own: a
			// stage1 module importing its siblings, which are not in this package.
			if code != 1 {
				t.Errorf("stage1 exited %d, want 1", code)
			}
			got := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
			stdout.Reset()

			// The Go side is generated and compared one file at a time. The whole trace
			// is over 1.6 million lines, and holding two copies of it is the difference
			// between this test fitting in the suite's memory budget and not.
			ids := ast.NewIDGen()
			preludeTree := parse.FileWith(source.NewFile(prelude.Name, string(pdata)), diag.New(), ids)
			at, total, bad := 0, 0, 0
			for _, path := range files {
				// The repository-relative path, not an absolute one: it is the name the
				// file is known by in a diagnostic, and diagnostics are sorted by that
				// name, so handing the Go side a different one would reorder them.
				want := goCheck(t, ids, preludeTree, path)
				total += len(want)
				for i, w := range want {
					if at+i >= len(got) {
						break
					}
					if got[at+i] != w {
						bad++
						if bad <= 8 {
							t.Errorf("%s, trace line %d:\n  stage1: %q\n  go:     %q",
								path, i, got[at+i], w)
						}
					}
				}
				at += len(want)
			}
			if bad > 8 {
				t.Errorf("... and %d more lines differ", bad-8)
			}
			if len(got) != total {
				t.Errorf("stage1 printed %d trace lines, the Go checker %d", len(got), total)
			}
			if bad == 0 && len(got) == total {
				t.Logf("%d files, %d trace lines identical", len(files), total)
			}
		})
	}
}
