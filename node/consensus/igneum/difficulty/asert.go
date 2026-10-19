// The Igneum difficulty adjustment: an ASERT candidate in
// the difficulty domain, calibrated for a short block-time target and
// switching-miner conditions (the plan, page 10). It has no dependency beyond
// the standard library so that the engine (consensus/igneum, protocol
// engineer A) adapts it with one call and the simulator
// (tools/difficulty/asert_sim.py) shares its integer rules exactly.
//
// Two rules are implemented. Both compute 2^x in fixed point with a cubic,
// both floor at Params.Min, and both are exact integer functions of their
// inputs.
//
// Relative, clamped (the candidate; Next), with the Igneum step: 32
// fractional bits and a cubic exact in value and slope at both ends:
//
//	D_next = D_parent * 2^((T - clamp(dt, 1, DtMax)) / HalfLife)
//	dt     = parent.Time - grandparent.Time
//
// Absolute, anchored at genesis (NextAbsolute; kept for the differential
// tests and as the comparison in the design), exactly Bitcoin Cash's
// aserti3-2d in the difficulty domain (radix 2^16, the published cubic with
// error below 0.013 percent; the 2020-11-15 ASERT specification, R11):
//
//	D_next = D_genesis * 2^((T * parent.Number - (parent.Time - genesis.Time)) / HalfLife)
//
// The relative rule with no clamp is algebraically the absolute rule (the
// per-block intervals telescope to the elapsed time since genesis); the
// clamp bounds what one long gap can count, so a long outage lowers the
// difficulty by at most one factor of 2^(DtMax/HalfLife) instead of running
// the chain at the timestamp-limited rate of one block a second until the
// whole gap is repaid. The clamp can only raise the difficulty relative to
// the absolute rule, so it opens no lowering vector. The per-block exponent
// is rounded to nearest, not floored, because the relative rule integrates
// any one-sided rounding into a block-time bias (measured in the simulator
// at HalfLife 28800: 0.12 s with floor, none with nearest).
package difficulty

import (
	"math/big"
)

const (
	rbits = 16
	radix = 1 << rbits
)

var (
	c1       = big.NewInt(195766423245049)
	c2       = big.NewInt(971821376)
	c3       = big.NewInt(5127)
	round48  = new(big.Int).Lsh(big.NewInt(1), 47)
	bigRadix = big.NewInt(radix)
)

// The Igneum step, used by the relative rule: 32 fractional bits and a cubic
// with exact value and slope at both ends (p(0) = 1, p'(0) = ln 2, p(1) = 2,
// p'(1) = 2 ln 2), coefficients scaled by 2^96 with i1 forced so that p(1) is
// exactly 2. The relative rule integrates any per-step error into a
// block-time offset: at 16 bits the multiplier is quantised to 0.26 percent
// of a typical step and the BCH cubic's slope is 0.34 percent high at the
// ends, which together measured as 15.03 to 15.07 s mean intervals; this
// step measures 15.00. Largest error 0.061 percent at x = 0.47; under 1e-9
// for the steps a block takes.
const (
	ibits  = 32
	iradix = 1 << ibits
)

var (
	i1        = constBig("54916777476169277816795299840")
	i2        = constBig("4194997640318035968")
	i3        = big.NewInt(341198823)
	round96   = new(big.Int).Lsh(big.NewInt(1), 95)
	bigIradix = big.NewInt(iradix)
)

func constBig(s string) *big.Int {
	b, ok := new(big.Int).SetString(s, 10)
	if !ok {
		panic("difficulty: bad constant " + s)
	}
	return b
}

// Params are the consensus constants of the rule. They live in the chain
// configuration (IgneumConfig, PA-08) and never change after genesis.
type Params struct {
	Target   uint64   // block-time target in seconds (T)
	HalfLife uint64   // seconds of accumulated lag per halving of difficulty
	DtMax    uint64   // the most one block interval can count, seconds (relative rule)
	Min      *big.Int // the difficulty floor; also the recommended genesis difficulty
}

// Candidate is the calibration proposed in docs/protocol-b/DESIGN.md for a
// 15-second target. Min is a placeholder until the algorithm is chosen (the
// floor is the work one ordinary GPU does in one block interval on the
// ratified algorithm, set with GPU engineer A's measurements).
var Candidate = Params{Target: 15, HalfLife: 3600, DtMax: 3600, Min: big.NewInt(1 << 20)}

