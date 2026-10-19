// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package igneum

import (
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// clockEngine is the genesis form with the amendment on: the launch
// clock's Delta 20 s, T_PI 250 ms, div 4 (the seal target 4,937 ms), the
// time floor 10 s, D_min 1,000 (a research value).
func clockEngine(t *testing.T) *Engine {
	t.Helper()
	e := genesisEngine(t)
	s := e.config.Parts.Sortition
	s.Delta, s.SealWindowDiv, s.TPiMs, s.TMin, s.DMin = 20, 4, 250, 10, big.NewInt(1_000)
	// PA-48b's constants as SortitionFor defaults them (K 16, the stagger
	// 2,000 ms): a K of 0 would read the fallback as always on.
	s.Rank0FallbackBlocks, s.SealStaggerMs = DefaultRank0FallbackBlocks, DefaultSealStaggerMs
	return e
}

func TestClockParameters(t *testing.T) {
	cfg := &ctypes.IgneumConfig{
		Authority: &ctypes.IgneumAuthority{Height: 1, Registry: testRegistry, Cap: big.NewInt(1), RegistryVersion: 4},
		Sortition: &ctypes.IgneumSortition{Height: 1, EpochBlocks: 100, SeedBlocks: 16, Expected: 1, Delta: 20, MaxRound: 16, UnbondedRound: 17, MaxPayouts: 256, OpenSeat: true, SealWindowDiv: 4},
	}
	if _, err := SortitionFor(cfg); err == nil {
		t.Fatal("the amendment without dMin accepted")
	}
	cfg.Sortition.DMin = big.NewInt(1_000)
	p, err := SortitionFor(cfg)
	if err != nil || p.TPiMs != 250 || p.SealWindowDiv != 4 || p.DMin.Int64() != 1_000 {
		t.Fatalf("the amendment: %v %+v", err, p)
	}
	cfg.Sortition.TMin = 21
	if _, err := SortitionFor(cfg); err == nil {
		t.Fatal("tMin over Delta accepted")
	}
	cfg.Sortition.TMin = 10
	cfg.Sortition.TPiMs = 20_000
	if _, err := SortitionFor(cfg); err == nil {
		t.Fatal("tPiMs at Delta accepted")
	}
	// The seal window (Delta - tMin) x 1000 - tPiMs must stay positive
	// (PA-48's target term): tMin 10 with tPiMs 10,000 leaves none.
	cfg.Sortition.TPiMs = 10_000
	if _, err := SortitionFor(cfg); err == nil {
		t.Fatal("tMin plus tPiMs at Delta accepted")
	}
	cfg.Sortition.TPiMs = 250
	cfg.Sortition.OpenSeat = false
	if _, err := SortitionFor(cfg); err == nil {
		t.Fatal("the amendment without the genesis form accepted")
	}
	cfg.Sortition.OpenSeat = true
	cfg.Sortition.SealWindowDiv = 0
	if p, err := SortitionFor(cfg); err != nil || p.SealWindowDiv != 0 {
		t.Fatalf("the amendment off: %v", err)
	}
}

// TestSealParamsInMilliseconds: the seal ASERT's target is (Delta - tMin -
// T_PI) / div in milliseconds (PA-48's term: 2,437 ms at the launch clock,
// the window left once the search starts at the time floor) with the
// chain's half-life and clamp scaled; a tMin of 12 s moves it to 1,937.
func TestSealParamsInMilliseconds(t *testing.T) {
	e := clockEngine(t)
	defer e.Close()
	p := e.sealParams()
	d := e.config.Parts.Difficulty
	if p.Target != 2437 || p.HalfLife != d.HalfLife*1000 || p.DtMax != d.DtMax*1000 || p.Min.Int64() != 1_000 {
		t.Fatalf("seal params %+v", p)
	}
	e.config.Parts.Sortition.TMin = 12
	if e.sealParams().Target != 1937 {
		t.Fatalf("target at tMin 12: %d", e.sealParams().Target)
	}
	e.config.Parts.Sortition.TMin = 10
	// At the target interval the difficulty holds; shorter raises, longer
	// lowers; the floor binds.
	hold := p.NextRelative(big.NewInt(1_000_000), 2437)
	if hold.Int64() < 999_000 || hold.Int64() > 1_001_000 {
		t.Fatalf("at the target: %v", hold)
	}
	if p.NextRelative(big.NewInt(1_000_000), 1000).Cmp(hold) <= 0 || p.NextRelative(big.NewInt(1_000_000), 20_000).Cmp(hold) >= 0 {
		t.Fatal("the direction")
	}
	if p.NextRelative(big.NewInt(1_001), 3_600_000).Int64() != 1_000 {
		t.Fatal("the floor")
	}
}

// TestClockTwoDifficulties walks the rule over four blocks on the genesis
// form: the seed at block 1, an open-seat block (the chain difficulty,
// D_seal unchanged), a bonded block (D_seal; its seal interval moves
// D_seal at the next block), the refusals of each form at the other's
// difficulty, Finalize's write, the time floor.
func TestClockTwoDifficulties(t *testing.T) {
	e := clockEngine(t)
	defer e.Close()
	s := e.config.Parts.Sortition
	st := nativeState(t, big.NewInt(1_000_000))
	chain := newSupplyChain()
	e.sortitionAnchor(chain)
	genesis := chain.canonical[0]
	genesis.Difficulty = big.NewInt(1 << 20)
	chain.add(genesis) // re-keyed under its hash after the mutation
	// extend keys a header by its hash at that moment; the test mutates
	// headers after, so each is re-added and its child re-pointed.
	relink := func(h *types.Header) {
		chain.add(h)
	}
	// Block 1: the seed. D(1) from the genesis difficulty at the target
	// interval; D_seal(1) = max(D_min, D(1) / 4).
	c1, err := e.ClockNext(st, genesis)
	if err != nil {
		t.Fatal(err)
	}
	wantChain := e.config.Parts.Difficulty.NextRelative(genesis.Difficulty, e.config.Parts.Difficulty.Target)
	if c1.Chain.Cmp(wantChain) != 0 || c1.Seal.Cmp(new(big.Int).Rsh(wantChain, 2)) != 0 {
		t.Fatalf("seed: %+v, want chain %v", c1, wantChain)
	}
	if d, _ := e.ExpectedDifficulty(st, genesis, true); d.Cmp(c1.Seal) != 0 {
		t.Fatal("ExpectedDifficulty bonded")
	}
	if d, _ := e.ExpectedDifficulty(st, genesis, false); d.Cmp(c1.Chain) != 0 {
		t.Fatal("ExpectedDifficulty open")
	}
	// Block 1 is an open-seat block at D(1), 15 s after genesis; written.
	b1 := chain.extend(0, 1)
	b1.ParentHash = genesis.Hash()
	b1.Difficulty = new(big.Int).Set(c1.Chain)
	relink(b1)
	if err := e.verifyClock(b1, genesis, st, false); err != nil {
		t.Fatalf("open block at the chain difficulty: %v", err)
	}
	if err := e.verifyClock(b1, genesis, st, true); !errors.Is(err, errSealDifficulty) {
		t.Fatalf("a bonded form at the chain difficulty: %v", err)
	}
	if err := e.finalizeClock(chain, b1, st); err != nil {
		t.Fatal(err)
	}
	w1, err := e.ReadClock(st)
	if err != nil || w1.Chain.Cmp(c1.Chain) != 0 || w1.Seal.Cmp(c1.Seal) != 0 || w1.Time != b1.Time || w1.ParentTime != genesis.Time {
		t.Fatalf("clock after block 1: %+v %v", w1, err)
	}
	// Block 2: a bonded block at round 1, sealed 3 s into its round (time
	// = parent + Delta + 3): carries D_seal(2) = D_seal(1) (the open
	// parent observed no seal interval); D(2) moves on the parent's 15 s.
	c2, err := e.ClockNext(st, b1)
	if err != nil || c2.Seal.Cmp(c1.Seal) != 0 {
		t.Fatalf("block 2: %+v %v", c2, err)
	}
	if c2.Chain.Cmp(e.config.Parts.Difficulty.NextRelative(c1.Chain, 15)) != 0 {
		t.Fatal("D(2) is not the chain's ASERT over the parent's interval")
	}
	key := common.HexToHash("0xb0")
	b2 := chain.extend(0, 1)
	b2.ParentHash = b1.Hash()
	b2.Time = b1.Time + s.Delta + 3
	b2.Round = 1
	b2.BondKey = &key
	b2.Shares = make([]byte, 64)
	b2.Difficulty = new(big.Int).Set(c2.Seal)
	relink(b2)
	if err := e.verifyClock(b2, b1, st, true); err != nil {
		t.Fatalf("bonded block at the seal difficulty: %v", err)
	}
	if err := e.verifyClock(b2, b1, st, false); !errors.Is(err, errChainDifficulty) {
		t.Fatalf("an open form at the seal difficulty: %v", err)
	}
	if err := e.finalizeClock(chain, b2, st); err != nil {
		t.Fatal(err)
	}
	// Block 3 (PA-48): the bonded parent sealed at ROUND 1, so its
	// interval reads the wait, not the search, and feeds the seal ASERT
	// nothing: D_seal(3) = D_seal(2); D(3) moves on the parent's 23 s.
	c3, err := e.ClockNext(st, b2)
	if err != nil {
		t.Fatal(err)
	}
	if c3.Seal.Cmp(c2.Seal) != 0 {
		t.Fatalf("D_seal(3) %v moved on a round-1 seal (want %v)", c3.Seal, c2.Seal)
	}
	if c3.Chain.Cmp(e.config.Parts.Difficulty.NextRelative(c2.Chain, 23)) != 0 {
		t.Fatal("D(3) is not the chain's ASERT over the parent's 23 s")
	}
	// A round-0 bonded parent sealed 12 s after its parent: the search
	// start is max(the round floor = parent.Time, parent.Time + tMin 10)
	// = parent + 10, the interval 2 s less tPiMs 250 ms = 1,750 ms, under
	// the 2,437 ms target, so D_seal rises by that step; and a round-0
	// seal exactly at the time floor reads a zero search and rises by
	// the largest step.
	// ClockNext reads the parent's time from the clock's STATE (the slots
	// Finalize wrote), so each variant writes its own clock first; the
	// real clock (block 2's) is put back after.
	saved, _ := e.ReadClock(st)
	r0 := *b2
	r0.Round = 0
	variant := func(parentTime uint64) *Clock {
		r0.Time = parentTime
		e.writeClock(st, &Clock{Chain: c2.Chain, Seal: c2.Seal, Time: r0.Time, ParentTime: b1.Time}, true)
		c, err := e.ClockNext(st, &r0)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	cr := variant(b1.Time + 12)
	if want := e.sealParams().NextRelative(c2.Seal, 1750); cr.Seal.Cmp(want) != 0 || cr.Seal.Cmp(c2.Seal) <= 0 {
		t.Fatalf("round-0 seal at +12 s: D_seal %v, want %v (over %v)", cr.Seal, want, c2.Seal)
	}
	cr2 := variant(b1.Time + 10)
	if want := e.sealParams().NextRelative(c2.Seal, 0); cr2.Seal.Cmp(want) != 0 || cr2.Seal.Cmp(cr.Seal) <= 0 {
		t.Fatalf("round-0 seal at the time floor: D_seal %v, want %v", cr2.Seal, want)
	}
	// A round-0 seal 30 s after its parent (20 s of search net of the
	// floor and the budget): over the target, D_seal falls.
	cr3 := variant(b1.Time + 30)
	if cr3.Seal.Cmp(c2.Seal) >= 0 {
		t.Fatalf("round-0 seal at +30 s did not lower D_seal: %v", cr3.Seal)
	}
	// PA-48a: the variants above wrote Rank 0 (the leader's seal); a
	// round-0 seal by a RANK-1 key at the same +12 s moves D_seal nothing
	// (its elapsed carries a stagger of wait no verifier can subtract),
	// and a block the rule wrote no rank for (slot 4 zero: every block
	// before PA-48a) reads -1 and moves nothing either.
	r0.Time = b1.Time + 12
	e.writeClock(st, &Clock{Chain: c2.Chain, Seal: c2.Seal, Time: r0.Time, ParentTime: b1.Time, Rank: 1}, true)
	if cr4, _ := e.ClockNext(st, &r0); cr4.Seal.Cmp(c2.Seal) != 0 {
		t.Fatalf("a rank-1 round-0 seal moved D_seal to %v", cr4.Seal)
	}
	e.writeClock(st, &Clock{Chain: c2.Chain, Seal: c2.Seal, Time: r0.Time, ParentTime: b1.Time, Rank: -1}, true)
	if got, _ := e.ReadClock(st); got.Rank != -1 {
		t.Fatalf("an unranked clock reads rank %d", got.Rank)
	}
	if cr5, _ := e.ClockNext(st, &r0); cr5.Seal.Cmp(c2.Seal) != 0 {
		t.Fatalf("an unranked round-0 seal moved D_seal to %v", cr5.Seal)
	}
	e.writeClock(st, saved, true)
	// The stateful entry through the anchored chain: the parent fetched by
	// hash; a chain not anchored is a fault.
	b3 := chain.extend(0, 1)
	b3.ParentHash = b2.Hash()
	b3.Difficulty = new(big.Int).Set(c3.Chain)
	relink(b3)
	if err := e.verifyClockAt(b3, st, false); err != nil {
		t.Fatalf("verifyClockAt: %v", err)
	}
	e.sortitionAnchor(nil)
	e.sortition.mu.Lock()
	e.sortition.chain = nil
	e.sortition.mu.Unlock()
	if err := e.verifyClockAt(b3, st, false); !errors.Is(err, errClockState) {
		t.Fatalf("no anchor: %v", err)
	}
	// The header path equals the state path: ChainDifficulty of blocks 1
	// to 3 from the headers alone (the anchored chain) is ClockNext's
	// chain value at each; the bonded block 2 (header at D_seal) reads D.
	e.sortitionAnchor(chain)
	for _, row := range []struct {
		h    *types.Header
		want *big.Int
	}{{b1, c1.Chain}, {b2, c2.Chain}, {b3, c3.Chain}} {
		if got := e.ChainDifficulty(row.h); got.Cmp(row.want) != 0 {
			t.Fatalf("ChainDifficulty of block %v: %v, want %v", row.h.Number, got, row.want)
		}
	}
	// Cached: a second read is the same; the cache holds the three.
	if got := e.ChainDifficulty(b2); got.Cmp(c2.Chain) != 0 || len(e.sortition.chainD) != 3 {
		t.Fatalf("cached read %v, cache %d", got, len(e.sortition.chainD))
	}
	// BlockWeight on the chain's D (the project's ruling): the bonded block at
	// round 1 weighs D(2) >> 1, not D_seal >> 1; an open block at round 1
	// with the same D would weigh the same.
	if w := e.BlockWeight(b2); w.Cmp(new(big.Int).Rsh(c2.Chain, 1)) != 0 {
		t.Fatalf("bonded weight %v, want %v (D_seal %v)", w, new(big.Int).Rsh(c2.Chain, 1), c2.Seal)
	}
	b2.Endorsed = 3
	if w := e.BlockWeight(b2); w.Cmp(new(big.Int).Add(new(big.Int).Rsh(c2.Chain, 1), new(big.Int).Mul(c2.Chain, big.NewInt(3)))) != 0 {
		t.Fatalf("endorsed weight %v", w)
	}
	b2.Endorsed = 0
	// A header whose parent the chain does not hold reads its own
	// difficulty (stated).
	orphan := &types.Header{Number: big.NewInt(9), ParentHash: common.HexToHash("0xdead"), Difficulty: big.NewInt(77)}
	if got := e.ChainDifficulty(orphan); got.Int64() != 77 {
		t.Fatalf("orphan: %v", got)
	}
	// The floor: a seal difficulty under D_min reads D_min.
	s.DMin = new(big.Int).Lsh(big.NewInt(1), 40)
	if c, _ := e.ClockNext(st, b2); c.Seal.Cmp(s.DMin) != 0 {
		t.Fatalf("the floor: %v", c.Seal)
	}
	// Off: ClockNext refuses and the stateless rule stands.
	s.SealWindowDiv = 0
	if _, err := e.ClockNext(st, b2); !errors.Is(err, errClockOff) {
		t.Fatalf("off: %v", err)
	}
}

// TestTimeFloor: under the amendment every block is at least T_min after
// its parent, whatever its round; the round floor stands beside it.
func TestTimeFloor(t *testing.T) {
	e := clockEngine(t)
	defer e.Close()
	s := e.config.Parts.Sortition
	key := common.HexToHash("0xb0")
	parent := &types.Header{Number: big.NewInt(4), Time: 1_000}
	mk := func(dt, round uint64) *types.Header {
		return &types.Header{Number: big.NewInt(5), Time: parent.Time + dt, Round: round, BondKey: &key, Sig: make([]byte, 64)}
	}
	if err := s.verifySortitionHeader(mk(9, 0), parent); !errors.Is(err, errUnderTimeFloor) {
		t.Fatalf("9 s at round 0: %v", err)
	}
	if err := s.verifySortitionHeader(mk(10, 0), parent); err != nil {
		t.Fatalf("10 s at round 0: %v", err)
	}
	if err := s.verifySortitionHeader(mk(15, 1), parent); !errors.Is(err, errSortitionEarlyTime) {
		t.Fatalf("15 s at round 1 (the round floor 20): %v", err)
	}
	if err := s.verifySortitionHeader(mk(20, 1), parent); err != nil {
		t.Fatalf("20 s at round 1: %v", err)
	}
	s.TMin = 0
	if err := s.verifySortitionHeader(mk(1, 0), parent); err != nil {
		t.Fatalf("no floor: %v", err)
	}
}

// TestRoundCensus counts rounds over the last n blocks ending at head.
func TestRoundCensus(t *testing.T) {
	e := clockEngine(t)
	defer e.Close()
	chain := newSupplyChain()
	for i := 0; i < 10; i++ {
		h := chain.extend(0, 1)
		if i%3 == 0 {
			h.Round = 1
			chain.add(h) // re-keyed under its hash after the mutation
		}
	}
	c := e.RoundCensus(chain, chain.CurrentHeader(), 300)
	if c[0] != 6 || c[1] != 4 {
		t.Fatalf("census %v", c)
	}
	if c := e.RoundCensus(chain, chain.CurrentHeader(), 2); c[0]+c[1] != 2 {
		t.Fatalf("census of 2: %v", c)
	}
}
