package ishr

import (
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
)

// ShareEngine is what the protocol needs of the consensus engine's share
// pool (consensus/igneum/sharegossip.go).
type ShareEngine interface {
	// ReceiveShare verifies a share from a peer exactly as a submission
	// (the parent first, the work, the duplicate, the caps, the bound) and
	// pools it. ok says it is pooled (and so is to be forwarded); reason is
	// the refusal's name; bad says the peer is at fault (the work fails)
	// rather than late or redundant.
	ReceiveShare(s Share, source string) (ok bool, reason string, bad bool)
	// HasShare says whether the pool holds, or held, the share by its hash.
	HasShare(hash common.Hash) bool
	// ShareByHash returns a held share for a peer's fetch.
	ShareByHash(hash common.Hash) (Share, bool)
	// ShareWindow is the recency: a share at a height more than this
	// below the head's successor is neither fetched nor pooled.
	ShareWindow() uint64
	// SetShareAnnouncer gives the engine the way to announce a share it
	// accepted by direct submission (the RPC) to the peers.
	SetShareAnnouncer(func(Share))
	// VerifyWidth is the node's own verification width (the proofs
	// verified at once) and VerifyCost its measured mean time to verify
	// one share (zero before any measurement): the fetch pacing's figures
	// (F-227), never more announced shares asked for than fetchPerWidth
	// (ten on the full form, one on the light, by the measured cost)
	// times the width a second.
	VerifyWidth() int
	VerifyCost() time.Duration
}

// The share flood's bound (F-227, the security review's cell of 16 October 2026; the
// chair's ruling aae07ac67): a flood of distinct invalid shares at 15 to 60 a
// second saturated a home raster-only sealer's verification workers at the
// light form's 100 ms, queued its honest shares past the recency window and
// its dataset-lane blocks behind, and orphaned its own raster blocks on
// stale parents. The cure, this layer's: (1) a share the node already holds
// by hash is dropped BEFORE any verification (a replayed valid share cost a
// verification before the duplicate check); (2) a peer's pushed shares are
// bounded by its bucket (peer.go: ShareRatePerPeer a second, a burst of
// ShareBurstPerPeer), the excess dropped unverified and counted; (3) the
// node's own fetches of announced shares are paced at its verification
// width in its OWN measured verification time (the security review's residual on
// the first cut: ten a second a worker is the full form's figure, 2 ms a
// share; at the light form's 100 ms it is the whole width, so a Sybil of
// peers announcing valid shares held a raster-only node's workers at 100
// percent and queued its block verification): fetchPerWidth is
// FetchPerWidthFull (ten) while the measured mean share cost is at or under
// FetchCostFull (10 ms), FetchPerWidthLight (one) at FetchCostLight (100 ms)
// and above, and between them FetchCostLight over the cost, times the
// width a second (30 on a 4-core full-form box, 3 on a light one), so no
// announcer's batch buys a batch of verifications at once and the workers
// keep their second for blocks; (4) the first share from a peer whose work
// fails is its disconnect, and the peer's identity (and, for an inbound
// peer, its address) is held out for BanWindow (the node has no standing
// ban form in this layer; without the window each new handshake bought one
// more worker slot, so the peer limit bounded nothing against reconnect
// churn), never for a static or trusted peer (the review lead's ruling 1bd4782cf:
// a node of record gossips only shares it verified, so a bad one from it is
// a verifier disagreement for the operators, dropped and counted with a
// line, not a peer to hold out; the research chain's four nodes of record
// share one address); (5) the pool's direct submit path (the sealer's own
// RPC behind the guard) is outside this bound, unchanged. The bad verdict
// in this path is the engine's TWO-PASS refusal (0.10.44's second
// verification, F-224: a share refused once and verified on the second pass
// is no strike), so the cut sits above it in the lineage.
const (
	// FetchPerWidthFull and FetchPerWidthLight are the fetch pacing's
	// multipliers at the two forms: the announced shares asked for a
	// second, per unit of the node's verification width.
	FetchPerWidthFull  = 10.0
	FetchPerWidthLight = 1.0
	// FetchCostFull and FetchCostLight are the measured mean share costs
	// the two multipliers hold at (the full form's 2 ms and the light's
	// 100 ms, with the margin).
	FetchCostFull  = 10 * time.Millisecond
	FetchCostLight = 100 * time.Millisecond
	// BanWindow is how long a peer that sent a share whose work fails is
	// held out after its disconnect.
	BanWindow = 10 * time.Minute
)

