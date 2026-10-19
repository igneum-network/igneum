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

package igneum

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// TestClockV1RePinsTheConstantsAtItsHeight: below the fork height, or with
// no entry, the seal clock reads the genesis document's tPiMs and dMin;
// from the height it reads the entry's figures (a zero field keeping the
// document's), in the round target, the seal floor and the seal step's
// allowance alike.
func TestClockV1RePinsTheConstantsAtItsHeight(t *testing.T) {
	e := genesisEngine(t)
	defer e.Close()
	s := e.config.Parts.Sortition
	s.Delta, s.TMin, s.TPiMs, s.SealWindowDiv = 20, 12, 250, 4
	s.DMin = big.NewInt(5800)
	docTarget := (20000 - 12000 - 250) / uint64(4)
	if p := e.sealParamsAt(1000, false); p.Target != docTarget || p.Min.Cmp(big.NewInt(5800)) != 0 {
		t.Fatalf("no entry: target %d floor %v, want %d and 5800", p.Target, p.Min, docTarget)
	}
	e.SetRuleForks(&ctypes.IgneumRuleForks{ClockV1: 2200, ClockTPiMs: 300, ClockDMin: 90000})
	if tp, dm := e.clockConstsAt(2199); tp != 250 || dm.Cmp(big.NewInt(5800)) != 0 {
		t.Fatalf("below the height: %d %v, want the document's 250 and 5800", tp, dm)
	}
	if tp, dm := e.clockConstsAt(2200); tp != 300 || dm.Cmp(big.NewInt(90000)) != 0 {
		t.Fatalf("at the height: %d %v, want 300 and 90000", tp, dm)
	}
	newTarget := (20000 - 12000 - 300) / uint64(4)
	if p := e.sealParamsAt(2200, false); p.Target != newTarget || p.Min.Cmp(big.NewInt(90000)) != 0 {
		t.Fatalf("at the height: target %d floor %v, want %d and 90000", p.Target, p.Min, newTarget)
	}
	if p := e.sealParamsAt(2199, false); p.Target != docTarget {
		t.Fatalf("below the height the target moved to %d", p.Target)
	}
	// A zero field keeps the document's figure.
	e.SetRuleForks(&ctypes.IgneumRuleForks{ClockV1: 2200, ClockTPiMs: 300})
	if tp, dm := e.clockConstsAt(3000); tp != 300 || dm.Cmp(big.NewInt(5800)) != 0 {
		t.Fatalf("a zero dMin field: %d %v, want 300 and the document's 5800", tp, dm)
	}
	// Zero height: off throughout.
	e.SetRuleForks(&ctypes.IgneumRuleForks{ClockTPiMs: 300, ClockDMin: 90000})
	if tp, _ := e.clockConstsAt(1 << 40); tp != 250 {
		t.Fatalf("a zero ClockV1 re-pinned: %d", tp)
	}
}
