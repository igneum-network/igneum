// Package tp115 is protocol B's independent implementation of the two
// per-lane difficulty finalists of PHASE-A-CONSENSUS.md section 6.3
// (protocol-a/mosaic-a-r3 at dc2fdaa8) and of their recovery paths for
// security's TP-115, written from that text alone, never from the protocol team's
// mosaic/daa.go: asert-lane (the launch rule's relative ASERT step over the
// lane's own target interval, D_next = max(floor x D_pre, step(D_last, dt)))
// and lwma-lane (D_next = max(floor x D_pre, D_avg x T_i x sum(k) / sum(k x
// dt_k)) over the lane's last 60 intervals, the newest weighted most, the
// pending silence counted as the newest interval once it exceeds T_i). The
// path model: the lane returns after S chain blocks of silence at its honest
// hash rate, each block taking T_i scaled by its difficulty over the
// pre-silence one (the expected interval, no randomness), until within one
// part in a thousand of 1.0. Research only.
package tp115

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/consensus/igneum/difficulty"
)

// Point is the research point: a 5-lane chain at 15 s, T_i = 75 s, HL = DT_MAX = 3600 s, window 60.
type Point struct {
	ChainSeconds uint64 // the chain's block interval
	Lanes        uint64
	HalfLife     uint64
	DtMax        uint64
	Window       int
}

var Research = Point{ChainSeconds: 15, Lanes: 5, HalfLife: 3600, DtMax: 3600, Window: 60}

// TargetInterval is T_i, the chain's target over the lane's share.
func (p Point) TargetInterval() uint64 { return p.ChainSeconds * p.Lanes }

// Floor is floor x D_pre as an exact rational floor = num / den, taken as floor(D_pre x num / den).
type Floor struct{ Num, Den int64 }

func (f Floor) Of(dPre *big.Int) *big.Int {
	v := new(big.Int).Mul(dPre, big.NewInt(f.Num))
	return v.Div(v, big.NewInt(f.Den))
}

func maxBig(a, b *big.Int) *big.Int {
	if a.Cmp(b) >= 0 {
		return new(big.Int).Set(a)
	}
	return new(big.Int).Set(b)
}

func clamp(dt, max uint64) uint64 {
	if dt < 1 {
		return 1
	}
	if dt > max {
		return max
	}
	return dt
}

// AsertStep is the launch rule's relative ASERT step at the lane's own
// interval: the Igneum fixed-point step of consensus/igneum/difficulty with
// Target = T_i, HalfLife = HL, DtMax = DT_MAX and no absolute minimum.
func (p Point) AsertStep(dLast *big.Int, dt uint64) *big.Int {
	params := difficulty.Params{Target: p.TargetInterval(), HalfLife: p.HalfLife, DtMax: p.DtMax}
	return params.NextRelative(dLast, dt)
}

// AsertNext is D_next = max(floor x D_pre, step(D_last, dt)).
func (p Point) AsertNext(dPre, dLast *big.Int, dt uint64, f Floor) *big.Int {
	return maxBig(f.Of(dPre), p.AsertStep(dLast, clamp(dt, p.DtMax)))
}

// LWMA holds the lane's last `window` blocks: their difficulties and the
// intervals that ended at them.
type LWMA struct {
	Diffs     []*big.Int
	Intervals []uint64
}

// LwmaNext is D_next = max(floor x D_pre, D_avg x T_i x sum(k) / sum(k x
// dt_k)) over the last window intervals (clamped to [1, DT_MAX]), the newest
// weighted most (k = 1 the oldest, k = window the newest), with the pending
// interval dt (the silence, or the expected interval) as the newest once it
// exceeds T_i, and D_avg the mean of the window's blocks' difficulties; every
// product before one floor division.
func (p Point) LwmaNext(dPre *big.Int, h LWMA, pending uint64, f Floor) *big.Int {
	n := p.Window
	intervals := append([]uint64(nil), h.Intervals...)
	diffs := append([]*big.Int(nil), h.Diffs...)
	if pending > p.TargetInterval() {
		intervals = append(intervals, pending)
		diffs = append(diffs, diffs[len(diffs)-1])
	}
	if len(intervals) > n {
		intervals = intervals[len(intervals)-n:]
		diffs = diffs[len(diffs)-n:]
	}
	m := len(intervals)
	sumK := int64(m * (m + 1) / 2)
	var sumKDt int64
	for i, dt := range intervals {
		sumKDt += int64(i+1) * int64(clamp(dt, p.DtMax))
	}
	avgNum := new(big.Int)
	for _, d := range diffs {
		avgNum.Add(avgNum, d)
	}
	// D_avg x T_i x sum(k) / sum(k x dt_k) with D_avg = sum(D) / m: one floor at the end
	v := new(big.Int).Mul(avgNum, big.NewInt(int64(p.TargetInterval())*sumK))
	v.Div(v, big.NewInt(int64(m)*sumKDt))
	return maxBig(f.Of(dPre), v)
}

