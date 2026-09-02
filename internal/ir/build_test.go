package ir

import (
	"strings"
	"testing"

	"github.com/scarypheonix/meta/internal/bytecode"
	"github.com/scarypheonix/meta/internal/diag"
)

// asm is a tiny assembler for the tests: it keeps the bytecode readable without
// hand-counting jump targets, which is the part that goes wrong when a case is edited.
type asm struct {
	code   []bytecode.Instr
	labels map[string]int
	fixups []fixup
}

type fixup struct {
	at    int
	label string
}

func newAsm() *asm { return &asm{labels: map[string]int{}} }

func (a *asm) op(op bytecode.Op, args ...int32) *asm {
	in := bytecode.Instr{Op: op}
	if len(args) > 0 {
		in.A = args[0]
	}
	if len(args) > 1 {
		in.B = args[1]
	}
	a.code = append(a.code, in)
	return a
}

// label marks the next instruction's position.
func (a *asm) label(name string) *asm {
	a.labels[name] = len(a.code)
	return a
}

// jump emits a jump-like instruction whose target is patched once the label is known.
func (a *asm) jump(op bytecode.Op, label string) *asm {
	a.fixups = append(a.fixups, fixup{at: len(a.code), label: label})
	return a.op(op, 0)
}

func (a *asm) fn(t *testing.T, name string, params, locals int) *bytecode.Fn {
	t.Helper()
	for _, f := range a.fixups {
		target, ok := a.labels[f.label]
		if !ok {
			t.Fatalf("undefined label %q", f.label)
		}
		a.code[f.at].A = int32(target)
	}
	return &bytecode.Fn{Name: name, Params: params, Locals: locals, Code: a.code}
}

// sumLoop is `fn(n) { let mut i = 0; let mut acc = 0; while i < n { acc = acc + i; i = i + 1 }; acc }`
// in bytecode. Slot 0 is n, 1 is i, 2 is acc; const 0 is the integer 0 and const 1 is 1.
func sumLoop(t *testing.T) *bytecode.Fn {
	t.Helper()
	a := newAsm()
	a.op(bytecode.OpConst, 0).op(bytecode.OpStore, 1)
	a.op(bytecode.OpConst, 0).op(bytecode.OpStore, 2)
	a.label("head")
	a.op(bytecode.OpLoad, 1).op(bytecode.OpLoad, 0).op(bytecode.OpLt)
	a.jump(bytecode.OpJumpIfFalse, "exit")
	a.op(bytecode.OpLoad, 2).op(bytecode.OpLoad, 1).op(bytecode.OpAdd).op(bytecode.OpStore, 2)
	a.op(bytecode.OpLoad, 1).op(bytecode.OpConst, 1).op(bytecode.OpAdd).op(bytecode.OpStore, 1)
	a.jump(bytecode.OpJump, "head")
	a.label("exit")
	a.op(bytecode.OpLoad, 2).op(bytecode.OpReturn)
	return a.fn(t, "sum", 1, 3)
}

// diamond is `fn(c) { if c { 1 } else { 2 } }`: slot 0 is the condition, slot 1 the result.
func diamond(t *testing.T) *bytecode.Fn {
	t.Helper()
	a := newAsm()
	a.op(bytecode.OpLoad, 0)
	a.jump(bytecode.OpJumpIfFalse, "else")
	a.op(bytecode.OpConst, 0).op(bytecode.OpStore, 1)
	a.jump(bytecode.OpJump, "join")
	a.label("else")
	a.op(bytecode.OpConst, 1).op(bytecode.OpStore, 1)
	a.label("join")
	a.op(bytecode.OpLoad, 1).op(bytecode.OpReturn)
	return a.fn(t, "pick", 1, 2)
}

func mustBuild(t *testing.T, fn *bytecode.Fn) *Func {
	t.Helper()
	f, err := Build(fn)
	if err != nil {
		t.Fatalf("building %s: %v", fn.Name, err)
	}
	verify(t, f)
	return f
}

// verify checks the structural invariants the whole optimizer relies on: every use names
// a value some block defines, every block ends in a terminator, and a φ has exactly one
// operand per predecessor.
func verify(t *testing.T, f *Func) {
	t.Helper()
	defined := map[*Value]bool{}
	for _, b := range f.Blocks {
		for _, v := range append(append([]*Value{}, b.Phis...), b.Instr...) {
			if defined[v] {
				t.Errorf("%s is defined twice", v)
			}
			defined[v] = true
			if v.Block != b {
				t.Errorf("%s is in %s but claims %v", v, b, v.Block)
			}
		}
	}
	for _, b := range f.Blocks {
		if b.Term == nil {
			t.Errorf("%s has no terminator", b)
		}
		for _, phi := range b.Phis {
			if len(phi.Args) != len(b.Preds) {
				t.Errorf("%s in %s has %d operands for %d predecessors",
					phi, b, len(phi.Args), len(b.Preds))
			}
		}
		values := append(append([]*Value{}, b.Phis...), b.Instr...)
		if b.Term != nil {
			values = append(values, b.Term)
		}
		for _, v := range values {
			for i, a := range v.Args {
				if a == nil {
					t.Errorf("%s in %s has a nil operand at %d", v, b, i)
					continue
				}
				if !defined[a] {
					t.Errorf("%s in %s uses %s, which no block defines", v, b, a)
				}
			}
		}
	}
}

