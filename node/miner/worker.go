// Copyright 2015 The go-ethereum Authors
// This file is part of the go-ethereum library.
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

package miner

import (
	"errors"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/igneum"
	"github.com/ethereum/go-ethereum/consensus/misc/eip1559"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
	"github.com/ethereum/go-ethereum/params/vars"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/holiman/uint256"
)

const (
	// resultQueueSize is the size of channel listening to sealing result.
	resultQueueSize = 10

	// txChanSize is the size of channel listening to NewTxsEvent.
	// The number is referenced from the size of tx pool.
	txChanSize = 4096

	// chainHeadChanSize is the size of channel listening to ChainHeadEvent.
	chainHeadChanSize = 10

	// resubmitAdjustChanSize is the size of resubmitting interval adjustment channel.
	resubmitAdjustChanSize = 10

	// sealingLogAtDepth is the number of confirmations before logging successful sealing.
	sealingLogAtDepth = 7

	// minRecommitInterval is the minimal time interval to recreate the sealing block with
	// any newly arrived transactions.
	minRecommitInterval = 1 * time.Second

	// maxRecommitInterval is the maximum time interval to recreate the sealing block with
	// any newly arrived transactions.
	maxRecommitInterval = 15 * time.Second

	// intervalAdjustRatio is the impact a single interval adjustment has on sealing work
	// resubmitting interval.
	intervalAdjustRatio = 0.1

	// intervalAdjustBias is applied during the new resubmit interval calculation in favor of
	// increasing upper limit or decreasing lower limit so that the limit can be reachable.
	intervalAdjustBias = 200 * 1000.0 * 1000.0

	// staleThreshold is the maximum depth of the acceptable stale block.
	staleThreshold = 7
)

var (
	errBlockInterruptedByNewHead  = errors.New("new head arrived while building block")
	errBlockInterruptedByRecommit = errors.New("recommit interrupt while building block")
)

// environment is the worker's current environment and holds all
// information of the sealing block generation.
type environment struct {
	signer types.Signer

	state    *state.StateDB // apply state changes here
	tcount   int            // tx count in cycle
	gasPool  *core.GasPool  // available gas used to pack transactions
	coinbase common.Address

	header   *types.Header
	txs      []*types.Transaction
	receipts []*types.Receipt
	payouts  []*types.Withdrawal // the sortition prototype's payout list (the body's withdrawals)
}

// copy creates a deep copy of environment.
func (env *environment) copy() *environment {
	cpy := &environment{
		signer:   env.signer,
		state:    env.state.Copy(),
		tcount:   env.tcount,
		coinbase: env.coinbase,
		header:   types.CopyHeader(env.header),
		receipts: copyReceipts(env.receipts),
		payouts:  env.payouts,
	}
	if env.gasPool != nil {
		gasPool := *env.gasPool
		cpy.gasPool = &gasPool
	}
	// The content of txs is immutable, unnecessary to do the expensive deep
	// copy for it.
	cpy.txs = make([]*types.Transaction, len(env.txs))
	copy(cpy.txs, env.txs)
	return cpy
}

// discard terminates the background prefetcher go-routine. It should
// always be called for all created environment instances otherwise
// the go-routine leak can happen.
func (env *environment) discard() {
	if env.state == nil {
		return
	}
	env.state.StopPrefetcher()
}

// task contains all information for consensus engine sealing and result submitting.
type task struct {
	receipts  []*types.Receipt
	state     *state.StateDB
	block     *types.Block
	createdAt time.Time
	// head is the chain's head when the template was committed (F-115):
	// a seal is dropped when the head has MOVED since, never for a parent
	// the sealer policy chose beside the head (part 4's grandparent, part
	// 5's first-seen twin at equal weight).
	head common.Hash
}

const (
	commitInterruptNone int32 = iota
	commitInterruptNewHead
	commitInterruptResubmit
)

// newWorkReq represents a request for new sealing work submitting with relative interrupt notifier.
type newWorkReq struct {
	interrupt *atomic.Int32
	noempty   bool
	timestamp int64
}

// intervalAdjust represents a resubmitting interval adjustment.
type intervalAdjust struct {
	ratio float64
	inc   bool
}

// worker is the main object which takes care of submitting new work to consensus engine
// and gathering the sealing result.
type worker struct {
	config      *Config
	chainConfig ctypes.ChainConfigurator
	engine      consensus.Engine
	eth         Backend
	chain       *core.BlockChain

	// Feeds
	pendingLogsFeed event.Feed

	// Subscriptions
	mux          *event.TypeMux
	txsCh        chan core.NewTxsEvent
	txsSub       event.Subscription
	chainHeadCh  chan core.ChainHeadEvent
	chainHeadSub event.Subscription

	// Channels
	newWorkCh          chan *newWorkReq
	taskCh             chan *task
	resultCh           chan *types.Block
	// policyYieldAt is the height at which the sealer policy yields to
	// the canonical head after a dropped seal beside it (F-120).
	policyYieldAt atomic.Uint64
	startCh            chan struct{}
	exitCh             chan struct{}
	resubmitIntervalCh chan time.Duration
	resubmitAdjustCh   chan *intervalAdjust

	wg sync.WaitGroup

	current     *environment       // An environment for current running cycle.
	unconfirmed *unconfirmedBlocks // A set of locally mined blocks pending canonicalness confirmations.

	mu       sync.RWMutex // The lock used to protect the coinbase and extra fields
	coinbase common.Address
	extra    []byte
	payouts  []*types.Withdrawal // the sortition prototype's payout list, set by miner_setPayoutList
	retry    atomic.Bool          // the engine asked for another template (not yet eligible)
	// sealing is the header of the template under search (the task
	// loop's last Seal), read by the recommit tick for F-171's round
	// re-issue; nil before the first task.
	sealing atomic.Pointer[types.Header]
	// waitLogged is the last height whose template wait was logged at
	// Info (the next ticks' waits at that height are Debug).
	waitLogged uint64
	tip      *uint256.Int // Minimum tip needed for non-local transaction to include them

	pendingMu    sync.RWMutex
	pendingTasks map[common.Hash]*task

	snapshotMu       sync.RWMutex // The lock used to protect the snapshots below
	snapshotBlock    *types.Block
	snapshotReceipts types.Receipts
	snapshotState    *state.StateDB

	// atomic status counters
	running atomic.Bool  // The indicator whether the consensus engine is running or not.
	newTxs  atomic.Int32 // New arrival transaction count since last sealing work submitting.
	syncing atomic.Bool  // The indicator whether the node is still syncing.

	// noempty is the flag used to control whether the feature of pre-seal empty
	// block is enabled. The default value is false(pre-seal is enabled by default).
	// But in some special scenario the consensus engine will seal blocks instantaneously,
	// in this case this feature will add all empty blocks into canonical chain
	// non-stop and no real transaction will be included.
	noempty atomic.Bool

	// recommit is the time interval to re-create sealing work.
	recommit time.Duration

	// Test hooks
	newTaskHook  func(*task)                        // Method to call upon receiving a new sealing task.
	skipSealHook func(*task) bool                   // Method to decide whether skipping the sealing.
	fullTaskHook func()                             // Method to call before pushing the full sealing task.
	resubmitHook func(time.Duration, time.Duration) // Method to call upon updating resubmitting interval.
}

