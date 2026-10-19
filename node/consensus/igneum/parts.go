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

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/difficulty"
	"github.com/ethereum/go-ethereum/consensus/igneum/issuance"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// Parts are protocol engineer B's rules as the chain configuration sets them
// (PB-01 difficulty, PB-02 issuance; PB-03 fees go through the configurator).
type Parts struct {
	Difficulty  difficulty.Params
	Issuance    *issuance.Table
	FundAddress common.Address
	// Authority is the miner-bound work authority prototype's rule
	// (authority.go); nil on every chain without it.
	Authority *AuthorityParams
	// Sortition is the sortition-gated rule of the same prototype
	// (sortition.go); nil on every chain without it.
	Sortition *SortitionParams
	// Program is the per-period programme's chain-side form (program.go):
	// the reveal delay behind which a period's anchor block sits. Nil when
	// the chain configuration names none, in which case a programmed
	// algorithm reads the relation's default (ProgramRevealDefault).
	Program *ProgramParams
}

// ProgramParams are the per-period programme's chain-side constants
// (ctypes.IgneumProgram).
type ProgramParams struct {
	RevealBlocks uint64
}

// PartsFor builds the difficulty and issuance parts from the engine block
// for a chain of record (PartsForChain with the lab word false).
func PartsFor(cfg *ctypes.IgneumConfig) (Parts, error) {
	return PartsForChain(cfg, false)
}

// PartsForChain builds the difficulty and issuance parts from the engine block.
// Every value is explicit in the chain configuration; a missing block is an
// error, not a default, so that what a node runs is what its genesis says.
// `lab` is the chain id's word (params.IsIgneumResearchChainID, handed in by
// the backend): a lab chain may pay its fund share to no address
// (issuance.Check, F-734); a chain of record is refused at start.
func PartsForChain(cfg *ctypes.IgneumConfig, lab bool) (Parts, error) {
	var p Parts
	if cfg == nil {
		return p, fmt.Errorf("igneum: chain configuration has no igneum engine block")
	}
	if cfg.Difficulty == nil {
		return p, fmt.Errorf("igneum: engine block has no difficulty parameters")
	}
	d := cfg.Difficulty
	if d.Target == 0 || d.HalfLife == 0 || d.DtMax == 0 || d.Min == nil || d.Min.Sign() <= 0 {
		return p, fmt.Errorf("igneum: difficulty parameters incomplete: %+v", *d)
	}
	p.Difficulty = difficulty.Params{Target: d.Target, HalfLife: d.HalfLife, DtMax: d.DtMax, Min: new(big.Int).Set(d.Min)}

	if cfg.Program != nil {
		if cfg.Program.RevealBlocks == 0 {
			return p, fmt.Errorf("igneum: program block names a reveal of zero blocks")
		}
		p.Program = &ProgramParams{RevealBlocks: cfg.Program.RevealBlocks}
	}

	if cfg.Issuance == nil {
		return p, fmt.Errorf("igneum: engine block has no issuance parameters")
	}
	i := cfg.Issuance
	ip, err := issuanceParams(i, i.FundBps)
	if err != nil {
		return p, err
	}
	ip.FundAddress = i.FundAddress
	// The height-gated re-cut's gate (issuance.Params.Next): refused at
	// start where it would cut an epoch's remainder; a fund share paid to
	// the zero address refused on a chain of record (F-734).
	if err := issuance.Check(ip, lab); err != nil {
		return p, fmt.Errorf("igneum: %v", err)
	}
	p.Issuance = issuance.New(ip)
	p.FundAddress = i.FundAddress
	a, err := AuthorityFor(cfg)
	if err != nil {
		return p, err
	}
	p.Authority = a
	so, err := SortitionFor(cfg)
	if err != nil {
		return p, err
	}
	p.Sortition = so
	return p, nil
}

// issuanceParams is the schedule's constants from a chain's issuance block,
// the next form (the height-gated re-cut) built after it; every form is
// checked whole, and a next form's fund share is the first form's (the fund
// is one across the gate).
func issuanceParams(i *ctypes.IgneumIssuance, fundBps uint64) (issuance.Params, error) {
	var p issuance.Params
	if i == nil {
		return p, errors.New("igneum: issuance parameters absent")
	}
	if i.Epoch == 0 || i.M == nil || i.M.Sign() <= 0 || i.Tail == nil || i.Tail.Sign() < 0 || i.FundBps > 10000 {
		return p, fmt.Errorf("igneum: issuance parameters incomplete: %+v", *i)
	}
	var tailPerEpoch *big.Int
	if i.EpochDiv > 0 {
		if i.TailPerEpoch == nil || i.TailPerEpoch.Sign() < 0 {
			return p, errors.New("igneum: the re-cut issuance (epochDiv) needs tailPerEpoch at or above zero")
		}
		if i.Tail.Sign() != 0 {
			return p, errors.New("igneum: the re-cut issuance (epochDiv) reads tailPerEpoch, not a per-block tail (set tail 0)")
		}
		tailPerEpoch = new(big.Int).Set(i.TailPerEpoch)
	}
	p = issuance.Params{
		Epoch:   i.Epoch,
		M:       new(big.Int).Set(i.M),
		K:       i.K,
		Tail:    new(big.Int).Set(i.Tail),
		FundBps: fundBps,
		// The re-cut schedule's form (issuance.Params.EpochDiv; 0 the
		// shift form) with its per-epoch tail.
		EpochDiv:     i.EpochDiv,
		TailPerEpoch: tailPerEpoch,
		NextFrom:     i.NextFrom,
	}
	if i.Next != nil {
		if i.NextFrom == 0 {
			return p, errors.New("igneum: the issuance names a next form without nextFrom")
		}
		if i.Next.FundBps != 0 && i.Next.FundBps != fundBps {
			return p, fmt.Errorf("igneum: the next issuance form's fund share (%d bps) differs from the first's (%d): the fund is one across the gate", i.Next.FundBps, fundBps)
		}
		next, err := issuanceParams(i.Next, fundBps)
		if err != nil {
			return p, err
		}
		p.Next = &next
	}
	return p, nil
}

// NewForChain builds the engine a chain configuration names: the algorithm
// from the registry (dev-only names need devMode, F-02) and B's parts from the
// engine block.
func NewForChain(cfg *ctypes.IgneumConfig, notify []string, noverify bool, devMode bool) (*Engine, error) {
	parts, err := PartsFor(cfg)
	if err != nil {
		return nil, err
	}
	return NewFromChainConfig(cfg, parts, notify, noverify, devMode)
}
