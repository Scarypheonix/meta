package compile

import (
	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/bytecode"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/resolve"
	"github.com/scarypheonix/meta/internal/types"
)

func (c *Compiler) unary(v *ast.Unary) error {
	if err := c.expr(v.X); err != nil {
		return err
	}
	switch v.Op {
	case ast.Not:
		c.emit(bytecode.OpNot, v.Span())
	default:
		if c.isFloat(v.X) {
			c.emit(bytecode.OpNegF, v.Span())
		} else {
			c.emit(bytecode.OpNeg, v.Span())
		}
	}
	return nil
}

// intOps and floatOps map an operator to its opcode. Integer arithmetic traps on
// overflow (ADR-0005); float arithmetic never does.
var intOps = map[ast.BinaryOp]bytecode.Op{
	ast.Add: bytecode.OpAdd, ast.Sub: bytecode.OpSub, ast.Mul: bytecode.OpMul,
	ast.Div: bytecode.OpDiv, ast.Rem: bytecode.OpRem,
	ast.BitAnd: bytecode.OpAnd, ast.BitOr: bytecode.OpOr, ast.BitXor: bytecode.OpXor,
	ast.Shl: bytecode.OpShl, ast.Shr: bytecode.OpShr,
}

var floatOps = map[ast.BinaryOp]bytecode.Op{
	ast.Add: bytecode.OpAddF, ast.Sub: bytecode.OpSubF, ast.Mul: bytecode.OpMulF,
	ast.Div: bytecode.OpDivF, ast.Rem: bytecode.OpRemF,
}

var cmpOps = map[ast.BinaryOp]bytecode.Op{
	ast.Eq: bytecode.OpEq, ast.Ne: bytecode.OpNe,
	ast.Lt: bytecode.OpLt, ast.Le: bytecode.OpLe,
	ast.Gt: bytecode.OpGt, ast.Ge: bytecode.OpGe,
}

func (c *Compiler) binary(v *ast.Binary) error {
	// `&&` and `||` short-circuit, so the right operand is compiled behind a jump
	// (spec/04-expressions.md).
	if v.Op == ast.AndAnd || v.Op == ast.OrOr {
		if err := c.expr(v.L); err != nil {
			return err
		}
		var jump int
		if v.Op == ast.AndAnd {
			jump = c.emitA(bytecode.OpJumpIfFalse, 0, v.Span())
		} else {
			jump = c.emitA(bytecode.OpJumpIfTrue, 0, v.Span())
		}
		if err := c.expr(v.R); err != nil {
			return err
		}
		toEnd := c.emitA(bytecode.OpJump, 0, v.Span())
		c.patch(jump)
		if v.Op == ast.AndAnd {
			c.emit(bytecode.OpFalse, v.Span())
		} else {
			c.emit(bytecode.OpTrue, v.Span())
		}
		c.patch(toEnd)
		return nil
	}

	if err := c.expr(v.L); err != nil {
		return err
	}
	if err := c.expr(v.R); err != nil {
		return err
	}
	if op, ok := cmpOps[v.Op]; ok {
		c.emit(op, v.Span())
		return nil
	}
	if c.isFloat(v.L) {
		if op, ok := floatOps[v.Op]; ok {
			c.emit(op, v.Span())
			return nil
		}
	}
	if op, ok := intOps[v.Op]; ok {
		c.emit(op, v.Span())
		return nil
	}
	return unsupported("this operator", v.Span())
}

