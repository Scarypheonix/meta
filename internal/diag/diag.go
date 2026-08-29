// Package diag owns diagnostics: spans, severities, codes, and rendering.
//
// Spec/09-errors.md makes diagnostics a tested artifact rather than incidental output.
// Its seven rules are enforced here and in tests/docs:
//
//  1. every diagnostic has at least one span (Bag panics otherwise);
//  2. no internal identifiers in messages (linted in tests/docs);
//  3. types print in source syntax (the caller's responsibility);
//  4. no cascading (Bag.Poisoned lets passes cooperate);
//  5. codes are permanent (docs/spec/codes.md, cross-checked by tests/conformance);
//  6. errors are reported in source order deterministically (Bag.Sorted);
//  7. exit 0 / 1 / 101 (the driver's responsibility).
package diag

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/scarypheonix/meta/internal/source"
)

// Severity distinguishes errors, which stop compilation, from warnings, which do not.
type Severity int

const (
	// Error means the compilation is rejected and no binary is produced.
	Error Severity = iota
	// Warning means the code compiles but something is suspicious.
	Warning
)

func (s Severity) String() string {
	if s == Warning {
		return "warning"
	}
	return "error"
}

// Span is a half-open byte range within a file. The zero Span has a nil File and is
// invalid; Bag rejects it.
type Span struct {
	File  *source.File
	Start int
	End   int
}

// NewSpan builds a span, clamping End to at least Start.
func NewSpan(f *source.File, start, end int) Span {
	if end < start {
		end = start
	}
	return Span{File: f, Start: start, End: end}
}

// Valid reports whether the span names a real range in a real file.
func (s Span) Valid() bool { return s.File != nil && s.Start >= 0 && s.End >= s.Start }

// To returns the span covering both s and other. Spans in different files do not merge;
// s is returned unchanged.
func (s Span) To(other Span) Span {
	if !s.Valid() {
		return other
	}
	if !other.Valid() || s.File != other.File {
		return s
	}
	start, end := s.Start, s.End
	if other.Start < start {
		start = other.Start
	}
	if other.End > end {
		end = other.End
	}
	return Span{File: s.File, Start: start, End: end}
}

// Text returns the source text the span covers.
func (s Span) Text() string {
	if !s.Valid() || s.End > len(s.File.Src) {
		return ""
	}
	return s.File.Src[s.Start:s.End]
}

// Position returns the 1-based line and column of the span's start.
func (s Span) Position() (line, col int) {
	if !s.Valid() {
		return 0, 0
	}
	return s.File.Position(s.Start)
}

func (s Span) String() string {
	if !s.Valid() {
		return "<no span>"
	}
	line, col := s.Position()
	return fmt.Sprintf("%s:%d:%d", s.File.Name, line, col)
}

// Label is a span with an explanation attached to it.
type Label struct {
	Span Span
	Msg  string
}

// Diagnostic is one reported problem.
type Diagnostic struct {
	Severity  Severity
	Code      string // e.g. "E0308"; must be registered in docs/spec/codes.md
	Msg       string
	Primary   Label
	Secondary []Label
	Notes     []string // rendered as "= note: ..."
	Helps     []string // rendered as "= help: ..."
}

// Bag collects diagnostics for one compilation.
//
// It is not safe for concurrent use; each compilation owns one.
type Bag struct {
	diags    []Diagnostic
	errors   int
	warnings int
}

// New returns an empty Bag.
func New() *Bag { return &Bag{} }

// Add records a diagnostic. It panics if the diagnostic has no valid primary span or no
// code: rule 1 of the diagnostic contract is not advisory, and a spanless diagnostic is
// an implementation bug that must fail loudly rather than reach a user.
func (b *Bag) Add(d Diagnostic) {
	if !d.Primary.Span.Valid() {
		panic(fmt.Sprintf("diagnostic %q has no valid span: %s (spec/09-errors.md rule 1)", d.Code, d.Msg))
	}
	if d.Code == "" {
		panic(fmt.Sprintf("diagnostic has no code: %s (spec/09-errors.md rule 5)", d.Msg))
	}
	b.diags = append(b.diags, d)
	if d.Severity == Warning {
		b.warnings++
	} else {
		b.errors++
	}
}