// fetchPerWidth is the pacing's multiplier for a measured mean share cost:
// ten at or under the full form's figure (and before any measurement), one
// at or over the light form's, the light figure over the cost between.
func fetchPerWidth(cost time.Duration) float64 {
	if cost <= FetchCostFull {
		return FetchPerWidthFull
	}
	if cost >= FetchCostLight {
		return FetchPerWidthLight
	}
	return float64(FetchCostLight) / float64(cost)
}

// Chain is what the protocol needs of the chain: the head, and a way to
// hear each new head (the backend adapts the blockchain's head event; this
// package names no core type, so the consensus engine may import it
// without a cycle through core).
type Chain interface {
	CurrentHeader() *types.Header
	// SubscribeHead calls fn on every new head until the returned func is
	// called.
	SubscribeHead(fn func(*types.Header)) (unsubscribe func())
}

// Backend is the node's side of the protocol.
type Backend interface {
	Chain() Chain
	ShareEngine() ShareEngine
}

// endorsementReceiver is the engine's optional endorsement pool: a list
// received from a peer enters it exactly as a pushed one (the signature
// over M checked per entry; the lot and the bond at the template and on
// import). The engine's SubmitEndorsements is this.
type endorsementReceiver interface {
	SubmitEndorsements(height uint64, blockHash common.Hash, entries []byte) (int, error)
}

// endorsementAnnouncerSetter gives the engine the way to hand its own
// endorsements to the mesh (the endorser's tick, beside the push).
type endorsementAnnouncerSetter interface {
	SetEndorsementAnnouncer(func(height uint64, blockHash common.Hash, entries []byte))
}

// expirer is the optional per-head sweep of the pool (the engine's
// ExpireSharesAtHead over the node team's ExpireShares): a non-sealing node
// never templates, so its pool is swept here on every new head.
type expirer interface {
	ExpireSharesAtHead(head *types.Header)
}

// Handler is the share protocol's state: the peers on it and the counters.
type Handler struct {
	backend Backend
	engine  ShareEngine
	chain   Chain

	peerMu sync.RWMutex
	peers  map[string]*Peer

	reqID atomic.Uint64

	// fetching is the in-flight set: a hash asked of one announcer is not
	// asked of the others for the arrival allowance (devnet7, 17:5x UK, 11
	// October 2026: nine peers announced each share within one round trip
	// and the peer fetched it from all of them, refusing six copies of
	// seven as duplicates; the eth transaction fetcher's rule).
	fetchMu  sync.Mutex
	fetching map[common.Hash]time.Time
	// The fetch pacing's bucket (F-227): fetchTokens refilled at
	// fetchPerWidth (by the measured share cost) times the engine's
	// verification width a second, the
	// burst one second's worth; a hash is asked for only with a token.
	fetchTokens float64
	fetchFilled time.Time

	// banned holds the peers disconnected for a share whose work failed
	// (by node id and, for an inbound peer, by remote address) until their
	// window ends (F-227); byID marks the id entries (the count of peers
	// held out, the stats' figure).
	banMu  sync.Mutex
	banned map[string]banEntry

	received  atomic.Uint64 // shares received from peers and pooled
	forwarded atomic.Uint64 // announcements sent (one per share per announced peer)
	pushed    atomic.Uint64 // shares pushed whole to the square-root subset (one per share per pushed peer)
	fetched   atomic.Uint64 // shares served to peers' fetches
	dropMu    sync.Mutex
	droppedBy map[string]uint64 // received shares refused, by reason

	// The endorsement gossip (ISHR3): the lists seen (spread once), the
	// engine's pool when it has one, the counters.
	seenEndorse *knownSet
	// lastOwn is this node's own latest endorsement list (BroadcastEndorsements),
	// handed to a peer that connects after it was made: a list made before
	// the peer was there never reached it otherwise (the scratch pair's read
	// of 09:4x UK, 12 October 2026: the endorser's tick ran within the
	// node's first seconds, the peer came after, the counters stayed zero;
	// a sealer's restart at a roll is the fleet's case).
	lastOwnMu       sync.Mutex
	lastOwn         *EndorsementsPacket
	endorseReceiver endorsementReceiver
	endorseIn       atomic.Uint64 // lists received from peers and pooled
	endorseOut      atomic.Uint64 // lists handed to peers (one per list per peer)

	unsubscribeHead func()
	quit            chan struct{}
	wg              sync.WaitGroup
}

