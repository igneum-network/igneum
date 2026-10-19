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

package igneumhash

import (
	"runtime"
	"sync"
)

// Dataset is one epoch's items materialised (section 5.1): a miner-side
// cache of item(i) for every i, identified by D. A verifier never builds one
// (Light serves the same items on demand, section 5.5); the harness builds
// one to check that the cached and the on-demand items agree and to compute
// the diagnostic dataset_digest of section 5.4.
type Dataset struct {
	params    Params
	epoch     uint64
	epochSeed [32]byte
	ID        [32]byte // D
	items     []byte   // N_ITEMS * W, item i at [W i, W (i + 1)); W = 128 below version 4
}

// Generate materialises the dataset for an epoch, in parallel across the
// cores: exactly DATASET_BYTES allocated.
func (p Params) Generate(epoch uint64) (*Dataset, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	ds := &Dataset{params: p, epoch: epoch, epochSeed: p.EpochSeed(epoch)}
	ds.ID = p.DatasetID(ds.epochSeed)
	var cache []byte
	if p.Version == Version1 || p.Wide() {
		cache = p.BuildCache(ds.epochSeed)
	}
	n := p.Items()
	width := uint64(p.Width())
	ds.items = make([]byte, n*width)
	workers := runtime.NumCPU()
	if workers > 32 {
		workers = 32
	}
	chunk := (n + uint64(workers) - 1) / uint64(workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		lo := uint64(w) * chunk
		hi := lo + chunk
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
				switch {
				case p.Wide():
					p.ItemIntoV4(ds.epochSeed, cache, i, ds.items[i*width:(i+1)*width])
					continue
				case cache != nil:
					ItemIntoV1(ds.epochSeed, cache, p.Parents, i, &item)
				default:
					ItemIntoV0(ds.epochSeed, i, p.ItemChain, &item)
				}
				copy(ds.items[i*ItemBytes:], item[:])
			}
		}(lo, hi)
	}
	wg.Wait()
	return ds, nil
}

func (ds *Dataset) Params() Params      { return ds.params }
func (ds *Dataset) Epoch() uint64       { return ds.epoch }
func (ds *Dataset) EpochSeed() [32]byte { return ds.epochSeed }

// Item copies item i out of the dataset (the first 128 bytes of a wider item).
func (ds *Dataset) Item(i uint64, out *[ItemBytes]byte) {
	w := uint64(ds.params.Width())
	copy(out[:], ds.items[i*w:i*w+ItemBytes])
}

// WideItem copies item i of width W out of the dataset.
func (ds *Dataset) WideItem(i uint64, out []byte) {
	w := uint64(ds.params.Width())
	copy(out, ds.items[i*w:(i+1)*w])
}

// Bytes is the dataset's size.
func (ds *Dataset) Bytes() uint64 { return uint64(len(ds.items)) }

// Digest is BLAKE3 over the whole dataset bytes in item order (section 5.4's
// diagnostic dataset_digest; never consensus).
func (ds *Dataset) Digest() [32]byte { return sum256(ds.items) }
