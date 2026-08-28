// Package conformance runs the type-system conformance suite: every case carries an
// expected accept/reject verdict, and reject cases name the diagnostic codes they
// expect.
//
// Codes land phase by phase. A case whose expected codes are not yet implemented skips
// with the phase named, rather than passing silently or failing noisily — the skip
// disappears on its own when the code is implemented, so nobody has to remember to
// re-enable it.
package conformance

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/scarypheonix/meta/internal/driver"
	"github.com/scarypheonix/meta/internal/source"
	"github.com/scarypheonix/meta/internal/testutil"
)

// implementedCodes are the diagnostic codes the compiler can emit today. Add a code
// here in the same commit that starts emitting it.
var implementedCodes = map[string]string{
	"E0001": "", // lexical error
	"E0002": "", // syntax error
	"E0034": "", // ambiguous / duplicate method
	"E0433": "", // unresolved path
	"E0594": "", // cannot assign

	// Not yet emitted; the value names the phase that will emit it.
	"E0004": "Phase 2 (exhaustiveness checker)",
	"E0005": "Phase 2 (refutability checker)",
	"E0006": "Phase 2 (usefulness checker)",
	"E0007": "Phase 2 (or-pattern binding check)",
	"E0117": "Phase 2 (coherence)",
	"E0119": "Phase 2 (coherence)",
	"E0277": "Phase 2 (trait bounds)",
	"E0308": "Phase 2 (type checker)",
	"E0309": "Phase 2 (type checker)",
	"E0310": "Phase 2 (occurs check)",
	"E0432": "Phase 2 (module system)",
	"E0599": "Phase 2 (method resolution)",
	"E0603": "Phase 2 (visibility)",
	"E0055": "Phase 2 (monomorphization)",
}

func caseFiles(t *testing.T) []string {
	t.Helper()
	dir := filepath.Join(testutil.RepoRoot(t), "tests", "conformance", "cases")
	return testutil.Cases(t, dir, ".origin")
}

func TestExpectationHeaders(t *testing.T) {
	cases := caseFiles(t)
	if len(cases) == 0 {
		t.Fatal("no conformance cases found; the suite would pass vacuously")
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
	for _, path := range caseFiles(t) {
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
			if _, known := implementedCodes[code]; !known {
				t.Errorf("%s expects %s, which is not listed in implementedCodes; "+
					"add it with the phase that will emit it", filepath.Base(path), code)
			}
		}
	}
}

// TestCompilationVerdicts is the conformance suite proper.
func TestCompilationVerdicts(t *testing.T) {
	for _, path := range caseFiles(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading case: %v", err)
			}
			exp, err := testutil.ParseExpectation(string(raw))
			if err != nil {
				t.Skip("malformed expectation header; reported by TestExpectationHeaders")
			}
			for _, code := range exp.Codes {
				if phase := implementedCodes[code]; phase != "" {
					t.Skipf("unimplemented: %s arrives in %s", code, phase)
				}
			}

			rel, relErr := filepath.Rel(testutil.RepoRoot(t), path)
			if relErr != nil {
				rel = path
			}
			var out bytes.Buffer
			_, ok := driver.Compile(source.NewFile(rel, string(raw)), &out)

			switch exp.Verdict {
			case testutil.Accept:
				if !ok {
					t.Errorf("expected this to compile, but it was rejected:\n%s", out.String())
				}
			case testutil.Reject:
				if ok {
					t.Fatalf("expected rejection with %v, but it compiled", exp.Codes)
				}
				got := out.String()
				for _, code := range exp.Codes {
					if !strings.Contains(got, "["+code+"]") {
						t.Errorf("expected diagnostic %s, got:\n%s", code, got)
					}
				}
			}
		})
	}
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