// NewHandler builds the protocol's state over the backend and wires the
// engine's announcer; Start runs the head sweep.
func NewHandler(backend Backend) *Handler {
	h := &Handler{backend: backend, engine: backend.ShareEngine(), chain: backend.Chain(), peers: make(map[string]*Peer), droppedBy: make(map[string]uint64), quit: make(chan struct{}), seenEndorse: newKnownSet(maxSeenEndorsements)}
	h.engine.SetShareAnnouncer(h.Broadcast)
	if r, ok := h.engine.(endorsementReceiver); ok {
		h.endorseReceiver = r
	}
	if s, ok := h.engine.(endorsementAnnouncerSetter); ok {
		s.SetEndorsementAnnouncer(h.BroadcastEndorsements)
	}
	return h
}

// Start runs the per-head pool sweep when the engine offers one (a
// non-sealing node never templates, so its pool is swept here).
func (h *Handler) Start() {
	if ex, ok := h.engine.(expirer); ok {
		h.unsubscribeHead = h.chain.SubscribeHead(func(head *types.Header) {
			if head != nil {
				ex.ExpireSharesAtHead(head)
			}
		})
	}
}

// Stop ends the sweep.
func (h *Handler) Stop() {
	close(h.quit)
	if h.unsubscribeHead != nil {
		h.unsubscribeHead()
	}
	h.wg.Wait()
}

// Stats is igneum_shareStats' gossip half: the peers on the protocol; the
// shares received from peers and pooled; the hashes announced (one per
// share per announced peer); the shares pushed whole (one per share per
// pushed peer, the square-root subset); the shares served to peers'
// fetches; the received shares dropped, by reason.
type Stats struct {
	Peers     int               `json:"gossipPeers"`
	Received  uint64            `json:"received"`
	Announced uint64            `json:"announced"`
	Pushed    uint64            `json:"pushed"`
	Served    uint64            `json:"served"`
	DroppedBy map[string]uint64 `json:"droppedBy,omitempty"`
	// The endorsement gossip's counters (ISHR3): lists received from
	// peers and pooled, lists handed to peers.
	EndorseIn  uint64 `json:"endorsementsIn"`
	EndorseOut uint64 `json:"endorsementsOut"`
	// HeldOut is the number of peers inside their ban window (F-227).
	HeldOut int `json:"heldOut"`
}

// Stats reads the counters.
func (h *Handler) Stats() Stats {
	h.peerMu.RLock()
	n := len(h.peers)
	h.peerMu.RUnlock()
	h.dropMu.Lock()
	var by map[string]uint64
	if len(h.droppedBy) > 0 {
		by = make(map[string]uint64, len(h.droppedBy))
		for k, v := range h.droppedBy {
			by[k] = v
		}
	}
	h.dropMu.Unlock()
	return Stats{Peers: n, Received: h.received.Load(), Announced: h.forwarded.Load(), Pushed: h.pushed.Load(), Served: h.fetched.Load(), DroppedBy: by, EndorseIn: h.endorseIn.Load(), EndorseOut: h.endorseOut.Load(), HeldOut: h.heldOutCount(time.Now())}
}

func (h *Handler) dropped(reason string) {
	h.dropMu.Lock()
	h.droppedBy[reason]++
	h.dropMu.Unlock()
}

// MakeProtocols constructs the P2P protocol definitions for `ishr`.
func MakeProtocols(h *Handler, dnsdisc enode.Iterator) []p2p.Protocol {
	protocols := make([]p2p.Protocol, len(ProtocolVersions))
	for i, version := range ProtocolVersions {
		version := version // Closure
		protocols[i] = p2p.Protocol{
			Name:    ProtocolName,
			Version: version,
			Length:  protocolLengths[version],
			Run: func(p *p2p.Peer, rw p2p.MsgReadWriter) error {
				peer := NewPeer(version, p, rw)
				return h.RunPeer(peer)
			},
			NodeInfo: func() interface{} {
				return map[string]interface{}{"version": version}
			},
			PeerInfo: func(id enode.ID) interface{} {
				h.peerMu.RLock()
				defer h.peerMu.RUnlock()
				if p, ok := h.peers[id.String()]; ok {
					return map[string]interface{}{"version": p.version}
				}
				return nil
			},
			DialCandidates: dnsdisc,
		}
	}
	return protocols
}

