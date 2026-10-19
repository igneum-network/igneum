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
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// PA-48b, the seal clock's rank-0 fallback (the outside review's condition on
// PA-48a, 20:15 UK, 11 October 2026; the security review's eight cases and
// the design seat's stagger amendment, 20:3x and 20:4x UK): the engine
// level, a scripted run of parents through the clock's state. Each step
// of the script is one parent block: its rank at its round (0 the leader,
// k a staggered rank, -1 open or unranked), its round, whether it was
// bonded, and its elapsed after its parent in seconds; the clock written
// by Finalize is driven by ClockNext over the parent's state, so what the
// verifier of the next block computes is what the test reads.

type clockStep struct {
	rank   int
	round  uint64
	bonded bool
	dt     uint64
}

// fallbackEngine is clockEngine (K 16 and the stagger constant 2,000 ms,
// the launch values as SortitionFor defaults them under the amendment).
func fallbackEngine(t *testing.T) *Engine {
	t.Helper()
	return clockEngine(t)
}

// runClock drives the script from a seeded clock (Chain and Seal distinct,
// so parentBonded reads the parent's difficulty) and returns the clock
// after every step: out[i] is the clock the block AFTER step i's parent
// carries (its Chain, Seal, Dark and ParentTime; its own Time is the next
// step's). Step i's parent is the block the current clock describes: its
// time is its parent's plus the step's dt, its form the step's.
func runClock(t *testing.T, e *Engine, script []clockStep) []*Clock {
	t.Helper()
	st := nativeState(t, big.NewInt(1_000_000))
	key := common.HexToHash("0xb0")
	cur := &Clock{Chain: big.NewInt(1 << 20), Seal: big.NewInt(1 << 14), ParentTime: 1_000, Rank: 0}
	var out []*Clock
	for i, step := range script {
		cur.Time = cur.ParentTime + step.dt
		cur.Rank = step.rank
		parent := &types.Header{Number: big.NewInt(int64(i + 1)), Time: cur.Time, Round: step.round, Difficulty: new(big.Int).Set(cur.Chain)}
		if step.bonded {
			parent.BondKey = &key
			parent.Shares = make([]byte, 64)
			parent.Difficulty = new(big.Int).Set(cur.Seal)
		}
		e.writeClock(st, cur, true)
		next, err := e.ClockNext(st, parent)
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		out = append(out, next)
		cur = next
	}
	return out
}

// leader is a round-0 bonded seal by the rank-0 key, dt seconds after its
// parent; ranked the same by a rank-k key; open an open-seat block at the
// liveness round.
func leader(dt uint64) clockStep {
	return clockStep{rank: 0, round: 0, bonded: true, dt: dt}
}

func ranked(k int, dt uint64) clockStep {
	return clockStep{rank: k, round: 0, bonded: true, dt: dt}
}

func open(dt uint64) clockStep {
	return clockStep{rank: -1, round: 17, bonded: false, dt: dt}
}

