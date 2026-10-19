//go:build !ignpi

package pi

// The node without the tag ignpi: no verifier is linked. The constants and the
// codes (codes.go) still name the rule; Verify answers ENotBuilt, so a node
// that is asked to verify pi (--igneum.pi-verify) refuses to start rather than
// accept a proof it cannot check (consensus/igneum/pi.go).

// Built reports whether the verifier is linked into this binary.
const Built = false

// ABIVersion is 0 without the library.
func ABIVersion() uint32 { return 0 }

// Verify answers ENotBuilt: nothing is verified in this build.
func Verify(domain uint8, pk [32]byte, m [][32]byte, s [][32]byte, pi []byte) int32 { return ENotBuilt }

// LibraryImageIDHex is empty without the library.
func LibraryImageIDHex() string { return "" }
