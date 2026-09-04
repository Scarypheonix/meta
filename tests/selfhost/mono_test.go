package selfhost

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/check"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/driver"
	"github.com/scarypheonix/meta/internal/mono"
	"github.com/scarypheonix/meta/internal/opt"
	"github.com/scarypheonix/meta/internal/parse"
	"github.com/scarypheonix/meta/internal/prelude"
	"github.com/scarypheonix/meta/internal/resolve"
	"github.com/scarypheonix/meta/internal/source"
	"github.com/scarypheonix/meta/internal/testutil"
)

// stage1/src/mono.origin is held to internal/mono over this repository's own Origin
// source, by the trace oracle check_test.go and resolve_test.go describe.
//
// What this trace has to pin is not only which specialized copies a program needs but
// which one every call site reaches, and a copy's *name* will not do it: two declarations
// in different modules may share a name, and `push[i64]` reached from two places is one
// copy or the walk is wrong. So a copy is numbered when it is created -- its position in
// the instantiation set -- and a call names that number. Two compilers that agree on
// every number agree on the whole graph.
//
// The `body` lines are not redundant with the `instance` lines. Instances are created
// eagerly and their bodies walked from a queue, so the two orders differ, and where they
// differ is exactly where an implementation can produce the right set of copies in the
// wrong order.
//
// Monomorphization runs only for a program that resolves and checks (ADR-0010: rejecting
// polymorphic recursion is a decision about legality, made before any code is generated),
// so a file with an earlier diagnostic contributes that diagnostic and nothing else --
// which is what stage1 does too.
func goMono(t *testing.T, ids *ast.IDGen, preludeTree *ast.File, path string) []string {
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
	tys := check.Program(cbag, res, preludeTree, tree)
	out = append(out, renderDiags(cbag)...)
	if cbag.HasErrors() {
		return out
	}

	mbag := diag.New()
	_, lines := mono.Trace(mbag, tys, preludeTree, tree)
	out = append(out, renderDiags(mbag)...)
	return append(out, lines...)
}

func TestStage1MonomorphizerMatchesTheGoOne(t *testing.T) {
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

			args := append([]string{"mono", preludePath}, files...)
			var stdout, stderr bytes.Buffer
			code := runStage1(t, stage1Root, e.engine, e.level, &stdout, &stderr, args...)
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

			ids := ast.NewIDGen()
			preludeTree := parse.FileWith(source.NewFile(prelude.Name, string(pdata)), diag.New(), ids)
			at, total, bad := 0, 0, 0
			for _, path := range files {
				want := goMono(t, ids, preludeTree, path)
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
				t.Errorf("stage1 printed %d trace lines, the Go monomorphizer %d", len(got), total)
			}
			if bad == 0 && len(got) == total {
				t.Logf("%d files, %d trace lines identical", len(files), total)
			}
		})
	}
}

// TestStage1MonomorphizesItsOwnSource runs stage1 over stage1: one package of every module
// under stage1/src, with the prelude, monomorphized whole.
//
// It is the closest the project has come to self-compilation, and it is a different test
// from the corpus one above rather than more of it. Every file in the corpus is compiled
// on its own, so a module that imports its siblings never gets past name resolution there
// -- which is most of stage1. Here they are one package, so the generic machinery stage1
// is actually built out of is what gets monomorphized: `List` and `Map` at a dozen element
// types, `Option` and `Result` at more, and the trait methods behind every `for` loop.
//
// stage1 is handed the file list rather than a directory because listing one is Phase 10
// (docs/deferred.md). It derives each module path from where the file sits under the root
// and sorts the units itself, so the two compilers agree on the unit order without the
// caller arranging it.
func TestStage1MonomorphizesItsOwnSource(t *testing.T) {
	root := testutil.RepoRoot(t)
	const srcRoot = "stage1/src"

	pdata, err := os.ReadFile(filepath.Join(root, preludePath))
	if err != nil {
		t.Fatal(err)
	}

	engines := []struct {
		name   string
		engine driver.Engine
		level  opt.Level
	}{
		{"native-O2", driver.Native, opt.O2},
		{"native-O0", driver.Native, opt.O0},
	}
	for _, e := range engines {
		t.Run(e.name, func(t *testing.T) {
			wd, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chdir(root); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = os.Chdir(wd) }()

			units, err := driver.LoadUnits(srcRoot)
			if err != nil {
				t.Fatal(err)
			}
			args := []string{"mono", preludePath, "--package", srcRoot}
			for _, u := range units {
				args = append(args, u.File.Name)
			}

			var stdout, stderr bytes.Buffer
			if code := runStage1(t, srcRoot, e.engine, e.level, &stdout, &stderr, args...); code != 0 {
				t.Fatalf("stage1 exited %d, want 0\nstderr:\n%s\nstdout:\n%s",
					code, stderr.String(), truncate(stdout.String()))
			}
			if stderr.Len() > 0 {
				t.Fatalf("stage1 wrote to stderr:\n%s", stderr.String())
			}
			got := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")

			want := goMonoPackage(t, string(pdata), srcRoot, units)
			if len(got) != len(want) {
				t.Errorf("stage1 printed %d trace lines, the Go monomorphizer %d", len(got), len(want))
			}
			bad := 0
			for i := 0; i < len(got) && i < len(want); i++ {
				if got[i] != want[i] {
					bad++
					if bad <= 8 {
						t.Errorf("trace line %d:\n  stage1: %q\n  go:     %q", i, got[i], want[i])
					}
				}
			}
			if bad > 8 {
				t.Errorf("... and %d more lines differ", bad-8)
			}
			if bad == 0 && len(got) == len(want) {
				t.Logf("%d modules, %d trace lines identical", len(units), len(want))
			}
		})
	}
}

// goMonoPackage renders the Go compiler's answer for one whole package, in stage1's own
// output format.
func goMonoPackage(t *testing.T, preludeSrc, srcRoot string, units []driver.Unit) []string {
	t.Helper()
	ids := ast.NewIDGen()
	preludeTree := parse.FileWith(source.NewFile(prelude.Name, preludeSrc), diag.New(), ids)

	asts := []*ast.File{preludeTree}
	inputs := []resolve.Input{{File: preludeTree, Prelude: true}}
	for _, u := range units {
		tree := parse.FileWith(u.File, diag.New(), ids)
		asts = append(asts, tree)
		inputs = append(inputs, resolve.Input{Module: u.Module, File: tree})
	}
	out := []string{"== " + srcRoot}

	bag := diag.New()
	res := resolve.Program(bag, inputs...)
	out = append(out, renderDiags(bag)...)
	if bag.HasErrors() {
		return out
	}
	cbag := diag.New()
	tys := check.Program(cbag, res, asts...)
	out = append(out, renderDiags(cbag)...)
	if cbag.HasErrors() {
		return out
	}
	mbag := diag.New()
	_, lines := mono.Trace(mbag, tys, asts...)
	out = append(out, renderDiags(mbag)...)
	return append(out, lines...)
}

func truncate(s string) string {
	if len(s) > 2000 {
		return s[:2000] + "..."
	}
	return s
}