func newWorker(config *Config, chainConfig ctypes.ChainConfigurator, engine consensus.Engine, eth Backend, mux *event.TypeMux, init bool) *worker {
	worker := &worker{
		config:             config,
		chainConfig:        chainConfig,
		engine:             engine,
		eth:                eth,
		chain:              eth.BlockChain(),
		mux:                mux,
		unconfirmed:        newUnconfirmedBlocks(eth.BlockChain(), sealingLogAtDepth),
		coinbase:           config.Etherbase,
		extra:              config.ExtraData,
		tip:                uint256.MustFromBig(config.GasPrice),
		pendingTasks:       make(map[common.Hash]*task),
		txsCh:              make(chan core.NewTxsEvent, txChanSize),
		chainHeadCh:        make(chan core.ChainHeadEvent, chainHeadChanSize),
		newWorkCh:          make(chan *newWorkReq),
		taskCh:             make(chan *task),
		resultCh:           make(chan *types.Block, resultQueueSize),
		startCh:            make(chan struct{}, 1),
		exitCh:             make(chan struct{}),
		resubmitIntervalCh: make(chan time.Duration),
		resubmitAdjustCh:   make(chan *intervalAdjust, resubmitAdjustChanSize),
	}
	// Subscribe for transaction insertion events (whether from network or resurrects)
	worker.txsSub = eth.TxPool().SubscribeTransactions(worker.txsCh, true)
	// Subscribe events for blockchain
	worker.chainHeadSub = eth.BlockChain().SubscribeChainHeadEvent(worker.chainHeadCh)

	// Sanitize recommit interval if the user-specified one is too short.
	recommit := worker.config.Recommit
	if recommit < minRecommitInterval {
		log.Warn("Sanitizing miner recommit interval", "provided", recommit, "updated", minRecommitInterval)
		recommit = minRecommitInterval
	}
	worker.recommit = recommit

	worker.wg.Add(4)
	go worker.mainLoop()
	go worker.newWorkLoop(recommit)
	go worker.resultLoop()
	go worker.taskLoop()

	// Submit first work to initialize pending state.
	if init {
		worker.startCh <- struct{}{}
	}
	return worker
}

// setEtherbase sets the etherbase used to initialize the block coinbase field.
func (w *worker) setEtherbase(addr common.Address) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.coinbase = addr
}

// etherbase retrieves the configured etherbase address.
func (w *worker) etherbase() common.Address {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.coinbase
}

func (w *worker) setGasCeil(ceil uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.config.GasCeil = ceil
}

// setExtra sets the content used to initialize the block extra field.
func (w *worker) setExtra(extra []byte) {
	w.mu.Lock()
	w.extra = extra
	w.mu.Unlock()
	// A new template follows at the next recommit tick rather than at the
	// next head or transaction (a pool setting the finder's key or its
	// list waited on the chain for the template, 11 October 2026).
	w.retry.Store(true)
}

// setPayouts sets the payout list the next templates carry (the sortition
// prototype; nil clears it).
func (w *worker) setPayouts(list []*types.Withdrawal) {
	w.mu.Lock()
	w.payouts = list
	w.mu.Unlock()
	w.retry.Store(true)
}

// setGasTip sets the minimum miner tip needed to include a non-local transaction.
func (w *worker) setGasTip(tip *big.Int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.tip = uint256.MustFromBig(tip)
}

// setRecommitInterval updates the interval for miner sealing work recommitting.
func (w *worker) setRecommitInterval(interval time.Duration) {
	select {
	case w.resubmitIntervalCh <- interval:
	case <-w.exitCh:
	}
}

// disablePreseal disables pre-sealing feature
func (w *worker) disablePreseal() {
	w.noempty.Store(true)
}

// enablePreseal enables pre-sealing feature
func (w *worker) enablePreseal() {
	w.noempty.Store(false)
}

// pending returns the pending state and corresponding block.
func (w *worker) pending() (*types.Block, *state.StateDB) {
	// return a snapshot to avoid contention on currentMu mutex
	w.snapshotMu.RLock()
	defer w.snapshotMu.RUnlock()
	if w.snapshotState == nil {
		return nil, nil
	}
	return w.snapshotBlock, w.snapshotState.Copy()
}

// pendingBlock returns pending block.
func (w *worker) pendingBlock() *types.Block {
	// return a snapshot to avoid contention on currentMu mutex
	w.snapshotMu.RLock()
	defer w.snapshotMu.RUnlock()
	return w.snapshotBlock
}

// pendingBlockAndReceipts returns pending block and corresponding receipts.
func (w *worker) pendingBlockAndReceipts() (*types.Block, types.Receipts) {
	// return a snapshot to avoid contention on currentMu mutex
	w.snapshotMu.RLock()
	defer w.snapshotMu.RUnlock()
	return w.snapshotBlock, w.snapshotReceipts
}

// start sets the running status as 1 and triggers new work submitting.
func (w *worker) start() {
	w.running.Store(true)
	w.startCh <- struct{}{}
}

// stop sets the running status as 0.
func (w *worker) stop() {
	w.running.Store(false)
}

// isRunning returns an indicator whether worker is running or not.
func (w *worker) isRunning() bool {
	return w.running.Load()
}

// close terminates all background threads maintained by the worker.
// Note the worker does not support being closed multiple times.
func (w *worker) close() {
	w.running.Store(false)
	close(w.exitCh)
	w.wg.Wait()
}

// recalcRecommit recalculates the resubmitting interval upon feedback.
func recalcRecommit(minRecommit, prev time.Duration, target float64, inc bool) time.Duration {
	var (
		prevF = float64(prev.Nanoseconds())
		next  float64
	)
	if inc {
		next = prevF*(1-intervalAdjustRatio) + intervalAdjustRatio*(target+intervalAdjustBias)
		max := float64(maxRecommitInterval.Nanoseconds())
		if next > max {
			next = max
		}
	} else {
		next = prevF*(1-intervalAdjustRatio) + intervalAdjustRatio*(target-intervalAdjustBias)
		min := float64(minRecommit.Nanoseconds())
		if next < min {
			next = min
		}
	}
	return time.Duration(int64(next))
}

