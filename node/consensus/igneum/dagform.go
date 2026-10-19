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

// The block DAG form (docs/money/ORDERING-DESIGN-2026-10-18.md; the ladder's
// step 0, docs/money/ORDERING-STEP1-2026-10-19.md), under the chain id's gate
// params.IgneumChainDAGFrom (ZERO IS OFF: every chain of record is at zero,
// and a header carrying merged parents is refused there, so the live chains
// are byte-identical). Under the landing check a block names its selected parent in
// ParentHash as every block does and its MERGED parents in the header's
// Parents field; the engine colours each block by consensus/igneum/dag
// (Kaspa's GHOSTDAG at the k the design's rate implies), keeps the result
// by hash, chooses the head by blue work (from the ladder's step 1,
// docs/money/ORDERING-STEP2-2026-10-19.md: FORM B, a block's own work the
// UNIT, its difficulty over its lane's difficulty in force at it, which the
// difficulty rule makes one, and the blue work the sum over the CREDITED
// blues, a lane credited only while its count is within the slack S = k of
// the least other lane's: dag.Lanes, dagLookup), hands the ordered mergeset to the state
// processor and the miner (core.ApplyMergeset), pays the subsidy of a blue
// block at its ORDER through the block that merges it (the design's
// section 3, the issuance row), and counts a lane's merged blue blocks in
// the lane's controller (vector.go), so the lane's difficulty follows its
// blue rate. The merge-depth bound (DAGMergeDepth) refuses a block that
// merges below its root. The rest of the consensus rules read the height
// as before: the record page lists them as untouched and unexercised by
// this step.

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/igneum/dag"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"
)

var (
	errDAGOff            = errors.New("igneum dag: merged parents on a chain without the ordering (the gate is zero)")
	errDAGParents        = errors.New("igneum dag: the merged parents are malformed")
	errDAGSelectedParent = errors.New("igneum dag: the header's parent is not the selected parent of its parents (the greatest blue work)")
	errDAGAntichain      = errors.New("igneum dag: a parent is in another parent's past")
	errDAGMergeDepth     = errors.New("igneum dag: a merged block is below the merge-depth root")
	// errDAGUnknownParent wraps consensus.ErrUnknownAncestor: the fetcher
	// treats it as a parent to ask for, never as a fault of the peer.
	errDAGUnknownParent = fmt.Errorf("igneum dag: a merged parent is unknown: %w", consensus.ErrUnknownAncestor)
)

const (
	// DAGMaxParents is Kaspa's bound on a block's parents, the selected
	// parent among them (consensus/core/src/config/params.rs, max_block_parents 16 at 10 bps).
	DAGMaxParents = 16
	// DAGMergeDepth is the merge-depth bound in blue score: the design's
	// 10 minutes at one block every 4 seconds (section 2.2 item 4). A block
	// whose mergeset holds a block not in the future of its selected chain's
	// block DAGMergeDepth blue blocks back is refused, so no block any node
	// accepts can reorder the chain below that root by merging.
	DAGMergeDepth = 150
	// dagDelayBound and dagDelta are the k's registration constants: the
	// delay bound D of 2 seconds (the design's TARGET for the geographic
	// row) and delta 0.01 (Kaspa's GHOSTDAG_TAIL_DELTA). The rate lambda is
	// the lane count over the lane cadence (DAGKFor; the ladder's step 2,
	// docs/money/ORDERING-STEP3-2026-10-19.md): one block every 4 seconds
	// at the registration's 12 s a lane, 2 D lambda = 1.
	dagDelayBound = 2.0
	dagDelta      = 0.01
	dagCacheBound = 1 << 16
	// DAGLaneSecondsDefault is the lane cadence where the gate entry names
	// none (params.DAGGate.LaneSeconds zero): the registration's 12 s a
	// lane, the steps 0 and 1 labs' figure.
	DAGLaneSecondsDefault = 12
	// dagDesignLanes is the lane count a chain of no lanes reads for its k:
	// the design's three (a merged block every 4 s at 12 s a lane).
	dagDesignLanes = 3
	// dagUnit is a block's own work under the vector form: its difficulty
	// over its lane's difficulty in force at the block, scaled by 2^32, and
	// the difficulty rule (verifyVectorHeader) makes the quotient one.
	dagUnit = 1 << 32
)

