package arith

import (
	"math"
	"math/big"
	"testing"

	"github.com/scarypheonix/meta/internal/bytecode"
)

// neg is the bit pattern of -n, spelled so the tests can name negative values without
// fighting Go's untyped-constant rules at every call.
func neg(n int64) uint64 { return uint64(-n) }

// allKinds is every integer type Origin has.
var allKinds = []bytecode.Kind{
	bytecode.KindI8, bytecode.KindI16, bytecode.KindI32, bytecode.KindI64,
	bytecode.KindU8, bytecode.KindU16, bytecode.KindU32, bytecode.KindU64,
}

// exact computes the operation in arbitrary precision, which is the oracle: this package's
// job is to agree with mathematics and to say so when it cannot.
func exact(k bytecode.Kind, op string, a, b uint64) *big.Int {
	x, y := asBig(k, a), asBig(k, b)
	switch op {
	case "+":
		return new(big.Int).Add(x, y)
	case "-":
		return new(big.Int).Sub(x, y)
	case "*":
		return new(big.Int).Mul(x, y)
	}
	panic("unknown op " + op)
}

func asBig(k bytecode.Kind, v uint64) *big.Int {
	if k.IsSigned() {
		return big.NewInt(int64(v))
	}
	return new(big.Int).SetUint64(v)
}

func inRange(k bytecode.Kind, v *big.Int) bool {
	return v.Cmp(asBig(k, Min(k))) >= 0 && v.Cmp(asBig(k, Max(k))) <= 0
}

// samples are the values worth trying for a kind: its bounds, the values next to them,
// and a few ordinary ones. Exhaustive testing is impossible at 64 bits and unnecessary --
// every bug in this kind of code is at a boundary.
func samples(k bytecode.Kind) []uint64 {
	out := []uint64{0, 1, 2, Max(k), Min(k)}
	if Max(k) > 0 {
		out = append(out, Max(k)-1, Max(k)/2)
	}
	if k.IsSigned() {
		out = append(out, Wrap(k, ^uint64(0)), Wrap(k, Min(k)+1), Wrap(k, neg(2)))
	} else if Max(k) > 2 {
		out = append(out, Max(k)/2+1)
	}
	return out
}

// TestAddSubMulAgreeWithArbitraryPrecision is the whole point of the package: at every
// width, the answer is the mathematical one when it fits and a trap when it does not.
func TestAddSubMulAgreeWithArbitraryPrecision(t *testing.T) {
	for _, k := range allKinds {
		for _, a := range samples(k) {
			for _, b := range samples(k) {
				for _, op := range []string{"+", "-", "*"} {
					want := exact(k, op, a, b)
					var got uint64
					var trap string
					switch op {
					case "+":
						got, trap = Add(k, a, b)
					case "-":
						got, trap = Sub(k, a, b)
					case "*":
						got, trap = Mul(k, a, b)
					}
					if !inRange(k, want) {
						if trap != Overflow {
							t.Errorf("%s: %d %s %d = %v, which does not fit; got %d with trap %q",
								k, asBig(k, a), op, asBig(k, b), want, got, trap)
						}
						continue
					}
					if trap != "" {
						t.Errorf("%s: %d %s %d = %v, which fits; got trap %q",
							k, asBig(k, a), op, asBig(k, b), want, trap)
						continue
					}
					if asBig(k, got).Cmp(want) != 0 {
						t.Errorf("%s: %d %s %d = %v, got %v",
							k, asBig(k, a), op, asBig(k, b), want, asBig(k, got))
					}
				}
			}
		}
	}
}

// Every result an operation produces is a value of its own kind: in range, and extended to
// sixty-four bits the way the kind says. That invariant is what lets a comparison ignore
// the width entirely.
func TestEveryResultIsInRangeForItsKind(t *testing.T) {
	for _, k := range allKinds {
		for _, a := range samples(k) {
			for _, b := range samples(k) {
				check := func(name string, v uint64, trap string) {
					if trap != "" {
						return
					}
					if !Fits(k, v) {
						t.Errorf("%s: %s(%d, %d) produced %#x, which is not a %s", k, name, a, b, v, k)
					}
				}
				v, trap := Add(k, a, b)
				check("Add", v, trap)
				v, trap = Sub(k, a, b)
				check("Sub", v, trap)
				v, trap = Mul(k, a, b)
				check("Mul", v, trap)
				v, trap = Div(k, a, b)
				check("Div", v, trap)
				v, trap = Rem(k, a, b)
				check("Rem", v, trap)
				check("And", And(k, a, b), "")
				check("Or", Or(k, a, b), "")
				check("Xor", Xor(k, a, b), "")
				for _, amt := range []uint64{0, 1, uint64(k.Bits()) - 1} {
					v, trap = Shl(k, a, amt)
					check("Shl", v, trap)
					v, trap = Shr(k, a, amt)
					check("Shr", v, trap)
				}
				for _, op := range []Op{OpAdd, OpSub, OpMul} {
					check("Saturating", Saturating(k, op, a, b), "")
					if got, ok := Checked(k, op, a, b); ok {
						check("Checked", got, "")
					}
				}
			}
		}
	}
}

