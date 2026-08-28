// Package e2e runs compiled Origin programs and asserts on their exact stdout, stderr
// and exit status.
//
// Phase 0 has no compiler. What runs today is the structural half: every case must have
// the companion files that pin its expected result, and every expected exit status must
// be a status the specification can actually produce.
package e2e

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/scarypheonix/meta/internal/testutil"
)

// validExitStatuses are the only statuses spec/09-errors.md defines for a compiled
// program: success, an explicit non-zero exit, or a trap.
func isValidExit(code int) bool { return code >= 0 && code <= 255 }

func TestEveryCaseHasItsCompanions(t *testing.T) {
	cases := testutil.Cases(t, "cases", ".origin")
	if len(cases) == 0 {
		t.Skip("no end-to-end cases yet")
	}
	for _, path := range cases {
		t.Run(filepath.Base(path), func(t *testing.T) {
			stem := strings.TrimSuffix(path, ".origin")
			for _, required := range []string{".out", ".exit"} {
				if _, err := os.Stat(stem + required); err != nil {
					t.Errorf("missing required companion %s%s", filepath.Base(stem), required)
				}
			}
			raw, err := os.ReadFile(stem + ".exit")
			if err != nil {
				return // already reported
			}
			code, err := strconv.Atoi(strings.TrimSpace(string(raw)))
			if err != nil {
				t.Fatalf("%s.exit is not an integer: %q", filepath.Base(stem), raw)
			}
			if !isValidExit(code) {
				t.Errorf("%s.exit is %d, which is not a possible process exit status", filepath.Base(stem), code)
			}
		})
	}
}

// TestPrograms is the real end-to-end test. Phase 1 replaces the skip with the
// interpreter; Phase 5 adds the native path; Phase 4 adds the -O0/-O1/-O2 comparison.
func TestPrograms(t *testing.T) {
	t.Skip("unimplemented: running programs needs originc run (Phase 1)")
}
