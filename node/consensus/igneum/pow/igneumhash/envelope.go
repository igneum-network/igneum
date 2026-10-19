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
	"encoding/binary"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"lukechampine.com/blake3"
)

// The domain tags (ENVELOPE-R0A-v0.md section 2), each written as its byte
// length then its bytes, so no tag's input is a prefix of another's.
const (
	tagSeed    = "IgneumHash/R0A/seed"
	tagEpoch   = "IgneumHash/R0A/epoch"
	tagItem    = "IgneumHash/R0A/item"
	tagDataset = "IgneumHash/R0A/dataset"
	tagFinal   = "IgneumHash/R0A/final"
	tagCache   = "IgneumHash/R0A/cache"
)

// domain is tag(T) || C || V || P: the prefix of every envelope hash input.
func domain(buf []byte, tag string, chainID uint64, version uint32, p [32]byte) []byte {
	buf = append(buf, byte(len(tag)))
	buf = append(buf, tag...)
	buf = binary.BigEndian.AppendUint64(buf, chainID)
	buf = binary.BigEndian.AppendUint32(buf, version)
	return append(buf, p[:]...)
}

// Envelope is the fixed part of the identity: chain, version, parameter set.
type Envelope struct {
	ChainID uint64
	Params  Params
	P       [32]byte
}

// NewEnvelope binds a chain id and a parameter set.
func NewEnvelope(chainID uint64, p Params) Envelope {
	return Envelope{ChainID: chainID, Params: p, P: p.Hash()}
}

// envBuf is the pooled input of Seed and Final: a stack array handed to
// blake3.Sum256 escapes to the heap once a call, which on a verifier is two
// allocations a proof (the project's order of 20:4x UK, 12 October 2026).
type envBuf struct {
	seed  [1 + len(tagSeed) + 8 + 4 + 32 + 32 + 8]byte
	final [1 + len(tagFinal) + 8 + 4 + 32 + 32 + 32]byte
}

var envPool = sync.Pool{New: func() any { return new(envBuf) }}

// Seed is S = BLAKE3(tag(T_SEED) || C || V || P || H || N) (section 4).
func (e Envelope) Seed(headerHash common.Hash, nonce uint64) [32]byte {
	eb := envPool.Get().(*envBuf)
	defer envPool.Put(eb)
	b := domain(eb.seed[:0], tagSeed, e.ChainID, e.Params.Version, e.P)
	b = append(b, headerHash[:]...)
	b = binary.BigEndian.AppendUint64(b, nonce)
	return blake3.Sum256(b)
}

// Epoch is floor(height / EPOCH_LENGTH) (section 5).
func (e Envelope) Epoch(height uint64) uint64 { return height / e.Params.EpochLength }

// EpochSeed is E = BLAKE3(tag(T_EPOCH) || C || V || P || u64be(epoch)).
func (e Envelope) EpochSeed(epoch uint64) [32]byte {
	var buf [1 + len(tagEpoch) + 8 + 4 + 32 + 8]byte
	b := domain(buf[:0], tagEpoch, e.ChainID, e.Params.Version, e.P)
	b = binary.BigEndian.AppendUint64(b, epoch)
	return blake3.Sum256(b)
}

// DatasetID is D = BLAKE3(tag(T_DATASET) || C || V || P || E || u64be(DATASET_BYTES)).
func (e Envelope) DatasetID(epochSeed [32]byte) [32]byte {
	var buf [1 + len(tagDataset) + 8 + 4 + 32 + 32 + 8]byte
	b := domain(buf[:0], tagDataset, e.ChainID, e.Params.Version, e.P)
	b = append(b, epochSeed[:]...)
	b = binary.BigEndian.AppendUint64(b, e.Params.DatasetBytes)
	return blake3.Sum256(b)
}

// Items derives dataset items for one epoch (SPEC-R0A-v0.md 5.3):
//
//	K_1 = BLAKE3k(E, tag(T_ITEM) || u64be(i))
//	K_j = BLAKE3k(E, tag(T_ITEM) || u64be(i) || K_(j-1))   j = 2..c
//	item(i) = the first 128 bytes of the keyed XOF over K_c's input
//
// Items are independent of one another, which is what gives the node light
// verification (section 5): a verifier derives only the items a proof touches
// and holds no dataset. One Items value holds one keyed hasher and is reused
// across the items of a proof; it is not safe for concurrent use.
type Items struct {
	h     *blake3.Hasher
	chain uint32
	in    [1 + len(tagItem) + 8 + 32]byte
	k     [32]byte // the chain link, kept here: a stack array handed to Sum escapes once a round
	fn    ItemFunc // the Item method bound once
}

