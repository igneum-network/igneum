package types

import (
	"github.com/ethereum/go-ethereum/common"
	"lukechampine.com/blake3"
)

// The settlement rule's endorsements (DEVNET6-RULES.md section 2; GUARD-C-
// CLOSURE.md 4.6 candidate 2): a block carries the endorsements of its
// PARENT as a list of fixed-size entries, key (32) || LE32(unit) || Ed25519
// signature (64), committed by the header's EndorsementsHash and counted in
// the header's Endorsed, both inside the seal hash.

// EndorsementBytes is one entry's size.
const EndorsementBytes = 32 + 4 + 64

// EndorsementsHash is the commitment to a block's carried list:
// BLAKE3(tag("IgneumHash/R0A/endorsements") || entries), tag(T) =
// u8(len(T)) || T.
func EndorsementsHash(entries []byte) common.Hash {
	const t = "IgneumHash/R0A/endorsements"
	h := blake3.New(32, nil)
	h.Write([]byte{byte(len(t))})
	h.Write([]byte(t))
	h.Write(entries)
	var out common.Hash
	h.Sum(out[:0])
	return out
}

// EndorsementCount is the number of whole entries in a list (a list whose
// length is not a multiple of the entry size is malformed: -1).
func EndorsementCount(entries []byte) int {
	if len(entries)%EndorsementBytes != 0 {
		return -1
	}
	return len(entries) / EndorsementBytes
}
