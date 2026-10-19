//go:build ignpi

package pi

// The binding of pow/pi/ffi (the header is the contract; the staticlib is built with
// `cd pow/pi && cargo build --release -p igneum-pi-ffi`, no guest toolchain or docker needed: the verifier carries the
// image id as a pinned constant). The link line matches the rxe1 and controls packages' form (a repo-relative path);
// -lstdc++ because a staticlib built beside the workspace's prover (the `prove` feature unified in) carries risc0's
// C++ circuit code, and the verify-only one links with or without it.

/*
#cgo CFLAGS: -I${SRCDIR}/../../../../../pow/pi/ffi/include
#cgo LDFLAGS: -L${SRCDIR}/../../../../../pow/pi/target/release -ligneum_pi_ffi -lstdc++ -lm -ldl -lpthread
#include "ignpi.h"
*/
import "C"

import (
	"encoding/hex"
	"unsafe"
)

// Built reports whether the verifier is linked into this binary (true under the tag ignpi).
const Built = true

// ABIVersion is the library's ABI version (the header's IGNPI_ABI_VERSION must equal it).
func ABIVersion() uint32 { return uint32(C.ignpi_abi_version()) }

// ImageID is PI_IMAGE_ID as the library carries it (32 bytes).
func ImageID() [32]byte {
	var out [32]byte
	C.ignpi_image_id((*C.uint8_t)(unsafe.Pointer(&out[0])))
	return out
}

// Verify checks pi (the wire form u8(2) || receipt) under domain (0 the seal, 1 the claim) against pk, the m_i in order
// and the carried shares, returning OK or the first failing check's code (EArg to EJournal; EPanic for a fault inside
// the verifier). A length mismatch (len(m) != len(s), len(m) 0 or over KMax) is EArg before any call. Pure; callable
// from any goroutine; a block's valid proof verifies in about 15 ms on an EPYC core (PI-NOTES.md section 2).
func Verify(domain uint8, pk [32]byte, m [][32]byte, s [][32]byte, pi []byte) int32 {
	k := len(m)
	if domain > 1 || k == 0 || k > KMax || len(s) != k {
		return EArg
	}
	mb := make([]byte, 32*k)
	sb := make([]byte, 32*k)
	for i := range m {
		copy(mb[32*i:], m[i][:])
		copy(sb[32*i:], s[i][:])
	}
	var pp *C.uint8_t
	if len(pi) > 0 {
		pp = (*C.uint8_t)(unsafe.Pointer(&pi[0]))
	}
	return int32(C.ignpi_verify(C.uint8_t(domain), C.uint32_t(k), (*C.uint8_t)(unsafe.Pointer(&pk[0])), (*C.uint8_t)(unsafe.Pointer(&mb[0])), (*C.uint8_t)(unsafe.Pointer(&sb[0])), pp, C.size_t(len(pi))))
}

// LibraryImageIDHex is the library's PI_IMAGE_ID as hex, for the start-up check against ImageIDHex.
func LibraryImageIDHex() string {
	id := ImageID()
	return hex.EncodeToString(id[:])
}
