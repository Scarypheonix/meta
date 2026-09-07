package selfhost

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/scarypheonix/meta/internal/backend"
	"github.com/scarypheonix/meta/internal/compile"
	"github.com/scarypheonix/meta/internal/driver"
	"github.com/scarypheonix/meta/internal/obj"
	"github.com/scarypheonix/meta/internal/opt"
	"github.com/scarypheonix/meta/internal/testutil"
)

// stage1 compiles a program all the way to a native executable, and the file it writes is
// held to the one `originc build` writes.
//
// This is the strongest oracle in the project and it needed no invention: two files that are
// byte-identical are the same program. It subsumes every differential below it -- the token
// stream, the tree, resolution, inference, monomorphization, the bytecode, the SSA, the
// encoded instructions and the executable's own layout are all upstream of these bytes, and a
// disagreement anywhere in them shows up here.
//
// It also reaches what none of those did. Three bugs turned up on the first run and every one
// was a *span*: the inliner treated an empty callee span as invalid where the Go compiler
// treats it as valid, a function's recorded span began at its name rather than at `fn`, and a
// `match` arm's began at its body rather than at its pattern. All three are invisible in the
// bytecode dump, which prints an instruction's operands and not where it came from, and all
// three are visible here -- in the DWARF line table's column, which is the only artefact in
// the project that renders a span.

// buildLevels is what a build differential costs: stage1 compiles the whole program, so each
// row is a full run of the compiler over the case *and* the prelude. The three levels are all
// kept because the optimizer is what reshapes the spans the line table renders, and `-O0`
// inlines nothing at all.
var buildLevels = []opt.Level{opt.O0, opt.O1, opt.O2}

// buildIdentifier names the code a Mach-O's ad-hoc signature covers (ADR-0024). `originc build`
// takes it from its own `-o`; stage1 has no `-o`, because a `String` is UTF-8 by construction
// and an executable is not text, so both sides are told the same name here.
const buildIdentifier = "case"

// buildTargets is every (level, container) pair. Mach-O is not a fourth spelling of the same
// file: it is based at 0x100000000 rather than 0x400000, so every address in the code differs,
// and it carries an ad-hoc SHA-256 signature over its own bytes that ELF has no analogue of.
var buildTargets = func() []struct {
	name   string
	level  opt.Level
	target obj.Target
	flag   string
} {
	var out []struct {
		name   string
		level  opt.Level
		target obj.Target
		flag   string
	}
	for _, level := range buildLevels {
		out = append(out, struct {
			name   string
			level  opt.Level
			target obj.Target
			flag   string
		}{fmt.Sprintf("linux-O%d", level), level, obj.Linux, "linux"})
		out = append(out, struct {
			name   string
			level  opt.Level
			target obj.Target
			flag   string
		}{fmt.Sprintf("macos-O%d", level), level, obj.MacOS, "macos"})
	}
	return out
}()

// TestStage1WritesTheSameExecutable compiles a sample of the end-to-end corpus with both
// compilers and compares the files.
//
// A sample rather than all of it: this is the most expensive differential in the suite by a
// wide margin, and the cases are chosen to span what a backend has to get right rather than to
// be exhaustive -- the whole corpus is swept by hand when the backend changes.
func TestStage1WritesTheSameExecutable(t *testing.T) {
	root := testutil.RepoRoot(t)
	cases := buildCases(t, root)

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(wd) }()

	for _, tc := range buildTargets {
		t.Run(tc.name, func(t *testing.T) {
			level := tc.level
			var want, args []string
			for _, c := range cases {
				want = append(want, goExecutable(t, c, level, tc.target)...)
				args = append(args, c)
			}

			// Native only. stage1 running its own backend over four programs is minutes of
			// work on the two hosted engines, and what this row is for is the *artefact*:
			// the engines are already held to each other everywhere below it.
			head := []string{"build", levelFlag(level), "--target", tc.flag,
				"--identifier", buildIdentifier,
				filepath.Join("internal", "prelude", "prelude.origin")}
			var stdout, stderr bytes.Buffer
			code := runStage1(t, stage1Root, driver.Native, opt.O2, &stdout, &stderr,
				append(head, args...)...)
			if code != 0 {
				t.Fatalf("stage1 exited %d\nstderr:\n%s", code, stderr.String())
			}

			// stage1 prints a `== <path>` header before each file's own output; the Go side
			// has no such header, so they are dropped rather than matched.
			var got []string
			for _, line := range strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n") {
				if strings.HasPrefix(line, "== ") && !strings.HasSuffix(line, " bytes") {
					continue
				}
				got = append(got, line)
			}

			if len(got) != len(want) {
				t.Errorf("stage1 printed %d lines, the Go backend %d", len(got), len(want))
			}
			bad := 0
			for i := 0; i < len(got) && i < len(want); i++ {
				if got[i] != want[i] {
					bad++
					if bad <= 6 {
						t.Errorf("line %d:\n  stage1: %s\n  go:     %s", i, got[i], want[i])
					}
				}
			}
			if bad > 6 {
				t.Errorf("... and %d more lines differ", bad-6)
			}
			if bad == 0 && len(got) == len(want) {
				t.Logf("%d executables, %d lines of bytes identical", len(cases), len(want))
			}
		})
	}
}

// buildCases names the end-to-end programs this compares, by what each one makes the backend
// do rather than by position in a sorted list -- a stride would silently stop covering
// closures or threads the next time a case is added.
func buildCases(t *testing.T, root string) []string {
	t.Helper()
	want := []string{
		// Traits, generics and the closure convention, which is most of the lowering.
		"generics_closures_and_threads.origin",
		// `cmp` and the three Ordering allocations, plus String ordering through the runtime.
		"cmp_and_ordering.origin",
		// Every arithmetic form at every width: trapping, wrapping, checked and saturating.
		"expression_language.origin",
		// The collections, which is the array primitives and the specified hash.
		"map_of_lists.origin",
	}
	var out []string
	for _, name := range want {
		p := filepath.Join(root, "tests", "e2e", "cases", name)
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("the build differential names %s, which is not in the corpus: %v", name, err)
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, rel)
	}
	sort.Strings(out)
	return out
}

// goExecutable is the Go compiler's answer for one program: the file `originc build` writes,
// as the same hex lines stage1 prints.
func goExecutable(t *testing.T, path string, level opt.Level, target obj.Target) []string {
	t.Helper()
	units, err := driver.LoadUnits(path)
	if err != nil {
		t.Fatalf("loading %s: %v", path, err)
	}
	var diags failWriter
	prog, ok := driver.CompilePackage(units, &diags)
	if !ok {
		t.Fatalf("%s does not compile:\n%s", path, diags.text)
	}
	code, err := compile.Program(prog.Resolved, prog.Types, prog.Mono, prog.AllASTs...)
	if err != nil {
		t.Fatalf("compiling %s: %v", path, err)
	}
	if err := opt.Run(code, level); err != nil {
		t.Fatalf("optimizing %s: %v", path, err)
	}
	img, err := backend.Build(code, target)
	if err != nil {
		t.Fatalf("building %s: %v", path, err)
	}
	img.Identifier = buildIdentifier
	var buf bytes.Buffer
	if err := img.Write(&buf); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return append([]string{fmt.Sprintf("== %d bytes", buf.Len())}, hexLines(buf.Bytes())...)
}
