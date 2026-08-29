package optsnap

import (
	"testing"

	"github.com/scarypheonix/meta/internal/bytecode"
	"github.com/scarypheonix/meta/internal/opt"
)

// The snapshots record what the optimizer did; these tests record that it did anything
// at all. A pass that silently stopped firing — because a cost model changed, or because
// the front end started emitting a shape it no longer recognizes — leaves every
// end-to-end test passing, since optimization is unobservable by construction
// (ADR-0005). Only a test that looks at the generated code can catch it.

// count tallies an opcode across every function of a program.
func count(prog *bytecode.Program, op bytecode.Op) int {
	n := 0
	for _, fn := range prog.Fns {
		for _, in := range fn.Code {
			if in.Op == op {
				n++
			}
		}
	}
	return n
}

func optimized(t *testing.T, name, src string, level opt.Level) *bytecode.Program {
	t.Helper()
	prog := build(t, name, src)
	if err := opt.Run(prog, level); err != nil {
		t.Fatalf("optimizing at %v: %v", level, err)
	}
	return prog
}

// TestInliningRemovesTheCall: `double` is three instructions, well inside the budget, so
// at -O2 the call to it must be gone.
func TestInliningRemovesTheCall(t *testing.T) {
	const src = `
use std::io;

fn double(n: i64) -> i64 {
    n * 2
}

fn main() {
    io::println(double(21).to_str());
}
`
	before := count(optimized(t, "inline_effect", src, opt.O0), bytecode.OpCall)
	after := count(optimized(t, "inline_effect", src, opt.O2), bytecode.OpCall)
	if before == 0 {
		t.Fatal("the unoptimized program contains no call, so the test proves nothing")
	}
	if after >= before {
		t.Errorf("calls went from %d to %d: inlining did not fire", before, after)
	}
}

// TestEscapeAnalysisRemovesTheAllocation: the point is built, read, and forgotten, so at
// -O2 nothing needs to reach the heap.
func TestEscapeAnalysisRemovesTheAllocation(t *testing.T) {
	const src = `
use std::io;

struct Point {
    x: i64,
    y: i64,
}

fn main() {
    let p = Point { x: 3, y: 4 };
    io::println((p.x + p.y).to_str());
}
`
	before := count(optimized(t, "escape_effect", src, opt.O0), bytecode.OpStruct)
	after := count(optimized(t, "escape_effect", src, opt.O2), bytecode.OpStruct)
	if before == 0 {
		t.Fatal("the unoptimized program allocates nothing, so the test proves nothing")
	}
	if after != 0 {
		t.Errorf("allocations went from %d to %d: the object did not escape, so none should remain",
			before, after)
	}
}

// TestFoldingRemovesTheArithmetic: every operand is a literal, so the whole expression
// is one constant by -O1.
func TestFoldingRemovesTheArithmetic(t *testing.T) {
	const src = `
use std::io;

fn main() {
    io::println((2 + 3 * 4).to_str());
}
`
	before := optimized(t, "fold_effect", src, opt.O0)
	after := optimized(t, "fold_effect", src, opt.O1)
	for _, op := range []bytecode.Op{bytecode.OpAdd, bytecode.OpMul} {
		if count(before, op) == 0 {
			t.Fatalf("the unoptimized program contains no %s", op)
		}
		if n := count(after, op); n != 0 {
			t.Errorf("%d %s instructions survived folding", n, op)
		}
	}
}

// TestLoopInvariantIsHoisted: the multiply does not depend on the loop counter, so it
// must end up before the loop — one multiply executed once rather than every iteration.
func TestLoopInvariantIsHoisted(t *testing.T) {
	const src = `
use std::io;

fn total(n: i64, a: i64, b: i64) -> i64 {
    let mut acc = 0;
    let mut i = 0;
    while i < n {
        acc = acc + (a & b);
        i = i + 1;
    }
    acc
}

fn main() {
    io::println(total(3, 5, 6).to_str());
}
`
	prog := optimized(t, "licm_effect", src, opt.O2)
	fn := findFn(t, prog, "total")
	backEdge := -1
	for pc, in := range fn.Code {
		if in.Op == bytecode.OpJump && int(in.A) <= pc {
			backEdge = pc
		}
	}
	if backEdge < 0 {
		t.Fatal("the compiled loop has no back edge")
	}
	loopStart := int(fn.Code[backEdge].A)
	for pc := loopStart; pc <= backEdge; pc++ {
		if fn.Code[pc].Op == bytecode.OpAnd {
			t.Errorf("the invariant `a & b` is still inside the loop, at %d", pc)
		}
	}
	if count(prog, bytecode.OpAnd) == 0 {
		t.Error("the invariant computation disappeared entirely")
	}
}

func findFn(t *testing.T, prog *bytecode.Program, name string) *bytecode.Fn {
	t.Helper()
	for _, fn := range prog.Fns {
		if fn.Name == name {
			return fn
		}
	}
	t.Fatalf("no function named %q in the program", name)
	return nil
}
