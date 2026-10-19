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

// Package fetcher contains the announcement based header, blocks or transaction synchronisation.
package fetcher

import (
	"errors"
	"math/rand"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/prque"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/trie"
)

// InsertChainEvent is posted by the handler when a propagated block is successfully imported.
// The block may have been propagated via announcement (hashes) or broadcast (full block, via its miner).
// The event should not be posted if the insert function returns any error.
type InsertChainEvent struct {
	Blocks types.Blocks
}

const (
	lightTimeout = time.Millisecond // Time allowance before an announced header is explicitly requested
	// F-182 (15 October 2026): an announced block is requested AT ONCE,
	// no allowance for the body to arrive by broadcast and no collation
	// slack (Ethereum's 500 ms and 100 ms; on the research chain's
	// eight-node mesh the wait, the two request round trips and the
	// downloader's fallback for a one-block gap put a direct peer's import
	// a median 0.85 s and p90 9 s after the seal, most of the sibling
	// window; the import work itself a median 84 ms).
	arriveTimeout = 0 * time.Millisecond // Time allowance before an announced block/transaction is explicitly requested
	gatherSlack   = 0 * time.Millisecond // Interval used to collate almost-expired announces with fetches
	fetchTimeout  = 5 * time.Second      // Maximum allotted time to return an explicitly requested block/transaction
)

const (
	maxUncleDist = 7   // Maximum allowed backward distance from the chain head
	maxQueueDist = 32  // Maximum allowed distance from the chain head to queue
	hashLimit    = 256 // Maximum number of unique blocks or headers a peer may have announced
	blockLimit   = 64  // Maximum number of unique blocks a peer may have delivered
)

var (
	blockAnnounceInMeter   = metrics.NewRegisteredMeter("eth/fetcher/block/announces/in", nil)
	blockAnnounceOutTimer  = metrics.NewRegisteredTimer("eth/fetcher/block/announces/out", nil)
	blockAnnounceDropMeter = metrics.NewRegisteredMeter("eth/fetcher/block/announces/drop", nil)
	blockAnnounceDOSMeter  = metrics.NewRegisteredMeter("eth/fetcher/block/announces/dos", nil)

	blockBroadcastInMeter   = metrics.NewRegisteredMeter("eth/fetcher/block/broadcasts/in", nil)
	blockBroadcastOutTimer  = metrics.NewRegisteredTimer("eth/fetcher/block/broadcasts/out", nil)
	blockBroadcastDropMeter = metrics.NewRegisteredMeter("eth/fetcher/block/broadcasts/drop", nil)
	blockBroadcastDOSMeter  = metrics.NewRegisteredMeter("eth/fetcher/block/broadcasts/dos", nil)

	headerFetchMeter = metrics.NewRegisteredMeter("eth/fetcher/block/headers", nil)
	bodyFetchMeter   = metrics.NewRegisteredMeter("eth/fetcher/block/bodies", nil)

	headerFilterInMeter  = metrics.NewRegisteredMeter("eth/fetcher/block/filter/headers/in", nil)
	headerFilterOutMeter = metrics.NewRegisteredMeter("eth/fetcher/block/filter/headers/out", nil)
	bodyFilterInMeter    = metrics.NewRegisteredMeter("eth/fetcher/block/filter/bodies/in", nil)
	bodyFilterOutMeter   = metrics.NewRegisteredMeter("eth/fetcher/block/filter/bodies/out", nil)
)

var errTerminated = errors.New("terminated")

// HeaderRetrievalFn is a callback type for retrieving a header from the local chain.
type HeaderRetrievalFn func(common.Hash) *types.Header

// blockRetrievalFn is a callback type for retrieving a block from the local chain.
type blockRetrievalFn func(common.Hash) *types.Block

// headerRequesterFn is a callback type for sending a header retrieval request.
type headerRequesterFn func(common.Hash, chan *eth.Response) (*eth.Request, error)

// bodyRequesterFn is a callback type for sending a body retrieval request.
type bodyRequesterFn func([]common.Hash, chan *eth.Response) (*eth.Request, error)

// headerVerifierFn is a callback type to verify a block's header for fast propagation.
type headerVerifierFn func(header *types.Header) error

// blockBroadcasterFn is a callback type for broadcasting a block to connected peers.
type blockBroadcasterFn func(block *types.Block, propagate bool)

// chainHeightFn is a callback type to retrieve the current chain height.
type chainHeightFn func() uint64

// headersInsertFn is a callback type to insert a batch of headers into the local chain.
type headersInsertFn func(headers []*types.Header) (int, error)

// chainInsertFn is a callback type to insert a batch of blocks into the local chain.
type chainInsertFn func(types.Blocks) (int, error)

// peerDropFn is a callback type for dropping a peer detected as malicious.
type peerDropFn func(id string)

// blockAnnounce is the hash notification of the availability of a new block in the
// network.
// requesterFns are a peer's header and body request functions as its last
// announcement carried them (F-182: the parent request on a one-block gap).
type requesterFns struct {
	header headerRequesterFn
	bodies bodyRequesterFn
}

type blockAnnounce struct {
	hash   common.Hash   // Hash of the block being announced
	number uint64        // Number of the block being announced (0 = unknown | old protocol)
	header *types.Header // Header of the block partially reassembled (new protocol)
	time   time.Time     // Timestamp of the announcement

	origin string // Identifier of the peer originating the notification

	fetchHeader headerRequesterFn // Fetcher function to retrieve the header of an announced block
	fetchBodies bodyRequesterFn   // Fetcher function to retrieve the body of an announced block
}

// headerFilterTask represents a batch of headers needing fetcher filtering.
type headerFilterTask struct {
	peer    string          // The source peer of block headers
	headers []*types.Header // Collection of headers to filter
	time    time.Time       // Arrival time of the headers
}

// bodyFilterTask represents a batch of block bodies (transactions and uncles)
// needing fetcher filtering.
type bodyFilterTask struct {
	peer         string                 // The source peer of block bodies
	transactions [][]*types.Transaction // Collection of transactions per block bodies
	uncles       [][]*types.Header      // Collection of uncles per block bodies
	withdrawals  [][]*types.Withdrawal  // Collection of withdrawals per block bodies (the sortition prototype's payout list rides it)
	pis          [][]byte               // The keyed seal's proof per block body (header.PiHash commits it)
	endorsements [][]byte               // The parent's endorsements per block body (header.EndorsementsHash commits them)
	minedShares  [][]byte               // The share lane's carried shares per block body (header.SharesRoot commits them)
	time         time.Time              // Arrival time of the blocks' contents
}

