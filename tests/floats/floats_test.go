// Package floats holds the differential test for spec/16-floats.md's rendering.
//
// The rendering is Origin source in the prelude (ADR-0031), which makes it the one piece
// of this compiler whose correctness is a claim about mathematics rather than about a
// translation: `to_str` must produce the shortest decimal that reads back as the same
// float, for every float there is. Go's own strconv already does that, so it is the oracle
// here -- the same arrangement the codegen suite has with clang.
//
// The cases are bit patterns rather than literals, so that subnormals, infinities and NaN
// are reachable without going through the lexer and a failure names the exact value that
// failed. They reach the program through a file rather than through its source, because
// twelve thousand `io::println` statements is a function large enough that compiling it
// costs more than a minute -- the program below is nine lines and the sweep costs what
// running it costs.
//
// The whole sweep runs on native code, which is two orders of magnitude faster at this
// than the tree-walking interpreter and is what keeps the suite inside its five minutes.
// The other two engines run a prefix. That is not a gap: all three run the *same Origin
// source*, so the sweep tests the algorithm once; that the three engines agree on running
// it is what tests/e2e asserts, on every case that prints a float.
package floats

import (
	"bytes"
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

// want is the rendering spec/16-floats.md describes, computed with Go's shortest
// conversion. The `.0` is the specification's own rule for a result with no point and no
// exponent, which is the only place Go's 'g' and Origin part company.
func want(f float64) string {
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if !strings.ContainsAny(s, ".eE") && !strings.Contains(s, "Inf") && !strings.Contains(s, "NaN") {
		s += ".0"
	}
	return s
}

// patterns is every bit pattern worth trying, most interesting first so that a prefix is
// still a good test: the named values, a single bit at each position, both ends of every
// binary exponent, and a deterministic random sample.
func patterns() []uint64 {
	var out []uint64
	named := []float64{
		0, math.Copysign(0, -1), 1, -1, 0.5, -0.5, 1.5, 2, 10, 100, 1e5, 1e6, 999999,
		1e-4, 1e-5, 1e20, 1e21, 1e100, 1e-100, 1e308, 1e-308, 3.14159265358979,
		1.0 / 3.0, 2.0 / 3.0, 0.1, 0.2, 0.3, 0.1 + 0.2, math.MaxFloat64,
		math.SmallestNonzeroFloat64, 2.2250738585072014e-308, math.Inf(1), math.Inf(-1),
		math.NaN(), 4.9e-324, 123456.7, 12345678, 1e15, 1e16, 1e17, math.Pow(2, -25),
	}
	for _, f := range named {
		out = append(out, math.Float64bits(f))
	}
	for i := 0; i < 64; i++ {
		out = append(out, uint64(1)<<i, ^(uint64(1) << i))
	}
	// A fixed seed, so a failure is reproducible and the suite has no weather.
	r := rand.New(rand.NewSource(20260902))
	for i := 0; i < 1200; i++ {
		out = append(out, math.Float64bits(r.NormFloat64()*math.Pow(10, float64(r.Intn(40)-20))))
	}
	// Both ends of every binary exponent. A significand of 2^52+1 is odd, so its value has
	// a terminating decimal expansion that ends in a five -- which is where the shortest
	// digits are an exact tie and the rounding rule is the whole answer.
	for e := 0; e < 2047; e++ {
		hi := uint64(e) << 52
		out = append(out, hi, hi|1, hi|3, hi|0xFFFFFFFFFFFFF, hi|0xFFFFFFFFFFFFE)
	}
	for i := 0; i < 3000; i++ {
		out = append(out, r.Uint64())
	}
	return out
}

// The prefixes the slower engines run. The ordering above puts the named values, the
// single-bit patterns and the ordinary random ones first.
const (
	interpreterPrefix = 300
	vmPrefix          = 2500
)

// source is the program: read the bit patterns, render each one. Nine lines, so that what
// the sweep costs is what running it costs rather than what compiling it costs.
const source = `use std::io;
use std::float;

fn main() {
    match read_to_string("%s") {
        Result::Err(e) => { io::println("cannot read the patterns: \(e.to_str())"); }
        Result::Ok(text) => {
            let lines = text.split("\n");
            let mut i = 0;
            while i < lines.len() - 1 {
                match lines.at(i).parse_int() {
                    Option::None => { io::println("not a number: \(lines.at(i))"); }
                    Option::Some(n) => { io::println(float::from_bits(n as u64).to_str()); }
                }
                i = i + 1;
            }
        }
    }
}
`

// write lays down the program and the patterns it reads, and returns the program's path.
// The patterns are written as signed decimals because that is what `parse_int` reads; the
// program casts each back to the u64 it is.
func write(dir string, bits []uint64) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	data := filepath.Join(dir, "patterns.txt")
	var b strings.Builder
	for _, u := range bits {
		fmt.Fprintf(&b, "%d\n", int64(u))
	}
	if err := os.WriteFile(data, []byte(b.String()), 0o644); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "render.origin")
	return path, os.WriteFile(path, []byte(fmt.Sprintf(source, data)), 0o644)
}

func TestEveryFloatRendersAsItsShortestDecimal(t *testing.T) {
	all := patterns()
	dir := t.TempDir()

	engines := []struct {
		name   string
		engine driver.Engine
		level  opt.Level
		n      int
	}{
		{"native-O2", driver.Native, opt.O2, len(all)},
		{"vm-O2", driver.VM, opt.O2, vmPrefix},
		{"interpreter", driver.Interpreter, opt.O0, interpreterPrefix},
	}
	for _, e := range engines {
		t.Run(e.name, func(t *testing.T) {
			bits := all[:e.n]
			path, err := write(filepath.Join(dir, e.name), bits)
			if err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			if code := driver.RunAt(path, e.engine, e.level, &stdout, &stderr); code != 0 {
				t.Fatalf("exit %d\n%s", code, stderr.String())
			}
			got := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
			if len(got) != len(bits) {
				t.Fatalf("printed %d lines, want %d (first: %q)", len(got), len(bits), got[0])
			}
			bad := 0
			for i, u := range bits {
				w := want(math.Float64frombits(u))
				if got[i] != w {
					bad++
					if bad <= 10 {
						t.Errorf("bits %#016x (%v): got %q, want %q",
							u, math.Float64frombits(u), got[i], w)
					}
				}
			}
			if bad > 10 {
				t.Errorf("... and %d more", bad-10)
			}
		})
	}
}
