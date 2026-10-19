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

// Package dag is Kaspa's published block ordering (PHANTOM/GHOSTDAG,
// Sompolinsky, Wyborski, Zohar, ePrint 2018/104) in the form the
// implementation of record states it (rusty-kaspa,
// consensus/src/processes/ghostdag/{protocol,mergeset,ordering}.rs, read on
// 19 October 2026), as the ordering ladder's first step
// (docs/money/ORDERING-DESIGN-2026-10-18.md section 7, step 0; the record
// docs/money/ORDERING-STEP1-2026-10-19.md). Nothing here reads the chain: a
// Lookup gives each known block's Data and its own work, and Ghostdag
// returns the new block's Data from its parents alone, so every node
// computes the same colouring from the same DAG.
//
// The rule, as published and as implemented: the selected parent is the
// parent with the greatest blue work (ties by the greater hash, Kaspa's
// form; this step keeps it, the sealer's lot being the sortition rule's and
// the lab chain running none); the mergeset is the new block's past minus
// the selected parent's past, walked in ascending (blue work, hash) order; a
// candidate is blue when its anticone within the new block's blue set holds
// at most k blocks and no blue block's blue anticone would exceed k by its
// addition, else red; the blue score is the selected parent's plus the
// mergeset blues (the selected parent among them); the blue work is the
// selected parent's plus the mergeset blues' own work. The ordering of
// blocks is the selected chain with each block's mergeset in (blue work,
// hash) order after its selected parent (ordering.rs).
//
// The ladder's step 1 (docs/money/ORDERING-STEP2-2026-10-19.md) carries the
// design's FORM B, the minimum over the lanes, into the blue work as a
// per-order CREDIT, so that the measure stays one additive scalar and the
// colouring, the selected parent and the mergeset order above read it
// unchanged. When the Lookup also implements Lanes, a blue block of lane L
// coloured at its ordered position is CREDITED (its own work added to the
// blue work) only while the lane's credited count in the blue past is under
// the least credited count of every other lane in the minimum plus the
// slack S, else it is blue, ordered and paid but adds nothing; the credits
// per lane are carried in Data.Credits along the selected chain. The
// invariant every valid block satisfies, Credits[L] <= min over the other
// lanes of Credits[c] + S, is the bound the record page proves from (section
// 2 of the page): no lane holds more than a third of the credited work plus
// 2 S / 3, and a branch on which one lane of the minimum is absent gains at
// most 2 S credits a present lane whatever its hash rate. A Lookup without
// Lanes (the Kaspa vectors, a chain without the vector form) credits every
// blue block, and the measure is the papers' blue work exactly. The order
// among equal credited work is the blue score (the additive count, the
// design's second key under form B), then the hash.
package dag

import (
	"bytes"
	"errors"
	"math"
	"math/big"
	"sort"

	"github.com/ethereum/go-ethereum/common"
)

var (
	ErrUnknownParent = errors.New("igneum dag: a parent is unknown")
	ErrNoParents     = errors.New("igneum dag: a block needs at least one parent")
	errNotBlue       = errors.New("igneum dag: a block is not in the blue set of the given context")
)

// Data is a block's GHOSTDAG data: its colouring of its own mergeset and
// the two cumulative numbers every rule reads.
type Data struct {
	Hash           common.Hash
	Parents        []common.Hash // every parent, the selected parent among them
	SelectedParent common.Hash
	// MergesetBlues is the blue part of the mergeset in the order the
	// candidates were coloured, the selected parent FIRST (Kaspa's form:
	// the selected parent is a mergeset blue of its child).
	MergesetBlues []common.Hash
	MergesetReds  []common.Hash
	// BluesAnticoneSizes holds, for the mergeset blues and for the blues
	// of the past whose blue anticone this block's blues enlarged, the
	// size of their anticone within this block's blue set.
	BluesAnticoneSizes map[common.Hash]uint8
	// BlueScore is the number of blue blocks in the block's past; the
	// design's ORDER of a selected-chain block (section 3).
	BlueScore uint64
	// BlueWork is the sum of the CREDITED blue past's own work: under form B
	// (a Lookup with Lanes) the credited blues' work, else every blue's
	// (the papers' additive blue work).
	BlueWork *big.Int
	// Credits is, per lane, the number of credited blue blocks in the
	// block's blue past (form B; nil without Lanes). Credited marks, for
	// each MergesetBlues entry, whether it was credited at this block.
	Credits  []uint64
	Credited []bool
	// Depth is one more than the greatest parent's depth (the genesis at
	// zero): the bound the reachability walk prunes by, since a block's
	// past holds only blocks of lower depth.
	Depth uint64
}

