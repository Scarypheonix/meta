//go:build js

package vm

import (
	"github.com/scarypheonix/meta/internal/compile"
	"github.com/scarypheonix/meta/internal/diag"
)

// The file operations, on the virtual machine, on a host that has no files (ADR-0033).
//
// The interpreter's counterpart in files_js.go carries the reasoning; this is the same
// four answers through the VM's value representation. The two engines have to agree about
// the browser exactly as they agree about Linux, and the way to make that true is for both
// to be this short.
func (v *VM) fsBuiltin(index int, args []Value, span diag.Span) (Value, bool) {
	switch index {
	case compile.BuiltinReadFile, compile.BuiltinWriteFile:
		return intVal(compile.IOOther), true

	case compile.BuiltinTakenText:
		// Unreachable behind a successful read, because there are none.
		return refVal(v.newString("", v.userSpan(span))), true

	case compile.BuiltinFileExists:
		return boolVal(false), true
	}
	return Value{}, false
}
