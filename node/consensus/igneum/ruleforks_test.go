package igneum

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// TestSealMeasureForkGatesTheFirstForm (the project's standing rule of 20:1x UK,
// 11 October 2026, from reliability's V1 stand-in refusing devnet7's block
// 13 under the swapped binary): below the chain's SealMeasureV1 fork the
// seal clock steps on EVERY bonded parent over its interval from the round
// floor (devnet7's blocks 10 to 350, sealed before PA-48's swap at 351:
// block 13 carries 54041 over the round-2 bonded block 12); from the fork
// the second form reads the leader's round-0 seals only; a chain without
// an entry (devnet8) runs the second form from genesis. 7317's entry is
// 351.
func TestSealMeasureForkGatesTheFirstForm(t *testing.T) {
	e := clockEngine(t)
	defer e.Close()
	s := e.config.Parts.Sortition
	// A bonded round-2 parent whose seal difficulty is the clock's seal
	// (parentBonded reads it so when the seal and chain differ), sealed 1 s
	// after its round floor (devnet7's block 12: round 2, one second over
	// its floor, so 13's seal rose from 54000 to 54041).
	// A seal well above D_min, so a one-second step shows in the integer
	// (at the test engine's D_min 1,000 a step of 0.05 percent rounds away).
	seal := new(big.Int).Mul(s.DMin, big.NewInt(1_000_000))
	chainD := new(big.Int).Div(seal, big.NewInt(3))
	key := common.Hash{0xb5}
	parentAt := func(height uint64) (*Clock, *types.Header) {
		c := &Clock{Chain: chainD, Seal: new(big.Int).Set(seal), ParentTime: 1000, Time: 1000 + 2*s.Delta + 1, Rank: 0}
		p := &types.Header{Number: new(big.Int).SetUint64(height - 1), Time: c.Time, Difficulty: new(big.Int).Set(seal), Round: 2, BondKey: &key}
		return c, p
	}
	if !e.ClockOn(13) {
		t.Fatal("the clock is off at 13")
	}
	// No forks: the second form, no step on a round-2 parent.
	c, p := parentAt(13)
	next, err := e.clockNextFrom(c, p)
	if err != nil {
		t.Fatal(err)
	}
	if next.Seal.Cmp(seal) != 0 {
		t.Fatalf("no forks, a round-2 parent: the seal stepped to %v", next.Seal)
	}
	// 7317's forks: height 13 steps under the first form (the interval 41
	// s from the round floor against the first form's target), 351 reads
	// the second.
	e.SetRuleForks(params.IgneumRuleForksForChainID(7317))
	if !e.sealMeasureFirstForm(13) || e.sealMeasureFirstForm(351) || !e.sealMeasureFirstForm(350) {
		t.Fatal("the fork's gate is not 351")
	}
	c, p = parentAt(13)
	next, err = e.clockNextFrom(c, p)
	if err != nil {
		t.Fatal(err)
	}
	want := e.sealParamsAt(13, true).NextRelative(seal, 1000)
	if next.Seal.Cmp(want) != 0 || next.Seal.Cmp(seal) <= 0 {
		t.Fatalf("height 13 under the first form: seal %v, want %v (risen from %v)", next.Seal, want, seal)
	}
	c, p = parentAt(351)
	next, err = e.clockNextFrom(c, p)
	if err != nil {
		t.Fatal(err)
	}
	if next.Seal.Cmp(seal) != 0 {
		t.Fatalf("height 351 under the second form: the seal stepped to %v", next.Seal)
	}
	// The first form's target is (Delta - T_PI) / div, the second's net of
	// tMin as well.
	if a, b := e.sealParamsAt(13, true).Target, e.sealParams().Target; a != (s.Delta*1000-s.TPiMs)/s.SealWindowDiv || b != (s.Delta*1000-s.TMin*1000-s.TPiMs)/s.SealWindowDiv {
		t.Fatalf("the targets: first %d, second %d", a, b)
	}
	// The second gate, ClockRankV1 (devnet7's 681, read off the archive
	// peer's storage: a fresh node under PA-48a wrote the clock's fifth
	// slot from block 10 and refused 10 on the state root): below it the
	// slot is not written and the step reads every round-0 bonded seal;
	// from it the slot is written and the leader's seals only are read.
	if f := params.IgneumRuleForksForChainID(7317); f.SealerSharesV1 != 1300 || f.ClockDarkV1 != 1300 {
		t.Fatalf("the third and fourth gates (the sealer's term, the dark count; planned heights) are %d and %d, not 1300", f.SealerSharesV1, f.ClockDarkV1)
	}
	// The dark count's slot is not written below its gate and the count
	// stays 0 there (a member on the new build and a sealer on the old one
	// agree on the state root to 1,299); from the landing check it is written.
	if e.clockDarkOn(1299) || !e.clockDarkOn(1300) {
		t.Fatal("the dark gate is not 1300")
	}
	{
		st := nativeState(t, big.NewInt(1))
		reg := e.config.Parts.Authority.Registry
		e.writeClockForms(st, &Clock{Chain: chainD, Seal: seal, Time: 7, ParentTime: 1, Rank: 0, Dark: 5}, true, false)
		if st.GetState(reg, clockSlot(5)) != (common.Hash{}) {
			t.Fatal("the dark slot was written below its gate")
		}
		if got, _ := e.readClock(st); got.Dark != 0 || got.Rank != 0 {
			t.Fatalf("below the dark gate: rank %d dark %d", got.Rank, got.Dark)
		}
		e.writeClockForms(st, &Clock{Chain: chainD, Seal: seal, Time: 7, ParentTime: 1, Rank: 0, Dark: 5}, true, true)
		if got, _ := e.readClock(st); got.Dark != 5 {
			t.Fatalf("from the dark gate: dark %d", got.Dark)
		}
		// A non-leader round-0 parent below the gate leaves the count 0.
		c := &Clock{Chain: chainD, Seal: new(big.Int).Set(seal), ParentTime: 1000, Time: 1000 + s.TMin + 1, Rank: 1, Dark: 3}
		p := &types.Header{Number: big.NewInt(1000), Time: c.Time, Difficulty: new(big.Int).Set(seal), Round: 0, BondKey: &key}
		if n, err := e.clockNextFrom(c, p); err != nil || n.Dark != 0 {
			t.Fatalf("below the dark gate the count moved: %v %v", n, err)
		}
		p.Number = big.NewInt(1400)
		if n, _ := e.clockNextFrom(c, p); n.Dark != 4 {
			t.Fatalf("from the dark gate the count did not move: %d", n.Dark)
		}
	}
	if e.clockRankOn(680) || !e.clockRankOn(681) || e.clockRankOn(10) {
		t.Fatal("the rank gate is not 681")
	}
	st := nativeState(t, big.NewInt(1))
	reg := e.config.Parts.Authority.Registry
	e.writeClock(st, &Clock{Chain: chainD, Seal: seal, Time: 7, ParentTime: 1, Rank: 2}, e.clockRankOn(680))
	if st.GetState(reg, clockSlot(4)) != (common.Hash{}) {
		t.Fatal("the rank slot was written below the gate")
	}
	e.writeClock(st, &Clock{Chain: chainD, Seal: seal, Time: 7, ParentTime: 1, Rank: 2}, e.clockRankOn(681))
	if got, _ := e.readClock(st); got.Rank != 2 {
		t.Fatalf("the rank slot from the landing check: rank %d", got.Rank)
	}
	// A round-0 bonded parent at rank 1 (or unranked, -1, as every parent
	// below the gate reads) steps the seal between the gates and not from
	// the rank gate.
	round0 := func(height uint64, rank int) *big.Int {
		c := &Clock{Chain: chainD, Seal: new(big.Int).Set(seal), ParentTime: 1000, Time: 1000 + s.TMin + 1, Rank: rank}
		p := &types.Header{Number: new(big.Int).SetUint64(height - 1), Time: c.Time, Difficulty: new(big.Int).Set(seal), Round: 0, BondKey: &key}
		n, err := e.clockNextFrom(c, p)
		if err != nil {
			t.Fatal(err)
		}
		return n.Seal
	}
	if round0(400, -1).Cmp(seal) == 0 {
		t.Fatal("between the gates an unranked round-0 seal did not step")
	}
	if round0(700, 1).Cmp(seal) != 0 || round0(700, -1).Cmp(seal) != 0 {
		t.Fatal("from the rank gate a non-leader round-0 seal stepped")
	}
	if round0(700, 0).Cmp(seal) == 0 {
		t.Fatal("from the rank gate the leader's round-0 seal did not step")
	}
	// An explicit zero fork height runs the latest form from genesis.
	e.SetRuleForks(&ctypes.IgneumRuleForks{})
	if e.sealMeasureFirstForm(13) || !e.clockRankOn(1) {
		t.Fatal("a zero fork height gated a rule")
	}
	if params.IgneumRuleForksForChainID(7318) != nil {
		t.Fatal("7318 carries a rule fork; it runs every rule's latest form from genesis")
	}
}
