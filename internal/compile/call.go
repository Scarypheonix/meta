package compile

import (
	"fmt"

	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/bytecode"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/layout"
	"github.com/scarypheonix/meta/internal/resolve"
	"github.com/scarypheonix/meta/internal/types"
)

// builtinIndex maps a compiler-provided function's path to its VM index.
var builtinIndex = map[string]int{
	"io::print":   BuiltinPrint,
	"io::println": BuiltinPrintln,
	"panic":       BuiltinPanic,
	"ref_eq":      BuiltinRefEq,

	"thread::spawn":       BuiltinSpawn,
	"thread::join_thread": BuiltinJoin,
	"chan::channel":       BuiltinChannel,
	"chan::send_value":    BuiltinSend,
	"chan::await_value":   BuiltinAwait,
	"chan::taken_value":   BuiltinTaken,
	"chan::close_sender":  BuiltinCloseChan,
	"sync::mutex":         BuiltinMutex,
	"sync::with_lock":     BuiltinWithLock,

	"array::new":      BuiltinNewArray,
	"array::len":      BuiltinArrayLen,
	"array::cap":      BuiltinArrayCap,
	"array::at":       BuiltinArrayAt,
	"array::set":      BuiltinArraySet,
	"array::push":     BuiltinArrayPush,
	"array::truncate": BuiltinArrayTruncate,
	"list::new":       BuiltinListNew,
}

func (c *Compiler) call(v *ast.Call) error {
	// A call to a tuple variant constructs it rather than calling anything.
	if pe, ok := v.Fn.(*ast.PathExpr); ok {
		ref, _ := c.res.Ref(pe.NodeID())
		switch ref.Kind {
		case resolve.Variant:
			for _, a := range v.Args {
				if err := c.expr(a); err != nil {
					return err
				}
			}
			vi, err := c.variantInst(ref.Variant, c.typeOf(v))
			if err != nil {
				return err
			}
			c.emitAB(bytecode.OpVariant, vi, len(v.Args), v.Span())
			return nil

		case resolve.Builtin:
			idx, ok := builtinIndex[ref.Builtin]
			if !ok {
				return unsupported("builtin `"+ref.Builtin+"`", v.Span())
			}
			if idx == BuiltinListNew {
				return c.listNew(v)
			}
			for _, a := range v.Args {
				if err := c.expr(a); err != nil {
					return err
				}
			}
			argc := len(v.Args)
			if idx == BuiltinNewArray {
				// `array::new` takes one more argument than it is written with: the
				// layout of the array it is about to make. An array's shape says
				// whether its elements are references, which only the compiler knows
				// and every collector must (ADR-0028, spec/13-collections.md).
				id, err := c.arrayInst(c.typeOf(v), v.Span())
				if err != nil {
					return err
				}
				c.emitA(bytecode.OpConst, c.intConst(int64(id)), v.Span())
				argc++
			}
			if wantsIsRef(idx) {
				// These three take one more argument than they are written with: whether
				// the value they will hold on the program's behalf is a reference. Native
				// code keeps a thread's result, a channel's queue and a mutex's guarded
				// value in raw memory with no stack map over it, so the collector cannot
				// tell a reference from an integer there. Only the compiler knows -- it has
				// the checker's own T -- so it says. The other two engines ignore the
				// argument; their hosts find references without being told
				// (spec/12-concurrency.md).
				isRef, err := c.concurrencyElemIsRef(idx, v)
				if err != nil {
					return err
				}
				c.emitA(bytecode.OpConst, c.intConst(isRef), v.Span())
				argc++
			}
			c.emitABK(bytecode.OpCallBuiltin, idx, argc, c.kindOf(v), v.Span())
			return c.wrapConcurrencyHandle(idx, v)
		}
	}

	if err := c.expr(v.Fn); err != nil {
		return err
	}
	for _, a := range v.Args {
		if err := c.expr(a); err != nil {
			return err
		}
	}
	c.emitAK(bytecode.OpCall, len(v.Args), c.kindOf(v), v.Span())
	return nil
}

