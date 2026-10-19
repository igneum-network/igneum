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
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm/igneumreg"
	"github.com/holiman/uint256"
)

// genesisEngine is the genesis form on a test engine: the native registry,
// the floor at 16 percent of max(issued, SRef), one epoch of 100 blocks.
func genesisEngine(t *testing.T) *Engine {
	t.Helper()
	e := authorityEngine(t, false)
	e.config.Parts.Authority.RegistryVersion = igneumreg.Version
	e.config.Parts.Authority.BRefBps = 1600
	e.config.Parts.Authority.SRef = big.NewInt(1_000_000)
	e.config.Parts.Sortition = &SortitionParams{Height: 1, EpochBlocks: 100, SeedBlocks: 16, Expected: 1, Delta: 6, MaxRound: 16, UnbondedRound: 17, MaxPayouts: 256, OpenSeat: true}
	return e
}

// nativeState is a state with the native registry: the placeholder code,
// the static cap, the fund address.
func nativeState(t *testing.T, cap *big.Int) *state.StateDB {
	t.Helper()
	st, err := state.New(types.EmptyRootHash, state.NewDatabase(rawdb.NewMemoryDatabase()), nil)
	if err != nil {
		t.Fatal(err)
	}
	st.SetCode(testRegistry, igneumreg.Placeholder)
	st.SetState(testRegistry, igneumreg.SlotCap, common.BigToHash(cap))
	st.SetState(testRegistry, igneumreg.SlotFund, common.BytesToHash(testFund.Bytes()))
	st.SetBalance(testHolder, uint256.NewInt(10_000_000))
	st.SetBalance(testOther, uint256.NewInt(10_000_000))
	return st
}

// bondNative posts a bond through the registry package as the EVM would.
func bondNative(t *testing.T, st *state.StateDB, from common.Address, key common.Hash, amount int64) {
	t.Helper()
	v := uint256.NewInt(uint64(amount))
	st.SubBalance(from, v)
	st.AddBalance(testRegistry, v)
	if _, err := igneumreg.Run(st, testRegistry, from, from, v, key[:]); err != nil {
		t.Fatalf("bond: %v", err)
	}
}

func TestOpenSeatThresholdAndDraw(t *testing.T) {
	s := &SortitionParams{Expected: 1, MaxRound: 16, UnbondedRound: 17}
	// An empty table: the open seat holds every unit, theta_open(r) is the
	// full draw's theta (E x 2^r / 2^16 of the space) and round 0 opens
	// with probability E / 2^16 x 2^16 = 1 unit expected: the same round
	// statistics as a full table.
	if ThetaOpen(1, 0, 0).Cmp(Theta(1, 0).Lsh(Theta(1, 0), 16)) != 0 {
		t.Fatalf("theta_open at n = 0: %v", ThetaOpen(1, 0, 0))
	}
	// A full table: the open seat never draws.
	if ThetaOpen(1, 16, NotionalUnits).Sign() != 0 {
		t.Fatal("theta_open at a full table is not zero")
	}
	// Half a table at round 16: everything.
	if ThetaOpen(1, 16, NotionalUnits/2).Cmp(new(big.Int).Lsh(big.NewInt(1), 256)) != 0 {
		t.Fatal("theta_open saturates at 2^256")
	}
	// The design seat's linear form: at an empty table and E = 1 the open
	// seat's threshold at round 0 is min(1, E x 2^16 / 2^16) = everything,
	// so every height opens at round 0 (plain proof of work at the
	// schedule's full speed); at a half-full table round 0 opens with
	// probability one half and the rest at round 1.
	seed := common.HexToHash("0x01")
	var atZero, none int
	for h := uint64(1); h <= 2000; h++ {
		r := s.OpenSeatRound(seed, 0, h)
		if r == 0 {
			atZero++
		}
		if r > s.MaxRound {
			none++
		}
	}
	if atZero != 2000 || none != 0 {
		t.Fatalf("empty table: round 0 at %d of 2000, none at %d", atZero, none)
	}
	atZero = 0
	var atOne int
	for h := uint64(1); h <= 2000; h++ {
		switch s.OpenSeatRound(seed, NotionalUnits/2, h) {
		case 0:
			atZero++
		case 1:
			atOne++
		}
	}
	if atZero < 900 || atZero > 1100 || atZero+atOne != 2000 {
		t.Fatalf("half table: round 0 at %d, round 1 at %d of 2000", atZero, atOne)
	}
	// The units: priced off the floor, capped at the quarter.
	u := unitPrice(big.NewInt(0), big.NewInt(1<<20))
	if u.Int64() != 16 {
		t.Fatalf("unit price off the floor: %v", u)
	}
	if unitsOf(big.NewInt(160), u) != 10 || unitsOf(big.NewInt(1<<30), u) != MaxUnitsPerKey || unitsOf(big.NewInt(0), u) != 0 {
		t.Fatal("units of a bond")
	}
}

