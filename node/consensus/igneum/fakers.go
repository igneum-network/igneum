// Copyright 2026 The Igneum Authors
// This file is part of the Igneum node.
//
// The Igneum node is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The Igneum node is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the Igneum node. If not, see <http://www.gnu.org/licenses/>.

package igneum

import (
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/difficulty"
	"github.com/ethereum/go-ethereum/consensus/igneum/issuance"
)

// The fake engines every test in the node shares (PA-09b): they replaced the
// ethash fakers one for one. Fake mode accepts every seal and seals with a
// zero nonce; the header rules, the difficulty rule and the issuance schedule
// still apply unless a constructor below says otherwise.

// TestFund is the fund address of TestParts.
var TestFund = common.HexToAddress("0x00000000000000000000000000000000000000f1")

// TestParts are the parts every fake engine runs on: B's candidate issuance
// (issuance.Candidate) and the development difficulty rule on a floor of
// 1024, which keeps a fake chain's difficulty on the floor at the target
// interval. params.IgneumTestChainConfig carries the same values in its
// engine block, so an engine built from that configuration agrees with a
// faker.
func TestParts() Parts {
	return Parts{
		Difficulty:  difficulty.Params{Target: 15, HalfLife: 3600, DtMax: 3600, Min: big.NewInt(1 << 10)},
		Issuance:    issuance.New(issuance.Candidate),
		FundAddress: TestFund,
	}
}

// NewFullFaker returns an engine that accepts every header without checking
// it, as well as every seal.
func NewFullFaker(parts Parts) *Engine {
	e, err := New(Config{FakePoW: true, FakeFull: true, Parts: parts})
	if err != nil {
		panic(err)
	}
	return e
}

// NewFakeFailer returns an engine that accepts every seal except the one at
// the given block number.
func NewFakeFailer(parts Parts, fail uint64) *Engine {
	e, err := New(Config{FakePoW: true, FakeFail: fail, Parts: parts})
	if err != nil {
		panic(err)
	}
	return e
}

// NewFakeDelayer returns an engine that accepts every seal after the given
// delay.
func NewFakeDelayer(parts Parts, delay time.Duration) *Engine {
	e, err := New(Config{FakePoW: true, FakeDelay: delay, Parts: parts})
	if err != nil {
		panic(err)
	}
	return e
}
