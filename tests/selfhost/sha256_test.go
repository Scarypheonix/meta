package selfhost

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scarypheonix/meta/internal/driver"
	"github.com/scarypheonix/meta/internal/opt"
	"github.com/scarypheonix/meta/internal/testutil"
)

// stage1/src/sha256.origin is held to Go's crypto/sha256.
//
// It exists for exactly one reason: macOS will not run an unsigned executable (ADR-0024), an
// ad-hoc signature is a content hash and nothing else, and Origin has no cryptographic library
// to borrow one from. That makes it the one piece of stage1 whose correctness is not implied by
// the executables agreeing -- a hash both compilers got wrong in the same way would still
// produce identical files -- so it is checked against a third implementation.
//
// FIPS 180-4's own vectors would test less than this does: every length from 0 to 130 covers the
// empty message, the point at 56 where the length field stops fitting in the final block, the
// exact block boundary at 64, and the second boundary at 128.
func TestStage1HashesTheSameAsCryptoSHA256(t *testing.T) {
	want := goDigests()

	dir := t.TempDir()
	writeShaPackage(t, dir)

	for _, e := range []struct {
		name   string
		engine driver.Engine
		level  opt.Level
	}{
		{"native-O2", driver.Native, opt.O2},
		{"native-O0", driver.Native, opt.O0},
		{"vm-O2", driver.VM, opt.O2},
		{"interpreter", driver.Interpreter, opt.O0},
	} {
		t.Run(e.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := runStage1(t, dir, e.engine, e.level, &stdout, &stderr); code != 0 {
				t.Fatalf("the hash driver exited %d\nstderr:\n%s", code, stderr.String())
			}
			got := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
			if len(got) != len(want) {
				t.Fatalf("stage1 printed %d digests, Go %d", len(got), len(want))
			}
			bad := 0
			for i := range got {
				if got[i] != want[i] {
					bad++
					if bad <= 6 {
						t.Errorf("line %d:\n  stage1: %s\n  go:     %s", i, got[i], want[i])
					}
				}
			}
			if bad > 6 {
				t.Errorf("... and %d more differ", bad-6)
			}
			if bad == 0 {
				t.Logf("%d digests identical", len(want))
			}
		})
	}
}

// goDigests is Go's answer, in the driver's own output format.
func goDigests() []string {
	shaBytes := func(n int) []byte {
		out := make([]byte, n)
		for i := range out {
			out[i] = byte((i*37 + 11) % 251)
		}
		return out
	}
	var lengths []int
	for n := 0; n <= 130; n++ {
		lengths = append(lengths, n)
	}
	lengths = append(lengths, 1000, 4096, 4097)

	var out []string
	for _, n := range lengths {
		h := sha256.Sum256(shaBytes(n))
		out = append(out, fmt.Sprintf("%d %s", n, hex.EncodeToString(h[:])))
	}
	big := shaBytes(9000)
	for lo := 0; lo < 9000; lo += 4096 {
		hi := lo + 4096
		if hi > 9000 {
			hi = 9000
		}
		h := sha256.Sum256(big[lo:hi])
		out = append(out, fmt.Sprintf("%d..%d %s", lo, hi, hex.EncodeToString(h[:])))
	}
	return out
}

func writeShaPackage(t *testing.T, dir string) {
	t.Helper()
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	root := testutil.RepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "stage1", "src", "sha256.origin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sha256.origin"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	main, err := os.ReadFile(filepath.Join(root, "tests", "selfhost", "testdata", "sha256_driver.origin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "main.origin"), main, 0o644); err != nil {
		t.Fatal(err)
	}
}
