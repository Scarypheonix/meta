package compile

import (
	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/bytecode"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/resolve"
)

// match compiles a `match` as a linear chain of arm tests.
//
// A decision tree would test no subexpression twice (spec/05-patterns.md), and Phase 4's
// IR is where that lowering belongs — it needs the control-flow graph the optimizer
// works on. What matters here is that the observable behaviour is the specified one:
// arms are tried top to bottom, a guard runs only after its pattern has fully matched,
// and testing a pattern never traps.
func (c *Compiler) match(v *ast.Match) error {
	if err := c.expr(v.Scrutinee); err != nil {
		return err
	}
	scrut := c.temp()
	c.emitA(bytecode.OpStore, scrut, v.Span())

	var toEnd []int
	for _, arm := range v.Arms {
		var fails []int
		if err := c.testPattern(arm.Pat, scrut, &fails); err != nil {
			return err
		}
		if arm.Guard != nil {
			if err := c.expr(arm.Guard); err != nil {
				return err
			}
			fails = append(fails, c.emitA(bytecode.OpJumpIfFalse, 0, arm.Guard.Span()))
		}
		if err := c.expr(arm.Body); err != nil {
			return err
		}
		toEnd = append(toEnd, c.emitA(bytecode.OpJump, 0, arm.Span()))
		for _, f := range fails {
			c.patch(f)
		}
	}

	// Unreachable: the exhaustiveness checker rejects a match that can fall through
	// (spec/05-patterns.md), so this trap exists to make the failure loud rather than
	// to be taken.
	c.emitA(bytecode.OpTrap, c.strConst("no match arm matched"), v.Span())
	for _, j := range toEnd {
		c.patch(j)
	}
	return nil
}

// testPattern emits code that tests the value in slot against a pattern, binding as it
// goes. Every way the test can fail is appended to fails as a jump to patch.
func (c *Compiler) testPattern(p ast.Pattern, slot int, fails *[]int) error {
	switch v := p.(type) {
	case nil, *ast.WildcardPat:
		return nil

	case *ast.BindPat:
		ref, _ := c.res.Ref(v.NodeID())
		switch ref.Kind {
		case resolve.Variant:
			c.emitA(bytecode.OpLoad, slot, v.Span())
			c.emitA(bytecode.OpIsVariant, c.variantIdx[ref.Variant], v.Span())
			*fails = append(*fails, c.emitA(bytecode.OpJumpIfFalse, 0, v.Span()))
			return nil
		case resolve.Const:
			c.emitA(bytecode.OpLoad, slot, v.Span())
			if err := c.expr(ref.Const.Value); err != nil {
				return err
			}
			c.emit(bytecode.OpEq, v.Span())
			*fails = append(*fails, c.emitA(bytecode.OpJumpIfFalse, 0, v.Span()))
			return nil
		}
		if l, ok := c.res.Bindings[v.NodeID()]; ok {
			c.emitA(bytecode.OpLoad, slot, v.Span())
			c.emitA(bytecode.OpStore, c.slotOf(l), v.Span())
		}
		if v.Sub != nil {
			return c.testPattern(v.Sub, slot, fails)
		}
		return nil

	case *ast.LitPat:
		c.emitA(bytecode.OpLoad, slot, v.Span())
		if err := c.literal(v); err != nil {
			return err
		}
		c.emit(bytecode.OpEq, v.Span())
		*fails = append(*fails, c.emitA(bytecode.OpJumpIfFalse, 0, v.Span()))
		return nil

	case *ast.TuplePat:
		for i, e := range v.Elems {
			sub := c.temp()
			c.emitA(bytecode.OpLoad, slot, v.Span())
			c.emitA(bytecode.OpGetTupleElem, i, v.Span())
			c.emitA(bytecode.OpStore, sub, v.Span())
			if err := c.testPattern(e, sub, fails); err != nil {
				return err
			}
		}
		return nil

	case *ast.PathPat:
		return c.testPathPattern(v, slot, fails)

	case *ast.OrPat:
		return c.testOrPattern(v, slot, fails)
	}
	return unsupported("this pattern", p.Span())
}

