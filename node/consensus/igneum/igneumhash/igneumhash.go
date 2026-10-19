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

// Package igneumhash is the node's independent implementation of
// IgneumHash-R0A, versions 0 and 1, written from the specification text
// only (docs/igneumhash/SPEC-R0A-v0.md revision 0.4, which reproduces
// the protocol team's envelope ENVELOPE-R0A-v0.md) and never from the C reference's
// code (PROGRAMME.md: the second of the two independent implementations
// hard gate G0 needs). Every mismatch against the reference's vectors is
// resolved as a correction to the text, never as a code copy. Section
// numbers in comments are the specification's.
//
// Research only. Every parameter is an ablation variable (Params, section
// 0), not a consensus constant. Version 0 is the evidence row (items one
// keyed hash from E); version 1 is the plan's "revise" outcome (items by a
// parent walk over a sequentially built per-epoch cache, section 5.3, the
// scratchpad optional).
package igneumhash

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"math/bits"
)

// Name is the node's registry key (section 1).
const Name = "igneumhash"

// The versions this package implements (section 0).
const (
	Version0 uint32 = 0
	Version1 uint32 = 1
	// Version4 is Strata, the whole-item width relation (section 0.4; the
	// founder's Strata brief step 1): version 3's 52-byte document under
	// "version": 4 with the round rewritten to the whole-item rule. Version
	// 3 is superseded as an instrument and is not implemented here.
	Version4 uint32 = 4
	// Version6 is the keyed reduction (section 0.6, Breakwater R3 on the red
	// team's S9; PROPOSED): version 4's document and relation with the leaves
	// keyed by the register each word is absorbed into, as it stands before the
	// absorb, and the root finalised. The consensus text keeps version 4.
	Version6 uint32 = 6
)

// Constants fixed by V (section 0).
const (
	ItemBytes = 128 // one dataset or cache item, 32 words
	ItemWords = ItemBytes / 4
	LaneWords = 16         // registers per lane
	LineWords = 4          // one scratch access is one 16-byte line
	Mul       = 0x9E3779B1 // the multiplier
	FNVPrime  = 0x01000193 // the FNV-1 step (version 1, section 5.3)
)

// perm is the register permutation perm(k) = (5k + 1) mod 16 (section 0).
func perm(k int) int { return (5*k + 1) % LaneWords }

// Domain tags (section 1), written as tag(T) = u8(len(T)) || T.
const (
	TagSeed    = "IgneumHash/R0A/seed"
	TagEpoch   = "IgneumHash/R0A/epoch"
	TagItem    = "IgneumHash/R0A/item"
	TagDataset = "IgneumHash/R0A/dataset"
	TagLanes   = "IgneumHash/R0A/lanes"
	TagMix     = "IgneumHash/R0A/mix"
	TagFinal   = "IgneumHash/R0A/final"
	TagCache   = "IgneumHash/R0A/cache" // version 1, section 5.3
)

func tag(t string) []byte {
	return append([]byte{byte(len(t))}, t...)
}

// Params are the parameters of section 0.1 plus the chain id. ParamsBytes
// is the canonical encoding (36 bytes under version 0, 48 under version 1)
// and ParamsHash P = BLAKE3(params).
type Params struct {
	ChainID      uint64 `json:"chainId"`
	Version      uint32 `json:"version"`
	DatasetBytes uint64 `json:"datasetBytes"` // a power of two
	ScratchBytes uint32 `json:"scratchBytes"` // 0 (v1 only) or a power of two
	Rounds       uint32 `json:"rounds"`       // also ACCESSES
	Lanes        uint32 `json:"lanes"`        // fixed at 8
	EpochLength  uint64 `json:"epochLength"`
	ItemChain    uint32 `json:"itemChain"`  // v0: keyed hashes per item; v1: 1
	CacheBytes   uint64 `json:"cacheBytes"` // v1 and v4: the per-epoch cache
	Parents      uint32 `json:"parents"`    // v1 and v4: cache reads per item
	ItemBytes    uint32 `json:"itemBytes"`  // v4 only: W, the dataset item width (64, 128, 256, 512); 128 otherwise
	// version 8 (section 0.8 and 0.9; zero otherwise)
	Blocks      uint32 `json:"blocks,omitempty"`      // BLOCKS: 1, 2, 4, 8, 16, 32 or 64
	Registers   uint32 `json:"registers,omitempty"`   // REGISTERS: 8, 16, 32 or 64 (32 and 64 widen the state; not reached by this port)
	Features    uint32 `json:"features,omitempty"`    // FEATURES: the option bits of v8.go
	KeyedRounds uint32 `json:"keyedRounds,omitempty"` // KEYED_ROUNDS S under bit 10: 1..ROUNDS
}

// Wide reports versions 4, 6 and 8 (the whole-item width relation, its keyed form and the blended relation).
func (p Params) Wide() bool { return p.Version == Version4 || p.Version == Version6 || p.Version == Version8 }

// Width is W, the dataset item width in bytes: ITEM_BYTES under versions 4 and 6, 128 otherwise.
func (p Params) Width() uint32 {
	if p.Wide() {
		return p.ItemBytes
	}
	return ItemBytes
}

// Variant is the specification's variant tag (section 9).
func (p Params) Variant() string {
	if p.Version == Version8 {
		s := fmt.Sprintf("v8-d%d-s%d-r%d-k%d-p%d-w%d-b%d-x%d", p.DatasetBytes>>20, p.ScratchBytes>>10, p.Rounds, p.CacheBytes>>20, p.Parents, p.ItemBytes, p.Blocks, p.Registers)
		if p.Features != 0 {
			s += fmt.Sprintf("-f%d", p.Features)
		}
		if p.Keyed() {
			s += fmt.Sprintf("-k%d", p.KeyedRounds)
		}
		return s
	}
	if p.Wide() {
		return fmt.Sprintf("v%d-d%d-s%d-r%d-k%d-p%d-w%d", p.Version, p.DatasetBytes>>20, p.ScratchBytes>>10, p.Rounds, p.CacheBytes>>20, p.Parents, p.ItemBytes)
	}
	if p.Version == Version1 {
		return fmt.Sprintf("v1-d%d-s%d-r%d-k%d-p%d", p.DatasetBytes>>20, p.ScratchBytes>>10, p.Rounds, p.CacheBytes>>20, p.Parents)
	}
	return fmt.Sprintf("d%d-s%d-r%d-c%d", p.DatasetBytes>>20, p.ScratchBytes>>10, p.Rounds, p.ItemChain)
}

