// Package driver wires the compiler passes together for the command line.
//
// The pass order and its error suppression rule come from spec/09-errors.md: lex, then
// parse (which always runs so syntax errors are reported alongside lexical ones), then
// resolve, then everything after it, each gated on the previous producing no errors.
package driver

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/backend"
	"github.com/scarypheonix/meta/internal/bytecode"
	"github.com/scarypheonix/meta/internal/check"
	"github.com/scarypheonix/meta/internal/compile"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/interp"
	"github.com/scarypheonix/meta/internal/mono"
	"github.com/scarypheonix/meta/internal/obj"
	"github.com/scarypheonix/meta/internal/opt"
	"github.com/scarypheonix/meta/internal/parse"
	"github.com/scarypheonix/meta/internal/prelude"
	"github.com/scarypheonix/meta/internal/resolve"
	"github.com/scarypheonix/meta/internal/source"
	"github.com/scarypheonix/meta/internal/vm"
)

// Exit statuses, per spec/09-errors.md.
const (
	// ExitOK means the compilation and, for `run`, the program succeeded.
	ExitOK = 0
	// ExitDiagnostics means the program was rejected.
	ExitDiagnostics = 1
	// ExitUsage means the command line was wrong.
	ExitUsage = 2
	// ExitTrap means the program trapped.
	ExitTrap = interp.TrapExitCode
)

// Program is a source file that has been parsed, resolved and type-checked.
type Program struct {
	File     *source.File
	AST      *ast.File
	Resolved *resolve.Result
	Types    *check.Result
	// AllASTs is every file of the compilation, prelude first. The bytecode compiler
	// needs them all, because a prelude enum's variants are constructed by user code.
	AllASTs []*ast.File
	// Mono is the program's instantiation set (ADR-0010): which specialized copy of
	// each generic function is needed, and which copy every call site reaches.
	Mono *mono.Result
}

// Compile lexes, parses, resolves and type-checks a single file with the prelude.
func Compile(f *source.File, w io.Writer) (*Program, bool) {
	return CompilePackage([]Unit{{File: f}}, w)
}

// Unit is one file of a package, with the module path it lives at.
type Unit struct {
	Module string
	File   *source.File
}

// CompilePackage compiles a whole package: the prelude, the root module, and every
// submodule. The pass order and its error suppression rule come from spec/09-errors.md:
// lex, then parse (which always runs, so syntax errors are reported alongside lexical
// ones), then resolve, then check, each gated on the previous producing no errors.
func CompilePackage(units []Unit, w io.Writer) (*Program, bool) {
	bag := diag.New()

	// One id generator for the whole compilation: side tables are keyed by node id, so
	// per-file numbering would make two files collide.
	ids := ast.NewIDGen()

	preludeFile := prelude.Source()
	preludeBag := diag.New()
	preludeAST := parse.FileWith(preludeFile, preludeBag, ids)
	if preludeBag.HasErrors() {
		// The prelude ships with the compiler; a broken one is our bug, not the user's.
		fmt.Fprintln(w, "this is a compiler bug: the prelude does not parse")
		preludeBag.Render(w)
		return nil, false
	}

	inputs := []resolve.Input{{File: preludeAST, Prelude: true}}
	asts := []*ast.File{preludeAST}
	var rootAST *ast.File
	var rootFile *source.File
	for _, u := range units {
		tree := parse.FileWith(u.File, bag, ids)
		inputs = append(inputs, resolve.Input{Module: u.Module, File: tree})
		asts = append(asts, tree)
		if u.Module == "" && rootAST == nil {
			rootAST, rootFile = tree, u.File
		}
	}
	if bag.HasErrors() {
		bag.Render(w)
		return nil, false
	}
	if rootAST == nil && len(asts) > 1 {
		rootAST, rootFile = asts[1], units[0].File
	}

	res := resolve.Program(bag, inputs...)
	if bag.HasErrors() {
		bag.Render(w)
		return nil, false
	}

	tys := check.Program(bag, res, asts...)
	if bag.HasErrors() {
		bag.Render(w)
		return nil, false
	}

	// Monomorphization (ADR-0010) runs as part of checking a program's legality: a
	// polymorphic-recursion chain that exceeds the instantiation depth (E0055) is
	// rejected before any code is generated, and `originc check` must reach the same
	// verdict as `originc run`.
	mo := mono.Program(bag, tys, asts...)
	if bag.HasErrors() {
		bag.Render(w)
		return nil, false
	}

	if bag.WarningCount() > 0 {
		bag.Render(w)
	}
	return &Program{File: rootFile, AST: rootAST, Resolved: res, Types: tys, AllASTs: asts, Mono: mo}, true
}

