// Package bytecode defines Origin's stack bytecode: the instruction set, the chunk that
// holds a compiled program, and a disassembler.
//
// The instruction set is a stack machine, which spec/03 of the phase plan permits
// ("stack or register bytecode"). A stack machine was chosen for one reason: Phase 4
// lowers this to an SSA IR anyway, so the register allocation problem is solved there,
// on a representation designed for it, rather than twice.
//
// Every instruction that can trap carries the source span it would trap at, so a trap
// message names a line and column without the VM keeping a shadow stack of positions.
package bytecode

import (
	"fmt"
	"strings"

	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/layout"
)

// Op is an opcode.
type Op uint8

// The instruction set. Operands are in the instruction's A and B fields; the stack
// supplies everything else.
const (
	// OpNop does nothing. It exists so that a jump target can be patched safely.
	OpNop Op = iota

	// --- constants and locals ---

	// OpConst pushes constant A.
	OpConst
	// OpUnit pushes the unit value.
	OpUnit
	// OpTrue and OpFalse push their booleans.
	OpTrue
	OpFalse
	// OpLoad pushes local slot A.
	OpLoad
	// OpStore pops into local slot A.
	OpStore
	// OpLoadCapture pushes capture slot A of the running closure.
	OpLoadCapture
	// OpPop discards the top of the stack.
	OpPop

	// --- arithmetic, all trapping per ADR-0005 ---

	OpAdd
	OpSub
	OpMul
	OpDiv
	OpRem
	OpNeg
	// OpAddF and friends are the float forms, which never trap.
	OpAddF
	OpSubF
	OpMulF
	OpDivF
	OpRemF
	OpNegF
	// OpWrapAdd and friends are the explicit non-trapping integer forms.
	OpWrapAdd
	OpWrapSub
	OpWrapMul

	// --- bitwise and shifts ---

	OpAnd
	OpOr
	OpXor
	OpShl
	OpShr

	// --- comparison ---

	// OpEq compares structurally (spec/04-expressions.md).
	OpEq
	OpNe
	OpLt
	OpLe
	OpGt
	OpGe
	OpNot

	// --- control flow ---

	// OpJump jumps to A.
	OpJump
	// OpJumpIfFalse pops a bool and jumps to A when it is false.
	OpJumpIfFalse
	// OpJumpIfTrue pops a bool and jumps to A when it is true.
	OpJumpIfTrue
	// OpReturn returns the top of the stack from the current function.
	OpReturn

	// --- calls ---

	// OpCall calls the callee beneath A arguments.
	OpCall
	// OpCallBuiltin calls builtin A with B arguments.
	OpCallBuiltin
	// OpClosure builds closure-creation site A (Program.Closures), capturing B values
	// from the stack.
	OpClosure
	// OpFunc pushes the top-level function A as a value.
	OpFunc

	// --- aggregates ---

	// OpStruct builds struct instantiation A (Program.Structs) from B values on the
	// stack.
	OpStruct
	// OpTuple builds tuple-shape descriptor A from B values on the stack.
	OpTuple
	// OpVariant builds enum variant A (constant index) from B payload values.
	OpVariant
	// OpGetField pushes field A of the object on top.
	OpGetField
	// OpSetField pops a value and stores it into field A of the object beneath it.
	OpSetField
	// OpIsVariant pushes whether the object on top is variant A of its enum.
	OpIsVariant
	// OpGetPayload pushes payload word A of the variant on top.
	OpGetPayload
	// OpGetTupleElem pushes element A of the tuple on top.
	OpGetTupleElem

	// --- primitive conversions ---

	// OpCast converts the top of the stack, with A naming the conversion.
	OpCast
	// OpToStr renders the top of the stack as a String.
	OpToStr

	// OpTrap stops with the message in constant A. It is how `panic` and an
	// unreachable match arm are emitted.
	OpTrap

	// OpHalt ends the program.
	OpHalt

	numOps
)

