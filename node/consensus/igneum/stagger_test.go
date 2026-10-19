package igneum

import (
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm/igneumreg"
)

// TestSealerFloorIsTheSearchStart (PA-48's sealer half; the project's ruling of
// 16:4x UK, 11 October 2026): the template's earliest time for the drawn
// round is max(the round floor, parent.Time + tMin), the start ClockNext's
// seal step measures from, so a round-0 sealer under the clock amendment
// searches from the time floor; a later round keeps its round floor when
// that is the later; and PA-52's stagger adds one whole second per rank
// at the default (rounded up at a fraction), nothing at rank 0 or off.
func TestSealerFloorIsTheSearchStart(t *testing.T) {
	s := &SortitionParams{Delta: 20, TMin: 12}
	parent := &types.Header{Number: big.NewInt(9), Time: 1000}
	for _, tc := range []struct {
		round     uint64
		rank      int
		staggerMs uint64
		want      uint64
	}{
		{0, 0, DefaultSealStaggerMs, 1012}, // round 0: the time floor, not the parent's time
		{1, 0, DefaultSealStaggerMs, 1020}, // round 1: the round floor is the later
		{0, 1, DefaultSealStaggerMs, 1014}, // rank 1 starts one stagger (2 s) after rank 0
		{0, 3, DefaultSealStaggerMs, 1018}, // rank 3 waits three
		{1, 2, 500, 1021},                  // 2 x 500 ms = one second
		{1, 1, 500, 1021},                  // 500 ms rounds up to the header's second
		{0, 2, 0, 1012},                    // off
		{0, -1, DefaultSealStaggerMs, 1012},
	} {
		if got := s.sealerFloor(parent, tc.round, tc.rank, tc.staggerMs); got != tc.want {
			t.Fatalf("round %d rank %d stagger %d ms: floor %d, want %d", tc.round, tc.rank, tc.staggerMs, got, tc.want)
		}
	}
	// Without the clock amendment (tMin 0) the floor is the round's alone.
	s0 := &SortitionParams{Delta: 20}
	if got := s0.sealerFloor(parent, 0, 0, DefaultSealStaggerMs); got != 1000 {
		t.Fatalf("no tMin: floor %d, want the parent's time", got)
	}
}

// TestRankOneStartsOneStaggerAfterRankZero (PA-52; the project's ruling of 20:5x
// UK, 11 October 2026): on a table of five bonded keys at the liveness
// round, where every key is eligible, the sealer's floor for the key at
// rank 1 (read from the node team's LotRankAt, the engine's rank source) is
// one stagger, 2 s, after the leader's, rank 2's two, and with the stagger
// off every key's floor is the leader's.
func TestRankOneStartsOneStaggerAfterRankZero(t *testing.T) {
	e := genesisEngine(t)
	defer e.Close()
	e.config.Parts.Authority.RegistryVersion = igneumreg.VersionPenalties
	st := nativeState(t, big.NewInt(1_000_000))
	st.SetState(testRegistry, igneumreg.SlotFloor, common.BigToHash(big.NewInt(160_000)))
	var keys []common.Hash
	for i := byte(0); i < 5; i++ {
		keys = append(keys, bondedKey(t, st, st, 11+i, 8_000))
	}
	const height = 7
	s := e.config.Parts.Sortition
	round := s.UnbondedRound
	byRank := make([]common.Hash, len(keys))
	for _, k := range keys {
		rank := e.staggerRank(st, k, height, round)
		if rank < 0 || rank >= len(keys) || byRank[rank] != (common.Hash{}) {
			t.Fatalf("key %x: rank %d", k[:2], rank)
		}
		byRank[rank] = k
	}
	parent := &types.Header{Number: big.NewInt(height - 1), Time: 5000}
	floor := func(k common.Hash) uint64 {
		return s.sealerFloor(parent, round, e.staggerRank(st, k, height, round), e.SealStaggerMs())
	}
	lead := floor(byRank[0])
	if lead != s.sealerFloor(parent, round, 0, 0) {
		t.Fatalf("the leader's floor %d is not the round's start %d", lead, s.sealerFloor(parent, round, 0, 0))
	}
	if f := floor(byRank[1]); f != lead+2 {
		t.Fatalf("rank 1 starts at %d, want the leader's %d + 2 s", f, lead)
	}
	if f := floor(byRank[2]); f != lead+4 {
		t.Fatalf("rank 2 starts at %d, want the leader's %d + 4 s", f, lead)
	}
	e.SetSealStagger(0)
	for i, k := range byRank {
		if f := floor(k); f != lead {
			t.Fatalf("stagger off: rank %d starts at %d, want %d", i, f, lead)
		}
	}
}