// Lanes is form B's reading of the lanes beside a Lookup: the lane count,
// a block's lane (ok false for a block of no lane, the genesis, which is
// credited always), the lanes in the minimum at a block being coloured
// (the lanes sealable at its height: a lane not yet registered or no
// longer sealable there is out of the minimum and its blocks, which cannot
// exist there, out of the credit), and the slack S. InMinimum is asked for
// the block's SELECTED PARENT, a block the chain holds, and answers for a
// child of it (the lanes sealable at the parent's height plus one): the
// block being coloured is not yet in the chain while it is verified, so a
// reading by its own hash would find no header and answer for height zero
// there, then for its true height once recomputed from the stored header
// (F-807, the panel's fifth reading).
type Lanes interface {
	LaneCount() int
	Lane(hash common.Hash) (int, bool)
	InMinimum(selectedParent common.Hash) []bool
	CreditSlack() uint64
}

// Accepted is one transaction's fate in the ordering at a block: carried by
// Block (the block itself or a block of its mergeset), accepted when it
// executed at its ordered position, else the reason it did not (a nonce
// already used, a balance gone, a fee cap under the base fee: not
// accepted, the block valid).
type Accepted struct {
	Tx       common.Hash `json:"tx"`
	Block    common.Hash `json:"block"`
	Accepted bool        `json:"accepted"`
	Reason   string      `json:"reason,omitempty"`
}

// Lookup is what the ordering reads: the Data of a known block (nil for an
// unknown one) and a block's own work.
type Lookup interface {
	Data(hash common.Hash) *Data
	Work(hash common.Hash) *big.Int
}

// Genesis is the genesis block's Data: no parents, blue score and blue work
// zero, depth zero.
func Genesis(hash common.Hash) *Data {
	return &Data{Hash: hash, BluesAnticoneSizes: map[common.Hash]uint8{}, BlueWork: new(big.Int)}
}

// K is Kaspa's calculate_ghostdag_k (consensus/core/src/config/params.rs,
// APPROXIMATE as read on the file): the least k such that the probability
// of more than k blocks within x = 2 D lambda (Poisson) is below delta.
func K(x, delta float64) uint8 {
	if x <= 0 || delta <= 0 || delta >= 1 {
		panic("igneum dag: K needs x > 0 and 0 < delta < 1")
	}
	var (
		k        uint64
		sigma    float64
		fraction = 1.0
		exp      = math.Exp(-x)
	)
	for {
		sigma += exp * fraction
		if 1.0-sigma < delta {
			if k > 255 {
				return 255
			}
			return uint8(k)
		}
		k++
		fraction *= x / float64(k)
	}
}

// less is the consensus order of two blocks: by blue work, then by blue
// score (form B's second key, the additive count; equal to the blue work
// wherever every blue is credited at unit work, so the papers' order is
// unchanged there), then by hash (bytes ascending, rusty-kaspa's derived
// Ord on the 32 bytes).
func less(l Lookup, a, b common.Hash) bool {
	da, db := l.Data(a), l.Data(b)
	if c := da.BlueWork.Cmp(db.BlueWork); c != 0 {
		return c < 0
	}
	if da.BlueScore != db.BlueScore {
		return da.BlueScore < db.BlueScore
	}
	return bytes.Compare(a[:], b[:]) < 0
}

// Less is the consensus order of two known blocks, exported for the fork
// choice (the head is the tip a virtual block would select).
func Less(l Lookup, a, b common.Hash) bool { return less(l, a, b) }

// SelectedParent is the parent with the greatest blue work, ties by the
// greater hash (find_selected_parent).
func SelectedParent(l Lookup, parents []common.Hash) (common.Hash, error) {
	if len(parents) == 0 {
		return common.Hash{}, ErrNoParents
	}
	best := parents[0]
	for _, p := range parents {
		if l.Data(p) == nil {
			return common.Hash{}, ErrUnknownParent
		}
	}
	for _, p := range parents[1:] {
		if less(l, best, p) {
			best = p
		}
	}
	return best, nil
}

