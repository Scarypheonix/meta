package interp_test

import (
	"io"

	"github.com/scarypheonix/meta/internal/driver"
	"github.com/scarypheonix/meta/internal/interp"
)

// newInterp runs a compiled program, returning its exit status. It exists so the tests
// can drive the interpreter directly without going through the file system.
func newInterp(prog *driver.Program, stdout, stderr io.Writer) int {
	return interp.New(prog.Resolved, stdout, stderr).Run()
}