// LoadUnits reads a program from disk. A file is compiled on its own; a directory is
// compiled as a package, with each file's path under it becoming its module path
// (spec/07-modules.md: the filesystem is the module tree).
func LoadUnits(path string) ([]Unit, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		f, err := readFile(path)
		if err != nil {
			return nil, err
		}
		return []Unit{{File: f}}, nil
	}

	root := path
	if sub := filepath.Join(path, "src"); dirExists(sub) {
		root = sub
	}

	var units []Unit
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".origin") {
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		f, readErr := readFile(p)
		if readErr != nil {
			return readErr
		}
		units = append(units, Unit{Module: modulePath(rel), File: f})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(units) == 0 {
		return nil, fmt.Errorf("no .origin files under %s", root)
	}
	// The root module first, then the rest in a stable order.
	sort.SliceStable(units, func(i, j int) bool {
		if (units[i].Module == "") != (units[j].Module == "") {
			return units[i].Module == ""
		}
		return units[i].Module < units[j].Module
	})
	return units, nil
}

// modulePath turns a path relative to the source root into a module path. `main.origin`
// and `lib.origin` at the root are the root module itself.
func modulePath(rel string) string {
	rel = strings.TrimSuffix(filepath.ToSlash(rel), ".origin")
	if rel == "main" || rel == "lib" {
		return ""
	}
	return strings.ReplaceAll(rel, "/", "::")
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// Check compiles a file and reports diagnostics only.
func Check(path string, stdout, stderr io.Writer) int {
	units, err := LoadUnits(path)
	if err != nil {
		fmt.Fprintf(stderr, "originc: %v\n", err)
		return ExitUsage
	}
	if _, ok := CompilePackage(units, stderr); !ok {
		return ExitDiagnostics
	}
	return ExitOK
}

// Engine selects which execution engine `run` uses.
type Engine int

const (
	// Interpreter is the Phase 1 tree-walking interpreter.
	Interpreter Engine = iota
	// VM is the Phase 3 bytecode virtual machine.
	VM
	// Native is the Phase 5 backend: the program is compiled to machine code, written
	// to a temporary executable, and run as a process.
	Native
)

// RunWith compiles a program and executes it on the chosen engine at -O0.
func RunWith(path string, engine Engine, stdout, stderr io.Writer) int {
	return RunAt(path, engine, opt.O0, stdout, stderr)
}

// RunAt compiles a program at an optimization level and executes it.
//
// Every engine and every level must produce byte-identical stdout, byte-identical
// stderr and the same exit status. That differential is the exit criterion of Phase 3
// (two engines) and Phase 4 (three levels), and tests/e2e runs the whole corpus through
// all of them.
func RunAt(path string, engine Engine, level opt.Level, stdout, stderr io.Writer) int {
	units, err := LoadUnits(path)
	if err != nil {
		fmt.Fprintf(stderr, "originc: %v\n", err)
		return ExitUsage
	}
	prog, ok := CompilePackage(units, stderr)
	if !ok {
		return ExitDiagnostics
	}
	if engine == Interpreter {
		return interp.New(prog.Resolved, prog.Types, prog.Mono, stdout, stderr).Run()
	}
	code, err := compile.Program(prog.Resolved, prog.Types, prog.Mono, prog.AllASTs...)
	if err != nil {
		fmt.Fprintf(stderr, "originc: %v\n", err)
		return ExitDiagnostics
	}
	if err := opt.Run(code, level); err != nil {
		fmt.Fprintf(stderr, "originc: %v\n", err)
		return ExitDiagnostics
	}
	if engine == Native {
		return runNative(code, stdout, stderr)
	}
	return vm.New(code, vm.Config{}, stdout, stderr).Run()
}

// runNative compiles to an executable for the host, runs it, and returns its exit
// status.
//
// It exists so that the end-to-end differential can include the native engine: the same
// corpus, the same expectations, compared byte for byte against the interpreter and the
// virtual machine. Only the host's own format can be run, which is why the container
// verifies ELF and the Mach-O acceptance criterion belongs to the user's machine
// (ADR-0003).
func runNative(code *bytecode.Program, stdout, stderr io.Writer) int {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		fmt.Fprintln(stderr, "originc: this host cannot run what it builds; use `originc build`")
		return ExitUsage
	}
	img, err := backend.Build(code, obj.Linux)
	if err != nil {
		fmt.Fprintf(stderr, "originc: %v\n", err)
		return ExitDiagnostics
	}

	dir, err := os.MkdirTemp("", "origin-native")
	if err != nil {
		fmt.Fprintf(stderr, "originc: %v\n", err)
		return ExitDiagnostics
	}
	defer os.RemoveAll(dir)

	exe := filepath.Join(dir, "program")
	if err := WriteExecutable(img, exe); err != nil {
		fmt.Fprintf(stderr, "originc: %v\n", err)
		return ExitDiagnostics
	}

	cmd := exec.Command(exe)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		fmt.Fprintf(stderr, "originc: running the compiled program: %v\n", err)
		return ExitDiagnostics
	}
	return ExitOK
}

