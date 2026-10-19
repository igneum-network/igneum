// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).

package pool

import (
	"crypto/mlkem"
	"encoding/binary"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
	"lukechampine.com/blake3"
)

// The shielded coinbase (Project Money stage two, cut two; docs/money/
// PAYOUT-PRIVACY-SPEC-2026-10-18.md section 4, the chosen option (b): a
// public amount, a hidden owner, no ciphertext and no proof). A sealer
// REGISTERS a shielded payout address by a transaction of no value to the
// pool address whose data is RegisterTag then the 1,227-byte address (pk,
// ek, d; the wallet's Address::to_bytes); Finalize stores it under the
// sender's account from the next block. From then on the block reward's
// sealer share of every block the account seals is MINTED as a note to pk
// in place of the public credit: r = B("Igneum Money coinbase mint",
// chain id, height, parent hash, sealer) reduced to a word, cm = H(H(v,
// pk), r), the commitment appended to the tree after the block's
// transactions' records, the pool address credited with v and the
// turnstile raised by v, and the record (the sealer, the value, the leaf
// index) kept in the pool's storage, so a node of the minimum class
// verifies the mint by the same arithmetic as the public credit, to the
// wei, and the state root covers the record (the block body is unchanged:
// the spec's body field is not needed when every field of the record is
// a function of the header and the registry). The fund's share is public
// as before. A registration with an empty address clears it; a
// registration with an encapsulation key that fails FIPS 203's check, or
// one carrying value, is refused with the value returned.
const (
	RegisterTag = 0x03

	// KEMPublicKey is ML-KEM-768's encapsulation key size.
	KEMPublicKey = 1184
	// Diversifier is the address's diversifier size (F-756: it means
	// nothing yet; carried so the address has its final shape).
	Diversifier = 11
	// AddressBytes is the wallet's address: pk (32), ek (1,184), d (11).
	AddressBytes = 32 + KEMPublicKey + Diversifier

	mintContext = "Igneum Money coinbase mint"
)

// Registration is a sealer's shielded payout address as the record carries
// it.
type Registration struct {
	PK [32]byte
	EK []byte
	D  [Diversifier]byte
}

// Encode is the record's bytes: the tag then the address (an empty
// registration, the tag alone, clears the sender's address).
func (r Registration) Encode() []byte {
	out := make([]byte, 0, 1+AddressBytes)
	out = append(out, RegisterTag)
	if len(r.EK) == 0 && r.PK == ([32]byte{}) {
		return out
	}
	out = append(out, r.PK[:]...)
	out = append(out, r.EK...)
	return append(out, r.D[:]...)
}

// Empty says whether the registration clears the address.
func (r Registration) Empty() bool { return len(r.EK) == 0 && r.PK == ([32]byte{}) }

// ParseRegistration reads a registration record by its layout (false: not
// one). The rules (the key's check, the word check) are AdmitRegistration's.
func ParseRegistration(data []byte) (Registration, bool) {
	if len(data) < 1 || data[0] != RegisterTag {
		return Registration{}, false
	}
	if len(data) == 1 {
		return Registration{}, true
	}
	if len(data) != 1+AddressBytes {
		return Registration{}, false
	}
	var r Registration
	copy(r.PK[:], data[1:33])
	r.EK = append([]byte(nil), data[33:33+KEMPublicKey]...)
	copy(r.D[:], data[33+KEMPublicKey:])
	return r, true
}

// AdmitRegistration applies a registration from sender: "" admits (the
// address stored, or cleared by an empty one), else the refusal's words;
// nothing is written on a refusal. The transaction carries no value.
func (p *State) AdmitRegistration(sender common.Address, value *big.Int, r Registration) string {
	if value != nil && value.Sign() != 0 {
		return "a registration carries no value"
	}
	if r.Empty() {
		p.clearPayout(sender)
		return ""
	}
	if r.PK == ([32]byte{}) {
		return "the recipient key is zero"
	}
	if !IsWord(r.PK) {
		return "the recipient key is not a word"
	}
	if len(r.EK) != KEMPublicKey {
		return "the encapsulation key is not 1,184 bytes"
	}
	if _, err := mlkem.NewEncapsulationKey768(r.EK); err != nil {
		return "the encapsulation key fails FIPS 203's check"
	}
	p.touch()
	p.st.SetState(p.addr, slot("payout-pk", sender[:]), r.PK)
	for i := 0; i < KEMPublicKey; i += 32 {
		var chunk [32]byte
		copy(chunk[:], r.EK[i:])
		p.st.SetState(p.addr, slot("payout-ek", sender[:], u64(uint64(i/32))), chunk)
	}
	var d [32]byte
	copy(d[:], r.D[:])
	p.st.SetState(p.addr, slot("payout-d", sender[:]), d)
	return ""
}

