// Copyright 2026 The core-geth Authors
// This file is part of the core-geth library.
//
// The core-geth library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The core-geth library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the core-geth library. If not, see <http://www.gnu.org/licenses/>.

package igneumhash

// ItemFunc derives dataset item i for the epoch in hand into out.
type ItemFunc func(i uint64, out *[ItemBytes]byte)

// Work is the resource-work function of ENVELOPE-R0A-v0.md section 6: from the
// work seed, the epoch seed and the dataset reached by index, a 32-byte state
// digest. A Work runs a fixed operation count for a parameter set, allocates
// nothing beyond the scratchpad and the lane states, and reads items through
// ItemFunc only. Accesses reports how many items one Mix reads, the constant
// the verifier contract records.
type Work interface {
	Mix(seed, epochSeed [32]byte, item ItemFunc, p Params) [32]byte
	Accesses(p Params) uint64
}
