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

package types

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rlp"
)

// TestHeaderTailRoundTrip: the work-authority prototype's optional tail
// (bondKey, round, sig, shares) survives the GENERATED encoder and the
// decoder, in every combination of presence, and the block hash covers the
// shares. The generated encoder is what every header write and wire message
// use; a field added to the struct alone is dropped on encoding (the fifth
// development chain's keyed seals lost their shares between the search and
// the block write, 11 October 2026).
func TestHeaderTailRoundTrip(t *testing.T) {
	base := func() *Header {
		return &Header{
			ParentHash: common.HexToHash("0x01"),
			Coinbase:   common.HexToAddress("0x02"),
			Root:       common.HexToHash("0x03"),
			TxHash:     EmptyTxsHash,
			Difficulty: big.NewInt(7),
			Number:     big.NewInt(9),
			GasLimit:   1_000_000,
			Time:       1_800_000_000,
			BaseFee:    big.NewInt(1),
		}
	}
	key := common.HexToHash("0xabcd")
	sig := bytes.Repeat([]byte{0x5a}, 64)
	shares := bytes.Repeat([]byte{0x33}, 64)
	cases := []struct {
		name string
		set  func(h *Header)
	}{
		{"none", func(h *Header) {}},
		{"key", func(h *Header) { k := key; h.BondKey = &k }},
		{"key round sig", func(h *Header) { k := key; h.BondKey = &k; h.Round = 3; h.Sig = sig }},
		{"key sig shares", func(h *Header) { k := key; h.BondKey = &k; h.Sig = sig; h.Shares = shares }},
		{"key round sig shares", func(h *Header) { k := key; h.BondKey = &k; h.Round = 2; h.Sig = sig; h.Shares = shares }},
		{"shares alone", func(h *Header) { h.Shares = shares }},
	}
	for _, c := range cases {
		h := base()
		c.set(h)
		enc, err := rlp.EncodeToBytes(h)
		if err != nil {
			t.Fatalf("%s: encode: %v", c.name, err)
		}
		var dec Header
		if err := rlp.DecodeBytes(enc, &dec); err != nil {
			t.Fatalf("%s: decode: %v", c.name, err)
		}
		if (dec.BondKey == nil) != (h.BondKey == nil) || (dec.BondKey != nil && *dec.BondKey != *h.BondKey) {
			t.Fatalf("%s: bond key lost", c.name)
		}
		if dec.Round != h.Round || !bytes.Equal(dec.Sig, h.Sig) || !bytes.Equal(dec.Shares, h.Shares) {
			t.Fatalf("%s: tail differs after the round trip: round %d sig %d shares %d", c.name, dec.Round, len(dec.Sig), len(dec.Shares))
		}
		if dec.Hash() != h.Hash() {
			t.Fatalf("%s: hash differs after the round trip", c.name)
		}
		// The copy keeps the tail too.
		cp := CopyHeader(h)
		if cp.Hash() != h.Hash() || !bytes.Equal(cp.Shares, h.Shares) {
			t.Fatalf("%s: copy drops the tail", c.name)
		}
	}
	// The block hash covers the shares: the same header with other shares
	// hashes differently.
	a, b := base(), base()
	k := key
	a.BondKey, b.BondKey = &k, &k
	a.Sig, b.Sig = sig, sig
	a.Shares = shares
	b.Shares = bytes.Repeat([]byte{0x34}, 64)
	if a.Hash() == b.Hash() {
		t.Fatal("the block hash does not cover the shares")
	}
}
