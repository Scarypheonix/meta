package vm

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/scarypheonix/meta/internal/bytecode"
	"github.com/scarypheonix/meta/internal/compile"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/layout"
)

// equal implements `==`: structural, total and recursive (spec/04-expressions.md).
//
// It must agree with the tree-walking interpreter's version exactly, including the
// IEEE cases: NaN is equal to nothing, and 0.0 equals -0.0. That is why a float inside
// an aggregate is compared as a float and not by its bits, and why the descriptor
// records which slots hold floats.
func (v *VM) equal(a, b Value, span diag.Span) bool {
	if a.Tag == layout.TagFloat || b.Tag == layout.TagFloat {
		if a.Tag != b.Tag {
			return false
		}
		return a.Float() == b.Float()
	}
	if a.Tag != b.Tag {
		return false
	}
	switch a.Tag {
	case layout.TagUnit:
		return true
	case layout.TagInt, layout.TagBool, layout.TagChar:
		return a.N == b.N
	case layout.TagFn, layout.TagBuiltin:
		v.trap(span, "function values cannot be compared with `==`")
	case layout.TagRef:
		return v.equalObjects(a.R, b.R, span)
	}
	return false
}

func (v *VM) equalObjects(a, b layout.Ref, span diag.Span) bool {
	if a == b {
		return true
	}
	ta, tb := v.heap.TypeOf(a), v.heap.TypeOf(b)
	if ta != tb {
		return false
	}
	desc := v.prog.Types.Get(ta)
	if desc.Shape == layout.ByteArray {
		return v.heap.Bytes(a) == v.heap.Bytes(b)
	}
	if desc.Shape == layout.RefArray || desc.Shape == layout.RawArray {
		// Element-wise, and capacity-blind: what an array holds is its length's worth of
		// elements, and the room above that is not part of the value
		// (spec/13-collections.md).
		na, nb := v.heap.Get(a, 0), v.heap.Get(b, 0)
		if na != nb {
			return false
		}
		for i := uint64(0); i < na; i++ {
			if !v.equal(v.arrayElem(desc, a, i), v.arrayElem(desc, b, i), span) {
				return false
			}
		}
		return true
	}
	if desc.Kind == layout.ObjClosure {
		v.trap(span, "function values cannot be compared with `==`")
	}
	for i := range desc.Kinds {
		if !v.equal(v.readField(desc, a, i), v.readField(desc, b, i), span) {
			return false
		}
	}
	return true
}

// compareOp implements `<` and friends, which are built in for integers, floats, char
// and String only (spec/04-expressions.md).
func (v *VM) compareOp(op bytecode.Op, a, b Value, span diag.Span) bool {
	var cmp int
	switch a.Tag {
	case layout.TagInt, layout.TagChar:
		cmp = compareInt(a.Int(), b.Int())
	case layout.TagFloat:
		// IEEE comparison: any comparison with NaN is false, which the ordering below
		// reproduces because NaN is neither less than, equal to, nor greater than.
		x, y := a.Float(), b.Float()
		switch {
		case x < y:
			cmp = -1
		case x > y:
			cmp = 1
		case x == y:
			cmp = 0
		default:
			return false // NaN on either side
		}
	case layout.TagRef:
		cmp = strings.Compare(v.heap.Bytes(a.R), v.heap.Bytes(b.R))
	default:
		v.trap(span, "`%s` is not defined here", op)
	}
	switch op {
	case bytecode.OpLt:
		return cmp < 0
	case bytecode.OpLe:
		return cmp <= 0
	case bytecode.OpGt:
		return cmp > 0
	default:
		return cmp >= 0
	}
}

