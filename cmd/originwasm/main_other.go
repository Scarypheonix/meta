//go:build !js

// This file exists so that `go build ./...` and `./check` have something to compile here
// on Linux and macOS. The command itself is WebAssembly-only: it imports `syscall/js`,
// which no other target has.
//
// It refuses rather than doing something approximate (process rule 8). The build line is
// in web/README.md and in tests/wasm, which builds it the same way.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr,
		"originwasm is the browser host and only builds for WebAssembly.\n"+
			"Build it with:\n"+
			"  GOOS=js GOARCH=wasm go build -o web/origin.wasm ./cmd/originwasm")
	os.Exit(2)
}