func (c *Compiler) assign(v *ast.Assign) error {
	switch place := v.Place.(type) {
	case *ast.PathExpr:
		ref, _ := c.res.Ref(place.NodeID())
		if ref.Local == nil {
			return unsupported("an assignment to something that is not a local", v.Span())
		}
		if op, compound := v.Op.BinaryOp(); compound {
			if err := c.loadLocal(ref.Local, place); err != nil {
				return err
			}
			if err := c.expr(v.Value); err != nil {
				return err
			}
			c.emitArith(op, c.isFloat(place), v.Span())
		} else if err := c.expr(v.Value); err != nil {
			return err
		}
		if _, isCapture := c.captures[ref.Local]; isCapture {
			return unsupported("assigning to a captured binding", v.Span())
		}
		c.emitA(bytecode.OpStore, c.slotOf(ref.Local), v.Span())
		c.emit(bytecode.OpUnit, v.Span())
		return nil

	case *ast.FieldAccess:
		t := c.typeOf(place.Recv)
		n, ok := types.AsNamed(t)
		if !ok || n.Def.Struct == nil {
			return unsupported("a field assignment on a non-struct", v.Span())
		}
		idx := fieldIndexOfStruct(n.Def.Struct, place.Name.Name)
		if idx < 0 {
			return unsupported("an unknown field", v.Span())
		}
		// The receiver is evaluated before the value (spec/04-expressions.md).
		if err := c.expr(place.Recv); err != nil {
			return err
		}
		recv := c.temp()
		c.emitA(bytecode.OpStore, recv, v.Span())

		if op, compound := v.Op.BinaryOp(); compound {
			c.emitA(bytecode.OpLoad, recv, v.Span())
			c.emitA(bytecode.OpGetField, idx, v.Span())
			if err := c.expr(v.Value); err != nil {
				return err
			}
			c.emitArith(op, c.isFloat(place), v.Span())
		} else if err := c.expr(v.Value); err != nil {
			return err
		}
		val := c.temp()
		c.emitA(bytecode.OpStore, val, v.Span())
		c.emitA(bytecode.OpLoad, recv, v.Span())
		c.emitA(bytecode.OpLoad, val, v.Span())
		c.emitA(bytecode.OpSetField, idx, v.Span())
		c.emit(bytecode.OpUnit, v.Span())
		return nil
	}
	return unsupported("this assignment target", v.Span())
}

func (c *Compiler) loadLocal(l *resolve.Local, at ast.Expr) error {
	if idx, isCapture := c.captures[l]; isCapture {
		c.emitA(bytecode.OpLoadCapture, idx, at.Span())
		return nil
	}
	c.emitA(bytecode.OpLoad, c.slotOf(l), at.Span())
	return nil
}

func (c *Compiler) emitArith(op ast.BinaryOp, isFloat bool, span diag.Span) {
	if isFloat {
		if o, ok := floatOps[op]; ok {
			c.emit(o, span)
			return
		}
	}
	if o, ok := intOps[op]; ok {
		c.emit(o, span)
	}
}

func (c *Compiler) cast(v *ast.Cast) error {
	if err := c.expr(v.X); err != nil {
		return err
	}
	from, fok := types.AsPrim(c.typeOf(v.X))
	to, tok := types.AsPrim(c.tys.ExprTypes[v.NodeID()])
	if !fok || !tok {
		return unsupported("this cast", v.Span())
	}

	switch {
	case from.Kind.IsInteger() && to.Kind.IsInteger():
		c.emitAB(bytecode.OpCast, int(bytecode.CastIntTrunc), castWidth(to.Kind), v.Span())
	case from.Kind.IsInteger() && to.Kind.IsFloat():
		c.emitAB(bytecode.OpCast, int(bytecode.CastIntToFloat), castWidth(to.Kind), v.Span())
	case from.Kind.IsFloat() && to.Kind.IsInteger():
		c.emitAB(bytecode.OpCast, int(bytecode.CastFloatToInt), castWidth(to.Kind), v.Span())
	case from.Kind.IsFloat() && to.Kind == types.F32:
		c.emitAB(bytecode.OpCast, int(bytecode.CastFloatNarrow), 32, v.Span())
	case from.Kind.IsFloat() && to.Kind == types.F64:
		c.emitAB(bytecode.OpCast, int(bytecode.CastFloatWiden), 64, v.Span())
	case from.Kind == types.Bool && to.Kind.IsInteger():
		c.emitAB(bytecode.OpCast, int(bytecode.CastBoolToInt), castWidth(to.Kind), v.Span())
	case from.Kind == types.Char && to.Kind.IsInteger():
		c.emitAB(bytecode.OpCast, int(bytecode.CastCharToInt), castWidth(to.Kind), v.Span())
	default:
		return unsupported("this cast", v.Span())
	}
	return nil
}

// castWidth packs the target's width and signedness into one operand: the width in bits,
// with bit 8 set for a signed type.
func castWidth(k types.PrimKind) int {
	w := int(k.Bits())
	if k.IsSigned() {
		w |= 1 << 8
	}
	if k.IsFloat() {
		if k == types.F32 {
			return 32
		}
		return 64
	}
	return w
}