// blockOrHeaderInject represents a schedules import operation.
type blockOrHeaderInject struct {
	origin string

	header *types.Header // Used for light mode fetcher which only cares about header.
	block  *types.Block  // Used for normal mode fetcher which imports full block.

	// The pushing peer's request functions (F-203's re-cut, the security review's
	// read): a peer that pushes a block is its own requester for the
	// parent its push may provoke, as a peer that announces is, so the
	// cost of a parent that never arrives falls on the pusher and never
	// on a third peer asked in its place.
	fetchHeader headerRequesterFn
	fetchBodies bodyRequesterFn
}

// number returns the block number of the injected object.
func (inject *blockOrHeaderInject) number() uint64 {
	if inject.header != nil {
		return inject.header.Number.Uint64()
	}
	return inject.block.NumberU64()
}

// number returns the block hash of the injected object.
func (inject *blockOrHeaderInject) hash() common.Hash {
	if inject.header != nil {
		return inject.header.Hash()
	}
	return inject.block.Hash()
}

// BlockFetcher is responsible for accumulating block announcements from various peers
// and scheduling them for retrieval.
type BlockFetcher struct {
	light bool // The indicator whether it's a light fetcher or normal one.

	// Various event channels
	notify chan *blockAnnounce
	inject chan *blockOrHeaderInject

	headerFilter chan chan *headerFilterTask
	bodyFilter   chan chan *bodyFilterTask

	done chan common.Hash
	quit chan struct{}

	// Announce states
	announces  map[string]int                   // Per peer blockAnnounce counts to prevent memory exhaustion
	announced  map[common.Hash][]*blockAnnounce // Announced blocks, scheduled for fetching
	requesters map[string]requesterFns          // F-182: each peer's request functions from its last announcement
	fetching   map[common.Hash]*blockAnnounce   // Announced blocks, currently fetching
	fetched    map[common.Hash][]*blockAnnounce // Blocks with headers fetched, scheduled for body retrieval
	completing map[common.Hash]*blockAnnounce   // Blocks with headers, currently body-completing

	// Block cache
	queue  *prque.Prque[int64, *blockOrHeaderInject] // Queue containing the import operations (block number sorted)
	queues map[string]int                            // Per peer block counts to prevent memory exhaustion
	queued map[common.Hash]*blockOrHeaderInject      // Set of already queued blocks (to dedup imports)
	// F-203 (16 October 2026, the security review's read of F-199): a pushed block at
	// the head's height plus one whose parent this node lacks but is
	// fetching waits here, keyed by the parent's hash, never in the queue
	// (a held block in the queue stalled every other import behind it for
	// the fetch timeout, five seconds a push at a peer's will); its slot
	// stays counted in queues and queued. It is re-queued when the
	// parent's import completes and dropped when the parent's fetch is
	// forgotten. provoked names the peer whose push provoked a parent's
	// announcement: the announcement counts against its allowance and the
	// peer is dropped when the fetch it provoked times out.
	awaiting map[common.Hash][]*blockOrHeaderInject
	provoked map[common.Hash]string
	// The block DAG ordering (consensus/igneum/dagform.go): a pushed block
	// whose selected or merged parents this node lacks waits aside for the
	// first of them while every one is requested by hash from the pusher
	// (announceHash, a number-less announcement: wanted marks the hashes
	// whose number is unknown so the number and distance checks let them
	// through); the handler's gap filler asks for the parents a batch
	// import found missing through want.
	missingParents func(*types.Block) []common.Hash
	wanted         map[common.Hash]bool
	want           chan []common.Hash

	// Callbacks
	getHeader    HeaderRetrievalFn // Retrieves a header from the local chain
	getBlock     blockRetrievalFn  // Retrieves a block from the local chain
	verifyHeader headerVerifierFn  // Checks if a block's headers have a valid proof of work
	// sealEquivocation reports a second, different seal under a key at a
	// height this node already holds a block of (the engine's ledger,
	// consensus/igneum/sealledger.go); nil without the rule.
	sealEquivocation func(header *types.Header) bool
	broadcastBlock   blockBroadcasterFn // Broadcasts a block to connected peers
	chainHeight      chainHeightFn      // Retrieves the current chain's height
	insertHeaders    headersInsertFn    // Injects a batch of headers into the chain
	insertChain      chainInsertFn      // Injects a batch of blocks into the chain
	dropPeer         peerDropFn         // Drops a peer for misbehaving

	// Testing hooks
	announceChangeHook func(common.Hash, bool)           // Method to call upon adding or deleting a hash from the blockAnnounce list
	queueChangeHook    func(common.Hash, bool)           // Method to call upon adding or deleting a block from the import queue
	fetchingHook       func([]common.Hash)               // Method to call upon starting a block (eth/61) or header (eth/62) fetch
	completingHook     func([]common.Hash)               // Method to call upon starting a block body fetch (eth/62)
	importedHook       func(*types.Header, *types.Block) // Method to call upon successful header or block import (both eth/61 and eth/62)
}

// NewBlockFetcher creates a block fetcher to retrieve blocks based on hash announcements.
func NewBlockFetcher(light bool, getHeader HeaderRetrievalFn, getBlock blockRetrievalFn, verifyHeader headerVerifierFn, broadcastBlock blockBroadcasterFn, chainHeight chainHeightFn, insertHeaders headersInsertFn, insertChain chainInsertFn, dropPeer peerDropFn) *BlockFetcher {
	return &BlockFetcher{
		light:          light,
		notify:         make(chan *blockAnnounce),
		want:           make(chan []common.Hash),
		inject:         make(chan *blockOrHeaderInject),
		headerFilter:   make(chan chan *headerFilterTask),
		bodyFilter:     make(chan chan *bodyFilterTask),
		done:           make(chan common.Hash),
		quit:           make(chan struct{}),
		announces:      make(map[string]int),
		announced:      make(map[common.Hash][]*blockAnnounce),
		requesters:     make(map[string]requesterFns),
		fetching:       make(map[common.Hash]*blockAnnounce),
		fetched:        make(map[common.Hash][]*blockAnnounce),
		completing:     make(map[common.Hash]*blockAnnounce),
		queue:          prque.New[int64, *blockOrHeaderInject](nil),
		queues:         make(map[string]int),
		queued:         make(map[common.Hash]*blockOrHeaderInject),
		awaiting:       make(map[common.Hash][]*blockOrHeaderInject),
		provoked:       make(map[common.Hash]string),
		getHeader:      getHeader,
		getBlock:       getBlock,
		verifyHeader:   verifyHeader,
		broadcastBlock: broadcastBlock,
		chainHeight:    chainHeight,
		insertHeaders:  insertHeaders,
		insertChain:    insertChain,
		dropPeer:       dropPeer,
	}
}

