package backend

import (
	"github.com/scarypheonix/meta/internal/bytecode"
	"github.com/scarypheonix/meta/internal/compile"
	"github.com/scarypheonix/meta/internal/ir"
)

// propagateKinds is the native-only pass that completes the Kind ir.Build already
// seeded (ADR-0021, internal/ir's own doc comment on Value.Kind) into every value's
// actual kind. It runs once per function, in buildIR, after resolveClosureCalls: a
// value's kind depends only on its own operation and its operands' kinds, never on
// anything closures.go's boxing decisions change, so the order between the two passes
// does not matter, but running after it means OpBoxFn and a repointed OpCallClosure
// already have their final Op.
//
// This, plus ir.Build's own seeding, is the whole of what "a value's kind travels with
// it" (ADR-0016, ADR-0021) means for the ops internal/opt and the VM never needed it
// for: internal/ir stays otherwise untyped, and this data exists only inside
// internal/backend's own copy of the IR.
func propagateKinds(f *ir.Func, prog *bytecode.Program) {
	var phis []*ir.Value
	f.Values(func(v *ir.Value) {
		if v.Op == ir.OpPhi {
			phis = append(phis, v)
			return
		}
		if k, ok := staticKind(v, prog); ok {
			v.Kind = k
		}
	})

	// A φ's kind is whatever its operands' already is -- a well-typed program's static
	// type does not change across branches -- but an operand can itself be another φ (a
	// loop-carried value), so this is a small fixed point rather than one pass. Each
	// round resolves at least one φ that was blocked only on another φ in this same
	// set, so it converges in at most len(phis) rounds.
	for round := 0; round < len(phis)+1; round++ {
		changed := false
		for _, p := range phis {
			if p.Kind != bytecode.KindUnknown {
				continue
			}
			for _, a := range p.Args {
				if a != nil && a.Kind != bytecode.KindUnknown {
					p.Kind = a.Kind
					changed = true
					break
				}
			}
		}
		if !changed {
			break
		}
	}
}

// staticKind reports the kind an operation fixes on its own. OpParam, OpCapture,
// OpGetField, OpCall and OpCallClosure already carry their kind from ir.Build (or, for
// OpCallClosure, from the OpCall closures.go repointed) -- this just reports it, or
// reports it as not yet known when ir.Build had nothing to seed it with (an
// out-of-range parameter or capture index, which reaching here is a compiler bug
// upstream of this pass, not one for it to raise). OpPhi is handled by
// propagateKinds's own fixed point instead of here, since it needs other values' kinds.
func staticKind(v *ir.Value, prog *bytecode.Program) (bytecode.Kind, bool) {
	switch v.Op {
	case ir.OpParam, ir.OpCapture, ir.OpGetField, ir.OpCall, ir.OpCallClosure:
		return v.Kind, v.Kind != bytecode.KindUnknown

	case ir.OpConst:
		if v.Const < 0 || v.Const >= len(prog.Consts) {
			return bytecode.KindUnknown, false
		}
		switch prog.Consts[v.Const].Kind {
		case bytecode.ConstInt:
			return bytecode.KindI64, true
		case bytecode.ConstFloat:
			return bytecode.KindFloat, true
		case bytecode.ConstChar:
			return bytecode.KindChar, true
		case bytecode.ConstString:
			return bytecode.KindString, true
		}
		return bytecode.KindUnknown, false

	case ir.OpUnit:
		return bytecode.KindUnit, true
	case ir.OpTrue, ir.OpFalse:
		return bytecode.KindBool, true

	case ir.OpAdd, ir.OpSub, ir.OpMul, ir.OpDiv, ir.OpRem, ir.OpNeg,
		ir.OpWrapAdd, ir.OpWrapSub, ir.OpWrapMul,
		ir.OpAnd, ir.OpOr, ir.OpXor, ir.OpShl, ir.OpShr:
		// Every one of these is an integer op. Every integer kind is "raw" to a stack
		// map -- the only distinction that matters here -- so neither the width nor the
		// signedness the checker resolved needs to travel this far, and KindI64 stands
		// for "a machine word that is not a reference".
		return bytecode.KindI64, true

	case ir.OpAddF, ir.OpSubF, ir.OpMulF, ir.OpDivF, ir.OpRemF, ir.OpNegF:
		return bytecode.KindFloat, true

	case ir.OpEq, ir.OpNe, ir.OpLt, ir.OpLe, ir.OpGt, ir.OpGe, ir.OpNot, ir.OpIsVariant:
		return bytecode.KindBool, true

	case ir.OpFunc:
		// A bare code address: raw, never a reference. closures.go's resolveClosureCalls
		// has already boxed any OpFunc value that escapes being an immediate call's own
		// callee, so every OpFunc value reaching here really is just an address.
		return bytecode.KindI64, true

	case ir.OpStruct, ir.OpTuple, ir.OpVariant, ir.OpClosure, ir.OpBoxFn:
		return bytecode.KindRef, true

	case ir.OpToStr:
		return bytecode.KindString, true

	case ir.OpCast:
		return castResultKind(bytecode.CastKind(v.Const), v.Aux), true

	case ir.OpCallBuiltin:
		if k := builtinResultKind(v.Const); k != bytecode.KindUnknown {
			return k, true
		}
		// A builtin whose result is the program's own T (`join`, `taken`, `with`) already
		// carries its kind on the instruction, seeded by internal/compile from the
		// checker's types. Returning "unknown, and I am sure" here would overwrite that
		// with nothing -- which is how a program that builds at -O1 came to panic at -O2.
		return bytecode.KindUnknown, false
	}
	return bytecode.KindUnknown, false
}

