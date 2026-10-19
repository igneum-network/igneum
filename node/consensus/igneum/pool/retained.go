// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).

package pool

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/rlp"
)

// The retained set (the proof-pruning rule of docs/money/STAGE1-JOIN-2026-
// 10-18.md section 8, built in the pruned-sync cut, docs/money/PRUNED-
// SYNC-DESIGN-2026-10-18.md): a record's proof is verified once at import
// and dropped N blocks later; what a node keeps of an admitted record is
// its retained encoding (the record's bytes up to the proof: the
// commitments, the ciphertexts, the nullifiers, the anchor, the values,
// the public output), the keccak of the dropped proof and its length.
// The retained records of a block are stored by the block's number and
// hash (RetainedBlock) and served to wallets (igneum_poolBlock) and to
// syncing nodes (igneum_poolRetained), which check each against the pool
// state the chain's headers commit to (Check).

// Retained is one admitted record without its proof.
type Retained struct {
	TxHash    common.Hash // the carrying transaction's hash (the original, with the proof)
	Kind      byte        // ShieldTag, SpendTag or a vault statement's tag (vault.go)
	Position  uint64      // the index of the record's first commitment in the tree
	Record    []byte      // the retained encoding: the record's bytes before the proof
	ProofHash common.Hash // keccak of the dropped proof
	ProofSize uint32      // the dropped proof's length
}

// RetainedBlock is a block's admitted records in order, with the
// position of the block's first output (the count before the block) and
// the count after it.
type RetainedBlock struct {
	Number   uint64
	Hash     common.Hash
	Position uint64 // the count before the block
	End      uint64 // the count after the block (Position when nothing was admitted)
	Records  []Retained
}

// SplitProof separates a shield or spend record into its retained bytes
// and its proof (false: not a readable record).
func SplitProof(data []byte) (retained, proof []byte, kind byte, ok bool) {
	if len(data) == 0 {
		return nil, nil, 0, false
	}
	switch data[0] {
	case ShieldTag:
		if _, ok := ParseShield(data); !ok {
			return nil, nil, 0, false
		}
		l := int(binary.BigEndian.Uint16(data[33:35]))
		n := 35 + l
		return data[:n], data[n:], ShieldTag, true
	case SpendTag:
		s, ok := ParseSpend(data)
		if !ok {
			return nil, nil, 0, false
		}
		n := len(data) - len(s.Proof)
		return data[:n], data[n:], SpendTag, true
	default:
		// A vault statement (stage two, cut one; vault.go): its proof is
		// the tail after the outputs, like a spend's.
		if !IsVaultTag(data[0]) {
			return nil, nil, 0, false
		}
		v, ok := ParseVault(data)
		if !ok {
			return nil, nil, 0, false
		}
		n := len(data) - len(v.Proof)
		return data[:n], data[n:], data[0], true
	}
}

// Prune is the stored form of a record's data after the proof is dropped:
// the retained bytes followed by the keccak of the proof (32 bytes), so
// the block body still parses as a record (the 32-byte tail reads as a
// proof that verifies nothing) and anyone holding the proof can check it
// against the tail.
func Prune(data []byte) ([]byte, bool) {
	retained, proof, _, ok := SplitProof(data)
	if !ok {
		return nil, false
	}
	out := make([]byte, 0, len(retained)+32)
	out = append(out, retained...)
	return append(out, crypto.Keccak256(proof)...), true
}

// Outputs are a retained record's commitments with their ciphertexts, in
// tree order, and a spend's nullifiers.
func (r Retained) Outputs() (outs []Output, nullifiers [][32]byte, ok bool) {
	switch r.Kind {
	case ShieldTag:
		sh, ok := ParseShield(r.Record)
		if !ok {
			return nil, nil, false
		}
		return []Output{{Commitment: sh.Commitment, Ciphertext: sh.Ciphertext}}, nil, true
	case SpendTag:
		sp, ok := ParseSpend(r.Record)
		if !ok {
			return nil, nil, false
		}
		return sp.Outputs, sp.Nullifiers, true
	default:
		// A vault statement: its outputs in order; its nullifiers and, for
		// a cancellation or a completion, the position tag among them (the
		// tag sits in the one set beside the nullifiers; a wallet marks the
		// pending note by it).
		if !IsVaultTag(r.Kind) {
			return nil, nil, false
		}
		vr, ok := ParseVault(r.Record)
		if !ok {
			return nil, nil, false
		}
		nfs := append([][32]byte(nil), vr.Nullifiers...)
		if vr.Tag == VaultCancelTag || vr.Tag == VaultCompleteTag {
			nfs = append(nfs, vr.Ptag)
		}
		return vr.Outputs, nfs, true
	}
}

// The store: one entry a block under the retained prefix.
var retainedPrefix = []byte("money-retained-")

