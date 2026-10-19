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

package rxe1

/*
#cgo CFLAGS: -I${SRCDIR}/../../../../../pow/ref/include
#cgo LDFLAGS: -L${SRCDIR}/../../../../../build/ref -lignpow -lrvn_ethash -lblake3 -lstdc++ -lm
#include <stdlib.h>
#include "rxe1.h"
*/
import "C"

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sync"
	"unsafe"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"lukechampine.com/blake3"
)

// Name is the algorithm id, the reference's.
const Name = "rxe1"

// The epoch-seed domain tag of the RXE1 namespace on the chain: E =
// BLAKE3(u8(len) || tag || u64be(chain id) || u32be(0) || pid || u64be(epoch)),
// the R0A envelope's shape with the RXE1 parameter commitment (pid) as P and
// a zero version word (the schema lives in the pid). This replaces the
// reference harness's H("RXE1/harness-epoch", [LE64(chain id), LE64(epoch)]),
// which is a harness convention and not consensus (REFERENCE-NOTES.md
// section 3); the two differ by construction, and the chain's E is what
// igneum_getWork's seedHash carries.
const tagEpoch = "Igneum/RXE1/epoch"

// The same admission bounds as the IgneumHash adapter's, by name.
const (
	EpochCacheEntries     = 3
	EpochAdmissionWindow  = 1
	CacheBuildConcurrency = 1
	PrebuildBlocks        = 256
)

var errParams = errors.New("rxe1: parameter document rejected by the reference")

func init() {
	pow.RegisterDevOnly(Name, func(params []byte) (pow.Algorithm, error) { return New(params) })
}

// Algorithm is RXE1 behind pow.Algorithm.
type Algorithm struct {
	doc     []byte // the canonical document (re-encoded)
	p       C.rxe1_params
	pid     common.Hash // the parameter commitment
	chainID uint64
	epochs  uint64 // epoch_blocks
	fields  map[string]any

	mu       sync.Mutex
	slot     [EpochCacheEntries]ctxSlot
	anchored bool
	anchor   uint64
	prebuild bool
	builds   uint64
}

type ctxSlot struct {
	epoch uint64
	seed  [32]byte
	ctx   *C.rxe1_ctx
	set   bool
}

// New builds the adapter from a parameter document (the reference's JSON
// keys: schema, mode, blocks, rounds, registers, dataset_bytes, and
// optionally parents, cache_bytes, chain_id, epoch_blocks, test_only).
func New(params []byte) (*Algorithm, error) {
	if len(params) == 0 {
		return nil, fmt.Errorf("%w: a document is required", errParams)
	}
	var fields map[string]any
	if err := json.Unmarshal(params, &fields); err != nil {
		return nil, fmt.Errorf("%w: %v", errParams, err)
	}
	cs := C.CString(string(params))
	defer C.free(unsafe.Pointer(cs))
	a := &Algorithm{fields: fields}
	if st := C.rxe1_params_parse(cs, &a.p); st != C.IGNPOW_OK {
		return nil, fmt.Errorf("%w: status %d", errParams, int(st))
	}
	var pid [32]byte
	C.rxe1_pid(&a.p, (*C.uint8_t)(unsafe.Pointer(&pid[0])))
	a.pid = common.Hash(pid)
	a.chainID = uint64(a.p.chain_id)
	a.epochs = uint64(a.p.epoch_blocks)
	if a.epochs == 0 {
		return nil, fmt.Errorf("%w: epoch_blocks is zero", errParams)
	}
	a.doc = append([]byte(nil), params...)
	return a, nil
}

// Name implements pow.Algorithm.
func (a *Algorithm) Name() string { return Name }

// Epoch implements pow.Algorithm: floor(height / epoch_blocks).
func (a *Algorithm) Epoch(height uint64) uint64 { return height / a.epochs }

// epochSeed is the chain's E for an epoch (the namespace rule above).
func (a *Algorithm) epochSeed(epoch uint64) [32]byte {
	buf := make([]byte, 0, 1+len(tagEpoch)+8+4+32+8)
	buf = append(buf, byte(len(tagEpoch)))
	buf = append(buf, tagEpoch...)
	buf = binary.BigEndian.AppendUint64(buf, a.chainID)
	buf = binary.BigEndian.AppendUint32(buf, 0)
	buf = append(buf, a.pid[:]...)
	buf = binary.BigEndian.AppendUint64(buf, epoch)
	return blake3.Sum256(buf)
}

// SeedHash implements pow.Algorithm: the chain's epoch seed E, what a worker
// derives the dataset from through the pinned R0A v1 builder.
func (a *Algorithm) SeedHash(height uint64) common.Hash {
	return common.Hash(a.epochSeed(a.Epoch(height)))
}

// ParamsDigest is the parameter commitment pid: the job tag.
func (a *Algorithm) ParamsDigest() common.Hash { return a.pid }

// WorkParams is the document igneum_getWork carries, verbatim.
func (a *Algorithm) WorkParams() any { return a.fields }

