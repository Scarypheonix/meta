package vm_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/check"
	"github.com/scarypheonix/meta/internal/compile"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/gc"
	"github.com/scarypheonix/meta/internal/mono"
	"github.com/scarypheonix/meta/internal/parse"
	"github.com/scarypheonix/meta/internal/prelude"
	"github.com/scarypheonix/meta/internal/resolve"
	"github.com/scarypheonix/meta/internal/source"
	"github.com/scarypheonix/meta/internal/vm"
)

// build compiles a program to bytecode, failing the test on any diagnostic.
func build(t *testing.T, src string) *bytecodeProgram {
	t.Helper()
	ids := ast.NewIDGen()
	bag := diag.New()
	pre := parse.FileWith(prelude.Source(), bag, ids)
	user := parse.FileWith(source.NewFile("t.origin", src), bag, ids)
	if bag.HasErrors() {
		t.Fatalf("parse:\n%s", bag)
	}
	res := resolve.Program(bag,
		resolve.Input{File: pre, Prelude: true},
		resolve.Input{File: user})
	if bag.HasErrors() {
		t.Fatalf("resolve:\n%s", bag)
	}
	tys := check.Program(bag, res, pre, user)
	if bag.HasErrors() {
		t.Fatalf("check:\n%s", bag)
	}
	mo := mono.Program(bag, tys, pre, user)
	if bag.HasErrors() {
		t.Fatalf("mono:\n%s", bag)
	}
	code, err := compile.Program(res, tys, mo, pre, user)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return &bytecodeProgram{code}
}

// runWith executes a program on a heap of the given size, which is how the tests force
// the collector to run under a real workload.
func runWith(t *testing.T, src string, cfg gc.Config) (string, string, int) {
	stdout, stderr, code, _ := runWithStats(t, src, cfg)
	return stdout, stderr, code
}

func runWithStats(t *testing.T, src string, cfg gc.Config) (string, string, int, gc.Stats) {
	t.Helper()
	p := build(t, src)
	var stdout, stderr bytes.Buffer
	m := vm.New(p.prog, vm.Config{Heap: cfg}, &stdout, &stderr)
	code := m.Run()
	return stdout.String(), stderr.String(), code, m.Stats()
}

func run(t *testing.T, src string) (string, string, int) {
	return runWith(t, src, gc.Config{})
}