// Start boots up the announcement based synchroniser, accepting and processing
// hash notifications and block fetches until termination requested.
func (f *BlockFetcher) Start() {
	go f.loop()
}

// Stop terminates the announcement based synchroniser, canceling all pending
// operations.
func (f *BlockFetcher) Stop() {
	close(f.quit)
}

// Notify announces the fetcher of the potential availability of a new block in
// the network.
func (f *BlockFetcher) Notify(peer string, hash common.Hash, number uint64, time time.Time,
	headerFetcher headerRequesterFn, bodyFetcher bodyRequesterFn) error {
	block := &blockAnnounce{
		hash:        hash,
		number:      number,
		time:        time,
		origin:      peer,
		fetchHeader: headerFetcher,
		fetchBodies: bodyFetcher,
	}
	select {
	case f.notify <- block:
		return nil
	case <-f.quit:
		return errTerminated
	}
}

// Enqueue tries to fill gaps the fetcher's future import queue.
func (f *BlockFetcher) Enqueue(peer string, block *types.Block) error {
	return f.EnqueueWith(peer, block, nil, nil)
}

// EnqueueWith is Enqueue with the pushing peer's request functions, so the
// peer is its own requester for a parent its push provokes (F-203); a push
// without them (a test's) is served by a third peer's requester at no cost
// to that peer.
func (f *BlockFetcher) EnqueueWith(peer string, block *types.Block, fetchHeader headerRequesterFn, fetchBodies bodyRequesterFn) error {
	op := &blockOrHeaderInject{
		origin:      peer,
		block:       block,
		fetchHeader: fetchHeader,
		fetchBodies: fetchBodies,
	}
	select {
	case f.inject <- op:
		return nil
	case <-f.quit:
		return errTerminated
	}
}

// FilterHeaders extracts all the headers that were explicitly requested by the fetcher,
// returning those that should be handled differently.
func (f *BlockFetcher) FilterHeaders(peer string, headers []*types.Header, time time.Time) []*types.Header {
	log.Trace("Filtering headers", "peer", peer, "headers", len(headers))

	// Send the filter channel to the fetcher
	filter := make(chan *headerFilterTask)

	select {
	case f.headerFilter <- filter:
	case <-f.quit:
		return nil
	}
	// Request the filtering of the header list
	select {
	case filter <- &headerFilterTask{peer: peer, headers: headers, time: time}:
	case <-f.quit:
		return nil
	}
	// Retrieve the headers remaining after filtering
	select {
	case task := <-filter:
		return task.headers
	case <-f.quit:
		return nil
	}
}

// FilterBodies extracts all the block bodies that were explicitly requested by
// the fetcher, returning those that should be handled differently.
func (f *BlockFetcher) FilterBodies(peer string, transactions [][]*types.Transaction, uncles [][]*types.Header, withdrawals [][]*types.Withdrawal, pis [][]byte, endorsements [][]byte, minedShares [][]byte, time time.Time) ([][]*types.Transaction, [][]*types.Header, [][]*types.Withdrawal, [][]byte, [][]byte, [][]byte) {
	log.Trace("Filtering bodies", "peer", peer, "txs", len(transactions), "uncles", len(uncles), "withdrawals", len(withdrawals), "proofs", len(pis), "endorsements", len(endorsements), "shares", len(minedShares))

	// Send the filter channel to the fetcher
	filter := make(chan *bodyFilterTask)

	select {
	case f.bodyFilter <- filter:
	case <-f.quit:
		return nil, nil, nil, nil, nil, nil
	}
	// Request the filtering of the body list
	select {
	case filter <- &bodyFilterTask{peer: peer, transactions: transactions, uncles: uncles, withdrawals: withdrawals, pis: pis, endorsements: endorsements, minedShares: minedShares, time: time}:
	case <-f.quit:
		return nil, nil, nil, nil, nil, nil
	}
	// Retrieve the bodies remaining after filtering
	select {
	case task := <-filter:
		return task.transactions, task.uncles, task.withdrawals, task.pis, task.endorsements, task.minedShares
	case <-f.quit:
		return nil, nil, nil, nil, nil, nil
	}
}

// SetSealEquivocation installs the one-block-per-key check on announced
// blocks (consensus/igneum/sealledger.go); call before Start.
func (f *BlockFetcher) SetSealEquivocation(fn func(header *types.Header) bool) {
	f.sealEquivocation = fn
}

// SetMissingParents sets the block DAG ordering's missing-parents reader.
func (f *BlockFetcher) SetMissingParents(fn func(*types.Block) []common.Hash) {
	f.missingParents = fn
}

// Want asks for blocks by hash from any peer that has announced to this
// node (the handler's gap filler, for the parents a batch import lacked).
func (f *BlockFetcher) Want(hashes []common.Hash) {
	select {
	case f.want <- hashes:
	case <-f.quit:
	}
}

// announceHash schedules a number-less fetch of a block by hash from the
// peer's requester when it has one, else any peer's. It reports whether a
// fetch was scheduled.
func (f *BlockFetcher) announceHash(origin string, hash common.Hash) bool {
	if f.light || f.getBlock(hash) != nil {
		return false
	}
	if _, ok := f.announced[hash]; ok {
		return false
	}
	if _, ok := f.fetching[hash]; ok {
		return false
	}
	if _, ok := f.completing[hash]; ok {
		return false
	}
	if _, ok := f.queued[hash]; ok {
		return false
	}
	fns, ok := f.requesters[origin]
	if !ok {
		for o, r := range f.requesters {
			origin, fns, ok = o, r, true
			break
		}
	}
	if !ok {
		return false
	}
	if f.wanted == nil {
		f.wanted = make(map[common.Hash]bool)
	}
	f.wanted[hash] = true
	f.announced[hash] = append(f.announced[hash], &blockAnnounce{hash: hash, number: 0, time: time.Now(), origin: origin, fetchHeader: fns.header, fetchBodies: fns.bodies})
	log.Debug("Requesting a DAG parent by hash", "peer", origin, "hash", hash)
	return true
}