// TestRank0FallbackDarkLeader (security's case 1 and the order's first
// test): the leader never seals; rank-1 seals at +14 s (a 2 s search
// after the stagger, under the target) run for K + 5 heights. The ASERT's
// input switches at exactly K: the clock steps nothing through the K
// dark parents, steps on the fallback from the (K + 1)th, and the count
// never exceeds K. An open-seat parent in the run counts as dark.
func TestRank0FallbackDarkLeader(t *testing.T) {
	e := fallbackEngine(t)
	defer e.Close()
	K := int(e.config.Parts.Sortition.Rank0FallbackBlocks)
	var script []clockStep
	for i := 0; i < K+5; i++ {
		if i == 3 {
			script = append(script, open(14))
			continue
		}
		script = append(script, ranked(1, 14))
	}
	out := runClock(t, e, script)
	seal0 := big.NewInt(1 << 14)
	for i := 0; i < K; i++ {
		if out[i].Seal.Cmp(seal0) != 0 {
			t.Fatalf("dark parent %d (count %d) moved D_seal to %v", i+1, out[i].Dark, out[i].Seal)
		}
		if want := uint64(i + 1); out[i].Dark != want {
			t.Fatalf("dark count after parent %d: %d, want %d", i+1, out[i].Dark, want)
		}
	}
	// The (K + 1)th dark parent is read: its elapsed 14 s less tMin 10 is
	// 4,000 ms, net of tPiMs 250 and one stagger 2,000: 1,750 ms, under
	// the 2,437 ms target, so D_seal rises by that step and nothing else.
	want := e.sealParams().NextRelative(seal0, 1750)
	if out[K].Seal.Cmp(want) != 0 || out[K].Seal.Cmp(seal0) <= 0 {
		t.Fatalf("the fallback's first read: D_seal %v, want %v", out[K].Seal, want)
	}
	for i := K; i < K+5; i++ {
		if out[i].Dark != uint64(K) {
			t.Fatalf("the count under the fallback after parent %d: %d, want %d", i+1, out[i].Dark, K)
		}
		if i > K && out[i].Seal.Cmp(out[i-1].Seal) <= 0 {
			t.Fatalf("the fallback stopped reading at parent %d: %v after %v", i+1, out[i].Seal, out[i-1].Seal)
		}
	}
	// Never at K - 1: the same run one height shorter never steps.
	short := runClock(t, e, script[:K])
	if short[K-1].Seal.Cmp(seal0) != 0 {
		t.Fatal("the fallback armed at K minus 1")
	}
}

// TestRank0FallbackStaggerNet (the design seat's amendment): under the
// fallback a rank-2 seal reads its elapsed net of two staggers, so a
// staggered rank-2 sealer that searched 2 s reads 1,750 ms as the leader
// would; a sealer of the same rank that did NOT stagger and sealed 4 s
// earlier reads short by the two staggers (zero search) and raises D_seal
// by the largest step; a raw read of either would see a slow search.
func TestRank0FallbackStaggerNet(t *testing.T) {
	e := fallbackEngine(t)
	defer e.Close()
	K := int(e.config.Parts.Sortition.Rank0FallbackBlocks)
	dark := make([]clockStep, K)
	for i := range dark {
		dark[i] = open(20)
	}
	seal0 := big.NewInt(1 << 14)
	staggered := runClock(t, e, append(append([]clockStep{}, dark...), ranked(2, 16)))
	if want := e.sealParams().NextRelative(seal0, 1750); staggered[K].Seal.Cmp(want) != 0 {
		t.Fatalf("a staggered rank-2 seal at +16 s: D_seal %v, want %v", staggered[K].Seal, want)
	}
	defector := runClock(t, e, append(append([]clockStep{}, dark...), ranked(2, 12)))
	if want := e.sealParams().NextRelative(seal0, 0); defector[K].Seal.Cmp(want) != 0 || defector[K].Seal.Cmp(staggered[K].Seal) <= 0 {
		t.Fatalf("a non-staggering rank-2 seal at +12 s: D_seal %v, want %v (over %v)", defector[K].Seal, want, staggered[K].Seal)
	}
	// The leader's read is unchanged by the constant: rank 0 subtracts no
	// stagger, so a leader at +12 s reads 1,750 ms as before PA-48b.
	lead := runClock(t, e, []clockStep{leader(12)})
	if want := e.sealParams().NextRelative(seal0, 1750); lead[0].Seal.Cmp(want) != 0 {
		t.Fatalf("the leader at +12 s: %v, want %v", lead[0].Seal, want)
	}
}

