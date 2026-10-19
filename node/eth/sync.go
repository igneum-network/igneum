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

package eth

import (
	"errors"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/downloader"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/log"
)

const (
	forceSyncCycle      = 10 * time.Second // Time interval to force syncs, even if few peers are available
	defaultMinSyncPeers = 5                // Amount of peers desired to start syncing
)

var ()

// syncTransactions starts sending all currently pending transactions to the given peer.
func (h *handler) syncTransactions(p *eth.Peer) {
	var hashes []common.Hash
	for _, batch := range h.txpool.Pending(txpool.PendingFilter{OnlyPlainTxs: true}) {
		for _, tx := range batch {
			hashes = append(hashes, tx.Hash)
		}
	}
	if len(hashes) == 0 {
		return
	}
	p.AsyncSendPooledTransactionHashes(hashes)
}

// chainSyncer coordinates blockchain sync components.
type chainSyncer struct {
	probed      map[string]probedHead // F-143: the rejoin probe's last head per peer
	handler     *handler
	force       *time.Timer
	forced      bool // true when force timer fired
	warned      time.Time
	peerEventCh chan struct{}
	doneCh      chan error // non-nil when sync is running
}

// chainSyncOp is a scheduled sync operation.
type chainSyncOp struct {
	mode downloader.SyncMode
	peer *eth.Peer
	td   *big.Int
	head common.Hash
}

// newChainSyncer creates a chainSyncer.
func newChainSyncer(handler *handler) *chainSyncer {
	return &chainSyncer{
		handler:     handler,
		peerEventCh: make(chan struct{}),
	}
}

// handlePeerEvent notifies the syncer about a change in the peer set.
// This is called for new peers and every time a peer announces a new
// chain head.
func (cs *chainSyncer) handlePeerEvent() bool {
	select {
	case cs.peerEventCh <- struct{}{}:
		return true
	case <-cs.handler.quitSync:
		return false
	}
}

// loop runs in its own goroutine and launches the sync when necessary.
func (cs *chainSyncer) loop() {
	defer cs.handler.wg.Done()

	cs.handler.blockFetcher.Start()
	cs.handler.txFetcher.Start()
	defer cs.handler.blockFetcher.Stop()
	defer cs.handler.txFetcher.Stop()
	defer cs.handler.downloader.Terminate()

	// The force timer lowers the peer count threshold down to one when it fires.
	// This ensures we'll always start sync even if there aren't enough peers.
	cs.force = time.NewTimer(forceSyncCycle)
	defer cs.force.Stop()

	for {
		if op := cs.nextSyncOp(); op != nil {
			cs.startSync(op)
		}
		select {
		case <-cs.peerEventCh:
			// Peer information changed, recheck.
		case err := <-cs.doneCh:
			cs.doneCh = nil
			cs.force.Reset(forceSyncCycle)
			cs.forced = false

			// If we've reached the merge transition but no beacon client is available, or
			// it has not yet switched us over, keep warning the user that their infra is
			// potentially flaky.
			if errors.Is(err, downloader.ErrMergeTransition) && time.Since(cs.warned) > 10*time.Second {
				log.Warn("Local chain is post-merge, waiting for beacon client sync switch-over...")
				cs.warned = time.Now()
			}
		case <-cs.force.C:
			cs.forced = true

		case <-cs.handler.quitSync:
			// Disable all insertion on the blockchain. This needs to happen before
			// terminating the downloader because the downloader waits for blockchain
			// inserts, and these can take a long time to finish.
			cs.handler.chain.StopInsert()
			cs.handler.downloader.Terminate()
			if cs.doneCh != nil {
				<-cs.doneCh
			}
			return
		}
	}
}

