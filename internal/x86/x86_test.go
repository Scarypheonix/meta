package x86

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The encoder is tested twice over, because it is the one place in the compiler where a
// mistake produces no error anywhere else: wrong bytes are still bytes, and the machine
// executes them.
//
//  1. TestEncodings pins the exact bytes of every instruction the backend emits. It
//     needs nothing installed and runs everywhere.
//  2. TestDisassemblesAsIntended feeds the same instructions to objdump and checks that
//     the disassembly says what the method name claims. It is the differential: the
//     golden bytes could be confidently wrong, and a disassembler written by someone
//     else will not make the same mistake.

type encCase struct {
	name string
	// text is what the instruction should disassemble to, in objdump's AT&T-free Intel
	// syntax, lowercased and with runs of spaces collapsed.
	text string
	emit func(a *Asm)
	want []byte
}

func cases() []encCase {
	return []encCase{
		// Data movement.
		{"mov rax, rbx", "mov rax,rbx", func(a *Asm) { a.MovRR(RAX, RBX) }, []byte{0x48, 0x89, 0xD8}},
		{"mov r12, r13", "mov r12,r13", func(a *Asm) { a.MovRR(R12, R13) }, []byte{0x4D, 0x89, 0xEC}},
		{"mov rdi, rsp", "mov rdi,rsp", func(a *Asm) { a.MovRR(RDI, RSP) }, []byte{0x48, 0x89, 0xE7}},
		{"mov rax, imm64", "movabs rax,0x123456789abcdef",
			func(a *Asm) { a.MovRI(RAX, 0x0123456789ABCDEF) },
			[]byte{0x48, 0xB8, 0xEF, 0xCD, 0xAB, 0x89, 0x67, 0x45, 0x23, 0x01}},
		{"mov r9, imm64", "movabs r9,0x1",
			func(a *Asm) { a.MovRI(R9, 1) },
			[]byte{0x49, 0xB9, 0x01, 0, 0, 0, 0, 0, 0, 0}},

		// Memory operands: the three ModRM shapes that are easy to get wrong.
		{"mov rax, [rbx]", "mov rax,qword ptr [rbx]", func(a *Asm) { a.MovRM(RAX, At(RBX, 0)) },
			[]byte{0x48, 0x8B, 0x03}},
		{"mov rax, [rbp+0]", "mov rax,qword ptr [rbp+0x0]", func(a *Asm) { a.MovRM(RAX, At(RBP, 0)) },
			[]byte{0x48, 0x8B, 0x45, 0x00}},
		{"mov rax, [rsp+0]", "mov rax,qword ptr [rsp]", func(a *Asm) { a.MovRM(RAX, At(RSP, 0)) },
			[]byte{0x48, 0x8B, 0x04, 0x24}},
		{"mov rax, [r12+8]", "mov rax,qword ptr [r12+0x8]", func(a *Asm) { a.MovRM(RAX, At(R12, 8)) },
			[]byte{0x49, 0x8B, 0x44, 0x24, 0x08}},
		{"mov rax, [r13-16]", "mov rax,qword ptr [r13-0x10]", func(a *Asm) { a.MovRM(RAX, At(R13, -16)) },
			[]byte{0x49, 0x8B, 0x45, 0xF0}},
		{"mov rax, [rbp-1024]", "mov rax,qword ptr [rbp-0x400]", func(a *Asm) { a.MovRM(RAX, At(RBP, -1024)) },
			[]byte{0x48, 0x8B, 0x85, 0x00, 0xFC, 0xFF, 0xFF}},
		{"mov [rbp-8], rsi", "mov qword ptr [rbp-0x8],rsi", func(a *Asm) { a.MovMR(At(RBP, -8), RSI) },
			[]byte{0x48, 0x89, 0x75, 0xF8}},
		{"mov qword [rbp-8], 5", "mov qword ptr [rbp-0x8],0x5", func(a *Asm) { a.MovMI(At(RBP, -8), 5) },
			[]byte{0x48, 0xC7, 0x45, 0xF8, 0x05, 0x00, 0x00, 0x00}},
		{"lea rax, [rbx+16]", "lea rax,[rbx+0x10]", func(a *Asm) { a.Lea(RAX, At(RBX, 16)) },
			[]byte{0x48, 0x8D, 0x43, 0x10}},

		{"push rbp", "push rbp", func(a *Asm) { a.Push(RBP) }, []byte{0x55}},
		{"push r15", "push r15", func(a *Asm) { a.Push(R15) }, []byte{0x41, 0x57}},
		{"pop rbp", "pop rbp", func(a *Asm) { a.Pop(RBP) }, []byte{0x5D}},
		{"pop r12", "pop r12", func(a *Asm) { a.Pop(R12) }, []byte{0x41, 0x5C}},

		{"movzx rax, al", "movzx rax,al", func(a *Asm) { a.Movzx8(RAX, RAX) },
			[]byte{0x48, 0x0F, 0xB6, 0xC0}},
		{"cmove rax, rcx", "cmove rax,rcx", func(a *Asm) { a.Cmov(Equal, RAX, RCX) },
			[]byte{0x48, 0x0F, 0x44, 0xC1}},
		{"setl al", "setl al", func(a *Asm) { a.Setcc(Less, RAX) }, []byte{0x0F, 0x9C, 0xC0}},
		// The forced REX: without it this would set ah, not sil.
		{"setne sil", "setne sil", func(a *Asm) { a.Setcc(NotEqual, RSI) },
			[]byte{0x40, 0x0F, 0x95, 0xC6}},
		{"sete r10b", "sete r10b", func(a *Asm) { a.Setcc(Equal, R10) },
			[]byte{0x41, 0x0F, 0x94, 0xC2}},

		// Integer arithmetic.
		{"add rax, rcx", "add rax,rcx", func(a *Asm) { a.AddRR(RAX, RCX) }, []byte{0x48, 0x01, 0xC8}},
		{"sub rdx, r8", "sub rdx,r8", func(a *Asm) { a.SubRR(RDX, R8) }, []byte{0x4C, 0x29, 0xC2}},
		{"and rax, rbx", "and rax,rbx", func(a *Asm) { a.AndRR(RAX, RBX) }, []byte{0x48, 0x21, 0xD8}},
		{"or rax, rbx", "or rax,rbx", func(a *Asm) { a.OrRR(RAX, RBX) }, []byte{0x48, 0x09, 0xD8}},
		{"xor rax, rax", "xor rax,rax", func(a *Asm) { a.XorRR(RAX, RAX) }, []byte{0x48, 0x31, 0xC0}},
		{"cmp rdi, rsi", "cmp rdi,rsi", func(a *Asm) { a.CmpRR(RDI, RSI) }, []byte{0x48, 0x39, 0xF7}},
		{"test rax, rax", "test rax,rax", func(a *Asm) { a.TestRR(RAX, RAX) }, []byte{0x48, 0x85, 0xC0}},
		{"imul rax, rcx", "imul rax,rcx", func(a *Asm) { a.ImulRR(RAX, RCX) },
			[]byte{0x48, 0x0F, 0xAF, 0xC1}},
		{"add rax, 1", "add rax,0x1", func(a *Asm) { a.AddRI(RAX, 1) },
			[]byte{0x48, 0x81, 0xC0, 0x01, 0x00, 0x00, 0x00}},
		{"sub rsp, 32", "sub rsp,0x20", func(a *Asm) { a.SubRI(RSP, 32) },
			[]byte{0x48, 0x81, 0xEC, 0x20, 0x00, 0x00, 0x00}},
		{"cmp rax, -1", "cmp rax,0xffffffffffffffff", func(a *Asm) { a.CmpRI(RAX, -1) },
			[]byte{0x48, 0x81, 0xF8, 0xFF, 0xFF, 0xFF, 0xFF}},
		{"and r11, 7", "and r11,0x7", func(a *Asm) { a.AndRI(R11, 7) },
			[]byte{0x49, 0x81, 0xE3, 0x07, 0x00, 0x00, 0x00}},
		{"or rcx, 8", "or rcx,0x8", func(a *Asm) { a.OrRI(RCX, 8) },
			[]byte{0x48, 0x81, 0xC9, 0x08, 0x00, 0x00, 0x00}},
		{"xor rdx, 15", "xor rdx,0xf", func(a *Asm) { a.XorRI(RDX, 15) },
			[]byte{0x48, 0x81, 0xF2, 0x0F, 0x00, 0x00, 0x00}},

		{"div rcx", "div rcx", func(a *Asm) { a.Div(RCX) }, []byte{0x48, 0xF7, 0xF1}},
		{"mov al, [r8]", "mov al,byte ptr [r8]", func(a *Asm) { a.MovRM8(RAX, At(R8, 0)) },
			[]byte{0x41, 0x8A, 0x00}},
		{"mov [rbx], dl", "mov byte ptr [rbx],dl", func(a *Asm) { a.MovMR8(At(RBX, 0), RDX) },
			[]byte{0x88, 0x13}},
		{"mov [r9+3], sil", "mov byte ptr [r9+0x3],sil", func(a *Asm) { a.MovMR8(At(R9, 3), RSI) },
			[]byte{0x41, 0x88, 0x71, 0x03}},
		{"neg rax", "neg rax", func(a *Asm) { a.Neg(RAX) }, []byte{0x48, 0xF7, 0xD8}},
		{"not rbx", "not rbx", func(a *Asm) { a.Not(RBX) }, []byte{0x48, 0xF7, 0xD3}},
		{"idiv rcx", "idiv rcx", func(a *Asm) { a.Idiv(RCX) }, []byte{0x48, 0xF7, 0xF9}},
		{"cqo", "cqo", func(a *Asm) { a.Cqo() }, []byte{0x48, 0x99}},

		{"shl rax, cl", "shl rax,cl", func(a *Asm) { a.ShlCL(RAX) }, []byte{0x48, 0xD3, 0xE0}},
		{"shr rax, cl", "shr rax,cl", func(a *Asm) { a.ShrCL(RAX) }, []byte{0x48, 0xD3, 0xE8}},
		{"sar rax, cl", "sar rax,cl", func(a *Asm) { a.SarCL(RAX) }, []byte{0x48, 0xD3, 0xF8}},
		{"shl rax, 3", "shl rax,0x3", func(a *Asm) { a.ShlI(RAX, 3) }, []byte{0x48, 0xC1, 0xE0, 0x03}},
		{"sar r9, 1", "sar r9,0x1", func(a *Asm) { a.SarI(R9, 1) }, []byte{0x49, 0xC1, 0xF9, 0x01}},

		// Control flow. A jump to itself is the shape with a known displacement: the
		// rel32 is measured from the end of the instruction, so jumping to the start of
		// a five-byte jmp is -5.
		{"jmp self", "jmp", func(a *Asm) {
			l := a.NewLabel("here")
			a.Bind(l)
			a.Jmp(l)
		}, []byte{0xE9, 0xFB, 0xFF, 0xFF, 0xFF}},
		{"je self", "je", func(a *Asm) {
			l := a.NewLabel("here")
			a.Bind(l)
			a.Jcc(Equal, l)
		}, []byte{0x0F, 0x84, 0xFA, 0xFF, 0xFF, 0xFF}},
		{"call self", "call", func(a *Asm) {
			l := a.NewLabel("here")
			a.Bind(l)
			a.Call(l)
		}, []byte{0xE8, 0xFB, 0xFF, 0xFF, 0xFF}},
		{"call rax", "call rax", func(a *Asm) { a.CallReg(RAX) }, []byte{0xFF, 0xD0}},
		{"call r11", "call r11", func(a *Asm) { a.CallReg(R11) }, []byte{0x41, 0xFF, 0xD3}},
		{"jmp rax", "jmp rax", func(a *Asm) { a.JmpReg(RAX) }, []byte{0xFF, 0xE0}},
		{"ret", "ret", func(a *Asm) { a.Ret() }, []byte{0xC3}},
		{"syscall", "syscall", func(a *Asm) { a.Syscall() }, []byte{0x0F, 0x05}},
		{"ud2", "ud2", func(a *Asm) { a.Ud2() }, []byte{0x0F, 0x0B}},
		{"nop", "nop", func(a *Asm) { a.Nop() }, []byte{0x90}},

		// Floating point.
		{"movsd xmm9, xmm12", "movsd xmm9,xmm12", func(a *Asm) { a.MovsdXX(XMM9, XMM12) },
			[]byte{0xF2, 0x45, 0x0F, 0x10, 0xCC}},
		{"addsd xmm8, xmm0", "addsd xmm8,xmm0", func(a *Asm) { a.AddsdXX(XMM8, XMM0) },
			[]byte{0xF2, 0x44, 0x0F, 0x58, 0xC0}},
		{"movsd xmm10, [r13+0]", "movsd xmm10,qword ptr [r13+0x0]",
			func(a *Asm) { a.MovsdXM(XMM10, At(R13, 0)) },
			[]byte{0xF2, 0x45, 0x0F, 0x10, 0x55, 0x00}},
		{"movq xmm15, r14", "movq xmm15,r14", func(a *Asm) { a.MovqXR(XMM15, R14) },
			[]byte{0x66, 0x4D, 0x0F, 0x6E, 0xFE}},
		{"movq r13, xmm7", "movq r13,xmm7", func(a *Asm) { a.MovqRX(R13, XMM7) },
			[]byte{0x66, 0x49, 0x0F, 0x7E, 0xFD}},
		{"movsd xmm0, xmm1", "movsd xmm0,xmm1", func(a *Asm) { a.MovsdXX(XMM0, XMM1) },
			[]byte{0xF2, 0x0F, 0x10, 0xC1}},
		{"movsd xmm0, [rbp-8]", "movsd xmm0,qword ptr [rbp-0x8]",
			func(a *Asm) { a.MovsdXM(XMM0, At(RBP, -8)) },
			[]byte{0xF2, 0x0F, 0x10, 0x45, 0xF8}},
		{"movsd [rbp-8], xmm3", "movsd qword ptr [rbp-0x8],xmm3",
			func(a *Asm) { a.MovsdMX(At(RBP, -8), XMM3) },
			[]byte{0xF2, 0x0F, 0x11, 0x5D, 0xF8}},
		{"addsd xmm0, xmm1", "addsd xmm0,xmm1", func(a *Asm) { a.AddsdXX(XMM0, XMM1) },
			[]byte{0xF2, 0x0F, 0x58, 0xC1}},
		{"subsd xmm0, xmm1", "subsd xmm0,xmm1", func(a *Asm) { a.SubsdXX(XMM0, XMM1) },
			[]byte{0xF2, 0x0F, 0x5C, 0xC1}},
		{"mulsd xmm0, xmm1", "mulsd xmm0,xmm1", func(a *Asm) { a.MulsdXX(XMM0, XMM1) },
			[]byte{0xF2, 0x0F, 0x59, 0xC1}},
		{"divsd xmm0, xmm1", "divsd xmm0,xmm1", func(a *Asm) { a.DivsdXX(XMM0, XMM1) },
			[]byte{0xF2, 0x0F, 0x5E, 0xC1}},
		{"ucomisd xmm0, xmm1", "ucomisd xmm0,xmm1", func(a *Asm) { a.UcomisdXX(XMM0, XMM1) },
			[]byte{0x66, 0x0F, 0x2E, 0xC1}},
		{"xorps xmm2, xmm2", "xorps xmm2,xmm2", func(a *Asm) { a.XorpsXX(XMM2, XMM2) },
			[]byte{0x0F, 0x57, 0xD2}},
		{"movq xmm0, rax", "movq xmm0,rax", func(a *Asm) { a.MovqXR(XMM0, RAX) },
			[]byte{0x66, 0x48, 0x0F, 0x6E, 0xC0}},
		{"movq rax, xmm0", "movq rax,xmm0", func(a *Asm) { a.MovqRX(RAX, XMM0) },
			[]byte{0x66, 0x48, 0x0F, 0x7E, 0xC0}},
		{"cvtsi2sd xmm0, rdi", "cvtsi2sd xmm0,rdi", func(a *Asm) { a.Cvtsi2sd(XMM0, RDI) },
			[]byte{0xF2, 0x48, 0x0F, 0x2A, 0xC7}},
		{"cvttsd2si rax, xmm1", "cvttsd2si rax,xmm1", func(a *Asm) { a.Cvttsd2si(RAX, XMM1) },
			[]byte{0xF2, 0x48, 0x0F, 0x2C, 0xC1}},
	}
}

