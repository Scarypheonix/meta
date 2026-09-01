package mono

import (
	"strings"
	"testing"

	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/check"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/parse"
	"github.com/scarypheonix/meta/internal/prelude"
	"github.com/scarypheonix/meta/internal/resolve"
	"github.com/scarypheonix/meta/internal/source"
)

// build checks a program and returns its instantiation set. It is a test helper only:
// production code reaches this through internal/driver, which gates it on the checker
// reporting no errors first.
func build(t *testing.T, src string) (*ast.File, *check.Result, *Result, *diag.Bag) {
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
	mo := Program(bag, tys, pre, user)
	return user, tys, mo, bag
}

func namesOf(r *Result) []string {
	var out []string
	for _, inst := range r.Instances {
		out = append(out, inst.Name)
	}
	return out
}

func TestNonGenericFunctionGetsOneInstance(t *testing.T) {
	_, _, mo, _ := build(t, `
fn add(a: i64, b: i64) -> i64 { a + b }
fn main() { let x = add(1, 2); }
`)
	if mo.Entry == nil {
		t.Fatal("no instance was recorded for main")
	}
	got := namesOf(mo)
	if len(got) != 2 {
		t.Fatalf("got %d instances (%v), want 2 (add, main)", len(got), got)
	}
}

func TestEachCallSiteGetsItsOwnInstance(t *testing.T) {
	_, _, mo, _ := build(t, `
fn identity[T](x: T) -> T { x }
fn main() {
    let a = identity(1);
    let b = identity("s");
    let c = identity(true);
}
`)
	got := namesOf(mo)
	want := map[string]bool{"identity[i64]": true, "identity[String]": true, "identity[bool]": true}
	found := map[string]bool{}
	for _, n := range got {
		if want[n] {
			found[n] = true
		}
	}
	if len(found) != len(want) {
		t.Errorf("instances are %v, want one each for %v", got, want)
	}
}

func TestRepeatedCallAtTheSameTypeSharesOneInstance(t *testing.T) {
	_, _, mo, _ := build(t, `
fn identity[T](x: T) -> T { x }
fn main() {
    let a = identity(1);
    let b = identity(2);
    let c = identity(3);
}
`)
	count := 0
	for _, inst := range mo.Instances {
		if inst.Name == "identity[i64]" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("three calls to identity(i64) produced %d instances, want 1", count)
	}
}

// TestGenericMethodOnATypeParameterResolvesPerInstantiation is the regression this
// package exists to fix: a call inside a generic body to a trait method on its own type
// parameter cannot be resolved until the parameter is a concrete type, and each
// instantiation may resolve it to a *different* function.
func TestGenericMethodOnATypeParameterResolvesPerInstantiation(t *testing.T) {
	user, _, mo, bag := build(t, `
struct Money { cents: i64 }

impl Ord for Money {
    fn cmp(self, other: Money) -> Ordering {
        self.cents.cmp(other.cents)
    }
}

fn max2[T: Ord](a: T, b: T) -> T {
    match a.cmp(b) {
        Ordering::Less => b,
        _ => a,
    }
}

fn main() {
    let a = max2(3, 7);
    let b = max2(Money { cents: 1 }, Money { cents: 2 });
}
`)
	if bag.HasErrors() {
		t.Fatalf("unexpected errors:\n%s", bag)
	}

	var intInst, moneyInst *Instance
	for _, inst := range mo.Instances {
		switch inst.Name {
		case "max2[i64]":
			intInst = inst
		case "max2[Money]":
			moneyInst = inst
		}
	}
	if intInst == nil || moneyInst == nil {
		t.Fatalf("got instances %v, want max2[i64] and max2[Money]", namesOf(mo))
	}

	// max2[i64]'s `a.cmp(b)` has no user body to call: i64's Ord impl is compiler
	// provided, so the call has no target and the code generator lowers it to a
	// builtin instead.
	if len(intInst.Calls) != 0 {
		t.Errorf("max2[i64] resolved %d calls, want 0 (i64's Ord is builtin)", len(intInst.Calls))
	}

	// max2[Money]'s `a.cmp(b)` must resolve to Money's own cmp, found by walking the
	// method call inside the body.
	var cmpNode ast.NodeID
	ast.Inspect(user, func(n ast.Node) bool {
		if mc, ok := n.(*ast.MethodCall); ok && mc.Name.Name == "cmp" {
			cmpNode = mc.NodeID()
		}
		return true
	})
	if cmpNode == 0 {
		t.Fatal("did not find the `.cmp` call in the source")
	}
	target, ok := mo.Lookup(moneyInst, cmpNode)
	if !ok {
		t.Fatal("max2[Money] has no resolved target for `a.cmp(b)`")
	}
	if target.Decl.Name.Name != "cmp" || target.Decl.Self == nil {
		t.Errorf("the resolved target is %q, want Money's `cmp` method", target.Decl.Name.Name)
	}
}