// TestStaggerRankReadsTheLotRank (PA-52): the sealer's rank for the floor
// is the rank source's (the node team's LotRankAt), 0 without a source, with
// the stagger off, for a key the source does not rank, or on its error.
func TestStaggerRankReadsTheLotRank(t *testing.T) {
	e := genesisEngine(t)
	defer e.Close()
	key := common.Hash{0xab}
	st := &state.StateDB{}
	e.SetLotRanker(nil) // the engine's default source is LotRankAt (igneum.go)
	if r := e.staggerRank(st, key, 10, 0); r != 0 {
		t.Fatalf("no source: rank %d", r)
	}
	var calls int
	e.SetLotRanker(func(st *state.StateDB, k common.Hash, height, round uint64) (int, int, error) {
		calls++
		if k != key || height != 10 || round != 1 {
			t.Fatalf("the source read (%x, %d, %d)", k[:1], height, round)
		}
		return 2, 3, nil
	})
	if r := e.staggerRank(st, key, 10, 1); r != 2 || calls != 1 {
		t.Fatalf("rank %d, calls %d", r, calls)
	}
	e.SetSealStagger(0)
	if r := e.staggerRank(st, key, 10, 1); r != 0 || calls != 1 {
		t.Fatalf("stagger off: rank %d, calls %d (the source is not read)", r, calls)
	}
	e.SetSealStagger(DefaultSealStaggerMs)
	e.SetLotRanker(func(*state.StateDB, common.Hash, uint64, uint64) (int, int, error) { return -1, 3, nil })
	if r := e.staggerRank(st, key, 10, 1); r != 0 {
		t.Fatalf("not eligible by the source: rank %d", r)
	}
	e.SetLotRanker(func(*state.StateDB, common.Hash, uint64, uint64) (int, int, error) {
		return 0, 0, errors.New("off")
	})
	if r := e.staggerRank(st, key, 10, 1); r != 0 {
		t.Fatalf("the source's error: rank %d", r)
	}
	if r := e.staggerRank(nil, key, 10, 1); r != 0 {
		t.Fatalf("no state: rank %d", r)
	}
	if e.SealStaggerMs() != DefaultSealStaggerMs {
		t.Fatalf("the default stagger: %d", e.SealStaggerMs())
	}
}

// TestOpenSpreadSlots pins F-114's open-seat spread: a slot of 0 to 3 by
// (parent, key), whole seconds rounded up, 0 when off, and a fixed key's
// slot moving with the parent.
func TestOpenSpreadSlots(t *testing.T) {
	var parent, key common.Hash
	parent[0], key[0] = 1, 2
	if openSpread(parent, key, 0) != 0 {
		t.Fatal("off is zero")
	}
	seen := map[uint64]bool{}
	for i := 0; i < 64; i++ {
		parent[31] = byte(i)
		d := openSpread(parent, key, 3000)
		if d%3 != 0 || d > 9 {
			t.Fatalf("parent %d: spread %d not a slot of 3 s under 4", i, d)
		}
		seen[d] = true
	}
	if len(seen) != 4 {
		t.Fatalf("64 parents reached %d of 4 slots", len(seen))
	}
	if openSpread(parent, key, 2500)%1 != 0 || openSpread(parent, key, 2500) > 8 {
		t.Fatal("2.5 s slots round up to whole seconds under 8")
	}
}
