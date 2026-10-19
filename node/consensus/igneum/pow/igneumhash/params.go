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

// Package igneumhash is the node's adapter for IgneumHash-R0A: the consensus
// envelope of docs/igneumhash/ENVELOPE-R0A-v0.md (domain tags, work seed,
// epoch and dataset-item derivation, final digest, target, verifier limits)
// around a resource-work function reached through the Work interface. The
// work function is the hash team's specification and protocol B's independent
// implementation; this package carries only a harness stand-in for it until
// that lands, and registers the algorithm as development-only (PHASE-ZERO.md:
// no hash is committed until the founder ratifies).
package igneumhash

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"

	"lukechampine.com/blake3"
)

// Name is the registry key.
const Name = "igneumhash"

// The versions this adapter knows: v0 (per-index items, the evidence row) and
// v1 (the cache and parent walk, SPEC-R0A-v0.md revision 0.4, the candidate).
const (
	Version0 uint32 = 0
	Version1 uint32 = 1
	// Version4 is Strata, the whole-item width relation (SPEC-R0A-v0.md
	// section 0.4): version 1's cache and walk at an item width of 64, 128,
	// 256 or 512 bytes and the round rewritten to absorb every word. Research
	// only; versions 2 and 3 are not known to this adapter.
	Version4 uint32 = 4
	// Version6 is the keyed reduction (SPEC-R0A-v0.md section 0.6, the
	// decisive round's primary under the outside review's decision B): version 4's
	// document and relation with the tree's leaves keyed by the absorb
	// register's prior value and the root finalised. Version 5 is not known
	// to this adapter.
	Version6 uint32 = 6
)

// Params is one parameter set (ENVELOPE-R0A-v0.md section 1). Every field is
// an ablation variable of the research candidate, not a consensus constant.
type Params struct {
	Version      uint32
	DatasetBytes uint64
	ScratchBytes uint32
	Rounds       uint32
	Lanes        uint32
	EpochLength  uint64
	// ItemChain is the number of keyed-hash invocations per dataset item under
	// version 0 (SPEC-R0A-v0.md 5.3); version 1 fixes it at 1.
	ItemChain uint32
	// CacheBytes and Parents are version 1's: the per-epoch cache the items
	// are walked from, and the number of cache parents per item.
	CacheBytes uint64
	Parents    uint32
	// Width is version 4's ITEM_BYTES: the dataset item's width in bytes (64,
	// 128, 256, 512, or 1024 since the Riptide revision). Zero under versions 0 and 1, whose items are
	// ItemBytes wide.
	Width uint32
}

// ItemWidth is the dataset item's width in bytes for this set: Width under
// version 4, ItemBytes otherwise.
func (p Params) ItemWidth() uint32 {
	if p.Wide() {
		return p.Width
	}
	return ItemBytes
}

// Wide reports a whole-item version (4 or 6): item_bytes is a parameter
// and the round is MixWide's.
func (p Params) Wide() bool { return p.Version == Version4 || p.Version == Version6 }

// ParamsSize is the canonical encoding's length.
const (
	ParamsSizeV0 = 4 + 8 + 4 + 4 + 4 + 8 + 4 // 36 bytes, the frozen prefix
	ParamsSizeV1 = ParamsSizeV0 + 8 + 4      // 48 bytes: the prefix, CACHE_BYTES, PARENTS
	ParamsSizeV4 = ParamsSizeV1 + 4          // 52 bytes: version 1's, then ITEM_BYTES u32be at byte 48
	ItemBytes    = 128                       // one dataset or cache item, 32 words
)

var (
	errParamsDocument = errors.New("igneumhash: algorithm parameters document")
	errParamsVersion  = errors.New("igneumhash: unknown algorithm version")
	errParamsRange    = errors.New("igneumhash: parameter out of range")
)

