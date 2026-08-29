// Package snapshot pins the bytecode the compiler generates.
//
// Process rule 2 requires a snapshot of the generated IR for every feature. The point is
// not that any particular encoding is correct — the end-to-end suite decides that — but
// that a change to code generation is visible in a diff rather than silent. A golden
// file is only ever updated with UPDATE_GOLDEN=1, never by hand.
package snapshot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/check"
	"github.com/scarypheonix/meta/internal/compile"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/parse"
	"github.com/scarypheonix/meta/internal/prelude"
	"github.com/scarypheonix/meta/internal/resolve"
	"github.com/scarypheonix/meta/internal/source"
	"github.com/scarypheonix/meta/internal/testutil"
)

func TestBytecodeSnapshots(t *testing.T) {
	root := testutil.RepoRoot(t)
	dir := filepath.Join(root, "tests", "snapshot", "cases")

	cases := testutil.Cases(t, dir, ".origin")
	if len(cases) == 0 {
		t.Fatal("no snapshot cases found")
	}

	for _, path := range cases {
		name := strings.TrimSuffix(filepath.Base(path), ".origin")
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading case: %v", err)
			}

			ids := ast.NewIDGen()
			bag := diag.New()
			pre := parse.FileWith(prelude.Source(), bag, ids)
			user := parse.FileWith(source.NewFile(name+".origin", string(src)), bag, ids)
			if bag.HasErrors() {
				t.Fatalf("parse:\n%s", bag)
			}
			res := resolve.Program(bag,
				resolve.Input{File: pre, Prelude: true},
				resolve.Input{File: user})
			if bag.HasErrors() {
				t.Fatalf("resolve:\n%s", bag)
			}
			tys := check.Program(bag, res, pre, user)
			if bag.HasErrors() {
				t.Fatalf("check:\n%s", bag)
			}
			prog, cerr := compile.Program(res, tys, pre, user)
			if cerr != nil {
				t.Fatalf("compile: %v", cerr)
			}

			// The prelude's functions are compiled into every program and would swamp
			// the snapshot, so only the case's own functions are pinned.
			dump := onlyUserFunctions(prog.Disassemble())
			testutil.Golden(t, filepath.Join(dir, name+".bytecode"), dump)
		})
	}
}

// onlyUserFunctions drops the prelude's compiled functions from a disassembly. The
// prelude declares traits whose methods have no bodies, so in practice it contributes
// nothing; this keeps the snapshot stable if that ever changes.
func onlyUserFunctions(dump string) string {
	var out []string
	keep := true
	for _, line := range strings.Split(dump, "\n") {
		if strings.HasPrefix(line, "fn ") {
			keep = !strings.Contains(line, "<prelude>")
		}
		if keep {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}
