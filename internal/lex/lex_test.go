package lex

import (
	"strconv"
	"strings"
	"testing"

	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/source"
)

// lexAll lexes src and returns the tokens plus the diagnostic bag.
func lexAll(t *testing.T, src string) ([]Token, *diag.Bag) {
	t.Helper()
	bag := diag.New()
	return Tokens(source.NewFile("t.origin", src), bag), bag
}

// kinds returns the token kinds excluding the trailing EOF.
func kinds(toks []Token) []Kind {
	if len(toks) == 0 {
		return nil
	}
	out := make([]Kind, 0, len(toks)-1)
	for _, tk := range toks[:len(toks)-1] {
		out = append(out, tk.Kind)
	}
	return out
}

func eqKinds(a, b []Kind) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestPunctuationMaximalMunch(t *testing.T) {
	tests := []struct {
		src  string
		want []Kind
	}{
		{"a>>b", []Kind{Ident, Shr, Ident}},
		{"a > > b", []Kind{Ident, Gt, Gt, Ident}},
		{"a::b", []Kind{Ident, ColonColon, Ident}},
		{"a:b", []Kind{Ident, Colon, Ident}},
		{"->", []Kind{Arrow}},
		{"=>", []Kind{FatArrow}},
		{"==", []Kind{EqEq}},
		{"=", []Kind{Assign}},
		{"!=", []Kind{BangEq}},
		{"!", []Kind{Bang}},
		{"&&", []Kind{AmpAmp}},
		{"&", []Kind{Amp}},
		{"||", []Kind{PipePipe}},
		{"|", []Kind{Pipe}},
		{"+=", []Kind{PlusEq}},
		{"..", []Kind{DotDot}},
		{".", []Kind{Dot}},
		{"Map[K, V]", []Kind{Ident, LBracket, Ident, Comma, Ident, RBracket}},
	}
	for _, tt := range tests {
		toks, bag := lexAll(t, tt.src)
		if bag.HasErrors() {
			t.Errorf("%q produced errors:\n%s", tt.src, bag)
		}
		if got := kinds(toks); !eqKinds(got, tt.want) {
			t.Errorf("%q lexed as %v, want %v", tt.src, got, tt.want)
		}
	}
}

