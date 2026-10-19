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

package tests

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/consensus/igneum"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/params/types/coregeth"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
	"github.com/ethereum/go-ethereum/params/types/goethereum"
)

// The upstream fixture suites (ethereum/tests, etclabscore/tests) were made
// for ethash: their blocks carry ethash's difficulty and their post-states
// ethash's block reward. With ethash gone (PA-09b) they run on the Igneum
// engine in fixture mode: the fixture's own EIP gates, unchanged, with the
// Igneum engine block; the header's difficulty accepted as given; the
// fixture's reward schedule credited in place of the issuance. Everything
// else (uncles refused under D-A4, no withdrawals, gas limit and base fee
// rules, the EVM) is the Igneum rule set, which is what the suites test.
//
// translateFixtureConfig is the one reviewed translation; its test asserts
// that every fork activation of the translated configuration equals the
// fixture's for every configuration in Forks.

// fixtureConfig is a fixture's configuration translated for the Igneum
// engine.
type fixtureConfig struct {
	// Config is the fixture's configuration with the Igneum engine block.
	Config ctypes.ChainConfigurator
	// Reward is the block reward the fixture's post-states carry: ethash's
	// schedule of 5, 3 (Byzantium, EIP-649) and 2 ether (Constantinople,
	// EIP-1234), and nothing for a block of zero difficulty (post-merge).
	Reward func(header *types.Header) *big.Int
}

// translateFixtureConfig copies the fixture's configuration, adds the Igneum
// engine block and derives the reward schedule from the fixture's gates.
// Three classes of fixture configuration are unsupported, because the rule
// they exercise does not exist on Igneum: a merge (a terminal total
// difficulty; Igneum never merges, DESIGN.md 4.6), the DAO fork (EIP-779;
// the mutation left with PA-09b-i) and withdrawals (EIP-4895; DESIGN.md
// 6.3). The block suite skips those by fork name; the state suite, which
// runs no engine, still covers their EVM.
func translateFixtureConfig(name string, c ctypes.ChainConfigurator) (*fixtureConfig, error) {
	switch {
	case c.GetEthashTerminalTotalDifficulty() != nil,
		c.GetEthashEIP779Transition() != nil,
		c.GetEIP4895TransitionTime() != nil || c.GetEIP4895Transition() != nil:
		return nil, UnsupportedForkError{name}
	}
	var out ctypes.ChainConfigurator
	switch cfg := c.(type) {
	case *goethereum.ChainConfig:
		cp := *cfg
		cp.Igneum = params.IgneumTestEngineBlock()
		out = &cp
	case *coregeth.CoreGethChainConfig:
		cp := *cfg
		cp.Igneum = params.IgneumTestEngineBlock()
		out = &cp
	default:
		return nil, fmt.Errorf("fixture %s: configuration type %T has no translation", name, c)
	}
	var (
		eip649  = out.GetEthashEIP649Transition()
		eip1234 = out.GetEthashEIP1234Transition()
		five    = new(big.Int).Mul(big.NewInt(5), big.NewInt(1e18))
		three   = new(big.Int).Mul(big.NewInt(3), big.NewInt(1e18))
		two     = new(big.Int).Mul(big.NewInt(2), big.NewInt(1e18))
	)
	reward := func(header *types.Header) *big.Int {
		if header.Difficulty == nil || header.Difficulty.Sign() == 0 {
			return new(big.Int)
		}
		n := header.Number.Uint64()
		switch {
		case eip1234 != nil && *eip1234 <= n:
			return two
		case eip649 != nil && *eip649 <= n:
			return three
		default:
			return five
		}
	}
	return &fixtureConfig{Config: out, Reward: reward}, nil
}

// Engine builds the Igneum engine in fixture mode for this configuration.
func (f *fixtureConfig) Engine() *igneum.Engine {
	e, err := igneum.New(igneum.Config{
		FakePoW:           true,
		FixtureDifficulty: true,
		FixtureReward:     f.Reward,
		Parts:             igneum.TestParts(),
	})
	if err != nil {
		panic(err)
	}
	return e
}
