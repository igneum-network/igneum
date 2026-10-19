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
	"os"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
)

// The keyed search rate of the CPU sealer on this machine: the prover's hash
// under a secret through the reference adapter, the full-dataset context the
// sealer runs with (sealer.go SetFullDataset), the chain's own document.
// The document is read from IGNEUM_KEYED_DOC when set, else the ninth
// development chain's launch document. The dataset build is outside the
// timed loop. Serial is one thread's rate; Parallel is the adapter's rate
// over GOMAXPROCS goroutines (-cpu), which the adapter's mutex serialises:
// the sealer's aggregate rate is read here, not inferred from thread
// counts. ops/s = 1e9 / ns/op in both forms.
func BenchmarkKeyedSearchRate(b *testing.B) {
	doc := []byte(`{"version": 8, "dataset_mib": 2048, "scratch_kib": 0, "rounds": 64, "cache_mib": 32, "parents": 256, "item_bytes": 128, "blocks": 2, "registers": 16, "features": 5152, "chain_id": 7319, "keyed_rounds": 2}`)
	if p := os.Getenv("IGNEUM_KEYED_DOC"); p != "" {
		d, err := os.ReadFile(p)
		if err != nil {
			b.Fatalf("document: %v", err)
		}
		doc = d
	}
	a, err := newIgneumhashRef(7319, doc)
	if err != nil {
		b.Fatalf("document: %v", err)
	}
	k, ok := a.(pow.Keyed)
	if !ok {
		b.Fatal("the reference adapter has no keyed entry")
	}
	if f, ok := a.(interface{ SetFullDataset(bool) }); ok {
		f.SetFullDataset(true)
	}
	var sk [32]byte
	sk[0], sk[31] = 7, 9
	header := common.HexToHash("0x1234")
	if m, r, _ := k.ComputeKeyed(header, 1, 0, &sk, nil); m == (common.Hash{}) && r == (common.Hash{}) {
		b.Fatal("the prover's keyed hash refused")
	}
	b.Run("Serial", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			k.ComputeKeyed(header, 1, uint64(i)+1, &sk, nil)
		}
	})
	b.Run("Parallel", func(b *testing.B) {
		var n uint64
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				k.ComputeKeyed(header, 1, atomic.AddUint64(&n, 1), &sk, nil)
			}
		})
	})
}