// DAGKFor is the k the paper's formula gives at a lane cadence (Kaspa's
// calculate_ghostdag_k, dag.K): lambda = lanes / laneSeconds blocks a
// second over the merged DAG, x = 2 D lambda, k the least with
// P[Poisson(x) > k] < delta. Three lanes at 12 s: x = 1, k = 4 (the steps
// 0 and 1 figure); at 6 s: x = 2, k = 6; at 3 s: x = 4, k = 9. A cadence
// of zero reads DAGLaneSecondsDefault; a lane count of zero (a chain of no
// lanes) reads the design's three.
func DAGKFor(lanes int, laneSeconds uint64) uint8 {
	if lanes <= 0 {
		lanes = dagDesignLanes
	}
	if laneSeconds == 0 {
		laneSeconds = DAGLaneSecondsDefault
	}
	return dag.K(2*dagDelayBound*float64(lanes)/float64(laneSeconds), dagDelta)
}

// DAGK is the k in force on this engine (set by SetDAGFrom from the landing check
// entry's cadence and the registration's lane count).
func (e *Engine) DAGK() uint8 {
	e.dag.mu.Lock()
	defer e.dag.mu.Unlock()
	return e.dag.k
}

// DAGLaneSeconds is the gate entry's lane cadence (zero: the registration's).
func (e *Engine) DAGLaneSeconds() uint64 {
	e.dag.mu.Lock()
	defer e.dag.mu.Unlock()
	return e.dag.laneSeconds
}

// dagLaneTarget is a lane's controller target at a height: the gate
// entry's cadence where the ordering is in force at the height and the
// entry names one, else the registration's target. The controller's lag is
// the folded one from the cadence on (vectorDifficulty), so a cadence from
// a later gate height changes the slope and not the difficulty at the landing check.
func (e *Engine) dagLaneTarget(registered, height uint64) uint64 {
	if e.dagCadenceOn(height) {
		return e.DAGLaneSeconds()
	}
	return registered
}

// dagCadenceOn reports whether the entry's cadence is in force at the height.
func (e *Engine) dagCadenceOn(height uint64) bool {
	e.dag.mu.Lock()
	defer e.dag.mu.Unlock()
	return e.dag.from != 0 && height >= e.dag.from && e.dag.laneSeconds != 0
}

// DAGReorg is one change of the selected chain's head to a block that was
// not the old head's child: the old head, the new head and the depth
// replaced (the blocks of the old selected chain above the common block).
type DAGReorg struct {
	At      int64       `json:"at"`
	OldHead common.Hash `json:"oldHead"`
	NewHead common.Hash `json:"newHead"`
	Depth   uint64      `json:"depth"`
	Added   uint64      `json:"added"`
}

type dagState struct {
	mu   sync.Mutex
	from uint64
	k    uint8
	// laneSeconds is the gate entry's lane cadence (zero: the registration's).
	laneSeconds uint64
	cache       map[common.Hash]*dag.Data
	// tips are the blocks this node holds that no block it holds names as a
	// parent: the template's merged parents beside the head.
	tips map[common.Hash]struct{}
	// missing are merged parents a verification found unknown, by the time
	// they were first missed (the handler's gap filler asks for them).
	missing map[common.Hash]time.Time
	// arrival is the local wall clock (milliseconds) at which a block was
	// written: the propagation delay's reading across nodes.
	arrival map[common.Hash]int64
	// carried is each transaction's carrying blocks; accepted is each
	// block's acceptance record (its own transactions and its mergeset's).
	carried  map[common.Hash][]common.Hash
	accepted map[common.Hash][]dag.Accepted
	reorgs   []DAGReorg
}

