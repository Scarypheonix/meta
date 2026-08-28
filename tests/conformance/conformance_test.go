// Package conformance runs the type-system conformance suite.
//
// Phase 0 has no compiler, so the compilation half of each case cannot run yet. What
// runs today is everything else: every case must carry a well-formed expectation
// header, and every diagnostic code it names must be registered in
// docs/spec/codes.md. Those checks catch real mistakes now and keep the suite honest
// before Phase 2 wires the compiler in.
package conformance

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/scarypheonix/meta/internal/testutil"
)

func TestExpectationHeaders(t *testing.T) {
	cases := testutil.Cases(t, "cases", ".origin")
	if len(cases) == 0 {
		t.Skip("no conformance cases yet")
	}
	for _, path := range cases {
		t.Run(filepath.Base(path), func(t *testing.T) {
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading case: %v", err)
			}
			if _, err := testutil.ParseExpectation(string(src)); err != nil {
				t.Errorf("%v", err)
			}
		})
	}
}

func TestExpectedCodesAreRegistered(t *testing.T) {
	registered := registeredCodes(t)
	for _, path := range testutil.Cases(t, "cases", ".origin") {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading case: %v", err)
		}
		exp, err := testutil.ParseExpectation(string(src))
		if err != nil {
			continue // reported by TestExpectationHeaders
		}
		for _, code := range exp.Codes {
			if !registered[code] {
				t.Errorf("%s expects %s, which is not in docs/spec/codes.md", filepath.Base(path), code)
			}
		}
	}
}

// TestCompilationVerdicts is the real conformance test. It is deliberately a failing-
// loudly skip rather than a silent pass: Phase 2 must delete this skip, not discover it.
func TestCompilationVerdicts(t *testing.T) {
	t.Skip("unimplemented: conformance verdicts need originc check (Phase 2)")
}

var codeRE = regexp.MustCompile(`\|\s*([EW][0-9]{4})\s*\|`)

func registeredCodes(t *testing.T) map[string]bool {
	t.Helper()
	path := filepath.Join(testutil.RepoRoot(t), "docs", "spec", "codes.md")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	out := map[string]bool{}
	for _, m := range codeRE.FindAllStringSubmatch(string(src), -1) {
		if out[m[1]] {
			t.Errorf("diagnostic code %s is registered twice in codes.md; codes are permanent and unique", m[1])
		}
		out[m[1]] = true
	}
	if len(out) == 0 {
		t.Fatal("codes.md registered no diagnostic codes; the table format probably changed")
	}
	return out
}
