// Copyright 2021 The go-ethereum Authors
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
	crand "crypto/rand"
	"errors"
	"fmt"
	"math/big"
	mrand "math/rand"

	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
)

// ForkChoice is the fork chooser based on the highest total difficulty of the
// chain(the fork choice used in the eth1) and the external fork choice (the fork
// choice used in the eth2). This main goal of this ForkChoice is not only for
// offering fork choice during the eth1/2 merge phase, but also keep the compatibility
// for all other proof-of-work networks.
type ForkChoice struct {
	chain consensus.ChainHeaderReader
	rand  *mrand.Rand

	// preserve is a helper function used in td fork choice.
	// Miners will prefer to choose the local mined block if the
	// local td is equal to the extern one. It can be nil for light
	// client
	preserve func(header *types.Header) bool

	// tie is the engine's deterministic tie-break at equal weight and
	// equal height (PA-47; consensus/igneum/forkchoice.go), in place of
	// the preserve-or-coin rule above when the engine provides it: the
	// preserve rule kept two sealers each on its own branch and the coin
	// is not the same on every node (security's F-41, 11 October 2026).
	tie forkTieBreaker
}

// forkTieBreaker is the engine hook: whether to move from current to
// extern, two heads of equal weight and height.
type forkTieBreaker interface {
	ForkTieBreak(chain consensus.ChainHeaderReader, current, extern *types.Header) (bool, error)
}

// forkWeigher is the engine's optional first hook (VectorWork): the fork
// choice by the engine's own weight, decided false on a chain without it.
type forkWeigher interface {
	PreferByWeight(chain consensus.ChainHeaderReader, current, extern *types.Header) (reorg, decided bool, err error)
}

// forkLengthRuler is the engine's optional second hook: at an equal-weight,
// unequal-height tie, whether the longer branch wins (F-44 part 3, by the
// chain's fork height).
type forkLengthRuler interface {
	ForkLongerWins(current, extern *types.Header) bool
}

// engineHolder is a chain reader that holds its engine (core.BlockChain).
type engineHolder interface {
	Engine() consensus.Engine
}

func NewForkChoice(chainReader consensus.ChainHeaderReader, preserve func(header *types.Header) bool) *ForkChoice {
	// Seed a fast but crypto originating random generator
	seed, err := crand.Int(crand.Reader, big.NewInt(math.MaxInt64))
	if err != nil {
		log.Crit("Failed to initialize random seed", "err", err)
	}
	f := &ForkChoice{
		chain:    chainReader,
		rand:     mrand.New(mrand.NewSource(seed.Int64())),
		preserve: preserve,
	}
	if h, ok := chainReader.(engineHolder); ok {
		if tb, ok := h.Engine().(forkTieBreaker); ok {
			f.tie = tb
		}
	}
	return f
}

func (f *ForkChoice) CommonAncestor(current *types.Header, header *types.Header) (*types.Header, error) {
	oldH, newH := types.CopyHeader(current), types.CopyHeader(header)
	var commonAncestor *types.Header

	// Reduce the longer chain to the same number as the shorter one.
	if oldH.Number.Uint64() > newH.Number.Uint64() {
		for ; oldH != nil && oldH.Number.Uint64() != newH.Number.Uint64(); oldH = f.chain.GetHeader(oldH.ParentHash, oldH.Number.Uint64()-1) {
			// noop (txes and logs aggregation not handled here)
		}
	} else {
		for ; newH != nil && newH.Number.Uint64() != oldH.Number.Uint64(); newH = f.chain.GetHeader(newH.ParentHash, newH.Number.Uint64()-1) {
			// noop
		}
	}

	// Both sides of the reorg are at the same number, reduce both until the
	// common ancestor is found.
	for {
		if oldH.Hash() == newH.Hash() {
			commonAncestor = oldH
			break
		}
		oldH = f.chain.GetHeader(oldH.ParentHash, oldH.Number.Uint64()-1)
		if oldH == nil {
			return nil, fmt.Errorf("invalid oldH chain")
		}

		newH = f.chain.GetHeader(newH.ParentHash, newH.Number.Uint64()-1)
		if newH == nil {
			return nil, fmt.Errorf("invalid newH chain")
		}
	}
	return commonAncestor, nil
}

// ReorgNeeded returns whether the reorg should be applied
// based on the given external header and local canonical chain.
// In the td mode, the new head is chosen if the corresponding
// total difficulty is higher. In the extern mode, the trusted
// header is always selected as the head.
func (f *ForkChoice) ReorgNeeded(current *types.Header, extern *types.Header) (bool, error) {
	// VectorWork's fork rule (consensus/igneum/vector.go): on a chain with
	// the vector form the engine weighs the two heads by their
	// per-coordinate cumulative work (the geometric rule's product or the
	// minimum rule's keys) with the lot tie-break at equality, and the
	// total difficulty below is not consulted; the engine says when it
	// has no such form and the order below holds.
	if vw, ok := f.tie.(forkWeigher); ok && f.tie != nil {
		if reorg, decided, err := vw.PreferByWeight(f.chain, current, extern); decided {
			return reorg, err
		}
	}
	var (
		localTD  = f.chain.GetTd(current.Hash(), current.Number.Uint64())
		externTd = f.chain.GetTd(extern.Hash(), extern.Number.Uint64())
	)
	if localTD == nil || externTd == nil {
		return false, errors.New("missing td")
	}
	// // If the total difficulty is higher than our known, add it to the canonical chain
	// if diff := externTd.Cmp(localTD); diff > 0 {
	// 	return true, nil
	// } else if diff < 0 {
	// 	return false, nil
	// }
	/*
		This is chunk was added with the following commit, citing it to be logically inoperative.
		core-geth omitted it for a later finality check it carried; Igneum has none.

			core: clarify code in forkchoice (#26257)

			refactoring without logic change
			0dc9b01c github.com/setunapo 20221128
	*/

	// Local and external difficulty is identical.
	// Second clause in the if statement reduces the vulnerability to selfish mining.
	// Please refer to http://www.cs.cornell.edu/~ie53/publications/btcProcFC.pdf
	reorg := externTd.Cmp(localTD) > 0
	tie := externTd.Cmp(localTD) == 0
	if tie {
		externNum, localNum := extern.Number.Uint64(), current.Number.Uint64()
		if externNum != localNum {
			// geth's order: the shorter head at equal weight (the
			// selfish-mining clause). Under the engine's length order
			// (F-44 part 3, by the chain's ForkLengthV1 height) the
			// branch with MORE blocks wins instead, and the lot term
			// below decides only at equal weight and equal length.
			longer := false
			if lw, ok := f.tie.(forkLengthRuler); ok && f.tie != nil {
				longer = lw.ForkLongerWins(current, extern)
			}
			if longer {
				reorg = externNum > localNum
			} else {
				reorg = externNum < localNum
			}
		} else {
			if f.tie != nil {
				// The engine's deterministic rule (PA-47): the first
				// contested height's endorsement weight, then the lower
				// seal hash; the same answer on every node.
				return f.tie.ForkTieBreak(f.chain, current, extern)
			}
			var currentPreserve, externPreserve bool
			if f.preserve != nil {
				currentPreserve, externPreserve = f.preserve(current), f.preserve(extern)
			}
			reorg = !currentPreserve && (externPreserve || f.rand.Float64() < 0.5)
		}
	}

	return reorg, nil
}