// Loop is the main fetcher loop, checking and processing various notification
// events.
func (f *BlockFetcher) loop() {
	// Iterate the block fetching until a quit is requested
	var (
		fetchTimer    = time.NewTimer(0)
		completeTimer = time.NewTimer(0)
	)
	<-fetchTimer.C // clear out the channel
	<-completeTimer.C
	defer fetchTimer.Stop()
	defer completeTimer.Stop()

	for {
		// Clean up any expired block fetches
		for hash, announce := range f.fetching {
			if time.Since(announce.time) > fetchTimeout {
				f.forgetHash(hash)
				f.forgetProvoked(hash)
			}
		}
		// Import any queued blocks that could potentially fit
		height := f.chainHeight()
		for !f.queue.Empty() {
			op := f.queue.PopItem()
			hash := op.hash()
			if f.queueChangeHook != nil {
				f.queueChangeHook(hash, false)
			}
			// If too high up the chain or phase, continue later (a block
			// asked for by hash under the block DAG ordering is tried
			// whatever its number: its parents decide)
			number := op.number()
			if number > height+1 && !f.wanted[hash] {
				f.queue.Push(op, -int64(number))
				if f.queueChangeHook != nil {
					f.queueChangeHook(hash, true)
				}
				break
			}
			// Otherwise if fresh and still unknown, try and import
			if (number+maxUncleDist < height && !f.wanted[hash]) || (f.light && f.getHeader(hash) != nil) || (!f.light && f.getBlock(hash) != nil) {
				f.forgetBlock(hash)
				continue
			}
			// F-199: a block at the head's own height plus one whose parent
			// this node lacks but is fetching (the sibling case, the parent
			// requested by announceParent) waits for the parent instead of
			// being dropped as an unknown parent. F-203: it waits ASIDE,
			// keyed by the parent, never in the queue, so the drain goes on
			// to every other block (a held block in the queue stalled the
			// drain for the fetch timeout, five seconds a push at a peer's
			// will); it is re-queued when the parent's import completes and
			// dropped when the parent's fetch is forgotten.
			if !f.light && op.block != nil && f.getBlock(op.block.ParentHash()) == nil && f.parentPending(op.block.ParentHash()) {
				parent := op.block.ParentHash()
				f.awaiting[parent] = append(f.awaiting[parent], op)
				log.Debug("Pushed block waits for its parent (F-203)", "peer", op.origin, "number", number, "hash", hash, "parent", parent)
				continue
			}
			// The block DAG ordering: a block whose selected or merged
			// parents this node lacks asks for every one by hash and waits
			// aside for the first.
			if !f.light && op.block != nil && f.missingParents != nil {
				if miss := f.missingParents(op.block); len(miss) > 0 {
					scheduled := false
					for _, m := range miss {
						if f.announceHash(op.origin, m) {
							scheduled = true
						}
					}
					f.awaiting[miss[0]] = append(f.awaiting[miss[0]], op)
					log.Debug("Pushed block waits for its DAG parents", "peer", op.origin, "number", number, "hash", hash, "missing", len(miss), "scheduled", scheduled)
					if scheduled {
						f.rescheduleFetch(fetchTimer)
					}
					continue
				}
			}
			if f.light {
				f.importHeaders(op.origin, op.header)
			} else {
				f.importBlocks(op.origin, op.block)
			}
		}
		// Wait for an outside event to occur
		select {
		case <-f.quit:
			// BlockFetcher terminating, abort all operations
			return

		case hashes := <-f.want:
			// The handler's gap filler (the block DAG ordering).
			scheduled := false
			for _, h := range hashes {
				if f.announceHash("", h) {
					scheduled = true
				}
			}
			if scheduled {
				f.rescheduleFetch(fetchTimer)
			}

		case notification := <-f.notify:
			// A block was announced, make sure the peer isn't DOSing us
			blockAnnounceInMeter.Mark(1)
			// F-182: the peer's request functions, kept for a parent request
			// on a one-block gap (announceParent).
			if notification.fetchHeader != nil && notification.fetchBodies != nil {
				f.requesters[notification.origin] = requesterFns{header: notification.fetchHeader, bodies: notification.fetchBodies}
			}

			count := f.announces[notification.origin] + 1
			if count > hashLimit {
				log.Debug("Peer exceeded outstanding announces", "peer", notification.origin, "limit", hashLimit)
				blockAnnounceDOSMeter.Mark(1)
				break
			}
			if notification.number == 0 {
				break
			}
			// If we have a valid block number, check that it's potentially useful
			if dist := int64(notification.number) - int64(f.chainHeight()); dist < -maxUncleDist || dist > maxQueueDist {
				log.Debug("Peer discarded announcement", "peer", notification.origin, "number", notification.number, "hash", notification.hash, "distance", dist)
				blockAnnounceDropMeter.Mark(1)
				break
			}
			// All is well, schedule the announce if block's not yet downloading
			if _, ok := f.fetching[notification.hash]; ok {
				break
			}
			if _, ok := f.completing[notification.hash]; ok {
				break
			}
			f.announces[notification.origin] = count
			f.announced[notification.hash] = append(f.announced[notification.hash], notification)
			if f.announceChangeHook != nil && len(f.announced[notification.hash]) == 1 {
				f.announceChangeHook(notification.hash, true)
			}
			if len(f.announced) == 1 {
				f.rescheduleFetch(fetchTimer)
			}

		case op := <-f.inject:
			// A direct block insertion was requested, try and fill any pending gaps
			blockBroadcastInMeter.Mark(1)

			// Now only direct block injection is allowed, drop the header injection
			// here silently if we receive.
			if f.light {
				continue
			}
			// F-205: a pushing peer with request functions is its own
			// requester for a parent its push provokes, as an announcing
			// peer is.
			if op.fetchHeader != nil && op.fetchBodies != nil {
				f.requesters[op.origin] = requesterFns{header: op.fetchHeader, bodies: op.fetchBodies}
			}
			f.enqueue(op.origin, nil, op.block)
			// F-182: a block one above a one-block gap (its parent unknown,
			// its number the head's plus two) asks the parent of its origin
			// at once, as an announcement of the parent, so the gap closes
			// through the fetcher and never waits for the downloader (a
			// sync of the whole branch, seconds, for one block).
			if f.announceParent(op.origin, op.block) {
				f.rescheduleFetch(fetchTimer)
			}

		case hash := <-f.done:
			// A pending import finished, remove all traces of the notification
			f.forgetHash(hash)
			f.forgetBlock(hash)
			// F-203: the blocks that waited for this one go back into the
			// queue; if the import failed they meet an unknown parent on the
			// next drain and drop as before.
			for _, op := range f.awaiting[hash] {
				f.queue.Push(op, -int64(op.number()))
				if f.queueChangeHook != nil {
					f.queueChangeHook(op.hash(), true)
				}
			}
			delete(f.awaiting, hash)
			delete(f.provoked, hash)

		case <-fetchTimer.C:
			// At least one block's timer ran out, check for needing retrieval
			request := make(map[string][]common.Hash)

			for hash, announces := range f.announced {
				// In current LES protocol(les2/les3), only header announce is
				// available, no need to wait too much time for header broadcast.
				timeout := arriveTimeout - gatherSlack
				if f.light {
					timeout = 0
				}
				if time.Since(announces[0].time) > timeout {
					// Pick a random peer to retrieve from, reset all others
					announce := announces[rand.Intn(len(announces))]
					f.forgetHash(hash)

					// If the block still didn't arrive, queue for fetching
					if (f.light && f.getHeader(hash) == nil) || (!f.light && f.getBlock(hash) == nil) {
						request[announce.origin] = append(request[announce.origin], hash)
						f.fetching[hash] = announce
					}
				}
			}
			// Send out all block header requests
			for peer, hashes := range request {
				log.Trace("Fetching scheduled headers", "peer", peer, "list", hashes)

				// Create a closure of the fetch and schedule in on a new thread
				fetchHeader, hashes := f.fetching[hashes[0]].fetchHeader, hashes
				go func(peer string) {
					if f.fetchingHook != nil {
						f.fetchingHook(hashes)
					}
					for _, hash := range hashes {
						headerFetchMeter.Mark(1)
						go func(hash common.Hash) {
							resCh := make(chan *eth.Response)

							req, err := fetchHeader(hash, resCh)
							if err != nil {
								return // Legacy code, yolo
							}
							defer req.Close()

							timeout := time.NewTimer(2 * fetchTimeout) // 2x leeway before dropping the peer
							defer timeout.Stop()

							select {
							case res := <-resCh:
								res.Done <- nil
								f.FilterHeaders(peer, *res.Res.(*eth.BlockHeadersRequest), time.Now())

							case <-timeout.C:
								// The peer didn't respond in time. The request
								// was already rescheduled at this point, we were
								// waiting for a catchup. With an unresponsive
								// peer however, it's a protocol violation.
								f.dropPeer(peer)
							}
						}(hash)
					}
				}(peer)
			}
			// Schedule the next fetch if blocks are still pending
			f.rescheduleFetch(fetchTimer)

		case <-completeTimer.C:
			// At least one header's timer ran out, retrieve everything
			request := make(map[string][]common.Hash)

			for hash, announces := range f.fetched {
				// Pick a random peer to retrieve from, reset all others
				announce := announces[rand.Intn(len(announces))]
				f.forgetHash(hash)

				// If the block still didn't arrive, queue for completion
				if f.getBlock(hash) == nil {
					request[announce.origin] = append(request[announce.origin], hash)
					f.completing[hash] = announce
				}
			}
			// Send out all block body requests
			for peer, hashes := range request {
				log.Trace("Fetching scheduled bodies", "peer", peer, "list", hashes)

				// Create a closure of the fetch and schedule in on a new thread
				if f.completingHook != nil {
					f.completingHook(hashes)
				}
				fetchBodies := f.completing[hashes[0]].fetchBodies
				bodyFetchMeter.Mark(int64(len(hashes)))

				go func(peer string, hashes []common.Hash) {
					resCh := make(chan *eth.Response)

					req, err := fetchBodies(hashes, resCh)
					if err != nil {
						return // Legacy code, yolo
					}
					defer req.Close()

					timeout := time.NewTimer(2 * fetchTimeout) // 2x leeway before dropping the peer
					defer timeout.Stop()

					select {
					case res := <-resCh:
						// The withdrawals list is the sortition prototype's payout
						// list on Igneum: a body without it was refused as
						// "missing withdrawals" on this path and imported whole
						// on the sync path seconds later (11 October 2026).
						txs, uncles, withdrawals, pis, endorsements, minedShares, err := res.Res.(*eth.BlockBodiesResponse).Unpack()
						res.Done <- err
						if err != nil {
							return
						}
						f.FilterBodies(peer, txs, uncles, withdrawals, pis, endorsements, minedShares, time.Now())

					case <-timeout.C:
						// The peer didn't respond in time. The request
						// was already rescheduled at this point, we were
						// waiting for a catchup. With an unresponsive
						// peer however, it's a protocol violation.
						f.dropPeer(peer)
					}
				}(peer, hashes)
			}
			// Schedule the next fetch if blocks are still pending
			f.rescheduleComplete(completeTimer)

		case filter := <-f.headerFilter:
			// Headers arrived from a remote peer. Extract those that were explicitly
			// requested by the fetcher, and return everything else so it's delivered
			// to other parts of the system.
			var task *headerFilterTask
			select {
			case task = <-filter:
			case <-f.quit:
				return
			}
			headerFilterInMeter.Mark(int64(len(task.headers)))

			// Split the batch of headers into unknown ones (to return to the caller),
			// known incomplete ones (requiring body retrievals) and completed blocks.
			unknown, incomplete, complete, lightHeaders := []*types.Header{}, []*blockAnnounce{}, []*types.Block{}, []*blockAnnounce{}
			for _, header := range task.headers {
				hash := header.Hash()

				// Filter fetcher-requested headers from other synchronisation algorithms
				if announce := f.fetching[hash]; announce != nil && announce.origin == task.peer && f.fetched[hash] == nil && f.completing[hash] == nil && f.queued[hash] == nil {
					// If the delivered header does not match the promised number, drop the announcer
					// (a number-less DAG request, announce.number zero, promised none).
					if announce.number != 0 && header.Number.Uint64() != announce.number {
						log.Trace("Invalid block number fetched", "peer", announce.origin, "hash", header.Hash(), "announced", announce.number, "provided", header.Number)
						f.dropPeer(announce.origin)
						f.forgetHash(hash)
						continue
					}
					// Collect all headers only if we are running in light
					// mode and the headers are not imported by other means.
					if f.light {
						if f.getHeader(hash) == nil {
							announce.header = header
							lightHeaders = append(lightHeaders, announce)
						}
						f.forgetHash(hash)
						continue
					}
					// Only keep if not imported by other means
					if f.getBlock(hash) == nil {
						announce.header = header
						announce.time = task.time

						// If the block is empty (header only), short circuit into the final import queue.
						// A header carrying a withdrawals root (the sortition prototype's payout list)
						// is empty only when that root is the empty list's: a listed block with no
						// transactions was completed here without a body, imported without its list
						// and refused as "missing withdrawals in block body" (11 October 2026, the
						// announcement-path fault security counted by name after 3ec647a8).
						if header.TxHash == types.EmptyTxsHash && header.UncleHash == types.EmptyUncleHash && header.PiHash == nil && header.EndorsementsHash == nil && header.SharesRoot == nil &&
							(header.WithdrawalsHash == nil || *header.WithdrawalsHash == types.EmptyWithdrawalsHash) {
							log.Trace("Block empty, skipping body retrieval", "peer", announce.origin, "number", header.Number, "hash", header.Hash())

							block := types.NewBlockWithHeader(header)
							if header.WithdrawalsHash != nil {
								block = block.WithWithdrawals([]*types.Withdrawal{})
							}
							block.ReceivedAt = task.time

							complete = append(complete, block)
							f.completing[hash] = announce
							continue
						}
						// Otherwise add to the list of blocks needing completion
						incomplete = append(incomplete, announce)
					} else {
						log.Trace("Block already imported, discarding header", "peer", announce.origin, "number", header.Number, "hash", header.Hash())
						f.forgetHash(hash)
					}
				} else {
					// BlockFetcher doesn't know about it, add to the return list
					unknown = append(unknown, header)
				}
			}
			headerFilterOutMeter.Mark(int64(len(unknown)))
			select {
			case filter <- &headerFilterTask{headers: unknown, time: task.time}:
			case <-f.quit:
				return
			}
			// Schedule the retrieved headers for body completion
			for _, announce := range incomplete {
				hash := announce.header.Hash()
				if _, ok := f.completing[hash]; ok {
					continue
				}
				f.fetched[hash] = append(f.fetched[hash], announce)
				if len(f.fetched) == 1 {
					f.rescheduleComplete(completeTimer)
				}
			}
			// Schedule the header for light fetcher import
			for _, announce := range lightHeaders {
				f.enqueue(announce.origin, announce.header, nil)
			}
			// Schedule the header-only blocks for import
			for _, block := range complete {
				if announce := f.completing[block.Hash()]; announce != nil {
					f.enqueue(announce.origin, nil, block)
				}
			}

		case filter := <-f.bodyFilter:
			// Block bodies arrived, extract any explicitly requested blocks, return the rest
			var task *bodyFilterTask
			select {
			case task = <-filter:
			case <-f.quit:
				return
			}
			bodyFilterInMeter.Mark(int64(len(task.transactions)))
			blocks := []*types.Block{}
			// abort early if there's nothing explicitly requested
			if len(f.completing) > 0 {
				for i := 0; i < len(task.transactions) && i < len(task.uncles); i++ {
					// Match up a body to any possible completion request
					var (
						matched   = false
						uncleHash common.Hash // calculated lazily and reused
						txnHash   common.Hash // calculated lazily and reused
					)
					for hash, announce := range f.completing {
						if f.queued[hash] != nil || announce.origin != task.peer {
							continue
						}
						if uncleHash == (common.Hash{}) {
							uncleHash = types.CalcUncleHash(task.uncles[i])
						}
						if uncleHash != announce.header.UncleHash {
							continue
						}
						if txnHash == (common.Hash{}) {
							txnHash = types.DeriveSha(types.Transactions(task.transactions[i]), trie.NewStackTrie(nil))
						}
						if txnHash != announce.header.TxHash {
							continue
						}
						// A header with a withdrawals root takes only a body whose
						// list hashes to it.
						var withdrawals []*types.Withdrawal
						if announce.header.WithdrawalsHash != nil {
							if i >= len(task.withdrawals) || types.DeriveSha(types.Withdrawals(task.withdrawals[i]), trie.NewStackTrie(nil)) != *announce.header.WithdrawalsHash {
								continue
							}
							withdrawals = task.withdrawals[i]
						}
						// A header committing to a keyed seal's proof takes only
						// a body whose proof hashes to it (the body stage checks
						// the same; here it decides which body is this header's).
						var pi []byte
						if announce.header.PiHash != nil {
							if i >= len(task.pis) || len(task.pis[i]) == 0 || types.PiHash(task.pis[i]) != *announce.header.PiHash {
								continue
							}
							pi = task.pis[i]
						} else if i < len(task.pis) && len(task.pis[i]) > 0 {
							continue
						}
						// A header committing to the parent's endorsements takes
						// only a body whose list hashes to it with its count.
						var endorsements []byte
						if announce.header.EndorsementsHash != nil {
							if i >= len(task.endorsements) || len(task.endorsements[i]) == 0 ||
								types.EndorsementsHash(task.endorsements[i]) != *announce.header.EndorsementsHash ||
								uint64(types.EndorsementCount(task.endorsements[i])) != announce.header.Endorsed {
								continue
							}
							endorsements = task.endorsements[i]
						} else if i < len(task.endorsements) && len(task.endorsements[i]) > 0 {
							continue
						}
						// A header committing to carried shares takes only a body
						// whose shares hash to it.
						var minedShares []byte
						if announce.header.SharesRoot != nil {
							if i >= len(task.minedShares) || len(task.minedShares[i]) == 0 || types.MinedSharesRoot(task.minedShares[i]) != *announce.header.SharesRoot {
								continue
							}
							minedShares = task.minedShares[i]
						} else if i < len(task.minedShares) && len(task.minedShares[i]) > 0 {
							continue
						}
						// Mark the body matched, reassemble if still unknown
						matched = true
						if f.getBlock(hash) == nil {
							block := types.NewBlockWithHeader(announce.header).WithBody(task.transactions[i], task.uncles[i])
							if withdrawals != nil {
								block = block.WithWithdrawals(withdrawals)
							}
							if pi != nil {
								block = block.WithPi(pi)
							}
							if endorsements != nil {
								block = block.WithEndorsements(endorsements)
							}
							if minedShares != nil {
								block = block.WithMinedShares(minedShares)
							}
							block.ReceivedAt = task.time
							blocks = append(blocks, block)
						} else {
							f.forgetHash(hash)
						}
					}
					if matched {
						task.transactions = append(task.transactions[:i], task.transactions[i+1:]...)
						task.uncles = append(task.uncles[:i], task.uncles[i+1:]...)
						if i < len(task.withdrawals) {
							task.withdrawals = append(task.withdrawals[:i], task.withdrawals[i+1:]...)
						}
						if i < len(task.pis) {
							task.pis = append(task.pis[:i], task.pis[i+1:]...)
						}
						if i < len(task.endorsements) {
							task.endorsements = append(task.endorsements[:i], task.endorsements[i+1:]...)
						}
						if i < len(task.minedShares) {
							task.minedShares = append(task.minedShares[:i], task.minedShares[i+1:]...)
						}
						i--
						continue
					}
				}
			}
			bodyFilterOutMeter.Mark(int64(len(task.transactions)))
			select {
			case filter <- task:
			case <-f.quit:
				return
			}
			// Schedule the retrieved blocks for ordered import
			for _, block := range blocks {
				if announce := f.completing[block.Hash()]; announce != nil {
					f.enqueue(announce.origin, nil, block)
				}
			}
		}
	}
}

