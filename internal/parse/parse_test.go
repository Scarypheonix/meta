package parse

import (
	"strings"
	"testing"

	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/source"
)

func parseSrc(t *testing.T, src string) (*ast.File, *diag.Bag) {
	t.Helper()
	bag := diag.New()
	return File(source.NewFile("t.origin", src), bag), bag
}

// parseExpr wraps an expression in a function so the item parser has something to chew,
// then returns the dump of the function body's tail expression.
func dumpExpr(t *testing.T, expr string) string {
	t.Helper()
	f, bag := parseSrc(t, "fn f() { "+expr+" }")
	if bag.HasErrors() {
		t.Fatalf("parsing %q produced errors:\n%s", expr, bag)
	}
	fn := f.Items[0].(*ast.FnDecl)
	if fn.Body.Tail == nil {
		t.Fatalf("expression %q did not become the block's tail", expr)
	}
	return ast.Dump(fn.Body.Tail)
}

func TestPrecedence(t *testing.T) {
	tests := []struct{ expr, want string }{
		// `a + b * c` groups as a + (b * c)
		{"a + b * c", "binary +\n  path a\n  binary *\n    path b\n    path c\n"},
		// `a * b as i64` — a cast binds tighter than `*`
		{"a * b as i64", "binary *\n  path a\n  cast\n    path b\n    type i64\n"},
		// `-x as i64` — unary binds tighter than `as`
		{"-x as i64", "cast\n  unary -\n    path x\n  type i64\n"},
		// assignment is right-associative
		{"x = y = 1", "assign =\n  path x\n  assign =\n    path y\n    int 1\n"},
		// `&&` binds tighter than `||`
		{"a || b && c", "binary ||\n  path a\n  binary &&\n    path b\n    path c\n"},
		// shifts bind tighter than `&`
		{"a & b << c", "binary &\n  path a\n  binary <<\n    path b\n    path c\n"},
	}
	for _, tt := range tests {
		if got := dumpExpr(t, tt.expr); got != tt.want {
			t.Errorf("%q parsed as:\n%s\nwant:\n%s", tt.expr, got, tt.want)
		}
	}
}

func TestComparisonIsNonAssociative(t *testing.T) {
	_, bag := parseSrc(t, "fn f() { a < b < c }")
	if !bag.HasErrors() {
		t.Fatal("`a < b < c` must be rejected")
	}
	out := bag.String()
	if !strings.Contains(out, "non-associative") {
		t.Errorf("diagnostic should say non-associative:\n%s", out)
	}
	if !strings.Contains(out, "parenthesize") {
		t.Errorf("diagnostic should suggest parenthesizing:\n%s", out)
	}
	if n := bag.ErrorCount(); n != 1 {
		t.Errorf("one mistake produced %d diagnostics, want 1 (spec/09 rule 4)\n%s", n, out)
	}
}

// Parser restriction 1: a struct literal may not appear at the top level of a condition.
func TestNoStructLiteralInConditionPosition(t *testing.T) {
	f, bag := parseSrc(t, "fn f() { if x { 1 } else { 2 } }")
	if bag.HasErrors() {
		t.Fatalf("`if x { .. }` must parse as an if, not a struct literal:\n%s", bag)
	}
	fn := f.Items[0].(*ast.FnDecl)
	if _, ok := fn.Body.Tail.(*ast.If); !ok {
		t.Fatalf("expected an If, got %T", fn.Body.Tail)
	}

	// Parenthesized, a struct literal is fine there.
	_, bag2 := parseSrc(t, "fn f() { if (P { x: 1 }).ok { 1 } else { 2 } }")
	if bag2.HasErrors() {
		t.Errorf("a parenthesized struct literal in a condition must parse:\n%s", bag2)
	}

	// Nested inside a call argument, it is also fine.
	_, bag3 := parseSrc(t, "fn f() { if g(P { x: 1 }) { 1 } else { 2 } }")
	if bag3.HasErrors() {
		t.Errorf("a struct literal inside call arguments must parse:\n%s", bag3)
	}
}