var opNames = [...]string{
	OpNop: "nop", OpConst: "const", OpUnit: "unit", OpTrue: "true", OpFalse: "false",
	OpLoad: "load", OpStore: "store", OpLoadCapture: "load_capture", OpPop: "pop",
	OpAdd: "add", OpSub: "sub", OpMul: "mul", OpDiv: "div", OpRem: "rem", OpNeg: "neg",
	OpAddF: "addf", OpSubF: "subf", OpMulF: "mulf", OpDivF: "divf", OpRemF: "remf", OpNegF: "negf",
	OpWrapAdd: "wrap_add", OpWrapSub: "wrap_sub", OpWrapMul: "wrap_mul",
	OpAnd: "and", OpOr: "or", OpXor: "xor", OpShl: "shl", OpShr: "shr",
	OpEq: "eq", OpNe: "ne", OpLt: "lt", OpLe: "le", OpGt: "gt", OpGe: "ge", OpNot: "not",
	OpJump: "jump", OpJumpIfFalse: "jump_if_false", OpJumpIfTrue: "jump_if_true", OpReturn: "return",
	OpCall: "call", OpCallBuiltin: "call_builtin", OpClosure: "closure", OpFunc: "func",
	OpStruct: "struct", OpTuple: "tuple", OpVariant: "variant",
	OpGetField: "get_field", OpSetField: "set_field",
	OpIsVariant: "is_variant", OpGetPayload: "get_payload", OpGetTupleElem: "get_tuple_elem",
	OpCast: "cast", OpToStr: "to_str", OpTrap: "trap", OpHalt: "halt",
}

func (o Op) String() string {
	if int(o) < len(opNames) && opNames[o] != "" {
		return opNames[o]
	}
	return fmt.Sprintf("op(%d)", int(o))
}

// Instr is one instruction. It is a struct rather than packed bytes because the VM is
// stage0: the packed encoding belongs to the native backend, and an explicit struct
// keeps the disassembler honest and the compiler readable.
type Instr struct {
	Op Op
	A  int32
	B  int32
	// Kind is the static type of the value this instruction produces, for the handful
	// of opcodes whose result the native backend cannot otherwise recover: a field,
	// payload or tuple-element read (internal/compile already knows a field's exact
	// kind when it builds the field's own object layout, ADR-0019) and a call's result
	// (which the checker already gave the call expression a type). KindUnknown is every
	// other instruction's value; the ops that populate this are documented on Kind
	// itself.
	Kind Kind
	// Span is where this instruction came from, so a trap names a source location.
	Span diag.Span
}

// Kind is the static type of an operand or a result, for the handful of opcodes whose
// behaviour — or whose value's own shape — the native backend cannot recover any other
// way.
//
// The virtual machine does not need this: every value it holds carries a tag, so `to_str`
// and `==` can look at what they were given, and a collection can look at an object's own
// header to know what it is. Native code has no tags — a register holds sixty-four bits
// and nothing says what they mean — so the static type has to travel with the
// instruction, both for the operations whose lowering depends on it (comparisons,
// `to_str`) and, since ADR-0021, for the ones a stack map needs to know are references at
// all: a field, payload or tuple-element read, and a call's result.
//
// ADR-0016 anticipated exactly this: when the backend needs something the bytecode has
// thrown away, the answer is to widen the bytecode rather than to grow a second lowering
// path from the AST. This is the first thing to arrive that way, and ADR-0021 is the
// second.
type Kind int32

// The operand kinds. KindUnknown is what an older or hand-written instruction carries,
// and the backend rejects it rather than guessing.
//
// An integer says its *width* as well as its signedness, because that is what "what do
// these sixty-four bits mean" actually asks: `255u8 + 1` has to trap and `255u32 + 1` has
// to be 256, and nothing in the register distinguishes them. One vocabulary rather than a
// kind plus a separate width operand, so there is one place to be wrong.
const (
	KindUnknown Kind = iota
	KindI8
	KindI16
	KindI32
	KindI64
	KindU8
	KindU16
	KindU32
	KindU64
	KindFloat
	KindBool
	KindChar
	KindString
	KindUnit
	// KindRef is an aggregate: a struct, an enum, a tuple or a closure. Its object
	// header names its type, so the runtime can work structurally from there.
	KindRef
)

// IsInteger reports whether k is one of the eight integer kinds.
func (k Kind) IsInteger() bool { return k >= KindI8 && k <= KindU64 }

