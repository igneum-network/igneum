// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).

package pool

import (
	"encoding/binary"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

// The two transaction forms (the node's form for a native record: a
// transaction to a derived address with no code, the record in its data,
// admitted or refused by Finalize; the fund's and the sponsored lane's
// form). A SHIELD carries value from a public account into a new note
// with a proof of money-shield-v1 (the commitment's hidden value is the
// shield's public value): the EVM moves the value to the pool address as
// the block runs, and Finalize appends the commitment and raises the
// turnstile, or refuses and returns the value to the sender in the same
// block. A SPEND carries no value: a proof of money-spend-v1 against a
// recent root, exactly two nullifiers, exactly two new commitments with
// their ciphertexts (a dummy input or output pads a smaller transfer, the
// statement's rule), an optional public output and a fee; Finalize checks
// the root, the nullifiers, the proof (through the Verifier) and the
// turnstile, then marks the nullifiers, appends the commitments, pays the
// public output from the pool and burns the fee (the fee rule's part 1;
// the signerless type whose fee splits into the base fee and the tip is
// F-761's stage 1 item, open).
const (
	ShieldTag = 0x01
	SpendTag  = 0x02

	// MaxInputs and MaxOutputs are the statement's shape: two and two.
	MaxInputs  = 2
	MaxOutputs = 2
	// OutputCiphertextBytes is the join's output ciphertext (docs/money/
	// STAGE1-CUT3-WALLET-2026-10-18.md: 1,293 bytes, the ML-KEM-768 capsule
	// and the sealed 205-byte body) and ClueBytes the light-sync page's
	// lattice clue (ML-KEM-512, 768 bytes); MaxCiphertext bounds what one
	// output carries in its ciphertext field: the clue and the ciphertext
	// together, 2,061 bytes (F-774; it was 2,048, below the sum).
	OutputCiphertextBytes = 1293
	ClueBytes             = 768
	MaxCiphertext         = ClueBytes + OutputCiphertextBytes
	// MaxProof bounds the proof bytes a record carries (the ADR's estimate
	// 80 to 140 KB for the two-in two-out STARK).
	MaxProof = 256 * 1024
	// MaxValue bounds the public value and the fee of a spend and the
	// value of a shield: 2^128 - 1 wei, far above the supply.
	maxValueBits = 128
)

// Shield is a shield's record: the new note's commitment, its ciphertext
// for the recipient (opaque to the node, carried on the chain in the
// transaction's data) and the proof of money-shield-v1 over the
// commitment and the transaction's value.
type Shield struct {
	Commitment [32]byte
	Ciphertext []byte
	Proof      []byte
}

// Encode is the record's bytes: the tag, the commitment, a two-byte
// ciphertext length, the ciphertext, then the proof to the end.
func (s Shield) Encode() []byte {
	out := make([]byte, 0, 1+32+2+len(s.Ciphertext)+len(s.Proof))
	out = append(out, ShieldTag)
	out = append(out, s.Commitment[:]...)
	var l [2]byte
	binary.BigEndian.PutUint16(l[:], uint16(len(s.Ciphertext)))
	out = append(out, l[:]...)
	out = append(out, s.Ciphertext...)
	return append(out, s.Proof...)
}

// ParseShield reads a shield record (false: not one, or a length past
// the form's bounds).
func ParseShield(data []byte) (Shield, bool) {
	if len(data) < 1+32+2 || data[0] != ShieldTag {
		return Shield{}, false
	}
	var s Shield
	copy(s.Commitment[:], data[1:33])
	l := int(binary.BigEndian.Uint16(data[33:35]))
	if l > MaxCiphertext || len(data) < 35+l {
		return Shield{}, false
	}
	s.Ciphertext = append([]byte(nil), data[35:35+l]...)
	if len(data)-(35+l) > MaxProof {
		return Shield{}, false
	}
	s.Proof = append([]byte(nil), data[35+l:]...)
	return s, true
}

// Statement is the shield's public inputs on a chain: the commitment and
// the value the carrying transaction brings.
func (s Shield) Statement(chainID uint64, value *big.Int) Statement {
	return Statement{Kind: ShieldTag, ChainID: chainID, Commitments: [][32]byte{s.Commitment}, PublicValue: value, Fee: new(big.Int)}
}

// Output is a spend's new note: its commitment and its ciphertext.
type Output struct {
	Commitment [32]byte
	Ciphertext []byte
}

// Spend is a spend's record.
type Spend struct {
	Anchor       [32]byte   // the root the proof was made against
	Nullifiers   [][32]byte // the spent notes' nullifiers (exactly MaxInputs)
	Outputs      []Output   // the new notes (exactly MaxOutputs)
	PublicValue  *big.Int   // v_pub: the value leaving the pool to PublicOutput (zero for a private transfer)
	Fee          *big.Int   // the fee, paid from the pool's value
	PublicOutput common.Address
	Proof        []byte
}

// Encode is the record's bytes (big-endian throughout): the tag, the
// anchor, the nullifier count and the nullifiers, the output count and
// each output as commitment, a two-byte ciphertext length and the
// ciphertext, the public value and the fee as 32 bytes each, the public
// output address, then the proof to the end.
func (s Spend) Encode() []byte {
	out := []byte{SpendTag}
	out = append(out, s.Anchor[:]...)
	out = append(out, byte(len(s.Nullifiers)))
	for _, nf := range s.Nullifiers {
		out = append(out, nf[:]...)
	}
	out = append(out, byte(len(s.Outputs)))
	for _, o := range s.Outputs {
		out = append(out, o.Commitment[:]...)
		var l [2]byte
		binary.BigEndian.PutUint16(l[:], uint16(len(o.Ciphertext)))
		out = append(out, l[:]...)
		out = append(out, o.Ciphertext...)
	}
	var v, f [32]byte
	if s.PublicValue != nil && s.PublicValue.Sign() > 0 && s.PublicValue.BitLen() <= 256 {
		s.PublicValue.FillBytes(v[:])
	}
	if s.Fee != nil && s.Fee.Sign() > 0 && s.Fee.BitLen() <= 256 {
		s.Fee.FillBytes(f[:])
	}
	out = append(out, v[:]...)
	out = append(out, f[:]...)
	out = append(out, s.PublicOutput[:]...)
	return append(out, s.Proof...)
}

// ParseSpend reads a spend record by its layout alone (false: not one, or
// a count or length past the form's bounds); the rules are AdmitSpend's.
func ParseSpend(data []byte) (Spend, bool) {
	var s Spend
	if len(data) < 1+32+1 || data[0] != SpendTag {
		return s, false
	}
	copy(s.Anchor[:], data[1:33])
	p := 33
	n := int(data[p])
	p++
	if n > MaxInputs || len(data) < p+32*n+1 {
		return s, false
	}
	for i := 0; i < n; i++ {
		var nf [32]byte
		copy(nf[:], data[p:p+32])
		s.Nullifiers = append(s.Nullifiers, nf)
		p += 32
	}
	m := int(data[p])
	p++
	if m > MaxOutputs {
		return s, false
	}
	for i := 0; i < m; i++ {
		if len(data) < p+34 {
			return s, false
		}
		var o Output
		copy(o.Commitment[:], data[p:p+32])
		l := int(binary.BigEndian.Uint16(data[p+32 : p+34]))
		p += 34
		if l > MaxCiphertext || len(data) < p+l {
			return s, false
		}
		o.Ciphertext = append([]byte(nil), data[p:p+l]...)
		p += l
		s.Outputs = append(s.Outputs, o)
	}
	if len(data) < p+32+32+20 {
		return s, false
	}
	s.PublicValue = new(big.Int).SetBytes(data[p : p+32])
	s.Fee = new(big.Int).SetBytes(data[p+32 : p+64])
	s.PublicOutput = common.BytesToAddress(data[p+64 : p+84])
	p += 84
	if len(data)-p > MaxProof {
		return s, false
	}
	s.Proof = append([]byte(nil), data[p:]...)
	return s, true
}

// Statement is the spend's public inputs on a chain, the statement the
// proof is verified against (the ADR's section 4.3 item 4: the anchor,
// the nullifiers, the new commitments, the public value balance, the fee
// and the public output, bound by the statement hash so a proof cannot be
// moved to another record or another chain).
func (s Spend) Statement(chainID uint64) Statement {
	st := Statement{Kind: SpendTag, ChainID: chainID, Anchor: s.Anchor, Nullifiers: s.Nullifiers, PublicValue: s.PublicValue, Fee: s.Fee, PublicOutput: s.PublicOutput}
	for _, o := range s.Outputs {
		st.Commitments = append(st.Commitments, o.Commitment)
	}
	return st
}
