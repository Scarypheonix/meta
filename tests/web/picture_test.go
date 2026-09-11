package web

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scarypheonix/meta/internal/diag"
	origindriver "github.com/scarypheonix/meta/internal/driver"
	"github.com/scarypheonix/meta/internal/lex"
	"github.com/scarypheonix/meta/internal/source"
	"github.com/scarypheonix/meta/internal/testutil"
)

// The tutorial's code pictures (web/tutorial.html, and the repository README).
//
// A picture of a program is the one thing on the site that cannot be checked by running it,
// so these are generated rather than drawn: every one is rendered here from source that
// this test also compiles, and the SVG on disk is a golden file like any other.
//
//	UPDATE_GOLDEN=1 go test ./tests/web/
//
// The colouring is the project's own lexer (internal/lex), not a second one written for the
// purpose. Comments are the gaps: the lexer skips them as trivia, so what is *between* two
// tokens is trivia by construction and a `//` in it runs to the end of its line. That keeps
// the one thing a separate tokenizer would have got subtly wrong -- what counts as code --
// answered by the code that decides it everywhere else (process rule 5).

// pictureDir is where the rendered cards live, relative to the repository root.
const pictureDir = "web/pictures"

// picture is one card: a name, and either an end-to-end case to take the source from or the
// source itself.
type picture struct {
	name string
	// from is a case in tests/e2e/cases; when it is empty, src is the program.
	from string
	src  string
}

// pictures are the tutorial's steps, in its order. A card whose program is a corpus case
// names it rather than repeating it, so the picture cannot drift from the program the suite
// actually runs. An inline one is compiled below instead.
var pictures = []picture{
	{name: "01-hello", from: "hello"},
	{name: "02-ask", src: `let name = ask("What is your name? ")
println("Hello, \(name)!")
`},
	{name: "03-numbers", src: `let answer = ask("How many slices? ")

if let Some(n) = answer.parse_int() {
    println("That is \(n * 2) halves.")
} else {
    println("I could not read that as a number.")
}
`},
	{name: "04-choices", src: `let score = 87

if score >= 90 {
    println("top marks")
} else if score >= 60 {
    println("a pass")
} else {
    println("not this time")
}

// A match is the same idea when there is a fixed set of answers.
let day = "sat"
match day {
    "sat" => println("weekend"),
    "sun" => println("weekend"),
    other => println("\(other) is a school day"),
}
`},
	{name: "05-repeating", src: `for n in range(1, 4) {
    println("\(n)...")
}
println("go")

let mut left = 3
while left > 0 {
    println("\(left) to go")
    left = left - 1
}
`},
	{name: "06-lists", src: `let names = ["ada", "grace", "alan", "edsger"]

println("there are \(names.len()) of them")
println(names.join(", "))

let long = names.filter(|n| n.len() > 4)
println("longer than four letters: \(long.join(", "))")

for n in range(1, 4) {
    println("\(n) squared is \(n * n)")
}
`},
	{name: "07-functions", from: "fib"},
	{name: "08-your-own-types", src: `struct Dog {
    name: String,
    mut treats: i64,
}

fn feed(d: Dog) {
    d.treats = d.treats + 1
}

let rex = Dog { name: "Rex", treats: 0 };
feed(rex)
feed(rex)
println("\(rex.name) has had \(rex.treats) treats")
`},
	{name: "09-nothing-and-errors", src: `// There is no null in Origin. A thing that might not be there is an Option,
// and the compiler will not let you forget to check.

let names = ["ada", "grace"]

match names.first() {
    Some(n) => println("first: \(n)"),
    None => println("the list is empty"),
}

// And a thing that might go wrong is a Result, which is a value too.
match read_to_string("notes.txt") {
    Ok(text) => println("\(text.len()) bytes"),
    Err(e) => println("could not read it: \(e.to_str())"),
}
`},
}

