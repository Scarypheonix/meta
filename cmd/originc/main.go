// Command originc is the Origin compiler driver (stage0, written in Go).
//
// Phase 1 implements `check`, `run` and `dump-ast` on top of the tree-walking
// interpreter. `build` still panics with "unimplemented": per process rule 8, a stub
// that returns a plausible wrong answer is worse than one that stops.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/scarypheonix/meta/internal/driver"
	"github.com/scarypheonix/meta/internal/opt"
)

// Version is the compiler version. It is not the language version; the language
// version lives in docs/spec/00-overview.md.
const Version = "0.1.0-phase1"

const usage = `originc ` + Version + `

usage:
  originc version            print the compiler version
  originc check <file>       parse and resolve, emit diagnostics only
  originc run <file>         run a program with the tree-walking interpreter
  originc run --vm <file>    run it on the bytecode virtual machine instead
  originc run -O1 <file>     run it on the VM with the optimizer (also -O0, -O2)
  originc dump-ir <file>     print the SSA intermediate representation
  originc dump-ast <file>    print the parsed syntax tree
  originc dump-bytecode <f>  print the compiled bytecode
  originc build <file>       compile to a native executable

exit status:
  0    success
  1    the program was rejected, or the compiler reported errors
  2    the command line was wrong
  101  the program trapped

The build subcommand is unimplemented until Phase 5 (native x86-64 backend).
`

func main() {
	os.Exit(guarded(os.Args[1:]))
}

// guarded runs the requested subcommand and converts a compiler panic into the exit
// status spec/09-errors.md rule 7 requires: print "this is a compiler bug" with the
// stage it happened in, and exit 101. A panic must never reach the user as a Go stack
// trace with no explanation of what to do with it.
func guarded(args []string) (code int) {
	stage := "startup"
	if len(args) > 0 {
		stage = args[0]
	}
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		// An `unimplemented:` panic is a deliberate stop at a path that is not built
		// yet (process rule 8), not a bug. Saying "compiler bug" for it would be a
		// misleading message, which is its own kind of lie.
		if msg, ok := r.(string); ok && strings.HasPrefix(msg, "unimplemented:") {
			fmt.Fprintf(os.Stderr, "originc: %s\n", msg)
			code = driver.ExitTrap
			return
		}
		fmt.Fprintf(os.Stderr, "originc: this is a compiler bug, during `%s`: %v\n", stage, r)
		fmt.Fprintln(os.Stderr, "originc: please report it with the input that caused it")
		code = driver.ExitTrap
	}()
	return run(args)
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return driver.ExitUsage
	}
	switch args[0] {
	case "version", "--version", "-V":
		fmt.Fprintln(os.Stdout, Version)
		return driver.ExitOK
	case "help", "--help", "-h":
		fmt.Fprint(os.Stdout, usage)
		return driver.ExitOK
	case "check", "run", "dump-ast", "dump-bytecode", "dump-ir":
		engine := driver.Interpreter
		level := opt.O0
		rest := args[1:]
		for len(rest) > 0 {
			switch rest[0] {
			case "--vm":
				engine = driver.VM
			case "-O0":
				level = opt.O0
			case "-O1":
				engine, level = driver.VM, opt.O1
			case "-O2":
				engine, level = driver.VM, opt.O2
			default:
				goto done
			}
			rest = rest[1:]
		}
	done:
		if len(rest) != 1 {
			fmt.Fprintf(os.Stderr, "originc: `%s` takes exactly one file or directory\n", args[0])
			return driver.ExitUsage
		}
		switch args[0] {
		case "check":
			return driver.Check(rest[0], os.Stdout, os.Stderr)
		case "run":
			return driver.RunAt(rest[0], engine, level, os.Stdout, os.Stderr)
		case "dump-bytecode":
			return driver.DumpBytecode(rest[0], os.Stdout, os.Stderr)
		case "dump-ir":
			return driver.DumpIR(rest[0], level, os.Stdout, os.Stderr)
		default:
			return driver.DumpAST(rest[0], os.Stdout, os.Stderr)
		}
	case "build":
		panic("unimplemented: originc build (Phase 5: native x86-64 backend)")
	default:
		fmt.Fprintf(os.Stderr, "originc: unknown subcommand %q\n\n", args[0])
		fmt.Fprint(os.Stderr, usage)
		return driver.ExitUsage
	}
}
