package compile

import (
	"fmt"

	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/bytecode"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/mono"
	"github.com/scarypheonix/meta/internal/resolve"
	"github.com/scarypheonix/meta/internal/types"
)

// compileInstance lowers one monomorphized instance's body.
//
// A non-generic function has exactly one instance and this is exactly the old
// per-declaration compilation. A generic one is compiled once per instantiation mono
// found, because the calls inside its body can differ: `a.cmp(b)` inside `max2[T: Ord]`
// is `Money::cmp` in one instance and the builtin integer comparison in another
// (ADR-0010), and only the concrete instance knows which.
func (c *Compiler) compileInstance(inst *mono.Instance) error {
	decl := inst.Decl
	if decl.Body == nil {
		return nil
	}
	idx := c.instIndex[inst]
	saved, savedInst, savedSlots, savedNext, savedCaps, savedLoops :=
		c.fn, c.inst, c.slots, c.nextSlot, c.captures, c.loops
	c.fn = c.prog.Fns[idx]
	c.inst = inst
	c.slots = map[*resolve.Local]int{}
	c.captures = nil
	c.nextSlot = 0
	c.loops = nil

	// Slot 0 is the receiver for a method, then the declared parameters in order.
	if decl.Self != nil {
		c.nextSlot++
		c.fn.Locals = c.nextSlot
	}
	for _, p := range decl.Params {
		c.bindParam(p.Pat)
	}

	if err := c.block(decl.Body); err != nil {
		return err
	}
	c.emit(bytecode.OpReturn, c.fn.Span)

	c.fn, c.inst, c.slots, c.nextSlot, c.captures, c.loops =
		saved, savedInst, savedSlots, savedNext, savedCaps, savedLoops
	return nil
}

// callTarget resolves the instance a call or method-call node reaches from the instance
// currently compiling, reporting false when the node has no concrete target: a call to
// a compiler-provided impl with no Origin body, which the caller lowers to a builtin.
func (c *Compiler) callTarget(node ast.NodeID) (*mono.Instance, bool) {
	return c.mono.Lookup(c.inst, node)
}

// bindParam gives a parameter its slot. Parameters are irrefutable (spec/05-patterns.md),
// so a destructuring parameter is bound by unpacking after the frame is set up.
func (c *Compiler) bindParam(p ast.Pattern) {
	switch v := p.(type) {
	case *ast.BindPat:
		if l, ok := c.res.Bindings[v.NodeID()]; ok {
			c.slotOf(l)
			return
		}
	}
	// A wildcard or a destructuring pattern still occupies an argument slot.
	c.temp()
}

// ---------------------------------------------------------------------------
// Statements
// ---------------------------------------------------------------------------

func (c *Compiler) block(b *ast.Block) error {
	if b == nil {
		c.emit(bytecode.OpUnit, diag.Span{})
		return nil
	}
	for _, s := range b.Stmts {
		if err := c.stmt(s); err != nil {
			return err
		}
	}
	if b.Tail == nil {
		c.emit(bytecode.OpUnit, b.Span())
		return nil
	}
	return c.expr(b.Tail)
}

func (c *Compiler) stmt(s ast.Stmt) error {
	switch v := s.(type) {
	case *ast.LetStmt:
		if err := c.expr(v.Value); err != nil {
			return err
		}
		return c.bindIrrefutable(v.Pat, v.Span())

	case *ast.ExprStmt:
		if err := c.expr(v.X); err != nil {
			return err
		}
		c.emit(bytecode.OpPop, v.Span())
		return nil

	case *ast.ItemStmt:
		return nil // items are compiled where they are declared
	}
	return nil
}

// bindIrrefutable stores the value on top of the stack into a pattern's bindings.
func (c *Compiler) bindIrrefutable(p ast.Pattern, span diag.Span) error {
	switch v := p.(type) {
	case nil, *ast.WildcardPat:
		c.emit(bytecode.OpPop, span)
		return nil

	case *ast.BindPat:
		if l, ok := c.res.Bindings[v.NodeID()]; ok {
			c.emitA(bytecode.OpStore, c.slotOf(l), span)
			return nil
		}
		c.emit(bytecode.OpPop, span)
		return nil

	case *ast.TuplePat:
		slot := c.temp()
		c.emitA(bytecode.OpStore, slot, span)
		for i, e := range v.Elems {
			c.emitA(bytecode.OpLoad, slot, span)
			c.emitA(bytecode.OpGetTupleElem, i, span)
			if err := c.bindIrrefutable(e, span); err != nil {
				return err
			}
		}
		return nil

	case *ast.PathPat:
		slot := c.temp()
		c.emitA(bytecode.OpStore, slot, span)
		return c.bindPathPayload(v, slot, span)
	}
	return unsupported("this pattern in a binding position", span)
}

