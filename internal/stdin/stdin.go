// Package stdin reads standard input a line at a time, the way spec/18-input.md says it
// is read.
//
// It exists as its own package for process rule 5's reason: the interpreter and the
// virtual machine have to answer `io::read_line` identically -- where a line ends, what
// counts as the last one, and which input is refused -- and an agreement written twice
// agrees by luck. The native runtime is the third implementation and cannot share this
// one, so what this package is really for is making the *rules* readable in one place for
// whoever writes the machine code against them.
package stdin

import (
	"bufio"
	"io"

	"github.com/scarypheonix/meta/internal/compile"
	"github.com/scarypheonix/meta/internal/layout"
)

// Reader is one program's view of standard input. A program has exactly one, because
// standard input is a position in a stream the process owns and there is no handle in the
// language to hold a second one (ADR-0043).
type Reader struct {
	br *bufio.Reader
}

// New wraps a source of bytes. The driver passes the process's own standard input; the
// browser passes the text in the playground's input box (ADR-0033's one exception).
func New(r io.Reader) *Reader { return &Reader{br: bufio.NewReader(r)} }

// ReadLine returns the next line without its `\n`, and a status from internal/compile:
//
//	IOOk          a line, which may be empty
//	IOEndOfInput  there was nothing left to read
//	IOTooLong     the line is longer than layout.MaxInputLine
//	IOOther       the source itself failed
//
// The final line of the input needs no terminator: `a\nb` is two lines and so is `a\nb\n`.
// That is what makes a file and a person's typing read the same, and it is why this cannot
// be `bufio.Scanner` plus a length check -- a Scanner reports a too-long line as the end of
// the input, which is the one answer here that must never be confused with another.
//
// The `\r` of a `\r\n` is left on. The prelude strips it, so the rule lives in one place
// for all three engines rather than in each runtime (spec/18-input.md).
func (r *Reader) ReadLine() (string, int) {
	var line []byte
	for {
		b, err := r.br.ReadByte()
		if err != nil {
			// A last line with no terminator is still a line; only an error with
			// nothing accumulated is the end, and only a *clean* end is
			// IOEndOfInput. A source that actually failed is not the same fact and
			// does not get to look like one.
			if len(line) > 0 {
				return string(line), compile.IOOk
			}
			if err == io.EOF {
				return "", compile.IOEndOfInput
			}
			return "", compile.IOOther
		}
		if b == '\n' {
			return string(line), compile.IOOk
		}
		if int64(len(line)) >= layout.MaxInputLine {
			return "", compile.IOTooLong
		}
		line = append(line, b)
	}
}
