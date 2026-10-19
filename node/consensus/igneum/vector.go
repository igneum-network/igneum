package igneum

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/igneum/difficulty"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/vector"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// VectorWork on the node (docs/consensus/VECTORWORK.md; the outside review's
// design through main, 13 October 2026, 00:5x UK): a block carries one
// coordinate tag inside its seal hash; its difficulty is that coordinate's
// ASERT output at the parent, read from the coordinate's own blocks alone
// (the genesis-anchored form: D_c = DRef_c x 2^((N_c x T_c - (t_parent -
// t_genesis)) / halfLife) with N_c the coordinate's block count before the
// block, so a coordinate without blocks decays and one at its target holds);
// its seal verifies under the coordinate's relation; and the fork choice
// weighs a chain by the product over coordinates of its cumulative work
// N_c = sum of D(b) over the coordinate's blocks from genesis (the
// geometric mean's monotone twin; the reference difficulties being the same
// constants on both branches of one chain, the cross-multiplication of the
// specification reduces to the plain products), or by the minimum of
// N_c / DRef_c under the minimum rule, with the chain's lot tie-break at
// equality. Every quantity is a function of the chain's blocks alone.
//
// The per-block state (the counts and the cumulative work per coordinate)
// is kept in a cache by block hash, extended from the parent's entry on
// each block and rebuilt from the nearest cached ancestor (or genesis)
// on a miss.

var (
	errVectorCoord      = errors.New("igneum vectorwork: the block's coordinate is missing or out of range")
	errVectorCoordOff   = errors.New("igneum vectorwork: a coordinate tag on a chain without the vector form")
	errVectorDifficulty = errors.New("igneum vectorwork: the block's difficulty is not its coordinate's controller output at the parent")
	errVectorNoChain    = errors.New("igneum vectorwork: no chain to read the coordinate state from")
	// F-166: a block on a coordinate with pay weight 0 at its height.
	errVectorCoordUnpaid = errors.New("igneum vectorwork: the block's coordinate has no pay weight at its height (not a sealing coordinate)")
)

// vectorState is a block's cumulative coordinate state.
type vectorState struct {
	counts [vector.MaxCoordinates]uint64   // N_c in blocks
	work   [vector.MaxCoordinates]*big.Int // N_c in difficulty: the sum of D(b) over the coordinate's blocks
	// start is the chain's clock on this branch: the genesis time, or the
	// first block's time when the genesis carries none (a development
	// genesis at time zero made every controller read a lag of the whole
	// epoch after block 1 and fall to the floor: vectorwork1, 13 October
	// 2026, 00:59 UTC, "have 240926, want 8192").
	start uint64
	// last is each coordinate's last block height on the branch (zero
	// while it has none): the readers' liveness figure (the pool's router
	// restores an emptied coordinate on it).
	last [vector.MaxCoordinates]uint64
	// lag is each coordinate's folded lag in seconds (revision 10): from
	// zero at the start, plus every block's interval, minus the target
	// for the coordinate's own blocks, capped at the coordinate's LagCap
	// where one is registered; time is the block's time the state is
	// after (the fold's t(h - 1) for its child).
	lag  [vector.MaxCoordinates]int64
	time uint64
	// gen is the menu generation the state was computed under (F-197, 16
	// October 2026): a registry landing replaces the menu, and a state
	// computed under the menu before it (a constants boundary not yet in
	// force, a coordinate not yet known) must never serve a verification
	// after it; the cache admits a state of the current generation only.
	gen uint64
}

func (s *vectorState) clone() *vectorState {
	out := &vectorState{counts: s.counts, start: s.start, last: s.last, lag: s.lag, time: s.time, gen: s.gen}
	for i := range s.work {
		if s.work[i] != nil {
			out.work[i] = new(big.Int).Set(s.work[i])
		} else {
			out.work[i] = new(big.Int)
		}
	}
	return out
}

const vectorCacheBound = 8192

// vectorMenu is the chain's vector form, nil on a single-relation chain.
func (e *Engine) vectorMenu() *vector.Menu {
	m, _ := e.algo.(*vector.Menu)
	return m
}

// coordOf is a header's coordinate: ok false when the header carries none
// or the chain has no vector form.
func (e *Engine) coordOf(header *types.Header) (uint8, bool) {
	m := e.vectorMenu()
	if m == nil {
		return 0, false
	}
	c, ok := header.CoordTag()
	if !ok || int(c) >= m.Count() {
		return 0, false
	}
	if header.Number != nil && !m.ActiveAt(c, header.Number.Uint64()) {
		// A block of a coordinate before its activation height carries
		// no coordinate of the chain's (revision 16's height-gated
		// re-registration).
		return 0, false
	}
	return c, true
}