// newWorkLoop is a standalone goroutine to submit new sealing work upon received events.
func (w *worker) newWorkLoop(recommit time.Duration) {
	defer w.wg.Done()
	var (
		interrupt   *atomic.Int32
		minRecommit = recommit // minimal resubmit interval specified by user.
		timestamp   int64      // timestamp for each round of sealing.
	)

	timer := time.NewTimer(0)
	defer timer.Stop()
	<-timer.C // discard the initial tick

	// commit aborts in-flight transaction execution with given signal and resubmits a new one.
	commit := func(noempty bool, s int32) {
		if interrupt != nil {
			interrupt.Store(s)
		}
		interrupt = new(atomic.Int32)
		select {
		case w.newWorkCh <- &newWorkReq{interrupt: interrupt, noempty: noempty, timestamp: timestamp}:
		case <-w.exitCh:
			return
		}
		timer.Reset(recommit)
		w.newTxs.Store(0)
	}
	// clearPending cleans the stale pending tasks.
	clearPending := func(number uint64) {
		w.pendingMu.Lock()
		for h, t := range w.pendingTasks {
			if t.block.NumberU64()+staleThreshold <= number {
				delete(w.pendingTasks, h)
			}
		}
		w.pendingMu.Unlock()
	}

	for {
		select {
		case <-w.startCh:
			clearPending(w.chain.CurrentBlock().Number.Uint64())
			timestamp = time.Now().Unix()
			commit(false, commitInterruptNewHead)

		case head := <-w.chainHeadCh:
			clearPending(head.Block.NumberU64())
			timestamp = time.Now().Unix()
			commit(false, commitInterruptNewHead)

		case <-timer.C:
			// If sealing is running resubmit a new work cycle periodically to pull in
			// higher priced transactions. Disable this overhead for pending blocks.
			if w.isRunning() && (!w.chainConfig.GetConsensusEngineType().IsClique() || w.chainConfig.GetCliquePeriod() > 0) {
				// Short circuit if no new transaction arrives, unless the
				// engine asked for another template (the sortition
				// prototype's round wait).
				if w.newTxs.Load() == 0 && !w.retry.Swap(false) && !w.templateRoundOver() {
					timer.Reset(recommit)
					continue
				}
				commit(true, commitInterruptResubmit)
			}

		case interval := <-w.resubmitIntervalCh:
			// Adjust resubmit interval explicitly by user.
			if interval < minRecommitInterval {
				log.Warn("Sanitizing miner recommit interval", "provided", interval, "updated", minRecommitInterval)
				interval = minRecommitInterval
			}
			log.Info("Miner recommit interval update", "from", minRecommit, "to", interval)
			minRecommit, recommit = interval, interval

			if w.resubmitHook != nil {
				w.resubmitHook(minRecommit, recommit)
			}

		case adjust := <-w.resubmitAdjustCh:
			// Adjust resubmit interval by feedback.
			if adjust.inc {
				before := recommit
				target := float64(recommit.Nanoseconds()) / adjust.ratio
				recommit = recalcRecommit(minRecommit, recommit, target, true)
				log.Trace("Increase miner recommit interval", "from", before, "to", recommit)
			} else {
				before := recommit
				recommit = recalcRecommit(minRecommit, recommit, float64(minRecommit.Nanoseconds()), false)
				log.Trace("Decrease miner recommit interval", "from", before, "to", recommit)
			}

			if w.resubmitHook != nil {
				w.resubmitHook(minRecommit, recommit)
			}

		case <-w.exitCh:
			return
		}
	}
}

// mainLoop is responsible for generating and submitting sealing work based on
// the received event. It can support two modes: automatically generate task and
// submit it or return task according to given parameters for various proposes.
func (w *worker) mainLoop() {
	defer w.wg.Done()
	defer w.txsSub.Unsubscribe()
	defer w.chainHeadSub.Unsubscribe()
	defer func() {
		if w.current != nil {
			w.current.discard()
		}
	}()

	for {
		select {
		case req := <-w.newWorkCh:
			w.commitWork(req.interrupt, req.noempty, req.timestamp)

		case ev := <-w.txsCh:
			// Apply transactions to the pending state if we're not sealing
			//
			// Note all transactions received may not be continuous with transactions
			// already included in the current sealing block. These transactions will
			// be automatically eliminated.
			if !w.isRunning() && w.current != nil {
				// If block is already full, abort
				if gp := w.current.gasPool; gp != nil && gp.Gas() < vars.TxGas {
					continue
				}
				txs := make(map[common.Address][]*txpool.LazyTransaction, len(ev.Txs))
				for _, tx := range ev.Txs {
					acc, _ := types.Sender(w.current.signer, tx)
					txs[acc] = append(txs[acc], &txpool.LazyTransaction{
						Pool:      w.eth.TxPool(), // We don't know where this came from, yolo resolve from everywhere
						Hash:      tx.Hash(),
						Tx:        nil, // Do *not* set this! We need to resolve it later to pull blobs in
						Time:      tx.Time(),
						GasFeeCap: uint256.MustFromBig(tx.GasFeeCap()),
						GasTipCap: uint256.MustFromBig(tx.GasTipCap()),
						Gas:       tx.Gas(),
					})
				}
				plainTxs := newTransactionsByPriceAndNonce(w.current.signer, txs, w.current.header.BaseFee) // Mixed bag of everrything, yolo

				tcount := w.current.tcount
				w.commitTransactions(w.current, plainTxs, nil)

				// Only update the snapshot if any new transactions were added
				// to the pending block
				if tcount != w.current.tcount {
					w.updateSnapshot(w.current)
				}
			}
			w.newTxs.Add(int32(len(ev.Txs)))

		// System stopped
		case <-w.exitCh:
			return
		case <-w.txsSub.Err():
			return
		case <-w.chainHeadSub.Err():
			return
		}
	}
}

// taskLoop is a standalone goroutine to fetch sealing task from the generator and
// push them to consensus engine.
// templateRoundOver says whether the template under search has outlived
// its round (F-171, 15 October 2026): a block's stamp is its template's
// time, so a search that runs past a round arrives "older than its
// arrival by more than a round" and policy part 4 builds the other lanes'
// templates on its parent's parent (siblings of the head; the public
// test network's four-minute gap at 281 to 283, 75 firings in the research
// chain's cadence hour beside its 24 reorganisations). The sealer
// re-issues the template at the end of each round of its search, so the
// stamp advances with the search (the drawn round and the floor as they
// were, the time the clock's) and an honest slow block is never read as
// withheld; the engine says when the round is over (TemplateRoundOver).
func (w *worker) templateRoundOver() bool {
	ro, ok := w.engine.(interface {
		TemplateRoundOver(consensus.ChainHeaderReader, *types.Header) bool
	})
	if !ok {
		return false
	}
	h := w.sealing.Load()
	if h == nil {
		return false
	}
	if !ro.TemplateRoundOver(w.chain, h) {
		return false
	}
	// Once per template: the re-issued template is another header.
	w.sealing.Store(nil)
	log.Info("Template re-issued at its round's end: the stamp advances with the search (F-171)", "number", h.Number, "round", h.Round, "stamp", h.Time)
	return true
}

func (w *worker) taskLoop() {
	defer w.wg.Done()
	var (
		stopCh chan struct{}
		prev   common.Hash
	)

	// interrupt aborts the in-flight sealing task.
	interrupt := func() {
		if stopCh != nil {
			close(stopCh)
			stopCh = nil
		}
	}
	for {
		select {
		case task := <-w.taskCh:
			if w.newTaskHook != nil {
				w.newTaskHook(task)
			}
			// Reject duplicate sealing work due to resubmitting.
			sealHash := w.engine.SealHash(task.block.Header())
			if sealHash == prev {
				continue
			}
			// Interrupt previous sealing operation
			interrupt()
			stopCh, prev = make(chan struct{}), sealHash

			if w.skipSealHook != nil && w.skipSealHook(task) {
				continue
			}
			w.pendingMu.Lock()
			w.pendingTasks[sealHash] = task
			w.pendingMu.Unlock()
			w.sealing.Store(task.block.Header())

			if err := w.engine.Seal(w.chain, task.block, w.resultCh, stopCh); err != nil {
				log.Warn("Block sealing failed", "err", err)
				w.pendingMu.Lock()
				delete(w.pendingTasks, sealHash)
				w.pendingMu.Unlock()
			}
		case <-w.exitCh:
			interrupt()
			return
		}
	}
}