// listNew compiles `list::new[T]()`: an empty array, wrapped in the prelude's `List[T]`.
//
// No engine implements this. It is bytecode the compiler writes out of operations that
// already exist -- the same arrangement `wrapConcurrencyHandle` uses, and for the same
// reason: the runtime has no business knowing what a prelude type is, and here it does not
// even have to know that a list was made.
func (c *Compiler) listNew(v *ast.Call) error {
	list, ok := types.AsNamed(c.concreteType(c.typeOf(v)))
	if !ok || len(list.Args) != 1 {
		return unsupported("a `list::new` whose type is not List[T]", v.Span())
	}
	elem := &types.Named{Def: types.ArrayDef, Args: []types.Type{list.Args[0]}}
	id, err := c.arrayInst(elem, v.Span())
	if err != nil {
		return err
	}
	// An empty array with no room: the first push grows it, which is one allocation
	// nobody pays for until a list is actually used.
	c.emitA(bytecode.OpConst, c.intConst(0), v.Span())
	c.emitA(bytecode.OpConst, c.intConst(int64(id)), v.Span())
	c.emitABK(bytecode.OpCallBuiltin, BuiltinNewArray, 2, bytecode.KindRef, v.Span())

	si, err := c.structInst(c.typeOf(v), v.Span())
	if err != nil {
		return err
	}
	c.emitAB(bytecode.OpStruct, si, 1, v.Span())
	return nil
}

// builtinMethods are the methods the compiler knows without an impl to call: the
// prelude declares their signatures, and the VM implements them (see the builtin impl
// table in internal/check). Phase 7 replaces them with Origin source.
var builtinMethodOps = map[string]bytecode.Op{
	"wrapping_add": bytecode.OpWrapAdd,
	"wrapping_sub": bytecode.OpWrapSub,
	"wrapping_mul": bytecode.OpWrapMul,
}

var builtinMethodCalls = map[string]int{
	"checked_add":    BuiltinCheckedAdd,
	"checked_sub":    BuiltinCheckedSub,
	"checked_mul":    BuiltinCheckedMul,
	"saturating_add": BuiltinSaturatingAdd,
	"saturating_sub": BuiltinSaturatingSub,
	"saturating_mul": BuiltinSaturatingMul,
}

// cmpBuiltinFor picks which of the per-kind `cmp` builtins a call needs. `Ord` is
// registered (internal/check's builtin impl table) for every signed and unsigned
// integer width, both float widths, `char` and `String`; nothing else reaches here for
// a well-typed program.
func cmpBuiltinFor(k bytecode.Kind, span diag.Span) (int, error) {
	switch k {
	case bytecode.KindInt, bytecode.KindChar:
		return BuiltinCmpInt, nil
	case bytecode.KindUint:
		return BuiltinCmpUint, nil
	case bytecode.KindFloat:
		return BuiltinCmpFloat, nil
	case bytecode.KindString:
		return BuiltinCmpString, nil
	}
	return 0, unsupported(fmt.Sprintf("`cmp` on a %s", k), span)
}

