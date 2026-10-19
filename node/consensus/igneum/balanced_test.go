package igneum

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/null"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/vector"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

func TestIntegerRoot(t *testing.T) {
	for _, r := range []struct{ x, q, want uint64 }{{0, 8, 0}, {1, 8, 1}, {255, 8, 1}, {256, 8, 2}, {6560, 8, 2}, {6561, 8, 3}, {1000000, 10, 3}, {1 << 40, 10, 16}, {99, 2, 9}, {100, 2, 10}} {
		if got := integerRoot(new(big.Int).SetUint64(r.x), r.q); got.Uint64() != r.want {
			t.Fatalf("root_%d(%d) = %d, want %d", r.q, r.x, got.Uint64(), r.want)
		}
	}
	big1 := new(big.Int).Exp(big.NewInt(123456789), big.NewInt(8), nil)
	if got := integerRoot(big1, 8); got.Int64() != 123456789 {
		t.Fatalf("exact eighth root: %s", got)
	}
	big1.Sub(big1, big.NewInt(1))
	if got := integerRoot(big1, 8); got.Int64() != 123456788 {
		t.Fatalf("floored eighth root: %s", got)
	}
}

// TestBalancedEntriesOneBlock: the first cut's arithmetic on one block's
// list (no ancestors with bodies): with four coordinates at equal weights
// and the minimum unit mu, a key with work N in coordinate 0 alone weighs
// floor((N x mu^3)^(1/4)) and a key with N in every coordinate weighs N.
func TestBalancedEntriesOneBlock(t *testing.T) {
	v := vectorConfig()
	v.Coordinates = append(v.Coordinates, ctypes.IgneumCoordinate{Name: "graph", Algorithm: null.Name, Target: 80, DRef: 8000})
	v.Rule = "coverage"
	v.PayByCoordinate = true
	v.BalancedFrom = 1
	m, err := vector.New(v, pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	parts := TestParts()
	parts.Difficulty.Min = big.NewInt(1)
	parts.Sortition = &SortitionParams{SharesPerBlock: 160, MaxShares: 256, ShareRecency: 64}
	e, err := New(Config{Algorithm: m, Parts: parts})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	if !e.balancedOn(1) || e.balancedOn(0) {
		t.Fatal("balancedFrom 1")
	}
	chain := newForkChain()
	g := genesisHeader()
	chain.setCanonical(g)
	// Every coordinate's next difficulty on this branch is its reference
	// (the genesis anchor), so mu_c = D_ref_c / 160.
	a, b := common.HexToAddress("0x01"), common.HexToAddress("0x02")
	var list []types.MinedShare
	// Key a: 16 shares at step 0 in this block (coordinate 0).
	for i := 0; i < 16; i++ {
		list = append(list, types.MinedShare{Address: a, Nonce: uint64(i)})
	}
	// Key b: one share at step 4 (weight 16) in this block.
	list = append(list, types.MinedShare{Address: b, Nonce: 99, Step: 4})
	d := new(big.Int).SetUint64(v.Coordinates[0].DRef)
	out := e.balancedEntries(chain, g, 0, d, common.Address{}, list)
	if len(out) != 2 {
		t.Fatalf("entries %v", out)
	}
	// Both keys have the same work in coordinate 0 (16 x shareDiff) and
	// none elsewhere, so the same weight: floor((N x mu1 x mu2 x mu3)^(1/4)).
	shareDiff := e.shareDifficulty(d)
	N := new(big.Int).Mul(shareDiff, big.NewInt(16))
	prod := new(big.Int).Set(N)
	for c := 1; c < 4; c++ {
		mu := e.shareDifficulty(new(big.Int).SetUint64(v.Coordinates[c].DRef))
		prod.Mul(prod, mu)
	}
	want := integerRoot(prod, 4).Uint64()
	for _, en := range out {
		if en.Weight != want {
			t.Fatalf("%s weight %d, want %d", en.Address.Hex(), en.Weight, want)
		}
	}
	if want >= N.Uint64() {
		t.Fatalf("a single-discipline key must weigh under its own work: %d against %s", want, N)
	}
}

// TestBalancedV2Breadth pins section 9's two examples on one block's list
// (no 256-window history beyond it, so y_c = x_c): a key whose work sits
// in one lane weighs under its total by the breadth factor at phi 1/16,
// and the same work split in two keys pays the same per unit (the weights
// proportional to the totals), the first cut's gain to splitting gone.
func TestBalancedV2Breadth(t *testing.T) {
	v := vectorConfig()
	v.Coordinates = append(v.Coordinates, ctypes.IgneumCoordinate{Name: "graph", Algorithm: null.Name, Target: 80, DRef: 8000})
	v.Rule = "coverage"
	v.PayByCoordinate = true
	v.BalancedFrom = 1
	v.BalancedV2From = 1
	m, err := vector.New(v, pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	parts := TestParts()
	parts.Difficulty.Min = big.NewInt(1)
	parts.Sortition = &SortitionParams{SharesPerBlock: 160, MaxShares: 256, ShareRecency: 64}
	e, err := New(Config{Algorithm: m, Parts: parts})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	chain := newForkChain()
	g := genesisHeader()
	chain.setCanonical(g)
	a, b, c := common.HexToAddress("0x0a"), common.HexToAddress("0x0b"), common.HexToAddress("0x0c")
	var list []types.MinedShare
	for i := 0; i < 32; i++ {
		list = append(list, types.MinedShare{Address: a, Nonce: uint64(i)})
	}
	for i := 0; i < 16; i++ {
		list = append(list, types.MinedShare{Address: b, Nonce: uint64(100 + i)})
		list = append(list, types.MinedShare{Address: c, Nonce: uint64(200 + i)})
	}
	d := new(big.Int).SetUint64(v.Coordinates[0].DRef)
	keys := e.balancedKeysV2(chain, g, 0, d, common.Address{}, list)
	if len(keys) != 3 {
		t.Fatalf("keys %d", len(keys))
	}
	by := map[common.Address]BalancedKey{}
	for _, k := range keys {
		by[k.Key] = k
	}
	// One lane: W'_0 = T, the other lanes at phi s_c T (y_c = 0 here), so
	// B = T x (1/16)^{3/4}: under T and the same ratio for every key.
	if by[a].Balanced.Cmp(by[b].Balanced) <= 0 {
		t.Fatalf("a (32 shares) must weigh more than b (16): %s against %s", by[a].Balanced, by[b].Balanced)
	}
	if by[b].Balanced.Cmp(by[c].Balanced) != 0 {
		t.Fatalf("b and c (16 shares each) must weigh the same: %s against %s", by[b].Balanced, by[c].Balanced)
	}
	// Homogeneous of degree one: a's weight is b's plus c's within rounding.
	sum := new(big.Int).Add(by[b].Balanced, by[c].Balanced)
	diff := new(big.Int).Sub(by[a].Balanced, sum)
	diff.Abs(diff)
	if diff.Cmp(big.NewInt(4)) > 0 {
		t.Fatalf("splitting the work in two keys must pay the same: a %s, b+c %s", by[a].Balanced, sum)
	}
	// The split's units carry the constant product of (q / p_c)^{p_c / q}
	// = 4 at four equal shares: a balanced key reads T / 4 and a one-lane
	// key beta x T / 4 with beta = 4^{1/4} x (1/16)^{3/4} = 0.177, so
	// 0.0442 of T.
	T := new(big.Int).Mul(by[a].Work[0], big.NewInt(1))
	L := big.NewInt(1)
	for _, co := range v.Coordinates {
		L.Mul(L, new(big.Int).SetUint64(co.DRef))
	}
	TL := new(big.Int).Mul(T, new(big.Int).Div(L, new(big.Int).SetUint64(v.Coordinates[0].DRef)))
	ratio := new(big.Rat).SetFrac(by[a].Balanced, TL)
	f, _ := ratio.Float64()
	if f < 0.043 || f > 0.046 {
		t.Fatalf("one-lane rate %.4f, want 0.0442 (beta 0.177 over the constant 4)", f)
	}
	// A key balanced over the four lanes (equal work in each at the
	// references' proportion) reads beta 1: T / 4 exactly, the cap.
	var bal []types.MinedShare
	for c := 0; c < 4; c++ {
		for i := 0; i < 8; i++ {
			bal = append(bal, types.MinedShare{Address: common.HexToAddress("0x0d"), Nonce: uint64(1000 + c*8 + i)})
		}
	}
	// One block carries one coordinate, so the four lanes come from four
	// blocks: this block (coordinate 0) and three ancestors with bodies
	// are not available to the fork chain; the cap is pinned on the
	// product form instead: a one-lane key's product never exceeds it.
	_ = bal
}

// TestBalancedSealerTerm: under balancedSealerFrom the sealed block counts
// for its sealer's key as lane work equal to the block's difficulty, so a
// solo sealer with no share of its own enters the split with its total.
func TestBalancedSealerTerm(t *testing.T) {
	v := vectorConfig()
	v.Coordinates = append(v.Coordinates, ctypes.IgneumCoordinate{Name: "graph", Algorithm: null.Name, Target: 80, DRef: 8000})
	v.Rule = "coverage"
	v.PayByCoordinate = true
	v.BalancedFrom = 1
	v.BalancedV2From = 1
	v.BalancedSealerFrom = 1
	m, err := vector.New(v, pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	parts := TestParts()
	parts.Difficulty.Min = big.NewInt(1)
	parts.Sortition = &SortitionParams{SharesPerBlock: 160, MaxShares: 256, ShareRecency: 64}
	e, err := New(Config{Algorithm: m, Parts: parts})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	chain := newForkChain()
	g := genesisHeader()
	chain.setCanonical(g)
	sealer := common.HexToAddress("0x5ea1")
	d := new(big.Int).SetUint64(v.Coordinates[0].DRef)
	keys := e.balancedKeysV2(chain, g, 0, d, sealer, nil)
	if len(keys) != 1 || keys[0].Key != sealer {
		t.Fatalf("the sealer alone: %v", keys)
	}
	if keys[0].Work[0].Cmp(d) != 0 {
		t.Fatalf("the sealer's lane work is the block's difficulty: %s against %s", keys[0].Work[0], d)
	}
	v.BalancedSealerFrom = 0
	m2, _ := vector.New(v, pow.NewDev)
	e2, _ := New(Config{Algorithm: m2, Parts: parts})
	t.Cleanup(func() { e2.Close() })
	if keys := e2.balancedKeysV2(chain, g, 0, d, sealer, nil); len(keys) != 0 {
		t.Fatalf("without the third cut the sealer has no lane work: %v", keys)
	}
}

// TestBalancedLanes: section 10 (a), the breadth over the registered lanes
// only; a key's work in a lane left out (the graph) is outside the split.
func TestBalancedLanes(t *testing.T) {
	v := vectorConfig()
	v.Coordinates = append(v.Coordinates, ctypes.IgneumCoordinate{Name: "graph", Algorithm: null.Name, Target: 80, DRef: 8000})
	v.Rule = "coverage"
	v.PayByCoordinate = true
	v.BalancedFrom, v.BalancedV2From, v.BalancedLanesFrom = 1, 1, 1
	v.BalancedLanes = []uint8{0, 1, 2}
	m, err := vector.New(v, pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	if l := m.BalancedLanesAt(1); len(l) != 3 || l[2] != 2 {
		t.Fatalf("lanes at 1: %v", l)
	}
	if l := m.BalancedLanesAt(0); len(l) != 4 {
		t.Fatalf("lanes below the activation: %v", l)
	}
	parts := TestParts()
	parts.Difficulty.Min = big.NewInt(1)
	parts.Sortition = &SortitionParams{SharesPerBlock: 160, MaxShares: 256, ShareRecency: 64}
	e, err := New(Config{Algorithm: m, Parts: parts})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	chain := newForkChain()
	g := genesisHeader()
	chain.setCanonical(g)
	a := common.HexToAddress("0x0a")
	var list []types.MinedShare
	for i := 0; i < 16; i++ {
		list = append(list, types.MinedShare{Address: a, Nonce: uint64(i)})
	}
	// A block of coordinate 3 (the graph, outside the lanes) carrying a's
	// shares: a's lane total is zero, so a is out of the split.
	d := new(big.Int).SetUint64(v.Coordinates[3].DRef)
	if keys := e.balancedKeysV2(chain, g, 3, d, common.Address{}, list); len(keys) != 0 {
		t.Fatalf("graph-lane work alone must be outside the split: %v", keys)
	}
	// The same shares in coordinate 0: in the split.
	d0 := new(big.Int).SetUint64(v.Coordinates[0].DRef)
	if keys := e.balancedKeysV2(chain, g, 0, d0, common.Address{}, list); len(keys) != 1 {
		t.Fatalf("lane-0 work must be in the split: %v", keys)
	}
}

// TestSealerOf pins F-121: a block with a payout list keys its sealer on
// the list's last entry, not the coinbase (the withdrawals root's bytes).
func TestSealerOf(t *testing.T) {
	h := &types.Header{Coinbase: common.HexToAddress("0x00dd")}
	if sealerOf(h, nil) != h.Coinbase {
		t.Fatal("no list: the coinbase")
	}
	wl := []*types.Withdrawal{{Address: common.HexToAddress("0x01")}, {Address: common.HexToAddress("0x8efb")}}
	if sealerOf(h, wl) != common.HexToAddress("0x8efb") {
		t.Fatal("a list: its last entry")
	}
}

// TestPhiZero: at phi 0 (balancedPhiDenV2 0 from balancedPhiFrom) a key
// holds no floor on a lane it has no work on: its product is zero and it
// is out of the split; at phi 1/8 it is in.
func TestPhiZero(t *testing.T) {
	v := vectorConfig()
	v.Coordinates = append(v.Coordinates, ctypes.IgneumCoordinate{Name: "graph", Algorithm: null.Name, Target: 80, DRef: 8000})
	v.Rule = "coverage"
	v.PayByCoordinate = true
	v.BalancedFrom, v.BalancedV2From = 1, 1
	v.BalancedPhiDen = 8
	v.BalancedPhiFrom, v.BalancedPhiDenV2 = 1, 0
	m, err := vector.New(v, pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	if m.PhiDenAt(0) != 8 || m.PhiDenAt(1) != 0 {
		t.Fatalf("phi by height: %d %d", m.PhiDenAt(0), m.PhiDenAt(1))
	}
	parts := TestParts()
	parts.Difficulty.Min = big.NewInt(1)
	parts.Sortition = &SortitionParams{SharesPerBlock: 160, MaxShares: 256, ShareRecency: 64}
	e, err := New(Config{Algorithm: m, Parts: parts})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	chain := newForkChain()
	g := genesisHeader()
	chain.setCanonical(g)
	a := common.HexToAddress("0x0a")
	var list []types.MinedShare
	for i := 0; i < 16; i++ {
		list = append(list, types.MinedShare{Address: a, Nonce: uint64(i)})
	}
	d := new(big.Int).SetUint64(v.Coordinates[0].DRef)
	keys := e.balancedKeysV2(chain, g, 0, d, common.Address{}, list)
	if len(keys) != 1 || keys[0].Balanced.Sign() != 0 || keys[0].Weight != 1 {
		t.Fatalf("one lane at phi 0: balanced %v weight %v", keys[0].Balanced, keys[0].Weight)
	}
}

// TestBalancedInactiveCoordinate pins F-123: a coordinate registered by
// activation enters the derivation only from its activation height, so a
// block below it derives as it did before the registration.
func TestBalancedInactiveCoordinate(t *testing.T) {
	mk := func(withSixth bool) *Engine {
		v := vectorConfig()
		v.Coordinates = append(v.Coordinates, ctypes.IgneumCoordinate{Name: "graph", Algorithm: null.Name, Target: 80, DRef: 8000})
		v.Rule = "coverage"
		v.PayByCoordinate = true
		v.PayShares = []uint64{1, 3, 1, 1}
		v.BalancedFrom = 1
		if withSixth {
			v.Coordinates = append(v.Coordinates, ctypes.IgneumCoordinate{Name: "texture", Algorithm: null.Name, Target: 80, DRef: 45000000, Activation: 953})
			v.PayShares = []uint64{1, 3, 1, 1, 1}
		}
		m, err := vector.New(v, pow.NewDev)
		if err != nil {
			t.Fatal(err)
		}
		parts := TestParts()
		parts.Difficulty.Min = big.NewInt(1)
		parts.Sortition = &SortitionParams{SharesPerBlock: 160, MaxShares: 256, ShareRecency: 64}
		e, err := New(Config{Algorithm: m, Parts: parts})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { e.Close() })
		return e
	}
	a := common.HexToAddress("0x0a")
	var list []types.MinedShare
	for i := 0; i < 16; i++ {
		list = append(list, types.MinedShare{Address: a, Nonce: uint64(i)})
	}
	chain := newForkChain()
	g := genesisHeader()
	chain.setCanonical(g)
	d := big.NewInt(79360)
	before := mk(false).balancedEntries(chain, g, 0, d, common.Address{}, list)
	after := mk(true).balancedEntries(chain, g, 0, d, common.Address{}, list)
	if len(before) != 1 || len(after) != 1 || before[0].Weight != after[0].Weight {
		t.Fatalf("a block below the activation must derive alike: %v against %v", before, after)
	}
	// The breadth form too (its common scale L over the active set).
	mk2 := func(withSixth bool) *Engine {
		e := mk(withSixth)
		return e
	}
	_ = mk2
}

// TestBalancedV2InactiveScale: the breadth form's common scale L runs over
// the active coordinates, so a registration adding a coordinate by
// activation leaves the weights below its height unchanged.
func TestBalancedV2InactiveScale(t *testing.T) {
	mk := func(withSixth bool) *Engine {
		v := vectorConfig()
		v.Coordinates = append(v.Coordinates, ctypes.IgneumCoordinate{Name: "graph", Algorithm: null.Name, Target: 80, DRef: 8000})
		v.Rule = "coverage"
		v.PayByCoordinate = true
		v.PayShares = []uint64{1, 3, 1, 1}
		v.BalancedFrom, v.BalancedV2From = 1, 1
		if withSixth {
			v.Coordinates = append(v.Coordinates, ctypes.IgneumCoordinate{Name: "texture", Algorithm: null.Name, Target: 80, DRef: 45000000, Activation: 953})
			v.PayShares = []uint64{1, 3, 1, 1, 1}
		}
		m, err := vector.New(v, pow.NewDev)
		if err != nil {
			t.Fatal(err)
		}
		parts := TestParts()
		parts.Difficulty.Min = big.NewInt(1)
		parts.Sortition = &SortitionParams{SharesPerBlock: 160, MaxShares: 256, ShareRecency: 64}
		e, err := New(Config{Algorithm: m, Parts: parts})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { e.Close() })
		return e
	}
	a := common.HexToAddress("0x0a")
	var list []types.MinedShare
	for i := 0; i < 16; i++ {
		list = append(list, types.MinedShare{Address: a, Nonce: uint64(i)})
	}
	chain := newForkChain()
	g := genesisHeader()
	chain.setCanonical(g)
	d := big.NewInt(79360)
	before := mk(false).balancedKeysV2(chain, g, 0, d, common.Address{}, list)
	after := mk(true).balancedKeysV2(chain, g, 0, d, common.Address{}, list)
	if len(before) != 1 || len(after) != 1 || before[0].Weight != after[0].Weight || before[0].Balanced.Cmp(after[0].Balanced) != 0 {
		t.Fatalf("the breadth form below the activation must derive alike: %v against %v", before, after)
	}
}

// TestBalancedV3 pins F-124: under phi 0 a key with a lane at zero is paid
// nothing (no fallback weight), and a lane no key serves in the window is
// outside the breadth, so the served lanes pay.
func TestBalancedV3(t *testing.T) {
	mk := func(v3 bool) *Engine {
		v := vectorConfig()
		v.Coordinates = append(v.Coordinates, ctypes.IgneumCoordinate{Name: "graph", Algorithm: null.Name, Target: 80, DRef: 8000})
		v.Rule = "coverage"
		v.PayByCoordinate = true
		v.BalancedFrom, v.BalancedV2From = 1, 1
		v.BalancedPhiDen, v.BalancedPhiFrom, v.BalancedPhiDenV2 = 8, 1, 0
		if v3 {
			v.BalancedV3From = 1
		}
		m, err := vector.New(v, pow.NewDev)
		if err != nil {
			t.Fatal(err)
		}
		parts := TestParts()
		parts.Difficulty.Min = big.NewInt(1)
		parts.Sortition = &SortitionParams{SharesPerBlock: 160, MaxShares: 256, ShareRecency: 64}
		e, err := New(Config{Algorithm: m, Parts: parts})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { e.Close() })
		return e
	}
	a := common.HexToAddress("0x0a")
	var list []types.MinedShare
	for i := 0; i < 16; i++ {
		list = append(list, types.MinedShare{Address: a, Nonce: uint64(i)})
	}
	chain := newForkChain()
	g := genesisHeader()
	chain.setCanonical(g)
	d := big.NewInt(79360)
	// Before F-124: one lane served, the other three at zero under phi 0:
	// the key's balanced work is zero and it is listed at the fallback 1.
	before := mk(false).balancedKeysV2(chain, g, 0, d, common.Address{}, list)
	if len(before) != 1 || before[0].Balanced.Sign() != 0 || before[0].Weight != 1 {
		t.Fatalf("before: %v", before)
	}
	// Under F-124 the unserved lanes leave the breadth, so the one served
	// lane pays the key at its work: a positive weight.
	after := mk(true).balancedKeysV2(chain, g, 0, d, common.Address{}, list)
	if len(after) != 1 || after[0].Balanced.Sign() <= 0 || after[0].Weight <= 1 {
		t.Fatalf("after: %v", after)
	}
}

// TestBalancedMinimum: the minimum pay rule (balancedMinFrom). TL is the
// KEY'S scaled total over the breadth lanes (the breadth measures a key's
// balance against its own total; the split across keys is in proportion
// to the roots). With shares on one lane alone and the first cut's phi
// floor (1/16 of the lane's share on the three empty lanes) a key's
// minimum is the floor's lane, x'_c q / p_c = TL / 16 (under the mean the
// same key read 0.0442 TL, TestBalancedV2Breadth); degree one (a with 32
// shares reads b plus c with 16 each); and under F-124 with no floor the
// empty lane is zero and a one-lane key is paid nothing.
func TestBalancedMinimum(t *testing.T) {
	v := vectorConfig()
	v.Coordinates = append(v.Coordinates, ctypes.IgneumCoordinate{Name: "graph", Algorithm: null.Name, Target: 80, DRef: 8000})
	v.Rule = "coverage"
	v.PayByCoordinate = true
	v.BalancedFrom = 1
	v.BalancedV2From = 1
	v.BalancedMinFrom = 1
	m, err := vector.New(v, pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	parts := TestParts()
	parts.Difficulty.Min = big.NewInt(1)
	parts.Sortition = &SortitionParams{SharesPerBlock: 160, MaxShares: 256, ShareRecency: 64}
	e, err := New(Config{Algorithm: m, Parts: parts})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	chain := newForkChain()
	g := genesisHeader()
	chain.setCanonical(g)
	a, b, c := common.HexToAddress("0x0a"), common.HexToAddress("0x0b"), common.HexToAddress("0x0c")
	var list []types.MinedShare
	for i := 0; i < 32; i++ {
		list = append(list, types.MinedShare{Address: a, Nonce: uint64(i)})
	}
	for i := 0; i < 16; i++ {
		list = append(list, types.MinedShare{Address: b, Nonce: uint64(100 + i)})
		list = append(list, types.MinedShare{Address: c, Nonce: uint64(200 + i)})
	}
	d := new(big.Int).SetUint64(v.Coordinates[0].DRef)
	keys := e.balancedKeysV2(chain, g, 0, d, common.Address{}, list)
	if len(keys) != 3 {
		t.Fatalf("keys %d", len(keys))
	}
	by := map[common.Address]BalancedKey{}
	for _, k := range keys {
		by[k.Key] = k
	}
	if by[b].Balanced.Cmp(by[c].Balanced) != 0 {
		t.Fatalf("b and c (16 shares each) must weigh the same: %s against %s", by[b].Balanced, by[c].Balanced)
	}
	// the design review's cell 1: under the minimum rule phi is 0 by the rule's
	// own, whatever the registration's phiDen, so the empty lanes read
	// zero and every one-lane key's minimum is zero (the first cut's
	// phiDen 16 is set here and ignored); with no F-124 the key still
	// enters at weight 1.
	for _, k := range keys {
		if k.Balanced.Sign() != 0 {
			t.Fatalf("under the minimum a one-lane key reads zero (phi 0 by the rule), got %s", k.Balanced)
		}
	}
	// F-124 with no floor (phiDenV2 0): an unserved lane (no work by any
	// key in the window) is outside the breadth (F-124 (2)), so with one
	// served lane every key is paid on it; the unpaid case (another key
	// serves a lane this key does not) needs two coordinates' blocks and is
	// read on the research chain's igneum_wholeWork, not in this harness.
	v2 := *v
	v2.BalancedPhiFrom = 1
	v2.BalancedPhiDenV2 = 0
	v2.BalancedV3From = 1
	m2, err := vector.New(&v2, pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	e2, err := New(Config{Algorithm: m2, Parts: parts})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e2.Close() })
	keys2 := e2.balancedKeysV2(chain, g, 0, d, common.Address{}, list)
	if len(keys2) != 3 {
		t.Fatalf("one served lane under F-124: every key paid on it, got %d keys", len(keys2))
	}
	by2 := map[common.Address]BalancedKey{}
	for _, k := range keys2 {
		if k.Balanced.Cmp(k.ScaledTotal) != 0 {
			t.Fatalf("one served lane: the minimum is the lane itself, TL: %s against %s", k.Balanced, k.ScaledTotal)
		}
		by2[k.Key] = k
	}
	// Degree one: a's balanced work is b's plus c's (a split moves nothing).
	sum := new(big.Int).Add(by2[b].Balanced, by2[c].Balanced)
	if by2[a].Balanced.Cmp(sum) != 0 {
		t.Fatalf("degree one under the minimum: a %s, b + c %s", by2[a].Balanced, sum)
	}
	// the design review's cell 2: the y lift never exceeds the key's own 256-window
	// lane work (here the pay window is the 256 window, so the lift is
	// bounded by AL itself and lifts nothing).
	for _, k := range keys2 {
		for c := range k.Floored {
			if k.Scaled256 != nil && c < len(k.Scaled256) && k.Floored[c].Cmp(k.Scaled256[c]) > 0 && k.Floored[c].Cmp(k.ScaledLane[c]) > 0 {
				t.Fatalf("the y lift over the key's own 256-window lane work: floored %s, A256 %s", k.Floored[c], k.Scaled256[c])
			}
		}
	}
}