// castResultKind derives a cast's result kind from its CastKind and the packed
// width/signedness compile.castWidth gave it -- always raw, since `as` only converts
// between primitives (spec/04-expressions.md), so only which raw kind is in question.
func castResultKind(k bytecode.CastKind, aux int) bytecode.Kind {
	switch k {
	case bytecode.CastIntToFloat, bytecode.CastFloatNarrow, bytecode.CastFloatWiden:
		return bytecode.KindFloat
	case bytecode.CastIntTrunc, bytecode.CastFloatToInt, bytecode.CastBoolToInt, bytecode.CastCharToInt:
		if aux&(1<<8) != 0 {
			return bytecode.KindI64
		}
		return bytecode.KindU64
	}
	return bytecode.KindUnknown
}

// builtinResultKind gives each compiler-provided builtin's fixed result kind
// (internal/vm/values.go's callBuiltin is the ground truth this mirrors: `Checked*`
// wraps in Option, `Saturating*` never fails so it does not, and every `Cmp*` builds an
// Ordering).
func builtinResultKind(idx int) bytecode.Kind {
	switch idx {
	case compile.BuiltinPrint, compile.BuiltinPrintln, compile.BuiltinPanic:
		return bytecode.KindUnit
	case compile.BuiltinRefEq:
		return bytecode.KindBool
	case compile.BuiltinCmpInt, compile.BuiltinCmpUint, compile.BuiltinCmpFloat, compile.BuiltinCmpString:
		return bytecode.KindRef
	case compile.BuiltinFitsAdd, compile.BuiltinFitsSub, compile.BuiltinFitsMul:
		return bytecode.KindBool
	case compile.BuiltinSaturatingAdd, compile.BuiltinSaturatingSub, compile.BuiltinSaturatingMul:
		return bytecode.KindI64
	case compile.BuiltinSpawn, compile.BuiltinChannel, compile.BuiltinMutex:
		// A bare handle: an index into the runtime's own tables, which internal/compile
		// then wraps in the prelude type the call's checked type names
		// (spec/12-concurrency.md).
		return bytecode.KindI64
	case compile.BuiltinAwait:
		return bytecode.KindBool
	case compile.BuiltinSend, compile.BuiltinCloseChan:
		return bytecode.KindUnit
	case compile.BuiltinJoin, compile.BuiltinTaken, compile.BuiltinWithLock:
		// These yield the program's own T, whose kind varies per call site. It is on the
		// instruction, put there by internal/compile from the checker's own types
		// (ADR-0021), so there is nothing fixed to return here -- and answering anyway
		// would be the "plausible wrong answer" process rule 8 refuses.
		return bytecode.KindUnknown
	}
	return bytecode.KindUnknown
}