// RunPeer registers the peer, serves it until it fails, and unregisters it.
func (h *Handler) RunPeer(peer *Peer) error {
	// A peer held out for a bad share is refused at the handshake (F-227:
	// the reconnect churn bought a worker slot a handshake otherwise).
	if until, held := h.heldOut(peer, time.Now()); held {
		return fmt.Errorf("%w: until %s", errBanned, until.UTC().Format(time.RFC3339))
	}
	h.peerMu.Lock()
	if _, dup := h.peers[peer.id]; dup {
		h.peerMu.Unlock()
		return fmt.Errorf("ishr: peer %s already registered", peer.id)
	}
	h.peers[peer.id] = peer
	h.peerMu.Unlock()
	defer func() {
		h.peerMu.Lock()
		delete(h.peers, peer.id)
		h.peerMu.Unlock()
	}()
	// The catch-up: this node's own latest endorsement list to the new
	// peer, once (the peer's known set keeps it from a second handing).
	if peer.version >= ISHR3 {
		h.lastOwnMu.Lock()
		last := h.lastOwn
		h.lastOwnMu.Unlock()
		if last != nil && !peer.Knows(last.Hash()) {
			if err := peer.SendEndorsements(last); err == nil {
				h.endorseOut.Add(1)
			}
		}
	}
	peer.Log().Debug("ishr peer connected", "version", peer.version)
	for {
		if err := h.HandleMessage(peer); err != nil {
			peer.Log().Debug("Message handling failed in `ishr`", "err", err)
			return err
		}
	}
}

// HandleMessage reads and serves one message. A returned error ends the
// peer (a malformed message, a bound exceeded, a share whose work fails).
func (h *Handler) HandleMessage(peer *Peer) error {
	msg, err := peer.rw.ReadMsg()
	if err != nil {
		return err
	}
	defer msg.Discard()
	if msg.Size > maxMessageSize {
		return fmt.Errorf("%w: %v > %v", errMsgTooLarge, msg.Size, maxMessageSize)
	}
	switch msg.Code {
	case NewShareHashesMsg:
		var ann NewShareHashesPacket
		if err := msg.Decode(&ann); err != nil {
			return fmt.Errorf("%w: %v", errDecode, err)
		}
		if len(ann) > maxAnnounces {
			return fmt.Errorf("%w: %d announces", errTooMany, len(ann))
		}
		return h.handleAnnounce(peer, ann)

	case GetSharesMsg:
		var req GetSharesPacket
		if err := msg.Decode(&req); err != nil {
			return fmt.Errorf("%w: %v", errDecode, err)
		}
		if len(req.Hashes) > maxRetrieval {
			return fmt.Errorf("%w: %d hashes", errTooMany, len(req.Hashes))
		}
		shares := make([]Share, 0, len(req.Hashes))
		for _, hash := range req.Hashes {
			if s, ok := h.engine.ShareByHash(hash); ok {
				shares = append(shares, s)
			}
		}
		h.fetched.Add(uint64(len(shares)))
		return peer.SendShares(req.RequestId, shares)

	case SharesMsg:
		var res SharesPacket
		if err := msg.Decode(&res); err != nil {
			return fmt.Errorf("%w: %v", errDecode, err)
		}
		if len(res.Shares) > maxRetrieval {
			return fmt.Errorf("%w: %d shares", errTooMany, len(res.Shares))
		}
		return h.handleShares(peer, res.Shares)

	case EndorsementsMsg:
		if peer.version < ISHR3 {
			return fmt.Errorf("%w: %v on ishr/%d", errInvalidMsgCode, msg.Code, peer.version)
		}
		var pkt EndorsementsPacket
		if err := msg.Decode(&pkt); err != nil {
			return fmt.Errorf("%w: %v", errDecode, err)
		}
		if len(pkt.Entries) > maxEndorsementBytes {
			return fmt.Errorf("%w: %d endorsement bytes", errTooMany, len(pkt.Entries))
		}
		return h.handleEndorsements(peer, &pkt)

	default:
		return fmt.Errorf("%w: %v", errInvalidMsgCode, msg.Code)
	}
}

