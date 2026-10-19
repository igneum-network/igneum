package igneum

import (
	"testing"

	"github.com/ethereum/go-ethereum/consensus/igneum/pow/vector"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// TestSponsoredEscrowIsTheLaneIdentitysAlone: the escrow depends on the chain
// id and the coordinate index and on nothing else, and differs across both.
func TestSponsoredEscrowIsTheLaneIdentitysAlone(t *testing.T) {
	a, b := SponsoredEscrow(7322, 8), SponsoredEscrow(7322, 8)
	if a != b {
		t.Fatalf("the derivation is not deterministic")
	}
	if SponsoredEscrow(7322, 9) == a || SponsoredEscrow(7358, 8) == a {
		t.Fatalf("the escrow must differ by coordinate and by chain")
	}
}

// TestSponsoredLaneShape (SPONSORED-LANE-NODE-FORM.md section 1): a sponsored
// coordinate's pay share is zero at every height and it is outside the
// breadth; the shape refuses a share on it, a breadth entry naming it, and
// a payChanges entry paying it, and admits the zero share a base lane
// could not carry.
func TestSponsoredLaneShape(t *testing.T) {
	base := func() *ctypes.IgneumVector {
		return &ctypes.IgneumVector{
			HalfLife: 900, DtMax: 900, Rule: "coverage",
			Coordinates: []ctypes.IgneumCoordinate{
				{Name: "raster", Algorithm: "null", Target: 36, DRef: 36000, DMin: 7200},
				{Name: "chainhash", Algorithm: "null", Target: 36, DRef: 1000, DMin: 250, Sponsored: true},
			},
			PayShares:     []uint64{1, 0},
			BalancedLanes: []uint8{0},
		}
	}
	if err := vector.Validate(base()); err != nil {
		t.Fatalf("a sponsored lane with a zero share outside the breadth is the shape: %v", err)
	}
	v := base()
	v.PayShares = []uint64{1, 1}
	if err := vector.Validate(v); err == nil {
		t.Fatalf("a share on a sponsored lane must be refused")
	}
	v = base()
	v.BalancedLanes = []uint8{0, 1}
	if err := vector.Validate(v); err == nil {
		t.Fatalf("a sponsored lane in the breadth must be refused")
	}
	v = base()
	v.BalancedLanesChanges = []ctypes.IgneumLanesChange{{Height: 100, Lanes: []uint8{1}}}
	if err := vector.Validate(v); err == nil {
		t.Fatalf("a sponsored lane in a breadth change must be refused")
	}
	v = base()
	v.PayChanges = []ctypes.IgneumPayChange{{Height: 100, PayShares: []uint64{1, 1}}}
	if err := vector.Validate(v); err == nil {
		t.Fatalf("a payChanges entry paying a sponsored lane must be refused")
	}
	v = base()
	v.Coordinates[1].Sponsored = false
	if err := vector.Validate(v); err == nil {
		t.Fatalf("a zero share on a base lane stays a shape fault")
	}
}

// TestSponsoredGateRefusesALandingUnderIt (the review lead's ruling e881c07ca): a
// document marking a coordinate sponsored lands only at or past the chain's
// sponsored gate; under it, or on a chain without a gate, it is refused; a
// document without the mark passes anywhere.
func TestSponsoredGateRefusesALandingUnderIt(t *testing.T) {
	plain := &ctypes.IgneumVector{Coordinates: []ctypes.IgneumCoordinate{{Name: "raster"}}}
	marked := &ctypes.IgneumVector{Coordinates: []ctypes.IgneumCoordinate{{Name: "raster"}, {Name: "chainhash", Sponsored: true}}}
	if err := SponsoredGateRefusal(7322, 100, plain); err != nil {
		t.Fatalf("a document without the mark passes: %v", err)
	}
	if err := SponsoredGateRefusal(7322, 15359, marked); err == nil {
		t.Fatalf("a sponsored coordinate under the public test network's gate (15,360) must be refused")
	}
	if err := SponsoredGateRefusal(7322, 15360, marked); err != nil {
		t.Fatalf("at the landing check it lands: %v", err)
	}
	if err := SponsoredGateRefusal(7358, 23551, marked); err == nil {
		t.Fatalf("under the research chain's gate (23,552) it is refused")
	}
	if err := SponsoredGateRefusal(1, 1_000_000, marked); err == nil {
		t.Fatalf("a chain without a gate admits no sponsored lane")
	}
}

// TestSponsoredRuleOffWithoutTheFork (the review lead's 1c08db88c; the finalize
// writes rule of 11 October 2026): without a gate, or below it, the
// engine's sponsored rule is off and nothing of it writes state; the params
// source and the engine's gate name one height.
func TestSponsoredRuleOffWithoutTheGate(t *testing.T) {
	e := &Engine{}
	if e.sponsoredOn(1_000_000) {
		t.Fatalf("no gate: the rule is off")
	}
	e.SetSponsoredFrom(15360)
	if e.sponsoredOn(15359) || !e.sponsoredOn(15360) {
		t.Fatalf("the rule reads from its gate height")
	}
	if params.IgneumSponsoredFromForChainID(7322) != 15360 || params.IgneumSponsoredFromForChainID(7358) != 23552 || params.IgneumSponsoredFromForChainID(7398) != 150 || params.IgneumSponsoredFromForChainID(7396) != 1 || params.IgneumSponsoredFromForChainID(7399) != 1 || params.IgneumSponsoredFromForChainID(1) != 0 {
		t.Fatalf("the params source reads the gate's table: %d %d %d", params.IgneumSponsoredFromForChainID(7322), params.IgneumSponsoredFromForChainID(7358), params.IgneumSponsoredFromForChainID(7398))
	}
}

// TestSponsoredGateLeavesTheRuleForksWhole is F-703 (18 October 2026): the
// sponsored gate's chains are NOT entries of the rule-forks table, because
// the engine reads a nil forks entry as the policy forms from genesis and a
// listed entry by its fields (clock.go policyOn), so a chain listed with
// the other rules at zero ran with the policy off, the sortition's open
// seat gone and every template refused as unbonded (the 0.10.65 lab). The
// public test network's entry carries its rules in full and stays.
func TestSponsoredGateLeavesTheRuleForksWhole(t *testing.T) {
	for _, id := range []uint64{7358, 7398, 7396, 7399} {
		if params.IgneumSponsoredFromForChainID(id) == 0 {
			t.Fatalf("chain %d carries a sponsored gate", id)
		}
		if f := params.IgneumRuleForksForChainID(id); f != nil {
			t.Fatalf("chain %d must not be an entry of the rule forks (F-703): %+v", id, *f)
		}
		e := &Engine{}
		if forks := params.IgneumRuleForksForChainID(id); forks != nil {
			e.SetRuleForks(forks)
		}
		e.SetSponsoredFrom(params.IgneumSponsoredFromForChainID(id))
		if !e.policyOn(1) || !e.sponsoredOn(params.IgneumSponsoredFromForChainID(id)) {
			t.Fatalf("chain %d: the policy on from genesis and the sponsored rule on from its gate", id)
		}
	}
	if f := params.IgneumRuleForksForChainID(7322); f == nil || f.PolicyV1 != 1 || f.PayV2 != 1 {
		t.Fatalf("the public test network's rule forks stay whole")
	}
}
