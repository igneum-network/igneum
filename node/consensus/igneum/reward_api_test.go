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
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/trie"
)

type fakeIssuer struct{}

func (fakeIssuer) Subsidy(height uint64) *big.Int { return big.NewInt(1000) }
func (fakeIssuer) Reward(height uint64) (*big.Int, *big.Int) {
	return big.NewInt(990), big.NewInt(10)
}

// TestBlockRewardComputation checks the per-block view: subsidy and split
// from the schedule, priority fees as gasUsed times the effective tip at the
// block's base fee (a dynamic-fee and a legacy transaction), burned as
// gasUsed times base fee.
func TestBlockRewardComputation(t *testing.T) {
	baseFee := big.NewInt(1_000_000_000) // 1 gwei
	to := common.HexToAddress("0x01")
	dyn := types.NewTx(&types.DynamicFeeTx{ChainID: big.NewInt(7310), Nonce: 0, GasTipCap: big.NewInt(2_000_000_000), GasFeeCap: big.NewInt(2_500_000_000), Gas: 21000, To: &to, Value: big.NewInt(1)})
	legacy := types.NewTx(&types.LegacyTx{Nonce: 1, GasPrice: big.NewInt(3_000_000_000), Gas: 21000, To: &to, Value: big.NewInt(1)})
	header := &types.Header{Number: big.NewInt(5), BaseFee: baseFee, Coinbase: common.HexToAddress("0xc0"), Difficulty: big.NewInt(1), GasLimit: 1_000_000}
	block := types.NewBlock(header, []*types.Transaction{dyn, legacy}, nil, nil, trie.NewStackTrie(nil))
	receipts := types.Receipts{{GasUsed: 21000}, {GasUsed: 30000}}

	r := blockReward(fakeIssuer{}, block, receipts)
	// dyn: tip = min(2 gwei, 2.5 gwei - 1 gwei) = 1.5 gwei over 21000 gas; legacy: 3 gwei - 1 gwei = 2 gwei over 30000 gas.
	wantFees := new(big.Int).Add(new(big.Int).Mul(big.NewInt(21000), big.NewInt(1_500_000_000)), new(big.Int).Mul(big.NewInt(30000), big.NewInt(2_000_000_000)))
	wantBurn := new(big.Int).Mul(big.NewInt(51000), baseFee)
	if (*big.Int)(r.PriorityFees).Cmp(wantFees) != 0 {
		t.Fatalf("priority fees: have %v want %v", (*big.Int)(r.PriorityFees), wantFees)
	}
	if (*big.Int)(r.Burned).Cmp(wantBurn) != 0 {
		t.Fatalf("burned: have %v want %v", (*big.Int)(r.Burned), wantBurn)
	}
	if (*big.Int)(r.Subsidy).Int64() != 1000 || (*big.Int)(r.Miner).Int64() != 990 || (*big.Int)(r.Fund).Int64() != 10 {
		t.Fatalf("subsidy split: %v %v %v", r.Subsidy, r.Miner, r.Fund)
	}
	if uint64(r.Number) != 5 || r.Coinbase != header.Coinbase || uint64(r.Transactions) != 2 {
		t.Fatalf("identity fields: %+v", r)
	}
	// An empty block pays the subsidy and nothing else.
	empty := blockReward(fakeIssuer{}, types.NewBlockWithHeader(header), nil)
	if (*big.Int)(empty.PriorityFees).Sign() != 0 || (*big.Int)(empty.Burned).Sign() != 0 {
		t.Fatalf("empty block: fees %v burned %v", empty.PriorityFees, empty.Burned)
	}
}
