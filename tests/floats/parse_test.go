package floats

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/scarypheonix/meta/internal/driver"
	"github.com/scarypheonix/meta/internal/opt"
)

// Reading a decimal is the other half of spec/16-floats.md, and the harder half to be sure
// about: rendering has a shortest answer that a differential can check digit by digit,
// while parsing has one *nearest* answer whose neighbours are one bit away. The oracle is
// Go's strconv.ParseFloat, which is correctly rounded.
//
// The two halves are also each other's inverses, and the sweep below leans on that: every
// float in tests/floats' own bit-pattern corpus is printed by Go and read back by Origin,
// so a rendering Origin produces and a rendering Origin consumes are held to the same
// answer.

// decimals is what to try: the shapes that break a naive parser, the shortest rendering of
// every pattern the rendering sweep uses, and a deterministic random sample.
func decimals() []string {
	var out []string
	out = append(out,
		"0", "-0", "0.0", "1", "-1", "1.5", "0.1", "0.2", "0.3",
		"3.14159265358979", "2.718281828459045",
		// Ties, where "nearest, ties to even" is the whole answer.
		"2.5", "0.5", "1.5e0", "9007199254740993", // 2^53 + 1, not representable
		"4503599627370497", "0.49999999999999994", "0.5000000000000001",
		// Both ends of the range and just past them.
		"1.7976931348623157e308", "1.7976931348623159e308", "1e308", "1e309", "1e-308",
		"5e-324", "2.4703282292062327e-324", "2.4703282292062328e-324", "1e-323",
		"2.2250738585072014e-308", "2.2250738585072011e-308",
		// Long digit strings that decide on a digit far to the right.
		"1.00000000000000000000000000000000000000000000000001",
		"0.99999999999999999999999999999999999999999999999999",
		"123456789012345678901234567890123456789012345678901234567890",
		"0.000000000000000000000000000000000000000000000000001",
		// Exponent forms.
		"1e0", "1E5", "1e+5", "1e-5", "-1e-5", "12345e-3", "0.00012345e5",
		"1000000000000000000000e-21",
	)
	// Every named and single-bit pattern from the rendering sweep, printed by Go and read
	// back by Origin: the two halves have to agree, and this is where that is checked.
	for _, u := range patterns()[:600] {
		f := math.Float64frombits(u)
		if math.IsInf(f, 0) || math.IsNaN(f) {
			continue
		}
		out = append(out, strconv.FormatFloat(f, 'g', -1, 64))
	}
	r := rand.New(rand.NewSource(20260903))
	for i := 0; i < 1500; i++ {
		f := r.NormFloat64() * math.Pow(10, float64(r.Intn(60)-30))
		out = append(out, strconv.FormatFloat(f, 'g', -1, 64))
	}
	// Random digit strings, which is where a parser that accumulates in a float rather
	// than exactly gets the last bit wrong.
	for i := 0; i < 1500; i++ {
		digits := 1 + r.Intn(25)
		var b strings.Builder
		if r.Intn(2) == 0 {
			b.WriteByte('-')
		}
		for j := 0; j < digits; j++ {
			b.WriteByte(byte('0' + r.Intn(10)))
			if j == 0 && digits > 1 && r.Intn(2) == 0 {
				b.WriteByte('.')
			}
		}
		fmt.Fprintf(&b, "e%d", r.Intn(640)-320)
		out = append(out, b.String())
	}
	return out
}

const parseSource = `use std::io;
use std::float;

fn main() {
    match read_to_string("%s") {
        Result::Err(e) => { io::println("cannot read the inputs: \(e.to_str())"); }
        Result::Ok(text) => {
            let lines = text.split("\n");
            let mut i = 0;
            while i < lines.len() - 1 {
                match lines.at(i).parse_float() {
                    Option::Some(f) => { io::println(float::bits(f).to_str()); }
                    Option::None => { io::println("none"); }
                }
                i = i + 1;
            }
        }
    }
}
`

func TestParseFloatIsCorrectlyRounded(t *testing.T) {
	inputs := decimals()
	dir := t.TempDir()
	data := filepath.Join(dir, "decimals.txt")
	if err := os.WriteFile(data, []byte(strings.Join(inputs, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "parse.origin")
	if err := os.WriteFile(path, []byte(fmt.Sprintf(parseSource, data)), 0o644); err != nil {
		t.Fatal(err)
	}

	engines := []struct {
		name   string
		engine driver.Engine
		level  opt.Level
		n      int
	}{
		{"native-O2", driver.Native, opt.O2, len(inputs)},
		{"vm-O2", driver.VM, opt.O2, 400},
		{"interpreter", driver.Interpreter, opt.O0, 200},
	}
	for _, e := range engines {
		t.Run(e.name, func(t *testing.T) {
			// Each engine reads a prefix of the same file, so one file serves all three.
			sub := filepath.Join(dir, e.name+".txt")
			if err := os.WriteFile(sub, []byte(strings.Join(inputs[:e.n], "\n")+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(dir, e.name+".origin")
			if err := os.WriteFile(p, []byte(fmt.Sprintf(parseSource, sub)), 0o644); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			if code := driver.RunAt(p, e.engine, e.level, &stdout, &stderr); code != 0 {
				t.Fatalf("exit %d\n%s", code, stderr.String())
			}
			got := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
			if len(got) != e.n {
				t.Fatalf("printed %d lines, want %d (first: %q)", len(got), e.n, got[0])
			}
			bad := 0
			for i, in := range inputs[:e.n] {
				f, err := strconv.ParseFloat(in, 64)
				// A range error is not a rejection: Go returns the infinity or the zero
				// the value rounds to, and so does Origin. Only a syntax error is one.
				if err != nil && !errors.Is(err, strconv.ErrRange) {
					if got[i] != "none" {
						t.Errorf("%q: Go rejects it, Origin gave %s", in, got[i])
					}
					continue
				}
				want := fmt.Sprintf("%d", math.Float64bits(f))
				if got[i] != want {
					bad++
					if bad <= 10 {
						gotBits, _ := strconv.ParseUint(got[i], 10, 64)
						t.Errorf("%q: got %v (%#016x), want %v (%#016x)",
							in, math.Float64frombits(gotBits), gotBits, f, math.Float64bits(f))
					}
				}
			}
			if bad > 10 {
				t.Errorf("... and %d more differ", bad-10)
			}
		})
	}
}
