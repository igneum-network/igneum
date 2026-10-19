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

package igneum

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"math/rand"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/core/types"
)

const (
	// staleThreshold is the maximum depth of an acceptable stale but valid solution.
	staleThreshold = 7
	// remoteSealerTimeout bounds each miner notification request.
	remoteSealerTimeout = 1 * time.Second
	// rateExpiry drops a remote worker's reported hashrate after this silence.
	rateExpiry = 10 * time.Second
)

var (
	errNoMiningWork      = errors.New("no mining work available yet")
	errInvalidSealResult = errors.New("invalid proof-of-work solution")
	errUnknownWork       = errors.New("no live job: no pending template with that header hash (the node's template at this height waits on its round, was re-cut, or is not yet made)")
	errStaleWork         = errors.New("stale: the template is behind the chain's head")
	errStaleParent       = errors.New("stale: the template's parent is not the chain's head (the seal would be dropped at the miner's parent check)")
	errResultNotRead     = errors.New("sealing result is not read by miner")
	errDuplicateWork     = errors.New("work submitted is a duplicate of one already seen for that header hash")
	errVerifierBusy      = errors.New("verifier busy: the submission queue is full, retry")
)

// Seal implements consensus.Engine. In fake mode it seals immediately with a
// zero nonce. Otherwise it pushes the work to the remote sealer (the mining
// RPC) and, when CPU threads are enabled, searches locally with the reference
// solver. CPU sealing is for the null-algorithm devnet and tests only.
func (e *Engine) Seal(chain consensus.ChainHeaderReader, block *types.Block, results chan<- *types.Block, stop <-chan struct{}) error {
	// One seal per height under a parent (the project's order of 23:0x UK, 11
	// October 2026, on A7's second seal at 935: a seal forwarded after the
	// stop while the re-cut template's search found another at the same
	// height): a template for a pair this node has already delivered a
	// seal for starts no search; the worker's ClearSealed reopens the pair
	// when the node's own validation refused the delivered block.
	if e.heightSealed(block.NumberU64(), block.ParentHash()) {
		e.log.Info("Height already sealed under this parent; no second search", "number", block.Number(), "parent", block.ParentHash())
		return nil
	}
	if e.config.FakePoW {
		header := block.Header()
		header.Nonce, header.MixDigest = types.BlockNonce{}, common.Hash{}
		e.deliverSeal(results, block.WithSeal(header), "fake")
		return nil
	}
	abort := make(chan struct{})
	// The drain (the prover-stall finding of 21:3x UK, 12 October 2026, on
	// the condition-4 box chain T: the first finder's proof ran beside 31
	// threads still hashing the same template, 3.5 and 5.0 s instead of
	// 0.3): the first finder closes drain BEFORE its proof and every
	// search thread stops on it as on abort, so the proof has the cores.
	// abort keeps its meaning (the template is gone; a finder inside its
	// proof still delivers).
	drain := make(chan struct{})
	var drainOnce, failedOnce sync.Once
	stopSearch := make(chan struct{})
	failed := make(chan struct{})
	go func() {
		select {
		case <-abort:
		case <-drain:
		}
		close(stopSearch)
	}()
	sealCtl := &sealControl{
		abort: abort, stopSearch: stopSearch,
		drain:  func() { drainOnce.Do(func() { close(drain) }) },
		failed: func() { failedOnce.Do(func() { close(failed) }) },
	}

	e.lock.Lock()
	threads := e.threads
	if e.rand == nil {
		seed, err := crand.Int(crand.Reader, big.NewInt(math.MaxInt64))
		if err != nil {
			e.lock.Unlock()
			return err
		}
		e.rand = rand.New(rand.NewSource(seed.Int64()))
	}
	e.lock.Unlock()
	if threads == 0 {
		threads = runtime.NumCPU()
	}
	keyed := e.keyedRoundsFor(block.Header()) > 0
	if threads < 0 && keyed && e.config.SealWorker == "" {
		// F-64 (the ninth development chain's Labs-outage exercise, 12
		// October 2026): a KEYED template is the bond's own search with the
		// bond's secret, which no remote worker can run; under
		// --dev.cpuseal=false (threads below zero, "remote workers only")
		// a bonded stand-in assembled 45 keyed templates in the window and
		// none was ever solved, its units idle. The flag governs the open
		// seat's CPU search alone: a keyed template searches on the CPU
		// threads whenever no seal worker is configured.
		threads = runtime.NumCPU()
	}
	if threads < 0 {
		threads = 0
	}
	// The keyed seal worker (sealworker.go): the search on the card under
	// --igneum.seal-worker, the third path beside the CPU threads and the
	// remote sealer. A KEYED template searches on the worker and on no CPU
	// thread; an unkeyed one (the open seat, the liveness round: the
	// chain's floor, which a card sealer must not go dark at; the protocol team's
	// read of 09:5x UK, 12 October 2026) seals on the CPU twin as today.
	worker := e.config.SealWorker != "" && keyed
	if worker {
		threads = 0
	}
	if threads > 0 {
		// A node that searches on its CPU holds full-dataset contexts (the
		// worker's rate); a verifier keeps light ones. Once, on the first
		// search (the fifth development chain sealed at the light path's
		// 500 hashes a second on 24 serialised threads, 11 October 2026).
		e.fullDataset.Do(func() {
			if f, ok := e.algo.(interface{ SetFullDataset(bool) }); ok {
				f.SetFullDataset(true)
				e.log.Info("CPU sealing on full-dataset contexts")
			}
		})
	}
	if e.remote != nil {
		e.remote.workCh <- &sealTask{block: block, results: results}
	}
	var (
		pend sync.WaitGroup
		// buffered to the thread count: a finder deposits its proven seal
		// without blocking, whatever the abort says (mine)
		locals = make(chan *types.Block, threads+1)
	)
	if threads > 0 {
		e.search.open(block.NumberU64())
		go func() {
			<-abort
			pend.Wait()
			e.search.close()
		}()
	}
	for i := 0; i < threads; i++ {
		pend.Add(1)
		go func(id int, start uint64) {
			defer pend.Done()
			e.mine(block, id, start, sealCtl, locals)
		}(i, uint64(e.rand.Int63()))
	}
	if worker {
		pend.Add(1)
		go func() {
			defer pend.Done()
			e.sealOnWorker(chain, block, abort, locals)
		}()
	}
	go func() {
		deliver := func(result *types.Block) { e.deliverSeal(results, result, "local") }
		select {
		case <-stop:
			close(abort)
			// A finder inside its proof at the stop finishes it and
			// deposits; the seal is forwarded (the worker keeps it when
			// its template is still pending at the height, and drops it
			// after a new head), never discarded here.
			pend.Wait()
			select {
			case result := <-locals:
				deliver(result)
			default:
			}
			return
		case result := <-locals:
			deliver(result)
			close(abort)
		case <-e.update:
			close(abort)
			if err := e.Seal(chain, block, results, stop); err != nil {
				e.log.Error("Failed to restart sealing after update", "err", err)
			}
		case <-failed:
			// The first finder's proof failed after the drain stopped the
			// template's other threads (the security review's F-76): the search is
			// reopened on the same template with a fresh control, the
			// claim released, so a second find is proven and the height
			// is never silently abandoned.
			close(abort)
			pend.Wait()
			// A thread that found before the drain reached it may have
			// claimed the template after the release and PROVEN its seal
			// into locals (the dev difficulty makes every attempt a seal):
			// that seal is delivered, not dropped, and no restart is
			// needed. Otherwise the claim is released again here, after
			// every old thread has stopped, so no late finder can hold it
			// against the resumed search (the race the first form had:
			// the resumed finders' claims all refused, the height
			// abandoned).
			select {
			case result := <-locals:
				deliver(result)
				return
			default:
			}
			e.claims.release(e.SealHash(block.Header()))
			e.log.Warn("Proof failed on the first find; the template's search resumes", "number", block.Number())
			if err := e.Seal(chain, block, results, stop); err != nil {
				e.log.Error("Failed to resume sealing after a failed proof", "err", err)
			}
			return
		}
		pend.Wait()
	}()
	return nil
}

