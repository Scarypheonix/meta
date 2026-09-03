// Package selfhost holds the differential tests for stage1 -- the compiler for Origin that
// is written in Origin, which Phase 9 exists to build.
//
// The rule is the one every other differential in this project follows: a stage1 component
// is held to the Go one it replaces, over a corpus large enough that agreement means
// something. Here the corpus is this repository's own Origin source -- the prelude, every
// end-to-end and conformance case, stage1 itself -- so the lexer is tested on exactly the
// text it will have to lex when it compiles the compiler.
package selfhost

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/driver"
	"github.com/scarypheonix/meta/internal/lex"
	"github.com/scarypheonix/meta/internal/opt"
	"github.com/scarypheonix/meta/internal/source"
	"github.com/scarypheonix/meta/internal/testutil"
)

// kindNumbers maps internal/lex's kinds to the numbers stage1/src/lex.origin gives them.
// They are written out rather than derived, so that renumbering either side is a test
// failure rather than a silent agreement about the wrong thing.
var kindNumbers = map[lex.Kind]int{
	lex.EOF: 0, lex.Ident: 1, lex.Int: 2, lex.Float: 3, lex.Str: 4, lex.Char: 5,

	lex.KwAs: 10, lex.KwBreak: 11, lex.KwConst: 12, lex.KwContinue: 13, lex.KwElse: 14,
	lex.KwEnum: 15, lex.KwFalse: 16, lex.KwFn: 17, lex.KwFor: 18, lex.KwIf: 19,
	lex.KwImpl: 20, lex.KwIn: 21, lex.KwLet: 22, lex.KwLoop: 23, lex.KwMatch: 24,
	lex.KwMut: 25, lex.KwPub: 26, lex.KwReturn: 27, lex.KwSelfValue: 28,
	lex.KwSelfType: 29, lex.KwStruct: 30, lex.KwTrait: 31, lex.KwTrue: 32,
	lex.KwType: 33, lex.KwUse: 34, lex.KwWhere: 35, lex.KwWhile: 36,

	lex.LParen: 40, lex.RParen: 41, lex.LBrace: 42, lex.RBrace: 43, lex.LBracket: 44,
	lex.RBracket: 45, lex.Comma: 46, lex.Semi: 47, lex.Colon: 48, lex.ColonColon: 49,
	lex.Dot: 50, lex.DotDot: 51, lex.Arrow: 52, lex.FatArrow: 53, lex.Underscore: 54,
	lex.At: 55,

	lex.Plus: 60, lex.Minus: 61, lex.Star: 62, lex.Slash: 63, lex.Percent: 64,
	lex.Amp: 65, lex.Pipe: 66, lex.Caret: 67, lex.Bang: 68, lex.Shl: 69, lex.Shr: 70,

	lex.Assign: 75, lex.PlusEq: 76, lex.MinusEq: 77, lex.StarEq: 78, lex.SlashEq: 79,
	lex.PercentEq: 80,

	lex.EqEq: 85, lex.BangEq: 86, lex.Lt: 87, lex.Le: 88, lex.Gt: 89, lex.Ge: 90,
	lex.AmpAmp: 91, lex.PipePipe: 92, lex.Question: 93,
}

// escape makes a token's text safe to put on one line of the dump. It is the same
// transformation stage1's dumper applies, spelled twice on purpose: a shared helper would
// be one place for both sides to be wrong together.
func escape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '|':
			b.WriteString(`\p`)
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// dumpGo renders one file's tokens the way stage1's dumper does.
func dumpGo(f *source.File) []string {
	bag := diag.New()
	var out []string
	var walk func(depth int, toks []lex.Token)
	walk = func(depth int, toks []lex.Token) {
		for _, t := range toks {
			n, ok := kindNumbers[t.Kind]
			if !ok {
				n = -1
			}
			interp := 0
			if t.Parts != nil {
				interp = 1
			}
			overflow := 0
			if t.IntOverflow {
				overflow = 1
			}
			out = append(out, fmt.Sprintf("%d %d %d %d %d %d %d %d %d |%s|%s|%s",
				depth, n, t.Span.Start, t.Span.End, t.Int, overflow, int(t.Char),
				interp, len(t.Parts),
				escape(t.Text), escape(t.Str), t.Suffix))
			for _, p := range t.Parts {
				if p.Expr == nil {
					out = append(out, fmt.Sprintf("%d text |%s|", depth+1, escape(p.Text)))
					continue
				}
				out = append(out, fmt.Sprintf("%d expr %d", depth+1, len(p.Expr)))
				walk(depth+1, p.Expr)
			}
		}
	}
	walk(0, lex.Tokens(f, bag))
	return out
}

// corpus is every .origin file in the repository, sorted, with the ones the two lexers are
// known to disagree about left out.
func corpus(t *testing.T) []string {
	t.Helper()
	root := testutil.RepoRoot(t)
	var files []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "site" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, ".origin") {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	if len(files) < 50 {
		t.Fatalf("found only %d .origin files; the corpus walk is wrong", len(files))
	}
	return files
}