func (p *State) clearPayout(sender common.Address) {
	if _, ok := p.PayoutKey(sender); !ok {
		return
	}
	p.st.SetState(p.addr, slot("payout-pk", sender[:]), common.Hash{})
	for i := 0; i < KEMPublicKey/32+1; i++ {
		p.st.SetState(p.addr, slot("payout-ek", sender[:], u64(uint64(i))), common.Hash{})
	}
	p.st.SetState(p.addr, slot("payout-d", sender[:]), common.Hash{})
}

// PayoutKey is the recipient key an account registered (false: none).
func (p *State) PayoutKey(account common.Address) ([32]byte, bool) {
	pk := [32]byte(p.st.GetState(p.addr, slot("payout-pk", account[:])))
	return pk, pk != ([32]byte{})
}

// PayoutAddress is the whole address an account registered (false: none).
func (p *State) PayoutAddress(account common.Address) (Registration, bool) {
	pk, ok := p.PayoutKey(account)
	if !ok {
		return Registration{}, false
	}
	r := Registration{PK: pk, EK: make([]byte, KEMPublicKey)}
	for i := 0; i < KEMPublicKey; i += 32 {
		chunk := p.st.GetState(p.addr, slot("payout-ek", account[:], u64(uint64(i/32))))
		copy(r.EK[i:], chunk[:])
	}
	d := p.st.GetState(p.addr, slot("payout-d", account[:]))
	copy(r.D[:], d[:Diversifier])
	return r, true
}

// MintRandomness is r for a coinbase note: B over the chain id, the
// height, the parent hash and the sealer, reduced to a word (each element
// little-endian, mod the field); public data, so the sealer's wallet
// recomputes it from the block alone.
func MintRandomness(chainID, height uint64, parent common.Hash, sealer common.Address) [32]byte {
	var material [8 + 8 + 32 + 20]byte
	binary.BigEndian.PutUint64(material[0:8], chainID)
	binary.BigEndian.PutUint64(material[8:16], height)
	copy(material[16:48], parent[:])
	copy(material[48:], sealer[:])
	var out [32]byte
	blake3.DeriveKey(out[:], mintContext, material[:])
	return WordBytes(wordOf(out))
}

// Mint appends the sealer's coinbase note: the commitment from pk (the
// key the sealer had registered as the block began, read by the caller
// before the block's records, so a registration or a clearing carried by
// the block counts from the next), the value and the block's randomness;
// the turnstile and the minted sum raised by the value; the pool address
// credited (the value the public credit would have carried); the record
// kept by leaf index and by height. ok is false when pk is zero, the
// value is not a 128-bit amount or the tree is full, and nothing is
// written.
func (p *State) Mint(sealer common.Address, pk [32]byte, value *big.Int, height uint64, parent common.Hash) (index uint64, cm [32]byte, ok bool) {
	if pk == ([32]byte{}) || value == nil || value.Sign() <= 0 || value.BitLen() > maxValueBits || p.Count() >= uint64(1)<<Depth {
		return 0, [32]byte{}, false
	}
	p.touch()
	r := MintRandomness(p.chainID, height, parent, sealer)
	cm = p.h.Commitment(value, pk, r)
	index = p.append(cm, nil) // a coinbase note carries no ciphertext (ct[i] is keccak of nothing)
	p.setTurnstile(new(big.Int).Add(p.Turnstile(), value))
	p.st.SetState(p.addr, slot("minted"), common.BigToHash(new(big.Int).Add(p.Minted(), value)))
	p.st.SetState(p.addr, slot("mint-key", u64(index)), common.BytesToHash(sealer[:]))
	p.st.SetState(p.addr, slot("mint-value", u64(index)), common.BigToHash(value))
	p.st.SetState(p.addr, slot("mint-at", u64(height)), common.BytesToHash(u64(index+1)))
	p.st.AddBalance(p.addr, uint256.MustFromBig(value))
	return index, cm, true
}

// Minted is the sum of every coinbase note's value since genesis (the
// turnstile's minted-in term: turnstile = shielded in + minted - unshielded
// out - fees).
func (p *State) Minted() *big.Int {
	return p.st.GetState(p.addr, slot("minted")).Big()
}

// MintAt is the leaf index of the coinbase note a block minted (false:
// the block minted none).
func (p *State) MintAt(height uint64) (uint64, bool) {
	v := binary.BigEndian.Uint64(p.st.GetState(p.addr, slot("mint-at", u64(height))).Bytes()[24:])
	return v - 1, v > 0
}

// MintRecord is the sealer and the value behind a leaf (false: the leaf
// is not a coinbase note).
func (p *State) MintRecord(index uint64) (sealer common.Address, value *big.Int, ok bool) {
	k := p.st.GetState(p.addr, slot("mint-key", u64(index)))
	if k == (common.Hash{}) {
		return common.Address{}, nil, false
	}
	return common.BytesToAddress(k[:]), p.st.GetState(p.addr, slot("mint-value", u64(index))).Big(), true
}