// deliverSeal forwards a proven seal to the worker and marks its (height,
// parent) pair sealed; a second seal for a marked pair (another thread's
// find on a re-cut template, a late forward after the stop) is dropped
// here with its name and never leaves the node.
func (e *Engine) deliverSeal(results chan<- *types.Block, result *types.Block, mode string) {
	e.sealedMu.Lock()
	h, parent := result.NumberU64(), result.ParentHash()
	if e.sealedSet && e.sealedHeight == h && e.sealedParent == parent {
		e.sealedMu.Unlock()
		e.log.Warn("A second seal for the height discarded (one seal per height under a parent)", "number", h, "parent", parent, "hash", result.Hash())
		return
	}
	e.sealedSet, e.sealedHeight, e.sealedParent = true, h, parent
	e.sealedMu.Unlock()
	select {
	case results <- result:
	default:
		e.log.Warn("Sealing result is not read by miner", "mode", mode, "sealhash", e.SealHash(result.Header()))
	}
}

// heightSealed says whether this node has delivered a seal for the pair.
func (e *Engine) heightSealed(height uint64, parent common.Hash) bool {
	e.sealedMu.Lock()
	defer e.sealedMu.Unlock()
	return e.sealedSet && e.sealedHeight == height && e.sealedParent == parent
}