// Parser restriction 2: `[` after an expression is always type application, so an
// explicit instantiation needs no turbofish.
func TestBracketsAreTypeApplication(t *testing.T) {
	got := dumpExpr(t, "f[i64](3)")
	want := "call\n  path f[..]\n    type i64\n  int 3\n"
	if got != want {
		t.Errorf("f[i64](3) parsed as:\n%s\nwant:\n%s", got, want)
	}
}

func TestOneTupleNeedsATrailingComma(t *testing.T) {
	if got := dumpExpr(t, "(1)"); got != "int 1\n" {
		t.Errorf("`(1)` should be a parenthesized int, got:\n%s", got)
	}
	if got := dumpExpr(t, "(1,)"); got != "tuple\n  int 1\n" {
		t.Errorf("`(1,)` should be a one-tuple, got:\n%s", got)
	}
	if got := dumpExpr(t, "()"); got != "unit\n" {
		t.Errorf("`()` should be unit, got:\n%s", got)
	}
}

func TestMethodCallsAndFieldAccess(t *testing.T) {
	got := dumpExpr(t, "v.get(0).x")
	want := "field x\n  method get\n    path v\n    int 0\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestTryOperator(t *testing.T) {
	got := dumpExpr(t, "g()?")
	want := "try\n  call\n    path g\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestLambdas(t *testing.T) {
	if got := dumpExpr(t, "|x| x + 1"); !strings.HasPrefix(got, "lambda\n  param\n    pat bind x\n") {
		t.Errorf("lambda with one parameter parsed as:\n%s", got)
	}
	// `||` in prefix position is an empty parameter list, not the or-operator.
	if got := dumpExpr(t, "|| 1"); got != "lambda\n  int 1\n" {
		t.Errorf("empty lambda parsed as:\n%s", got)
	}
	if got := dumpExpr(t, "|x: i64| -> i64 { x }"); !strings.Contains(got, "type i64") {
		t.Errorf("annotated lambda parsed as:\n%s", got)
	}
}

func TestMatchArms(t *testing.T) {
	src := `fn f() {
    match v {
        Option::Some(n) if n > 0 => n,
        Option::Some(n) => 0,
        Option::None => -1,
    }
}`
	f, bag := parseSrc(t, src)
	if bag.HasErrors() {
		t.Fatalf("unexpected errors:\n%s", bag)
	}
	m := f.Items[0].(*ast.FnDecl).Body.Tail.(*ast.Match)
	if len(m.Arms) != 3 {
		t.Fatalf("parsed %d arms, want 3", len(m.Arms))
	}
	if m.Arms[0].Guard == nil {
		t.Error("first arm should have a guard")
	}
	if m.Arms[1].Guard != nil {
		t.Error("second arm should not have a guard")
	}
}

func TestBraceBodiedArmMayOmitItsComma(t *testing.T) {
	src := `fn f() {
    match v {
        A => { 1 }
        B => 2,
    }
}`
	_, bag := parseSrc(t, src)
	if bag.HasErrors() {
		t.Errorf("a brace-bodied arm may omit its comma:\n%s", bag)
	}
}

func TestPatterns(t *testing.T) {
	tests := []struct{ pat, want string }{
		{"_", "pat _"},
		{"x", "pat bind x"},
		{"mut x", "pat bind mut x"},
		{"1", "pat lit"},
		{"-1", "pat lit -"},
		{"(a, b)", "pat tuple"},
		{"A | B", "pat or"},
		{"Some(x)", "pat path Some(..)"},
		{"P { x, .. }", "pat path P{..} .."},
		{"x @ Some(y)", "pat bind x"},
	}
	for _, tt := range tests {
		f, bag := parseSrc(t, "fn f() { match v { "+tt.pat+" => 1, _ => 2, } }")
		if bag.HasErrors() {
			t.Errorf("pattern %q produced errors:\n%s", tt.pat, bag)
			continue
		}
		m := f.Items[0].(*ast.FnDecl).Body.Tail.(*ast.Match)
		got := ast.Dump(m.Arms[0].Pat)
		if !strings.HasPrefix(got, tt.want) {
			t.Errorf("pattern %q dumped as:\n%s\nwant prefix %q", tt.pat, got, tt.want)
		}
	}
}

