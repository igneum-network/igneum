package ishr

import (
	"fmt"
	"math/big"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/p2p"
)

// fakeEngine is a share pool for the transport test: a share is valid when
// its nonce is even (odd nonces fail "the work", the peer's fault); a
// duplicate is refused without fault.
type fakeEngine struct {
	mu        sync.Mutex
	pool      map[common.Hash]Share
	announcer func(Share)
	window    uint64
	// The endorsement gossip's side: the lists received (SubmitEndorsements)
	// and the announcer the handler set.
	endorsed   []EndorsementsPacket
	endorseAnn func(uint64, common.Hash, []byte)
	// cost is the fake's measured mean share verification time (the
	// fetch pacing's figure): zero reads as the full form.
	cost time.Duration
}

func (f *fakeEngine) SubmitEndorsements(height uint64, blockHash common.Hash, entries []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(entries)%100 != 0 {
		return 0, fmt.Errorf("igneum endorse: a list of %d bytes", len(entries))
	}
	f.endorsed = append(f.endorsed, EndorsementsPacket{Height: height, BlockHash: blockHash, Entries: append([]byte{}, entries...)})
	return len(entries) / 100, nil
}
func (f *fakeEngine) SetEndorsementAnnouncer(fn func(uint64, common.Hash, []byte)) { f.endorseAnn = fn }
func (f *fakeEngine) endorsedLists() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.endorsed)
}

func newFakeEngine() *fakeEngine { return &fakeEngine{pool: make(map[common.Hash]Share), window: 8} }

func (f *fakeEngine) ReceiveShare(s Share, source string) (bool, string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s.Nonce%2 == 1 {
		return false, "igneum shares: the share's work does not verify", true
	}
	if _, ok := f.pool[s.Hash()]; ok {
		return false, "igneum shares: a share carried twice", false
	}
	f.pool[s.Hash()] = s
	return true, "", false
}
func (f *fakeEngine) HasShare(h common.Hash) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.pool[h]
	return ok
}
func (f *fakeEngine) ShareByHash(h common.Hash) (Share, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.pool[h]
	return s, ok
}
func (f *fakeEngine) ShareWindow() uint64              { return f.window }
func (f *fakeEngine) SetShareAnnouncer(fn func(Share)) { f.announcer = fn }
func (f *fakeEngine) VerifyWidth() int                 { return 3 } // a 4-core home sealer's three workers
func (f *fakeEngine) VerifyCost() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cost
}
func (f *fakeEngine) setCost(d time.Duration) {
	f.mu.Lock()
	f.cost = d
	f.mu.Unlock()
}
func (f *fakeEngine) pooled() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.pool)
}
func (f *fakeEngine) add(s Share) {
	f.mu.Lock()
	f.pool[s.Hash()] = s
	f.mu.Unlock()
}

type fakeChain struct{ head *types.Header }

func (c *fakeChain) CurrentHeader() *types.Header                        { return c.head }
func (c *fakeChain) SubscribeHead(fn func(*types.Header)) (unsub func()) { return func() {} }

type fakeBackend struct {
	chain  *fakeChain
	engine *fakeEngine
}

func (b *fakeBackend) Chain() Chain             { return b.chain }
func (b *fakeBackend) ShareEngine() ShareEngine { return b.engine }

// node is a test node: its handler over a fake engine and chain at head 100.
func node() (*Handler, *fakeEngine) {
	eng := newFakeEngine()
	h := NewHandler(&fakeBackend{chain: &fakeChain{head: &types.Header{Number: big.NewInt(100)}}, engine: eng})
	return h, eng
}

var linkSeq atomic.Uint64

// link connects two handlers over a message pipe, each running the other
// as a peer (unique ids per link, so repeated runs in one process never
// collide); it returns the peers and their loops' error channels.
func link(a, b *Handler) (pa, pb *Peer, errA, errB <-chan error) {
	return linkV(a, b, ISHR2)
}