// HeightSealed is heightSealed for the worker's tests: whether the pair
// still holds this node's seal mark.
func (e *Engine) HeightSealed(height uint64, parent common.Hash) bool {
	return e.heightSealed(height, parent)
}

// ClearSealed reopens a (height, parent) pair for sealing: the worker's
// call when the node's own validation refused the delivered block, the
// one case where the node must seal the height again under the same
// parent. A new head is a new parent and needs no call.
func (e *Engine) ClearSealed(height uint64, parent common.Hash) {
	e.sealedMu.Lock()
	defer e.sealedMu.Unlock()
	if e.sealedSet && e.sealedHeight == height && e.sealedParent == parent {
		e.sealedSet = false
	}
}

// mine is one CPU search thread over the reference solver.
// attachHookForTest runs after the proof's attachment with the sealed
// header: a test's stand-in for a prover failure (the security review's F-76); nil
// in production, set and cleared by the test alone.
var attachHookForTest func(*types.Header) error

// release gives a template back after its first finder's proof FAILED
// (the security review's F-76): the next finder on the same template claims it and
// proves, so a failing prover on one find never abandons the height.
func (c *sealClaims) release(sealHash common.Hash) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.set == nil || !c.set[sealHash] {
		return
	}
	delete(c.set, sealHash)
	for i, h := range c.order {
		if h == sealHash {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
}

// sealControl is one Seal call's channels for its search threads: abort
// (the template is gone), stopSearch (abort or the first finder's drain,
// what the search loops stop on) and drain (the first finder's call before
// its proof, once per template).
type sealControl struct {
	abort      chan struct{}
	stopSearch chan struct{}
	drain      func()
	// failed is the first finder's word that its proof failed after the
	// drain (F-76): Seal reopens the search on the same template.
	failed func()
}

func (e *Engine) mine(block *types.Block, id int, start uint64, ctl *sealControl, found chan *types.Block) {
	header := block.Header()
	sealHash := e.SealHash(header)
	logger := e.log.New("miner", id)
	abort := ctl.abort
	var (
		nonce  uint64
		mix    common.Hash
		shares []byte
		ticket []byte
		ok     bool
	)
	if s := e.keyedRoundsFor(header); s > 0 {
		// The keyed document (the hostile-key rule's work binding): every
		// attempt is the prover's hash under this node's secret; the
		// solution carries its shares. No secret, no attempts.
		sk, _ := e.bondSecret()
		// The search runs under the programme of the template's period
		// on its parent's branch (program.go); no anchor, no attempt on
		// this template (F-67).
		algo := e.sealAlgoFor(header)
		if algo == nil {
			<-abort
			return
		}
		k, isKeyed := algo.(pow.Keyed)
		if sk == nil || !isKeyed {
			logger.Warn("Keyed document and no bond secret on this node: the CPU sealer makes no attempt")
			<-abort
			return
		}
		// The meter marked every SolveTickEvery attempts: the keyed search
		// read LIVE while it runs (igneum_getHashrate; the project's dMin rule of
		// 04:00 UK, 12 October 2026), not only at a find.
		nonce, mix, shares, ok = pow.SolveKeyedTick(k, sealHash, header.Number.Uint64(), e.sealDifficulty(header), sk, start, ctl.stopSearch, e.searchTick)
		if !ok {
			select {
			case <-ctl.stopSearch:
			default:
				logger.Warn("Keyed search refused by the algorithm (no attempt made)", "number", header.Number, "sealhash", sealHash)
				<-abort
			}
			return
		}
	} else {
		algo := e.sealAlgoFor(header)
		if algo == nil {
			<-abort
			return
		}
		if t, isTicketed := algo.(pow.Ticketed); isTicketed {
			// The Graph coordinate: one graph per nonce through the
			// reference solver, the first ticket under the target the
			// seal, carried in the header outside the seal hash.
			nonce, mix, ticket, ok = pow.SolveTicketTick(t, sealHash, header.Number.Uint64(), e.sealDifficulty(header), start, ctl.stopSearch, e.searchTick)
		} else {
			nonce, mix, ok = pow.SolveTick(algo, sealHash, header.Number.Uint64(), e.sealDifficulty(header), start, ctl.stopSearch, e.searchTick)
		}
	}
	if !ok {
		// Stopped: by the abort (the template is gone) or by the first
		// finder's drain (its proof has the cores); quiet either way.
		logger.Trace("Nonce search stopped")
		return
	}
	header = types.CopyHeader(header)
	header.Nonce = types.EncodeNonce(nonce)
	header.MixDigest = mix
	header.Shares = shares
	header.Ticket = ticket
	logger.Info("Seal found", "number", header.Number, "keyedRounds", e.keyedRoundsFor(header), "shares", len(shares), "sealhash", sealHash)
	// The first finder claims the template (pi.go sealClaims): a later
	// finder on the same template discards its seal without proving, so one
	// proof is made per template and never over another finder's nonce.
	if !e.claims.claim(sealHash) {
		logger.Trace("Seal found after another thread's on this template; discarded", "nonce", nonce)
		return
	}
	// A find for a pair already sealed (a re-cut template at the same
	// height under the same parent whose earlier seal was delivered) is
	// discarded before the proof: no second seal, no proof spent on it.
	if e.heightSealed(header.Number.Uint64(), header.ParentHash) {
		logger.Info("Seal found for a height already sealed under this parent; discarded before the proof", "number", header.Number)
		return
	}
	// The first finder stops the template's other search threads BEFORE
	// its proof (the drain), so the prover runs on free cores: on the
	// box chain T the proof ran beside 31 hashing threads and took 3.5 to
	// 5.0 s instead of 0.3 (the prover-stall finding, 12 October 2026).
	ctl.drain()
	// The proof pi under --igneum.pi-verify (pi.go): a keyed seal is
	// announced with its proof or not at all.
	if e.proofHook != nil {
		e.proofHook()
	}
	sealed, err := e.attachPi(block, header)
	if err == nil && attachHookForTest != nil {
		// A test's stand-in for a prover failure (F-76's row).
		err = attachHookForTest(header)
	}
	if err != nil {
		// The proof failed after the drain stopped the template's other
		// threads (F-76): the claim is released and Seal told, so the
		// search reopens on the template and a second find is proven
		// rather than the height abandoned until the next recommit.
		logger.Warn("Seal dropped: no proof; the template's search resumes", "number", header.Number, "err", err)
		e.claims.release(sealHash)
		ctl.failed()
		return
	}
	// Delivered whatever the abort says (the node team's read of 21:3x UK,
	// 11 October 2026): the worker re-cuts the template at every recommit
	// with new transactions and closes abort, and a finder that was inside
	// the 4 to 10 s CPU proof then took the closed abort and discarded a
	// PROVEN seal ("Nonce found but discarded"); under a transaction
	// stream a CPU leader lost its seals again and again. The channel is
	// buffered to the thread count, so the deposit never blocks; Seal's
	// goroutine forwards a late seal after the stop, and the worker's
	// result loop keeps a seal whose template is still pending at the
	// height and drops one for an old height.
	found <- sealed
	logger.Trace("Nonce found and reported", "nonce", nonce)
}

// workPackage builds the WorkPackage for a pending block.
func (e *Engine) workPackage(block *types.Block) pow.WorkPackage {
	header := block.Header()
	height := header.Number.Uint64()
	wp := pow.WorkPackage{
		Algorithm:   e.algo.Name(),
		HeaderHash:  e.SealHash(header),
		Height:      height,
		ParentHash:  header.ParentHash,
		Epoch:       e.algo.Epoch(height),
		SeedHash:    e.algo.SeedHash(height),
		Target:      pow.Target(e.sealDifficulty(header)),
		Coinbase:    header.Coinbase,
		BondKey:     header.BondKey,
		KeyedRounds: e.keyedRoundsFor(header),
	}
	for _, w := range block.Withdrawals() {
		wp.Payouts = append(wp.Payouts, pow.PayoutEntry{Address: w.Address, Weight: w.Amount})
	}
	// An algorithm with a parameter set publishes it (the project's decision, 10
	// October 2026): the miner passes it to the worker verbatim.
	// Under a schedule the document is the one in force at the template's
	// height (the bounded menu); otherwise the algorithm's one document.
	// Under VectorWork the document is the template's coordinate's
	// (vector.go), and the package names the coordinate.
	docAlgo := e.algo
	if m := e.vectorMenu(); m != nil {
		if c, ok := e.coordOf(header); ok {
			coord, _ := m.CoordinateAt(c, height) // the document in force at the template's height
			docAlgo = coord.Algorithm
			cc := c
			wp.Coord, wp.CoordName, wp.CoordDifficulty = &cc, coord.Name, new(big.Int).Set(header.Difficulty)
			wp.Algorithm = coord.Algorithm.Name()
			// The coordinate's last block on the template's branch, from
			// the engine's own reader (the parent's state).
			if ch := e.chainReader(); ch != nil && height > 0 {
				if parent := ch.GetHeader(header.ParentHash, height-1); parent != nil {
					if s, err := e.vectorStateOf(ch, parent); err == nil {
						wp.CoordLastHeight = s.last[c]
					}
				}
			}
		}
	}
	if a, ah, ok := e.programAnchorFor(docAlgo, header.ParentHash, height); ok {
		anchor := a
		wp.ProgramAnchor, wp.AnchorHeight = &anchor, ah
	}
	if wp2, ok := docAlgo.(interface{ WorkParamsAt(uint64) any }); ok {
		wp.Params = wp2.WorkParamsAt(height)
	} else if wp2, ok := docAlgo.(interface{ WorkParams() any }); ok {
		wp.Params = wp2.WorkParams()
	}
	if d, ok := docAlgo.(interface{ ParamsDigestAt(uint64) common.Hash }); ok {
		wp.ParamsDigest = d.ParamsDigestAt(height)
	} else if d, ok := docAlgo.(interface{ ParamsDigest() common.Hash }); ok {
		wp.ParamsDigest = d.ParamsDigest()
	}
	// An UNKEYED template on a keyed row (the open seat, the liveness round,
	// the shares) is hashed with the row's unkeyed twin: the worker gets the
	// twin's document and digest, never the keyed one it cannot run without
	// the secret (schedule.UnkeyedWorkParamsAt).
	if wp.KeyedRounds == 0 {
		if u, ok := e.algo.(interface {
			UnkeyedWorkParamsAt(uint64) (any, common.Hash, bool)
		}); ok {
			if params, digest, keyedRow := u.UnkeyedWorkParamsAt(height); keyedRow {
				wp.Params, wp.ParamsDigest = params, digest
			}
		}
	}
	// The advance notice of a bounded menu's next document (pow.WorkPackage).
	if n, ok := e.algo.(interface {
		NextAt(uint64) (any, common.Hash, uint64, bool)
	}); ok {
		if params, digest, at, ahead := n.NextAt(height); ahead {
			wp.NextParams, wp.NextParamsDigest, wp.NextParamsHeight = params, digest, at
		}
	}
	// The share lane's fields (mineshares.go), from the document fields set
	// above.
	e.shareWorkFields(&wp, header)
	return wp
}

// legacyWork is the ethash-compatible eth_getWork form of a WorkPackage:
// header hash, seed hash, target, block number.
func legacyWork(w pow.WorkPackage) [4]string {
	return [4]string{
		w.HeaderHash.Hex(),
		w.SeedHash.Hex(),
		common.BytesToHash(w.Target.Bytes()).Hex(),
		hexutil.EncodeUint64(w.Height),
	}
}

type remoteSealer struct {
	works        map[common.Hash]*types.Block
	seen         map[common.Hash]*seenRing // per work: the last MaxSeenPerWork (nonce, mix digest) pairs submitted
	verifyCh     chan *verifyJob           // the bounded queue to the verifier pool
	verifyDelay  time.Duration             // test hook: a sleep before every proof check
	rates        map[common.Hash]hashrate
	currentBlock *types.Block
	currentWork  pow.WorkPackage
	// currentNumber mirrors currentBlock's height for the verifier
	// goroutines (the sealer loop owns currentBlock).
	currentNumber atomic.Uint64
	notifyCtx     context.Context
	cancelNotify  context.CancelFunc
	reqWG         sync.WaitGroup

	engine       *Engine
	noverify     bool
	notifyURLs   []string
	results      chan<- *types.Block
	workCh       chan *sealTask
	fetchWorkCh  chan *sealWork
	submitWorkCh chan *mineResult
	fetchRateCh  chan chan uint64
	submitRateCh chan *hashrate
	requestExit  chan struct{}
	exitCh       chan struct{}
}

type sealTask struct {
	block   *types.Block
	results chan<- *types.Block
}

type mineResult struct {
	nonce     types.BlockNonce
	mixDigest common.Hash
	hash      common.Hash
	shares    []byte // the keyed document's S shares (empty otherwise)
	ticket    []byte // a ticketed coordinate's witness (the Graph's 168 bytes; empty otherwise)
	// sealed and number name the block the submission seals (set by the
	// verifier from the delivered block, after attachPi, before it
	// answers errc; read by the API after): the pool matches its solution
	// to the chain by this hash (F-418).
	sealed common.Hash
	number uint64
	errc   chan error
}

type hashrate struct {
	id   common.Hash
	ping time.Time
	rate uint64
	done chan struct{}
}

type sealWork struct {
	errc chan error
	res  chan pow.WorkPackage
}

// seenKey is one submission's identity under a work package.
type seenKey struct {
	nonce types.BlockNonce
	mix   common.Hash
}

// seenRing remembers the last MaxSeenPerWork submissions of one work package
// for duplicate detection: past the bound the oldest entry is forgotten, so
// a flood of distinct submissions can never make the bound refuse an honest
// one (security's F-19); a duplicate beyond the ring costs one verification,
// bounded by the queue like any other submission.
type seenRing struct {
	set   map[seenKey]struct{}
	order []seenKey
	next  int
}

func newSeenRing() *seenRing {
	return &seenRing{set: make(map[seenKey]struct{}, MaxSeenPerWork), order: make([]seenKey, MaxSeenPerWork)}
}

// has reports whether the key is in the ring.
func (r *seenRing) has(k seenKey) bool {
	_, ok := r.set[k]
	return ok
}

// add puts the key in the ring, forgetting the oldest when the ring is full.
func (r *seenRing) add(k seenKey) {
	if len(r.set) >= MaxSeenPerWork {
		delete(r.set, r.order[r.next])
	}
	r.order[r.next] = k
	r.next = (r.next + 1) % MaxSeenPerWork
	r.set[k] = struct{}{}
}

// verifyJob is a submission past admission, waiting for a verifier: the
// header to verify, the height the pending block had, where to deliver the
// sealed block and the reply channel.
type verifyJob struct {
	block    *types.Block // the pending block the template came from: the sealed block is it with the seal
	header   *types.Header
	number   uint64
	sealhash common.Hash
	results  chan<- *types.Block
	current  uint64 // the current block's number at admission (the staleness reference)
	errc     chan error
	// res is the submission the job answers: verify sets its sealed hash
	// and number from the delivered block AFTER attachPi (a keyed seal's
	// PiHash sits inside the block hash; the security review's read), before
	// errc is answered, so the API's read is ordered.
	res *mineResult
}

func startRemoteSealer(e *Engine, urls []string, noverify bool) *remoteSealer {
	ctx, cancel := context.WithCancel(context.Background())
	s := &remoteSealer{
		engine:       e,
		noverify:     noverify,
		notifyURLs:   urls,
		notifyCtx:    ctx,
		cancelNotify: cancel,
		works:        make(map[common.Hash]*types.Block),
		seen:         make(map[common.Hash]*seenRing),
		verifyCh:     make(chan *verifyJob, SubmitQueueDepth()),
		rates:        make(map[common.Hash]hashrate),
		workCh:       make(chan *sealTask),
		fetchWorkCh:  make(chan *sealWork),
		submitWorkCh: make(chan *mineResult),
		fetchRateCh:  make(chan chan uint64),
		submitRateCh: make(chan *hashrate),
		requestExit:  make(chan struct{}),
		exitCh:       make(chan struct{}),
	}
	for i := 0; i < VerifyWorkers(); i++ {
		go s.verifier()
	}
	go s.loop()
	return s
}

// verifier is one of VerifyWorkers goroutines taking admitted submissions off
// the bounded queue: the expensive proof check runs here, never in the
// sealer's loop, so a flood of submissions cannot stall igneum_getWork or the
// arrival of new work.
func (s *remoteSealer) verifier() {
	for {
		select {
		case job := <-s.verifyCh:
			// A solution for a height the chain has already passed is
			// stale before any proof check: a GPU worker at the DAA's
			// starting difficulty submitted sixteen solutions a second
			// for one height, and every queued one after the first was
			// verified in full while the worker waited (11 October 2026).
			if cur := s.currentNumber.Load(); job.number < cur {
				s.engine.log.Warn("Work submitted is stale at verification", "number", job.number, "current", cur, "sealhash", job.sealhash)
				job.errc <- errStaleWork
				continue
			}
			job.errc <- s.verify(job)
		case <-s.exitCh:
			return
		}
	}
}

// verify checks one admitted submission and delivers the sealed block.
func (s *remoteSealer) verify(job *verifyJob) error {
	start := time.Now()
	if s.verifyDelay > 0 {
		time.Sleep(s.verifyDelay)
	}
	if !s.noverify {
		// The height is the node-built pending block's, not anything the worker sent.
		if err := s.engine.verifySeal(nil, job.header, job.number, true); err != nil {
			s.engine.log.Warn("Invalid proof-of-work submitted", "sealhash", job.sealhash, "elapsed", common.PrettyDuration(time.Since(start)), "err", err)
			// The reason names the fact (chapter 15 counts invalid, stale and
			// rejected apart): a mix digest that does not recompute, a result
			// above the target, or the verifier's own words.
			switch {
			case errors.Is(err, pow.ErrInvalidMixDigest):
				return fmt.Errorf("%w: mix digest does not recompute", errInvalidSealResult)
			case errors.Is(err, pow.ErrResultAboveTarget):
				return fmt.Errorf("%w: result above target", errInvalidSealResult)
			default:
				return fmt.Errorf("%w: %v", errInvalidSealResult, err)
			}
		}
	}
	if job.results == nil {
		s.engine.log.Warn("Result channel is empty, submitted mining result is rejected")
		return errResultNotRead
	}
	// The sealed block is the pending block with the seal, body and all:
	// a header-only block here dropped every transaction of a remote-sealed
	// block from the chain (the state carried them, no body or receipt did;
	// the pool's development chain, 10 October 2026).
	// The proof pi under --igneum.pi-verify (pi.go): a remote keyed seal
	// under this node's key is announced with its proof or not at all.
	solution, err := s.engine.attachPi(job.block, job.header)
	if err != nil {
		s.engine.log.Warn("Submitted seal dropped: no proof", "number", job.number, "err", err)
		return err
	}
	if job.res != nil {
		job.res.sealed, job.res.number = solution.Hash(), solution.NumberU64()
	}
	select {
	case job.results <- solution:
		s.engine.log.Info("Remote seal accepted and handed to the miner", "number", solution.NumberU64(), "sealhash", job.sealhash, "hash", solution.Hash(), "shares", len(job.header.Shares))
		return nil
	default:
		s.engine.log.Warn("Sealing result is not read by miner", "mode", "remote", "sealhash", job.sealhash)
		return errResultNotRead
	}
}

func (s *remoteSealer) loop() {
	defer func() {
		s.engine.log.Trace("Igneum remote sealer is exiting")
		s.cancelNotify()
		s.reqWG.Wait()
		close(s.exitCh)
	}()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case work := <-s.workCh:
			s.results = work.results
			s.makeWork(work.block)
			s.notifyWork()

		case work := <-s.fetchWorkCh:
			if s.currentBlock == nil {
				work.errc <- errNoMiningWork
			} else {
				work.res <- s.currentWork
			}

		case result := <-s.submitWorkCh:
			if err := s.admit(result); err != nil {
				result.errc <- err
			}

		case result := <-s.submitRateCh:
			s.rates[result.id] = hashrate{rate: result.rate, ping: time.Now()}
			close(result.done)

		case req := <-s.fetchRateCh:
			var total uint64
			for _, rate := range s.rates {
				total += rate.rate
			}
			req <- total

		case <-ticker.C:
			for id, rate := range s.rates {
				if time.Since(rate.ping) > rateExpiry {
					delete(s.rates, id)
				}
			}
			if s.currentBlock != nil {
				for hash, block := range s.works {
					if block.NumberU64()+staleThreshold <= s.currentBlock.NumberU64() {
						delete(s.works, hash)
						delete(s.seen, hash)
					}
				}
			}

		case <-s.requestExit:
			return
		}
	}
}