func TestItems(t *testing.T) {
	src := `use std::io;
use std::collections::{Vec, Map};

pub struct Point { pub x: f64, mut y: f64 }

enum Shape { Circle(f64), Rect { w: f64, h: f64 }, Empty }

pub trait Iterator {
    type Item;
    fn next(mut self) -> Option[Self::Item];
    fn count(mut self) -> u64 { 0 }
}

impl Iterator for Counter {
    type Item = u64;
    fn next(mut self) -> Option[u64] { Option::None }
}

impl[T] Stack[T] {
    pub fn push(mut self, v: T) { }
}

type Bytes = Vec[u8];

const LIMIT: i64 = 100;

fn largest[T: Ord + Show](items: Vec[T]) -> Option[T] where T: Clone { Option::None }
`
	f, bag := parseSrc(t, src)
	if bag.HasErrors() {
		t.Fatalf("unexpected errors:\n%s", bag)
	}
	if len(f.Uses) != 2 {
		t.Errorf("parsed %d use declarations, want 2", len(f.Uses))
	}
	if len(f.Uses[1].Names) != 2 {
		t.Errorf("braced use imported %d names, want 2", len(f.Uses[1].Names))
	}
	if len(f.Items) != 8 {
		t.Fatalf("parsed %d items, want 8", len(f.Items))
	}
	if s, ok := f.Items[0].(*ast.StructDecl); !ok || !s.Pub || len(s.Fields) != 2 {
		t.Errorf("first item is not the pub struct with 2 fields: %#v", f.Items[0])
	} else if !s.Fields[0].Pub || s.Fields[0].Mut {
		t.Error("field `x` should be pub and not mut")
	} else if s.Fields[1].Pub || !s.Fields[1].Mut {
		t.Error("field `y` should be mut and not pub")
	}
	if e, ok := f.Items[1].(*ast.EnumDecl); !ok || len(e.Variants) != 3 {
		t.Errorf("second item is not the 3-variant enum")
	} else {
		kinds := []ast.VariantKind{ast.TupleVariant, ast.StructVariant, ast.UnitVariant}
		for i, want := range kinds {
			if e.Variants[i].Kind != want {
				t.Errorf("variant %d has kind %v, want %v", i, e.Variants[i].Kind, want)
			}
		}
	}
	if tr, ok := f.Items[2].(*ast.TraitDecl); !ok || len(tr.AssocTypes) != 1 || len(tr.Methods) != 2 {
		t.Errorf("third item is not the trait with 1 assoc type and 2 methods")
	} else if tr.Methods[0].Body != nil {
		t.Error("a required trait method should have no body")
	} else if tr.Methods[1].Body == nil {
		t.Error("a default trait method should have a body")
	}
	if im, ok := f.Items[3].(*ast.ImplDecl); !ok || im.Trait == nil {
		t.Error("fourth item is not a trait impl")
	}
	if im, ok := f.Items[4].(*ast.ImplDecl); !ok || im.Trait != nil {
		t.Error("fifth item is not an inherent impl")
	}
	if fn, ok := f.Items[7].(*ast.FnDecl); !ok || len(fn.Generics) != 1 || len(fn.Where) != 1 {
		t.Error("last item is not the generic fn with a where clause")
	} else if len(fn.Generics[0].Bounds) != 2 {
		t.Errorf("`T: Ord + Show` parsed %d bounds, want 2", len(fn.Generics[0].Bounds))
	}
}

func TestUseMustPrecedeItems(t *testing.T) {
	_, bag := parseSrc(t, "fn f() {}\nuse std::io;\n")
	if !bag.HasErrors() {
		t.Fatal("a `use` after an item must be rejected")
	}
	if !strings.Contains(bag.String(), "must come before all items") {
		t.Errorf("diagnostic should explain the ordering rule:\n%s", bag)
	}
}

