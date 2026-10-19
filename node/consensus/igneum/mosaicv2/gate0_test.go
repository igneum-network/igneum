package mosaicv2

import (
	"math/big"
	"testing"
)

func TestDocumentAndScore(t *testing.T) {
	if err := V02.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Document{{Q: []uint64{5000, 5000, 0}}, {Q: []uint64{5000, 4000}}, {Q: nil}, {Q: []uint64{1, 1, 1, 1, 1, 1, 1, 1, 9992}}} {
		if bad.Validate() == nil {
			t.Fatalf("accepted %v", bad)
		}
	}
	// W = q gives S = 10200 / 1 exactly; the text's package check
	m, num, den, err := Score(V02, V02.Q)
	if err != nil {
		t.Fatal(err)
	}
	if m != 0 || new(big.Int).Div(num, den).Uint64() != 10200 || new(big.Int).Mod(num, den).Sign() != 0 {
		t.Fatal(m, num, den)
	}
	// unreduced: num = 2422500000, den = 237500 at W = q (min lane 0, q_m 2375)
	if num.String() != "2422500000" || den.String() != "237500" {
		t.Fatal(num, den)
	}
	if ScoreFloat(V02, V02.Q) != 10200.0 {
		t.Fatal(ScoreFloat(V02, V02.Q))
	}
	// the floating checks in the package's order
	if ConcentrationMultiplier(2375) != 214.73684210526318 || ConcentrationMultiplier(500) != 1020.0 {
		t.Fatal(ConcentrationMultiplier(2375), ConcentrationMultiplier(500))
	}
	if OutageRatio(2375) != 0.014950980392156862 || OutageRatio(500) != 0.018627450980392157 {
		t.Fatal(OutageRatio(2375), OutageRatio(500))
	}
	if PortfolioAdvantage(V02, []float64{2.2, 2.2, 2.2, 2.2, 200}) != 2.314449529219926 || PortfolioAdvantage(V02, []float64{1.9, 1.9, 1.9, 1.9, 200}) != 1.999000499750125 {
		t.Fatal("portfolio advantage")
	}
	// the minimum lane by quota-normalised service with ties to the smallest index
	if MinLane(V02, []uint64{2375, 2375, 2375, 2375, 500}) != 0 || MinLane(V02, []uint64{2375, 2375, 2374, 2375, 500}) != 2 || MinLane(V02, []uint64{2375, 2375, 2375, 2375, 499}) != 4 {
		t.Fatal("min lane")
	}
	// comparison by cross-multiplication
	_, n1, d1, _ := Score(V02, []uint64{10, 10, 10, 10, 2})
	_, n2, d2, _ := Score(V02, []uint64{10, 10, 10, 10, 3})
	if CompareScores(n1, d1, n2, d2) >= 0 || CompareScores(n2, d2, n1, d1) <= 0 || CompareScores(n1, d1, n1, d1) != 0 {
		t.Fatal("compare")
	}
	// the single lane: S = (A_NUM E_DEN Q W + E_NUM Q W) / (E_DEN Q): W(1 + e) in quota units
	_, n, d, _ := Score(SingleLane, []uint64{100})
	if new(big.Int).Mul(n, u(100)).Cmp(new(big.Int).Mul(d, u(102*100))) != 0 {
		t.Fatal("single lane score", n, d)
	}
}

func TestTransition(t *testing.T) {
	menu := []Entry{
		{0, 0, V02},       // genesis
		{100, 3000, V02},  // valid
		{2500, 4000, V02}, // ratified less than two windows ahead: invalid
		{1000, 3500, V02}, // not on a window boundary: invalid
		{1000, 3000, V02}, // not after the latest valid activation: invalid
		{1000, 5000, Document{Q: []uint64{5000, 5000, 1}}}, // bad document: invalid
		{2000, 5000, SingleLane},                           // valid
	}
	v := Validity(menu, Window)
	want := []bool{true, true, false, false, false, false, true}
	for i := range want {
		if v[i] != want[i] {
			t.Fatalf("entry %d validity %v, want %v", i, v[i], want[i])
		}
	}
	if Active(menu, v, 2999) != 0 || Active(menu, v, 3000) != 1 || Active(menu, v, 4999) != 1 || Active(menu, v, 5000) != 6 {
		t.Fatal("active document")
	}
}