// WriteExecutable writes an image to disk with the execute bit set.
func WriteExecutable(img *obj.Image, path string) error {
	var buf bytes.Buffer
	if err := img.Write(&buf); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o755)
}

// Build compiles a program to a native executable.
func Build(path, outPath, targetName string, level opt.Level, stdout, stderr io.Writer) int {
	target, err := obj.TargetFor(targetName)
	if err != nil {
		fmt.Fprintf(stderr, "originc: %v\n", err)
		return ExitUsage
	}
	units, err := LoadUnits(path)
	if err != nil {
		fmt.Fprintf(stderr, "originc: %v\n", err)
		return ExitUsage
	}
	prog, ok := CompilePackage(units, stderr)
	if !ok {
		return ExitDiagnostics
	}
	code, cerr := compile.Program(prog.Resolved, prog.Types, prog.Mono, prog.AllASTs...)
	if cerr != nil {
		fmt.Fprintf(stderr, "originc: %v\n", cerr)
		return ExitDiagnostics
	}
	if err := opt.Run(code, level); err != nil {
		fmt.Fprintf(stderr, "originc: %v\n", err)
		return ExitDiagnostics
	}
	img, err := backend.Build(code, target)
	if err != nil {
		fmt.Fprintf(stderr, "originc: %v\n", err)
		return ExitDiagnostics
	}
	if outPath == "" {
		outPath = defaultOutputName(path)
	}
	// A Mach-O's ad-hoc signature names the code it covers (ADR-0024). `codesign` itself
	// defaults that name to the output file's own, so this does too.
	img.Identifier = filepath.Base(outPath)
	if err := WriteExecutable(img, outPath); err != nil {
		fmt.Fprintf(stderr, "originc: %v\n", err)
		return ExitDiagnostics
	}
	return ExitOK
}

// defaultOutputName is the source's name with its extension removed, which is what a
// programmer expects `originc build hello.origin` to leave behind.
func defaultOutputName(path string) string {
	base := filepath.Base(strings.TrimSuffix(path, ".origin"))
	if base == "." || base == "/" || base == "" {
		return "a.out"
	}
	return base
}

