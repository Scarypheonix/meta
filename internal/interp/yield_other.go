//go:build !js

package interp

// backEdge is a no-op where the host preempts on its own, which is every target but the
// browser. See yield_js.go for why that one is different. An empty method inlines away, so
// a loop on these hosts pays nothing for the browser's requirement.
func (in *Interp) backEdge() {}