// vectorStateOf is the cumulative state after a header (the header's own
// block included), from the cache or by walking to a cached ancestor.
func (e *Engine) vectorStateOf(chain consensus.ChainHeaderReader, header *types.Header) (*vectorState, error) {
	m := e.vectorMenu()
	if m == nil {
		return nil, errVectorCoordOff
	}
	// F-197: a cached state serves only under the menu generation it was
	// computed under; a landing's new menu recomputes from the cached
	// ancestor of its own generation or genesis.
	gen := m.Generation()
	e.vectorMu.Lock()
	if s, ok := e.vectorCache[header.Hash()]; ok && s.gen == gen {
		e.vectorMu.Unlock()
		return s, nil
	}
	e.vectorMu.Unlock()
	// Walk back to a cached ancestor or genesis, then roll forward.
	var path []*types.Header
	h := header
	var base *vectorState
	for {
		if h.Number.Sign() == 0 {
			base = &vectorState{start: h.Time, time: h.Time}
			for i := range base.work {
				base.work[i] = new(big.Int)
				// The genesis seed (revision 9): N_c includes D_ref_c
				// from height 0, so the product is never zero.
				if m.SeedGenesis() {
					if coord, ok := m.Coordinate(uint8(i)); ok && i < m.Count() {
						base.work[i].Set(coord.DRef)
					}
				}
			}
			break
		}
		path = append(path, h)
		if chain == nil {
			return nil, errVectorNoChain
		}
		parent := chain.GetHeader(h.ParentHash, h.Number.Uint64()-1)
		if parent == nil {
			return nil, consensus.ErrUnknownAncestor
		}
		e.vectorMu.Lock()
		s, ok := e.vectorCache[parent.Hash()]
		e.vectorMu.Unlock()
		if ok && s.gen == gen {
			base = s
			break
		}
		h = parent
	}
	s := base.clone()
	s.gen = gen
	for i := len(path) - 1; i >= 0; i-- {
		b := path[i]
		c, ok := e.coordOf(b)
		if !ok {
			return nil, fmt.Errorf("%w: block %d", errVectorCoord, b.Number.Uint64())
		}
		s.counts[c]++
		s.work[c] = new(big.Int).Add(s.work[c], b.Difficulty)
		s.last[c] = b.Number.Uint64()
		// The block DAG ordering (dagform.go; the design's lane-difficulty
		// row): a lane's controller counts the lane's BLUE blocks in the
		// selected-chain past, so the blues this chain block merged beside
		// its selected parent count here with their work; the reds do not.
		if e.DAGOn(b.Number.Uint64()) {
			if d, err := e.dagDataOf(chain, b.Hash()); err == nil && d != nil {
				for _, mb := range d.MergesetBlues[1:] {
					mh := chain.GetHeaderByHash(mb)
					if mh == nil {
						continue
					}
					if mc, ok := e.coordOf(mh); ok {
						s.counts[mc]++
						s.work[mc] = new(big.Int).Add(s.work[mc], mh.Difficulty)
					}
				}
			}
		}
		if s.start == 0 {
			s.start = b.Time
		}
		// The folded lag (revision 10): a time-zero genesis gives the
		// first block an interval of zero (the start is its own time).
		var dt int64
		if s.time > 0 && b.Time > s.time {
			dt = int64(b.Time - s.time)
		}
		for i := 0; i < m.Count(); i++ {
			coord, _ := m.Coordinate(uint8(i))
			if !m.ActiveAt(uint8(i), b.Number.Uint64()) {
				// Not yet a coordinate: no lag accrues before its
				// activation (its controller anchors there).
				continue
			}
			s.lag[i] += dt
			if uint8(i) == c {
				// The lane cadence under the ordering's gate entry
				// (dagLaneTarget), else the registration's target.
				s.lag[i] -= int64(e.dagLaneTarget(coord.Target, b.Number.Uint64()))
			}
			// The cap in force at the block's height (F-151: a changed
			// half-life carries its derived cap from its height).
			if lc := m.LagCapAt(uint8(i), b.Number.Uint64()); lc > 0 && s.lag[i] > int64(lc) {
				s.lag[i] = int64(lc)
			}
		}
		// F-151, the boundary (the design review's residual at 2f3e5369a): the
		// carried lag is a count of seconds the exponent divides by the
		// half-life, so where the child's half-life differs from this
		// block's the lag is rescaled by the half-lives' ratio, L' = L x
		// H_new / H_old, and the child's controller reads the difficulty
		// this block's would have (continuous at A; nothing gained from
		// seeing the change coming); the derived cap follows by the same
		// ratio, so a capped lag stays at the cap.
		if hNew, hOld := m.HalfLifeAt(b.Number.Uint64()+1), m.HalfLifeAt(b.Number.Uint64()); hNew != hOld && hOld > 0 {
			for i := 0; i < m.Count(); i++ {
				if m.ActiveAt(uint8(i), b.Number.Uint64()) {
					s.lag[i] = s.lag[i] * int64(hNew) / int64(hOld)
				}
			}
		}
		// An in-place document change at the child's height (LANE-
		// MODIFICATION.md section 1a; the security review's read): the
		// coordinate's reference moves from this block's form to the
		// child's, and the lag is re-anchored so the child's controller
		// reads the difficulty this block's would have (continuous at A,
		// only the target moving; a change's reference is no difficulty
		// reset handed to whoever is ready).
		for i := 0; i < m.Count(); i++ {
			if !m.ActiveAt(uint8(i), b.Number.Uint64()) {
				continue
			}
			if cur, next := m.ByCoordinateAt(uint8(i), b.Number.Uint64()), m.ByCoordinateAt(uint8(i), b.Number.Uint64()+1); cur != next {
				fo, _ := m.CoordinateAt(uint8(i), b.Number.Uint64())
				fn, _ := m.CoordinateAt(uint8(i), b.Number.Uint64()+1)
				s.lag[i] = vector.ReanchorLag(m.HalfLifeAt(b.Number.Uint64()+1), s.lag[i], fo.DRef.Uint64(), fn.DRef.Uint64())
			}
		}
		s.time = b.Time
		e.vectorMu.Lock()
		if e.vectorCache == nil || len(e.vectorCache) >= vectorCacheBound {
			e.vectorCache = make(map[common.Hash]*vectorState)
		}
		e.vectorCache[b.Hash()] = s.clone()
		e.vectorMu.Unlock()
	}
	return s, nil
}

// vectorDifficulty is coordinate c's controller output at the parent: the
// genesis-anchored ASERT over the coordinate's own blocks (its count
// before the new block and the parent's time against genesis), floored at
// the chain's minimum.
func (e *Engine) vectorDifficulty(chain consensus.ChainHeaderReader, parent *types.Header, c uint8) (*big.Int, error) {
	m := e.vectorMenu()
	if m == nil {
		return nil, errVectorCoordOff
	}
	// The coordinate's form in force at the CHILD's height: its reference,
	// floor and cap (an in-place document change moves them from its
	// height; the controller's lag and count carry over).
	coord, ok := m.CoordinateAt(c, parent.Number.Uint64()+1)
	if !ok {
		return nil, errVectorCoord
	}
	s, err := e.vectorStateOf(chain, parent)
	if err != nil {
		return nil, err
	}
	minD := e.config.Parts.Difficulty.Min
	if coord.DMin != nil {
		minD = coord.DMin
	}
	// The constants in force at the child's height (F-151): the block at
	// a constantsChanges height is the first whose difficulty reads them.
	height := parent.Number.Uint64() + 1
	p := difficulty.Params{Target: e.dagLaneTarget(coord.Target, height), HalfLife: m.HalfLifeAt(height), DtMax: m.DtMaxAt(height), Min: minD}
	// NextAbsolute reads the lag as parentTime - startTime - Target x
	// parentNumber; the coordinate's "number" is its own block count and
	// the start is the branch's clock (the genesis time, else the first
	// block's). At the genesis itself the lag is zero: the reference.
	// The folded lag also under the landing check entry's lane cadence (the ladder's
	// step 2): the anchored form would re-read the whole history at the
	// new target and jump at a later gate height; the folded lag changes
	// slope there and nothing else (equal to the anchored lag wherever the
	// cadence is the registration's and the cap was never reached).
	if coord.LagCap > 0 || m.HalfLifeAt(height) != m.HalfLife() || e.dagCadenceOn(height) {
		// The folded lag: the coordinate's registered cap, or (F-151)
		// every coordinate from the first half-life change, the folded
		// lag being the one rescaled at the boundary (the anchored form
		// reads the clock alone and would jump there).
		// Revision 10: the folded, capped lag in place of the anchored
		// one (the two agree wherever the cap was never reached).
		return p.NextFromLag(coord.DRef, s.lag[c]), nil
	}
	start := s.start
	if start == 0 || parent.Number.Sign() == 0 {
		start = parent.Time
	}
	return p.NextAbsolute(coord.DRef, start, parent.Time, s.counts[c]), nil
}

// verifyVectorHeader is the header rule under the vector form: the tag
// present and in range, the difficulty the coordinate's controller output
// at the parent. Nil on a chain without the form (a tag there is refused).
func (e *Engine) verifyVectorHeader(chain consensus.ChainHeaderReader, header, parent *types.Header) error {
	m := e.vectorMenu()
	if m == nil {
		if len(header.Coord) > 0 {
			return errVectorCoordOff
		}
		return nil
	}
	c, ok := e.coordOf(header)
	if !ok {
		return errVectorCoord
	}
	// F-166: from sealingBreadthFrom only a coordinate with pay weight at
	// the block's own height seals (the registered weight; the coordinate
	// stays registered and its controller runs, so a payChanges entry
	// restores it from its height).
	if !m.SealableAt(c, header.Number.Uint64()) {
		return fmt.Errorf("%w: coordinate %d at %d", errVectorCoordUnpaid, c, header.Number.Uint64())
	}
	want, err := e.vectorDifficulty(chain, parent, c)
	if err != nil {
		return err
	}
	if header.Difficulty == nil || header.Difficulty.Cmp(want) != 0 {
		return fmt.Errorf("%w: have %v, want %v (coordinate %d)", errVectorDifficulty, header.Difficulty, want, c)
	}
	return nil
}