func TestBuildDiamondMergesWithAPhi(t *testing.T) {
	f := mustBuild(t, diamond(t))

	var phis int
	f.Values(func(v *Value) {
		if v.Op == OpPhi {
			phis++
			if len(v.Args) != 2 {
				t.Errorf("the join φ has %d operands, want 2", len(v.Args))
			}
		}
	})
	if phis != 1 {
		t.Errorf("built %d φs, want exactly one for the join", phis)
	}
	if len(f.Blocks) != 4 {
		t.Errorf("built %d blocks, want 4 (entry, then, else, join)", len(f.Blocks))
	}
}

func TestBuildLoopHasABackEdge(t *testing.T) {
	f := mustBuild(t, sumLoop(t))

	d := ComputeDominators(f)
	loops := Loops(f, d)
	if len(loops) != 1 {
		t.Fatalf("found %d loops, want 1", len(loops))
	}
	loop := loops[0]
	if len(loop.Header.Phis) != 2 {
		t.Errorf("the loop header has %d φs, want 2 (the counter and the accumulator)",
			len(loop.Header.Phis))
	}
	if loop.Preheader == nil {
		t.Error("the loop has no preheader, so nothing can be hoisted out of it")
	}
	if !loop.Blocks[loop.Header] {
		t.Error("the loop's block set does not contain its own header")
	}
}

// TestBuildIsDeterministic is a regression test. Sealing a block completes its φs by
// reading their variable in every predecessor, which can create further φs; iterating
// the pending set in map order therefore made value numbering differ between runs, and
// every IR snapshot flaky with it.
func TestBuildIsDeterministic(t *testing.T) {
	for _, mk := range []func(*testing.T) *bytecode.Fn{sumLoop, diamond} {
		first := mustBuild(t, mk(t)).Print(nil)
		for i := 0; i < 200; i++ {
			if got := mustBuild(t, mk(t)).Print(nil); got != first {
				t.Fatalf("build %d differs:\n--- first ---\n%s--- got ---\n%s", i, first, got)
			}
		}
	}
}

// TestBuildRejectsInconsistentStackDepth guards the assumption the translation rests on:
// that the operand-stack depth at each instruction is the same on every path to it.
func TestBuildRejectsInconsistentStackDepth(t *testing.T) {
	a := newAsm()
	a.op(bytecode.OpLoad, 0)
	a.jump(bytecode.OpJumpIfFalse, "join")
	a.op(bytecode.OpConst, 0) // one path leaves a value behind, the other does not
	a.label("join")
	a.op(bytecode.OpUnit).op(bytecode.OpReturn)
	_, err := Build(a.fn(t, "ragged", 1, 1))
	if err == nil {
		t.Fatal("built a function whose stack depth disagrees between paths")
	}
	if !strings.Contains(err.Error(), "depth") {
		t.Errorf("error does not name the problem: %v", err)
	}
}

func TestEmitRoundTripsBlockStructure(t *testing.T) {
	for _, mk := range []func(*testing.T) *bytecode.Fn{sumLoop, diamond} {
		src := mk(t)
		f := mustBuild(t, src)
		out, err := Emit(f, src.Name, diag.Span{})
		if err != nil {
			t.Fatalf("emitting %s: %v", src.Name, err)
		}
		if out.Params != src.Params {
			t.Errorf("%s: emitted %d params, want %d", src.Name, out.Params, src.Params)
		}
		if len(out.Code) == 0 {
			t.Fatalf("%s: emitted no code", src.Name)
		}
		// Rebuilding what was emitted must work: the emitter's output is ordinary
		// bytecode, and the pipeline runs the pair repeatedly.
		again, err := Build(out)
		if err != nil {
			t.Fatalf("rebuilding emitted %s: %v", src.Name, err)
		}
		verify(t, again)
	}
}

// TestRoundTripPreservesOperands is a regression test, and the general form of a bug
// that hid for a whole phase.
//
// A comparison and `to_str` carry the static kind of their operand (bytecode.Kind),
// because native code has no runtime tag to ask. Emission dropped that field, so the
// bytecode that came back out of the optimizer was subtly not the bytecode that went in.
// Nothing noticed: the virtual machine reads a tag off the value and never looks at the
// field, so every existing test passed while compiled code was being handed a kind of
// zero.
func TestRoundTripPreservesOperands(t *testing.T) {
	a := newAsm()
	a.op(bytecode.OpLoad, 0)
	a.op(bytecode.OpConst, 0)
	a.op(bytecode.OpLt, int32(bytecode.KindU64))
	a.op(bytecode.OpLoad, 0)
	a.op(bytecode.OpToStr, int32(bytecode.KindI64))
	a.op(bytecode.OpPop)
	a.op(bytecode.OpReturn)
	src := a.fn(t, "kinds", 1, 1)

	f := mustBuild(t, src)
	out, err := Emit(f, src.Name, diag.Span{})
	if err != nil {
		t.Fatalf("emitting: %v", err)
	}

	want := map[bytecode.Op]int32{
		bytecode.OpLt:    int32(bytecode.KindU64),
		bytecode.OpToStr: int32(bytecode.KindI64),
	}
	seen := map[bytecode.Op]bool{}
	for _, in := range out.Code {
		w, interesting := want[in.Op]
		if !interesting {
			continue
		}
		seen[in.Op] = true
		if in.A != w {
			t.Errorf("%s came back with operand %d, want %d", in.Op, in.A, w)
		}
	}
	for op := range want {
		if !seen[op] {
			t.Errorf("%s did not survive the round trip at all", op)
		}
	}
}
