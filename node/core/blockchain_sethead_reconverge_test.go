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

package core

import (
	"testing"

	"github.com/ethereum/go-ethereum/consensus/igneum"
	"github.com/ethereum/go-ethereum/core/rawdb"
)

// TestSetHeadReconverges is the condition under which debug_setHead stays on
// the allow list (security review, 9 October 2026): a rewind creates no pin.
// After SetHead the node must accept the blocks it just dropped and return to
// its old head, and a shorter fork mined on the rewound head must not hold the
// node once the heavier original chain is offered again.
func TestSetHeadReconverges(t *testing.T) {
	testSetHeadReconverges(t, rawdb.HashScheme)
	testSetHeadReconverges(t, rawdb.PathScheme)
}

func testSetHeadReconverges(t *testing.T, scheme string) {
	const (
		length = 12
		rewind = 6
	)
	engine := igneum.NewFaker(igneum.TestParts())
	_, genesis, chain, err := newCanonical(engine, 0, true, scheme)
	if err != nil {
		t.Fatalf("%s: failed to make new canonical chain: %v", scheme, err)
	}
	defer chain.Stop()

	// genDb holds the state of every generated block; the fork is built on it.
	genDb, blocks := makeBlockChainWithGenesis(genesis, length, engine, canonicalSeed)
	if _, err := chain.InsertChain(blocks); err != nil {
		t.Fatalf("%s: failed to insert the chain: %v", scheme, err)
	}
	oldHead := chain.CurrentBlock()
	if oldHead.Number.Uint64() != length {
		t.Fatalf("%s: head is %d, want %d", scheme, oldHead.Number.Uint64(), length)
	}

	// Rewind, as debug_setHead does.
	if err := chain.SetHead(rewind); err != nil {
		t.Fatalf("%s: SetHead(%d): %v", scheme, rewind, err)
	}
	if n := chain.CurrentBlock().Number.Uint64(); n != rewind {
		t.Fatalf("%s: head after SetHead is %d, want %d", scheme, n, rewind)
	}
	if chain.GetBlockByHash(oldHead.Hash()) != nil {
		t.Fatalf("%s: the dropped head %x is still a known block after SetHead", scheme, oldHead.Hash())
	}

	// A shorter fork arrives on the rewound head first: it becomes the head, as
	// it is the only chain the node knows past the rewind point.
	forkParent := chain.GetBlockByHash(chain.CurrentBlock().Hash())
	fork := makeBlockChain(chain.chainConfig, forkParent, 3, engine, genDb, forkSeed)
	if _, err := chain.InsertChain(fork); err != nil {
		t.Fatalf("%s: failed to insert the fork: %v", scheme, err)
	}
	if h := chain.CurrentBlock(); h.Hash() != fork[len(fork)-1].Hash() {
		t.Fatalf("%s: head after the fork is %x, want the fork tip %x", scheme, h.Hash(), fork[len(fork)-1].Hash())
	}

	// The original (heavier) chain is offered again from the rewind point: the
	// node must reconverge on its old head. Nothing about the rewind pins it.
	if _, err := chain.InsertChain(blocks[rewind:]); err != nil {
		t.Fatalf("%s: failed to re-insert the dropped blocks: %v", scheme, err)
	}
	if h := chain.CurrentBlock(); h.Hash() != oldHead.Hash() {
		t.Fatalf("%s: head after reconvergence is %x at %d, want the old head %x at %d", scheme, h.Hash(), h.Number.Uint64(), oldHead.Hash(), length)
	}
	for i, b := range blocks {
		if got := chain.GetCanonicalHash(uint64(i + 1)); got != b.Hash() {
			t.Fatalf("%s: canonical hash at %d is %x, want %x", scheme, i+1, got, b.Hash())
		}
	}
}
