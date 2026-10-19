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
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
)

// TestKeyedReferenceParallel: the keyed hash from eight goroutines over one
// context (the read lock) gives the serial results nonce for nonce, and
// the context's drop (SetFullDataset under the write lock) waits for the
// hashes in flight. The 64 MiB document; the sealer's threads run this
// path in parallel since the lock was narrowed.
func TestKeyedReferenceParallel(t *testing.T) {
	doc := []byte(`{"version": 8, "dataset_mib": 64, "scratch_kib": 0, "rounds": 64, "cache_mib": 32, "parents": 256, "item_bytes": 128, "blocks": 2, "registers": 16, "features": 5152, "chain_id": 7315, "keyed_rounds": 2}`)
	a, err := newIgneumhashRef(7315, doc)
	if err != nil {
		t.Fatalf("document: %v", err)
	}
	k := a.(pow.Keyed)
	var sk [32]byte
	sk[0], sk[31] = 7, 9
	header := common.HexToHash("0x1234")
	const n = 64
	type res struct {
		mix, result common.Hash
		shares      []byte
	}
	var serial [n]res
	for i := 0; i < n; i++ {
		m, r, s := k.ComputeKeyed(header, 1, uint64(i), &sk, nil)
		if m == (common.Hash{}) && r == (common.Hash{}) {
			t.Fatalf("the serial hash refused at nonce %d", i)
		}
		serial[i] = res{m, r, s}
	}
	var parallel [n]res
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := g; i < n; i += 8 {
				m, r, s := k.ComputeKeyed(header, 1, uint64(i), &sk, nil)
				parallel[i] = res{m, r, s}
			}
		}(g)
	}
	wg.Wait()
	for i := 0; i < n; i++ {
		if parallel[i].mix != serial[i].mix || parallel[i].result != serial[i].result || !bytes.Equal(parallel[i].shares, serial[i].shares) {
			t.Fatalf("nonce %d: the parallel hash differs from the serial one", i)
		}
	}
	// The context drop under the write lock beside hashes in flight.
	f := a.(interface{ SetFullDataset(bool) })
	var wg2 sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg2.Add(1)
		go func(g int) {
			defer wg2.Done()
			for i := 0; i < 16; i++ {
				m, r, _ := k.ComputeKeyed(header, 1, uint64(1000+g*16+i), &sk, nil)
				if m == (common.Hash{}) && r == (common.Hash{}) {
					t.Errorf("the hash refused beside a context drop (nonce %d)", 1000+g*16+i)
					return
				}
			}
		}(g)
	}
	f.SetFullDataset(true) // the light contexts dropped; the next hash builds the full 64 MiB one
	wg2.Wait()
	a.(interface{ Close() }).Close()
}