// linkV is link at a protocol version (both ends speak it, as a negotiated
// capability would have them).
func linkV(a, b *Handler, version uint) (pa, pb *Peer, errA, errB <-chan error) {
	ra, rb := p2p.MsgPipe()
	n := linkSeq.Add(1)
	pa = NewFakePeer(version, fmt.Sprintf("peer-b-of-link-%d", n), ra) // a's view of b
	pb = NewFakePeer(version, fmt.Sprintf("peer-a-of-link-%d", n), rb) // b's view of a
	ea, eb := make(chan error, 1), make(chan error, 1)
	go func() { ea <- a.RunPeer(pa) }()
	go func() { eb <- b.RunPeer(pb) }()
	a.waitPeers(1, time.Second)
	b.waitPeers(1, time.Second)
	return pa, pb, ea, eb
}

func share(nonce, height uint64) Share {
	return Share{Address: common.Address{0x11}, ExtraNonce: 1, Nonce: nonce, MixDigest: common.Hash{0x22}, ParentHash: common.Hash{0x33}, Height: height}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("not reached: %s", what)
}

// TestShareReachesThePeerAndForwardsOnce (PA-46): a share accepted by node A
// (a direct submission) is announced to B, fetched by B, verified and pooled
// at B, and B announces it onward to C but not back to A; a second
// announcement of the same share fetches nothing.
func TestShareReachesThePeerAndForwardsOnce(t *testing.T) {
	a, ea := node()
	b, eb := node()
	c, ec := node()
	_, pbOfA, _, _ := link(a, b) // pbOfA: B's view of A
	link(b, c)
	s := share(2, 101)
	ea.add(s)
	ea.announcer(s) // the engine's announce on a direct submission
	eventually(t, "B pooled the share", func() bool { return eb.HasShare(s.Hash()) })
	eventually(t, "C pooled the share through B", func() bool { return ec.HasShare(s.Hash()) })
	eventually(t, "B's stats settled", func() bool { st := b.Stats(); return st.Received == 1 && st.Announced+st.Pushed >= 1 })
	// Forward-once: B's view of A knows the hash (A announced it), so B
	// never announces it back to A, and A pools nothing from peers.
	if !pbOfA.Knows(s.Hash()) {
		t.Fatal("B's view of A does not know the share A announced")
	}
	if a.Stats().Received != 0 {
		t.Fatalf("A received its own share back: %+v", a.Stats())
	}
	// A second announce of the same share fetches nothing more: B's pool
	// holds it, so B asks for nothing and its received count stays.
	before := b.Stats().Received
	ea.announcer(s)
	eventually(t, "the re-announce was seen", func() bool { return b.Stats().Received == before })
	time.Sleep(20 * time.Millisecond)
	if b.Stats().Received != before {
		t.Fatalf("a re-announced share was fetched and pooled again")
	}
}

