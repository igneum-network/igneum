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
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// TestForkLengthGate (F-44 part 3): the fork choice's length order is OFF
// with no forks and at zero, and on from the named height read at the
// longer head's height.
func TestForkLengthGate(t *testing.T) {
	e := clockEngine(t)
	defer e.Close()
	at := func(n uint64) *types.Header { return &types.Header{Number: new(big.Int).SetUint64(n)} }
	if e.ForkLongerWins(at(10), at(12)) {
		t.Fatal("no forks: the length order is on")
	}
	e.SetRuleForks(&ctypes.IgneumRuleForks{})
	if e.ForkLongerWins(at(10), at(12)) {
		t.Fatal("zero: the length order is on")
	}
	e.SetRuleForks(&ctypes.IgneumRuleForks{ForkLengthV1: 500})
	if e.ForkLongerWins(at(498), at(499)) {
		t.Fatal("below the height: the length order is on")
	}
	if !e.ForkLongerWins(at(498), at(500)) || !e.ForkLongerWins(at(500), at(498)) {
		t.Fatal("the longer head at the height: the length order is off")
	}
}
