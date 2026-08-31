// Package codesign builds the ad-hoc code signature a macOS executable must carry to run
// at all (ADR-0024).
//
// Since macOS 11 the kernel SIGKILLs any executable with no valid signature. Nothing
// about that is Gatekeeper or the quarantine bit: it is unconditional, and the reason it
// usually goes unnoticed is that Apple's linker ad-hoc signs its own output. Origin has
// no linker (ADR-0017), so it signs its own executables here.
//
// "Ad hoc" means the signature asserts the binary's identity by content hash alone: no
// certificate, no key, no CMS payload, and so nothing in this package beyond SHA-256 and
// exact byte layout. It is what `codesign --sign -` produces, and it is exactly enough
// for the kernel to run the program.
//
// This package produces bytes; internal/obj decides where in the file they sit and how
// __LINKEDIT and LC_CODE_SIGNATURE describe them (process rule 5, the same split as
// internal/dwarf).
package codesign

import (
	"crypto/sha256"
	"encoding/binary"
)

// PageSize is the granularity the signature hashes code at. Apple fixes it at 4 KiB for
// every architecture this project targets, independent of the kernel's own page size.
const PageSize = 4096

// Blob magic numbers (§ "Code Signing" in Apple's Security framework; the same constants
// `cs_blobs.h` names). Every field in every blob here is big-endian, unlike the rest of
// the Mach-O file around it.
const (
	magicEmbeddedSignature = 0xfade0cc0 // the superblob wrapping everything below
	magicCodeDirectory     = 0xfade0c02
	magicRequirements      = 0xfade0c01
	magicBlobWrapper       = 0xfade0b01 // holds the CMS signature; empty for an ad-hoc one
)

// Slot numbers within the superblob's index.
const (
	slotCodeDirectory = 0
	slotRequirements  = 2
	slotCMSSignature  = 0x10000
)

// The CodeDirectory's own shape.
const (
	// cdVersion 0x20400 is the first that carries execSegBase/Limit/Flags, which is how a
	// signature says which part of the file is the executable segment. Apple's own tooling
	// emits it, and the kernel reads those fields.
	cdVersion = 0x20400
	// cdFlagAdhoc marks a signature that claims no signing authority.
	cdFlagAdhoc = 0x00000002
	// cdHashTypeSHA256, and its digest length. ADR-0024 emits only this: the SHA-1
	// directory Apple also emits exists for macOS versions predating SHA-256 support.
	cdHashTypeSHA256 = 2
	cdHashSize       = sha256.Size

	// execSegMainBinary marks the executable segment of a main program, as opposed to a
	// library loaded into someone else's process.
	execSegMainBinary = 0x1

	// cdHeaderSize is the fixed part of a version-0x20400 CodeDirectory, before the
	// identifier string and the hash slots that follow it.
	cdHeaderSize = 88

	// nSpecialSlots is how many negative-indexed slots precede the code hashes. Two exist
	// here: slot 1 (an Info.plist, which a bare executable has none of, so its hash is
	// zero) and slot 2 (the requirement set below).
	nSpecialSlots = 2
)

// requirementsBlob is an empty requirement set: magic, length, and a count of zero. A
// real ad-hoc signature carries one, and the CodeDirectory hashes it into special slot 2.
var requirementsBlob = []byte{
	0xfa, 0xde, 0x0c, 0x01, // magic
	0x00, 0x00, 0x00, 0x0c, // length: 12, the whole blob
	0x00, 0x00, 0x00, 0x00, // count: no requirements
}

// cmsBlob is an empty CMS wrapper: an ad-hoc signature has no cryptographic signature to
// put in it, but the slot is present because that is the shape every consumer expects.
var cmsBlob = []byte{
	0xfa, 0xde, 0x0b, 0x01, // magic
	0x00, 0x00, 0x00, 0x08, // length: 8, a header and nothing else
}

// Size is how many bytes Build will produce for a signature covering codeLimit bytes of
// file under the given identifier.
//
// It is exact, and it is computable before the file it signs exists, which is what keeps
// the layout non-circular: __LINKEDIT's own size and LC_CODE_SIGNATURE both name the
// signature's size and both sit inside the region the signature hashes, so the size has
// to be known before a single byte is written (ADR-0024, and the same rule
// internal/obj.Plan follows for addresses).
func Size(identifier string, codeLimit uint64) uint64 {
	return uint64(superBlobSize(identifier, codeSlots(codeLimit)))
}