// resultLoop is a standalone goroutine to handle sealing result submitting
// and flush relative data to the database.
func (w *worker) resultLoop() {
	defer w.wg.Done()
	for {
		select {
		case block := <-w.resultCh:
			// Short circuit when receiving empty result.
			if block == nil {
				continue
			}
			// Short circuit when receiving duplicate result caused by resubmitting.
			if w.chain.HasBlock(block.Hash(), block.NumberU64()) {
				log.Info("Sealed block already in the chain; nothing to do", "number", block.Number(), "hash", block.Hash())
				continue
			}
			var (
				sealhash = w.engine.SealHash(block.Header())
				hash     = block.Hash()
			)
			w.pendingMu.RLock()
			task, exist := w.pendingTasks[sealhash]
			w.pendingMu.RUnlock()
			if !exist {
				log.Error("Block found but no relative pending task", "number", block.Number(), "sealhash", sealhash, "hash", hash)
				continue
			}
			// Different block could share same sealhash, deep copy here to prevent write-write conflict.
			var (
				receipts = make([]*types.Receipt, len(task.receipts))
				logs     []*types.Log
			)
			for i, taskReceipt := range task.receipts {
				receipt := new(types.Receipt)
				receipts[i] = receipt
				*receipt = *taskReceipt

				// add block location fields
				receipt.BlockHash = hash
				receipt.BlockNumber = block.Number()
				receipt.TransactionIndex = uint(i)

				// Update the block hash in all logs since it is now available and not when the
				// receipt/log of individual transactions were created.
				receipt.Logs = make([]*types.Log, len(taskReceipt.Logs))
				for i, taskLog := range taskReceipt.Logs {
					log := new(types.Log)
					receipt.Logs[i] = log
					*log = *taskLog
					log.BlockHash = hash
				}
				logs = append(logs, receipt.Logs...)
			}
			// The sealer's own block goes through the FULL validation (the
			// header with its seal, the body and its roots, the state
			// processor with the authority, endorsement and proof rules)
			// exactly as a peer's block would, and is refused and logged
			// when it fails, never written or broadcast: the sixth
			// development chain's sealer wrote and announced ninety blocks
			// with zero transaction roots that every peer refused (11
			// October 2026; the project's order). The write-and-set-head shortcut
			// that trusted the sealing environment's receipts is retired.
			_ = receipts
			_ = logs
			// A seal landed beside the chain (F-112, 14 October 2026, the
			// the old public test network (chain 7320) copy's read at 01:03Z: node2 imported node1's block
			// 12 at 36.841 s and announced its own 12 at 39.183 s, the seal
			// found during the other's proof and its own proof finished
			// after the height was canonical; on the old public test network (chain 7320) two or more
			// blocks stood at 75 percent of A10's heights and 3,825 reorgs
			// followed, because the deterministic tie-break then flipped
			// every node to whichever twin drew lower, and 37 percent of
			// the public pool's shares expired on the losing twins'
			// heads). The sealer's form, no consensus: a sealed block
			// whose parent is no longer this node's head (the chain holds
			// a canonical block at its height, or the parent itself lost)
			// is DROPPED, neither inserted nor announced, and the sealer
			// re-templates on the head at once (F-95's retry). Twins
			// remain only between seals within one propagation of each
			// other, which the fork rule's draw settles once.
			// The drop reads whether the HEAD HAS MOVED since the template
			// was committed, not whether the parent is the head (F-115,
			// the old public test network (chain 7320) 06:49 to 06:59Z, 14 October 2026: policy part 5
			// builds on the first-seen twin of the head at equal weight
			// and part 4 on the parent's parent, and the drop as first cut
			// refused every such seal as "not the head", so three sealers
			// dropped their 5873s and the chain stood 614 s on the twin at
			// 5872). A head unchanged since the template keeps the seal
			// whatever parent the policy chose; a head that moved means
			// the chain holds a newer block and the late seal is dropped.
			// F-117 (0.10.11's twin rate, 14 October 2026): the drop
			// is keyed on the POLICY'S parent now, not on the head: the
			// seal is kept when the sealer's own policy (parts 4 and 5,
			// policyParent) would still choose its parent this instant,
			// and dropped when it would choose another (the head moved
			// past it, or the twin pair it built on has settled), so a
			// late seal is refused as under F-112 and the first-seen
			// twin's successor, the twin race's resolution, is kept as
			// under F-115.
			// F-120's root (the network team's hours: 0.10.10 read share expiry
			// 0.038 and no sibling reorganisation, 0.10.11 and 0.10.12
			// 0.25 and 8 to 9 an hour): 0.10.10 dropped every seal whose
			// parent was not the HEAD at completion, which also dropped
			// the seals the sealer policy builds beside the head on
			// purpose (part 4's grandparent, part 5's first-seen twin),
			// and those are the twin race: a seal beside the head is a
			// sibling of the head by construction. 0.10.11 kept them to
			// end the stall (every sealer on the twin, every seal dropped,
			// 614 s). The form that keeps both: the strict drop (the
			// parent must be the head at completion), and after such a
			// drop the sealer YIELDS the policy for that height, building
			// its next template on the canonical head (policyYieldAt), so
			// no sealer can stand on a twin it cannot seal.
			if cur := w.chain.CurrentBlock(); cur != nil && cur.Hash() != block.ParentHash() {
				w.policyYieldAt.Store(block.NumberU64())
				log.Info("Sealed block dropped: its parent is not the head (F-112, F-120); the policy yields at this height, re-templating on the head", "number", block.Number(), "hash", hash, "parent", block.ParentHash(), "templateHead", task.head, "head", cur.Hash(), "headNumber", cur.Number)
				// The engine's seal-liveness mark for the pair (height,
				// parent) is cleared as on a refusal (F-116, the old public test network (chain 7320)
				// 07:58 to 08:1x Z, 14 October 2026: the dropped 6034s left
				// the pair marked, every later template on that parent
				// read "Height already sealed under this parent; no second
				// search", and the three sealers stood with hashrate 0).
				if cs, ok := w.engine.(interface {
					ClearSealed(uint64, common.Hash)
				}); ok {
					cs.ClearSealed(block.NumberU64(), block.ParentHash())
				}
				w.retry.Store(true)
				continue
			}
			// The share lane: the carried shares leave the pool with the
			// block that carries them (consensus/igneum/mineshares.go),
			// marked BEFORE the insert. The chain's side event for an own
			// block inserted beside a heavier twin reaches the share
			// backend as soon as the insert posts it, and its uncarry must
			// find the carried set: marked after the insert, the uncarry
			// ran first on nothing and the mark then held the shares out
			// of the pool for good (27 of a member's 174 at height 783 on
			// the ninth development chain, 12 October 2026, the node team's
			// identity-4 read). A refused block unmarks below; a block the
			// chain takes as its head is marked again by the head event,
			// which finds nothing left to move.
			mc, hasMark := w.engine.(interface {
				MarkCarried(consensus.ChainHeaderReader, *types.Block)
			})
			if hasMark {
				mc.MarkCarried(w.chain, block)
			}
			// The node's own block is never past-drifted at this node
			// (F-51): noted before the insert, ahead of the head event's
			// arrival record.
			if ob, ok := w.engine.(interface{ NoteOwnBlock(common.Hash, uint64) }); ok {
				ob.NoteOwnBlock(hash, block.NumberU64())
			}
			if _, err := w.chain.InsertChain(types.Blocks{block}); err != nil {
				if uc, ok := w.engine.(interface{ UnmarkCarried(*types.Block) }); ok {
					uc.UnmarkCarried(block)
				}
				log.Error("Sealed block REFUSED by this node's own validation (not written, not broadcast)", "number", block.Number(), "hash", hash, "sealhash", sealhash, "err", err)
				// The sealer re-templates at once (the recommit timer's
				// retry flag): without it the worker commits no new
				// template until a new head or a restart and stands at the
				// height (devnet7's stall at 350, 11 October 2026, F-42);
				// the next template drops the refused shares by name
				// (consensus/igneum/mineshares.go templateShares). The
				// engine's seal-liveness mark for the pair (height, parent)
				// is cleared, or no search would start on the re-cut
				// template ("Height already sealed under this parent; no
				// second search": the ninth development chain stood at
				// 110 from 02:05 UK on 12 October 2026 after one refusal).
				if cs, ok := w.engine.(interface {
					ClearSealed(uint64, common.Hash)
				}); ok {
					cs.ClearSealed(block.NumberU64(), block.ParentHash())
				}
				w.retry.Store(true)
				continue
			}
			log.Info("Successfully sealed new block", "number", block.Number(), "sealhash", sealhash, "hash", hash,
				"elapsed", common.PrettyDuration(time.Since(task.createdAt)))
			// An own block the chain takes BESIDE its head (a side block:
			// the fork rule kept the head at equal weight, as when policy
			// part 4 built this template on the parent's parent and the
			// first-seen twin won the height on the lot) posts no head
			// event, so without this the worker commits no new template
			// until a new head arrives from elsewhere: vectorwork2 stood
			// at 862 from 08:40 UTC on 13 October 2026 with three of its
			// four sealers idle after their 862s landed beside the head
			// (F-95). The sealer re-templates at once on the chain's head.
			if cur := w.chain.CurrentBlock(); cur != nil && cur.Hash() != hash {
				log.Info("Sealed block taken beside the head (not the head): re-templating on the head (F-95)", "number", block.Number(), "hash", hash, "head", cur.Hash())
				w.retry.Store(true)
			}

			// The withholding instrument (consensus/igneum/labmode.go, the
			// lab build's --dev.withhold for the guard's branch races): the
			// engine decides which blocks are broadcast now and which this
			// node holds back; with the instrument off it returns this block
			// for an immediate broadcast.
			broadcast := []*types.Block{block}
			if wh, ok := w.engine.(interface {
				Withhold(*types.Block) ([]*types.Block, bool)
			}); ok {
				broadcast, _ = wh.Withhold(block)
			}
			for _, b := range broadcast {
				// Broadcast the block and announce chain insertion event.
				// The line is the release's mark for the propagation rows
				// (the project's ruling of 04:50 UK, 12 October 2026, on the V2
				// window's reading): the block leaves this node here,
				// after its own import; the seal line precedes it by the
				// import alone.
				w.mux.Post(core.NewMinedBlockEvent{Block: b})
				log.Info("Block announced", "number", b.Number(), "hash", b.Hash(), "sinceStamp", time.Since(time.Unix(int64(b.Time()), 0)))
				// Insert the block into the set of pending ones to resultLoop for confirmations
				w.unconfirmed.Insert(b.NumberU64(), b.Hash())
			}

		case <-w.exitCh:
			return
		}
	}
}

