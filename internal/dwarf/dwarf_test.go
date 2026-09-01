package dwarf

import (
	"bytes"
	stddwarf "debug/dwarf"
	"testing"
)

// TestBuildRoundTripsThroughTheStandardLibraryReader is the real oracle for this
// package: Go's own debug/dwarf is an independent implementation of the same format, so
// parsing the bytes Build produces back through it is a much stronger check than
// anything this package could assert about its own encoding.
func TestBuildRoundTripsThroughTheStandardLibraryReader(t *testing.T) {
	lines := []Line{
		{Address: 0x401020, File: "hello.origin", Line: 6, Col: 1},
		{Address: 0x401000, File: "hello.origin", Line: 3, Col: 5},
		{Address: 0x401010, File: "hello.origin", Line: 4, Col: 5},
	}
	abbrev, info, line := Build("hello.origin", lines, 0x401000, 0x401030)

	d, err := stddwarf.New(abbrev, nil, nil, info, line, nil, nil, nil)
	if err != nil {
		t.Fatalf("debug/dwarf rejected the encoded sections: %v", err)
	}

	r := d.Reader()
	cu, err := r.Next()
	if err != nil {
		t.Fatalf("reading the compile-unit DIE: %v", err)
	}
	if cu == nil || cu.Tag != stddwarf.TagCompileUnit {
		t.Fatalf("first DIE is %+v, want a compile unit", cu)
	}
	if name, _ := cu.Val(stddwarf.AttrName).(string); name != "hello.origin" {
		t.Errorf("DW_AT_name = %q, want %q", name, "hello.origin")
	}
	if low, _ := cu.Val(stddwarf.AttrLowpc).(uint64); low != 0x401000 {
		t.Errorf("DW_AT_low_pc = %#x, want %#x", low, 0x401000)
	}
	// DW_FORM_data8 puts DW_AT_high_pc in debug/dwarf's ClassConstant, whose Go type is
	// int64 (an offset from low_pc, DWARF4 §2.17.2), not ClassAddress's uint64.
	if high, _ := cu.Val(stddwarf.AttrHighpc).(int64); high != 0x30 {
		t.Errorf("DW_AT_high_pc = %#x, want an offset of %#x", high, 0x30)
	}
	ranges, err := d.Ranges(cu)
	if err != nil || len(ranges) != 1 || ranges[0] != [2]uint64{0x401000, 0x401030} {
		t.Errorf("d.Ranges(cu) = %v, err %v, want a single [0x401000, 0x401030) range", ranges, err)
	}
	if next, err := r.Next(); err != nil || next != nil {
		t.Errorf("a second DIE exists (%+v); ADR-0023 scopes exactly one", next)
	}

	lr, err := d.LineReader(cu)
	if err != nil {
		t.Fatalf("opening the line reader: %v", err)
	}

	// Build sorts by address regardless of the order lines were given in, so the rows
	// read back in ascending address order, ending with the synthetic end-of-sequence
	// row DW_LNE_end_sequence always adds.
	want := []struct {
		addr uint64
		line int
		col  int
	}{
		{0x401000, 3, 5},
		{0x401010, 4, 5},
		{0x401020, 6, 1},
	}
	for i, w := range want {
		var e stddwarf.LineEntry
		if err := lr.Next(&e); err != nil {
			t.Fatalf("row %d: %v", i, err)
		}
		if e.Address != w.addr || e.Line != w.line || e.Column != w.col {
			t.Errorf("row %d = {addr:%#x line:%d col:%d}, want {addr:%#x line:%d col:%d}",
				i, e.Address, e.Line, e.Column, w.addr, w.line, w.col)
		}
		if e.File == nil || e.File.Name != "hello.origin" {
			t.Errorf("row %d's file is %v, want hello.origin", i, e.File)
		}
	}

	var end stddwarf.LineEntry
	if err := lr.Next(&end); err != nil {
		t.Fatalf("end-of-sequence row: %v", err)
	}
	if !end.EndSequence || end.Address != 0x401030 {
		t.Errorf("end-of-sequence row = {endSeq:%v addr:%#x}, want {true %#x}",
			end.EndSequence, end.Address, uint64(0x401030))
	}
	if err := lr.Next(&end); err == nil {
		t.Error("a row exists past end-of-sequence")
	}
}