// vectorWeight is a head's fork weight: the product of its cumulative
// per-coordinate work under the geometric rule (zero when any coordinate
// has none), or the minimum of N_c / DRef_c as a cross-multiplied pair
// under the minimum rule. The second value is the sum of the scaled W_c,
// the second key under both rules (the zero phase of the geometric rule;
// the specification's lexicographic order for the minimum rule).
func (e *Engine) vectorWeight(chain consensus.ChainHeaderReader, header *types.Header) (*big.Int, *big.Int, error) {
	m := e.vectorMenu()
	s, err := e.vectorStateOf(chain, header)
	if err != nil {
		return nil, nil, err
	}
	n := m.Count()
	// The second key under both rules is the sum of the scaled W_c (the
	// specification's lexicographic order for the minimum rule; for the
	// geometric rule the zero phase's order: the product is zero while any
	// coordinate has no block from genesis, so a chain's first blocks would
	// all tie with the genesis and the lot could refuse a chain its own
	// child; the first tagged block of vectorwork1, 13 October 2026, 00:41
	// UTC, was imported and not made canonical on exactly this). A child
	// always carries more scaled sum than its parent, so an extension is
	// never refused; two branches at zero product order by their work.
	den := big.NewInt(1)
	for c := 0; c < n; c++ {
		coord, _ := m.CoordinateAt(uint8(c), header.Number.Uint64())
		den.Mul(den, coord.DRef)
	}
	var minv, sum *big.Int
	for c := 0; c < n; c++ {
		coord, _ := m.CoordinateAt(uint8(c), header.Number.Uint64())
		v := new(big.Int).Mul(s.work[c], new(big.Int).Div(den, coord.DRef))
		// F-166 (the design review's line): under the minimum form the fork
		// vector's breadth at the head's height is the sealable
		// coordinates; a coordinate frozen at weight 0 would otherwise
		// be the minimum on every branch and fork choice degenerate to
		// first-seen for good.
		if sealable := m.SealableAt(uint8(c), header.Number.Uint64()); sealable && (minv == nil || v.Cmp(minv) < 0) {
			minv = v
		}
		if sum == nil {
			sum = new(big.Int).Set(v)
		} else {
			sum.Add(sum, v)
		}
	}
	if !m.Minimum() {
		prod := big.NewInt(1)
		for c := 0; c < n; c++ {
			prod.Mul(prod, s.work[c])
		}
		return prod, sum, nil
	}
	if minv == nil {
		minv = new(big.Int)
	}
	return minv, sum, nil
}

// PreferByWeight is the fork choice under the vector form (the forker's
// first hook): true to move from current to extern, decided false on a
// chain without the form (the forker's total-difficulty order then). At
// equal weight the chain's lot tie-break decides (ForkTieBreak), as the
// specification registers.
func (e *Engine) PreferByWeight(chain consensus.ChainHeaderReader, current, extern *types.Header) (reorg, decided bool, err error) {
	// The block DAG ordering (dagform.go): the head by blue work, ties to
	// the greater hash, whatever the vector form says below.
	if current != nil && extern != nil && current.Hash() != extern.Hash() && e.DAGOn(current.Number.Uint64()) && e.DAGOn(extern.Number.Uint64()) {
		reorg, err = e.dagPreferByWeight(chain, current, extern)
		return reorg, true, err
	}
	m := e.vectorMenu()
	if m == nil {
		return false, false, nil
	}
	if current == nil || extern == nil || current.Hash() == extern.Hash() {
		return false, true, nil
	}
	// The same-height rule (firstseen.go; the review lead's ruling of record of
	// 16 October 2026, 13:37 UK, as main re-pointed it at b5460078c), in
	// force from the chain's gate at the first contested height: at one
	// height between single blocks the block imported first STANDS
	// whatever the competitor's weight; in every other race under the landing check
	// the normalised MEASURE is the primary order, total by itself (the
	// first key the minimum over the served lanes past L, the scaled sum
	// below it), the LENGTH only at equal measure, then the lot. (An
	// earlier wording here quoted c60cb15e0's "a shorter branch never
	// replaces", which the ruling of record replaced and these bytes never
	// carried: F-569, 17 October 2026, the depth-five replacement at 8,342
	// decided by the sum as written.) Below the gate the measure decides
	// every race as before, the lot at exact equality.
	if from := e.FirstSeenFrom(); from != 0 {
		_, cur, ext, err := branchSuffixes(chain, current, extern)
		if err != nil {
			return false, true, err
		}
		contested := extern.Number.Uint64()
		if len(cur) > 0 {
			contested = cur[0].Number.Uint64()
		}
		if contested >= from {
			hc, he := current.Number.Uint64(), extern.Number.Uint64()
			if he == hc && len(cur) == 1 && len(ext) == 1 {
				// First-seen between single blocks at one height: the
				// block imported first stands.
				return false, true, nil
			}
			// The measure as a total order by itself: the first key the
			// minimum over the served lanes (the lanes in the band at the
			// two branches' common ancestor, servedAt), the second the
			// scaled sum, then the length, then the lot.
			c, err := e.compareByMeasure(chain, m, current, extern, true)
			if err != nil {
				return false, true, err
			}
			if c != 0 {
				return c > 0, true, nil
			}
			if he != hc {
				return he > hc, true, nil
			}
			reorg, err = e.ForkTieBreak(chain, current, extern)
			return reorg, true, err
		}
	}
	c, err := e.compareByMeasure(chain, m, current, extern, false)
	if err != nil {
		return false, true, err
	}
	switch {
	case c > 0:
		return true, true, nil
	case c < 0:
		return false, true, nil
	}
	reorg, err = e.ForkTieBreak(chain, current, extern)
	return reorg, true, err
}