func (s *remoteSealer) makeWork(block *types.Block) {
	w := s.engine.workPackage(block)
	s.currentWork = w
	s.currentBlock = block
	s.currentNumber.Store(block.NumberU64())
	s.works[w.HeaderHash] = block
	if s.seen[w.HeaderHash] == nil {
		s.seen[w.HeaderHash] = newSeenRing()
	}
}

// notification is the body POSTed to --miner.notify URLs: the work package
// and, with NotifyFull, the pending header.
type notification struct {
	pow.WorkPackage
	Header *types.Header `json:"header,omitempty"`
}

func (s *remoteSealer) notifyWork() {
	if len(s.notifyURLs) == 0 {
		return
	}
	n := notification{WorkPackage: s.currentWork}
	if s.engine.config.NotifyFull {
		n.Header = s.currentBlock.Header()
	}
	blob, _ := json.Marshal(n)
	s.reqWG.Add(len(s.notifyURLs))
	for _, url := range s.notifyURLs {
		go s.sendNotification(s.notifyCtx, url, blob, s.currentWork)
	}
}

func (s *remoteSealer) sendNotification(ctx context.Context, url string, body []byte, work pow.WorkPackage) {
	defer s.reqWG.Done()
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		s.engine.log.Warn("Can't create remote miner notification", "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, remoteSealerTimeout)
	defer cancel()
	req = req.WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.engine.log.Warn("Failed to notify remote miner", "err", err)
	} else {
		s.engine.log.Trace("Notified remote miner", "miner", url, "hash", work.HeaderHash, "target", work.Target)
		resp.Body.Close()
	}
}

