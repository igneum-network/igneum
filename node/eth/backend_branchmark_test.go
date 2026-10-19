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

package eth

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// branchChain holds blocks by hash for canonicalBranchBack.
type branchChain map[common.Hash]*types.Block

func (c branchChain) GetBlock(hash common.Hash, number uint64) *types.Block {
	if b := c[hash]; b != nil && b.NumberU64() == number {
		return b
	}
	return nil
}

// linkedBlocks makes n blocks 1..n, each the child of the one before
// (block 1's parent is the given genesis hash), with distinct hashes.
func linkedBlocks(n int, genesis common.Hash) ([]*types.Block, branchChain) {
	chain := branchChain{}
	var blocks []*types.Block
	parent := genesis
	for i := 1; i <= n; i++ {
		h := &types.Header{Number: new(big.Int).SetUint64(uint64(i)), ParentHash: parent, Extra: []byte{byte(i)}}
		b := types.NewBlockWithHeader(h)
		blocks = append(blocks, b)
		chain[b.Hash()] = b
		parent = b.Hash()
	}
	return blocks, chain
}

// TestCanonicalBranchBack: the blocks a head event does not announce (a
// reorg's re-added blocks, a segment's blocks under its last) are listed
// oldest first between the last head seen and the new head's parent; one
// block on the last head lists nothing; the walk is bounded and stops at a
// missing block (the ninth development chain's A9 at 778 and 779, 12
// October 2026: 148 shares the chain carried expired by window on the
// node's account because the re-added blocks were never marked).
func TestCanonicalBranchBack(t *testing.T) {
	genesis := common.HexToHash("0x01")
	blocks, chain := linkedBlocks(6, genesis)
	names := func(bs []*types.Block) []uint64 {
		var out []uint64
		for _, b := range bs {
			out = append(out, b.NumberU64())
		}
		return out
	}
	equal := func(got []*types.Block, want ...uint64) bool {
		g := names(got)
		if len(g) != len(want) {
			return false
		}
		for i := range g {
			if g[i] != want[i] {
				return false
			}
		}
		return true
	}
	// One block on the last head: nothing between.
	if got := canonicalBranchBack(chain, blocks[2], blocks[1].Hash(), 64); !equal(got) {
		t.Fatalf("one block on the last head: got %v, want none", names(got))
	}
	// Head 6 after the last head 2: blocks 3, 4, 5, oldest first.
	if got := canonicalBranchBack(chain, blocks[5], blocks[1].Hash(), 64); !equal(got, 3, 4, 5) {
		t.Fatalf("head 6 on last head 2: got %v, want [3 4 5]", names(got))
	}
	// A reorg: the last head seen was a twin of 4 (not on this chain), so
	// the walk runs to the bound or the genesis: 1 to 5 from a fresh node
	// (last head the genesis), 4 and 5 under a bound of 2.
	if got := canonicalBranchBack(chain, blocks[5], genesis, 64); !equal(got, 1, 2, 3, 4, 5) {
		t.Fatalf("head 6 on the genesis: got %v, want [1 2 3 4 5]", names(got))
	}
	if got := canonicalBranchBack(chain, blocks[5], common.HexToHash("0xbeef"), 2); !equal(got, 4, 5) {
		t.Fatalf("bounded walk under an unknown last head: got %v, want [4 5]", names(got))
	}
	// A missing block ends the walk with what was read above it.
	delete(chain, blocks[2].Hash())
	if got := canonicalBranchBack(chain, blocks[5], genesis, 64); !equal(got, 4, 5) {
		t.Fatalf("missing block 3: got %v, want [4 5]", names(got))
	}
}