func (c *Compiler) testPathPattern(v *ast.PathPat, slot int, fails *[]int) error {
	ref, _ := c.res.Ref(v.NodeID())
	switch ref.Kind {
	case resolve.Variant:
		c.emitA(bytecode.OpLoad, slot, v.Span())
		c.emitA(bytecode.OpIsVariant, c.variantIdx[ref.Variant], v.Span())
		*fails = append(*fails, c.emitA(bytecode.OpJumpIfFalse, 0, v.Span()))

		for i, e := range v.Elems {
			sub := c.temp()
			c.emitA(bytecode.OpLoad, slot, v.Span())
			c.emitA(bytecode.OpGetPayload, i, v.Span())
			c.emitA(bytecode.OpStore, sub, v.Span())
			if err := c.testPattern(e, sub, fails); err != nil {
				return err
			}
		}
		for _, fp := range v.Fields {
			i := fieldIndexOfVariant(ref.Variant, fp.Name.Name)
			if i < 0 {
				return unsupported("an unknown field in a pattern", v.Span())
			}
			sub := c.temp()
			c.emitA(bytecode.OpLoad, slot, v.Span())
			c.emitA(bytecode.OpGetPayload, i, v.Span())
			c.emitA(bytecode.OpStore, sub, v.Span())
			if err := c.testPattern(fp.Pat, sub, fails); err != nil {
				return err
			}
		}
		return nil

	case resolve.Struct:
		for _, fp := range v.Fields {
			i := fieldIndexOfStruct(ref.Struct, fp.Name.Name)
			if i < 0 {
				return unsupported("an unknown field in a pattern", v.Span())
			}
			sub := c.temp()
			c.emitA(bytecode.OpLoad, slot, v.Span())
			c.emitA(bytecode.OpGetField, i, v.Span())
			c.emitA(bytecode.OpStore, sub, v.Span())
			if err := c.testPattern(fp.Pat, sub, fails); err != nil {
				return err
			}
		}
		return nil
	}
	return unsupported("this pattern", v.Span())
}

// testOrPattern tries each alternative in turn, jumping to the arm's body on the first
// that matches. Every alternative binds the same names (E0007), so whichever succeeds
// leaves the frame in the same shape.
func (c *Compiler) testOrPattern(v *ast.OrPat, slot int, fails *[]int) error {
	var matched []int
	for i, alt := range v.Alts {
		var altFails []int
		if err := c.testPattern(alt, slot, &altFails); err != nil {
			return err
		}
		if i == len(v.Alts)-1 {
			// The last alternative's failure is the whole pattern's failure.
			*fails = append(*fails, altFails...)
			break
		}
		matched = append(matched, c.emitA(bytecode.OpJump, 0, v.Span()))
		for _, f := range altFails {
			c.patch(f)
		}
	}
	for _, m := range matched {
		c.patch(m)
	}
	return nil
}

// literal pushes a literal pattern's value.
func (c *Compiler) literal(v *ast.LitPat) error {
	switch lit := v.Lit.(type) {
	case *ast.IntLit:
		val := int64(lit.Value)
		if v.Neg {
			val = -val
		}
		c.emitA(bytecode.OpConst, c.intConst(val), v.Span())
		return nil
	case *ast.FloatLit:
		return unsupported("a float literal pattern", v.Span())
	case *ast.StrLit:
		c.emitA(bytecode.OpConst, c.strConst(lit.Value), v.Span())
		return nil
	case *ast.CharLit:
		c.emitA(bytecode.OpConst, c.constant(bytecode.ConstChar, uint64(lit.Value), ""), v.Span())
		return nil
	case *ast.BoolLit:
		if lit.Value {
			c.emit(bytecode.OpTrue, v.Span())
		} else {
			c.emit(bytecode.OpFalse, v.Span())
		}
		return nil
	}
	return unsupported("this literal pattern", v.Span())
}

var _ = diag.Span{}