// compareByMeasure is the fork rule's normalised measure as a signed
// comparison: positive when extern's branch outweighs current's, negative
// when it is lighter, zero at equality (the coverage rule's count then its
// keys; the geometric and minimum rules' two keys). Under the same-height
// rule (served true, from the chain's gate; the review lead's ruling b5460078c
// as main re-pointed it, the served mark as ruled at b9ff75f28) the measure
// is a total order by itself: the first key the MINIMUM of the normalised
// work over the SERVED lanes (a lane with a block on either branch and IN
// THE BAND at the two branches' COMMON ANCESTOR, servedAt: its difficulty
// at the fork at or above twice its floor; as F-124 excludes an unserved
// lane from the breadth, a branch lacking a served lane scores zero and a
// floor lane's majority holder casts no vote by adding, lifting or
// withholding its blocks), the second key the scaled sum over every lane;
// the length and the lot are the caller's.
func (e *Engine) compareByMeasure(chain consensus.ChainHeaderReader, m *vector.Menu, current, extern *types.Header, served bool) (int, error) {
	if m.Coverage() {
		return e.compareByCoverage(chain, m, current, extern, served)
	}
	if !served {
		cw, cs, err := e.vectorWeight(chain, current)
		if err != nil {
			return 0, err
		}
		ew, es, err := e.vectorWeight(chain, extern)
		if err != nil {
			return 0, err
		}
		if cmp := ew.Cmp(cw); cmp != 0 {
			return cmp, nil
		}
		if cs != nil && es != nil {
			return es.Cmp(cs), nil
		}
		return 0, nil
	}
	anc, cur, ext, err := branchSuffixes(chain, current, extern)
	if err != nil {
		return 0, err
	}
	// F-669 (the review lead's ruling 9825b99d0, 18 October 2026): from the
	// fork-weight gate a coordinate whose pay share is ZERO at the race's
	// height (a sponsored lane, or a lane retired by a zero weight) is
	// outside the fork weight and the served band: its work is bought by a
	// sponsor, never a vote the chain's pay backs; keyed on the share, not
	// the mark; never retroactive below the gate.
	var excluded []bool
	if anc != nil && e.forkWeightOn(anc.Number.Uint64()+1) {
		excluded = unpaidLanes(m.PaySharesAt(anc.Number.Uint64()+1), m.Count())
	}
	band, err := e.servedAt(chain, m, anc)
	if err != nil {
		return 0, err
	}
	for c := range band {
		if c < len(excluded) && excluded[c] {
			band[c] = false
		}
	}
	// The lanes' references for the weights: the registered dRef below the
	// fork-weight gate; from the landing check (F-569, the review lead's ruling 7f95a4dc1
	// of 18 October 2026) each lane's RUNNING difficulty at the common
	// ancestor, the controller's figure for its next block there (the same
	// on both branches by construction, since the ancestor is shared), so a
	// block weighs about one unit of its own lane whatever the lane's
	// distance from its reference: at 8,342 two raster blocks at 1.84 x
	// their reference displaced five card-lane blocks at 0.26 to 0.29 x
	// theirs under dRef; under the running figure the five weigh five.
	var refs []*big.Int
	if anc != nil && e.forkWeightOn(anc.Number.Uint64()+1) {
		if refs, err = e.runningAt(chain, m, anc); err != nil {
			return 0, err
		}
	}
	wc, err := e.laneWeightsBy(chain, current, refs, excluded)
	if err != nil {
		return 0, err
	}
	we, err := e.laneWeightsBy(chain, extern, refs, excluded)
	if err != nil {
		return 0, err
	}
	if refs != nil {
		// From the fork-weight gate the scaled SUM over the lanes decides
		// at every depth, the first key's minimum retired (the review lead's
		// ruling of record b04c0769f, the security review's strike 75fs on the
		// cut's first half): under the running references the minimum
		// names the lane with the smallest history in its own current
		// units, a ratio a lane operator moves by his hash before the
		// fork and then decides the deep race with his own suffix; the
		// sum has no such handle, and the per-lane normalisation is what
		// keeps a floor lane's blocks at a unit each.
		return we.sum.Cmp(wc.sum), nil
	}
	return compareServed(wc, we, band, len(cur), len(ext), e.suffixPresence(m, cur), e.suffixPresence(m, ext), m.Minimum()), nil
}

// runningAt is every lane's running difficulty at a race's common ancestor
// (the controller's figure for the lane's next block there, servedAt's
// own read), nil for a lane not sealable above the ancestor: the fork
// weights' references from the fork-weight gate.
func (e *Engine) runningAt(chain consensus.ChainHeaderReader, m *vector.Menu, anc *types.Header) ([]*big.Int, error) {
	n := m.Count()
	refs := make([]*big.Int, n)
	height := anc.Number.Uint64() + 1
	for c := 0; c < n; c++ {
		if !m.SealableAt(uint8(c), height) {
			continue
		}
		d, err := e.vectorDifficulty(chain, anc, uint8(c))
		if err != nil {
			return nil, err
		}
		refs[c] = d
	}
	return refs, nil
}

// withholdingL is the length past which a served lane's ABSENCE on a
// branch is read (the review lead's ruling bf15bed11 on the security review's
// withholding lever, 16 October 2026): 4k blocks of the branch's own
// length above the ancestor, k the served-lane count at the ancestor (16
// at four lanes, 12 at three). An equal-rate lane is absent by luck over L
// blocks with chance (1 - 1/k)^L: at two rounds one honest stretch in ten,
// at four rounds one in a hundred.
func withholdingL(band []bool) int {
	k := 0
	for _, b := range band {
		if b {
			k++
		}
	}
	return 4 * k
}

// firstKey is the served form's first key over two branches' per-lane
// scaled work (the window's on the coverage rule, the cumulative on the
// minimum rule), ruled against the zero at bf15bed11: when EITHER branch
// is shorter than L (withholdingL) there is no first key (nil, nil: the
// scaled sum decides, then the length, then the lot), since under a zero
// the first key is a presence contest a sealer alone on a served lane
// wins by withholding it; when both are at L or past it, a served lane
// absent on a branch that carries every other served lane is EXCLUDED
// from the key for both branches (de facto unserved there, withheld or
// abandoned, the protocol cannot tell which; a lane absent on both the
// same), the key the minimum over the lanes that remain, and a branch
// lacking two or more served lanes scores zero. The rule's cost, named in
// VECTORWORK.md: past L a branch carrying all served lanes but one with
// more work than the other on each of the rest wins; the rule's reach is
// one lane.
func firstKey(band []bool, vc, ve []*big.Int, lenC, lenE int, presC, presE []bool) (minC, minE *big.Int) {
	L := withholdingL(band)
	if L == 0 || lenC < L || lenE < L {
		return nil, nil
	}
	missC, missE := 0, 0
	for i, b := range band {
		if !b {
			continue
		}
		if !presC[i] {
			missC++
		}
		if !presE[i] {
			missE++
		}
	}
	for i, b := range band {
		if !b {
			continue
		}
		if (!presC[i] && !presE[i]) || (!presC[i] && missC == 1) || (!presE[i] && missE == 1) {
			continue
		}
		if minC == nil || vc[i].Cmp(minC) < 0 {
			minC = vc[i]
		}
		if minE == nil || ve[i].Cmp(minE) < 0 {
			minE = ve[i]
		}
	}
	if minC == nil || missC >= 2 {
		minC = new(big.Int)
	}
	if minE == nil || missE >= 2 {
		minE = new(big.Int)
	}
	return minC, minE
}

// suffixPresence marks the lanes with a sealable block in a branch's own
// headers above the ancestor (the withholding rule's absence is read on
// the branch's own blocks, never on the shared prefix or the window).
func (e *Engine) suffixPresence(m *vector.Menu, suffix []*types.Header) []bool {
	present := make([]bool, m.Count())
	for _, h := range suffix {
		c, ok := e.coordOf(h)
		if !ok || int(c) >= len(present) || !m.SealableAt(c, h.Number.Uint64()) {
			continue
		}
		present[c] = true
	}
	return present
}

// servedAt is the served band read at a race's common ancestor (the review lead's
// ruling b9ff75f28 on the security review's read of 1acc2ea4d: a mark read on the
// branches' own blocks let the lane's majority holder lift the lane into
// the first key or drop it out by its own mining, the veto returning as a
// bought vote). A lane is served when it is sealable above the ancestor and
// its difficulty at the fork, the controller's figure for its next block
// there (the figure the registry check reads as nextDifficulty, the same on
// both branches by construction since the ancestor is shared, so neither
// branch's mining moves it), is at or above TWICE ITS FLOOR (half its
// reference); the controller's figure comes from the ancestor's own state
// walk (the branch's headers by parent hash, never a canonical read by
// number). Presence stays the callers' union: a served lane absent on a
// branch scores zero there, the burst defence.
func (e *Engine) servedAt(chain consensus.ChainHeaderReader, m *vector.Menu, anc *types.Header) ([]bool, error) {
	n := m.Count()
	served := make([]bool, n)
	if anc == nil {
		return served, nil
	}
	height := anc.Number.Uint64() + 1
	for c := 0; c < n; c++ {
		if !m.SealableAt(uint8(c), height) {
			continue
		}
		d, err := e.vectorDifficulty(chain, anc, uint8(c))
		if err != nil {
			return nil, err
		}
		band := new(big.Int).Lsh(e.laneFloor(m, uint8(c), height), 1)
		served[c] = d.Cmp(band) >= 0
	}
	return served, nil
}