func TestEncodings(t *testing.T) {
	for _, c := range cases() {
		t.Run(c.name, func(t *testing.T) {
			a := New(0)
			c.emit(a)
			got := a.Code()
			if !bytes.Equal(got, c.want) {
				t.Errorf("encoded %x, want %x", got, c.want)
			}
		})
	}
}

// TestDisassemblesAsIntended is the differential: our bytes, someone else's decoder.
//
// Pinned bytes prove the encoder is stable, not that it is right — a confidently wrong
// constant would be pinned just as firmly. Disassembling with objdump and reading back
// the mnemonic is what makes the test say what the instruction *means*.
func TestDisassemblesAsIntended(t *testing.T) {
	objdump, err := exec.LookPath("objdump")
	if err != nil {
		t.Skip("objdump is not installed: the byte-level encodings are still checked by TestEncodings")
	}

	for _, c := range cases() {
		t.Run(c.name, func(t *testing.T) {
			a := New(0)
			c.emit(a)
			text := disassemble(t, objdump, a.Code())
			if !strings.HasPrefix(text, c.text) {
				t.Errorf("objdump reads this as %q, want it to start with %q\nbytes: %x",
					text, c.text, a.Code())
			}
		})
	}
}

var objdumpLine = regexp.MustCompile(`^\s+[0-9a-f]+:\s+(?:[0-9a-f]{2} )+\s*(.*)$`)