// IsSigned reports whether k is a signed integer. It is false for everything that is not
// an integer at all, so a caller must ask IsInteger first when that matters.
func (k Kind) IsSigned() bool { return k >= KindI8 && k <= KindI64 }

// Bits is an integer kind's width. It is 64 for anything else, which is the width of the
// machine word every other value occupies.
func (k Kind) Bits() uint {
	switch k {
	case KindI8, KindU8:
		return 8
	case KindI16, KindU16:
		return 16
	case KindI32, KindU32:
		return 32
	}
	return 64
}

// IntKindNamed is the integer kind a primitive type's source name denotes. It is here
// rather than in a compiler package because `u64::MAX` has to mean the same number in the
// interpreter and in the bytecode compiler, and the number is arith.Max of this kind.
func IntKindNamed(name string) (Kind, bool) {
	switch name {
	case "i8":
		return KindI8, true
	case "i16":
		return KindI16, true
	case "i32":
		return KindI32, true
	case "i64":
		return KindI64, true
	case "u8":
		return KindU8, true
	case "u16":
		return KindU16, true
	case "u32":
		return KindU32, true
	case "u64":
		return KindU64, true
	}
	return KindUnknown, false
}

func (k Kind) String() string {
	switch k {
	case KindI8:
		return "i8"
	case KindI16:
		return "i16"
	case KindI32:
		return "i32"
	case KindI64:
		return "i64"
	case KindU8:
		return "u8"
	case KindU16:
		return "u16"
	case KindU32:
		return "u32"
	case KindU64:
		return "u64"
	case KindFloat:
		return "float"
	case KindBool:
		return "bool"
	case KindChar:
		return "char"
	case KindString:
		return "string"
	case KindUnit:
		return "unit"
	case KindRef:
		return "ref"
	}
	return "unknown"
}

// ConstKind says what a constant holds.
type ConstKind uint8

const (
	// ConstInt is an integer.
	ConstInt ConstKind = iota
	// ConstFloat is a float, stored as its IEEE bits.
	ConstFloat
	// ConstChar is a Unicode scalar value.
	ConstChar
	// ConstString is text, interned into the heap on first use.
	ConstString
)

// Const is a constant-pool entry.
type Const struct {
	Kind ConstKind
	Bits uint64
	Str  string
}

// CastKind names a primitive conversion, resolved at compile time from the `as` matrix
// in spec/04-expressions.md.
type CastKind uint8

const (
	// CastIntTrunc truncates an integer to a narrower width; it never traps.
	CastIntTrunc CastKind = iota
	// CastIntToFloat converts an integer to a float.
	CastIntToFloat
	// CastFloatToInt truncates toward zero and traps when out of range or NaN.
	CastFloatToInt
	// CastFloatNarrow rounds f64 to f32.
	CastFloatNarrow
	// CastFloatWiden is exact.
	CastFloatWiden
	// CastBoolToInt yields 0 or 1.
	CastBoolToInt
	// CastCharToInt yields the scalar value.
	CastCharToInt
)

// Fn is a compiled function.
type Fn struct {
	Name string
	// Params is how many arguments the function takes.
	Params int
	// Locals is how many local slots the frame needs, including parameters.
	Locals int
	// Captures is how many captured values a closure over this function holds.
	Captures int
	Code     []Instr
	// ParamKinds and CaptureKinds are each parameter's and each capture's static kind,
	// parallel to Params and Captures. The IR builder gives internal/ir.OpParam and
	// OpCapture their Kind from here (ADR-0021): a parameter or a capture is an entry
	// value with no producing instruction of its own for a Kind to travel with any
	// other way. Empty for a Fn no native build ever reaches (a compiler-provided impl
	// with no body); every Fn internal/compile emits carries both, in full.
	ParamKinds   []Kind
	CaptureKinds []Kind
	// Span is the declaration's span, used when a trap has no better location.
	Span diag.Span
}

