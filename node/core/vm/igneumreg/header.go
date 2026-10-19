// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package igneumreg

import (
	"encoding/binary"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rlp"
	"golang.org/x/crypto/sha3"
	"lukechampine.com/blake3"
)

// The messages the penalty operations check signatures and proofs over.
// The two header hashes are the engine's (consensus/igneum/consensus.go
// SealHash and sortition.go SigningHash) written here a second time
// because the registry sits below the engine in the import graph; the
// engine's own test asserts the two agree on every header form
// (consensus/igneum/penalties_test.go), so a field added to the seal hash
// without its twin here is a red test, never a silent divergence.

// sealFields is the seal hash's field list before the sortition fields:
// every header field but nonce, mix digest and shares, with the bond key
// and the payout list root after the base fee when set.
func sealFields(header *types.Header) []interface{} {
	enc := []interface{}{
		header.ParentHash,
		header.UncleHash,
		header.Coinbase,
		header.Root,
		header.TxHash,
		header.ReceiptHash,
		header.Bloom,
		header.Difficulty,
		header.Number,
		header.GasLimit,
		header.GasUsed,
		header.Time,
		header.Extra,
	}
	if header.BaseFee != nil {
		enc = append(enc, header.BaseFee)
	}
	if header.BondKey != nil {
		enc = append(enc, *header.BondKey)
	}
	if header.EndorsementsHash != nil {
		// The settlement rule's fields (consensus/igneum/endorse.go), as the
		// engine's sealFields order them: after BondKey, before the list root.
		enc = append(enc, *header.EndorsementsHash, header.Endorsed)
	}
	if header.SharesRoot != nil {
		// The share lane's commitment (consensus/igneum/mineshares.go), as
		// the engine orders it: after the endorsement fields, before the
		// list root.
		enc = append(enc, *header.SharesRoot)
	}
	if header.WithdrawalsHash != nil {
		enc = append(enc, *header.WithdrawalsHash)
	}
	return enc
}

// SigningHash is KECCAK(RLP(seal fields, Round)): what the bond's signer
// signs.
func SigningHash(header *types.Header) (hash common.Hash) {
	hasher := sha3.NewLegacyKeccak256()
	enc := append(sealFields(header), header.Round)
	rlp.Encode(hasher, enc)
	hasher.Sum(hash[:0])
	return hash
}

// SealHash is KECCAK(RLP(seal fields, Round, Sig)) for a signed header
// (the seal fields alone for an unsigned one): the hash the proof of work
// seals, so two headers with one signing hash and two signatures are two
// seals.
func SealHash(header *types.Header) (hash common.Hash) {
	hasher := sha3.NewLegacyKeccak256()
	enc := sealFields(header)
	if len(header.Sig) > 0 {
		enc = append(enc, header.Round, header.Sig)
	}
	rlp.Encode(hasher, enc)
	hasher.Sum(hash[:0])
	return hash
}

func tagBytes(t string) []byte { return append([]byte{byte(len(t))}, t...) }

var (
	endorseMsgDomain = tagBytes("IgneumHash/R0A/endorse-msg")
	hostileDomain    = tagBytes("IgneumHash/R0A/hostile")
)

// EndorseMessage is what a unit's signer signs to endorse a block: tag
// ("IgneumHash/R0A/endorse-msg") || u64be(chain id) || LE64(height) ||
// block hash (consensus/igneum/endorse.go's form, the protocol team's branch).
func EndorseMessage(chainID, height uint64, blockHash common.Hash) []byte {
	var b8 [8]byte
	m := make([]byte, 0, len(endorseMsgDomain)+16+32)
	m = append(m, endorseMsgDomain...)
	binary.BigEndian.PutUint64(b8[:], chainID)
	m = append(m, b8[:]...)
	binary.LittleEndian.PutUint64(b8[:], height)
	m = append(m, b8[:]...)
	m = append(m, blockHash[:]...)
	return m
}

// ClaimMessage is m_0 of the hostile claim and the holder's release
// (SPEC-R0A-v0.md 0.9.1; LAUNCH-GUARDS.md 2.7 rule 3): BLAKE3(tag
// ("IgneumHash/R0A/hostile") || u64be(chain id) || pk || LE64(nonce_b) ||
// to), to the 20-byte claimant address.
func ClaimMessage(chainID uint64, pk common.Hash, nonce uint64, to common.Address) [32]byte {
	var b8 [8]byte
	h := blake3.New(32, nil)
	h.Write(hostileDomain)
	binary.BigEndian.PutUint64(b8[:], chainID)
	h.Write(b8[:])
	h.Write(pk[:])
	binary.LittleEndian.PutUint64(b8[:], nonce)
	h.Write(b8[:])
	h.Write(to[:])
	var out [32]byte
	h.Sum(out[:0])
	return out
}
