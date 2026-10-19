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
#include "igneumhash.h"
*/
import "C"

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/igneumhash"
	"github.com/ethereum/go-ethereum/log"
)

// The IgneumHash documents the Go WORK does not carry (version 8, the
// blended relation, SPEC-R0A-v0.md section 0.8) are served by the hash
// lead's C reference in this lab build: the registry's "igneumhash" name
// stays (a schedule's rows are one algorithm), the document is the
// reference's, the parameter digest is the reference's pid
// (ignh_params_hash), and every hash and verification runs in libignpow on
// a light context per epoch. The transition proof of the gate's form on the
// second development chain, 11 October 2026; development only.
func init() {
	igneumhash.RegisterReference(newIgneumhashRef)
	// F-219: the raster form's buffer pool is bounded by the node's
	// parallel verification width (pow.SetVerifyWidth, the engine's
	// VerifyWorkers at its construction); until the engine sets it the
	// reference bounds by the hardware's concurrency.
	pow.OnVerifyWidth(SetParallelWidth)
}

// SetParallelWidth tells the reference the node's parallel verification
// width: the bound of the raster form's buffer pool per context (F-219, 16
// October 2026; the buffers were per OS thread and never freed). A width
// under one is the reference's own default (the hardware's concurrency).
func SetParallelWidth(n int) {
	if n < 0 {
		n = 0
	}
	C.ignpow_set_parallel_width(C.uint32_t(n))
}

// setRasterOracle switches the raster form onto its oracle paths (the
// normative calloc'd target for the unkeyed form, the sparse overlay for
// the keyed): the same bytes by construction, slower and allocated per
// attempt. The differential tests' switch; never a node's setting.
func setRasterOracle(on bool) {
	v := C.int(0)
	if on {
		v = 1
	}
	C.ignpow_set_raster_oracle(v)
}

// errSharedRefused: the reference refused a shared context over the donor
// (a document of another base, or a donor state the ABI does not accept).
var errSharedRefused = errors.New("controls: igneumhash reference: no shared context over the donor")

// errContextEvicted: the epoch's context was evicted between its creation
// and the hash (a sweep of epochs wider than the slot table).
var errContextEvicted = errors.New("controls: igneumhash reference: the epoch's context was evicted before the hash")

type ihRef struct {
	chainID uint64
	doc     []byte         // the document as given (the reference parses it)
	fields  map[string]any // the document's members, served to workers verbatim
	pid     common.Hash    // ignh_params_hash over the canonical bytes

	// mu guards the slot table: a context's creation, eviction and free
	// under the write lock; every hash over a context under the read lock
	// (the reference hashes over a const context, so the CPU sealer's
	// threads run in parallel and a free waits for the hashes in flight).
	// The write lock on every hash made the sealer's N threads one thread
	// (the project's finding of 00:5x UK, 12 October 2026).
	mu   sync.RWMutex
	slot [EpochCacheEntries]ihSlot
	// full: contexts are built with the dataset (a sealing node's CPU
	// search at the worker's rate); false: the verifier's light contexts.
	full bool
	// single: one resident context at a time (the minimum node's resident
	// verifier form, the node team's 4 GB row of 13 October 2026: the
	// lookahead built the next epoch's dataset beside the head's and the
	// pod crossed its line). Under it the lookahead builds nothing; the
	// next epoch's context builds on demand at the boundary, after the
	// old one is evicted, so one dataset is ever held.
	single bool
	// budget: under the single form the row and its unkeyed twin share one
	// full context; the builder claims it and the other's is freed.
	budget interface{ Claim(interface{ FreeFull() }) func() }
	// headEpoch is the anchor's epoch (the chain head's), the one epoch
	// the single form holds resident; another epoch's height verifies on
	// a light context built beside it (the node team's eighth window, 13
	// October 2026: the verifier's head-minus-12 check referenced the
	// last epoch for twelve blocks after a boundary and the one-dataset
	// form rebuilt the dataset for every block, three minutes each).
	headEpoch   uint64
	headEpochOk bool
	// curEpoch and curSince: the head's epoch as freeStale last saw it and
	// the first head height seen in it (F-215's band counts from there).
	curEpoch   uint64
	curSince   uint64
	curSinceOk bool
	// The build-ahead of 6a877ae41 (the next epoch's full context built
	// under the single form with the current evicted first) is WITHDRAWN
	// (reliability's and the node team's fault of record on the e64 set,
	// 13 October 2026, 10:45 to 11:08Z: a 100-block lead on 64-block
	// epochs kept every head "within the lead", the current full was
	// evicted for a build the head could not use, the head's verify
	// rebuilt it, and the two builds alternated every 90 s, one block
	// per cycle). the project's rule: the head's own context is never evicted
	// while the head stays in its epoch, and the lead is bounded by the
	// epoch's length. Under the single form (one dataset ever held) a
	// build-ahead would need two, so the crossing's on-demand build
	// stands, served light meanwhile; a node holding two (the non-single
	// form) builds ahead by the lookahead below, its lead bounded by
	// half the epoch's length.
	// donor: a keyed document's adapter rides its unkeyed twin's contexts
	// (the hash team's ignpow_ctx_create_shared, main b9377a81463c, SPEC-
	// R0E-v0 section 5a: a document and its twin derive the same cache and
	// dataset bytes, the keyed hash refuses on the twin's context, so a
	// context with the keyed document over the twin's memory serves the
	// keyed path; one dataset for the pair). Every full-context use of
	// this adapter wraps the donor's context for the call and frees the
	// wrapper; this adapter builds no full context of its own.
	donor *ihRef
	// flights names the epochs whose context a build is making, under
	// bmu: ONE build in flight per epoch, shared by the on-demand path
	// (withContext, which waits for it) and the lookahead (ensureAhead,
	// which returns): the build runs outside mu, so the hashes and
	// imports of the epochs held go on, and the finished context is
	// installed under mu's write side. F-56 (12 October 2026): without the
	// single flight every search thread's first hash built its own 2 GiB
	// context at once and the cgroup killed the sealer.
	bmu     sync.Mutex
	flights map[uint64]*ihFlight
	// builds counts the context builds this adapter ran (the test's read).
	builds atomic.Int64
}

