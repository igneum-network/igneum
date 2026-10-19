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

package forkid

import (
	"os"
	"testing"

	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/params"
)

// igneumDevForkID is the fork id of the development chain at every height
// (one fork, genesis, DESIGN.md 6.2), frozen on the build box; zero until then.
var igneumDevForkID = ID{Hash: [4]byte{0xd5, 0x9d, 0x35, 0x6a}, Next: 0} // re-frozen 10 October 2026 with the F-11 dev genesis

// TestIgneumDevForkID pins the development chain's fork id (PA-16): the
// handshake carries it and a node with another genesis or fork schedule is
// dropped (eth/protocols/eth: TestHandshake covers the drop).
func TestIgneumDevForkID(t *testing.T) {
	genesis := core.GenesisToBlock(params.DefaultIgneumDevGenesisBlock(), nil)
	have := NewID(params.IgneumDevChainConfig, genesis, 0, 0)
	if igneumDevForkID == (ID{}) {
		t.Logf("fork id not frozen; computed: Hash %x Next %d", have.Hash, have.Next)
		if os.Getenv("IGNEUM_REQUIRE_FIXTURES") != "" {
			t.Fatal("the development chain's fork id must be frozen")
		}
		return
	}
	if have != igneumDevForkID {
		t.Fatalf("fork id: have %x/%d want %x/%d", have.Hash, have.Next, igneumDevForkID.Hash, igneumDevForkID.Next)
	}
	// One fork only: the id is the same at any later height and time, and
	// Next is 0 (nothing scheduled).
	if later := NewID(params.IgneumDevChainConfig, genesis, 1_000_000, 1_000_000_000); later != have {
		t.Fatalf("fork id changes with height: %x/%d", later.Hash, later.Next)
	}
	if have.Next != 0 {
		t.Fatalf("Next is %d; the development chain schedules no fork", have.Next)
	}
}