// SetDAGFrom sets the chain's gate and lane cadence (the backend, from
// params.IgneumChainDAGFrom's entry); the k follows the cadence and the
// registration's lane count (DAGKFor), and the slack S is that k.
func (e *Engine) SetDAGFrom(from, laneSeconds uint64) {
	lanes := 0
	if m := e.vectorMenu(); m != nil {
		lanes = m.Count()
	}
	e.dag.mu.Lock()
	defer e.dag.mu.Unlock()
	e.dag.from = from
	e.dag.laneSeconds = laneSeconds
	e.dag.k = DAGKFor(lanes, laneSeconds)
	if e.dag.cache == nil {
		e.dag.cache = make(map[common.Hash]*dag.Data)
		e.dag.tips = make(map[common.Hash]struct{})
		e.dag.missing = make(map[common.Hash]time.Time)
		e.dag.arrival = make(map[common.Hash]int64)
		e.dag.carried = make(map[common.Hash][]common.Hash)
		e.dag.accepted = make(map[common.Hash][]dag.Accepted)
	}
}

// DAGOn reports whether a block at the height is under the ordering.
func (e *Engine) DAGOn(number uint64) bool {
	e.dag.mu.Lock()
	defer e.dag.mu.Unlock()
	return e.dag.from != 0 && number >= e.dag.from
}

// DAGFrom is the gate (zero off).
func (e *Engine) DAGFrom() uint64 {
	e.dag.mu.Lock()
	defer e.dag.mu.Unlock()
	return e.dag.from
}

// dagLookup reads the chain for the ordering: Data by hash from the cache or
// recomputed from the headers; a block's own work (the unit under the
// vector form); and form B's lanes (dag.Lanes): the registered count, a
// header's coordinate tag, the lanes sealable at a block's height, the
// slack.
type dagLookup struct {
	e     *Engine
	chain consensus.ChainHeaderReader
}

func (l dagLookup) Data(h common.Hash) *dag.Data {
	d, _ := l.e.dagDataOf(l.chain, h)
	return d
}

func (l dagLookup) Work(h common.Hash) *big.Int {
	header := l.chain.GetHeaderByHash(h)
	if header == nil {
		return new(big.Int)
	}
	return l.e.dagWork(header)
}

func (l dagLookup) LaneCount() int {
	m := l.e.vectorMenu()
	if m == nil {
		return 0
	}
	return m.Count()
}

func (l dagLookup) Lane(h common.Hash) (int, bool) {
	header := l.chain.GetHeaderByHash(h)
	if header == nil {
		return 0, false
	}
	c, ok := l.e.coordOf(header)
	return int(c), ok
}

// InMinimum is the lanes in form B's minimum at a block being coloured,
// asked for by its SELECTED PARENT (dag.Lanes): every lane sealable at the
// parent's height plus one, the block's own height (the registration's
// set, changed only by a height-gated re-registration: nothing a sealer
// does moves a lane in or out; the served band of the chain form is NOT
// read, the record page's section 7 states why). The parent, not the
// block: a block under verification is not yet in the chain, so its own
// hash would read no header (F-807). A parent the chain does not hold
// answers nil (every blue credited); the callers verify the parents first.
func (l dagLookup) InMinimum(selectedParent common.Hash) []bool {
	m := l.e.vectorMenu()
	if m == nil {
		return nil
	}
	header := l.chain.GetHeaderByHash(selectedParent)
	if header == nil {
		return nil
	}
	height := header.Number.Uint64() + 1
	in := make([]bool, m.Count())
	for c := range in {
		in[c] = m.SealableAt(uint8(c), height)
	}
	return in
}

// CreditSlack is form B's slack S: the credited count of a lane may lead
// the least other lane's by at most S, one anticone's worth of one lane's
// blocks, the k in force (from the ladder's step 2 the k follows the lane
// cadence, DAGKFor, and S follows k). The bound it gives
// (docs/money/ORDERING-STEP2-2026-10-19.md section 2): a lane's share of the
// credited work at most 1/3 + 2 S / (3 T) over T credited units, and over
// any window at most 1/3 + 4 S / (3 T_window); a branch lacking a lane of
// the minimum gains at most 2 S credits a present lane. Read as the
// colouring reads k (dagDataOf's copy, set once by SetDAGFrom).
func (l dagLookup) CreditSlack() uint64 { return uint64(l.e.dag.k) }

