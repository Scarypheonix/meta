// Package optsnap gives every optimization pass its own snapshot.
//
// Phase 4's exit criterion is that each pass has one. The whole pipeline's output shows
// what the optimizer did but not which pass did it, so a change in one pass would show
// up as a diff attributed to all of them. Here each pass runs alone, over a program
// written to exercise it, and the golden records the IR before and after.
//
// A golden is only ever updated with UPDATE_GOLDEN=1, never by hand.
package optsnap

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/bytecode"
	"github.com/scarypheonix/meta/internal/check"
	"github.com/scarypheonix/meta/internal/compile"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/mono"
	"github.com/scarypheonix/meta/internal/opt"
	"github.com/scarypheonix/meta/internal/parse"
	"github.com/scarypheonix/meta/internal/prelude"
	"github.com/scarypheonix/meta/internal/resolve"
	"github.com/scarypheonix/meta/internal/source"
	"github.com/scarypheonix/meta/internal/testutil"
)

// build compiles a case to bytecode.
func build(t *testing.T, name, src string) *bytecode.Program {
	t.Helper()
	ids := ast.NewIDGen()
	bag := diag.New()
	pre := parse.FileWith(prelude.Source(), bag, ids)
	user := parse.FileWith(source.NewFile(name+".origin", src), bag, ids)
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
	mo := mono.Program(bag, tys, pre, user)
	if bag.HasErrors() {
		t.Fatalf("mono:\n%s", bag)
	}
	prog, err := compile.Program(res, tys, mo, pre, user)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return prog
}

func caseSource(t *testing.T, dir, name string) string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join(dir, name+".origin"))
	if err != nil {
		t.Fatalf("reading case %s: %v", name, err)
	}
	return string(src)
}

// TestEveryPassHasASnapshot is the exit criterion stated as a test: if a pass is added
// to the pipeline without a case exercising it, this fails.
func TestEveryPassHasASnapshot(t *testing.T) {
	dir := filepath.Join(testutil.RepoRoot(t), "tests", "optsnap", "cases")
	for _, name := range opt.PassNames() {
		if _, err := os.Stat(filepath.Join(dir, name+".origin")); err != nil {
			t.Errorf("pass %q has no snapshot case at cases/%s.origin", name, name)
		}
	}
	// Inlining runs outside the per-function pipeline, so it is listed separately.
	if _, err := os.Stat(filepath.Join(dir, "inline.origin")); err != nil {
		t.Error("inlining has no snapshot case at cases/inline.origin")
	}
}

func TestPassSnapshots(t *testing.T) {
	dir := filepath.Join(testutil.RepoRoot(t), "tests", "optsnap", "cases")
	names := append([]string{}, opt.PassNames()...)
	sort.Strings(names)

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			src := caseSource(t, dir, name)
			prog := build(t, name, src)
			before, after, err := opt.DumpPass(prog, name)
			if err != nil {
				t.Fatalf("running pass: %v", err)
			}
			if before == after {
				t.Errorf("the %q case is not exercising the pass: the IR is unchanged", name)
			}
			testutil.Golden(t, filepath.Join(dir, name+".ir"),
				renderWith(opt.Prerequisites(name), before, after))
		})
	}
}

func TestInlineSnapshot(t *testing.T) {
	dir := filepath.Join(testutil.RepoRoot(t), "tests", "optsnap", "cases")
	prog := build(t, "inline", caseSource(t, dir, "inline"))
	before, after, err := opt.DumpInline(prog)
	if err != nil {
		t.Fatalf("inlining: %v", err)
	}
	if before == after {
		t.Error("the inline case is not exercising the pass: the IR is unchanged")
	}
	testutil.Golden(t, filepath.Join(dir, "inline.ir"), render(before, after))
}

func render(before, after string) string { return renderWith(nil, before, after) }

// renderWith records which passes ran before the one under test, so the snapshot says
// what state it was operating on rather than leaving a reader to guess.
func renderWith(prereqs []string, before, after string) string {
	var sb strings.Builder
	if len(prereqs) > 0 {
		sb.WriteString("=== after prerequisites: " + strings.Join(prereqs, ", ") + " ===\n")
	} else {
		sb.WriteString("=== before ===\n")
	}
	sb.WriteString(before)
	sb.WriteString("=== after ===\n")
	sb.WriteString(after)
	return sb.String()
}
