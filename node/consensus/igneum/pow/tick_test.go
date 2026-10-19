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

package pow

import (
	"math/big"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// tickAlgo is a stand-in algorithm whose result is the fixed byte it is
// given (0xff: above every target but the loosest; 0x00: under every
// target), counting its attempts.
type tickAlgo struct {
	result   byte
	attempts int64
}

func (a *tickAlgo) Name() string                       { return "tick" }
func (a *tickAlgo) Epoch(height uint64) uint64         { return 0 }
func (a *tickAlgo) SeedHash(height uint64) common.Hash { return common.Hash{} }
func (a *tickAlgo) Compute(headerHash common.Hash, height uint64, nonce uint64) (common.Hash, common.Hash) {
	atomic.AddInt64(&a.attempts, 1)
	var r common.Hash
	for i := range r {
		r[i] = a.result
	}
	return common.Hash{1}, r
}
func (a *tickAlgo) Verify(headerHash common.Hash, height uint64, nonce uint64, mixDigest common.Hash, difficulty *big.Int, fullDataset bool) error {
	return nil
}
func (a *tickAlgo) Vectors() []Vector { return nil }

// TestSolveTickCountsEveryAttempt: the tick fires every SolveTickEvery
// attempts and once more at the abort with the remainder, the counts summing
// to the attempts made; a nil tick is the plain search, which finds at once
// on a target every result meets.
func TestSolveTickCountsEveryAttempt(t *testing.T) {
	a := &tickAlgo{result: 0xff}
	abort := make(chan struct{})
	var ticks, counted int64
	_, _, ok := SolveTick(a, common.Hash{2}, 1, big.NewInt(1<<40), 0, abort, func(n int64) {
		ticks++
		counted += n
		if ticks == 3 {
			close(abort)
		}
	})
	if ok {
		t.Fatal("found on a target no result meets")
	}
	if ticks < 3 || counted != atomic.LoadInt64(&a.attempts) {
		t.Fatalf("ticks %d, counted %d, attempts %d", ticks, counted, a.attempts)
	}
	if counted < 3*SolveTickEvery || counted > 4*SolveTickEvery {
		t.Fatalf("counted %d attempts over three ticks of %d", counted, SolveTickEvery)
	}
	b := &tickAlgo{result: 0x00}
	nonce, _, ok := Solve(b, common.Hash{2}, 1, big.NewInt(1), 7, make(chan struct{}))
	if !ok || nonce != 7 || b.attempts != 1 {
		t.Fatalf("the plain search: ok %v nonce %d attempts %d", ok, nonce, b.attempts)
	}
}
