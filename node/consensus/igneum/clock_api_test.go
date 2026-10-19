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
)

// TestSortitionPrintsTheClock: igneum_sortition carries the clock's state
// after the head under the amendment (the two difficulties, the times, the
// rank and PA-48b's dark count, slot 5), absent before the rule's first
// block and off the amendment.
func TestSortitionPrintsTheClock(t *testing.T) {
	e, chain := mineShareEngine(t)
	defer e.Close()
	s := e.config.Parts.Sortition
	s.Delta, s.SealWindowDiv, s.TPiMs, s.TMin, s.DMin = 20, 4, 250, 10, big.NewInt(1_000)
	s.Rank0FallbackBlocks, s.SealStaggerMs = DefaultRank0FallbackBlocks, DefaultSealStaggerMs
	api := &IgneumAPI{engine: e, chain: chain}
	info, err := api.Sortition()
	if err != nil {
		t.Fatal(err)
	}
	if info.Clock != nil {
		t.Fatalf("a clock before the rule's first block: %+v", info.Clock)
	}
	e.writeClock(chain.st, &Clock{Chain: big.NewInt(5_000), Seal: big.NewInt(1_200), Time: 900, ParentTime: 880, Rank: 2, Dark: 7}, true)
	info, err = api.Sortition()
	if err != nil || info.Clock == nil {
		t.Fatalf("the clock: %+v %v", info.Clock, err)
	}
	c := info.Clock
	if (*big.Int)(c.Chain).Int64() != 5_000 || (*big.Int)(c.Seal).Int64() != 1_200 || c.Time != 900 || c.ParentTime != 880 || c.Rank != 2 || c.Dark != 7 {
		t.Fatalf("the clock as printed: %+v", c)
	}
	s.SealWindowDiv = 0
	if info, err := api.Sortition(); err != nil || info.Clock != nil {
		t.Fatalf("off the amendment: %+v %v", info.Clock, err)
	}
}