// Defaults are the version's defaults on the development chain (section
// 0.1): version 1 is the baseline of {}; version 0 is {"version": 0}.
func Defaults(version uint32) Params {
	switch version {
	case Version0:
		return Params{ChainID: 7310, Version: Version0, DatasetBytes: 1 << 28, ScratchBytes: 16384, Rounds: 64, Lanes: 8, EpochLength: 4096, ItemChain: 1}
	default:
		return Params{ChainID: 7310, Version: Version1, DatasetBytes: 1 << 30, ScratchBytes: 0, Rounds: 64, Lanes: 8, EpochLength: 4096, ItemChain: 1, CacheBytes: 1 << 25, Parents: 256}
	}
}

// Baseline is the version 1 baseline; BaselineV0 the version 0 one.
var (
	Baseline   = Defaults(Version1)
	BaselineV0 = Defaults(Version0)
)

// Ablation names the research cells: the version 0 matrix, the
// pre-registered version 1 matrix of section 9, and tiny cells for unit
// tests and the harness's smoke runs.
var Ablation = func() map[string]Params {
	m := map[string]Params{}
	for _, d := range []uint64{1 << 28, 1 << 29, 1 << 30} {
		for _, s := range []uint32{16384, 65536} {
			for _, r := range []uint32{32, 64} {
				for _, c := range []uint32{1, 4, 16} {
					p := BaselineV0
					p.DatasetBytes, p.ScratchBytes, p.Rounds, p.ItemChain = d, s, r, c
					m[p.Variant()] = p
				}
			}
		}
	}
	add := func(f func(p *Params)) {
		p := Baseline
		f(&p)
		m[p.Variant()] = p
	}
	add(func(p *Params) {})
	add(func(p *Params) { p.DatasetBytes = 2 << 30 })
	add(func(p *Params) { p.DatasetBytes = 4 << 30 })
	add(func(p *Params) { p.ScratchBytes = 4096 })
	add(func(p *Params) { p.Rounds = 32 })
	add(func(p *Params) { p.CacheBytes = 1 << 26 })
	add(func(p *Params) { p.Parents = 512 })
	add(func(p *Params) { p.DatasetBytes = 1 << 28 })
	add(func(p *Params) { p.DatasetBytes = 1 << 29 })
	// version 6, the keyed reduction (section 0.6): the first two documents
	for _, w := range []uint32{128, 256} {
		p := Params{ChainID: 7310, Version: Version6, DatasetBytes: 1 << 31, ScratchBytes: 0, Rounds: 64, Lanes: 8, EpochLength: 4096, ItemChain: 1, CacheBytes: 1 << 25, Parents: 256, ItemBytes: w}
		m[p.Variant()] = p
	}
	// version 4, Strata: the four frozen width documents (section 0.4, STEP1-NOTES.md section 3)
	for _, w := range []uint32{64, 128, 256, 512, 1024} {
		p := Params{ChainID: 7310, Version: Version4, DatasetBytes: 1 << 31, ScratchBytes: 0, Rounds: 64, Lanes: 8, EpochLength: 4096, ItemChain: 1, CacheBytes: 1 << 25, Parents: 256, ItemBytes: w}
		m[p.Variant()] = p
		// the Riptide documents: R(W) = 64 x 128 / W rounds (strata/RIPTIDE.md section 1)
		if w >= 256 {
			r := p
			r.Rounds = 64 * 128 / w
			m[r.Variant()] = r
			if w == 512 {
				r4 := r
				r4.DatasetBytes = 1 << 32
				m[r4.Variant()] = r4
			}
		}
		q := p
		q.DatasetBytes, q.CacheBytes, q.Rounds, q.EpochLength = 1<<20, 1<<20, 8, 100
		m["test-tiny-v4-w"+itoa(w)] = q
	}
	m["test-tiny"] = Params{ChainID: 7310, Version: Version0, DatasetBytes: 1 << 20, ScratchBytes: 4096, Rounds: 8, Lanes: 8, EpochLength: 100, ItemChain: 1}
	m["test-tiny-v1"] = Params{ChainID: 7310, Version: Version1, DatasetBytes: 1 << 20, ScratchBytes: 0, Rounds: 8, Lanes: 8, EpochLength: 100, ItemChain: 1, CacheBytes: 1 << 20, Parents: 16}
	m["test-tiny-v1-s4"] = Params{ChainID: 7310, Version: Version1, DatasetBytes: 1 << 20, ScratchBytes: 4096, Rounds: 8, Lanes: 8, EpochLength: 100, ItemChain: 1, CacheBytes: 1 << 20, Parents: 16}
	return m
}()

// Verifier allocation caps (section 5.1, chapter 27) and the reference's
// accepted ranges (section 0.1).
const (
	MaxDatasetBytes = 4 << 30
	MinScratchBytes = 1 << 10 // revision 0.12: scratch_kib a power of two from 1 to 1024 (1 and 2 for the state-expansion control; F-2)
	MaxScratchBytes = 1 << 20
	MaxRounds       = 4096
	MaxItemChain    = 256
	MinCacheBytes   = 1 << 20
	MaxCacheBytes   = 256 << 20
	MaxParents      = 4096
)

var (
	ErrParams     = errors.New("igneumhash: parameters rejected")
	ErrDifficulty = errors.New("igneumhash: difficulty rejected")
	ErrEpoch      = errors.New("igneumhash: item source is for another epoch or parameter set")
	ErrMix        = errors.New("igneumhash: mix digest mismatch")
	ErrTarget     = errors.New("igneumhash: digest above target")
	// ErrKeyed is section 0.9's refusal: a keyed document has no keyless work,
	// and the keyed entry needs the secret or the shares (the reference's IGNPOW_E_ARG).
	ErrKeyed = errors.New("igneumhash: a keyed document needs the bond's secret or the shares")
	// ErrNotReached names a document this port accepts at the rule and cannot
	// compute: version 8's wider state (REGISTERS 32 or 64), which the launch
	// document does not use.
	ErrNotReached = errors.New("igneumhash: not reached by this port")
)

func isPow2(x uint64) bool { return x != 0 && x&(x-1) == 0 }

func itoa(w uint32) string { return fmt.Sprint(w) }

