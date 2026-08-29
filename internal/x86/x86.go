// Package x86 encodes x86-64 machine code.
//
// It is the bottom of the backend: everything above it decides *what* to emit, and this
// decides what bytes that is. There is no external assembler (ADR-0017), so an encoding
// mistake here is a mistake nothing else will catch — which is why every instruction the
// package emits is checked byte for byte against a disassembler in the tests.
//
// The encoder is deliberately explicit. There is no general operand type and no
// instruction-selection cleverness: one method per (instruction, operand shape) the
// backend actually needs. A wrong encoding is then a wrong constant in one method,
// rather than an emergent property of a table.
//
// Only the baseline of docs/spec/11-codegen.md is emitted: the target is a Broadwell
// i5-5350U, and code that cannot run on the machine the differential tests run on cannot
// be tested by running it.
package x86

import "fmt"

// Reg is a general-purpose register, numbered as the instruction encoding numbers them.
type Reg uint8

// The general-purpose registers.
const (
	RAX Reg = iota
	RCX
	RDX
	RBX
	RSP
	RBP
	RSI
	RDI
	R8
	R9
	R10
	R11
	R12
	R13
	R14
	R15
)

var regNames = [...]string{
	"rax", "rcx", "rdx", "rbx", "rsp", "rbp", "rsi", "rdi",
	"r8", "r9", "r10", "r11", "r12", "r13", "r14", "r15",
}

func (r Reg) String() string {
	if int(r) < len(regNames) {
		return regNames[r]
	}
	return fmt.Sprintf("r?%d", uint8(r))
}

// Xmm is an SSE register, used for f32 and f64 values.
type Xmm uint8

// The SSE registers.
const (
	XMM0 Xmm = iota
	XMM1
	XMM2
	XMM3
	XMM4
	XMM5
	XMM6
	XMM7
	XMM8
	XMM9
	XMM10
	XMM11
	XMM12
	XMM13
	XMM14
	XMM15
)

func (x Xmm) String() string { return fmt.Sprintf("xmm%d", uint8(x)) }

// Cond is a condition code, numbered as the `jcc`, `setcc` and `cmovcc` opcodes number
// them: the opcode is a base plus the condition.
type Cond uint8

// The condition codes. Signed and unsigned comparisons are distinct instructions, so
// the caller picks: Origin's `<` on `u64` is Below, on `i64` it is Less.
const (
	Overflow     Cond = 0x0
	NoOverflow   Cond = 0x1
	Below        Cond = 0x2
	AboveEqual   Cond = 0x3
	Equal        Cond = 0x4
	NotEqual     Cond = 0x5
	BelowEqual   Cond = 0x6
	Above        Cond = 0x7
	Sign         Cond = 0x8
	NoSign       Cond = 0x9
	Parity       Cond = 0xA
	NoParity     Cond = 0xB
	Less         Cond = 0xC
	GreaterEqual Cond = 0xD
	LessEqual    Cond = 0xE
	Greater      Cond = 0xF
)

var condNames = [...]string{
	"o", "no", "b", "ae", "e", "ne", "be", "a",
	"s", "ns", "p", "np", "l", "ge", "le", "g",
}

func (c Cond) String() string {
	if int(c) < len(condNames) {
		return condNames[c]
	}
	return fmt.Sprintf("cc?%d", uint8(c))
}

// Invert returns the condition that is true exactly when c is false. Emitting `jne` to
// skip a `then` block is how a two-way branch becomes one conditional jump, so the
// backend asks for this constantly.
func (c Cond) Invert() Cond { return c ^ 1 }

// Mem is a memory operand: [Base + Disp]. It is the only addressing form the backend
// needs — a frame slot, a field at a constant offset, or a runtime-block member — and
// leaving out index-scale addressing keeps the encoder small enough to verify.
type Mem struct {
	Base Reg
	Disp int32
}

// At builds a memory operand.
func At(base Reg, disp int32) Mem { return Mem{Base: base, Disp: disp} }

