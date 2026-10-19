//go:build controls && cgo

package main

// the hash team's C reference behind the IgneumHash documents the Go WORK
// does not carry (the live lanes' versions 15, 18, 21 and 25): linked under
// the controls tag as cmd/geth links it, so the regcheck builds a change's
// form as the engine would (LANE-MODIFICATION.md section 1a's cut 3). A
// regcheck built without the tag refuses every such document as unbuildable,
// which is the honest answer of a tool that cannot compile it.
import _ "github.com/ethereum/go-ethereum/consensus/igneum/pow/controls"
