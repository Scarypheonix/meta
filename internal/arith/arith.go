// Package arith is integer arithmetic at a declared width: one definition of what
// `+`, `/`, `wrapping_add` and the rest mean for each of Origin's eight integer types.
//
// It exists because three engines have to agree (spec/04-expressions.md). Two of them are
// Go and share this code outright; the third is hand-written machine code that mirrors it,
// and `tests/e2e` is what holds the mirror honest. Before this package the two Go engines
// had a `checkedOp` and a `saturate` each, both 64-bit only, and both silently wrong for
// every type narrower than `i64` -- which is exactly the shape of duplication CLAUDE.md's
// rule 5 is about.
//
// # Representation
//
// A value is a `uint64` bit pattern, and its Kind says how to read it. A signed value is
// two's complement **sign-extended** to sixty-four bits; an unsigned one is
// **zero-extended**. That is an invariant, not a convention: a value of an integer type is
// always representable in that type, because an operation that would leave one that is not
// traps instead (spec/04-expressions.md). Every function here preserves it, and `Wrap` is
// how an operation that deliberately does not trap re-establishes it.
//
// Carrying `uint64` rather than `int64` is what lets `u64` hold values above `i64::MAX` at
// all: they are ordinary bit patterns here, and only the interpretation differs.
package arith

import (
	"math/bits"

	"github.com/scarypheonix/meta/internal/bytecode"
)

// The trap messages, spelled once. spec/04-expressions.md's trap table is the source, and
// every engine prints these bytes exactly.
const (
	Overflow  = "arithmetic overflow"
	DivZero   = "divide by zero"
	RemZero   = "remainder by zero"
	ShiftWide = "shift amount out of range"
)

// Wrap re-reads v as a value of kind k: it discards the bits above k's width and extends
// what is left back to sixty-four bits, signed or unsigned as k says.
//
// This is two's-complement wraparound, which is what `wrapping_add` means and what `as`
// does to a narrowing cast. Applying it to a value already in range leaves it unchanged,
// which is why it is safe to apply everywhere the invariant might be in doubt.
func Wrap(k bytecode.Kind, v uint64) uint64 {
	w := k.Bits()
	if w == 64 {
		return v
	}
	v &= (uint64(1) << w) - 1
	if k.IsSigned() && v&(uint64(1)<<(w-1)) != 0 {
		v |= ^((uint64(1) << w) - 1) // sign-extend
	}
	return v
}

// Fits reports whether the exact value v -- read as a signed or unsigned 64-bit number
// according to k -- is representable in k.
func Fits(k bytecode.Kind, v uint64) bool { return Wrap(k, v) == v }

// Max is the largest value of kind k, as its own bit pattern.
func Max(k bytecode.Kind) uint64 {
	w := k.Bits()
	if k.IsSigned() {
		return (uint64(1) << (w - 1)) - 1
	}
	if w == 64 {
		return ^uint64(0)
	}
	return (uint64(1) << w) - 1
}

// Min is the smallest value of kind k, as its own bit pattern: sign-extended for a signed
// kind, and zero for every unsigned one.
func Min(k bytecode.Kind) uint64 {
	if !k.IsSigned() {
		return 0
	}
	return Wrap(k, uint64(1)<<(k.Bits()-1))
}

// Less compares two values of kind k, signed or unsigned as k says.
func Less(k bytecode.Kind, a, b uint64) bool {
	if k.IsSigned() {
		return int64(a) < int64(b)
	}
	return a < b
}

// Add, Sub and Mul return the result and the trap message, which is "" when there is none.
//
// The arithmetic is done at full width and the *result* is checked against k, which is
// correct for every width because the operands are in range by the invariant: two values
// of a type narrower than 64 bits cannot make a 64-bit computation itself overflow.
func Add(k bytecode.Kind, a, b uint64) (uint64, string) {
	s := a + b
	if k.Bits() == 64 {
		if k.IsSigned() {
			if (int64(a) >= 0) == (int64(b) >= 0) && (int64(s) >= 0) != (int64(a) >= 0) {
				return 0, Overflow
			}
			return s, ""
		}
		if s < a { // an unsigned carry out of the top bit
			return 0, Overflow
		}
		return s, ""
	}
	if !Fits(k, s) {
		return 0, Overflow
	}
	return s, ""
}

func Sub(k bytecode.Kind, a, b uint64) (uint64, string) {
	d := a - b
	if k.Bits() == 64 {
		if k.IsSigned() {
			if (int64(a) >= 0) != (int64(b) >= 0) && (int64(d) >= 0) != (int64(a) >= 0) {
				return 0, Overflow
			}
			return d, ""
		}
		if b > a {
			return 0, Overflow
		}
		return d, ""
	}
	if !Fits(k, d) {
		return 0, Overflow
	}
	return d, ""
}

