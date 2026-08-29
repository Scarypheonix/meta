// Package opt is Origin's optimizer.
//
// It runs over the SSA IR of internal/ir, which is built from bytecode and emitted back
// to bytecode (ADR-0016). `-O0` does not run it at all, so the levels share a lowering
// and differ only by the passes below — which is what makes the identical-output
// requirement of Phase 4 an oracle rather than a smoke test.
//
// Every pass must be semantics-preserving under the language's rules, and two of those
// rules bite harder here than anywhere else:
//
//   - Integer overflow traps at every optimization level (ADR-0005). A trapping
//     operation is therefore never dead, and constant folding folds it *to the trap*
//     rather than around it.
//   - Evaluation order is fully specified (ADR-0012). Anything with an effect keeps its
//     order relative to everything else with an effect.
package opt

import (
	"fmt"

	"github.com/scarypheonix/meta/internal/bytecode"
	"github.com/scarypheonix/meta/internal/ir"
)

// Level is an optimization level.
type Level int

const (
	// O0 does not build the IR at all: the bytecode the lowering produced is what runs.
	O0 Level = iota
	// O1 runs the passes that are cheap and always profitable.
	O1
	// O2 adds the passes that cost compile time or code size.
	O2
)

// Pass is one optimization over one function.
type Pass struct {
	Name string
	// Run rewrites the function and reports whether it changed anything.
	Run func(f *ir.Func, prog *bytecode.Program) bool
	// MinLevel is the lowest level at which the pass runs.
	MinLevel Level
	// Needs names passes that must run before this one has anything to do. It exists
	// for the snapshots: unreachable-block removal only becomes relevant once folding
	// has resolved a constant branch, and copy propagation only once that dead arm has
	// been removed and taken its φ operand with it. Running either alone over freshly
	// built SSA would produce an empty diff and a snapshot that proves nothing.
	Needs []string
}

// passes is the pipeline, in order. Order matters: folding creates dead values, so
// elimination follows it; elimination exposes more folding, so the pipeline iterates.
var passes = []Pass{
	{Name: "constant-fold", Run: ConstantFold, MinLevel: O1},
	{Name: "copy-propagate", Run: CopyPropagate, MinLevel: O1, Needs: []string{"constant-fold", "unreachable-blocks"}},
	{Name: "cse", Run: CommonSubexpressions, MinLevel: O2},
	{Name: "licm", Run: LoopInvariantCodeMotion, MinLevel: O2},
	{Name: "escape-analysis", Run: EscapeAnalysis, MinLevel: O2},
	{Name: "dce", Run: DeadCodeElimination, MinLevel: O1},
	{Name: "unreachable-blocks", Run: RemoveUnreachableBlocks, MinLevel: O1, Needs: []string{"constant-fold"}},
}

// maxRounds bounds the pipeline's iteration. Every pass is monotone — it removes work or
// makes a value cheaper — so a fixed point exists; the bound is there to turn a pass
// that oscillates into a bug report rather than a hang.
const maxRounds = 8

// Run optimizes a program in place.
func Run(prog *bytecode.Program, level Level) error {
	if level == O0 {
		return nil
	}
	funcs, err := optimizeAll(prog, level)
	if err != nil {
		return err
	}
	for i, f := range funcs {
		if f == nil {
			continue
		}
		out, err := ir.Emit(f, prog.Fns[i].Name, prog.Fns[i].Span)
		if err != nil {
			return fmt.Errorf("emitting %s: %w", prog.Fns[i].Name, err)
		}
		prog.Fns[i] = out
	}
	return nil
}

// optimizeAll builds every function into SSA and runs the pipeline over all of them.
//
// Every function is built first because inlining needs the callee's IR while the caller
// is being rewritten. This is the one place the pipeline is defined, so that a dump and
// a run cannot disagree about what the optimizer did — an earlier version had the dump
// skip inlining, which made a real miscompilation invisible in the artefact meant to
// diagnose it.
func optimizeAll(prog *bytecode.Program, level Level) ([]*ir.Func, error) {
	funcs := make([]*ir.Func, len(prog.Fns))
	for i, fn := range prog.Fns {
		if len(fn.Code) == 0 {
			continue
		}
		f, err := ir.Build(fn)
		if err != nil {
			return nil, fmt.Errorf("building %s: %w", fn.Name, err)
		}
		funcs[i] = f
	}

	for i, f := range funcs {
		if f == nil {
			continue
		}
		if err := runPasses(f, prog, level); err != nil {
			return nil, fmt.Errorf("optimizing %s: %w", prog.Fns[i].Name, err)
		}
		if level >= O2 && Inline(f, i, funcs, prog, level) {
			// Inlining exposes constants across the call boundary, so the local passes
			// run again on the enlarged function.
			if err := runPasses(f, prog, level); err != nil {
				return nil, fmt.Errorf("optimizing %s after inlining: %w", prog.Fns[i].Name, err)
			}
		}
	}
	return funcs, nil
}