// laneWeights is a branch's per-lane scaled cumulative work (the minimum
// rule's terms) with each lane's presence (any block from genesis); the
// served band is the ancestor's (servedAt), not the branch's.
type laneWeights struct {
	v       []*big.Int
	present []bool
	sum     *big.Int
	prod    *big.Int
}

func (e *Engine) laneWeights(chain consensus.ChainHeaderReader, header *types.Header) (laneWeights, error) {
	return e.laneWeightsBy(chain, header, nil, nil)
}

// unpaidLanes marks the coordinates whose pay share is zero in shares (an
// absent entry reads zero): the lanes the fork weight excludes from the
// gate (F-669).
func unpaidLanes(shares []uint64, n int) []bool {
	out := make([]bool, n)
	for c := 0; c < n; c++ {
		out[c] = c >= len(shares) || shares[c] == 0
	}
	return out
}

// excludeLaneWork zeroes the work of the excluded lanes (F-669): their
// blocks weigh nothing in the sum, on both branches alike.
func excludeLaneWork(work []*big.Int, excluded []bool) []*big.Int {
	out := make([]*big.Int, len(work))
	for c, w := range work {
		if c < len(excluded) && excluded[c] {
			out[c] = new(big.Int)
		} else {
			out[c] = w
		}
	}
	return out
}

// laneWeightsBy is laneWeights with the lanes' references given: refs[c]
// (the running difficulty at the race's common ancestor from the
// fork-weight gate) where set, the registered dRef where nil or absent;
// excluded[c] (F-669: the unpaid lanes from the landing check) drops a lane's work
// from the weights and its presence.
func (e *Engine) laneWeightsBy(chain consensus.ChainHeaderReader, header *types.Header, refs []*big.Int, excluded []bool) (laneWeights, error) {
	m := e.vectorMenu()
	s, err := e.vectorStateOf(chain, header)
	if err != nil {
		return laneWeights{}, err
	}
	n := m.Count()
	work := make([]*big.Int, n)
	byRef := make([]*big.Int, n)
	for c := 0; c < n; c++ {
		coord, _ := m.Coordinate(uint8(c))
		work[c] = s.work[c]
		byRef[c] = coord.DRef
		if c < len(refs) && refs[c] != nil && refs[c].Sign() > 0 {
			byRef[c] = refs[c]
		}
	}
	if excluded != nil {
		work = excludeLaneWork(work, excluded)
	}
	v, sum := scaleLaneWork(work, byRef)
	w := laneWeights{v: v, present: make([]bool, n), sum: sum, prod: big.NewInt(1)}
	for c := 0; c < n; c++ {
		w.prod.Mul(w.prod, s.work[c])
		w.present[c] = m.SealableAt(uint8(c), header.Number.Uint64()) && s.last[c] != 0 && !(c < len(excluded) && excluded[c])
	}
	return w, nil
}

// scaleLaneWork is the measure's scaling: each lane's work over its
// reference, in the common unit of the references' product (integer
// arithmetic, no division of the work): v[c] = work[c] x (prod(refs) /
// refs[c]), and the sum over the lanes.
func scaleLaneWork(work, refs []*big.Int) (v []*big.Int, sum *big.Int) {
	den := big.NewInt(1)
	for _, r := range refs {
		den.Mul(den, r)
	}
	v = make([]*big.Int, len(work))
	sum = new(big.Int)
	for c := range work {
		v[c] = new(big.Int).Mul(work[c], new(big.Int).Div(den, refs[c]))
		sum.Add(sum, v[c])
	}
	return v, sum
}

// compareServed is the served form's comparison of two branches' lane
// weights under the minimum rule: the first key of firstKey (the minimum
// over the served lanes less the excluded, past L on both branches; none
// in a short race), then the scaled sum; positive when e outweighs c.
// Under the geometric rule the product keeps its place as the first key.
func compareServed(c, e laneWeights, band []bool, lenC, lenE int, presC, presE []bool, minimum bool) int {
	if !minimum {
		if cmp := e.prod.Cmp(c.prod); cmp != 0 {
			return cmp
		}
		return e.sum.Cmp(c.sum)
	}
	if minC, minE := firstKey(band, c.v, e.v, lenC, lenE, presC, presE); minC != nil && minE != nil {
		if cmp := minE.Cmp(minC); cmp != 0 {
			return cmp
		}
	}
	return e.sum.Cmp(c.sum)
}

// laneFloor is coordinate c's difficulty floor at a height: the dMin of the
// form in force there (an in-place document change carries its own floor
// from its height, LANE-MODIFICATION.md section 1a; F-245: the base
// document's floor read against the changed form's difficulty put the
// served band at the old floor), else the chain's minimum.
func (e *Engine) laneFloor(m *vector.Menu, c uint8, height uint64) *big.Int {
	if coord, ok := m.CoordinateAt(c, height); ok && coord.DMin != nil {
		return coord.DMin
	}
	return e.config.Parts.Difficulty.Min
}

// sealDifficulty is the difficulty a header's seal is checked and found
// against: the header's, or under the vector form the coordinate's scaled
// unit (header.Difficulty / WorkScale, at least 1). The controller, the
// weight and the header rule run on header.Difficulty itself.
func (e *Engine) sealDifficulty(header *types.Header) *big.Int {
	d := header.Difficulty
	m := e.vectorMenu()
	if m == nil || d == nil {
		return d
	}
	c, ok := e.coordOf(header)
	if !ok {
		return d
	}
	coord, ok := m.Coordinate(c)
	if !ok || coord.WorkScale <= 1 {
		return d
	}
	s := new(big.Int).Div(d, new(big.Int).SetUint64(coord.WorkScale))
	if s.Sign() <= 0 {
		return big.NewInt(1)
	}
	return s
}

// EqualWeight reports whether two heads carry the same weight under the
// vector form (the product and the second key both equal), decided false
// on a chain without the form. The sealer's twin policy (miner/worker.go
// part 5) reads it: it builds on the first-seen twin only at equal vector
// weight, never on a twin the fork rule ranks below the head (F-88).
func (e *Engine) EqualWeight(chain consensus.ChainHeaderReader, a, b *types.Header) (equal, decided bool, err error) {
	if e.vectorMenu() == nil {
		return false, false, nil
	}
	aw, as, err := e.vectorWeight(chain, a)
	if err != nil {
		return false, true, err
	}
	bw, bs, err := e.vectorWeight(chain, b)
	if err != nil {
		return false, true, err
	}
	if aw.Cmp(bw) != 0 {
		return false, true, nil
	}
	if as != nil && bs != nil && as.Cmp(bs) != 0 {
		return false, true, nil
	}
	return true, true, nil
}

// BlockWeight under the vector form is the block's difficulty (the
// forker's total difficulty is not consulted for the choice, PreferByWeight
// is; the sum stays the chain's recorded "td" for the readers).

