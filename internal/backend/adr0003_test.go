package backend

import (
	"testing"

	"github.com/scarypheonix/meta/internal/obj"
)

// TestBothTargetsShareOneInstructionStream is ADR-0003's central claim, checked against
// programs the compiler actually produced rather than against a hand-written stub.
//
// internal/obj already asserts this for a few instructions written by hand. That is the
// easy case. The claim that matters is about real output: compiling the same source for
// Linux and for macOS must produce the *same instructions*, differing only where an
// address or a syscall number is baked into an immediate. If the two ever differed in
// length, "two object writers is not cross-compilation" would stop being true — there
// would be two code generators, and the differential this project runs on ELF would stop
// saying anything about the Mach-O it ships.
func TestBothTargetsShareOneInstructionStream(t *testing.T) {
	programs := map[string]string{
		"arithmetic and calls": `
use std::io;

fn add(a: i64, b: i64) -> i64 { a + b }

fn main() {
    io::println(add(1, 2).to_str());
}
`,
		"structs and the heap": `
use std::io;

struct Pair { a: i64, b: i64 }

fn main() {
    let mut i = 0;
    let mut last = Pair { a: 0, b: 0 };
    while i < 100 {
        last = Pair { a: i, b: i * 2 };
        i = i + 1;
    }
    io::println(last.b.to_str());
}
`,
		"closures and control flow": `
use std::io;

fn pick(f: fn(i64) -> i64, g: fn(i64) -> i64, which: bool) -> fn(i64) -> i64 {
    if which { f } else { g }
}

fn main() {
    let double = |x: i64| -> i64 { x * 2 };
    let negate = |x: i64| -> i64 { 0 - x };
    let chosen = pick(double, negate, true);
    io::println(chosen(21).to_str());
}
`,
	}

	for name, src := range programs {
		t.Run(name, func(t *testing.T) {
			linux := buildStackMapTestImageFor(t, obj.Linux, src)
			mac := buildStackMapTestImageFor(t, obj.MacOS, src)

			if len(linux.Text) != len(mac.Text) {
				t.Fatalf("the two targets emitted %d and %d bytes of code; one instruction "+
					"stream means one length, and an instruction's length must never depend "+
					"on the value of an address (ADR-0017)",
					len(linux.Text), len(mac.Text))
			}
			if len(linux.RoData) != len(mac.RoData) {
				t.Errorf("read-only data is %d bytes for Linux and %d for macOS",
					len(linux.RoData), len(mac.RoData))
			}

			diffs := 0
			for i := range linux.Text {
				if linux.Text[i] != mac.Text[i] {
					diffs++
				}
			}
			if diffs == 0 {
				t.Fatal("the two builds are byte-identical, so this is not exercising " +
					"two targets at all")
			}
			// Every difference must live inside a baked-in 64-bit immediate: a load
			// address (the two bases differ) or a syscall number. Those are sparse, so a
			// large fraction of differing bytes would mean the two targets are being
			// compiled differently rather than merely relocated differently. In practice
			// these programs differ in about 2% of their code bytes -- 64 of 3514 for the
			// struct loop, which is eight baked-in 64-bit values -- so the bound below is
			// loose enough not to be brittle and far below what genuinely divergent code
			// generation would produce.
			if pct := 100 * diffs / len(linux.Text); pct > 15 {
				t.Errorf("%d of %d code bytes differ (%d%%), too many to be baked-in "+
					"addresses and syscall numbers alone", diffs, len(linux.Text), pct)
			}
		})
	}
}
