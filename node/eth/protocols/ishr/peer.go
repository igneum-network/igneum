package ishr

import (
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p"
)

// Peer is a collection of relevant information we have about an `ishr` peer.
type Peer struct {
	id string // Unique ID for the peer, cached

	*p2p.Peer                   // The embedded P2P package peer
	rw        p2p.MsgReadWriter // Input/output streams for ishr
	version   uint              // Protocol version negotiated

	known  *knownSet  // the share hashes this peer announced or was sent (forward-once)
	logger log.Logger // Contextual logger with the peer id injected
	// bad counts the shares this peer sent whose work failed on this
	// link; the link is dropped at BadShareDropThreshold (handler.go).
	bad int
	// The per-peer share rate (F-227, 0.10.45): a token bucket of
	// ShareBurstPerPeer tokens refilled at ShareRatePerPeer a second; a
	// received share takes one, and a share arriving with the bucket
	// empty is dropped before any verification and counted by peer.
	tokens float64
	filled time.Time
	// overRate counts the shares this peer sent past its bucket (the
	// log's figure beside the drop).
	overRate uint64
	// static says the peer is a static or trusted one (the node's own
	// mesh: the review lead's ruling 1bd4782cf, a node of record gossips only
	// shares it verified, so a bad one from it is a verifier
	// disagreement for the operators, never a peer to hold out); inbound
	// says the peer dialled this node (the churn is inbound, so the
	// remote host is a hold-out key for an inbound peer alone; an
	// outbound dial is the node's own choice). host is a fake peer's
	// remote host for the tests (a real peer's comes from its
	// connection).
	static  bool
	inbound bool
	host    string
}

// ShareRatePerPeer and ShareBurstPerPeer are the per-peer share rate's
// bound (F-227, the security review's share-flood cell of 16 October 2026: a flood
// of distinct invalid shares at 15 to 60 a second saturated a home
// raster-only sealer's verification at the light form's 100 ms): a peer may
// hand this node ShareRatePerPeer shares a second sustained, with a burst
// of ShareBurstPerPeer (one fetch batch, maxRetrieval: a reconnecting
// peer's or a reorganisation's refill), the excess dropped unverified. The
// honest figure the bound sits near: the review lead's 13 a second through one
// peer (a sealer pushing its pool's shares on the research chain), the
// measured gossip of 16 October 0.17 a second received on the research
// chain's node0 and none on the public sealers (the pool submits to each
// sealer directly).
const (
	ShareRatePerPeer  = 16.0
	ShareBurstPerPeer = float64(maxRetrieval)
)

// NewPeer creates a wrapper for a network connection and negotiated protocol
// version.
func NewPeer(version uint, p *p2p.Peer, rw p2p.MsgReadWriter) *Peer {
	id := p.ID().String()
	peer := &Peer{id: id, Peer: p, rw: rw, version: version, known: newKnownSet(maxKnownShares), logger: log.New("peer", id[:8]), tokens: ShareBurstPerPeer, filled: time.Now()}
	if info := p.Info(); info != nil {
		peer.static = info.Network.Static || info.Network.Trusted
		peer.inbound = info.Network.Inbound
	}
	return peer
}

// NewFakePeer creates a fake ishr peer without a backing p2p peer, for testing.
func NewFakePeer(version uint, id string, rw p2p.MsgReadWriter) *Peer {
	return &Peer{id: id, rw: rw, version: version, known: newKnownSet(maxKnownShares), logger: log.New("peer", id[:8]), tokens: ShareBurstPerPeer, filled: time.Now()}
}

// remoteHost is the peer's remote address without the port (the ban's
// second key), empty for a peer without a backing connection.
func (p *Peer) remoteHost() string {
	if p.Peer == nil {
		return p.host
	}
	addr := p.RemoteAddr()
	if addr == nil {
		return ""
	}
	host := addr.String()
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return strings.Trim(host, "[]")
}

// allowShare takes one token from the peer's bucket at now, refilling it
// first by the time since the last fill at ShareRatePerPeer a second (the
// bucket never above ShareBurstPerPeer); false with the bucket empty. The
// handler's peer loop is the one caller, so no lock.
func (p *Peer) allowShare(now time.Time) bool {
	if elapsed := now.Sub(p.filled).Seconds(); elapsed > 0 {
		p.tokens += elapsed * ShareRatePerPeer
		if p.tokens > ShareBurstPerPeer {
			p.tokens = ShareBurstPerPeer
		}
	}
	p.filled = now
	if p.tokens < 1 {
		return false
	}
	p.tokens--
	return true
}

// ID retrieves the peer's unique identifier.
func (p *Peer) ID() string { return p.id }

// Version retrieves the peer's negotiated `ishr` protocol version.
func (p *Peer) Version() uint { return p.version }

// Log overrides the P2P logger with the higher level one containing only the id.
func (p *Peer) Log() log.Logger { return p.logger }

// Close signals the end of the peer; a fake peer has no backing connection.
func (p *Peer) Close() {}

// Knows says whether the peer announced or was sent the share.
func (p *Peer) Knows(hash common.Hash) bool { return p.known.Contains(hash) }

// MarkKnown records the share as known to the peer.
func (p *Peer) MarkKnown(hash common.Hash) { p.known.Add(hash) }

// SendNewShareHashes announces shares to the peer (and marks them known).
func (p *Peer) SendNewShareHashes(ann []Announce) error {
	for _, a := range ann {
		p.known.Add(a.Hash)
	}
	return p2p.Send(p.rw, NewShareHashesMsg, NewShareHashesPacket(ann))
}

// RequestShares fetches shares by hash.
func (p *Peer) RequestShares(id uint64, hashes []common.Hash) error {
	return p2p.Send(p.rw, GetSharesMsg, &GetSharesPacket{RequestId: id, Hashes: hashes})
}

// SendShares answers a fetch (and marks the shares known).
func (p *Peer) SendShares(id uint64, shares []Share) error {
	for i := range shares {
		p.known.Add(shares[i].Hash())
	}
	return p2p.Send(p.rw, SharesMsg, &SharesPacket{RequestId: id, Shares: shares})
}

// SendEndorsements hands an endorser's list to the peer (ISHR3; marks the
// list known so it is not handed twice).
func (p *Peer) SendEndorsements(pkt *EndorsementsPacket) error {
	p.known.Add(pkt.Hash())
	return p2p.Send(p.rw, EndorsementsMsg, pkt)
}

// knownSet is a bounded set of hashes: the oldest go when the bound is
// reached (the eth peer's knownCache, without the dependency).
type knownSet struct {
	mu    sync.Mutex
	max   int
	set   map[common.Hash]struct{}
	order []common.Hash
}

func newKnownSet(max int) *knownSet {
	return &knownSet{max: max, set: make(map[common.Hash]struct{})}
}

func (k *knownSet) Add(h common.Hash) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, ok := k.set[h]; ok {
		return
	}
	if len(k.order) >= k.max {
		drop := k.order[:len(k.order)-k.max+1]
		for _, d := range drop {
			delete(k.set, d)
		}
		k.order = k.order[len(drop):]
	}
	k.set[h] = struct{}{}
	k.order = append(k.order, h)
}

func (k *knownSet) Contains(h common.Hash) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	_, ok := k.set[h]
	return ok
}
