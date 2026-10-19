// Copyright 2026 The core-geth Authors
// This file is part of the core-geth library.
//
// The core-geth library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The core-geth library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the core-geth library. If not, see <http://www.gnu.org/licenses/>.

package igneumhash

import (
	"fmt"
	"math/big"
	"runtime"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// The G1 harness (docs/igneumhash/VERIFIER-R0A.md): the verifier's cost per
// proof under hostile inputs, per parameter set, as ns/op, B/op and allocs/op.
// Run on the build box:
//
//	go test -run xxx -bench 'Verifier' -benchmem ./consensus/igneum/pow/igneumhash/

func benchParams() []Params {
	var out []Params
	for _, scratch := range []uint32{16 << 10, 64 << 10} {
		for _, rounds := range []uint32{32, 64} {
			for _, chain := range []uint32{1, 4, 16} {
				out = append(out, Params{Version: Version0, DatasetBytes: 1 << 30, ScratchBytes: scratch, Rounds: rounds, Lanes: 8, EpochLength: 4096, ItemChain: chain})
			}
		}
	}
	for _, scratch := range []uint32{0, 4 << 10} {
		for _, rounds := range []uint32{32, 64} {
			for _, cache := range []uint64{32 << 20, 64 << 20} {
				for _, parents := range []uint32{256, 512} {
					out = append(out, Params{Version: Version1, DatasetBytes: 1 << 30, ScratchBytes: scratch, Rounds: rounds, Lanes: 8, EpochLength: 4096, ItemChain: 1, CacheBytes: cache, Parents: parents})
				}
			}
		}
	}
	// The registered shape of record (the hash team's v8 twin,
	// v8-d2048-s0-r64-k32-p256-w256: W256, rounds 64, parents 256, cache 32
	// MiB, dataset 2,048 MiB) under both wide versions the Go verifier runs,
	// for the minimum node's cost row (the project's order of 20:4x UK, 12 October
	// 2026); the hash team names which is the twin of record.
	for _, v := range []uint32{Version4, Version6} {
		out = append(out, Params{Version: v, DatasetBytes: 2048 << 20, ScratchBytes: 0, Rounds: 64, Lanes: 8, EpochLength: 4096, ItemChain: 1, CacheBytes: 32 << 20, Parents: 256, Width: 256})
	}
	return out
}

func label(p Params) string {
	if p.Wide() {
		return fmt.Sprintf("v%d/w%d/rounds=%d/cache=%dM/parents=%d", p.Version, p.Width, p.Rounds, p.CacheBytes>>20, p.Parents)
	}
	if p.Version == Version1 {
		return fmt.Sprintf("v1/scratch=%dK/rounds=%d/cache=%dM/parents=%d", p.ScratchBytes>>10, p.Rounds, p.CacheBytes>>20, p.Parents)
	}
	return fmt.Sprintf("v0/scratch=%dK/rounds=%d/chain=%d", p.ScratchBytes>>10, p.Rounds, p.ItemChain)
}

// The version 1 cache: build time and bytes per epoch, the chapter 27
// number for the light verifier, and the two-epoch overlap.
func BenchmarkCacheBuild(b *testing.B) {
	es := NewEnvelope(7310, DefaultParams()).EpochSeed(1)
	for _, size := range []uint64{32 << 20, 64 << 20} {
		b.Run(fmt.Sprintf("cache=%dM", size>>20), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = BuildCache(es, size)
			}
		})
	}
}

// BenchmarkVerifierNewEpochV1 is a proof in a never-seen epoch under version
// 1: one cache build plus one proof, the worst single verification a peer can
// cause, and bounded by the two-entry cache (a sweep of epochs costs one
// build each, never more than two resident).
func BenchmarkVerifierNewEpochV1(b *testing.B) {
	p := DefaultParams()
	a := New(7310, p, specWork{})
	h := common.HexToHash("0x11")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		a.Verify(h, uint64(i)*4096, uint64(i), common.Hash{}, big.NewInt(1), false)
	}
	if got := a.CacheResident(); got > int(EpochCacheEntries*p.CacheBytes) {
		b.Fatalf("cache resident %d bytes after an epoch sweep, want at most %d epochs", got, EpochCacheEntries)
	}
}

