package codesign

import (
	"crypto/sha256"
	"encoding/binary"
	"testing"
)

// parsedCD is a CodeDirectory read back out of a built signature by a reader written
// independently of the builder: it walks the superblob index and the fixed header by
// offset, the way any consumer (the kernel included) does, rather than trusting the
// layout the builder intended.
type parsedCD struct {
	version, flags       uint32
	hashOffset, identOff uint32
	nSpecial, nCode      uint32
	codeLimit            uint32
	hashSize, hashType   uint8
	pageLog              uint8
	execBase, execLimit  uint64
	execFlags            uint64
	identifier           string
	blob                 []byte
}

func parse(t *testing.T, sig []byte) parsedCD {
	t.Helper()
	if got := binary.BigEndian.Uint32(sig); got != magicEmbeddedSignature {
		t.Fatalf("superblob magic is %#x, want %#x", got, magicEmbeddedSignature)
	}
	if got := binary.BigEndian.Uint32(sig[4:]); got != uint32(len(sig)) {
		t.Errorf("superblob says it is %d bytes, but it is %d", got, len(sig))
	}
	count := binary.BigEndian.Uint32(sig[8:])

	var cd parsedCD
	found := false
	for i := uint32(0); i < count; i++ {
		slot := binary.BigEndian.Uint32(sig[12+i*8:])
		off := binary.BigEndian.Uint32(sig[12+i*8+4:])
		if off >= uint32(len(sig)) {
			t.Fatalf("blob %d claims offset %d, past the %d-byte superblob", i, off, len(sig))
		}
		blob := sig[off:]
		if slot != slotCodeDirectory {
			continue
		}
		found = true
		cd.blob = blob
		cd.version = binary.BigEndian.Uint32(blob[8:])
		cd.flags = binary.BigEndian.Uint32(blob[12:])
		cd.hashOffset = binary.BigEndian.Uint32(blob[16:])
		cd.identOff = binary.BigEndian.Uint32(blob[20:])
		cd.nSpecial = binary.BigEndian.Uint32(blob[24:])
		cd.nCode = binary.BigEndian.Uint32(blob[28:])
		cd.codeLimit = binary.BigEndian.Uint32(blob[32:])
		cd.hashSize, cd.hashType, cd.pageLog = blob[36], blob[37], blob[39]
		cd.execBase = binary.BigEndian.Uint64(blob[64:])
		cd.execLimit = binary.BigEndian.Uint64(blob[72:])
		cd.execFlags = binary.BigEndian.Uint64(blob[80:])

		end := cd.identOff
		for blob[end] != 0 {
			end++
		}
		cd.identifier = string(blob[cd.identOff:end])
	}
	if !found {
		t.Fatal("the signature has no CodeDirectory")
	}
	return cd
}