// bindPathPayload unpacks a struct or single-variant enum pattern that is known to
// match.
func (c *Compiler) bindPathPayload(v *ast.PathPat, slot int, span diag.Span) error {
	ref, _ := c.res.Ref(v.NodeID())
	switch ref.Kind {
	case resolve.Variant:
		for i, e := range v.Elems {
			c.emitA(bytecode.OpLoad, slot, span)
			c.emitA(bytecode.OpGetPayload, i, span)
			if err := c.bindIrrefutable(e, span); err != nil {
				return err
			}
		}
		for _, fp := range v.Fields {
			i := fieldIndexOfVariant(ref.Variant, fp.Name.Name)
			if i < 0 {
				return unsupported("an unknown field in a pattern", span)
			}
			c.emitA(bytecode.OpLoad, slot, span)
			c.emitA(bytecode.OpGetPayload, i, span)
			if err := c.bindIrrefutable(fp.Pat, span); err != nil {
				return err
			}
		}
		return nil

	case resolve.Struct:
		for _, fp := range v.Fields {
			i := fieldIndexOfStruct(ref.Struct, fp.Name.Name)
			if i < 0 {
				return unsupported("an unknown field in a pattern", span)
			}
			c.emitA(bytecode.OpLoad, slot, span)
			c.emitA(bytecode.OpGetField, i, span)
			if err := c.bindIrrefutable(fp.Pat, span); err != nil {
				return err
			}
		}
		return nil
	}
	return unsupported("this pattern in a binding position", span)
}

func fieldIndexOfVariant(v *ast.Variant, name string) int {
	for i, f := range v.Fields {
		if f.Name.Name == name {
			return i
		}
	}
	return -1
}

func fieldIndexOfStruct(s *ast.StructDecl, name string) int {
	for i, f := range s.Fields {
		if f.Name.Name == name {
			return i
		}
	}
	return -1
}

// ---------------------------------------------------------------------------
// Expressions
// ---------------------------------------------------------------------------

func (c *Compiler) expr(e ast.Expr) error {
	switch v := e.(type) {
	case nil:
		c.emit(bytecode.OpUnit, diag.Span{})
		return nil

	case *ast.IntLit:
		c.emitA(bytecode.OpConst, c.intConst(int64(v.Value)), v.Span())
		return nil

	case *ast.FloatLit:
		c.emitA(bytecode.OpConst, c.constant(bytecode.ConstFloat, floatBits(v.Value), ""), v.Span())
		return nil

	case *ast.StrLit:
		c.emitA(bytecode.OpConst, c.strConst(v.Value), v.Span())
		return nil

	case *ast.CharLit:
		c.emitA(bytecode.OpConst, c.constant(bytecode.ConstChar, uint64(v.Value), ""), v.Span())
		return nil

	case *ast.BoolLit:
		if v.Value {
			c.emit(bytecode.OpTrue, v.Span())
		} else {
			c.emit(bytecode.OpFalse, v.Span())
		}
		return nil

	case *ast.SelfExpr:
		c.emitA(bytecode.OpLoad, 0, v.Span())
		return nil

	case *ast.PathExpr:
		return c.path(v)

	case *ast.StructLit:
		return c.structLit(v)

	case *ast.TupleExpr:
		if len(v.Elems) == 0 {
			c.emit(bytecode.OpUnit, v.Span())
			return nil
		}
		for _, el := range v.Elems {
			if err := c.expr(el); err != nil {
				return err
			}
		}
		c.emitAB(bytecode.OpTuple, int(c.tupleType(len(v.Elems))), len(v.Elems), v.Span())
		return nil

	case *ast.Lambda:
		return c.lambda(v)

	case *ast.Block:
		return c.block(v)

	case *ast.If:
		return c.ifExpr(v)

	case *ast.Match:
		return c.match(v)

	case *ast.While:
		return c.whileExpr(v)

	case *ast.Loop:
		return c.loopExpr(v)

	case *ast.For:
		return unsupported("`for` in compiled code (the tree-walking interpreter runs it)", v.Span())

	case *ast.Break:
		return c.breakExpr(v)

	case *ast.Continue:
		if len(c.loops) == 0 {
			return unsupported("`continue` outside a loop", v.Span())
		}
		top := &c.loops[len(c.loops)-1]
		top.continues = append(top.continues, c.emitA(bytecode.OpJump, 0, v.Span()))
		c.emit(bytecode.OpUnit, v.Span())
		return nil

	case *ast.Return:
		if v.Value == nil {
			c.emit(bytecode.OpUnit, v.Span())
		} else if err := c.expr(v.Value); err != nil {
			return err
		}
		c.emit(bytecode.OpReturn, v.Span())
		c.emit(bytecode.OpUnit, v.Span()) // keep the stack shape consistent
		return nil

	case *ast.Unary:
		return c.unary(v)

	case *ast.Binary:
		return c.binary(v)

	case *ast.Assign:
		return c.assign(v)

	case *ast.Cast:
		return c.cast(v)

	case *ast.Call:
		return c.call(v)

	case *ast.MethodCall:
		return c.methodCall(v)

	case *ast.FieldAccess:
		return c.fieldAccess(v)

	case *ast.Try:
		return c.tryExpr(v)
	}
	return unsupported(fmt.Sprintf("%T", e), e.Span())
}

