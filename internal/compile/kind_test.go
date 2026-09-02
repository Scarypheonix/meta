package compile

import (
	"testing"

	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/bytecode"
	"github.com/scarypheonix/meta/internal/check"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/mono"
	"github.com/scarypheonix/meta/internal/parse"
	"github.com/scarypheonix/meta/internal/prelude"
	"github.com/scarypheonix/meta/internal/resolve"
	"github.com/scarypheonix/meta/internal/source"
)

// buildProgram compiles src to bytecode. It is a test helper only: production code
// reaches this pipeline through internal/driver, which gates it on the checker
// reporting no errors first.
func buildProgram(t *testing.T, src string) *bytecode.Program {
	t.Helper()
	ids := ast.NewIDGen()
	bag := diag.New()
	pre := parse.FileWith(prelude.Source(), diag.New(), ids)
	user := parse.FileWith(source.NewFile("case.origin", src), bag, ids)
	if bag.HasErrors() {
		t.Fatalf("parse:\n%s", bag)
	}
	res := resolve.Program(bag, resolve.Input{File: pre, Prelude: true}, resolve.Input{File: user})
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
	prog, err := Program(res, tys, mo, pre, user)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return prog
}

// fnByName finds a compiled function by name. Panics (via t.Fatalf) rather than
// returning ok=false, since every case here names a function it knows was compiled.
func fnByName(t *testing.T, prog *bytecode.Program, name string) *bytecode.Fn {
	t.Helper()
	for _, fn := range prog.Fns {
		if fn.Name == name {
			return fn
		}
	}
	t.Fatalf("no function named %q; have: %v", name, fnNames(prog))
	return nil
}

func fnNames(prog *bytecode.Program) []string {
	var out []string
	for _, fn := range prog.Fns {
		out = append(out, fn.Name)
	}
	return out
}

// kindsAt collects the Kind of every instruction of op in fn, in code order. It exists
// so a test can assert "the struct field read got KindInt, the String field got
// KindRef" without hardcoding a program counter that would shift if the function's
// shape changed elsewhere.
func kindsAt(fn *bytecode.Fn, op bytecode.Op) []bytecode.Kind {
	var out []bytecode.Kind
	for _, in := range fn.Code {
		if in.Op == op {
			out = append(out, in.Kind)
		}
	}
	return out
}

// ADR-0021: internal/compile attaches a static Kind to the handful of bytecode
// instructions whose result the native backend cannot otherwise tell is a reference —
// a field/payload/tuple-element read and a call — because it is the last point in the
// pipeline that still has the checker's own type for the value. These tests verify that
// attachment directly, against hand-picked programs whose right answer is unambiguous,
// so a future consumer (a stack map) can trust the data without re-deriving it.

func TestFieldReadKindsMatchTheirDeclaredTypes(t *testing.T) {
	prog := buildProgram(t, `
struct Point { x: i64, y: String }

fn readX(p: Point) -> i64 { p.x }
fn readY(p: Point) -> String { p.y }
fn main() {}
`)
	x := fnByName(t, prog, "readX")
	if got := kindsAt(x, bytecode.OpGetField); len(got) != 1 || got[0] != bytecode.KindI64 {
		t.Errorf("readX's get_field kind = %v, want [KindInt]", got)
	}
	y := fnByName(t, prog, "readY")
	if got := kindsAt(y, bytecode.OpGetField); len(got) != 1 || got[0] != bytecode.KindString {
		t.Errorf("readY's get_field kind = %v, want [KindString]", got)
	}
}

func TestVariantPayloadKindsMatchTheirDeclaredTypes(t *testing.T) {
	prog := buildProgram(t, `
enum Shape {
    Circle { radius: f64 },
    Named { label: String, count: i64 },
}

fn describe(s: Shape) -> i64 {
    match s {
        Shape::Circle { radius } => 0,
        Shape::Named { label, count } => count,
    }
}
fn main() {}
`)
	fn := fnByName(t, prog, "describe")
	got := kindsAt(fn, bytecode.OpGetPayload)
	// One payload read per field pattern tested: radius (float), then label (string)
	// and count (int) for the second arm.
	want := []bytecode.Kind{bytecode.KindFloat, bytecode.KindString, bytecode.KindI64}
	if len(got) != len(want) {
		t.Fatalf("get_payload kinds = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("get_payload[%d] = %s, want %s", i, got[i], want[i])
		}
	}
}

