package igneum

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/crypto"
)

// PA-52, the sealer policy (the project's order of 20:2x UK, 11 October 2026): at
// a round where several keys are eligible, the least-lot key starts its
// search at the round's start and each next rank waits one stagger more,
// so the eligible keys' seals spread instead of racing at the floor. No
// consensus change and no genesis field: a node's configuration value
// (--miner.seal-stagger, milliseconds, 0 off; DefaultSealStaggerMs), read
// by prepareSortition through sealerFloor. The rank comes from node seat
// B's LotRankAt (forkchoice.go): the eligible keys at the round ordered
// by their least lot, deterministic on every node from the table and the
// seed; the engine takes it as a lotRanker so the sealer's floor and its
// test stand on their own.

// DefaultSealStaggerMs is the stagger's default, two seconds (the project's ruling
// of 20:5x UK, 11 October 2026, the design seat's row at 2 s); devnet8
// runs its first 300 heights at 0 and the swap after them turns it on, so
// C14 reads both halves.
const DefaultSealStaggerMs = 2000

// lotRanker ranks key among the keys eligible at the round at the parent's
// state: 0 the leader, -1 not eligible, with the eligible count.
type lotRanker func(st *state.StateDB, key common.Hash, height, round uint64) (rank, eligible int, err error)

// SetSealStagger sets the stagger in milliseconds (0 off).
func (e *Engine) SetSealStagger(ms uint64) {
	e.lock.Lock()
	defer e.lock.Unlock()
	e.sealStaggerMs = ms
}

// SealStaggerMs is the stagger in milliseconds.
func (e *Engine) SealStaggerMs() uint64 {
	e.lock.Lock()
	defer e.lock.Unlock()
	return e.sealStaggerMs
}

// SetLotRanker sets the rank source (nil: every key the leader, no
// stagger).
func (e *Engine) SetLotRanker(r lotRanker) {
	e.lock.Lock()
	defer e.lock.Unlock()
	e.lotRank = r
}

// staggerRank is the sealer's rank at the round for the floor: 0 without a
// stagger, a rank source, a state, or an eligible rank (a key the source
// does not rank starts at the round's start, as before PA-52).
func (e *Engine) staggerRank(st *state.StateDB, key common.Hash, height, round uint64) int {
	e.lock.Lock()
	ms, rank := e.sealStaggerMs, e.lotRank
	e.lock.Unlock()
	if ms == 0 || rank == nil || st == nil {
		return 0
	}
	r, _, err := rank(st, key, height, round)
	if err != nil || r < 0 {
		return 0
	}
	return r
}

// OpenSpreadSlots is the open-seat spread's slot count (F-114).
const OpenSpreadSlots = 4

// DefaultOpenSpreadMs is the open-seat spread's default slot width, three
// seconds (the project's word of 08:0x UK, 14 October 2026).
const DefaultOpenSpreadMs = 3000

// SetOpenSpread sets the open-seat spread's slot width in milliseconds
// (0 off).
func (e *Engine) SetOpenSpread(ms uint64) {
	e.lock.Lock()
	defer e.lock.Unlock()
	e.openSpreadMs = ms
}

// OpenSpreadMs is the open-seat spread's slot width in milliseconds.
func (e *Engine) OpenSpreadMs() uint64 {
	e.lock.Lock()
	defer e.lock.Unlock()
	return e.openSpreadMs
}

// openSpread is F-114's sealer policy for the OPEN SEAT (the old public test network (chain 7320), 14
// October 2026: with every sealer eligible at round 0 through the open
// seat, all of them started their search at the time floor, so two or
// more blocks stood at 48 percent of heights whatever the lot-rank
// stagger did, since the lot source ranks no open-seat key): the sealer's
// search start moves by slot x width, slot = keccak(parent hash, the key
// the block is sealed under) mod OpenSpreadSlots, so a fixed set of
// open-seat sealers lands on one slot one time in four instead of always,
// and the late ones are dropped by F-112. A node's value, no consensus
// (the verifier's floors stay the round's and tMin's); 0 off. The seconds
// are the header's unit, rounded up.
func openSpread(parent common.Hash, key common.Hash, widthMs uint64) uint64 {
	if widthMs == 0 {
		return 0
	}
	h := crypto.Keccak256(parent[:], key[:])
	slot := uint64(h[31] % OpenSpreadSlots)
	return (slot*widthMs + 999) / 1000
}
