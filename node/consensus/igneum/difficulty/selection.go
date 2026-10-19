// The succession selection's opening condition (ENVELOPE-R0A-v0.md section
// 9.3): a consensus-visible, manipulation-bounded signal from headers alone
// that opens the candidate competition, beside a periodic floor. Research
// values until ratified; no consensus path reads this yet.
package difficulty

import "math/big"

// Selection holds the opening rule's constants, all in blocks and all
// consensus constants of the chain's menu once ratified.
type Selection struct {
	// Lookback is the trailing span the reference difficulty is the median
	// of, ending Sustain blocks before the height examined.
	Lookback uint64
	// Sustain is the span over which the growth must hold at every block
	// for the condition to open (a rented burst shorter than this does not).
	Sustain uint64
	// GrowthNum / GrowthDen is the line: the difficulty at every block of
	// the sustain span is at least Growth times the reference.
	GrowthNum, GrowthDen uint64
	// Period is the periodic floor: a competition opens at every multiple
	// of Period whether or not the signal fires.
	Period uint64
}

// ResearchSelection is the research point at 15-second blocks: a six-month
// reference (1,036,800 blocks), growth of four times sustained over one
// activation window (518,400 blocks, three months), a periodic opening
// every 24 months (4,147,200 blocks).
var ResearchSelection = Selection{Lookback: 1036800, Sustain: 518400, GrowthNum: 4, GrowthDen: 1, Period: 4147200}

// median of a slice of difficulties (the lower middle for an even count).
func median(ds []*big.Int) *big.Int {
	if len(ds) == 0 {
		return new(big.Int)
	}
	sorted := make([]*big.Int, len(ds))
	copy(sorted, ds)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j].Cmp(sorted[j-1]) < 0; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	return new(big.Int).Set(sorted[(len(sorted)-1)/2])
}

// SignalOpens reports whether the growth signal fires at the height whose
// parents' difficulties are given oldest first (the last entry the parent):
// the median over the Lookback span that ends Sustain blocks before the
// height is the reference, and every one of the last Sustain difficulties
// is at least Growth times it. With fewer than Lookback + Sustain parents
// the signal never fires (no reference exists).
func (s Selection) SignalOpens(parents []*big.Int) bool {
	n := uint64(len(parents))
	if n < s.Lookback+s.Sustain || s.Sustain == 0 {
		return false
	}
	ref := median(parents[n-s.Lookback-s.Sustain : n-s.Sustain])
	line := new(big.Int).Mul(ref, new(big.Int).SetUint64(s.GrowthNum))
	den := new(big.Int).SetUint64(s.GrowthDen)
	for _, d := range parents[n-s.Sustain:] {
		if new(big.Int).Mul(d, den).Cmp(line) < 0 {
			return false
		}
	}
	return true
}

// PeriodicOpens reports whether the periodic floor opens a competition at
// a height: every multiple of Period above zero.
func (s Selection) PeriodicOpens(height uint64) bool {
	return s.Period != 0 && height > 0 && height%s.Period == 0
}

// Opens is the chain's condition: the signal or the floor, and genesis by
// rule (the first entry's competition opens at height 0).
func (s Selection) Opens(height uint64, parents []*big.Int) bool {
	return height == 0 || s.PeriodicOpens(height) || s.SignalOpens(parents)
}
