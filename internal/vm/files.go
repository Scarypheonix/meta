package vm

import (
	"errors"
	"io/fs"
	"os"
	"strings"

	"github.com/scarypheonix/meta/internal/compile"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/layout"
)

// The file operations, on the virtual machine (spec/15-files.md).
//
// The system calls are Go's here as they are in the interpreter, so what these two engines
// share is the *classification*: which failure is `NotFound`, which is `PermissionDenied`,
// and which is everything else. The native runtime reads the same three cases off errno.
//
// The bytes a read produced are held on the calling thread until `fs::taken_text` takes
// them, which is the arrangement `chan::await_value`/`chan::taken_value` uses and for the
// same reason: a compiler-provided operation returns a primitive or a String, never a
// prelude type, so the `Result` is built in Origin from two calls.

func (v *VM) fsBuiltin(index int, args []Value, span diag.Span) (Value, bool) {
	span = v.userSpan(span)
	switch index {
	case compile.BuiltinReadFile:
		path := v.heap.Bytes(v.strRef(args[0], span))
		if strings.ContainsRune(path, 0) {
			return intVal(compile.IOOther), true
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return intVal(int64(ioStatus(err))), true
		}
		if int64(len(data)) > layout.MaxStringBytes {
			return intVal(compile.IOOther), true
		}
		v.takenText = string(data)
		return intVal(compile.IOOk), true

	case compile.BuiltinTakenText:
		s := v.takenText
		v.takenText = ""
		return refVal(v.newString(s, span)), true

	case compile.BuiltinWriteFile:
		path := v.heap.Bytes(v.strRef(args[0], span))
		if strings.ContainsRune(path, 0) {
			return intVal(compile.IOOther), true
		}
		if err := os.WriteFile(path, []byte(v.heap.Bytes(v.strRef(args[1], span))), 0o644); err != nil {
			return intVal(int64(ioStatus(err))), true
		}
		return intVal(compile.IOOk), true

	case compile.BuiltinFileExists:
		path := v.heap.Bytes(v.strRef(args[0], span))
		if strings.ContainsRune(path, 0) {
			return boolVal(false), true
		}
		f, err := os.Open(path)
		if err != nil {
			return boolVal(false), true
		}
		f.Close()
		return boolVal(true), true
	}
	return Value{}, false
}

// ioStatus classifies a failure the way spec/15-files.md's table does.
func ioStatus(err error) int {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return compile.IONotFound
	case errors.Is(err, fs.ErrPermission):
		return compile.IOPermission
	}
	return compile.IOOther
}
