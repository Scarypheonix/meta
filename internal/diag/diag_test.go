package diag

import (
	"strings"
	"testing"

	"github.com/scarypheonix/meta/internal/source"
)

func mkspan(f *source.File, start, end int) Span { return NewSpan(f, start, end) }

func TestRenderMatchesSpecFormat(t *testing.T) {
	src := "fn main() {\n    let c = 1;\n    let x = if c { 1 } else { \"no\" };\n}\n"
	f := source.NewFile("src/main.origin", src)
	b := New()
	lo := strings.Index(src, "1 }")
	hi := strings.Index(src, "\"no\"")
	b.Errorf("E0308", mkspan(f, lo, lo+1), "type mismatch in `if` branches").
		Label("this branch has type `i64`").
		Secondary(mkspan(f, hi, hi+4), "this branch has type `String`").
		Note("both branches of an `if` expression must have the same type").
		Help("convert one branch, or use a `match` returning a common enum")

	got := b.String()
	for _, want := range []string{
		"error[E0308]: type mismatch in `if` branches",
		"--> src/main.origin:3:20",
		"^ this branch has type `i64`",
		"- this branch has type `String`",
		"= note: both branches",
		"= help: convert one branch",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered diagnostic missing %q:\n%s", want, got)
		}
	}
}

func TestSpanlessDiagnosticPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("Add accepted a diagnostic with no span; rule 1 must fail loudly")
		}
	}()
	New().Add(Diagnostic{Code: "E0001", Msg: "no span"})
}

func TestCodelessDiagnosticPanics(t *testing.T) {
	f := source.NewFile("t.origin", "x")
	defer func() {
		if r := recover(); r == nil {
			t.Error("Add accepted a diagnostic with no code; rule 5 must fail loudly")
		}
	}()
	New().Add(Diagnostic{Msg: "no code", Primary: Label{Span: mkspan(f, 0, 1)}})
}

func TestAllIsSourceOrderedRegardlessOfDiscoveryOrder(t *testing.T) {
	f := source.NewFile("t.origin", "aaaa\nbbbb\ncccc\n")
	b := New()
	b.Errorf("E0002", mkspan(f, 10, 11), "third")
	b.Errorf("E0002", mkspan(f, 0, 1), "first")
	b.Errorf("E0002", mkspan(f, 5, 6), "second")
	var got []string
	for _, d := range b.All() {
		got = append(got, d.Msg)
	}
	want := []string{"first", "second", "third"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("All() = %v, want %v (rule 6: deterministic source order)", got, want)
		}
	}
}

func TestCountsAndHasErrors(t *testing.T) {
	f := source.NewFile("t.origin", "abc")
	b := New()
	if b.HasErrors() {
		t.Error("empty bag reports errors")
	}
	b.Warnf("W0001", mkspan(f, 0, 1), "a warning")
	if b.HasErrors() {
		t.Error("a warning must not count as an error")
	}
	b.Errorf("E0001", mkspan(f, 1, 2), "an error")
	if !b.HasErrors() || b.ErrorCount() != 1 || b.WarningCount() != 1 {
		t.Errorf("counts = %d errors, %d warnings; want 1 and 1", b.ErrorCount(), b.WarningCount())
	}
}

func TestSpanTo(t *testing.T) {
	f := source.NewFile("t.origin", "abcdef")
	g := source.NewFile("other.origin", "abcdef")
	a, c := mkspan(f, 1, 2), mkspan(f, 4, 5)
	if got := a.To(c); got.Start != 1 || got.End != 5 {
		t.Errorf("To() = %d..%d, want 1..5", got.Start, got.End)
	}
	if got := c.To(a); got.Start != 1 || got.End != 5 {
		t.Errorf("To() is not symmetric: %d..%d", got.Start, got.End)
	}
	if got := a.To(mkspan(g, 0, 6)); got.File != f || got.End != 2 {
		t.Error("spans in different files must not merge")
	}
	var zero Span
	if got := zero.To(a); got != a {
		t.Error("merging with an invalid span should yield the valid one")
	}
}

func TestSpanTextAndString(t *testing.T) {
	f := source.NewFile("t.origin", "let x = 1;")
	s := mkspan(f, 4, 5)
	if s.Text() != "x" {
		t.Errorf("Text() = %q, want %q", s.Text(), "x")
	}
	if s.String() != "t.origin:1:5" {
		t.Errorf("String() = %q, want %q", s.String(), "t.origin:1:5")
	}
	var zero Span
	if zero.String() != "<no span>" || zero.Text() != "" {
		t.Error("an invalid span must render safely")
	}
}

func TestUnderlineWidthIsInScalarValues(t *testing.T) {
	// "héllo" is 6 bytes but 5 scalar values; the underline must be 5 carets.
	f := source.NewFile("t.origin", "let héllo = 1;")
	b := New()
	b.Errorf("E0433", mkspan(f, 4, 10), "unresolved name").Label("here")
	got := b.String()
	if !strings.Contains(got, "^^^^^ here") {
		t.Errorf("expected a 5-caret underline for a 5-scalar-value name:\n%s", got)
	}
}

func TestMultiLineSpanUnderlinesOnlyItsFirstLine(t *testing.T) {
	f := source.NewFile("t.origin", "fn f() {\n    1\n}\n")
	b := New()
	b.Errorf("E0002", mkspan(f, 0, 16), "spans three lines").Label("here")
	got := b.String()
	if strings.Count(got, "\n") > 8 {
		t.Errorf("multi-line span rendered too much:\n%s", got)
	}
	if !strings.Contains(got, "^^^^^^^^ here") {
		t.Errorf("expected the first line to be underlined to its end:\n%s", got)
	}
}

func TestRenderSeparatesMultipleDiagnostics(t *testing.T) {
	f := source.NewFile("t.origin", "aaa\nbbb\n")
	b := New()
	b.Errorf("E0001", mkspan(f, 0, 1), "one")
	b.Errorf("E0002", mkspan(f, 4, 5), "two")
	got := b.String()
	if strings.Count(got, "error[") != 2 {
		t.Errorf("expected two rendered diagnostics:\n%s", got)
	}
}

func TestSeverityString(t *testing.T) {
	if Error.String() != "error" || Warning.String() != "warning" {
		t.Error("severity strings must match the rendered format")
	}
}
