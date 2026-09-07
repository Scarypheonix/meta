package selfhost

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/scarypheonix/meta/internal/backend"
	"github.com/scarypheonix/meta/internal/compile"
	"github.com/scarypheonix/meta/internal/driver"
	"github.com/scarypheonix/meta/internal/obj"
	"github.com/scarypheonix/meta/internal/opt"
	"github.com/scarypheonix/meta/internal/testutil"
)

// The bootstrap: stage1 compiling its own source, and the binary that does it.
//
// This is Phase 9's exit criterion and there is nothing above it. `bootstrap/` holds the
// compiler for Origin written in Origin, compiled to native x86-64; these two tests hold both
// halves of what makes it a bootstrap rather than a build artefact nobody can reproduce.
//
// The oracle needs no invention, as it has not since `dump-bytecode`: two files that are
// byte-identical are the same program.

// bootstrapBinary is the committed compiler.
const bootstrapBinary = "bootstrap/stage1-linux-amd64"

// bootstrapLevel is what the committed binary was built at, and what it is asked to build at.
// `originc build`'s own default; the level has to be named on both sides because a compiler
// built at one level and asked for another produces a different file for good reason.
const bootstrapLevel = opt.O1

// TestTheBootstrapBinaryIsWhatTheCompilerBuilds rebuilds stage1 from source with `originc` and
// compares it against the committed binary.
//
// Process rule 9 is "never break the bootstrap", and this is what notices. It fails the moment
// a change to the Go compiler, to stage1's own source, or to the prelude would produce a
// different binary -- which is the signal to regenerate `bootstrap/` deliberately rather than
// to find the drift later, when the reason is no longer in anybody's head.
func TestTheBootstrapBinaryIsWhatTheCompilerBuilds(t *testing.T) {
	root := testutil.RepoRoot(t)
	want, err := os.ReadFile(filepath.Join(root, bootstrapBinary))
	if err != nil {
		t.Fatalf("reading the committed compiler: %v", err)
	}
	got := goBuildStage1(t, root)
	if !bytes.Equal(got, want) {
		t.Fatalf("`originc build -O%d %s` is %d bytes; %s is %d.\n"+
			"The bootstrap has drifted from its source. Regenerate it deliberately:\n"+
			"  go run ./cmd/originc build -O%d --target linux -o %s %s",
			bootstrapLevel, stage1Root, len(got), bootstrapBinary, len(want),
			bootstrapLevel, bootstrapBinary, stage1Root)
	}
	t.Logf("%d bytes identical", len(want))
}

// TestStage1CompilesItself runs the committed compiler over stage1's own source and checks that
// what comes out is the committed compiler.
//
// A compiler that can only be built by the compiler it replaces is not self-hosting. This is
// the fixed point: the same source, through two different compilers, giving the same bytes --
// and then those bytes doing it again.
//
// It costs about forty seconds, which is real against the suite's five-minute ceiling
// (CLAUDE.md) and is the most expensive single test in the project. It is here rather than
// behind a flag because it is the one thing Phase 9 exists to prove, and a proof nobody runs
// is not one.
func TestStage1CompilesItself(t *testing.T) {
	root := testutil.RepoRoot(t)
	exe := filepath.Join(root, bootstrapBinary)
	want, err := os.ReadFile(exe)
	if err != nil {
		t.Fatalf("reading the committed compiler: %v", err)
	}

	// stage1 names the files it is given by the path on its command line, so it runs from the
	// repository root exactly as `originc build stage1/src` does.
	args := []string{"build", levelFlag(bootstrapLevel), "--target", "linux",
		filepath.Join("internal", "prelude", "prelude.origin"),
		"--package", stage1Root}
	args = append(args, stage1Sources(t, root)...)

	cmd := exec.Command(exe, args...)
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("the committed compiler failed to compile its own source: %v\nstderr:\n%s",
			err, stderr.String())
	}
	if stderr.Len() > 0 {
		t.Fatalf("the committed compiler wrote to stderr:\n%s", stderr.String())
	}

	got := parseHexDump(t, stdout.String())
	if !bytes.Equal(got, want) {
		t.Fatalf("stage1 compiled itself to %d bytes; it is %d.\n"+
			"The compiler is no longer a fixed point of itself.", len(got), len(want))
	}
	t.Logf("%d bytes identical: the compiler reproduces itself", len(want))
}

// goBuildStage1 is `originc build -O1 --target linux stage1/src`, in process.
func goBuildStage1(t *testing.T, root string) []byte {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(wd) }()

	units, err := driver.LoadUnits(stage1Root)
	if err != nil {
		t.Fatalf("loading %s: %v", stage1Root, err)
	}
	var diags failWriter
	prog, ok := driver.CompilePackage(units, &diags)
	if !ok {
		t.Fatalf("%s does not compile:\n%s", stage1Root, diags.text)
	}
	code, err := compile.Program(prog.Resolved, prog.Types, prog.Mono, prog.AllASTs...)
	if err != nil {
		t.Fatalf("compiling %s: %v", stage1Root, err)
	}
	if err := opt.Run(code, bootstrapLevel); err != nil {
		t.Fatalf("optimizing %s: %v", stage1Root, err)
	}
	img, err := backend.Build(code, obj.Linux)
	if err != nil {
		t.Fatalf("building %s: %v", stage1Root, err)
	}
	var buf bytes.Buffer
	if err := img.Write(&buf); err != nil {
		t.Fatalf("writing %s: %v", stage1Root, err)
	}
	return buf.Bytes()
}

// stage1Sources is every module of stage1, by the repository-relative path stage1 will name it
// by. They are listed rather than discovered because stage1 has no directory listing
// (docs/deferred.md); this is the Go side doing what the caller would otherwise type.
func stage1Sources(t *testing.T, root string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(root, stage1Root, "*.origin"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) < 20 {
		t.Fatalf("found only %d modules in %s; the glob is wrong", len(matches), stage1Root)
	}
	var out []string
	for _, m := range matches {
		rel, err := filepath.Rel(root, m)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, rel)
	}
	sort.Strings(out)
	return out
}

// parseHexDump turns what stage1's `build` prints back into the file it describes: a
// `== <n> bytes` header, then the bytes as lowercase hex, thirty-two to a line.
//
// stage1 prints hex rather than writing the file because a `String` is UTF-8 by construction
// (spec/14-strings.md) and an executable is not text, so there is nothing to hand
// `fs::write_file`. The oracle is unaffected: two programs that print the same bytes wrote the
// same executable.
func parseHexDump(t *testing.T, out string) []byte {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	var want int
	var body []string
	for _, l := range lines {
		if strings.HasPrefix(l, "== ") {
			if n, err := fmt.Sscanf(l, "== %d bytes", &want); err == nil && n == 1 {
				continue
			}
			continue // the `== <path>` header stage1 prints before each package
		}
		body = append(body, l)
	}
	data, err := hex.DecodeString(strings.Join(body, ""))
	if err != nil {
		t.Fatalf("stage1's output is not hex: %v", err)
	}
	if want != 0 && len(data) != want {
		t.Fatalf("stage1 announced %d bytes and printed %d", want, len(data))
	}
	return data
}
