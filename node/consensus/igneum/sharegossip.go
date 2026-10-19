package igneum

import (
	"math/big"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/protocols/ishr"
)

// The engine's side of the share gossip (PA-46; eth/protocols/ishr): a
// bounded index of the shares this pool accepted, by the wire hash, so a
// peer's announcement is answered from it and a fetch is served from it; a
// share received from a peer goes through acceptShare exactly as a
// submission; a share accepted by direct submission is announced to the
// peers through the announcer the protocol sets.
type shareGossip struct {
	mu        sync.Mutex
	byHash    map[common.Hash]ishr.Share
	order     []common.Hash
	announcer func(ishr.Share)
	chain     consensus.ChainHeaderReader
	stats     func() ishr.Stats
}

// gossipIndexBound is the index's size: twice the pool's bound (a carried
// share stays answerable for a while after it left the pool, so peers do
// not re-fetch it), never under 2,048.
func (e *Engine) gossipIndexBound() int {
	n := 2 * int(e.SharePoolBound())
	if n < 2048 {
		n = 2048
	}
	return n
}

func (e *Engine) gossipRemember(s ishr.Share) {
	g := &e.gossip
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.byHash == nil {
		g.byHash = make(map[common.Hash]ishr.Share)
	}
	h := s.Hash()
	if _, ok := g.byHash[h]; ok {
		return
	}
	if bound := e.gossipIndexBound(); len(g.order) >= bound {
		drop := g.order[:len(g.order)-bound+1]
		for _, d := range drop {
			delete(g.byHash, d)
		}
		g.order = g.order[len(drop):]
	}
	g.byHash[h] = s
	g.order = append(g.order, h)
}

// HasShare is ishr.ShareEngine's: the index holds the share.
func (e *Engine) HasShare(hash common.Hash) bool {
	g := &e.gossip
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.byHash[hash]
	return ok
}

// ShareByHash is ishr.ShareEngine's: a held share for a peer's fetch.
func (e *Engine) ShareByHash(hash common.Hash) (ishr.Share, bool) {
	g := &e.gossip
	g.mu.Lock()
	defer g.mu.Unlock()
	s, ok := g.byHash[hash]
	return s, ok
}

// ShareWindow is ishr.ShareEngine's: the recency.
func (e *Engine) ShareWindow() uint64 { return e.ShareRecencyBlocks() }

// VerifyWidth and VerifyCost are ishr.ShareEngine's: the node's
// verification width and its measured mean share verification time (the
// verifier budget's running mean; zero before any measurement or with the
// budget off), the fetch pacing's figures (F-227: ten fetches a second per
// width on the full form, one on the light).
func (e *Engine) VerifyWidth() int          { return VerifyWorkers() }
func (e *Engine) VerifyCost() time.Duration { return e.budget.meanShareTime() }

// SetShareAnnouncer is ishr.ShareEngine's: the protocol's broadcast, called
// for every share this node accepts by direct submission.
func (e *Engine) SetShareAnnouncer(fn func(ishr.Share)) {
	g := &e.gossip
	g.mu.Lock()
	defer g.mu.Unlock()
	g.announcer = fn
}

// SetGossipChain gives the receive path its chain reader (the backend's
// blockchain; the RPC path carries its own).
func (e *Engine) SetGossipChain(chain consensus.ChainHeaderReader) {
	g := &e.gossip
	g.mu.Lock()
	defer g.mu.Unlock()
	g.chain = chain
}

// ReceiveShare is ishr.ShareEngine's: a share from a peer, verified exactly
// as a submission (the parent against this node's canonical block at its
// height first, the work, the duplicate, the per-address cap, the pool
// bound), journaled and pooled. bad is true only when the WORK fails (the
// peer sent a share that cannot be honest); a stale parent, a duplicate, a
// cap or a full pool is late or redundant, never the peer's fault.
func (e *Engine) ReceiveShare(s ishr.Share, source string) (ok bool, reason string, bad bool) {
	e.gossip.mu.Lock()
	chain := e.gossip.chain
	e.gossip.mu.Unlock()
	if chain == nil {
		return false, "no chain", false
	}
	// A share a canonical block within the window already carried (by any
	// sealer) is not pooled again (F-42): refused by name, not the peer's
	// fault, not forwarded.
	// The key with the share's parent (the gossip names it; a share for a
	// sibling parent of the same fields is another share).
	if carried := e.carriedAtHead(chain); carried[e.carriedKeyAt(types.MinedShare{Address: s.Address, ExtraNonce: s.ExtraNonce, Nonce: s.Nonce, MixDigest: s.MixDigest}, s.Height, s.ParentHash)] {
		e.sharePool.mu.Lock()
		e.sharePool.refuse(errShareCarriedElsewhere, s.Address)
		e.sharePool.mu.Unlock()
		return false, errShareCarriedElsewhere.Error(), false
	}
	parent := s.ParentHash
	// A gossiped share is a PEER's arrival, not the miner's submission:
	// its refusals (a copy this node already holds by forward or by
	// gossip, a stale parent, bad work from a peer) are not journalled
	// on the miner's account (the settlement team's read of the old public test network's (chain 7320)
	// clean hour, 13 October 2026: 18,555 "carried twice" on the mini's
	// account were gossip arrivals of shares the node held); the gossip
	// threshold on the peer is the peer's measure.
	ok, reason = e.acceptShare(chain, s.Address, s.ExtraNonce, s.Nonce, s.MixDigest, s.Height, &parent, s.Step, false, true, nil)
	if ok {
		e.gossipRemember(s)
		return true, "", false
	}
	if len(reason) >= len(errShareWork.Error()) && reason[:len(errShareWork.Error())] == errShareWork.Error() {
		return false, reason, true
	}
	return false, reason, false
}