// Validate rejects a parameter set outside section 0.1's ranges for its
// version. The tiny test cells sit below the dataset floor and are allowed
// by Ablation name only.
func (p Params) Validate() error {
	switch {
	case p.Version != Version0 && p.Version != Version1 && p.Version != Version4 && p.Version != Version6 && p.Version != Version8:
		return fmt.Errorf("%w: version %d", ErrParams, p.Version)
	case p.EpochLength == 0 || p.EpochLength > 1<<32:
		return fmt.Errorf("%w: epochLength %d", ErrParams, p.EpochLength)
	case p.Version == Version8 && (p.DatasetBytes%(16<<20) != 0 || p.DatasetBytes < 16<<20 || p.DatasetBytes > 16384<<20):
		// version 8 (section 0.8): any multiple of 16 MiB from 16 MiB to 16 GiB, the address map chosen by the size
		return fmt.Errorf("%w: datasetBytes %d (version 8: a multiple of 16 MiB, 16 MiB to 16 GiB)", ErrParams, p.DatasetBytes)
	case p.Version != Version8 && (!isPow2(p.DatasetBytes) || p.DatasetBytes > MaxDatasetBytes || p.DatasetBytes < ItemBytes*8):
		return fmt.Errorf("%w: datasetBytes %d", ErrParams, p.DatasetBytes)
	case p.Lanes != 8:
		return fmt.Errorf("%w: lanes %d (fixed at 8)", ErrParams, p.Lanes)
	case p.Rounds == 0 || p.Rounds > MaxRounds:
		return fmt.Errorf("%w: rounds %d", ErrParams, p.Rounds)
	}
	scratchOK := isPow2(uint64(p.ScratchBytes)) && p.ScratchBytes >= MinScratchBytes && p.ScratchBytes <= MaxScratchBytes
	switch p.Version {
	case Version0:
		switch {
		case !scratchOK:
			return fmt.Errorf("%w: scratchBytes %d", ErrParams, p.ScratchBytes)
		case p.ItemChain == 0 || p.ItemChain > MaxItemChain:
			return fmt.Errorf("%w: itemChain %d", ErrParams, p.ItemChain)
		case p.CacheBytes != 0 || p.Parents != 0:
			return fmt.Errorf("%w: cache and parents are version 1 fields", ErrParams)
		}
	case Version1, Version4, Version6, Version8:
		switch {
		case p.Version == Version1 && p.ItemBytes != 0:
			return fmt.Errorf("%w: item_bytes is a version 4 field", ErrParams)
		case p.Version == Version8 && p.ItemBytes == 32:
			// the narrow control of the reference's table (section 0.4): admitted at the rule, computed as the 64-byte cut
		case p.Wide() && p.ItemBytes != 64 && p.ItemBytes != 128 && p.ItemBytes != 256 && p.ItemBytes != 512 && p.ItemBytes != 1024:
			// 1024 admitted at section 0.4's revision for Riptide (strata/RIPTIDE.md): two words per register, 256 leaves
			return fmt.Errorf("%w: item_bytes %d (64, 128, 256, 512 or 1024)", ErrParams, p.ItemBytes)
		case p.Wide() && p.DatasetBytes < uint64(p.ItemBytes)*8:
			return fmt.Errorf("%w: datasetBytes %d below eight items of %d", ErrParams, p.DatasetBytes, p.ItemBytes)
		case p.ScratchBytes != 0 && !scratchOK:
			return fmt.Errorf("%w: scratchBytes %d", ErrParams, p.ScratchBytes)
		case p.ItemChain != 1:
			return fmt.Errorf("%w: itemChain %d (version 1 keeps 1)", ErrParams, p.ItemChain)
		case !isPow2(p.CacheBytes) || p.CacheBytes < MinCacheBytes || p.CacheBytes > MaxCacheBytes:
			return fmt.Errorf("%w: cacheBytes %d", ErrParams, p.CacheBytes)
		case p.Parents == 0 || p.Parents > MaxParents:
			return fmt.Errorf("%w: parents %d", ErrParams, p.Parents)
		}
	}
	if p.Version != Version8 {
		if p.Blocks != 0 || p.Registers != 0 || p.Features != 0 || p.KeyedRounds != 0 {
			return fmt.Errorf("%w: blocks, registers, features and keyed_rounds are version 8 fields", ErrParams)
		}
		return nil
	}
	return p.validateV8()
}

// validateV8 is the rule of sections 0.8 and 0.9 on the blended relation's
// own fields, in the reference's order.
func (p Params) validateV8() error {
	f := p.Features
	switch {
	case f > FeatMax || f&FeatReserved6 != 0:
		return fmt.Errorf("%w: features %d (bit 6 reserved; over 8191)", ErrParams, f)
	case f&(FeatWalkOnly|FeatProgrammeOnly) == FeatWalkOnly|FeatProgrammeOnly:
		return fmt.Errorf("%w: features: bits 8 and 9 together", ErrParams)
	case f&FeatFormA != 0 && f&FeatFormB != 0:
		return fmt.Errorf("%w: features: the two 2c forms are exclusive", ErrParams)
	case f&FeatFormA != 0 && f&(FeatTwoChains|FeatFourChains) != 0:
		return fmt.Errorf("%w: features: the narrow window needs the full 16 registers (no chains)", ErrParams)
	case f&FeatFormA != 0 && p.Registers != 16:
		return fmt.Errorf("%w: features: form (a) needs registers 16", ErrParams)
	case f&FeatKeyedRounds != 0 && (p.KeyedRounds < 1 || p.KeyedRounds > p.Rounds):
		return fmt.Errorf("%w: keyed_rounds %d (1 to ROUNDS under bit 10)", ErrParams, p.KeyedRounds)
	case f&FeatKeyedRounds == 0 && p.KeyedRounds != 0:
		return fmt.Errorf("%w: keyed_rounds without bit 10", ErrParams)
	case f&(FeatTwoChains|FeatFourChains) == FeatTwoChains|FeatFourChains:
		return fmt.Errorf("%w: features: bits 2 and 3 together", ErrParams)
	case f&FeatScratch != 0 && p.ScratchBytes == 0:
		return fmt.Errorf("%w: features: bit 0 needs a scratchpad", ErrParams)
	case p.Blocks != 1 && p.Blocks != 2 && p.Blocks != 4 && p.Blocks != 8 && p.Blocks != 16 && p.Blocks != 32 && p.Blocks != 64:
		return fmt.Errorf("%w: blocks %d", ErrParams, p.Blocks)
	case p.Registers != 8 && p.Registers != 16 && p.Registers != 32 && p.Registers != 64:
		return fmt.Errorf("%w: registers %d", ErrParams, p.Registers)
	}
	return nil
}