// Asm accumulates encoded instructions.
//
// Addresses are known while emitting (ADR-0017: the executable is laid out statically
// and has no relocations), but a forward jump's target is not known when the jump is
// emitted. Labels close that gap: a jump to an unbound label records a fixup, and
// binding the label patches every fixup that names it.
type Asm struct {
	code   []byte
	labels []label
	fixups []fixup
	// base is the virtual address the first emitted byte will live at, so that a
	// RIP-relative reference can be computed against the final layout.
	base uint64
}

type label struct {
	name  string
	bound bool
	at    int
}

type fixup struct {
	// at is the offset of the 4-byte field to patch.
	at int
	// end is the offset of the instruction after this one: a rel32 is relative to it.
	end   int
	label Label
}

// Label names a position in the instruction stream.
type Label int

// New returns an assembler whose first byte will be loaded at the given virtual address.
func New(base uint64) *Asm { return &Asm{base: base} }

// Len is how many bytes have been emitted.
func (a *Asm) Len() int { return len(a.code) }

// PC is the virtual address of the next byte to be emitted.
func (a *Asm) PC() uint64 { return a.base + uint64(len(a.code)) }

// Code returns the encoded bytes. It panics if any label is still unbound, because a
// jump into nowhere is not something to discover at run time.
func (a *Asm) Code() []byte {
	for _, f := range a.fixups {
		if !a.labels[f.label].bound {
			panic(fmt.Sprintf("label %q is never bound", a.labels[f.label].name))
		}
	}
	return a.code
}

// NewLabel creates an unbound label.
func (a *Asm) NewLabel(name string) Label {
	a.labels = append(a.labels, label{name: name})
	return Label(len(a.labels) - 1)
}

// Bind fixes a label at the current position and patches every jump to it.
func (a *Asm) Bind(l Label) {
	if a.labels[l].bound {
		panic(fmt.Sprintf("label %q is bound twice", a.labels[l].name))
	}
	a.labels[l].bound = true
	a.labels[l].at = len(a.code)
	for _, f := range a.fixups {
		if f.label != l {
			continue
		}
		a.patch32(f.at, int32(a.labels[l].at-f.end))
	}
}

// Offset reports where a bound label sits in the instruction stream.
func (a *Asm) Offset(l Label) int {
	if !a.labels[l].bound {
		panic(fmt.Sprintf("label %q is not bound", a.labels[l].name))
	}
	return a.labels[l].at
}

// Addr reports the virtual address of a bound label.
func (a *Asm) Addr(l Label) uint64 { return a.base + uint64(a.Offset(l)) }

func (a *Asm) emit(b ...byte) { a.code = append(a.code, b...) }

