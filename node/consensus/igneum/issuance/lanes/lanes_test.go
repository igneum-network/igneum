package lanes

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/consensus/igneum/issuance"
)

func five(rule Rule) Policy {
	// divert-after-timeout is admitted here only as the rejected control the Phase B decision keeps in tests
	return Policy{Enabled: true, LaneBps: []uint32{2000, 2000, 2000, 2000, 2000}, Rule: rule, ReleaseBps: 1000, TimeoutBlocks: 50, SmoothingBlocks: 10, AssemblerBps: 5000, AllowRejectedControls: rule == Divert}
}

func conserved(t *testing.T, r *Replay) {
	// miner part of the schedule = issued + reservoirs + never issued
	total := MinerTotal(r.S, r.State.Height)
	sum := new(big.Int).Set(r.State.Issued)
	sum.Add(sum, r.State.NeverIssued)
	for _, v := range r.State.Reservoir {
		sum.Add(sum, v)
	}
	if sum.Cmp(total) != 0 {
		t.Fatalf("%s: not conserved: %s against %s", r.P.Rule, sum, total)
	}
}

func TestPolicy(t *testing.T) {
	for _, bad := range []Policy{
		{Enabled: true, LaneBps: []uint32{5000, 4000}},
		{Enabled: true, LaneBps: nil},
		{Enabled: true, LaneBps: []uint32{10000}, Rule: Divert, TimeoutBlocks: 5},
		{Enabled: true, LaneBps: []uint32{10000}, Rule: SlowRelease},
		{Enabled: true, LaneBps: []uint32{10000}, FeeRule: FeesSmoothed},
	} {
		if bad.Validate() == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	if _, err := New(five(CarryForward), issuance.New(issuance.Candidate)); err != nil {
		t.Fatal(err)
	}
	// the rejected control is not selectable
	rej := five(Divert)
	rej.AllowRejectedControls = false
	if rej.Validate() == nil {
		t.Fatal("divert-after-timeout selectable")
	}
	r, _ := New(Policy{LaneBps: []uint32{10000}}, issuance.New(issuance.Candidate))
	if _, err := r.Next(0, new(big.Int)); err == nil {
		t.Fatal("a disabled policy paid")
	}
}

func TestRulesOverRoundRobin(t *testing.T) {
	s := issuance.New(issuance.Candidate)
	for _, rule := range []Rule{CarryForward, NeverIssue, SlowRelease, Divert} {
		r, _ := New(five(rule), s)
		for h := 1; h <= 500; h++ {
			if _, err := r.Next(h%5, new(big.Int)); err != nil {
				t.Fatal(err)
			}
		}
		conserved(t, r)
		// round robin: every lane draws; under carry forward and divert nothing is left over five heights
		// beyond the credits since each lane's last block
		if rule == NeverIssue && r.State.NeverIssued.Sign() == 0 {
			t.Fatal("never-issue burned nothing under round robin (the other lanes' credits)")
		}
		if rule != NeverIssue && r.State.NeverIssued.Sign() != 0 {
			t.Fatalf("%s burned", rule)
		}
	}
}

func TestNoImmediateSpill(t *testing.T) {
	// lane 4 stops at height 100 (its blocks go to lane 0); every other lane's draw at every height
	// up to the divert timeout must equal the control run where lane 4 keeps mining, under every
	// rule (section 8: immediate redistribution is rejected)
	s := issuance.New(issuance.Candidate)
	for _, rule := range []Rule{CarryForward, NeverIssue, SlowRelease, Divert} {
		ctl, _ := New(five(rule), s)
		r, _ := New(five(rule), s)
		for h := 1; h <= 140; h++ {
			lane := h % 5
			pc, _ := ctl.Next(lane, new(big.Int))
			if h > 100 && lane == 4 {
				lane = 0
			}
			p, _ := r.Next(lane, new(big.Int))
			if lane != 0 && lane != 4 && p.Subsidy.Cmp(pc.Subsidy) != 0 {
				t.Fatalf("%s: height %d lane %d drew %s with lane 4 silent, %s with lane 4 active (spill)", rule, h, lane, p.Subsidy, pc.Subsidy)
			}
			if h > 100 && lane == 0 && h%5 == 4 && p.Subsidy.Cmp(pc.Subsidy) > 0 && rule != CarryForward && rule != Divert {
				// lane 0 mining in lane 4's slots draws its own reservoir only; under carry forward
				// its own accumulated credits are legitimately its own
				t.Fatalf("%s: lane 0 drew more in lane 4's slot", rule)
			}
		}
		conserved(t, r)
		if rule == Divert && r.State.Diverted.Sign() != 0 {
			t.Fatal("divert fired before the timeout")
		}
		for h := 141; h <= 400; h++ {
			lane := h % 5
			if lane == 4 {
				lane = 0
			}
			r.Next(lane, new(big.Int))
		}
		conserved(t, r)
		if rule == Divert && r.State.Diverted.Sign() == 0 {
			t.Fatal("divert never fired after the timeout")
		}
		if rule != Divert && r.State.Diverted.Sign() != 0 {
			t.Fatalf("%s diverted", rule)
		}
		if rule == CarryForward && r.State.Reservoir[4].Sign() == 0 {
			t.Fatal("carry forward did not retain the silent lane's issuance")
		}
		if rule == Divert && r.State.Reservoir[4].Sign() != 0 && r.State.Height-r.State.LastBlock[4] >= 50 {
			t.Fatal("divert left the silent lane's reservoir")
		}
	}
}

func TestSlowReleaseAndReturn(t *testing.T) {
	s := issuance.New(issuance.Candidate)
	r, _ := New(five(SlowRelease), s)
	for h := 1; h <= 60; h++ {
		lane := h % 5
		if lane == 4 {
			lane = 1
		}
		r.Next(lane, new(big.Int))
	}
	// lane 4 returns: it draws its own share plus 10 percent of the carry, not the whole reservoir
	res := new(big.Int).Set(r.State.Reservoir[4])
	p, _ := r.Next(4, new(big.Int))
	miner, _ := s.Reward(61)
	own := bps(miner, 2000)
	want := new(big.Int).Add(own, bps(new(big.Int).Sub(new(big.Int).Add(res, own), own), 1000))
	if p.Subsidy.Cmp(want) != 0 || p.Reservoir.Sign() == 0 {
		t.Fatalf("slow release drew %s, want %s (reservoir after %s)", p.Subsidy, want, p.Reservoir)
	}
	conserved(t, r)
}

func TestFeeRules(t *testing.T) {
	s := issuance.New(issuance.Candidate)
	fee := big.NewInt(1_000_000)
	for _, fr := range []FeeRule{FeesToAssembler, FeesFixedAssembler, FeesSmoothed, FeesLaneReservoirs} {
		p := five(CarryForward)
		p.FeeRule = fr
		r, _ := New(p, s)
		paid := new(big.Int)
		for h := 1; h <= 100; h++ {
			out, err := r.Next(h%5, fee)
			if err != nil {
				t.Fatal(err)
			}
			paid.Add(paid, out.Fees)
			switch fr {
			case FeesToAssembler:
				if out.Fees.Cmp(fee) != 0 {
					t.Fatal("baseline pays the block's fees")
				}
			case FeesFixedAssembler:
				if out.Fees.Cmp(bps(fee, 5000)) < 0 {
					t.Fatal("the fixed component is below the assembler's share")
				}
			case FeesSmoothed:
				if h == 1 && out.Fees.Cmp(new(big.Int).Div(fee, big.NewInt(10))) != 0 {
					t.Fatal("smoothing pays pool / smoothing_blocks")
				}
			}
		}
		// fees conserved: paid + pool + lane fee reservoirs = 100 x fee
		sum := new(big.Int).Add(paid, r.State.FeePool)
		for _, v := range r.State.FeeReservoir {
			sum.Add(sum, v)
		}
		if sum.Cmp(new(big.Int).Mul(fee, big.NewInt(100))) != 0 {
			t.Fatalf("%s: fees not conserved", fr)
		}
	}
}
