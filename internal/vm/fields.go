package vm

import (
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/layout"
)

// Fixed-shape field access (ADR-0019). A heap object's descriptor is the only source of
// truth for what each payload word holds; unlike a value still on the VM's stack, a
// field carries no runtime tag of its own to read back.

// readField reconstructs a stack Value from payload word i of object r.
func (v *VM) readField(desc *layout.Descriptor, r layout.Ref, i int) Value {
	switch desc.Kinds[i] {
	case layout.WordRef:
		return refVal(v.heap.GetRef(r, uint64(i)))
	case layout.WordFloat:
		return Value{Tag: layout.TagFloat, N: v.heap.Get(r, uint64(i))}
	case layout.WordBool:
		return Value{Tag: layout.TagBool, N: v.heap.Get(r, uint64(i))}
	case layout.WordChar:
		return Value{Tag: layout.TagChar, N: v.heap.Get(r, uint64(i))}
	case layout.WordUnit:
		return Value{Tag: layout.TagUnit}
	default: // WordInt, WordRaw
		return Value{Tag: layout.TagInt, N: v.heap.Get(r, uint64(i))}
	}
}

// writeField stores val into payload word i, boxing a bare function reference first
// when the word is reference-shaped: every WordRef word must hold a genuine heap
// object, and a path naming a top-level function evaluates to TagFn, not a Ref
// (ADR-0019).
func (v *VM) writeField(desc *layout.Descriptor, r layout.Ref, i int, val Value, span diag.Span) {
	if desc.Kinds[i] == layout.WordRef {
		val = v.boxIfFn(val, span)
		v.heap.SetRef(r, uint64(i), val.R)
		return
	}
	v.heap.Set(r, uint64(i), val.N)
}

// boxIfFn wraps a bare top-level function reference in a one-word captureless closure,
// the same shape a zero-capture lambda produces. A lambda literal always evaluates to a
// heap object even with no captures; a plain function name is cheaper, an index with no
// allocation at all -- so the two representations of the same static type (a function
// value) only need reconciling at the point one is written where the other is expected.
func (v *VM) boxIfFn(val Value, span diag.Span) Value {
	if val.Tag != layout.TagFn {
		return val
	}
	r := v.alloc(v.prog.FnBoxType, 1, span)
	v.heap.Set(r, 0, val.N)
	return refVal(r)
}
