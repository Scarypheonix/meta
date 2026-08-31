// Package debuginfo checks the native backend's debug information against the tools that
// actually consume it, rather than only against Go's own readers.
//
// internal/dwarf and internal/obj verify their own encoding by parsing it back with
// debug/dwarf and debug/macho, which proves the bytes are well formed. This proves
// something else: that a debugger resolves a source line in them to the address the
// compiler says it should, which is the acceptance criterion spec/11-codegen.md actually
// states. `lldb` and `llvm-dwarfdump` both read ELF and Mach-O cross-platform, so both
// formats are checked here even though only one of them can be executed (ADR-0003).
//
// Every test skips when the tool it needs is absent: these are external dependencies,
// and `./check` must pass without them.
package debuginfo

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// programs are a few shapes worth covering: a trivial one, recursion, a loop with a
// desugared iterator, and a closure. Each exercises a different amount of line-table
// churn, which is where an off-by-one in the dedupe or a stale per-function reset shows.
var programs = map[string]string{
	"trivial": `
use std::io;

fn add(a: i64, b: i64) -> i64 {
    let result = a + b;
    result
}

fn main() {
    let sum = add(1, 2);
    io::println(sum.to_str());
}
`,
	"recursion": `
use std::io;

fn fact(n: i64) -> i64 {
    if n <= 1 {
        1
    } else {
        n * fact(n - 1)
    }
}

fn main() {
    io::println(fact(5).to_str());
}
`,
	"loop_and_closure": `
use std::io;

fn make_adder(base: i64) -> fn(i64) -> i64 {
    |x: i64| -> i64 { base + x }
}

fn main() {
    let add10 = make_adder(10);
    let mut total = 0;
    let mut i = 0;
    while i < 5 {
        total = total + add10(i);
        i = i + 1;
    }
    io::println(total.to_str());
}
`,
}

// build compiles src to a native executable for one target, through the real compiler.
func build(t *testing.T, dir, name, src, target string) string {
	t.Helper()
	srcPath := filepath.Join(dir, name+".origin")
	if err := os.WriteFile(srcPath, []byte(src), 0o644); err != nil {
		t.Fatalf("writing %s: %v", srcPath, err)
	}
	out := filepath.Join(dir, name+"-"+target)
	cmd := exec.Command("go", "run", "../../cmd/originc", "build", "-o", out, "--target", target, srcPath)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("originc build --target %s: %v\n%s", target, err, b)
	}
	return out
}

func haveTool(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s is not installed; this check needs it", name)
	}
}

// lineRows maps a source line to every machine-code address the compiler's own line table
// gives it, read back out of the binary with llvm-dwarfdump.
func lineRows(t *testing.T, binary string) map[int][]uint64 {
	t.Helper()
	out, err := exec.Command("llvm-dwarfdump", "--debug-line", binary).Output()
	if err != nil {
		t.Fatalf("llvm-dwarfdump --debug-line: %v", err)
	}
	rows := map[int][]uint64{}
	for _, ln := range strings.Split(string(out), "\n") {
		f := strings.Fields(ln)
		if len(f) < 3 || !strings.HasPrefix(f[0], "0x") {
			continue
		}
		addr, err := strconv.ParseUint(f[0], 0, 64)
		if err != nil || addr == 0 {
			continue
		}
		line, err := strconv.Atoi(f[1])
		if err != nil {
			continue
		}
		rows[line] = append(rows[line], addr)
	}
	return rows
}

// TestDWARFPassesLLVMsOwnVerifier runs the validity checker LLVM ships, which is a far
// stricter reader than "debug/dwarf parsed it without erroring".
func TestDWARFPassesLLVMsOwnVerifier(t *testing.T) {
	haveTool(t, "llvm-dwarfdump")
	dir := t.TempDir()
	for name, src := range programs {
		for _, target := range []string{"linux", "macos"} {
			binary := build(t, dir, name, src, target)
			out, err := exec.Command("llvm-dwarfdump", "--verify", binary).CombinedOutput()
			if err != nil {
				t.Errorf("%s/%s: llvm-dwarfdump --verify failed: %v\n%s", name, target, err, out)
			}
		}
	}
}

