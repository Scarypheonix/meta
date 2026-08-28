// Package testutil is the shared test harness for the Origin toolchain.
//
// It owns three things and nothing else: locating test cases on disk, parsing the
// expectation header that every conformance and end-to-end case carries, and comparing
// actual output against a golden file. It knows nothing about the compiler; adding a
// compiler import here is a layering violation.
package testutil

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// RepoRoot walks up from the working directory until it finds go.mod.
func RepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate repository root (no go.mod found)")
		}
		dir = parent
	}
}

// Cases returns every file under dir with the given extension, sorted by path so that
// test output is deterministic regardless of filesystem order.
func Cases(t *testing.T, dir, ext string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ext) {
			out = append(out, path)
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("walking %s: %v", dir, err)
	}
	sort.Strings(out)
	return out
}

// Verdict is the expected outcome of compiling a conformance case.
type Verdict string

const (
	// Accept means the case must compile with no errors.
	Accept Verdict = "accept"
	// Reject means the case must be rejected with at least one error.
	Reject Verdict = "reject"
)

// Expectation is the parsed form of a case file's header. Every conformance case must
// begin with a line of the form:
//
//	// EXPECT: accept
//	// EXPECT: reject E0308
//	// EXPECT: reject E0308 E0004
//
// A reject expectation lists the diagnostic codes that must appear. Listing no code on
// a reject is an error: "it fails somehow" is not an expectation.
type Expectation struct {
	Verdict Verdict
	Codes   []string
}

const expectPrefix = "// EXPECT:"

// ParseExpectation reads the expectation header from a case file's contents.
func ParseExpectation(src string) (Expectation, error) {
	for _, line := range strings.Split(src, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || (strings.HasPrefix(line, "//") && !strings.HasPrefix(line, expectPrefix)) {
			continue
		}
		if !strings.HasPrefix(line, expectPrefix) {
			break
		}
		fields := strings.Fields(strings.TrimPrefix(line, expectPrefix))
		if len(fields) == 0 {
			return Expectation{}, fmt.Errorf("EXPECT header has no verdict")
		}
		switch Verdict(fields[0]) {
		case Accept:
			if len(fields) > 1 {
				return Expectation{}, fmt.Errorf("EXPECT: accept takes no diagnostic codes, got %v", fields[1:])
			}
			return Expectation{Verdict: Accept}, nil
		case Reject:
			if len(fields) == 1 {
				return Expectation{}, fmt.Errorf("EXPECT: reject must name at least one diagnostic code")
			}
			for _, c := range fields[1:] {
				if !validCode(c) {
					return Expectation{}, fmt.Errorf("malformed diagnostic code %q", c)
				}
			}
			return Expectation{Verdict: Reject, Codes: fields[1:]}, nil
		default:
			return Expectation{}, fmt.Errorf("unknown verdict %q, want accept or reject", fields[0])
		}
	}
	return Expectation{}, fmt.Errorf("no %q header found", expectPrefix)
}

// validCode reports whether c looks like E1234 or W1234.
func validCode(c string) bool {
	if len(c) != 5 {
		return false
	}
	if c[0] != 'E' && c[0] != 'W' {
		return false
	}
	for _, r := range c[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Golden compares actual against the contents of goldenPath. Setting UPDATE_GOLDEN=1
// rewrites the file instead of failing, which is the only supported way to change a
// golden: editing one by hand to make a test pass defeats its purpose.
func Golden(t *testing.T, goldenPath, actual string) {
	t.Helper()
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatalf("creating golden dir: %v", err)
		}
		if err := os.WriteFile(goldenPath, []byte(actual), 0o644); err != nil {
			t.Fatalf("writing golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("reading golden %s: %v\nre-run with UPDATE_GOLDEN=1 to create it", goldenPath, err)
	}
	if string(want) != actual {
		t.Errorf("output does not match %s\n--- want ---\n%s\n--- got ---\n%s", goldenPath, want, actual)
	}
}
