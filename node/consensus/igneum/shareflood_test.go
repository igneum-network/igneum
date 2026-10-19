package igneum

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/eth/protocols/ishr"
)

// TestGossipFlapIsNoStrike (F-227 above F-224, the review lead's ruling
// 1bd4782cf): a gossiped share whose work fails the first verification and
// passes the second (F-224's flap) is pooled and names the peer at no
// fault, so the gossip layer's one-strike hold-out never fires on a flap;
// a share failing both passes names the peer at fault.
func TestGossipFlapIsNoStrike(t *testing.T) {
	e, chain := mineShareEngine(t)
	defer e.Close()
	flaky := &flakyAlgo{Algorithm: e.algo}
	e.algo = flaky
	e.SetGossipChain(chain)
	head := chain.head
	h := head.Number.Uint64() + 1
	a := common.Address{0x55}
	d := e.shareChainDifficulty(chain, head, nil)
	hs := ShareHeaderHash(head.Hash(), h, a, 1)
	nonce, mix := solveShare(t, e, hs, h, d)
	flaky.fails.Store(1)
	ok, reason, bad := e.ReceiveShare(ishr.Share{Address: a, ExtraNonce: 1, Nonce: nonce, MixDigest: mix, ParentHash: head.Hash(), Height: h}, "peer-x")
	if !ok || bad {
		t.Fatalf("a flapping gossiped share: ok %v bad %v reason %q (want pooled, the peer at no fault)", ok, bad, reason)
	}
	if st := e.ShareStatsNow(); st.Flapped != 1 || st.Accepted != 1 {
		t.Fatalf("the flap not counted as such: flapped %d accepted %d", st.Flapped, st.Accepted)
	}
	hs2 := ShareHeaderHash(head.Hash(), h, a, 2)
	nonce2, mix2 := solveShare(t, e, hs2, h, d)
	flaky.fails.Store(2)
	ok, _, bad = e.ReceiveShare(ishr.Share{Address: a, ExtraNonce: 2, Nonce: nonce2, MixDigest: mix2, ParentHash: head.Hash(), Height: h}, "peer-x")
	if ok || !bad {
		t.Fatalf("a share failing both passes: ok %v bad %v (want refused, the peer at fault)", ok, bad)
	}
}
