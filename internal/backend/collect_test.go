package backend

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/ir"
	"github.com/scarypheonix/meta/internal/obj"
)

// A collection is where every root the backend forgot to declare shows itself, and only
// there: a reference the collector cannot find is copied nowhere, so the program keeps
// using the address it had before the objects moved and reads whatever the vacated
// semispace still happens to hold. Nothing about such a program looks wrong until a
// collection actually runs, which is why these tests shrink the heap (heapSize,
// runtime.go) rather than trusting the corpus to allocate 64 MiB.

// runWithHeap builds src for Linux with each semispace shrunk to size bytes, runs the
// executable and returns its stdout with the exit status.
func runWithHeap(t *testing.T, size int32, src string) (string, int) {
	t.Helper()
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skipf("this host cannot run an x86-64 ELF (%s/%s)", runtime.GOOS, runtime.GOARCH)
	}

	restore := heapSize
	heapSize = size
	img := buildStackMapTestImageFor(t, obj.Linux, src)
	heapSize = restore

	path := filepath.Join(t.TempDir(), "prog")
	var buf bytes.Buffer
	if err := img.Write(&buf); err != nil {
		t.Fatalf("writing image: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o755); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.Command(path)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("running the compiled program: %v", err)
		}
		code = ee.ExitCode()
	}
	if stderr.Len() > 0 {
		t.Logf("stderr:\n%s", stderr.String())
	}
	return stdout.String(), code
}

func checkRun(t *testing.T, src, want string) {
	t.Helper()
	out, code := runWithHeap(t, 64<<10, src)
	if code != 0 {
		t.Fatalf("exit status %d, want 0 (stdout %q)", code, out)
	}
	if strings.TrimSpace(out) != want {
		t.Errorf("stdout is %q, want %q", strings.TrimSpace(out), want)
	}
}

// listSource is the smallest program that keeps a reference in a constructor's own
// operand across the allocation that constructor makes: `tail: out` names the list built
// so far, and the object holding it is allocated after that value is read. Building
// enough of them to overflow the heap several times makes the collection land in the
// middle of one such construction.
const listSource = `
use std::io;

struct Node { value: i64, tail: Option[Node] }

fn build(n: i64) -> Option[Node] {
    let mut out = Option::None;
    let mut i = 0;
    while i < n {
        out = Option::Some(Node { value: i, tail: out });
        i = i + 1;
    }
    out
}

fn total(list: Option[Node]) -> i64 {
    let mut sum = 0;
    let mut cur = list;
    while true {
        match cur {
            Option::Some(nd) => { sum = sum + nd.value; cur = nd.tail; },
            Option::None => { return sum; },
        }
    }
    sum
}

fn main() {
    let mut acc = 0;
    let mut k = 0;
    while k < 400 {
        acc = acc + total(build(80));
        k = k + 1;
    }
    io::println(acc.to_str());
}
`

// TestACollectionForwardsAConstructorsOperands is the regression test for a field value
// parked on the raw stack across the allocator call that construct (lower.go) used to
// emit. The stack is not part of the collector's root set -- registers named by RegMask
// and the reference slots RefOffset/RefCount describe are (ADR-0021, ADR-0022) -- so the
// popped operand named the object's pre-collection address and the new node's `tail`
// pointed into the space just vacated. The list then ended in a word that decoded as
// neither variant of Option and the program trapped with `no match arm matched`.
func TestACollectionForwardsAConstructorsOperands(t *testing.T) {
	// 400 lists of 80 nodes: sum(0..79) = 3160 each.
	checkRun(t, listSource, "1264000")
}