// spec/04-expressions.md's own worked-examples rows, spelled out.
func TestSpecifiedExamples(t *testing.T) {
	u8, i8, u32, u64, i64 := bytecode.KindU8, bytecode.KindI8, bytecode.KindU32, bytecode.KindU64, bytecode.KindI64

	if _, trap := Add(u8, 255, 1); trap != Overflow {
		t.Errorf("255u8 + 1 gave %q, want a trap", trap)
	}
	if v, trap := Add(u32, 255, 1); trap != "" || v != 256 {
		t.Errorf("255u32 + 1 = %d %q, want 256", v, trap)
	}
	if _, trap := Add(i8, 127, 1); trap != Overflow {
		t.Errorf("127i8 + 1 gave %q, want a trap", trap)
	}
	if _, trap := Sub(i8, Wrap(i8, neg(128)), 1); trap != Overflow {
		t.Errorf("-128i8 - 1 gave %q, want a trap", trap)
	}
	if _, trap := Neg(i8, Min(i8)); trap != Overflow {
		t.Errorf("-(-128i8) gave %q, want a trap", trap)
	}
	if v := Wrap(u8, 256); v != 0 {
		t.Errorf("255u8.wrapping_add(1) = %d, want 0", v)
	}
	if v := Saturating(u8, OpAdd, 255, 1); v != 255 {
		t.Errorf("255u8.saturating_add(1) = %d, want 255", v)
	}
	if _, ok := Checked(u8, OpAdd, 255, 1); ok {
		t.Error("255u8.checked_add(1) succeeded, want None")
	}
	if _, trap := Mul(u8, 200, 2); trap != Overflow {
		t.Errorf("200u8 * 2 gave %q, want a trap", trap)
	}
	if _, trap := Div(i8, Min(i8), Wrap(i8, neg(1))); trap != Overflow {
		t.Errorf("-128i8 / -1 gave %q, want a trap", trap)
	}
	if v, trap := Shl(u8, 1, 7); trap != "" || v != 128 {
		t.Errorf("1u8 << 7 = %d %q, want 128", v, trap)
	}
	if _, trap := Shl(u8, 3, 7); trap != Overflow {
		t.Errorf("3u8 << 7 gave %q, want a trap", trap)
	}
	if _, trap := Shl(u8, 1, 8); trap != ShiftWide {
		t.Errorf("1u8 << 8 gave %q, want a shift trap", trap)
	}
	if Max(u64) != math.MaxUint64 {
		t.Errorf("u64::MAX = %d, want %d", Max(u64), uint64(math.MaxUint64))
	}
	if _, trap := Add(u64, Max(u64), 1); trap != Overflow {
		t.Errorf("u64::MAX + 1 gave %q, want a trap", trap)
	}
	if v, _ := Div(u64, Max(u64), 2); v != math.MaxInt64 {
		t.Errorf("u64::MAX / 2 = %d, want %d", v, uint64(math.MaxInt64))
	}
	if !Less(u64, 1, Max(u64)) {
		t.Error("u64::MAX > 1 was false: the comparison used signed order")
	}
	if Less(i64, Wrap(i64, Max(u64)), 1) != true {
		t.Error("the same bits read as i64 are -1, which is less than 1")
	}
}

// `>>` is arithmetic on a signed type and logical on an unsigned one, which is the whole
// reason the kind travels with a shift.
func TestShiftRightFollowsSignedness(t *testing.T) {
	minus8 := Wrap(bytecode.KindI64, neg(8))
	if v, _ := Shr(bytecode.KindI64, minus8, 1); int64(v) != -4 {
		t.Errorf("-8i64 >> 1 = %d, want -4", int64(v))
	}
	if v, _ := Shr(bytecode.KindU64, minus8, 1); v != math.MaxUint64/2-3 {
		t.Errorf("the same bits as u64 >> 1 = %d, want a logical shift", v)
	}
	if v, _ := Shr(bytecode.KindI8, Wrap(bytecode.KindI8, neg(8)), 1); int64(v) != -4 {
		t.Errorf("-8i8 >> 1 = %d, want -4", int64(v))
	}
	if v, _ := Shr(bytecode.KindU8, 200, 1); v != 100 {
		t.Errorf("200u8 >> 1 = %d, want 100", v)
	}
}

// Saturating clamps to the right end, which the wrapped answer cannot say.
func TestSaturatingPicksTheEndItRanOffAt(t *testing.T) {
	i8, u8 := bytecode.KindI8, bytecode.KindU8
	if v := Saturating(i8, OpAdd, 127, 1); int64(v) != 127 {
		t.Errorf("127i8 saturating+ 1 = %d, want 127", int64(v))
	}
	if v := Saturating(i8, OpSub, Min(i8), 1); int64(v) != -128 {
		t.Errorf("-128i8 saturating- 1 = %d, want -128", int64(v))
	}
	if v := Saturating(i8, OpMul, Wrap(i8, neg(100)), 100); int64(v) != -128 {
		t.Errorf("-100i8 saturating* 100 = %d, want -128", int64(v))
	}
	if v := Saturating(i8, OpMul, 100, 100); int64(v) != 127 {
		t.Errorf("100i8 saturating* 100 = %d, want 127", int64(v))
	}
	if v := Saturating(u8, OpSub, 1, 2); v != 0 {
		t.Errorf("1u8 saturating- 2 = %d, want 0", v)
	}
	if v := Saturating(bytecode.KindU64, OpAdd, Max(bytecode.KindU64), 1); v != Max(bytecode.KindU64) {
		t.Errorf("u64::MAX saturating+ 1 = %d, want u64::MAX", v)
	}
}