// ihFlight is one build in flight: closed when installed or failed.
type ihFlight struct {
	done chan struct{}
	err  error
}

// LookaheadBlocks is how far ahead of an epoch boundary Anchor starts the
// next epoch's context build in the background (the project's rule of 08:3x UK,
// 12 October 2026: three times the slowest admitted core's build at the
// 20 s clock; the fleet's slowest, the minimum node, builds in 10.8 min,
// so 100; pod a's six minutes gave 64). Two contexts are resident for
// these blocks. The ninth development chain's 1024 boundary
// cost every node its build on the critical path, under the lock every
// hash and every import's verification needs: 24 s on the fastest core,
// six minutes on the slowest, with the sealer's templates, the pool's
// getWork and the sync all waiting on it.
const LookaheadBlocks = 100

// SetFullDataset switches the reference between light contexts (the
// verifier) and full-dataset contexts (a node that seals on its CPU); the
// cached contexts of the other kind are dropped.
func (a *ihRef) SetFullDataset(full bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.full == full {
		return
	}
	a.full = full
	for i := range a.slot {
		if a.slot[i].set {
			C.ignpow_ctx_free(a.slot[i].ctx)
			a.slot[i] = ihSlot{}
		}
	}
}

type ihSlot struct {
	epoch uint64
	ctx   *C.ignpow_ctx
	set   bool
	full  bool   // the context carries the dataset (the light form otherwise)
	bytes uint64 // the dataset's (or the cache's) bytes as the reference reports them, for the free's line
}

// F-215 (16 October 2026, the research chain's nodes at 18 to 26 GiB and
// three killed by the kernel): the slot table held THREE epochs' full
// contexts per coordinate (the previous, the current and the lookahead's
// next, 4 GiB each on the bandwidth lane) and freed the previous only when
// a fourth was installed. The bound: at most FullContextsHeld full contexts
// resident per adapter (the current and the next), a third's install
// freeing the one farthest from it first; and the previous epoch's full
// context freed by Anchor once the head is StaleBand blocks past the
// boundary (the band the late blocks of the old epoch and the reorg window
// need; a height there verifies on a light context after). Every free is
// logged with the epoch and the bytes.
const (
	FullContextsHeld = 2
	StaleBand        = 64
)

