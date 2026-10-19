// Copyright 2020 The go-ethereum Authors
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

package core

import (
	"errors"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/igneum"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/params/types/genesisT"
	"github.com/ethereum/go-ethereum/params/vars"
	"github.com/ethereum/go-ethereum/triedb"
)

func verifyUnbrokenCanonchain(hc *HeaderChain) error {
	h := hc.CurrentHeader()
	for {
		canonHash := rawdb.ReadCanonicalHash(hc.chainDb, h.Number.Uint64())
		if exp := h.Hash(); canonHash != exp {
			return fmt.Errorf("Canon hash chain broken, block %d got %x, expected %x",
				h.Number, canonHash[:8], exp[:8])
		}
		// Verify that we have the TD
		if td := rawdb.ReadTd(hc.chainDb, canonHash, h.Number.Uint64()); td == nil {
			return fmt.Errorf("Canon TD missing at block %d", h.Number)
		}
		if h.Number.Uint64() == 0 {
			break
		}
		h = hc.GetHeader(h.ParentHash, h.Number.Uint64()-1)
	}
	return nil
}

func testInsert(t *testing.T, hc *HeaderChain, chain []*types.Header, wantStatus WriteStatus, wantErr error, forker *ForkChoice) {
	t.Helper()

	status, err := hc.InsertHeaderChain(chain, time.Now(), forker)
	if status != wantStatus {
		t.Errorf("wrong write status from InsertHeaderChain: got %v, want %v", status, wantStatus)
	}
	// Always verify that the header chain is unbroken
	if err := verifyUnbrokenCanonchain(hc); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("unexpected error from InsertHeaderChain: %v", err)
	}
}

// This test checks status reporting of InsertHeaderChain.
func TestHeaderInsertion(t *testing.T) {
	var (
		db    = rawdb.NewMemoryDatabase()
		gspec = &genesisT.Genesis{BaseFee: big.NewInt(vars.InitialBaseFee), Config: params.IgneumTestChainConfig}
	)
	CommitGenesis(gspec, db, triedb.NewDatabase(db, nil))

	hc, err := NewHeaderChain(db, gspec.Config, igneum.NewFaker(igneum.TestParts()), func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	// chain A: G->A1->A2...A128
	genDb, chainA := makeHeaderChainWithGenesis(gspec, 128, igneum.NewFaker(igneum.TestParts()), 10)
	// chain B: G->A1->B1...B128
	chainB := makeHeaderChain(gspec.Config, chainA[0], 128, igneum.NewFaker(igneum.TestParts()), genDb, 10)

	forker := NewForkChoice(hc, nil)
	// Inserting 64 headers on an empty chain, expecting
	// 1 callbacks, 1 canon-status, 0 sidestatus,
	testInsert(t, hc, chainA[:64], CanonStatTy, nil, forker)

	// Inserting 64 identical headers, expecting
	// 0 callbacks, 0 canon-status, 0 sidestatus,
	testInsert(t, hc, chainA[:64], NonStatTy, nil, forker)

	// Inserting the same some old, some new headers
	// 1 callbacks, 1 canon, 0 side
	testInsert(t, hc, chainA[32:96], CanonStatTy, nil, forker)

	// Inserting side blocks, but not overtaking the canon chain
	testInsert(t, hc, chainB[0:32], SideStatTy, nil, forker)

	// Inserting more side blocks, but we don't have the parent
	testInsert(t, hc, chainB[34:36], NonStatTy, consensus.ErrUnknownAncestor, forker)

	// Inserting more sideblocks, overtaking the canon chain
	testInsert(t, hc, chainB[32:97], CanonStatTy, nil, forker)

	// Inserting more A-headers, taking back the canonicality
	testInsert(t, hc, chainA[90:100], CanonStatTy, nil, forker)

	// And B becomes canon again
	testInsert(t, hc, chainB[97:107], CanonStatTy, nil, forker)

	// And B becomes even longer
	testInsert(t, hc, chainB[107:128], CanonStatTy, nil, forker)
}

// TestReorgCanonicalIndexAfterFork (F-92, 13 October 2026): a node on its
// own fork adopts the heavier chain; after the reorg the canonical
// number->hash index must name the new chain at EVERY height from the fork
// point to the new head, with none left on the dropped fork and no gap
// (the read-by-number replay a pool, explorer or fresh peer makes).
func TestReorgCanonicalIndexAfterFork(t *testing.T) {
	var (
		db    = rawdb.NewMemoryDatabase()
		gspec = &genesisT.Genesis{BaseFee: big.NewInt(vars.InitialBaseFee), Config: params.IgneumTestChainConfig}
	)
	CommitGenesis(gspec, db, triedb.NewDatabase(db, nil))
	hc, err := NewHeaderChain(db, gspec.Config, igneum.NewFaker(igneum.TestParts()), func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	// chainA: the heavier chain the node will adopt. chainB: the node's own
	// fork from A's 13th header.
	genDb, chainA := makeHeaderChainWithGenesis(gspec, 128, igneum.NewFaker(igneum.TestParts()), 10)
	chainB := makeHeaderChain(gspec.Config, chainA[12], 60, igneum.NewFaker(igneum.TestParts()), genDb, 11)
	forker := NewForkChoice(hc, nil)
	// The node seals and canonicalises its own fork first (G..A13..B).
	testInsert(t, hc, chainA[:13], CanonStatTy, nil, forker)
	testInsert(t, hc, chainB, CanonStatTy, nil, forker)
	// It then adopts the heavier chainA. Imported in two batches, the
	// first a side chain that does not overtake, the second overtaking:
	// the reorg must rewrite the canonical index across the whole span,
	// not only the overtaking batch.
	testInsert(t, hc, chainA[13:40], SideStatTy, nil, forker)
	testInsert(t, hc, chainA[40:128], CanonStatTy, nil, forker)
	// verifyUnbrokenCanonchain (inside testInsert) already walks head to
	// genesis; assert explicitly that no height names the dropped fork and
	// none is empty, over the whole fork-point-to-head span.
	for _, h := range chainA {
		n := h.Number.Uint64()
		got := rawdb.ReadCanonicalHash(hc.chainDb, n)
		if got != h.Hash() {
			t.Fatalf("canonical index at height %d: got %x, want chainA %x (fork left stale or empty)", n, got[:8], h.Hash().Bytes()[:8])
		}
	}
	if hc.CurrentHeader().Hash() != chainA[len(chainA)-1].Hash() {
		t.Fatal("the head is not chainA's")
	}
}
