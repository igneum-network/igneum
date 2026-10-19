package igneum

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// TestTemplateOrderOldestFirst (F-174, F-122's second face): on a full
// block at the cap, within a step the oldest shares go first (the share's
// height by its ancestor, then arrival), whatever the addresses' carried
// weight, so a never-carried key's older shares ride ahead of a carried
// key's newer ones; the heavier step still goes first; the per-address
// admission bound is untouched (the pool's acceptance, not the template).
func TestTemplateOrderOldestFirst(t *testing.T) {
	e, chain := mineShareEngine(t)
	defer e.Close()
	head := chain.head // height 10; the template at 11
	older := chain.byNumber[8] // a share on it is mined for height 9
	carried, fresh := common.Address{0xC1}, common.Address{0xF2}
	p := &e.sharePool
	p.mu.Lock()
	if p.byParent == nil {
		p.byParent = make(map[common.Hash][]pooledShare)
	}
	// The carried key holds carriage already (the term F-174 retires).
	p.account(carried).carried[5] = 1000
	mk := func(a common.Address, extra uint64, step uint8, parent *types.Header) pooledShare {
		return pooledShare{share: types.MinedShare{Address: a, ExtraNonce: extra, Nonce: extra, Step: step}, parent: parent.Hash(), height: parent.Number.Uint64() + 1}
	}
	// The carried key's seven shares on the head (the newest), one at a
	// heavier step; the fresh key's four older shares at the same step on
	// the ancestor two below.
	p.byParent[head.Hash()] = append(p.byParent[head.Hash()], mk(carried, 1, 5, head))
	for i := uint64(2); i <= 7; i++ {
		p.byParent[head.Hash()] = append(p.byParent[head.Hash()], mk(carried, i, 3, head))
	}
	for i := uint64(1); i <= 4; i++ {
		p.byParent[older.Hash()] = append(p.byParent[older.Hash()], mk(fresh, 10+i, 3, older))
	}
	p.mu.Unlock()
	out, _ := e.templateShares(chain, head)
	if len(out) != 8 {
		t.Fatalf("the list holds %d shares, want the cap 8", len(out))
	}
	// The heaviest step first; then the fresh key's four older step-3
	// shares in arrival order; then the carried key's newest three.
	if out[0].Address != carried || out[0].Step != 5 {
		t.Fatalf("the first share %v step %d, want the carried key's step-5 share", out[0].Address, out[0].Step)
	}
	for i := 1; i <= 4; i++ {
		if out[i].Address != fresh || out[i].ExtraNonce != uint64(10+i) {
			t.Fatalf("slot %d: %v extra %d, want the fresh key's older share %d", i, out[i].Address, out[i].ExtraNonce, 10+i)
		}
	}
	for i := 5; i < 8; i++ {
		if out[i].Address != carried || out[i].ExtraNonce != uint64(i-3) {
			t.Fatalf("slot %d: %v extra %d, want the carried key's share %d", i, out[i].Address, out[i].ExtraNonce, i-3)
		}
	}
	// Under the old order the carried key's six step-3 shares would have
	// filled the cap ahead of every fresh share: the fresh key holds four
	// of the eight slots here and the carried key's three newest wait.
	if n := e.admissionPooledPerAddress(); n != e.config.Parts.Sortition.MaxShares {
		t.Fatalf("the per-address admission bound moved: %d", n)
	}
}