// nextSyncOp determines whether sync is required at this time.
func (cs *chainSyncer) nextSyncOp() *chainSyncOp {
	if cs.doneCh != nil {
		return nil // Sync already running
	}
	// If a beacon client once took over control, disable the entire legacy sync
	// path from here on end. Note, there is a slight "race" between reaching TTD
	// and the beacon client taking over. The downloader will enforce that nothing
	// above the first TTD will be delivered to the chain for import.
	//
	// An alternative would be to check the local chain for exceeding the TTD and
	// avoid triggering a sync in that case, but that could also miss sibling or
	// other family TTD block being accepted.
	// Ensure we're at minimum peer count.
	minPeers := defaultMinSyncPeers
	if cs.forced {
		minPeers = 1
	} else if minPeers > cs.handler.maxPeers {
		minPeers = cs.handler.maxPeers
	}
	if cs.handler.peers.len() < minPeers {
		return nil
	}
	// We have enough peers, pick the one with the highest TD, but avoid going
	// over the terminal total difficulty. Above that we expect the consensus
	// clients to direct the chain head to sync to.
	peer := cs.handler.peers.peerWithHighestTD()
	if peer == nil {
		return nil
	}
	mode, ourTD := cs.modeAndLocalHead()
	op := peerToSyncOp(mode, peer)
	// F-182: a peer ahead by at most one block (its total difficulty over
	// ours by no more than its head block's difficulty) is the fetcher's
	// to follow (the block pushed whole or announced and requested at
	// once, its parent on a one-block gap too), never the downloader's (a
	// sync of the branch, seconds, for one block: 18 of the research
	// chain's 283 late imports went that way).
	// F-199 (16 October 2026): the one-block rule holds only when this node
	// HOLDS the peer's head block (handleBlockBroadcast sets a peer's head
	// to its last pushed block's parent, so holding it is the true
	// one-block lead the fetcher closes); a peer whose head this node lacks
	// is on a branch this node does not have (a sibling of this node's head
	// after a restart, or a longer gap), and the downloader takes it as
	// before F-182: two of the research chain's eight sat on a sibling head
	// for two and a half minutes after a roll, the peers' lead of seven
	// small-difficulty blocks under one head's difficulty, every pushed
	// child dropped as an unknown parent.
	if head, _, headDiff := peer.Head(); fetcherLead(func(h common.Hash) bool { return cs.handler.chain.GetBlockByHash(h) != nil }, head, headDiff, op.td, ourTD) {
		return nil
	}
	if op.td.Cmp(ourTD) <= 0 {
		// F-143 (the research chain, 15 October 2026): under a fork rule
		// that is not the total difficulty's (VectorWork's coverage rule,
		// the engine's PreferByWeight) a node ahead on a lighter branch
		// read every peer's head as "in sync" by TD and never fetched the
		// heavier-by-rule branch (the fetcher drops an announcement more
		// than seven below its head), so a resynced node forked for good.
		// The REJOIN PROBE: a peer whose head this node does not hold is
		// synced from once per head whatever its TD; the downloader fetches
		// its branch to the common ancestor and the chain's own fork choice
		// (the forker's PreferByWeight) keeps or takes it.
		if cs.weightRule() {
			if probe := cs.rejoinPeer(); probe != nil {
				h, _, _ := probe.Head()
				log.Info("Rejoin probe (F-143): a peer's head is not on this chain; syncing its branch for the fork rule's own choice", "peer", probe.ID(), "head", h, "ourTD", ourTD)
				return peerToSyncOp(mode, probe)
			}
		}
		// We seem to be in sync according to the legacy rules. In the merge
		// world, it can also mean we're stuck on the merge block, waiting for
		// a beacon client. In the latter case, notify the user.
		if ttd := cs.handler.chain.Config().GetEthashTerminalTotalDifficulty(); ttd != nil && ourTD.Cmp(ttd) >= 0 && time.Since(cs.warned) > 10*time.Second {
			log.Warn("Local chain is post-merge, waiting for beacon client sync switch-over...")
			cs.warned = time.Now()
		}
		return nil // We're in sync.
	}
	return op
}

// weightRule reports whether the chain's engine decides forks by its own
// weight rule (consensus/igneum's PreferByWeight), not the total difficulty.
// fetcherLead reports whether a peer's lead is the fetcher's to close rather
// than the downloader's (F-182): the peer is ahead by at most its head
// block's difficulty AND this node holds the peer's head block (F-199, 16
// October 2026: handleBlockBroadcast sets a peer's head to its last pushed
// block's parent, so holding it is the true one-block lead; a head this
// node lacks is a branch it does not have, a sibling of its own head after a
// restart or a longer gap, and the downloader takes it as before F-182).
func fetcherLead(has func(common.Hash) bool, head common.Hash, headDiff, peerTD, ourTD *big.Int) bool {
	if headDiff == nil || headDiff.Sign() <= 0 || peerTD == nil || ourTD == nil {
		return false
	}
	if peerTD.Cmp(ourTD) <= 0 || new(big.Int).Sub(peerTD, ourTD).Cmp(headDiff) > 0 {
		return false
	}
	return has(head)
}

func (cs *chainSyncer) weightRule() bool {
	type weighted interface {
		PreferByWeight(chain consensus.ChainHeaderReader, current, extern *types.Header) (reorg, decided bool, err error)
	}
	if _, ok := cs.handler.chain.Engine().(weighted); !ok {
		return false
	}
	// The rule decides only on a chain with the vector form; the engine
	// answers decided false elsewhere, read once on the head.
	head := cs.handler.chain.CurrentHeader()
	if head == nil {
		return false
	}
	_, decided, _ := cs.handler.chain.Engine().(weighted).PreferByWeight(cs.handler.chain, head, head)
	return decided
}