// IsAncestor reports whether a is in the past of b, or a == b (Kaspa's
// is_dag_ancestor_of, inclusive). A walk back from b over parents, pruned
// at a's depth; the lab form (Kaspa keeps an interval reachability tree;
// the walk is bounded here by the thin DAG's anticones, a cost the record
// page states).
func IsAncestor(l Lookup, a, b common.Hash) bool {
	if a == b {
		return true
	}
	da, db := l.Data(a), l.Data(b)
	if da == nil || db == nil || da.Depth >= db.Depth {
		return false
	}
	visited := map[common.Hash]struct{}{b: {}}
	stack := []common.Hash{b}
	for len(stack) > 0 {
		x := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, p := range l.Data(x).Parents {
			if p == a {
				return true
			}
			if _, seen := visited[p]; seen {
				continue
			}
			visited[p] = struct{}{}
			if dp := l.Data(p); dp != nil && dp.Depth > da.Depth {
				stack = append(stack, p)
			}
		}
	}
	return false
}

// Mergeset is the new block's past minus the selected parent's past, without
// the selected parent, in ascending (blue work, hash) order (mergeset.rs
// unordered_mergeset_without_selected_parent, then sort_blocks).
func Mergeset(l Lookup, selectedParent common.Hash, parents []common.Hash) ([]common.Hash, error) {
	var queue []common.Hash
	mergeset := map[common.Hash]struct{}{}
	for _, p := range parents {
		if p != selectedParent {
			if _, dup := mergeset[p]; dup {
				continue
			}
			queue = append(queue, p)
			mergeset[p] = struct{}{}
		}
	}
	past := map[common.Hash]struct{}{}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		d := l.Data(current)
		if d == nil {
			return nil, ErrUnknownParent
		}
		for _, p := range d.Parents {
			if _, ok := mergeset[p]; ok {
				continue
			}
			if _, ok := past[p]; ok {
				continue
			}
			if IsAncestor(l, p, selectedParent) {
				past[p] = struct{}{}
				continue
			}
			mergeset[p] = struct{}{}
			queue = append(queue, p)
		}
	}
	out := make([]common.Hash, 0, len(mergeset))
	for h := range mergeset {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return less(l, out[i], out[j]) })
	return out, nil
}

// blueAnticoneSize is block's blue anticone size from the worldview of ctx
// (block must be in ctx's blue set): ctx's own map, then the selected
// chain's maps back until one names it.
func blueAnticoneSize(l Lookup, block common.Hash, ctx *Data) (uint8, error) {
	sizes, sp := ctx.BluesAnticoneSizes, ctx.SelectedParent
	for {
		if s, ok := sizes[block]; ok {
			return s, nil
		}
		d := l.Data(sp)
		if d == nil || d.Depth == 0 {
			return 0, errNotBlue
		}
		sizes, sp = d.BluesAnticoneSizes, d.SelectedParent
	}
}

// checkBlueCandidate colours one mergeset candidate against the new block's
// data so far (check_blue_candidate and check_blue_candidate_with_chain_block).
func checkBlueCandidate(l Lookup, nd *Data, candidate common.Hash, k uint8) (blue bool, size uint8, sizes map[common.Hash]uint8, err error) {
	// The mergeset blues can be k+1 long, the selected parent among them.
	if len(nd.MergesetBlues) == int(k)+1 {
		return false, 0, nil, nil
	}
	sizes = map[common.Hash]uint8{}
	var (
		chainHash common.Hash // zero: the new block itself
		chainData = nd
		isNew     = true
	)
	for {
		// A chain block in the candidate's past: every remaining blue
		// is in that block's past and so in the candidate's; the
		// candidate's blue anticone cannot exceed k. (The genesis ends
		// the walk this way, being in every block's past.)
		if !isNew && IsAncestor(l, chainHash, candidate) {
			return true, size, sizes, nil
		}
		for _, peer := range chainData.MergesetBlues {
			if IsAncestor(l, peer, candidate) {
				continue
			}
			ps, err := blueAnticoneSize(l, peer, nd)
			if err != nil {
				return false, 0, nil, err
			}
			sizes[peer] = ps
			size++
			if size > k {
				return false, 0, nil, nil
			}
			if ps == k {
				return false, 0, nil, nil
			}
		}
		chainHash, isNew = chainData.SelectedParent, false
		if chainData = l.Data(chainHash); chainData == nil {
			return false, 0, nil, ErrUnknownParent
		}
	}
}

