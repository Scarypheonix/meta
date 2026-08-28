// Package driver wires the compiler passes together for the command line.
//
// The pass order and its error suppression rule come from spec/09-errors.md: lex, then
// parse (which always runs so syntax errors are reported alongside lexical ones), then
// resolve, then everything after it, each gated on the previous producing no errors.
package driver

import (
	"fmt"
	"io"
	"os"

	"github.com/scarypheonix/meta/internal/ast"
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

// Program is a source file that has been parsed and resolved.
type Program struct {
	File     *source.File
	AST      *ast.File
	Resolved *resolve.Result
}

// Compile lexes, parses and resolves a source file together with the prelude, writing
// diagnostics to w. It returns the program and whether it is free of errors.
func Compile(f *source.File, w io.Writer) (*Program, bool) {
	bag := diag.New()

	preludeFile := prelude.Source()
	preludeBag := diag.New()
	preludeAST := parse.File(preludeFile, preludeBag)
	if preludeBag.HasErrors() {
		// The prelude ships with the compiler; a broken one is our bug, not the user's.
		fmt.Fprintln(w, "this is a compiler bug: the prelude does not parse")
		preludeBag.Render(w)
		return nil, false
	}

	userAST := parse.File(f, bag)
	if bag.HasErrors() {
		bag.Render(w)
		return nil, false
	}

	res := resolve.Files(bag, preludeAST, userAST)
	if bag.HasErrors() {
		bag.Render(w)
		return nil, false
	}
	if bag.WarningCount() > 0 {
		bag.Render(w)
	}
	return &Program{File: f, AST: userAST, Resolved: res}, true
}

// Check compiles a file and reports diagnostics only.
func Check(path string, stdout, stderr io.Writer) int {
	f, err := readFile(path)
	if err != nil {
		fmt.Fprintf(stderr, "originc: %v\n", err)
		return ExitUsage
	}
	if _, ok := Compile(f, stderr); !ok {
		return ExitDiagnostics
	}
	return ExitOK
}

// Run compiles a file and interprets it, returning the program's exit status.
func Run(path string, stdout, stderr io.Writer) int {
	f, err := readFile(path)
	if err != nil {
		fmt.Fprintf(stderr, "originc: %v\n", err)
		return ExitUsage
	}
	prog, ok := Compile(f, stderr)
	if !ok {
		return ExitDiagnostics
	}
	return interp.New(prog.Resolved, stdout, stderr).Run()
}

// DumpAST compiles a file and prints its syntax tree.
func DumpAST(path string, stdout, stderr io.Writer) int {
	f, err := readFile(path)
	if err != nil {
		fmt.Fprintf(stderr, "originc: %v\n", err)
		return ExitUsage
	}
	prog, ok := Compile(f, stderr)
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