func (a *Asm) imm32(v int32) {
	a.emit(byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
}

func (a *Asm) imm64(v uint64) {
	for i := 0; i < 8; i++ {
		a.emit(byte(v >> (8 * i)))
	}
}

func (a *Asm) patch32(at int, v int32) {
	a.code[at] = byte(v)
	a.code[at+1] = byte(v >> 8)
	a.code[at+2] = byte(v >> 16)
	a.code[at+3] = byte(v >> 24)
}

// ref32 emits a placeholder for a label-relative displacement and records the fixup. A
// label already bound is patched immediately, which is what makes a backward jump — a
// loop's back edge — need no second pass.
func (a *Asm) ref32(l Label) {
	at := len(a.code)
	a.imm32(0)
	end := len(a.code)
	if a.labels[l].bound {
		a.patch32(at, int32(a.labels[l].at-end))
		return
	}
	a.fixups = append(a.fixups, fixup{at: at, end: end, label: l})
}

// ---------------------------------------------------------------------------
// Encoding primitives
// ---------------------------------------------------------------------------

// rex emits a REX prefix when one is needed. w selects 64-bit operand size; r, x and b
// are the high bits of the register fields.
//
// force exists for the byte-operand instructions: `setcc spl` needs an empty REX prefix
// to mean spl rather than ah, and leaving it out silently writes a different register.
func (a *Asm) rex(w bool, r, x, b Reg, force bool) {
	v := byte(0)
	if w {
		v |= 0x08
	}
	if r >= R8 {
		v |= 0x04
	}
	if x >= R8 {
		v |= 0x02
	}
	if b >= R8 {
		v |= 0x01
	}
	if v != 0 || force {
		a.emit(0x40 | v)
	}
}

// modrmReg encodes a register-to-register operand pair (mod = 11).
func (a *Asm) modrmReg(reg, rm Reg) {
	a.emit(0xC0 | byte(reg&7)<<3 | byte(rm&7))
}

// modrmMem encodes [base + disp].
//
// Two cases in the encoding are not uniform and are the usual source of a wrong address:
// rm = 4 (rsp, r12) always needs a SIB byte, and rm = 5 (rbp, r13) has no disp-less form
// because that slot means RIP-relative, so it takes an explicit zero displacement.
func (a *Asm) modrmMem(reg Reg, m Mem) {
	base := m.Base
	needDisp8 := m.Disp != 0 || base&7 == 5
	mod := byte(0x00)
	switch {
	case !needDisp8:
		mod = 0x00
	case m.Disp >= -128 && m.Disp <= 127:
		mod = 0x40
	default:
		mod = 0x80
	}

	a.emit(mod | byte(reg&7)<<3 | byte(base&7))
	if base&7 == 4 {
		// SIB with no index: base only.
		a.emit(0x24)
	}
	switch mod {
	case 0x40:
		a.emit(byte(m.Disp))
	case 0x80:
		a.imm32(m.Disp)
	}
}

// op64rr emits a REX.W instruction with a register-register ModRM.
func (a *Asm) op64rr(opcode byte, reg, rm Reg) {
	a.rex(true, reg, 0, rm, false)
	a.emit(opcode)
	a.modrmReg(reg, rm)
}

// op64rm emits a REX.W instruction with a register-memory ModRM.
func (a *Asm) op64rm(opcode byte, reg Reg, m Mem) {
	a.rex(true, reg, 0, m.Base, false)
	a.emit(opcode)
	a.modrmMem(reg, m)
}

// ---------------------------------------------------------------------------
// Data movement
// ---------------------------------------------------------------------------

// MovRR is `mov dst, src`.
func (a *Asm) MovRR(dst, src Reg) {
	if dst == src {
		return
	}
	a.op64rr(0x89, src, dst)
}

// MovRI is `mov dst, imm64`, always in the ten-byte form so the instruction's length
// does not depend on the value. A backend that patches an immediate after the fact
// needs the length to be fixed; a peephole can shrink it later.
func (a *Asm) MovRI(dst Reg, imm uint64) {
	a.rex(true, 0, 0, dst, false)
	a.emit(0xB8 | byte(dst&7))
	a.imm64(imm)
}

// MovRM is `mov dst, [base+disp]`.
func (a *Asm) MovRM(dst Reg, m Mem) { a.op64rm(0x8B, dst, m) }

// MovMR is `mov [base+disp], src`.
func (a *Asm) MovMR(m Mem, src Reg) { a.op64rm(0x89, src, m) }

// MovMI is `mov qword [base+disp], imm32`, sign-extended to 64 bits.
func (a *Asm) MovMI(m Mem, imm int32) {
	a.rex(true, 0, 0, m.Base, false)
	a.emit(0xC7)
	a.modrmMem(0, m)
	a.imm32(imm)
}

// Lea is `lea dst, [base+disp]`.
func (a *Asm) Lea(dst Reg, m Mem) { a.op64rm(0x8D, dst, m) }

// LeaLabel is `lea dst, [rip + (label - next instruction)]`: the address of a label,
// computed without a relocation.
func (a *Asm) LeaLabel(dst Reg, l Label) {
	a.rex(true, dst, 0, 0, false)
	a.emit(0x8D)
	// mod = 00, rm = 101 is RIP-relative.
	a.emit(0x00 | byte(dst&7)<<3 | 0x05)
	a.ref32(l)
}

// Push is `push src`.
func (a *Asm) Push(src Reg) {
	a.rex(false, 0, 0, src, false)
	a.emit(0x50 | byte(src&7))
}

// Pop is `pop dst`.
func (a *Asm) Pop(dst Reg) {
	a.rex(false, 0, 0, dst, false)
	a.emit(0x58 | byte(dst&7))
}

// Movzx8 is `movzx dst, src8`: zero-extend a byte register to 64 bits, which is how a
// `setcc` result becomes an Origin `bool`.
func (a *Asm) Movzx8(dst, src Reg) {
	a.rex(true, dst, 0, src, false)
	a.emit(0x0F, 0xB6)
	a.modrmReg(dst, src)
}

// Cmov is `cmovcc dst, src`.
func (a *Asm) Cmov(c Cond, dst, src Reg) {
	a.rex(true, dst, 0, src, false)
	a.emit(0x0F, 0x40|byte(c))
	a.modrmReg(dst, src)
}

// Setcc is `setcc dst8`: set the low byte to 0 or 1. The REX prefix is forced so that
// dst 4..7 means spl/bpl/sil/dil rather than ah/ch/dh/bh.
func (a *Asm) Setcc(c Cond, dst Reg) {
	a.rex(false, 0, 0, dst, dst >= RSP && dst < R8)
	a.emit(0x0F, 0x90|byte(c))
	a.emit(0xC0 | byte(dst&7))
}

// ---------------------------------------------------------------------------
// Integer arithmetic
// ---------------------------------------------------------------------------

// AddRR is `add dst, src`.
func (a *Asm) AddRR(dst, src Reg) { a.op64rr(0x01, src, dst) }

// SubRR is `sub dst, src`.
func (a *Asm) SubRR(dst, src Reg) { a.op64rr(0x29, src, dst) }

// AndRR is `and dst, src`.
func (a *Asm) AndRR(dst, src Reg) { a.op64rr(0x21, src, dst) }

// OrRR is `or dst, src`.
func (a *Asm) OrRR(dst, src Reg) { a.op64rr(0x09, src, dst) }

// XorRR is `xor dst, src`.
func (a *Asm) XorRR(dst, src Reg) { a.op64rr(0x31, src, dst) }

// CmpRR is `cmp a, b`.
func (a *Asm) CmpRR(x, y Reg) { a.op64rr(0x39, y, x) }

// TestRR is `test a, b`.
func (a *Asm) TestRR(x, y Reg) { a.op64rr(0x85, y, x) }

// ImulRR is `imul dst, src`: the two-operand form, which keeps the result in dst and
// sets OF on signed overflow. ADR-0005 makes that flag the whole point.
func (a *Asm) ImulRR(dst, src Reg) {
	a.rex(true, dst, 0, src, false)
	a.emit(0x0F, 0xAF)
	a.modrmReg(dst, src)
}

// aluImm emits one of the `81 /n` group with a 32-bit immediate. The 8-bit form is not
// used: a fixed instruction length is worth more here than a byte.
func (a *Asm) aluImm(ext byte, dst Reg, imm int32) {
	a.rex(true, 0, 0, dst, false)
	a.emit(0x81)
	a.emit(0xC0 | ext<<3 | byte(dst&7))
	a.imm32(imm)
}

// AddRI is `add dst, imm32`.
func (a *Asm) AddRI(dst Reg, imm int32) { a.aluImm(0, dst, imm) }

// OrRI is `or dst, imm32`.
func (a *Asm) OrRI(dst Reg, imm int32) { a.aluImm(1, dst, imm) }

// AndRI is `and dst, imm32`.
func (a *Asm) AndRI(dst Reg, imm int32) { a.aluImm(4, dst, imm) }

// SubRI is `sub dst, imm32`.
func (a *Asm) SubRI(dst Reg, imm int32) { a.aluImm(5, dst, imm) }

// XorRI is `xor dst, imm32`.
func (a *Asm) XorRI(dst Reg, imm int32) { a.aluImm(6, dst, imm) }

// CmpRI is `cmp dst, imm32`.
func (a *Asm) CmpRI(dst Reg, imm int32) { a.aluImm(7, dst, imm) }

// unary emits one of the `F7 /n` group.
func (a *Asm) unary(ext byte, r Reg) {
	a.rex(true, 0, 0, r, false)
	a.emit(0xF7)
	a.emit(0xC0 | ext<<3 | byte(r&7))
}

// Not is `not r`.
func (a *Asm) Not(r Reg) { a.unary(2, r) }

// Neg is `neg r`, which sets OF when the operand is the most negative integer — the one
// case where negation overflows.
func (a *Asm) Neg(r Reg) { a.unary(3, r) }

// Idiv is `idiv r`: divides RDX:RAX by r, quotient in RAX, remainder in RDX. The caller
// must have executed Cqo, and must have already excluded a zero divisor and the
// `MIN / -1` overflow, both of which raise a hardware exception rather than trapping the
// way spec/04-expressions.md requires.
func (a *Asm) Idiv(r Reg) { a.unary(7, r) }

// Cqo sign-extends RAX into RDX:RAX, which is what makes `idiv` a signed division.
func (a *Asm) Cqo() { a.emit(0x48, 0x99) }

// shiftCL emits one of the `D3 /n` group: shift by the count in CL.
func (a *Asm) shiftCL(ext byte, r Reg) {
	a.rex(true, 0, 0, r, false)
	a.emit(0xD3)
	a.emit(0xC0 | ext<<3 | byte(r&7))
}

// ShlCL is `shl r, cl`.
func (a *Asm) ShlCL(r Reg) { a.shiftCL(4, r) }

// ShrCL is `shr r, cl` (logical).
func (a *Asm) ShrCL(r Reg) { a.shiftCL(5, r) }

// SarCL is `sar r, cl` (arithmetic).
func (a *Asm) SarCL(r Reg) { a.shiftCL(7, r) }

// shiftImm emits one of the `C1 /n` group.
func (a *Asm) shiftImm(ext byte, r Reg, count uint8) {
	a.rex(true, 0, 0, r, false)
	a.emit(0xC1)
	a.emit(0xC0 | ext<<3 | byte(r&7))
	a.emit(count)
}

// ShlI is `shl r, imm8`.
func (a *Asm) ShlI(r Reg, count uint8) { a.shiftImm(4, r, count) }

// ShrI is `shr r, imm8`.
func (a *Asm) ShrI(r Reg, count uint8) { a.shiftImm(5, r, count) }

// SarI is `sar r, imm8`.
func (a *Asm) SarI(r Reg, count uint8) { a.shiftImm(7, r, count) }

// ---------------------------------------------------------------------------
// Control flow
// ---------------------------------------------------------------------------

// Jmp is `jmp label`, always in the rel32 form so that binding a label never changes an
// instruction's length.
func (a *Asm) Jmp(l Label) {
	a.emit(0xE9)
	a.ref32(l)
}

// Jcc is `jcc label`, rel32.
func (a *Asm) Jcc(c Cond, l Label) {
	a.emit(0x0F, 0x80|byte(c))
	a.ref32(l)
}

// Call is `call label`, rel32.
func (a *Asm) Call(l Label) {
	a.emit(0xE8)
	a.ref32(l)
}

// CallReg is `call r`, for a call through a function value.
func (a *Asm) CallReg(r Reg) {
	a.rex(false, 0, 0, r, false)
	a.emit(0xFF)
	a.emit(0xD0 | byte(r&7))
}

// JmpReg is `jmp r`.
func (a *Asm) JmpReg(r Reg) {
	a.rex(false, 0, 0, r, false)
	a.emit(0xFF)
	a.emit(0xE0 | byte(r&7))
}

// Ret is `ret`.
func (a *Asm) Ret() { a.emit(0xC3) }

// Syscall is `syscall`: the only way this runtime reaches the operating system
// (ADR-0017).
func (a *Asm) Syscall() { a.emit(0x0F, 0x05) }

// Ud2 is `ud2`, an instruction guaranteed to fault. It is emitted where control must not
// reach — after a trap that exits, or off the end of a function that always returns —
// so that a bug in the code generator crashes immediately instead of executing whatever
// bytes follow.
func (a *Asm) Ud2() { a.emit(0x0F, 0x0B) }

// Nop is a one-byte `nop`, used to pad a function to an alignment boundary.
func (a *Asm) Nop() { a.emit(0x90) }

// Align pads with `nop` until the next multiple of n.
func (a *Asm) Align(n int) {
	for len(a.code)%n != 0 {
		a.Nop()
	}
}

// ---------------------------------------------------------------------------
// Floating point (SSE2, doubles)
// ---------------------------------------------------------------------------

// sse emits a two-byte-opcode SSE instruction with an xmm-xmm operand pair. The prefix
// order matters and is easy to get backwards: legacy prefix, then REX, then opcode.
func (a *Asm) sse(prefix byte, opcode byte, dst, src Xmm) {
	if prefix != 0 {
		a.emit(prefix)
	}
	a.rex(false, Reg(dst), 0, Reg(src), false)
	a.emit(0x0F, opcode)
	a.emit(0xC0 | byte(dst&7)<<3 | byte(src&7))
}

// MovsdXX is `movsd dst, src` between registers.
func (a *Asm) MovsdXX(dst, src Xmm) {
	if dst == src {
		return
	}
	a.sse(0xF2, 0x10, dst, src)
}

// MovsdXM is `movsd dst, [base+disp]`.
func (a *Asm) MovsdXM(dst Xmm, m Mem) {
	a.emit(0xF2)
	a.rex(false, Reg(dst), 0, m.Base, false)
	a.emit(0x0F, 0x10)
	a.modrmMem(Reg(dst), m)
}

// MovsdMX is `movsd [base+disp], src`.
func (a *Asm) MovsdMX(m Mem, src Xmm) {
	a.emit(0xF2)
	a.rex(false, Reg(src), 0, m.Base, false)
	a.emit(0x0F, 0x11)
	a.modrmMem(Reg(src), m)
}

// AddsdXX is `addsd dst, src`.
func (a *Asm) AddsdXX(dst, src Xmm) { a.sse(0xF2, 0x58, dst, src) }

// SubsdXX is `subsd dst, src`.
func (a *Asm) SubsdXX(dst, src Xmm) { a.sse(0xF2, 0x5C, dst, src) }

// MulsdXX is `mulsd dst, src`.
func (a *Asm) MulsdXX(dst, src Xmm) { a.sse(0xF2, 0x59, dst, src) }

// DivsdXX is `divsd dst, src`. Float division never traps: it produces an infinity or a
// NaN, per spec/04-expressions.md.
func (a *Asm) DivsdXX(dst, src Xmm) { a.sse(0xF2, 0x5E, dst, src) }

// UcomisdXX is `ucomisd a, b`: an unordered compare setting ZF, PF and CF. PF is set
// when either operand is NaN, which is how `NaN != NaN` is implemented.
func (a *Asm) UcomisdXX(x, y Xmm) { a.sse(0x66, 0x2E, x, y) }

// XorpsXX is `xorps dst, src`, used with dst == src to make a zero.
func (a *Asm) XorpsXX(dst, src Xmm) { a.sse(0, 0x57, dst, src) }

// MovqXR is `movq dst_xmm, src_reg`: move the raw bits, no conversion.
func (a *Asm) MovqXR(dst Xmm, src Reg) {
	a.emit(0x66)
	a.rex(true, Reg(dst), 0, src, false)
	a.emit(0x0F, 0x6E)
	a.emit(0xC0 | byte(dst&7)<<3 | byte(src&7))
}

// MovqRX is `movq dst_reg, src_xmm`: move the raw bits, no conversion.
func (a *Asm) MovqRX(dst Reg, src Xmm) {
	a.emit(0x66)
	a.rex(true, Reg(src), 0, dst, false)
	a.emit(0x0F, 0x7E)
	a.emit(0xC0 | byte(src&7)<<3 | byte(dst&7))
}

// Cvtsi2sd is `cvtsi2sd dst, src`: signed 64-bit integer to double.
func (a *Asm) Cvtsi2sd(dst Xmm, src Reg) {
	a.emit(0xF2)
	a.rex(true, Reg(dst), 0, src, false)
	a.emit(0x0F, 0x2A)
	a.emit(0xC0 | byte(dst&7)<<3 | byte(src&7))
}

// Cvttsd2si is `cvttsd2si dst, src`: double to signed 64-bit integer, truncating toward
// zero.
func (a *Asm) Cvttsd2si(dst Reg, src Xmm) {
	a.emit(0xF2)
	a.rex(true, dst, 0, Reg(src), false)
	a.emit(0x0F, 0x2C)
	a.emit(0xC0 | byte(dst&7)<<3 | byte(src&7))
}
