package interp

import (
	"math"

	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/diag"
)

func (in *Interp) evalUnary(u *ast.Unary) (Value, ctrl) {
	v, c := in.evalExpr(u.X)
	if c.stops() {
		return Unit{}, c
	}
	switch u.Op {
	case ast.Neg:
		switch n := v.(type) {
		case Int:
			if int64(n) == math.MinInt64 {
				in.trap(u.Span(), "arithmetic overflow")
			}
			return Int(-int64(n)), normal
		case Float:
			return Float(-float64(n)), normal
		}
		in.trap(u.Span(), "cannot negate %s", TypeName(v))
	case ast.Not:
		b, ok := v.(Bool)
		if !ok {
			in.trap(u.Span(), "`!` applies to `bool`, found %s", TypeName(v))
		}
		return Bool(!bool(b)), normal
	}
	return Unit{}, normal
}

func (in *Interp) evalBinary(b *ast.Binary) (Value, ctrl) {
	// `&&` and `||` short-circuit: the right operand is not evaluated when the left
	// determines the result (spec/04-expressions.md).
	if b.Op == ast.AndAnd || b.Op == ast.OrOr {
		l, c := in.evalExpr(b.L)
		if c.stops() {
			return Unit{}, c
		}
		lb, ok := l.(Bool)
		if !ok {
			in.trap(b.L.Span(), "`%s` applies to `bool`, found %s", b.Op, TypeName(l))
		}
		if (b.Op == ast.AndAnd && !bool(lb)) || (b.Op == ast.OrOr && bool(lb)) {
			return lb, normal
		}
		r, c := in.evalExpr(b.R)
		if c.stops() {
			return Unit{}, c
		}
		rb, ok := r.(Bool)
		if !ok {
			in.trap(b.R.Span(), "`%s` applies to `bool`, found %s", b.Op, TypeName(r))
		}
		return rb, normal
	}

	// Everything else evaluates left, then right, then the operation.
	l, c := in.evalExpr(b.L)
	if c.stops() {
		return Unit{}, c
	}
	r, c := in.evalExpr(b.R)
	if c.stops() {
		return Unit{}, c
	}
	return in.applyBinary(b, l, r), normal
}

func (in *Interp) applyBinary(b *ast.Binary, l, r Value) Value {
	span := b.Span()

	switch b.Op {
	case ast.Eq, ast.Ne:
		eq, err := equal(l, r)
		if err != nil {
			in.trap(span, "%s", err.Error())
		}
		if b.Op == ast.Ne {
			return Bool(!eq)
		}
		return Bool(eq)
	}

	switch li := l.(type) {
	case Int:
		ri, ok := r.(Int)
		if !ok {
			in.trapMixed(span, b, l, r)
		}
		return in.intOp(span, b.Op, int64(li), int64(ri))
	case Float:
		rf, ok := r.(Float)
		if !ok {
			in.trapMixed(span, b, l, r)
		}
		return in.floatOp(span, b.Op, float64(li), float64(rf))
	case Char:
		rc, ok := r.(Char)
		if !ok {
			in.trapMixed(span, b, l, r)
		}
		return in.orderOp(span, b.Op, compareInt(int64(li), int64(rc)))
	case *Str:
		rs, ok := r.(*Str)
		if !ok {
			in.trapMixed(span, b, l, r)
		}
		return in.orderOp(span, b.Op, compareString(li.S, rs.S))
	}
	in.trap(span, "`%s` is not defined on %s", b.Op, TypeName(l))
	return Unit{}
}