// SetCoordinate names the coordinate this node seals under (--igneum.coord).
//
// F-146 (15 October 2026, node7 of the research chain): the menu at this
// call holds the STORED registration (the genesis or bootstrap document
// the datadir keeps); under cut B the registration in force is chain-held,
// layered on it by the landings, and a coordinate a registry transaction
// added is not in the stored menu's count. A chain-held registration (one
// naming a registry key) therefore defers the range check: the coordinate
// is recorded and the seal set by sealFromRegistry when the registry is
// read (the journal's replay, the start-up rescan, each landing). A
// registration naming no key has no landings and refuses here as before.
func (e *Engine) SetCoordinate(c uint8) error {
	m := e.vectorMenu()
	if m == nil {
		return errVectorCoordOff
	}
	if int(c) >= m.Count() {
		r := m.Registration()
		if r == nil || (r.RegistryKey == (common.Address{}) && r.RegistryKeyNext == (common.Address{})) {
			return errVectorCoord
		}
		e.vectorMu.Lock()
		e.coordinate = c
		e.sealPending = true
		e.vectorMu.Unlock()
		e.log.Warn("Seal coordinate beyond the stored registration; the seal waits for the chain-held registry that holds it", "coordinate", c, "stored", m.Count())
		return nil
	}
	e.vectorMu.Lock()
	e.coordinate = c
	e.sealPending = false
	e.vectorMu.Unlock()
	m.SetSealCoordinate(c)
	return nil
}

// sealFromRegistry (F-146) sets the seal on a coordinate SetCoordinate
// deferred once the registration in force holds it, and defers it again
// when a dropped landing (UnlandRegistry) takes the coordinate away; called
// with the menu after every replacement of the registration in force. It
// reports whether the node's coordinate is in the registration in force.
func (e *Engine) sealFromRegistry(m *vector.Menu) bool {
	if m == nil {
		return false
	}
	e.vectorMu.Lock()
	defer e.vectorMu.Unlock()
	c := e.coordinate
	if int(c) < m.Count() {
		if e.sealPending {
			e.sealPending = false
			m.SetSealCoordinate(c)
			e.log.Info("Seal coordinate taken from the chain-held registry", "coordinate", c, "coordinates", m.Count())
		}
		return true
	}
	if !e.sealPending {
		e.sealPending = true
		e.log.Warn("Seal coordinate beyond the registration in force; the seal waits for a landing that holds it", "coordinate", c, "coordinates", m.Count())
	}
	return false
}

// Coordinate is the node's sealing coordinate.
func (e *Engine) Coordinate() uint8 {
	e.vectorMu.Lock()
	defer e.vectorMu.Unlock()
	return e.coordinate
}

// prepareVector sets a template's coordinate (the node's) and its
// difficulty (the coordinate's controller output at the parent).
func (e *Engine) prepareVector(chain consensus.ChainHeaderReader, header, parent *types.Header) error {
	m := e.vectorMenu()
	if m == nil {
		return nil
	}
	c := e.Coordinate()
	// F-166: no template on a coordinate without pay weight at the
	// template's height; the node idles there, one line per height.
	if h := parent.Number.Uint64() + 1; !m.SealableAt(c, h) {
		e.lock.Lock()
		logged := e.unpaidLogged == h
		e.unpaidLogged = h
		e.lock.Unlock()
		if !logged {
			e.log.Warn("No template: this node's coordinate has no pay weight at the height (F-166); idle until a payChanges entry restores it or the coordinate moves", "coordinate", c, "number", h)
		}
		return fmt.Errorf("%w: coordinate %d at %d", errVectorCoordUnpaid, c, h)
	}
	d, err := e.vectorDifficulty(chain, parent, c)
	if err != nil {
		return err
	}
	header.SetCoordTag(c)
	header.Difficulty = d
	return nil
}

// vectorAlgoFor is the coordinate's algorithm for a header on a vector
// chain, nil elsewhere.
func (e *Engine) vectorAlgoFor(header *types.Header) (pow.Algorithm, bool) {
	m := e.vectorMenu()
	if m == nil {
		return nil, false
	}
	c, ok := e.coordOf(header)
	if !ok {
		return nil, true
	}
	// The form in force at the header's height (LANE-MODIFICATION.md
	// section 1: a block of coordinate c at height h is valid only under
	// the document in force at h).
	return m.ByCoordinateAt(c, header.Number.Uint64()), true
}

// vectorUnkeyedFor is the twin a header's block is hashed with under the
// vector form when the block is unkeyed on a keyed coordinate (F-494): nil
// for a keyed block (shares carried), for this node's own template marked
// keyed at assembly, for an unkeyed coordinate, or without the vector form.
func (e *Engine) vectorUnkeyedFor(header *types.Header) pow.Algorithm {
	m := e.vectorMenu()
	if m == nil || len(header.Shares) > 0 {
		return nil
	}
	if keyed, known := e.keyed.get(e.SealHash(header)); known && keyed {
		return nil
	}
	c, ok := e.coordOf(header)
	if !ok {
		return nil
	}
	return m.UnkeyedAlgorithmAt(c, header.Number.Uint64())
}

// VectorWorkInfo is igneum_vectorWork's answer for a block: its coordinate,
// the counts and the cumulative work per coordinate after it, the
// normalised work W_c = N_c / DRef_c as a rational (numerator the work,
// denominator the reference), the geometric rule's product and the
// minimum rule's keys.
type VectorWorkInfo struct {
	Number           uint64                         `json:"number"`
	Hash             common.Hash                    `json:"hash"`
	Coord            uint8                          `json:"coord"`
	Difficulty       *big.Int                       `json:"difficulty"`
	Names            []string                       `json:"names"`
	Counts           []uint64                       `json:"counts"`
	Work             []*big.Int                     `json:"work"`
	DRef             []*big.Int                     `json:"dRef"`
	Product          *big.Int                       `json:"product"`
	MinScaled        *big.Int                       `json:"minScaled"`
	SumScaled        *big.Int                       `json:"sumScaled"`
	Rule             string                         `json:"rule"`
	Window           uint64                         `json:"window,omitempty"`           // revision 13: K_w under the coverage rule
	PayByCoordinate  bool                           `json:"payByCoordinate,omitempty"`  // revision 13 section 9
	PayShares        []uint64                       `json:"payShares,omitempty"`        // section 9 (4): the per-coordinate issuance weights in force at this height; absent = equal
	PayChanges       []ctypes.IgneumPayChange       `json:"payChanges,omitempty"`       // the registration's height-gated weight changes
	HalfLife         uint64                         `json:"halfLife"`                   // the controllers' half-life in force for the child of this block (F-151)
	ConstantsChanges []ctypes.IgneumConstantsChange `json:"constantsChanges,omitempty"` // the registration's height-gated clock constant changes (F-151)
	Targets          []uint64                       `json:"targets"`
	Next             []*big.Int                     `json:"nextDifficulty"` // each coordinate's controller output for the child of this block
	Last             []uint64                       `json:"lastHeights"`    // each coordinate's last block height (zero while it has none)
	Lags             []int64                        `json:"lags"`           // each coordinate's folded lag in seconds (revision 10)
}

