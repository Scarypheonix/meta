package driver_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scarypheonix/meta/internal/driver"
)

// writePackage lays out a package on disk and returns its root directory.
func writePackage(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, src := range files {
		path := filepath.Join(dir, "src", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	return dir
}

func runPackage(t *testing.T, files map[string]string) (string, string, int) {
	t.Helper()
	dir := writePackage(t, files)
	var stdout, stderr bytes.Buffer
	code := driver.Run(dir, &stdout, &stderr)
	return stdout.String(), stderr.String(), code
}

func TestMultiFilePackage(t *testing.T) {
	stdout, stderr, code := runPackage(t, map[string]string{
		"main.origin": `use std::io;
use geometry::shape::{Shape, area};

fn main() {
    io::println(area(Shape::Circle(2.0)).to_str());
    io::println(area(Shape::Rect { w: 3.0, h: 4.0 }).to_str());
}
`,
		"geometry/shape.origin": `pub enum Shape {
    Circle(f64),
    Rect { w: f64, h: f64 },
}

const PI: f64 = 3.14159;

pub fn area(s: Shape) -> f64 {
    match s {
        Shape::Circle(r) => PI * r * r,
        Shape::Rect { w, h } => w * h,
    }
}
`,
	})
	if code != 0 {
		t.Fatalf("exit %d\nstderr:\n%s", code, stderr)
	}
	if stdout != "12.56636\n12.0\n" {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestPrivateItemIsNotImportable(t *testing.T) {
	_, stderr, code := runPackage(t, map[string]string{
		"main.origin": `use helper::secret;

fn main() {
    let x = secret();
}
`,
		"helper.origin": `fn secret() -> i64 { 42 }
`,
	})
	if code != driver.ExitDiagnostics {
		t.Fatalf("expected the program to be rejected, got exit %d", code)
	}
	if !strings.Contains(stderr, "[E0603]") {
		t.Errorf("expected E0603, got:\n%s", stderr)
	}
	// spec/09-errors.md rule 4: one mistake, one diagnostic.
	if n := strings.Count(stderr, "error["); n != 1 {
		t.Errorf("a failed import produced %d diagnostics, want 1:\n%s", n, stderr)
	}
	// The other file's declaration must be shown, or the reader cannot act on it.
	if !strings.Contains(stderr, "helper.origin") {
		t.Errorf("the diagnostic should point at the private declaration:\n%s", stderr)
	}
}

func TestModuleCyclesArePermitted(t *testing.T) {
	// spec/07-modules.md: module-level cycles are legal, because names are resolved
	// across the whole package before any body is checked.
	_, stderr, code := runPackage(t, map[string]string{
		"main.origin": `use std::io;
use a::ping;

fn main() {
    io::println(ping(3).to_str());
}
`,
		"a.origin": `use b::pong;

pub fn ping(n: i64) -> i64 {
    if n == 0 { 0 } else { pong(n - 1) }
}
`,
		"b.origin": `use a::ping;

pub fn pong(n: i64) -> i64 {
    if n == 0 { 1 } else { ping(n - 1) }
}
`,
	})
	if code != 0 {
		t.Fatalf("module cycles must be permitted; got exit %d\nstderr:\n%s", code, stderr)
	}
}

func TestQualifiedPathWithoutImport(t *testing.T) {
	stdout, stderr, code := runPackage(t, map[string]string{
		"main.origin": `use std::io;

fn main() {
    io::println(util::math::double(21).to_str());
}
`,
		"util/math.origin": `pub fn double(n: i64) -> i64 { n * 2 }
`,
	})
	if code != 0 {
		t.Fatalf("exit %d\nstderr:\n%s", code, stderr)
	}
	if stdout != "42\n" {
		t.Errorf("stdout = %q, want %q", stdout, "42\n")
	}
}

func TestUnknownModuleIsReported(t *testing.T) {
	_, stderr, code := runPackage(t, map[string]string{
		"main.origin": `use nowhere::thing;

fn main() { }
`,
	})
	if code != driver.ExitDiagnostics {
		t.Fatalf("expected rejection, got exit %d", code)
	}
	if !strings.Contains(stderr, "[E0432]") {
		t.Errorf("expected E0432, got:\n%s", stderr)
	}
}

func TestSingleFileStillCompiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hello.origin")
	if err := os.WriteFile(path, []byte("use std::io;\n\nfn main() { io::println(\"hi\"); }\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	var stdout, stderr bytes.Buffer
	if code := driver.Run(path, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d\nstderr:\n%s", code, stderr.String())
	}
	if stdout.String() != "hi\n" {
		t.Errorf("stdout = %q", stdout.String())
	}
}

func TestModulePathsComeFromTheFilesystem(t *testing.T) {
	// `main.origin` and `lib.origin` are the root module; everything else takes its
	// path from its location (spec/07-modules.md).
	stdout, _, code := runPackage(t, map[string]string{
		"lib.origin":                  "use std::io;\n\nfn main() { io::println(deep::nested::mod_name::hello()); }\n",
		"deep/nested/mod_name.origin": "pub fn hello() -> String { \"from a nested module\" }\n",
	})
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if stdout != "from a nested module\n" {
		t.Errorf("stdout = %q", stdout)
	}
}

// TestBothEnginesRunAPackage checks that the bytecode compiler handles a multi-file
// package, not just a single file: module paths, cross-module calls and all.
func TestBothEnginesRunAPackage(t *testing.T) {
	files := map[string]string{
		"main.origin": `use std::io;
use geometry::shape::{Shape, area};

fn main() {
    io::println(area(Shape::Circle(2.0)).to_str());
    io::println(area(Shape::Rect { w: 3.0, h: 4.0 }).to_str());
}
`,
		"geometry/shape.origin": `pub enum Shape {
    Circle(f64),
    Rect { w: f64, h: f64 },
}

const PI: f64 = 3.14159;

pub fn area(s: Shape) -> f64 {
    match s {
        Shape::Circle(r) => PI * r * r,
        Shape::Rect { w, h } => w * h,
    }
}
`,
	}
	dir := writePackage(t, files)

	var interpOut, interpErr bytes.Buffer
	interpCode := driver.RunWith(dir, driver.Interpreter, &interpOut, &interpErr)

	var vmOut, vmErr bytes.Buffer
	vmCode := driver.RunWith(dir, driver.VM, &vmOut, &vmErr)

	if interpCode != 0 {
		t.Fatalf("interpreter exited %d\n%s", interpCode, interpErr.String())
	}
	if vmCode != 0 {
		t.Fatalf("vm exited %d\n%s", vmCode, vmErr.String())
	}
	if interpOut.String() != vmOut.String() {
		t.Errorf("engines disagree\n--- interpreter ---\n%s\n--- vm ---\n%s", interpOut.String(), vmOut.String())
	}
	if got := vmOut.String(); got != "12.56636\n12.0\n" {
		t.Errorf("stdout = %q", got)
	}
}

// A variant of another module's enum is named by its full path, in an expression and in a
// pattern. This is what a program in more than one file writes constantly -- a compiler in
// Origin would spell every AST node this way -- and it did not resolve: the code for it sat
// inside a branch that required `shapes::Shape` itself to name a module, which it never
// does, so the only way to reach a variant was to `use` the enum first.
func TestQualifiedVariantOfAnotherModulesEnum(t *testing.T) {
	stdout, stderr, code := runPackage(t, map[string]string{
		"main.origin": `use std::io;
use shapes;

fn main() {
    io::println(name(shapes::Shape::Circle(2)));
    io::println(name(shapes::Shape::Rect(3, 4)));
    io::println(name(shapes::Shape::Dot));
}

fn name(s: shapes::Shape) -> String {
    match s {
        shapes::Shape::Circle(r) => "circle \(r)",
        shapes::Shape::Rect(w, h) => "rect \(w * h)",
        shapes::Shape::Dot => "dot",
    }
}
`,
		"shapes.origin": `pub enum Shape {
    Circle(i64),
    Rect(i64, i64),
    Dot,
}
`,
	})
	if code != 0 {
		t.Fatalf("exit %d\nstderr:\n%s", code, stderr)
	}
	if want := "circle 2\nrect 12\ndot\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

// A submodule's enum, so the module prefix is more than one segment.
func TestQualifiedVariantThroughASubmodule(t *testing.T) {
	stdout, stderr, code := runPackage(t, map[string]string{
		"main.origin": `use std::io;
use ast::expr;

fn main() {
    let e = ast::expr::Node::Leaf(7);
    match e {
        ast::expr::Node::Leaf(n) => io::println(n.to_str()),
        ast::expr::Node::Empty => io::println("empty"),
    }
}
`,
		"ast/expr.origin": `pub enum Node {
    Leaf(i64),
    Empty,
}
`,
	})
	if code != 0 {
		t.Fatalf("exit %d\nstderr:\n%s", code, stderr)
	}
	if want := "7\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

// The three ways a qualified variant path can be wrong, each with its own diagnostic
// rather than the generic "cannot resolve this path".
func TestQualifiedVariantDiagnostics(t *testing.T) {
	cases := []struct {
		name, main, want string
	}{
		{"private enum", "let a = other::Hidden::One;", "`Hidden` is private"},
		{"not an enum", "let a = other::Thing::One;", "`Thing` is not an enum"},
		{"no such item", "let a = other::Nope::One;", "has no item `Nope`"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, stderr, code := runPackage(t, map[string]string{
				"main.origin": "use other;\n\nfn main() {\n    " + tc.main + "\n}\n",
				"other.origin": `enum Hidden { One, Two }
pub struct Thing { x: i64 }
`,
			})
			if code == 0 {
				t.Fatal("expected this to be rejected")
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr does not mention %q:\n%s", tc.want, stderr)
			}
		})
	}
}