// TestRank0FallbackLateLeader (security's case 2, the order's second
// test): a rank-0 leader seals every height at the end of its window
// (+29 s, a 19 s search net of nothing but the budget): each seal is read
// as the leader's own elapsed (the count resets to zero every height, the
// fallback never arms) and D_seal falls by the ASERT over that elapsed to
// the floor D_min and no further; a late DECLARATION beyond the sealer's
// real time is what PA-50 bounds, by the drift against the receiving
// node's clock, not by K: a bonded header 2 s ahead is held, one within
// the drift passes.
func TestRank0FallbackLateLeader(t *testing.T) {
	e := fallbackEngine(t)
	defer e.Close()
	s := e.config.Parts.Sortition
	script := make([]clockStep, 40)
	for i := range script {
		script[i] = leader(29)
	}
	out := runClock(t, e, script)
	seal0 := big.NewInt(1 << 14)
	if want := e.sealParams().NextRelative(seal0, 19_000-s.TPiMs); out[0].Seal.Cmp(want) != 0 || out[0].Seal.Cmp(seal0) >= 0 {
		t.Fatalf("the late leader's first read: %v, want %v (under %v)", out[0].Seal, want, seal0)
	}
	prev := seal0
	for i, c := range out {
		if c.Dark != 0 {
			t.Fatalf("the late leader's count after parent %d: %d", i+1, c.Dark)
		}
		if c.Seal.Cmp(prev) >= 0 || c.Seal.Cmp(s.DMin) < 0 {
			t.Fatalf("D_seal at parent %d: %v after %v (floor %v)", i+1, c.Seal, prev, s.DMin)
		}
		prev = c.Seal
	}
	// The floor binds: with D_min at the seed the same run sits at D_min
	// from the first read and goes no lower.
	s.DMin = new(big.Int).Set(seal0)
	for i, c := range runClock(t, e, script[:5]) {
		if c.Seal.Cmp(s.DMin) != 0 {
			t.Fatalf("the floor at parent %d: %v", i+1, c.Seal)
		}
	}
	s.DMin = big.NewInt(1_000)
	// PA-50's bound on the declaration.
	key := common.HexToHash("0xb0")
	h := &types.Header{Number: big.NewInt(5), Time: 1_000, BondKey: &key, Shares: make([]byte, 64)}
	if err := e.verifyDeclaredTime(h, 998); err == nil {
		t.Fatal("a bonded header 2 s ahead of the node's clock passed")
	}
	if err := e.verifyDeclaredTime(h, 1_000); err != nil {
		t.Fatalf("a bonded header at the node's clock: %v", err)
	}
}

// TestRank0FallbackSparseLeader (security's case 3): the leader seals once
// every K heights (dark for K - 1 = 15, present for 1) at +12 s, the dark
// heights rank-1 seals at +14 s: the count reaches K - 1 and resets, the
// fallback never arms, and the ASERT reads only the leader's sparse
// seals, so D_seal moves on exactly 10 of 160 heights; the dense case
// (the leader every height) moves on all 160. The two paths are printed
// for the row.
func TestRank0FallbackSparseLeader(t *testing.T) {
	e := fallbackEngine(t)
	defer e.Close()
	K := int(e.config.Parts.Sortition.Rank0FallbackBlocks)
	var sparse, dense []clockStep
	for i := 0; i < 160; i++ {
		dense = append(dense, leader(12))
		if (i+1)%K == 0 {
			sparse = append(sparse, leader(12))
		} else {
			sparse = append(sparse, ranked(1, 14))
		}
	}
	sp := runClock(t, e, sparse)
	dn := runClock(t, e, dense)
	moves, maxDark := 0, uint64(0)
	prev := big.NewInt(1 << 14)
	for i, c := range sp {
		if c.Seal.Cmp(prev) != 0 {
			moves++
			if sparse[i].rank != 0 {
				t.Fatalf("D_seal moved on a rank-1 seal at parent %d with the fallback off", i+1)
			}
		}
		if c.Dark > maxDark {
			maxDark = c.Dark
		}
		prev = c.Seal
	}
	if moves != 160/K || maxDark != uint64(K-1) {
		t.Fatalf("sparse leader: %d moves (want %d), max count %d (want %d)", moves, 160/K, maxDark, K-1)
	}
	t.Logf("sparse leader at K-1: D_seal %v after 160 heights on %d reads; dense leader: %v on 160 reads; the first read %v from %v",
		sp[159].Seal, moves, dn[159].Seal, dn[0].Seal, 1<<14)
}

