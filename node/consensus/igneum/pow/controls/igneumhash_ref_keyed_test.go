//go:build controls

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

package controls

import (
	"bytes"
	"fmt"
	"os"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
)

// TestKeyedReferenceRoundTrip: the keyed document (version 8 bit 10, S = 2)
// through the reference: the prover's hash under a secret writes S shares;
// the verifier's replay from those shares reaches the same mix and result;
// a flipped share changes the result; the keyless Compute refuses the
// document (zero hashes).
func TestKeyedReferenceRoundTrip(t *testing.T) {
	keyedRoundTrip(t, 64)
}

// TestKeyedReferenceAtChainSize is the same at the fifth development
// chain's 2048 MiB document (IGNEUM_KEYED_LARGE=1; seconds and 2 GiB).
func TestKeyedReferenceAtChainSize(t *testing.T) {
	if os.Getenv("IGNEUM_KEYED_LARGE") == "" {
		t.Skip("IGNEUM_KEYED_LARGE unset")
	}
	keyedRoundTrip(t, 2048)
}

func keyedRoundTrip(t *testing.T, datasetMiB int) {
	doc := []byte(fmt.Sprintf(`{"version": 8, "dataset_mib": %d, "scratch_kib": 0, "rounds": 64, "cache_mib": 32, "parents": 256, "item_bytes": 128, "blocks": 2, "registers": 16, "features": 5152, "chain_id": 7315, "keyed_rounds": 2}`, datasetMiB))
	a, err := newIgneumhashRef(7315, doc)
	if err != nil {
		t.Fatalf("document: %v", err)
	}
	k, ok := a.(pow.Keyed)
	if !ok {
		t.Fatal("the reference adapter has no keyed entry")
	}
	if s := k.KeyedRounds(1); s != 2 {
		t.Fatalf("keyed rounds %d", s)
	}
	var sk [32]byte
	sk[0], sk[31] = 7, 9
	header := common.HexToHash("0x1234")
	mix, result, shares := k.ComputeKeyed(header, 1, 42, &sk, nil)
	if mix == (common.Hash{}) && result == (common.Hash{}) {
		t.Fatal("the prover's keyed hash refused")
	}
	if len(shares) != 2*pow.ShareBytes {
		t.Fatalf("shares %d bytes", len(shares))
	}
	mix2, result2, out := k.ComputeKeyed(header, 1, 42, nil, shares)
	if mix2 != mix || result2 != result || !bytes.Equal(out, shares) {
		t.Fatalf("the replay differs: %x %x vs %x %x", mix2, result2, mix, result)
	}
	flipped := append([]byte(nil), shares...)
	flipped[5] ^= 1
	_, result3, _ := k.ComputeKeyed(header, 1, 42, nil, flipped)
	if result3 == result {
		t.Fatal("a flipped share leaves the result unchanged")
	}
	if m, r := a.Compute(header, 1, 42); m != (common.Hash{}) || r != (common.Hash{}) {
		t.Fatal("the keyless entry accepted a keyed document")
	}
	// A short share buffer is refused.
	if m, r, _ := k.ComputeKeyed(header, 1, 42, nil, shares[:32]); m != (common.Hash{}) || r != (common.Hash{}) {
		t.Fatal("a short share buffer was accepted")
	}
}
