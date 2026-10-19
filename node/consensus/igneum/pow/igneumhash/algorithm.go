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
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
)

func init() {
	// Development-only until the founder ratifies a selection (PHASE-ZERO.md):
	// only --dev builds it, never a genesis file.
	pow.RegisterDevOnly(Name, func(params []byte) (pow.Algorithm, error) {
		// A document of a version only the C reference carries (version
		// 8, the blended relation) goes to the reference through the lab
		// build (reference.go); the public build refuses it.
		if ref, chainID := referenceOnly(params); ref {
			return newReference(chainID, params)
		}
		chainID, p, err := DecodeChainParams(params)
		if err != nil {
			return nil, err
		}
		return New(chainID, p, defaultWork()), nil
	})
}

// Works are the implementations of WORK the node carries, both written from
// SPEC-R0A-v0.md's text and agreed with the reference on every vector
// (spec_vectors_test.go runs each): "spec", this package's (the protocol team), the
// default; "candidate", protocol B's package behind the same interface,
// selectable for the differential (--dev.algorithm.work). A consensus rule
// is never chosen by this switch: both compute the same M or the test fails.
var Works = map[string]Work{
	"spec":      specWork{},
	"candidate": CandidateWork{},
}

var (
	workMu       sync.Mutex
	selectedWork = "spec"
)

// SelectWork names the WORK implementation the registry builds with.
func SelectWork(name string) error {
	workMu.Lock()
	defer workMu.Unlock()
	if _, ok := Works[name]; !ok {
		return fmt.Errorf("igneumhash: unknown WORK implementation %q (spec or candidate)", name)
	}
	selectedWork = name
	return nil
}

func defaultWork() Work {
	workMu.Lock()
	defer workMu.Unlock()
	return Works[selectedWork]
}

// Algorithm is IgneumHash-R0A behind the node's pow.Algorithm interface. It
// is safe for concurrent use; its only mutable state is the two-entry epoch
// cache: the epoch seed and, under version 1, the built cache (CACHE_BYTES),
// for the current and the previous epoch so a reorg across a boundary does
// not rebuild.
type Algorithm struct {
	env  Envelope
	work Work

	mu       sync.Mutex
	cache    [EpochCacheEntries]epochSeed // the previous, current and next epoch around the anchor
	anchored bool                         // the engine has told us the chain head's epoch
	anchor   uint64                       // the chain head's epoch (authenticated by the chain, never a peer's number)
	builds   uint64                       // cache builds so far (the bound's witness in tests)
	prebuild bool                         // a build of the next epoch is in flight
}

// The admission and resource bounds of the adapter (the engineering
// specification section 11; security's F-18), by name so that a saturation
// test asserts the declared numbers:
const (
	// EpochCacheEntries is the most epoch caches resident: the previous, the
	// current and the next epoch around the chain head.
	EpochCacheEntries = 3
	// EpochAdmissionWindow is how many epochs each side of the chain head's
	// epoch a proof may be verified in; a height outside it is refused before
	// any cache is built (pow.ErrEpochNotAdmitted). Unanchored (no engine, the
	// vector tools and tests) every epoch is admitted.
	EpochAdmissionWindow = 1
	// CacheBuildConcurrency is how many caches build at once: one, under the
	// lock; concurrent verifiers of a new epoch wait for that build.
	CacheBuildConcurrency = 1
	// PrebuildBlocks is how far before an epoch boundary the next epoch's
	// cache is built ahead (in the background, once), so that no header within
	// the window rebuilds at the boundary.
	PrebuildBlocks = 256
)

type epochSeed struct {
	epoch uint64
	seed  [32]byte
	cache []byte // version 1: the built cache; nil under version 0
	set   bool
	// items is the pool of version 1 item derivers over this entry's seed and
	// cache (each with its keyed hasher), taken and returned per proof so a
	// verification allocates nothing after warm-up (the project's order of 20:4x UK,
	// 12 October 2026: the minimum node's 15 allocations and 9.5 KB an op).
	// A pointer: an entry is assigned by value and a sync.Pool is never copied.
	items *sync.Pool
	// items0 is the same for version 0's Items (one keyed hasher each)
	items0 *sync.Pool
}

