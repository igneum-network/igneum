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
	"errors"
	"fmt"
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/igneum/pool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
)

// SupplyResult is the answer to igneum_supply (docs/protocol-b/DESIGN.md
// section 5, L5c): the supply accounting of the chain at a height, from the
// schedule and the headers alone.
type SupplyResult struct {
	Height     hexutil.Uint64 `json:"height"`
	Hash       common.Hash    `json:"hash"`
	Issued     *hexutil.Big   `json:"issued"`             // the supply that reached addresses through this height: fund + credited miner parts (F-98; the schedule's gross is Scheduled)
	Scheduled  *hexutil.Big   `json:"scheduled"`          // the schedule's gross subsidy through this height, credited or not
	Unissued   *hexutil.Big   `json:"unissued,omitempty"` // scheduled - issued: minted by the schedule and credited to nobody (v2's three quarters; the coordinate rule's shares still accruing)
	FundIssued *hexutil.Big   `json:"fundIssued"`         // the maintenance fund's share of the schedule
	Burned     *hexutil.Big   `json:"burned"`             // sum over blocks 1..height of gasUsed * baseFee
	Net        *hexutil.Big   `json:"net"`                // issued - burned: the supply in circulation
	// MintedShielded is the part of Issued minted as shielded coinbase
	// notes through this height (pool/coinbase.go; absent where the pool
	// is off): the ledger's minted column, read from the pool's state.
	MintedShielded *hexutil.Big `json:"mintedShielded,omitempty"`
}

// burnIndex memoises the cumulative burn per height on the canonical chain.
// Every entry carries the header hash it was computed for, so a reorganised
// height is recomputed rather than trusted.
type burnIndex struct {
	mu   sync.Mutex
	hash map[uint64]common.Hash
	cum  map[uint64]*big.Int
}

func newBurnIndex() *burnIndex {
	return &burnIndex{hash: make(map[uint64]common.Hash), cum: make(map[uint64]*big.Int)}
}

// blockBurn is what one block removes from supply: every transaction in a
// block pays the same base fee, which is credited to nobody
// (core/state_transition.go credits gasUsed * effectiveTip only).
func blockBurn(h *types.Header) *big.Int {
	if h.BaseFee == nil || h.GasUsed == 0 {
		return new(big.Int)
	}
	return new(big.Int).Mul(new(big.Int).SetUint64(h.GasUsed), h.BaseFee)
}

// cumulative returns the burn through the given header, walking back to the
// nearest cached ancestor and forward again.
func (b *burnIndex) cumulative(chain consensus.ChainHeaderReader, header *types.Header) (*big.Int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var (
		path []*types.Header
		cur  = header
		base = new(big.Int)
	)
	for {
		n := cur.Number.Uint64()
		if n == 0 {
			break
		}
		if h, ok := b.hash[n]; ok && h == cur.Hash() {
			base.Set(b.cum[n])
			break
		}
		path = append(path, cur)
		parent := chain.GetHeader(cur.ParentHash, n-1)
		if parent == nil {
			return nil, fmt.Errorf("igneum: header %d (%s) has no parent in the chain", n-1, cur.ParentHash)
		}
		cur = parent
	}
	for i := len(path) - 1; i >= 0; i-- {
		h := path[i]
		base.Add(base, blockBurn(h))
		n := h.Number.Uint64()
		b.hash[n] = h.Hash()
		b.cum[n] = new(big.Int).Set(base)
	}
	return base, nil
}

// creditIndex memoises the cumulative credited issuance per height on the
// canonical chain, as burnIndex does the burn: every entry carries the
// header hash it was computed for, so a reorganised height is recomputed.
type creditIndex struct {
	mu   sync.Mutex
	hash map[uint64]common.Hash
	cum  map[uint64]*big.Int
}

func newCreditIndex() *creditIndex {
	return &creditIndex{hash: make(map[uint64]common.Hash), cum: make(map[uint64]*big.Int)}
}

