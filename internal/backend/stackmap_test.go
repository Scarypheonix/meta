package backend

import (
	"encoding/binary"
	"testing"

	"github.com/scarypheonix/meta/internal/ast"
	"github.com/scarypheonix/meta/internal/check"
	"github.com/scarypheonix/meta/internal/compile"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/layout"
	"github.com/scarypheonix/meta/internal/mono"
	"github.com/scarypheonix/meta/internal/obj"
	"github.com/scarypheonix/meta/internal/parse"
	"github.com/scarypheonix/meta/internal/prelude"
	"github.com/scarypheonix/meta/internal/resolve"
	"github.com/scarypheonix/meta/internal/source"
)

// buildStackMapTestImage compiles src all the way to a native image, the same pipeline
// internal/driver's runNative uses.
func buildStackMapTestImage(t *testing.T, src string) *obj.Image {
	t.Helper()
	ids := ast.NewIDGen()
	bag := diag.New()
	pre := parse.FileWith(prelude.Source(), diag.New(), ids)
	user := parse.FileWith(source.NewFile("case.origin", src), bag, ids)
	if bag.HasErrors() {
		t.Fatalf("parse:\n%s", bag)
	}
	res := resolve.Program(bag, resolve.Input{File: pre, Prelude: true}, resolve.Input{File: user})
	if bag.HasErrors() {
		t.Fatalf("resolve:\n%s", bag)
	}
	tys := check.Program(bag, res, pre, user)
	if bag.HasErrors() {
		t.Fatalf("check:\n%s", bag)
	}
	mo := mono.Program(bag, tys, pre, user)
	if bag.HasErrors() {
		t.Fatalf("mono:\n%s", bag)
	}
	prog, err := compile.Program(res, tys, mo, pre, user)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	img, err := Build(prog, obj.Linux)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return img
}

// stackMapFrom reads the table an image's own runtime block points at, exactly the way
// the future collector will: the address and count are compile-time constants poked
// directly into the initial data segment (writeStackMapFields), and the table itself
// lives in read-only data at that address.
func stackMapFrom(t *testing.T, img *obj.Image) []layout.StackMapEntry {
	t.Helper()
	if len(img.Data) < rtStackMapCountOff+8 {
		t.Fatalf("the data segment is %d bytes, too small to hold the stack-map fields", len(img.Data))
	}
	addr := binary.LittleEndian.Uint64(img.Data[rtStackMapOff:])
	count := binary.LittleEndian.Uint64(img.Data[rtStackMapCountOff:])
	if addr < img.RoDataAddr || addr > img.RoDataAddr+uint64(len(img.RoData)) {
		t.Fatalf("stack-map table address %#x is outside read-only data [%#x, %#x)",
			addr, img.RoDataAddr, img.RoDataAddr+uint64(len(img.RoData)))
	}
	off := addr - img.RoDataAddr
	end := off + count*layout.StackMapEntrySize
	if end > uint64(len(img.RoData)) {
		t.Fatalf("stack-map table [%#x, %#x) runs past the end of read-only data (%d bytes)",
			off, end, len(img.RoData))
	}
	return layout.DecodeStackMap(img.RoData[off:end])
}

// TestStackMapTableIsWellFormed checks the table any native build now produces, end to
// end through the real compiler: every entry's return address must land inside the
// text segment (it names a real call site), and the whole table must be sorted by
// address (EncodeStackMap's own precondition, which LookupStackMap's binary search
// depends on).
func TestStackMapTableIsWellFormed(t *testing.T) {
	img := buildStackMapTestImage(t, `
struct Pair { a: i64, b: i64 }
struct Holder { x: Pair, y: Pair }

fn build(p: Pair) -> Holder {
    let extra = Pair { a: 9, b: 9 };
    Holder { x: p, y: extra }
}

fn main() {
    let h = build(Pair { a: 1, b: 2 });
}
`)
	entries := stackMapFrom(t, img)
	if len(entries) == 0 {
		t.Fatal("the table has no entries at all; every allocation in this program should have one")
	}
	for i, e := range entries {
		if e.ReturnAddr < img.TextAddr || e.ReturnAddr >= img.TextAddr+uint64(len(img.Text)) {
			t.Errorf("entry %d's return address %#x is outside the text segment [%#x, %#x)",
				i, e.ReturnAddr, img.TextAddr, img.TextAddr+uint64(len(img.Text)))
		}
		if i > 0 && entries[i-1].ReturnAddr >= e.ReturnAddr {
			t.Errorf("entries %d and %d are not in strictly ascending address order (%#x, then %#x)",
				i-1, i, entries[i-1].ReturnAddr, e.ReturnAddr)
		}
	}
}

// TestStackMapRecordsALiveReferenceInARegisterAcrossAnAllocation is the table's actual
// job: building Holder{x: p, y: extra} allocates while both p and extra -- themselves
// references (Pair is a struct) -- are still needed for the second field. Both intervals
// span that call, so ADR-0018's own allocation invariant puts each in a callee-saved
// register, and at least one entry in the table must say so.
func TestStackMapRecordsALiveReferenceInARegisterAcrossAnAllocation(t *testing.T) {
	img := buildStackMapTestImage(t, `
struct Pair { a: i64, b: i64 }
struct Holder { x: Pair, y: Pair }

fn build(p: Pair) -> Holder {
    let extra = Pair { a: 9, b: 9 };
    Holder { x: p, y: extra }
}

fn main() {
    let h = build(Pair { a: 1, b: 2 });
}
`)
	entries := stackMapFrom(t, img)
	found := false
	for _, e := range entries {
		if e.RegMask != 0 {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no entry records a live reference in a register; got %d entries, all RegMask=0", len(entries))
	}
}

// TestStackMapEntryCountMatchesTheRuntimeBlock is a consistency check on
// writeStackMapFields itself: the count it wrote must match how many entries actually
// decode, so the collector's own read of rtStackMapCountOff can be trusted.
func TestStackMapEntryCountMatchesTheRuntimeBlock(t *testing.T) {
	img := buildStackMapTestImage(t, `
use std::io;

fn main() {
    let s = "hello";
    io::println(s);
}
`)
	count := binary.LittleEndian.Uint64(img.Data[rtStackMapCountOff:])
	entries := stackMapFrom(t, img)
	if uint64(len(entries)) != count {
		t.Errorf("decoded %d entries, but the runtime block says %d", len(entries), count)
	}
}