// inWindow says a height is within the recency of the head's successor.
func (h *Handler) inWindow(height uint64) bool {
	head := h.chain.CurrentHeader()
	if head == nil {
		return false
	}
	next := head.Number.Uint64() + 1
	return height <= next && next-height < h.engine.ShareWindow()
}

// fetchAllowance is how long a hash asked of one announcer is left to
// arrive before another announcer is asked (the transaction fetcher's
// 500 ms, with the proof-bearing block's import in the same loop allowed
// for).
const fetchAllowance = 2 * time.Second

// claimFetch says whether the caller should ask for the hash now: not when
// another announcer was asked within the allowance. Stale entries are
// pruned as they are met.
func (h *Handler) claimFetch(hash common.Hash, now time.Time) bool {
	h.fetchMu.Lock()
	defer h.fetchMu.Unlock()
	if h.fetching == nil {
		h.fetching = make(map[common.Hash]time.Time)
	}
	if t, ok := h.fetching[hash]; ok && now.Sub(t) < fetchAllowance {
		return false
	}
	if len(h.fetching) > 4*maxAnnounces {
		for k, t := range h.fetching {
			if now.Sub(t) >= fetchAllowance {
				delete(h.fetching, k)
			}
		}
	}
	h.fetching[hash] = now
	return true
}

// arrived clears a hash from the in-flight set (it arrived, or was refused).
func (h *Handler) arrived(hash common.Hash) {
	h.fetchMu.Lock()
	delete(h.fetching, hash)
	h.fetchMu.Unlock()
}

// fetchBudget takes up to n tokens from the fetch pacing's bucket at now
// (F-227: the bucket refilled at fetchPerWidth times the verification width
// a second, the burst one second's worth) and returns how many it got.
func (h *Handler) fetchBudget(n int, now time.Time) int {
	h.fetchMu.Lock()
	defer h.fetchMu.Unlock()
	rate := fetchPerWidth(h.engine.VerifyCost()) * float64(h.engine.VerifyWidth())
	if rate < 1 {
		rate = 1
	}
	if h.fetchFilled.IsZero() {
		h.fetchTokens, h.fetchFilled = rate, now
	} else if elapsed := now.Sub(h.fetchFilled).Seconds(); elapsed > 0 {
		h.fetchTokens += elapsed * rate
		if h.fetchTokens > rate {
			h.fetchTokens = rate
		}
		h.fetchFilled = now
	}
	got := int(h.fetchTokens)
	if got > n {
		got = n
	}
	h.fetchTokens -= float64(got)
	return got
}

// handleAnnounce fetches what the node lacks of an announcement, from one
// announcer at a time per hash, within the fetch pacing's budget (the
// rest of an announcement is left for its re-announce or another
// announcer; F-227).
func (h *Handler) handleAnnounce(peer *Peer, ann NewShareHashesPacket) error {
	var want []common.Hash
	now := time.Now()
	budget := h.fetchBudget(maxRetrieval, now)
	for _, a := range ann {
		peer.MarkKnown(a.Hash)
		if h.engine.HasShare(a.Hash) || !h.inWindow(a.Height) {
			continue
		}
		if len(want) >= budget {
			h.dropped("fetch paced")
			continue
		}
		if !h.claimFetch(a.Hash, now) {
			continue
		}
		want = append(want, a.Hash)
		if len(want) == maxRetrieval {
			break
		}
	}
	if len(want) == 0 {
		return nil
	}
	return peer.RequestShares(h.reqID.Add(1), want)
}

// holdOut records a peer's disconnect for a bad share: its node id and its
// remote address are refused at the handshake for BanWindow (F-227).
func (h *Handler) holdOut(peer *Peer, now time.Time) {
	h.banMu.Lock()
	defer h.banMu.Unlock()
	if h.banned == nil {
		h.banned = make(map[string]banEntry)
	}
	until := now.Add(BanWindow)
	h.banned[peer.id] = banEntry{until: until, byID: true}
	// The remote host is a key for an INBOUND peer alone (the churn is
	// inbound; an outbound dial is the node's own choice, and a fleet
	// behind one address must not be held out with one of its own).
	if host := peer.remoteHost(); host != "" && peer.inbound {
		h.banned[host] = banEntry{until: until}
	}
	for k, e := range h.banned {
		if !now.Before(e.until) {
			delete(h.banned, k)
		}
	}
}