func newIgneumhashRef(chainID uint64, document []byte) (pow.Algorithm, error) {
	var fields map[string]any
	if err := json.Unmarshal(document, &fields); err != nil {
		return nil, fmt.Errorf("document: %w", err)
	}
	cdoc := C.CString(string(document))
	defer C.free(unsafe.Pointer(cdoc))
	var p C.ignh_params
	if st := C.ignh_params_parse(cdoc, &p); st != C.IGNPOW_OK {
		return nil, fmt.Errorf("the reference refused the document: %s", statusName(int(st)))
	}
	var pid common.Hash
	C.ignh_params_hash(&p, (*C.uint8_t)(unsafe.Pointer(&pid[0])))
	// The reference must also accept it as a context at height 0 (a
	// schedule's validation builds every row's algorithm before any chain
	// carries it; a document the reference cannot run is refused here).
	calgo := C.CString(C.IGNPOW_ALGO_IGNEUMHASH)
	defer C.free(unsafe.Pointer(calgo))
	var st C.int
	if n := C.ignpow_epoch_of(calgo, cdoc, 0, &st); st != C.IGNPOW_OK {
		return nil, fmt.Errorf("the reference refused the document's schedule: %s (%d)", statusName(int(st)), uint64(n))
	}
	return &ihRef{chainID: chainID, doc: append([]byte(nil), document...), fields: fields, pid: pid}, nil
}

func (a *ihRef) Name() string { return igneumhash.Name }

// Epoch is the reference's schedule for the document (EPOCH_LENGTH).
func (a *ihRef) Epoch(height uint64) uint64 {
	calgo := C.CString(C.IGNPOW_ALGO_IGNEUMHASH)
	defer C.free(unsafe.Pointer(calgo))
	cdoc := C.CString(string(a.doc))
	defer C.free(unsafe.Pointer(cdoc))
	var st C.int
	n := C.ignpow_epoch_of(calgo, cdoc, C.uint64_t(height), &st)
	if st != C.IGNPOW_OK {
		return 0
	}
	return uint64(n)
}

// SeedHash is the epoch seed E the reference derives the dataset from
// (ignh_dataset_info on the epoch's context); zero when no context can be
// built.
func (a *ihRef) SeedHash(height uint64) common.Hash {
	var e, d common.Hash
	var ms C.uint64_t
	var has C.int
	st := C.int(C.IGNPOW_OK)
	if err := a.withContext(height, func(ctx *C.ignpow_ctx) {
		// E alone: the reference computes the cache and dataset DIGESTS
		// only for non-null output pointers, and a dataset digest is a
		// BLAKE3 over the whole resident dataset (4 GiB on version 15:
		// 18 to 23 s per template on vectorwork7's bandwidth node, 1 to
		// 6 s on the 2 GiB rows, the share lane stale on arrival), which
		// a template's seed hash never needs.
		st = C.ignh_dataset_info(ctx, (*C.uint8_t)(unsafe.Pointer(&e[0])), (*C.uint8_t)(unsafe.Pointer(&d[0])), nil, nil, &ms, &has)
	}); err != nil || st != C.IGNPOW_OK {
		return common.Hash{}
	}
	return e
}

// ParamsDigest is the reference's pid: the digest a worker keys its datasets
// and results by.
func (a *ihRef) ParamsDigest() common.Hash { return a.pid }

// WorkParams serves the document verbatim.
func (a *ihRef) WorkParams() any { return a.fields }

// Anchor is the engine's word of the chain head (every import's
// verification and every template): the lookahead. When the head is within
// LookaheadBlocks of the next epoch's first height and that epoch's
// context is not held, its build starts in the background, outside the
// lock, and is installed when done; the head's own epoch likewise when a
// node starts inside an epoch with no context yet. A build already running
// for the epoch is not started twice. The hash paths build on demand as
// before when the lookahead has not run (a test, a height far from the
// anchor).
func (a *ihRef) Anchor(height uint64) {
	cur := a.Epoch(height)
	a.mu.Lock()
	a.headEpoch, a.headEpochOk = cur, true
	single := a.single && a.full
	holder := a.donor == nil
	headFull := false
	for i := range a.slot {
		if a.slot[i].set && a.slot[i].full && a.slot[i].epoch == cur {
			headFull = true
		}
	}
	a.mu.Unlock()
	if !single {
		a.freeStale(height, cur)
	}
	if single {
		// The single form builds nothing eagerly at the anchor: a row
		// and its twin both anchored would both build the epoch (the
		// seventh window's two holders at the start); the path that
		// verifies builds on demand, under the pair's one budget. ONE
		// exception, the head's epoch's upgrade (the node team's 8 GB row
		// on c7595eb0e, 13 October 2026): a fast sync verifies the
		// epochs it passes on LIGHT contexts (built as other epochs'
		// while the head was behind), so the epoch that ends up the
		// head's holds a light context alone and every head verification
		// after the sync runs light (p99 2 ms alone, 103 ms with the
		// twin). The holder (the row without a donor) builds the head
		// epoch's FULL context in the background once the head is in
		// it, the other epochs' full contexts evicted first (buildOnce)
		// and the full replacing the light on install; one flight at a
		// time, so a sync crossing many epochs starts at most one build
		// per build's length and converges on the epoch it stops in.
		if holder && !headFull && !a.inFlight() {
			a.buildOnce(height, cur, false)
		}
		return
	}
	a.ensureAhead(height, cur)
	ahead := height + a.lead()
	if next := a.Epoch(ahead); next != cur {
		a.ensureAhead(ahead, next)
	}
}