func retainedKey(number uint64, hash common.Hash) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], number)
	return append(append(append([]byte(nil), retainedPrefix...), b[:]...), hash[:]...)
}

// WriteRetainedBlock stores a block's retained records.
func WriteRetainedBlock(db ethdb.KeyValueWriter, rb *RetainedBlock) error {
	enc, err := rlp.EncodeToBytes(rb)
	if err != nil {
		return err
	}
	return db.Put(retainedKey(rb.Number, rb.Hash), enc)
}

// ReadRetainedBlock reads a block's retained records (nil: none stored).
func ReadRetainedBlock(db ethdb.KeyValueReader, number uint64, hash common.Hash) *RetainedBlock {
	enc, _ := db.Get(retainedKey(number, hash))
	if len(enc) == 0 {
		return nil
	}
	var rb RetainedBlock
	if err := rlp.DecodeBytes(enc, &rb); err != nil {
		return nil
	}
	return &rb
}

// HasRetainedBlock says whether a block's retained records are stored.
func HasRetainedBlock(db ethdb.KeyValueReader, number uint64, hash common.Hash) bool {
	ok, _ := db.Has(retainedKey(number, hash))
	return ok
}

// Check verifies a block's retained set, as a peer served it, against the
// pool state a header the node holds commits to (the state at or after
// the block): the records' positions are contiguous from the block's
// first, every commitment is the leaf at its position, every ciphertext
// hashes to the state's hash at that position, every nullifier is in the
// set, and the block's end count is the count-at the state recorded for
// the block's height (zero when the block appended nothing, in which case
// the block carries no record). A record that fails any of these, or a
// set that stops short or runs past the recorded count, is refused with
// the words; nothing of a refused block is to be stored.
func Check(p *State, rb *RetainedBlock, expectStart uint64) error {
	if rb == nil {
		return errors.New("no retained block")
	}
	if rb.Position != expectStart {
		return fmt.Errorf("block %d starts at position %d, the chain's count before it is %d", rb.Number, rb.Position, expectStart)
	}
	pos := rb.Position
	for i, r := range rb.Records {
		if r.Position != pos {
			return fmt.Errorf("block %d record %d is at position %d, expected %d", rb.Number, i, r.Position, pos)
		}
		outs, nfs, ok := r.Outputs()
		if !ok {
			return fmt.Errorf("block %d record %d does not parse", rb.Number, i)
		}
		if len(r.Record) == 0 || r.Record[0] != r.Kind {
			return fmt.Errorf("block %d record %d names a kind its bytes do not carry", rb.Number, i)
		}
		for j, o := range outs {
			if pos >= p.Count() {
				return fmt.Errorf("block %d record %d output %d is past the pool's count %d", rb.Number, i, j, p.Count())
			}
			if p.Leaf(pos) != o.Commitment {
				return fmt.Errorf("block %d record %d output %d: the commitment is not the leaf at position %d", rb.Number, i, j, pos)
			}
			if p.CiphertextHash(pos) != [32]byte(crypto.Keccak256Hash(o.Ciphertext)) {
				return fmt.Errorf("block %d record %d output %d: the ciphertext does not hash to the state's at position %d", rb.Number, i, j, pos)
			}
			pos++
		}
		for j, nf := range nfs {
			if !p.NullifierSeen(nf) {
				return fmt.Errorf("block %d record %d nullifier %d is not in the set", rb.Number, i, j)
			}
		}
	}
	// The block's coinbase note, if the chain minted one at this height
	// (coinbase.go MintAt: in the state, not in the served set), sits after
	// the records; the end count includes it (F-791).
	minted := false
	if index, ok := p.MintAt(rb.Number); ok {
		if index != pos {
			return fmt.Errorf("block %d's coinbase note is at position %d, its records end at %d", rb.Number, index, pos)
		}
		pos++
		minted = true
	}
	if rb.End != pos {
		return fmt.Errorf("block %d ends at %d by its records, %d by its word", rb.Number, pos, rb.End)
	}
	countAt := p.CountAt(rb.Number)
	if len(rb.Records) > 0 || minted {
		if countAt != pos {
			return fmt.Errorf("block %d ends at %d by its records, the chain recorded %d", rb.Number, pos, countAt)
		}
	} else if countAt != 0 && countAt != rb.Position {
		return fmt.Errorf("block %d carries no record, the chain recorded a count of %d after it", rb.Number, countAt)
	}
	return nil
}

// Equal says whether two retained blocks are the same bytes.
func (rb *RetainedBlock) Equal(o *RetainedBlock) bool {
	a, _ := rlp.EncodeToBytes(rb)
	b, _ := rlp.EncodeToBytes(o)
	return bytes.Equal(a, b)
}