// TestBuildIsDeterministic mirrors what ADR-0023 relies on for the two-pass emitter:
// the same logical input, built twice, produces byte-identical sections.
func TestBuildIsDeterministic(t *testing.T) {
	lines := []Line{
		{Address: 0x401000, File: "a.origin", Line: 1, Col: 1},
		{Address: 0x401010, File: "b.origin", Line: 9, Col: 3},
	}
	a1, i1, l1 := Build("a.origin", lines, 0x401000, 0x401020)
	a2, i2, l2 := Build("a.origin", lines, 0x401000, 0x401020)
	if string(a1) != string(a2) || string(i1) != string(i2) || string(l1) != string(l2) {
		t.Error("building the same lines twice produced different bytes")
	}
}

// TestBuildHandlesMultipleFiles: a program spanning more than one source file (modules)
// gets a file table with one entry per file, in first-appearance order.
func TestBuildHandlesMultipleFiles(t *testing.T) {
	lines := []Line{
		{Address: 0x401000, File: "main.origin", Line: 1, Col: 1},
		{Address: 0x401010, File: "util.origin", Line: 2, Col: 1},
		{Address: 0x401020, File: "main.origin", Line: 5, Col: 1},
	}
	abbrev, info, line := Build("main.origin", lines, 0x401000, 0x401030)

	d, err := stddwarf.New(abbrev, nil, nil, info, line, nil, nil, nil)
	if err != nil {
		t.Fatalf("debug/dwarf rejected the encoded sections: %v", err)
	}
	r := d.Reader()
	cu, err := r.Next()
	if err != nil || cu == nil {
		t.Fatalf("reading the compile-unit DIE: %v", err)
	}
	lr, err := d.LineReader(cu)
	if err != nil {
		t.Fatalf("opening the line reader: %v", err)
	}

	names := map[string]bool{}
	for {
		var e stddwarf.LineEntry
		if err := lr.Next(&e); err != nil {
			break
		}
		if e.EndSequence {
			continue
		}
		if e.File == nil {
			t.Fatal("a row has no file")
		}
		names[e.File.Name] = true
	}
	if !names["main.origin"] || !names["util.origin"] {
		t.Errorf("rows named %v, want both main.origin and util.origin", names)
	}
}

// TestCompileUnitIsNamedForMainNotTheLowestAddress: the compile unit takes its name from
// the file it is told is the program's, not from the first row of a table that is sorted
// by address. Which function lands lowest in .text is the code generator's business, and
// once the prelude contributes a function of its own it is a prelude one -- naming every
// Origin program `<prelude>` in every debugger that reads it.
func TestCompileUnitIsNamedForMainNotTheLowestAddress(t *testing.T) {
	lines := []Line{
		{Address: 0x401000, File: "<prelude>", Line: 12, Col: 5},
		{Address: 0x401010, File: "main.origin", Line: 3, Col: 5},
	}
	abbrev, info, line := Build("main.origin", lines, 0x401000, 0x401020)

	d, err := stddwarf.New(abbrev, nil, nil, info, line, nil, nil, nil)
	if err != nil {
		t.Fatalf("debug/dwarf rejected the encoded sections: %v", err)
	}
	cu, err := d.Reader().Next()
	if err != nil || cu == nil {
		t.Fatalf("reading the compile-unit DIE: %v", err)
	}
	if name, _ := cu.Val(stddwarf.AttrName).(string); name != "main.origin" {
		t.Errorf("DW_AT_name = %q, want %q", name, "main.origin")
	}
}

// A name that contributed no line row at all falls back to the first file, which is what
// this did before there was anything to get wrong.
func TestCompileUnitFallsBackToTheFirstFile(t *testing.T) {
	lines := []Line{{Address: 0x401000, File: "only.origin", Line: 1, Col: 1}}
	_, info, _ := Build("", lines, 0x401000, 0x401010)
	if !bytes.Contains(info, []byte("only.origin\x00")) {
		t.Error("the compile unit does not name the only file there is")
	}
}