// disassemble runs objdump over raw bytes and returns the first instruction's text,
// normalized: lowercase, single-spaced, with any trailing comment removed.
func disassemble(t *testing.T, objdump string, code []byte) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "code.bin")
	if err := os.WriteFile(path, code, 0o600); err != nil {
		t.Fatalf("writing bytes: %v", err)
	}
	out, err := exec.Command(objdump, "-D", "-b", "binary", "-m", "i386:x86-64",
		"-M", "intel", path).Output()
	if err != nil {
		t.Fatalf("objdump: %v", err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		m := objdumpLine.FindStringSubmatch(scanner.Text())
		if m == nil {
			continue
		}
		text := strings.TrimSpace(m[1])
		if i := strings.Index(text, "#"); i >= 0 {
			text = strings.TrimSpace(text[:i])
		}
		return strings.Join(strings.Fields(strings.ToLower(text)), " ")
	}
	t.Fatalf("objdump produced no instruction for %x:\n%s", code, out)
	return ""
}

// TestForwardJumpIsPatched checks the label machinery over a shape the backend emits for
// every `if`: a conditional jump forward over a block whose length is not yet known.
func TestForwardJumpIsPatched(t *testing.T) {
	a := New(0)
	end := a.NewLabel("end")
	a.Jcc(Equal, end) // 6 bytes
	a.Nop()           // 1 byte
	a.Nop()           // 1 byte
	a.Bind(end)
	a.Ret()

	want := []byte{0x0F, 0x84, 0x02, 0x00, 0x00, 0x00, 0x90, 0x90, 0xC3}
	if got := a.Code(); !bytes.Equal(got, want) {
		t.Errorf("encoded %x, want %x", got, want)
	}
}

