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
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/scarypheonix/meta/internal/driver"
	"github.com/scarypheonix/meta/internal/opt"
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

// engine names one way of running a program: an execution engine and, for the VM, an
// optimization level.
type engineSpec struct {
	name   string
	engine driver.Engine
	level  opt.Level
}

// engines is every way a program can be run. The end-to-end corpus goes through all of
// them and must produce byte-identical results, which is the exit criterion of Phase 3
// (two engines) and Phase 4 (three levels).
var engines = []engineSpec{
	{"interpreter", driver.Interpreter, opt.O0},
	{"vm-O0", driver.VM, opt.O0},
	{"vm-O1", driver.VM, opt.O1},
	{"vm-O2", driver.VM, opt.O2},
	{"native-O0", driver.Native, opt.O0},
	{"native-O1", driver.Native, opt.O1},
	{"native-O2", driver.Native, opt.O2},
}

// nativeSupported reports whether a case can go through the native backend yet.
//
// The backend does not lower every operation, and a case that reaches one gets an
// `unimplemented:` error rather than a wrong answer (process rule 8). Skipping those
// here, by name and with the reason, is how the suite stays honest about what is built:
// a case in this list is a case the native engine is not being tested on, and the list
// is meant to empty out. It is empty now — every case in the corpus runs on every
// engine — so a case landing here again is a real, temporary regression, not the norm.
var nativeSkips = map[string]string{}

// concurrencyCases are the cases that spawn a thread. The interpreter runs them today;
// the virtual machine and the native backend do not yet, so they are skipped there by
// name rather than quietly omitted from the corpus (process rule 8).
//
// This list is the live record of how far Phase 6 has reached, exactly as `nativeSkips`
// was for Phase 5, and it is meant to empty the same way: an entry disappears when the
// engine can run it, and nothing else changes.
var concurrencyCases = map[string]bool{
	"thread_spawn_and_join":        true,
	"channel_rendezvous_and_close": true,
	"mutex_guards_shared_mutation": true,
	"channel_send_on_closed_traps": true,
	"deadlock_is_a_trap":           true,
}

// concurrencySkips names the engines that cannot yet run a concurrent program.
var concurrencySkips = map[driver.Engine]string{
	driver.VM:     "Phase 6: the virtual machine has no scheduler yet",
	driver.Native: "Phase 6: the native backend has no scheduler yet",
}

// runsNatively reports whether the host can execute what the backend produces. Only
// x86-64 Linux can, which is the point of ADR-0003's second writer.
func runsNatively() bool { return runtime.GOOS == "linux" && runtime.GOARCH == "amd64" }

// TestPrograms compiles and runs each case on each engine and level, comparing stdout,
// stderr and exit status byte for byte against the expected files.
//
// The expectations come from docs/spec/10-examples.md, so a divergence means one of the
// engines disagrees with the specification and the suite says which one.
func TestPrograms(t *testing.T) {
	cases := loadCases(t)
	if len(cases) == 0 {
		t.Fatal("no end-to-end cases found; the suite would pass vacuously")
	}
	root := testutil.RepoRoot(t)

	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			for _, e := range engines {
				t.Run(e.name, func(t *testing.T) {
					if skip, ok := skipNative(c, e); ok {
						t.Skip(skip)
					}
					stdout, stderr, code := runCase(t, root, c, e)

					if stdout != c.WantOut {
						t.Errorf("stdout mismatch\n--- want ---\n%s\n--- got ---\n%s", c.WantOut, stdout)
					}
					wantErr := ""
					if c.HasErr {
						wantErr = c.WantErr
					}
					if stderr != wantErr {
						t.Errorf("stderr mismatch\n--- want ---\n%s\n--- got ---\n%s", wantErr, stderr)
					}
					if code != c.WantExit {
						t.Errorf("exit status = %d, want %d", code, c.WantExit)
					}
				})
			}
		})
	}
}

// TestEnginesAgree states the differential directly: whatever the engines produce, they
// must all produce the same thing.
//
// It is deliberately separate from the comparison against the expected files, so that a
// case whose expectations are wrong still reports "these two disagree" and names the
// pair, rather than showing four identical-looking failures.
func TestEnginesAgree(t *testing.T) {
	root := testutil.RepoRoot(t)
	for _, c := range loadCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			baseOut, baseErr, baseCode := runCase(t, root, c, engines[0])
			for _, e := range engines[1:] {
				if _, skipped := skipNative(c, e); skipped {
					continue
				}
				out, errText, code := runCase(t, root, c, e)
				if out != baseOut {
					t.Errorf("stdout differs between %s and %s\n--- %s ---\n%s\n--- %s ---\n%s",
						engines[0].name, e.name, engines[0].name, baseOut, e.name, out)
				}
				if errText != baseErr {
					t.Errorf("stderr differs between %s and %s\n--- %s ---\n%s\n--- %s ---\n%s",
						engines[0].name, e.name, engines[0].name, baseErr, e.name, errText)
				}
				if code != baseCode {
					t.Errorf("exit status differs: %s gave %d, %s gave %d",
						engines[0].name, baseCode, e.name, code)
				}
			}
		})
	}
}

// runCase executes one case from the repository root, so that diagnostics name the case
// by its repo-relative path and the expected stderr is stable wherever the test runs.
func runCase(t *testing.T, root string, c caseFile, e engineSpec) (string, string, int) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(wd) }()

	var stdout, stderr bytes.Buffer
	code := driver.RunAt(c.RelPath, e.engine, e.level, &stdout, &stderr)
	return stdout.String(), stderr.String(), code
}

// TestSuiteIsNotEmpty catches a case directory with no cases, which would otherwise let
// the whole suite pass vacuously.
func TestSuiteIsNotEmpty(t *testing.T) {
	if n := len(loadCases(t)); n < 5 {
		t.Errorf("only %d end-to-end cases; the suite is too thin to be meaningful", n)
	}
}

// skipNative reports why a case cannot run on the native engine, if it cannot.
func skipNative(c caseFile, e engineSpec) (string, bool) {
	if concurrencyCases[c.Name] {
		if reason, unsupported := concurrencySkips[e.engine]; unsupported {
			return reason, true
		}
	}
	if e.engine != driver.Native {
		return "", false
	}
	if !runsNatively() {
		return "this host cannot execute an x86-64 Linux executable", true
	}
	if reason, listed := nativeSkips[c.Name]; listed {
		return "Phase 5: " + reason, true
	}
	return "", false
}
