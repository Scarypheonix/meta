// Command originc is the Origin compiler driver (stage0, written in Go).
//
// Phase 0 ships the driver skeleton only. Every subcommand that would require a
// compiler panics with "unimplemented"; per process rule 8, a stub that returns a
// plausible wrong answer is worse than one that stops.
package main

import (
	"fmt"
	"os"
)

// Version is the compiler version. It is not the language version; the language
// version lives in docs/spec/00-overview.md.
const Version = "0.1.0-phase0"

const usage = `originc ` + Version + `

usage:
  originc version            print the compiler version
  originc check <file>       parse and type-check, emit diagnostics only
  originc build <file>       compile to a native executable
  originc run <file>         compile and run

Phase 0 implements none of check/build/run. See docs/phases/ for status.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr *os.File) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "version", "--version", "-V":
		fmt.Fprintln(stdout, Version)
		return 0
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usage)
		return 0
	case "check":
		panic("unimplemented: originc check (Phase 1: lexer, parser, resolver)")
	case "build":
		panic("unimplemented: originc build (Phase 5: native x86-64 backend)")
	case "run":
		panic("unimplemented: originc run (Phase 1: tree-walking interpreter)")
	default:
		fmt.Fprintf(stderr, "originc: unknown subcommand %q\n\n", args[0])
		fmt.Fprint(stderr, usage)
		return 2
	}
}