// cumulative returns the credited issuance through the header: the sum of
// per(block) over blocks 1..height, from the nearest cached ancestor.
func (c *creditIndex) cumulative(chain consensus.ChainHeaderReader, header *types.Header, per func(*types.Header) (*big.Int, error)) (*big.Int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var (
		path []*types.Header
		cur  = header
		base = new(big.Int)
	)
	for {
		n := cur.Number.Uint64()
		if n == 0 {
			break
		}
		if h, ok := c.hash[n]; ok && h == cur.Hash() {
			base.Set(c.cum[n])
			break
		}
		path = append(path, cur)
		parent := chain.GetHeader(cur.ParentHash, n-1)
		if parent == nil {
			return nil, fmt.Errorf("igneum: header %d (%s) has no parent in the chain", n-1, cur.ParentHash)
		}
		cur = parent
	}
	for i := len(path) - 1; i >= 0; i-- {
		h := path[i]
		v, err := per(h)
		if err != nil {
			return nil, err
		}
		base.Add(base, v)
		n := h.Number.Uint64()
		c.hash[n] = h.Hash()
		c.cum[n] = new(big.Int).Set(base)
	}
	return base, nil
}

// Supply answers igneum_supply: the schedule's gross issuance and fund share
// at the height, the burn summed from the headers, and the net. "latest" and
// a number are accepted; "pending", "safe" and "finalized" are not (the plan,
// page 10: no invented finality).
func (api *IgneumAPI) Supply(number rpc.BlockNumber) (*SupplyResult, error) {
	if api.chain == nil {
		return nil, errors.New("igneum: no chain behind the API")
	}
	var header *types.Header
	switch {
	case number == rpc.LatestBlockNumber:
		header = api.chain.CurrentHeader()
	case number >= 0:
		header = api.chain.GetHeaderByNumber(uint64(number))
	default:
		return nil, fmt.Errorf("igneum: block tag %s is not supported by igneum_supply", number.String())
	}
	if header == nil {
		return nil, errors.New("igneum: block not found")
	}
	sched := api.engine.config.Parts.Issuance
	if sched == nil {
		return nil, errors.New("igneum: engine has no issuance schedule")
	}
	h := header.Number.Uint64()
	burned, err := api.burns.cumulative(api.chain, header)
	if err != nil {
		return nil, err
	}
	scheduled := sched.Issued(h)
	// The credited sum needs the blocks' bodies (the payout lists); a chain
	// serving headers only answers the schedule's gross as issued and no
	// unissued figure, which the field's absence says.
	issued, unissued := scheduled, (*hexutil.Big)(nil)
	if _, ok := api.chain.(receiptChain); ok {
		credited, err := api.credits.cumulative(api.chain, header, api.blockCredited)
		if err != nil {
			return nil, err
		}
		issued, unissued = credited, (*hexutil.Big)(new(big.Int).Sub(scheduled, credited))
	}
	var minted *hexutil.Big
	if api.engine.poolOn(h) && api.chain.Config() != nil && api.chain.Config().GetChainID() != nil {
		n := rpc.BlockNumber(h)
		if st, err := api.stateAt(&n); err == nil {
			chainID := api.chain.Config().GetChainID().Uint64()
			minted = (*hexutil.Big)(pool.Open(st, pool.Address(chainID), chainID, pool.Poseidon2{}).Minted())
		}
	}
	return &SupplyResult{
		MintedShielded: minted,
		Height:         hexutil.Uint64(h),
		Hash:           header.Hash(),
		Issued:         (*hexutil.Big)(issued),
		Scheduled:      (*hexutil.Big)(scheduled),
		Unissued:       unissued,
		FundIssued:     (*hexutil.Big)(sched.FundIssued(h)),
		Burned:         (*hexutil.Big)(burned),
		Net:            (*hexutil.Big)(new(big.Int).Sub(issued, burned)),
	}, nil
}
