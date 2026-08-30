package layout

import "testing"

func TestStackMapRoundTripsEveryEntryExactly(t *testing.T) {
	entries := []StackMapEntry{
		{ReturnAddr: 0x401000, RefOffset: 16, RefCount: 2, RegMask: 0b0001},
		{ReturnAddr: 0x401050, RefOffset: 24, RefCount: 0, RegMask: 0b1010},
		{ReturnAddr: 0x402000, RefOffset: 0, RefCount: 5, RegMask: 0b1111},
	}
	table := EncodeStackMap(entries)

	for _, want := range entries {
		got, ok := LookupStackMap(table, want.ReturnAddr)
		if !ok {
			t.Fatalf("no entry found for return address %#x", want.ReturnAddr)
		}
		if got != want {
			t.Errorf("entry for %#x = %+v, want %+v", want.ReturnAddr, got, want)
		}
	}
}

func TestStackMapLookupMissesAnAddressNotInTheTable(t *testing.T) {
	table := EncodeStackMap([]StackMapEntry{
		{ReturnAddr: 0x401000},
		{ReturnAddr: 0x402000},
	})
	if _, ok := LookupStackMap(table, 0x401500); ok {
		t.Error("found an entry for an address never encoded")
	}
	if _, ok := LookupStackMap(table, 0); ok {
		t.Error("found an entry for address 0, never encoded")
	}
}

func TestStackMapDecodesEveryEntryInOrder(t *testing.T) {
	entries := []StackMapEntry{
		{ReturnAddr: 0x401000, RefOffset: 8, RefCount: 1, RegMask: 0},
		{ReturnAddr: 0x401200, RefOffset: 16, RefCount: 3, RegMask: 0b0101},
	}
	table := EncodeStackMap(entries)
	got := DecodeStackMap(table)
	if len(got) != len(entries) {
		t.Fatalf("decoded %d entries, want %d", len(got), len(entries))
	}
	for i := range entries {
		if got[i] != entries[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], entries[i])
		}
	}
}

func TestStackMapLookupOnAnEmptyTable(t *testing.T) {
	if _, ok := LookupStackMap(nil, 0x401000); ok {
		t.Error("found an entry in an empty table")
	}
}
