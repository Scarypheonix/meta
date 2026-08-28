package source

import "testing"

func TestPosition(t *testing.T) {
	f := NewFile("t.origin", "abc\ndef\n\nghi")
	tests := []struct {
		offset    int
		line, col int
	}{
		{0, 1, 1}, {1, 1, 2}, {3, 1, 4}, // offset 3 is the \n, still on line 1
		{4, 2, 1}, {6, 2, 3},
		{8, 3, 1}, // the empty line
		{9, 4, 1}, {11, 4, 3},
		{12, 4, 4},  // one past the end of content
		{999, 4, 4}, // clamped
		{-5, 1, 1},  // clamped
	}
	for _, tt := range tests {
		line, col := f.Position(tt.offset)
		if line != tt.line || col != tt.col {
			t.Errorf("Position(%d) = %d:%d, want %d:%d", tt.offset, line, col, tt.line, tt.col)
		}
	}
}

func TestColumnsCountScalarValuesNotBytes(t *testing.T) {
	// "é" is 2 bytes, "😀" is 4. After both, the next column is 3.
	f := NewFile("t.origin", "é😀x")
	if line, col := f.Position(6); line != 1 || col != 3 {
		t.Errorf("Position(6) = %d:%d, want 1:3", line, col)
	}
	// A tab advances the column by exactly 1 (spec/01-lexical.md).
	g := NewFile("t.origin", "\t\tx")
	if _, col := g.Position(2); col != 3 {
		t.Errorf("tab column = %d, want 3", col)
	}
}

func TestLineText(t *testing.T) {
	f := NewFile("t.origin", "abc\r\ndef\n")
	if got := f.LineText(1); got != "abc" {
		t.Errorf("LineText(1) = %q, want %q (CRLF is one terminator)", got, "abc")
	}
	if got := f.LineText(2); got != "def" {
		t.Errorf("LineText(2) = %q, want %q", got, "def")
	}
	if got := f.LineText(99); got != "" {
		t.Errorf("LineText(99) = %q, want empty", got)
	}
}

func TestLoneCarriageReturnDoesNotStartALine(t *testing.T) {
	f := NewFile("t.origin", "a\rb\nc")
	if got := f.LineCount(); got != 2 {
		t.Errorf("LineCount = %d, want 2 (a lone \\r is whitespace, not a terminator)", got)
	}
	if line, _ := f.Position(2); line != 1 {
		t.Errorf("offset after lone \\r is on line %d, want 1", line)
	}
}

func TestLineStart(t *testing.T) {
	f := NewFile("t.origin", "ab\ncd\n")
	for _, tt := range []struct{ line, want int }{{1, 0}, {2, 3}, {0, 0}, {99, 6}} {
		if got := f.LineStart(tt.line); got != tt.want {
			t.Errorf("LineStart(%d) = %d, want %d", tt.line, got, tt.want)
		}
	}
}

func TestEmptyFile(t *testing.T) {
	f := NewFile("t.origin", "")
	if line, col := f.Position(0); line != 1 || col != 1 {
		t.Errorf("empty file Position(0) = %d:%d, want 1:1", line, col)
	}
	if f.LineCount() != 1 {
		t.Errorf("empty file LineCount = %d, want 1", f.LineCount())
	}
}
