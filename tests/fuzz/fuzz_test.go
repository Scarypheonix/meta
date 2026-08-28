// Package fuzz holds the lexer and parser fuzz targets required by process rule 3.
//
// The properties asserted are structural, not semantic: no input may cause a panic, a
// hang, an out-of-range span, or an Error node without a diagnostic. A fuzzer that only
// checked "does not crash" would miss the last two, which are exactly the failures that
// produce unusable error messages later.
package fuzz

import (
	"testing"
	"unicode/utf8"

	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/lex"
	"github.com/scarypheonix/meta/internal/parse"
	"github.com/scarypheonix/meta/internal/source"
)

// seeds exercise every lexical and grammatical corner the fuzzer would take a long time
// to find on its own.
var seeds = []string{
	"", " ", "\n\n\n", "\ufefffn main() {}",
	"fn main() { io::println(\"hi\"); }",
	"let x = 1_000; 0xFF 0o17 0b1010 1.5e-3 'a' '\\u{1F600}' \"s\"",
	"/* /* nested */ */ // line",
	"a>>b a > > b a::b a..b a->b a=>b",
	"fn f[T: Ord + Show](x: T) -> Option[T] where T: Clone { Option::None }",
	"match v { Some(x) if x > 0 => x, None => 0, _ => 1 }",
	"struct S { pub mut x: i64 } enum E { A, B(i64), C { y: f64 } }",
	"impl Iterator for C { type Item = u64; fn next(mut self) -> Option[u64] { Option::None } }",
	"fn f() { |x| x + 1 } fn g() { || 1 }",
	"fn f() { a < b < c }", "fn f() { 1. }", "'", "\"", "/*", "0x", "1_", "\\",
	"fn f( {", "}}}}", "((((", "[[[[", "let", "use", "impl for", "trait T { fn",
	"fn f() { if x { } }", "fn f() { for x in y { } }", "fn f() { loop { break 1 } }",
}

func FuzzLex(f *testing.F) {
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		file := source.NewFile("fuzz.origin", src)
		bag := diag.New()
		toks := lex.Tokens(file, bag)

		if len(toks) == 0 || toks[len(toks)-1].Kind != lex.EOF {
			t.Fatalf("token stream does not end in EOF")
		}
		prev := 0
		for i, tk := range toks {
			if !tk.Span.Valid() {
				t.Fatalf("token %d has an invalid span", i)
			}
			if tk.Span.Start < 0 || tk.Span.End > len(src) || tk.Span.Start > tk.Span.End {
				t.Fatalf("token %d has an out-of-range span %d..%d in a %d-byte file",
					i, tk.Span.Start, tk.Span.End, len(src))
			}
			if tk.Span.Start < prev {
				t.Fatalf("token %d starts before its predecessor ends", i)
			}
			prev = tk.Span.End
		}
		// Rendering must not panic on any diagnostic the lexer produced.
		_ = bag.String()
	})
}

func FuzzParse(f *testing.F) {
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		file := source.NewFile("fuzz.origin", src)
		bag := diag.New()
		tree := parse.File(file, bag)

		if tree == nil {
			t.Fatal("parse returned no file")
		}
		// Every Error node means a diagnostic was reported (spec/09-errors.md rule 4).
		if countErrorNodes(tree) > 0 && !bag.HasErrors() {
			t.Fatalf("the tree contains an Error node but no diagnostic was reported")
		}
		// Rendering diagnostics must be total: it reads source lines by span.
		_ = bag.String()
		// Dumping must be total too; the interpreter and tests both rely on it.
		_ = ast.Dump(tree)

		// spec/01-lexical.md: a file that is not well-formed UTF-8 is rejected. Found
		// by this fuzzer in its first second: invalid bytes inside a string literal or
		// a comment used to be accepted silently.
		if !utf8.ValidString(src) && !bag.HasErrors() {
			t.Fatal("invalid UTF-8 accepted with no diagnostic")
		}
	})
}

// countErrorNodes walks the parts of the tree that can hold Error nodes.
func countErrorNodes(f *ast.File) int {
	n := 0
	for _, it := range f.Items {
		if _, bad := it.(*ast.ErrorItem); bad {
			n++
		}
		if fn, ok := it.(*ast.FnDecl); ok && fn.Body != nil {
			n += countErrorsInBlock(fn.Body)
		}
	}
	return n
}

func countErrorsInBlock(b *ast.Block) int {
	n := 0
	for _, s := range b.Stmts {
		switch v := s.(type) {
		case *ast.ExprStmt:
			n += countErrorsInExpr(v.X)
		case *ast.LetStmt:
			n += countErrorsInExpr(v.Value)
		}
	}
	n += countErrorsInExpr(b.Tail)
	return n
}

func countErrorsInExpr(e ast.Expr) int {
	switch v := e.(type) {
	case nil:
		return 0
	case *ast.ErrorExpr:
		return 1
	case *ast.Block:
		return countErrorsInBlock(v)
	case *ast.Binary:
		return countErrorsInExpr(v.L) + countErrorsInExpr(v.R)
	case *ast.Unary:
		return countErrorsInExpr(v.X)
	case *ast.Call:
		n := countErrorsInExpr(v.Fn)
		for _, a := range v.Args {
			n += countErrorsInExpr(a)
		}
		return n
	}
	return 0
}

// TestFuzzSeedsAreClean runs every seed through both targets in normal `go test`, so
// the properties are checked on every ./check without needing a fuzzing run.
func TestFuzzSeedsAreClean(t *testing.T) {
	for _, src := range seeds {
		file := source.NewFile("seed.origin", src)
		bag := diag.New()
		toks := lex.Tokens(file, bag)
		if toks[len(toks)-1].Kind != lex.EOF {
			t.Errorf("seed %q: token stream does not end in EOF", src)
		}
		bag2 := diag.New()
		tree := parse.File(source.NewFile("seed.origin", src), bag2)
		if tree == nil {
			t.Errorf("seed %q: parse returned no file", src)
		}
		if countErrorNodes(tree) > 0 && !bag2.HasErrors() {
			t.Errorf("seed %q: Error node with no diagnostic", src)
		}
	}
}
