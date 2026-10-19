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
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// TestPolicyGateAt899And900 (the project's order of 05:3x UK, 12 October 2026, the
// second fault release node/0.9.3 on the ninth chain): under chain 7319's
// forks a node at 899 behaves as 0.9.2 and at 900 as the new form, by the
// gate alone: the carry quorum absolute below and by the table's share
// from 900; the carried key and the journal's record by the height below
// and by the parent from 900; the past-drift refusal off below and on from
// 900. No forks entry at all (a chain started on the release, a test
// engine) runs the parts from genesis; a zero entry is off.
func TestPolicyGateAt899And900(t *testing.T) {
	e := genesisEngine(t)
	defer e.Close()
	s := e.config.Parts.Sortition
	s.EndorseExpected = 32
	s.Delta = 20
	f := params.IgneumRuleForksForChainID(7319)
	if f == nil || f.PolicyV1 != 900 || f.ForkLengthV1 != 700 {
		t.Fatalf("the ninth chain's forks read %+v, want ForkLengthV1 700 and PolicyV1 900", f)
	}
	e.SetRuleForks(f)
	if e.policyOn(899) || !e.policyOn(900) || !e.policyOn(5000) {
		t.Fatal("the gate is not at 900")
	}
	// The quorum: absolute 22 at 899, the table's share (16 at 49,152
	// units) at 900.
	st := nativeState(t, big.NewInt(1_000_000_000))
	for _, epoch := range []uint64{8, 9} {
		st.SetState(testRegistry, tableSlot(epoch, 0), common.Hash{0x55})
		st.SetState(testRegistry, tableSlot(epoch, 1), common.BigToHash(big.NewInt(1)))
		st.SetState(testRegistry, tableSlot(epoch, 2), common.BigToHash(big.NewInt(49152)))
		st.SetState(testRegistry, tableSlot(epoch, 3), common.BigToHash(big.NewInt(1)))
	}
	if q := e.carryQuorumAt(st, 899); q != 22 {
		t.Fatalf("the quorum at 899 is %d, want the absolute 22", q)
	}
	if q := e.carryQuorumAt(st, 900); q != 16 {
		t.Fatalf("the quorum at 900 is %d, want 16 by the table's share", q)
	}
	// The carried key: by the height at 899, by the parent at 900.
	sh := types.MinedShare{Address: common.Address{0x11}, ExtraNonce: 5, Nonce: 9}
	p := common.Hash{0xaa}
	if k := e.carriedKeyAt(sh, 899, p); k != shareKey(sh.Address, 5, 9)+"@899" {
		t.Fatalf("the key at 899 is %q, want the height form", k)
	}
	if k := e.carriedKeyAt(sh, 900, p); k != carriedKey(sh, 900, p) {
		t.Fatalf("the key at 900 is %q, want the parent form", k)
	}
	if r := e.carriedRecordAt(sh.Address, 5, 9, 899, p); r[0] != shareJournalCarriedAt {
		t.Fatalf("the record at 899 is type %d, want 3", r[0])
	}
	if r := e.carriedRecordAt(sh.Address, 5, 9, 900, p); r[0] != shareJournalCarriedP {
		t.Fatalf("the record at 900 is type %d, want 7", r[0])
	}
	if r := e.uncarriedRecordAt(sh.Address, 5, 9, 899, p); r[0] != shareJournalUncarried {
		t.Fatalf("the uncarried record at 899 is type %d, want 6", r[0])
	}
	if r := e.uncarriedRecordAt(sh.Address, 5, 9, 900, p); r[0] != shareJournalUncarriedP {
		t.Fatalf("the uncarried record at 900 is type %d, want 8", r[0])
	}
	// The past-drift record is kept whatever the gate (the refusal reads
	// the gate at its sites); PastDrifted itself reads the record.
	now := uint64(time.Now().Unix())
	warmUp(e, 898-arrivalWarmup, 897, now)
	stale := &types.Header{Number: big.NewInt(898), Time: now - 2*s.Delta - 5}
	e.NoteArrival(stale)
	if !e.PastDrifted(stale) {
		t.Fatal("the drift record does not read")
	}
	// A zero entry is off everywhere; no entry is on from genesis.
	e.SetRuleForks(&ctypes.IgneumRuleForks{})
	if e.policyOn(5000) {
		t.Fatal("a zero PolicyV1 is on")
	}
	e.SetRuleForks(nil)
	if !e.policyOn(1) {
		t.Fatal("no forks entry: the parts are off at genesis")
	}
	// The wait class names the round wait and the no-bond-key refusal, not
	// another error.
	if !IsTemplateWait(errSortitionWait) || !IsTemplateWait(errBondKeyUnset) || IsTemplateWait(errors.New("other")) {
		t.Fatal("IsTemplateWait misreads its class")
	}
}