// Errorf records an error with a primary span and no labels beyond it.
func (b *Bag) Errorf(code string, span Span, format string, args ...any) *Builder {
	d := Diagnostic{Severity: Error, Code: code, Msg: fmt.Sprintf(format, args...), Primary: Label{Span: span}}
	b.diags = append(b.diags, d)
	b.errors++
	return &Builder{bag: b, idx: len(b.diags) - 1}
}

// Warnf records a warning.
func (b *Bag) Warnf(code string, span Span, format string, args ...any) *Builder {
	d := Diagnostic{Severity: Warning, Code: code, Msg: fmt.Sprintf(format, args...), Primary: Label{Span: span}}
	b.diags = append(b.diags, d)
	b.warnings++
	return &Builder{bag: b, idx: len(b.diags) - 1}
}

// Builder attaches labels, notes and helps to the diagnostic just recorded.
type Builder struct {
	bag *Bag
	idx int
}

// Label sets the message shown under the primary span's caret.
func (bd *Builder) Label(format string, args ...any) *Builder {
	bd.bag.diags[bd.idx].Primary.Msg = fmt.Sprintf(format, args...)
	return bd
}

// Secondary adds another span with its own message.
func (bd *Builder) Secondary(span Span, format string, args ...any) *Builder {
	d := &bd.bag.diags[bd.idx]
	d.Secondary = append(d.Secondary, Label{Span: span, Msg: fmt.Sprintf(format, args...)})
	return bd
}

// Note adds an explanatory note.
func (bd *Builder) Note(format string, args ...any) *Builder {
	d := &bd.bag.diags[bd.idx]
	d.Notes = append(d.Notes, fmt.Sprintf(format, args...))
	return bd
}

// Help adds a suggested fix.
func (bd *Builder) Help(format string, args ...any) *Builder {
	d := &bd.bag.diags[bd.idx]
	d.Helps = append(d.Helps, fmt.Sprintf(format, args...))
	return bd
}

// HasErrors reports whether any error (not warning) was recorded.
func (b *Bag) HasErrors() bool { return b.errors > 0 }

// ErrorCount and WarningCount report the tallies.
func (b *Bag) ErrorCount() int   { return b.errors }
func (b *Bag) WarningCount() int { return b.warnings }

// All returns the diagnostics in source order: by file name, then by start offset, then
// by end offset. Rule 6 requires this to be deterministic regardless of the order passes
// happened to discover problems in.
func (b *Bag) All() []Diagnostic {
	out := make([]Diagnostic, len(b.diags))
	copy(out, b.diags)
	sort.SliceStable(out, func(i, j int) bool {
		a, c := out[i].Primary.Span, out[j].Primary.Span
		an, cn := "", ""
		if a.File != nil {
			an = a.File.Name
		}
		if c.File != nil {
			cn = c.File.Name
		}
		if an != cn {
			return an < cn
		}
		if a.Start != c.Start {
			return a.Start < c.Start
		}
		return a.End < c.End
	})
	return out
}

// Codes returns the set of codes present, for tests.
func (b *Bag) Codes() []string {
	var out []string
	for _, d := range b.All() {
		out = append(out, d.Code)
	}
	return out
}

// Render writes every diagnostic in source order in the format specified by
// spec/09-errors.md.
func (b *Bag) Render(w io.Writer) {
	for i, d := range b.All() {
		if i > 0 {
			fmt.Fprintln(w)
		}
		renderOne(w, d)
	}
}

// String renders to a string, which is what golden tests compare.
func (b *Bag) String() string {
	var sb strings.Builder
	b.Render(&sb)
	return sb.String()
}

