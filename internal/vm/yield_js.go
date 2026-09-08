//go:build js

package vm

import "runtime"

// schedulerYield hands the processor to another goroutine at a safepoint.
//
// It exists because releasing and re-acquiring the world lock is not, by itself, a
// scheduling point. On a host with real threads it is enough: a thread blocked on
// `exec.Lock` runs on another processor and takes the mutex the instant it is free, and
// Go's own starvation handoff backs that up. Under GOOS=js there is one thread and no
// asynchronous preemption -- no signals to interrupt a goroutine with -- so `Unlock`
// followed immediately by `Lock` re-acquires the lock every time and the waiting thread
// never runs. `runtime.Gosched` is the yield that host has to be asked for explicitly.
//
// spec/08-memory-model.md's "preemptive at safepoints" is the guarantee this keeps, and
// tests/e2e/cases/preemption_at_a_back_edge.origin is the program that holds it.
func schedulerYield() { runtime.Gosched() }