var addrRe = regexp.MustCompile(`address = (0x[0-9a-f]+)`)

// TestLLDBResolvesEverySourceLineToItsOwnAddress is spec/11-codegen.md's worked example,
// generalized: for every line the compiler put in its line table, `breakpoint set --file
// f --line N` must resolve, and must resolve to an address the compiler itself associates
// with that line. A line resolving to the wrong address is a breakpoint that stops in the
// wrong place, which no amount of structural validity would catch.
//
// This runs against Mach-O as well as ELF. Resolving a breakpoint is a static DWARF
// lookup, so lldb does it cross-platform for the format it cannot execute — leaving only
// the live process (stopping there, and `bt` at run time) for the target machine.
func TestLLDBResolvesEverySourceLineToItsOwnAddress(t *testing.T) {
	haveTool(t, "lldb")
	haveTool(t, "llvm-dwarfdump")

	dir := t.TempDir()
	for name, src := range programs {
		for _, target := range []string{"linux", "macos"} {
			binary := build(t, dir, name, src, target)
			rows := lineRows(t, binary)
			if len(rows) == 0 {
				t.Errorf("%s/%s: the binary has no line rows at all", name, target)
				continue
			}

			lines := make([]int, 0, len(rows))
			for l := range rows {
				lines = append(lines, l)
			}
			// A stable order, so lldb's answers pair with the lines that asked for them.
			for i := range lines {
				for j := i + 1; j < len(lines); j++ {
					if lines[j] < lines[i] {
						lines[i], lines[j] = lines[j], lines[i]
					}
				}
			}

			args := []string{"-b"}
			for _, l := range lines {
				args = append(args, "-o",
					"breakpoint set --file "+name+".origin --line "+strconv.Itoa(l))
			}
			out, err := exec.Command("lldb", append(args, binary)...).CombinedOutput()
			if err != nil {
				t.Fatalf("%s/%s: lldb: %v\n%s", name, target, err, out)
			}

			got := addrRe.FindAllStringSubmatch(string(out), -1)
			if len(got) != len(lines) {
				t.Errorf("%s/%s: lldb resolved %d of %d lines\n%s",
					name, target, len(got), len(lines), out)
				continue
			}
			for i, l := range lines {
				addr, err := strconv.ParseUint(got[i][1], 0, 64)
				if err != nil {
					t.Errorf("%s/%s: unparseable address %q", name, target, got[i][1])
					continue
				}
				ok := false
				for _, want := range rows[l] {
					if addr == want {
						ok = true
						break
					}
				}
				if !ok {
					t.Errorf("%s/%s: lldb resolves line %d to %#x, but the line table gives it %#x",
						name, target, l, addr, rows[l])
				}
			}
		}
	}
}

// TestLLDBNamesEveryFunction is the other half of the criterion: `bt` has to name the
// Origin function a frame is in, which comes from the symbol table rather than from any
// DWARF subprogram DIE (ADR-0023).
func TestLLDBNamesEveryFunction(t *testing.T) {
	haveTool(t, "lldb")
	dir := t.TempDir()
	for _, target := range []string{"linux", "macos"} {
		binary := build(t, dir, "trivial", programs["trivial"], target)
		out, err := exec.Command("lldb", "-b",
			"-o", "image lookup -n add",
			"-o", "image lookup -n main",
			binary).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: lldb: %v\n%s", target, err, out)
		}
		for _, want := range []string{"add", "main"} {
			if !strings.Contains(string(out), "`"+want) {
				t.Errorf("%s: lldb could not find a symbol named %q; `bt` would not name that frame\n%s",
					target, want, out)
			}
		}
	}
}