// SetDonor makes this (keyed) adapter ride the donor's (its unkeyed
// twin's) contexts.
func (a *ihRef) SetDonor(d any) {
	if d == nil {
		a.mu.Lock()
		a.donor = nil
		a.mu.Unlock()
		return
	}
	t, ok := d.(*ihRef)
	if !ok {
		if u, has := d.(interface{ Adapter() any }); has {
			t, ok = u.Adapter().(*ihRef)
		}
	}
	if ok && t != a {
		a.mu.Lock()
		a.donor = t
		a.mu.Unlock()
	}
}

// withSharedContext runs fn on a context of this adapter's document over
// the donor's context for the height's epoch (built in the donor on
// demand), the wrapper freed after the call; the donor's read lock holds
// its memory in place for the call's length.
func (a *ihRef) withSharedContext(height uint64, fn func(ctx *C.ignpow_ctx)) error {
	d := a.donor
	calgo := C.CString(C.IGNPOW_ALGO_IGNEUMHASH)
	defer C.free(unsafe.Pointer(calgo))
	cdoc := C.CString(string(a.doc))
	defer C.free(unsafe.Pointer(cdoc))
	var inner error
	err := d.withContext(height, func(donorCtx *C.ignpow_ctx) {
		var st C.int
		shared := C.ignpow_ctx_create_shared(calgo, cdoc, C.uint64_t(height), donorCtx, &st)
		if shared == nil || st != C.IGNPOW_OK {
			inner = fmt.Errorf("%w at height %d: %s", errSharedRefused, height, statusName(int(st)))
			return
		}
		fn(shared)
		C.ignpow_ctx_free(shared)
	})
	if err != nil {
		return err
	}
	return inner
}

// SetSingleBudget shares one full context between this adapter and its twin.
func (a *ihRef) SetSingleBudget(b interface{ Claim(interface{ FreeFull() }) func() }) {
	a.mu.Lock()
	a.budget = b
	a.mu.Unlock()
}

// FreeFull drops every held context (the twin claimed the single budget).
func (a *ihRef) FreeFull() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.slot {
		if a.slot[i].set {
			C.ignpow_ctx_free(a.slot[i].ctx)
			a.slot[i] = ihSlot{}
		}
	}
}

// SetSingleDataset makes the adapter hold one resident context at a time
// (no lookahead build; the boundary's build evicts the old epoch first).
func (a *ihRef) SetSingleDataset(on bool) {
	a.mu.Lock()
	a.single = on
	a.mu.Unlock()
}

// ensureAhead starts the epoch's context build in the background unless
// the context is held or its build is in flight.
func (a *ihRef) ensureAhead(height, epoch uint64) {
	a.mu.RLock()
	held := a.lookup(epoch) != nil
	a.mu.RUnlock()
	if held {
		return
	}
	a.buildOnce(height, epoch, false)
}

// lead is the lookahead's lead in blocks: LookaheadBlocks, bounded by
// half the document's epoch length where the document names one
// (epoch_blocks), so a short epoch never keeps every head "within the
// lead" (the e64 set's fault of record).
func (a *ihRef) lead() uint64 {
	lead := uint64(LookaheadBlocks)
	if v, ok := a.fields["epoch_blocks"]; ok {
		if f, ok := v.(float64); ok && f >= 2 && uint64(f)/2 < lead {
			lead = uint64(f) / 2
		}
	}
	return lead
}

// inFlight reports whether any epoch's context build is in flight on this
// adapter.
func (a *ihRef) inFlight() bool {
	a.bmu.Lock()
	defer a.bmu.Unlock()
	return len(a.flights) > 0
}