// rescheduleFetch resets the specified fetch timer to the next blockAnnounce timeout.
func (f *BlockFetcher) rescheduleFetch(fetch *time.Timer) {
	// Short circuit if no blocks are announced
	if len(f.announced) == 0 {
		return
	}
	// Schedule announcement retrieval quickly for light mode
	// since server won't send any headers to client.
	if f.light {
		fetch.Reset(lightTimeout)
		return
	}
	// Otherwise find the earliest expiring announcement
	earliest := time.Now()
	for _, announces := range f.announced {
		if earliest.After(announces[0].time) {
			earliest = announces[0].time
		}
	}
	fetch.Reset(arriveTimeout - time.Since(earliest))
}

// rescheduleComplete resets the specified completion timer to the next fetch timeout.
func (f *BlockFetcher) rescheduleComplete(complete *time.Timer) {
	// Short circuit if no headers are fetched
	if len(f.fetched) == 0 {
		return
	}
	// Otherwise find the earliest expiring announcement
	earliest := time.Now()
	for _, announces := range f.fetched {
		if earliest.After(announces[0].time) {
			earliest = announces[0].time
		}
	}
	complete.Reset(gatherSlack - time.Since(earliest))
}

// enqueue schedules a new header or block import operation, if the component
// to be imported has not yet been seen.
// announceParent (F-182) turns a delivered block whose parent this node does
// not hold, at the head's height plus two, into an announcement of its parent
// from the block's origin (or any peer whose request functions are known), so
// the fetcher requests the parent at once; true when an announcement was
// added. The fetcher's loop calls it; the lock is its own.
// parentPending reports whether a hash is announced, being fetched or
// completed, or queued for import: a child waiting on it stays queued
// (F-199).
func (f *BlockFetcher) parentPending(parent common.Hash) bool {
	if _, ok := f.announced[parent]; ok {
		return true
	}
	if _, ok := f.fetching[parent]; ok {
		return true
	}
	if _, ok := f.completing[parent]; ok {
		return true
	}
	_, ok := f.queued[parent]
	return ok
}

