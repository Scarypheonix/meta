// Package docs enforces the documentation invariants from the project's process rules:
// specs exist, ADRs are uniquely numbered and structurally complete, and every deferred
// item carries a phase. These are cheap, they run in milliseconds, and they catch the
// documentation rot that a long-running project accumulates silently.
package docs

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/scarypheonix/meta/internal/testutil"
)

func specDir(t *testing.T) string { return filepath.Join(testutil.RepoRoot(t), "docs", "spec") }
func adrDir(t *testing.T) string  { return filepath.Join(testutil.RepoRoot(t), "docs", "adr") }

func TestEverySpecDocumentExists(t *testing.T) {
	required := []string{
		"00-overview.md", "01-lexical.md", "02-grammar.md", "03-types.md",
		"04-expressions.md", "05-patterns.md", "06-traits-generics.md",
		"07-modules.md", "08-memory-model.md", "09-errors.md", "10-examples.md",
		"11-codegen.md", "12-concurrency.md", "codes.md",
	}
	for _, name := range required {
		if _, err := os.Stat(filepath.Join(specDir(t), name)); err != nil {
			t.Errorf("missing spec document %s", name)
		}
	}
}

var adrNameRE = regexp.MustCompile(`^([0-9]{4})-[a-z0-9-]+\.md$`)

func TestADRsAreUniquelyNumberedAndComplete(t *testing.T) {
	entries, err := os.ReadDir(adrDir(t))
	if err != nil {
		t.Fatalf("reading ADR directory: %v", err)
	}
	seen := map[string]string{}
	count := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := adrNameRE.FindStringSubmatch(e.Name())
		if m == nil {
			t.Errorf("ADR %q does not match NNNN-kebab-title.md", e.Name())
			continue
		}
		if prev, dup := seen[m[1]]; dup {
			t.Errorf("ADR number %s used by both %s and %s", m[1], prev, e.Name())
		}
		seen[m[1]] = e.Name()
		count++

		body, err := os.ReadFile(filepath.Join(adrDir(t), e.Name()))
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}
		for _, section := range []string{"## Context", "## Decision", "## Consequences"} {
			if !strings.Contains(string(body), section) {
				t.Errorf("%s is missing the %q section", e.Name(), section)
			}
		}
	}
	if count < 2 {
		t.Errorf("expected at least the template and one real ADR, found %d", count)
	}
}

// phaseRe matches the phase column's own form, e.g. "Phase 7".
var phaseRe = regexp.MustCompile(`^Phase [0-9]+$`)

func TestDeferredItemsCarryAPhase(t *testing.T) {
	path := filepath.Join(testutil.RepoRoot(t), "docs", "deferred.md")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading deferred.md: %v", err)
	}
	rows := 0
	for n, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		// Every table row, not just the backticked ones: a weaker match would let
		// most of the file skip the check silently.
		if !strings.HasPrefix(line, "|") {
			continue
		}
		if strings.HasPrefix(line, "|---") || strings.Contains(line, "| Item |") {
			continue
		}
		rows++
		// The disposition is the last cell, not anywhere in the row. Matching the whole
		// line lets an item pass on its prose -- "delivered in Phase 5" in the *reason*
		// column satisfied a substring check while saying nothing about where the item
		// stands -- which is the opposite of what this test is for.
		cells := strings.Split(strings.Trim(line, "|"), "|")
		disposition := strings.TrimSpace(cells[len(cells)-1])
		ok := phaseRe.MatchString(disposition) ||
			disposition == "done" ||
			strings.Contains(disposition, "not planned") ||
			// An item can be blocked on a decision rather than scheduled behind one.
			// That is a disposition too, and a more informative one than a phase
			// number: it names what would have to change first.
			strings.Contains(disposition, "blocked on ")
		if !ok {
			t.Errorf("deferred.md:%d has no disposition in its last column "+
				"(want a phase, \"done\", \"not planned\", or \"blocked on ...\"), got %q: %s",
				n+1, disposition, line)
		}
	}
	if rows < 20 {
		t.Errorf("deferred.md has only %d item rows; the file has been truncated or the table format changed", rows)
	}
}

// TestDiagnosticsAvoidInternalIdentifiers enforces spec/09-errors.md rule 2 over the
// specification's diagnostic examples. From Phase 1 it also covers the compiler's
// message catalogue.
func TestDiagnosticsAvoidInternalIdentifiers(t *testing.T) {
	banned := regexp.MustCompile(`cannot unify t[0-9]+|\?[0-9]+ with|0x[0-9a-f]{8}`)
	// A document may quote the banned form as the thing not to do, but it must say so on
	// the line itself. An exemption that is invisible in the prose is a loophole.
	const allow = "<!-- allow-internal-identifier"
	for _, path := range testutil.Cases(t, specDir(t), ".md") {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		src := strings.Split(string(body), "\n")
		for n, line := range src {
			if strings.Contains(line, allow) {
				continue
			}
			// The quote may wrap; a marker on the next line covers this one too.
			if n+1 < len(src) && strings.Contains(src[n+1], allow) {
				continue
			}
			if m := banned.FindString(line); m != "" {
				t.Errorf("%s:%d contains an internal identifier in a diagnostic: %q",
					filepath.Base(path), n+1, m)
			}
		}
	}
}
