package backend

import (
	"testing"

	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/bytecode"
	"github.com/scarypheonix/meta/internal/check"
	"github.com/scarypheonix/meta/internal/compile"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/ir"
	"github.com/scarypheonix/meta/internal/mono"
	"github.com/scarypheonix/meta/internal/parse"
	"github.com/scarypheonix/meta/internal/prelude"
	"github.com/scarypheonix/meta/internal/resolve"
	"github.com/scarypheonix/meta/internal/source"
)

// buildKindTestProgram compiles src to bytecode. It is a test helper only: production
// code reaches this pipeline through internal/driver, which gates it on the checker
// reporting no errors first.
func buildKindTestProgram(t *testing.T, src string) *bytecode.Program {
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
	prog, err := compile.Program(res, tys, mo, pre, user)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return prog
}

// funcIR builds and fully prepares (resolveClosureCalls, propagateKinds) the native IR
// for one bytecode function, found by name.
func funcIR(t *testing.T, prog *bytecode.Program, name string) *ir.Func {
	t.Helper()
	for i, fn := range prog.Fns {
		if fn.Name != name {
			continue
		}
		f, err := ir.Build(fn)
		if err != nil {
			t.Fatalf("building %s: %v", name, err)
		}
		resolveClosureCalls(f)
		propagateKinds(f, prog)
		_ = i
		return f
	}
	t.Fatalf("no function named %q", name)
	return nil
}

// kindsOf collects the Kind of every value of op in f, in the order Values visits them.
func kindsOf(f *ir.Func, op ir.Op) []bytecode.Kind {
	var out []bytecode.Kind
	f.Values(func(v *ir.Value) {
		if v.Op == op {
			out = append(out, v.Kind)
		}
	})
	return out
}

// ADR-0021's second half: propagateKinds (kinds.go) completes the seed data
// internal/compile attaches to bytecode into every IR value's actual kind. These tests
// exercise the parts ir.Build cannot seed on its own -- OpConst (needs the constant
// pool), every structurally-fixed op, and OpPhi's fixed point across a branch -- against
// hand-picked programs whose right answer is unambiguous.

func TestConstKindsMatchTheConstantPoolsKind(t *testing.T) {
	prog := buildKindTestProgram(t, `
fn main() {
    let i = 1;
    let f = 1.5;
    let c = 'x';
    let s = "hi";
}
`)
	f := funcIR(t, prog, "main")
	got := kindsOf(f, ir.OpConst)
	want := []bytecode.Kind{bytecode.KindI64, bytecode.KindFloat, bytecode.KindChar, bytecode.KindString}
	if len(got) != len(want) {
		t.Fatalf("const kinds = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("const[%d] = %s, want %s", i, got[i], want[i])
		}
	}
}

func TestArithmeticAndComparisonKindsAreAlwaysRaw(t *testing.T) {
	prog := buildKindTestProgram(t, `
fn main() {
    let a = 1 + 2;
    let b = 1.5 + 2.5;
    let c = 1 < 2;
    let d = !c;
}
`)
	f := funcIR(t, prog, "main")
	for _, v := range []struct {
		op   ir.Op
		want bytecode.Kind
	}{
		{ir.OpAdd, bytecode.KindI64},
		{ir.OpAddF, bytecode.KindFloat},
		{ir.OpLt, bytecode.KindBool},
		{ir.OpNot, bytecode.KindBool},
	} {
		got := kindsOf(f, v.op)
		if len(got) != 1 || got[0] != v.want {
			t.Errorf("%s kinds = %v, want [%s]", v.op, got, v.want)
		}
	}
}

