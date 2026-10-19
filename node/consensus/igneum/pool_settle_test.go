package igneum

import (
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/params/types/coregeth"
	"github.com/holiman/uint256"
)

func poolTestChain(id uint64) *fakeChain {
	c := newFakeChain()
	c.config = &coregeth.CoreGethChainConfig{ChainID: new(big.Int).SetUint64(id), Igneum: c.config.(*coregeth.CoreGethChainConfig).Igneum}
	return c
}

// TestPoolGateOffByDefault: every chain of record has no pool gate; an
// engine with the gate at zero, or above the block, writes nothing for a
// block carrying a shield to the pool address (the state root unchanged),
// and with the gate at or under the block the same block appends.
func TestPoolGateOffByDefault(t *testing.T) {
	for _, id := range []uint64{7310, 7322, 7358, 7398} {
		if params.IgneumPoolFromForChainID(id) != 0 {
			t.Fatalf("chain %d has a pool gate", id)
		}
	}
	key, _ := crypto.GenerateKey()
	chainID := big.NewInt(7401)
	addr := pool.Address(7401)
	signer := types.LatestSignerForChainID(chainID)
	sh := pool.Shield{Commitment: common.Hash{1}}
	sh.Proof = pool.DoubleProof(sh.Statement(7401, big.NewInt(5)), []byte("lab"))
	tx := types.MustSignNewTx(key, signer, &types.DynamicFeeTx{ChainID: chainID, Nonce: 0, To: &addr, Value: big.NewInt(5), Gas: 100000, GasFeeCap: big.NewInt(1), Data: sh.Encode()})
	header := &types.Header{Number: big.NewInt(10)}
	chain := poolTestChain(7401)
	for _, gate := range []uint64{0, 11, 10, 1} {
		st := sponsoredTestState(t)
		st.AddBalance(addr, uint256.NewInt(5)) // the EVM moved the value
		e := &Engine{log: log.Root()}
		e.SetPoolFrom(gate)
		e.SetPoolVerifier(pool.UnsafeDigestDouble{})
		before := st.IntermediateRoot(true)
		e.settlePoolTxs(chain, header, st, []*types.Transaction{tx})
		p := pool.Open(st, addr, 7401, pool.Poseidon2{})
		if gate == 0 || gate > 10 {
			if st.IntermediateRoot(true) != before || p.Count() != 0 {
				t.Fatalf("gate %d: the pool wrote state below its gate", gate)
			}
			continue
		}
		if p.Count() != 1 || p.Seq() != 1 || p.Turnstile().Int64() != 5 {
			t.Fatalf("gate %d: the shield is admitted: count %d seq %d turnstile %v", gate, p.Count(), p.Seq(), p.Turnstile())
		}
	}
}

// TestPoolRefusalReturnsValue: a transaction to the pool with no readable
// record is refused and its value returned to the sender in the same
// block; the engine's default verifier is the one that refuses every
// proof; a block with no pool transaction records no root.
func TestPoolRefusalReturnsValue(t *testing.T) {
	key, _ := crypto.GenerateKey()
	from := crypto.PubkeyToAddress(key.PublicKey)
	chainID := big.NewInt(7401)
	addr := pool.Address(7401)
	signer := types.LatestSignerForChainID(chainID)
	tx := types.MustSignNewTx(key, signer, &types.DynamicFeeTx{ChainID: chainID, Nonce: 0, To: &addr, Value: big.NewInt(5), Gas: 100000, GasFeeCap: big.NewInt(1), Data: []byte{0x77}})
	st := sponsoredTestState(t)
	st.AddBalance(addr, uint256.NewInt(5))
	e := &Engine{log: log.Root()}
	e.SetPoolFrom(1)
	e.settlePoolTxs(poolTestChain(7401), &types.Header{Number: big.NewInt(10)}, st, []*types.Transaction{tx})
	if st.GetBalance(from).Uint64() != 5 || st.GetBalance(addr).Sign() != 0 {
		t.Fatalf("the value is returned: sender %v pool %v", st.GetBalance(from), st.GetBalance(addr))
	}
	if e.PoolVerifier().Name() != "none" {
		t.Fatal("the default verifier refuses every proof")
	}
	other := common.Address{9}
	plain := types.MustSignNewTx(key, signer, &types.DynamicFeeTx{ChainID: chainID, Nonce: 1, To: &other, Value: big.NewInt(5), Gas: 21000, GasFeeCap: big.NewInt(1)})
	before := st.IntermediateRoot(true)
	e.settlePoolTxs(poolTestChain(7401), &types.Header{Number: big.NewInt(11)}, st, []*types.Transaction{plain})
	if st.IntermediateRoot(true) != before {
		t.Fatal("a block with no pool transaction writes nothing")
	}
}

