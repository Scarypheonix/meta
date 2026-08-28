// Package source owns file identity and the byte-offset-to-line/column mapping.
//
// Every span in the compiler is a byte range into a File. Line and column are derived
// here and nowhere else, so the rules in spec/01-lexical.md (CRLF counts as one
// terminator, a lone CR does not terminate a line, columns count Unicode scalar values)
// have exactly one implementation.
package source

import (
	"sort"
	"unicode/utf8"
)

// File is an immutable source file with a precomputed line index.
type File struct {
	Name string
	Src  string
	// lineStarts[i] is the byte offset at which line i+1 begins. lineStarts[0] is
	// always 0.
	lineStarts []int
}

// NewFile indexes src for position lookup. Name is used verbatim in diagnostics.
func NewFile(name, src string) *File {
	f := &File{Name: name, Src: src, lineStarts: []int{0}}
	for i := 0; i < len(src); i++ {
		if src[i] == '\n' {
			f.lineStarts = append(f.lineStarts, i+1)
		}
	}
	return f
}

// LineCount reports the number of lines. A file ending in a newline does not have a
// trailing empty line counted unless there is content after it.
func (f *File) LineCount() int { return len(f.lineStarts) }

// Position converts a byte offset to a 1-based line and a 1-based column counted in
// Unicode scalar values. An offset past the end clamps to the end of the file.
func (f *File) Position(offset int) (line, col int) {
	if offset < 0 {
		offset = 0
	}
	if offset > len(f.Src) {
		offset = len(f.Src)
	}
	// The first line whose start is > offset, minus one, is the line containing offset.
	i := sort.Search(len(f.lineStarts), func(i int) bool { return f.lineStarts[i] > offset })
	line = i // because lineStarts is 0-based and lines are 1-based, i is already line-1+1
	start := f.lineStarts[i-1]

	// Count scalar values, not bytes, from the start of the line.
	col = 1
	for p := start; p < offset; {
		_, size := utf8.DecodeRuneInString(f.Src[p:])
		if size == 0 {
			size = 1
		}
		p += size
		col++
	}
	return line, col
}

// LineText returns the text of a 1-based line, without its terminator. A \r\n pair
// counts as one terminator, so the \r is stripped; a lone \r is ordinary whitespace and
// is kept (spec/01-lexical.md).
func (f *File) LineText(line int) string {
	if line < 1 || line > len(f.lineStarts) {
		return ""
	}
	start := f.lineStarts[line-1]
	end := len(f.Src)
	if line < len(f.lineStarts) {
		end = f.lineStarts[line] - 1 // drop the \n
	}
	if end > start && end <= len(f.Src) && end-1 >= start && f.Src[end-1] == '\r' {
		end--
	}
	return f.Src[start:end]
}

// LineStart returns the byte offset at which a 1-based line begins.
func (f *File) LineStart(line int) int {
	if line < 1 {
		return 0
	}
	if line > len(f.lineStarts) {
		return len(f.Src)
	}
	return f.lineStarts[line-1]
}
