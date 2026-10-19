package selectioncheck

import (
	"math/big"
	"testing"
)

func chain(vals ...int64) []*big.Int {
	out := make([]*big.Int, len(vals))
	for i, v := range vals {
		out[i] = big.NewInt(v)
	}
	return out
}

func TestSignal(t *testing.T) {
	p := Params{Lookback: 8, Sustain: 4, GrowthNum: 4, GrowthDen: 1, Period: 100}
	flat := chain(1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000)
	if r, ok := p.Reference(flat, 12); !ok || r.Int64() != 1000 || p.SignalFires(flat, 12) || p.Opens(flat, 12) {
		t.Fatal("flat")
	}
	if _, ok := p.Reference(flat[:11], 11); ok || p.SignalFires(flat[:11], 11) {
		t.Fatal("undefined below L + S")
	}
	four := chain(1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 4000, 4000, 4000, 4000)
	if !p.SignalFires(four, 12) || !p.Opens(four, 12) {
		t.Fatal("fourfold sustained")
	}
	four[10] = big.NewInt(3999)
	if p.SignalFires(four, 12) {
		t.Fatal("one block below the line")
	}
	// the even-count median is the lower middle
	even := chain(1000, 1000, 1000, 1000, 2000, 2000, 2000, 2000, 4000, 4000, 4000, 4000)
	if r, _ := p.Reference(even, 12); r.Int64() != 1000 {
		t.Fatal("lower middle", r)
	}
	// the floor at a positive multiple; genesis by rule; the opening height after an activation
	if !p.FloorFires(100) || p.FloorFires(99) || p.FloorFires(0) || !p.Opens(nil, 0) {
		t.Fatal("floor and genesis")
	}
	long := make([]*big.Int, 250)
	for i := range long {
		long[i] = big.NewInt(1000)
	}
	if h, ok := p.OpeningHeight(long, 0); !ok || h != 100 {
		t.Fatal("first opening after genesis", h, ok)
	}
	if h, ok := p.OpeningHeight(long, 100); !ok || h != 200 {
		t.Fatal("a competition in progress is not reopened; the next floor", h, ok)
	}
	// exact products: 1001 x 4 = 4004 fires, 4003 does not
	edge := chain(1001, 1001, 1001, 1001, 1001, 1001, 1001, 1001, 4004, 4004, 4004, 4004)
	if !p.SignalFires(edge, 12) {
		t.Fatal("at least")
	}
	edge[11] = big.NewInt(4003)
	if p.SignalFires(edge, 12) {
		t.Fatal("below by one")
	}
}
