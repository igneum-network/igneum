// Package selectioncheck is protocol B's independent implementation of the
// succession opening condition (docs/igneumhash/ENVELOPE-R0A-v0.md section
// 9.3 item 1, "the signal, exactly", at 5fefb214), written from the text
// alone, never from the protocol team's difficulty/selection.go. Research values
// until ratified.
package selectioncheck

import (
	"math/big"
	"sort"
)

// Params are the research constants: the lookback L, the sustain S, the
// growth G = G_num / G_den and the floor's period P.
type Params struct {
	Lookback  uint64
	Sustain   uint64
	GrowthNum uint64
	GrowthDen uint64
	Period    uint64
}

// Research is the research point: six months, three months, 4 / 1, 24 months, in blocks at 15 s.
var Research = Params{Lookback: 1_036_800, Sustain: 518_400, GrowthNum: 4, GrowthDen: 1, Period: 4_147_200}

// Reference is the median R of the L difficulties d[h - L - S .. h - S)
// (parents oldest first): sorted ascending, the element at index
// (L - 1) div 2, the lower middle on an even count. ok is false when h is
// below L + S (the signal is undefined; it does not fire).
func (p Params) Reference(parents []*big.Int, h uint64) (*big.Int, bool) {
	if h < p.Lookback+p.Sustain || uint64(len(parents)) < h {
		return nil, false
	}
	span := make([]*big.Int, p.Lookback)
	copy(span, parents[h-p.Lookback-p.Sustain:h-p.Sustain])
	sort.Slice(span, func(i, j int) bool { return span[i].Cmp(span[j]) < 0 })
	return new(big.Int).Set(span[(p.Lookback-1)/2]), true
}

// SignalFires reports whether every one of the S difficulties d[h - S .. h)
// satisfies d x G_den >= R x G_num (on the products, exact integers).
func (p Params) SignalFires(parents []*big.Int, h uint64) bool {
	r, ok := p.Reference(parents, h)
	if !ok {
		return false
	}
	line := new(big.Int).Mul(r, new(big.Int).SetUint64(p.GrowthNum))
	den := new(big.Int).SetUint64(p.GrowthDen)
	for _, d := range parents[h-p.Sustain : h] {
		if new(big.Int).Mul(d, den).Cmp(line) < 0 {
			return false
		}
	}
	return true
}

// FloorFires reports whether h is a positive multiple of P.
func (p Params) FloorFires(h uint64) bool { return h > 0 && h%p.Period == 0 }

// Opens reports whether the condition opens at h: the signal or the floor
// fires there, or h = 0 by rule for the first entry (neither consulted at 0).
func (p Params) Opens(parents []*big.Int, h uint64) bool {
	if h == 0 {
		return true
	}
	return p.SignalFires(parents, h) || p.FloorFires(h)
}

// OpeningHeight is the first h above the previous competition's activation
// height at which the condition holds, searching up to the parents given
// (the chain examined); ok is false when none opens. A competition in
// progress is not reopened: the caller passes the last activation height.
func (p Params) OpeningHeight(parents []*big.Int, lastActivation uint64) (uint64, bool) {
	start := lastActivation + 1
	if lastActivation == 0 && len(parents) == 0 {
		return 0, true
	}
	for h := start; h <= uint64(len(parents)); h++ {
		if p.Opens(parents, h) {
			return h, true
		}
	}
	return 0, false
}
