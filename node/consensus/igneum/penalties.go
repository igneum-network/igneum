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
	"math/big"
)

// The penalty and exit rules (LAUNCH-GUARDS.md 2.7; core/vm/igneumreg
// version 4) as the engine meets them:
//
//   - the deterministic half of ONE BLOCK PER KEY PER HEIGHT is the
//     registry's mark: verifySortitionGenesis refuses a block whose bond
//     key is marked at the parent's state (errKeyMarked) and drawAt gives
//     a marked key no units, so from the block after the equivocation
//     transaction the key seals nothing, at any round, and endorses
//     nothing (sortition_genesis.go);
//   - the node-local half (a second block seen under one key at one
//     height is not imported into the canonical chain and not relayed; the
//     pair is kept as evidence and handed to the registry by the first
//     node holding both) is a RELAY AND FORK-CHOICE rule, never a validity
//     rule: a node that has not seen the first block imports the second
//     as valid, and the chain's answer is the penalty transaction, not a
//     refusal. It belongs to the fetcher and the fork choice (the protocol team's
//     files) and is recorded in DESIGN.md PA-38 as such; it is not in this
//     file;
//   - MinerReward is one block's reward (the miner's share of the subsidy
//     at a height), which core.NewEVMBlockContext hands the registry as
//     the equivocation reporter's pay.

var errKeyMarked = errors.New("igneum sortition: the key is marked by an equivocation")

// MinerReward is the miner's share of the subsidy at height (the reporter's
// pay of an equivocation, core/vm/igneumreg).
func (e *Engine) MinerReward(height uint64) *big.Int {
	if e.config.Parts.Issuance == nil {
		return nil
	}
	miner, _ := e.config.Parts.Issuance.Reward(height)
	return miner
}
