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

	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/check"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/driver"
	"github.com/scarypheonix/meta/internal/parse"
	"github.com/scarypheonix/meta/internal/prelude"
	"github.com/scarypheonix/meta/internal/resolve"
	"github.com/scarypheonix/meta/internal/source"
	"github.com/scarypheonix/meta/internal/testutil"
)

// implementedCodes are the diagnostic codes the compiler can emit today. Add a code
// here in the same commit that starts emitting it.
var implementedCodes = map[string]string{
	// Emitted today.
	"E0001": "", "E0002": "", "E0004": "", "E0005": "", "E0006": "", "E0007": "",
	"E0023": "", "E0026": "", "E0027": "", "E0034": "", "E0046": "", "E0061": "",
	"E0062": "", "E0063": "", "E0107": "", "E0109": "", "E0119": "", "E0220": "",
	"E0277": "", "E0308": "", "E0309": "", "E0310": "", "E0369": "", "E0404": "",
	"E0407": "", "E0411": "", "E0412": "", "E0423": "", "E0424": "", "E0432": "",
	"E0433": "", "E0532": "", "E0533": "", "E0560": "", "E0571": "", "E0573": "",
	"E0574": "", "E0594": "", "E0599": "", "E0600": "", "E0603": "", "E0605": "",
	"E0609": "", "E0618": "", "E0658": "", "E0055": "", "E0700": "", "E0701": "", "E0702": "",
	"W0001": "", "W0002": "", "W0003": "",

	// Not yet emitted; the value names the phase that will emit it.
	"E0117": "Phase 8 (orphan rule needs more than one package)",
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

// TestDiagnosticQuality enforces the half of Phase 2's exit criterion that is about the
// messages rather than the verdicts: a type error must name a source span and explain
// the conflict in plain language.
//
// It runs over the whole reject corpus, so a new diagnostic that is merely correct but
// unhelpful fails here rather than reaching a user.
func TestDiagnosticQuality(t *testing.T) {
	// Phrasings that mean the compiler leaked its own bookkeeping into a message.
	internal := regexp.MustCompile(`\bt[0-9]+\b|\?[0-9]+|0x[0-9a-f]{8}|NodeID|\*types\.|\*ast\.`)

	// Codes whose message is a statement of fact that needs no further explanation.
	selfExplanatory := map[string]bool{
		"E0002": true, // syntax errors quote the expected token
		"E0001": true, // lexical errors name the character or literal
	}

	checked := 0
	for _, path := range caseFiles(t) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading case: %v", err)
		}
		exp, err := testutil.ParseExpectation(string(raw))
		if err != nil || exp.Verdict != testutil.Reject {
			continue
		}
		skip := false
		for _, code := range exp.Codes {
			if implementedCodes[code] != "" {
				skip = true
			}
		}
		if skip {
			continue
		}

		rel, relErr := filepath.Rel(testutil.RepoRoot(t), path)
		if relErr != nil {
			rel = path
		}
		name := filepath.Base(path)

		bag := diag.New()
		f := source.NewFile(rel, string(raw))
		ids := ast.NewIDGen()
		preludeAST := parse.FileWith(prelude.Source(), diag.New(), ids)
		userAST := parse.FileWith(f, bag, ids)
		if !bag.HasErrors() {
			res := resolve.Program(bag,
				resolve.Input{File: preludeAST, Prelude: true},
				resolve.Input{File: userAST})
			if !bag.HasErrors() {
				check.Program(bag, res, preludeAST, userAST)
			}
		}
		if !bag.HasErrors() {
			continue // TestCompilationVerdicts already reports this
		}
		checked++

		for _, d := range bag.All() {
			if d.Severity != diag.Error {
				continue
			}
			where := name + " " + d.Code

			if !d.Primary.Span.Valid() {
				t.Errorf("%s: diagnostic has no span", where)
				continue
			}
			if d.Primary.Span.File != f {
				continue // a diagnostic about the prelude is a compiler bug reported elsewhere
			}
			if m := internal.FindString(d.Msg + " " + d.Primary.Msg); m != "" {
				t.Errorf("%s: message contains the internal identifier %q: %s", where, m, d.Msg)
			}
			for _, n := range append(append([]string{}, d.Notes...), d.Helps...) {
				if m := internal.FindString(n); m != "" {
					t.Errorf("%s: note contains the internal identifier %q", where, m)
				}
			}
			if d.Primary.Msg == "" {
				t.Errorf("%s: the primary span has no label saying what is wrong", where)
			}
			if !selfExplanatory[d.Code] && len(d.Notes) == 0 && len(d.Helps) == 0 && len(d.Secondary) == 0 {
				t.Errorf("%s: %q has no note, help or second span; it states the problem but does not explain it",
					where, d.Msg)
			}
		}
	}
	if checked < 100 {
		t.Errorf("only %d rejected cases were examined; the corpus should be larger", checked)
	}
}
