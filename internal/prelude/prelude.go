// Package prelude holds the Origin source that is in scope in every module.
//
// The prelude is written in Origin, not built into the compiler, so that the language's
// own types go through the same lexer, parser and resolver as user code. A prelude that
// fails to parse is a compiler bug and fails loudly at startup rather than producing
// confusing errors in the user's file.
package prelude

import (
	_ "embed"

	"github.com/scarypheonix/meta/internal/source"
)

//go:embed prelude.origin
var src string

// Name is the file name the prelude reports in diagnostics.
const Name = "<prelude>"

// Source returns the prelude as a source file.
func Source() *source.File { return source.NewFile(Name, src) }