// TestPolymorphicRecursionIsRejected is spec/06-traits-generics.md's termination rule:
// a generic function that calls itself at a strictly larger type has an infinite
// instantiation set, and must be rejected once the chain exceeds the depth limit rather
// than hang or run out of memory.
func TestPolymorphicRecursionIsRejected(t *testing.T) {
	_, _, _, bag := build(t, `
struct Wrap[T] { inner: T }

fn grow[T](x: T) -> i64 {
    grow(Wrap { inner: x })
}

fn main() {
    let a = grow(1);
}
`)
	if !bag.HasErrors() {
		t.Fatal("polymorphic recursion was accepted")
	}
	found := false
	for _, d := range bag.All() {
		if d.Code == "E0055" {
			found = true
		}
	}
	if !found {
		t.Errorf("no E0055 diagnostic was reported; got:\n%s", bag)
	}
}

func TestBuiltinImplLeavesTheCallUnresolved(t *testing.T) {
	_, _, mo, bag := build(t, `
fn show[T: Show](x: T) -> String { x.to_str() }
fn main() {
    let s = show(42);
}
`)
	if bag.HasErrors() {
		t.Fatalf("unexpected errors:\n%s", bag)
	}
	var inst *Instance
	for _, i := range mo.Instances {
		if strings.HasPrefix(i.Name, "show[") {
			inst = i
		}
	}
	if inst == nil {
		t.Fatal("no instance of `show` was created")
	}
	if len(inst.Calls) != 0 {
		t.Errorf("`to_str` on i64 resolved to %d calls, want 0: i64's Show is compiler-provided",
			len(inst.Calls))
	}
}

func TestInstantiationIsDeterministic(t *testing.T) {
	const src = `
fn identity[T](x: T) -> T { x }
fn pair[A, B](a: A, b: B) -> A { a }
fn main() {
    let a = identity(1);
    let b = identity("s");
    let c = pair(1, "s");
    let d = pair(true, 2.0);
}
`
	var first []string
	for i := 0; i < 50; i++ {
		_, _, mo, bag := build(t, src)
		if bag.HasErrors() {
			t.Fatalf("unexpected errors:\n%s", bag)
		}
		got := namesOf(mo)
		if i == 0 {
			first = got
			continue
		}
		if len(got) != len(first) {
			t.Fatalf("run %d produced %d instances, run 0 produced %d", i, len(got), len(first))
		}
		for j := range got {
			if got[j] != first[j] {
				t.Fatalf("run %d differs at position %d: %q vs %q", i, j, got[j], first[j])
			}
		}
	}
}

// A trait's default method body is generic in `Self` (internal/check's `signature` puts
// `Self` in the method's generic parameters), so it monomorphizes like any other generic
// function: one instance per implementing type, each resolving the required methods it
// calls against that type's own impl.
func TestDefaultMethodGetsOneInstancePerImplementor(t *testing.T) {
	_, _, mo, bag := build(t, `
trait Named {
    fn name(self) -> String;
    fn describe(self) -> String { self.name() }
}
struct Cat {}
struct Dog {}
impl Named for Cat { fn name(self) -> String { "cat" } }
impl Named for Dog { fn name(self) -> String { "dog" } }
fn main() {
    let a = Cat {}.describe();
    let b = Dog {}.describe();
}
`)
	if bag.HasErrors() {
		t.Fatalf("unexpected errors:\n%s", bag)
	}
	var describes []*Instance
	for _, i := range mo.Instances {
		if strings.HasPrefix(i.Name, "describe[") {
			describes = append(describes, i)
		}
	}
	if len(describes) != 2 {
		t.Fatalf("`describe` produced %d instances, want 2 (one per implementor): %v",
			len(describes), namesOf(mo))
	}
	// Each one must reach its own implementor's `name`, not the trait's declaration.
	for _, inst := range describes {
		if len(inst.Calls) != 1 {
			t.Fatalf("%s resolved %d calls, want 1 (`self.name()`)", inst.Name, len(inst.Calls))
		}
		for _, target := range inst.Calls {
			if target.Decl.Body == nil {
				t.Errorf("%s reached a bodyless declaration", inst.Name)
			}
		}
	}
	// Both impls call their method `name`, so the instance names match; what must
	// differ is the declaration each one reached.
	var reached []*ast.FnDecl
	for _, inst := range describes {
		for _, target := range inst.Calls {
			reached = append(reached, target.Decl)
		}
	}
	if reached[0] == reached[1] {
		t.Error("both instances of `describe` reached the same `name`; each should reach its own impl")
	}
}