// makeEnv creates a new environment for the sealing block.
func (w *worker) makeEnv(parent *types.Header, header *types.Header, coinbase common.Address) (*environment, error) {
	// Retrieve the parent state to execute on top and start a prefetcher for
	// the miner to speed block sealing up a bit.
	state, err := w.chain.StateAt(parent.Root)
	if err != nil {
		return nil, err
	}
	state.StartPrefetcher("miner")

	// Note the passed coinbase may be different with header.Coinbase.
	env := &environment{
		signer:   types.MakeSigner(w.chainConfig, header.Number, header.Time),
		state:    state,
		coinbase: coinbase,
		header:   header,
	}
	// Keep track of transactions which return errors so they can be removed
	env.tcount = 0
	return env, nil
}

// updateSnapshot updates pending snapshot block, receipts and state.
func (w *worker) updateSnapshot(env *environment) {
	w.snapshotMu.Lock()
	defer w.snapshotMu.Unlock()

	w.snapshotBlock = types.NewBlock(
		env.header,
		env.txs,
		nil,
		env.receipts,
		trie.NewStackTrie(nil),
	)
	w.snapshotReceipts = copyReceipts(env.receipts)
	w.snapshotState = env.state.Copy()
}

func (w *worker) commitTransaction(env *environment, tx *types.Transaction) ([]*types.Log, error) {
	receipt, err := w.applyTransaction(env, tx)
	if err != nil {
		return nil, err
	}
	env.txs = append(env.txs, tx)
	env.receipts = append(env.receipts, receipt)
	return receipt.Logs, nil
}

// applyTransaction runs the transaction. If execution fails, state and gas pool are reverted.
func (w *worker) applyTransaction(env *environment, tx *types.Transaction) (*types.Receipt, error) {
	var (
		snap = env.state.Snapshot()
		gp   = env.gasPool.Gas()
	)
	receipt, err := core.ApplyTransaction(w.chainConfig, w.chain, &env.coinbase, env.gasPool, env.state, env.header, tx, &env.header.GasUsed, *w.chain.GetVMConfig())
	if err != nil {
		env.state.RevertToSnapshot(snap)
		env.gasPool.SetGas(gp)
	}
	return receipt, err
}

func (w *worker) commitTransactions(env *environment, txs *transactionsByPriceAndNonce, interrupt *atomic.Int32) error {
	gasLimit := env.header.GasLimit
	if env.gasPool == nil {
		env.gasPool = new(core.GasPool).AddGas(gasLimit)
	}
	var coalescedLogs []*types.Log

	for {
		// Check interruption signal and abort building if it's fired.
		if interrupt != nil {
			if signal := interrupt.Load(); signal != commitInterruptNone {
				return signalToErr(signal)
			}
		}
		// If we don't have enough gas for any further transactions then we're done.
		if env.gasPool.Gas() < vars.TxGas {
			log.Trace("Not enough gas for further transactions", "have", env.gasPool, "want", vars.TxGas)
			break
		}
		// Retrieve the next transaction and abort if all done.
		ltx, _ := txs.Peek()
		if ltx == nil {
			break
		}
		// If we don't have enough space for the next transaction, skip the account.
		if env.gasPool.Gas() < ltx.Gas {
			log.Trace("Not enough gas left for transaction", "hash", ltx.Hash, "left", env.gasPool.Gas(), "needed", ltx.Gas)
			txs.Pop()
			continue
		}
		// Transaction seems to fit, pull it up from the pool
		tx := ltx.Resolve()
		if tx == nil {
			log.Trace("Ignoring evicted transaction", "hash", ltx.Hash)
			txs.Pop()
			continue
		}
		// Error may be ignored here. The error has already been checked
		// during transaction acceptance is the transaction pool.
		from, _ := types.Sender(env.signer, tx)

		// Check whether the tx is replay protected. If we're not in the EIP155 hf
		// phase, start ignoring the sender until we do.
		if tx.Protected() && !w.chainConfig.IsEnabled(w.chainConfig.GetEIP155Transition, env.header.Number) {
			log.Trace("Ignoring replay protected transaction", "hash", ltx.Hash, "eip155", w.chainConfig.GetEIP155Transition())
			txs.Pop()
			continue
		}
		// Start executing the transaction
		env.state.SetTxContext(tx.Hash(), env.tcount)

		logs, err := w.commitTransaction(env, tx)
		switch {
		case errors.Is(err, core.ErrNonceTooLow):
			// New head notification data race between the transaction pool and miner, shift
			log.Trace("Skipping transaction with low nonce", "hash", ltx.Hash, "sender", from, "nonce", tx.Nonce())
			txs.Shift()

		case errors.Is(err, nil):
			// Everything ok, collect the logs and shift in the next transaction from the same account
			coalescedLogs = append(coalescedLogs, logs...)
			env.tcount++
			txs.Shift()

		default:
			// Transaction is regarded as invalid, drop all consecutive transactions from
			// the same sender because of `nonce-too-high` clause.
			log.Debug("Transaction failed, account skipped", "hash", ltx.Hash, "err", err)
			txs.Pop()
		}
	}
	if !w.isRunning() && len(coalescedLogs) > 0 {
		// We don't push the pendingLogsEvent while we are sealing. The reason is that
		// when we are sealing, the worker will regenerate a sealing block every 3 seconds.
		// In order to avoid pushing the repeated pendingLog, we disable the pending log pushing.

		// make a copy, the state caches the logs and these logs get "upgraded" from pending to mined
		// logs by filling in the block hash when the block was mined by the local miner. This can
		// cause a race condition if a log was "upgraded" before the PendingLogsEvent is processed.
		cpy := make([]*types.Log, len(coalescedLogs))
		for i, l := range coalescedLogs {
			cpy[i] = new(types.Log)
			*cpy[i] = *l
		}
		w.pendingLogsFeed.Send(cpy)
	}
	return nil
}

