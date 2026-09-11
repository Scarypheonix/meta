//go:build js

// Command originwasm is Origin's browser host: the compiler front end, the bytecode
// compiler, the optimizer and both engines, compiled to WebAssembly and handed a program
// as a string instead of a path.
//
// docs/spec/playground-runtime.md is normative for what this does; ADR-0032 (both engines,
// the VM by default), ADR-0033 (a host with no filesystem) and ADR-0036 (a worker, chunked
// and bounded output) are the decisions it implements. The native backend is deliberately
// absent -- nothing here imports internal/backend, internal/obj or internal/x86, so a
// change that made the browser build depend on machine code would fail to build rather
// than quietly grow the download.
//
// The boundary is data only (spec §1.1). It exports one function; an Origin program can
// reach no host object, no callback and no capability it was not handed.
package main

import (
	"strings"
	"syscall/js"
	"time"

	"github.com/scarypheonix/meta/internal/compile"
	"github.com/scarypheonix/meta/internal/driver"
	"github.com/scarypheonix/meta/internal/interp"
	"github.com/scarypheonix/meta/internal/opt"
	"github.com/scarypheonix/meta/internal/source"
	"github.com/scarypheonix/meta/internal/vm"
)

// The two tuning numbers spec §4 leaves to this file.
const (
	// outputLimit caps what one run may accumulate on each stream. The largest expected
	// output in the end-to-end corpus is 2,333 bytes, so this is three orders of magnitude
	// clear of any program the suite holds this build to, and is here for the runaway
	// `while true { io::println(..) }` rather than for anything intended.
	outputLimit = 4 << 20

	// flushInterval is how stale delivered output may get while a program is still
	// running. It is checked when the program writes, not on a timer: js/wasm has no
	// asynchronous preemption, so a goroutine woken by a ticker is not guaranteed to run
	// while an Origin loop is spinning, and a flush that depends on the program yielding
	// is a flush that does not happen in the one case it exists for. Checking at write
	// time needs no scheduler cooperation at all -- a program with output to deliver is,
	// by construction, a program that is writing.
	flushInterval = 50 * time.Millisecond

	// flushBytes delivers eagerly when a program is producing output faster than the
	// interval, so a burst arrives in a few chunks rather than one per write.
	flushBytes = 8 << 10
)

// defaultName is what the program is called when the caller does not say.
//
// It is both argv[0] (spec/17-process.md: index 0 is the path the program was compiled
// from, on every engine) and the name diagnostics render, exactly as `driver.RunAt` uses
// one path for both. The caller can set it, which is what lets the differential give a
// case the relative path the native run gave it and compare the two byte for byte.
const defaultName = "playground.origin"

func main() {
	js.Global().Set("originRun", js.FuncOf(run))
	// The Go runtime exits when main returns, taking the exported function with it.
	select {}
}

// run compiles and runs one program. It is the whole boundary.
//
// Request:  {source, name?, engine?, opt?, args?, stdin?, onOutput?}
// Response: {stdout, stderr, exit, truncated, internalError?}
//
// stdout, stderr and exit are exactly what `originc run` produces for the same program on
// the same engine -- diagnostics included, since a rejected program renders them to stderr
// and exits 1 there too. That correspondence is what tests/wasm checks, and it is why this
// function reports a compile failure the same way it reports a trap rather than inventing
// a category for it.
func run(_ js.Value, args []js.Value) any {
	if len(args) == 0 || args[0].Type() != js.TypeObject {
		return map[string]any{"internalError": "originRun expects one request object"}
	}
	req := args[0]

	var sink js.Value
	if cb := req.Get("onOutput"); cb.Type() == js.TypeFunction {
		sink = cb
	}
	stdout := &capture{stream: "stdout", sink: sink}
	stderr := &capture{stream: "stderr", sink: sink}

	resp := map[string]any{}
	exit := func() int {
		// A Go panic reaching here is a defect in this compiler, not in the program that
		// provoked it, and spec §7 keeps the three kinds of text apart: it is reported on
		// the host's own channel and never written into either stream.
		defer func() {
			if r := recover(); r != nil {
				resp["internalError"] = "this is a compiler bug: " + describe(r)
			}
		}()
		return execute(req, stdout, stderr)
	}()

	stdout.flush(true)
	stderr.flush(true)

	resp["stdout"] = stdout.text.String()
	resp["stderr"] = stderr.text.String()
	resp["exit"] = exit
	resp["truncated"] = stdout.truncated || stderr.truncated
	return resp
}

