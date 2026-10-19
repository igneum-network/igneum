package service

import (
	"math/big"
	"testing"
)

// F-497 (0.1.85): a member's pay on two lanes equals its claims under the chain's balanced rule (p_c times the window's
// income times its weight on c over the pool's), and the sum over members and lanes equals the income less the fee
// and the remainder, exact in wei. The row is the public test network's: equal pay shares on 5, 6 and 7, and one
// member's two windows of record (521,934 raster weight; 1,444,398,794 bandwidth weight) beside a second member.
func TestSplitByCoordPaysEachLaneItsOwn(t *testing.T) {
	income, _ := new(big.Int).SetString("3000000000000000000000", 10) // 3,000 units in wei
	mini := "0xaaaa"
	other := "0xbbbb"
	windows := []coordWindow{
		{coord: 5, payShare: 1, work: 521934 + 12000000, feeWork: 5219 + 120000, members: map[string]memberWork{mini: {521934, 5219}, other: {12000000, 120000}}},
		{coord: 6, payShare: 1, work: 1444398794 + 140000000000, feeWork: 14443987 + 1400000000, members: map[string]memberWork{mini: {1444398794, 14443987}, other: {140000000000, 1400000000}}},
		{coord: 7, payShare: 1, work: 2647806721422, feeWork: 26478067214, members: map[string]memberWork{other: {2647806721422, 26478067214}}},
	}
	dues, byCoord, fee, remainder := splitByCoord(income, windows)
	// each coordinate takes a third of the income exactly (floored)
	third := new(big.Int).Div(income, big.NewInt(3))
	// the mini's claim on 5: a third times (521934 - 5219) / (521934 + 12000000)
	claim5 := new(big.Int).Mul(third, big.NewInt(521934-5219))
	claim5.Div(claim5, big.NewInt(521934+12000000))
	claim6 := new(big.Int).Mul(third, big.NewInt(1444398794-14443987))
	claim6.Div(claim6, big.NewInt(1444398794+140000000000))
	want := new(big.Int).Add(claim5, claim6)
	if dues[mini].Cmp(want) != 0 {
		t.Fatalf("the mini's due %s, its claims %s", dues[mini], want)
	}
	if len(byCoord[mini]) != 2 || byCoord[mini][0].Coord != 5 || byCoord[mini][1].Coord != 6 || byCoord[mini][0].DueWei != claim5.String() {
		t.Fatalf("the mini's rows %+v", byCoord[mini])
	}
	// the raster window pays the mini by the pool's raster coverage, not one part in 2,767 of the bandwidth window
	if claim5.Cmp(claim6) <= 0 {
		t.Fatalf("the thin lane's claim must exceed the thick lane's for the same member: %s vs %s", claim5, claim6)
	}
	// the invariant: dues + fee + remainder = income, exact in wei
	sum := new(big.Int).Set(fee)
	sum.Add(sum, remainder)
	for _, d := range dues {
		sum.Add(sum, d)
	}
	if sum.Cmp(income) != 0 {
		t.Fatalf("dues + fee + remainder = %s, the income %s", sum, income)
	}
	if remainder.Sign() < 0 || remainder.Cmp(big.NewInt(10)) > 0 {
		t.Fatalf("the remainder is the rounding's alone: %s", remainder)
	}
	// one coordinate (or a one-form chain): today's rule, every member by its weight over the total
	one := []coordWindow{{coord: 0, payShare: 1, work: 300, feeWork: 3, members: map[string]memberWork{mini: {100, 1}, other: {200, 2}}}}
	d1, _, f1, r1 := splitByCoord(big.NewInt(3000), one)
	if d1[mini].Int64() != 990 || d1[other].Int64() != 1980 || f1.Int64() != 30 || r1.Int64() != 0 {
		t.Fatalf("the one-form rule: %v fee %s remainder %s", d1, f1, r1)
	}
	// a coordinate with no carried work takes no share of the income
	none := []coordWindow{{coord: 5, payShare: 1, work: 0}, {coord: 6, payShare: 1, work: 10, members: map[string]memberWork{mini: {10, 0}}}}
	d2, _, _, _ := splitByCoord(big.NewInt(1000), none)
	if d2[mini].Int64() != 1000 {
		t.Fatalf("an empty coordinate takes nothing: %v", d2)
	}
}