// announceAccepted remembers and announces a share this node accepted by
// direct submission (SubmitShare, SubmitShareOn): a solo miner's share
// submitted to ANY node, sealer or not, reaches every sealer's pool.
func (e *Engine) announceAccepted(chain consensus.ChainHeaderReader, address common.Address, extraNonce, nonce uint64, mix common.Hash, height uint64, step uint8) {
	parent := chain.GetHeaderByNumber(height - 1)
	if parent == nil {
		return
	}
	s := ishr.Share{Address: address, ExtraNonce: extraNonce, Nonce: nonce, MixDigest: mix, ParentHash: parent.Hash(), Height: height, Step: step}
	e.gossipRemember(s)
	e.gossip.mu.Lock()
	fn := e.gossip.announcer
	e.gossip.mu.Unlock()
	if fn != nil {
		fn(s)
	}
}

// GossipIndexSize is the index's size (igneum_shareStats).
func (e *Engine) GossipIndexSize() int {
	g := &e.gossip
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.byHash)
}

// ExpireSharesAtHead is the gossip handler's per-head sweep (ishr's
// expirer): the node team's ExpireShares over the gossip's chain reader, so a
// NON-sealing node's pool, which never templates, still expires what left
// the recency (and names it in expiredNoBlock) on every new head.
func (e *Engine) ExpireSharesAtHead(head *types.Header) {
	e.gossip.mu.Lock()
	chain := e.gossip.chain
	e.gossip.mu.Unlock()
	if chain == nil || head == nil {
		return
	}
	e.ExpireShares(chain, head)
}

// ShareWork is the work package of a node that seals nothing (the launch
// form's shape: a worker names ONE node, sealer or not; the product team's finding
// of 16:4x UK, 12 October 2026, the peer answering "no mining work available
// yet"): the share fields alone for the head's successor (the share target
// at the chain's D for head + 1, the count, the height, the parent, the
// twin document and its digest), a zero header hash and no seal target, so
// a worker mines shares on it and submits them here, where they are pooled
// and gossiped to every sealer. Refused when the share lane is off at the
// height or the node has no chain.
func (e *Engine) ShareWork() (*pow.WorkPackage, error) {
	e.gossip.mu.Lock()
	chain := e.gossip.chain
	e.gossip.mu.Unlock()
	if chain == nil {
		return nil, errNoMiningWork
	}
	head := chain.CurrentHeader()
	if head == nil {
		return nil, errNoMiningWork
	}
	height := head.Number.Uint64() + 1
	if !e.SharesOn(height) {
		return nil, errNoMiningWork
	}
	// The successor's header as the share fields read it: the parent and
	// the chain's D for the height (shareWorkFields reads the chain's D
	// through templateChainDifficulty, never this difficulty, which is set
	// for the fields' form only).
	h := &types.Header{ParentHash: head.Hash(), Number: new(big.Int).SetUint64(height), Difficulty: e.shareChainDifficulty(chain, head, nil)}
	wp := pow.WorkPackage{Algorithm: e.algo.Name(), Height: height, ShareOnly: true}
	e.shareWorkFields(&wp, h)
	if wp.ShareTarget == nil {
		return nil, errNoMiningWork
	}
	// The period's anchor for the SHARE height on the share parent (the
	// pool seat's journal read of 16 October 2026: at the first height of
	// a period the work named the previous period's anchor for 6 to 32 s
	// while the sealer's template sat behind the head, so every share of
	// those seconds was mined under the old programme and refused as an
	// invalid mix digest). The anchor block of a height's period (its
	// start minus the reveal) stands two blocks before the boundary, so
	// the share height's own anchor resolves from the first ask.
	if a, ah, ok := e.programAnchorFor(e.shareDocAlgo(height), head.Hash(), height); ok {
		anchor := a
		wp.ProgramAnchor, wp.AnchorHeight = &anchor, ah
	}
	return &wp, nil
}

// shareDocAlgo is the algorithm a share of this node's lane is hashed with
// at the share's height: the node's own coordinate's document in force at
// that height under the vector form (a document changed in place by a lane
// modification takes effect at its height, LANE-MODIFICATION.md section 1a;
// the review lead's condition on 0.10.44 of 16 October 2026), else the chain's.
func (e *Engine) shareDocAlgo(height uint64) pow.Algorithm {
	if m := e.vectorMenu(); m != nil {
		if a := m.ByCoordinateAt(e.Coordinate(), height); a != nil {
			return a
		}
	}
	return e.algo
}

// SetGossipStats gives igneum_shareStats the protocol's counters (the
// backend sets it to the handler's Stats).
func (e *Engine) SetGossipStats(fn func() ishr.Stats) {
	g := &e.gossip
	g.mu.Lock()
	defer g.mu.Unlock()
	g.stats = fn
}

// gossipStats reads the protocol's counters, or nil without the protocol.
func (e *Engine) gossipStats() *ishr.Stats {
	g := &e.gossip
	g.mu.Lock()
	fn := g.stats
	g.mu.Unlock()
	if fn == nil {
		return nil
	}
	s := fn()
	return &s
}

var _ ishr.ShareEngine = (*Engine)(nil)
var _ = types.EmptyRootHash
