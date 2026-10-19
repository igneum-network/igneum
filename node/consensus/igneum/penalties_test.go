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
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm/igneumreg"
	"github.com/holiman/uint256"
)

// TestRegistryHashTwins asserts the registry's SigningHash and SealHash
// (core/vm/igneumreg/header.go, the equivocation check's hashes) equal
// the engine's on every header form: bare, with a base fee, a bond key,
// a payout list root, a round, a signature. A field added to the seal
// hash here without its twin there is this test red.
func TestRegistryHashTwins(t *testing.T) {
	e := newTestEngine(t)
	defer e.Close()
	key := common.HexToHash("0xab")
	root := common.HexToHash("0xcd")
	forms := []*types.Header{
		{Number: big.NewInt(1), Difficulty: big.NewInt(1)},
		{Number: big.NewInt(2), Difficulty: big.NewInt(1), BaseFee: big.NewInt(7), Extra: []byte("x")},
		{Number: big.NewInt(3), Difficulty: big.NewInt(1), BaseFee: big.NewInt(7), BondKey: &key},
		{Number: big.NewInt(4), Difficulty: big.NewInt(1), BondKey: &key, WithdrawalsHash: &root},
		{Number: big.NewInt(5), Difficulty: big.NewInt(1), BondKey: &key, Round: 3},
		{Number: big.NewInt(6), Difficulty: big.NewInt(1), BondKey: &key, Round: 3, Sig: make([]byte, ed25519.SignatureSize)},
	}
	for i, h := range forms {
		if igneumreg.SigningHash(h) != e.SigningHash(h) {
			t.Fatalf("form %d: signing hashes differ", i)
		}
		if igneumreg.SealHash(h) != e.SealHash(h) {
			t.Fatalf("form %d: seal hashes differ", i)
		}
	}
}

// TestMarkedKeySealsNothing is the deterministic half of one block per key
// per height: a bonded key marked by an equivocation at the parent's
// state is refused by the validity rule at every round (its own code) and
// draws no units, even from a table that still carries them; an unmarked
// key is unchanged.
func TestMarkedKeySealsNothing(t *testing.T) {
	e := genesisEngine(t)
	defer e.Close()
	e.config.Parts.Authority.RegistryVersion = igneumreg.VersionPenalties
	st := nativeState(t, big.NewInt(1_000_000))
	// A key whose bytes are an Ed25519 public key (no registered signer).
	var seed [ed25519.SeedSize]byte
	seed[0] = 3
	priv := ed25519.NewKeyFromSeed(seed[:])
	key := common.BytesToHash(priv.Public().(ed25519.PublicKey))
	// A floor of 160,000 set directly (as TestGenesisTableWriteAndRead
	// does: the test parts' issuance is far above the test's SRef): the
	// cap 26 percent of it = 41,600, the unit 160,000 >> 16 = 2, so a
	// 40,000 bond holds the quarter cap of 16,384 units.
	st.SetState(testRegistry, igneumreg.SlotFloor, common.BigToHash(big.NewInt(160_000)))
	v := uint256.NewInt(40_000)
	st.SubBalance(testHolder, v)
	st.AddBalance(testRegistry, v)
	ctx := igneumreg.Context{Version: igneumreg.VersionPenalties, Number: 1}
	if _, err := igneumreg.Exec(ctx, st, testRegistry, testHolder, testHolder, v, key[:]); err != nil {
		t.Fatalf("bond: %v", err)
	}
	d, err := e.drawAt(st, key, 5)
	if err != nil || d.units == 0 {
		t.Fatalf("the bonded key draws no units: %v, %+v", err, d)
	}
	sign := func(round uint64) *types.Header {
		h := &types.Header{Number: big.NewInt(5), Difficulty: big.NewInt(1), Coinbase: testHolder, BondKey: &key, Round: round}
		sh := e.SigningHash(h)
		h.Sig = ed25519.Sign(priv, sh[:])
		return h
	}
	// Unmarked: the liveness round's block passes the stateful rule (any
	// signer, unkeyed); the drawn-round check below the liveness round
	// reads the draw (eligible or not, never the mark's code).
	if err := e.verifySortitionGenesis(sign(17), st, false); err != nil {
		t.Fatalf("unmarked liveness block: %v", err)
	}
	if err := e.verifySortitionGenesis(sign(d.round), st, false); err != nil {
		t.Fatalf("unmarked drawn block: %v", err)
	}
	// Marked (the registry's slot, as the equivocation operation writes
	// it): refused at every round by the mark's code; no units.
	st.SetState(testRegistry, igneumreg.MarkedSlot(key), common.BigToHash(big.NewInt(1)))
	for _, round := range []uint64{0, d.round, 16, 17} {
		if err := e.verifySortitionGenesis(sign(round), st, false); !errors.Is(err, errKeyMarked) {
			t.Fatalf("marked block at round %d: %v", round, err)
		}
	}
	if d2, err := e.drawAt(st, key, 5); err != nil || d2.units != 0 || !d2.open {
		t.Fatalf("a marked key draws: %v, %+v", err, d2)
	}
}

// TestMinerRewardForTheReporter: the engine's one block's reward is the
// miner's share of the subsidy at a height (what core.NewEVMBlockContext
// hands the registry), zero at genesis.
func TestMinerRewardForTheReporter(t *testing.T) {
	e := newTestEngine(t)
	defer e.Close()
	if e.MinerReward(0).Sign() != 0 {
		t.Fatal("a reward at genesis")
	}
	miner, _ := e.config.Parts.Issuance.Reward(100)
	if got := e.MinerReward(100); got.Cmp(miner) != 0 || got.Sign() <= 0 {
		t.Fatalf("reward at 100: %v, want %v", got, miner)
	}
}