// TestRank0FallbackTwoLeaders (security's case 5) and the epoch boundary
// (case 6): two rank-0 keys alternate heights, each the leader at its own
// height; and a new key becomes the leader after a snapshot. The count is
// per height on the chain and not per key: every height has a rank-0 seal,
// the count never climbs, no fallback; a new leader's first seal resets a
// count the old leader's absence built.
func TestRank0FallbackTwoLeaders(t *testing.T) {
	e := fallbackEngine(t)
	defer e.Close()
	script := make([]clockStep, 40)
	for i := range script {
		script[i] = leader(12) // the sealer differs by height; the clock reads the rank
	}
	for i, c := range runClock(t, e, script) {
		if c.Dark != 0 {
			t.Fatalf("alternating leaders: count %d after parent %d", c.Dark, i+1)
		}
	}
	// The old leader dark for 10, the new leader's first seal: reset.
	var sw []clockStep
	for i := 0; i < 10; i++ {
		sw = append(sw, ranked(1, 14))
	}
	sw = append(sw, leader(12), ranked(1, 14))
	out := runClock(t, e, sw)
	if out[9].Dark != 10 || out[10].Dark != 0 || out[11].Dark != 1 {
		t.Fatalf("the new leader's reset: counts %d %d %d", out[9].Dark, out[10].Dark, out[11].Dark)
	}
}

// TestRank0FallbackSwitches (security's case 8 and the order's reset
// test): present for K, dark for K, present for one, dark for K: the
// fallback arms after the 2Kth parent and disarms at the (2K + 1)th, and
// re-arms after the (3K + 1)th; the ASERT's input is the union of the two
// readings with no gap and no double count at the switches (every read
// moves D_seal once; every unread parent leaves it).
func TestRank0FallbackSwitches(t *testing.T) {
	e := fallbackEngine(t)
	defer e.Close()
	K := int(e.config.Parts.Sortition.Rank0FallbackBlocks)
	var script []clockStep
	for i := 0; i < K; i++ {
		script = append(script, leader(12))
	}
	for i := 0; i < K; i++ {
		script = append(script, ranked(1, 14))
	}
	script = append(script, leader(12))
	for i := 0; i < K; i++ {
		script = append(script, ranked(1, 14))
	}
	out := runClock(t, e, script)
	read := func(i int) bool { // was parent i (0-based) read by the ASERT?
		prev := big.NewInt(1 << 14)
		if i > 0 {
			prev = out[i-1].Seal
		}
		return out[i].Seal.Cmp(prev) != 0
	}
	for i := range script {
		want := false
		switch {
		case i < K: // the leader's K seals
			want = true
		case i < 2*K: // K dark parents: the count climbs, nothing read
			want = false
		case i == 2*K: // the leader's return: read, the count reset
			want = true
		case i <= 3*K: // the K dark parents after: the count climbs again to K
			want = false
		}
		if i == 2*K-1 && out[i].Dark != uint64(K) {
			t.Fatalf("the count after the 2Kth parent: %d", out[i].Dark)
		}
		if i == 2*K && out[i].Dark != 0 {
			t.Fatalf("the count after the leader's return: %d", out[i].Dark)
		}
		if got := read(i); got != want {
			t.Fatalf("parent %d (rank %d, count %d): read %v, want %v", i+1, script[i].rank, out[i].Dark, got, want)
		}
	}
	// One more dark parent after the second K: the fallback is armed again
	// and reads it.
	out2 := runClock(t, e, append(script, ranked(1, 14)))
	if last := len(out2) - 1; out2[last].Seal.Cmp(out2[last-1].Seal) == 0 {
		t.Fatal("the fallback did not re-arm after the second K dark heights")
	}
	// Steps under the fallback at 2K would have double-counted: the arm
	// after exactly K dark parents, the 2Kth parent itself unread.
	armed := runClock(t, e, append(script[:2*K], ranked(1, 14)))
	if armed[2*K-1].Seal.Cmp(armed[K-1].Seal) != 0 || armed[2*K].Seal.Cmp(armed[2*K-1].Seal) == 0 {
		t.Fatal("the switch at 2K is off by one")
	}
}