// DatasetParamsHash is Pd (section 0.9.3): P of the document with bit 10
// cleared and KEYED_ROUNDS absent, the unkeyed twin's P, so a keyed document
// and its twin share one dataset; equal to P for an unkeyed document. The
// epoch seed E and the dataset id D derive from Pd; the work seed S stays on P.
func (p Params) DatasetParamsHash() [32]byte {
	t := p
	t.Features &^= FeatKeyedRounds
	t.KeyedRounds = 0
	return t.ParamsHash()
}

// Items is N_ITEMS = DATASET_BYTES / W (W the item width); CacheItems is N_CACHE (128-byte cache items).
func (p Params) Items() uint64      { return p.DatasetBytes / uint64(p.Width()) }
func (p Params) CacheItems() uint64 { return p.CacheBytes / ItemBytes }

// SliceWords is SLICE_WORDS = SCRATCH_BYTES / (4 * LANES) (0 with no
// scratchpad); SliceLines is SLICE_WORDS / LINE_WORDS.
func (p Params) SliceWords() uint32 { return p.ScratchBytes / (4 * p.Lanes) }
func (p Params) SliceLines() uint32 { return p.SliceWords() / LineWords }

// ParamsBytes is params: V || DATASET_BYTES || SCRATCH_BYTES || ROUNDS ||
// LANES || EPOCH_LENGTH || ITEM_CHAIN (36 bytes), then under versions 1 and
// 4 CACHE_BYTES || PARENTS (48 bytes), then under version 4 ITEM_BYTES
// (u32be at byte 48; 52 bytes, section 0.4).
func (p Params) ParamsBytes() []byte {
	out := make([]byte, 0, 48)
	out = binary.BigEndian.AppendUint32(out, p.Version)
	out = binary.BigEndian.AppendUint64(out, p.DatasetBytes)
	out = binary.BigEndian.AppendUint32(out, p.ScratchBytes)
	out = binary.BigEndian.AppendUint32(out, p.Rounds)
	out = binary.BigEndian.AppendUint32(out, p.Lanes)
	out = binary.BigEndian.AppendUint64(out, p.EpochLength)
	out = binary.BigEndian.AppendUint32(out, p.ItemChain)
	if p.Version == Version1 || p.Wide() {
		out = binary.BigEndian.AppendUint64(out, p.CacheBytes)
		out = binary.BigEndian.AppendUint32(out, p.Parents)
	}
	if p.Wide() {
		out = binary.BigEndian.AppendUint32(out, p.ItemBytes)
	}
	if p.Version == Version8 {
		// section 0.8: BLOCKS at byte 52, REGISTERS at 56, FEATURES at 60 only when non-zero (so the six feature-free
		// documents keep their 60-byte record), KEYED_ROUNDS at 64 under bit 10 (section 0.9)
		out = binary.BigEndian.AppendUint32(out, p.Blocks)
		out = binary.BigEndian.AppendUint32(out, p.Registers)
		if p.Features != 0 {
			out = binary.BigEndian.AppendUint32(out, p.Features)
		}
		if p.Features&FeatKeyedRounds != 0 {
			out = binary.BigEndian.AppendUint32(out, p.KeyedRounds)
		}
	}
	return out
}

// ParamsHash is P = BLAKE3(params).
func (p Params) ParamsHash() [32]byte { return sum256(p.ParamsBytes()) }

// cvp is C || V || P (the work identity: the seed S and the final digest F).
func (p Params) cvp() []byte {
	out := make([]byte, 0, 8+4+32)
	out = binary.BigEndian.AppendUint64(out, p.ChainID)
	out = binary.BigEndian.AppendUint32(out, p.Version)
	ph := p.ParamsHash()
	return append(out, ph[:]...)
}

// cvpD is C || V || Pd (the dataset identity: the epoch seed E and the dataset id D; section 0.9.3).
func (p Params) cvpD() []byte {
	out := make([]byte, 0, 8+4+32)
	out = binary.BigEndian.AppendUint64(out, p.ChainID)
	out = binary.BigEndian.AppendUint32(out, p.Version)
	ph := p.DatasetParamsHash()
	return append(out, ph[:]...)
}

// EpochOf is epoch(h) = floor(h / EPOCH_LENGTH) (section 2).
func (p Params) EpochOf(height uint64) uint64 { return height / p.EpochLength }

// Seed is S = BLAKE3(tag(T_SEED) || C || V || P || H || u64be(N)) (section 2).
func (p Params) Seed(headerHash [32]byte, nonce uint64) [32]byte {
	in := append(tag(TagSeed), p.cvp()...)
	in = append(in, headerHash[:]...)
	in = binary.BigEndian.AppendUint64(in, nonce)
	return sum256(in)
}

// EpochSeed is E = BLAKE3(tag(T_EPOCH) || C || V || Pd || u64be(epoch)) (section 5.2; Pd = P below version 8).
func (p Params) EpochSeed(epoch uint64) [32]byte {
	in := append(tag(TagEpoch), p.cvpD()...)
	in = binary.BigEndian.AppendUint64(in, epoch)
	return sum256(in)
}

// ItemIntoV0 writes version 0's item(i) (section 5.3): with c = ITEM_CHAIN,
// K_1 = BLAKE3k(E, tag(T_ITEM) || u64be(i)), K_j = BLAKE3k(E, tag(T_ITEM) || u64be(i) || K_(j-1)) for j = 2..c,
// item(i) = BLAKE3kxof(E, <the input of K_c>, 128).
func ItemIntoV0(epochSeed [32]byte, i uint64, chain uint32, out *[ItemBytes]byte) {
	in := make([]byte, 0, 1+len(TagItem)+8+32)
	in = append(in, tag(TagItem)...)
	in = binary.BigEndian.AppendUint64(in, i)
	base := len(in)
	for j := uint32(2); j <= chain; j++ {
		k := keyedSum(epochSeed, in) // K_(j-1)
		in = append(in[:base], k[:]...)
	}
	keyedXOF(epochSeed, in, out[:])
}