func (c *Compiler) methodCall(v *ast.MethodCall) error {
	// A method resolving to Origin source — a user impl, or a trait's default body —
	// is a direct call to the instance mono picked for this call site. Which instance
	// that is depends on the receiver's concrete type, which is why the same source
	// call `a.cmp(b)` inside a generic body can reach a different callee in each
	// instantiation (ADR-0010).
	if inst, ok := c.callTarget(v.NodeID()); ok {
		c.emitA(bytecode.OpFunc, c.instIndex[inst], v.Span())
		if err := c.expr(v.Recv); err != nil {
			return err
		}
		for _, a := range v.Args {
			if err := c.expr(a); err != nil {
				return err
			}
		}
		c.emitAK(bytecode.OpCall, len(v.Args)+1, c.kindOf(v), v.Span())
		return nil
	}

	// Otherwise it is one of the compiler-provided methods on a primitive.
	if v.Name.Name == "to_str" && len(v.Args) == 0 {
		if err := c.expr(v.Recv); err != nil {
			return err
		}
		// Rendering depends on what is being rendered, and native code cannot ask a
		// register what it holds, so the kind travels with the instruction.
		c.emitA(bytecode.OpToStr, int(c.kindOf(v.Recv)), v.Span())
		return nil
	}
	if op, ok := builtinMethodOps[v.Name.Name]; ok && len(v.Args) == 1 {
		if err := c.expr(v.Recv); err != nil {
			return err
		}
		if err := c.expr(v.Args[0]); err != nil {
			return err
		}
		c.emit(op, v.Span())
		return nil
	}
	if v.Name.Name == "cmp" && len(v.Args) == 1 {
		idx, err := cmpBuiltinFor(c.kindOf(v.Recv), v.Span())
		if err != nil {
			return err
		}
		if err := c.expr(v.Recv); err != nil {
			return err
		}
		if err := c.expr(v.Args[0]); err != nil {
			return err
		}
		c.emitAB(bytecode.OpCallBuiltin, idx, 2, v.Span())
		return nil
	}
	if idx, ok := builtinMethodCalls[v.Name.Name]; ok {
		if err := c.expr(v.Recv); err != nil {
			return err
		}
		for _, a := range v.Args {
			if err := c.expr(a); err != nil {
				return err
			}
		}
		c.emitAB(bytecode.OpCallBuiltin, idx, len(v.Args)+1, v.Span())
		return nil
	}
	return unsupported("method `"+v.Name.Name+"`", v.Span())
}

// tryExpr compiles `?`: unwrap `Ok`, or return `Err` from the enclosing function.
func (c *Compiler) tryExpr(v *ast.Try) error {
	if err := c.expr(v.X); err != nil {
		return err
	}
	inner := c.typeOf(v.X)
	okVariant, errVariant, err := c.resultVariants(inner, v.Span())
	if err != nil {
		return err
	}
	okKind := bytecode.KindUnknown
	if n, ok := types.AsNamed(c.concreteType(inner)); ok && len(n.Args) == 2 {
		okKind = kindOfType(c.concreteType(n.Args[0]))
	}

	slot := c.temp()
	c.emitA(bytecode.OpStore, slot, v.Span())
	c.emitA(bytecode.OpLoad, slot, v.Span())
	c.emitA(bytecode.OpIsVariant, okVariant, v.Span())
	toErr := c.emitA(bytecode.OpJumpIfFalse, 0, v.Span())

	c.emitA(bytecode.OpLoad, slot, v.Span())
	c.emitAK(bytecode.OpGetPayload, 0, okKind, v.Span())
	toEnd := c.emitA(bytecode.OpJump, 0, v.Span())

	c.patch(toErr)
	c.emitA(bytecode.OpLoad, slot, v.Span())
	c.emit(bytecode.OpReturn, v.Span())
	c.emit(bytecode.OpUnit, v.Span())
	_ = errVariant

	c.patch(toEnd)
	return nil
}

// optionSomeVariant returns the bytecode variant index of `Option::Some` at a concrete
// `item` type, building its exact layout on first use. forExpr uses it to test the
// `Option[Item]` a `for` loop's desugared `next()` call returns (spec/04-expressions.md);
// testing only `Some` and falling through to `break` otherwise is equivalent to also
// testing `None`, since the checker already proved `next()` returns nothing else.
func (c *Compiler) optionSomeVariant(item types.Type, span diag.Span) (int, error) {
	if c.optionDef == nil || c.optionSome == nil {
		return 0, unsupported("`for`: the prelude does not declare `Option`", span)
	}
	optionT := &types.Named{Def: c.optionDef, Args: []types.Type{item}}
	return c.variantInst(c.optionSome, optionT)
}

// resultVariants finds, building on first use, the instantiated indices of `Ok` and
// `Err` for a concrete Result type.
func (c *Compiler) resultVariants(t types.Type, span diag.Span) (ok, errIdx int, err error) {
	n, isNamed := types.AsNamed(c.concreteType(t))
	if !isNamed || n.Def == nil || n.Def.Enum == nil {
		return 0, 0, unsupported("`?` on something that is not a Result", span)
	}
	var okVar, errVar *ast.Variant
	for _, v := range n.Def.Enum.Variants {
		switch v.Name.Name {
		case "Ok":
			okVar = v
		case "Err":
			errVar = v
		}
	}
	if okVar == nil || errVar == nil {
		return 0, 0, unsupported("`?` on something that is not a Result", span)
	}
	if ok, err = c.variantInst(okVar, n); err != nil {
		return 0, 0, err
	}
	if errIdx, err = c.variantInst(errVar, n); err != nil {
		return 0, 0, err
	}
	return ok, errIdx, nil
}

