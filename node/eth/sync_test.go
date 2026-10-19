// Copyright 2015 The go-ethereum Authors
// This file is part of the go-ethereum library.
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

package eth

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/ethereum/go-ethereum/consensus/igneum"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/eth/downloader"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/eth/protocols/snap"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/params/types/genesisT"
	"github.com/ethereum/go-ethereum/triedb"
)

// blockGenContemporaryTime creates a block gen function that will bump the block times to within throwing
// distance of the current time.
// Without this, block times are built on a genesis with a 0 (or 10) timestamp, which is in 1970.
// This causes the apparent age of the chain to exceed the safety interval and thus interferes with
// reasonable expectations of staleness.
func blockGenContemporaryTime(numBlocks int64) func(i int, gen *core.BlockGen) {
	startTimeUnix := time.Now().Unix()
	return func(i int, gen *core.BlockGen) {
		if i == 0 {
			// Here, 1024 is the max number of block we'll create.
			// 10 is the magic number that the test helper block gen will use for the block time offsets.
			gen.OffsetTime(startTimeUnix - (numBlocks * 10))
		}
	}
}

// newTestHandlerWithBlocks creates a new handler for testing purposes, with a
// given number of initial blocks.
func newTestHandlerWithBlocksWithOpts(blocks int, mode downloader.SyncMode, gen func(int, *core.BlockGen)) *testHandler {
	// Create a database pre-initialize with a genesis block
	db := rawdb.NewMemoryDatabase()
	gspec := &genesisT.Genesis{
		Config: params.IgneumTestChainConfig,
		Alloc:  genesisT.GenesisAlloc{testAddr: {Balance: big.NewInt(1000000)}},
	}
	core.MustCommitGenesis(db, triedb.NewDatabase(db, nil), gspec)

	chain, _ := core.NewBlockChain(db, nil, gspec, nil, igneum.NewFaker(igneum.TestParts()), vm.Config{}, nil, nil)

	bs, _ := core.GenerateChain(params.IgneumTestChainConfig, chain.Genesis(), igneum.NewFaker(igneum.TestParts()), db, blocks, gen)
	if _, err := chain.InsertChain(bs); err != nil {
		panic(err)
	}
	txpool := newTestTxPool()

	handler, _ := newHandler(&handlerConfig{
		Database:   db,
		Chain:      chain,
		TxPool:     txpool,
		Network:    1,
		Sync:       mode,
		BloomCache: 1,
	})
	handler.Start(1000)

	return &testHandler{
		db:      db,
		chain:   chain,
		txpool:  txpool,
		handler: handler,
	}
}

// Tests that snap sync is disabled after a successful sync cycle.
func TestSnapSyncDisabling68(t *testing.T) { testSnapSyncDisabling(t, eth.ETH68, snap.SNAP1) }

// Tests that snap sync gets disabled as soon as a real block is successfully
// imported into the blockchain.
func testSnapSyncDisabling(t *testing.T, ethVer uint, snapVer uint) {
	t.Parallel()

	// Create an empty handler and ensure it's in snap sync mode
	empty := newTestHandler()
	if !empty.handler.snapSync.Load() {
		t.Fatalf("snap sync disabled on pristine blockchain")
	}
	defer empty.close()

	// Create a full handler and ensure snap sync ends up disabled
	full := newTestHandlerWithBlocks(1024)
	if full.handler.snapSync.Load() {
		t.Fatalf("snap sync not disabled on non-empty blockchain")
	}
	defer full.close()

	// Sync up the two handlers via both `eth` and `snap`
	caps := []p2p.Cap{{Name: "eth", Version: ethVer}, {Name: "snap", Version: snapVer}}

	emptyPipeEth, fullPipeEth := p2p.MsgPipe()
	defer emptyPipeEth.Close()
	defer fullPipeEth.Close()

	emptyPeerEth := eth.NewPeer(ethVer, p2p.NewPeer(enode.ID{1}, "", caps), emptyPipeEth, empty.txpool)
	fullPeerEth := eth.NewPeer(ethVer, p2p.NewPeer(enode.ID{2}, "", caps), fullPipeEth, full.txpool)
	defer emptyPeerEth.Close()
	defer fullPeerEth.Close()

	go empty.handler.runEthPeer(emptyPeerEth, func(peer *eth.Peer) error {
		return eth.Handle((*ethHandler)(empty.handler), peer)
	})
	go full.handler.runEthPeer(fullPeerEth, func(peer *eth.Peer) error {
		return eth.Handle((*ethHandler)(full.handler), peer)
	})

	emptyPipeSnap, fullPipeSnap := p2p.MsgPipe()
	defer emptyPipeSnap.Close()
	defer fullPipeSnap.Close()

	emptyPeerSnap := snap.NewPeer(snapVer, p2p.NewPeer(enode.ID{1}, "", caps), emptyPipeSnap)
	fullPeerSnap := snap.NewPeer(snapVer, p2p.NewPeer(enode.ID{2}, "", caps), fullPipeSnap)

	go empty.handler.runSnapExtension(emptyPeerSnap, func(peer *snap.Peer) error {
		return snap.Handle((*snapHandler)(empty.handler), peer)
	})
	go full.handler.runSnapExtension(fullPeerSnap, func(peer *snap.Peer) error {
		return snap.Handle((*snapHandler)(full.handler), peer)
	})
	// Wait a bit for the above handlers to start
	time.Sleep(250 * time.Millisecond)

	// Check that snap sync was disabled
	op := peerToSyncOp(downloader.SnapSync, empty.handler.peers.peerWithHighestTD())
	if err := empty.handler.doSync(op); err != nil {
		t.Fatal("sync failed:", err)
	}
	if empty.handler.snapSync.Load() {
		t.Fatalf("snap sync not disabled after successful synchronisation")
	}
}

// TestRejoinProbeTake (F-143, 15 October 2026): the rejoin probe's rule for
// a peer: a head this node holds is never probed; an unknown head is probed
// once, not again within the minute, again after it; a peer whose head
// moved is probed on the new head at once.
func TestRejoinProbeTake(t *testing.T) {
	known := common.HexToHash("0x01")
	unknown := common.HexToHash("0x02")
	moved := common.HexToHash("0x03")
	has := func(h common.Hash) bool { return h == known }
	probed := map[string]probedHead{}
	now := time.Unix(1_000_000, 0)
	if rejoinTake(has, probed, "p1", known, now) {
		t.Fatal("a held head probed")
	}
	if !rejoinTake(has, probed, "p1", unknown, now) {
		t.Fatal("an unknown head not probed")
	}
	if rejoinTake(has, probed, "p1", unknown, now.Add(30*time.Second)) {
		t.Fatal("the same head probed again within the minute")
	}
	if !rejoinTake(has, probed, "p1", moved, now.Add(31*time.Second)) {
		t.Fatal("a moved head not probed at once")
	}
	if !rejoinTake(has, probed, "p1", moved, now.Add(2*time.Minute)) {
		t.Fatal("the head not probed again after the minute")
	}
	if !rejoinTake(has, probed, "p2", unknown, now) {
		t.Fatal("another peer's unknown head not probed")
	}
}