// rejoinPeer is the F-143 probe's choice: a peer whose announced head this
// node does not hold and has not probed in the last minute (the same head
// is not fetched twice; a refused branch stays refused until the peer's
// head moves).
func (cs *chainSyncer) rejoinPeer() *eth.Peer {
	if cs.probed == nil {
		cs.probed = make(map[string]probedHead)
	}
	now := time.Now()
	has := func(h common.Hash) bool { return cs.handler.chain.GetHeaderByHash(h) != nil }
	return cs.handler.peers.peerWithUnknownHead(func(p *eth.Peer, head common.Hash) bool {
		return rejoinTake(has, cs.probed, p.ID(), head, now)
	})
}

// rejoinTake is the probe's rule for one peer: its head not held by this
// node and not probed from this peer within the last minute; a taken peer
// is marked.
func rejoinTake(has func(common.Hash) bool, probed map[string]probedHead, id string, head common.Hash, now time.Time) bool {
	if has(head) {
		return false
	}
	if last, ok := probed[id]; ok && last.hash == head && now.Sub(last.at) < time.Minute {
		return false
	}
	probed[id] = probedHead{hash: head, at: now}
	return true
}

// probedHead is the last head the rejoin probe synced from a peer.
type probedHead struct {
	hash common.Hash
	at   time.Time
}

func peerToSyncOp(mode downloader.SyncMode, p *eth.Peer) *chainSyncOp {
	peerHead, peerTD, _ := p.Head()
	return &chainSyncOp{mode: mode, peer: p, td: peerTD, head: peerHead}
}

func (cs *chainSyncer) modeAndLocalHead() (downloader.SyncMode, *big.Int) {
	// If we're in snap sync mode, return that directly
	if cs.handler.snapSync.Load() {
		block := cs.handler.chain.CurrentSnapBlock()
		td := cs.handler.chain.GetTd(block.Hash(), block.Number.Uint64())
		return downloader.SnapSync, td
	}
	// We are probably in full sync, but we might have rewound to before the
	// snap sync pivot, check if we should re-enable snap sync.
	head := cs.handler.chain.CurrentBlock()
	if pivot := rawdb.ReadLastPivotNumber(cs.handler.database); pivot != nil {
		if head.Number.Uint64() < *pivot {
			block := cs.handler.chain.CurrentSnapBlock()
			td := cs.handler.chain.GetTd(block.Hash(), block.Number.Uint64())
			return downloader.SnapSync, td
		}
	}
	// We are in a full sync, but the associated head state is missing. To complete
	// the head state, forcefully rerun the snap sync. Note it doesn't mean the
	// persistent state is corrupted, just mismatch with the head block.
	if !cs.handler.chain.HasState(head.Root) {
		block := cs.handler.chain.CurrentSnapBlock()
		td := cs.handler.chain.GetTd(block.Hash(), block.Number.Uint64())
		log.Info("Reenabled snap sync as chain is stateless")
		return downloader.SnapSync, td
	}
	// Nope, we're really full syncing
	td := cs.handler.chain.GetTd(head.Hash(), head.Number.Uint64())
	return downloader.FullSync, td
}

// startSync launches doSync in a new goroutine.
func (cs *chainSyncer) startSync(op *chainSyncOp) {
	cs.doneCh = make(chan error, 1)
	go func() { cs.doneCh <- cs.handler.doSync(op) }()
}

// doSync synchronizes the local blockchain with a remote peer.
func (h *handler) doSync(op *chainSyncOp) error {
	// Run the sync cycle, and disable snap sync if we're past the pivot block
	err := h.downloader.LegacySync(op.peer.ID(), op.head, op.td, h.chain.Config().GetEthashTerminalTotalDifficulty(), op.mode)
	if err != nil {
		return err
	}
	h.enableSyncedFeatures()

	head := h.chain.CurrentBlock()
	// Sanity check the timestamp before calling the node synced (a fallback
	// for private networks whose genesis is ancient).
	if head.Time >= uint64(time.Now().AddDate(0, -1, 0).Unix()) {
		h.synced.Store(true)
	}
	if head.Number.Uint64() > 0 {
		// We've completed a sync cycle, notify all peers of new state. This path is
		// essential in star-topology networks where a gateway node needs to notify
		// all its out-of-date peers of the availability of a new block. This failure
		// scenario will most often crop up in private and hackathon networks with
		// degenerate connectivity, but it should be healthy for the mainnet too to
		// more reliably update peers or the local TD state.
		if block := h.chain.GetBlock(head.Hash(), head.Number.Uint64()); block != nil {
			h.BroadcastBlock(block, false)
		}
	}
	return nil
}