// banEntry is one hold-out: its end, and whether the key is a node id (a
// peer held out) rather than an inbound peer's address.
type banEntry struct {
	until time.Time
	byID  bool
}

// heldOut says whether a peer (by node id or remote address) is inside its
// window, and when it ends. A static or trusted peer is never held out, at
// the handshake as at the share (the review lead's ruling 1bd4782cf; the security review's
// read cc4f24c38: a node of record sharing an address with a held-out
// inbound outsider must not be refused by the address key).
func (h *Handler) heldOut(peer *Peer, now time.Time) (time.Time, bool) {
	if peer.static {
		return time.Time{}, false
	}
	h.banMu.Lock()
	defer h.banMu.Unlock()
	for _, k := range []string{peer.id, peer.remoteHost()} {
		if k == "" {
			continue
		}
		if e, ok := h.banned[k]; ok && now.Before(e.until) {
			return e.until, true
		}
	}
	return time.Time{}, false
}

// heldOutCount is the number of peers (id entries) inside their window
// (the stats).
func (h *Handler) heldOutCount(now time.Time) int {
	h.banMu.Lock()
	defer h.banMu.Unlock()
	n := 0
	for _, e := range h.banned {
		if e.byID && now.Before(e.until) {
			n++
		}
	}
	return n
}

// handleShares verifies and pools received shares, forwards the pooled ones
// once, and drops the peer on a share whose work fails.
// BadShareDropThreshold is the number of received shares whose work fails,
// on one link, at which the link is dropped: ONE (F-227; the three of the
// 11 October form bought a flooder two more verifications a handshake). The
// drop is logged by peer, height and parent, so a share accepted on one
// node and failing on another still reads as the two nodes' difference.
const BadShareDropThreshold = 1

func (h *Handler) handleShares(peer *Peer, shares []Share) error {
	var forward []Share
	now := time.Now()
	for i := range shares {
		s := shares[i]
		hash := s.Hash()
		peer.MarkKnown(hash)
		h.arrived(hash)
		// A share the node holds, or held, by hash: dropped before any
		// verification (F-227's replay flood: a pushed copy of a valid
		// share cost the light form's 100 ms before the duplicate check).
		if h.engine.HasShare(hash) {
			h.dropped("known")
			continue
		}
		// The peer's bucket: a share past it is dropped unverified.
		if !peer.allowShare(now) {
			peer.overRate++
			h.dropped("rate")
			if peer.overRate == 1 || peer.overRate%1000 == 0 {
				peer.logger.Warn("Share gossip: a peer's shares past its rate", "overRate", peer.overRate, "rate", ShareRatePerPeer, "burst", ShareBurstPerPeer)
			}
			continue
		}
		if !h.inWindow(s.Height) {
			h.dropped("out of window")
			continue
		}
		ok, reason, bad := h.engine.ReceiveShare(s, peer.id)
		if bad {
			// A share whose work fails (the engine's two-pass verdict):
			// counted and named in the log (the peer, the height, the
			// share's parent). From a static or trusted peer it is a
			// verifier disagreement for the operators, never a
			// disconnect; from any other peer the link is dropped at the
			// threshold and the peer held out for the window.
			h.dropped(reason)
			peer.bad++
			if peer.static {
				peer.logger.Warn("Share gossip: a node of record's share fails the work (a verifier disagreement, not a disconnect)", "height", s.Height, "parent", s.ParentHash, "address", s.Address, "bad", peer.bad)
				continue
			}
			peer.logger.Warn("Share gossip: a received share's work fails", "height", s.Height, "parent", s.ParentHash, "address", s.Address, "bad", peer.bad, "threshold", BadShareDropThreshold, "heldOut", BanWindow)
			if peer.bad >= BadShareDropThreshold {
				h.holdOut(peer, now)
				return fmt.Errorf("%w: %s", errBadShare, reason)
			}
			continue
		}
		if !ok {
			h.dropped(reason)
			continue
		}
		h.received.Add(1)
		forward = append(forward, s)
	}
	for i := range forward {
		h.Broadcast(forward[i])
	}
	return nil
}

