//go:build !js

package stdin

import (
	"io"
	"os"
)

// Default is the process's own standard input.
func Default() io.Reader { return os.Stdin }
