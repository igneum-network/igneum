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

// The surface the protocol team's node adapter (pow/igneumhash) plugs WORK through:
// the adapter owns S, E, item derivation, F, the target and the rejection
// order; this package owns WORK and keeps the whole envelope as well for the
// differential against the reference.

// ItemFunc serves item(i) into out.
type ItemFunc func(i uint64, out *[ItemBytes]byte)

// Work is WORK(S, E) = M behind the adapter's interface.
type Work struct{}

type funcSource struct {
	p    Params
	e    [32]byte
	item ItemFunc
}

func (f funcSource) Params() Params                      { return f.p }
func (f funcSource) Epoch() uint64                       { return 0 }
func (f funcSource) EpochSeed() [32]byte                 { return f.e }
func (f funcSource) Item(i uint64, out *[ItemBytes]byte) { f.item(i, out) }

// Mix is M for the seed and epoch seed over the adapter's item function.
func (Work) Mix(seed, epochSeed [32]byte, item ItemFunc, p Params) [32]byte {
	return p.work(funcSource{p: p, e: epochSeed, item: item}, seed, nil, nil)
}

// Accesses is the dataset item reads per hash: ROUNDS (section 0).
func (Work) Accesses(p Params) uint64 { return uint64(p.Rounds) }