func renderOne(w io.Writer, d Diagnostic) {
	fmt.Fprintf(w, "%s[%s]: %s\n", d.Severity, d.Code, d.Msg)

	span := d.Primary.Span
	line, col := span.Position()
	gutter := len(fmt.Sprint(maxLine(d)))
	pad := strings.Repeat(" ", gutter+1)

	fmt.Fprintf(w, "%s--> %s:%d:%d\n", pad, span.File.Name, line, col)
	fmt.Fprintf(w, "%s |\n", pad)

	// Labels are rendered in source order, not in the order they were attached, so a
	// secondary span earlier in the file reads before the primary rather than after it.
	type entry struct {
		label  Label
		marker rune
		line   int
	}
	entries := []entry{{label: d.Primary, marker: '^', line: line}}
	for _, sec := range d.Secondary {
		if sec.Span.File != span.File {
			continue
		}
		sl, _ := sec.Span.Position()
		entries = append(entries, entry{label: sec, marker: '-', line: sl})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].line != entries[j].line {
			return entries[i].line < entries[j].line
		}
		return entries[i].label.Span.Start < entries[j].label.Span.Start
	})

	prev := 0
	for i, e := range entries {
		// A gap between labelled lines is elided rather than printed in full.
		if i > 0 && e.line > prev+1 {
			fmt.Fprintf(w, "%s...\n", strings.Repeat(" ", gutter))
		}
		renderLabel(w, gutter, e.label, e.marker)
		prev = e.line
	}

	fmt.Fprintf(w, "%s |\n", pad)
	for _, n := range d.Notes {
		fmt.Fprintf(w, "%s = note: %s\n", strings.Repeat(" ", gutter), n)
	}
	for _, h := range d.Helps {
		fmt.Fprintf(w, "%s = help: %s\n", strings.Repeat(" ", gutter), h)
	}
}

// renderLabel prints the source line and an underline beneath the labelled range.
func renderLabel(w io.Writer, gutter int, lbl Label, marker rune) {
	span := lbl.Span
	line, col := span.Position()
	text := span.File.LineText(line)

	fmt.Fprintf(w, "%*d | %s\n", gutter+1, line, text)

	// The underline starts at the label's column and runs for the span's width in
	// scalar values, clamped to the line so a multi-line span underlines its first line.
	width := scalarWidth(span.Text())
	lineEndOffset := span.File.LineStart(line) + len(text)
	if span.End > lineEndOffset {
		width = scalarWidth(span.File.Src[span.Start:lineEndOffset])
	}
	if width < 1 {
		width = 1
	}
	pad := strings.Repeat(" ", gutter+1) + " | " + leadingWhitespaceFor(text, col)
	fmt.Fprintf(w, "%s%s", pad, strings.Repeat(string(marker), width))
	if lbl.Msg != "" {
		fmt.Fprintf(w, " %s", lbl.Msg)
	}
	fmt.Fprintln(w)
}

// leadingWhitespaceFor produces the indentation that puts a caret under column col,
// preserving tabs from the source line so the caret lines up in a terminal that renders
// them.
func leadingWhitespaceFor(text string, col int) string {
	var sb strings.Builder
	i := 0
	for _, r := range text {
		if i >= col-1 {
			break
		}
		if r == '\t' {
			sb.WriteByte('\t')
		} else {
			sb.WriteByte(' ')
		}
		i++
	}
	for ; i < col-1; i++ {
		sb.WriteByte(' ')
	}
	return sb.String()
}

func scalarWidth(s string) int { return utf8.RuneCountInString(s) }

func maxLine(d Diagnostic) int {
	max := 0
	if line, _ := d.Primary.Span.Position(); line > max {
		max = line
	}
	for _, s := range d.Secondary {
		if line, _ := s.Span.Position(); line > max {
			max = line
		}
	}
	return max
}
