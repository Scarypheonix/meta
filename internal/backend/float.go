package backend

import "github.com/scarypheonix/meta/internal/x86"

// Float remainder, in machine code (spec/04-expressions.md).
//
// SSE has no remainder instruction. x87's `fprem` is the only hardware form, and pulling the
// x87 stack in for one operation was not worth it in Phase 5 — so `%` on floats worked on the
// interpreter and the virtual machine (Go's `math.Mod`) and failed to build natively, loudly,
// for four phases (docs/deferred.md).
//
// What closes it is not `fprem` and not Frexp/Ldexp, but the observation that the remainder is
// *unique*: for finite x and non-zero y there is exactly one r with |r| < |y|, sign(r) =
// sign(x) and x - r a multiple of y. Any exact algorithm therefore agrees with `math.Mod` bit
// for bit, and repeated halving is exact:
//
//	a, d = |x|, |y|
//	scaled = d;  while scaled*2 <= a { scaled *= 2 }
//	while scaled >= d { if a >= scaled { a -= scaled }; scaled /= 2 }
//
// Every doubling and every halving is a change to the exponent alone, so no bit of the
// significand is ever rounded away — including when d is subnormal, where halving retraces
// exactly the values doubling produced. `scaled *= 2` overflowing to +Inf ends the first loop
// rather than misbehaving, since `Inf <= a` is false for finite a.
//
// The optimizer's own constant folder has been running this algorithm in Origin since stage1's
// `opt.origin` was written, held to `math.Mod` by the `-O2` differential.

// nanBits is the quiet NaN Go's math.NaN() returns. The other two engines produce it from
// math.Mod, and `float::bits` can see which NaN a program got, so native code produces the
// same one.
const nanBits = 0x7FF8000000000001

// twoBits is 2.0.
const twoBits = 0x4000000000000000

// emitFloatMod writes `rt_float_mod(x rdi, y rsi) -> rax`, all three raw f64 bits.
//
// A leaf: it calls nothing, allocates nothing and touches only scratch, so `%` on floats costs
// a call and no safepoint.
func (e *emitter) emitFloatMod() {
	a := e.a
	a.Align(16)
	a.Bind(e.rt.floatMod)

	retNaN := a.NewLabel("fmod_nan")
	retX := a.NewLabel("fmod_x")
	scaleUp := a.NewLabel("fmod_scale_up")
	scaled := a.NewLabel("fmod_scaled")
	reduce := a.NewLabel("fmod_reduce")
	skip := a.NewLabel("fmod_skip")
	done := a.NewLabel("fmod_done")

	// |x| and |y|, as bit patterns. A magnitude comparison on the bits is the same ordering
	// as on the values for non-negative finite floats, which is what makes the three
	// classification tests below plain integer compares.
	a.MovRI(x86.RAX, ^uint64(1<<63))
	a.MovRR(scratchA, x86.RDI)
	a.AndRR(scratchA, x86.RAX) // |x|
	a.MovRR(scratchB, x86.RSI)
	a.AndRR(scratchB, x86.RAX) // |y|

	// Anything at or above the infinity pattern is Inf or NaN.
	a.MovRI(x86.RCX, 0x7FF0000000000000)
	a.CmpRR(scratchA, x86.RCX)
	a.Jcc(x86.AboveEqual, retNaN) // x is Inf or NaN
	a.CmpRR(scratchB, x86.RCX)
	a.Jcc(x86.Above, retNaN) // y is NaN
	a.Jcc(x86.Equal, retX)   // y is Inf: every finite x is its own remainder
	a.TestRR(scratchB, scratchB)
	a.Jcc(x86.Equal, retNaN) // y is zero
	a.CmpRR(scratchA, scratchB)
	a.Jcc(x86.Below, retX) // |x| < |y|: x is its own remainder, sign and all

	a.MovqXR(x86.XMM0, scratchA) // a = |x|
	a.MovqXR(x86.XMM1, scratchB) // d = |y|
	a.MovsdXX(x86.XMM2, x86.XMM1)
	a.MovRI(x86.RCX, twoBits)
	a.MovqXR(x86.XMM3, x86.RCX)

	// The largest d*2^k that is no greater than a.
	a.Bind(scaleUp)
	a.MovsdXX(x86.XMM4, x86.XMM2)
	a.MulsdXX(x86.XMM4, x86.XMM3)
	a.UcomisdXX(x86.XMM4, x86.XMM0)
	a.Jcc(x86.Above, scaled)
	a.MovsdXX(x86.XMM2, x86.XMM4)
	a.Jmp(scaleUp)
	a.Bind(scaled)

	// Then back down, subtracting where it fits. Neither operand can be NaN here, so the
	// unordered case ucomisd would signal with the parity flag cannot arise.
	a.Bind(reduce)
	a.UcomisdXX(x86.XMM2, x86.XMM1)
	a.Jcc(x86.Below, done)
	a.UcomisdXX(x86.XMM0, x86.XMM2)
	a.Jcc(x86.Below, skip)
	a.SubsdXX(x86.XMM0, x86.XMM2)
	a.Bind(skip)
	a.DivsdXX(x86.XMM2, x86.XMM3)
	a.Jmp(reduce)

	a.Bind(done)
	// The remainder takes x's sign, which is what makes `a == (a/b)*b + (a%b)` hold. `a` is
	// non-negative, so its own sign bit is clear and an OR is enough.
	a.MovqRX(x86.RAX, x86.XMM0)
	a.MovRI(x86.RCX, 1<<63)
	a.AndRR(x86.RCX, x86.RDI)
	a.OrRR(x86.RAX, x86.RCX)
	a.Ret()

	a.Bind(retX)
	a.MovRR(x86.RAX, x86.RDI)
	a.Ret()

	a.Bind(retNaN)
	a.MovRI(x86.RAX, nanBits)
	a.Ret()
}