func TestTupleElementKindsMatchTheirDeclaredTypes(t *testing.T) {
	prog := buildProgram(t, `
fn firstOfPair(t: (i64, String)) -> i64 {
    let (a, b) = t;
    a
}
fn main() {}
`)
	fn := fnByName(t, prog, "firstOfPair")
	got := kindsAt(fn, bytecode.OpGetTupleElem)
	want := []bytecode.Kind{bytecode.KindI64, bytecode.KindString}
	if len(got) != len(want) {
		t.Fatalf("get_tuple_elem kinds = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("get_tuple_elem[%d] = %s, want %s", i, got[i], want[i])
		}
	}
}

func TestCallResultKindMatchesTheCalleesReturnType(t *testing.T) {
	prog := buildProgram(t, `
struct Box { n: i64 }

fn makeBox() -> Box { Box { n: 1 } }
fn addOne(n: i64) -> i64 { n + 1 }

fn main() {
    let b = makeBox();
    let n = addOne(5);
}
`)
	mainFn := fnByName(t, prog, "main")
	got := kindsAt(mainFn, bytecode.OpCall)
	want := []bytecode.Kind{bytecode.KindRef, bytecode.KindI64}
	if len(got) != len(want) {
		t.Fatalf("call kinds = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call[%d] = %s, want %s", i, got[i], want[i])
		}
	}
}

func TestParamKindsMatchDeclaredParameterTypes(t *testing.T) {
	prog := buildProgram(t, `
struct Box { n: i64 }

fn take(b: Box, n: i64, s: String) -> i64 { n }
fn main() {}
`)
	fn := fnByName(t, prog, "take")
	want := []bytecode.Kind{bytecode.KindRef, bytecode.KindI64, bytecode.KindString}
	if len(fn.ParamKinds) != len(want) {
		t.Fatalf("ParamKinds = %v, want %v", fn.ParamKinds, want)
	}
	for i := range want {
		if fn.ParamKinds[i] != want[i] {
			t.Errorf("ParamKinds[%d] = %s, want %s", i, fn.ParamKinds[i], want[i])
		}
	}
}

func TestSelfKindIsTheFirstParamKind(t *testing.T) {
	prog := buildProgram(t, `
struct Counter { mut n: i64 }

impl Counter {
    fn bump(self, by: i64) -> i64 { self.n + by }
}

fn main() {
    let c = Counter { n: 0 };
    let r = c.bump(1);
}
`)
	fn := fnByName(t, prog, "bump")
	want := []bytecode.Kind{bytecode.KindRef, bytecode.KindI64}
	if len(fn.ParamKinds) != len(want) {
		t.Fatalf("ParamKinds = %v, want %v (self, by)", fn.ParamKinds, want)
	}
	for i := range want {
		if fn.ParamKinds[i] != want[i] {
			t.Errorf("ParamKinds[%d] = %s, want %s", i, fn.ParamKinds[i], want[i])
		}
	}
}

func TestClosureCaptureKindsMatchTheCapturedLocalsTypes(t *testing.T) {
	prog := buildProgram(t, `
struct Box { n: i64 }

fn main() {
    let count = 0;
    let label = "hi";
    let f = || { count };
    let g = || { label };
}
`)
	var fCount, gCount int
	for _, fn := range prog.Fns {
		if fn.Name != "<lambda>" {
			continue
		}
		switch len(fn.CaptureKinds) {
		case 1:
			switch fn.CaptureKinds[0] {
			case bytecode.KindI64:
				fCount++
			case bytecode.KindString:
				gCount++
			default:
				t.Errorf("a lambda's single capture kind = %s, want KindInt or KindString", fn.CaptureKinds[0])
			}
		default:
			t.Errorf("a lambda has %d captures, want 1", len(fn.CaptureKinds))
		}
	}
	if fCount != 1 || gCount != 1 {
		t.Fatalf("found %d int-capturing and %d string-capturing lambdas, want 1 each", fCount, gCount)
	}
}