func (in *Interp) trapMixed(span diag.Span, b *ast.Binary, l, r Value) {
	in.trap(span, "`%s` needs operands of the same type, found %s and %s", b.Op, TypeName(l), TypeName(r))
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

func compareString(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func (in *Interp) orderOp(span diag.Span, op ast.BinaryOp, cmp int) Value {
	switch op {
	case ast.Lt:
		return Bool(cmp < 0)
	case ast.Le:
		return Bool(cmp <= 0)
	case ast.Gt:
		return Bool(cmp > 0)
	case ast.Ge:
		return Bool(cmp >= 0)
	}
	in.trap(span, "`%s` is not defined here", op)
	return Unit{}
}

// intOp implements integer arithmetic with the trapping semantics of ADR-0005.
func (in *Interp) intOp(span diag.Span, op ast.BinaryOp, a, b int64) Value {
	switch op {
	case ast.Add:
		s := a + b
		if (a > 0 && b > 0 && s < 0) || (a < 0 && b < 0 && s >= 0) {
			in.trap(span, "arithmetic overflow")
		}
		return Int(s)
	case ast.Sub:
		d := a - b
		if (a >= 0 && b < 0 && d < 0) || (a < 0 && b > 0 && d >= 0) {
			in.trap(span, "arithmetic overflow")
		}
		return Int(d)
	case ast.Mul:
		if a != 0 && b != 0 {
			p := a * b
			if p/b != a || (a == math.MinInt64 && b == -1) || (b == math.MinInt64 && a == -1) {
				in.trap(span, "arithmetic overflow")
			}
			return Int(p)
		}
		return Int(0)
	case ast.Div:
		if b == 0 {
			in.trap(span, "divide by zero")
		}
		if a == math.MinInt64 && b == -1 {
			in.trap(span, "arithmetic overflow")
		}
		return Int(a / b) // Go truncates toward zero, as the specification requires
	case ast.Rem:
		if b == 0 {
			in.trap(span, "remainder by zero")
		}
		if a == math.MinInt64 && b == -1 {
			in.trap(span, "arithmetic overflow")
		}
		return Int(a % b) // Go's remainder takes the sign of the dividend
	case ast.BitAnd:
		return Int(a & b)
	case ast.BitOr:
		return Int(a | b)
	case ast.BitXor:
		return Int(a ^ b)
	case ast.Shl:
		if b < 0 || b >= 64 {
			in.trap(span, "shift amount out of range")
		}
		return Int(a << uint(b))
	case ast.Shr:
		if b < 0 || b >= 64 {
			in.trap(span, "shift amount out of range")
		}
		return Int(a >> uint(b)) // arithmetic: Phase 1 integers are signed
	}
	return in.orderOp(span, op, compareInt(a, b))
}

// floatOp implements IEEE-754 arithmetic. Floats never trap.
func (in *Interp) floatOp(span diag.Span, op ast.BinaryOp, a, b float64) Value {
	switch op {
	case ast.Add:
		return Float(a + b)
	case ast.Sub:
		return Float(a - b)
	case ast.Mul:
		return Float(a * b)
	case ast.Div:
		return Float(a / b)
	case ast.Rem:
		return Float(math.Mod(a, b))
	case ast.Lt:
		return Bool(a < b)
	case ast.Le:
		return Bool(a <= b)
	case ast.Gt:
		return Bool(a > b)
	case ast.Ge:
		return Bool(a >= b)
	}
	in.trap(span, "`%s` is not defined on floats", op)
	return Unit{}
}

// ---------------------------------------------------------------------------
// Assignment
// ---------------------------------------------------------------------------

func (in *Interp) evalAssign(a *ast.Assign) (Value, ctrl) {
	// The place's subexpressions are evaluated before the value
	// (spec/04-expressions.md).
	switch place := a.Place.(type) {
	case *ast.PathExpr:
		ref, ok := in.res.Ref(place.NodeID())
		if !ok {
			in.trap(place.Span(), "assigning to an unresolved name")
		}
		local := ref.Local
		if local == nil {
			in.trap(place.Span(), "`%s` is not a place", place.Path)
		}
		f := in.frame()
		var cur Value
		if op, compound := a.Op.BinaryOp(); compound {
			var found bool
			cur, found = f.lookup(local)
			if !found {
				in.trap(place.Span(), "`%s` is not initialized here", local.Name)
			}
			val, c := in.evalExpr(a.Value)
			if c.stops() {
				return Unit{}, c
			}
			f.vars[local] = in.applyCompound(a, op, cur, val)
			return Unit{}, normal
		}
		val, c := in.evalExpr(a.Value)
		if c.stops() {
			return Unit{}, c
		}
		f.vars[local] = val
		return Unit{}, normal

	case *ast.FieldAccess:
		recv, c := in.evalExpr(place.Recv)
		if c.stops() {
			return Unit{}, c
		}
		idx, mut, ok := mutableFieldIndex(recv, place.Name.Name)
		if !ok {
			in.trap(place.Span(), "no field `%s` on %s", place.Name.Name, TypeName(recv))
		}
		if !mut {
			// spec/08-memory-model.md: a field not declared `mut` is fixed at
			// construction. Phase 2 rejects this at compile time as E0594.
			in.trap(place.Span(), "field `%s` of `%s` is not declared `mut`",
				place.Name.Name, TypeName(recv))
		}
		var slot []Value
		switch r := recv.(type) {
		case *Struct:
			slot = r.Vals
		case *Enum:
			slot = r.Vals
		}
		if op, compound := a.Op.BinaryOp(); compound {
			val, c := in.evalExpr(a.Value)
			if c.stops() {
				return Unit{}, c
			}
			slot[idx] = in.applyCompound(a, op, slot[idx], val)
			return Unit{}, normal
		}
		val, c := in.evalExpr(a.Value)
		if c.stops() {
			return Unit{}, c
		}
		slot[idx] = val
		return Unit{}, normal
	}
	in.trap(a.Place.Span(), "this expression is not a place")
	return Unit{}, normal
}

// applyCompound performs the arithmetic half of a compound assignment, with the same
// trapping semantics as the corresponding operator.
func (in *Interp) applyCompound(a *ast.Assign, op ast.BinaryOp, cur, val Value) Value {
	synthetic := &ast.Binary{Base: ast.Base{ID: a.NodeID(), Loc: a.Loc}, Op: op}
	return in.applyBinary(synthetic, cur, val)
}

func mutableFieldIndex(recv Value, name string) (idx int, mut bool, ok bool) {
	switch r := recv.(type) {
	case *Struct:
		for i, f := range r.Def.Fields {
			if f.Name.Name == name {
				return i, f.Mut, true
			}
		}
	case *Enum:
		for i, f := range r.Variant.Fields {
			if f.Name.Name == name {
				return i, f.Mut, true
			}
		}
	}
	return 0, false, false
}

// ---------------------------------------------------------------------------
// Casts
// ---------------------------------------------------------------------------

// evalCast implements the `as` matrix from spec/04-expressions.md. The target type is
// read from the syntax, which is why casts work in Phase 1 without a type checker.
func (in *Interp) evalCast(c *ast.Cast) (Value, ctrl) {
	v, fc := in.evalExpr(c.X)
	if fc.stops() {
		return Unit{}, fc
	}
	target := typeName(c.Type)
	span := c.Span()

	switch src := v.(type) {
	case Int:
		return in.castFromInt(span, int64(src), target), normal
	case Float:
		return in.castFromFloat(span, float64(src), target), normal
	case Bool:
		if isIntType(target) {
			if src {
				return Int(1), normal
			}
			return Int(0), normal
		}
	case Char:
		if target == "u32" {
			return Int(int64(src)), normal
		}
		if isIntType(target) {
			return in.castFromInt(span, int64(src), target), normal
		}
	}
	in.trap(span, "cannot cast %s to `%s`", TypeName(v), target)
	return Unit{}, normal
}

var intWidths = map[string]struct {
	bits   uint
	signed bool
}{
	"i8": {8, true}, "i16": {16, true}, "i32": {32, true}, "i64": {64, true},
	"u8": {8, false}, "u16": {16, false}, "u32": {32, false}, "u64": {64, false},
}

func isIntType(name string) bool { _, ok := intWidths[name]; return ok }

// castFromInt truncates to the target width, which never traps: narrowing is how a
// programmer says "take the low bits" (spec/04-expressions.md).
func (in *Interp) castFromInt(span diag.Span, v int64, target string) Value {
	if w, ok := intWidths[target]; ok {
		return Int(truncate(v, w.bits, w.signed))
	}
	switch target {
	case "f32":
		return Float(float64(float32(v)))
	case "f64":
		return Float(float64(v))
	case "char":
		in.trap(span, "cannot cast an integer to `char`; use `char::from_u32`")
	}
	in.trap(span, "cannot cast an integer to `%s`", target)
	return Unit{}
}

func truncate(v int64, bits uint, signed bool) int64 {
	if bits >= 64 {
		return v
	}
	mask := int64(1)<<bits - 1
	t := v & mask
	if signed && t&(int64(1)<<(bits-1)) != 0 {
		t -= int64(1) << bits
	}
	return t
}

// castFromFloat truncates toward zero and traps when the result is not representable,
// because there is no defensible value to produce.
func (in *Interp) castFromFloat(span diag.Span, f float64, target string) Value {
	switch target {
	case "f32":
		return Float(float64(float32(f)))
	case "f64":
		return Float(f)
	}
	w, ok := intWidths[target]
	if !ok {
		in.trap(span, "cannot cast a float to `%s`", target)
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		in.trap(span, "invalid float to integer cast")
	}
	t := math.Trunc(f)
	lo, hi := intRange(w.bits, w.signed)
	if t < lo || t > hi {
		in.trap(span, "invalid float to integer cast")
	}
	return Int(int64(t))
}

func intRange(bits uint, signed bool) (lo, hi float64) {
	if signed {
		return -math.Pow(2, float64(bits-1)), math.Pow(2, float64(bits-1)) - 1
	}
	return 0, math.Pow(2, float64(bits)) - 1
}

// typeName renders a syntactic type for the cast table and for diagnostics.
func typeName(t ast.Type) string {
	switch v := t.(type) {
	case *ast.PathType:
		return v.Path.String()
	case *ast.UnitType:
		return "()"
	case *ast.SelfType:
		return "Self"
	case *ast.TupleType:
		return "tuple"
	case *ast.FnType:
		return "fn"
	}
	return "?"
}