// TestPoolStrayValueBurned (F-775): value that reached the pool address by
// a path other than a shield (an internal call: no transaction to the
// address, so the block carries none) is burned at the block's end, the
// balance equal to the turnstile again; below the gate nothing is touched.
func TestPoolStrayValueBurned(t *testing.T) {
	addr := pool.Address(7401)
	for _, gate := range []uint64{0, 1} {
		st := sponsoredTestState(t)
		st.AddBalance(addr, uint256.NewInt(9)) // an internal call's value
		e := &Engine{log: log.Root()}
		e.SetPoolFrom(gate)
		e.settlePoolTxs(poolTestChain(7401), &types.Header{Number: big.NewInt(10)}, st, nil)
		if gate == 0 && st.GetBalance(addr).Uint64() != 9 {
			t.Fatal("below the gate the pool touches nothing")
		}
		if gate == 1 && (st.GetBalance(addr).Sign() != 0 || pool.Open(st, addr, 7401, pool.Poseidon2{}).Turnstile().Sign() != 0) {
			t.Fatalf("stray value is burned: balance %v", st.GetBalance(addr))
		}
	}
}

// TestVaultHeadRaceOrdered (F-801): a thief's withdrawal and the recovery
// authority's announcement proven against one head, in one block, in either
// transaction order: the announcement is applied first and takes the head,
// the withdrawal is refused (its nullifier spent), the state root is the
// same either way, and Extract lists the announcement at the position
// Finalize appended it. Then a completion and a cancellation of one pending
// note in one block, either order: the cancellation wins. On main the
// first transaction won.
func TestVaultHeadRaceOrdered(t *testing.T) {
	key, _ := crypto.GenerateKey()
	chainID := big.NewInt(7421)
	addr := pool.Address(7421)
	signer := types.LatestSignerForChainID(chainID)
	mk := func(nonce uint64, value int64, data []byte) *types.Transaction {
		return types.MustSignNewTx(key, signer, &types.DynamicFeeTx{ChainID: chainID, Nonce: nonce, To: &addr, Value: big.NewInt(value), Gas: 300000, GasFeeCap: big.NewInt(1), Data: data})
	}
	out := func(b byte) pool.Output { return pool.Output{Commitment: [32]byte{b}, Ciphertext: []byte{b, b}} }
	chain := poolTestChain(7421)
	var roots []common.Hash
	for _, order := range []string{"the request first", "the announcement first"} {
		st := sponsoredTestState(t)
		e := &Engine{log: log.Root()}
		e.SetPoolFrom(1)
		e.SetPoolVerifier(pool.UnsafeDigestDouble{})
		// block 10: a shield, the anchor
		sh := pool.Shield{Commitment: common.Hash{1}}
		sh.Proof = pool.DoubleProof(sh.Statement(7421, big.NewInt(5)), []byte("lab"))
		st.AddBalance(addr, uint256.NewInt(5))
		e.settlePoolTxs(chain, &types.Header{Number: big.NewInt(10), Time: 1000}, st, []*types.Transaction{mk(0, 5, sh.Encode())})
		p := pool.Open(st, addr, 7421, pool.Poseidon2{})
		anchor := p.Root()
		head := [32]byte{0x31}
		w := pool.Vault{Tag: pool.VaultWithdrawTag, Anchor: anchor, Nullifiers: [][32]byte{head}, Outputs: []pool.Output{out(0x41), out(0x42)}, Height: 11, Time: 1003}
		w.Proof = pool.DoubleProof(w.Statement(7421), []byte("lab"))
		a := pool.Vault{Tag: pool.VaultAnnounceTag, Anchor: anchor, Nullifiers: [][32]byte{head}, Outputs: []pool.Output{out(0x51)}, Revealed: pool.WordBytes(pool.Word{424242, 1, 2, 3}), Height: 11, Time: 1003}
		a.Proof = pool.DoubleProof(a.Statement(7421), []byte("lab"))
		txs := []*types.Transaction{mk(1, 0, w.Encode()), mk(2, 0, a.Encode())}
		if order == "the announcement first" {
			txs = []*types.Transaction{mk(1, 0, a.Encode()), mk(2, 0, w.Encode())}
		}
		parent := pool.Open(st.Copy(), addr, 7421, pool.Poseidon2{})
		e.settlePoolTxs(chain, &types.Header{Number: big.NewInt(11), Time: 1003}, st, txs)
		q := pool.Open(st, addr, 7421, pool.Poseidon2{})
		if q.Count() != 2 || q.Leaf(1) != a.Outputs[0].Commitment || q.RevokedCount() != 1 || !q.NullifierSeen(head) {
			t.Fatalf("%s: the announcement takes the head: count %d, leaf 1 %x, revoked %d", order, q.Count(), q.Leaf(1), q.RevokedCount())
		}
		rb := pool.Extract(q, parent, 11, common.Hash{11}, txs)
		if len(rb.Records) != 1 || rb.Records[0].Kind != pool.VaultAnnounceTag || rb.Records[0].Position != 1 || rb.End != 2 {
			t.Fatalf("%s: the retained set mirrors the order: %+v", order, rb)
		}
		// block 12: a completion and a cancellation of one pending note (one position tag): the cancellation wins
		anchor2 := q.Root()
		k := pool.Vault{Tag: pool.VaultCompleteTag, Anchor: anchor2, Nullifiers: [][32]byte{{0x52}}, Ptag: [32]byte{0x70}, RevokedRoot: q.RevokedRoot(), Outputs: []pool.Output{out(0x61), out(0x62)}, Height: 12, Time: 1006}
		k.Proof = pool.DoubleProof(k.Statement(7421), []byte("lab"))
		c := pool.Vault{Tag: pool.VaultCancelTag, Anchor: anchor2, Nullifiers: [][32]byte{{0x53}}, Ptag: [32]byte{0x70}, Outputs: []pool.Output{out(0x71)}}
		c.Proof = pool.DoubleProof(c.Statement(7421), []byte("lab"))
		txs = []*types.Transaction{mk(3, 0, k.Encode()), mk(4, 0, c.Encode())}
		if order == "the announcement first" {
			txs = []*types.Transaction{mk(3, 0, c.Encode()), mk(4, 0, k.Encode())}
		}
		e.settlePoolTxs(chain, &types.Header{Number: big.NewInt(12), Time: 1006}, st, txs)
		r := pool.Open(st, addr, 7421, pool.Poseidon2{})
		if r.Count() != 3 || r.Leaf(2) != c.Outputs[0].Commitment || !r.NullifierSeen([32]byte{0x53}) || r.NullifierSeen([32]byte{0x52}) {
			t.Fatalf("%s: the cancellation beats the completion: count %d, leaf 2 %x", order, r.Count(), r.Leaf(2))
		}
		roots = append(roots, st.IntermediateRoot(true))
	}
	if roots[0] != roots[1] {
		t.Fatalf("the two transaction orders leave two state roots: %x, %x", roots[0], roots[1])
	}
}

// TestPoolBackfillReportCarriesBothBounds (F-793): the backfill report's
// two bounds are two json fields; the pruned cut gave them one tag.
func TestPoolBackfillReportCarriesBothBounds(t *testing.T) {
	b, err := json.Marshal(&PoolBackfill{From: 3, To: 9})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["from"] != float64(3) || m["to"] != float64(9) {
		t.Fatalf("the report: %s", b)
	}
}
