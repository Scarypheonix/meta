// Package driver wires the compiler passes together for the command line.
//
// The pass order and its error suppression rule come from spec/09-errors.md: lex, then
// parse (which always runs so syntax errors are reported alongside lexical ones), then
// resolve, then everything after it, each gated on the previous producing no errors.
package driver

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/check"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/interp"
	"github.com/scarypheonix/meta/internal/parse"
	"github.com/scarypheonix/meta/internal/prelude"
	"github.com/scarypheonix/meta/internal/resolve"
	"github.com/scarypheonix/meta/internal/source"
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
	if bag.WarningCount() > 0 {
		bag.Render(w)
	}
	return &Program{File: rootFile, AST: rootAST, Resolved: res, Types: tys}, true
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
	return interp.New(prog.Resolved, stdout, stderr).Run()
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

func readFile(path string) (*source.File, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return source.NewFile(path, string(b)), nil
}
