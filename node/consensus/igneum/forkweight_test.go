package igneum

import (
	"math/big"
	"testing"
)

// TestForkWeightByRunningDifficultyKeepsTheFiveOf8342 (F-569, the review lead's
// ruling 7f95a4dc1 of 18 October 2026) replays the event of record on the
// public test network at 8,342: the dropped branch of five card-lane
// blocks (anvil 137,433,663; bandwidth 82,955,322; anvil 140,211,337;
// bandwidth 84,635,009; anvil 142,823,501) against the added branch of two
// raster blocks (66,108; 67,651), the registered references anvil
// 471,600,000, bandwidth 320,000,000, raster 36,000 and the lanes' running
// difficulties at the common ancestor about 137,400,000, 83,000,000 and
// 66,000. Under dRef the measure read 3.716 for the two against 1.416 for
// the five and the two replaced the five; under the running difficulty
// the five weigh about 5.1 against 2.0 and stand. Both branches are
// shorter than L (12 at three served lanes), so the sum decides.
func TestForkWeightByRunningDifficultyKeepsTheFiveOf8342(t *testing.T) {
	const anvil, bandwidth, raster = 0, 1, 2
	five := []*big.Int{big.NewInt(137433663 + 140211337 + 142823501), big.NewInt(82955322 + 84635009), big.NewInt(0)}
	two := []*big.Int{big.NewInt(0), big.NewInt(0), big.NewInt(66108 + 67651)}
	dRef := []*big.Int{big.NewInt(471600000), big.NewInt(320000000), big.NewInt(36000)}
	running := []*big.Int{big.NewInt(137400000), big.NewInt(83000000), big.NewInt(66000)}
	band := []bool{true, true, true}
	presFive := []bool{true, true, false}
	presTwo := []bool{false, false, true}
	weights := func(work, refs []*big.Int) laneWeights {
		v, sum := scaleLaneWork(work, refs)
		return laneWeights{v: v, present: []bool{true, true, true}, sum: sum, prod: big.NewInt(1)}
	}
	// Below the gate (the registered references): the two outweigh the
	// five, the event as it happened.
	if c := compareServed(weights(five, dRef), weights(two, dRef), band, 5, 2, presFive, presTwo, true); c <= 0 {
		t.Fatalf("under dRef the two raster blocks outweighed the five at 8,342 (the event of record): got %d", c)
	}
	// From the landing check (the running difficulties at the ancestor): the five
	// stand, the two lighter.
	if c := compareServed(weights(five, running), weights(two, running), band, 5, 2, presFive, presTwo, true); c >= 0 {
		t.Fatalf("under the running difficulty the five must stand against the two: got %d", c)
	}
	// The figures in the unit of the references' product, read back as
	// lane units: five / running about 5.08, two / running about 2.03.
	v5, s5 := scaleLaneWork(five, running)
	_, s2 := scaleLaneWork(two, running)
	unit := new(big.Int).Mul(new(big.Int).Mul(running[anvil], running[bandwidth]), running[raster])
	for lane, want := range map[int]float64{anvil: 3.06, bandwidth: 2.02} {
		got, _ := new(big.Float).Quo(new(big.Float).SetInt(v5[lane]), new(big.Float).SetInt(unit)).Float64()
		if got < want-0.02 || got > want+0.02 {
			t.Fatalf("lane %d weighs %.3f units, want about %.2f", lane, got, want)
		}
	}
	f5, _ := new(big.Float).Quo(new(big.Float).SetInt(s5), new(big.Float).SetInt(unit)).Float64()
	f2, _ := new(big.Float).Quo(new(big.Float).SetInt(s2), new(big.Float).SetInt(unit)).Float64()
	if f5 < 5.0 || f5 > 5.2 || f2 < 1.9 || f2 > 2.1 {
		t.Fatalf("the branches weigh %.3f and %.3f lane units, want about 5.08 and 2.03", f5, f2)
	}
}