// dagWork is a block's own work: under the vector form the UNIT (its
// difficulty over its lane's difficulty in force at it, which the
// difficulty rule makes one, times 2^32: a block of a lane at any hash
// rate weighs a block of any other, the lane's controller holding its
// rate), else the chain form's D(b).
func (e *Engine) dagWork(header *types.Header) *big.Int {
	if header.Difficulty == nil {
		return new(big.Int)
	}
	if _, ok := e.coordOf(header); !ok {
		return new(big.Int).Set(header.Difficulty)
	}
	return big.NewInt(dagUnit)
}

// dagDataOf is a block's Data from the cache, else recomputed from its
// headers back to a cached ancestor or the genesis (an iterative walk, no
// recursion). Nil with an error when a header is unknown.
func (e *Engine) dagDataOf(chain consensus.ChainHeaderReader, hash common.Hash) (*dag.Data, error) {
	e.dag.mu.Lock()
	d, ok := e.dag.cache[hash]
	k := e.dag.k
	e.dag.mu.Unlock()
	if ok {
		return d, nil
	}
	if chain == nil {
		return nil, errVectorNoChain
	}
	l := dagLookup{e, chain}
	stack := []common.Hash{hash}
	for len(stack) > 0 {
		h := stack[len(stack)-1]
		e.dag.mu.Lock()
		_, done := e.dag.cache[h]
		e.dag.mu.Unlock()
		if done {
			stack = stack[:len(stack)-1]
			continue
		}
		header := chain.GetHeaderByHash(h)
		if header == nil {
			return nil, errDAGUnknownParent
		}
		if header.Number.Sign() == 0 {
			e.dagStore(dag.Genesis(h))
			stack = stack[:len(stack)-1]
			continue
		}
		parents, err := dagParentsOf(header)
		if err != nil {
			return nil, err
		}
		pending := false
		for _, p := range parents {
			e.dag.mu.Lock()
			_, have := e.dag.cache[p]
			e.dag.mu.Unlock()
			if !have {
				if chain.GetHeaderByHash(p) == nil {
					return nil, errDAGUnknownParent
				}
				stack = append(stack, p)
				pending = true
			}
		}
		if pending {
			continue
		}
		nd, err := dag.Ghostdag(l, k, h, parents)
		if err != nil {
			return nil, err
		}
		e.dagStore(nd)
		stack = stack[:len(stack)-1]
	}
	e.dag.mu.Lock()
	d = e.dag.cache[hash]
	e.dag.mu.Unlock()
	return d, nil
}

func (e *Engine) dagStore(d *dag.Data) {
	e.dag.mu.Lock()
	defer e.dag.mu.Unlock()
	if len(e.dag.cache) >= dagCacheBound {
		// A bounded cache: everything is recomputable from the headers.
		e.dag.cache = make(map[common.Hash]*dag.Data)
	}
	e.dag.cache[d.Hash] = d
}

// dagParentsOf is a header's parents, the selected parent first then the
// merged ones, checked for shape: 32-byte hashes, distinct, none the
// selected parent, at most DAGMaxParents in all.
func dagParentsOf(header *types.Header) ([]common.Hash, error) {
	merged, err := header.MergedParents()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errDAGParents, err)
	}
	if len(merged)+1 > DAGMaxParents {
		return nil, fmt.Errorf("%w: %d parents, at most %d", errDAGParents, len(merged)+1, DAGMaxParents)
	}
	parents := make([]common.Hash, 0, len(merged)+1)
	parents = append(parents, header.ParentHash)
	seen := map[common.Hash]struct{}{header.ParentHash: {}}
	for _, m := range merged {
		if _, dup := seen[m]; dup {
			return nil, fmt.Errorf("%w: a parent named twice", errDAGParents)
		}
		seen[m] = struct{}{}
		parents = append(parents, m)
	}
	return parents, nil
}

// dagDataFor is a header's Data whether or not the block is known: the
// cache by hash, else the colouring computed from its parents (a template,
// whose hash is not final; not cached).
func (e *Engine) dagDataFor(chain consensus.ChainHeaderReader, header *types.Header) (*dag.Data, error) {
	if header.Number.Sign() == 0 {
		return dag.Genesis(header.Hash()), nil
	}
	e.dag.mu.Lock()
	d, ok := e.dag.cache[header.Hash()]
	k := e.dag.k
	e.dag.mu.Unlock()
	if ok {
		return d, nil
	}
	parents, err := dagParentsOf(header)
	if err != nil {
		return nil, err
	}
	for _, p := range parents {
		if _, err := e.dagDataOf(chain, p); err != nil {
			return nil, err
		}
	}
	return dag.Ghostdag(dagLookup{e, chain}, k, header.Hash(), parents)
}

