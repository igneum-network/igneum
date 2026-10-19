package igneum

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/vector"
	"github.com/ethereum/go-ethereum/consensus/igneum/shares"
)

// TestShareLaneGateSwitchesTheTargetsBase (F-690, the review lead's ruling
// bd0784c6d): below the share-lane gate the share target's difficulty is
// the clock's chain D(h) (the chain-level ASERT at the part's 20-second
// target, climbing without bound against a 12.6-second chain: 1.17 x 10^11
// at 06:17Z on the public test network); from the landing check it is the lane's
// difficulty of the block (59,000 on lane 5 then), so the served target
// on lane 5 reads 2^256 / (59,000 / 160): a share every 369 hashes
// against one in 728 million; the gate reads from its height alone and a
// chain without an entry stays on the clock's figure.
func TestShareLaneGateSwitchesTheTargetsBase(t *testing.T) {
	e := &Engine{}
	if e.shareLaneOn(1) || e.ShareLaneFrom() != 0 {
		t.Fatalf("the rule is off without a gate")
	}
	e.SetShareLaneFrom(14400)
	if e.shareLaneOn(14399) || !e.shareLaneOn(14400) || e.ShareLaneFrom() != 14400 {
		t.Fatalf("the gate reads from its height")
	}
	clockD, _ := new(big.Int).SetString("116601799040", 10) // 728,761,244 x 160
	laneD := big.NewInt(59000)
	const n = 160
	below := shares.Target(clockD, n)
	from := shares.Target(laneD, n)
	if below.Cmp(from) >= 0 {
		t.Fatalf("the lane's target is the easier one")
	}
	// The share difficulty the targets imply: 2^256 / target.
	two256 := new(big.Int).Lsh(big.NewInt(1), 256)
	dBelow := new(big.Int).Div(two256, below)
	dFrom := new(big.Int).Div(two256, from)
	if dBelow.Cmp(big.NewInt(700_000_000)) < 0 || dFrom.Int64() < 360 || dFrom.Int64() > 370 {
		t.Fatalf("the implied share difficulties: below %v (about 728 million), from %v (about 369)", dBelow, dFrom)
	}
	if pow.Target(big.NewInt(1)).Cmp(two256) > 0 {
		t.Fatalf("pow.Target(1) is the whole range")
	}
}

// TestShareLaneDNilWithoutTheForm: shareLaneD answers nil without a chain,
// a parent, the gate or the vector form, so every caller keeps the clock's
// figure there (the chain's nodes agree below the gate as before).
func TestShareLaneDNilWithoutTheForm(t *testing.T) {
	e := &Engine{}
	if e.shareLaneD(nil, nil, 10, 0) != nil {
		t.Fatalf("no chain, no lane figure")
	}
	e.SetShareLaneFrom(5)
	if e.shareLaneD(nil, nil, 10, 0) != nil {
		t.Fatalf("no chain under the landing check, no lane figure")
	}
}

// TestShareLaneImportRefusesAnUnreadableFigure (the security review's cell on
// 964cd234f): from the landing check, under the vector form, the import path treats
// a lane figure it cannot read as the block's verification failure, never
// a fallback to the clock's D(h); below the gate it keeps the clock's.
func TestShareLaneImportRefusesAnUnreadableFigure(t *testing.T) {
	e := &Engine{}
	if d, err := e.shareLaneDForImport(nil, nil, 10, 1); d != nil || err != nil {
		t.Fatalf("below the gate the import keeps the clock's figure: %v %v", d, err)
	}
	e.SetShareLaneFrom(5)
	if d, err := e.shareLaneDForImport(nil, nil, 10, 1); d != nil || err != nil {
		t.Fatalf("without the vector form the import keeps the clock's figure: %v %v", d, err)
	}
	v := vectorConfig()
	v.Rule = "coverage"
	m, err := vector.New(v, pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	e.algo = m
	if d, err := e.shareLaneDForImport(nil, nil, 10, 1); d != nil || err == nil {
		t.Fatalf("from the landing check under the form an unreadable lane figure fails the block: %v %v", d, err)
	}
}