func TestAggregateConstructionKindsAreAlwaysRef(t *testing.T) {
	prog := buildKindTestProgram(t, `
struct Point { x: i64, y: i64 }

fn main() {
    let p = Point { x: 1, y: 2 };
    let t = (1, 2);
}
`)
	f := funcIR(t, prog, "main")
	for _, op := range []ir.Op{ir.OpStruct, ir.OpTuple} {
		got := kindsOf(f, op)
		if len(got) != 1 || got[0] != bytecode.KindRef {
			t.Errorf("%s kinds = %v, want [KindRef]", op, got)
		}
	}
}

func TestPhiKindMatchesItsOperandsAcrossABranch(t *testing.T) {
	prog := buildKindTestProgram(t, `
struct Box { n: i64 }

fn pick(flag: bool) -> Box {
    if flag { Box { n: 1 } } else { Box { n: 2 } }
}

fn main() {
    let b = pick(true);
}
`)
	f := funcIR(t, prog, "pick")
	got := kindsOf(f, ir.OpPhi)
	if len(got) != 1 || got[0] != bytecode.KindRef {
		t.Fatalf("pick's phi kind = %v, want [KindRef]", got)
	}
}

func TestCastKindsAreRawAndMatchTheTargetsSignedness(t *testing.T) {
	prog := buildKindTestProgram(t, `
fn main() {
    let a = 1 as u32;
    let b = 1 as f64;
}
`)
	f := funcIR(t, prog, "main")
	got := kindsOf(f, ir.OpCast)
	want := []bytecode.Kind{bytecode.KindU64, bytecode.KindFloat}
	if len(got) != len(want) {
		t.Fatalf("cast kinds = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("cast[%d] = %s, want %s", i, got[i], want[i])
		}
	}
}

// A bare function value that escapes gets boxed into a closure object by
// closures.go's resolveClosureCalls (ADR-0020), which runs before propagateKinds:
// this checks the two passes agree that the box, and the call through it, are both
// references and a reference respectively -- not left over as OpFunc's own KindInt.
func TestBoxedFunctionValueKindIsRefNotTheUnderlyingFunctions(t *testing.T) {
	prog := buildKindTestProgram(t, `
fn double(x: i64) -> i64 { x * 2 }
fn apply(f: fn(i64) -> i64, x: i64) -> i64 { f(x) }

fn main() {
    let r = apply(double, 5);
}
`)
	f := funcIR(t, prog, "main")
	if got := kindsOf(f, ir.OpBoxFn); len(got) != 1 || got[0] != bytecode.KindRef {
		t.Errorf("box_fn kind = %v, want [KindRef]", got)
	}
	// apply's own call to f(x) is now indirect (its callee is a parameter, not a bare
	// OpFunc), so it is an OpCallClosure whose kind came from the OpCall it used to be.
	applyFn := funcIR(t, prog, "apply")
	if got := kindsOf(applyFn, ir.OpCallClosure); len(got) != 1 || got[0] != bytecode.KindI64 {
		t.Errorf("apply's call_closure kind = %v, want [KindInt]", got)
	}
}

func TestBuiltinResultKindsMatchTheirActualShape(t *testing.T) {
	prog := buildKindTestProgram(t, `
fn main() {
    let o = 3.cmp(5);
    let c = 1i64.checked_add(2);
    let s = 1i64.saturating_add(2);
    let e = ref_eq(1, 1);
}
`)
	f := funcIR(t, prog, "main")
	got := kindsOf(f, ir.OpCallBuiltin)
	// `cmp` builds an `Ordering`, which is a reference. `checked_add` is the *predicate*
	// behind one -- a bool -- because the `Option` it decides is built by internal/compile
	// at the call's own instantiation rather than by a runtime with one recorded variant
	// index to build from. `saturating_add` answers at the receiver's own width, and
	// `ref_eq` is a bool.
	want := []bytecode.Kind{bytecode.KindRef, bytecode.KindBool, bytecode.KindI64, bytecode.KindBool}
	if len(got) != len(want) {
		t.Fatalf("call_builtin kinds = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call_builtin[%d] = %s, want %s", i, got[i], want[i])
		}
	}
}