func TestContinuityReservoir(t *testing.T) {
	r, _ := NewReservoir(ResearchReservoir, V02)
	// a full-credit block pays the whole budget; an eased block the fraction its work justifies
	rec, _ := r.Block(1, 0, 1, 1_000_000)
	if rec.B != 237_500 || rec.Imm != 237_500 || rec.Unpaid != 0 {
		t.Fatal(rec)
	}
	rec, _ = r.Block(2, 0, 2, 400_000)
	if rec.Imm != 95_000 || rec.Unpaid != 142_500 || !r.Lanes[0].Weak || rec.BalanceAfter != 142_500 {
		t.Fatal(rec)
	}
	rec, _ = r.Block(3, 0, 3, 600_000)
	if rec.Unpaid != 95_000 || rec.BalanceAfter != 237_500 || r.Lanes[0].Table[2] != 400_000 || r.Lanes[0].Table[3] != 600_000 {
		t.Fatal(rec)
	}
	// the weak interval ends at the first full-credit block (h 4, stable 1); no release while stable <= STABLE
	// (h 4 to 7, stable 1 to 4); the release comes at h 8 (stable 5 > 4), attributed over the table
	for h := uint64(4); h <= 7; h++ {
		rec, _ = r.Block(h, 0, 9, 1_000_000)
		if len(rec.Released) != 0 {
			t.Fatalf("height %d released inside the stable window: %+v", h, rec)
		}
	}
	rec, _ = r.Block(8, 0, 9, 1_000_000)
	if len(rec.Released) != 2 || rec.Released[0].Miner != 2 || rec.Released[1].Miner != 3 {
		t.Fatalf("release attribution: %+v", rec)
	}
	// r = min(balance, cap) = 50,000; pay_2 = floor(50000 * 400000 / 1000000) = 20000; pay_3 = 30000; miner 9 nothing
	if rec.Released[0].Amount != 20_000 || rec.Released[1].Amount != 30_000 || rec.BalanceAfter != 187_500 {
		t.Fatalf("release amounts: %+v", rec)
	}
	// expiry: the balance burns at expiry (h 4 + 200 = 204) when no further weak block came
	for h := uint64(9); h < 204; h++ {
		r.Block(h, 1, 1, 1_000_000)
	}
	rec, _ = r.Block(204, 1, 1, 1_000_000)
	if len(rec.Burned) != 1 || rec.Burned[0].Lane != 0 || r.Lanes[0].Balance != 0 {
		t.Fatalf("expiry burn: %+v", rec)
	}
	// reading R-1: a new weak interval burns an unreleased earlier balance and its table
	r2, _ := NewReservoir(ResearchReservoir, V02)
	r2.Block(1, 0, 1, 500_000)   // weak, balance 118750
	r2.Block(2, 0, 2, 1_000_000) // ends the interval; expiry 202
	rec, _ = r2.Block(3, 0, 3, 500_000)
	if len(rec.Burned) != 1 || rec.Burned[0].Amount != 118_750 || rec.BalanceAfter != 118_750 || len(r2.Lanes[0].Table) != 1 {
		t.Fatalf("R-1: %+v", rec)
	}
	// rule 7 and conservation over a random chain: budgets = immediate + balances + paid + burned, per lane
	r3, _ := NewReservoir(ResearchReservoir, V02)
	g := &Xorshift{S: 20261010}
	var budget, imm, paid, burned [5]uint64
	for h := uint64(1); h <= 2000; h++ {
		x := g.Next()
		lane := int(x % 5)
		d := ResearchReservoir.DRef * (40 + x%121) / 100
		rec, err := r3.Block(h, lane, x%4, d)
		if err != nil {
			t.Fatal(err)
		}
		budget[lane] += rec.B
		imm[lane] += rec.Imm
		for _, p := range rec.Released {
			paid[lane] += p.Amount
		}
		for _, b := range rec.Burned {
			burned[b.Lane] += b.Amount
		}
	}
	for i := range budget {
		if budget[i] != imm[i]+paid[i]+burned[i]+r3.Lanes[i].Balance {
			t.Fatalf("lane %d not conserved: %d against %d + %d + %d + %d", i, budget[i], imm[i], paid[i], burned[i], r3.Lanes[i].Balance)
		}
	}
	// the single lane: the same procedure at q = [Q]
	s, _ := NewReservoir(ResearchReservoir, SingleLane)
	rec, _ = s.Block(1, 0, 1, 250_000)
	if rec.B != 1_000_000 || rec.Imm != 250_000 || rec.Unpaid != 750_000 {
		t.Fatal(rec)
	}
}

