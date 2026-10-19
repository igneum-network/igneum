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

package igneum

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

// TestAttributionWindow: the metric counts the canonical blocks inside the
// window by identity (bond key where present, else coinbase), sums their
// difficulty, reads shares in basis points, and cuts at the window's start
// by block time.
func TestAttributionWindow(t *testing.T) {
	chain := newSupplyChain()
	solo := common.HexToAddress("0x1111111111111111111111111111111111111111")
	pool := common.HexToAddress("0x2222222222222222222222222222222222222222")
	key := common.HexToHash("0xabcdef")
	// 10 blocks 15 s apart: heights 1 to 4 solo (difficulty 100 each),
	// 5 to 8 the pool's coinbase (200 each), 9 and 10 under the bond key
	// with the pool's coinbase (400 each).
	for i := 1; i <= 10; i++ {
		h := chain.extend(0, 1)
		switch {
		case i <= 4:
			h.Coinbase, h.Difficulty = solo, big.NewInt(100)
		case i <= 8:
			h.Coinbase, h.Difficulty = pool, big.NewInt(200)
		default:
			h.Coinbase, h.Difficulty = pool, big.NewInt(400)
			k := key
			h.BondKey = &k
		}
		// re-index the mutated header (the fake chain keys by hash)
		chain.add(h)
		chain.canonical[len(chain.canonical)-1] = h
	}
	// Each extend reads the parent's hash before the mutation above, so
	// rebuild the parent links on the mutated headers.
	for i := 1; i < len(chain.canonical); i++ {
		chain.canonical[i].ParentHash = chain.canonical[i-1].Hash()
		chain.add(chain.canonical[i])
	}

	e := newTestEngine(t)
	defer e.Close()
	api := NewIgneumAPI(e, chain)

	// The whole chain (window far wider than 150 s).
	rep := api.Attribution(nil)
	if rep.Blocks != 10 || rep.FromHeight != 1 || rep.Head != 10 {
		t.Fatalf("whole window: blocks %d from %d head %d", rep.Blocks, rep.FromHeight, rep.Head)
	}
	if got := (*big.Int)(rep.TotalWork).Int64(); got != 400+800+800 {
		t.Fatalf("total work %d", got)
	}
	if len(rep.Identities) != 3 {
		t.Fatalf("identities %d: %+v", len(rep.Identities), rep.Identities)
	}
	want := []struct {
		id     string
		kind   string
		blocks uint64
		work   int64
		bps    uint64
	}{
		{"bond:" + key.Hex(), "bond", 2, 800, 4000},
		{"coinbase:" + pool.Hex(), "coinbase", 4, 800, 4000},
		{"coinbase:" + solo.Hex(), "coinbase", 4, 400, 2000},
	}
	for i, w := range want {
		g := rep.Identities[i]
		if g.Identity != w.id || g.Kind != w.kind || g.Blocks != w.blocks || (*big.Int)(g.Work).Int64() != w.work || g.ShareBps != w.bps {
			t.Fatalf("row %d: got %+v want %+v", i, g, w)
		}
	}
	if len(rep.Limits) == 0 || rep.Label == "" {
		t.Fatal("the report must carry its label and limits")
	}

	// A 30 s window from the head's time keeps heights 8, 9, 10 (times
	// head-30, head-15, head): the cut is by block time.
	w := hexutil.Uint64(30)
	rep = api.Attribution(&w)
	if rep.Blocks != 3 || rep.FromHeight != 8 {
		t.Fatalf("30 s window: blocks %d from %d", rep.Blocks, rep.FromHeight)
	}
	if got := (*big.Int)(rep.TotalWork).Int64(); got != 200+800 {
		t.Fatalf("30 s window work %d", got)
	}
	if rep.Identities[0].ShareBps != 8000 || rep.Identities[1].ShareBps != 2000 {
		t.Fatalf("30 s shares %+v", rep.Identities)
	}

	// The cache: the same head and window returns the same report.
	if again := api.Attribution(&w); again != rep {
		t.Fatal("the report at an unchanged head is not cached")
	}
}