// driverSource is the stage1 dumper: read the manifest, lex each file it names, print the
// tokens. One program for the whole corpus, so the lexer is compiled once.
const driverSource = `use std::io;
use std::list;
use lex;

fn escape(s: String) -> String {
    let mut out = "";
    let mut i = 0;
    while i < s.len() {
        // Byte by byte would trap: a slice refuses an index that would split a character
        // (spec/14-strings.md). Every byte this replaces is ASCII, so stepping a whole
        // character at a time reaches all of them and copies the rest through untouched.
        let w = s.char_width(i);
        if w > 1 {
            out = out.concat(s.slice(i, i + w));
            i = i + w;
        } else {
        let b = s.byte_at(i);
        if b == 92 {
            out = out.concat("\\\\");
        } else {
            if b == 10 {
                out = out.concat("\\n");
            } else {
                if b == 13 {
                    out = out.concat("\\r");
                } else {
                    if b == 9 {
                        out = out.concat("\\t");
                    } else {
                        if b == 124 {
                            out = out.concat("\\p");
                        } else {
                            out = out.concat(s.slice(i, i + 1));
                        }
                    }
                }
            }
        }
        i = i + 1;
        }
    }
    out
}

fn flag(b: bool) -> i64 {
    if b { 1 } else { 0 }
}

fn dump(depth: i64, toks: List[lex::Token]) {
    let mut i = 0;
    while i < toks.len() {
        let t = toks.at(i);
        io::println("\(depth) \(t.kind) \(t.start) \(t.end) \(t.int) \(flag(t.overflow)) \(t.ch) \(flag(t.interpolated)) \(t.parts.len()) |\(escape(t.text))|\(escape(t.str))|\(t.suffix)");
        let mut j = 0;
        while j < t.parts.len() {
            match t.parts.at(j) {
                lex::Part::Text(s) => { io::println("\(depth + 1) text |\(escape(s))|"); }
                lex::Part::Expr(inner) => {
                    io::println("\(depth + 1) expr \(inner.len())");
                    dump(depth + 1, inner);
                }
            }
            j = j + 1;
        }
        i = i + 1;
    }
}

fn main() {
    match read_to_string("%s") {
        Result::Err(e) => { io::println("cannot read the manifest: \(e.to_str())"); }
        Result::Ok(manifest) => {
            let paths = manifest.split("\n");
            let mut i = 0;
            while i < paths.len() {
                let path = paths.at(i);
                if !path.is_empty() {
                    match read_to_string(path) {
                        Result::Err(e) => { io::println("cannot read \(path): \(e.to_str())"); }
                        Result::Ok(src) => {
                            io::println("== \(path)");
                            dump(0, lex::lex(src).tokens);
                        }
                    }
                }
                i = i + 1;
            }
        }
    }
}
`

// writePackage lays out the stage1 lexer with a generated driver beside it.
func writePackage(t *testing.T, dir string, files []string) {
	t.Helper()
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	lexSrc, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), "stage1", "src", "lex.origin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "lex.origin"), lexSrc, 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(dir, "manifest.txt")
	if err := os.WriteFile(manifest, []byte(strings.Join(files, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "main.origin"),
		[]byte(fmt.Sprintf(driverSource, manifest)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestStage1LexerMatchesTheGoLexer runs both over the repository's own Origin source.
func TestStage1LexerMatchesTheGoLexer(t *testing.T) {
	files := corpus(t)
	dir := t.TempDir()
	writePackage(t, dir, files)

	var want []string
	for _, path := range files {
		want = append(want, "== "+path)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, dumpGo(source.NewFile(path, string(data)))...)
	}

	// Every engine, because the lexer is Origin: it has to lex the same on all three, and
	// this is the largest Origin program in the project by a wide margin.
	engines := []struct {
		name   string
		engine driver.Engine
		level  opt.Level
	}{
		{"native-O2", driver.Native, opt.O2},
		{"native-O0", driver.Native, opt.O0},
		{"vm-O2", driver.VM, opt.O2},
		{"interpreter", driver.Interpreter, opt.O0},
	}
	for _, e := range engines {
		t.Run(e.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := driver.RunAt(dir, e.engine, e.level, &stdout, &stderr); code != 0 {
				t.Fatalf("exit %d\n%s", code, stderr.String())
			}
			got := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
			if len(got) != len(want) {
				t.Errorf("stage1 printed %d lines, the Go lexer %d", len(got), len(want))
			}
			file := ""
			bad := 0
			for i := 0; i < len(got) && i < len(want); i++ {
				if strings.HasPrefix(want[i], "== ") {
					file = want[i][3:]
				}
				if got[i] != want[i] {
					bad++
					if bad <= 8 {
						t.Errorf("%s, dump line %d:\n  stage1: %s\n  go:     %s", file, i, got[i], want[i])
					}
				}
			}
			if bad > 8 {
				t.Errorf("... and %d more lines differ", bad-8)
			}
			if bad == 0 && len(got) == len(want) {
				t.Logf("%d files, %d dump lines identical", len(files), len(want))
			}
		})
	}
}