func TestFeeReservoir(t *testing.T) {
	f, _ := NewFeeReservoir(ResearchFees, V02)
	rec, _ := f.Block(1, 0, 7, 1_000_000, 100_000)
	// imm 20,000; R 80,000; shares 19,000 x 4 + 4,000; dust 0; due = 19000 * D / D = 19000 <= cap
	if rec.Imm != 20_000 || rec.Shares[0] != 19_000 || rec.Shares[4] != 4_000 || rec.Dust != 0 || rec.Due != 19_000 || rec.Rel != 19_000 || rec.Overflow != 0 || rec.AssemblerTotal != 39_000 {
		t.Fatalf("%+v", rec)
	}
	// the cap holds back an overflow; the window weights by intrinsic work
	for h := uint64(2); h <= 5; h++ {
		rec, _ = f.Block(h, 1, 7, 1_000_000, 1_000_000)
	}
	if rec.Overflow == 0 || rec.Rel != ResearchFees.FeeCap {
		t.Fatalf("cap: %+v", rec)
	}
	// inactivity burn on a lane silent for FEE_TIMEOUT
	f.Block(6, 4, 1, 1_000_000, 1000)
	for h := uint64(7); h < 46; h++ {
		f.Block(h, 0, 1, 1_000_000, 1000)
	}
	rec, _ = f.Block(46, 0, 1, 1_000_000, 1000)
	burned4 := false
	for _, b := range rec.FeeBurned {
		if b.Lane == 4 && b.Amount > 0 {
			burned4 = true
		}
	}
	if !burned4 {
		t.Fatalf("fee burn on the silent lane: %+v", rec)
	}
	// conservation: fees = imm + dust + rel + balances + burned
	f2, _ := NewFeeReservoir(ResearchFees, V02)
	g := &Xorshift{S: 20261010}
	var fees, out, burned uint64
	for h := uint64(1); h <= 2000; h++ {
		x := g.Next()
		rec, err := f2.Block(h, int(x%5), x%4, 1_000_000*(40+x%121)/100, x%200000)
		if err != nil {
			t.Fatal(err)
		}
		fees += rec.F
		out += rec.AssemblerTotal
		for _, b := range rec.FeeBurned {
			burned += b.Amount
		}
	}
	var bal uint64
	for _, L := range f2.Lanes {
		bal += L.Balance
	}
	if fees != out+burned+bal {
		t.Fatalf("fees not conserved: %d against %d + %d + %d", fees, out, burned, bal)
	}
	// the launch chain: zero immediate inclusion on the single lane
	s, _ := NewFeeReservoir(FeeParams{ImmPct: 0, FeeCap: 30_000, FeeWindow: 8, FeeTimeout: 40}, SingleLane)
	rec, _ = s.Block(1, 0, 1, 1_000_000, 100_000)
	if rec.Imm != 0 || rec.Shares[0] != 100_000 || rec.Dust != 0 {
		t.Fatalf("%+v", rec)
	}
}

func TestGenerator(t *testing.T) {
	g := &Xorshift{S: 20261010}
	a, b := g.Next(), g.Next()
	if a == b || a == 0 || g.S == 0 {
		t.Fatal("xorshift64*")
	}
}