// generateParams wraps the settings for generating a sealing task: the
// block's time and the fee recipient. The task always builds on the chain
// head (the merge-era payload building on a chosen parent left with PA-10).
type generateParams struct {
	timestamp uint64         // The timestamp for sealing task
	coinbase  common.Address // The fee recipient address for including transaction
}

// policyParent is the sealer policy's parent for a template on head: the
// head itself, or its parent under part 4, or its first-seen twin at equal
// weight under part 5 (the comments in the body). It is read at the
// template and AGAIN at the seal's completion (F-117): a seal is kept when
// the policy still names its parent, dropped when it names another (the
// head moved, the twin pair settled), so the drop refuses only what the
// sealer's own policy already refused and never the twin race's resolution.
func (w *worker) policyParent(head *types.Header, verbose bool) *types.Header {
	parent := head
	// F-120: the policy yields at a height where this node's seal beside
	// the head was dropped; the template is the head's child.
	if y := w.policyYieldAt.Load(); y != 0 && y == head.Number.Uint64()+1 {
		return parent
	}
	// Policy part 4 (the past-drift refusal, the design seat's ruling of
	// 02:4x UK, 12 October 2026, with its two bounds): a head whose stamp
	// is older than its live arrival by more than one round is not built
	// on; the sealer builds on its parent instead, ONE height back only (a
	// deeper past-stamped branch is the weight's to settle under F-44's
	// parts 1 and 2a). Policy part 5 (the ruling of 03:5x UK): among twins
	// of equal height and equal cumulative weight (before any child's
	// carry distinguishes them) the sealer builds on the one its own
	// first-seen record names, the one it endorsed, so the endorsed twin
	// wins by the carried weight within one height; the canonical-head
	// rule for import, fork choice and sync is untouched (PA-47's lot term
	// stays the importer's tie), the sealer merely builds one height on a
	// twin that is not its canonical head, which validity allows.
	// Both parts from PolicyV1 at the child's height (node/0.9.3's gate;
	// zero in the config is off): below it the canonical head as before.
	policy := true
	if po, ok := w.engine.(interface{ PolicyOn(uint64) bool }); ok {
		policy = po.PolicyOn(parent.Number.Uint64() + 1)
	}
	pd, hasDrift := w.engine.(interface{ PastDrifted(*types.Header) bool })
	hasDrift = hasDrift && policy
	if hasDrift && parent.Number.Sign() > 0 && pd.PastDrifted(parent) {
		if gp := w.chain.GetHeader(parent.ParentHash, parent.Number.Uint64()-1); gp != nil {
			// The walk-back never stands the sealer (F-51, the design
			// seat's bound (iii)): a (height, grandparent) pair this node
			// sealed already is not searched twice, so the template stays
			// on the head and the policy yields for this node at this
			// height rather than hold it at "Height already sealed under
			// this parent" (the scratch pair's full stall, 12 October
			// 2026; pod d at 1021).
			if hs, ok := w.engine.(interface {
				HeightSealed(uint64, common.Hash) bool
			}); ok && hs.HeightSealed(parent.Number.Uint64(), gp.Hash()) {
				if verbose {
					log.Info("Template stays on a past-drifted head: this node sealed the height under the parent's parent already (policy part 4 yields)", "head", parent.Number, "hash", parent.Hash())
				}
			} else {
				if verbose {
					log.Info("Template built on the parent's parent: the head's stamp is older than its arrival by more than a round (policy part 4)", "head", parent.Number, "hash", parent.Hash())
				}
				parent = gp
			}
		}
	}
	if fs, ok := w.engine.(interface {
		FirstSeenAt(uint64) (common.Hash, bool)
	}); ok && policy {
		n := parent.Number.Uint64()
		if first, ok := fs.FirstSeenAt(n); ok && first != parent.Hash() {
			// A first-seen twin that is itself past-drifted is not built
			// on either (part 4 reads before part 5).
			if twin := w.chain.GetHeader(first, n); twin != nil && !(hasDrift && pd.PastDrifted(twin)) {
				// "Equal weight" is the chain's weight rule: under the
				// vector form the engine's (the per-coordinate work;
				// F-88, vectorwork1 height 31, 13 October 2026: the
				// sealers built on the first-seen twin of lower vector
				// weight because this read the total difficulty), else
				// the total difficulty.
				if w.equalWeightHeaders(parent, twin) {
					if verbose {
						log.Info("Template built on the first-seen twin of the head at equal weight (policy part 5)", "number", n, "head", parent.Hash(), "twin", first)
					}
					parent = twin
				}
			}
		}
	}
	return parent
}

