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
)

// TestDAGWorkIsTheUnitWhateverTheDifficulty: the step-0 finding was a lane
// whose controller had lifted its difficulty to 17.75 times its reference
// weighing twenty times a lane at 0.74 under form A; under this cut a
// block's own work is the unit for every lane at any difficulty (its
// difficulty over the lane's difficulty in force, one by the difficulty
// rule), so a lane's hash rate buys it no weight a block. A header without a
// lane keeps the chain form's difficulty as its work.
func TestDAGWorkIsTheUnitWhateverTheDifficulty(t *testing.T) {
	e := vectorEngine(t, "")
	g := genesisHeader()
	heavy := childOf(g)
	heavy.SetCoordTag(2)
	heavy.Difficulty = big.NewInt(887_744_140) // 17.75 x the step-0 reference
	light := childOf(g)
	light.SetCoordTag(0)
	light.Difficulty = big.NewInt(37_000_000) // 0.74 x
	unit := big.NewInt(dagUnit)
	if w := e.dagWork(heavy); w.Cmp(unit) != 0 {
		t.Fatalf("the heavy lane's block weighs %v, want the unit %v", w, unit)
	}
	if w := e.dagWork(light); w.Cmp(unit) != 0 {
		t.Fatalf("the light lane's block weighs %v, want the unit %v", w, unit)
	}
	plain := childOf(g)
	if w := e.dagWork(plain); w.Cmp(plain.Difficulty) != 0 {
		t.Fatalf("a block of no lane weighs %v, want its difficulty %v", w, plain.Difficulty)
	}
	// The lanes form B reads from the engine: three, every one in the
	// minimum at height 1, the slack k.
	l := dagLookup{e, nil}
	e.SetDAGFrom(1, 0)
	if l.LaneCount() != 3 || l.CreditSlack() != uint64(e.DAGK()) || e.DAGK() != 4 {
		t.Fatalf("lanes %d slack %d k %d", l.LaneCount(), l.CreditSlack(), e.DAGK())
	}
}
