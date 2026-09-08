//go:build js

package interp

// Aliased: this package has its own type named `runtime` (concurrent.go).
import gort "runtime"

// backEdge is spec/08-memory-model.md's safepoint, on the one host that needs to be asked
// for it explicitly.
//
// The interpreter's green threads are goroutines and its own comment in concurrent.go
// says why that is not a shortcut: Go's scheduler is M:N and it preempts, which is what
// §08 asks for. Under GOOS=js that stops being true. There is one thread and no
// asynchronous preemption -- preemption needs signals and a browser has none -- so a
// goroutine that never blocks never yields, and a loop waiting on another thread waits
// forever. Every other engine runs the program; this one hangs, which is the worst way to
// differ.
//
// So the browser build asks. `runtime.Gosched` at a back edge is the scheduling point the
// host does not insert on its own, and tests/e2e/cases/preemption_at_a_back_edge.origin is
// the program that holds it: a spin loop that finishes only if the thread setting its flag
// gets to run.
func (in *Interp) backEdge() {
	if in.rt.spawned.Load() {
		gort.Gosched()
	}
}
