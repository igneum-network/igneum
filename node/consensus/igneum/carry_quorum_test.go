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
)

// TestCarryQuorumByTableShare (the design seat's ruling of 03:4x UK, 12
// October 2026): the carry's quorum is ceil(2/3 x E_att x the bonded share
// of the epoch's table): 16 at devnet9's 49,152 of 65,536 units, 22 near
// one, 1 at the smallest bonded share, 0 in an epoch whose table holds no
// units or is missing (the ninth chain's epoch 2, written before its
// bonds), and 0 where the endorsement rule is off.
func TestCarryQuorumByTableShare(t *testing.T) {
	e := genesisEngine(t)
	defer e.Close()
	s := e.config.Parts.Sortition
	s.EndorseExpected = 32
	st := nativeState(t, big.NewInt(1_000_000_000))
	write := func(epoch, nTotal uint64) {
		st.SetState(testRegistry, tableSlot(epoch, 0), common.Hash{0x55})
		st.SetState(testRegistry, tableSlot(epoch, 1), common.BigToHash(big.NewInt(1)))
		st.SetState(testRegistry, tableSlot(epoch, 2), common.BigToHash(new(big.Int).SetUint64(nTotal)))
		st.SetState(testRegistry, tableSlot(epoch, 3), common.BigToHash(big.NewInt(1)))
	}
	// Epoch 2's table missing (the ninth chain's case): no hold.
	if q := e.carryQuorumAt(st, 250); q != 0 {
		t.Fatalf("missing table: quorum %d, want 0", q)
	}
	// Epoch 2 written with no units: no hold.
	write(2, 0)
	if q := e.carryQuorumAt(st, 250); q != 0 {
		t.Fatalf("empty table: quorum %d, want 0", q)
	}
	for _, c := range []struct{ units, want uint64 }{
		{49152, 16}, // devnet9: 0.75 x 32 x 2/3 = 16
		{65536, 22}, // the launch form near one
		{1, 1},      // the smallest bonded share holds for one
		{32768, 11}, // a half: ceil(10.67)
	} {
		write(3, c.units)
		if q := e.carryQuorumAt(st, 350); q != c.want {
			t.Fatalf("units %d: quorum %d, want %d", c.units, q, c.want)
		}
	}
	// The rule off: zero whatever the table.
	s.EndorseExpected = 0
	if q := e.carryQuorumAt(st, 350); q != 0 {
		t.Fatalf("rule off: quorum %d", q)
	}
	s.EndorseExpected = 32
	if q := e.carryQuorumAt(nil, 350); q != 0 {
		t.Fatalf("no state: quorum %d", q)
	}
}
