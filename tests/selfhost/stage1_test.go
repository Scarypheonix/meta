package selfhost

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/scarypheonix/meta/internal/backend"
	"github.com/scarypheonix/meta/internal/bytecode"
	"github.com/scarypheonix/meta/internal/compile"
	"github.com/scarypheonix/meta/internal/driver"
	"github.com/scarypheonix/meta/internal/interp"
	"github.com/scarypheonix/meta/internal/obj"
	"github.com/scarypheonix/meta/internal/opt"
	"github.com/scarypheonix/meta/internal/vm"
)

// runStage1 runs stage1 on one engine at one optimization level, compiling it at most
// once per pair.
//
// It exists because this package's five differentials call stage1 about twenty times
// between them, and driver.RunAt compiles from source on every call: 13,500 lines of
// Origin through parsing, resolution, checking, monomorphization, the bytecode compiler,
// the optimizer and -- for native -- the whole backend and an ELF write, to run a program
// whose source has not changed since the last call. Compiling once per pair is the same
// coverage; the suite's five-minute ceiling is a hard constraint (CLAUDE.md) and this
// package is most of it.
//
// What is deliberately *not* cached is the run: each call gets a fresh interpreter, a
// fresh VM or a fresh process, so no state leaks from one differential into the next.
func runStage1(t *testing.T, root string, engine driver.Engine, level opt.Level, stdout, stderr io.Writer, args ...string) int {
	t.Helper()
	// Index 0 of what the program sees is the path it was compiled from, on every engine
	// (spec/17-process.md).
	argv := append([]string{root}, args...)

	b := stage1Build(t, root, engine, level)
	switch engine {
	case driver.Interpreter:
		in := interp.New(b.prog.Resolved, b.prog.Types, b.prog.Mono, stdout, stderr)
		in.SetArgs(argv)
		return in.Run()
	case driver.Native:
		cmd := exec.Command(b.exe)
		cmd.Args = argv
		cmd.Stdout, cmd.Stderr = stdout, stderr
		if err := cmd.Run(); err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				return ee.ExitCode()
			}
			t.Fatalf("running the compiled stage1: %v", err)
		}
		return driver.ExitOK
	default:
		return vm.New(b.code, vm.Config{Args: argv}, stdout, stderr).Run()
	}
}

// stage1Root is the package the whole-compiler differentials run. The lexer, parser and
// position differentials build smaller packages of their own, one module and a driver, and
// those are cached by their own paths.
const stage1Root = "stage1/src"

type stage1Key struct {
	root   string
	engine driver.Engine
	level  opt.Level
}

type stage1Compiled struct {
	prog *driver.Program
	code *bytecode.Program
	// exe is the built executable, for the native engine only.
	exe  string
	once sync.Once
	err  error
}

var (
	stage1Mu     sync.Mutex
	stage1Builds = map[stage1Key]*stage1Compiled{}
	stage1Dir    string
	stage1Exes   int
)

// stage1Build compiles stage1 for one engine and level, once.
//
// A relative root is resolved against the working directory, so a caller using one must
// already have changed to the repository root -- which the whole-compiler differentials do,
// because stage1 names the files it is given by the path on its command line and the Go
// side has to name them the same way.
func stage1Build(t *testing.T, root string, engine driver.Engine, level opt.Level) *stage1Compiled {
	t.Helper()
	stage1Mu.Lock()
	key := stage1Key{root, engine, level}
	b := stage1Builds[key]
	if b == nil {
		b = &stage1Compiled{}
		stage1Builds[key] = b
	}
	stage1Mu.Unlock()

	b.once.Do(func() { b.err = b.build(root, engine, level) })
	if b.err != nil {
		t.Fatalf("compiling %s for engine %d at -O%d: %v", root, engine, level, b.err)
	}
	return b
}

func (b *stage1Compiled) build(root string, engine driver.Engine, level opt.Level) error {
	units, err := driver.LoadUnits(root)
	if err != nil {
		return err
	}
	var diags failWriter
	prog, ok := driver.CompilePackage(units, &diags)
	if !ok {
		return errors.New(root + " does not compile:\n" + diags.text)
	}
	b.prog = prog
	if engine == driver.Interpreter {
		return nil
	}

	code, err := compile.Program(prog.Resolved, prog.Types, prog.Mono, prog.AllASTs...)
	if err != nil {
		return err
	}
	if err := opt.Run(code, level); err != nil {
		return err
	}
	b.code = code
	if engine != driver.Native {
		return nil
	}

	img, err := backend.Build(code, obj.Linux)
	if err != nil {
		return err
	}
	dir, err := stage1TempDir()
	if err != nil {
		return err
	}
	stage1Mu.Lock()
	stage1Exes++
	n := stage1Exes
	stage1Mu.Unlock()
	b.exe = filepath.Join(dir, fmt.Sprintf("stage1-%d", n))
	return driver.WriteExecutable(img, b.exe)
}

// stage1TempDir holds the built executables for the run. It outlives every subtest, so it
// cannot be a t.TempDir; TestMain removes it.
func stage1TempDir() (string, error) {
	stage1Mu.Lock()
	defer stage1Mu.Unlock()
	if stage1Dir != "" {
		return stage1Dir, nil
	}
	dir, err := os.MkdirTemp("", "origin-selfhost")
	if err != nil {
		return "", err
	}
	stage1Dir = dir
	return dir, nil
}

func TestMain(m *testing.M) {
	code := m.Run()
	if stage1Dir != "" {
		_ = os.RemoveAll(stage1Dir)
	}
	os.Exit(code)
}

// failWriter collects whatever the compiler wrote about a failure, so that a broken
// stage1 fails the suite with the diagnostics rather than with "not ok".
type failWriter struct{ text string }

func (w *failWriter) Write(p []byte) (int, error) {
	w.text += string(p)
	return len(p), nil
}