// forgetProvoked (F-203) is called when a fetch times out: a parent whose
// request a peer's push provoked and that never arrived costs that peer
// the connection (it pushed a block it cannot back), and the blocks that
// waited for the parent are dropped with their slots freed.
func (f *BlockFetcher) forgetProvoked(parent common.Hash) {
	peer, ok := f.provoked[parent]
	if !ok {
		return
	}
	delete(f.provoked, parent)
	for _, op := range f.awaiting[parent] {
		f.forgetBlock(op.hash())
	}
	delete(f.awaiting, parent)
	log.Debug("Dropping peer: the parent its pushed block provoked never arrived (F-203)", "peer", peer, "parent", parent)
	f.dropPeer(peer)
}

func (f *BlockFetcher) announceParent(origin string, block *types.Block) bool {
	if block == nil || f.light {
		return false
	}
	number := block.NumberU64()
	// A one-block gap (the parent one above this node's head), or the
	// child of a SIBLING of this node's head (F-199, 16 October 2026: two
	// of the research chain's eight sat on a sibling head for two and a
	// half minutes after a roll, every pushed child dropped as "Unknown
	// parent" while the downloader held off under the one-block rule, the
	// peers' lead being small-difficulty blocks under one head's
	// difficulty; the parent is requested here instead, imported as a
	// side block, and the fork rule takes the branch as its children land).
	if number < 2 || (number != f.chainHeight()+2 && number != f.chainHeight()+1) {
		return false
	}
	parent := block.ParentHash()
	if f.getBlock(parent) != nil {
		return false
	}
	if _, ok := f.announced[parent]; ok {
		return false
	}
	if _, ok := f.fetching[parent]; ok {
		return false
	}
	if _, ok := f.completing[parent]; ok {
		return false
	}
	if _, ok := f.queued[parent]; ok {
		return false
	}
	// F-205 (the security review's read of F-203): the pusher's name is kept apart
	// from the requester's. The request goes to the pusher's own requester
	// when it has one (a push carries it, EnqueueWith, as an announcement
	// does), and then the provoked announcement counts against the pusher's
	// allowance and the pusher is dropped if the parent never arrives; a
	// request that has to go through a third peer (a push without request
	// functions) charges and names nobody, the third peer answering or not
	// at no cost to it, as F-182's form stood.
	pusher := origin
	fns, ok := f.requesters[pusher]
	if !ok {
		for o, r := range f.requesters {
			origin, fns, ok = o, r, true
			break
		}
	}
	if !ok {
		return false
	}
	if origin == pusher {
		if f.announces[pusher]+1 > hashLimit {
			log.Debug("Not requesting the parent of a delivered block, the peer's announcement allowance is spent (F-203)", "peer", pusher, "number", number-1, "hash", parent, "limit", hashLimit)
			return false
		}
		f.announces[pusher]++
		f.provoked[parent] = pusher
	}
	f.announced[parent] = append(f.announced[parent], &blockAnnounce{hash: parent, number: number - 1, time: time.Now(), origin: origin, fetchHeader: fns.header, fetchBodies: fns.bodies})
	log.Debug("Requesting the parent of a delivered block at once (F-182)", "peer", origin, "number", number-1, "hash", parent)
	return true
}

