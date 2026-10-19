// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).

// Package pool is the shielded pool of Project Money, stage one (docs/money/
// ADR-0001-PRIVATE-PQ-SAFE-MONEY-2026-10-18.md, section 4.3 items 2,
// 3, 4 and 7): the node-side state of the pool (an append-only Merkle tree
// of note commitments of depth 32 and the nullifier set, kept in the chain
// state under the pool address so every node computes one root), the two
// transaction forms it admits (a shield and a spend, each carrying a proof
// of its statement), the turnstile, and the Verifier interface the proofs
// are checked through. Nothing here is in force on a chain whose gate
// (params.IgneumChainPoolFrom) is zero.
package pool

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

// Depth is the commitment tree's depth (the ADR's section 4.3 item 2):
// 2^32 notes.
const Depth = 32

// Hasher is the hash the tree, the commitments and the nullifiers are
// computed with: H, the ADR's in-circuit hash. Cut four fixes it as
// Poseidon2 over Goldilocks, the prover's instance (poseidon2.go); the
// interface stays so the hash can be replaced at a gated height by a new
// tree (section 4.7), one value, not a rewrite.
type Hasher interface {
	// Name is the hash's name on the record.
	Name() string
	// Node is a tree node: H(left, right).
	Node(left, right [32]byte) [32]byte
	// Commitment is a note's commitment cm = H(H(v, pk), r), v as four
	// 32-bit limbs.
	Commitment(value *big.Int, pk [32]byte, r [32]byte) [32]byte
	// Nullifier is a note's nullifier nf = H(H(nk, cm), [position, 0, 0, 0]).
	Nullifier(nk [32]byte, cm [32]byte, position uint64) [32]byte
}

// Poseidon2 is the Hasher of record: Miden VM 0.36.1's Poseidon2 over
// Goldilocks (poseidon2.go), every use the two-to-one merge. A 32-byte
// value is a word (four canonical field elements, little-endian); the
// admissions refuse a commitment, a nullifier or an anchor that is not one
// (IsWord), so the reduction in wordOf is never reached by consensus.
type Poseidon2 struct{}

// Name implements Hasher.
func (Poseidon2) Name() string { return "poseidon2-goldilocks-v1" }

func wordOf(b [32]byte) Word {
	w, ok := WordOf(b)
	if ok {
		return w
	}
	for i := 0; i < 4; i++ {
		var v uint64
		for j := 7; j >= 0; j-- {
			v = v<<8 | uint64(b[8*i+j])
		}
		w[i] = v % goldilocks
	}
	return w
}

// IsWord says whether 32 bytes read as a word (every element canonical).
func IsWord(b [32]byte) bool {
	_, ok := WordOf(b)
	return ok
}

// Node implements Hasher.
func (Poseidon2) Node(left, right [32]byte) [32]byte {
	return WordBytes(Merge(wordOf(left), wordOf(right)))
}

// Commitment implements Hasher.
func (Poseidon2) Commitment(value *big.Int, pk [32]byte, r [32]byte) [32]byte {
	vw, _ := ValueWord(value)
	return WordBytes(Merge(Merge(vw, wordOf(pk)), wordOf(r)))
}

// Nullifier implements Hasher.
func (Poseidon2) Nullifier(nk [32]byte, cm [32]byte, position uint64) [32]byte {
	return WordBytes(Merge(Merge(wordOf(nk), wordOf(cm)), Word{position, 0, 0, 0}))
}

// ValueWord is a value as four 32-bit limbs, limb 0 the least significant
// (the value rule of cut four: 128 bits of wei in one word, each limb
// range-checked in the circuit). ok is false for a negative value or one
// past 128 bits; nil reads as zero.
func ValueWord(v *big.Int) (w Word, ok bool) {
	if v == nil {
		return Word{}, true
	}
	if v.Sign() < 0 || v.BitLen() > 128 {
		return Word{}, false
	}
	var b [16]byte
	v.FillBytes(b[:])
	for i := 0; i < 4; i++ {
		// limb i is bytes 16-4(i+1) .. 16-4i big-endian
		w[i] = uint64(b[15-4*i]) | uint64(b[14-4*i])<<8 | uint64(b[13-4*i])<<16 | uint64(b[12-4*i])<<24
	}
	return w, true
}

// AddressWord is a 20-byte address as four 40-bit limbs, little-endian,
// in order.
func AddressWord(a common.Address) (w Word) {
	for i := 0; i < 4; i++ {
		var v uint64
		for j := 4; j >= 0; j-- {
			v = v<<8 | uint64(a[5*i+j])
		}
		w[i] = v
	}
	return w
}

// DomainWord is a statement's domain: the chain id and the kind
// (ShieldTag or SpendTag).
func DomainWord(chainID uint64, kind byte) Word {
	return Word{chainID, uint64(kind), 0, 0}
}

// Chain is H folded over words: H(...H(H(w0, w1), w2)..., wn).
func Chain(words ...Word) Word {
	acc := words[0]
	for _, w := range words[1:] {
		acc = Merge(acc, w)
	}
	return acc
}

// Zeros are the empty subtree hashes by level: Zeros[0] the empty leaf
// (32 zero bytes), Zeros[i+1] = Node(Zeros[i], Zeros[i]); Zeros[Depth] the
// empty tree's root.
func Zeros(h Hasher) [Depth + 1][32]byte {
	var z [Depth + 1][32]byte
	for i := 0; i < Depth; i++ {
		z[i+1] = h.Node(z[i], z[i])
	}
	return z
}
