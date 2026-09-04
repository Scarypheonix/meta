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
	"github.com/scarypheonix/meta/internal/prelude"
	"github.com/scarypheonix/meta/internal/resolve"
	"github.com/scarypheonix/meta/internal/source"
	"github.com/scarypheonix/meta/internal/testutil"
)

// stage1/src/resolve.origin is held to internal/resolve over this repository's own Origin
// source, and this is the one component whose oracle is not shaped like the others.
//
// The Go resolver's output is side tables keyed by ast.NodeID, and stage1's syntax tree
// deliberately has no node ids, so the tables cannot be compared directly. What can be is
// the *sequence*: both resolvers walk the same tree in the same order, so one line per
// resolution event, in order, pins every one of them. A line's position in the trace
// identifies the node and what the line says identifies what the node resolved to --
// including, for a binding, the `<file>:<offset>` where it was declared, which is what
// makes shadowing observable. internal/resolve/trace.go is the Go side of that format.
//
// Diagnostics are compared here in full, message text included, unlike the parser's
// (docs/deferred.md): a resolver's messages carry only names, which stage1 has, rather
// than a rendered token.
//
// Each file is resolved as its own package, with the prelude, because that is the shape
// `originc check <file>` compiles in -- two files that both declare `main` are not a
// package.

// preludePath is where the prelude is read from. The *name* it is known by is
// prelude.Name -- the literal `<prelude>` -- on both sides, because that name is
// load-bearing rather than cosmetic: internal/check's DeclaredInPrelude, internal/mono
// and the two runtimes all decide "is this the prelude?" by comparing it, so a second
// compiler that called the file anything else would not be the same compiler.
const preludePath = "internal/prelude/prelude.origin"

// goResolution renders the Go resolver's answer for one file, in stage1's own output
// format: the `== path` header, then the diagnostics, then the trace.
func goResolution(t *testing.T, ids *ast.IDGen, preludeTree *ast.File, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tree := parse.FileWith(source.NewFile(path, string(data)), diag.New(), ids)
	bag := diag.New()
	_, lines := resolve.Trace(bag,
		resolve.Input{File: preludeTree, Prelude: true},
		resolve.Input{File: tree},
	)
	out := []string{"== " + path}
	for _, d := range bag.All() {
		s := d.Primary.Span
		out = append(out, fmt.Sprintf("error %s %s:%d:%d: %s", d.Code, s.File.Name, s.Start, s.End, d.Msg))
	}
	return append(out, lines...)
}

func TestStage1ResolverMatchesTheGoResolver(t *testing.T) {
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
		{"vm-O2", driver.VM, opt.O2, 12},
		{"interpreter", driver.Interpreter, opt.O0, 12},
	}
	for _, e := range engines {
		t.Run(e.name, func(t *testing.T) {
			files := all
			if e.stride > 1 {
				files = stride(files, e.stride)
			}

			// One id generator and one parse of the prelude for the whole run, matching
			// what stage1 does: resolution does not write into the tree.
			ids := ast.NewIDGen()
			preludeTree := parse.FileWith(source.NewFile(prelude.Name, string(pdata)), diag.New(), ids)
			var want []string
			for _, path := range files {
				want = append(want, goResolution(t, ids, preludeTree, filepath.Join(root, path))...)
			}
			// The Go side names files by the path it was handed; stage1 names them by the
			// path on its command line. Both are repository-relative here.
			for i, l := range want {
				want[i] = strings.ReplaceAll(l, root+string(filepath.Separator), "")
			}

			wd, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chdir(root); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = os.Chdir(wd) }()

			args := append([]string{"resolve", preludePath}, files...)
			var stdout, stderr bytes.Buffer
			code := runStage1(t, stage1Root, e.engine, e.level, &stdout, &stderr, args...)
			if stderr.Len() > 0 {
				t.Fatalf("stage1 wrote to stderr:\n%s", stderr.String())
			}
			// 1 because the corpus contains files that do not resolve on their own: a
			// stage1 module importing its siblings, which are not in this package.
			if code != 1 {
				t.Errorf("stage1 exited %d, want 1", code)
			}

			got := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
			if len(got) != len(want) {
				t.Errorf("stage1 printed %d trace lines, the Go resolver %d", len(got), len(want))
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
						t.Errorf("%s, trace line %d:\n  stage1: %q\n  go:     %q", file, i, got[i], want[i])
					}
				}
			}
			if bad > 8 {
				t.Errorf("... and %d more lines differ", bad-8)
			}
			if bad == 0 && len(got) == len(want) {
				t.Logf("%d files, %d trace lines identical", len(files), len(want))
			}
		})
	}
}

// relativeCorpus is the corpus as repository-relative paths, which is how both sides name
// a file in the trace.
func relativeCorpus(t *testing.T) []string {
	t.Helper()
	root := testutil.RepoRoot(t)
	var out []string
	for _, p := range corpus(t) {
		rel, err := filepath.Rel(root, p)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, rel)
	}
	return out
}