// TestBackwardJumpNeedsNoSecondPass: a loop's back edge names a label that is already
// bound, so it is patched as it is emitted.
func TestBackwardJumpNeedsNoSecondPass(t *testing.T) {
	a := New(0)
	top := a.NewLabel("top")
	a.Bind(top)
	a.Nop()
	a.Jmp(top)

	want := []byte{0x90, 0xE9, 0xFA, 0xFF, 0xFF, 0xFF}
	if got := a.Code(); !bytes.Equal(got, want) {
		t.Errorf("encoded %x, want %x", got, want)
	}
}

func TestUnboundLabelPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Code() returned with a label still unbound; a jump into nowhere must not be silent")
		}
	}()
	a := New(0)
	a.Jmp(a.NewLabel("nowhere"))
	a.Code()
}

// TestAddrTracksTheLoadAddress: the assembler is told where its first byte will live, so
// a bound label's address is absolute. The runtime block and every static string are
// referenced that way (ADR-0017: no relocations).
func TestAddrTracksTheLoadAddress(t *testing.T) {
	const base = 0x400000
	a := New(base)
	a.Nop()
	a.Nop()
	l := a.NewLabel("here")
	a.Bind(l)
	if got, want := a.Addr(l), uint64(base+2); got != want {
		t.Errorf("label address is %#x, want %#x", got, want)
	}
	if got, want := a.PC(), uint64(base+2); got != want {
		t.Errorf("PC is %#x, want %#x", got, want)
	}
}

