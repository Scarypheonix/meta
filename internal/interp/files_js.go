//go:build js

package interp

import (
	"github.com/scarypheonix/meta/internal/compile"
	"github.com/scarypheonix/meta/internal/diag"
)

// The file operations, interpreted, on a host that has no files (ADR-0033).
//
// This file is the whole of what the browser changes about either engine. It exists rather
// than a branch inside the os-backed one because Go's js/wasm port ships an `fs` shim that
// a Node host backs with the real filesystem: a build that merely declined to use `os`
// would still be a build that could. Selecting this implementation means the calls are not
// linked, so the module cannot reach a filesystem rather than being trusted not to.
//
// Every answer here is true rather than convenient (process rule 8). No file exists, so
// `file_exists` is `false`; the read did not happen, so it is `IOOther` -- the "everything
// else" status spec/15-files.md already defines, which the prelude turns into
// `Err(IoError::Other)`. `IoError` does not grow a case for one host.
func (in *Interp) fsBuiltin(name string, args []Value, span diag.Span) (Value, bool) {
	switch name {
	case "fs::read_file", "fs::write_file":
		return Int(compile.IOOther), true

	case "fs::taken_text":
		// Unreachable behind a successful read, because there are none. It answers at all
		// so that a program calling it directly gets a String rather than a missing case.
		return &Str{S: ""}, true

	case "fs::file_exists":
		return Bool(false), true
	}
	return nil, false
}