// BuildCache builds version 1's per-epoch cache (section 5.3): N_CACHE
// items of 128 bytes, sequential: cache(0) = BLAKE3kxof(E, tag(T_CACHE) || u64be(0), 128),
// cache(j) = BLAKE3kxof(E, tag(T_CACHE) || u64be(j) || cache(j-1)[0..32), 128).
func (p Params) BuildCache(epochSeed [32]byte) []byte {
	n := p.CacheItems()
	cache := make([]byte, n*ItemBytes)
	in := make([]byte, 0, 1+len(TagCache)+8+32)
	in = append(in, tag(TagCache)...)
	in = binary.BigEndian.AppendUint64(in, 0)
	keyedXOF(epochSeed, in, cache[:ItemBytes])
	base := len(in) - 8
	for j := uint64(1); j < n; j++ {
		in = in[:base]
		in = binary.BigEndian.AppendUint64(in, j)
		in = append(in, cache[(j-1)*ItemBytes:(j-1)*ItemBytes+32]...)
		keyedXOF(epochSeed, in, cache[j*ItemBytes:(j+1)*ItemBytes])
	}
	return cache
}

func fnv(a, b uint32) uint32 { return (a * FNVPrime) ^ b }

// ItemIntoV1 writes version 1's item(i) by the parent walk over the cache
// (section 5.3).
func ItemIntoV1(epochSeed [32]byte, cache []byte, parents uint32, i uint64, out *[ItemBytes]byte) {
	nCache := uint64(len(cache) / ItemBytes)
	mask := nCache - 1
	var mix [ItemWords]uint32
	c := cache[(i&mask)*ItemBytes:]
	for k := 0; k < ItemWords; k++ {
		mix[k] = binary.LittleEndian.Uint32(c[4*k:])
	}
	lo, hi := uint32(i), uint32(i>>32)
	mix[0] ^= lo
	mix[1] ^= hi
	for pp := uint32(0); pp < parents; pp++ {
		parent := uint64(fnv(lo^pp, mix[pp%ItemWords])) & mask
		pc := cache[parent*ItemBytes:]
		for k := 0; k < ItemWords; k++ {
			mix[k] = fnv(mix[k], binary.LittleEndian.Uint32(pc[4*k:]))
		}
	}
	in := make([]byte, 0, 1+len(TagItem)+8+ItemBytes)
	in = append(in, tag(TagItem)...)
	in = binary.BigEndian.AppendUint64(in, i)
	for k := 0; k < ItemWords; k++ {
		in = binary.LittleEndian.AppendUint32(in, mix[k])
	}
	keyedXOF(epochSeed, in, out[:])
}

// DatasetID is D = BLAKE3(tag(T_DATASET) || C || V || Pd || E || u64be(DATASET_BYTES)) (section 5.4; Pd = P below version 8).
func (p Params) DatasetID(epochSeed [32]byte) [32]byte {
	in := append(tag(TagDataset), p.cvpD()...)
	in = append(in, epochSeed[:]...)
	in = binary.BigEndian.AppendUint64(in, p.DatasetBytes)
	return sum256(in)
}

// Final is F = BLAKE3(tag(T_FINAL) || C || V || P || S || M) (section 7).
func (p Params) Final(seed, mix [32]byte) [32]byte {
	in := append(tag(TagFinal), p.cvp()...)
	in = append(in, seed[:]...)
	in = append(in, mix[:]...)
	return sum256(in)
}

// ItemSource serves dataset items by index for one (parameter set, epoch):
// the materialised Dataset (a miner's cache) or a Light source deriving each
// item on demand (the verifier; section 5.5).
type ItemSource interface {
	Params() Params
	Epoch() uint64
	EpochSeed() [32]byte
	Item(i uint64, out *[ItemBytes]byte)
}

// Light derives items on demand: directly from E under version 0; through
// the epoch's cache, built once and held, under version 1 (section 5.5).
type Light struct {
	params    Params
	epoch     uint64
	epochSeed [32]byte
	cache     []byte // version 1 only
}

// NewLight is the verifier's item source for an epoch (builds the version 1 cache).
func (p Params) NewLight(epoch uint64) *Light {
	l := &Light{params: p, epoch: epoch, epochSeed: p.EpochSeed(epoch)}
	if p.Version == Version1 || p.Wide() {
		l.cache = p.BuildCache(l.epochSeed)
	}
	return l
}

func (l *Light) Params() Params      { return l.params }
func (l *Light) Epoch() uint64       { return l.epoch }
func (l *Light) EpochSeed() [32]byte { return l.epochSeed }
func (l *Light) Item(i uint64, out *[ItemBytes]byte) {
	if l.params.Version == Version1 {
		ItemIntoV1(l.epochSeed, l.cache, l.params.Parents, i, out)
		return
	}
	if l.params.Wide() {
		l.params.ItemIntoV4(l.epochSeed, l.cache, i, out[:])
		return
	}
	ItemIntoV0(l.epochSeed, i, l.params.ItemChain, out)
}

// WideItem writes item i of width W (versions 4 and 6; 128 bytes otherwise).
func (l *Light) WideItem(i uint64, out []byte) {
	if l.params.Wide() {
		l.params.ItemIntoV4(l.epochSeed, l.cache, i, out)
		return
	}
	var item [ItemBytes]byte
	l.Item(i, &item)
	copy(out, item[:])
}

// ItemIntoV4 writes dataset item i of width W (section 0.4): for W >= 128
// one parent walk over runs of W / 128 consecutive cache items (section
// 0.3's rule; version 1's word for word at W = 128); for W = 64 version 1's
// walk over one cache item with the keyed XOF cut to 64 bytes.
func (p Params) ItemIntoV4(epochSeed [32]byte, cache []byte, i uint64, out []byte) {
	w := int(p.Width())
	if len(out) != w {
		panic("igneumhash: item buffer is not the item width")
	}
	nCache := uint64(len(cache)) / ItemBytes
	runItems := w / ItemBytes // 0 at W = 64
	words := w / 4
	if w < ItemBytes {
		runItems, words = 1, ItemWords
	}
	mix := make([]uint32, words)
	run := func(start uint64, dst []uint32) {
		for r := 0; r < runItems; r++ {
			c := cache[((start+uint64(r))%nCache)*ItemBytes:]
			for k := 0; k < ItemWords; k++ {
				dst[r*ItemWords+k] = binary.LittleEndian.Uint32(c[4*k:])
			}
		}
	}
	run(i%nCache, mix)
	mix[0] ^= uint32(i)
	mix[1] ^= uint32(i >> 32)
	parentWords := make([]uint32, words)
	for q := uint32(0); q < p.Parents; q++ {
		parent := uint64(fnv(uint32(i)^q, mix[int(q)%words])) % nCache
		run(parent, parentWords)
		for k := 0; k < words; k++ {
			mix[k] = fnv(mix[k], parentWords[k])
		}
	}
	msg := make([]byte, 0, len(TagItem)+1+8+4*words)
	msg = append(msg, tag(TagItem)...)
	msg = binary.BigEndian.AppendUint64(msg, i)
	for k := 0; k < words; k++ {
		msg = binary.LittleEndian.AppendUint32(msg, mix[k])
	}
	keyedXOF(epochSeed, msg, out)
}

