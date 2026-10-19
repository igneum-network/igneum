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
	"github.com/ethereum/go-ethereum/params/types/ctypes"
	"github.com/ethereum/go-ethereum/consensus/igneum/issuance"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
)

// supplyChain is a header chain by number and hash for the supply API.
type supplyChain struct {
	*fakeChain
	canonical []*types.Header
}

func (c *supplyChain) CurrentHeader() *types.Header { return c.canonical[len(c.canonical)-1] }
func (c *supplyChain) GetHeaderByNumber(n uint64) *types.Header {
	if n < uint64(len(c.canonical)) {
		return c.canonical[n]
	}
	return nil
}

func (c *supplyChain) extend(gasUsed uint64, baseFee int64) *types.Header {
	parent := c.CurrentHeader()
	h := &types.Header{
		ParentHash: parent.Hash(),
		Number:     new(big.Int).Add(parent.Number, big.NewInt(1)),
		GasLimit:   16_000_000,
		GasUsed:    gasUsed,
		BaseFee:    big.NewInt(baseFee),
		Time:       parent.Time + 15,
		Difficulty: big.NewInt(1),
	}
	c.add(h)
	c.canonical = append(c.canonical, h)
	return h
}

func newSupplyChain() *supplyChain {
	c := &supplyChain{fakeChain: newFakeChain()}
	genesis := &types.Header{Number: big.NewInt(0), GasLimit: 16_000_000, BaseFee: big.NewInt(1_000_000_000), Difficulty: big.NewInt(1), Time: 1_800_000_000}
	c.add(genesis)
	c.canonical = []*types.Header{genesis}
	return c
}

func TestSupplyBurnAndIssuance(t *testing.T) {
	chain := newSupplyChain()
	gwei := int64(1_000_000_000)
	chain.extend(21_000, gwei)      // block 1 burns 21,000 gwei
	chain.extend(0, gwei)           // block 2 burns nothing
	chain.extend(1_000_000, 2*gwei) // block 3 burns 2,000,000 gwei

	e := newTestEngine(t)
	defer e.Close()
	sched := issuance.New(issuance.Candidate)
	e.config.Parts.Issuance = sched
	api := NewIgneumAPI(e, chain)

	res, err := api.Supply(rpc.LatestBlockNumber)
	if err != nil {
		t.Fatal(err)
	}
	wantBurn := big.NewInt(21_000*gwei + 1_000_000*2*gwei)
	if (*big.Int)(res.Burned).Cmp(wantBurn) != 0 {
		t.Fatalf("burned: got %s want %s", (*big.Int)(res.Burned), wantBurn)
	}
	if uint64(res.Height) != 3 || res.Hash != chain.CurrentHeader().Hash() {
		t.Fatalf("height/hash wrong: %d %s", res.Height, res.Hash)
	}
	if (*big.Int)(res.Issued).Cmp(sched.Issued(3)) != 0 || (*big.Int)(res.FundIssued).Cmp(sched.FundIssued(3)) != 0 {
		t.Fatal("issuance does not match the schedule")
	}
	net := new(big.Int).Sub(sched.Issued(3), wantBurn)
	if (*big.Int)(res.Net).Cmp(net) != 0 {
		t.Fatalf("net: got %s want %s", (*big.Int)(res.Net), net)
	}
	// a number resolves through the canonical chain
	res2, err := api.Supply(rpc.BlockNumber(1))
	if err != nil {
		t.Fatal(err)
	}
	if (*big.Int)(res2.Burned).Int64() != 21_000*gwei {
		t.Fatalf("block 1 burned: got %s", (*big.Int)(res2.Burned))
	}
	// the cache is keyed by hash: a different block 3 is recomputed, not trusted
	alt := &types.Header{ParentHash: chain.canonical[2].Hash(), Number: big.NewInt(3), GasLimit: 16_000_000, GasUsed: 500_000, BaseFee: big.NewInt(gwei), Time: chain.canonical[2].Time + 20, Difficulty: big.NewInt(1)}
	chain.add(alt)
	chain.canonical[3] = alt
	res3, err := api.Supply(rpc.BlockNumber(3))
	if err != nil {
		t.Fatal(err)
	}
	if (*big.Int)(res3.Burned).Int64() != 21_000*gwei+500_000*gwei {
		t.Fatalf("reorganised block 3 burned: got %s", (*big.Int)(res3.Burned))
	}
	// unsupported tags are refused, unknown heights are not found
	if _, err := api.Supply(rpc.FinalizedBlockNumber); err == nil {
		t.Fatal("finalized must be refused")
	}
	if _, err := api.Supply(rpc.BlockNumber(99)); err == nil {
		t.Fatal("unknown height must fail")
	}
	// genesis: nothing issued, nothing burned
	res0, err := api.Supply(rpc.BlockNumber(0))
	if err != nil {
		t.Fatal(err)
	}
	if (*big.Int)(res0.Issued).Sign() != 0 || (*big.Int)(res0.Burned).Sign() != 0 {
		t.Fatal("genesis must report zero")
	}
	_ = common.Hash{}
}

// TestFundAtFork pins FundV2: the stand-in below the fork, the contract
// from it, the move at the fork height alone.
func TestFundAtFork(t *testing.T) {
	e := NewFullFaker(TestParts())
	t.Cleanup(func() { e.Close() })
	if a, _, mv := e.fundAt(10); a != TestFund || mv {
		t.Fatalf("no forks: %v %v", a, mv)
	}
	c := common.HexToAddress("0x142d218ccf2d20e57cc2fdf56f2616d25c20ad60")
	e.SetRuleForks(&ctypes.IgneumRuleForks{FundV2: 100, FundV2Address: c})
	if a, _, mv := e.fundAt(99); a != TestFund || mv {
		t.Fatalf("below: %v %v", a, mv)
	}
	if a, from, mv := e.fundAt(100); a != c || from != TestFund || !mv {
		t.Fatalf("at: %v %v %v", a, from, mv)
	}
	if a, _, mv := e.fundAt(101); a != c || mv {
		t.Fatalf("above: %v %v", a, mv)
	}
}