// newItemsPool is the entry's pool of ItemsV1 for one seed and cache.
func newItemsPool(seed [32]byte, cache []byte, parents uint32) *sync.Pool {
	return &sync.Pool{New: func() any { return NewItemsV1(seed, cache, parents) }}
}

// New builds the algorithm for a chain and a parameter set over the given
// resource-work function.
func New(chainID uint64, p Params, work Work) *Algorithm {
	return &Algorithm{env: NewEnvelope(chainID, p), work: work}
}

// Name implements pow.Algorithm.
func (a *Algorithm) Name() string { return Name }

// Params returns the parameter set.
func (a *Algorithm) Params() Params { return a.env.Params }

// Epoch implements pow.Algorithm: floor(height / EPOCH_LENGTH).
func (a *Algorithm) Epoch(height uint64) uint64 { return a.env.Epoch(height) }

// SeedHash implements pow.Algorithm: the epoch seed E, what a miner derives the
// dataset from. The verifier never builds that dataset.
func (a *Algorithm) SeedHash(height uint64) common.Hash {
	// E alone (two hashes); no cache is built for a seed request.
	return common.Hash(a.env.EpochSeed(a.env.Epoch(height)))
}

// Anchor tells the adapter the chain head's height (the engine calls it with
// its chain's current header before verifying, preparing or sealing): the
// window of admitted epochs follows the head, entries outside it are
// released, and near the end of an epoch the next epoch's cache is built
// ahead in the background. The head is the chain's, so no peer's number ever
// selects an epoch or triggers a build.
func (a *Algorithm) Anchor(height uint64) {
	epoch := a.env.Epoch(height)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.anchored, a.anchor = true, epoch
	for i := range a.cache {
		if a.cache[i].set && !a.inWindow(a.cache[i].epoch) {
			a.cache[i] = epochSeed{}
		}
	}
	if (a.env.Params.Version != Version1 && !a.env.Params.Wide()) || a.prebuild {
		return
	}
	if height%a.env.Params.EpochLength >= a.env.Params.EpochLength-PrebuildBlocks && !a.held(epoch+1) {
		a.prebuild = true
		go func() {
			a.mu.Lock()
			defer a.mu.Unlock()
			a.prebuild = false
			if a.inWindow(epoch+1) && !a.held(epoch+1) {
				a.build(epoch + 1)
			}
		}()
	}
}

// Builds is the number of cache builds so far.
func (a *Algorithm) Builds() uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.builds
}

// inWindow reports whether an epoch is admitted (the lock held).
func (a *Algorithm) inWindow(epoch uint64) bool {
	if !a.anchored {
		return true
	}
	lo := uint64(0)
	if a.anchor > EpochAdmissionWindow {
		lo = a.anchor - EpochAdmissionWindow
	}
	return epoch >= lo && epoch <= a.anchor+EpochAdmissionWindow
}

// held reports whether an epoch's entry is resident (the lock held).
func (a *Algorithm) held(epoch uint64) bool {
	for i := range a.cache {
		if a.cache[i].set && a.cache[i].epoch == epoch {
			return true
		}
	}
	return false
}

// admits reports whether a height's epoch may be verified now.
func (a *Algorithm) admits(height uint64) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.inWindow(a.env.Epoch(height))
}