// TestBadShareDropsThePeer (PA-46's DoS bound; F-227's threshold of one): a
// peer whose share fails the work is disconnected at the FIRST and held
// out for the window; a peer sending a stale or duplicate share is not; a
// message over the bound is a disconnect.
func TestBadShareDropsThePeer(t *testing.T) {
	a, _ := node()
	b, eb := node()
	_, pb, errA, _ := link(a, b)
	// B sends A a share whose work fails (an odd nonce): A drops B at once
	// and holds it out.
	if err := pb.SendShares(0, []Share{share(uint64(2*BadShareDropThreshold+1), 101)}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errA:
		if err == nil {
			t.Fatal("A's peer loop ended without the bad-share error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("A did not drop the peer that sent a bad share")
	}
	if st := a.Stats(); st.DroppedBy["igneum shares: the share's work does not verify"] != uint64(BadShareDropThreshold) || st.HeldOut != 1 {
		t.Fatalf("the drop was not counted by reason, or the peer not held out: %+v", st)
	}
	// A fresh pair: a copy of a share A holds is dropped before any
	// verification (F-227's replay flood), the peer kept.
	a2, ea2 := node()
	b2, _ := node()
	_, pb2, errA2, _ := link(a2, b2)
	dup := share(4, 101)
	ea2.add(dup)
	if err := pb2.SendShares(0, []Share{dup}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errA2:
		t.Fatalf("a duplicate dropped the peer: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if a2.Stats().DroppedBy["known"] != 1 {
		t.Fatalf("the known share was not dropped by reason before verification: %+v", a2.Stats())
	}
	// A share out of the window is dropped without fault.
	if err := pb2.SendShares(0, []Share{share(6, 10)}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the out-of-window drop counted", func() bool { return a2.Stats().DroppedBy["out of window"] == 1 })
	// Over the bound: a disconnect.
	many := make([]Share, maxRetrieval+1)
	for i := range many {
		many[i] = share(uint64(2*i), 101)
	}
	if err := pb2.SendShares(0, many); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errA2:
		if err == nil {
			t.Fatal("over the bound ended the loop without an error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("A kept a peer that sent more shares than the bound")
	}
	_ = eb
}

// TestOneFetchPerHashAcrossAnnouncers (devnet7, 17:5x UK, 11 October 2026:
// nine announcers, six copies of seven refused as duplicates): a hash
// announced by two peers within the allowance is asked of ONE of them; once
// it arrives the second announce fetches nothing; after the allowance with
// no arrival the other announcer is asked.
func TestOneFetchPerHashAcrossAnnouncers(t *testing.T) {
	a, _ := node()
	b, eb := node()
	c, ec := node()
	// A has B and C as peers; B and C both hold the share and announce it.
	_, pbOfA, _, _ := link(a, b)
	_, pcOfA, _, _ := link(a, c)
	s := share(2, 101)
	eb.add(s)
	ec.add(s)
	ann := []Announce{{Hash: s.Hash(), Height: s.Height}}
	if err := pbOfA.SendNewShareHashes(ann); err != nil {
		t.Fatal(err)
	}
	if err := pcOfA.SendNewShareHashes(ann); err != nil {
		t.Fatal(err)
	}
	eventually(t, "A pooled the share", func() bool { return a.Stats().Received == 1 })
	time.Sleep(50 * time.Millisecond)
	st := a.Stats()
	if st.Received != 1 || st.DroppedBy["igneum shares: a share carried twice"] != 0 {
		t.Fatalf("A fetched the share from more than one announcer: %+v", st)
	}
	if b.Stats().Served+c.Stats().Served > 1 {
		t.Fatalf("served %d + %d fetches, want at most 1", b.Stats().Served, c.Stats().Served)
	}
	// The in-flight rule alone: a second claim within the allowance is
	// refused, after it admitted.
	h2 := common.Hash{0xaa}
	now := time.Now()
	if !a.claimFetch(h2, now) || a.claimFetch(h2, now.Add(fetchAllowance/2)) {
		t.Fatal("a hash in flight was asked of a second announcer within the allowance")
	}
	if !a.claimFetch(h2, now.Add(fetchAllowance+time.Millisecond)) {
		t.Fatal("a hash that never arrived was not asked again after the allowance")
	}
	a.arrived(h2)
	if !a.claimFetch(h2, now.Add(fetchAllowance+2*time.Millisecond)) {
		t.Fatal("a cleared hash is not claimable")
	}
}

// TestFetchServesHeldShares: a peer's fetch is answered from the index.
func TestFetchServesHeldShares(t *testing.T) {
	a, ea := node()
	b, eb := node()
	_, pb, _, _ := link(a, b)
	s1, s2 := share(2, 101), share(4, 101)
	ea.add(s1)
	ea.add(s2)
	if err := pb.RequestShares(7, []common.Hash{s1.Hash(), s2.Hash(), common.Hash{0xff}}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "B pooled the two fetched shares", func() bool { return eb.HasShare(s1.Hash()) && eb.HasShare(s2.Hash()) })
	if a.Stats().Served != 2 {
		t.Fatalf("A served %d, want 2", a.Stats().Served)
	}
}

// TestPushAndAnnounceSpreadsTheHanding (the project's order of 18:5x UK, 11
// October 2026: a source linked to every peer served every peer's fetch,
// one copy per peer per share): with ten peers that do not know a share,
// the source pushes it whole to ceil(sqrt(10)) = 4 of them and announces the
// hash to the other 6, who fetch it; every peer receives the share exactly
// once, none refuses a duplicate, and the source serves at most the six
// announced peers' fetches.
func TestPushAndAnnounceSpreadsTheHanding(t *testing.T) {
	src, es := node()
	peers := make([]*Handler, 10)
	engines := make([]*fakeEngine, 10)
	for i := range peers {
		peers[i], engines[i] = node()
		link(src, peers[i])
	}
	s := share(2, 101)
	es.add(s)
	es.announcer(s)
	for i := range peers {
		eng := engines[i]
		eventually(t, fmt.Sprintf("peer %d pooled the share", i), func() bool { return eng.HasShare(s.Hash()) })
	}
	time.Sleep(50 * time.Millisecond)
	st := src.Stats()
	if st.Pushed != 4 || st.Announced != 6 {
		t.Fatalf("the source pushed %d and announced %d, want 4 and 6", st.Pushed, st.Announced)
	}
	if st.Served > 6 {
		t.Fatalf("the source served %d fetches, want at most the 6 announced", st.Served)
	}
	for i, p := range peers {
		ps := p.Stats()
		if ps.Received != 1 || ps.DroppedBy["igneum shares: a share carried twice"] != 0 {
			t.Fatalf("peer %d: %+v (want received once, no duplicate)", i, ps)
		}
	}
}

// TestShareStepOnTheWire (PA-53, ISHR2): the declared target step travels
// with the share and reaches the peer's engine as sent; the share's hash
// is without it.
func TestShareStepOnTheWire(t *testing.T) {
	a, ea := node()
	b, eb := node()
	link(a, b)
	s := share(4, 101)
	s.Step = 3
	plain := share(4, 101)
	if s.Hash() != plain.Hash() {
		t.Fatal("the step is in the share's identity")
	}
	ea.add(s)
	ea.announcer(s)
	eventually(t, "B pooled the share", func() bool { return eb.HasShare(s.Hash()) })
	eb.mu.Lock()
	got := eb.pool[s.Hash()]
	eb.mu.Unlock()
	if got.Step != 3 {
		t.Fatalf("the step on B: %d, want 3", got.Step)
	}
	_ = b
}

// entries makes n endorsement entries of the wire's 100 bytes each.
func entries(n int, seed byte) []byte {
	b := make([]byte, 100*n)
	for i := range b {
		b[i] = seed + byte(i%7)
	}
	return b
}

// TestEndorsementsSpreadOnceOverTheMesh (ISHR3): the endorser's own list
// handed to the handler reaches every sealer on the mesh once (A to B to
// C), enters each engine's pool once, never returns to its source, and a
// second handing of the same list goes nowhere.
func TestEndorsementsSpreadOnceOverTheMesh(t *testing.T) {
	a, ea := node()
	b, eb := node()
	c, ec := node()
	linkV(a, b, ISHR3)
	linkV(b, c, ISHR3)
	if ea.endorseAnn == nil {
		t.Fatal("the handler set no endorsement announcer on the engine")
	}
	list := entries(3, 0x10)
	ea.endorseAnn(101, common.Hash{0xab}, list)
	eventually(t, "B pooled the list", func() bool { return eb.endorsedLists() == 1 })
	eventually(t, "C pooled the list", func() bool { return ec.endorsedLists() == 1 })
	eb.mu.Lock()
	got := eb.endorsed[0]
	eb.mu.Unlock()
	if got.Height != 101 || got.BlockHash != (common.Hash{0xab}) || string(got.Entries) != string(list) {
		t.Fatalf("B's list: %+v", got)
	}
	// Handed again: nothing moves (the seen set); A's own pool never
	// receives its own list back from B.
	ea.endorseAnn(101, common.Hash{0xab}, list)
	time.Sleep(100 * time.Millisecond)
	if eb.endorsedLists() != 1 || ec.endorsedLists() != 1 || ea.endorsedLists() != 0 {
		t.Fatalf("lists after the second handing: A %d, B %d, C %d (want 0, 1, 1)", ea.endorsedLists(), eb.endorsedLists(), ec.endorsedLists())
	}
	if st := a.Stats(); st.EndorseOut != 1 {
		t.Fatalf("A handed %d lists, want 1", st.EndorseOut)
	}
	if st := b.Stats(); st.EndorseIn != 1 || st.EndorseOut != 1 {
		t.Fatalf("B: in %d out %d, want 1 and 1", st.EndorseIn, st.EndorseOut)
	}
	// A malformed list is dropped at the receiver and not forwarded.
	ea.endorseAnn(101, common.Hash{0xcd}, []byte{1, 2, 3})
	time.Sleep(100 * time.Millisecond)
	if eb.endorsedLists() != 1 || ec.endorsedLists() != 1 {
		t.Fatalf("a malformed list reached a pool: B %d, C %d", eb.endorsedLists(), ec.endorsedLists())
	}
	if st := b.Stats(); st.DroppedBy["endorsements: igneum endorse: a list of 3 bytes"] != 1 {
		t.Fatalf("B's drops: %+v", st.DroppedBy)
	}
}

// TestLatePeerReceivesTheLastOwnList (the catch-up): a list this node made
// before a peer connected is handed to that peer when it connects, once;
// an older list is not kept over a newer one.
func TestLatePeerReceivesTheLastOwnList(t *testing.T) {
	a, ea := node()
	b, eb := node()
	ea.endorseAnn(101, common.Hash{0x1a}, entries(2, 0x30))
	ea.endorseAnn(100, common.Hash{0x1b}, entries(2, 0x31)) // older: not kept
	time.Sleep(50 * time.Millisecond)
	if st := a.Stats(); st.EndorseOut != 0 {
		t.Fatalf("a list handed to no one counted %d", st.EndorseOut)
	}
	linkV(a, b, ISHR3)
	eventually(t, "B received A's last list on connecting", func() bool { return eb.endorsedLists() == 1 })
	eb.mu.Lock()
	got := eb.endorsed[0]
	eb.mu.Unlock()
	if got.Height != 101 {
		t.Fatalf("B received the list at %d, want the latest at 101", got.Height)
	}
	if st := a.Stats(); st.EndorseOut != 1 {
		t.Fatalf("A handed %d lists, want 1", st.EndorseOut)
	}
}

// TestMixedVersionsKeepTheShareGossip: a node on ISHR3 linked to a node on
// ISHR2 (a fleet mid-roll) keeps the share gossip whole and hands it no
// endorsements; the link stays up.
func TestMixedVersionsKeepTheShareGossip(t *testing.T) {
	a, ea := node()
	b, eb := node()
	_, _, errA, errB := linkV(a, b, ISHR2)
	s := share(6, 101)
	ea.add(s)
	ea.announcer(s)
	eventually(t, "B pooled the share over ISHR2", func() bool { return eb.HasShare(s.Hash()) })
	ea.endorseAnn(101, common.Hash{0xab}, entries(2, 0x20))
	time.Sleep(100 * time.Millisecond)
	if eb.endorsedLists() != 0 {
		t.Fatalf("an ISHR2 peer received %d endorsement lists", eb.endorsedLists())
	}
	if st := a.Stats(); st.EndorseOut != 0 {
		t.Fatalf("A handed %d lists to an ISHR2 peer", st.EndorseOut)
	}
	select {
	case err := <-errA:
		t.Fatalf("A's link ended: %v", err)
	case err := <-errB:
		t.Fatalf("B's link ended: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	s2 := share(8, 101)
	ea.add(s2)
	ea.announcer(s2)
	eventually(t, "B pooled the second share after the endorsement handing", func() bool { return eb.HasShare(s2.Hash()) })
}

// The share flood's cells (F-227, the review lead's ruling aae07ac67): the
// one-peer flood, the peer-limit flood with the reconnect churn, the honest
// rate untouched, the fetch pacing at the node's width.

// evenShares makes n distinct valid shares (even nonces from first) at a
// height inside the window.
func evenShares(first, n int) []Share {
	out := make([]Share, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, share(uint64(first+2*i), 101))
	}
	return out
}

// TestShareFloodFromOnePeer: a peer's first batch of distinct valid shares
// fills its burst; the next batch at once is dropped unverified past the
// bucket; its first invalid share is its disconnect and its hold-out, and
// a reconnect inside the window is refused at the handshake.
func TestShareFloodFromOnePeer(t *testing.T) {
	a, ea := node()
	b, _ := node()
	_, pb, errA, _ := link(a, b)
	burst := int(ShareBurstPerPeer)
	if err := pb.SendShares(0, evenShares(1000, burst)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the burst verified and pooled", func() bool { return ea.pooled() == burst })
	if err := pb.SendShares(0, evenShares(100000, burst)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the second batch dropped past the bucket", func() bool { return a.Stats().DroppedBy["rate"] >= uint64(burst-8) })
	if n := ea.pooled(); n > burst+8 {
		t.Fatalf("the flood bought %d verifications past the burst of %d", n-burst, burst)
	}
	// An invalid share past the bucket is dropped unverified like any
	// other; the strike needs a token back (a few hundred ms at 16 a
	// second), then it is the disconnect.
	time.Sleep(300 * time.Millisecond)
	if err := pb.SendShares(0, []Share{share(200001, 101)}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errA:
		if err == nil {
			t.Fatal("the loop ended without the bad-share error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the flooder's invalid share did not drop it")
	}
	// The reconnect inside the window: refused at the handshake (the
	// same node id; the fake peer has no address).
	ra, _ := p2p.MsgPipe()
	again := NewFakePeer(ISHR2, pb.id, ra)
	again.id = a.peerIDOf(pb)
	if err := a.RunPeer(again); err == nil || !strings.Contains(err.Error(), errBanned.Error()) {
		t.Fatalf("a held-out peer was admitted at the handshake: %v", err)
	}
	if st := a.Stats(); st.HeldOut != 1 {
		t.Fatalf("held out: %+v", st)
	}
}

// peerIDOf is A's id for its view of the peer that B's view pb mirrors:
// the link names them as a pair, so A's registered id is the other end's.
func (h *Handler) peerIDOf(other *Peer) string {
	h.peerMu.RLock()
	defer h.peerMu.RUnlock()
	for id := range h.peers {
		return id
	}
	// The peer already left: its id is the link's pair of the other's.
	return strings.Replace(other.id, "peer-a-of", "peer-b-of", 1)
}

// TestShareFloodFromThePeerLimitWithChurn: the peer limit's worth of peers
// each send a burst and an invalid share and reconnect; the node's
// verifications stay under peers times the burst, every flooder is held
// out, and the reconnects are refused at the handshake.
func TestShareFloodFromThePeerLimitWithChurn(t *testing.T) {
	a, ea := node()
	const peers = 8
	burst := int(ShareBurstPerPeer)
	var pbs []*Peer
	var errs []<-chan error
	for i := 0; i < peers; i++ {
		b, _ := node()
		pa, pb, errA, _ := link(a, b)
		pa.inbound, pa.host = true, fmt.Sprintf("10.9.0.%d", i+1) // A's view of the flooder: inbound from its own address
		pbs = append(pbs, pb)
		errs = append(errs, errA)
	}
	for i, pb := range pbs {
		if err := pb.SendShares(0, evenShares(1000000*(i+1), burst)); err != nil {
			t.Fatal(err)
		}
		if err := pb.SendShares(0, evenShares(1000000*(i+1)+100000, burst)); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "the second bursts dropped past the buckets", func() bool { return a.Stats().DroppedBy["rate"] >= uint64(peers*(burst-8)) })
	time.Sleep(300 * time.Millisecond) // a token back on each bucket
	for i, pb := range pbs {
		if err := pb.SendShares(0, []Share{share(uint64(1000000*(i+1)+200001), 101)}); err != nil {
			t.Fatal(err)
		}
	}
	for i, errA := range errs {
		select {
		case err := <-errA:
			if err == nil {
				t.Fatalf("flooder %d's loop ended without the bad-share error", i)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("flooder %d was not dropped", i)
		}
	}
	if n := ea.pooled(); n > peers*burst {
		t.Fatalf("the flood bought %d verifications over %d peers' bursts of %d", n, peers, burst)
	}
	if st := a.Stats(); st.HeldOut != peers || st.DroppedBy["rate"] < uint64(peers*(burst-8)) {
		t.Fatalf("the churn's hold-out: %+v", st)
	}
	// The churn: every flooder's reconnect is refused at the handshake.
	a.peerMu.RLock()
	live := len(a.peers)
	a.peerMu.RUnlock()
	if live != 0 {
		t.Fatalf("%d flooders still registered", live)
	}
	a.banMu.Lock()
	ids := make([]string, 0, len(a.banned))
	for id, e := range a.banned {
		if e.byID {
			ids = append(ids, id)
		}
	}
	a.banMu.Unlock()
	for _, id := range ids {
		ra, _ := p2p.MsgPipe()
		again := NewFakePeer(ISHR2, id, ra)
		if err := a.RunPeer(again); err == nil || !strings.Contains(err.Error(), errBanned.Error()) {
			t.Fatalf("held-out peer %s admitted at the handshake: %v", id, err)
		}
	}
}

// TestHonestShareRateUntouched: a peer handing shares at the honest rate
// (13 a second, the research chain's whole through one peer) for three
// seconds loses none to the bucket.
func TestHonestShareRateUntouched(t *testing.T) {
	a, ea := node()
	b, _ := node()
	_, pb, errA, _ := link(a, b)
	const perSecond = 13
	for round := 0; round < 3; round++ {
		if err := pb.SendShares(0, evenShares(1000+round*1000, perSecond)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
	}
	eventually(t, "every honest share pooled", func() bool { return ea.pooled() == 3*perSecond })
	select {
	case err := <-errA:
		t.Fatalf("the honest peer was dropped: %v", err)
	default:
	}
	if st := a.Stats(); st.DroppedBy["rate"] != 0 || st.Received != 3*perSecond {
		t.Fatalf("the honest rate touched: %+v", st)
	}
}

// TestFetchPacedAtTheVerificationWidth: an announcement of more hashes than
// one second of the node's fetch budget (FetchPerWidth times the width: 30
// for the fake's three workers) is fetched up to the budget, the rest left
// for a re-announce; a second announce a second later fetches the next
// budget's worth.
func TestFetchPacedAtTheVerificationWidth(t *testing.T) {
	a, ea := node()
	b, eb := node()
	_, pb, _, _ := link(a, b)
	budget := int(FetchPerWidthFull * float64(eb.VerifyWidth()))
	many := evenShares(5000, 3*budget)
	ann := make([]Announce, 0, len(many))
	for _, s := range many {
		eb.add(s)
		ann = append(ann, Announce{Hash: s.Hash(), Height: s.Height})
	}
	if err := pb.SendNewShareHashes(ann); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the first budget's worth fetched", func() bool { return ea.pooled() == budget })
	time.Sleep(150 * time.Millisecond)
	if n := ea.pooled(); n != budget {
		t.Fatalf("fetched %d of one announcement, the budget %d", n, budget)
	}
	if st := a.Stats(); st.DroppedBy["fetch paced"] != uint64(2*budget) {
		t.Fatalf("the paced hashes not counted: %+v", st)
	}
	time.Sleep(time.Second)
	if err := pb.SendNewShareHashes(ann); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the second budget's worth fetched on the re-announce", func() bool { return ea.pooled() == 2*budget })
}

// TestStaticPeersBadShareIsNoHoldOut (the review lead's ruling 1bd4782cf): a bad
// share from a static or trusted peer is dropped and counted, never a
// disconnect or a hold-out, and the other static peers behind the same
// address stay connected and served; an inbound peer outside the trusted
// set is held out by its node id AND its address, an outbound one by its
// id alone.
func TestStaticPeersBadShareIsNoHoldOut(t *testing.T) {
	a, ea := node()
	const n = 4
	var pbs []*Peer
	var errs []<-chan error
	for i := 0; i < n; i++ {
		b, _ := node()
		pa, pb, errA, _ := link(a, b)
		pa.static, pa.inbound, pa.host = true, true, "203.0.113.2" // a documentation address: the four nodes of record on one host
		pbs = append(pbs, pb)
		errs = append(errs, errA)
	}
	if err := pbs[0].SendShares(0, []Share{share(7, 101)}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the bad share counted", func() bool {
		return a.Stats().DroppedBy["igneum shares: the share's work does not verify"] == 1
	})
	for i, errA := range errs {
		select {
		case err := <-errA:
			t.Fatalf("static peer %d dropped on a bad share from peer 0: %v", i, err)
		default:
		}
	}
	if st := a.Stats(); st.HeldOut != 0 || st.Peers != n {
		t.Fatalf("a static peer held out: %+v", st)
	}
	// The other three still hand shares in.
	if err := pbs[1].SendShares(0, evenShares(2000, 3)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the other static peers' shares pooled", func() bool { return ea.pooled() == 3 })
	// An inbound peer outside the set: held out by id and by address.
	b2, _ := node()
	pa2, pb2, errA2, _ := link(a, b2)
	pa2.inbound, pa2.host = true, "203.0.113.9"
	if err := pb2.SendShares(0, []Share{share(9, 101)}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errA2:
		if err == nil {
			t.Fatal("the inbound peer's loop ended without the bad-share error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the inbound peer was not dropped")
	}
	ra, _ := p2p.MsgPipe()
	other := NewFakePeer(ISHR2, "another-id-from-the-same-address", ra)
	other.inbound, other.host = true, "203.0.113.9"
	if err := a.RunPeer(other); err == nil || !strings.Contains(err.Error(), errBanned.Error()) {
		t.Fatalf("a second inbound peer from the held-out address was admitted: %v", err)
	}
	// A static peer from the held-out address is admitted (the review lead's
	// ruling at the handshake as at the share; the security review's cc4f24c38).
	rs, _ := p2p.MsgPipe()
	nodeOfRecord := NewFakePeer(ISHR2, "a-node-of-record-behind-the-same-address", rs)
	nodeOfRecord.static, nodeOfRecord.inbound, nodeOfRecord.host = true, true, "203.0.113.9"
	admitted := make(chan error, 1)
	go func() { admitted <- a.RunPeer(nodeOfRecord) }()
	select {
	case err := <-admitted:
		t.Fatalf("a static peer from a held-out address was refused at the handshake: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	rs.Close()
	// An outbound peer outside the set: held out by id alone, its address
	// free for another.
	b3, _ := node()
	pa3, pb3, errA3, _ := link(a, b3)
	pa3.inbound, pa3.host = false, "198.51.100.4"
	if err := pb3.SendShares(0, []Share{share(11, 101)}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errA3:
		if err == nil {
			t.Fatal("the outbound peer's loop ended without the bad-share error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the outbound peer was not dropped")
	}
	rb, _ := p2p.MsgPipe()
	fresh := NewFakePeer(ISHR2, "a-fresh-id-from-the-outbound-address", rb)
	fresh.inbound, fresh.host = true, "198.51.100.4"
	done := make(chan error, 1)
	go func() { done <- a.RunPeer(fresh) }()
	select {
	case err := <-done:
		t.Fatalf("the outbound peer's address was held out: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	rb.Close()
}

// TestFetchPacedByTheVerificationForm (the security review's residual on the first
// cut): the fetch pacing reads the node's own measured share cost: at the
// full form's figure ten a second per width (30 for the fake's three
// workers), at the light form's 100 ms one per width (3), so a Sybil of
// peers announcing valid shares to a light-form node buys three
// verifications a second across all of them, the block verification's
// latency bounded by the width's second.
func TestFetchPacedByTheVerificationForm(t *testing.T) {
	for _, c := range []struct {
		cost time.Duration
		want float64
	}{{0, 10}, {2 * time.Millisecond, 10}, {10 * time.Millisecond, 10}, {50 * time.Millisecond, 2}, {100 * time.Millisecond, 1}, {400 * time.Millisecond, 1}} {
		if got := fetchPerWidth(c.cost); got != c.want {
			t.Fatalf("fetchPerWidth(%v) = %v, want %v", c.cost, got, c.want)
		}
	}
	a, ea := node()
	ea.setCost(FetchCostLight) // the light form: one fetch a second per width
	const peers = 8
	light := int(FetchPerWidthLight * float64(ea.VerifyWidth()))
	for i := 0; i < peers; i++ {
		b, eb := node()
		_, pb, _, _ := link(a, b)
		batch := evenShares(10000*(i+1), 16)
		ann := make([]Announce, 0, len(batch))
		for _, s := range batch {
			eb.add(s)
			ann = append(ann, Announce{Hash: s.Hash(), Height: s.Height})
		}
		if err := pb.SendNewShareHashes(ann); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "the light budget's worth fetched", func() bool { return ea.pooled() >= light })
	time.Sleep(200 * time.Millisecond)
	if n := ea.pooled(); n > light+1 {
		t.Fatalf("a light-form node fetched %d of the Sybil's announcements inside a second, the budget %d", n, light)
	}
	if st := a.Stats(); st.DroppedBy["fetch paced"] < uint64(peers*16-light-1) {
		t.Fatalf("the paced announcements not counted: %+v", st)
	}
}