func BenchmarkVerifierValid(b *testing.B) {
	for _, p := range benchParams() {
		a := New(7310, p, specWork{})
		h := common.HexToHash("0x11")
		mix, _ := a.Compute(h, 10, 5)
		b.Run(label(p), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if err := a.Verify(h, 10, 5, mix, big.NewInt(1), false); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// An invalid proof (wrong nonce for the mix) costs the same as a valid one:
// the verifier cannot tell before the work.
func BenchmarkVerifierInvalidProof(b *testing.B) {
	for _, p := range benchParams() {
		a := New(7310, p, specWork{})
		h := common.HexToHash("0x11")
		mix, _ := a.Compute(h, 10, 5)
		b.Run(label(p), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if err := a.Verify(h, 10, uint64(i)+6, mix, big.NewInt(1), false); err == nil {
					b.Fatal("accepted")
				}
			}
		})
	}
}

// Extreme targets: the maximum difficulty (target 0) costs one proof; zero
// and nil difficulty cost nothing, rejected before any work.
func BenchmarkVerifierExtremeTarget(b *testing.B) {
	p := DefaultParams()
	a := New(7310, p, specWork{})
	h := common.HexToHash("0x11")
	mix, _ := a.Compute(h, 10, 5)
	maxD := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	b.Run("max-difficulty", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			a.Verify(h, 10, 5, mix, maxD, false)
		}
	})
	b.Run("zero-difficulty", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			a.Verify(h, 10, 5, mix, new(big.Int), false)
		}
	})
	b.Run("nil-difficulty", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			a.Verify(h, 10, 5, mix, nil, false)
		}
	})
	b.Run("over-wide-difficulty", func(b *testing.B) {
		b.ReportAllocs()
		wide := new(big.Int).Lsh(big.NewInt(1), 300)
		for i := 0; i < b.N; i++ {
			a.Verify(h, 10, 5, mix, wide, false)
		}
	})
}

// Two consecutive epochs resident: alternating heights across a boundary
// must cost no more than one epoch (the two-entry seed cache), and a sweep
// over many epochs costs one extra BLAKE3 per new epoch, never a dataset.
func BenchmarkVerifierEpochs(b *testing.B) {
	p := DefaultParams()
	a := New(7310, p, specWork{})
	h := common.HexToHash("0x11")
	b.Run("alternating-boundary", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			height := uint64(4095 + i%2)
			a.Verify(h, height, uint64(i), common.Hash{}, big.NewInt(1), false)
		}
	})
	b.Run("every-proof-new-epoch", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			a.Verify(h, uint64(i)*4096, uint64(i), common.Hash{}, big.NewInt(1), false)
		}
	})
}

// TestVerifierMemoryBounded: thousands of invalid proofs leave the heap
// where it started plus at most the three resident epoch caches; nothing
// accumulates per proof, and an epoch sweep never holds more than three
// caches (NO-01).
func TestVerifierMemoryBounded(t *testing.T) {
	p := DefaultParams()
	a := New(7310, p, specWork{})
	h := common.HexToHash("0x11")
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	// Two epochs, alternating across the boundary: the common case.
	for i := 0; i < 2_000; i++ {
		a.Verify(h, uint64(4095+i%2), uint64(i)*7919, common.Hash{byte(i)}, big.NewInt(int64(i%3)), false)
	}
	// Twenty distinct epochs: one cache build each, two resident at the end.
	for i := 0; i < 20; i++ {
		a.Verify(h, uint64(i)*4096*7, uint64(i), common.Hash{byte(i)}, big.NewInt(1), false)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	bound := int64(EpochCacheEntries*p.CacheBytes) + 4<<20
	if grown := int64(after.HeapInuse) - int64(before.HeapInuse); grown > bound {
		t.Fatalf("heap in use grew by %d bytes over the proofs, bound %d (%d caches and slack)", grown, bound, EpochCacheEntries)
	}
	if got := a.CacheResident(); got > int(EpochCacheEntries*p.CacheBytes) {
		t.Fatalf("cache resident %d bytes, want at most %d epochs", got, EpochCacheEntries)
	}
	for i := range a.cache {
		for j := range a.cache {
			if i != j && a.cache[i].set && a.cache[j].set && a.cache[i].epoch == a.cache[j].epoch {
				t.Fatal("cache holds one epoch twice")
			}
		}
	}
}
