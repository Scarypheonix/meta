// Package wasm holds the browser build to the same expectations the native engines are
// held to: the end-to-end corpus, compared byte for byte.
//
// This is a fourth engine in the sense Phase 5's native backend was a third, and it is
// added the same way. The corpus and its golden files are the oracle; nothing here has its
// own idea of what a program should print, because a second opinion about that is how a
// differential stops being one.
//
// docs/spec/playground-runtime.md §6 is what these tests enforce. §3 is why four cases are
// named as exclusions rather than matched by a pattern.
package wasm

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/scarypheonix/meta/internal/testutil"
)

// fsCases are the four corpus programs that use std::fs, and the only ones whose browser
// output is expected to differ from their native output (ADR-0033).
//
// They are listed by name, with the reason, rather than matched by a pattern over their
// source: a pattern would silently grow to cover a case that started failing for an
// unrelated reason, and the whole value of an exclusion list is that it cannot do that.
// TestFileOperationsFailInTheBrowser is what holds these four instead.
var fsCases = map[string]string{
	"file_read_write_round_trip": "writes a file and reads it back",
	"file_large_and_threaded":    "writes a file from several threads",
	"word_frequency":             "reads its input from a file",
	"option_and_result_methods":  "uses a file read to produce a Result to test methods on",
}

// engines is what the browser build exposes (ADR-0032). The optimization level is the VM's
// only; the interpreter walks the checked tree and has no bytecode to optimize.
var engines = []struct {
	name string
	opt  int
}{
	{"vm", 1},
	{"interp", 1},
}

type request struct {
	Case   string   `json:"case"`
	Name   string   `json:"name"`
	Path   string   `json:"path"`
	Args   []string `json:"args"`
	Stdin  string   `json:"stdin"`
	Engine string   `json:"engine"`
	Opt    int      `json:"opt"`
}

type response struct {
	Case          string `json:"case"`
	Engine        string `json:"engine"`
	Opt           int    `json:"opt"`
	Stdout        string `json:"stdout"`
	Stderr        string `json:"stderr"`
	Exit          int    `json:"exit"`
	Truncated     bool   `json:"truncated"`
	InternalError string `json:"internalError"`
	Thrown        string `json:"thrown"`
}

// expectation is one case's golden files: exactly what tests/e2e holds the native engines to.
type expectation struct {
	name   string
	source string // repository-relative, and therefore argv[0] and the name in diagnostics
	abs    string
	args   []string
	stdin  string
	stdout string
	stderr string
	exit   int
}

func TestTheCorpusRunsInTheBrowserBuild(t *testing.T) {
	root := testutil.RepoRoot(t)
	node := requireNode(t)

	cases := loadCorpus(t, root)
	if len(cases) < 90 {
		t.Fatalf("found %d runnable cases; the corpus has been truncated or the glob is wrong", len(cases))
	}

	var reqs []request
	for _, c := range cases {
		for _, e := range engines {
			reqs = append(reqs, request{
				Case: c.name, Name: c.source, Path: c.abs, Args: c.args, Stdin: c.stdin,
				Engine: e.name, Opt: e.opt,
			})
		}
	}

	got := runInWasm(t, root, node, reqs)
	byKey := map[string]response{}
	for _, r := range got {
		byKey[r.Case+"/"+r.Engine] = r
	}

	want := map[string]expectation{}
	for _, c := range cases {
		want[c.name] = c
	}

	for _, r := range reqs {
		key := r.Case + "/" + r.Engine
		res, ok := byKey[key]
		if !ok {
			t.Errorf("%s on the %s: the browser build reported nothing", r.Case, r.Engine)
			continue
		}
		exp := want[r.Case]
		switch {
		case res.Thrown != "":
			t.Errorf("%s on the %s: the module threw: %s", r.Case, r.Engine, res.Thrown)
		case res.InternalError != "":
			t.Errorf("%s on the %s: %s", r.Case, r.Engine, res.InternalError)
		case res.Truncated:
			t.Errorf("%s on the %s: output hit the ceiling, which no corpus case should", r.Case, r.Engine)
		}
		if res.Stdout != exp.stdout {
			t.Errorf("%s on the %s: stdout differs from the native run.\n got: %q\nwant: %q",
				r.Case, r.Engine, res.Stdout, exp.stdout)
		}
		if res.Stderr != exp.stderr {
			t.Errorf("%s on the %s: stderr differs from the native run.\n got: %q\nwant: %q",
				r.Case, r.Engine, res.Stderr, exp.stderr)
		}
		if res.Exit != exp.exit {
			t.Errorf("%s on the %s: exit status %d, native gives %d", r.Case, r.Engine, res.Exit, exp.exit)
		}
	}
	t.Logf("%d cases on %d engines: %d runs identical to the native corpus",
		len(cases), len(engines), len(reqs))
}

