// Package pi is the node's verifier of the proof pi of SPEC-R0A-v0.md section 0.9.1 (the carried shares bound to the
// bond's public key), a cgo binding of pow/pi/ffi (libigneum_pi_ffi.a: risc0 3.0.6's verifier with PI_IMAGE_ID pinned,
// the per-domain receipt kind and bound, the journal rule). The binding itself (Verify, ImageID) builds under the tag
// ignpi, which needs the staticlib (docs/igneumhash/pi/PI-NOTES.md section 5 is the recipe); this file builds always,
// so the codes and the constants are referenceable without cgo (the protocol team's validity rule names them).
package pi

// The refusal codes of Verify, in the order the checks run (pow/pi/ffi/include/ignpi.h; one code set, no other).
const (
	OK        = 0 // the proof verifies and its journal is the block's
	EArg      = 1 // domain not 0 or 1, K 0 or over 64, or a length mismatch: a malformed call, never a proof's verdict
	EBound    = 2 // pi longer than the domain's bound, refused before parsing
	ESystem   = 3 // pi empty or its identifier byte not 2
	EDecode   = 4 // the receipt does not decode
	EKind     = 5 // the receipt's kind is not the domain's (composite of one segment for the seal, Groth16 for the claim)
	EProof    = 6 // the STARK or SNARK does not verify against PI_IMAGE_ID
	EJournal  = 7 // the journal differs: pk, an m_i or a share is not the proof's
	EPanic    = 8 // a panic inside the verifier, caught at the boundary (a fault, never a verdict on the proof)
	ENotBuilt = 9 // this node was built without the tag ignpi: no verifier is linked (the stand-in's domain)
)

// The constants of section 0.9.1 the node needs without the library.
const (
	System        = 2      // the proof system identifier byte of the wire form pi = u8(2) || receipt
	KMax          = 64     // KEYED_ROUNDS the guest admits
	MaxBytesBlock = 294912 // PI_MAX_BYTES, domain 0
	MaxBytesClaim = 2048   // PI_CLAIM_MAX_BYTES, domain 1
	DomainSeal    = 0
	DomainClaim   = 1
	// ImageIDHex is PI_IMAGE_ID (host-independent, docker mode, two build boxes agreeing, 11 Oct 2026); the library
	// carries the same bytes, and Verify's package test asserts they agree.
	ImageIDHex = "006bf405038926b456e1c6056f4564358e0fb1cab70f5d5d7f3257cb8a83787c"
)

// MaxBytes is the wire bound of a domain's pi (0 for a domain not 0 or 1).
func MaxBytes(domain uint8) int {
	switch domain {
	case DomainSeal:
		return MaxBytesBlock
	case DomainClaim:
		return MaxBytesClaim
	}
	return 0
}

// CodeName names a code for logs.
func CodeName(code int32) string {
	switch code {
	case OK:
		return "ok"
	case EArg:
		return "arg"
	case EBound:
		return "bound"
	case ESystem:
		return "system"
	case EDecode:
		return "decode"
	case EKind:
		return "kind"
	case EProof:
		return "proof"
	case EJournal:
		return "journal"
	case EPanic:
		return "panic"
	case ENotBuilt:
		return "not built"
	}
	return "unknown"
}