// WideSource is an item source of width W (version 4).
type WideSource interface {
	ItemSource
	WideItem(i uint64, out []byte)
}

// Cache is the version 1 cache bytes (nil under version 0).
func (l *Light) Cache() []byte { return l.cache }

// CacheDigest is BLAKE3 over the cache in item order (section 9's
// cache_digest, diagnostic).
func (l *Light) CacheDigest() [32]byte { return sum256(l.cache) }

// Stages are the intermediate values of one proof: the vectors' fields
// (section 9) and the trace class's intermediates.
type Stages struct {
	ParamsHash [32]byte // P
	Seed       [32]byte // S
	Epoch      uint64   // epoch(height)
	EpochSeed  [32]byte // E
	DatasetID  [32]byte // D
	Trace      *Trace   // the trace class's intermediates when asked for
	Mix        [32]byte // M
	Digest     [32]byte // F
	// section 0.9 (bit 10): the keyed rounds' shares s_i (the prover's output,
	// the verifier's input replayed) and their messages m_i (m_0 = S, m_i the
	// eight lane words at the keyed round's entry), KEYED_ROUNDS of each
	Shares [][32]byte
	M      [][32]byte
}

// Trace holds every intermediate of section 4 for the trace vector class.
type Trace struct {
	AfterInit  [][LaneWords]uint32
	AfterFill  [][LaneWords]uint32 // equal to AfterInit with no scratchpad
	FirstIdx   uint64
	Rounds     [][][LaneWords]uint32 // [round][lane]
	RoundIdx   []uint64              // idx after each round
	Items      [][]uint32            // version 4: the item words as read, per round (version 8: after the share's injection)
	H          []uint32              // version 4: the item word h (the reduction tree's root), per round
	Fold       [][LineWords]uint32   // f[l]; nil with no scratchpad
	StateBytes []byte                // ST
	// version 8: per round, the registers after the block schedule (before the
	// tree and the cell), the opcode census as a string of digits, and the
	// keyed round's share when the round was keyed (nil otherwise)
	Block  [][][LaneWords]uint32
	Ops    []string
	Shares [][]byte
}

// Compute runs the envelope and WORK for a proof over an item source for
// epoch(height). withTrace records section 4's intermediates. A keyed
// document (section 0.9) has no keyless work: ErrKeyed, nothing computed.
func (p Params) Compute(src ItemSource, headerHash [32]byte, height, nonce uint64, withTrace bool) (Stages, error) {
	if p.Keyed() {
		return Stages{}, ErrKeyed
	}
	return p.compute(src, headerHash, height, nonce, withTrace, nil)
}

// ComputeKeyed is the keyed entry of section 0.9: with sk the prover's
// hash under the bond's secret (the shares written to the stages), with sk
// nil and shares given the verifier's replay; both return the messages m_i.
// Neither given is ErrKeyed. An unkeyed document is refused (ErrParams).
func (p Params) ComputeKeyed(src ItemSource, headerHash [32]byte, height, nonce uint64, sk *[32]byte, shares [][32]byte, withTrace bool) (Stages, error) {
	if !p.Keyed() {
		return Stages{}, fmt.Errorf("%w: the keyed entry on an unkeyed document", ErrParams)
	}
	if sk == nil && shares == nil {
		return Stages{}, ErrKeyed
	}
	if sk == nil && len(shares) != int(p.KeyedRounds) {
		return Stages{}, fmt.Errorf("%w: %d shares given, KEYED_ROUNDS is %d", ErrKeyed, len(shares), p.KeyedRounds)
	}
	io := &keyedIO{sk: sk, shares: make([][32]byte, p.KeyedRounds), m: make([][32]byte, p.KeyedRounds)}
	if sk == nil {
		copy(io.shares, shares)
	}
	return p.compute(src, headerHash, height, nonce, withTrace, io)
}

// keyedIO carries the keyed rounds' secret or shares into the work and the
// shares and messages out (section 0.9).
type keyedIO struct {
	sk     *[32]byte  // the prover's secret; nil on the verifier's replay
	shares [][32]byte // written by the prover, read by the verifier
	m      [][32]byte // the messages, written on either call
}

func (p Params) compute(src ItemSource, headerHash [32]byte, height, nonce uint64, withTrace bool, io *keyedIO) (Stages, error) {
	var st Stages
	if err := p.Validate(); err != nil {
		return st, err
	}
	if p.Version == Version8 && p.Registers > LaneWords {
		return st, fmt.Errorf("%w: version 8 with REGISTERS %d (the wider state)", ErrNotReached, p.Registers)
	}
	epoch := p.EpochOf(height)
	if src == nil || src.Epoch() != epoch || src.Params() != p {
		return st, ErrEpoch
	}
	st.ParamsHash = p.ParamsHash()
	st.Epoch = epoch
	st.EpochSeed = src.EpochSeed()
	st.DatasetID = p.DatasetID(st.EpochSeed)
	st.Seed = p.Seed(headerHash, nonce)
	if withTrace {
		st.Trace = &Trace{}
	}
	st.Mix = p.work(src, st.Seed, st.Trace, io)
	st.Digest = p.Final(st.Seed, st.Mix)
	if io != nil {
		st.Shares, st.M = io.shares, io.m
	}
	return st, nil
}

// Target is floor((2^256 - 1) / difficulty) for 1 <= difficulty <= 2^256 - 1 (section 7).
func Target(difficulty *big.Int) (target [32]byte, ok bool) {
	if difficulty == nil || difficulty.Sign() <= 0 || difficulty.BitLen() > 256 {
		return target, false
	}
	max := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	max.Div(max, difficulty).FillBytes(target[:])
	return target, true
}