func Mul(k bytecode.Kind, a, b uint64) (uint64, string) {
	if k.Bits() == 64 {
		if k.IsSigned() {
			x, y := int64(a), int64(b)
			p := x * y
			if x != 0 && (p/x != y || (x == -1 && y == minInt64) || (y == -1 && x == minInt64)) {
				return 0, Overflow
			}
			return uint64(p), ""
		}
		hi, lo := bits.Mul64(a, b)
		if hi != 0 {
			return 0, Overflow
		}
		return lo, ""
	}
	// Narrower than the machine word, so the full product is exact in 64 bits and the
	// only question is whether it fits in k.
	var p uint64
	if k.IsSigned() {
		p = uint64(int64(a) * int64(b))
	} else {
		p = a * b
	}
	if !Fits(k, p) {
		return 0, Overflow
	}
	return p, ""
}

const minInt64 = -1 << 63

// Div and Rem truncate toward zero, so `a == (a/b)*b + (a%b)` holds whenever neither side
// traps (spec/04-expressions.md). Division by zero traps, and so does the one signed
// division whose result is not representable: `MIN / -1`, at every width.
func Div(k bytecode.Kind, a, b uint64) (uint64, string) {
	if b == 0 {
		return 0, DivZero
	}
	if !k.IsSigned() {
		return a / b, ""
	}
	x, y := int64(a), int64(b)
	if uint64(x) == Min(k) && y == -1 {
		return 0, Overflow
	}
	return uint64(x / y), ""
}

func Rem(k bytecode.Kind, a, b uint64) (uint64, string) {
	if b == 0 {
		return 0, RemZero
	}
	if !k.IsSigned() {
		return a % b, ""
	}
	x, y := int64(a), int64(b)
	if uint64(x) == Min(k) && y == -1 {
		return 0, Overflow
	}
	return uint64(x % y), ""
}

// Neg traps on the one operand whose negation is not representable, which is k's minimum.
// It is REJECTED on an unsigned type before it reaches here (spec/04-expressions.md).
func Neg(k bytecode.Kind, a uint64) (uint64, string) {
	if a == Min(k) {
		return 0, Overflow
	}
	return uint64(-int64(a)), ""
}

// Shl traps on an amount at or past k's width, and on a result that does not fit in k.
// Shr traps only on the amount: it can never overflow.
//
// `>>` is arithmetic on a signed type and logical on an unsigned one, which is the same
// distinction `Less` makes and the reason the kind has to be here at all.
func Shl(k bytecode.Kind, a, amount uint64) (uint64, string) {
	if amount >= uint64(k.Bits()) {
		return 0, ShiftWide
	}
	s := a << amount
	if k.Bits() < 64 {
		if !Fits(k, s) {
			return 0, Overflow
		}
		return s, ""
	}
	// At full width, the shift overflowed exactly when shifting back does not recover
	// the operand -- which is the definition, and which works for both signednesses.
	if k.IsSigned() {
		if int64(s)>>amount != int64(a) {
			return 0, Overflow
		}
	} else if s>>amount != a {
		return 0, Overflow
	}
	return s, ""
}

func Shr(k bytecode.Kind, a, amount uint64) (uint64, string) {
	if amount >= uint64(k.Bits()) {
		return 0, ShiftWide
	}
	if k.IsSigned() {
		return uint64(int64(a) >> amount), ""
	}
	return a >> amount, ""
}

// And, Or and Xor cannot overflow: every bit pattern they can produce is one the operands
// already had, so a value in range stays in range.
func And(k bytecode.Kind, a, b uint64) uint64 { return a & b }
func Or(k bytecode.Kind, a, b uint64) uint64  { return a | b }
func Xor(k bytecode.Kind, a, b uint64) uint64 { return a ^ b }

// Checked is `checked_add` and its two siblings: the result, or false when it does not fit
// in k. Overflow is the only failure it reports, so a division is not one of these.
func Checked(k bytecode.Kind, op Op, a, b uint64) (uint64, bool) {
	v, trap := apply(k, op, a, b)
	return v, trap == ""
}

// Saturating clamps to k's own bounds rather than trapping. Which bound depends on the
// sign of the *true* result, which the wrapped answer is exactly what cannot say -- so it
// is read off the operands.
func Saturating(k bytecode.Kind, op Op, a, b uint64) uint64 {
	if v, trap := apply(k, op, a, b); trap == "" {
		return v
	}
	if negativeOverflow(k, op, a, b) {
		return Min(k)
	}
	return Max(k)
}

// negativeOverflow reports which end an overflowing operation ran off.
func negativeOverflow(k bytecode.Kind, op Op, a, b uint64) bool {
	if !k.IsSigned() {
		// An unsigned type only runs off the bottom by subtracting too much.
		return op == OpSub
	}
	x, y := int64(a), int64(b)
	switch op {
	case OpAdd:
		return x < 0 // the operands share a sign, and the true sum has it
	case OpSub:
		return x < 0 // the operands differ in sign, and the true difference has a's
	default:
		return (x < 0) != (y < 0) // a product is negative when the signs differ
	}
}

// Op names the three operations that have checked and saturating forms.
type Op uint8

const (
	OpAdd Op = iota
	OpSub
	OpMul
)

func apply(k bytecode.Kind, op Op, a, b uint64) (uint64, string) {
	switch op {
	case OpAdd:
		return Add(k, a, b)
	case OpSub:
		return Sub(k, a, b)
	}
	return Mul(k, a, b)
}
