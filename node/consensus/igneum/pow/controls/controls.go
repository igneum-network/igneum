// Copyright 2026 The Igneum Authors
// This file is part of the Igneum node.
//
// The Igneum node is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The Igneum node is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the Igneum node. If not, see <http://www.gnu.org/licenses/>.

//go:build controls && cgo

package controls

/*
#cgo CFLAGS: -I${SRCDIR}/../../../../../pow/ref/include
#cgo LDFLAGS: -L${SRCDIR}/../../../../../build/ref -lignpow -lrvn_ethash -lblake3 -lstdc++ -lm
#include <stdlib.h>
#include "ignpow.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"sync"
	"unsafe"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
)

// The control ids, as the reference names them (ignpow.h).
const (
	KawpowName    = "kawpow-0.9.4"
	FishhashName  = "fishhash-1"
	AutolykosName = "autolykos-2"
)

// Names lists the controls this package adapts, in the comparison's order.
var Names = []string{KawpowName, FishhashName, AutolykosName}

var errNotInBuild = errors.New("controls: candidate is not in the linked reference build")

func init() {
	for _, name := range Names {
		name := name
		pow.RegisterDevOnly(name, func(params []byte) (pow.Algorithm, error) {
			if len(params) != 0 && string(params) != "{}" && string(params) != "null" {
				return nil, fmt.Errorf("controls: %s takes no parameter document (upstream defaults only), got %q", name, params)
			}
			if !inBuild(name) {
				return nil, fmt.Errorf("%w: %s (build string: %s)", errNotInBuild, name, Build())
			}
			return &Algorithm{id: name}, nil
		})
	}
}

// inBuild reports whether the linked reference carries the candidate
// (FishHash is present only when the reference was built with its source).
func inBuild(name string) bool {
	n := int(C.ignpow_algo_count())
	for i := 0; i < n; i++ {
		if C.GoString(C.ignpow_algo_id(C.size_t(i))) == name {
			return true
		}
	}
	return false
}

// Build is the reference's build string (ABI version, candidates, pins, commit).
func Build() string { return C.GoString(C.ignpow_build_string()) }

// SelfTest runs the vectors embedded in the reference for one candidate (or
// all with an empty name) and returns the count checked.
func SelfTest(name string) (int, error) {
	var count C.size_t
	var cname *C.char
	if name != "" {
		cname = C.CString(name)
		defer C.free(unsafe.Pointer(cname))
	}
	if st := C.ignpow_vectors_check(cname, &count); st != C.IGNPOW_OK {
		return int(count), fmt.Errorf("controls: embedded vectors failed for %q: %s", name, statusName(int(st)))
	}
	return int(count), nil
}

func statusName(st int) string {
	switch st {
	case C.IGNPOW_OK:
		return "ok"
	case C.IGNPOW_E_ALGO:
		return "unknown candidate"
	case C.IGNPOW_E_PARAMS:
		return "params rejected"
	case C.IGNPOW_E_HEIGHT:
		return "height outside the schedule"
	case C.IGNPOW_E_EPOCH:
		return "context prepared for another epoch"
	case C.IGNPOW_E_BOUNDARY:
		return "final hash above the boundary"
	case C.IGNPOW_E_MIX:
		return "mix hash mismatch"
	case C.IGNPOW_E_ARG:
		return "bad argument"
	case C.IGNPOW_E_MEMORY:
		return "context allocation refused"
	case C.IGNPOW_E_VECTORS:
		return "embedded self-test failed"
	}
	return fmt.Sprintf("status %d", st)
}

// Algorithm is one control behind pow.Algorithm. It keeps at most two light
// contexts (the current epoch and the one before or after it), built under
// the lock so that concurrent verifiers of a new epoch wait for one build
// rather than each building, the same shape as the IgneumHash adapter.
type Algorithm struct {
	id string

	mu       sync.Mutex
	slot     [EpochCacheEntries]ctxSlot
	anchored bool
	anchor   uint64 // the chain head's epoch, from the engine
	prebuild bool
	builds   uint64
}

// The same admission bounds as the IgneumHash adapter's, by name.
const (
	EpochCacheEntries     = 3
	EpochAdmissionWindow  = 1
	CacheBuildConcurrency = 1
	PrebuildBlocks        = 256
)

type ctxSlot struct {
	epoch uint64
	ctx   *C.ignpow_ctx
	set   bool
}

// Name implements pow.Algorithm.
func (a *Algorithm) Name() string { return a.id }

// Epoch implements pow.Algorithm through the reference's schedule for the
// candidate (KAWPOW: ethash epochs of 7500 blocks; Autolykos: its own height
// schedule; FishHash: one epoch for ever).
func (a *Algorithm) Epoch(height uint64) uint64 {
	cid := C.CString(a.id)
	defer C.free(unsafe.Pointer(cid))
	var st C.int
	e := C.ignpow_epoch_of(cid, nil, C.uint64_t(height), &st)
	if st != C.IGNPOW_OK {
		return 0
	}
	return uint64(e)
}

// SeedHash implements pow.Algorithm. The controls expose no seed through the
// reference ABI: a worker derives the epoch state from the height alone
// (ignpow_ctx_create), so the node's work package carries a zero seed for
// them. Documented in VERIFIER-R0A.md section 6; the controls are verified,
// never mined for keeps.
func (a *Algorithm) SeedHash(height uint64) common.Hash { return common.Hash{} }

// Anchor tells the adapter the chain head's height (the engine calls it
// before verifying, preparing or sealing): the admitted window follows the
// head, contexts outside it are freed, and near the end of an epoch the next
// epoch's context is built ahead in the background (the ethash family's
// cache; nothing for Autolykos, whose epoch is its own height schedule).
func (a *Algorithm) Anchor(height uint64) {
	epoch := a.Epoch(height)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.anchored, a.anchor = true, epoch
	for i := range a.slot {
		if a.slot[i].set && !a.inWindow(a.slot[i].epoch) {
			C.ignpow_ctx_free(a.slot[i].ctx)
			a.slot[i] = ctxSlot{}
		}
	}
	if a.id == AutolykosName || a.prebuild {
		return
	}
	next := height + PrebuildBlocks
	if nextEpoch := a.Epoch(next); nextEpoch == epoch+1 && !a.held(nextEpoch) {
		a.prebuild = true
		go func() {
			a.mu.Lock()
			defer a.mu.Unlock()
			a.prebuild = false
			if a.inWindow(nextEpoch) && !a.held(nextEpoch) {
				a.create(next, nextEpoch)
			}
		}()
	}
}

// Builds is the number of context builds so far.
func (a *Algorithm) Builds() uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.builds
}

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

func (a *Algorithm) held(epoch uint64) bool {
	for i := range a.slot {
		if a.slot[i].set && a.slot[i].epoch == epoch {
			return true
		}
	}
	return false
}

// admits reports whether a height's epoch may be verified now.
func (a *Algorithm) admits(height uint64) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.inWindow(a.Epoch(height))
}

// create builds the light context for a height's epoch (the lock held),
// replacing the resident context farthest from the anchor.
func (a *Algorithm) create(height, epoch uint64) (*C.ignpow_ctx, error) {
	cid := C.CString(a.id)
	defer C.free(unsafe.Pointer(cid))
	var st C.int
	ctx := C.ignpow_ctx_create(cid, nil, C.uint64_t(height), 0, &st)
	if ctx == nil || st != C.IGNPOW_OK {
		return nil, fmt.Errorf("controls: %s: no light context at height %d: %s", a.id, height, statusName(int(st)))
	}
	a.builds++
	slot, worst := -1, int64(math.MinInt64)
	for i := range a.slot {
		if !a.slot[i].set {
			slot = i
			break
		}
		d := int64(a.slot[i].epoch) - int64(a.anchor)
		if !a.anchored {
			d = -int64(a.slot[i].epoch)
		} else if d < 0 {
			d = -d
		}
		if d > worst {
			slot, worst = i, d
		}
	}
	if a.slot[slot].set {
		C.ignpow_ctx_free(a.slot[slot].ctx)
	}
	a.slot[slot] = ctxSlot{epoch: epoch, ctx: ctx, set: true}
	return ctx, nil
}

// context returns the light context for the epoch that contains height,
// within the admitted window only, creating it once and keeping at most
// EpochCacheEntries.
func (a *Algorithm) context(height uint64) (*C.ignpow_ctx, error) {
	epoch := a.Epoch(height)
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.inWindow(epoch) {
		return nil, pow.ErrEpochNotAdmitted
	}
	for i := range a.slot {
		if a.slot[i].set && a.slot[i].epoch == epoch {
			return a.slot[i].ctx, nil
		}
	}
	return a.create(height, epoch)
}

// Compute implements pow.Algorithm: (mix, final) for (header hash, height,
// nonce) through the reference's light path. A height the reference refuses
// yields zero hashes, which no target or mix digest matches.
func (a *Algorithm) Compute(headerHash common.Hash, height uint64, nonce uint64) (common.Hash, common.Hash) {
	ctx, err := a.context(height)
	if err != nil {
		return common.Hash{}, common.Hash{}
	}
	var mix, out common.Hash
	st := C.ignpow_hash(ctx, C.uint64_t(height), (*C.uint8_t)(unsafe.Pointer(&headerHash[0])), C.uint64_t(nonce),
		(*C.uint8_t)(unsafe.Pointer(&mix[0])), (*C.uint8_t)(unsafe.Pointer(&out[0])))
	if st != C.IGNPOW_OK {
		return common.Hash{}, common.Hash{}
	}
	return mix, out
}

// Verify implements pow.Algorithm with the chain's target semantics
// (floor((2^256 - 1) / difficulty), the final hash read big-endian, inclusive):
// the same comparison the reference's own verify makes against a boundary.
func (a *Algorithm) Verify(headerHash common.Hash, height uint64, nonce uint64, mixDigest common.Hash, difficulty *big.Int, fullDataset bool) error {
	if pow.Target(difficulty) == nil {
		return pow.ErrInvalidDifficulty
	}
	if !a.admits(height) {
		return pow.ErrEpochNotAdmitted
	}
	return pow.Verify(a, headerHash, height, nonce, mixDigest, difficulty)
}

// Vectors implements pow.Algorithm. The controls' exactness truth is the
// reference's own vector corpus (pow/vectors/<id>/, checked by this package's
// tests through the adapter) and its embedded self-test (SelfTest); the node
// embeds none of its own for them.
func (a *Algorithm) Vectors() []pow.Vector { return nil }

// CacheBytes is the light cache size of the epoch containing height, as the
// reference reports it (0 for a table-based candidate).
func (a *Algorithm) CacheBytes(height uint64) uint64 {
	cid := C.CString(a.id)
	defer C.free(unsafe.Pointer(cid))
	var st C.int
	return uint64(C.ignpow_cache_bytes(cid, nil, C.uint64_t(height), &st))
}

// Close frees the contexts held.
func (a *Algorithm) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.slot {
		if a.slot[i].set {
			C.ignpow_ctx_free(a.slot[i].ctx)
			a.slot[i] = ctxSlot{}
		}
	}
}
