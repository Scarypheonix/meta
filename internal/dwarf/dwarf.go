// Package dwarf encodes the minimal DWARF4 debug information a native Origin binary
// carries (ADR-0023): a line-number program mapping machine-code addresses to source
// positions, and the one compile-unit DIE that anchors it. Full DWARF type and variable
// information is out of scope — the spec's own acceptance criterion is a working
// `breakpoint set --file f --line N`, and a function's *name* for `bt` comes from a
// plain symbol table (Funcs here, encoded by internal/obj into whichever container
// format's own symbol table), never from a DWARF subprogram DIE.
//
// This package only produces bytes; internal/obj decides where they sit in a file and
// how ELF's section headers or Mach-O's __DWARF segment describe them (process rule 5:
// the encoding is the one place both agree, neither derives it independently).
package dwarf

import "sort"

// Line is one source-position row: the machine-code address where a source line begins.
// Origin has no macro or inlining expansion, so "where a line begins" is unambiguous —
// unlike a compiler that must also record which line a row's code was *inlined from*.
type Line struct {
	// Address is a virtual address inside .text.
	Address uint64
	// File is the source file's own recorded name (source.File.Name), used verbatim:
	// spec/11-codegen.md's Determinism clause forbids resolving it to an absolute path,
	// which would vary across checkouts and machines.
	File string
	// Line and Col are 1-based, matching source.File.Position and DWARF's own
	// convention. Col 0 means "column not tracked for this row."
	Line, Col int
}

// Func names one function's address range, for the symbol table `bt` reads a frame's
// name from.
type Func struct {
	Name    string
	Address uint64
	Size    uint64
}

// Build encodes .debug_abbrev, .debug_info and .debug_line for one compiled program.
// lines must be sorted by Address ascending (BuildLineProgram re-sorts defensively, but
// the caller — internal/backend — already produces them in address order, since it
// records one entry per emitted instruction's line change while walking blocks in the
// order they are laid out).
//
// lowPC and highPC bound the whole program's own .text: the compile-unit DIE's address
// range, and where the line-number program's own DW_LNE_end_sequence closes the final
// row. Both are addresses the caller already has (backend.Build's own text layout), so
// this package never needs to know anything about segments or files beyond the strings
// in Line.File.
func Build(main string, lines []Line, lowPC, highPC uint64) (abbrev, info, line []byte) {
	files := fileTable(lines)
	return buildAbbrev(), buildInfo(primary(main, files), lowPC, highPC), buildLineProgram(lines, files, highPC)
}

// primary is what the compile unit is named after: the file `main` is written in.
//
// It is passed in rather than taken from the line table, because the line table is in
// address order and the lowest address is whichever function the code generator emitted
// first -- which is a prelude one as soon as the prelude contributes any non-generic
// function at all, and would name every Origin program `<prelude>`. An empty name, or one
// that contributed no line at all, falls back to the first file, which is what this did
// before there was anything to get wrong.
func primary(main string, files []string) string {
	for _, f := range files {
		if f == main {
			return f
		}
	}
	if len(files) > 0 {
		return files[0]
	}
	return "<unknown>"
}

// fileTable returns every distinct file name in lines, in order of first appearance —
// deterministic because lines itself is produced in a deterministic (address) order.
func fileTable(lines []Line) []string {
	seen := map[string]bool{}
	var files []string
	for _, l := range lines {
		if l.File == "" || seen[l.File] {
			continue
		}
		seen[l.File] = true
		files = append(files, l.File)
	}
	if len(files) == 0 {
		files = []string{"<unknown>"}
	}
	return files
}

// fileIndex is a Line.File's 1-based position in files, DWARF's own file-table
// convention (index 0 is reserved).
func fileIndex(files []string, name string) int {
	for i, f := range files {
		if f == name {
			return i + 1
		}
	}
	return 1
}

// sortLines returns lines sorted by Address, defensively: the encoders below assume
// ascending order (a line-number program is a sequence of forward-only address advances)
// and produce nonsense silently rather than an error if that is violated, so this is the
// one place that invariant is enforced regardless of what the caller does.
func sortLines(lines []Line) []Line {
	out := make([]Line, len(lines))
	copy(out, lines)
	sort.Slice(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	return out
}