// buildOnce is the single flight: the first caller for an epoch builds its
// context and installs it; a later caller joins the flight, waiting for it
// (wait true: the on-demand path, which needs the context now) or
// returning at once (wait false: the lookahead). The flight's error is the
// build's. One context build in flight per epoch per adapter, whatever
// the thread count.
func (a *ihRef) buildOnce(height, epoch uint64, wait bool) error {
	a.bmu.Lock()
	if a.flights == nil {
		a.flights = make(map[uint64]*ihFlight)
	}
	if f := a.flights[epoch]; f != nil {
		a.bmu.Unlock()
		if !wait {
			return nil
		}
		<-f.done
		return f.err
	}
	// F-222 (16 October 2026): the caller's lookup and this flight check
	// are two steps; a flight that installed its context and retired
	// between them leaves the epoch held, and a second build of a held
	// epoch was started here (3 builds where 2 were wanted under thread
	// load, a 4 GiB set built twice on a sealing node's restart). The
	// epoch is read again under the flight lock: held in the form wanted,
	// nothing is built. The form wanted is the build's own (the security review's
	// read of the first cut): full where the adapter is full, except a
	// non-head epoch under the single form, which the build makes LIGHT
	// (the lookahead's band, a late block of the last epoch), so a held
	// light context of such an epoch is the form wanted and no second
	// light build starts.
	a.mu.RLock()
	wantFull := a.full && !(a.single && a.headEpochOk && epoch != a.headEpoch)
	held := false
	for i := range a.slot {
		if a.slot[i].set && a.slot[i].epoch == epoch && (a.slot[i].full || !wantFull) {
			held = true
		}
	}
	a.mu.RUnlock()
	if held {
		a.bmu.Unlock()
		return nil
	}
	f := &ihFlight{done: make(chan struct{})}
	a.flights[epoch] = f
	a.bmu.Unlock()
	run := func() {
		a.mu.RLock()
		full, single, budget := a.full, a.single, a.budget
		a.mu.RUnlock()
		if full && single && budget != nil {
			// One full context across the row and its twin: the claim
			// is held across the build and its install, the other
			// holder's contexts go first, a second flight waits.
			release := budget.Claim(a)
			defer release()
		}
		// The single form's one dataset is the HEAD's epoch's; another
		// epoch's height (the boundary band's head-minus-12 check, a
		// late block of the last epoch) builds a LIGHT context beside it
		// and evicts nothing. The head epoch's full build evicts the other
		// epochs' FULL contexts alone; the light ones stay.
		buildFull := full
		if full && single {
			a.mu.Lock()
			if a.headEpochOk && epoch != a.headEpoch {
				buildFull = false
			} else {
				for i := range a.slot {
					if a.slot[i].set && a.slot[i].full && a.slot[i].epoch != epoch {
						C.ignpow_ctx_free(a.slot[i].ctx)
						a.slot[i] = ihSlot{}
					}
				}
			}
			a.mu.Unlock()
		}
		ctx, err := a.buildAs(height, buildFull)
		if err == nil {
			var st C.int
			calgo := C.CString(C.IGNPOW_ALGO_IGNEUMHASH)
			cdoc := C.CString(string(a.doc))
			bytes := uint64(C.ignpow_dataset_bytes(calgo, cdoc, C.uint64_t(height), &st))
			if !buildFull {
				bytes = uint64(C.ignpow_cache_bytes(calgo, cdoc, C.uint64_t(height), &st))
			}
			C.free(unsafe.Pointer(cdoc))
			C.free(unsafe.Pointer(calgo))
			log.Info("Reference context built", "epoch", epoch, "height", height, "full", buildFull, "single", single, "bytes", bytes, "wait", wait, "holder", fmt.Sprintf("%p", a))
			a.mu.Lock()
			a.installAs(epoch, ctx, buildFull, bytes)
			a.mu.Unlock()
		}
		f.err = err
		a.bmu.Lock()
		delete(a.flights, epoch)
		a.bmu.Unlock()
		close(f.done)
	}
	if wait {
		run()
		return f.err
	}
	go run()
	return nil
}

// build makes the context for the height's epoch in the reference (the
// dataset's generation for a full context); it takes no lock, so a build
// never stands between the hashes and the contexts they hold.
func (a *ihRef) build(height uint64) (*C.ignpow_ctx, error) {
	return a.buildAs(height, a.full)
}

// buildAs is build with the context's kind given (the single form builds
// a non-head epoch light).
func (a *ihRef) buildAs(height uint64, wantFull bool) (*C.ignpow_ctx, error) {
	calgo := C.CString(C.IGNPOW_ALGO_IGNEUMHASH)
	defer C.free(unsafe.Pointer(calgo))
	cdoc := C.CString(string(a.doc))
	defer C.free(unsafe.Pointer(cdoc))
	var st C.int
	full := C.int(0)
	if wantFull {
		full = 1
	}
	a.builds.Add(1)
	ctx := C.ignpow_ctx_create(calgo, cdoc, C.uint64_t(height), full, &st)
	if ctx == nil || st != C.IGNPOW_OK {
		return nil, fmt.Errorf("controls: igneumhash reference: no context (full %d) at height %d: %s", full, height, statusName(int(st)))
	}
	return ctx, nil
}