func floatBits(f float64) uint64 { return mathFloat64bits(f) }

func (c *Compiler) path(v *ast.PathExpr) error {
	ref, ok := c.res.Ref(v.NodeID())
	if !ok {
		return unsupported("an unresolved name", v.Span())
	}
	switch ref.Kind {
	case resolve.LocalVar:
		if idx, isCapture := c.captures[ref.Local]; isCapture {
			c.emitA(bytecode.OpLoadCapture, idx, v.Span())
			return nil
		}
		c.emitA(bytecode.OpLoad, c.slotOf(ref.Local), v.Span())
		return nil

	case resolve.Fn:
		inst, ok := c.callTarget(v.NodeID())
		if !ok {
			inst, ok = c.rootIndex[ref.Fn], c.rootIndex[ref.Fn] != nil
		}
		if !ok {
			return unsupported("a call to a function with no body", v.Span())
		}
		c.emitA(bytecode.OpFunc, c.instIndex[inst], v.Span())
		return nil

	case resolve.Const:
		return c.expr(ref.Const.Value)

	case resolve.PrimConst:
		val, err := primConstValue(ref.Name, ref.Member)
		if err != nil {
			return fmt.Errorf("%v at %s", err, v.Span())
		}
		c.emitA(bytecode.OpConst, c.intConst(val), v.Span())
		return nil

	case resolve.Variant:
		vi, ok := c.variantIdx[ref.Variant]
		if !ok {
			return unsupported("an unknown variant", v.Span())
		}
		if ref.Variant.Kind != ast.UnitVariant {
			return unsupported("a variant constructor used as a value", v.Span())
		}
		c.emitAB(bytecode.OpVariant, vi, 0, v.Span())
		return nil

	case resolve.Builtin:
		return unsupported("a builtin used as a value", v.Span())
	}
	return unsupported("this name as a value", v.Span())
}

func (c *Compiler) structLit(v *ast.StructLit) error {
	ref, _ := c.res.Ref(v.NodeID())
	switch ref.Kind {
	case resolve.Struct:
		idx := c.structIdx[ref.Struct]
		// Field initializers run in source order (spec/04-expressions.md), then the
		// values are placed in declaration order, so the two are reconciled through
		// temporaries rather than by reordering the evaluation.
		slots := make([]int, len(ref.Struct.Fields))
		for i := range slots {
			slots[i] = -1
		}
		for _, init := range v.Fields {
			if err := c.expr(init.Value); err != nil {
				return err
			}
			t := c.temp()
			c.emitA(bytecode.OpStore, t, init.Span())
			if fi := fieldIndexOfStruct(ref.Struct, init.Name.Name); fi >= 0 {
				slots[fi] = t
			}
		}
		for _, s := range slots {
			if s < 0 {
				return unsupported("a struct literal missing a field", v.Span())
			}
			c.emitA(bytecode.OpLoad, s, v.Span())
		}
		c.emitAB(bytecode.OpStruct, idx, len(ref.Struct.Fields), v.Span())
		return nil

	case resolve.Variant:
		vi := c.variantIdx[ref.Variant]
		slots := make([]int, len(ref.Variant.Fields))
		for i := range slots {
			slots[i] = -1
		}
		for _, init := range v.Fields {
			if err := c.expr(init.Value); err != nil {
				return err
			}
			t := c.temp()
			c.emitA(bytecode.OpStore, t, init.Span())
			if fi := fieldIndexOfVariant(ref.Variant, init.Name.Name); fi >= 0 {
				slots[fi] = t
			}
		}
		for _, s := range slots {
			if s < 0 {
				return unsupported("a variant literal missing a field", v.Span())
			}
			c.emitA(bytecode.OpLoad, s, v.Span())
		}
		c.emitAB(bytecode.OpVariant, vi, len(ref.Variant.Fields), v.Span())
		return nil
	}
	return unsupported("this struct literal", v.Span())
}

