package resolve

import (
	"strings"
	"testing"

	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/parse"
	"github.com/scarypheonix/meta/internal/prelude"
	"github.com/scarypheonix/meta/internal/source"
)

// resolveSrc parses and resolves a single file with no prelude.
func resolveSrc(t *testing.T, src string) (*ast.File, *Result, *diag.Bag) {
	t.Helper()
	bag := diag.New()
	f := parse.File(source.NewFile("t.origin", src), bag)
	if bag.HasErrors() {
		t.Fatalf("the test source does not parse:\n%s", bag)
	}
	return f, Files(bag, f), bag
}

// resolveWithPrelude resolves a file together with the prelude, sharing one id
// generator so that node ids stay unique across both.
func resolveWithPrelude(t *testing.T, src string) (*ast.File, *Result, *diag.Bag) {
	t.Helper()
	ids := ast.NewIDGen()
	bag := diag.New()
	pre := parse.FileWith(prelude.Source(), bag, ids)
	f := parse.FileWith(source.NewFile("t.origin", src), bag, ids)
	if bag.HasErrors() {
		t.Fatalf("the test source does not parse:\n%s", bag)
	}
	return f, Files(bag, pre, f), bag
}

func TestItemsAreVisibleBeforeTheyAreDeclared(t *testing.T) {
	// Mutual recursion needs items collected before any body is resolved.
	_, _, bag := resolveSrc(t, `
fn a(n: i64) -> bool { b(n) }
fn b(n: i64) -> bool { a(n) }
`)
	if bag.HasErrors() {
		t.Errorf("mutual recursion should resolve:\n%s", bag)
	}
}

func TestUnresolvedNameIsReported(t *testing.T) {
	_, _, bag := resolveSrc(t, "fn main() { let x = nope; }")
	if !bag.HasErrors() {
		t.Fatal("expected an unresolved-name error")
	}
	if !strings.Contains(bag.String(), "E0433") {
		t.Errorf("expected E0433:\n%s", bag)
	}
}

func TestShadowingCreatesADistinctBinding(t *testing.T) {
	f, res, bag := resolveSrc(t, "fn main() { let x = 1; let x = 2; }")
	if bag.HasErrors() {
		t.Fatalf("shadowing is legal:\n%s", bag)
	}
	body := f.Items[0].(*ast.FnDecl).Body
	first := body.Stmts[0].(*ast.LetStmt).Pat.(*ast.BindPat)
	second := body.Stmts[1].(*ast.LetStmt).Pat.(*ast.BindPat)
	if res.Bindings[first.NodeID()] == res.Bindings[second.NodeID()] {
		t.Error("shadowing must produce two distinct bindings")
	}
}

func TestLetInitializerSeesTheOuterBinding(t *testing.T) {
	f, res, bag := resolveSrc(t, "fn main() { let x = 1; let x = x; }")
	if bag.HasErrors() {
		t.Fatalf("unexpected errors:\n%s", bag)
	}
	body := f.Items[0].(*ast.FnDecl).Body
	outer := res.Bindings[body.Stmts[0].(*ast.LetStmt).Pat.(*ast.BindPat).NodeID()]
	init := body.Stmts[1].(*ast.LetStmt).Value.(*ast.PathExpr)
	ref := res.Refs[init.NodeID()]
	if ref.Local != outer {
		t.Error("`let x = x;` must read the outer binding, not the one being introduced")
	}
}

func TestAssignmentToImmutableBindingIsRejected(t *testing.T) {
	_, _, bag := resolveSrc(t, "fn main() { let x = 1; x = 2; }")
	if !bag.HasErrors() {
		t.Fatal("assigning to a non-mut binding must be rejected")
	}
	out := bag.String()
	if !strings.Contains(out, "E0594") {
		t.Errorf("expected E0594:\n%s", out)
	}
	if !strings.Contains(out, "let mut x") {
		t.Errorf("the diagnostic should suggest `let mut x`:\n%s", out)
	}
	if !strings.Contains(out, "is bound here") {
		t.Errorf("the diagnostic should point at the binding:\n%s", out)
	}
}

func TestAssignmentToMutBindingIsAccepted(t *testing.T) {
	_, _, bag := resolveSrc(t, "fn main() { let mut x = 1; x = 2; }")
	if bag.HasErrors() {
		t.Errorf("assigning to a `mut` binding is legal:\n%s", bag)
	}
}

