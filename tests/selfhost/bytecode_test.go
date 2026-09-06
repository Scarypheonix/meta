package selfhost

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/check"
	"github.com/scarypheonix/meta/internal/compile"
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

// stage1/src/compile.origin and stage1/src/bytecode.origin are held to internal/compile
// and internal/bytecode, and the oracle here is not a trace: it is the artefact.
//
// The front end needed traces because its answers are side tables keyed by ast.NodeID and
// stage1's tree has no node ids, so only the sequence that builds them was comparable. A
// bytecode program is not a side table. `originc dump-bytecode` already existed, both
// compilers write the same format, and two compilers that emit the same bytecode for the
// same input agree about everything the format records: the instructions, their operands,
// the constant pool and its order, every exact object layout ADR-0019 assigns, and the
// static kind ADR-0021 makes each instruction carry.
//
// Diagnostics are not part of it. `originc` writes every diagnostic to stderr and the
// bytecode to stdout; stage1 has one stream, and withholds a warning from it for that
// reason (main.origin).

// TestStage1BytecodeMatchesTheGoCompiler compiles every corpus file that compiles on its
// own, and compares the disassembly byte for byte.
func TestStage1BytecodeMatchesTheGoCompiler(t *testing.T) {
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

			args := append([]string{"dump-bytecode", preludePath}, files...)
			var stdout, stderr bytes.Buffer
			code := runStage1(t, stage1Root, e.engine, e.level, &stdout, &stderr, args...)
			if stderr.Len() > 0 {
				t.Fatalf("stage1 wrote to stderr:\n%s", stderr.String())
			}
			// 1 because the corpus contains files that do not compile on their own: a
			// stage1 module importing its siblings, which are not in this package.
			if code != 1 {
				t.Errorf("stage1 exited %d, want 1", code)
			}
			got := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
			stdout.Reset()

			ids := ast.NewIDGen()
			preludeTree := parse.FileWith(source.NewFile(prelude.Name, string(pdata)), diag.New(), ids)
			at, total, bad, compiled := 0, 0, 0, 0
			for _, path := range files {
				want := goBytecode(t, ids, preludeTree, path)
				if len(want) > 1 {
					compiled++
				}
				total += len(want)
				for i, w := range want {
					if at+i >= len(got) {
						break
					}
					if got[at+i] != w {
						bad++
						if bad <= 8 {
							t.Errorf("%s, line %d:\n  stage1: %q\n  go:     %q",
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
				t.Errorf("stage1 printed %d lines, the Go compiler %d", len(got), total)
			}
			if bad == 0 && len(got) == total {
				t.Logf("%d files (%d compiled), %d lines identical", len(files), compiled, total)
			}
		})
	}
}

// TestStage1CompilesItsOwnSourceToBytecode is the whole thing: stage1's own source, as one
// package, lowered to bytecode by stage1 and by the compiler it replaces.
//
// It is the last step before a self-hosted compiler that runs. What is still missing is
// only the back half of code generation -- the SSA IR, the optimizer and the native
// backend -- so this bytecode is not yet executable by stage1 itself; it is executable by
// the virtual machine, which is what makes it a complete and checkable answer.
func TestStage1CompilesItsOwnSourceToBytecode(t *testing.T) {
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
			args := []string{"dump-bytecode", preludePath, "--package", srcRoot}
			for _, u := range units {
				args = append(args, u.File.Name)
			}

			var stdout, stderr bytes.Buffer
			if code := runStage1(t, srcRoot, e.engine, e.level, &stdout, &stderr, args...); code != 0 {
				t.Fatalf("stage1 exited %d, want 0\nstderr:\n%s\nstdout:\n%s",
					code, stderr.String(), truncate(stdout.String()))
			}
			got := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")

			want := goBytecodePackage(t, string(pdata), srcRoot, units)
			if len(got) != len(want) {
				t.Errorf("stage1 printed %d lines, the Go compiler %d", len(got), len(want))
			}
			bad := 0
			for i := 0; i < len(got) && i < len(want); i++ {
				if got[i] != want[i] {
					bad++
					if bad <= 8 {
						t.Errorf("line %d:\n  stage1: %q\n  go:     %q", i, got[i], want[i])
					}
				}
			}
			if bad > 8 {
				t.Errorf("... and %d more lines differ", bad-8)
			}
			if bad == 0 && len(got) == len(want) {
				t.Logf("%d modules, %d lines of bytecode identical", len(units), len(want))
			}
		})
	}
}

// goBytecode renders the Go compiler's bytecode for one file, in stage1's own output
// format: the `== path` header, then the disassembly. A file that does not get that far
// contributes its header and nothing else, which is what stage1 does too.
func goBytecode(t *testing.T, ids *ast.IDGen, preludeTree *ast.File, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pbag := diag.New()
	tree := parse.FileWith(source.NewFile(path, string(data)), pbag, ids)
	out := []string{"== " + path}
	if pbag.HasErrors() {
		// A file that does not parse has error nodes in its tree, and there is no code to
		// generate from one. The other differentials deliberately run the passes before
		// code generation over the recovered tree, which is what makes them demanding;
		// lowering it would only ask both compilers to refuse, which is what `originc`
		// does by stopping after parsing (spec/09-errors.md).
		return out
	}

	bag := diag.New()
	res := resolve.Program(bag, resolve.Input{File: preludeTree, Prelude: true},
		resolve.Input{File: tree})
	if bag.HasErrors() {
		return append(out, renderDiags(bag)...)
	}
	cbag := diag.New()
	tys := check.Program(cbag, res, preludeTree, tree)
	if cbag.HasErrors() {
		return append(out, renderDiags(cbag)...)
	}
	mbag := diag.New()
	mo := mono.Program(mbag, tys, preludeTree, tree)
	if mbag.HasErrors() {
		return append(out, renderDiags(mbag)...)
	}
	if mo.Entry == nil {
		// A library rather than a program: nothing to lower, and nothing wrong. The Go
		// compiler reports it as an error from compile.Program and puts nothing on
		// stdout; stage1 says the same by lowering nothing.
		return out
	}
	code, cerr := compile.Program(res, tys, mo, preludeTree, tree)
	if cerr != nil {
		t.Fatalf("%s: the Go compiler could not lower a checked program: %v", path, cerr)
	}
	return append(out, strings.Split(strings.TrimSuffix(code.Disassemble(), "\n"), "\n")...)
}

// goBytecodePackage is goBytecode over one whole package.
func goBytecodePackage(t *testing.T, preludeSrc, srcRoot string, units []driver.Unit) []string {
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
	if bag.HasErrors() {
		return append(out, renderDiags(bag)...)
	}
	cbag := diag.New()
	tys := check.Program(cbag, res, asts...)
	if cbag.HasErrors() {
		return append(out, renderDiags(cbag)...)
	}
	mbag := diag.New()
	mo := mono.Program(mbag, tys, asts...)
	if mbag.HasErrors() {
		return append(out, renderDiags(mbag)...)
	}
	code, cerr := compile.Program(res, tys, mo, asts...)
	if cerr != nil {
		t.Fatalf("the Go compiler could not lower %s: %v", srcRoot, cerr)
	}
	return append(out, strings.Split(strings.TrimSuffix(code.Disassemble(), "\n"), "\n")...)
}
