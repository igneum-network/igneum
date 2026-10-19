// Copyright 2016 The go-ethereum Authors
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

package params

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
	"github.com/ethereum/go-ethereum/params/types/goethereum"
)

func newUint64(val uint64) *uint64 { return &val }

// IgneumTestChainConfig is the one test configuration of the node (PA-09b):
// every EIP gate of go-ethereum's former AllEthashProtocolChanges and
// TestChainConfig, all at genesis, with no merge and no time-based fork, and
// the Igneum engine block in place of ethash. Tests build their engine with
// igneum.NewFaker(igneum.TestParts()); the engine block carries the same
// parts, so an engine built from this configuration (the null algorithm
// under --dev, or fake proof of work) agrees with the faker.
var IgneumTestChainConfig = &goethereum.ChainConfig{
	ChainID:             big.NewInt(1337),
	HomesteadBlock:      big.NewInt(0),
	EIP150Block:         big.NewInt(0),
	EIP155Block:         big.NewInt(0),
	EIP158Block:         big.NewInt(0),
	ByzantiumBlock:      big.NewInt(0),
	ConstantinopleBlock: big.NewInt(0),
	PetersburgBlock:     big.NewInt(0),
	IstanbulBlock:       big.NewInt(0),
	MuirGlacierBlock:    big.NewInt(0),
	BerlinBlock:         big.NewInt(0),
	LondonBlock:         big.NewInt(0),
	ArrowGlacierBlock:   big.NewInt(0),
	GrayGlacierBlock:    big.NewInt(0),
	Igneum:              IgneumTestEngineBlock(),
}

// IgneumTestEngineBlock is the engine block of IgneumTestChainConfig: the
// null algorithm (development only, F-02) on the parts of
// igneum.TestParts(), which this must stay equal to.
func IgneumTestEngineBlock() *ctypes.IgneumConfig {
	return &ctypes.IgneumConfig{
		Algorithm:  "null",
		Difficulty: &ctypes.IgneumDifficulty{Target: 15, HalfLife: 3600, DtMax: 3600, Min: big.NewInt(1 << 10)},
		Issuance: &ctypes.IgneumIssuance{
			Epoch:       8192,
			M:           new(big.Int).Mul(big.NewInt(100_000_000), big.NewInt(1e18)),
			K:           24,
			Tail:        big.NewInt(250_000_000_000_000_000),
			FundBps:     100,
			FundAddress: common.HexToAddress("0x00000000000000000000000000000000000000f1"),
		},
	}
}

// NetworkNames are user friendly names to use in the chain spec banner.
var NetworkNames = map[string]string{
	IgneumDevChainID.String(): "igneum-dev",
}
