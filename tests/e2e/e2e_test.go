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

// TestPrograms is the end-to-end suite: compile and run each case, then compare stdout,
// stderr and exit status byte for byte.
func TestPrograms(t *testing.T) {
	cases := loadCases(t)
	if len(cases) == 0 {
		t.Fatal("no end-to-end cases found; the suite would pass vacuously")
	}
	root := testutil.RepoRoot(t)

	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			// Run from the repository root so diagnostics name the case by its
			// repo-relative path, matching the expected stderr.
			wd, err := os.Getwd()
			if err != nil {
				t.Fatalf("getwd: %v", err)
			}
			if err := os.Chdir(root); err != nil {
				t.Fatalf("chdir: %v", err)
			}
			defer func() { _ = os.Chdir(wd) }()

			var stdout, stderr bytes.Buffer
			code := driver.Run(c.RelPath, &stdout, &stderr)

			if got := stdout.String(); got != c.WantOut {
				t.Errorf("stdout mismatch\n--- want ---\n%s\n--- got ---\n%s", c.WantOut, got)
			}
			wantErr := ""
			if c.HasErr {
				wantErr = c.WantErr
			}
			if got := stderr.String(); got != wantErr {
				t.Errorf("stderr mismatch\n--- want ---\n%s\n--- got ---\n%s", wantErr, got)
			}
			if code != c.WantExit {
				t.Errorf("exit status = %d, want %d", code, c.WantExit)
			}
		})
	}
}

// TestEveryCaseHasItsCompanions is folded into loadCases, which reports a missing
// companion as a test failure. This test exists so a case directory with no cases at
// all is caught rather than passing vacuously.
func TestSuiteIsNotEmpty(t *testing.T) {
	if n := len(loadCases(t)); n < 5 {
		t.Errorf("only %d end-to-end cases; the suite is too thin to be meaningful", n)
	}
}