// Build returns the signature blob for a file whose bytes up to codeLimit are given.
//
// file must be the finished executable up to (not including) the point the signature
// itself will be written at, with every header field — including the ones naming this
// signature's own offset and size — already final. identifier names the code the
// signature covers; execSegBase and execSegLimit bound the executable segment (__TEXT).
func Build(file []byte, identifier string, codeLimit, execSegBase, execSegLimit uint64) []byte {
	cd := buildCodeDirectory(file, identifier, codeLimit, execSegBase, execSegLimit)

	blobs := []struct {
		slot uint32
		data []byte
	}{
		{slotCodeDirectory, cd},
		{slotRequirements, requirementsBlob},
		{slotCMSSignature, cmsBlob},
	}

	// The superblob: a header, an index naming each blob's slot and offset, then the
	// blobs themselves in the same order.
	indexSize := 12 + 8*len(blobs)
	total := indexSize
	for _, b := range blobs {
		total += len(b.data)
	}

	out := make([]byte, 0, total)
	out = binary.BigEndian.AppendUint32(out, magicEmbeddedSignature)
	out = binary.BigEndian.AppendUint32(out, uint32(total))
	out = binary.BigEndian.AppendUint32(out, uint32(len(blobs)))

	off := uint32(indexSize)
	for _, b := range blobs {
		out = binary.BigEndian.AppendUint32(out, b.slot)
		out = binary.BigEndian.AppendUint32(out, off)
		off += uint32(len(b.data))
	}
	for _, b := range blobs {
		out = append(out, b.data...)
	}
	return out
}

// buildCodeDirectory encodes the CodeDirectory itself: the fixed header, the identifier,
// the special slot hashes, and one SHA-256 per 4 KiB page of the signed range.
func buildCodeDirectory(file []byte, identifier string, codeLimit, execSegBase, execSegLimit uint64) []byte {
	nCode := codeSlots(codeLimit)

	identOffset := uint32(cdHeaderSize)
	// The special slots sit between the identifier and the code hashes, in reverse index
	// order (slot 2 first, then slot 1), so that hashOffset can point at code slot zero
	// and a special slot is read at a negative offset from it.
	hashOffset := identOffset + uint32(len(identifier)) + 1 + nSpecialSlots*cdHashSize

	out := make([]byte, 0, superBlobSize(identifier, nCode))
	out = binary.BigEndian.AppendUint32(out, magicCodeDirectory)
	out = binary.BigEndian.AppendUint32(out, uint32(cdBlobSize(identifier, nCode)))
	out = binary.BigEndian.AppendUint32(out, cdVersion)
	out = binary.BigEndian.AppendUint32(out, cdFlagAdhoc)
	out = binary.BigEndian.AppendUint32(out, hashOffset)
	out = binary.BigEndian.AppendUint32(out, identOffset)
	out = binary.BigEndian.AppendUint32(out, nSpecialSlots)
	out = binary.BigEndian.AppendUint32(out, nCode)
	out = binary.BigEndian.AppendUint32(out, uint32(codeLimit))
	out = append(out, cdHashSize, cdHashTypeSHA256, 0 /* platform: not a platform binary */, 12 /* log2(PageSize) */)
	out = binary.BigEndian.AppendUint32(out, 0) // spare2
	out = binary.BigEndian.AppendUint32(out, 0) // scatterOffset: no scatter vector
	out = binary.BigEndian.AppendUint32(out, 0) // teamOffset: no team identifier
	out = binary.BigEndian.AppendUint32(out, 0) // spare3
	out = binary.BigEndian.AppendUint64(out, 0) // codeLimit64: only for files past 4 GiB
	out = binary.BigEndian.AppendUint64(out, execSegBase)
	out = binary.BigEndian.AppendUint64(out, execSegLimit)
	out = binary.BigEndian.AppendUint64(out, execSegMainBinary)

	out = append(out, identifier...)
	out = append(out, 0)

	// Special slot 2: the requirement set. Slot 1 (an Info.plist) is all zeros, since a
	// bare executable has none. Reverse order, so slot 2 is written first.
	reqHash := sha256.Sum256(requirementsBlob)
	out = append(out, reqHash[:]...)
	out = append(out, make([]byte, cdHashSize)...)

	// One hash per page of the signed range, the last one short if codeLimit is not a
	// multiple of the page size.
	for i := uint32(0); i < nCode; i++ {
		start := uint64(i) * PageSize
		end := start + PageSize
		if end > codeLimit {
			end = codeLimit
		}
		h := sha256.Sum256(file[start:end])
		out = append(out, h[:]...)
	}
	return out
}

// codeSlots is how many page hashes cover codeLimit bytes.
func codeSlots(codeLimit uint64) uint32 {
	return uint32((codeLimit + PageSize - 1) / PageSize)
}

func cdBlobSize(identifier string, nCode uint32) int {
	return cdHeaderSize + len(identifier) + 1 +
		(nSpecialSlots+int(nCode))*cdHashSize
}

func superBlobSize(identifier string, nCode uint32) int {
	const blobCount = 3
	return 12 + 8*blobCount +
		cdBlobSize(identifier, nCode) +
		len(requirementsBlob) + len(cmsBlob)
}