// Program is a whole compiled program.
type Program struct {
	Fns    []*Fn
	Consts []Const
	// Types is the layout registry the heap and this program share.
	Types *layout.Registry
	// Entry is the index of `main`.
	Entry int
	// VariantTags maps a variant constant index to its enum's type id and tag.
	Variants []VariantInfo
	// Structs maps a struct constant index to its type id.
	Structs []layout.TypeID
	// StringType is the descriptor id for `String`.
	StringType layout.TypeID
	// Closures describes each closure-creation site: which function it invokes, and
	// the exact layout of its captures (ADR-0019). Indexed directly by OpClosure's A
	// operand, the same way Variants is indexed by OpVariant's.
	Closures []ClosureInfo
	// FnBoxType is the descriptor for a captureless closure that wraps a bare
	// top-level function reference. A path naming a function evaluates to TagFn -- an
	// index, not a heap object -- but a struct field or tuple slot of function type
	// has one Fixed word that must always hold the same kind of value, so the VM boxes
	// a TagFn into one of these before writing it there (ADR-0019).
	FnBoxType layout.TypeID
	// Prelude records the variant indices the VM needs to build values of its own:
	// `Option` for the checked-arithmetic methods, `Ordering` for `cmp`. They are
	// resolved at compile time so the VM never looks anything up by name.
	Prelude PreludeVariants
}

// PreludeVariants names the prelude variants the VM constructs directly.
type PreludeVariants struct {
	OptionSome int
	OptionNone int
	Less       int
	Equal      int
	Greater    int
	// Found reports whether the prelude supplied all of them.
	Found bool
}

// VariantInfo describes one enum variant to the VM.
type VariantInfo struct {
	Type layout.TypeID
	// Tag is the variant's index within its enum.
	Tag int
	// Payload is how many payload values the variant carries.
	Payload int
	// Name is used in diagnostics and disassembly.
	Name string
}

// ClosureInfo describes one closure-creation site.
type ClosureInfo struct {
	// FnIndex is the closure's body, an index into Program.Fns.
	FnIndex int
	// Type is the exact layout of the closure object: slot 0 is always the function
	// index, and the rest are the captures, in capture order (ADR-0019).
	Type layout.TypeID
}

// Disassemble renders a program as text. Snapshot tests compare its output, which is
// the "snapshot test of the generated IR" process rule 2 requires.
func (p *Program) Disassemble() string {
	var sb strings.Builder
	for i, fn := range p.Fns {
		fmt.Fprintf(&sb, "fn %d %s(params=%d locals=%d captures=%d)\n",
			i, fn.Name, fn.Params, fn.Locals, fn.Captures)
		for pc, in := range fn.Code {
			fmt.Fprintf(&sb, "  %4d  %s", pc, in.Op)
			switch in.Op {
			case OpConst:
				fmt.Fprintf(&sb, " %d  ; %s", in.A, p.constText(int(in.A)))
			case OpVariant:
				if int(in.A) < len(p.Variants) {
					fmt.Fprintf(&sb, " %d %d  ; %s", in.A, in.B, p.Variants[in.A].Name)
				}
			case OpIsVariant:
				if int(in.A) < len(p.Variants) {
					fmt.Fprintf(&sb, " %d  ; %s", in.A, p.Variants[in.A].Name)
				}
			case OpCall:
				fmt.Fprintf(&sb, " %d  ; kind=%s", in.A, in.Kind)
			case OpStruct, OpTuple, OpCallBuiltin, OpClosure:
				fmt.Fprintf(&sb, " %d %d", in.A, in.B)
			case OpGetField, OpSetField, OpGetPayload, OpGetTupleElem:
				fmt.Fprintf(&sb, " %d  ; kind=%s", in.A, in.Kind)
			case OpLoad, OpStore, OpLoadCapture, OpJump, OpJumpIfFalse, OpJumpIfTrue,
				OpFunc, OpCast, OpTrap:
				fmt.Fprintf(&sb, " %d", in.A)
			}
			sb.WriteByte('\n')
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

func (p *Program) constText(i int) string {
	if i < 0 || i >= len(p.Consts) {
		return "?"
	}
	c := p.Consts[i]
	switch c.Kind {
	case ConstString:
		return fmt.Sprintf("%q", c.Str)
	case ConstFloat:
		return "float"
	case ConstChar:
		return fmt.Sprintf("%q", rune(c.Bits))
	default:
		return fmt.Sprint(int64(c.Bits))
	}
}