func expectOut(t *testing.T, src, want string) {
	t.Helper()
	stdout, stderr, code := run(t, src)
	if code != 0 {
		t.Fatalf("exit %d\nstderr:\n%s", code, stderr)
	}
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

func expectTrap(t *testing.T, src, wantMsg string) {
	t.Helper()
	_, stderr, code := run(t, src)
	if code != vm.TrapExitCode {
		t.Fatalf("exit = %d, want %d\nstderr:\n%s", code, vm.TrapExitCode, stderr)
	}
	if !strings.Contains(stderr, wantMsg) {
		t.Errorf("trap = %q, want it to contain %q", stderr, wantMsg)
	}
	if !strings.HasPrefix(stderr, "origin: ") || !strings.Contains(stderr, "t.origin:") {
		t.Errorf("a trap must read `origin: <message> at <file>:<line>:<col>`, got %q", stderr)
	}
}

func wrap(body string) string {
	return "use std::io;\n\nfn main() {\n" + body + "\n}\n"
}

func TestArithmeticTraps(t *testing.T) {
	for _, tt := range []struct{ name, body, want string }{
		{"add", "let x = i64::MAX; let y = x + 1;", "arithmetic overflow"},
		{"sub", "let x = i64::MIN; let y = x - 1;", "arithmetic overflow"},
		{"mul", "let x = 4611686018427387904; let y = x * 4;", "arithmetic overflow"},
		{"div zero", "let z = 0; let y = 1 / z;", "divide by zero"},
		{"rem zero", "let z = 0; let y = 1 % z;", "remainder by zero"},
		{"shift", "let n = 64; let y = 1 << n;", "shift amount out of range"},
		{"negate min", "let x = i64::MIN; let y = -x;", "arithmetic overflow"},
	} {
		t.Run(tt.name, func(t *testing.T) { expectTrap(t, wrap(tt.body), tt.want) })
	}
}

func TestArithmeticResults(t *testing.T) {
	for _, tt := range []struct{ expr, want string }{
		{"2 + 3 * 4", "14"},
		{"(0 - 7) / 2", "-3"},
		{"(0 - 7) % 2", "-1"},
		{"1 << 10", "1024"},
		{"(0 - 1) >> 1", "-1"},
		{"6 & 3", "2"},
		{"6 | 3", "7"},
		{"6 ^ 3", "5"},
		{"i64::MAX.wrapping_add(1)", "-9223372036854775808"},
		{"5.saturating_mul(i64::MAX)", "9223372036854775807"},
		{"300 as u8", "44"},
		{"1.9 as i64", "1"},
		{"'A' as u32", "65"},
		{"true as i64", "1"},
	} {
		expectOut(t, wrap("    io::println(("+tt.expr+").to_str());"), tt.want+"\n")
	}
}

func TestShortCircuit(t *testing.T) {
	src := `use std::io;

fn loud(b: bool) -> bool {
    io::println("evaluated");
    b
}

fn main() {
    let a = false && loud(true);
    let b = true || loud(true);
    io::println("done");
}
`
	expectOut(t, src, "done\n")
}

func TestStructuralEqualityMatchesTheSpecification(t *testing.T) {
	for _, tt := range []struct{ expr, want string }{
		{"(1, 2) == (1, 2)", "true"},
		{"(1, 2) == (1, 3)", "false"},
		{`"a" == "a"`, "true"},
		{`"a" == "b"`, "false"},
		{"0.0 == (0.0 - 0.0)", "true"},
	} {
		expectOut(t, wrap("    io::println(("+tt.expr+").to_str());"), tt.want+"\n")
	}
	// NaN is equal to nothing, including itself -- inside an aggregate as well as
	// outside one, which is why the layout records which slots hold floats.
	expectOut(t, wrap("    let z = 0.0;\n    let nan = z / z;\n    io::println((nan == nan).to_str());\n    io::println(((nan, 1) == (nan, 1)).to_str());"),
		"false\nfalse\n")
}

func TestClosureCapturesTheSameObject(t *testing.T) {
	src := `use std::io;

struct Cell { mut v: i64 }

fn make_counter() -> fn() -> i64 {
    let cell = Cell { v: 0 };
    || { cell.v = cell.v + 1; cell.v }
}

fn main() {
    let c = make_counter();
    let d = make_counter();
    io::println(c().to_str());
    io::println(c().to_str());
    io::println(d().to_str());
}
`
	expectOut(t, src, "1\n2\n1\n")
}

func TestStackOverflowTraps(t *testing.T) {
	expectTrap(t, "fn down(n: i64) -> i64 { down(n + 1) }\n\nfn main() { let x = down(0); }\n", "stack overflow")
}

// TestCollectorRunsUnderTheVM is the point of the phase: the VM allocates on the real
// heap, the collector runs while a program is executing, and the program still produces
// the right answer. A tiny heap forces hundreds of collections during one run.
func TestCollectorRunsUnderTheVM(t *testing.T) {
	src := `use std::io;

struct Node { value: i64, next: Option[Node] }

fn build(n: i64, acc: Option[Node]) -> Option[Node] {
    if n == 0 {
        acc
    } else {
        build(n - 1, Option::Some(Node { value: n, next: acc }))
    }
}

fn sum(l: Option[Node]) -> i64 {
    match l {
        Option::None => 0,
        Option::Some(node) => node.value + sum(node.next),
    }
}

fn main() {
    let mut total = 0;
    let mut i = 0;
    while i < 200 {
        total = total + sum(build(20, Option::None));
        i = i + 1;
    }
    io::println(total.to_str());
}
`
	// A heap far too small to hold the run's allocations: it only completes if the
	// collector is reclaiming the lists that each iteration drops.
	stdout, stderr, code, stats := runWithStats(t, src, gc.Config{NurseryWords: 1 << 10, OldWords: 1 << 12, CardWords: 32})
	if code != 0 {
		t.Fatalf("exit %d\nstderr:\n%s", code, stderr)
	}
	// 200 iterations of 1+2+...+20.
	if stdout != "42000\n" {
		t.Errorf("stdout = %q, want %q", stdout, "42000\n")
	}
	if stats.MinorCollections == 0 {
		t.Fatal("no collection ran; the heap was large enough to hold the whole workload, so this test proved nothing")
	}
	t.Logf("%d allocations, %d minor and %d major collections during the run",
		stats.ObjectsAllocated, stats.MinorCollections, stats.MajorCollections)
}

func TestLiveDataSurvivesCollectionDuringExecution(t *testing.T) {
	src := `use std::io;

struct Box { value: i64 }

fn churn(n: i64) -> i64 {
    let mut i = 0;
    let mut acc = 0;
    while i < n {
        let junk = Box { value: i };
        acc = acc + junk.value;
        i = i + 1;
    }
    acc
}

fn main() {
    let kept = Box { value: 4242 };
    let churned = churn(5000);
    io::println(churned.to_str());
    io::println(kept.value.to_str());
}
`
	stdout, stderr, code, stats := runWithStats(t, src, gc.Config{NurseryWords: 256, OldWords: 1 << 12, CardWords: 16})
	if code != 0 {
		t.Fatalf("exit %d\nstderr:\n%s", code, stderr)
	}
	// The second line is the object held across every collection of the run.
	if !strings.HasSuffix(stdout, "4242\n") {
		t.Errorf("the live object did not survive: stdout = %q", stdout)
	}
	if stats.MinorCollections < 10 {
		t.Fatalf("only %d collections; the object was not held across enough of them to mean anything",
			stats.MinorCollections)
	}
}

func TestOutOfMemoryTraps(t *testing.T) {
	src := `struct Node { next: Option[Node] }

fn build(n: i64, acc: Option[Node]) -> Option[Node] {
    if n == 0 { acc } else { build(n - 1, Option::Some(Node { next: acc })) }
}

fn main() {
    let l = build(100000, Option::None);
}
`
	_, stderr, code := runWith(t, src, gc.Config{NurseryWords: 128, OldWords: 256, CardWords: 8})
	if code != vm.TrapExitCode {
		t.Fatalf("a heap that cannot hold the live set must trap, got exit %d\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "out of memory") && !strings.Contains(stderr, "stack overflow") {
		t.Errorf("expected an `out of memory` or `stack overflow` trap, got:\n%s", stderr)
	}
}
