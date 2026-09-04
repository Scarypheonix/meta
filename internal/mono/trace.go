package mono

import (
	"fmt"

	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/check"
	"github.com/scarypheonix/meta/internal/diag"
)

// The monomorphization trace: one line per event, in the order the walk produces them.
//
// It exists so that a second monomorphizer can be held to this one, and it is the same
// oracle internal/resolve and internal/check carry, for a reason that has not changed:
// the answer is a graph keyed by ast.NodeID, and stage1's syntax tree has no node ids, so
// the graph cannot be compared but the sequence that builds it can.
//
// What the sequence has to pin is not only which instances exist but which one each call
// site reaches. An instance is numbered when it is created -- its position in
// Result.Instances -- and a call names that number rather than the instance's name,
// because a name is not an identity: two declarations in different modules may share one,
// and `foo` the non-generic function and `foo[i64]` are told apart by their arguments but
// two copies of `foo` at the same argument are the same copy or the walk is wrong.

// noteInstance records an instance the moment it is created, which is what gives it its
// number.
func (w *walker) noteInstance(inst *Instance) {
	if w.trace == nil {
		return
	}
	*w.trace = append(*w.trace, fmt.Sprintf("instance %d %s %s",
		inst.index, inst.Name, spanText(inst.Decl.Name.Loc)))
}

// noteBody records that an instance's body is being walked. It is not redundant with the
// instance line: instances are created eagerly and their bodies walked from a queue, so
// the two orders differ, and where they differ is exactly where a second implementation
// can produce the right set of copies in the wrong order.
func (w *walker) noteBody(inst *Instance) {
	if w.trace == nil {
		return
	}
	*w.trace = append(*w.trace, fmt.Sprintf("body %d", inst.index))
}

// noteCall records one call site's target, by the offset the call was written at and the
// number of the copy it reaches. `what` tells the loop's two implicit calls apart: `call`
// is every call site including `for`'s `next`, `iter` is `for`'s `into_iter` (Result
// explains why they cannot share a table).
func (w *walker) noteCall(what string, span diag.Span, target *Instance) {
	if w.trace == nil {
		return
	}
	*w.trace = append(*w.trace, fmt.Sprintf("%s %d %d", what, span.Start, target.index))
}

// noteEntry records which copy the program starts at, which is the one thing about the
// result that is not a consequence of the walk order.
func (w *walker) noteEntry() {
	if w.trace == nil {
		return
	}
	if w.out.Entry == nil {
		*w.trace = append(*w.trace, "entry none")
		return
	}
	*w.trace = append(*w.trace, fmt.Sprintf("entry %d", w.out.Entry.index))
}

func spanText(s diag.Span) string {
	if s.File == nil {
		return "?:0"
	}
	return fmt.Sprintf("%s:%d", s.File.Name, s.Start)
}

// Trace computes the instantiation set and returns the trace beside it.
func Trace(bag *diag.Bag, tys *check.Result, files ...*ast.File) (*Result, []string) {
	return program(bag, tys, true, files...)
}
