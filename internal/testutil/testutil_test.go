package testutil

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseExpectation(t *testing.T) {
	tests := []struct {
		name    string
		src     string
		want    Expectation
		wantErr bool
	}{
		{"accept", "// EXPECT: accept\nfn main() {}\n", Expectation{Verdict: Accept}, false},
		{"reject one code", "// EXPECT: reject E0308\n", Expectation{Verdict: Reject, Codes: []string{"E0308"}}, false},
		{"reject two codes", "// EXPECT: reject E0308 E0004\n", Expectation{Verdict: Reject, Codes: []string{"E0308", "E0004"}}, false},
		{"warning code", "// EXPECT: reject W0001\n", Expectation{Verdict: Reject, Codes: []string{"W0001"}}, false},
		{"leading comment", "// a note\n// EXPECT: accept\n", Expectation{Verdict: Accept}, false},
		{"leading blank line", "\n// EXPECT: accept\n", Expectation{Verdict: Accept}, false},
		{"missing header", "fn main() {}\n", Expectation{}, true},
		{"header after code", "fn main() {}\n// EXPECT: accept\n", Expectation{}, true},
		{"accept with code", "// EXPECT: accept E0308\n", Expectation{}, true},
		{"reject without code", "// EXPECT: reject\n", Expectation{}, true},
		{"unknown verdict", "// EXPECT: maybe\n", Expectation{}, true},
		{"empty verdict", "// EXPECT:\n", Expectation{}, true},
		{"malformed code", "// EXPECT: reject E308\n", Expectation{}, true},
		{"lowercase code", "// EXPECT: reject e0308\n", Expectation{}, true},
		{"non-numeric code", "// EXPECT: reject EABCD\n", Expectation{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseExpectation(tt.src)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseExpectation() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseExpectation() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestRepoRootFindsGoMod(t *testing.T) {
	root := RepoRoot(t)
	if _, err := filepath.Abs(root); err != nil {
		t.Fatalf("repo root %q is not a usable path: %v", root, err)
	}
	if len(Cases(t, filepath.Join(root, "docs", "adr"), ".md")) == 0 {
		t.Error("expected ADR files under docs/adr; Cases found none")
	}
}

func TestCasesIsSortedAndFiltersByExtension(t *testing.T) {
	root := RepoRoot(t)
	got := Cases(t, filepath.Join(root, "docs", "spec"), ".md")
	for i := 1; i < len(got); i++ {
		if got[i-1] >= got[i] {
			t.Fatalf("Cases is not sorted: %q before %q", got[i-1], got[i])
		}
	}
	if n := len(Cases(t, filepath.Join(root, "docs", "spec"), ".nonexistent")); n != 0 {
		t.Errorf("expected 0 files with a bogus extension, got %d", n)
	}
}

func TestCasesOnMissingDirectoryIsEmpty(t *testing.T) {
	if n := len(Cases(t, filepath.Join(RepoRoot(t), "no", "such", "dir"), ".origin")); n != 0 {
		t.Errorf("expected 0 cases in a missing directory, got %d", n)
	}
}