// TestBuildProducesAVerifiableSignature is the property the kernel actually checks: every
// page hash in the CodeDirectory must match the bytes of the file it covers. It is
// re-derived here from the file, not compared against what the builder computed.
func TestBuildProducesAVerifiableSignature(t *testing.T) {
	// A file whose length is deliberately not a multiple of the page size, so the last
	// page is short -- the case an off-by-one in the page walk gets wrong.
	file := make([]byte, 3*PageSize+123)
	for i := range file {
		file[i] = byte(i * 7)
	}
	codeLimit := uint64(len(file))

	sig := Build(file, "hello", codeLimit, 0, 0x2000)
	cd := parse(t, sig)

	if cd.version != cdVersion {
		t.Errorf("version is %#x, want %#x", cd.version, cdVersion)
	}
	if cd.flags&cdFlagAdhoc == 0 {
		t.Errorf("flags are %#x, want the adhoc bit (%#x) set", cd.flags, cdFlagAdhoc)
	}
	if cd.hashType != cdHashTypeSHA256 || cd.hashSize != cdHashSize {
		t.Errorf("hash is type %d size %d, want SHA-256 (%d, %d)",
			cd.hashType, cd.hashSize, cdHashTypeSHA256, cdHashSize)
	}
	if 1<<cd.pageLog != PageSize {
		t.Errorf("page size is 2^%d, want %d", cd.pageLog, PageSize)
	}
	if cd.identifier != "hello" {
		t.Errorf("identifier is %q, want %q", cd.identifier, "hello")
	}
	if uint64(cd.codeLimit) != codeLimit {
		t.Errorf("codeLimit is %d, want %d", cd.codeLimit, codeLimit)
	}
	if cd.nCode != 4 {
		t.Errorf("nCode is %d, want 4 (three full pages and a short one)", cd.nCode)
	}
	if cd.execLimit != 0x2000 || cd.execFlags != execSegMainBinary {
		t.Errorf("exec segment is [%#x, +%#x) flags %#x, want limit %#x flags %#x",
			cd.execBase, cd.execLimit, cd.execFlags, 0x2000, execSegMainBinary)
	}

	// Every code slot, re-hashed from the file.
	for i := uint32(0); i < cd.nCode; i++ {
		start := uint64(i) * PageSize
		end := start + PageSize
		if end > codeLimit {
			end = codeLimit
		}
		want := sha256.Sum256(file[start:end])
		off := cd.hashOffset + i*uint32(cd.hashSize)
		if got := cd.blob[off : off+uint32(cd.hashSize)]; string(got) != string(want[:]) {
			t.Errorf("page %d's digest does not match the file's own bytes", i)
		}
	}

	// The special slots sit below hashOffset in reverse order: slot 1 (an Info.plist,
	// which a bare executable has none of, so zero) then slot 2 (the requirement set).
	if cd.nSpecial != nSpecialSlots {
		t.Fatalf("nSpecialSlots is %d, want %d", cd.nSpecial, nSpecialSlots)
	}
	infoSlot := cd.blob[cd.hashOffset-cdHashSize : cd.hashOffset]
	for _, b := range infoSlot {
		if b != 0 {
			t.Error("special slot 1 (Info.plist) is not zero, but there is no Info.plist")
			break
		}
	}
	reqWant := sha256.Sum256(requirementsBlob)
	reqGot := cd.blob[cd.hashOffset-2*cdHashSize : cd.hashOffset-cdHashSize]
	if string(reqGot) != string(reqWant[:]) {
		t.Error("special slot 2 does not hold the requirement set's own digest")
	}
}

// TestSizeAgreesWithBuild is what the whole layout depends on: internal/obj reserves room
// for the signature, and writes that reservation into a header the signature then hashes,
// before Build is ever called. If the two disagree by a byte the file is corrupt.
func TestSizeAgreesWithBuild(t *testing.T) {
	for _, tc := range []struct {
		ident string
		limit uint64
	}{
		{"origin", 4096},
		{"origin", 4097},          // just past a page boundary
		{"a", 1},                  // one short page
		{"hello", 3*PageSize + 1}, // several pages and a one-byte tail
		{"a-rather-longer-identifier-than-usual", 100000},
	} {
		file := make([]byte, tc.limit)
		got := uint64(len(Build(file, tc.ident, tc.limit, 0, 0x1000)))
		if want := Size(tc.ident, tc.limit); got != want {
			t.Errorf("identifier %q, codeLimit %d: Build produced %d bytes, Size said %d",
				tc.ident, tc.limit, got, want)
		}
	}
}

// TestBuildIsDeterministic: the same input signs to the same bytes, which
// spec/11-codegen.md's determinism clause requires of everything in the file.
func TestBuildIsDeterministic(t *testing.T) {
	file := make([]byte, 2*PageSize)
	a := Build(file, "origin", uint64(len(file)), 0, 0x1000)
	b := Build(file, "origin", uint64(len(file)), 0, 0x1000)
	if string(a) != string(b) {
		t.Error("signing the same file twice produced different bytes")
	}
}

// TestASingleChangedByteBreaksTheSignature: the signature has to actually be a function
// of the file's contents, which is the whole reason the kernel checks it.
func TestASingleChangedByteBreaksTheSignature(t *testing.T) {
	file := make([]byte, 2*PageSize)
	before := Build(file, "origin", uint64(len(file)), 0, 0x1000)

	file[PageSize+7] = 1
	after := Build(file, "origin", uint64(len(file)), 0, 0x1000)

	if string(before) == string(after) {
		t.Error("changing a byte of the file did not change its signature")
	}
	if len(before) != len(after) {
		t.Errorf("the signature's length changed (%d then %d) for a same-length file",
			len(before), len(after))
	}
}