// TestForkWeightGateOffKeepsTheReferences: without the gate the lane
// weights run on the registered references (refs nil), and a nil or zero
// reference for a lane falls back to its dRef.
func TestForkWeightGateOffKeepsTheReferences(t *testing.T) {
	work := []*big.Int{big.NewInt(100), big.NewInt(200)}
	v, sum := scaleLaneWork(work, []*big.Int{big.NewInt(10), big.NewInt(20)})
	if v[0].Int64() != 100*20 || v[1].Int64() != 200*10 || sum.Int64() != 4000 {
		t.Fatalf("scaleLaneWork: %v %v", v, sum)
	}
	e := &Engine{}
	if e.forkWeightOn(1) {
		t.Fatalf("the rule is off without a gate")
	}
	e.SetForkWeightFrom(150)
	if e.forkWeightOn(149) || !e.forkWeightOn(150) || e.ForkWeightFrom() != 150 {
		t.Fatalf("the gate reads from its height")
	}
}

// TestForkWeightBeyondLTheSumAloneDecides (the review lead's ruling b04c0769f;
// the security review's strike 75fs): from the fork-weight gate the scaled sum
// decides at every depth and the first key's minimum is retired. A deep
// race (both branches at 13, past L = 12 at three served lanes) where the
// forms differ: the current branch carries three units on every lane (nine
// in all), the competitor one unit on two lanes and eight on the third
// (ten in all); the minimum form keeps the current branch (its least lane
// 3 against 1), the sum form takes the competitor (10 against 9), and the
// competitor's handle under the minimum (a lane's unit moved by its own
// hash before the fork) is gone.
func TestForkWeightBeyondLTheSumAloneDecides(t *testing.T) {
	refs := []*big.Int{big.NewInt(1000), big.NewInt(1000), big.NewInt(1000)}
	cur := []*big.Int{big.NewInt(3000), big.NewInt(3000), big.NewInt(3000)}
	ext := []*big.Int{big.NewInt(1000), big.NewInt(1000), big.NewInt(8000)}
	weights := func(work []*big.Int) laneWeights {
		v, sum := scaleLaneWork(work, refs)
		return laneWeights{v: v, present: []bool{true, true, true}, sum: sum, prod: big.NewInt(1)}
	}
	band := []bool{true, true, true}
	pres := []bool{true, true, true}
	// The minimum form (below the gate) keeps the current branch.
	if c := compareServed(weights(cur), weights(ext), band, 13, 13, pres, pres, true); c >= 0 {
		t.Fatalf("under the minimum the current branch's least lane (3) beats the competitor's (1): got %d", c)
	}
	// The gate's form: the sum alone, the competitor heavier.
	wc, we := weights(cur), weights(ext)
	if c := we.sum.Cmp(wc.sum); c <= 0 {
		t.Fatalf("under the sum the competitor (10 units) outweighs the current branch (9): got %d", c)
	}
}

// TestForkWeightExcludesTheUnpaidLane (F-669, the review lead's ruling 9825b99d0):
// from the landing check a lane whose pay share is zero at the race's height (a
// sponsored lane) weighs nothing in the fork: a race the sponsored lane's
// work alone would decide reverses to the base lanes' sum.
func TestForkWeightExcludesTheUnpaidLane(t *testing.T) {
	refs := []*big.Int{big.NewInt(1000), big.NewInt(1000), big.NewInt(1000)}
	current := []*big.Int{big.NewInt(2000), big.NewInt(2000), big.NewInt(0)}  // the base lanes 2 + 2, nothing sponsored
	extern := []*big.Int{big.NewInt(1000), big.NewInt(1000), big.NewInt(10000)} // the base lanes 1 + 1, the sponsored lane 10
	shares := []uint64{1, 1, 0}
	_, sc := scaleLaneWork(current, refs)
	_, se := scaleLaneWork(extern, refs)
	if se.Cmp(sc) <= 0 {
		t.Fatalf("with the sponsored lane counted the competitor outweighs the current branch (12 against 4)")
	}
	excluded := unpaidLanes(shares, 3)
	if excluded[0] || excluded[1] || !excluded[2] {
		t.Fatalf("the unpaid lane is the third: %v", excluded)
	}
	_, sc = scaleLaneWork(excludeLaneWork(current, excluded), refs)
	_, se = scaleLaneWork(excludeLaneWork(extern, excluded), refs)
	if se.Cmp(sc) >= 0 {
		t.Fatalf("with the sponsored lane excluded the current branch stands (4 against 2): got %v vs %v", se, sc)
	}
	if ex := unpaidLanes([]uint64{1, 1}, 3); !ex[2] || ex[0] {
		t.Fatalf("an absent share entry reads zero")
	}
}