// Phase 1 exit criterion: the parser reports multiple syntax errors in one pass.
func TestMultipleSyntaxErrorsInOnePass(t *testing.T) {
	src := `fn a() { let x = ; }
fn b( { }
fn c() -> { }
fn d() { 1 + }
`
	_, bag := parseSrc(t, src)
	if n := bag.ErrorCount(); n < 4 {
		t.Errorf("reported %d errors, want at least 4 (one per broken function):\n%s", n, bag)
	}
}

// An error in one item must not prevent the next item from parsing.
func TestRecoveryContinuesToTheNextItem(t *testing.T) {
	src := `fn broken( { }
fn good() { 42 }
`
	f, bag := parseSrc(t, src)
	if !bag.HasErrors() {
		t.Fatal("expected an error in the first function")
	}
	var found bool
	for _, it := range f.Items {
		if fn, ok := it.(*ast.FnDecl); ok && fn.Name.Name == "good" && fn.Body != nil && fn.Body.Tail != nil {
			found = true
		}
	}
	if !found {
		t.Errorf("the parser did not recover to parse `good`:\n%s", ast.Dump(f))
	}
}

func TestErrorSpansPointAtTheRightLineAndColumn(t *testing.T) {
	src := "fn f() {\n    let x = ;\n}\n"
	_, bag := parseSrc(t, src)
	if !bag.HasErrors() {
		t.Fatal("expected an error")
	}
	d := bag.All()[0]
	line, col := d.Primary.Span.Position()
	if line != 2 || col != 13 {
		t.Errorf("error reported at %d:%d, want 2:13 (the `;`)\n%s", line, col, bag)
	}
}

func TestBlockTailVersusStatement(t *testing.T) {
	f, bag := parseSrc(t, "fn f() { let a = 1; a }")
	if bag.HasErrors() {
		t.Fatalf("unexpected errors:\n%s", bag)
	}
	body := f.Items[0].(*ast.FnDecl).Body
	if len(body.Stmts) != 1 || body.Tail == nil {
		t.Errorf("expected one statement and a tail, got %d statements, tail=%v", len(body.Stmts), body.Tail != nil)
	}

	f2, _ := parseSrc(t, "fn f() { let a = 1; a; }")
	body2 := f2.Items[0].(*ast.FnDecl).Body
	if len(body2.Stmts) != 2 || body2.Tail != nil {
		t.Errorf("a trailing `;` means no tail: got %d statements, tail=%v", len(body2.Stmts), body2.Tail != nil)
	}
}

func TestParserTerminatesOnPathologicalInput(t *testing.T) {
	for _, src := range []string{
		"", "fn", "fn f", "fn f(", "fn f()", "fn f() {", "{{{{{{{{", "))))))))",
		"match", "impl for", "let", "use", "trait T { fn", "enum E {",
		"fn f() { if if if if }", "fn f() { |||||| }", "fn f() { ((((((((",
	} {
		done := make(chan struct{})
		go func(s string) {
			defer close(done)
			defer func() { recover() }()
			bag := diag.New()
			File(source.NewFile("t.origin", s), bag)
		}(src)
		<-done // the test binary's own timeout catches a hang
	}
}

func TestNodeIDsAreUniqueAndNonZero(t *testing.T) {
	f, bag := parseSrc(t, "fn f(a: i64) -> i64 { let b = a + 1; if b > 0 { b } else { 0 } }")
	if bag.HasErrors() {
		t.Fatalf("unexpected errors:\n%s", bag)
	}
	seen := map[ast.NodeID]bool{}
	var walk func(ast.Node)
	walk = func(n ast.Node) {
		if n == nil {
			return
		}
		id := n.NodeID()
		if id == ast.NoID {
			t.Errorf("node %T has the zero NodeID", n)
		}
		if seen[id] {
			t.Errorf("NodeID %d is used twice; side tables would collide", id)
		}
		seen[id] = true
	}
	walk(f)
	for _, it := range f.Items {
		walk(it)
		if fn, ok := it.(*ast.FnDecl); ok {
			walk(fn.Body)
			for _, s := range fn.Body.Stmts {
				walk(s)
			}
			walk(fn.Body.Tail)
		}
	}
	if len(seen) < 4 {
		t.Errorf("walked only %d nodes; the test is not exercising much", len(seen))
	}
}
