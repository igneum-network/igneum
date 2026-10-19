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

	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// TestClockDeltaFromForks (0.10.49, 16 October 2026): a forks entry naming
// ClockDelta from ClockV1 re-pins the round clock's Delta from that height:
// RoundStart reads the genesis Delta below it and the entry's from it (the
// rounds' floors for r 0 to 3 at the height before the landing check and at the
// gate), the seal target's window follows, a registered tailDelta keeps
// its own step, and the hook left nil keeps Delta everywhere.
func TestClockDeltaFromForks(t *testing.T) {
	e := genesisEngine(t)
	defer e.Close()
	s := e.config.Parts.Sortition
	s.Delta, s.TailDelta, s.TailFrom = 20, 0, 0
	// Without an entry: 20-second rounds everywhere.
	for _, h := range []uint64{1, 4399, 4400, 9000} {
		if got := s.RoundStart(h, 2); got != 40 {
			t.Fatalf("no entry: RoundStart(%d, 2) = %d, want 40", h, got)
		}
	}
	e.SetRuleForks(&ctypes.IgneumRuleForks{ClockV1: 4400, ClockDelta: 13, ClockTPiMs: 1200})
	// Below the gate the genesis Delta; from it the entry's: r x Delta
	// under the single round length.
	for _, c := range []struct{ h, r, want uint64 }{
		{4399, 0, 0}, {4399, 1, 20}, {4399, 2, 40}, {4399, 3, 60},
		{4400, 0, 0}, {4400, 1, 13}, {4400, 2, 26}, {4400, 3, 39},
		{9000, 1, 13},
	} {
		if got := s.RoundStart(c.h, c.r); got != c.want {
			t.Fatalf("RoundStart(%d, %d) = %d, want %d", c.h, c.r, got, c.want)
		}
	}
	// The seal target's window follows the Delta and the tPiMs at the
	// height: the first form (Delta x 1000 - tPiMs) / div, the genesis
	// 4,690 ms below the gate and the entry's 1,200 from it (0.10.50);
	// the second form (Delta - tMin) x 1000 - tPiMs over the div reads
	// 2,200 ms at the landing check under tMin 3.
	s.SealWindowDiv, s.TPiMs = 4, 4690
	if s.DMin == nil {
		s.DMin = big.NewInt(1000)
	}
	if p := e.sealParamsAt(4399, true); p.Target != (20*1000-4690)/4 {
		t.Fatalf("the seal target below the gate %d, want %d", p.Target, (20*1000-4690)/4)
	}
	if p := e.sealParamsAt(4400, true); p.Target != (13*1000-1200)/4 {
		t.Fatalf("the seal target at the landing check %d, want %d", p.Target, (13*1000-1200)/4)
	}
	s.ConstantsAt = func(height uint64) (uint64, uint64) {
		if height >= 3184 {
			return 3, 5
		}
		return 0, 0
	}
	if p := e.sealParamsAt(4400, false); p.Target != 2200 {
		t.Fatalf("the seal target's second form at the landing check %d, want 2200", p.Target)
	}
	s.ConstantsAt = nil
	// A registered tailDelta (F-151's entry) keeps its own step on the new
	// Delta: Delta + (r - 1) x tailDelta.
	s.ConstantsAt = func(height uint64) (uint64, uint64) {
		if height >= 3184 {
			return 3, 5
		}
		return 0, 0
	}
	if got := s.RoundStart(4400, 3); got != 13+2*5 {
		t.Fatalf("RoundStart(4400, 3) with tailDelta 5 = %d, want 23", got)
	}
	if got := s.RoundStart(4399, 3); got != 20+2*5 {
		t.Fatalf("RoundStart(4399, 3) with tailDelta 5 = %d, want 30", got)
	}
	// The entry without ClockDelta keeps the genesis Delta.
	e.SetRuleForks(&ctypes.IgneumRuleForks{ClockV1: 4400, ClockTPiMs: 4690})
	if got := s.RoundStart(4400, 1); got != 20 {
		t.Fatalf("an entry without clockDelta: RoundStart(4400, 1) = %d, want 20", got)
	}
}