func (f *BlockFetcher) enqueue(peer string, header *types.Header, block *types.Block) {
	var (
		hash   common.Hash
		number uint64
	)
	if header != nil {
		hash, number = header.Hash(), header.Number.Uint64()
	} else {
		hash, number = block.Hash(), block.NumberU64()
	}
	// Ensure the peer isn't DOSing us
	count := f.queues[peer] + 1
	if count > blockLimit {
		log.Debug("Discarded delivered header or block, exceeded allowance", "peer", peer, "number", number, "hash", hash, "limit", blockLimit)
		blockBroadcastDOSMeter.Mark(1)
		f.forgetHash(hash)
		return
	}
	// Discard any past or too distant blocks (a block this node asked for
	// by hash under the block DAG ordering is wanted at any distance).
	if dist := int64(number) - int64(f.chainHeight()); !f.wanted[hash] && (dist < -maxUncleDist || dist > maxQueueDist) {
		log.Debug("Discarded delivered header or block, too far away", "peer", peer, "number", number, "hash", hash, "distance", dist)
		blockBroadcastDropMeter.Mark(1)
		f.forgetHash(hash)
		return
	}
	// Schedule the block for future importing
	if _, ok := f.queued[hash]; !ok {
		op := &blockOrHeaderInject{origin: peer}
		if header != nil {
			op.header = header
		} else {
			op.block = block
		}
		f.queues[peer] = count
		f.queued[hash] = op
		f.queue.Push(op, -int64(number))
		if f.queueChangeHook != nil {
			f.queueChangeHook(hash, true)
		}
		log.Debug("Queued delivered header or block", "peer", peer, "number", number, "hash", hash, "queued", f.queue.Size())
	}
}

