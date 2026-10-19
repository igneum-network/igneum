// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).

package pool

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// Extract reads a block's admitted records from its transactions to the
// pool address against the block's state and its parent's (a shield is
// admitted when its commitment is the leaf at the next position; a spend
// when its first nullifier is in the set at this block and was not at the
// parent, and its first output is the leaf at the next position): the
// block's retained set, with each record's proof hashed and its length
// counted. The parent may be nil for the first block. The transactions are
// walked in the pool's order (Ordered, F-801), the order Finalize appended
// in, so the positions agree.
func Extract(now, parent *State, number uint64, hash common.Hash, txs []*types.Transaction) *RetainedBlock {
	rb := &RetainedBlock{Number: number, Hash: hash, Records: []Retained{}}
	if parent != nil {
		rb.Position = parent.Count()
	}
	pos := rb.Position
	addr := now.Address()
	for _, tx := range Ordered(txs, addr) {
		data := tx.Data()
		if sh, ok := ParseShield(data); ok {
			if pos < now.Count() && now.Leaf(pos) == sh.Commitment {
				retained, proof, _, _ := SplitProof(data)
				rb.Records = append(rb.Records, Retained{TxHash: tx.Hash(), Kind: ShieldTag, Position: pos, Record: append([]byte(nil), retained...), ProofHash: crypto.Keccak256Hash(proof), ProofSize: uint32(len(proof))})
				pos++
			}
		} else if sp, ok := ParseSpend(data); ok && len(sp.Nullifiers) > 0 && len(sp.Outputs) > 0 {
			nf := sp.Nullifiers[0]
			if now.NullifierSeen(nf) && (parent == nil || !parent.NullifierSeen(nf)) && pos+uint64(len(sp.Outputs)) <= now.Count() && now.Leaf(pos) == sp.Outputs[0].Commitment {
				retained, proof, _, _ := SplitProof(data)
				rb.Records = append(rb.Records, Retained{TxHash: tx.Hash(), Kind: SpendTag, Position: pos, Record: append([]byte(nil), retained...), ProofHash: crypto.Keccak256Hash(proof), ProofSize: uint32(len(proof))})
				pos += uint64(len(sp.Outputs))
			}
		} else if vr, ok := ParseVault(data); ok && len(vr.Nullifiers) > 0 && len(vr.Outputs) > 0 {
			// A vault statement (stage two, cut one; vault.go): admitted when
			// its first nullifier is in the set at this block and was not at
			// the parent, and its first output is the leaf at the next
			// position (its proof pruned like a spend's).
			nf := vr.Nullifiers[0]
			if now.NullifierSeen(nf) && (parent == nil || !parent.NullifierSeen(nf)) && pos+uint64(len(vr.Outputs)) <= now.Count() && now.Leaf(pos) == vr.Outputs[0].Commitment {
				retained, proof, _, _ := SplitProof(data)
				rb.Records = append(rb.Records, Retained{TxHash: tx.Hash(), Kind: vr.Tag, Position: pos, Record: append([]byte(nil), retained...), ProofHash: crypto.Keccak256Hash(proof), ProofSize: uint32(len(proof))})
				pos += uint64(len(vr.Outputs))
			}
		}
	}
	// The block's coinbase note (the coinbase cut, coinbase.go: minted after
	// the block's records, so its leaf is the next position) is in the state
	// by height, not among the transactions; the block's end count includes
	// it (F-791: a block that minted was refused by Check and the next
	// block's start read one short).
	if index, ok := now.MintAt(number); ok && index == pos {
		pos++
	}
	rb.End = pos
	return rb
}
