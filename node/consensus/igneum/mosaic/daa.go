package mosaic

import (
	"math/big"

	"github.com/ethereum/go-ethereum/consensus/igneum/difficulty"
)

// The two per-lane difficulty finalists of Phase A section 6.3, defined
// exactly so their recovery paths are the rules' own output (security's
// TP-115 reads them through tests/igneumhash/protocol-a/out/tp115-paths.json).
// Both are research mechanisms for Phase B's simulator; neither is on a
// consensus path.

// LaneDAA is the per-lane state and constants both finalists share.
type LaneDAA struct {
	Target   uint64 // the lane's block interval in seconds (the chain's target over the lane's share)
	HalfLife uint64 // asert-lane: seconds of lag per halving
	DtMax    uint64 // the most one interval counts, seconds (both)
	Window   int    // lwma-lane: the number of lane intervals averaged
	FloorNum uint64 // the floor as a fraction of the pre-silence difficulty
	FloorDen uint64
}

// ResearchLaneDAA is the research point: a 5-lane chain at 15 s (75 s per
// lane), HL and DT_MAX 3600 s as the launch rule, a 60-block LWMA window.
var ResearchLaneDAA = LaneDAA{Target: 75, HalfLife: 3600, DtMax: 3600, Window: 60, FloorNum: 1, FloorDen: 4}

func (c LaneDAA) floor(base *big.Int) *big.Int {
	f := new(big.Int).Mul(base, new(big.Int).SetUint64(c.FloorNum))
	den := new(big.Int).SetUint64(c.FloorDen)
	f.Add(f, new(big.Int).Sub(den, big.NewInt(1))).Div(f, den)
	if f.Sign() <= 0 {
		return big.NewInt(1)
	}
	return f
}

// AsertLaneNext is asert-lane: the launch rule's relative ASERT step
// (difficulty.Params.NextRelative, the Igneum fixed-point step) with the
// lane's own target over the interval since the lane's last block, the
// interval clamped to [1, DtMax], the result floored at the fraction of
// the pre-silence difficulty. During a silence the candidate target eases
// as the chain's time advances (global progress), bounded by the clamp.
func (c LaneDAA) AsertLaneNext(last *big.Int, dt uint64, preSilence *big.Int) *big.Int {
	p := difficulty.Params{Target: c.Target, HalfLife: c.HalfLife, DtMax: c.DtMax, Min: big.NewInt(1)}
	d := p.NextRelative(last, dt)
	if f := c.floor(preSilence); d.Cmp(f) < 0 {
		return f
	}
	return d
}

// LwmaLaneNext is lwma-lane: D_next = D_avg x Target x sum(k) / sum(k x
// dt_k) over the last Window lane intervals, each clamped to [1, DtMax],
// the newest weighted most (k = 1 .. n), with the pending silence counted
// as the newest interval when it exceeds the target (so a silent lane
// eases on global progress); the result floored as asert-lane.
func (c LaneDAA) LwmaLaneNext(diffs []*big.Int, intervals []uint64, pending uint64, preSilence *big.Int) *big.Int {
	n := len(intervals)
	if n > c.Window {
		diffs, intervals = diffs[n-c.Window:], intervals[n-c.Window:]
		n = c.Window
	}
	ds := append([]*big.Int{}, diffs...)
	is := append([]uint64{}, intervals...)
	if pending > c.Target {
		is = append(is, pending)
		ds = append(ds, diffs[len(diffs)-1])
		if len(is) > c.Window {
			is, ds = is[1:], ds[1:]
		}
	}
	n = len(is)
	sumK := uint64(n * (n + 1) / 2)
	var sumKdt uint64
	avg := new(big.Int)
	for k := 1; k <= n; k++ {
		dt := is[k-1]
		if dt < 1 {
			dt = 1
		}
		if dt > c.DtMax {
			dt = c.DtMax
		}
		sumKdt += uint64(k) * dt
		avg.Add(avg, ds[k-1])
	}
	avg.Div(avg, big.NewInt(int64(n)))
	d := new(big.Int).Mul(avg, new(big.Int).SetUint64(c.Target*sumK))
	d.Div(d, new(big.Int).SetUint64(sumKdt))
	if f := c.floor(preSilence); d.Cmp(f) < 0 {
		return f
	}
	if d.Sign() <= 0 {
		return big.NewInt(1)
	}
	return d
}

// RecoveryPath is the exact path of a lane's difficulty, as a fraction of
// its pre-silence difficulty, for the blocks it mines after returning from
// a suppression of silenceBlocks chain blocks (chainTarget seconds each):
// the lane holds its honest hash rate throughout, so each returning block
// takes the lane's target interval scaled by its difficulty over the
// pre-silence one (the expected interval, no randomness: the rule's own
// path, not a fit). The path runs until the difficulty is back within one
// part in a thousand of the pre-silence value or 2,000 blocks. rule is
// "asert" or "lwma".
func (c LaneDAA) RecoveryPath(rule string, silenceBlocks, chainTarget uint64) []*big.Rat {
	d0 := new(big.Int).Lsh(big.NewInt(1), 40) // the pre-silence difficulty; the path is a ratio and does not depend on it
	// The steady history before the silence: Window blocks at the target.
	var diffs []*big.Int
	var intervals []uint64
	for i := 0; i < c.Window; i++ {
		diffs = append(diffs, new(big.Int).Set(d0))
		intervals = append(intervals, c.Target)
	}
	last := new(big.Int).Set(d0)
	silence := silenceBlocks * chainTarget
	var path []*big.Rat
	pending := silence
	for len(path) < 2000 {
		var next *big.Int
		if rule == "asert" {
			next = c.AsertLaneNext(last, pending, d0)
		} else {
			next = c.LwmaLaneNext(diffs, intervals, pending, d0)
		}
		r := new(big.Rat).SetFrac(next, d0)
		path = append(path, r)
		// The returning block at this difficulty takes the scaled interval.
		dt := new(big.Int).Mul(next, new(big.Int).SetUint64(c.Target))
		dt.Div(dt, d0)
		interval := dt.Uint64()
		if interval < 1 {
			interval = 1
		}
		diffs = append(diffs, next)
		intervals = append(intervals, pending)
		pending = interval
		last = next
		diff := new(big.Rat).Sub(r, big.NewRat(1, 1))
		if diff.Abs(diff).Cmp(big.NewRat(1, 1000)) <= 0 && len(path) > 1 {
			break
		}
	}
	return path
}