func TestClosureCapturesAreRecorded(t *testing.T) {
	f, res, bag := resolveSrc(t, `
fn main() {
    let a = 1;
    let b = 2;
    let f = |x| x + a;
}
`)
	if bag.HasErrors() {
		t.Fatalf("unexpected errors:\n%s", bag)
	}
	body := f.Items[0].(*ast.FnDecl).Body
	lam := body.Stmts[2].(*ast.LetStmt).Value.(*ast.Lambda)
	caps := res.Captures[lam.NodeID()]
	if len(caps) != 1 || caps[0].Name != "a" {
		var names []string
		for _, c := range caps {
			names = append(names, c.Name)
		}
		t.Errorf("captured %v, want exactly [a]: a lambda captures what it uses, not what is in scope", names)
	}
}

func TestLambdaParametersAreNotCaptures(t *testing.T) {
	f, res, bag := resolveSrc(t, "fn main() { let f = |x| x + 1; }")
	if bag.HasErrors() {
		t.Fatalf("unexpected errors:\n%s", bag)
	}
	lam := f.Items[0].(*ast.FnDecl).Body.Stmts[0].(*ast.LetStmt).Value.(*ast.Lambda)
	if n := len(res.Captures[lam.NodeID()]); n != 0 {
		t.Errorf("captured %d bindings, want 0: a parameter belongs to the lambda's own frame", n)
	}
}

func TestNestedLambdasBothCaptureAnOuterBinding(t *testing.T) {
	f, res, bag := resolveSrc(t, `
fn main() {
    let a = 1;
    let outer = || { let inner = || a; inner };
}
`)
	if bag.HasErrors() {
		t.Fatalf("unexpected errors:\n%s", bag)
	}
	outerLam := f.Items[0].(*ast.FnDecl).Body.Stmts[1].(*ast.LetStmt).Value.(*ast.Lambda)
	if len(res.Captures[outerLam.NodeID()]) != 1 {
		t.Error("the outer lambda must capture `a` so it can hand it to the inner one")
	}
}

func TestBareNameInPatternBindsUnlessItNamesAConstant(t *testing.T) {
	// spec/02-grammar.md: a bare name in a pattern binds, unless it resolves to a unit
	// variant or a constant. In 0.1 only the constant half is reachable, because a
	// variant always needs a path.
	src := `
const LIMIT: i64 = 10;
fn main() {
    let v = 1;
    match v {
        LIMIT => { }
        other => { }
    }
}
`
	f, res, bag := resolveSrc(t, src)
	if bag.HasErrors() {
		t.Fatalf("unexpected errors:\n%s", bag)
	}
	m := f.Items[1].(*ast.FnDecl).Body.Tail.(*ast.Match)
	first := m.Arms[0].Pat.(*ast.BindPat)
	if ref := res.Refs[first.NodeID()]; ref.Kind != Const {
		t.Errorf("`LIMIT` names a constant, so the pattern must match it, not bind (got %v)", ref.Kind)
	}
	second := m.Arms[1].Pat.(*ast.BindPat)
	if ref := res.Refs[second.NodeID()]; ref.Kind != LocalVar {
		t.Errorf("`other` names nothing, so the pattern must bind (got %v)", ref.Kind)
	}
}

func TestUnknownVariantIsReportedWithTheAvailableOnes(t *testing.T) {
	_, _, bag := resolveSrc(t, "enum E { A, B }\nfn main() { let v = E::C; }")
	if !bag.HasErrors() {
		t.Fatal("expected an error for an unknown variant")
	}
	out := bag.String()
	if !strings.Contains(out, "`A`") || !strings.Contains(out, "`B`") {
		t.Errorf("the diagnostic should list the variants that do exist:\n%s", out)
	}
}

func TestDuplicateItemNameIsReported(t *testing.T) {
	_, _, bag := resolveSrc(t, "fn f() {}\nfn f() {}")
	if !bag.HasErrors() {
		t.Fatal("a duplicate item name must be rejected")
	}
}

func TestBreakOutsideALoopIsReported(t *testing.T) {
	_, _, bag := resolveSrc(t, "fn main() { break; }")
	if !bag.HasErrors() {
		t.Fatal("`break` outside a loop must be rejected")
	}
	if !strings.Contains(bag.String(), "outside of a loop") {
		t.Errorf("unexpected diagnostic:\n%s", bag)
	}
}