// wrapConcurrencyHandle turns the bare handle a concurrency builtin returns into the
// prelude type the checker says the call has (spec/12-concurrency.md).
//
// The runtime deliberately knows nothing about `JoinHandle[T]` or `Sender[T]`: it returns
// an integer, and the wrapping happens here, where the call's own checked type names the
// instantiation to build. That is what keeps the three engines sharing one design -- the
// native backend could not construct a prelude type from machine code, and now nothing
// asks it to.
func (c *Compiler) wrapConcurrencyHandle(idx int, v *ast.Call) error {
	switch idx {
	case BuiltinSpawn, BuiltinMutex:
		// A single handle in a single-field struct: `JoinHandle[T]` or `Mutex[T]`.
		si, err := c.structInst(c.typeOf(v), v.Span())
		if err != nil {
			return err
		}
		c.emitAB(bytecode.OpStruct, si, 1, v.Span())
		return nil

	case BuiltinChannel:
		// One handle, two ends: `(Sender[T], Receiver[T])`. Both name the same queue --
		// they are separate types so that a receiver cannot send and a sender cannot
		// close what it does not own.
		tup, ok := c.concreteType(c.typeOf(v)).(*types.TupleT)
		if !ok || len(tup.Elems) != 2 {
			return unsupported("a channel whose type is not a pair of ends", v.Span())
		}
		sender, err := c.structInst(tup.Elems[0], v.Span())
		if err != nil {
			return err
		}
		receiver, err := c.structInst(tup.Elems[1], v.Span())
		if err != nil {
			return err
		}
		// The handle is produced once and needed twice, so it goes through an anonymous
		// frame slot rather than a stack-duplicating opcode -- one fewer instruction for
		// three engines to agree on.
		slot := c.temp()
		c.emitA(bytecode.OpStore, slot, v.Span())
		c.emitA(bytecode.OpLoad, slot, v.Span())
		c.emitAB(bytecode.OpStruct, sender, 1, v.Span())
		c.emitA(bytecode.OpLoad, slot, v.Span())
		c.emitAB(bytecode.OpStruct, receiver, 1, v.Span())
		ti, err := c.tupleInst(c.typeOf(v), v.Span())
		if err != nil {
			return err
		}
		c.emitAB(bytecode.OpTuple, int(ti), 2, v.Span())
		return nil
	}
	return nil
}

// wantsIsRef reports whether a builtin takes the extra "is this a reference" argument.
func wantsIsRef(idx int) bool {
	return idx == BuiltinSpawn || idx == BuiltinChannel || idx == BuiltinMutex
}

// concurrencyElemIsRef reports whether the value the runtime will hold for this call is
// something the collector must treat as a reference.
//
// Which type that is depends on the call: `spawn` has `JoinHandle[T]`, `mutex` has
// `Mutex[T]`, and `channel` has `(Sender[T], Receiver[T])` -- a pair, whose first end names
// the same T the queue will hold.
func (c *Compiler) concurrencyElemIsRef(idx int, v *ast.Call) (int64, error) {
	t := c.concreteType(c.typeOf(v))
	if idx == BuiltinChannel {
		tup, ok := t.(*types.TupleT)
		if !ok || len(tup.Elems) != 2 {
			return 0, unsupported("a channel whose type is not a pair of ends", v.Span())
		}
		t = c.concreteType(tup.Elems[0])
	}
	named, ok := types.AsNamed(t)
	if !ok || len(named.Args) != 1 {
		return 0, unsupported("a concurrency handle that is not of the form Handle[T]", v.Span())
	}
	k, ok := wordKindFor(c.concreteType(named.Args[0]))
	if !ok {
		return 0, unsupported("a concurrency handle whose element type has no object layout", v.Span())
	}
	if k == layout.WordRef {
		return 1, nil
	}
	return 0, nil
}