// expectedInterval is floor(T_i x D / D_pre) whole seconds (the lane at its
// honest hash rate; reading R-1: the floor, as the paths show).
func (p Point) expectedInterval(d, dPre *big.Int) uint64 {
	v := new(big.Int).Mul(d, big.NewInt(int64(p.TargetInterval())))
	v.Div(v, dPre)
	return v.Uint64()
}

func ratio(d, dPre *big.Int) string {
	// six decimals, rounded to nearest (reading R-2: the file's form)
	v := new(big.Int).Mul(d, big.NewInt(2_000_000))
	v.Add(v, dPre)
	v.Div(v, new(big.Int).Mul(dPre, big.NewInt(2)))
	return fmt.Sprintf("%d.%06d", new(big.Int).Div(v, big.NewInt(1_000_000)), new(big.Int).Mod(v, big.NewInt(1_000_000)))
}

func within(d, dPre *big.Int) bool {
	// within one part in a thousand of 1.0: |D - D_pre| x 1000 <= D_pre
	diff := new(big.Int).Sub(d, dPre)
	diff.Abs(diff)
	diff.Mul(diff, big.NewInt(1000))
	return diff.Cmp(dPre) <= 0
}

const maxPath = 100000

// AsertPath is the lane's difficulty path after S chain blocks of silence,
// each entry D_j / D_pre to six decimals, until within one part in a
// thousand of 1.0.
func (p Point) AsertPath(dPre *big.Int, s uint64, f Floor) []string {
	var out []string
	dLast := new(big.Int).Set(dPre)
	dt := s * p.ChainSeconds
	for len(out) < maxPath {
		d := p.AsertNext(dPre, dLast, dt, f)
		out = append(out, ratio(d, dPre))
		if within(d, dPre) {
			break
		}
		dt = p.expectedInterval(d, dPre)
		dLast = d
	}
	return out
}

// LwmaPath is the same for lwma-lane: the window starts full of T_i
// intervals at D_pre; the silence is the pending interval of the first block.
func (p Point) LwmaPath(dPre *big.Int, s uint64, f Floor) []string {
	h := LWMA{}
	for i := 0; i < p.Window; i++ {
		h.Diffs = append(h.Diffs, new(big.Int).Set(dPre))
		h.Intervals = append(h.Intervals, p.TargetInterval())
	}
	var out []string
	dt := s * p.ChainSeconds
	for len(out) < maxPath {
		d := p.LwmaNext(dPre, h, dt, f)
		out = append(out, ratio(d, dPre))
		h.Diffs = append(h.Diffs[1:], d)
		h.Intervals = append(h.Intervals[1:], clamp(dt, p.DtMax))
		if within(d, dPre) {
			break
		}
		dt = p.expectedInterval(d, dPre)
	}
	return out
}

// Keys are the 18 keys of security's file: asert_S{10,40,160}_f{0.5,0.333,0.25} and lwma_...
var Floors = []struct {
	Key string
	F   Floor
}{{"0.5", Floor{1, 2}}, {"0.333", Floor{1, 3}}, {"0.25", Floor{1, 4}}}

var Silences = []uint64{10, 40, 160}

// Paths generates the paths at a D_pre for the silences given (the 18 keys
// at 10, 40, 160; the file of e9899850 adds 300, 600, 1200).
func (p Point) Paths(dPre *big.Int, silences []uint64) map[string][]string {
	out := map[string][]string{}
	for _, s := range silences {
		for _, fl := range Floors {
			out[fmt.Sprintf("asert_S%d_f%s", s, fl.Key)] = p.AsertPath(dPre, s, fl.F)
			out[fmt.Sprintf("lwma_S%d_f%s", s, fl.Key)] = p.LwmaPath(dPre, s, fl.F)
		}
	}
	return out
}

// PathFor generates the path of one key of the form <rule>_S<n>_f<floor>.
func (p Point) PathFor(dPre *big.Int, key string) ([]string, bool) {
	var rule, fkey string
	var s uint64
	if _, err := fmt.Sscanf(key, "%5s_S%d_f%s", &rule, &s, &fkey); err != nil {
		if _, err := fmt.Sscanf(key, "%4s_S%d_f%s", &rule, &s, &fkey); err != nil {
			return nil, false
		}
	}
	var f Floor
	found := false
	for _, fl := range Floors {
		if fl.Key == fkey {
			f, found = fl.F, true
		}
	}
	if !found {
		return nil, false
	}
	switch rule {
	case "asert":
		return p.AsertPath(dPre, s, f), true
	case "lwma":
		return p.LwmaPath(dPre, s, f), true
	}
	return nil, false
}