func (c *Compiler) lambda(v *ast.Lambda) error {
	// The captured values are pushed in the order the resolver recorded, and the
	// closure object takes them off the stack.
	caps := c.res.Captures[v.NodeID()]
	for _, l := range caps {
		if idx, isCapture := c.captures[l]; isCapture {
			c.emitA(bytecode.OpLoadCapture, idx, v.Span())
			continue
		}
		c.emitA(bytecode.OpLoad, c.slotOf(l), v.Span())
	}

	// The lambda's body becomes an ordinary function whose captures are addressed
	// separately from its locals.
	idx := len(c.prog.Fns)
	lfn := &bytecode.Fn{
		Name: "<lambda>", Params: len(v.Params), Captures: len(caps), Span: v.Span(),
	}
	c.prog.Fns = append(c.prog.Fns, lfn)

	saved, savedSlots, savedNext, savedCaps, savedLoops := c.fn, c.slots, c.nextSlot, c.captures, c.loops
	c.fn = lfn
	c.slots = map[*resolve.Local]int{}
	c.captures = map[*resolve.Local]int{}
	c.nextSlot = 0
	c.loops = nil
	for i, l := range caps {
		c.captures[l] = i
	}
	for _, p := range v.Params {
		c.bindParam(p.Pat)
	}
	err := c.expr(v.Body)
	if err == nil {
		c.emit(bytecode.OpReturn, v.Span())
	}
	c.fn, c.slots, c.nextSlot, c.captures, c.loops = saved, savedSlots, savedNext, savedCaps, savedLoops
	if err != nil {
		return err
	}

	c.emitAB(bytecode.OpClosure, idx, len(caps), v.Span())
	return nil
}

func (c *Compiler) ifExpr(v *ast.If) error {
	if err := c.expr(v.Cond); err != nil {
		return err
	}
	toElse := c.emitA(bytecode.OpJumpIfFalse, 0, v.Cond.Span())
	if err := c.block(v.Then); err != nil {
		return err
	}
	toEnd := c.emitA(bytecode.OpJump, 0, v.Span())
	c.patch(toElse)
	if v.Else != nil {
		if err := c.expr(v.Else); err != nil {
			return err
		}
	} else {
		c.emit(bytecode.OpUnit, v.Span())
	}
	c.patch(toEnd)
	return nil
}

func (c *Compiler) whileExpr(v *ast.While) error {
	top := c.here()
	c.loops = append(c.loops, loopCtx{})
	if err := c.expr(v.Cond); err != nil {
		return err
	}
	exit := c.emitA(bytecode.OpJumpIfFalse, 0, v.Cond.Span())
	if err := c.block(v.Body); err != nil {
		return err
	}
	c.emit(bytecode.OpPop, v.Span()) // a `while` body's value is discarded
	contTarget := c.here()
	c.emitA(bytecode.OpJump, top, v.Span())
	c.patch(exit)

	ctx := c.loops[len(c.loops)-1]
	c.loops = c.loops[:len(c.loops)-1]
	for _, b := range ctx.breaks {
		c.patch(b)
	}
	for _, k := range ctx.continues {
		c.fn.Code[k].A = int32(contTarget)
	}
	c.emit(bytecode.OpUnit, v.Span())
	return nil
}

func (c *Compiler) loopExpr(v *ast.Loop) error {
	top := c.here()
	c.loops = append(c.loops, loopCtx{isValueLoop: true})
	if err := c.block(v.Body); err != nil {
		return err
	}
	c.emit(bytecode.OpPop, v.Span())
	c.emitA(bytecode.OpJump, top, v.Span())

	ctx := c.loops[len(c.loops)-1]
	c.loops = c.loops[:len(c.loops)-1]
	for _, b := range ctx.breaks {
		c.patch(b)
	}
	for _, k := range ctx.continues {
		c.fn.Code[k].A = int32(top)
	}
	// A `loop` leaves its break value on the stack; a loop with no break never gets
	// here, and the checker gives it type `()`.
	return nil
}

func (c *Compiler) breakExpr(v *ast.Break) error {
	if len(c.loops) == 0 {
		return unsupported("`break` outside a loop", v.Span())
	}
	top := &c.loops[len(c.loops)-1]
	if top.isValueLoop {
		if v.Value == nil {
			c.emit(bytecode.OpUnit, v.Span())
		} else if err := c.expr(v.Value); err != nil {
			return err
		}
	}
	top.breaks = append(top.breaks, c.emitA(bytecode.OpJump, 0, v.Span()))
	if !top.isValueLoop {
		c.emit(bytecode.OpUnit, v.Span())
	}
	return nil
}

func (c *Compiler) fieldAccess(v *ast.FieldAccess) error {
	if err := c.expr(v.Recv); err != nil {
		return err
	}
	t := c.typeOf(v.Recv)
	n, ok := types.AsNamed(t)
	if !ok || n.Def.Struct == nil {
		return unsupported("a field read on a non-struct", v.Span())
	}
	idx := fieldIndexOfStruct(n.Def.Struct, v.Name.Name)
	if idx < 0 {
		return unsupported("an unknown field", v.Span())
	}
	c.emitA(bytecode.OpGetField, idx, v.Span())
	return nil
}