// Bounds on the parameter set: what the verifier contract was measured for.
// A chain configuration outside them is refused at construction, before any
// block is looked at.
// The spec's accepted ranges (SPEC-R0A-v0.md section 0): dataset a power of
// two from 16 MiB to 4 GiB, scratchpad a power of two from 4 KiB to 1 MiB,
// rounds 1 to 4096, lanes 8, epoch 1 to 2^32 blocks, item chain 1 to 256,
// version 0.
const (
	minDatasetBytes = 16 << 20
	maxDatasetBytes = 4 << 30
	minScratchBytes = 4 << 10
	maxScratchBytes = 1 << 20
	maxRounds       = 4096
	fixedLanes      = 8
	maxEpochLength  = 1 << 32
	maxItemChain    = 256
	minCacheBytes   = 1 << 20
	maxCacheBytes   = 256 << 20
	maxParents      = 4096
)

// DefaultParams is the version 1 baseline cell (SPEC-R0A-v0.md revision 0.4,
// section 0.1: the document {}): 1 GiB dataset, no scratchpad, 64 rounds, 8
// lanes, 4096-block epochs, item chain 1, a 32 MiB cache, 256 parents.
func DefaultParams() Params {
	return Params{Version: Version1, DatasetBytes: 1 << 30, ScratchBytes: 0, Rounds: 64, Lanes: 8, EpochLength: 4096, ItemChain: 1, CacheBytes: 32 << 20, Parents: 256}
}

// DefaultParamsV0 is the version 0 baseline cell (the document {"version": 0}):
// 256 MiB dataset, 16 KiB scratchpad, 64 rounds, 8 lanes, 4096-block epochs,
// item chain 1. The evidence row; its vectors are immutable.
func DefaultParamsV0() Params {
	return Params{Version: Version0, DatasetBytes: 256 << 20, ScratchBytes: 16 << 10, Rounds: 64, Lanes: 8, EpochLength: 4096, ItemChain: 1}
}

// Size is the canonical encoding's length for this version.
func (p Params) Size() int {
	switch p.Version {
	case Version4, Version6:
		return ParamsSizeV4
	case Version1:
		return ParamsSizeV1
	}
	return ParamsSizeV0
}

// Encode is the canonical encoding: the frozen 36-byte prefix, and under
// version 1 CACHE_BYTES and PARENTS after it (48 bytes).
func (p Params) Encode() []byte {
	b := make([]byte, p.Size())
	binary.BigEndian.PutUint32(b[0:], p.Version)
	binary.BigEndian.PutUint64(b[4:], p.DatasetBytes)
	binary.BigEndian.PutUint32(b[12:], p.ScratchBytes)
	binary.BigEndian.PutUint32(b[16:], p.Rounds)
	binary.BigEndian.PutUint32(b[20:], p.Lanes)
	binary.BigEndian.PutUint64(b[24:], p.EpochLength)
	binary.BigEndian.PutUint32(b[32:], p.ItemChain)
	if p.Version == Version1 || p.Wide() {
		binary.BigEndian.PutUint64(b[36:], p.CacheBytes)
		binary.BigEndian.PutUint32(b[44:], p.Parents)
	}
	if p.Wide() {
		binary.BigEndian.PutUint32(b[48:], p.Width)
	}
	return b
}

// CacheItems is the per-epoch cache's item count (version 1).
func (p Params) CacheItems() uint64 { return p.CacheBytes / ItemBytes }

// Hash is P, the parameter-set binding in every domain input.
func (p Params) Hash() [32]byte { return blake3.Sum256(p.Encode()) }

// Items is the dataset's item count.
func (p Params) Items() uint64 { return p.DatasetBytes / uint64(p.ItemWidth()) }