// BuildOnly runs the IR round trip with no passes. It exists so that the round trip
// itself can be tested: if `-O0` and a pass-free `-O1` disagree, the fault is in SSA
// construction or in emission, not in an optimization.
func BuildOnly(prog *bytecode.Program) error {
	for i, fn := range prog.Fns {
		if len(fn.Code) == 0 {
			continue
		}
		f, err := ir.Build(fn)
		if err != nil {
			return fmt.Errorf("building %s: %w", fn.Name, err)
		}
		out, err := ir.Emit(f, fn.Name, fn.Span)
		if err != nil {
			return fmt.Errorf("emitting %s: %w", fn.Name, err)
		}
		prog.Fns[i] = out
	}
	return nil
}

// DumpIR renders a program's SSA form after running the passes for a level. Snapshot
// tests compare its output, so a change to any pass shows up as a diff rather than as
// silently different code. It runs exactly the pipeline Run does.
func DumpIR(prog *bytecode.Program, level Level) (string, error) {
	if level == O0 {
		var out string
		for _, fn := range prog.Fns {
			if len(fn.Code) == 0 {
				continue
			}
			f, err := ir.Build(fn)
			if err != nil {
				return "", fmt.Errorf("building %s: %w", fn.Name, err)
			}
			out += f.Print(prog) + "\n"
		}
		return out, nil
	}

	funcs, err := optimizeAll(prog, level)
	if err != nil {
		return "", err
	}
	var out string
	for i, f := range funcs {
		if f == nil {
			continue
		}
		out += f.Print(prog) + "\n"
		_ = i
	}
	return out, nil
}

// runPasses iterates the pipeline to a fixed point.
func runPasses(f *ir.Func, prog *bytecode.Program, level Level) error {
	for round := 0; round < maxRounds; round++ {
		changed := false
		for _, p := range passes {
			if p.MinLevel > level {
				continue
			}
			if p.Run(f, prog) {
				changed = true
			}
		}
		f.RecomputeUses()
		if !changed {
			return nil
		}
	}
	return fmt.Errorf(
		"this is a compiler bug: the pipeline did not reach a fixed point in %d rounds", maxRounds)
}

// PassNames returns the pipeline's passes in order, for tests that must cover each one.
func PassNames() []string {
	out := make([]string, 0, len(passes))
	for _, p := range passes {
		out = append(out, p.Name)
	}
	return out
}

// DumpPass runs exactly one pass over a program and returns the IR before and after it.
//
// This is what gives each pass its own snapshot: the whole pipeline's output shows what
// the optimizer did, but not which pass did it, so a change in one would show up as a
// diff attributed to all of them.
func DumpPass(prog *bytecode.Program, name string) (before, after string, err error) {
	pass := findPass(name)
	if pass == nil {
		return "", "", fmt.Errorf("no pass named %q", name)
	}

	for _, fn := range prog.Fns {
		if len(fn.Code) == 0 {
			continue
		}
		f, buildErr := ir.Build(fn)
		if buildErr != nil {
			return "", "", fmt.Errorf("building %s: %w", fn.Name, buildErr)
		}
		// Prerequisites run first, so the "before" state is the one in which this pass
		// actually has work to do.
		for _, needed := range pass.Needs {
			if p := findPass(needed); p != nil {
				p.Run(f, prog)
				f.RecomputeUses()
			}
		}
		before += f.Print(prog) + "\n"
		pass.Run(f, prog)
		f.RecomputeUses()
		after += f.Print(prog) + "\n"
	}
	return before, after, nil
}

func findPass(name string) *Pass {
	for i := range passes {
		if passes[i].Name == name {
			return &passes[i]
		}
	}
	return nil
}

// Prerequisites reports the passes that must run before the named one for it to have
// anything to do.
func Prerequisites(name string) []string {
	if p := findPass(name); p != nil {
		return p.Needs
	}
	return nil
}

// DumpInline runs inlining alone, which the per-pass dump cannot do because inlining
// needs every function's IR at once rather than one at a time.
func DumpInline(prog *bytecode.Program) (before, after string, err error) {
	funcs := make([]*ir.Func, len(prog.Fns))
	for i, fn := range prog.Fns {
		if len(fn.Code) == 0 {
			continue
		}
		f, buildErr := ir.Build(fn)
		if buildErr != nil {
			return "", "", fmt.Errorf("building %s: %w", fn.Name, buildErr)
		}
		funcs[i] = f
		before += f.Print(prog) + "\n"
	}
	for i, f := range funcs {
		if f == nil {
			continue
		}
		Inline(f, i, funcs, prog, O2)
	}
	for _, f := range funcs {
		if f == nil {
			continue
		}
		after += f.Print(prog) + "\n"
	}
	return before, after, nil
}