// RunRoundTrip executes a program through the IR with no optimization passes, which
// isolates SSA construction and emission from the passes built on them.
func RunRoundTrip(path string, stdout, stderr io.Writer) int {
	units, err := LoadUnits(path)
	if err != nil {
		fmt.Fprintf(stderr, "originc: %v\n", err)
		return ExitUsage
	}
	prog, ok := CompilePackage(units, stderr)
	if !ok {
		return ExitDiagnostics
	}
	code, err := compile.Program(prog.Resolved, prog.Types, prog.Mono, prog.AllASTs...)
	if err != nil {
		fmt.Fprintf(stderr, "originc: %v\n", err)
		return ExitDiagnostics
	}
	if err := opt.BuildOnly(code); err != nil {
		fmt.Fprintf(stderr, "originc: %v\n", err)
		return ExitDiagnostics
	}
	return vm.New(code, vm.Config{}, stdout, stderr).Run()
}

// Run compiles a file and interprets it, returning the program's exit status.
func Run(path string, stdout, stderr io.Writer) int {
	units, err := LoadUnits(path)
	if err != nil {
		fmt.Fprintf(stderr, "originc: %v\n", err)
		return ExitUsage
	}
	prog, ok := CompilePackage(units, stderr)
	if !ok {
		return ExitDiagnostics
	}
	return interp.New(prog.Resolved, prog.Types, prog.Mono, stdout, stderr).Run()
}

// DumpAST compiles a file and prints its syntax tree.
func DumpAST(path string, stdout, stderr io.Writer) int {
	units, err := LoadUnits(path)
	if err != nil {
		fmt.Fprintf(stderr, "originc: %v\n", err)
		return ExitUsage
	}
	prog, ok := CompilePackage(units, stderr)
	if !ok {
		return ExitDiagnostics
	}
	fmt.Fprint(stdout, ast.Dump(prog.AST))
	return ExitOK
}

// DumpBytecode compiles a program and prints its bytecode. It is the snapshot artefact
// process rule 2 requires for every feature that reaches code generation.
func DumpBytecode(path string, stdout, stderr io.Writer) int {
	units, err := LoadUnits(path)
	if err != nil {
		fmt.Fprintf(stderr, "originc: %v\n", err)
		return ExitUsage
	}
	prog, ok := CompilePackage(units, stderr)
	if !ok {
		return ExitDiagnostics
	}
	code, cerr := compile.Program(prog.Resolved, prog.Types, prog.Mono, prog.AllASTs...)
	if cerr != nil {
		fmt.Fprintf(stderr, "originc: %v\n", cerr)
		return ExitDiagnostics
	}
	fmt.Fprint(stdout, code.Disassemble())
	return ExitOK
}

// DumpIR compiles a program and prints its SSA form, optionally after optimizing it.
// It is the artefact the optimizer's snapshot tests compare.
func DumpIR(path string, level opt.Level, stdout, stderr io.Writer) int {
	units, err := LoadUnits(path)
	if err != nil {
		fmt.Fprintf(stderr, "originc: %v\n", err)
		return ExitUsage
	}
	prog, ok := CompilePackage(units, stderr)
	if !ok {
		return ExitDiagnostics
	}
	code, cerr := compile.Program(prog.Resolved, prog.Types, prog.Mono, prog.AllASTs...)
	if cerr != nil {
		fmt.Fprintf(stderr, "originc: %v\n", cerr)
		return ExitDiagnostics
	}
	text, derr := opt.DumpIR(code, level)
	if derr != nil {
		fmt.Fprintf(stderr, "originc: %v\n", derr)
		return ExitDiagnostics
	}
	fmt.Fprint(stdout, text)
	return ExitOK
}

func readFile(path string) (*source.File, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return source.NewFile(path, string(b)), nil
}