// TestTutorialProgramsCompile holds every picture to the same standard as everything else
// the site shows: it is a program that compiles. A card taken from a corpus case is already
// run on three engines at three optimization levels by tests/e2e; an inline one is checked
// here, which is the whole reason inline ones are allowed at all.
func TestTutorialProgramsCompile(t *testing.T) {
	root := testutil.RepoRoot(t)
	dir := t.TempDir()
	for _, p := range pictures {
		if p.from != "" {
			continue
		}
		t.Run(p.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(p.name, "-", "_")+".origin")
			if err := os.WriteFile(path, []byte(p.src), 0o644); err != nil {
				t.Fatal(err)
			}
			var out strings.Builder
			if code := origindriver.Check(path, &out, &out); code != origindriver.ExitOK {
				t.Errorf("the tutorial's %s does not compile:\n%s", p.name, out.String())
			}
		})
	}
	_ = root
}

// programsPath is the tutorial's own programs, as data the page can read.
//
// The page shows a picture, and a picture cannot be copied or run. Rather than write the
// same program twice -- once as a card and once as text for the buttons -- both come from
// here, so the "copy" button and the "open in the playground" link can never hand someone a
// different program from the one in the picture above them.
const programsPath = "web/tutorial-programs.js"

// TestTutorialProgramsFileIsCurrent regenerates that file and fails if it has drifted.
func TestTutorialProgramsFileIsCurrent(t *testing.T) {
	root := testutil.RepoRoot(t)
	var b strings.Builder
	b.WriteString("// Generated by tests/web/picture_test.go. Do not edit.\n")
	b.WriteString("//\n")
	b.WriteString("// The tutorial's programs, as text, so that the page's copy and run buttons hand\n")
	b.WriteString("// over exactly what web/pictures/*.svg shows.\n")
	b.WriteString("//\n")
	b.WriteString("// Regenerate: UPDATE_GOLDEN=1 go test ./tests/web/\n")
	b.WriteString("window.originTutorial = {\n")
	for _, p := range pictures {
		fmt.Fprintf(&b, "  %s: %s,\n", jsString(p.name), jsString(sourceOf(t, root, p)))
	}
	b.WriteString("};\n")

	path := filepath.Join(root, programsPath)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
		return
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v\nregenerate: UPDATE_GOLDEN=1 go test ./tests/web/", path, err)
	}
	if string(got) != b.String() {
		t.Errorf("%s is out of date.\nRegenerate: UPDATE_GOLDEN=1 go test ./tests/web/", programsPath)
	}
}

func sourceOf(t *testing.T, root string, p picture) string {
	t.Helper()
	if p.from != "" {
		return readCase(t, root, p.from, ".origin")
	}
	return p.src
}

