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

package rxe1

import (
	"runtime"
	"sync"

	"github.com/ethereum/go-ethereum/consensus/igneum/igneumhash"
	"lukechampine.com/blake3"
)

// The dataset interface that must be bound (section 6): D(pid, epoch_seed)
// is the immutable dataset of the pinned R0A builder for the record's
// dataset parameters; a lookup is exactly 128 bytes; N is a power of two.
//
// Binding assumption B-1 (recorded in DIFF-RXE1.md for the hash team's
// confirmation or correction): the builder is R0A version 1's cache and
// parent walk (SPEC-R0A-v0.md 5.3) with the epoch seed E taken to be the
// RXE1 epoch_seed input directly, the cache of cache_bytes and the walk of
// parents items from the record; items by index on demand (light), as the
// node's verifier does. The builder depends on nothing else: not the mode,
// B, R, K or pid (section 13, stage A).

// Light serves items on demand through the per-epoch cache; a verifier
// allocates the cache and never the dataset (section 11).
type Light struct {
	p     Params
	seed  [32]byte
	cache []byte
}

// NewLight builds the cache for an epoch seed.
func NewLight(p Params, epochSeed [32]byte) *Light {
	cache := igneumhash.Params{CacheBytes: p.CacheBytes}.BuildCache(epochSeed)
	return &Light{p: p, seed: epochSeed, cache: cache}
}

func (l *Light) N() uint64 { return l.p.N() }

func (l *Light) Lookup(index uint64, out *[ItemBytes]byte) {
	igneumhash.ItemIntoV1(l.seed, l.cache, l.p.Parents, index, (*[igneumhash.ItemBytes]byte)(out))
}

// CacheDigest is BLAKE3 over the cache bytes in item order (the reference's
// cache_digest, diagnostic).
func (l *Light) CacheDigest() [32]byte { return blake3.Sum256(l.cache) }

// Full materialises every item (a miner's dataset, or the small test-only
// datasets of the bounded campaign).
type Full struct {
	n     uint64
	items []byte
}

// NewFull builds the whole dataset from a light source, in parallel across
// the cores (a miner's build; the harness's digest check).
func NewFull(l *Light) *Full {
	n := l.N()
	f := &Full{n: n, items: make([]byte, n*ItemBytes)}
	workers := runtime.NumCPU()
	if workers > 32 {
		workers = 32
	}
	chunk := (n + uint64(workers) - 1) / uint64(workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		lo, hi := uint64(w)*chunk, (uint64(w)+1)*chunk
		if hi > n {
			hi = n
		}
		if lo >= hi {
			continue
		}
		wg.Add(1)
		go func(lo, hi uint64) {
			defer wg.Done()
			var item [ItemBytes]byte
			for i := lo; i < hi; i++ {
				l.Lookup(i, &item)
				copy(f.items[i*ItemBytes:], item[:])
			}
		}(lo, hi)
	}
	wg.Wait()
	return f
}

func (f *Full) N() uint64 { return f.n }

func (f *Full) Lookup(index uint64, out *[ItemBytes]byte) {
	copy(out[:], f.items[index*ItemBytes:(index+1)*ItemBytes])
}

// Digest is BLAKE3 over the dataset bytes in item order (diagnostic; the
// stage A check that every M/F/E comparison used identical dataset bytes).
func (f *Full) Digest() [32]byte { return blake3.Sum256(f.items) }