// TestACollectionForwardsAClosuresCaptures is the same defect one level further out. A
// closure body's own object arrives just above the return address (lower.go's
// callClosure), which no stack map can name, and OpCapture used to read it from there
// every time. A collection inside the body moved the closure, so every capture read after
// it went to the abandoned copy: this program's counter silently lost exactly the
// increments that landed on a collection.
func TestACollectionForwardsAClosuresCaptures(t *testing.T) {
	checkRun(t, `
use std::io;

struct Cell { mut value: i64 }
struct Node { value: i64, tail: Option[Node] }

fn build(n: i64) -> Option[Node] {
    let mut out = Option::None;
    let mut i = 0;
    while i < n { out = Option::Some(Node { value: i, tail: out }); i = i + 1; }
    out
}

fn total(list: Option[Node]) -> i64 {
    let mut sum = 0;
    let mut cur = list;
    while true {
        match cur {
            Option::Some(nd) => { sum = sum + nd.value; cur = nd.tail; },
            Option::None => { return sum; },
        }
    }
    sum
}

fn make(base: i64) -> fn(i64) -> i64 {
    let cell = Cell { value: base };
    |n| {
        let junk = total(build(n));
        cell.value = cell.value + 1;
        cell.value + junk
    }
}

fn main() {
    let f = make(1000);
    let mut k = 0;
    let mut last = 0;
    while k < 300 { last = f(50); k = k + 1; }
    io::println(last.to_str());
}
`, "2525") // 1000 + 300 increments, plus sum(0..49) = 1225.
}

// TestAClosureBodyReservesAReferenceSlotForItsOwnObject checks the mechanism the test
// above can only observe indirectly, and on every host rather than only an x86-64 Linux
// one: a function with captures gets reference slot 1 for the closure object, which puts
// it inside the area RefOffset/RefCount already describes, and its own spills start
// after it.
func TestAClosureBodyReservesAReferenceSlotForItsOwnObject(t *testing.T) {
	body := ir.NewFunc("closure body", 0, 2)
	body.Entry.SetTerminator(body.NewValue(ir.OpReturn, diag.Span{},
		body.Entry.Append(body.NewValue(ir.OpUnit, diag.Span{}))))
	a := allocate(body)
	if a.closureSlot != 1 {
		t.Errorf("closureSlot is %d, want 1", a.closureSlot)
	}
	if a.refSlots < 1 {
		t.Errorf("refSlots is %d, so the stack map would not cover the closure slot", a.refSlots)
	}

	plain := ir.NewFunc("plain", 0, 0)
	plain.Entry.SetTerminator(plain.NewValue(ir.OpReturn, diag.Span{},
		plain.Entry.Append(plain.NewValue(ir.OpUnit, diag.Span{}))))
	if b := allocate(plain); b.closureSlot != 0 {
		t.Errorf("a function with no captures reserved slot %d", b.closureSlot)
	}
}

// TestACollectionWalksAJoiningThreadsOwnFrames covers the other half of the root set: a
// stack that is not the one that allocated. `main` parks inside `join` while the thread it
// joined runs and allocates, so every reference `main` was holding is reachable only
// through its own parked frames.
//
// Two separate defects made that fail, and both had to be fixed to make it pass. `main`
// was deliberately left off the runtime's thread list, on the reasoning that the running
// thread's stack is walked from its live registers -- true only while `main` is the thread
// running, and it is not while it waits inside `join`. And `rt_join` set up no frame
// pointer, so the rbp `rt_switch` saved for the parked thread was `main`'s rather than
// `rt_join`'s: the walk then described `main`'s frame with the synthetic entry a runtime
// frame gets (no roots, all four registers saved) instead of `main`'s own stack map, and
// read its roots out of the wrong slots.
func TestACollectionWalksAJoiningThreadsOwnFrames(t *testing.T) {
	checkRun(t, `
use std::io;
use std::thread;

struct Node { value: i64, tail: Option[Node] }

fn build(n: i64) -> Option[Node] {
    let mut out = Option::None;
    let mut i = 0;
    while i < n { out = Option::Some(Node { value: i, tail: out }); i = i + 1; }
    out
}

fn total(list: Option[Node]) -> i64 {
    let mut sum = 0;
    let mut cur = list;
    while true {
        match cur {
            Option::Some(nd) => { sum = sum + nd.value; cur = nd.tail; },
            Option::None => { return sum; },
        }
    }
    sum
}

fn main() {
    // Held across the join, and reachable only through main's own parked frames.
    let keep = build(50);
    let h = thread::spawn(|| -> i64 {
        let mut acc = 0;
        let mut k = 0;
        while k < 200 { acc = acc + total(build(60)); k = k + 1; }
        acc
    });
    let got = h.join();
    io::println(total(keep).to_str());
    io::println(got.to_str());
}
`, "1225\n354000")
}