// Verify is the verifier of section 8 in its order: difficulty range first
// (nothing hashed on a bad one), the epoch from the validated height, then
// S, light WORK, M against MixDigest, then F against the target. src nil
// means a Light source for the height's epoch.
func (p Params) Verify(src ItemSource, headerHash [32]byte, height, nonce uint64, mixDigest [32]byte, difficulty *big.Int) (Stages, error) {
	target, ok := Target(difficulty)
	if !ok {
		return Stages{}, ErrDifficulty
	}
	if src == nil {
		src = p.NewLight(p.EpochOf(height))
	}
	st, err := p.Compute(src, headerHash, height, nonce, false)
	if err != nil {
		return st, err
	}
	if st.Mix != mixDigest {
		return st, ErrMix
	}
	if !lessOrEqual(st.Digest, target) {
		return st, ErrTarget
	}
	return st, nil
}

// lessOrEqual is uint256be(a) <= uint256be(b), full width.
func lessOrEqual(a, b [32]byte) bool {
	for i := 0; i < 32; i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Section 4: WORK(S, E) = M. Every value is a u32 with wrap-around; rotates
// and shifts mask their amount with & 31; words are little-endian. With
// SCRATCH_BYTES = 0 the scratch fill (4.2), the scratch step (4.5) and the
// fold (4.7) are absent and ST is the 512 register bytes alone.

func rotl(x uint32, n uint32) uint32 { return bits.RotateLeft32(x, int(n&31)) }
func rotr(x uint32, n uint32) uint32 { return bits.RotateLeft32(x, -int(n&31)) }

func (p Params) work(src ItemSource, seed [32]byte, tr *Trace, io *keyedIO) [32]byte {
	lanes := int(p.Lanes) // 8
	sliceWords := p.SliceWords()
	sliceLines := p.SliceLines()
	hasScratch := sliceWords > 0
	nItems := p.Items()
	// the address map (section 0.5, version 8 by the size): a power of two masks; otherwise the multiply-high map
	// idx = (x N_ITEMS) >> 32 over the 32-bit x
	fastrange := p.Version == Version8 && !isPow2(nItems)
	itemMask := nItems - 1
	mapIdx := func(x uint32) uint64 {
		if fastrange {
			return (uint64(x) * nItems) >> 32
		}
		return uint64(x) & itemMask
	}
	blended := p.Version == Version8
	F := p.Features

	// 4.1 lane initialisation: L = BLAKE3xof(tag(T_LANES) || S, 64 * LANES)
	R := make([][LaneWords]uint32, lanes)
	lbuf := make([]byte, 64*lanes)
	hashXOF(append(tag(TagLanes), seed[:]...), lbuf)
	for l := 0; l < lanes; l++ {
		for k := 0; k < LaneWords; k++ {
			R[l][k] = binary.LittleEndian.Uint32(lbuf[64*l+4*k:])
		}
	}
	if tr != nil {
		tr.AfterInit = cloneRegs(R)
	}

	// 4.2 scratch fill, each lane on its own slice
	var SL [][]uint32
	if hasScratch {
		SL = make([][]uint32, lanes)
		for l := 0; l < lanes; l++ {
			SL[l] = make([]uint32, sliceWords)
			for j := uint32(0); j < sliceWords; j++ {
				a := R[l][j%16]
				b := R[l][(j+5)%16]
				a = (a ^ rotl(b, 13)) * Mul
				a = a + (b >> 7)
				R[l][j%16] = a
				SL[l][j] = a ^ R[l][(j+11)%16]
			}
		}
	}
	// 4.3 first address
	idx := mapIdx(R[0][0] ^ R[4][8])
	if tr != nil {
		tr.AfterFill = cloneRegs(R)
		tr.FirstIdx = idx
	}

	var item [ItemBytes]byte
	var W [ItemWords]uint32
	var X [8][LaneWords]uint32
	// versions 4 and 6 (sections 0.4 and 0.6): the whole item of width n = W / 4 words
	strata := p.Wide()
	keyed := p.Version == Version6
	var keys []uint32
	var wide WideSource
	var wideItem []byte
	var wideWords, leaves []uint32
	var h uint32
	if strata {
		var ok bool
		if wide, ok = src.(WideSource); !ok {
			panic("igneumhash: version 4 needs a wide item source")
		}
		wideItem = make([]byte, p.Width())
		wideWords = make([]uint32, p.Width()/4)
		leaves = make([]uint32, p.Width()/4)
		keys = make([]uint32, p.Width()/4)
	}
	var desc []v8desc
	var epochSeed [32]byte
	if blended {
		epochSeed = src.EpochSeed()
	}
	for r := uint32(0); r < p.Rounds; r++ {
		// 4.4 item load and arithmetic
		if strata {
			n := len(wideWords)
			if blended && F&FeatProgrammeOnly != 0 {
				// programme-only (bit 9, a measurement mode): no fetch; the item's words from the lane state itself with the
				// epoch seed folded in (eight register-resident words), so the height binding by the epoch stays
				for j := 0; j < n; j++ {
					wideWords[j] = rotl(R[j%lanes][(j/lanes)%LaneWords], 7) ^ (uint32(j+1) * Mul) ^ binary.LittleEndian.Uint32(epochSeed[4*(j%8):])
				}
			} else {
				wide.WideItem(idx, wideItem)
				for j := 0; j < n; j++ {
					wideWords[j] = binary.LittleEndian.Uint32(wideItem[4*j:])
				}
			}
			// version 6: the leaf keys K[j] = R[j mod 8][(j div 8) mod 16] as the registers stand before the absorb
			if keyed {
				for j := 0; j < n; j++ {
					keys[j] = R[j%8][(j/8)%LaneWords]
				}
			}
			// section 0.9 (bit 10): the keyed round i's share s_i = H(sk, m_i) enters the fetched item's first eight
			// words before the absorb, the programme and the tree, so the work cannot proceed without the secret; the
			// verifier replays the shares it was given. m_0 = S, m_i the eight lane words R[l][0] at the round's entry.
			var shareHex []byte
			if io != nil {
				if i, isKeyed := p.KeyedRound(r); isKeyed {
					var m [32]byte
					if i == 0 {
						m = seed
					} else {
						for l := 0; l < 8; l++ {
							binary.LittleEndian.PutUint32(m[4*l:], R[l][0])
						}
					}
					io.m[i] = m
					if io.sk != nil {
						io.shares[i] = BondShare(*io.sk, i, m)
					}
					s := io.shares[i]
					for j := 0; j < 8; j++ {
						wideWords[j] ^= binary.LittleEndian.Uint32(s[4*j:])
					}
					shareHex = s[:]
				}
			}
			// step 1: absorb, every word into its own register, in order
			for j := 0; j < n; j++ {
				R[j%8][(j/8)%LaneWords] += wideWords[j]
			}
			// step 1b (section 0.8, version 8): B blocks of K descriptors on registers 0..K-1 of every lane, the programme
			// drawn from the fetched item (draw revision 2); walk-only (bit 8) replaces the block's output by a constant
			if blended {
				K, B := p.Registers, p.Blocks
				if F&FeatWalkOnly != 0 {
					B = 0
					for d := uint32(0); d < K; d++ {
						for l := 0; l < lanes; l++ {
							R[l][d] ^= walkConst
						}
					}
				}
				if B > 0 {
					desc = v8decode(wideWords, B, K, F, desc)
					v8block(R, desc, B, K, F, SL, sliceWords)
				}
				if tr != nil {
					tr.Block = append(tr.Block, cloneRegs(R))
					ops := make([]byte, 0, B*K)
					for i := uint32(0); i < B*K; i++ {
						ops = append(ops, '0'+desc[i].opcode)
					}
					tr.Ops = append(tr.Ops, string(ops))
					tr.Shares = append(tr.Shares, shareHex)
				}
			}
			// step 2: the balanced domain-separated reduction tree (version 6: keyed leaves, a finalised root)
			for j := 0; j < n; j++ {
				leaves[j] = wideWords[j] ^ (uint32(j+1) * Mul)
				if keyed {
					leaves[j] ^= keys[j]
				}
			}
			for m := n; m >= 2; m /= 2 {
				for i := 0; i < m/2; i++ {
					x, y := leaves[2*i], leaves[2*i+1]
					leaves[i] = ((x ^ rotl(y, 13)) * Mul) + (y >> 7)
				}
			}
			h = leaves[0]
			if keyed {
				h ^= h >> 16
				h *= Mul
				h ^= h >> 15
			}
			if tr != nil {
				tr.Items = append(tr.Items, append([]uint32(nil), wideWords...))
				tr.H = append(tr.H, h)
			}
		} else {
			src.Item(idx, &item)
			for k := 0; k < ItemWords; k++ {
				W[k] = binary.LittleEndian.Uint32(item[4*k:])
			}
		}
		for l := 0; l < lanes; l++ {
			for k := 0; k < 4; k++ {
				a, b, c, d := R[l][4*k], R[l][4*k+1], R[l][4*k+2], R[l][4*k+3]
				w := W[4*l+k]
				if strata {
					w = h // step 3: the cell once per lane with w = h for every quad
				}
				a = a + w
				b = b ^ a
				c = c + b*Mul
				d = rotr(d^c, a&31)
				a = a ^ (d >> (w & 31))
				b = rotl(b+d, 7)
				c = c ^ rotl(a, 17)
				d = d + b*c
				R[l][4*k], R[l][4*k+1], R[l][4*k+2], R[l][4*k+3] = a, b, c, d
			}
			T := R[l]
			for k := 0; k < LaneWords; k++ {
				R[l][k] = T[perm(k)]
			}
		}
		// 4.5 scratch access, each lane on its own slice
		if hasScratch {
			for l := 0; l < lanes; l++ {
				line := R[l][0] & (sliceLines - 1)
				for j := uint32(0); j < 4; j++ {
					s := SL[l][4*line+j]
					R[l][8+j] = rotl(R[l][8+j]^s, 5) + R[l][j]
					SL[l][4*line+j] = (s + R[l][12+j]) ^ R[l][8+j]
				}
			}
		}
		// 4.5 lane exchange with snapshot semantics
		for l := 0; l < lanes; l++ {
			X[l] = R[l]
		}
		for l := 0; l < lanes; l++ {
			pp := l ^ (1 << (r % 3))
			q := (l + 1 + int(X[l][1]%7)) % 8
			R[l][0] = X[l][0] + X[pp][0]
			R[l][1] = X[l][1] ^ X[q][1]
			R[l][2] = X[l][2] + rotl(X[pp][2], 11)
			R[l][3] = X[l][3] ^ rotl(X[q][3], 23)
		}
		// 4.6 next address (version 4, step 5: idx = (R[g][0] ^ h) mod N_ITEMS)
		g := int((r + 1) % p.Lanes)
		if strata {
			idx = mapIdx(R[g][0] ^ h)
		} else {
			idx = mapIdx(R[g][0])
		}
		if tr != nil {
			tr.Rounds = append(tr.Rounds, cloneRegs(R))
			tr.RoundIdx = append(tr.RoundIdx, idx)
		}
	}

	// 4.7 finalisation: the scratch fold (if any) and the state digest
	ST := make([]byte, 0, 4*(LaneWords+LineWords)*lanes)
	for l := 0; l < lanes; l++ {
		for k := 0; k < LaneWords; k++ {
			ST = binary.LittleEndian.AppendUint32(ST, R[l][k])
		}
	}
	if hasScratch {
		f := make([][LineWords]uint32, lanes)
		for l := 0; l < lanes; l++ {
			for i := uint32(0); i < sliceLines; i++ {
				for j := uint32(0); j < 4; j++ {
					f[l][j] = rotl(f[l][j]^SL[l][4*i+j], 9) + SL[l][4*i+((j+1)%4)]
				}
			}
		}
		for l := 0; l < lanes; l++ {
			for j := 0; j < LineWords; j++ {
				ST = binary.LittleEndian.AppendUint32(ST, f[l][j])
			}
		}
		if tr != nil {
			tr.Fold = f
		}
	}
	if tr != nil {
		tr.StateBytes = ST
	}
	in := append(tag(TagMix), seed[:]...)
	in = append(in, ST...)
	return sum256(in)
}

func cloneRegs(R [][LaneWords]uint32) [][LaneWords]uint32 {
	out := make([][LaneWords]uint32, len(R))
	copy(out, R)
	return out
}