// TestPicturesAreCurrent regenerates the cards and fails if what is committed differs.
func TestPicturesAreCurrent(t *testing.T) {
	root := testutil.RepoRoot(t)
	update := os.Getenv("UPDATE_GOLDEN") == "1"
	if update {
		if err := os.MkdirAll(filepath.Join(root, pictureDir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range pictures {
		want := renderPicture(sourceOf(t, root, p))
		path := filepath.Join(root, pictureDir, p.name+".svg")
		if update {
			if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
				t.Fatalf("writing %s: %v", path, err)
			}
			continue
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("reading %s: %v\nregenerate: UPDATE_GOLDEN=1 go test ./tests/web/", path, err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s/%s.svg is out of date.\nRegenerate: UPDATE_GOLDEN=1 go test ./tests/web/",
				pictureDir, p.name)
		}
	}
}

// ---------------------------------------------------------------- rendering

// The card's geometry, in CSS pixels. The advance width is the one number that has to be
// guessed, and it is used only to size the box: runs within a line are laid out by the SVG
// text engine itself, so a font whose advance differs shifts the right-hand margin and
// nothing else.
const (
	picFontSize   = 13.0
	picLineHeight = 20.8
	picAdvance    = 7.83
	picPadX       = 18.0
	picPadY       = 16.0
)

// The palette is web/style.css's light theme, written out rather than referenced: the card
// is shown on GitHub as well as on the site, where no stylesheet of ours applies, so it has
// to carry its own colours and its own background.
var picColors = map[string]string{
	"keyword": "#8250a8",
	"type":    "#2f6f5e",
	"fn":      "#2f5d7c",
	"num":     "#9a5518",
	"str":     "#3f7a3f",
	"comment": "#8a8a85",
	"op":      "#55606b",
	"plain":   "#1a1a1a",
}

// run is one coloured stretch of one line.
type run struct {
	text  string
	class string
}

func renderPicture(src string) string {
	lines := colorize(src)

	width := 0
	for _, l := range lines {
		n := 0
		for _, r := range l {
			n += len([]rune(r.text))
		}
		if n > width {
			width = n
		}
	}

	w := picPadX*2 + float64(width)*picAdvance
	h := picPadY*2 + float64(len(lines))*picLineHeight

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%.0f" height="%.0f" `+
		`viewBox="0 0 %.0f %.0f" role="img" aria-label="Origin source">`+"\n", w, h, w, h)
	fmt.Fprintf(&b, `<rect x="0" y="0" width="%.0f" height="%.0f" rx="8" fill="#ffffff" stroke="#e2e2df"/>`+"\n", w, h)
	fmt.Fprintf(&b, `<g font-family="ui-monospace, SFMono-Regular, Menlo, Consolas, monospace" `+
		`font-size="%.0f" xml:space="preserve">`+"\n", picFontSize)
	for i, l := range lines {
		y := picPadY + float64(i)*picLineHeight + picFontSize
		fmt.Fprintf(&b, `<text x="%.0f" y="%.1f">`, picPadX, y)
		for _, r := range l {
			style := ""
			if r.class == "comment" {
				style = ` font-style="italic"`
			}
			fmt.Fprintf(&b, `<tspan fill="%s"%s>%s</tspan>`, picColors[r.class], style, escapeXML(r.text))
		}
		b.WriteString("</text>\n")
	}
	b.WriteString("</g>\n</svg>\n")
	return b.String()
}

// colorize splits source into lines of coloured runs.
//
// The token spans come from internal/lex. Everything between one token's end and the next
// one's start is trivia, and a `//` there runs to the end of its line; that is the whole of
// what this has to decide for itself, because the lexer has already decided what is code.
func colorize(src string) [][]run {
	f := source.NewFile("picture.origin", src)
	bag := diag.New()
	l := lex.New(f, bag)

	var runs []run
	at := 0
	emitTrivia := func(upto int) {
		gap := src[at:upto]
		for len(gap) > 0 {
			i := strings.Index(gap, "//")
			if i < 0 {
				runs = append(runs, run{gap, "plain"})
				break
			}
			if i > 0 {
				runs = append(runs, run{gap[:i], "plain"})
			}
			gap = gap[i:]
			end := strings.IndexByte(gap, '\n')
			if end < 0 {
				runs = append(runs, run{gap, "comment"})
				break
			}
			runs = append(runs, run{gap[:end], "comment"})
			gap = gap[end:]
		}
		at = upto
	}

	for {
		tok := l.Next()
		if tok.Kind == lex.EOF {
			break
		}
		start, end := int(tok.Span.Start), int(tok.Span.End)
		// A semicolon the lexer inserted at a line break has no text of its own
		// (ADR-0040); there is nothing to draw for it.
		if end <= start || start < at {
			continue
		}
		emitTrivia(start)
		runs = append(runs, run{src[start:end], classOf(tok, src, end)})
		at = end
	}
	emitTrivia(len(src))

	// Split the runs into lines, which is what the SVG draws one <text> at a time.
	var lines [][]run
	var cur []run
	for _, r := range runs {
		for {
			i := strings.IndexByte(r.text, '\n')
			if i < 0 {
				if r.text != "" {
					cur = append(cur, r)
				}
				break
			}
			if i > 0 {
				cur = append(cur, run{r.text[:i], r.class})
			}
			lines = append(lines, cur)
			cur = nil
			r.text = r.text[i+1:]
		}
	}
	if len(cur) > 0 {
		lines = append(lines, cur)
	}
	// A trailing newline is a line terminator, not an empty last line.
	for len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func classOf(tok lex.Token, src string, end int) string {
	switch tok.Kind {
	case lex.Int, lex.Float:
		return "num"
	case lex.Str, lex.Char:
		return "str"
	case lex.Ident:
		text := src[tok.Span.Start:end]
		if c := text[0]; c >= 'A' && c <= 'Z' {
			return "type"
		}
		// A name immediately followed by `(` is being called, which is the distinction a
		// reader of a tutorial card is actually making.
		if end < len(src) && src[end] == '(' {
			return "fn"
		}
		return "plain"
	}
	if tok.Kind.IsKeyword() {
		return "keyword"
	}
	return "op"
}

func escapeXML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}
