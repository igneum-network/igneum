// Copyright 2026 The core-geth Authors
// This file is part of the core-geth library.
//
// The core-geth library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The core-geth library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the core-geth library. If not, see <http://www.gnu.org/licenses/>.

package miner

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/igneum"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

// markOrderEngine wraps the faker and records the share lane's calls from
// the worker: whether MarkCarried ran while the block was still absent
// from the chain (before the insert), and how often UnmarkCarried ran.
type markOrderEngine struct {
	*igneum.Engine
	markedBeforeInsert atomic.Int32
	markedAfterInsert  atomic.Int32
	unmarked           atomic.Int32
}

func (e *markOrderEngine) MarkCarried(chain consensus.ChainHeaderReader, block *types.Block) {
	if chain.GetHeader(block.Hash(), block.NumberU64()) == nil {
		e.markedBeforeInsert.Add(1)
	} else {
		e.markedAfterInsert.Add(1)
	}
	e.Engine.MarkCarried(chain, block)
}

func (e *markOrderEngine) UnmarkCarried(block *types.Block) {
	e.unmarked.Add(1)
	e.Engine.UnmarkCarried(block)
}

// TestOwnBlockMarkedCarriedBeforeInsert: the worker marks its own block's
// shares carried BEFORE w.chain.InsertChain, so the chain's side event for
// an own block inserted beside a heavier twin finds the carried set and
// puts the shares back (27 of a member's 174 shares at height 783 on the
// ninth development chain, 12 October 2026, stayed carried on pod d's
// account after the uncarry ran first on nothing: the node team's
// identity-4 read), and unmarks when its own validation refuses the block.
func TestOwnBlockMarkedCarriedBeforeInsert(t *testing.T) {
	t.Parallel()
	faker := igneum.NewFaker(igneum.TestParts())
	w, b := newTestWorker(t, params.IgneumTestChainConfig, faker, rawdb.NewMemoryDatabase(), 0)
	defer w.close()
	// The chain validates with the faker; the worker's share-lane calls go
	// through the recorder (the worker's engine is the field the result
	// loop asserts MarkCarried and UnmarkCarried on).
	engine := &markOrderEngine{Engine: faker}
	w.engine = engine

	sub := w.mux.Subscribe(core.NewMinedBlockEvent{})
	defer sub.Unsubscribe()

	w.start()
	b.txPool.Add([]*types.Transaction{b.newRandomTx(true)}, true, false)

	select {
	case ev := <-sub.Chan():
		if n := ev.Data.(core.NewMinedBlockEvent).Block.NumberU64(); n != 1 {
			t.Fatalf("announced block %d, want 1", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no block sealed within 5 s")
	}
	w.stop()
	if engine.markedBeforeInsert.Load() == 0 {
		t.Fatalf("the own block's shares were not marked carried before the insert (before %d, after %d)",
			engine.markedBeforeInsert.Load(), engine.markedAfterInsert.Load())
	}
	if engine.markedAfterInsert.Load() != 0 {
		t.Fatalf("the worker marked the own block after the insert (%d times): the side event's uncarry would run first",
			engine.markedAfterInsert.Load())
	}
	if engine.unmarked.Load() != 0 {
		t.Fatalf("an accepted own block was unmarked %d times", engine.unmarked.Load())
	}
}

// TestRefusedOwnBlockUnmarked: a block the node's own validation refuses
// is unmarked, so its shares return to the pool for the re-cut template.
func TestRefusedOwnBlockUnmarked(t *testing.T) {
	t.Parallel()
	failer := igneum.NewFakeFailer(igneum.TestParts(), 1)
	w, b := newTestWorker(t, params.IgneumTestChainConfig, failer, rawdb.NewMemoryDatabase(), 0)
	defer w.close()
	engine := &markOrderEngine{Engine: failer}
	w.engine = engine

	sub := w.mux.Subscribe(core.NewMinedBlockEvent{})
	defer sub.Unsubscribe()

	w.start()
	b.txPool.Add([]*types.Transaction{b.newRandomTx(true)}, true, false)

	select {
	case ev := <-sub.Chan():
		t.Fatalf("an invalid sealed block %d was announced", ev.Data.(core.NewMinedBlockEvent).Block.NumberU64())
	case <-time.After(3 * time.Second):
	}
	w.stop()
	if engine.markedBeforeInsert.Load() == 0 {
		t.Fatal("the refused block's shares were never marked before its insert")
	}
	if engine.unmarked.Load() == 0 {
		t.Fatal("the refused block's shares were not unmarked: they would stay out of the pool")
	}
}
