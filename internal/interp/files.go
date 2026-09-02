package interp

import (
	"errors"
	"io/fs"
	"os"
	"strings"

	"github.com/scarypheonix/meta/internal/compile"
	"github.com/scarypheonix/meta/internal/diag"
	"github.com/scarypheonix/meta/internal/layout"
)

// The file operations, interpreted (spec/15-files.md).
//
// The system calls are Go's here, so what this has to get right is not the I/O but the
// *classification*: which failure is `NotFound`, which is `PermissionDenied`, and which is
// everything else. All three engines have to agree on that, because the status is what the
// prelude turns into an `IoError` a program matches on.

func (in *Interp) fsBuiltin(name string, args []Value, span diag.Span) (Value, bool) {
	switch name {
	case "fs::read_file":
		path := in.strArg(args[0], span)
		if strings.ContainsRune(path, 0) {
			return Int(compile.IOOther), true
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return Int(ioStatus(err)), true
		}
		if int64(len(data)) > layout.MaxStringBytes {
			return Int(compile.IOOther), true
		}
		in.takenText = string(data)
		return Int(compile.IOOk), true

	case "fs::taken_text":
		s := in.takenText
		in.takenText = ""
		return &Str{S: s}, true

	case "fs::write_file":
		path := in.strArg(args[0], span)
		if strings.ContainsRune(path, 0) {
			return Int(compile.IOOther), true
		}
		if err := os.WriteFile(path, []byte(in.strArg(args[1], span)), 0o644); err != nil {
			return Int(ioStatus(err)), true
		}
		return Int(compile.IOOk), true

	case "fs::file_exists":
		path := in.strArg(args[0], span)
		if strings.ContainsRune(path, 0) {
			return Bool(false), true
		}
		f, err := os.Open(path)
		if err != nil {
			return Bool(false), true
		}
		f.Close()
		return Bool(true), true
	}
	return nil, false
}

// ioStatus classifies a failure the way spec/15-files.md's table does. The native runtime
// reads the same three cases off errno, so this is where the two vocabularies are made to
// say the same thing.
func ioStatus(err error) int {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return compile.IONotFound
	case errors.Is(err, fs.ErrPermission):
		return compile.IOPermission
	}
	return compile.IOOther
}
