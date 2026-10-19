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
	"crypto/ed25519"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm/igneumreg"
	"github.com/holiman/uint256"
)

// TestPrepareClockSetsTheTemplateDifficulty is devnet7's block-10 fault
// (13:2x UK, 11 October 2026): the sealer's template for a bonded key
// carried the chain difficulty and its own validation refused the sealed
// block ("a bonded block must carry the seal difficulty: have 8246, want
// 54000"). Through prepareClock a bonded template carries D_seal and an
// open-seat template D, each passing verifyClock at the same state; off
// the amendment the chain difficulty stands.
func TestPrepareClockSetsTheTemplateDifficulty(t *testing.T) {
	e := clockEngine(t)
	defer e.Close()
	s := e.config.Parts.Sortition
	st := nativeState(t, big.NewInt(1_000_000))
	// A bonded key: an Ed25519 public key as the bond key, 40,000 at a
	// floor of 160,000 (the quarter cap of units), as the mark test does.
	var seed [ed25519.SeedSize]byte
	seed[0] = 5
	priv := ed25519.NewKeyFromSeed(seed[:])
	key := common.BytesToHash(priv.Public().(ed25519.PublicKey))
	st.SetState(testRegistry, igneumreg.SlotFloor, common.BigToHash(big.NewInt(160_000)))
	v := uint256.NewInt(40_000)
	st.SubBalance(testHolder, v)
	st.AddBalance(testRegistry, v)
	if _, err := igneumreg.Exec(igneumreg.Context{Version: igneumreg.VersionPenalties, Number: 1}, st, testRegistry, testHolder, testHolder, v, key[:]); err != nil {
		t.Fatalf("bond: %v", err)
	}
	chain := &stateChain{fakeChain: newFakeChain(), st: st}
	genesis := &types.Header{Number: big.NewInt(0), Difficulty: big.NewInt(1 << 20), Time: 1_000}
	chain.add(genesis)
	// Block 1 under the rule: the parent is genesis; the clock seeds. The
	// template for the bonded key at its drawn round.
	d, err := e.drawAt(st, key, 1)
	if err != nil || d.open {
		t.Fatalf("the bonded key draws the open seat: %+v %v", d, err)
	}
	c1, err := e.ClockNext(st, genesis)
	if err != nil {
		t.Fatal(err)
	}
	if c1.Seal.Cmp(c1.Chain) == 0 {
		t.Fatal("the two difficulties coincide; the test proves nothing")
	}
	tmpl := &types.Header{Number: big.NewInt(1), ParentHash: genesis.Hash(), Difficulty: new(big.Int).Set(c1.Chain), BondKey: &key, Round: d.round, Time: genesis.Time + d.round*s.Delta + 1}
	if err := e.prepareClock(chain, tmpl, genesis); err != nil {
		t.Fatalf("prepareClock: %v", err)
	}
	if tmpl.Difficulty.Cmp(c1.Seal) != 0 {
		t.Fatalf("bonded template at %v, want D_seal %v (chain %v)", tmpl.Difficulty, c1.Seal, c1.Chain)
	}
	if err := e.verifyClock(tmpl, genesis, st, true); err != nil {
		t.Fatalf("the bonded template refused by the check: %v", err)
	}
	// An open-seat template (a key with no units) carries D.
	other := common.HexToHash("0xcc")
	open := &types.Header{Number: big.NewInt(1), ParentHash: genesis.Hash(), Difficulty: big.NewInt(1), BondKey: &other, Round: 0, Time: genesis.Time + 1}
	if err := e.prepareClock(chain, open, genesis); err != nil {
		t.Fatalf("prepareClock open: %v", err)
	}
	if open.Difficulty.Cmp(c1.Chain) != 0 {
		t.Fatalf("open template at %v, want D %v", open.Difficulty, c1.Chain)
	}
	if err := e.verifyClock(open, genesis, st, false); err != nil {
		t.Fatalf("the open template refused by the check: %v", err)
	}
	// The liveness round under the bonded key is open too.
	live := &types.Header{Number: big.NewInt(1), ParentHash: genesis.Hash(), Difficulty: big.NewInt(1), BondKey: &key, Round: s.UnbondedRound, Time: genesis.Time + s.UnbondedRound*s.Delta}
	if err := e.prepareClock(chain, live, genesis); err != nil || live.Difficulty.Cmp(c1.Chain) != 0 {
		t.Fatalf("liveness template at %v, want D %v (%v)", live.Difficulty, c1.Chain, err)
	}
	// PA-48a: Finalize of a bonded round-0 block by the only eligible key
	// writes its sealer's rank 0 into the clock's slot, so the next
	// block's ClockNext reads the leader's seal; an open-seat block writes
	// -1.
	chain.add(tmpl)
	if err := e.finalizeClock(chain, tmpl, st); err != nil {
		t.Fatalf("finalizeClock of the bonded block: %v", err)
	}
	if c, err := e.ReadClock(st); err != nil || c.Rank != 0 {
		t.Fatalf("the leader's rank in the clock: %+v %v", c, err)
	}
	open.Time = genesis.Time + 1
	chain.add(open)
	if err := e.finalizeClock(chain, open, st); err != nil {
		t.Fatalf("finalizeClock of the open block: %v", err)
	}
	if c, _ := e.ReadClock(st); c.Rank != -1 {
		t.Fatalf("the open seat's rank in the clock: %d", c.Rank)
	}
	// A chain without state access is a fault, never a silent chain
	// difficulty.
	if err := e.prepareClock(newFakeChain(), tmpl, genesis); err == nil {
		t.Fatal("no state access accepted")
	}
	// Off the amendment: the difficulty is left as Prepare set it.
	s.SealWindowDiv = 0
	off := &types.Header{Number: big.NewInt(1), ParentHash: genesis.Hash(), Difficulty: big.NewInt(777), BondKey: &key, Round: d.round}
	if err := e.prepareClock(chain, off, genesis); err != nil || off.Difficulty.Int64() != 777 {
		t.Fatalf("off: %v %v", off.Difficulty, err)
	}
}