// importHeaders spawns a new goroutine to run a header insertion into the chain.
// If the header's number is at the same height as the current import phase, it
// updates the phase states accordingly.
func (f *BlockFetcher) importHeaders(peer string, header *types.Header) {
	hash := header.Hash()
	log.Debug("Importing propagated header", "peer", peer, "number", header.Number, "hash", hash)

	go func() {
		defer func() { f.done <- hash }()
		// If the parent's unknown, abort insertion
		parent := f.getHeader(header.ParentHash)
		if parent == nil {
			log.Debug("Unknown parent of propagated header", "peer", peer, "number", header.Number, "hash", hash, "parent", header.ParentHash)
			return
		}
		// Validate the header and if something went wrong, drop the peer
		if err := f.verifyHeader(header); err != nil && err != consensus.ErrFutureBlock {
			log.Debug("Propagated header verification failed", "peer", peer, "number", header.Number, "hash", hash, "err", err)
			f.dropPeer(peer)
			return
		}
		// Run the actual import and log any issues
		if _, err := f.insertHeaders([]*types.Header{header}); err != nil {
			log.Debug("Propagated header import failed", "peer", peer, "number", header.Number, "hash", hash, "err", err)
			return
		}
		// Invoke the testing hook if needed
		if f.importedHook != nil {
			f.importedHook(header, nil)
		}
	}()
}

// importBlocks spawns a new goroutine to run a block insertion into the chain. If the
// block's number is at the same height as the current import phase, it updates
// the phase states accordingly.
func (f *BlockFetcher) importBlocks(peer string, block *types.Block) {
	hash := block.Hash()

	// Run the import on a new thread
	log.Debug("Importing propagated block", "peer", peer, "number", block.Number(), "hash", hash)
	go func() {
		defer func() { f.done <- hash }()

		// If the parent's unknown, abort insertion
		parent := f.getBlock(block.ParentHash())
		if parent == nil {
			log.Debug("Unknown parent of propagated block", "peer", peer, "number", block.Number(), "hash", hash, "parent", block.ParentHash())
			return
		}
		// Quickly validate the header and propagate the block if it passes
		err := f.verifyHeader(block.Header())
		if errors.Is(err, consensus.ErrUnknownAncestor) {
			err = consensus.ErrUnknownAncestor
		}
		switch err {
		case nil:
			// One block per key per height on the relay and the fork choice
			// (consensus/igneum/sealledger.go): a second, different seal
			// under a key this node already holds a block of at this
			// height is kept as evidence by the engine, not relayed and
			// not imported; the first seen stands. Never a validity rule.
			if f.sealEquivocation != nil && f.sealEquivocation(block.Header()) {
				log.Warn("Second seal under one key at one height: not relayed, not imported", "peer", peer, "number", block.Number(), "hash", hash)
				return
			}
			// All ok, quickly propagate to our peers
			blockBroadcastOutTimer.UpdateSince(block.ReceivedAt)
			go f.broadcastBlock(block, true)

		case consensus.ErrFutureBlock:
			// Weird future block, don't fail, but neither propagate

		case consensus.ErrUnknownAncestor:
			// A DAG parent still unknown (the block DAG ordering): asked
			// for by hash on the next drain, never the peer's fault.
			return

		default:
			// Something went very wrong, drop the peer
			log.Debug("Propagated block verification failed", "peer", peer, "number", block.Number(), "hash", hash, "err", err)
			f.dropPeer(peer)
			return
		}
		// Run the actual import and log any issues
		if _, err := f.insertChain(types.Blocks{block}); err != nil {
			log.Debug("Propagated block import failed", "peer", peer, "number", block.Number(), "hash", hash, "err", err)
			return
		}
		// If import succeeded, broadcast the block
		blockAnnounceOutTimer.UpdateSince(block.ReceivedAt)
		go f.broadcastBlock(block, false)

		// Invoke the testing hook if needed
		if f.importedHook != nil {
			f.importedHook(nil, block)
		}
	}()
}

// forgetHash removes all traces of a block announcement from the fetcher's
// internal state.
func (f *BlockFetcher) forgetHash(hash common.Hash) {
	// Remove all pending announces and decrement DOS counters
	if announceMap, ok := f.announced[hash]; ok {
		for _, announce := range announceMap {
			f.announces[announce.origin]--
			if f.announces[announce.origin] <= 0 {
				delete(f.announces, announce.origin)
			}
		}
		delete(f.announced, hash)
		if f.announceChangeHook != nil {
			f.announceChangeHook(hash, false)
		}
	}
	// Remove any pending fetches and decrement the DOS counters
	if announce := f.fetching[hash]; announce != nil {
		f.announces[announce.origin]--
		if f.announces[announce.origin] <= 0 {
			delete(f.announces, announce.origin)
		}
		delete(f.fetching, hash)
	}

	// Remove any pending completion requests and decrement the DOS counters
	for _, announce := range f.fetched[hash] {
		f.announces[announce.origin]--
		if f.announces[announce.origin] <= 0 {
			delete(f.announces, announce.origin)
		}
	}
	delete(f.fetched, hash)

	// Remove any pending completions and decrement the DOS counters
	if announce := f.completing[hash]; announce != nil {
		f.announces[announce.origin]--
		if f.announces[announce.origin] <= 0 {
			delete(f.announces, announce.origin)
		}
		delete(f.completing, hash)
	}
}

// forgetBlock removes all traces of a queued block from the fetcher's internal
// state.
func (f *BlockFetcher) forgetBlock(hash common.Hash) {
	if insert := f.queued[hash]; insert != nil {
		f.queues[insert.origin]--
		if f.queues[insert.origin] == 0 {
			delete(f.queues, insert.origin)
		}
		delete(f.queued, hash)
	}
}
