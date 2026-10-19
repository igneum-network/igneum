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

package params

import "testing"

// TestDAGGateZeroOnEveryChainOfRecord: the block DAG ordering's gate is set
// on the throwaway lab chains alone (7411 step 0; 7412 and 7413 step 1; 7422
// and 7423 step 2, the lane cadence at 6 and 3 seconds); the chains of
// record (the public test network 7322, the research chain 7358, the
// development chain 7310 and every earlier lab) are at zero and carry no
// cadence.
func TestDAGGateZeroOnEveryChainOfRecord(t *testing.T) {
	labs := map[uint64]uint64{7411: 0, 7412: 0, 7413: 0, 7422: 6, 7423: 3}
	for id := uint64(7300); id <= 7499; id++ {
		from, lane := IgneumDAGFromForChainID(id), IgneumDAGLaneSecondsForChainID(id)
		if want, ok := labs[id]; ok {
			if from != 1 || lane != want {
				t.Fatalf("the lab chain %d's gate %d cadence %d, want 1 and %d", id, from, lane, want)
			}
			continue
		}
		if from != 0 || lane != 0 {
			t.Fatalf("chain %d carries a DAG gate %d / cadence %d", id, from, lane)
		}
	}
	if len(IgneumChainDAGFrom) != len(labs) {
		t.Fatalf("%d chains carry the gate, want the %d labs alone", len(IgneumChainDAGFrom), len(labs))
	}
}

// TestDAGLaneCadenceReadOnlyThroughTheGate: the cadence is a field of the
// gate's entry and is read as zero wherever the gate is zero (an entry at
// zero with a cadence would be a cadence with no ordering to read it, so no
// entry may be at zero); the chains of record named read zero and zero.
func TestDAGLaneCadenceReadOnlyThroughTheGate(t *testing.T) {
	for id, g := range IgneumChainDAGFrom {
		if g.From == 0 {
			t.Fatalf("chain %d: an entry at zero (cadence %d)", id, g.LaneSeconds)
		}
		if IgneumDAGLaneSecondsForChainID(id) != g.LaneSeconds {
			t.Fatalf("chain %d: the cadence read %d, the entry %d", id, IgneumDAGLaneSecondsForChainID(id), g.LaneSeconds)
		}
	}
	if IgneumDAGLaneSecondsForChainID(1) != 0 || IgneumDAGFromForChainID(1) != 0 {
		t.Fatalf("a chain without an entry reads a gate or a cadence")
	}
	for _, id := range []uint64{7310, 7322, 7358} {
		if IgneumDAGFromForChainID(id) != 0 || IgneumDAGLaneSecondsForChainID(id) != 0 {
			t.Fatalf("the chain of record %d carries the ordering", id)
		}
	}
}