// install puts a built context into the slot table (the caller holds the
// write lock); a context the table holds for the epoch already wins and the
// new one is freed; else the epoch farthest from this one is evicted.
func (a *ihRef) install(epoch uint64, ctx *C.ignpow_ctx) { a.installAs(epoch, ctx, a.full, 0) }

// installAs is install with the context's kind and bytes recorded on the
// slot. A full context's install first holds the bound of F-215: when
// FullContextsHeld full contexts of other epochs are resident, the one
// farthest from the new epoch is freed before the new one is placed.
func (a *ihRef) installAs(epoch uint64, ctx *C.ignpow_ctx, full bool, bytes uint64) {
	for i := range a.slot {
		if a.slot[i].set && a.slot[i].epoch == epoch {
			if full && !a.slot[i].full {
				// The head epoch's upgrade: the full context replaces
				// the light one built for the epoch during a sync.
				C.ignpow_ctx_free(a.slot[i].ctx)
				a.slot[i] = ihSlot{epoch: epoch, ctx: ctx, set: true, full: true, bytes: bytes}
				a.boundFull(epoch)
				return
			}
			C.ignpow_ctx_free(ctx)
			return
		}
	}
	if full {
		a.boundFull(epoch)
	}
	slot := -1
	for i := range a.slot {
		if !a.slot[i].set {
			slot = i
			break
		}
	}
	if slot < 0 {
		// Evict the epoch farthest from this one.
		worst := int64(-1)
		for i := range a.slot {
			d := int64(a.slot[i].epoch) - int64(epoch)
			if d < 0 {
				d = -d
			}
			if d > worst {
				slot, worst = i, d
			}
		}
		log.Info("Reference context freed", "why", "slots", "epoch", a.slot[slot].epoch, "full", a.slot[slot].full, "bytes", a.slot[slot].bytes, "for", epoch, "holder", fmt.Sprintf("%p", a))
		C.ignpow_ctx_free(a.slot[slot].ctx)
	}
	a.slot[slot] = ihSlot{epoch: epoch, ctx: ctx, set: true, full: full, bytes: bytes}
}

// boundFull keeps at most FullContextsHeld full contexts resident once the
// full context for epoch is counted: while more full contexts of OTHER
// epochs are held than the bound leaves room for, the one farthest from
// epoch is freed (the caller holds the write lock).
func (a *ihRef) boundFull(epoch uint64) {
	for {
		others, worst, far := 0, -1, int64(-1)
		for i := range a.slot {
			if !a.slot[i].set || !a.slot[i].full || a.slot[i].epoch == epoch {
				continue
			}
			others++
			d := int64(a.slot[i].epoch) - int64(epoch)
			if d < 0 {
				d = -d
			}
			if d > far {
				worst, far = i, d
			}
		}
		if others < FullContextsHeld || worst < 0 {
			return
		}
		log.Info("Reference context freed", "why", "bound", "epoch", a.slot[worst].epoch, "bytes", a.slot[worst].bytes, "for", epoch, "held", FullContextsHeld, "holder", fmt.Sprintf("%p", a))
		C.ignpow_ctx_free(a.slot[worst].ctx)
		a.slot[worst] = ihSlot{}
	}
}

// freeStale frees the full contexts of epochs before the head's once the
// head is StaleBand blocks past the first height Anchor saw in the head's
// epoch (the boundary, on a node anchored every block; a start inside an
// epoch counts from its first head, and holds nothing older anyway). The
// non-single form alone (the single form evicts at its own build). Light
// contexts stay: they are the band's verification path and a cache is
// small.
func (a *ihRef) freeStale(height, cur uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.curSinceOk || a.curEpoch != cur {
		a.curEpoch, a.curSince, a.curSinceOk = cur, height, true
	}
	if height < a.curSince+StaleBand {
		return
	}
	for i := range a.slot {
		s := &a.slot[i]
		if s.set && s.full && s.epoch < cur {
			log.Info("Reference context freed", "why", "stale", "epoch", s.epoch, "bytes", s.bytes, "head", height, "headEpoch", cur, "holder", fmt.Sprintf("%p", a))
			C.ignpow_ctx_free(s.ctx)
			a.slot[i] = ihSlot{}
		}
	}
}