// execute is driver.RunAt's body with the filesystem taken out of it: the source arrives as
// a string, so there is no path to load and no directory to walk.
func execute(req js.Value, stdout, stderr *capture) int {
	src := req.Get("source")
	if src.Type() != js.TypeString {
		return driver.ExitUsage
	}
	argv := argVector(req)

	f := source.NewFile(argv[0], src.String())
	prog, ok := driver.CompilePackage([]driver.Unit{{File: f}}, stderr)
	if !ok {
		return driver.ExitDiagnostics
	}

	if req.Get("engine").String() == "interp" {
		in := interp.New(prog.Resolved, prog.Types, prog.Mono, stdout, stderr)
		in.SetArgs(argv)
		in.SetStdin(strings.NewReader(standardInput(req)))
		return in.Run()
	}

	code, err := compile.Program(prog.Resolved, prog.Types, prog.Mono, prog.AllASTs...)
	if err != nil {
		stderr.WriteString("originc: " + err.Error() + "\n")
		return driver.ExitDiagnostics
	}
	if err := opt.Run(code, level(req)); err != nil {
		stderr.WriteString("originc: " + err.Error() + "\n")
		return driver.ExitDiagnostics
	}
	return vm.New(code, vm.Config{Args: argv, Stdin: strings.NewReader(standardInput(req))}, stdout, stderr).Run()
}

// standardInput is what `read_line` reads (spec/18-input.md).
//
// This is the one place ADR-0033 does not extend. A host with no filesystem fails every
// file operation, because there is no file and nothing on the page could be one; a host
// with no standard input is a different case, because a page *can* have text in a box and
// the caller can hand it over. A caller that hands over nothing gets an empty stream, which
// is what a program run with its input closed gets anywhere.
func standardInput(req js.Value) string {
	if v := req.Get("stdin"); v.Type() == js.TypeString {
		return v.String()
	}
	return ""
}

// level reads the optimization level, defaulting to -O1 as `originc build` does. It is
// ignored by the interpreter, which runs the checked tree and has no bytecode to optimize.
func level(req js.Value) opt.Level {
	switch req.Get("opt").String() {
	case "0":
		return opt.O0
	case "2":
		return opt.O2
	}
	if n := req.Get("opt"); n.Type() == js.TypeNumber {
		switch n.Int() {
		case 0:
			return opt.O0
		case 2:
			return opt.O2
		}
	}
	return opt.O1
}

// argVector builds argv. Index 0 is the program's name on every engine, and the caller's
// arguments follow it, so `args()` in the browser reads what it reads at a terminal.
func argVector(req js.Value) []string {
	name := defaultName
	if n := req.Get("name"); n.Type() == js.TypeString && n.String() != "" {
		name = n.String()
	}
	argv := []string{name}
	list := req.Get("args")
	if list.Type() != js.TypeObject {
		return argv
	}
	for i := 0; i < list.Length(); i++ {
		argv = append(argv, list.Index(i).String())
	}
	return argv
}

// capture is one of the program's two streams: an io.Writer that accumulates, delivers what
// it has to the page as it goes, and stops accumulating at a ceiling.
//
// Ordering within a stream is exact and content is untouched (spec §4). Chunking decides
// when bytes are delivered and nothing else, which is why the differential can compare the
// concatenation against the native run byte for byte.
type capture struct {
	stream    string
	sink      js.Value
	text      strings.Builder
	pending   strings.Builder
	truncated bool
	lastFlush time.Time
}

func (c *capture) Write(p []byte) (int, error) {
	c.WriteString(string(p))
	// A short count would make the engine believe the program's own write failed. It did
	// not: the ceiling is the host's limit, reported on the host's channel (spec §4), and
	// a truncated run is not a program that errored.
	return len(p), nil
}

func (c *capture) WriteString(s string) {
	if room := outputLimit - c.text.Len(); room < len(s) {
		if room <= 0 {
			c.truncated = true
			return
		}
		s, c.truncated = s[:room], true
	}
	c.text.WriteString(s)
	if c.sink.IsUndefined() {
		return
	}
	c.pending.WriteString(s)
	c.flush(false)
}

// flush delivers what has accumulated. force is used at every terminal event, so a program
// that printed and then never returned still shows what it printed.
func (c *capture) flush(force bool) {
	if c.pending.Len() == 0 || c.sink.IsUndefined() {
		return
	}
	if !force && c.pending.Len() < flushBytes && time.Since(c.lastFlush) < flushInterval {
		return
	}
	c.sink.Invoke(c.stream, c.pending.String())
	c.pending.Reset()
	c.lastFlush = time.Now()
}

func describe(r any) string {
	if err, ok := r.(error); ok {
		return err.Error()
	}
	if s, ok := r.(string); ok {
		return s
	}
	return "unknown failure"
}
