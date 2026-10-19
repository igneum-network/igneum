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

package issuance

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// testFund is the fund address the package's test forms pay to.
var testFund = common.HexToAddress("0x00000000000000000000000000000000000000f1")

// TestCheckRefusesTheZeroFundAddress (F-734, the review lead's reading of 18
// October 2026): a fund share set with the zero fund address is refused on
// a chain of record and allowed on a lab chain id; no fund share with the
// zero address passes, and a named fund passes; the refusal reads the
// FIRST form's address alone (the fund is one across every gate), so a
// next form without one is no fault.
func TestCheckRefusesTheZeroFundAddress(t *testing.T) {
	p := Params{Epoch: 10240, M: ign(1_000_000_000), Tail: new(big.Int), FundBps: 100, EpochDiv: 2048, TailPerEpoch: ign(20480)}
	if err := Check(p, false); err == nil {
		t.Fatal("a fund share paid to the zero address on a chain of record was accepted")
	}
	if err := Check(p, true); err != nil {
		t.Fatalf("a lab chain without a fund address refused: %v", err)
	}
	named := p
	named.FundAddress = testFund
	if err := Check(named, false); err != nil {
		t.Fatalf("a named fund refused: %v", err)
	}
	none := p
	none.FundBps = 0
	if err := Check(none, false); err != nil {
		t.Fatalf("no fund share with the zero address refused: %v", err)
	}
	// The next form names no fund (the first form's is read): no fault.
	next := Params{Epoch: 10240, M: ign(1_000_000_000), Tail: new(big.Int), FundBps: 100, EpochDiv: 2048, TailPerEpoch: ign(20480)}
	named.NextFrom, named.Next = 10240, &next
	if err := Check(named, false); err != nil {
		t.Fatalf("a next form without a fund address refused: %v", err)
	}
	// The gate rule is unchanged behind it.
	named.NextFrom = 10000
	if err := Check(named, false); err == nil {
		t.Fatal("a gate inside an epoch accepted")
	}
}