// lookup is the slot table's context for the epoch, nil when none; the
// caller holds mu (either side).
func (a *ihRef) lookup(epoch uint64) *C.ignpow_ctx {
	for i := range a.slot {
		if a.slot[i].set && a.slot[i].epoch == epoch {
			return a.slot[i].ctx
		}
	}
	return nil
}

// withContext runs fn over the epoch's context under the read lock, so
// hashes run in parallel and no eviction frees the context under fn. A
// missing context is built first, OUTSIDE the lock (the hashes of the
// epochs held go on meanwhile) by the epoch's single flight (buildOnce):
// the first caller builds and installs, the others wait for that one
// build, so N search threads at their first hash cost one context, not N.
func (a *ihRef) withContext(height uint64, fn func(ctx *C.ignpow_ctx)) error {
	a.mu.RLock()
	d, full := a.donor, a.full
	a.mu.RUnlock()
	if d != nil && full {
		// The document rides the donor's dataset (one for the pair, or
		// for every coordinate over one base). A donor over ANOTHER base
		// (vectorwork6, 13 October 2026: the version-15 bandwidth node's
		// 4 GiB dataset is not the 2 GiB documents' E, and the shared
		// context is refused with E_PARAMS, so the node refused every
		// tensor and fabric block, "the algorithm refused the triple",
		// and stood at height 1 while the chain ran) is dropped once and
		// the document holds its own context from then on.
		err := a.withSharedContext(height, fn)
		if err == nil || !errors.Is(err, errSharedRefused) {
			return err
		}
		a.dropDonor(height, err.Error())
	}
	epoch := a.Epoch(height)
	a.mu.RLock()
	ctx := a.lookup(epoch)
	if ctx == nil {
		a.mu.RUnlock()
		if err := a.buildOnce(height, epoch, true); err != nil {
			return err
		}
		a.mu.RLock()
		if ctx = a.lookup(epoch); ctx == nil {
			// Evicted between the two locks (a sweep of epochs); the
			// caller's retry rebuilds it.
			a.mu.RUnlock()
			return errContextEvicted
		}
	}
	fn(ctx)
	a.mu.RUnlock()
	return nil
}

// dropDonor makes the adapter hold its own context from now on (the donor
// was another base: the shared context was refused, or built and then
// refused the hash). Reports whether a donor was set.
func (a *ihRef) dropDonor(height uint64, why string) bool {
	a.mu.Lock()
	had := a.donor != nil
	a.donor = nil
	a.mu.Unlock()
	if had {
		log.Warn("Reference context: the donor's dataset is another base; this document holds its own context", "height", height, "why", why)
	}
	return had
}

// Compute implements pow.Algorithm through the reference. A hash the
// reference refuses on a shared context (vectorwork6's bandwidth node,
// 13 October 2026: the version-15 donor built a shared context for the
// version-14 and -13 rows and then refused every hash on it, so the node
// refused every tensor and fabric block as "no hash") drops the donor and
// runs once more on the row's own context.
func (a *ihRef) Compute(headerHash common.Hash, height uint64, nonce uint64) (common.Hash, common.Hash) {
	for attempt := 0; attempt < 2; attempt++ {
		var mix, out common.Hash
		st := C.int(C.IGNPOW_OK)
		err := a.withContext(height, func(ctx *C.ignpow_ctx) {
			st = C.ignpow_hash(ctx, C.uint64_t(height), (*C.uint8_t)(unsafe.Pointer(&headerHash[0])), C.uint64_t(nonce),
				(*C.uint8_t)(unsafe.Pointer(&mix[0])), (*C.uint8_t)(unsafe.Pointer(&out[0])))
		})
		if err == nil && st == C.IGNPOW_OK {
			return mix, out
		}
		why := statusName(int(st))
		if err != nil {
			why = err.Error()
		}
		if !a.dropDonor(height, why) {
			log.Warn("Reference hash refused", "height", height, "why", why)
			return common.Hash{}, common.Hash{}
		}
	}
	return common.Hash{}, common.Hash{}
}

// KeyedRounds implements pow.Keyed: S from the document's keyed_rounds
// (the specification's section 0.9, bit 10), zero for an unkeyed document.
func (a *ihRef) KeyedRounds(height uint64) uint64 {
	v, ok := a.fields["keyed_rounds"]
	if !ok {
		return 0
	}
	f, ok := v.(float64)
	if !ok || f <= 0 {
		return 0
	}
	return uint64(f)
}