// Broadcast hands a pooled share to every peer that does not know it, once
// (forward-once), in the transaction propagation's form (the project's order of
// 18:5x UK, 11 October 2026: a source linked to every peer served every
// peer's fetch, one copy per peer per share, 5.9 at ten peers): the share
// itself is PUSHED to a square-root subset of those peers, and the hash is
// ANNOUNCED to the rest, who fetch it from whichever neighbour reaches them
// first, so the handing spreads over the mesh and the source serves about
// sqrt(n) plus the stragglers instead of n. The engine calls it for a direct
// submission; the handler for a share received from a peer.
func (h *Handler) Broadcast(s Share) {
	hash := s.Hash()
	ann := []Announce{{Hash: hash, Height: s.Height}}
	h.peerMu.RLock()
	peers := make([]*Peer, 0, len(h.peers))
	for _, p := range h.peers {
		if !p.Knows(hash) {
			peers = append(peers, p)
		}
	}
	h.peerMu.RUnlock()
	// The pushed subset: ceil(sqrt(n)) of the peers, the map's order (a
	// random order, so the subset varies share to share).
	direct := int(math.Ceil(math.Sqrt(float64(len(peers)))))
	for i, p := range peers {
		if i < direct {
			if err := p.SendShares(0, []Share{s}); err != nil {
				p.Log().Debug("ishr push failed", "err", err)
				continue
			}
			h.pushed.Add(1)
			continue
		}
		if err := p.SendNewShareHashes(ann); err != nil {
			p.Log().Debug("ishr announce failed", "err", err)
			continue
		}
		h.forwarded.Add(1)
	}
}

// handleEndorsements pools a received list and spreads it once: a list
// seen before (this node's own, or one already received) goes no further;
// one out of the recency window is dropped; one the engine refuses as
// malformed is dropped and not forwarded (the peer is not dropped: a list
// for a block this node does not hold yet, or past its window, is late,
// not a fault); a list the engine pooled, or holds no pool for (a node
// without the rule), is handed on to the peers that have not seen it.
func (h *Handler) handleEndorsements(peer *Peer, pkt *EndorsementsPacket) error {
	hash := pkt.Hash()
	peer.MarkKnown(hash)
	if h.seenEndorse.Contains(hash) {
		return nil
	}
	h.seenEndorse.Add(hash)
	if !h.inWindow(pkt.Height) {
		h.dropped("endorsements out of window")
		return nil
	}
	if h.endorseReceiver != nil {
		if _, err := h.endorseReceiver.SubmitEndorsements(pkt.Height, pkt.BlockHash, pkt.Entries); err != nil {
			h.dropped("endorsements: " + err.Error())
			return nil
		}
		h.endorseIn.Add(1)
	}
	h.spreadEndorsements(pkt, hash)
	return nil
}

// BroadcastEndorsements is the engine's announcer: the endorser's own list
// for a block, handed to every ISHR3 peer that has not seen it (and marked
// seen here, so its return from a peer goes no further).
func (h *Handler) BroadcastEndorsements(height uint64, blockHash common.Hash, entries []byte) {
	pkt := &EndorsementsPacket{Height: height, BlockHash: blockHash, Entries: entries}
	hash := pkt.Hash()
	h.seenEndorse.Add(hash)
	h.lastOwnMu.Lock()
	if h.lastOwn == nil || height >= h.lastOwn.Height {
		h.lastOwn = pkt
	}
	h.lastOwnMu.Unlock()
	h.spreadEndorsements(pkt, hash)
}

func (h *Handler) spreadEndorsements(pkt *EndorsementsPacket, hash common.Hash) {
	h.peerMu.RLock()
	peers := make([]*Peer, 0, len(h.peers))
	for _, p := range h.peers {
		if p.version >= ISHR3 && !p.Knows(hash) {
			peers = append(peers, p)
		}
	}
	h.peerMu.RUnlock()
	for _, p := range peers {
		if err := p.SendEndorsements(pkt); err != nil {
			p.Log().Debug("ishr endorsements failed", "err", err)
			continue
		}
		h.endorseOut.Add(1)
	}
}

// PeerCount is the number of peers on the protocol.
func (h *Handler) PeerCount() int {
	h.peerMu.RLock()
	defer h.peerMu.RUnlock()
	return len(h.peers)
}

// waitPeers is a test helper: it waits until n peers are on the protocol.
func (h *Handler) waitPeers(n int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if h.PeerCount() >= n {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}
