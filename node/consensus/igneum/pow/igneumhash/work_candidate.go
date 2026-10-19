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

package igneumhash

import (
	candidate "github.com/ethereum/go-ethereum/consensus/igneum/igneumhash"
)

// CandidateWork is the candidate's WORK from protocol B's independent
// implementation (consensus/igneum/igneumhash, written from SPEC-R0A-v0.md
// and agreed with the reference on every published vector), behind this
// adapter's Work interface. The adapter keeps the envelope; the candidate
// package computes M. The conversion carries every field this Params type
// has; the version 1 fields (cache and parents) belong to the item
// derivation, which is the adapter's NewItems (version 0) and, for version
// 1, candidate.BuildCache and candidate.ItemIntoV1.
type CandidateWork struct{}

func toCandidate(p Params) candidate.Params {
	return candidate.Params{
		Version:      p.Version,
		DatasetBytes: p.DatasetBytes,
		ScratchBytes: p.ScratchBytes,
		Rounds:       p.Rounds,
		Lanes:        p.Lanes,
		EpochLength:  p.EpochLength,
		ItemChain:    p.ItemChain,
		CacheBytes:   p.CacheBytes,
		Parents:      p.Parents,
	}
}

// Mix is M = WORK(S, E) over the adapter's item function.
func (CandidateWork) Mix(seed, epochSeed [32]byte, item ItemFunc, p Params) [32]byte {
	return candidate.Work{}.Mix(seed, epochSeed, candidate.ItemFunc(item), toCandidate(p))
}

// Accesses is ROUNDS.
func (CandidateWork) Accesses(p Params) uint64 {
	return candidate.Work{}.Accesses(toCandidate(p))
}