// build makes an epoch's entry (the lock held), replacing the resident entry
// farthest from the anchor (the oldest when unanchored).
func (a *Algorithm) build(epoch uint64) *epochSeed {
	seed := a.env.EpochSeed(epoch)
	var cache []byte
	if a.env.Params.Version == Version1 || a.env.Params.Wide() {
		cache = BuildCache(seed, a.env.Params.CacheBytes)
	}
	a.builds++
	slot, worst := -1, int64(math.MinInt64)
	for i := range a.cache {
		if !a.cache[i].set {
			slot = i
			break
		}
		var d int64
		if a.anchored {
			d = int64(a.cache[i].epoch) - int64(a.anchor)
			if d < 0 {
				d = -d
			}
		} else {
			d = -int64(a.cache[i].epoch) // oldest epoch first
		}
		if d > worst {
			slot, worst = i, d
		}
	}
	a.cache[slot] = epochSeed{epoch: epoch, seed: seed, cache: cache, set: true}
	if cache != nil {
		a.cache[slot].items = newItemsPool(seed, cache, a.env.Params.Parents)
	} else {
		chain := a.env.Params.ItemChain
		a.cache[slot].items0 = &sync.Pool{New: func() any { return NewItems(seed, chain) }}
	}
	return &a.cache[slot]
}

// epochEntry returns the epoch's entry (E and, under version 1, the built
// cache), building it once within the admitted window and refusing outside
// it before any work. Building the version 1 cache is N_CACHE sequential
// keyed hashes; it happens at most once per epoch per node, under the lock
// (CacheBuildConcurrency), so concurrent verifiers of the same new epoch wait
// for one build rather than each building.
func (a *Algorithm) epochEntry(epoch uint64) (*epochSeed, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.inWindow(epoch) {
		return nil, pow.ErrEpochNotAdmitted
	}
	for i := range a.cache {
		if a.cache[i].set && a.cache[i].epoch == epoch {
			return &a.cache[i], nil
		}
	}
	return a.build(epoch), nil
}

// CacheResident reports the bytes of epoch cache held (version 1: up to
// EpochCacheEntries epochs), for the verifier contract.
func (a *Algorithm) CacheResident() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for i := range a.cache {
		n += len(a.cache[i].cache)
	}
	return n
}

// Compute implements pow.Algorithm: (M, F) for (H, height, N) with light
// verification, deriving only the items the work touches.
func (a *Algorithm) Compute(headerHash common.Hash, height uint64, nonce uint64) (common.Hash, common.Hash) {
	seed := a.env.Seed(headerHash, nonce)
	entry, err := a.epochEntry(a.env.Epoch(height))
	if err != nil {
		// Outside the admitted window: no cache is built and the hashes are
		// zero, which no mix digest or target matches (Verify refuses first).
		return common.Hash{}, common.Hash{}
	}
	es := entry.seed
	var mix [32]byte
	if a.env.Params.Wide() {
		// Strata: the width-W items and the whole-item round (specWork only;
		// a Work without it yields nothing under versions 4 and 6).
		wide, ok := a.work.(interface {
			MixWide(seed, epochSeed [32]byte, item WideItemFunc, p Params) [32]byte
		})
		if !ok {
			// A Work without the whole-item round (the candidate package)
			// cannot compute version 4: zero hashes, which nothing matches.
			return common.Hash{}, common.Hash{}
		}
		mix = wide.MixWide(seed, es, NewItemsW(es, entry.cache, a.env.Params.Parents, a.env.Params.Width).Item, a.env.Params)
	} else {
		var item ItemFunc
		if a.env.Params.Version == Version1 {
			// The pooled deriver over this entry's seed and cache: one keyed
			// hasher, taken for this proof and returned after the mix.
			// (The entry is read outside a.mu; its pool is set by build under
			// the lock and never written here. An entry without one, which a
			// version 1 build never leaves, derives one-off as before.)
			if entry.items == nil {
				item = NewItemsV1(es, entry.cache, a.env.Params.Parents).Item
				mix = a.work.Mix(seed, es, item, a.env.Params)
				return common.Hash(mix), common.Hash(a.env.Final(seed, mix))
			}
			it := entry.items.Get().(*ItemsV1)
			mix = a.work.Mix(seed, es, it.Fn(), a.env.Params)
			entry.items.Put(it)
			return common.Hash(mix), common.Hash(a.env.Final(seed, mix))
		}
		if entry.items0 == nil {
			item = NewItems(es, a.env.Params.ItemChain).Item
			mix = a.work.Mix(seed, es, item, a.env.Params)
			return common.Hash(mix), common.Hash(a.env.Final(seed, mix))
		}
		it0 := entry.items0.Get().(*Items)
		mix = a.work.Mix(seed, es, it0.Fn(), a.env.Params)
		entry.items0.Put(it0)
	}
	return common.Hash(mix), common.Hash(a.env.Final(seed, mix))
}