// TestRank0FallbackReplayIdentity (security's cases 4 and 7, the order's
// fourth test): the count and the difficulties are a function of the
// chain alone: the same script replayed from the same seed gives the same
// clock at every height, slot for slot (Chain, Seal, Dark, the times);
// the slots round-trip through the state (write, read); and a withheld
// block released late is, to the clock, the canonical parent it becomes:
// the count at a height reads the canonical parent's form and nothing a
// node remembered (two scripts that agree on the canonical chain agree on
// the count whatever order the blocks arrived in, which is what a replay
// from genesis is).
func TestRank0FallbackReplayIdentity(t *testing.T) {
	e := fallbackEngine(t)
	defer e.Close()
	K := int(e.config.Parts.Sortition.Rank0FallbackBlocks)
	var script []clockStep
	for i := 0; i < 3*K; i++ {
		switch i % 7 {
		case 0, 1:
			script = append(script, leader(12+uint64(i%5)))
		case 2:
			script = append(script, open(20))
		default:
			script = append(script, ranked(1+i%3, 14+uint64(i%4)))
		}
	}
	a := runClock(t, e, script)
	b := runClock(t, e, script)
	for i := range a {
		if a[i].Chain.Cmp(b[i].Chain) != 0 || a[i].Seal.Cmp(b[i].Seal) != 0 || a[i].Dark != b[i].Dark || a[i].ParentTime != b[i].ParentTime {
			t.Fatalf("replay differs at parent %d: %+v vs %+v", i+1, a[i], b[i])
		}
	}
	// The slots: a clock with a count and a rank round-trips.
	st := nativeState(t, big.NewInt(1_000_000))
	w := &Clock{Chain: big.NewInt(5), Seal: big.NewInt(3), Time: 70, ParentTime: 55, Rank: 2, Dark: 9}
	e.writeClock(st, w, true)
	r, err := e.ReadClock(st)
	if err != nil || r.Dark != 9 || r.Rank != 2 || r.Time != 70 {
		t.Fatalf("round trip: %+v %v", r, err)
	}
	// A count written by a build before PA-48b (slot 5 zero) reads zero.
	w.Dark = 0
	e.writeClock(st, w, true)
	if r, _ := e.ReadClock(st); r.Dark != 0 {
		t.Fatalf("a zero slot reads %d", r.Dark)
	}
}

// TestRank0FallbackConstants: SortitionFor carries K and the stagger
// constant, defaulting both under the amendment (16 heights, 2,000 ms) so
// a genesis cut before PA-48b needs no change; the JSON names are
// rank0FallbackBlocks and sealStaggerMs.
func TestRank0FallbackConstants(t *testing.T) {
	cfg := &ctypes.IgneumConfig{
		Authority: &ctypes.IgneumAuthority{Height: 1, Registry: testRegistry, Cap: big.NewInt(1), RegistryVersion: 4},
		Sortition: &ctypes.IgneumSortition{Height: 1, EpochBlocks: 100, SeedBlocks: 16, Expected: 1, Delta: 20, MaxRound: 16, UnbondedRound: 17, MaxPayouts: 256, OpenSeat: true, SealWindowDiv: 4, DMin: big.NewInt(1_000), TMin: 10},
	}
	p, err := SortitionFor(cfg)
	if err != nil || p.Rank0FallbackBlocks != 16 || p.SealStaggerMs != 2000 {
		t.Fatalf("the defaults: %v K %d stagger %d", err, p.Rank0FallbackBlocks, p.SealStaggerMs)
	}
	cfg.Sortition.Rank0FallbackBlocks, cfg.Sortition.SealStaggerMs = 8, 1500
	if p, _ := SortitionFor(cfg); p.Rank0FallbackBlocks != 8 || p.SealStaggerMs != 1500 {
		t.Fatalf("the genesis values: K %d stagger %d", p.Rank0FallbackBlocks, p.SealStaggerMs)
	}
	cfg.Sortition.SealWindowDiv = 0
	if p, _ := SortitionFor(cfg); p.Rank0FallbackBlocks != 8 {
		t.Fatal("the constants are carried off the amendment")
	}
}