func TestGenesisTableWriteAndRead(t *testing.T) {
	e := genesisEngine(t)
	defer e.Close()
	st := nativeState(t, big.NewInt(1_000_000_000))
	chain := newSupplyChain()
	for i := 0; i < 99; i++ {
		chain.extend(0, 1)
	}
	// Epochs 0 and 1 read live from the state: no bonds, every unit open.
	t0, err := e.tableAt(st, 0)
	if err != nil || !t0.live || t0.nTotal != 0 || t0.seed != genesisSeed(0) {
		t.Fatalf("live table of epoch 0: %+v %v", t0, err)
	}
	// Epoch 2's table is missing until written.
	if _, err := e.tableAt(st, 2); !errors.Is(err, errTableMissing) {
		t.Fatalf("epoch 2 before the write: %v", err)
	}
	// The floor: Finalize at height 99 writes B_floor = 16 percent of
	// max(issued, SRef); the test parts' issuance to 99 is far above the
	// test's SRef, so the floor is 16 percent of the issuance.
	h99 := chain.canonical[99]
	e.writeFloor(h99, st)
	wantFloor := new(big.Int).Mul(e.config.Parts.Issuance.Issued(99), big.NewInt(1600))
	wantFloor.Div(wantFloor, big.NewInt(10000))
	if f := igneumreg.Floor(st, testRegistry); f.Cmp(wantFloor) != 0 {
		t.Fatalf("floor %v, want %v", f, wantFloor)
	}
	// A small SRef, a small floor: the rest of the test prices the unit
	// off a floor of 160,000 (SRef 1,000,000, the issuance below it on a
	// chain whose issuance is zero), set directly.
	st.SetState(testRegistry, igneumreg.SlotFloor, common.BigToHash(big.NewInt(160_000)))
	// A bond inside the cap (26 percent of max(balance, floor) = 41,600).
	bondNative(t, st, testHolder, testKey, 40_000)
	// Height 99 is the last block of epoch 0: the table of epoch 2 is
	// written from this state and the 16 hashes ending at the parent.
	if err := e.writeTable(chain, h99, st); err != nil {
		t.Fatalf("writeTable: %v", err)
	}
	t2, err := e.tableAt(st, 2)
	if err != nil || t2.live {
		t.Fatalf("epoch 2 after the write: %+v %v", t2, err)
	}
	// u = max(40,000, 160,000) / 2^16 = 2 (integer); the key holds
	// min(40,000 / 2, 16,384) = 16,384 units (the quarter cap).
	if t2.u.Int64() != 2 || t2.nTotal != MaxUnitsPerKey || e.unitsAt(st, t2, testKey) != MaxUnitsPerKey {
		t.Fatalf("table of epoch 2: u %v total %d key %d", t2.u, t2.nTotal, e.unitsAt(st, t2, testKey))
	}
	// Another key is open-seat in that epoch; the open share is three
	// quarters.
	other := common.HexToHash("0x0abc")
	if e.unitsAt(st, t2, other) != 0 {
		t.Fatal("an unbonded key holds units")
	}
	d, err := e.drawAt(st, other, 250)
	if err != nil || !d.open || d.table.epoch != 2 {
		t.Fatalf("open draw: %+v %v", d, err)
	}
	db, err := e.drawAt(st, testKey, 250)
	if err != nil || db.open || db.units != MaxUnitsPerKey {
		t.Fatalf("bonded draw: %+v %v", db, err)
	}
	// Height 98 is not an epoch end: nothing written.
	st2 := nativeState(t, big.NewInt(1_000_000_000))
	if err := e.writeTable(chain, chain.canonical[98], st2); err != nil {
		t.Fatal(err)
	}
	if _, err := e.tableAt(st2, 2); !errors.Is(err, errTableMissing) {
		t.Fatal("a table written off the epoch end")
	}
	// Determinism: the same inputs on a fresh state give the same seed.
	st3 := nativeState(t, big.NewInt(1_000_000_000))
	st3.SetState(testRegistry, igneumreg.SlotFloor, common.BigToHash(big.NewInt(160_000)))
	bondNative(t, st3, testHolder, testKey, 40_000)
	if err := e.writeTable(chain, h99, st3); err != nil {
		t.Fatal(err)
	}
	t3, _ := e.tableAt(st3, 2)
	if t3.seed != t2.seed || t3.nTotal != t2.nTotal {
		t.Fatal("two states diverge on one table")
	}
}

func TestKeyedSealMarks(t *testing.T) {
	e := genesisEngine(t)
	defer e.Close()
	var k keyedSeals
	h := common.HexToHash("0x11")
	if _, known := k.get(h); known {
		t.Fatal("unknown mark reads known")
	}
	k.set(h, false)
	if keyed, known := k.get(h); !known || keyed {
		t.Fatal("mark not kept")
	}
	// An unkeyed document reads zero rounds whatever the mark; the test
	// engine's null algorithm is unkeyed.
	if e.keyedRoundsFor(&types.Header{Number: big.NewInt(5)}) != 0 {
		t.Fatal("rounds on an unkeyed document")
	}
}