// Verify implements pow.Algorithm in the envelope's rejection order (section
// 8): the difficulty before any work, then the work with light verification,
// then the mix digest, then the target. fullDataset is ignored: a node never
// needs the dataset.
func (a *Algorithm) Verify(headerHash common.Hash, height uint64, nonce uint64, mixDigest common.Hash, difficulty *big.Int, fullDataset bool) error {
	if difficulty == nil || difficulty.Sign() <= 0 || difficulty.BitLen() > 256 {
		return pow.ErrInvalidDifficulty
	}
	if !a.admits(height) {
		return pow.ErrEpochNotAdmitted
	}
	return pow.Verify(a, headerHash, height, nonce, mixDigest, difficulty)
}

// Accesses is the number of dataset items one proof reads, the verifier
// contract's constant for this parameter set.
func (a *Algorithm) Accesses() uint64 { return a.work.Accesses(a.env.Params) }

// ParamsDigest is P, the digest of the canonical parameter encoding: the
// job tag beside the algorithm name, the epoch and the template identifier.
func (a *Algorithm) ParamsDigest() common.Hash { return common.Hash(a.env.Params.Hash()) }

// WorkParams is the params document igneum_getWork carries for this chain
// (the spec's shape, the one the worker takes verbatim).
func (a *Algorithm) WorkParams() any { return a.env.Params.Document(a.env.ChainID) }

// Vectors implements pow.Algorithm: the frozen vectors embedded for one
// parameter set and chain (the baseline, written by TestWriteVectors on
// the build box from this package's WORK, which agrees with the reference's
// vectors, spec_vectors_test.go). An algorithm on another set or chain has
// none here; under the fixture rule an empty set fails the vector test rather
// than passing it.
func (a *Algorithm) Vectors() []pow.Vector {
	f := frozenVectors()
	if f == nil || a.env.Params != f.params || a.env.ChainID != f.chainID {
		return nil
	}
	return f.vectors
}

// vectorChainID is the chain the frozen vectors are for: the development chain.
const vectorChainID = 7310

//go:embed testdata/vectors-v0.json
var vectorsV0 []byte

type frozen struct {
	chainID uint64
	params  Params
	vectors []pow.Vector
}

var (
	frozenOnce sync.Once
	frozenSet  *frozen
)

type vectorFile struct {
	ChainID uint64 `json:"chainId"`
	Params  Params `json:"params"`
	Vectors []struct {
		Name       string      `json:"name"`
		HeaderHash common.Hash `json:"headerHash"`
		Height     uint64      `json:"height"`
		Nonce      uint64      `json:"nonce"`
		MixDigest  common.Hash `json:"mixDigest"`
		Result     common.Hash `json:"result"`
	} `json:"vectors"`
}

func frozenVectors() *frozen {
	frozenOnce.Do(func() {
		var f vectorFile
		if len(vectorsV0) == 0 || json.Unmarshal(vectorsV0, &f) != nil || len(f.Vectors) == 0 {
			return
		}
		set := &frozen{chainID: f.ChainID, params: f.Params}
		for _, v := range f.Vectors {
			set.vectors = append(set.vectors, pow.Vector{Name: v.Name, HeaderHash: v.HeaderHash, Height: v.Height, Nonce: v.Nonce, MixDigest: v.MixDigest, Result: v.Result})
		}
		frozenSet = set
	})
	return frozenSet
}
