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

// The same-height rule of the fork choice (the review lead's ruling ed600ddc8 of
// 16 October 2026 on the GPU team's reading of the research chain's 36-second
// lanes and the network team's read of the public test network's replaced blocks):
// the fork weight is normalised to each lane's reference, so at ONE height
// a block of a lane at its floor difficulty (a quarter of its reference by
// construction) always loses the race to a lane near its reference,
// whatever arrived first; the rule paid the heavy lane for arriving late
// (the public test network's five replaced blocks won 0.7 to 12 s after the
// first; lane 25 lost 53 percent of the heights it solved) and handed the
// heaviest lane a one-block reorganisation lever. From the chain's gate
// height (the review lead's ruling b5460078c as main re-pointed it, 16 October
// 2026, read at the first contested height of a race): the measure is
// PRIMARY and a TOTAL ORDER by itself, its first key the minimum of the
// normalised work over the SERVED lanes (the served band read at the two
// branches' COMMON ANCESTOR, the review lead's ruling b9ff75f28 on the red
// team's read: a lane whose difficulty at the fork, the controller's
// figure there, is at or above twice its floor, with presence the union
// of the branches; a mark read on the branches' own blocks let the lane's
// majority holder lift it into the first key or drop it out by its own
// mining, the veto returning as a bought vote; as F-124 excludes an
// unserved lane from the breadth, a floor lane's majority holder casts no
// vote by adding, lifting or withholding its blocks; and the withholding
// rule bf15bed11: no first key while either branch is shorter than L =
// 4k blocks of its own length above the ancestor, k the served-lane
// count there, the sum deciding; past L on both a served lane absent on
// a branch that carries every other is excluded from the key for both,
// a branch lacking two or more at zero: vector.go firstKey), its second key the
// scaled sum over every lane, then the length, then the lot; FIRST-SEEN decides
// only between SINGLE blocks at equal height (two siblings: the block a
// node imported first stands), never a deeper race; no count bound (a
// one-lane burst inside the controller's half-life costs a fraction of
// one card, so any length bound re-opens the lever); nodes split at one
// height converge on the next blocks by the measure. The two-for-three
// margin at unequal heights stands as the floor lane's own weakness, its
// depth a proof cell. The lot tie-break stays for the races below the
// gate. The hazards, named for the review: a transient one-height split
// between nodes that saw different first blocks; the selfish sealer's
// classic margin under first-seen, bounded by the three sealing nodes and
// the measure; a block arriving during the import of another is counted
// second. Named for the next cut, not this one: the energy-true references
// and the product over served lanes as the balanced form.

// SetFirstSeenFrom sets the chain's gate (where the engine is built,
// ethconfig.CreateConsensusEngine, from the chain id's entry
// params.IgneumChainFirstSeenFrom, so the node's start and the command
// line's chain maker carry it alike); zero is the rule off.
func (e *Engine) SetFirstSeenFrom(from uint64) {
	e.lock.Lock()
	e.firstSeenFrom = from
	e.lock.Unlock()
}

// SetForkWeightFrom sets the chain's fork-weight gate (set beside the
// first-seen gate where the engine is built, from the chain id's entry
// params.IgneumChainForkWeightFrom); zero is the rule off, the measure on
// the registered references as before.
func (e *Engine) SetForkWeightFrom(from uint64) {
	e.lock.Lock()
	e.forkWeightFrom = from
	e.lock.Unlock()
}

// SetShareLaneFrom sets the chain's share-lane gate (F-690: from it the
// share target's difficulty is the lane's, not the clock's chain D(h));
// set beside the other gates where the engine is built, from the chain
// id's entry params.IgneumChainShareLaneFrom; zero is the rule off.
func (e *Engine) SetShareLaneFrom(from uint64) {
	e.lock.Lock()
	e.shareLaneFrom = from
	e.lock.Unlock()
}

// SetSponsoredFrom sets the chain's sponsored gate (the first height at
// which the sponsored lane's rule is in force, sponsored.go sponsoredOn);
// set beside the other gates where the engine is built, from the chain
// id's entry params.IgneumChainSponsoredFrom; zero is the rule off (F-703:
// its own table, never a field of the rule forks).
func (e *Engine) SetSponsoredFrom(from uint64) {
	e.lock.Lock()
	e.sponsoredFrom = from
	e.lock.Unlock()
}

// SponsoredFrom is the chain's sponsored gate (0: off).
func (e *Engine) SponsoredFrom() uint64 {
	e.lock.Lock()
	defer e.lock.Unlock()
	return e.sponsoredFrom
}

// ShareLaneFrom is the chain's share-lane gate (0: off).
func (e *Engine) ShareLaneFrom() uint64 {
	e.lock.Lock()
	defer e.lock.Unlock()
	return e.shareLaneFrom
}

// shareLaneOn says whether a share at height reads the lane's difficulty:
// the gate set and the height at or above it.
func (e *Engine) shareLaneOn(height uint64) bool {
	e.lock.Lock()
	from := e.shareLaneFrom
	e.lock.Unlock()
	return from != 0 && height >= from
}

// ForkWeightFrom is the chain's fork-weight gate (0: off).
func (e *Engine) ForkWeightFrom() uint64 {
	e.lock.Lock()
	defer e.lock.Unlock()
	return e.forkWeightFrom
}

// forkWeightOn says whether a race whose first contested height is height
// weighs its lanes by their running difficulties at the common ancestor:
// the gate set and the height at or above it.
func (e *Engine) forkWeightOn(height uint64) bool {
	e.lock.Lock()
	from := e.forkWeightFrom
	e.lock.Unlock()
	return from != 0 && height >= from
}

// FirstSeenFrom is the gate in force (0: none).
func (e *Engine) FirstSeenFrom() uint64 {
	e.lock.Lock()
	defer e.lock.Unlock()
	return e.firstSeenFrom
}

// firstSeenOn says whether the rule is in force for a race whose first
// contested height is height: the gate set and the height at or above it.
func (e *Engine) firstSeenOn(height uint64) bool {
	e.lock.Lock()
	from := e.firstSeenFrom
	e.lock.Unlock()
	return from != 0 && height >= from
}