// verifyDAGHeader is the header rule under the landing check: the merged parents
// well formed and known, an antichain with the selected parent, the
// selected parent the one of greatest blue work, the mergeset above the
// merge-depth root. Off the gate a header carrying merged parents is
// refused, so the live chains' headers are what they were.
func (e *Engine) verifyDAGHeader(chain consensus.ChainHeaderReader, header *types.Header) error {
	if !e.DAGOn(header.Number.Uint64()) {
		if len(header.Parents) > 0 {
			return errDAGOff
		}
		return nil
	}
	parents, err := dagParentsOf(header)
	if err != nil {
		return err
	}
	var missing []common.Hash
	for _, p := range parents[1:] {
		if chain.GetHeaderByHash(p) == nil {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		e.dag.mu.Lock()
		for _, m := range missing {
			if _, ok := e.dag.missing[m]; !ok {
				e.dag.missing[m] = time.Now()
			}
		}
		e.dag.mu.Unlock()
		return fmt.Errorf("%w (%d of %d, the first %x)", errDAGUnknownParent, len(missing), len(parents)-1, missing[0][:4])
	}
	l := dagLookup{e, chain}
	for _, p := range parents {
		if _, err := e.dagDataOf(chain, p); err != nil {
			return err
		}
	}
	for i, a := range parents {
		for j, b := range parents {
			if i != j && dag.IsAncestor(l, a, b) {
				return fmt.Errorf("%w: %x in the past of %x", errDAGAntichain, a[:4], b[:4])
			}
		}
	}
	d, err := dag.Ghostdag(l, e.dag.k, header.Hash(), parents)
	if err != nil {
		return err
	}
	if d.SelectedParent != header.ParentHash {
		return fmt.Errorf("%w: have %x, want %x", errDAGSelectedParent, header.ParentHash[:4], d.SelectedParent[:4])
	}
	// The merge-depth bound: the root is the selected-chain block whose
	// blue score is at or under the new block's minus DAGMergeDepth; every
	// mergeset block must have the root in its past.
	if root, ok := e.dagRootOf(chain, d); ok {
		for _, m := range d.MergesetReds {
			if !dag.IsAncestor(l, root, m) {
				return fmt.Errorf("%w: red %x below root %x", errDAGMergeDepth, m[:4], root[:4])
			}
		}
		for _, m := range d.MergesetBlues[1:] {
			if !dag.IsAncestor(l, root, m) {
				return fmt.Errorf("%w: blue %x below root %x", errDAGMergeDepth, m[:4], root[:4])
			}
		}
	}
	e.dagStore(d)
	return nil
}

// dagRootOf is the merge-depth root of a block's data: the selected-chain
// ancestor whose blue score is at or under BlueScore - DAGMergeDepth (ok
// false while the chain is shorter than the bound).
func (e *Engine) dagRootOf(chain consensus.ChainHeaderReader, d *dag.Data) (common.Hash, bool) {
	if d.BlueScore < DAGMergeDepth {
		return common.Hash{}, false
	}
	target := d.BlueScore - DAGMergeDepth
	cur := d.SelectedParent
	for {
		cd, err := e.dagDataOf(chain, cur)
		if err != nil || cd == nil {
			return common.Hash{}, false
		}
		if cd.BlueScore <= target || cd.Depth == 0 {
			return cur, true
		}
		cur = cd.SelectedParent
	}
}

// MissingDAGParents is what a block needs before it can be verified: its
// selected parent and merged parents this node does not hold (the fetcher
// asks the pushing peer for them by hash).
func (e *Engine) MissingDAGParents(chain consensus.ChainHeaderReader, block *types.Block) []common.Hash {
	if !e.DAGOn(block.NumberU64()) {
		return nil
	}
	parents, err := dagParentsOf(block.Header())
	if err != nil {
		return nil
	}
	var out []common.Hash
	for _, p := range parents {
		if chain.GetHeaderByHash(p) == nil {
			out = append(out, p)
		}
	}
	return out
}

// DAGMissing is the merged parents verifications found unknown and still
// unknown (the handler's gap filler asks any peer for them); entries older
// than two minutes or now known are dropped.
func (e *Engine) DAGMissing(chain consensus.ChainHeaderReader) []common.Hash {
	e.dag.mu.Lock()
	defer e.dag.mu.Unlock()
	var out []common.Hash
	for h, t := range e.dag.missing {
		if time.Since(t) > 2*time.Minute || chain.GetHeaderByHash(h) != nil {
			delete(e.dag.missing, h)
			continue
		}
		out = append(out, h)
	}
	return out
}

// BlockWritten (core.BlockChain, after a block's write): the tips, the
// arrival clock and the carried transactions.
func (e *Engine) BlockWritten(block *types.Block) {
	if !e.DAGOn(block.NumberU64()) {
		return
	}
	parents, err := dagParentsOf(block.Header())
	if err != nil {
		return
	}
	e.dag.mu.Lock()
	defer e.dag.mu.Unlock()
	for _, p := range parents {
		delete(e.dag.tips, p)
	}
	e.dag.tips[block.Hash()] = struct{}{}
	if _, ok := e.dag.arrival[block.Hash()]; !ok {
		e.dag.arrival[block.Hash()] = time.Now().UnixMilli()
	}
	for _, tx := range block.Transactions() {
		e.dag.carried[tx.Hash()] = append(e.dag.carried[tx.Hash()], block.Hash())
	}
}

// Reorged (core.BlockChain.reorg): the selected chain moved to a head that
// was not the old head's child.
func (e *Engine) Reorged(oldHead, newHead, commonBlock *types.Header) {
	if !e.DAGOn(newHead.Number.Uint64()) {
		return
	}
	e.dag.mu.Lock()
	defer e.dag.mu.Unlock()
	r := DAGReorg{At: time.Now().UnixMilli(), OldHead: oldHead.Hash(), NewHead: newHead.Hash()}
	if commonBlock != nil {
		r.Depth = oldHead.Number.Uint64() - commonBlock.Number.Uint64()
		r.Added = newHead.Number.Uint64() - commonBlock.Number.Uint64()
	}
	e.dag.reorgs = append(e.dag.reorgs, r)
}

// NoteDAGAccepted records a block's acceptance record (the state processor
// on import, the miner's result loop for its own block).
func (e *Engine) NoteDAGAccepted(block common.Hash, recs []dag.Accepted) {
	e.dag.mu.Lock()
	defer e.dag.mu.Unlock()
	if e.dag.accepted == nil {
		return
	}
	e.dag.accepted[block] = recs
}

// TemplateParents is the template's merged parents beside its selected
// parent: this node's tips other than the parent, none with the parent in
// its past, by blue work descending, at most DAGMaxParents - 1.
func (e *Engine) TemplateParents(chain consensus.ChainHeaderReader, parent *types.Header) []common.Hash {
	e.dag.mu.Lock()
	tips := make([]common.Hash, 0, len(e.dag.tips))
	for t := range e.dag.tips {
		if t != parent.Hash() {
			tips = append(tips, t)
		}
	}
	e.dag.mu.Unlock()
	l := dagLookup{e, chain}
	var out []common.Hash
	for _, t := range tips {
		if chain.GetHeaderByHash(t) == nil {
			continue
		}
		d, err := e.dagDataOf(chain, t)
		if err != nil || d == nil {
			continue
		}
		if dag.IsAncestor(l, t, parent.Hash()) || dag.IsAncestor(l, parent.Hash(), t) {
			continue
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return dag.Less(l, out[j], out[i]) })
	// An antichain among themselves (tips are, but a late tip's child may
	// have landed between the two reads): drop any tip in another's past.
	kept := out[:0]
	for i, a := range out {
		covered := false
		for j, b := range out {
			if i != j && dag.IsAncestor(l, a, b) {
				covered = true
				break
			}
		}
		if !covered {
			kept = append(kept, a)
		}
	}
	if len(kept) > DAGMaxParents-1 {
		kept = kept[:DAGMaxParents-1]
	}
	return kept
}

// DAGMergeset is a header's mergeset in the ordering's order WITHOUT the
// selected parent (whose transactions are in the parent state already): the
// blocks whose transactions the state processor and the miner apply, in
// this order, before the block's own (core.ApplyMergeset).
func (e *Engine) DAGMergeset(chain consensus.ChainHeaderReader, header *types.Header) ([]common.Hash, error) {
	d, err := e.dagDataFor(chain, header)
	if err != nil {
		return nil, err
	}
	return dag.OrderedMergeset(dagLookup{e, chain}, d)[1:], nil
}

// dagPreferByWeight is the fork choice under the landing check: the head of greater
// blue work, then of greater blue score (form B's second key), ties to the
// greater hash (the selected-parent rule, dag.Less, so the head is the
// parent a virtual block over every tip would select).
func (e *Engine) dagPreferByWeight(chain consensus.ChainHeaderReader, current, extern *types.Header) (reorg bool, err error) {
	if _, err := e.dagDataOf(chain, current.Hash()); err != nil {
		return false, err
	}
	if _, err := e.dagDataOf(chain, extern.Hash()); err != nil {
		return false, err
	}
	return dag.Less(dagLookup{e, chain}, current.Hash(), extern.Hash()), nil
}

// finalizeDAG pays the subsidy of the block's mergeset blues, each at its
// ORDER (its position among the blue blocks of the ordering, the selected
// parent at the parent's blue score, the i-th other blue at that plus i;
// the genesis at order zero issues nothing): the chain form's Reward(h)
// becomes Reward(order), so an epoch of 10,240 orders issues exactly what
// an epoch of 10,240 heights did. A red block issues nothing. The block's
// own subsidy is paid by the block that merges it, so Finalize credits the
// block's own coinbase nothing here (its fees the EVM credited stay).
func (e *Engine) finalizeDAG(chain consensus.ChainHeaderReader, header *types.Header, st *state.StateDB) error {
	d, err := e.dagDataFor(chain, header)
	if err != nil {
		return err
	}
	spd, err := e.dagDataOf(chain, d.SelectedParent)
	if err != nil {
		return err
	}
	for i, b := range d.MergesetBlues {
		order := spd.BlueScore + uint64(i)
		if order == 0 {
			continue
		}
		bh := chain.GetHeaderByHash(b)
		if bh == nil {
			return errDAGUnknownParent
		}
		miner, fund := e.config.Parts.Issuance.Reward(order)
		if miner.Sign() > 0 {
			creditReward(st, bh.Coinbase, uint256.MustFromBig(miner))
		}
		if fund.Sign() > 0 {
			fundAddr, _, _ := e.fundAt(order)
			creditReward(st, fundAddr, uint256.MustFromBig(fund))
		}
	}
	return nil
}

// DAG readers for the RPC (api_dag.go).

func (e *Engine) dagArrival(h common.Hash) (int64, bool) {
	e.dag.mu.Lock()
	defer e.dag.mu.Unlock()
	t, ok := e.dag.arrival[h]
	return t, ok
}

func (e *Engine) dagTips() []common.Hash {
	e.dag.mu.Lock()
	defer e.dag.mu.Unlock()
	out := make([]common.Hash, 0, len(e.dag.tips))
	for t := range e.dag.tips {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return bytes.Compare(out[i][:], out[j][:]) < 0 })
	return out
}

func (e *Engine) dagCarried(tx common.Hash) []common.Hash {
	e.dag.mu.Lock()
	defer e.dag.mu.Unlock()
	return append([]common.Hash(nil), e.dag.carried[tx]...)
}

func (e *Engine) dagAccepted(block common.Hash) []dag.Accepted {
	e.dag.mu.Lock()
	defer e.dag.mu.Unlock()
	return append([]dag.Accepted(nil), e.dag.accepted[block]...)
}

func (e *Engine) dagReorgs() []DAGReorg {
	e.dag.mu.Lock()
	defer e.dag.mu.Unlock()
	return append([]DAGReorg(nil), e.dag.reorgs...)
}