// TestLeaLabelIsRipRelative pins the one addressing mode that is measured from the *end*
// of the instruction rather than from its start.
func TestLeaLabelIsRipRelative(t *testing.T) {
	a := New(0)
	msg := a.NewLabel("msg")
	a.LeaLabel(RDI, msg) // 7 bytes
	a.Nop()              // 1 byte
	a.Bind(msg)

	want := []byte{0x48, 0x8D, 0x3D, 0x01, 0x00, 0x00, 0x00, 0x90}
	if got := a.Code(); !bytes.Equal(got, want) {
		t.Fatalf("encoded %x, want %x", got, want)
	}
}

func TestCondInvert(t *testing.T) {
	pairs := [][2]Cond{
		{Equal, NotEqual}, {Less, GreaterEqual}, {LessEqual, Greater},
		{Below, AboveEqual}, {BelowEqual, Above}, {Overflow, NoOverflow},
		{Sign, NoSign}, {Parity, NoParity},
	}
	for _, p := range pairs {
		if p[0].Invert() != p[1] || p[1].Invert() != p[0] {
			t.Errorf("%s and %s are not each other's inverse", p[0], p[1])
		}
	}
}

// TestAlignPadsToBoundary: functions start aligned, so a call target is not split across
// a cache line.
func TestAlignPadsToBoundary(t *testing.T) {
	a := New(0)
	a.Ret()
	a.Align(16)
	if got := a.Len(); got != 16 {
		t.Errorf("aligned to %d bytes, want 16", got)
	}
	if code := a.Code(); code[0] != 0xC3 {
		t.Error("Align overwrote the instruction it was padding after")
	}
}

func ExampleAsm() {
	a := New(0)
	a.MovRI(RAX, 60)
	a.XorRR(RDI, RDI)
	a.Syscall()
	fmt.Printf("%x\n", a.Code())
	// Output: 48b83c000000000000004831ff0f05
}