// NewItems prepares item derivation for an epoch seed and an item chain.
func NewItems(epochSeed [32]byte, chain uint32) *Items {
	it := &Items{h: blake3.New(32, epochSeed[:]), chain: chain}
	it.in[0] = byte(len(tagItem))
	copy(it.in[1:], tagItem)
	it.fn = it.Item
	return it
}

// Fn is the deriver's Item as an ItemFunc, bound once.
func (it *Items) Fn() ItemFunc { return it.fn }

// Item derives item i into out.
func (it *Items) Item(i uint64, out *[ItemBytes]byte) {
	binary.BigEndian.PutUint64(it.in[1+len(tagItem):], i)
	n := 1 + len(tagItem) + 8
	k := &it.k
	for j := uint32(1); j < it.chain; j++ {
		it.h.Reset()
		it.h.Write(it.in[:n])
		it.h.Sum(k[:0])
		copy(it.in[1+len(tagItem)+8:], k[:])
		n = 1 + len(tagItem) + 8 + 32
	}
	it.h.Reset()
	it.h.Write(it.in[:n])
	it.h.XOF().Read(out[:])
}

// Item is the one-off form of Items.Item.
func Item(epochSeed [32]byte, chain uint32, i uint64) [ItemBytes]byte {
	var out [ItemBytes]byte
	NewItems(epochSeed, chain).Item(i, &out)
	return out
}

// Final is F = BLAKE3(tag(T_FINAL) || C || V || P || S || M) (section 7).
func (e Envelope) Final(seed, mix [32]byte) [32]byte {
	eb := envPool.Get().(*envBuf)
	defer envPool.Put(eb)
	b := domain(eb.final[:0], tagFinal, e.ChainID, e.Params.Version, e.P)
	b = append(b, seed[:]...)
	b = append(b, mix[:]...)
	return blake3.Sum256(b)
}

// BuildCache is version 1's per-epoch cache (SPEC-R0A-v0.md 5.3): N_CACHE
// items of 128 bytes built sequentially, cache(0) the keyed XOF over
// tag(T_CACHE) || u64be(0) and cache(j) over tag(T_CACHE) || u64be(j) ||
// cache(j-1)[0..32), so that no item is derivable without the one before it.
func BuildCache(epochSeed [32]byte, cacheBytes uint64) []byte {
	n := cacheBytes / ItemBytes
	cache := make([]byte, n*ItemBytes)
	h := blake3.New(32, epochSeed[:])
	var in [1 + len(tagCache) + 8 + 32]byte
	in[0] = byte(len(tagCache))
	copy(in[1:], tagCache)
	for j := uint64(0); j < n; j++ {
		binary.BigEndian.PutUint64(in[1+len(tagCache):], j)
		size := 1 + len(tagCache) + 8
		if j > 0 {
			copy(in[size:], cache[(j-1)*ItemBytes:(j-1)*ItemBytes+32])
			size += 32
		}
		h.Reset()
		h.Write(in[:size])
		h.XOF().Read(cache[j*ItemBytes : (j+1)*ItemBytes])
	}
	return cache
}

// fnv is the FNV-1 step as Ethash and FishHash use it.
func fnv(a, b uint32) uint32 { return (a * 0x01000193) ^ b }

// ItemsV1 derives version 1 dataset items by the parent walk over an epoch's
// cache (SPEC-R0A-v0.md 5.3): mix starts as cache(i mod N_CACHE) with the
// index folded into its first two words, takes PARENTS parents chosen by the
// running mix, and the item is the keyed XOF over tag(T_ITEM) || u64be(i) ||
// mix. One ItemsV1 holds one keyed hasher; it is not safe for concurrent use.
type ItemsV1 struct {
	h       *blake3.Hasher
	cache   []byte
	n       uint64
	parents uint32
	in      [1 + len(tagItem) + 8 + ItemBytes]byte
	// fn is the Item method bound once (a method value bound per proof is one
	// allocation each; the pooled deriver hands out the same one)
	fn ItemFunc
}

