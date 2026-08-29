// Package e2e runs Origin programs and asserts on their exact stdout, stderr and exit
// status.
//
// A case is NAME.origin plus companions holding the expected result:
//
//	NAME.out    exact expected stdout (required, may be empty)
//	NAME.exit   exact expected exit status, one line (required)
//	NAME.err    exact expected stderr (optional; absent means stderr must be empty)
//
// Cases are derived from docs/spec/10-examples.md, which is normative. When a case and
// the specification disagree, one of them is a bug; the fix is never to edit the
// expected output to match the implementation.
package e2e

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/scarypheonix/meta/internal/driver"
	"github.com/scarypheonix/meta/internal/testutil"
)

// caseFile is one end-to-end case with its expectations loaded.
type caseFile struct {
	// RelPath is the path as it appears in diagnostics: relative to the repository
	// root, so that expected stderr is stable wherever the test runs from.
	RelPath  string
	AbsPath  string
	Name     string
	WantOut  string
	WantErr  string
	HasErr   bool
	WantExit int
}

func loadCases(t *testing.T) []caseFile {
	t.Helper()
	root := testutil.RepoRoot(t)
	dir := filepath.Join(root, "tests", "e2e", "cases")

	var out []caseFile
	for _, path := range testutil.Cases(t, dir, ".origin") {
		stem := strings.TrimSuffix(path, ".origin")
		name := filepath.Base(stem)

		wantOut, err := os.ReadFile(stem + ".out")
		if err != nil {
			t.Errorf("%s: missing required companion %s.out", name, name)
			continue
		}
		rawExit, err := os.ReadFile(stem + ".exit")
		if err != nil {
			t.Errorf("%s: missing required companion %s.exit", name, name)
			continue
		}
		code, err := strconv.Atoi(strings.TrimSpace(string(rawExit)))
		if err != nil {
			t.Errorf("%s.exit is not an integer: %q", name, rawExit)
			continue
		}
		if code < 0 || code > 255 {
			t.Errorf("%s.exit is %d, which is not a possible process exit status", name, code)
			continue
		}
		c := caseFile{AbsPath: path, Name: name, WantOut: string(wantOut), WantExit: code}
		if rel, err := filepath.Rel(root, path); err == nil {
			c.RelPath = rel
		} else {
			c.RelPath = path
		}
		if wantErr, err := os.ReadFile(stem + ".err"); err == nil {
			c.WantErr, c.HasErr = string(wantErr), true
		}
		out = append(out, c)
	}
	return out
}

// TestPrograms is the end-to-end suite: compile and run each case on each engine, then
// compare stdout, stderr and exit status byte for byte against the expected files.
//
// Running both engines against the same expectations is Phase 3's exit criterion. It is
// a real oracle rather than a smoke test: the expectations come from
// docs/spec/10-examples.md, so a divergence means one of the two engines disagrees with
// the specification, and the suite says which.
func TestPrograms(t *testing.T) {
	cases := loadCases(t)
	if len(cases) == 0 {
		t.Fatal("no end-to-end cases found; the suite would pass vacuously")
	}
	root := testutil.RepoRoot(t)

	engines := []struct {
		name   string
		engine driver.Engine
	}{
		{"interpreter", driver.Interpreter},
		{"vm", driver.VM},
	}

	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			for _, e := range engines {
				t.Run(e.name, func(t *testing.T) {
					stdout, stderr, code := runCase(t, root, c, e.engine)

					if stdout != c.WantOut {
						t.Errorf("stdout mismatch\n--- want ---\n%s\n--- got ---\n%s", c.WantOut, stdout)
					}
					wantErr := ""
					if c.HasErr {
						wantErr = c.WantErr
					}
					if stderr != wantErr {
						t.Errorf("stderr mismatch\n--- want ---\n%s\n--- got ---\n%s", wantErr, stderr)
					}
					if code != c.WantExit {
						t.Errorf("exit status = %d, want %d", code, c.WantExit)
					}
				})
			}
		})
	}
}

// TestEnginesAgree states the differential directly: whatever the two engines produce,
// they must produce the same thing. It is deliberately separate from the comparison
// against the expected files, so that a case whose expectations are wrong still reports
// "the engines disagree" rather than two identical-looking failures.
func TestEnginesAgree(t *testing.T) {
	root := testutil.RepoRoot(t)
	for _, c := range loadCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			iOut, iErr, iCode := runCase(t, root, c, driver.Interpreter)
			vOut, vErr, vCode := runCase(t, root, c, driver.VM)

			if iOut != vOut {
				t.Errorf("stdout differs between engines\n--- interpreter ---\n%s\n--- vm ---\n%s", iOut, vOut)
			}
			if iErr != vErr {
				t.Errorf("stderr differs between engines\n--- interpreter ---\n%s\n--- vm ---\n%s", iErr, vErr)
			}
			if iCode != vCode {
				t.Errorf("exit status differs: interpreter %d, vm %d", iCode, vCode)
			}
		})
	}
}

// runCase executes one case from the repository root, so that diagnostics name the case
// by its repo-relative path and the expected stderr is stable wherever the test runs.
func runCase(t *testing.T, root string, c caseFile, engine driver.Engine) (string, string, int) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(wd) }()

	var stdout, stderr bytes.Buffer
	code := driver.RunWith(c.RelPath, engine, &stdout, &stderr)
	return stdout.String(), stderr.String(), code
}

// TestSuiteIsNotEmpty catches a case directory with no cases, which would otherwise let
// the whole suite pass vacuously.
func TestSuiteIsNotEmpty(t *testing.T) {
	if n := len(loadCases(t)); n < 5 {
		t.Errorf("only %d end-to-end cases; the suite is too thin to be meaningful", n)
	}
}
