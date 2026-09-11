//go:build js

package stdin

import (
	"io"
	"strings"
)

// Default is empty on a host that has no standard input (ADR-0033). The browser build does
// not fall through to this: cmd/originwasm hands the playground's input box to New, which
// is the one place ADR-0033's "no host facility" does not apply, because a page can have
// text in a box. This is what a program gets when nothing supplied any — the end of input,
// immediately — rather than a call into a shim Go's js port would back with something else.
func Default() io.Reader { return strings.NewReader("") }