// Ghostdag computes the new block's Data from its parents (ghostdag()).
// The block's own work is not part of its data (its child counts it).
func Ghostdag(l Lookup, k uint8, hash common.Hash, parents []common.Hash) (*Data, error) {
	sp, err := SelectedParent(l, parents)
	if err != nil {
		return nil, err
	}
	nd := &Data{
		Hash:               hash,
		Parents:            append([]common.Hash(nil), parents...),
		SelectedParent:     sp,
		MergesetBlues:      []common.Hash{sp},
		BluesAnticoneSizes: map[common.Hash]uint8{sp: 0},
	}
	ordered, err := Mergeset(l, sp, parents)
	if err != nil {
		return nil, err
	}
	for _, candidate := range ordered {
		blue, size, sizes, err := checkBlueCandidate(l, nd, candidate, k)
		if err != nil {
			return nil, err
		}
		if blue {
			nd.MergesetBlues = append(nd.MergesetBlues, candidate)
			nd.BluesAnticoneSizes[candidate] = size
			for peer, s := range sizes {
				nd.BluesAnticoneSizes[peer] = s + 1
			}
		} else {
			nd.MergesetReds = append(nd.MergesetReds, candidate)
		}
	}
	spd := l.Data(sp)
	nd.BlueScore = spd.BlueScore + uint64(len(nd.MergesetBlues))
	nd.BlueWork = new(big.Int).Set(spd.BlueWork)
	if ln, ok := l.(Lanes); ok && ln.LaneCount() > 0 {
		credit(ln, l, nd, spd)
	} else {
		for _, b := range nd.MergesetBlues {
			nd.BlueWork.Add(nd.BlueWork, l.Work(b))
		}
	}
	for _, p := range parents {
		if d := l.Data(p).Depth + 1; d > nd.Depth {
			nd.Depth = d
		}
	}
	return nd, nil
}

// credit is form B's rule at one block: the mergeset blues in their
// colouring order (the selected parent first), each credited when its lane's
// count so far is under the least count of the other lanes in the minimum
// plus the slack; the counts start from the selected parent's. A block of
// no lane (the genesis) is credited. With no other lane in the minimum
// there is nothing to balance against and the block is credited (the
// chain's degraded state, stated on the record page).
func credit(ln Lanes, l Lookup, nd, spd *Data) {
	n := ln.LaneCount()
	cr := make([]uint64, n)
	copy(cr, spd.Credits)
	in := ln.InMinimum(nd.SelectedParent)
	slack := ln.CreditSlack()
	nd.Credited = make([]bool, len(nd.MergesetBlues))
	for i, b := range nd.MergesetBlues {
		lane, ok := ln.Lane(b)
		if !ok || lane < 0 || lane >= n {
			nd.Credited[i] = true
			nd.BlueWork.Add(nd.BlueWork, l.Work(b))
			continue
		}
		least, others := ^uint64(0), false
		for c := 0; c < n; c++ {
			if c == lane || c >= len(in) || !in[c] {
				continue
			}
			others = true
			if cr[c] < least {
				least = cr[c]
			}
		}
		if others && cr[lane] >= least+slack {
			continue // blue, ordered and paid; uncredited
		}
		cr[lane]++
		nd.Credited[i] = true
		nd.BlueWork.Add(nd.BlueWork, l.Work(b))
	}
	nd.Credits = cr
}

// OrderedMergeset is the block's whole mergeset in the ordering's order: the
// selected parent first, then the other blues and the reds together in
// ascending (blue work, hash) order (ordering.rs; Kaspa's
// consensus_ordered_mergeset). The transactions of a block are applied in
// this order after its selected parent's state.
func OrderedMergeset(l Lookup, d *Data) []common.Hash {
	rest := make([]common.Hash, 0, len(d.MergesetBlues)+len(d.MergesetReds))
	rest = append(rest, d.MergesetBlues[1:]...)
	rest = append(rest, d.MergesetReds...)
	sort.Slice(rest, func(i, j int) bool { return less(l, rest[i], rest[j]) })
	return append([]common.Hash{d.SelectedParent}, rest...)
}

// IsBlue reports whether block is blue from the worldview of ctx: in the
// mergeset blues of ctx or of a selected-chain ancestor of ctx (walked back
// until a chain block is in block's past, when block can no longer be in a
// later mergeset).
func IsBlue(l Lookup, ctx *Data, block common.Hash) bool {
	d := ctx
	for d != nil {
		for _, b := range d.MergesetBlues {
			if b == block {
				return true
			}
		}
		if d.Depth == 0 {
			return block == d.Hash
		}
		if !IsAncestor(l, block, d.SelectedParent) {
			// Not in the selected parent's past: no earlier chain block
			// merged it, and this one did not merge it blue.
			return false
		}
		d = l.Data(d.SelectedParent)
	}
	return false
}