// floorDiv divides rounding toward negative infinity (Python's //), for b > 0.
func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// pow2Fixed returns (factor, shifts) with 2^(exponent/radix) = factor * 2^shifts / radix,
// where exponent is a signed fixed-point number with rbits fractional bits.
func pow2Fixed(exponent int64) (*big.Int, int64) {
	shifts := exponent >> rbits // arithmetic shift: floor
	frac := exponent - (shifts << rbits)
	f := big.NewInt(frac)
	f2 := new(big.Int).Mul(f, f)
	f3 := new(big.Int).Mul(f2, f)
	acc := new(big.Int).Mul(c1, f)
	acc.Add(acc, new(big.Int).Mul(c2, f2))
	acc.Add(acc, new(big.Int).Mul(c3, f3))
	acc.Add(acc, round48)
	acc.Rsh(acc, 48)
	acc.Add(acc, bigRadix)
	return acc, shifts
}

// pow2Igneum returns (factor, shifts) with 2^(exponent/iradix) = factor * 2^shifts / iradix,
// where exponent is a signed fixed-point number with ibits fractional bits.
func pow2Igneum(exponent int64) (*big.Int, int64) {
	shifts := exponent >> ibits
	frac := exponent - (shifts << ibits)
	f := big.NewInt(frac)
	f2 := new(big.Int).Mul(f, f)
	f3 := new(big.Int).Mul(f2, f)
	acc := new(big.Int).Mul(i1, f)
	acc.Add(acc, new(big.Int).Mul(i2, f2))
	acc.Add(acc, new(big.Int).Mul(i3, f3))
	acc.Add(acc, round96)
	acc.Rsh(acc, 96)
	acc.Add(acc, bigIradix)
	return acc, shifts
}

func applyShift(d *big.Int, shifts int64) *big.Int {
	if shifts >= 0 {
		return d.Lsh(d, uint(shifts))
	}
	return d.Rsh(d, uint(-shifts))
}

func (p Params) floor(d *big.Int) *big.Int {
	if p.Min != nil && d.Cmp(p.Min) < 0 {
		return new(big.Int).Set(p.Min)
	}
	return d
}

// NextRelative is the candidate rule for the block after the parent.
// dt is the parent's interval (parent.Time - grandparent.Time); the caller
// passes Target for the block after genesis, which has no grandparent.
func (p Params) NextRelative(parentDifficulty *big.Int, dt uint64) *big.Int {
	if dt < 1 {
		dt = 1
	}
	if dt > p.DtMax {
		dt = p.DtMax
	}
	hl := int64(p.HalfLife)
	num := (int64(p.Target)-int64(dt))*iradix*2 + hl
	exponent := floorDiv(num, 2*hl) // round to nearest
	factor, shifts := pow2Igneum(exponent)
	d := new(big.Int).Mul(parentDifficulty, factor)
	d = applyShift(d, shifts)
	d.Add(d, big.NewInt(1<<(ibits-1)))
	d.Rsh(d, ibits)
	return p.floor(d)
}

// NextAbsolute is the genesis-anchored rule for the block after the parent.
func (p Params) NextAbsolute(genesisDifficulty *big.Int, genesisTime, parentTime, parentNumber uint64) *big.Int {
	lag := int64(parentTime) - int64(genesisTime) - int64(p.Target)*int64(parentNumber)
	return p.NextFromLag(genesisDifficulty, lag)
}

// NextFromLag is NextAbsolute from a lag given directly (VectorWork's
// per-coordinate folded lag with its cap, VECTORWORK.md revision 10).
func (p Params) NextFromLag(genesisDifficulty *big.Int, lag int64) *big.Int {
	exponent := floorDiv(-lag*radix, int64(p.HalfLife))
	factor, shifts := pow2Fixed(exponent)
	d := new(big.Int).Mul(genesisDifficulty, factor)
	d = applyShift(d, shifts)
	d.Rsh(d, rbits)
	return p.floor(d)
}

// Next is the engine's call: the difficulty of the block whose parent is
// described by the arguments. For parentNumber == 0 (the block after genesis)
// grandparentTime is ignored and the interval is taken as Target. The engine
// verifies header.Difficulty == Next(...) and the miner fills it the same way.
func (p Params) Next(parentNumber, parentTime, grandparentTime uint64, parentDifficulty *big.Int) *big.Int {
	dt := p.Target
	if parentNumber > 0 {
		if parentTime > grandparentTime {
			dt = parentTime - grandparentTime
		} else {
			dt = 1 // unreachable under the header rule Time > parent.Time; kept total
		}
	}
	return p.NextRelative(parentDifficulty, dt)
}