func TestKeywordsAndIdentifiers(t *testing.T) {
	toks, bag := lexAll(t, "fn let mut self Self while identifier _")
	if bag.HasErrors() {
		t.Fatalf("unexpected errors:\n%s", bag)
	}
	want := []Kind{KwFn, KwLet, KwMut, KwSelfValue, KwSelfType, KwWhile, Ident, Underscore}
	if got := kinds(toks); !eqKinds(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestReservedWordsAreRejectedButStillLexAsIdentifiers(t *testing.T) {
	toks, bag := lexAll(t, "let unsafe = 1;")
	if !bag.HasErrors() {
		t.Fatal("expected an error for a reserved word")
	}
	if !strings.Contains(bag.String(), "reserved word") {
		t.Errorf("diagnostic should say `reserved word`:\n%s", bag)
	}
	// Recovery: it still lexes as an identifier so the parser can continue.
	if toks[1].Kind != Ident {
		t.Errorf("reserved word lexed as %v, want Ident for recovery", toks[1].Kind)
	}
}

func TestIntegerLiterals(t *testing.T) {
	tests := []struct {
		src      string
		want     uint64
		suffix   string
		wantErrs bool
	}{
		{"0", 0, "", false},
		{"42", 42, "", false},
		{"1_000", 1000, "", false},
		{"0xFF", 255, "", false},
		{"0xff", 255, "", false},
		{"0o17", 15, "", false},
		{"0b1010", 10, "", false},
		{"0b1010i32", 10, "i32", false},
		{"255u8", 255, "u8", false},
		{"9223372036854775807", 9223372036854775807, "", false},
		{"1_0_0", 100, "", false},
		{"_1", 0, "", false}, // lexes as an identifier, not a literal
		{"1_", 0, "", true},
		{"0xFF_u8", 0, "", true}, // separator immediately before the suffix
		{"0x", 0, "", true},
		{"1z", 1, "", true},
		{"18446744073709551616", 0, "", false}, // overflows u64: flagged, not an error here
	}
	for _, tt := range tests {
		toks, bag := lexAll(t, tt.src)
		if bag.HasErrors() != tt.wantErrs {
			t.Errorf("%q: errors = %v, want %v\n%s", tt.src, bag.HasErrors(), tt.wantErrs, bag)
			continue
		}
		if tt.wantErrs || tt.src == "_1" {
			continue
		}
		tk := toks[0]
		if tk.Kind != Int {
			t.Errorf("%q lexed as %v, want Int", tt.src, tk.Kind)
			continue
		}
		if tt.src == "18446744073709551616" {
			if !tk.IntOverflow {
				t.Errorf("%q should set IntOverflow", tt.src)
			}
			continue
		}
		if tk.Int != tt.want || tk.Suffix != tt.suffix {
			t.Errorf("%q = %d suffix %q, want %d suffix %q", tt.src, tk.Int, tk.Suffix, tt.want, tt.suffix)
		}
	}
}

func TestFloatLiterals(t *testing.T) {
	tests := []struct {
		src      string
		want     float64
		suffix   string
		wantKind Kind
		wantErrs bool
	}{
		{"1.5", 1.5, "", Float, false},
		{"1.5e-3", 1.5e-3, "", Float, false},
		{"1e10", 1e10, "", Float, false},
		{"1E+10", 1e10, "", Float, false},
		{"3.14f32", 3.14, "f32", Float, false},
		{"1_000.5", 1000.5, "", Float, false},
		{"1.5i32", 0, "", Float, true},
		{"1e400", 0, "", Float, true},
	}
	for _, tt := range tests {
		toks, bag := lexAll(t, tt.src)
		if bag.HasErrors() != tt.wantErrs {
			t.Errorf("%q: errors = %v, want %v\n%s", tt.src, bag.HasErrors(), tt.wantErrs, bag)
			continue
		}
		if tt.wantErrs {
			continue
		}
		tk := toks[0]
		if tk.Kind != tt.wantKind || tk.Float != tt.want || tk.Suffix != tt.suffix {
			t.Errorf("%q = %v %g suffix %q, want %v %g suffix %q",
				tt.src, tk.Kind, tk.Float, tk.Suffix, tt.wantKind, tt.want, tt.suffix)
		}
	}
}

// A '.' not followed by a digit is a field access, so `1.to_str()` works and a bare
// `1.` is left for the parser to reject (spec/01-lexical.md).
func TestDotAfterIntegerIsFieldAccessNotFloat(t *testing.T) {
	toks, bag := lexAll(t, "1.to_str()")
	if bag.HasErrors() {
		t.Fatalf("unexpected errors:\n%s", bag)
	}
	want := []Kind{Int, Dot, Ident, LParen, RParen}
	if got := kinds(toks); !eqKinds(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}

	toks2, bag2 := lexAll(t, "1.")
	if bag2.HasErrors() {
		t.Fatalf("`1.` should lex cleanly and be rejected by the parser:\n%s", bag2)
	}
	if got := kinds(toks2); !eqKinds(got, []Kind{Int, Dot}) {
		t.Errorf("`1.` lexed as %v, want Int Dot", got)
	}
}

func TestStringLiterals(t *testing.T) {
	tests := []struct {
		src      string
		want     string
		wantErrs bool
	}{
		{`"hello"`, "hello", false},
		{`""`, "", false},
		{`"a\nb"`, "a\nb", false},
		{`"a\tb\r\0"`, "a\tb\r\x00", false},
		{`"\\"`, `\`, false},
		{`"\""`, `"`, false},
		{`"\x41"`, "A", false},
		{`"\u{1F600}"`, "\U0001F600", false},
		// A literal may span lines and the newline is part of the value
		// (spec/01-lexical.md); only the end of the file leaves one unterminated.
		{"\"line\nbreak\"", "line\nbreak", false},
		{`"unterminated`, "", true},
		{`"\q"`, "", true},
		{`"\x80"`, "", true},
		{`"\u{D800}"`, "", true},
		{`"\u{110000}"`, "", true},
		{`"\u{}"`, "", true},
		{`"\u{1234567}"`, "", true},
	}
	for _, tt := range tests {
		toks, bag := lexAll(t, tt.src)
		if bag.HasErrors() != tt.wantErrs {
			t.Errorf("%q: errors = %v, want %v\n%s", tt.src, bag.HasErrors(), tt.wantErrs, bag)
			continue
		}
		if tt.wantErrs {
			continue
		}
		if toks[0].Kind != Str || toks[0].Str != tt.want {
			t.Errorf("%q = %v %q, want Str %q", tt.src, toks[0].Kind, toks[0].Str, tt.want)
		}
	}
}

func TestCharLiterals(t *testing.T) {
	tests := []struct {
		src      string
		want     rune
		wantErrs bool
	}{
		{`'a'`, 'a', false},
		{`'\n'`, '\n', false},
		{`'\''`, '\'', false},
		{`'\u{1F600}'`, '\U0001F600', false},
		{`'é'`, 'é', false},
		{`''`, 0, true},
		{`'ab'`, 0, true},
		{`'a`, 0, true},
		{`'\u{D800}'`, 0, true},
	}
	for _, tt := range tests {
		toks, bag := lexAll(t, tt.src)
		if bag.HasErrors() != tt.wantErrs {
			t.Errorf("%q: errors = %v, want %v\n%s", tt.src, bag.HasErrors(), tt.wantErrs, bag)
			continue
		}
		if tt.wantErrs {
			continue
		}
		if toks[0].Kind != Char || toks[0].Char != tt.want {
			t.Errorf("%q = %v %q, want Char %q", tt.src, toks[0].Kind, toks[0].Char, tt.want)
		}
	}
}

func TestComments(t *testing.T) {
	toks, bag := lexAll(t, "a // line comment\nb /* block */ c /* /* nested */ */ d")
	if bag.HasErrors() {
		t.Fatalf("unexpected errors:\n%s", bag)
	}
	if got := kinds(toks); !eqKinds(got, []Kind{Ident, Ident, Ident, Ident}) {
		t.Errorf("got %v, want four identifiers", got)
	}
}

func TestUnterminatedBlockCommentPointsAtTheOutermostOpener(t *testing.T) {
	src := "a /* /* inner */ still open"
	_, bag := lexAll(t, src)
	if !bag.HasErrors() {
		t.Fatal("expected an unterminated block comment error")
	}
	d := bag.All()[0]
	if d.Primary.Span.Start != strings.Index(src, "/*") {
		t.Errorf("span starts at %d, want the outermost `/*` at %d",
			d.Primary.Span.Start, strings.Index(src, "/*"))
	}
}

// The lexer must report every error in one pass, not stop at the first.
func TestMultipleErrorsInOnePass(t *testing.T) {
	_, bag := lexAll(t, "let a = '' ; let b = \"\\q\" ; let c = 1_ ;")
	if n := bag.ErrorCount(); n < 3 {
		t.Errorf("reported %d errors, want at least 3 in one pass:\n%s", n, bag)
	}
}

func TestInvalidCharacterRecovers(t *testing.T) {
	toks, bag := lexAll(t, "a # b")
	if !bag.HasErrors() {
		t.Fatal("expected an error for `#`")
	}
	if got := kinds(toks); !eqKinds(got, []Kind{Ident, Ident}) {
		t.Errorf("got %v, want the two identifiers around the invalid character", got)
	}
}

func TestByteOrderMarkIsSkippedAtStart(t *testing.T) {
	toks, bag := lexAll(t, "\uFEFFfn")
	if bag.HasErrors() {
		t.Fatalf("a leading BOM must be skipped:\n%s", bag)
	}
	if toks[0].Kind != KwFn {
		t.Errorf("got %v, want KwFn", toks[0].Kind)
	}
}

func TestSpansArePreciseAndInOrder(t *testing.T) {
	src := "let x = 42;"
	toks, bag := lexAll(t, src)
	if bag.HasErrors() {
		t.Fatalf("unexpected errors:\n%s", bag)
	}
	for i, tk := range toks {
		if !tk.Span.Valid() {
			t.Fatalf("token %d has an invalid span", i)
		}
		if tk.Span.Start > tk.Span.End || tk.Span.End > len(src) {
			t.Fatalf("token %d has an out-of-range span %d..%d", i, tk.Span.Start, tk.Span.End)
		}
		if i > 0 && tk.Span.Start < toks[i-1].Span.End {
			t.Fatalf("token %d overlaps its predecessor", i)
		}
	}
	if got := toks[3].Span.Text(); got != "42" {
		t.Errorf("literal span covers %q, want %q", got, "42")
	}
	if line, col := toks[1].Span.Position(); line != 1 || col != 5 {
		t.Errorf("`x` is at %d:%d, want 1:5", line, col)
	}
}

func TestEmptyInputYieldsOnlyEOF(t *testing.T) {
	toks, bag := lexAll(t, "")
	if bag.HasErrors() || len(toks) != 1 || toks[0].Kind != EOF {
		t.Errorf("empty input produced %d tokens, errors=%v", len(toks), bag.HasErrors())
	}
}

func TestKindStringsReadNaturallyInDiagnostics(t *testing.T) {
	for _, tt := range []struct {
		k    Kind
		want string
	}{
		{Semi, ";"}, {KwFn, "fn"}, {Ident, "identifier"}, {EOF, "end of file"},
	} {
		if got := tt.k.String(); got != tt.want {
			t.Errorf("Kind(%d).String() = %q, want %q", int(tt.k), got, tt.want)
		}
	}
}

// String interpolation (spec/01-lexical.md): `\(` opens an expression, and the lexer
// produces its tokens rather than a source range for the parser to re-lex.
func TestStringInterpolation(t *testing.T) {
	tests := []struct {
		src string
		// want is the parts, written as "text" for a literal chunk and "<n>" for an
		// expression part of n tokens (the terminating EOF included).
		want     []string
		wantErrs bool
	}{
		{`"plain"`, nil, false},                            // no parts at all
		{`"a\(x)b"`, []string{"a", "<2>", "b"}, false},     // x, EOF
		{`"\(x)"`, []string{"<2>"}, false},                 // an empty chunk is not a part
		{`"\(x)\(y)"`, []string{"<2>", "<2>"}, false},      // and neither is the one between
		{`"a\(x + 1)"`, []string{"a", "<4>"}, false},       // x + 1 EOF
		{`"a\(f(1))"`, []string{"a", "<5>"}, false},        // f ( 1 ) EOF
		{`"a\("b)c")d"`, []string{"a", "<2>", "d"}, false}, // a `)` in a nested string closes nothing
		{`"a\nb\(x)"`, []string{"a\nb", "<2>"}, false},     // ordinary escapes still work
		{`"\("`, nil, true},                                // unterminated
		{`"\()"`, nil, true},                               // empty
		{`"\(x"`, nil, true},                               // never closed
	}
	for _, tt := range tests {
		toks, bag := lexAll(t, tt.src)
		if bag.HasErrors() != tt.wantErrs {
			t.Errorf("%q: errors = %v, want %v\n%s", tt.src, bag.HasErrors(), tt.wantErrs, bag)
			continue
		}
		if tt.wantErrs {
			continue
		}
		if toks[0].Kind != Str {
			t.Errorf("%q: first token is %v, want a string", tt.src, toks[0].Kind)
			continue
		}
		if (toks[0].Parts != nil) != (tt.want != nil) {
			t.Errorf("%q: Parts != nil is %v, want %v", tt.src, toks[0].Parts != nil, tt.want != nil)
			continue
		}
		var got []string
		for _, p := range toks[0].Parts {
			if p.Expr == nil {
				got = append(got, p.Text)
			} else {
				got = append(got, "<"+strconv.Itoa(len(p.Expr))+">")
			}
		}
		if len(got) != len(tt.want) {
			t.Errorf("%q: parts = %v, want %v", tt.src, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("%q: part %d = %q, want %q", tt.src, i, got[i], tt.want[i])
			}
		}
	}
}

// The text of an interpolated literal, with the interpolations removed, is what a
// diagnostic naming the literal has to show.
func TestInterpolatedLiteralKeepsItsText(t *testing.T) {
	toks, bag := lexAll(t, `"a\(x)b\(y)c"`)
	if bag.HasErrors() {
		t.Fatalf("unexpected errors:\n%s", bag)
	}
	if toks[0].Str != "abc" {
		t.Errorf("Str = %q, want %q", toks[0].Str, "abc")
	}
}
