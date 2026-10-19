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
	"bytes"
	"crypto/ed25519"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm/igneumreg"
	"github.com/holiman/uint256"
)

// bondedKey posts a bond under an Ed25519 public key as the bond key
// (the open-seat signing form) and returns the key.
func bondedKey(t *testing.T, st interface {
	SubBalance(common.Address, *uint256.Int)
	AddBalance(common.Address, *uint256.Int)
}, regSt igneumreg.StateDB, seed byte, amount uint64) common.Hash {
	t.Helper()
	var s [ed25519.SeedSize]byte
	s[0] = seed
	priv := ed25519.NewKeyFromSeed(s[:])
	key := common.BytesToHash(priv.Public().(ed25519.PublicKey))
	v := uint256.NewInt(amount)
	st.SubBalance(testHolder, v)
	st.AddBalance(testRegistry, v)
	if _, err := igneumreg.Exec(igneumreg.Context{Version: igneumreg.VersionPenalties, Number: 1}, regSt, testRegistry, testHolder, testHolder, v, key[:]); err != nil {
		t.Fatalf("bond: %v", err)
	}
	return key
}

// TestLotRankAtOrdersEligibleKeysByLot is PA-52's rank (the sealer
// policy's stagger reads it): five bonded keys in the live table at one
// height; at the liveness round every one is eligible and the ranks are
// the order of their least lots, the leader 0 and each rank's lot not
// above the next; at round 0 only the keys whose draw opens at 0 are
// counted and the others read -1; an unbonded key reads -1; the genesis
// form is required.
func TestLotRankAtOrdersEligibleKeysByLot(t *testing.T) {
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
	// The liveness round: every bonded key is eligible at or below it.
	n := 0
	lots := map[common.Hash]*big.Int{}
	for _, k := range keys {
		d, err := e.drawAt(st, k, height)
		if err != nil || d.open {
			t.Fatalf("key %x not in the table: %v", k[:2], err)
		}
		lots[k] = minLot(d.table.seed, k, d.units, height)
		n++
	}
	for _, k := range keys {
		rank, eligible, err := e.LotRankAt(st, k, height, s.UnbondedRound)
		if err != nil || eligible != n || rank < 0 || rank >= n {
			t.Fatalf("key %x at the liveness round: rank %d of %d, %v", k[:2], rank, eligible, err)
		}
	}
	// The order: rank i's lot at or under rank i+1's; the leader the least.
	byRank := make([]common.Hash, n)
	for _, k := range keys {
		rank, _, _ := e.LotRankAt(st, k, height, s.UnbondedRound)
		byRank[rank] = k
	}
	for i := 1; i < n; i++ {
		if lots[byRank[i-1]].Cmp(lots[byRank[i]]) > 0 {
			t.Fatalf("rank %d's lot is above rank %d's", i-1, i)
		}
	}
	for _, k := range keys {
		if lots[k].Cmp(lots[byRank[0]]) < 0 {
			t.Fatal("the leader is not the least lot")
		}
	}
	// Round 0: only the keys whose draw opens at round 0 count; the rest
	// read -1 and the count is theirs.
	var open0 int
	for _, k := range keys {
		d, _ := e.drawAt(st, k, height)
		if d.round == 0 {
			open0++
		}
	}
	for _, k := range keys {
		d, _ := e.drawAt(st, k, height)
		rank, eligible, err := e.LotRankAt(st, k, height, 0)
		if err != nil || eligible != open0 {
			t.Fatalf("round 0 count %d, want %d (%v)", eligible, open0, err)
		}
		if (d.round == 0) != (rank >= 0) {
			t.Fatalf("key %x at round 0: draw round %d, rank %d", k[:2], d.round, rank)
		}
	}
	// An unbonded key reads -1 with the same count; the genesis form is
	// required.
	if rank, eligible, err := e.LotRankAt(st, common.HexToHash("0xee"), height, s.UnbondedRound); err != nil || rank != -1 || eligible != n {
		t.Fatalf("an unbonded key: %d of %d, %v", rank, eligible, err)
	}
	s.OpenSeat = false
	if _, _, err := e.LotRankAt(st, keys[0], height, 0); !errors.Is(err, errLotRankOff) {
		t.Fatalf("without the genesis form: %v", err)
	}
}

