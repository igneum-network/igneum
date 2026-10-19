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

package dag

// Form B's credit (docs/money/ORDERING-STEP2-2026-10-19.md section 2) on
// DAGs built in memory: the step-0 scenario (one lane at twenty times the
// others' rate) with the lane's credited weight capped at the bound, the
// windowed bound over every 150-order window, the absent-lane branch's
// gain bounded by 2 S a lane, and the chain without lanes unchanged.

import (
	"fmt"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

const testSlack = 4

// laneStore is a memStore with lanes: form B's Lookup.
type laneStore struct {
	*memStore
	n     int
	lanes map[common.Hash]int
	in    []bool
}

func newLaneStore(n int) *laneStore {
	in := make([]bool, n)
	for i := range in {
		in[i] = true
	}
	return &laneStore{memStore: newMemStore(), n: n, lanes: map[common.Hash]int{}, in: in}
}

func (s *laneStore) LaneCount() int { return s.n }
func (s *laneStore) Lane(h common.Hash) (int, bool) {
	l, ok := s.lanes[h]
	return l, ok
}
func (s *laneStore) InMinimum(common.Hash) []bool { return s.in }
func (s *laneStore) CreditSlack() uint64          { return testSlack }

var seq int

func nextHash(lane int) common.Hash {
	seq++
	return idHash(fmt.Sprintf("L%d-%06d", lane, seq))
}

// add colours a block of the lane over the parents and stores it.
func (s *laneStore) add(t *testing.T, k uint8, lane int, parents ...common.Hash) common.Hash {
	t.Helper()
	h := nextHash(lane)
	d, err := Ghostdag(s, k, h, parents)
	if err != nil {
		t.Fatal(err)
	}
	s.data[h] = d
	s.lanes[h] = lane
	return h
}

func minOthers(cr []uint64, lane int) uint64 {
	m := ^uint64(0)
	for c, v := range cr {
		if c != lane && v < m {
			m = v
		}
	}
	return m
}

func sum(cr []uint64) uint64 {
	var t uint64
	for _, v := range cr {
		t += v
	}
	return t
}

// chipDAG is the step-0 scenario as a DAG: lane 2 sealing `ratio` blocks for
// every block of lanes 0 and 1, the two honest blocks parallel children of
// the chip lane's tip, merged by the chip lane's next block (k = 4, the
// honest anticone of size one is blue). Returns the selected chain's data
// in order (every chain block's Credits a snapshot) and the per-lane blue
// block counts.
func chipDAG(t *testing.T, ratio, rounds int) (*laneStore, []*Data, map[int]int) {
	t.Helper()
	s := newLaneStore(3)
	genesis := idHash("genesis")
	s.data[genesis] = Genesis(genesis)
	tip := genesis
	blues := map[int]int{}
	for r := 0; r < rounds; r++ {
		for i := 0; i < ratio; i++ {
			tip = s.add(t, 4, 2, tip)
			blues[2]++
		}
		h0 := s.add(t, 4, 0, tip)
		h1 := s.add(t, 4, 1, tip)
		blues[0]++
		blues[1]++
		// The parents an antichain (the engine's rule): the chip tip is in
		// both honest blocks' past.
		tip = s.add(t, 4, 2, h0, h1)
		blues[2]++
	}
	var chain []*Data
	for h := tip; ; {
		d := s.data[h]
		chain = append([]*Data{d}, chain...)
		if d.Depth == 0 {
			break
		}
		h = d.SelectedParent
	}
	return s, chain, blues
}

// TestFormBOneLaneAtTwentyTimesIsCapped: the step-0 finding (a lane back at
// twenty times the others' rate held 87 percent of the blue work under form
// A) under form B: the lane's credits never exceed the least other lane's
// plus S, its share of the credited work is within 1/3 + 2 S / (3 T), and
// every one of its blocks is still blue and ordered.
func TestFormBOneLaneAtTwentyTimesIsCapped(t *testing.T) {
	_, chain, blues := chipDAG(t, 20, 50)
	head := chain[len(chain)-1]
	for _, d := range chain[1:] {
		if d.Credits[2] > minOthers(d.Credits, 2)+testSlack {
			t.Fatalf("at %x lane 2's credits %v exceed the least other lane's plus %d", d.Hash[:4], d.Credits, testSlack)
		}
		for c := range d.Credits {
			if d.Credits[c] > minOthers(d.Credits, c)+testSlack {
				t.Fatalf("at %x lane %d's credits %v exceed the bound", d.Hash[:4], c, d.Credits)
			}
		}
	}
	total := sum(head.Credits)
	// The bound: Credits[L] <= (T + 2 S) / 3.
	if 3*head.Credits[2] > total+2*testSlack {
		t.Fatalf("lane 2 credited %d of %d: over (T + 2 S) / 3", head.Credits[2], total)
	}
	share := float64(head.Credits[2]) / float64(total)
	if share > 1.0/3+2*testSlack/(3*float64(total)) {
		t.Fatalf("lane 2's share %.4f over the bound", share)
	}
	if share < 0.3 {
		t.Fatalf("lane 2's share %.4f under a third: the bound is not the cap's floor", share)
	}
	if blues[2] < 15*blues[0] {
		t.Fatalf("the scenario: lane 2 sealed %d against %d", blues[2], blues[0])
	}
	// The blue work is the credited count at unit work plus the genesis's
	// unit (a block of no lane, credited always); the blue score counts
	// every blue, the lane's uncredited blocks among them.
	if head.BlueWork.Cmp(new(big.Int).SetUint64(total+1)) != 0 {
		t.Fatalf("blue work %v, credits %d", head.BlueWork, total)
	}
	if head.BlueScore != uint64(blues[0]+blues[1]+blues[2]) {
		t.Fatalf("blue score %d, blues %v", head.BlueScore, blues)
	}
	// Every red is none: the honest parallel blocks are inside k.
	for _, d := range chain {
		if len(d.MergesetReds) != 0 {
			t.Fatalf("a red at %x", d.Hash[:4])
		}
	}
	t.Logf("lane 2: %d blue blocks, %d credited (%.4f of %d credited units; lanes 0 and 1 %d and %d); blue score %d", blues[2], head.Credits[2], share, total, head.Credits[0], head.Credits[1], head.BlueScore)
}

// TestFormBWindowBound: over every window of W = 150 consecutive chain
// blocks any lane's credits are at most every other lane's plus 2 S (the
// record page's windowed theorem), so its share of the window's credited
// work is at most 1/3 + 4 S / (3 T_window).
func TestFormBWindowBound(t *testing.T) {
	_, chain, _ := chipDAG(t, 20, 60)
	const W = 150
	for i := 1; i+W < len(chain); i++ {
		a, b := chain[i].Credits, chain[i+W].Credits
		delta := make([]uint64, len(a))
		for c := range a {
			delta[c] = b[c] - a[c]
		}
		for c := range delta {
			if delta[c] > minOthers(delta, c)+2*testSlack {
				t.Fatalf("window %d..%d: lane %d gained %v, over the least other lane's plus 2 S", i, i+W, c, delta)
			}
		}
		if tw := sum(delta); tw > 0 && 3*delta[2] > tw+4*testSlack {
			t.Fatalf("window %d..%d: lane 2's share of %d credited units over 1/3 + 4 S / (3 T)", i, i+W, tw)
		}
	}
}

// TestFormBAbsentLaneBranchGainsAtMostTwoSlack: from a balanced chain, a
// private branch of lanes 0 and 1 alone (a two-lane chip at any rate: 600
// blocks) gains at most 2 S credits a lane, 16 units in all, while the
// honest branch with every lane present gains one unit a block: after 17
// honest blocks the honest head is preferred, whatever the private branch's
// length.
func TestFormBAbsentLaneBranchGainsAtMostTwoSlack(t *testing.T) {
	s := newLaneStore(3)
	genesis := idHash("genesis")
	s.data[genesis] = Genesis(genesis)
	tip := genesis
	for i := 0; i < 90; i++ {
		tip = s.add(t, 4, i%3, tip)
	}
	// The first block of either branch credits the fork tip itself (its
	// own credit is its child's to give), so the gains are read from there.
	private := s.add(t, 4, 0, tip)
	base := s.data[private]
	for i := 1; i < 600; i++ {
		private = s.add(t, 4, i%2, private)
	}
	pd := s.data[private]
	gain := new(big.Int).Sub(pd.BlueWork, base.BlueWork)
	if gain.Cmp(big.NewInt(2*testSlack*2)) > 0 {
		t.Fatalf("the two-lane private branch gained %v credits, over 2 S a lane", gain)
	}
	if pd.Credits[2] != base.Credits[2] {
		t.Fatalf("the absent lane's count moved: %d from %d", pd.Credits[2], base.Credits[2])
	}
	honest := tip
	for i := 0; i < 2*testSlack*2+2; i++ {
		honest = s.add(t, 4, i%3, honest)
	}
	if !less(s, private, honest) {
		t.Fatalf("the private branch of 600 blocks (blue work %v, score %d) is preferred to the honest branch of 18 (blue work %v, score %d)", pd.BlueWork, pd.BlueScore, s.data[honest].BlueWork, s.data[honest].BlueScore)
	}
	t.Logf("private 600 blocks: +%v credited after the fork tip's; honest 18 blocks: +%v", gain, new(big.Int).Sub(s.data[honest].BlueWork, base.BlueWork))
}

// TestFormBWithoutLanesIsThePapersWork: a Lookup without Lanes credits every
// blue, so the blue work is the sum of the blues' work and no Credits are
// kept (the Kaspa vectors of TestGhostdagAgainstKaspaVectors run on this
// store).
func TestFormBWithoutLanesIsThePapersWork(t *testing.T) {
	s := newMemStore()
	genesis := idHash("genesis")
	s.data[genesis] = Genesis(genesis)
	tip := genesis
	for i := 0; i < 10; i++ {
		h := idHash(fmt.Sprintf("plain-%d", i))
		d, err := Ghostdag(s, 4, h, []common.Hash{tip})
		if err != nil {
			t.Fatal(err)
		}
		s.data[h] = d
		tip = h
	}
	d := s.data[tip]
	if d.Credits != nil || d.Credited != nil || d.BlueWork.Cmp(big.NewInt(10)) != 0 || d.BlueScore != 10 {
		t.Fatalf("without lanes: credits %v credited %v work %v score %d", d.Credits, d.Credited, d.BlueWork, d.BlueScore)
	}
}

// TestFormBSecondKeyIsTheBlueScore: at equal credited work the greater blue
// score wins (form B's second key), then the hash.
func TestFormBSecondKeyIsTheBlueScore(t *testing.T) {
	s := newLaneStore(3)
	genesis := idHash("genesis")
	s.data[genesis] = Genesis(genesis)
	// Lane 0 alone from the genesis: S credits then nothing more.
	a := genesis
	for i := 0; i < testSlack; i++ {
		a = s.add(t, 4, 0, a)
	}
	b := s.add(t, 4, 0, a)
	c := s.add(t, 4, 0, b)
	if s.data[c].BlueWork.Cmp(s.data[b].BlueWork) != 0 {
		t.Fatalf("lane 0 alone still credited past S: %v then %v", s.data[b].BlueWork, s.data[c].BlueWork)
	}
	if !less(s, b, c) {
		t.Fatal("at equal credited work the greater blue score is not preferred")
	}
}

// askingStore is a laneStore that records which block InMinimum was asked
// for and refuses a hash it does not hold.
type askingStore struct {
	*laneStore
	t     *testing.T
	asked []common.Hash
}

func (s *askingStore) InMinimum(h common.Hash) []bool {
	s.asked = append(s.asked, h)
	if _, ok := s.data[h]; !ok {
		s.t.Fatalf("the minimum asked at a block the chain does not hold: %x", h[:8])
	}
	return s.in
}

// TestFormBMinimumReadAtTheSelectedParent (F-807, the panel's fifth
// reading): the credit asks for the lanes in the minimum by the SELECTED
// PARENT, a block the chain holds, never by the hash of the block being
// coloured, which is not yet in the chain while it is verified (an engine
// reading it by its own hash found no header and answered for height zero,
// then for the true height once recomputed from the stored header: two
// answers for one block wherever a lane's activation is above zero).
func TestFormBMinimumReadAtTheSelectedParent(t *testing.T) {
	s := &askingStore{laneStore: newLaneStore(3), t: t}
	genesis := idHash("genesis")
	s.data[genesis] = Genesis(genesis)
	a := s.add(t, 4, 0, genesis)
	b := s.add(t, 4, 1, genesis)
	h := nextHash(2)
	s.asked = nil
	d, err := Ghostdag(s, 4, h, []common.Hash{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.asked) != 1 || s.asked[0] != d.SelectedParent {
		t.Fatalf("the minimum asked at %v, want the selected parent %x once", s.asked, d.SelectedParent[:8])
	}
	if d.Credits[0]+d.Credits[1]+d.Credits[2] != 2 {
		t.Fatalf("the two mergeset blues not both credited: %v", d.Credits)
	}
}
