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
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// TestDAGParentsInsideTheSealHash (F-803, the panel's fourth reading): the
// merged parents are inside the seal hash, so the proof of work binds the
// mergeset: two headers alike but for their merged parents have different
// seal hashes, and a header without the field keeps the seal hash it had
// (the field encodes as absent, so every block of the chains of record,
// where the field is refused, seals as before).
func TestDAGParentsInsideTheSealHash(t *testing.T) {
	e := newTestEngine(t)
	h := childOf(genesisHeader())
	plain := e.SealHash(h)
	withNil := *h
	withNil.SetMergedParents(nil)
	if e.SealHash(&withNil) != plain {
		t.Fatal("an empty merged-parents field changes the seal hash")
	}
	one := *h
	one.SetMergedParents([]common.Hash{common.HexToHash("0x01")})
	two := *h
	two.SetMergedParents([]common.Hash{common.HexToHash("0x02")})
	if e.SealHash(&one) == plain {
		t.Fatal("the merged parents are outside the seal hash")
	}
	if e.SealHash(&one) == e.SealHash(&two) {
		t.Fatal("two mergesets seal alike")
	}
	both := *h
	both.SetMergedParents([]common.Hash{common.HexToHash("0x01"), common.HexToHash("0x02")})
	if e.SealHash(&both) == e.SealHash(&one) {
		t.Fatal("a second merged parent is outside the seal hash")
	}
}