func (e *Engine) vectorInfo(chain consensus.ChainHeaderReader, header *types.Header) (*VectorWorkInfo, error) {
	m := e.vectorMenu()
	if m == nil {
		return nil, nil
	}
	c, _ := e.coordOf(header)
	s, err := e.vectorStateOf(chain, header)
	if err != nil {
		return nil, err
	}
	info := &VectorWorkInfo{Number: header.Number.Uint64(), Hash: header.Hash(), Coord: c, Difficulty: new(big.Int).Set(header.Difficulty), HalfLife: m.HalfLifeAt(header.Number.Uint64() + 1), Rule: "geometric"}
	if m.Minimum() {
		info.Rule = "minimum"
	}
	if m.Coverage() {
		info.Rule = "coverage"
		info.Window = m.Window()
	}
	info.PayByCoordinate = m.PayByCoordinate()
	info.PayShares = m.PaySharesAt(header.Number.Uint64())
	info.PayChanges = m.PayChanges()
	info.ConstantsChanges = m.ConstantsChanges()
	for i := 0; i < m.Count(); i++ {
		coord, _ := m.CoordinateAt(uint8(i), header.Number.Uint64())
		info.Names = append(info.Names, coord.Name)
		info.Counts = append(info.Counts, s.counts[i])
		info.Work = append(info.Work, new(big.Int).Set(s.work[i]))
		info.DRef = append(info.DRef, new(big.Int).Set(coord.DRef))
		info.Targets = append(info.Targets, coord.Target)
		info.Last = append(info.Last, s.last[i])
		info.Lags = append(info.Lags, s.lag[i])
		d, err := e.vectorDifficulty(chain, header, uint8(i))
		if err != nil {
			return nil, err
		}
		info.Next = append(info.Next, d)
	}
	prod := big.NewInt(1)
	for i := 0; i < m.Count(); i++ {
		prod.Mul(prod, s.work[i])
	}
	info.Product = prod
	den := big.NewInt(1)
	for i := 0; i < m.Count(); i++ {
		coord, _ := m.CoordinateAt(uint8(i), header.Number.Uint64())
		den.Mul(den, coord.DRef)
	}
	var minv, sum *big.Int
	for i := 0; i < m.Count(); i++ {
		coord, _ := m.CoordinateAt(uint8(i), header.Number.Uint64())
		v := new(big.Int).Mul(s.work[i], new(big.Int).Div(den, coord.DRef))
		if minv == nil || v.Cmp(minv) < 0 {
			minv = v
		}
		if sum == nil {
			sum = new(big.Int).Set(v)
		} else {
			sum.Add(sum, v)
		}
	}
	info.MinScaled, info.SumScaled = minv, sum
	return info, nil
}

// coverageWindow is a branch's trailing window under revision 13's rule:
// its blocks since the common ancestor, the last K_w of them at most.
// covered is the number of coordinates with a block in the window and
// work the window's difficulty summed per coordinate (zero where none).
type coverageWindow struct {
	covered int
	work    []*big.Int
}

// branchSuffixes walks current and extern back to their common ancestor
// and returns the ancestor and each branch's headers above it, oldest
// first.
func branchSuffixes(chain consensus.ChainHeaderReader, current, extern *types.Header) (anc *types.Header, cur, ext []*types.Header, err error) {
	a, b := current, extern
	for a.Number.Uint64() > b.Number.Uint64() {
		cur = append(cur, a)
		if a = chain.GetHeader(a.ParentHash, a.Number.Uint64()-1); a == nil {
			return nil, nil, nil, errForkNoAncestor
		}
	}
	for b.Number.Uint64() > a.Number.Uint64() {
		ext = append(ext, b)
		if b = chain.GetHeader(b.ParentHash, b.Number.Uint64()-1); b == nil {
			return nil, nil, nil, errForkNoAncestor
		}
	}
	for a.Hash() != b.Hash() {
		cur, ext = append(cur, a), append(ext, b)
		if a.Number.Sign() == 0 {
			return nil, nil, nil, errForkNoAncestor
		}
		if a = chain.GetHeader(a.ParentHash, a.Number.Uint64()-1); a == nil {
			return nil, nil, nil, errForkNoAncestor
		}
		if b = chain.GetHeader(b.ParentHash, b.Number.Uint64()-1); b == nil {
			return nil, nil, nil, errForkNoAncestor
		}
	}
	// Oldest first.
	for i, j := 0, len(cur)-1; i < j; i, j = i+1, j-1 {
		cur[i], cur[j] = cur[j], cur[i]
	}
	for i, j := 0, len(ext)-1; i < j; i, j = i+1, j-1 {
		ext[i], ext[j] = ext[j], ext[i]
	}
	return a, cur, ext, nil
}

// windowOf reads a branch suffix's trailing window (the last K_w headers
// at most) into its coverage and per-coordinate work.
func (e *Engine) windowOf(m *vector.Menu, suffix []*types.Header) coverageWindow {
	n := m.Count()
	w := coverageWindow{work: make([]*big.Int, n)}
	for i := range w.work {
		w.work[i] = new(big.Int)
	}
	start := 0
	if k := int(m.Window()); k > 0 && len(suffix) > k {
		start = len(suffix) - k
	}
	for _, h := range suffix[start:] {
		c, ok := e.coordOf(h)
		if !ok || h.Difficulty == nil {
			continue
		}
		// F-166: a block on a coordinate without pay weight at its
		// height counts for no coverage (as a retired one's).
		if !m.SealableAt(c, h.Number.Uint64()) {
			continue
		}
		w.work[c].Add(w.work[c], h.Difficulty)
	}
	for _, x := range w.work {
		if x.Sign() > 0 {
			w.covered++
		}
	}
	return w
}