func compareInt(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// display renders a value the way `to_str` does. It must match the interpreter's
// rendering byte for byte, because the two are compared on the same programs.
func (v *VM) display(val Value) string {
	switch val.Tag {
	case layout.TagUnit:
		return "()"
	case layout.TagInt:
		return strconv.FormatInt(val.Int(), 10)
	case layout.TagFloat:
		return formatFloat(val.Float())
	case layout.TagBool:
		if val.Bool() {
			return "true"
		}
		return "false"
	case layout.TagChar:
		return string(rune(val.N))
	case layout.TagFn, layout.TagBuiltin:
		return "<function>"
	case layout.TagRef:
		return v.displayObject(val.R)
	}
	return "<value>"
}

func (v *VM) displayObject(r layout.Ref) string {
	desc := v.prog.Types.Get(v.heap.TypeOf(r))
	if desc.Shape == layout.ByteArray {
		return v.heap.Bytes(r)
	}
	if desc.Shape == layout.RefArray || desc.Shape == layout.RawArray {
		n := v.heap.Get(r, 0)
		parts := make([]string, 0, n)
		for i := uint64(0); i < n; i++ {
			parts = append(parts, v.display(v.arrayElem(desc, r, i)))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	}
	slots := make([]string, 0, len(desc.Kinds))
	for i := range desc.Kinds {
		slots = append(slots, v.display(v.readField(desc, r, i)))
	}

	switch desc.Kind {
	case layout.ObjTuple:
		return "(" + strings.Join(slots, ", ") + ")"
	case layout.ObjClosure:
		return "<function>"
	case layout.ObjStruct:
		var parts []string
		for i, name := range desc.FieldNames {
			if i < len(slots) {
				parts = append(parts, name+": "+slots[i])
			}
		}
		return desc.TypeName + " { " + strings.Join(parts, ", ") + " }"
	default:
		if len(slots) == 0 {
			return desc.VariantName
		}
		if len(desc.FieldNames) > 0 {
			var parts []string
			for i, name := range desc.FieldNames {
				if i < len(slots) {
					parts = append(parts, name+": "+slots[i])
				}
			}
			return desc.VariantName + " { " + strings.Join(parts, ", ") + " }"
		}
		return desc.VariantName + "(" + strings.Join(slots, ", ") + ")"
	}
}

// formatFloat matches the interpreter's rendering exactly; the specification does not
// yet fix one (docs/deferred.md, Phase 7).
func formatFloat(f float64) string {
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if !strings.ContainsAny(s, ".eE") && !strings.Contains(s, "Inf") && !strings.Contains(s, "NaN") {
		s += ".0"
	}
	return s
}

// cast implements the `as` matrix of spec/04-expressions.md. The target's width and
// signedness are packed into the operand by the compiler.
func (v *VM) cast(kind bytecode.CastKind, target int, val Value, span diag.Span) Value {
	bits := uint(target & 0xFF)
	signed := target&(1<<8) != 0

	switch kind {
	case bytecode.CastIntTrunc:
		return intVal(truncate(val.Int(), bits, signed))
	case bytecode.CastBoolToInt:
		if val.Bool() {
			return intVal(1)
		}
		return intVal(0)
	case bytecode.CastCharToInt:
		return intVal(truncate(int64(val.N), bits, signed))
	case bytecode.CastIntToFloat:
		if bits == 32 {
			return floatVal(float64(float32(val.Int())))
		}
		return floatVal(float64(val.Int()))
	case bytecode.CastFloatNarrow:
		return floatVal(float64(float32(val.Float())))
	case bytecode.CastFloatWiden:
		return floatVal(val.Float())
	case bytecode.CastFloatToInt:
		f := val.Float()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			v.trap(span, "invalid float to integer cast")
		}
		t := math.Trunc(f)
		lo, hi := intRange(bits, signed)
		if t < lo || t > hi {
			v.trap(span, "invalid float to integer cast")
		}
		return intVal(int64(t))
	}
	panic(fmt.Sprintf("vm: unimplemented cast %d", kind))
}

func truncate(v int64, bits uint, signed bool) int64 {
	if bits >= 64 || bits == 0 {
		return v
	}
	mask := int64(1)<<bits - 1
	t := v & mask
	if signed && t&(int64(1)<<(bits-1)) != 0 {
		t -= int64(1) << bits
	}
	return t
}

func intRange(bits uint, signed bool) (lo, hi float64) {
	if signed {
		return -math.Pow(2, float64(bits-1)), math.Pow(2, float64(bits-1)) - 1
	}
	return 0, math.Pow(2, float64(bits)) - 1
}

// callBuiltin implements the compiler-provided functions and methods.
func (v *VM) callBuiltin(index, argCount int, span diag.Span) {
	// The arguments leave the stack but stay reachable: temps is scanned as roots, so
	// a builtin that allocates cannot leave its own arguments pointing at nothing.
	base := len(v.temps)
	for i := 0; i < argCount; i++ {
		v.temps = append(v.temps, Value{})
	}
	for i := argCount - 1; i >= 0; i-- {
		v.temps[base+i] = v.pop()
	}
	args := v.temps[base : base+argCount]
	defer func() { v.temps = v.temps[:base] }()

	if val, handled := v.concurrencyBuiltin(index, args, span); handled {
		v.push(val)
		return
	}
	if val, handled := v.arrayBuiltin(index, args, span); handled {
		v.push(val)
		return
	}
	if val, handled := v.strBuiltin(index, args, span); handled {
		v.push(val)
		return
	}

	switch index {
	case compile.BuiltinPrint, compile.BuiltinPrintln:
		s := v.heap.Bytes(args[0].R)
		if index == compile.BuiltinPrintln {
			fmt.Fprintln(v.stdout, s)
		} else {
			fmt.Fprint(v.stdout, s)
		}
		v.push(unitVal())

	case compile.BuiltinPanic:
		v.trap(span, "%s", v.heap.Bytes(args[0].R))

	case compile.BuiltinRefEq:
		a, b := args[0], args[1]
		if a.Tag != layout.TagRef || b.Tag != layout.TagRef {
			v.trap(span, "`ref_eq` is defined only for aggregates")
		}
		v.push(boolVal(a.R == b.R))

	case compile.BuiltinCmpInt, compile.BuiltinCmpUint, compile.BuiltinCmpFloat, compile.BuiltinCmpString:
		// One builtin per receiver kind exists so that native code (which cannot ask a
		// register what it holds) knows which comparison to run; the VM's values
		// already carry their own kind and treat all four identically.
		v.push(refVal(v.ordering(args[0], args[1], span)))

	case compile.BuiltinCheckedAdd, compile.BuiltinCheckedSub, compile.BuiltinCheckedMul:
		res, ok := checkedOp(index, args[0].Int(), args[1].Int())
		if !ok {
			v.push(refVal(v.optionNone(span)))
		} else {
			v.push(refVal(v.optionSome(intVal(res), span)))
		}

	case compile.BuiltinSaturatingAdd, compile.BuiltinSaturatingSub, compile.BuiltinSaturatingMul:
		v.push(intVal(saturate(index, args[0].Int(), args[1].Int())))

	default:
		panic(fmt.Sprintf("vm: unimplemented builtin %d", index))
	}
}

func checkedOp(index int, a, b int64) (int64, bool) {
	switch index {
	case compile.BuiltinCheckedAdd:
		s := a + b
		if (a > 0 && b > 0 && s < 0) || (a < 0 && b < 0 && s >= 0) {
			return 0, false
		}
		return s, true
	case compile.BuiltinCheckedSub:
		d := a - b
		if (a >= 0 && b < 0 && d < 0) || (a < 0 && b > 0 && d >= 0) {
			return 0, false
		}
		return d, true
	default:
		if a == 0 || b == 0 {
			return 0, true
		}
		p := a * b
		if p/b != a || (a == math.MinInt64 && b == -1) || (b == math.MinInt64 && a == -1) {
			return 0, false
		}
		return p, true
	}
}

func saturate(index int, a, b int64) int64 {
	checked := map[int]int{
		compile.BuiltinSaturatingAdd: compile.BuiltinCheckedAdd,
		compile.BuiltinSaturatingSub: compile.BuiltinCheckedSub,
		compile.BuiltinSaturatingMul: compile.BuiltinCheckedMul,
	}[index]
	if r, ok := checkedOp(checked, a, b); ok {
		return r
	}
	switch index {
	case compile.BuiltinSaturatingAdd:
		if b > 0 {
			return math.MaxInt64
		}
		return math.MinInt64
	case compile.BuiltinSaturatingSub:
		if b > 0 {
			return math.MinInt64
		}
		return math.MaxInt64
	default:
		if (a > 0) == (b > 0) {
			return math.MaxInt64
		}
		return math.MinInt64
	}
}

// optionSome, optionNone and ordering build prelude values the VM needs for the
// compiler-provided methods. The compiler recorded their variant indices, so nothing is
// looked up by name at run time.
func (v *VM) optionSome(val Value, span diag.Span) layout.Ref {
	v.requirePrelude(span)
	info := v.prog.Variants[v.prog.Prelude.OptionSome]
	desc := v.prog.Types.Get(info.Type)
	r := v.alloc(info.Type, desc.Words, span)
	v.writeField(desc, r, 0, val, span)
	return r
}

func (v *VM) optionNone(span diag.Span) layout.Ref {
	v.requirePrelude(span)
	return v.alloc(v.prog.Variants[v.prog.Prelude.OptionNone].Type, 0, span)
}

func (v *VM) ordering(a, b Value, span diag.Span) layout.Ref {
	v.requirePrelude(span)
	var idx int
	switch {
	case v.compareOp(bytecode.OpLt, a, b, span):
		idx = v.prog.Prelude.Less
	case v.compareOp(bytecode.OpGt, a, b, span):
		idx = v.prog.Prelude.Greater
	default:
		idx = v.prog.Prelude.Equal
	}
	return v.alloc(v.prog.Variants[idx].Type, 0, span)
}

func (v *VM) requirePrelude(span diag.Span) {
	if !v.prog.Prelude.Found {
		v.trap(span, "the prelude does not define `Option` and `Ordering`")
	}
}
