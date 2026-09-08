//go:build !js

package vm

// schedulerYield is a no-op where the host preempts on its own. See yield_js.go for why
// the browser build needs the explicit version and this one does not: an unconditional
// `runtime.Gosched` at every back edge would be a real cost on a host that does not need
// it, paid by every multi-threaded program.
func schedulerYield() {}