// TestForkTieBreakConvergesWithinOneHeight is PA-47's rule (security's
// F-41) with the tie term on the LOT (the design seat's grinding finding;
// the project's ruling of 16:4x UK, 11 October 2026): two sealers on an
// equal-length fork. At the contested height with no successors, two
// bonded blocks tie on the lower lot value of the sealing unit at that
// height, the same answer on both nodes (each asked from its own head);
// a bonded block beats an open-seat block; two open-seat blocks tie on the
// seal hash; once each branch has a successor the first contested
// height's endorsement weight decides; a reader without state ties on the
// seal hash (stated); unequal lengths answer by the first height all the
// same and an ancestor head is no fork.
func TestForkTieBreakConvergesWithinOneHeight(t *testing.T) {
	e := genesisEngine(t)
	defer e.Close()
	e.config.Parts.Authority.RegistryVersion = igneumreg.VersionPenalties
	st := nativeState(t, big.NewInt(1_000_000))
	// A floor of 160,000: the unit 2, two keys of 20,000 (10,000 units
	// each, under the quarter cap) so both are in the live table.
	st.SetState(testRegistry, igneumreg.SlotFloor, common.BigToHash(big.NewInt(160_000)))
	keyA := bondedKey(t, st, st, 3, 20_000)
	keyB := bondedKey(t, st, st, 4, 20_000)
	chain := &stateChain{fakeChain: newFakeChain(), st: st}
	g := genesisHeader()
	chain.add(g)
	// The contested height 1: A's and B's blocks, both bonded at round 0.
	a := childOf(g)
	a.BondKey, a.Round, a.Extra = &keyA, 0, []byte("sealer A")
	b := childOf(g)
	b.BondKey, b.Round, b.Extra = &keyB, 0, []byte("sealer B")
	chain.add(a)
	chain.add(b)
	dA, err := e.drawAt(st, keyA, 1)
	if err != nil || dA.open {
		t.Fatalf("key A not in the table: %+v %v", dA, err)
	}
	dB, _ := e.drawAt(st, keyB, 1)
	lotA := minLot(dA.table.seed, keyA, dA.units, 1)
	lotB := minLot(dB.table.seed, keyB, dB.units, 1)
	if lotA.Cmp(lotB) == 0 {
		t.Fatal("the two keys' lots coincide; the test proves nothing")
	}
	lowerIsB := lotB.Cmp(lotA) < 0
	moveA, err := e.PreferBranch(chain, a, b)
	if err != nil {
		t.Fatal(err)
	}
	moveB, err := e.PreferBranch(chain, b, a)
	if err != nil {
		t.Fatal(err)
	}
	if moveA != lowerIsB || moveB == moveA {
		t.Fatalf("the lot tie: node A moves %v, node B moves %v, b lower %v", moveA, moveB, lowerIsB)
	}
	// The lot is not the seal hash's order: whichever way the hashes
	// fall, the lot decides (asserted by construction when they differ).
	ha, hb := e.SealHash(a), e.SealHash(b)
	if (bytes.Compare(hb[:], ha[:]) < 0) != lowerIsB {
		// The hash order disagrees with the lot order: the answer must be
		// the lot's, which moveA already asserts.
		t.Logf("the hash order disagrees with the lot order here; the lot decided")
	}
	// A bonded block beats an open-seat block at equal weight and height,
	// from either head.
	open := childOf(g)
	openKey := common.HexToHash("0xcc")
	open.BondKey, open.Round, open.Extra = &openKey, 0, []byte("open")
	chain.add(open)
	if move, _ := e.PreferBranch(chain, open, a); !move {
		t.Fatal("an open-seat head does not move to the bonded block")
	}
	if move, _ := e.PreferBranch(chain, a, open); move {
		t.Fatal("a bonded head moves to the open-seat block")
	}
	// Two open-seat blocks: the lower seal hash.
	open2 := childOf(g)
	open2Key := common.HexToHash("0xdd")
	open2.BondKey, open2.Round, open2.Extra = &open2Key, 0, []byte("open two")
	chain.add(open2)
	h1, h2 := e.SealHash(open), e.SealHash(open2)
	if move, _ := e.PreferBranch(chain, open, open2); move != (bytes.Compare(h2[:], h1[:]) < 0) {
		t.Fatal("two open-seat blocks do not tie on the seal hash")
	}
	// Each branch grows a successor carrying its endorsements: the
	// greater weight at the first contested height wins whatever the lots
	// say.
	a2 := childOf(a)
	a2.Endorsed = 3
	b2 := childOf(b)
	b2.Endorsed = 9
	chain.add(a2)
	chain.add(b2)
	if move, _ := e.PreferBranch(chain, a2, b2); !move {
		t.Fatal("the heavier endorsed branch not preferred from a")
	}
	if move, _ := e.PreferBranch(chain, b2, a2); move {
		t.Fatal("the lighter branch preferred from b")
	}
	// Equal weights at the first contested height fall back to the lot,
	// whatever later blocks carry (the FIRST contested height decides).
	b2.Endorsed = 3
	chain.add(b2)
	a3 := childOf(a2)
	a3.Endorsed = 50
	chain.add(a3)
	b3 := childOf(b2)
	chain.add(b3)
	if move, _ := e.PreferBranch(chain, a3, b3); move != lowerIsB {
		t.Fatalf("equal first-height weight: move %v, want the lower lot's %v", move, lowerIsB)
	}
	// Unequal lengths answer by the first height all the same (the fork
	// choice never asks there); an ancestor head and the same head: no
	// move.
	if move, err := e.PreferBranch(chain, a3, b2); err != nil || move != lowerIsB {
		t.Fatalf("unequal lengths: %v %v, want the lower lot's %v", move, err, lowerIsB)
	}
	if move, err := e.PreferBranch(chain, a3, a2); err != nil || move {
		t.Fatalf("an ancestor: %v %v", move, err)
	}
	if move, err := e.PreferBranch(chain, a3, a3); err != nil || move {
		t.Fatalf("the same head: %v %v", move, err)
	}
	// A reader without state (the header chain's) ties two bonded blocks
	// on the seal hash, stated.
	if move, _ := e.PreferBranch(chain.fakeChain, a, b); move != (bytes.Compare(hb[:], ha[:]) < 0) {
		t.Fatal("without state the seal hash does not decide")
	}
	// A head whose branch the chain does not hold is a fault, never a
	// silent answer.
	orphan := childOf(&types.Header{Number: a.Number, Extra: []byte("elsewhere")})
	if _, err := e.PreferBranch(chain, a3, orphan); !errors.Is(err, errForkNoAncestor) {
		t.Fatalf("orphan: %v", err)
	}
}