func TestImplMethodsAreCollected(t *testing.T) {
	_, res, bag := resolveSrc(t, `
struct S { x: i64 }
impl S {
    fn get(self) -> i64 { 0 }
    fn set(mut self, v: i64) { }
}
`)
	if bag.HasErrors() {
		t.Fatalf("unexpected errors:\n%s", bag)
	}
	methods := res.Methods["S"]
	if len(methods) != 2 || methods["get"] == nil || methods["set"] == nil {
		t.Errorf("collected %d methods on `S`, want get and set", len(methods))
	}
}

// Whether two methods with one name clash depends on which traits their impls are for,
// which only the checker knows. The resolver just collects them for the interpreter's
// runtime dispatch; the rejection is a conformance case (trait_duplicate_inherent_method).
func TestMethodTableCollectsEveryImplMethod(t *testing.T) {
	_, res, bag := resolveSrc(t, `
struct S { x: i64 }
impl S {
    fn get(self) -> i64 { 0 }
}
impl S {
    fn other(self) -> i64 { 1 }
}
`)
	if bag.HasErrors() {
		t.Fatalf("unexpected errors:\n%s", bag)
	}
	if m := res.Methods["S"]; m["get"] == nil || m["other"] == nil {
		t.Errorf("methods from separate impls should all be collected, got %d", len(m))
	}
}

func TestMatchArmBindingsDoNotLeak(t *testing.T) {
	_, _, bag := resolveSrc(t, `
enum E { A(i64), B }
fn main() {
    let v = E::B;
    match v {
        E::A(n) => { }
        E::B => { }
    }
    let x = n;
}
`)
	if !bag.HasErrors() {
		t.Fatal("a binding from a match arm must not be visible after the match")
	}
}

// TestEveryTypePathResolves is a structural invariant over the whole tree, not a check
// of one construct.
//
// It exists because of a real bug: the resolver walked types in declarations but not in
// bodies, so `let x: i64 = ...` recorded no resolution, the checker read the missing
// entry as the error type, and every annotated binding silently type-checked against
// anything. One forgotten call site was enough. This test fails for any type position
// that is ever added and not walked.
func TestEveryTypePathResolves(t *testing.T) {
	src := `
struct Wrapper[T] { inner: T, count: i64 }

enum Shape[T] { Empty, One(T), Two { a: T, b: i64 } }

type Alias = Wrapper[i64];

const LIMIT: i64 = 10;

trait Container {
    type Item;
    fn get(self) -> Option[Self::Item];
    fn size(self) -> u64 { 0 }
}

impl[T] Container for Wrapper[T] {
    type Item = T;
    fn get(self) -> Option[T] { Option::None }
}

impl[T] Wrapper[T] {
    fn count_of(self) -> i64 { self.count }
}

fn generic[T: Container](x: T, y: i64) -> Option[T] where T: Container {
    let annotated: i64 = y;
    let cast = annotated as u8;
    let lambda = |z: i64| -> i64 { z };
    let inferred = lambda(annotated);
    let tuple: (i64, bool) = (1, true);
    let nested: Wrapper[Wrapper[i64]] = Wrapper { inner: Wrapper { inner: 1, count: 2 }, count: 3 };
    let func: fn(i64) -> i64 = lambda;
    let shape: Shape[i64] = Shape::One(1);
    match shape {
        Shape::Empty => { }
        Shape::One(n) => { }
        Shape::Two { a, b } => { }
    }
    Option::None
}

fn main() {
    let w: Wrapper[i64] = Wrapper { inner: 1, count: 0 };
    let r = generic(w, LIMIT);
}
`
	f, res, bag := resolveWithPrelude(t, src)
	if bag.HasErrors() {
		t.Fatalf("this program should resolve cleanly:\n%s", bag)
	}

	unresolved := 0
	ast.Inspect(f, func(n ast.Node) bool {
		switch n.(type) {
		case *ast.PathType, *ast.TraitRef:
			ref, ok := res.Ref(n.NodeID())
			if !ok {
				t.Errorf("%s: no resolution recorded for a %T; the resolver does not walk this position",
					n.Span(), n)
				unresolved++
			} else if ref.Kind == Unresolved {
				t.Errorf("%s: %T resolved to nothing", n.Span(), n)
				unresolved++
			}
		}
		return true
	})

	// A guard against the test silently walking nothing.
	seen := 0
	ast.Inspect(f, func(n ast.Node) bool {
		if _, ok := n.(*ast.PathType); ok {
			seen++
		}
		return true
	})
	if seen < 25 {
		t.Errorf("the walk found only %d type paths; the test is not exercising much", seen)
	}
}