// prepareWork constructs the sealing task according to the given parameters
// on the last chain head. In this function the pending transactions are not
// filled yet, only the empty task returned.
func (w *worker) prepareWork(genParams *generateParams) (*environment, error) {
	w.mu.RLock()
	defer w.mu.RUnlock()

	parent := w.policyParent(w.chain.CurrentBlock(), true)
	// Sanity check the timestamp correctness, recap the timestamp
	// to parent+1 if the mutation is allowed.
	timestamp := genParams.timestamp
	// Never seal ahead of the wall clock: a cheap algorithm (the null stand-in
	// on a devnet) seals faster than one block a second, and every header's Time
	// must exceed its parent's, so without this wait the chain's timestamps run
	// into the future and every peer drops the blocks (consensus: MaxFutureDrift).
	// The earliest time for the new block is the parent's plus one second, or
	// plus the configured minimum interval (--dev.period) when that is longer.
	earliest := parent.Time + 1
	if gap := uint64(w.config.MinInterval / time.Second); gap > 1 {
		earliest = parent.Time + gap
	}
	if now := uint64(time.Now().Unix()); earliest > now {
		if wait := time.Duration(earliest-now) * time.Second; wait <= 10*time.Minute {
			time.Sleep(wait)
		}
		timestamp = uint64(time.Now().Unix())
	}
	// A template never carries a time older than the clock: a resubmitted
	// template (the recommit timer, the engine's retry) took the timestamp
	// of the last head event, so a sealer waiting out a round's time floor
	// under the sortition prototype waited forever (11 October 2026).
	if now := uint64(time.Now().Unix()); timestamp < now {
		timestamp = now
	}
	if parent.Time >= timestamp {
		timestamp = parent.Time + 1
	}
	// Construct the sealing block header.
	header := &types.Header{
		ParentHash: parent.Hash(),
		Number:     new(big.Int).Add(parent.Number, common.Big1),
		GasLimit:   core.CalcGasLimit(parent.GasLimit, w.config.GasCeil),
		Time:       timestamp,
		Coinbase:   genParams.coinbase,
	}
	// Set the extra field.
	if len(w.extra) != 0 {
		header.Extra = w.extra
	}
	// Set baseFee and GasLimit if we are on an EIP-1559 chain
	if w.chainConfig.IsEnabled(w.chainConfig.GetEIP1559Transition, header.Number) {
		header.BaseFee = eip1559.CalcBaseFee(w.chainConfig, parent)
		if !w.chainConfig.IsEnabled(w.chainConfig.GetEIP1559Transition, parent.Number) {
			parentGasLimit := parent.GasLimit * w.chainConfig.GetElasticityMultiplier()
			header.GasLimit = core.CalcGasLimit(parentGasLimit, w.config.GasCeil)
		}
	}
	// Run the consensus preparation with the default or customized consensus engine.
	if err := w.engine.Prepare(w.chain, header); err != nil {
		// A WAIT (the round's floor, the carry bound, a node with no bond
		// key) is not a fault: once per height at Info, then per tick at
		// Debug (the ninth chain's logs read 2,955 Error lines of it on a
		// sealer and one every two seconds on every non-sealing node).
		if igneum.IsTemplateWait(err) {
			if h := header.Number.Uint64(); w.waitLogged != h {
				w.waitLogged = h
				log.Info("Template waits (the round's floor, the carry bound or no bond key); asked again at the next tick", "number", h, "why", err)
			} else {
				log.Debug("Template waits", "number", h, "why", err)
			}
		} else if !w.isRunning() {
			// F-521 (17 October 2026): the pending block is assembled on
			// every chain head whether or not this node seals (the
			// pending state's reader), so a reader under --mine=false
			// prepared a template on a retired coordinate at every block
			// and wrote an Error a block for two days (A12: 72,813 lines).
			// A node that is not sealing needs no template: the refusal
			// is the reader's state, logged at Debug.
			log.Debug("Template not prepared on a node that is not sealing", "err", err)
		} else {
			log.Error("Failed to prepare header for sealing", "err", err)
		}
		w.retry.Store(true)
		return nil, err
	}
	// The block DAG ordering (consensus/igneum/dagform.go): the template
	// names every other tip this node holds as a merged parent.
	if tp, ok := w.engine.(interface {
		DAGOn(uint64) bool
		TemplateParents(consensus.ChainHeaderReader, *types.Header) []common.Hash
	}); ok && tp.DAGOn(header.Number.Uint64()) {
		header.SetMergedParents(tp.TemplateParents(w.chain, parent))
	}
	// The sortition prototype's payout list: its root in the header and
	// the root's prefix as the coinbase, before the template is signed.
	// Under the share lane (consensus/igneum/mineshares.go) the list is
	// DERIVED from the carried shares and the state's credits at the
	// parent's state (the sealer's own entry last, at the template's
	// coinbase), the carried shares committed by the header's SharesRoot;
	// miner_setPayoutList's list is not consulted under the lane.
	var payouts []*types.Withdrawal
	if ts, ok := w.engine.(interface {
		SharesOn(uint64) bool
		TemplateShares(chain consensus.ChainHeaderReader, parent *types.Header, st *state.StateDB, sealer common.Address) ([]byte, []*types.Withdrawal, error)
	}); ok && ts.SharesOn(header.Number.Uint64()) {
		pst, err := w.chain.StateAt(parent.Root)
		if err != nil {
			log.Error("Failed to read the parent's state for the share lane", "err", err)
			w.retry.Store(true)
			return nil, err
		}
		entries, list, err := ts.TemplateShares(w.chain, parent, pst, header.Coinbase)
		if err != nil {
			if igneum.IsTemplateWait(err) {
				// The empty-list hold after a reorganisation (mineshares.go).
				if h := header.Number.Uint64(); w.waitLogged != h {
					w.waitLogged = h
					log.Info("Template waits (the share lane's hold after a reorganisation); asked again at the next tick", "number", h, "why", err)
				} else {
					log.Debug("Template waits", "number", h, "why", err)
				}
			} else {
				log.Error("Failed to derive the template's payout list", "err", err)
			}
			w.retry.Store(true)
			return nil, err
		}
		if len(entries) > 0 {
			root := types.MinedSharesRoot(entries)
			header.SharesRoot = &root
		}
		if len(list) > 0 {
			payouts = list
			root := types.DeriveSha(types.Withdrawals(payouts), trie.NewStackTrie(nil))
			header.WithdrawalsHash = &root
			header.Coinbase = common.BytesToAddress(root[:20])
		}
	} else if l, ok := w.engine.(interface {
		AllowsPayoutList(*types.Header) bool
	}); ok && l.AllowsPayoutList(header) && len(w.payouts) > 0 {
		payouts = make([]*types.Withdrawal, len(w.payouts))
		copy(payouts, w.payouts)
		root := types.DeriveSha(types.Withdrawals(payouts), trie.NewStackTrie(nil))
		header.WithdrawalsHash = &root
		header.Coinbase = common.BytesToAddress(root[:20])
	}
	// Could potentially happen if starting to mine in an odd state.
	// The fee recipient of the block's transactions is the header's
	// coinbase in both paths (core.StateProcessor pays the tip to
	// header.Coinbase): under a payout list that is the list root's prefix,
	// never the etherbase, or the sealer's state root differs from every
	// verifier's (security's F-35: the fourth development chain split
	// three ways at 2185 on the first pool-listed block that carried a
	// transaction, 11 October 2026). Finalize then pays the prefix's
	// balance pro rata to the list beside the subsidy.
	env, err := w.makeEnv(parent, header, header.Coinbase)
	if env != nil {
		env.payouts = payouts
	}
	if err != nil {
		log.Error("Failed to create sealing context", "err", err)
		return nil, err
	}
	// The block DAG ordering: the mergeset's transactions applied to the
	// parent state before the template's own (core.ApplyMergeset, the
	// same function the import runs).
	if core.DAGOn(w.engine, header.Number.Uint64()) {
		if _, err := core.ApplyMergeset(w.chainConfig, w.chain, w.engine, header, env.state, *w.chain.GetVMConfig()); err != nil {
			log.Error("Failed to apply the template's mergeset", "err", err)
			w.retry.Store(true)
			return nil, err
		}
	}
	return env, nil
}