// NewItemsV1 prepares item derivation over a built cache.
func NewItemsV1(epochSeed [32]byte, cache []byte, parents uint32) *ItemsV1 {
	it := &ItemsV1{h: blake3.New(32, epochSeed[:]), cache: cache, n: uint64(len(cache) / ItemBytes), parents: parents}
	it.in[0] = byte(len(tagItem))
	copy(it.in[1:], tagItem)
	it.fn = it.Item
	return it
}

// Fn is the deriver's Item as an ItemFunc, bound once.
func (it *ItemsV1) Fn() ItemFunc { return it.fn }

// Item derives item i into out.
func (it *ItemsV1) Item(i uint64, out *[ItemBytes]byte) {
	var mix [32]uint32
	base := it.cache[(i%it.n)*ItemBytes:]
	for k := range mix {
		mix[k] = binary.LittleEndian.Uint32(base[4*k:])
	}
	lo, hi := uint32(i), uint32(i>>32)
	mix[0] ^= lo
	mix[1] ^= hi
	for p := uint32(0); p < it.parents; p++ {
		parent := uint64(fnv(lo^p, mix[p%32])) % it.n
		pb := it.cache[parent*ItemBytes:]
		for k := range mix {
			mix[k] = fnv(mix[k], binary.LittleEndian.Uint32(pb[4*k:]))
		}
	}
	binary.BigEndian.PutUint64(it.in[1+len(tagItem):], i)
	for k := range mix {
		binary.LittleEndian.PutUint32(it.in[1+len(tagItem)+8+4*k:], mix[k])
	}
	it.h.Reset()
	it.h.Write(it.in[:])
	it.h.XOF().Read(out[:])
}

// ItemsW derives version 4 dataset items of width W over an epoch's cache
// (SPEC-R0A-v0.md sections 0.3 and 0.4): for W >= 128 the walk runs over
// runs of W / 128 consecutive cache items (mix of W / 4 words, parents of
// the same width) and the item is the keyed XOF of W bytes; for W = 64 the
// walk is version 1's over one cache item and only the output is cut to 64
// bytes. At W = 128 it is ItemsV1 word for word. Not safe for concurrent use.
type ItemsW struct {
	h        *blake3.Hasher
	cache    []byte
	n        uint64
	parents  uint32
	width    uint32
	runItems uint32
	words    uint32
	mix      []uint32
	par      []uint32
	in       []byte
}

// NewItemsW prepares width-W item derivation over a built cache.
func NewItemsW(epochSeed [32]byte, cache []byte, parents uint32, width uint32) *ItemsW {
	run := uint32(1)
	if width >= ItemBytes {
		run = width / ItemBytes
	}
	words := 32 * run
	it := &ItemsW{h: blake3.New(32, epochSeed[:]), cache: cache, n: uint64(len(cache) / ItemBytes), parents: parents, width: width, runItems: run, words: words,
		mix: make([]uint32, words), par: make([]uint32, words), in: make([]byte, 1+len(tagItem)+8+4*int(words))}
	it.in[0] = byte(len(tagItem))
	copy(it.in[1:], tagItem)
	return it
}

func (it *ItemsW) loadRun(first uint64, dst []uint32) {
	for r := uint64(0); r < uint64(it.runItems); r++ {
		src := it.cache[((first+r)%it.n)*ItemBytes:]
		for k := 0; k < 32; k++ {
			dst[r*32+uint64(k)] = binary.LittleEndian.Uint32(src[4*k:])
		}
	}
}

// Item derives item i into out, len(out) = W.
func (it *ItemsW) Item(i uint64, out []byte) {
	it.loadRun(i%it.n, it.mix)
	lo, hi := uint32(i), uint32(i>>32)
	it.mix[0] ^= lo
	it.mix[1] ^= hi
	for p := uint32(0); p < it.parents; p++ {
		parent := uint64(fnv(lo^p, it.mix[p%it.words])) % it.n
		it.loadRun(parent, it.par)
		for k := range it.mix {
			it.mix[k] = fnv(it.mix[k], it.par[k])
		}
	}
	binary.BigEndian.PutUint64(it.in[1+len(tagItem):], i)
	for k, m := range it.mix {
		binary.LittleEndian.PutUint32(it.in[1+len(tagItem)+8+4*k:], m)
	}
	it.h.Reset()
	it.h.Write(it.in)
	it.h.XOF().Read(out[:it.width])
}
