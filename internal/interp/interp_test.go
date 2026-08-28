package interp_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/scarypheonix/meta/internal/driver"
	"github.com/scarypheonix/meta/internal/source"
)

// run compiles and runs a program, returning stdout, stderr and the exit status.
func run(t *testing.T, src string) (string, string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	f := source.NewFile("t.origin", src)
	prog, ok := driver.Compile(f, &stderr)
	if !ok {
		return stdout.String(), stderr.String(), driver.ExitDiagnostics
	}
	code := newInterp(prog, &stdout, &stderr)
	return stdout.String(), stderr.String(), code
}

// wrap puts statements in a main function so tests read as programs, not fragments.
func wrap(body string) string {
	return "use std::io;\n\nfn main() {\n" + body + "\n}\n"
}

func expectOut(t *testing.T, src, want string) {
	t.Helper()
	stdout, stderr, code := run(t, src)
	if code != 0 {
		t.Fatalf("program exited %d\nstderr:\n%s", code, stderr)
	}
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

func expectTrap(t *testing.T, src, wantMsg string) {
	t.Helper()
	_, stderr, code := run(t, src)
	if code != driver.ExitTrap {
		t.Fatalf("exit status = %d, want %d (a trap)\nstderr:\n%s", code, driver.ExitTrap, stderr)
	}
	if !strings.Contains(stderr, wantMsg) {
		t.Errorf("trap message = %q, want it to contain %q", stderr, wantMsg)
	}
	if !strings.HasPrefix(stderr, "origin: ") {
		t.Errorf("a trap must be reported as `origin: <message> at <file>:<line>:<col>`, got %q", stderr)
	}
	if !strings.Contains(stderr, "t.origin:") {
		t.Errorf("a trap must name its source location, got %q", stderr)
	}
}

func TestArithmeticTraps(t *testing.T) {
	tests := []struct{ name, body, want string }{
		{"add overflow", "let x = 9223372036854775807; let y = x + 1;", "arithmetic overflow"},
		{"sub overflow", "let x = 0 - 9223372036854775807; let y = x - 2;", "arithmetic overflow"},
		{"mul overflow", "let x = 4611686018427387904; let y = x * 4;", "arithmetic overflow"},
		{"negate min", "let x = 0 - 9223372036854775807; let y = 0 - (x - 1);", "arithmetic overflow"},
		{"divide by zero", "let z = 0; let y = 1 / z;", "divide by zero"},
		{"remainder by zero", "let z = 0; let y = 1 % z;", "remainder by zero"},
		{"shift too far", "let n = 64; let y = 1 << n;", "shift amount out of range"},
		{"negative shift", "let n = 0 - 1; let y = 1 << n;", "shift amount out of range"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { expectTrap(t, wrap(tt.body), tt.want) })
	}
}

func TestArithmeticResults(t *testing.T) {
	tests := []struct{ expr, want string }{
		{"2 + 3 * 4", "14"},
		{"(0 - 7) / 2", "-3"}, // truncates toward zero
		{"(0 - 7) % 2", "-1"}, // sign of the dividend
		{"7 / 2", "3"},
		{"1 << 10", "1024"},
		{"(0 - 1) >> 1", "-1"}, // arithmetic shift
		{"6 & 3", "2"},
		{"6 | 3", "7"},
		{"6 ^ 3", "5"},
		{"9223372036854775807.wrapping_add(1)", "-9223372036854775808"},
		{"5.saturating_mul(9223372036854775807)", "9223372036854775807"},
	}
	for _, tt := range tests {
		expectOut(t, wrap("    io::println(("+tt.expr+").to_str());"), tt.want+"\n")
	}
}

func TestEvaluationOrderIsLeftToRight(t *testing.T) {
	src := `use std::io;

struct Log { mut s: String }

fn note(l: Log, m: String) -> i64 {
    l.s = m;
    io::println(m);
    1
}

fn main() {
    let l = Log { s: "" };
    let total = note(l, "left") + note(l, "right");
}
`
	expectOut(t, src, "left\nright\n")
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

func TestStructuralEquality(t *testing.T) {
	tests := []struct{ expr, want string }{
		{"1 == 1", "true"},
		{"1 == 2", "false"},
		{`"a" == "a"`, "true"},
		{"'a' == 'a'", "true"},
		{"(1, 2) == (1, 2)", "true"},
		{"(1, 2) == (1, 3)", "false"},
		{"1.0 == 1.0", "true"},
		{"0.0 == (0.0 - 0.0)", "true"},
	}
	for _, tt := range tests {
		expectOut(t, wrap("    io::println(("+tt.expr+").to_str());"), tt.want+"\n")
	}
}

func TestNaNIsNotEqualToItself(t *testing.T) {
	expectOut(t, wrap("    let z = 0.0;\n    let nan = z / z;\n    io::println((nan == nan).to_str());"), "false\n")
}

func TestCasts(t *testing.T) {
	tests := []struct{ expr, want string }{
		{"300 as u8", "44"},
		{"300 as i8", "44"},
		{"255 as i8", "-1"},
		{"1 as f64", "1.0"},
		{"1.9 as i64", "1"},
		{"(0.0 - 1.9) as i64", "-1"},
		{"'A' as u32", "65"},
		{"true as i64", "1"},
	}
	for _, tt := range tests {
		expectOut(t, wrap("    io::println(("+tt.expr+").to_str());"), tt.want+"\n")
	}
}

func TestFloatToIntCastTraps(t *testing.T) {
	expectTrap(t, wrap("    let big = 1.0e20;\n    let n = big as i32;"), "invalid float to integer cast")
	expectTrap(t, wrap("    let z = 0.0;\n    let n = (z / z) as i64;"), "invalid float to integer cast")
}

func TestFieldMutability(t *testing.T) {
	// spec/08-memory-model.md: a field not declared `mut` is fixed at construction.
	// Phase 2 rejects this at compile time; Phase 1 catches it here.
	src := `struct P { x: i64 }

fn main() {
    let p = P { x: 1 };
    p.x = 2;
}
`
	expectTrap(t, src, "is not declared `mut`")
}

func TestAliasingIsObservable(t *testing.T) {
	src := `use std::io;

struct C { mut n: i64 }

fn main() {
    let a = C { n: 0 };
    let b = a;
    b.n = 5;
    io::println(a.n.to_str());
}
`
	expectOut(t, src, "5\n")
}

func TestStackOverflowTraps(t *testing.T) {
	src := `fn down(n: i64) -> i64 { down(n + 1) }

fn main() {
    let x = down(0);
}
`
	expectTrap(t, src, "stack overflow")
}

func TestPanicExits101(t *testing.T) {
	expectTrap(t, wrap(`    panic("boom");`), "boom")
}

func TestMatchArmBindingsDoNotLeakBetweenArms(t *testing.T) {
	src := `use std::io;

enum E { A(i64, i64), B(i64) }

fn describe(e: E) -> i64 {
    match e {
        E::A(x, y) => x + y,
        E::B(x) => x,
    }
}

fn main() {
    io::println(describe(E::A(1, 2)).to_str());
    io::println(describe(E::B(9)).to_str());
}
`
	expectOut(t, src, "3\n9\n")
}

func TestGuardsAreEvaluatedOnlyAfterThePatternMatches(t *testing.T) {
	src := `use std::io;

enum E { A(i64), B }

fn f(e: E) -> String {
    match e {
        E::A(n) if n > 0 => "positive",
        E::A(n) => "non-positive",
        E::B => "b",
    }
}

fn main() {
    io::println(f(E::A(1)));
    io::println(f(E::A(0)));
    io::println(f(E::B));
}
`
	expectOut(t, src, "positive\nnon-positive\nb\n")
}

func TestUnmatchedMatchTraps(t *testing.T) {
	// Phase 2's exhaustiveness checker makes this a compile error (E0004). Until then a
	// trap is the honest behaviour: it stops rather than producing a wrong answer.
	src := `enum E { A, B }

fn main() {
    let v = E::B;
    match v {
        E::A => { }
    }
}
`
	expectTrap(t, src, "no match arm matched")
}

func TestNestedClosuresShareTheCapturedObject(t *testing.T) {
	src := `use std::io;

struct Cell { mut v: i64 }

fn main() {
    let c = Cell { v: 0 };
    let inc = || { c.v = c.v + 1; c.v };
    let read = || c.v;
    let a = inc();
    let b = inc();
    io::println(read().to_str());
}
`
	expectOut(t, src, "2\n")
}

func TestCompoundAssignmentTraps(t *testing.T) {
	expectTrap(t, wrap("    let mut x = 9223372036854775807;\n    x += 1;"), "arithmetic overflow")
}

func TestCompoundAssignment(t *testing.T) {
	expectOut(t, wrap("    let mut x = 10;\n    x += 5;\n    x -= 3;\n    x *= 2;\n    x /= 4;\n    x %= 4;\n    io::println(x.to_str());"), "2\n")
}

func TestLoopBreakCarriesAValue(t *testing.T) {
	src := wrap(`    let mut i = 0;
    let found = loop {
        i = i + 1;
        if i == 5 { break i * 10; }
    };
    io::println(found.to_str());`)
	expectOut(t, src, "50\n")
}

func TestReturnFromInsideALoop(t *testing.T) {
	src := `use std::io;

fn first_over(limit: i64) -> i64 {
    let mut i = 0;
    while true {
        if i > limit { return i; }
        i = i + 1;
    }
    0
}

fn main() {
    io::println(first_over(3).to_str());
}
`
	expectOut(t, src, "4\n")
}
