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
	"math/rand"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// TestSealDriftBoundsABondedBlocksTime: the outside review's condition on PA-48
// (the project's ruling of 18:1x UK, 11 October 2026): a bonded block's declared
// time (read statelessly by its carried shares) may not exceed the node's
// clock by more than the chain constant sealDriftMs (500 ms: zero whole
// seconds), an open-seat or liveness block keeps the node's 15 s; a block
// ahead is ErrFutureBlock (held to its time, never refused for good); off
// the amendment every block keeps the 15 s; the constant reads 500 when
// the genesis names none and the genesis may name another.
func TestSealDriftBoundsABondedBlocksTime(t *testing.T) {
	e := clockEngine(t)
	defer e.Close()
	s := e.config.Parts.Sortition
	s.SealDriftMs = DefaultSealDriftMs
	now := int64(1_000_000)
	bonded := func(at int64) *types.Header {
		return &types.Header{Number: big.NewInt(5), Time: uint64(at), Shares: make([]byte, 64)}
	}
	open := func(at int64) *types.Header {
		return &types.Header{Number: big.NewInt(5), Time: uint64(at)}
	}
	if err := e.verifyDeclaredTime(bonded(now), now); err != nil {
		t.Fatalf("a bonded block at the node's second: %v", err)
	}
	if err := e.verifyDeclaredTime(bonded(now+1), now); !errors.Is(err, consensus.ErrFutureBlock) {
		t.Fatalf("a bonded block one second ahead: %v", err)
	}
	if err := e.verifyDeclaredTime(open(now+15), now); err != nil {
		t.Fatalf("an open-seat block 15 s ahead: %v", err)
	}
	if err := e.verifyDeclaredTime(open(now+16), now); !errors.Is(err, consensus.ErrFutureBlock) {
		t.Fatalf("an open-seat block 16 s ahead: %v", err)
	}
	// A genesis naming 2,000 ms: two whole seconds.
	s.SealDriftMs = 2_000
	if err := e.verifyDeclaredTime(bonded(now+2), now); err != nil {
		t.Fatalf("a bonded block two seconds ahead at a 2 s drift: %v", err)
	}
	if err := e.verifyDeclaredTime(bonded(now+3), now); !errors.Is(err, consensus.ErrFutureBlock) {
		t.Fatalf("a bonded block three seconds ahead at a 2 s drift: %v", err)
	}
	// Off the amendment: the node's 15 s for a bonded block too.
	s.SealWindowDiv = 0
	if err := e.verifyDeclaredTime(bonded(now+15), now); err != nil {
		t.Fatalf("off the amendment, a bonded block 15 s ahead: %v", err)
	}
	// The default when the genesis names none.
	cfg := &ctypes.IgneumConfig{
		Authority: &ctypes.IgneumAuthority{Height: 1, Registry: testRegistry, Cap: big.NewInt(1), RegistryVersion: 4},
		Sortition: &ctypes.IgneumSortition{Height: 1, EpochBlocks: 100, SeedBlocks: 16, Expected: 1, Delta: 20, MaxRound: 16, UnbondedRound: 17, MaxPayouts: 256, OpenSeat: true, SealWindowDiv: 4, DMin: big.NewInt(1_000)},
	}
	p, err := SortitionFor(cfg)
	if err != nil || p.SealDriftMs != DefaultSealDriftMs {
		t.Fatalf("the default drift: %d, %v", p.SealDriftMs, err)
	}
	cfg.Sortition.SealDriftMs = 1_500
	if p, _ := SortitionFor(cfg); p.SealDriftMs != 1_500 {
		t.Fatal("a named drift not taken")
	}
}

// TestLotOneShot: the lot's one-shot form (blake3.Sum256 over the message
// in a stack buffer) is byte-identical to the streaming form over 10,000
// random inputs and the edge values (security's S13 profile; the project's order
// of 18:1x UK, 11 October 2026).
func TestLotOneShot(t *testing.T) {
	r := rand.New(rand.NewSource(0x1704))
	for i := 0; i < 10_000; i++ {
		var seed, key common.Hash
		r.Read(seed[:])
		r.Read(key[:])
		unit, height := r.Uint64(), r.Uint64()
		if i%7 == 0 {
			unit = uint64(r.Intn(16384))
			height = uint64(r.Intn(1 << 20))
		}
		if Lot(seed, key, unit, height) != lotStreaming(seed, key, unit, height) {
			t.Fatalf("the one-shot lot differs at input %d", i)
		}
	}
	for _, c := range []struct{ unit, height uint64 }{{0, 0}, {0, 1}, {65535, 1 << 40}, {^uint64(0), ^uint64(0)}} {
		if Lot(common.Hash{}, common.Hash{}, c.unit, c.height) != lotStreaming(common.Hash{}, common.Hash{}, c.unit, c.height) {
			t.Fatalf("the one-shot lot differs at the edge %+v", c)
		}
	}
	if lotMessageLen != 98 {
		t.Fatalf("the lot message is %d bytes, the record says 98", lotMessageLen)
	}
}