// admit runs the cheap checks of a submission in the sealer's loop, in the
// order no work, unknown work, stale work, duplicate (within the ring of the
// last MaxSeenPerWork submissions), queue full, and hands
// an admitted submission to the verifier pool; the expensive proof check
// never runs here (the specification's section 11: bounded parsing, chain
// context checks and duplicate detection before expensive work). The reply
// for an admitted submission comes from the verifier; for a refused one it
// is returned here and sent by the caller.
func (s *remoteSealer) admit(result *mineResult) error {
	if s.currentBlock == nil {
		return errNoMiningWork
	}
	block := s.works[result.hash]
	if block == nil {
		s.engine.log.Warn("Work submitted but none pending", "sealhash", result.hash, "curnumber", s.currentBlock.NumberU64())
		return errUnknownWork
	}
	if block.NumberU64()+staleThreshold <= s.currentBlock.NumberU64() {
		s.engine.log.Warn("Work submitted is too old", "number", block.NumberU64(), "sealhash", result.hash)
		return errStaleWork
	}
	seen := s.seen[result.hash]
	if seen == nil {
		seen = newSeenRing()
		s.seen[result.hash] = seen
	}
	key := seenKey{nonce: result.nonce, mix: result.mixDigest}
	if seen.has(key) {
		s.engine.log.Warn("Work submitted is a duplicate", "number", block.NumberU64(), "sealhash", result.hash)
		return errDuplicateWork
	}
	// F-418 (17 October 2026): a seal whose parent is not the head is
	// dropped by the miner's result loop (F-112, F-120), so a solution
	// against such a template is refused HERE with the fact, before the
	// proof check and before any "accepted": the pool's one keyed solution
	// of the bounded run was admitted, verified, answered accepted and
	// dropped 0.46 s later with nothing at the pool's end to say so.
	if ch := s.engine.chainReader(); ch != nil {
		if head := ch.CurrentHeader(); head != nil && block.ParentHash() != head.Hash() {
			s.engine.log.Warn("Work submitted on a template whose parent is not the head", "number", block.NumberU64(), "parent", block.ParentHash(), "head", head.Number, "headhash", head.Hash(), "sealhash", result.hash)
			return errStaleParent
		}
	}
	header := block.Header()
	header.Nonce = result.nonce
	header.MixDigest = result.mixDigest
	header.Shares = result.shares
	if len(result.ticket) > 0 {
		// A remote worker's ticket (igneum_submitTicket): the witness
		// rides the header outside the seal hash and the verifier below
		// checks it under the triple (VerifyTicketed).
		header.Ticket = append([]byte(nil), result.ticket...)
	}
	job := &verifyJob{block: block, header: header, number: block.NumberU64(), sealhash: result.hash, results: s.results, current: s.currentBlock.NumberU64(), errc: result.errc, res: result}
	select {
	case s.verifyCh <- job:
		seen.add(key)
		return nil
	default:
		return errVerifierBusy
	}
}
