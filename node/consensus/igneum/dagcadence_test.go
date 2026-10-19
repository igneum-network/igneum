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

import "testing"

// TestDAGKFollowsTheCadence: the paper's k from the rate the lane cadence
// implies (dag.K, Kaspa's calculate_ghostdag_k: the least k with
// P[Poisson(2 D lambda) > k] < delta at D = 2 s and delta = 0.01, lambda the
// lane count over the cadence): three lanes at 12 s (a merged block every
// 4 s) k = 4, the steps 0 and 1 figure; at 6 s (every 2 s) k = 6; at 3 s
// (every second) k = 9; a cadence of zero reads 12; a chain of no lanes
// reads the design's three.
func TestDAGKFollowsTheCadence(t *testing.T) {
	for _, c := range []struct {
		lanes int
		secs  uint64
		k     uint8
	}{{3, 12, 4}, {3, 0, 4}, {0, 0, 4}, {3, 6, 6}, {3, 3, 9}, {1, 4, 4}, {1, 2, 6}} {
		if k := DAGKFor(c.lanes, c.secs); k != c.k {
			t.Fatalf("%d lanes at %d s: k %d, want %d", c.lanes, c.secs, k, c.k)
		}
	}
}

// TestDAGLaneCadenceFromTheEntry: under a gate whose entry names a cadence
// the lane's controller target is the entry's at every height from the
// gate and the registration's before it; an entry without a cadence leaves
// the registration's everywhere; the k and the slack S follow the cadence.
func TestDAGLaneCadenceFromTheEntry(t *testing.T) {
	e := vectorEngine(t, "")
	l := dagLookup{e, nil}
	e.SetDAGFrom(10, 6)
	if got := e.dagLaneTarget(12, 9); got != 12 {
		t.Fatalf("before the landing check the target %d, want the registration's 12", got)
	}
	for _, h := range []uint64{10, 11, 1000} {
		if got := e.dagLaneTarget(12, h); got != 6 {
			t.Fatalf("at %d the target %d, want the entry's 6", h, got)
		}
	}
	if e.dagCadenceOn(9) || !e.dagCadenceOn(10) {
		t.Fatalf("the cadence in force at 9 %v, at 10 %v", e.dagCadenceOn(9), e.dagCadenceOn(10))
	}
	if e.DAGK() != 6 || l.CreditSlack() != 6 || e.DAGLaneSeconds() != 6 {
		t.Fatalf("at 6 s: k %d slack %d cadence %d, want 6 / 6 / 6", e.DAGK(), l.CreditSlack(), e.DAGLaneSeconds())
	}
	e.SetDAGFrom(1, 0)
	if got := e.dagLaneTarget(12, 5); got != 12 || e.dagCadenceOn(5) {
		t.Fatalf("no cadence: the target %d (in force %v), want the registration's 12", got, e.dagCadenceOn(5))
	}
	if e.DAGK() != 4 || l.CreditSlack() != 4 || e.DAGLaneSeconds() != 0 {
		t.Fatalf("no cadence: k %d slack %d cadence %d, want 4 / 4 / 0", e.DAGK(), l.CreditSlack(), e.DAGLaneSeconds())
	}
	e.SetDAGFrom(1, 3)
	if e.DAGK() != 9 || l.CreditSlack() != 9 || e.dagLaneTarget(12, 1) != 3 {
		t.Fatalf("at 3 s: k %d slack %d target %d, want 9 / 9 / 3", e.DAGK(), l.CreditSlack(), e.dagLaneTarget(12, 1))
	}
	// Off the gate (the chains of record): nothing read.
	off := vectorEngine(t, "")
	if off.dagCadenceOn(1) || off.dagLaneTarget(12, 1) != 12 || off.DAGLaneSeconds() != 0 {
		t.Fatalf("off the gate the cadence is read")
	}
}