// Validate refuses a parameter set outside the spec's accepted ranges for
// its version (SPEC-R0A-v0.md section 0.1).
func (p Params) Validate() error {
	pow2 := func(x uint64) bool { return x != 0 && x&(x-1) == 0 }
	if p.Version != Version0 && p.Version != Version1 && !p.Wide() {
		return fmt.Errorf("%w: %d", errParamsVersion, p.Version)
	}
	switch {
	case p.DatasetBytes < minDatasetBytes || p.DatasetBytes > maxDatasetBytes || !pow2(p.DatasetBytes):
		return fmt.Errorf("%w: dataset %d bytes", errParamsRange, p.DatasetBytes)
	case p.Rounds == 0 || p.Rounds > maxRounds:
		return fmt.Errorf("%w: %d rounds", errParamsRange, p.Rounds)
	case p.Lanes != fixedLanes:
		return fmt.Errorf("%w: %d lanes (8 is fixed)", errParamsRange, p.Lanes)
	case p.EpochLength == 0 || p.EpochLength > maxEpochLength:
		return fmt.Errorf("%w: epoch length %d", errParamsRange, p.EpochLength)
	}
	scratchOK := uint64(p.ScratchBytes) >= minScratchBytes && uint64(p.ScratchBytes) <= maxScratchBytes && pow2(uint64(p.ScratchBytes))
	switch p.Version {
	case Version0:
		switch {
		case !scratchOK:
			return fmt.Errorf("%w: scratchpad %d bytes", errParamsRange, p.ScratchBytes)
		case p.ItemChain == 0 || p.ItemChain > maxItemChain:
			return fmt.Errorf("%w: item chain %d", errParamsRange, p.ItemChain)
		case p.CacheBytes != 0 || p.Parents != 0:
			return fmt.Errorf("%w: cache and parents are version 1 members", errParamsRange)
		}
	case Version1, Version4, Version6:
		switch {
		case p.Version == Version1 && p.Width != 0:
			return fmt.Errorf("%w: item_bytes is a member of versions 4 and 6", errParamsRange)
		case p.Wide() && p.Width != 64 && p.Width != 128 && p.Width != 256 && p.Width != 512 && p.Width != 1024:
			return fmt.Errorf("%w: item_bytes %d (64, 128, 256, 512 or 1024)", errParamsRange, p.Width)
		case p.Wide() && p.ScratchBytes != 0:
			return fmt.Errorf("%w: versions 4 and 6 have no scratchpad", errParamsRange)
		case p.ScratchBytes != 0 && !scratchOK:
			return fmt.Errorf("%w: scratchpad %d bytes", errParamsRange, p.ScratchBytes)
		case p.ItemChain != 1:
			return fmt.Errorf("%w: item chain %d (version 1 fixes 1)", errParamsRange, p.ItemChain)
		case p.CacheBytes < minCacheBytes || p.CacheBytes > maxCacheBytes || !pow2(p.CacheBytes):
			return fmt.Errorf("%w: cache %d bytes", errParamsRange, p.CacheBytes)
		case p.Parents == 0 || p.Parents > maxParents:
			return fmt.Errorf("%w: %d parents", errParamsRange, p.Parents)
		}
	}
	return nil
}

// Document is the params document of SPEC-R0A-v0.md section 0.1 (the
// reference ABI's params_json): integer members, "version" mandatory
// (revision 0.5), any other absent member taking its version's default
// (dataset_mib and scratch_kib: 1024 and 0 under version 1, 256 and 16 under
// version 0), an unknown member or an out-of-range value rejected, cache_mib
// and parents version 1 only. It is what the chain configuration's algorithmParams holds
// (the field is raw JSON), what igneum_getWork serves the miner, and what the
// worker builds its dataset from, one shape throughout.
type Document struct {
	Version     uint32 `json:"version"`
	DatasetMiB  uint64 `json:"dataset_mib"`
	ScratchKiB  uint32 `json:"scratch_kib"`
	Rounds      uint32 `json:"rounds"`
	Lanes       uint32 `json:"lanes"`
	EpochBlocks uint64 `json:"epoch_blocks"`
	ItemChain   uint32 `json:"item_chain"`
	CacheMiB    uint64 `json:"cache_mib,omitempty"`
	Parents     uint32 `json:"parents,omitempty"`
	ItemBytes   uint32 `json:"item_bytes,omitempty"`
	ChainID     uint64 `json:"chain_id"`
}

// DefaultChainID is the document's default chain: the development chain.
const DefaultChainID = 7310

// Document returns the params document for a chain and this set.
func (p Params) Document(chainID uint64) Document {
	return Document{Version: p.Version, DatasetMiB: p.DatasetBytes >> 20, ScratchKiB: p.ScratchBytes >> 10, Rounds: p.Rounds, Lanes: p.Lanes, EpochBlocks: p.EpochLength, ItemChain: p.ItemChain, CacheMiB: p.CacheBytes >> 20, Parents: p.Parents, ItemBytes: p.Width, ChainID: chainID}
}