// TestACollectionWalksEveryThreadsStacks is the multi-thread version: three spawned
// threads allocating, one of them joined and the other two left for the drain `_start`
// runs after `main` returns (sched.go), while `main` holds a list of its own across all of
// it. Every one of those stacks is a root set, and the list `main` keeps is the assertion
// that none of them was walked in a way that missed or moved it wrongly.
func TestACollectionWalksEveryThreadsStacks(t *testing.T) {
	checkRun(t, `
use std::io;
use std::thread;

struct Node { value: i64, tail: Option[Node] }

fn build(n: i64) -> Option[Node] {
    let mut out = Option::None;
    let mut i = 0;
    while i < n { out = Option::Some(Node { value: i, tail: out }); i = i + 1; }
    out
}

fn total(list: Option[Node]) -> i64 {
    let mut sum = 0;
    let mut cur = list;
    while true {
        match cur {
            Option::Some(nd) => { sum = sum + nd.value; cur = nd.tail; },
            Option::None => { return sum; },
        }
    }
    sum
}

fn work() -> i64 {
    let mut acc = 0;
    let mut k = 0;
    while k < 60 { acc = acc + total(build(40)); k = k + 1; }
    acc
}

fn main() {
    let keep = build(50);
    let a = thread::spawn(|| -> i64 { work() });
    let b = thread::spawn(|| -> i64 { work() });
    let c = thread::spawn(|| -> i64 { work() });
    io::println(total(keep).to_str());
    io::println(a.join().to_str());
    io::println(total(keep).to_str());
}
`, "1225\n46800\n1225")
}

// TestACollectionForwardsValuesSittingInAChannel is the channel half of the root set. A
// value crossing a channel is an ordinary heap reference, but the queue holding it is raw
// mmap'd memory with no stack map over it and no header to read a shape from -- so the
// collector walks it from the outside, told by the compiler whether this channel's element
// type is a reference at all (chan.go, collect.go's gcEvacuateChannels).
//
// The producer allocates hard enough to collect many times over with lists queued behind
// it, so a queue slot the collector failed to rewrite is a list that reads back as garbage
// rather than as a sum.
func TestACollectionForwardsValuesSittingInAChannel(t *testing.T) {
	checkRun(t, `
use std::io;
use std::thread;
use std::chan;

struct Node { value: i64, tail: Option[Node] }

fn build(n: i64) -> Option[Node] {
    let mut out = Option::None;
    let mut i = 0;
    while i < n { out = Option::Some(Node { value: i, tail: out }); i = i + 1; }
    out
}

fn total(list: Option[Node]) -> i64 {
    let mut sum = 0;
    let mut cur = list;
    while true {
        match cur {
            Option::Some(nd) => { sum = sum + nd.value; cur = nd.tail; },
            Option::None => { return sum; },
        }
    }
    sum
}

fn main() {
    let (s, r) = chan::channel[Option[Node]](4);
    let h = thread::spawn(|| -> i64 {
        let mut i = 0;
        while i < 60 {
            s.send(build(30));
            i = i + 1;
        }
        s.close();
        0
    });
    let mut acc = 0;
    while true {
        match r.recv() {
            Option::Some(list) => { acc = acc + total(list); },
            Option::None => { io::println(acc.to_str()); return; },
        }
    }
}
`, "26100") // 60 lists of sum(0..29) = 435.
}