// TestFileOperationsFailInTheBrowser holds the four excluded cases' subject matter directly,
// so that the exclusion list above is not a hole in the suite.
//
// ADR-0033: every file operation fails, and it fails as the Err(IoError::Other) that
// spec/15-files.md already defines rather than as a trap, a compile error or a fabricated
// success. `file_exists` answering false is the true answer on a host with no files.
func TestFileOperationsFailInTheBrowser(t *testing.T) {
	root := testutil.RepoRoot(t)
	node := requireNode(t)

	const src = `use std::io;

fn main() {
    match read_to_string("anything.txt") {
        Result::Ok(text) => io::println("read \(text.len()) bytes"),
        Result::Err(e) => io::println("read: \(e.to_str())"),
    }
    match write_string("anything.txt", "x") {
        Result::Ok(_) => io::println("wrote it"),
        Result::Err(e) => io::println("write: \(e.to_str())"),
    }
    io::println("exists: \(file_exists("anything.txt"))");
}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "files.origin")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	// "I/O error" is how the prelude's `Show for IoError` renders `Other`, so this asserts
	// the case ADR-0033 chose and not merely that something failed.
	const want = "read: I/O error\nwrite: I/O error\nexists: false\n"
	var reqs []request
	for _, e := range engines {
		reqs = append(reqs, request{Case: "files", Name: "files.origin", Path: path, Engine: e.name, Opt: e.opt})
	}
	for _, r := range runInWasm(t, root, node, reqs) {
		if r.Stdout != want {
			t.Errorf("on the %s:\n got: %q\nwant: %q", r.Engine, r.Stdout, want)
		}
		if r.Exit != 0 {
			t.Errorf("on the %s: exit %d; a failed file operation is a value, not a trap", r.Engine, r.Exit)
		}
	}
}

// TestTheBrowserBuildCannotReachAFilesystem checks the structural half of ADR-0033.
//
// The decision was not only that file operations fail but that the code which could
// succeed is not linked, because Go's js/wasm port ships an `fs` shim that a Node host
// backs with the real filesystem -- so a build that merely declined to call `os` would
// still be a build that could. This is the difference between a module that cannot reach a
// filesystem and one that is trusted not to, and it is worth a test rather than a comment.
// It searches the module's bytes for the symbol names rather than asking `go tool nm`,
// which cannot read a wasm binary at all -- it answers "unrecognized object file", and an
// empty symbol list would make this test pass for the wrong reason. Searching the bytes was
// checked both ways before being relied on: a wasm binary built from a program that calls
// os.ReadFile contains the name, and this module does not.
//
// The positive control is the load-bearing half. Without it, a build that stripped its
// symbol names would satisfy every assertion below by containing no names at all.
func TestTheBrowserBuildCannotReachAFilesystem(t *testing.T) {
	root := testutil.RepoRoot(t)
	module := buildModule(t, root)

	image, err := os.ReadFile(module)
	if err != nil {
		t.Fatalf("reading the module: %v", err)
	}
	body := string(image)

	const control = "fmt.Fprintln"
	if !strings.Contains(body, control) {
		t.Fatalf("the module does not contain %q, so it carries no symbol names and this "+
			"test cannot tell whether the file operations are linked. It would otherwise "+
			"pass for the wrong reason.", control)
	}

	for _, sym := range []string{"os.ReadFile", "os.WriteFile", "os.OpenFile", "os.openFileNolog"} {
		if strings.Contains(body, sym) {
			t.Errorf("%s is linked into the browser build; ADR-0033 says it must not be", sym)
		}
	}
}

// loadCorpus reads the end-to-end cases and their golden files, minus the four that use
// std::fs.
func loadCorpus(t *testing.T, root string) []expectation {
	t.Helper()
	dir := filepath.Join(root, "tests", "e2e", "cases")
	paths, err := filepath.Glob(filepath.Join(dir, "*.origin"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)

	var out []expectation
	for _, p := range paths {
		name := strings.TrimSuffix(filepath.Base(p), ".origin")
		if _, skip := fsCases[name]; skip {
			continue
		}
		stem := strings.TrimSuffix(p, ".origin")

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
		stdout, err := os.ReadFile(stem + ".out")
		if err != nil {
			t.Errorf("%s: missing required companion %s.out", name, name)
			continue
		}
		e := expectation{
			name:   name,
			source: filepath.ToSlash(filepath.Join("tests", "e2e", "cases", name+".origin")),
			abs:    p,
			stdout: string(stdout),
			exit:   code,
		}
		if raw, err := os.ReadFile(stem + ".err"); err == nil {
			e.stderr = string(raw)
		}
		if raw, err := os.ReadFile(stem + ".args"); err == nil && len(raw) > 0 {
			e.args = strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
		}
		if raw, err := os.ReadFile(stem + ".in"); err == nil {
			e.stdin = string(raw)
		}
		out = append(out, e)
	}
	return out
}

// runInWasm builds the module, hands the requests to driver.js, and returns what came back.
func runInWasm(t *testing.T, root, node string, reqs []request) []response {
	t.Helper()
	module := buildModule(t, root)

	// driver.js resolves wasm_exec.js beside itself, so both are staged into one directory.
	dir := t.TempDir()
	stage(t, filepath.Join(root, "tests", "wasm", "driver.js"), filepath.Join(dir, "driver.js"))
	stage(t, filepath.Join(runtime.GOROOT(), "lib", "wasm", "wasm_exec.js"), filepath.Join(dir, "wasm_exec.js"))

	outPath := filepath.Join(dir, "results.json")
	manifest := filepath.Join(dir, "manifest.json")
	body, err := json.Marshal(map[string]any{"cases": reqs, "out": outPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, body, 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(node, filepath.Join(dir, "driver.js"), module, manifest)
	cmd.Dir = root
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("running the browser build under node: %v\n%s", err, stderr.String())
	}

	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("reading the driver's results: %v\n%s", err, stderr.String())
	}
	var got []response
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("parsing the driver's results: %v", err)
	}
	return got
}

// moduleOnce caches the built module for the package: it is the same six megabytes for
// every test here, and building it three times would be most of what this package costs.
var moduleOnce struct {
	path string
	err  error
	done bool
}

func buildModule(t *testing.T, root string) string {
	t.Helper()
	if !moduleOnce.done {
		moduleOnce.done = true
		dir, err := os.MkdirTemp("", "origin-wasm")
		if err != nil {
			moduleOnce.err = err
		} else {
			out := filepath.Join(dir, "origin.wasm")
			cmd := exec.Command("go", "build", "-o", out, "./cmd/originwasm")
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "GOOS=js", "GOARCH=wasm")
			if b, err := cmd.CombinedOutput(); err != nil {
				moduleOnce.err = err
				t.Logf("building the browser build:\n%s", b)
			} else {
				moduleOnce.path = out
			}
		}
	}
	if moduleOnce.err != nil {
		t.Fatalf("building the browser build: %v", moduleOnce.err)
	}
	return moduleOnce.path
}

func stage(t *testing.T, from, to string) {
	t.Helper()
	b, err := os.ReadFile(from)
	if err != nil {
		t.Fatalf("staging %s: %v", from, err)
	}
	if err := os.WriteFile(to, b, 0o644); err != nil {
		t.Fatalf("staging %s: %v", to, err)
	}
}

// requireNode finds the host that runs the module. Absent, this package skips with the
// reason named rather than passing silently (process rule 8) -- the same arrangement
// tests/debuginfo has for lldb.
func requireNode(t *testing.T) string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; it is what hosts the browser build outside a browser")
	}
	return node
}