// EncodeChainParams is the algorithmParams document for a chain and a set.
func EncodeChainParams(chainID uint64, p Params) []byte {
	b, err := json.Marshal(p.Document(chainID))
	if err != nil {
		panic(err)
	}
	return b
}

// document is the parse form: pointers tell an absent member from a zero.
type document struct {
	Version     *uint32 `json:"version"`
	DatasetMiB  *uint64 `json:"dataset_mib"`
	ScratchKiB  *uint32 `json:"scratch_kib"`
	Rounds      *uint32 `json:"rounds"`
	Lanes       *uint32 `json:"lanes"`
	EpochBlocks *uint64 `json:"epoch_blocks"`
	ItemChain   *uint32 `json:"item_chain"`
	CacheMiB    *uint64 `json:"cache_mib"`
	Parents     *uint32 `json:"parents"`
	ItemBytes   *uint32 `json:"item_bytes"`
	ChainID     *uint64 `json:"chain_id"`
}

// DecodeChainParams parses a params document (SPEC-R0A-v0.md section 0.1,
// revision 0.5): "version" is mandatory, so an empty document, null or {} is
// refused rather than defaulted to the newest version (security's F-14: a
// document that means a different algorithm after a revision is no
// document); every other absent member takes its version's default; an
// unknown member is rejected; cache_mib and parents are rejected under
// version 0; the result is validated.
func DecodeChainParams(b []byte) (chainID uint64, p Params, err error) {
	var d document
	trimmed := bytes.TrimSpace(b)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return 0, p, fmt.Errorf("%w: empty; \"version\" is mandatory", errParamsDocument)
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return 0, p, fmt.Errorf("%w: %v", errParamsDocument, err)
	}
	if dec.More() {
		return 0, p, fmt.Errorf("%w: trailing data", errParamsDocument)
	}
	if d.Version == nil {
		return 0, p, fmt.Errorf("%w: \"version\" is mandatory (0 or 1)", errParamsDocument)
	}
	version := *d.Version
	def := DefaultParams()
	if version == Version0 {
		def = DefaultParamsV0()
	}
	if version == Version0 && (d.CacheMiB != nil || d.Parents != nil) {
		return 0, p, fmt.Errorf("%w: cache_mib and parents are version 1 members", errParamsDocument)
	}
	wide := version == Version4 || version == Version6
	if !wide && d.ItemBytes != nil {
		return 0, p, fmt.Errorf("%w: item_bytes is a member of versions 4 and 6", errParamsDocument)
	}
	if wide && d.ItemBytes == nil {
		return 0, p, fmt.Errorf("%w: item_bytes is mandatory under versions 4 and 6", errParamsDocument)
	}
	u64 := func(v *uint64, def uint64) uint64 {
		if v != nil {
			return *v
		}
		return def
	}
	u32 := func(v *uint32, def uint32) uint32 {
		if v != nil {
			return *v
		}
		return def
	}
	datasetMiB, scratchKiB, cacheMiB := u64(d.DatasetMiB, def.DatasetBytes>>20), u32(d.ScratchKiB, uint32(def.ScratchBytes>>10)), u64(d.CacheMiB, def.CacheBytes>>20)
	if datasetMiB > maxDatasetBytes>>20 || scratchKiB > maxScratchBytes>>10 || cacheMiB > maxCacheBytes>>20 {
		return 0, p, fmt.Errorf("%w: dataset_mib %d scratch_kib %d cache_mib %d", errParamsRange, datasetMiB, scratchKiB, cacheMiB)
	}
	p = Params{
		Version:      version,
		DatasetBytes: datasetMiB << 20,
		ScratchBytes: scratchKiB << 10,
		Rounds:       u32(d.Rounds, def.Rounds),
		Lanes:        u32(d.Lanes, def.Lanes),
		EpochLength:  u64(d.EpochBlocks, def.EpochLength),
		ItemChain:    u32(d.ItemChain, def.ItemChain),
		CacheBytes:   cacheMiB << 20,
		Parents:      u32(d.Parents, def.Parents),
		Width:        u32(d.ItemBytes, 0),
	}
	if err := p.Validate(); err != nil {
		return 0, p, err
	}
	return u64(d.ChainID, DefaultChainID), p, nil
}