// Anchor, Builds, inWindow, held, admits, create and context: the same
// shape as the controls' adapters (F-18).
func (a *Algorithm) Anchor(height uint64) {
	epoch := a.Epoch(height)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.anchored, a.anchor = true, epoch
	for i := range a.slot {
		if a.slot[i].set && !a.inWindow(a.slot[i].epoch) {
			C.rxe1_ctx_free(a.slot[i].ctx)
			a.slot[i] = ctxSlot{}
		}
	}
	if a.prebuild {
		return
	}
	if height%a.epochs >= a.epochs-PrebuildBlocks && !a.held(epoch+1) {
		a.prebuild = true
		go func() {
			a.mu.Lock()
			defer a.mu.Unlock()
			a.prebuild = false
			if a.inWindow(epoch+1) && !a.held(epoch+1) {
				a.create(epoch + 1)
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

func (a *Algorithm) admits(height uint64) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.inWindow(a.Epoch(height))
}

func (a *Algorithm) create(epoch uint64) (*ctxSlot, error) {
	seed := a.epochSeed(epoch)
	var st C.int
	ctx := C.rxe1_ctx_create(&a.p, (*C.uint8_t)(unsafe.Pointer(&seed[0])), 0, &st)
	if ctx == nil || st != C.IGNPOW_OK {
		return nil, fmt.Errorf("rxe1: no light context for epoch %d: status %d", epoch, int(st))
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
		C.rxe1_ctx_free(a.slot[slot].ctx)
	}
	a.slot[slot] = ctxSlot{epoch: epoch, seed: seed, ctx: ctx, set: true}
	return &a.slot[slot], nil
}

func (a *Algorithm) context(height uint64) (*ctxSlot, error) {
	epoch := a.Epoch(height)
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.inWindow(epoch) {
		return nil, pow.ErrEpochNotAdmitted
	}
	for i := range a.slot {
		if a.slot[i].set && a.slot[i].epoch == epoch {
			return &a.slot[i], nil
		}
	}
	return a.create(epoch)
}

// Compute implements pow.Algorithm through the reference's harness-side
// evaluation with the chain's inputs: chain_id_bytes = LE64(chain id) (the
// reference's reading), the 32-byte sealing hash as the header blob, the
// chain's epoch number and seed, the nonce. mix = the seed (what a wrong
// seed is caught by before the loop), result = out.
func (a *Algorithm) Compute(headerHash common.Hash, height uint64, nonce uint64) (common.Hash, common.Hash) {
	slot, err := a.context(height)
	if err != nil {
		return common.Hash{}, common.Hash{}
	}
	var cid [8]byte
	binary.LittleEndian.PutUint64(cid[:], a.chainID)
	var out, seed common.Hash
	st := C.rxe1_eval(slot.ctx, (*C.uint8_t)(unsafe.Pointer(&cid[0])), 8, (*C.uint8_t)(unsafe.Pointer(&headerHash[0])), 32,
		C.uint64_t(a.Epoch(height)), C.uint64_t(nonce), (*C.uint8_t)(unsafe.Pointer(&out[0])), (*C.uint8_t)(unsafe.Pointer(&seed[0])), nil)
	if st != C.IGNPOW_OK {
		return common.Hash{}, common.Hash{}
	}
	return seed, out
}

// Verify implements pow.Algorithm with the chain's target semantics: the
// final hash read big-endian, inclusive against floor((2^256 - 1) /
// difficulty), never the harness's little-endian reading (section 19's
// reconciliation, made explicit here and nowhere else).
func (a *Algorithm) Verify(headerHash common.Hash, height uint64, nonce uint64, mixDigest common.Hash, difficulty *big.Int, fullDataset bool) error {
	if pow.Target(difficulty) == nil {
		return pow.ErrInvalidDifficulty
	}
	if !a.admits(height) {
		return pow.ErrEpochNotAdmitted
	}
	return pow.Verify(a, headerHash, height, nonce, mixDigest, difficulty)
}

// Vectors implements pow.Algorithm: none embedded; the reference's spec-form
// vectors carry explicit epoch seeds and are checked through evalWithSeed in
// this package's tests.
func (a *Algorithm) Vectors() []pow.Vector { return nil }

// evalWithSeed evaluates with an explicit epoch seed (the harness's inputs),
// for the corpus test only: it proves the plumbing against the reference's
// own vectors, which carry the harness epoch seed, not the chain's.
func (a *Algorithm) evalWithSeed(seed [32]byte, headerHash common.Hash, epoch uint64, nonce uint64) (common.Hash, common.Hash, error) {
	var st C.int
	ctx := C.rxe1_ctx_create(&a.p, (*C.uint8_t)(unsafe.Pointer(&seed[0])), 0, &st)
	if ctx == nil || st != C.IGNPOW_OK {
		return common.Hash{}, common.Hash{}, fmt.Errorf("rxe1: context: status %d", int(st))
	}
	defer C.rxe1_ctx_free(ctx)
	var cid [8]byte
	binary.LittleEndian.PutUint64(cid[:], a.chainID)
	var out, s common.Hash
	st = C.rxe1_eval(ctx, (*C.uint8_t)(unsafe.Pointer(&cid[0])), 8, (*C.uint8_t)(unsafe.Pointer(&headerHash[0])), 32,
		C.uint64_t(epoch), C.uint64_t(nonce), (*C.uint8_t)(unsafe.Pointer(&out[0])), (*C.uint8_t)(unsafe.Pointer(&s[0])), nil)
	if st != C.IGNPOW_OK {
		return common.Hash{}, common.Hash{}, fmt.Errorf("rxe1: eval: status %d", int(st))
	}
	return s, out, nil
}

// harnessEpochSeed is the reference harness's rule, for the corpus test.
func harnessEpochSeed(chainID, epoch uint64) [32]byte {
	var out [32]byte
	C.rxe1_harness_epoch_seed(C.uint64_t(chainID), C.uint64_t(epoch), (*C.uint8_t)(unsafe.Pointer(&out[0])))
	return out
}

// Close frees the contexts held.
func (a *Algorithm) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.slot {
		if a.slot[i].set {
			C.rxe1_ctx_free(a.slot[i].ctx)
			a.slot[i] = ctxSlot{}
		}
	}
}