// ComputeKeyed implements pow.Keyed through the reference's ignh_hash_keyed:
// with sk the prover's hash (the shares written), without sk the verifier's
// replay from the shares given (their length S x 32).
func (a *ihRef) ComputeKeyed(headerHash common.Hash, height uint64, nonce uint64, sk *[32]byte, shares []byte) (common.Hash, common.Hash, []byte) {
	s := a.KeyedRounds(height)
	if s == 0 {
		return common.Hash{}, common.Hash{}, nil
	}
	buf := make([]byte, s*pow.ShareBytes)
	var skp *C.uint8_t
	if sk != nil {
		skp = (*C.uint8_t)(unsafe.Pointer(&sk[0]))
	} else {
		if uint64(len(shares)) != s*pow.ShareBytes {
			return common.Hash{}, common.Hash{}, nil
		}
		copy(buf, shares)
	}
	for attempt := 0; attempt < 2; attempt++ {
		var mix, out common.Hash
		st := C.int(C.IGNPOW_OK)
		err := a.withContext(height, func(ctx *C.ignpow_ctx) {
			st = C.ignh_hash_keyed(ctx, C.uint64_t(height), (*C.uint8_t)(unsafe.Pointer(&headerHash[0])), C.uint64_t(nonce),
				skp, (*C.uint8_t)(unsafe.Pointer(&buf[0])), (*C.uint8_t)(unsafe.Pointer(&mix[0])), (*C.uint8_t)(unsafe.Pointer(&out[0])))
		})
		if err == nil && st == C.IGNPOW_OK {
			return mix, out, buf
		}
		why := statusName(int(st))
		if err != nil {
			why = err.Error()
		}
		if !a.dropDonor(height, why) {
			log.Warn("Reference keyed hash refused", "height", height, "why", why)
			return common.Hash{}, common.Hash{}, nil
		}
	}
	return common.Hash{}, common.Hash{}, nil
}

// ComputeKeyedM implements pow.KeyedM through the reference's
// ignh_hash_keyed_m: ComputeKeyed with the proof pi's messages m_0 .. m_(S-1)
// (S x 32 bytes) written on the prover's call and on the replay alike (the
// hash lead's entry of 11 October 2026).
func (a *ihRef) ComputeKeyedM(headerHash common.Hash, height uint64, nonce uint64, sk *[32]byte, shares []byte) (common.Hash, common.Hash, []byte, []byte) {
	s := a.KeyedRounds(height)
	if s == 0 {
		return common.Hash{}, common.Hash{}, nil, nil
	}
	buf := make([]byte, s*pow.ShareBytes)
	m := make([]byte, s*32)
	var skp *C.uint8_t
	if sk != nil {
		skp = (*C.uint8_t)(unsafe.Pointer(&sk[0]))
	} else {
		if uint64(len(shares)) != s*pow.ShareBytes {
			return common.Hash{}, common.Hash{}, nil, nil
		}
		copy(buf, shares)
	}
	var mix, out common.Hash
	st := C.int(C.IGNPOW_OK)
	if err := a.withContext(height, func(ctx *C.ignpow_ctx) {
		st = C.ignh_hash_keyed_m(ctx, C.uint64_t(height), (*C.uint8_t)(unsafe.Pointer(&headerHash[0])), C.uint64_t(nonce),
			skp, (*C.uint8_t)(unsafe.Pointer(&buf[0])), (*C.uint8_t)(unsafe.Pointer(&mix[0])), (*C.uint8_t)(unsafe.Pointer(&out[0])), (*C.uint8_t)(unsafe.Pointer(&m[0])))
	}); err != nil || st != C.IGNPOW_OK {
		return common.Hash{}, common.Hash{}, nil, nil
	}
	return mix, out, buf, m
}

// Verify implements pow.Algorithm: the shared body over the reference.
func (a *ihRef) Verify(headerHash common.Hash, height uint64, nonce uint64, mixDigest common.Hash, difficulty *big.Int, fullDataset bool) error {
	if pow.Target(difficulty) == nil {
		return pow.ErrInvalidDifficulty
	}
	return pow.Verify(a, headerHash, height, nonce, mixDigest, difficulty)
}

func (a *ihRef) Vectors() []pow.Vector { return nil }

// Close frees the reference's contexts.
func (a *ihRef) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.slot {
		if a.slot[i].set {
			C.ignpow_ctx_free(a.slot[i].ctx)
			a.slot[i].set = false
		}
	}
}
