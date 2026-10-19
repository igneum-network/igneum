package types

import (
	"github.com/ethereum/go-ethereum/common"
	"lukechampine.com/blake3"
)

// PiHash is the keyed seal's proof commitment as the header carries it
// (SPEC-R0A-v0.md 0.9.1): BLAKE3(tag("IgneumHash/R0A/pi") || pi) with tag(T)
// = u8(len(T)) || T, over the wire form of the proof carried in the body.
func PiHash(pi []byte) common.Hash {
	const t = "IgneumHash/R0A/pi"
	h := blake3.New(32, nil)
	h.Write([]byte{byte(len(t))})
	h.Write([]byte(t))
	h.Write(pi)
	var out common.Hash
	h.Sum(out[:0])
	return out
}
