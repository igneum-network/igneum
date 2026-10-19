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
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// TestOneSealPerHeightUnderAParent (the project's order of 23:0x UK, 11 October
// 2026, on A7's second seal at 935): once a proven seal for (height,
// parent) is delivered, a re-cut template at the same pair starts no
// search and a find on it is discarded, so no second seal for the height
// leaves the node; the worker's ClearSealed (its own-validation refusal)
// reopens the pair; a template under another parent at the same height
// seals as usual; a late deposit for a sealed pair is dropped at the
// forward.
func TestOneSealPerHeightUnderAParent(t *testing.T) {
	e := newTestEngine(t)
	e.SetThreads(1)
	parent := common.HexToHash("0xaa")
	mk := func(parent common.Hash, tm uint64) *types.Block {
		return types.NewBlockWithHeader(&types.Header{Number: big.NewInt(7), ParentHash: parent, Difficulty: new(big.Int).Set(testDifficulty), Time: tm, GasLimit: 1})
	}
	seal := func(b *types.Block, wait time.Duration) *types.Block {
		results := make(chan *types.Block, 1)
		stop := make(chan struct{})
		defer close(stop)
		if err := e.Seal(nil, b, results, stop); err != nil {
			t.Fatal(err)
		}
		select {
		case r := <-results:
			return r
		case <-time.After(wait):
			return nil
		}
	}
	first := mk(parent, 1000)
	if seal(first, 2*time.Second) == nil {
		t.Fatal("the first template found no seal")
	}
	// A re-cut at the same height under the same parent: no search, no
	// result.
	recut := mk(parent, 1001)
	if r := seal(recut, 300*time.Millisecond); r != nil {
		t.Fatalf("a second seal left the node for the sealed pair: %v", r.Hash())
	}
	// A find deposited for the sealed pair is dropped at the forward.
	results := make(chan *types.Block, 1)
	e.deliverSeal(results, recut.WithSeal(recut.Header()), "test")
	select {
	case r := <-results:
		t.Fatalf("a late result for the sealed pair was forwarded: %v", r.Hash())
	default:
	}
	// Another parent at the same height seals.
	other := mk(common.HexToHash("0xbb"), 1001)
	if seal(other, 2*time.Second) == nil {
		t.Fatal("a template under another parent at the sealed height found no seal")
	}
	// The mark is the LAST pair: the first parent is open again (a reorg
	// back to it is a new head to the worker), and ClearSealed reopens
	// the current one for the own-validation refusal.
	if seal(mk(parent, 1002), 2*time.Second) == nil {
		t.Fatal("the earlier parent stayed marked")
	}
	if r := seal(mk(parent, 1003), 300*time.Millisecond); r != nil {
		t.Fatalf("a second seal for the re-marked pair: %v", r.Hash())
	}
	e.ClearSealed(7, parent)
	if seal(mk(parent, 1004), 2*time.Second) == nil {
		t.Fatal("ClearSealed did not reopen the pair")
	}
	e.ClearSealed(8, parent) // another pair: no effect
	if r := seal(mk(parent, 1005), 300*time.Millisecond); r != nil {
		t.Fatalf("a clear of another pair reopened this one: %v", r.Hash())
	}
}
