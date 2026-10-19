package service

import (
	"math/big"
	"testing"
)

// F-496 under F-497's rule (0.1.86): lane_worth is a coordinate's income over the pool's carried share weight on it
// over the closed window, wei per unit of share weight: the figure a member's marginal pay there IS under the
// per-coordinate payout. The window of record (10:23:08Z, 17 October 2026) with an income of 3,000 units split a third
// each: raster's third over 12,476,645; bandwidth's over 140,076,107,286; programme's over 2,647,806,721,422.
func TestLaneWorthWeiIsTheCoordinateIncomeOverItsWeight(t *testing.T) {
	income, _ := new(big.Int).SetString("3000000000000000000000", 10)
	windows := []coordWindow{
		{coord: 5, payShare: 1, work: 12476645, feeWork: 124766, members: map[string]memberWork{"0xa": {12476645, 124766}}},
		{coord: 6, payShare: 1, work: 140076107286, feeWork: 1400761072, members: map[string]memberWork{"0xa": {140076107286, 1400761072}}},
		{coord: 7, payShare: 1, work: 2647806721422, feeWork: 26478067214, members: map[string]memberWork{"0xa": {2647806721422, 26478067214}}},
	}
	_, _, _, _, rows := splitByCoordRows(income, windows)
	if len(rows) != 3 {
		t.Fatalf("three rows, got %d", len(rows))
	}
	third := new(big.Int).Div(income, big.NewInt(3))
	want := map[uint8]string{}
	for _, c := range []struct {
		coord uint8
		work  uint64
	}{{5, 12476645}, {6, 140076107286}, {7, 2647806721422}} {
		f, _ := new(big.Float).Quo(new(big.Float).SetInt(third), new(big.Float).SetUint64(c.work)).Float64()
		want[c.coord] = formatWorth(f)
	}
	for _, r := range rows {
		if r.IncomeWei != third.String() {
			t.Fatalf("coordinate %d's income %s, want a third %s", r.Coord, r.IncomeWei, third)
		}
		if got := LaneWorthWei(r.IncomeWei, r.Work); got != want[r.Coord] {
			t.Fatalf("coordinate %d: lane_worth %s, want %s", r.Coord, got, want[r.Coord])
		}
	}
	// the thin lane is worth more per unit of share weight than the thick one, by the coverage ratio
	if LaneWorthWei(rows[0].IncomeWei, rows[0].Work) <= LaneWorthWei(rows[1].IncomeWei, rows[1].Work) {
		// scientific strings compare by exponent first only when the mantissas align; compare the floats instead
		a, _ := new(big.Float).Quo(new(big.Float).SetInt(third), new(big.Float).SetUint64(rows[0].Work)).Float64()
		b, _ := new(big.Float).Quo(new(big.Float).SetInt(third), new(big.Float).SetUint64(rows[1].Work)).Float64()
		if a <= b {
			t.Fatalf("raster %v must exceed bandwidth %v", a, b)
		}
	}
	// undefined: no work, no income, a bad string
	if LaneWorthWei("1000", 0) != "" || LaneWorthWei("0", 10) != "" || LaneWorthWei("x", 10) != "" {
		t.Fatalf("an undefined worth must be empty")
	}
}

func formatWorth(f float64) string { return LaneWorthWei(big.NewFloat(f*1e6).Text('f', 0), 1000000) }