// preferByCoverage is revision 13's fork rule (VECTORWORK.md section 2,
// the formal seat's R4 COVERAGE-WINDOW-GEO on the project's two conditions,
// 13 October 2026, after the three-coordinate reorganisation of
// vectorwork4): the two branches are compared from their common ancestor
// forward, never from genesis, so no branch rides the shared prefix; each
// branch's trailing window is its last K_w blocks above the ancestor;
// the branch covering MORE coordinates in its window wins; at equal
// coverage the geometric mean of the window's normalised work over the
// covered coordinates, compared exactly by cross-multiplication
// (prod_{c in S_X} N_c(X) x prod_{c in S_Y} D_ref_c against
// prod_{c in S_Y} N_c(Y) x prod_{c in S_X} D_ref_c, the two covered
// sets having the same size); at equality the chain's lot tie-break.
// The genesis seed does not enter (the window carries no genesis term),
// and the lag cap stays as registered on the controllers.
func (e *Engine) compareByCoverage(chain consensus.ChainHeaderReader, m *vector.Menu, current, extern *types.Header, served bool) (int, error) {
	if current.Hash() == extern.Hash() {
		return 0, nil
	}
	anc, cur, ext, err := branchSuffixes(chain, current, extern)
	if err != nil {
		return 0, err
	}
	// The branches' own lengths and lane presence above the ancestor (the
	// withholding rule's figures), read before the window form adds the
	// shared prefix to both.
	lenC, lenE := len(cur), len(ext)
	var presC, presE []bool
	if served {
		presC, presE = e.suffixPresence(m, cur), e.suffixPresence(m, ext)
	}
	if m.WindowBranch() && len(cur) > 0 {
		// Revision 16's window form (b22d69be2): each branch's window
		// is its last K headers WITH the common prefix, so the prefix
		// below the fork, the same on both sides, enters both windows
		// and a depth-one race compares the two siblings on the window
		// each completes (the coverage count first). The prefix is
		// walked once from the common ancestor, at most K - len(suffix)
		// headers from the header cache, and shared by both windows.
		k := int(m.Window())
		if k == 0 {
			k = vector.DefaultWindow
		}
		need := k - len(cur)
		if n := k - len(ext); n > need {
			need = n
		}
		var prefix []*types.Header
		for a := chain.GetHeader(cur[0].ParentHash, cur[0].Number.Uint64()-1); a != nil && need > 0; need-- {
			prefix = append(prefix, a)
			if a.Number.Sign() == 0 {
				break
			}
			a = chain.GetHeader(a.ParentHash, a.Number.Uint64()-1)
		}
		for i, j := 0, len(prefix)-1; i < j; i, j = i+1, j-1 {
			prefix[i], prefix[j] = prefix[j], prefix[i]
		}
		cur = append(append([]*types.Header(nil), prefix...), cur...)
		ext = append(append([]*types.Header(nil), prefix...), ext...)
	}
	wc, we := e.windowOf(m, cur), e.windowOf(m, ext)
	if served {
		// The served form (the review lead's ruling b5460078c as main re-pointed
		// it, the served mark as ruled at b9ff75f28): the first key the
		// MINIMUM of the window's normalised work over the served lanes (a
		// lane with a block on either branch's window and in the band at
		// the common ancestor, servedAt), so a branch lacking a served
		// lane scores zero as the count made it and a floor lane's holder
		// casts no vote by adding, lifting or withholding its blocks; the
		// second key the scaled sum of the window's work over every lane
		// (the product over served lanes the balanced form of the next
		// cut, the project's call).
		band, err := e.servedAt(chain, m, anc)
		if err != nil {
			return 0, err
		}
		den := big.NewInt(1)
		for c := 0; c < m.Count(); c++ {
			coord, _ := m.Coordinate(uint8(c))
			den.Mul(den, coord.DRef)
		}
		n := m.Count()
		vc, ve := make([]*big.Int, n), make([]*big.Int, n)
		sumC, sumE := new(big.Int), new(big.Int)
		for c := 0; c < n; c++ {
			coord, _ := m.Coordinate(uint8(c))
			scale := new(big.Int).Div(den, coord.DRef)
			vc[c], ve[c] = new(big.Int).Mul(wc.work[c], scale), new(big.Int).Mul(we.work[c], scale)
			sumC.Add(sumC, vc[c])
			sumE.Add(sumE, ve[c])
		}
		// The first key past L on both branches (firstKey: the withholding
		// rule's exclusion), none in a short race.
		if minC, minE := firstKey(band, vc, ve, lenC, lenE, presC, presE); minC != nil && minE != nil {
			if cmp := minE.Cmp(minC); cmp != 0 {
				return cmp, nil
			}
		}
		return sumE.Cmp(sumC), nil
	}
	switch {
	case we.covered > wc.covered:
		return 1, nil
	case we.covered < wc.covered:
		return -1, nil
	}
	keyC, keyE := big.NewInt(1), big.NewInt(1)
	for c := 0; c < m.Count(); c++ {
		// The reference in force at the windows' first header (the common
		// prefix's start, shared by both sides), so one reference
		// normalises both windows' work across an in-place change.
		coord, _ := m.CoordinateAt(uint8(c), cur[0].Number.Uint64())
		if wc.work[c].Sign() > 0 {
			keyC.Mul(keyC, wc.work[c])
			keyE.Mul(keyE, coord.DRef)
		}
		if we.work[c].Sign() > 0 {
			keyE.Mul(keyE, we.work[c])
			keyC.Mul(keyC, coord.DRef)
		}
	}
	return keyE.Cmp(keyC), nil
}

// coordinateShare is C-34's integer share of a block's miner subsidy for
// coordinate c among n: M // n, plus one wei for c < M mod n, so the n
// shares sum to M exactly (docs/economics/ISSUANCE-SHARE-2026-10-13.md).
func coordinateShare(miner *big.Int, c, n int) *big.Int {
	q, r := new(big.Int).QuoRem(miner, big.NewInt(int64(n)), new(big.Int))
	if int64(c) < r.Int64() {
		q.Add(q, big.NewInt(1))
	}
	return q
}

// weightedShare is coordinateShare under the registration's weights w
// (VECTORWORK.md section 9, the governing share): coordinate c takes
// floor(M x w_c / W) with W the weights' sum, and the wei remainder
// (M - the floors' sum, under n) goes one wei each to the lowest-numbered
// coordinates, so the shares sum to M exactly; nil weights are the equal
// shares.
func weightedShare(miner *big.Int, c int, w []uint64) *big.Int {
	n := len(w)
	if n == 0 {
		return nil
	}
	W := new(big.Int)
	for _, x := range w {
		W.Add(W, new(big.Int).SetUint64(x))
	}
	floors := make([]*big.Int, n)
	sum := new(big.Int)
	for i := range w {
		floors[i] = new(big.Int).Div(new(big.Int).Mul(miner, new(big.Int).SetUint64(w[i])), W)
		sum.Add(sum, floors[i])
	}
	// The remainder, one wei each to the lowest-numbered coordinates WITH
	// a weight (a zero weight, an inactive coordinate, takes none).
	rem := rem64(new(big.Int).Sub(miner, sum))
	for i := 0; i < n && rem > 0; i++ {
		if w[i] == 0 {
			continue
		}
		if i == c {
			floors[c].Add(floors[c], big.NewInt(1))
		}
		rem--
	}
	return floors[c]
}

// rem64 is a small non-negative big.Int as an int (the wei remainder under
// the coordinate count).
func rem64(x *big.Int) int {
	if x.Sign() <= 0 || !x.IsInt64() {
		return 0
	}
	return int(x.Int64())
}

// coordinatePay is the Pay rule's coordinate dimension (VECTORWORK.md
// revision 13, section 9, the project's order of 13 October 2026 on the starved
// coordinate's economic recovery): under a registration with
// payByCoordinate, a block of coordinate c is paid the sum of c's shares
// of the miner subsidy over every height since c's last block on this
// branch (zero while it has none, so the first block of a coordinate
// takes its share from height 1), this block's height included. Read
// from the headers alone (the branch's vector state carries each
// coordinate's last height), so assembly and import land on one number.
// ok false where the rule is not registered or the header carries no
// coordinate; the caller pays the plain subsidy then.
func (e *Engine) coordinatePay(chain consensus.ChainHeaderReader, header *types.Header) (*big.Int, bool) {
	m := e.vectorMenu()
	if m == nil || !m.PayByCoordinate() || chain == nil || header == nil || header.Number == nil {
		return nil, false
	}
	c, ok := e.coordOf(header)
	if !ok {
		return nil, false
	}
	height := header.Number.Uint64()
	var last uint64
	if height > 1 {
		parent := chain.GetHeader(header.ParentHash, height-1)
		if parent == nil {
			return nil, false
		}
		st, err := e.vectorStateOf(chain, parent)
		if err != nil {
			return nil, false
		}
		last = st.last[c]
	}
	pot := new(big.Int)
	for h := last + 1; h <= height; h++ {
		// The weights in force at each accrued height (PayChanges): a
		// change at H splits the accrual, the heights below H at the
		// old weights and from H at the new.
		w := m.PaySharesAt(h)
		if len(w) != m.Count() {
			w = make([]uint64, m.Count())
			for i := range w {
				w[i] = 1
			}
		}
		miner, _ := e.config.Parts.Issuance.Reward(h)
		// The shares at a height are over the coordinates ACTIVE at it:
		// an inactive coordinate's weight is zero there and takes no
		// remainder (revision 16's activation), so the active shares sum
		// to M(h) exactly.
		wh := make([]uint64, len(w))
		for i := range w {
			if m.ActiveAt(uint8(i), h) {
				wh[i] = w[i]
			}
		}
		pot.Add(pot, weightedShare(miner, int(c), wh))
	}
	return pot, true
}
