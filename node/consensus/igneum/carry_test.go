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

	"github.com/ethereum/go-ethereum/core/types"
)

// TestCarryQuorum (F-44 part 2a): the carry's threshold is ceil(2 x E_att
// / 3) where the chain carries the endorsement rule, zero where it does
// not, and the pool holds nothing for a block it never saw.
func TestCarryQuorum(t *testing.T) {
	e := genesisEngine(t)
	defer e.Close()
	s := e.config.Parts.Sortition
	h := s.Height + 1
	for _, c := range []struct{ expected, want uint64 }{{32, 22}, {1, 1}, {3, 2}, {0, 0}} {
		s.EndorseExpected = c.expected
		if got := e.carryQuorum(h); got != c.want {
			t.Fatalf("E_att %d: quorum %d, want %d", c.expected, got, c.want)
		}
	}
	s.EndorseExpected = 32
	if s.Height > 0 && e.carryQuorum(s.Height-1) != 0 {
		t.Fatal("the quorum stands below the rule's height")
	}
	parent := &types.Header{Number: new(big.Int).SetUint64(h - 1), Time: 1000}
	if held := e.endorsementsHeld(parent); held != 0 {
		t.Fatalf("the pool holds %d for a block never seen", held)
	}
	if e.endorsementsHeld(nil) != 0 {
		t.Fatal("a nil block holds endorsements")
	}
}