// fillTransactions retrieves the pending transactions from the txpool and fills them
// into the given sealing block. The transaction selection and ordering strategy can
// be customized with the plugin in the future.
func (w *worker) fillTransactions(interrupt *atomic.Int32, env *environment) error {
	w.mu.RLock()
	tip := w.tip
	w.mu.RUnlock()

	// Retrieve the pending transactions pre-filtered by the 1559/4844 dynamic fees
	filter := txpool.PendingFilter{
		MinTip: tip,
	}
	if env.header.BaseFee != nil {
		filter.BaseFee = uint256.MustFromBig(env.header.BaseFee)
	}
	filter.OnlyPlainTxs = true
	pendingPlainTxs := w.eth.TxPool().Pending(filter)

	// Split the pending transactions into locals and remotes.
	localPlainTxs, remotePlainTxs := make(map[common.Address][]*txpool.LazyTransaction), pendingPlainTxs

	for _, account := range w.eth.TxPool().Locals() {
		if txs := remotePlainTxs[account]; len(txs) > 0 {
			delete(remotePlainTxs, account)
			localPlainTxs[account] = txs
		}
	}
	// Fill the block with all available pending transactions.
	if len(localPlainTxs) > 0 {
		plainTxs := newTransactionsByPriceAndNonce(env.signer, localPlainTxs, env.header.BaseFee)
		if err := w.commitTransactions(env, plainTxs, interrupt); err != nil {
			return err
		}
	}
	if len(remotePlainTxs) > 0 {
		plainTxs := newTransactionsByPriceAndNonce(env.signer, remotePlainTxs, env.header.BaseFee)
		if err := w.commitTransactions(env, plainTxs, interrupt); err != nil {
			return err
		}
	}
	return nil
}

// commitWork generates several new sealing tasks based on the parent block
// and submit them to the sealer.
func (w *worker) commitWork(interrupt *atomic.Int32, noempty bool, timestamp int64) {
	// Abort committing if node is still syncing
	if w.syncing.Load() {
		return
	}
	start := time.Now()

	// Set the coinbase if the worker is running or it's required
	var coinbase common.Address
	if w.isRunning() {
		coinbase = w.etherbase()
		if coinbase == (common.Address{}) {
			log.Error("Refusing to mine without etherbase")
			return
		}
	}
	work, err := w.prepareWork(&generateParams{
		timestamp: uint64(timestamp),
		coinbase:  coinbase,
	})
	if err != nil {
		return
	}
	// Create an empty block based on temporary copied state for
	// sealing in advance without waiting block execution finished.
	if !noempty && !w.noempty.Load() {
		w.commit(work.copy(), nil, false, start)
	}
	// Fill pending transactions from the txpool into the block.
	err = w.fillTransactions(interrupt, work)
	switch {
	case err == nil:
		// The entire block is filled, decrease resubmit interval in case
		// of current interval is larger than the user-specified one.
		w.adjustResubmitInterval(&intervalAdjust{inc: false})

	case errors.Is(err, errBlockInterruptedByRecommit):
		// Notify resubmit loop to increase resubmitting interval if the
		// interruption is due to frequent commits.
		gaslimit := work.header.GasLimit
		ratio := float64(gaslimit-work.gasPool.Gas()) / float64(gaslimit)
		if ratio < 0.1 {
			ratio = 0.1
		}
		w.adjustResubmitInterval(&intervalAdjust{
			ratio: ratio,
			inc:   true,
		})

	case errors.Is(err, errBlockInterruptedByNewHead):
		// If the block building is interrupted by newhead event, discard it
		// totally. Committing the interrupted block introduces unnecessary
		// delay, and possibly causes miner to mine on the previous head.
		work.discard()
		return
	}
	// Submit the generated block for consensus sealing.
	w.commit(work.copy(), w.fullTaskHook, true, start)

	// Swap out the old work with the new one, terminating any leftover
	// prefetcher processes in the mean time and starting a new one.
	if w.current != nil {
		w.current.discard()
	}
	w.current = work
}

// commit runs any post-transaction state modifications, assembles the final block
// and commits new work if consensus engine is running.
// Note the assumption is held that the mutation is allowed to the passed env, do
// the deep copy first.
func (w *worker) commit(env *environment, interval func(), update bool, start time.Time) error {
	if w.isRunning() {
		if interval != nil {
			interval()
		}
		// Create a local environment copy, avoid the data race with snapshot state.
		// https://github.com/ethereum/go-ethereum/issues/24299
		env := env.copy()
		// No uncles and no withdrawals on Igneum (D-A4, DESIGN.md 6.3).
		block, err := w.engine.FinalizeAndAssemble(w.chain, env.header, env.state, env.txs, nil, env.receipts, env.payouts)
		if err != nil {
			// A template the engine refuses (the work-authority prototype's
			// own-block check, or its round wait) is tried again at the
			// next recommit instead of silently never (a sealer whose
			// coinbase was set after its start sat idle, 11 October 2026).
			log.Warn("Engine refused the template; retrying at the next commit", "err", err)
			w.retry.Store(true)
			return err
		}
		{
			select {
			case w.taskCh <- &task{receipts: env.receipts, state: env.state, block: block, createdAt: time.Now(), head: w.chain.CurrentBlock().Hash()}:
				w.unconfirmed.Shift(block.NumberU64() - 1)

				fees := totalFees(block, env.receipts)
				feesInEther := new(big.Float).Quo(new(big.Float).SetInt(fees), big.NewFloat(vars.Ether))
				log.Info("Commit new sealing work", "number", block.Number(), "sealhash", w.engine.SealHash(block.Header()),
					"txs", env.tcount,
					"gas", block.GasUsed(), "fees", feesInEther,
					"elapsed", common.PrettyDuration(time.Since(start)))

			case <-w.exitCh:
				log.Info("Worker has exited")
			}
		}
	}
	if update {
		w.updateSnapshot(env)
	}
	return nil
}

// adjustResubmitInterval adjusts the resubmit interval.
func (w *worker) adjustResubmitInterval(message *intervalAdjust) {
	select {
	case w.resubmitAdjustCh <- message:
	default:
		log.Warn("the resubmitAdjustCh is full, discard the message")
	}
}

// copyReceipts makes a deep copy of the given receipts.
func copyReceipts(receipts []*types.Receipt) []*types.Receipt {
	result := make([]*types.Receipt, len(receipts))
	for i, l := range receipts {
		cpy := *l
		result[i] = &cpy
	}
	return result
}

// totalFees computes total consumed miner fees in Wei. Block transactions and receipts have to have the same order.
func totalFees(block *types.Block, receipts []*types.Receipt) *big.Int {
	feesWei := new(big.Int)
	for i, tx := range block.Transactions() {
		minerFee, _ := tx.EffectiveGasTip(block.BaseFee())
		feesWei.Add(feesWei, new(big.Int).Mul(new(big.Int).SetUint64(receipts[i].GasUsed), minerFee))
	}
	return feesWei
}

// signalToErr converts the interruption signal to a concrete error type for return.
// The given signal must be a valid interruption signal.
func signalToErr(signal int32) error {
	switch signal {
	case commitInterruptNewHead:
		return errBlockInterruptedByNewHead
	case commitInterruptResubmit:
		return errBlockInterruptedByRecommit
	default:
		panic(fmt.Errorf("undefined signal %d", signal))
	}
}

// equalWeightHeaders is policy part 5's "equal weight": the chain's weight
// rule, under the vector form the engine's (the per-coordinate work; F-88,
// vectorwork1 height 31, 13 October 2026: the sealers built on the
// first-seen twin of lower vector weight because this read the total
// difficulty), else the total difficulty.
func (w *worker) equalWeightHeaders(a, b *types.Header) bool {
	if ew, ok := w.engine.(interface {
		EqualWeight(consensus.ChainHeaderReader, *types.Header, *types.Header) (bool, bool, error)
	}); ok {
		if eq, decided, err := ew.EqualWeight(w.chain, a, b); decided {
			return eq && err == nil
		}
	}
	tdA, tdB := w.chain.GetTd(a.Hash(), a.Number.Uint64()), w.chain.GetTd(b.Hash(), b.Number.Uint64())
	return tdA != nil && tdB != nil && tdA.Cmp(tdB) == 0
}
