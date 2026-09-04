// Package e2e's round-trip test isolates the IR from the optimizer.
package e2e

import (
	"bytes"
	"os"
	"testing"

	"github.com/scarypheonix/meta/internal/driver"
	"github.com/scarypheonix/meta/internal/testutil"
)

// TestIRRoundTrip runs every case through SSA construction and emission with no
// optimization passes at all.
//
// It exists so that a failure has one meaning. If this passes and an optimized level
// fails, the fault is in a pass; if this fails, the fault is in the IR itself. Without
// it, every miscompilation would have two suspects.
func TestIRRoundTrip(t *testing.T) {
	root := testutil.RepoRoot(t)
	for _, c := range loadCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			wd, err := os.Getwd()
			if err != nil {
				t.Fatalf("getwd: %v", err)
			}
			if err := os.Chdir(root); err != nil {
				t.Fatalf("chdir: %v", err)
			}
			defer func() { _ = os.Chdir(wd) }()

			var stdout, stderr bytes.Buffer
			var code int
			run := func() { code = driver.RunRoundTrip(c.RelPath, &stdout, &stderr, c.Args...) }
			// A case that touches the filesystem gets the same fresh scratch directory
			// every other engine's run of it gets (e2e_test.go's withScratch).
			if usesFiles(c) {
				withScratch(t, root, run)
			} else {
				run()
			}

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
