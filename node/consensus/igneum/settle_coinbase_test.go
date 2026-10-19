// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).

package igneum

import (
	"crypto/mlkem"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// TestCoinbaseMintUnderGate (Project Money stage two, cut two): below the
// gate a registration record is an ordinary transaction (nothing written,
// the state root unchanged) and the sealer is credited as before; under
// the gate the sealer is credited until its registration lands, a
// registration carried by block N counts from N+1 (the mint check reads
// the key before the block's records), and from then on the share is
// withheld from the coinbase and minted as a note after the block's
// records, the pool address credited, the turnstile and the minted sum
// raised, the record readable by height; a clearing registration returns
// the sealer to the public credit.
func TestCoinbaseMintUnderGate(t *testing.T) {
	key, _ := crypto.GenerateKey()
	sealer := crypto.PubkeyToAddress(key.PublicKey)
	chainID := big.NewInt(7405)
	addr := pool.Address(7405)
	signer := types.LatestSignerForChainID(chainID)
	dk, err := mlkem.GenerateKey768()
	if err != nil {
		t.Fatal(err)
	}
	reg := pool.Registration{PK: pool.WordBytes(pool.Word{9, 8, 7, 6}), EK: dk.EncapsulationKey().Bytes()}
	regTx := types.MustSignNewTx(key, signer, &types.DynamicFeeTx{ChainID: chainID, Nonce: 0, To: &addr, Gas: 100000, GasFeeCap: big.NewInt(1), Data: reg.Encode()})
	clearTx := types.MustSignNewTx(key, signer, &types.DynamicFeeTx{ChainID: chainID, Nonce: 1, To: &addr, Gas: 100000, GasFeeCap: big.NewInt(1), Data: pool.Registration{}.Encode()})
	share := big.NewInt(4763716106026549823)
	chain := poolTestChain(7405)
	header := func(n uint64) *types.Header {
		return &types.Header{Number: new(big.Int).SetUint64(n), Coinbase: sealer, ParentHash: common.Hash{byte(n)}}
	}

	// Below the gate: byte-identical to the build before.
	st := sponsoredTestState(t)
	e := &Engine{log: log.Root()}
	e.SetPoolFrom(0)
	before := st.IntermediateRoot(true)
	if m := e.paySealer(chain, header(10), st, share); m.Sign() != 0 {
		t.Fatal("withheld below the gate")
	}
	e.settlePool(chain, header(10), st, []*types.Transaction{regTx}, nil)
	if st.GetBalance(sealer).Cmp(uint256.MustFromBig(share)) != 0 {
		t.Fatal("the sealer is not credited below the gate")
	}
	st.SubBalance(sealer, uint256.MustFromBig(share))
	if st.IntermediateRoot(true) != before {
		t.Fatal("a registration wrote state below the gate")
	}

	// Under the landing check.
	st = sponsoredTestState(t)
	e = &Engine{log: log.Root()}
	e.SetPoolFrom(1)
	h := header(10)
	// block 10 carries the registration: the sealer is still credited
	m := e.paySealer(chain, h, st, share)
	e.settlePool(chain, h, st, []*types.Transaction{regTx}, m)
	if m.Sign() != 0 || st.GetBalance(sealer).Cmp(uint256.MustFromBig(share)) != 0 {
		t.Fatal("the registration's own block is not paid publicly")
	}
	p := pool.Open(st, addr, 7405, pool.Poseidon2{})
	if p.Count() != 0 || p.Seq() != 0 {
		t.Fatal("the registration's block appended")
	}
	if _, ok := p.PayoutKey(sealer); !ok {
		t.Fatal("the key is not registered")
	}
	// block 11: withheld and minted
	h = header(11)
	m = e.paySealer(chain, h, st, share)
	if m.Cmp(share) != 0 {
		t.Fatalf("withheld %v", m)
	}
	e.settlePool(chain, h, st, nil, m)
	if st.GetBalance(sealer).Cmp(uint256.MustFromBig(share)) != 0 {
		t.Fatal("the sealer was credited while minting")
	}
	p = pool.Open(st, addr, 7405, pool.Poseidon2{})
	if p.Count() != 1 || p.Seq() != 1 || p.Turnstile().Cmp(share) != 0 || p.Minted().Cmp(share) != 0 || st.GetBalance(addr).Cmp(uint256.MustFromBig(share)) != 0 {
		t.Fatalf("the mint: count %d seq %d turnstile %v minted %v balance %v", p.Count(), p.Seq(), p.Turnstile(), p.Minted(), st.GetBalance(addr))
	}
	index, ok := p.MintAt(11)
	if !ok || index != 0 {
		t.Fatal("the height record")
	}
	want := pool.Poseidon2{}.Commitment(share, reg.PK, pool.MintRandomness(7405, 11, h.ParentHash, sealer))
	if p.Leaf(0) != want {
		t.Fatal("the leaf is not the note the wallet rebuilds")
	}
	// another sealer in block 12 is paid publicly, nothing minted
	other := header(12)
	other.Coinbase = common.Address{0xee}
	m = e.paySealer(chain, other, st, share)
	e.settlePool(chain, other, st, nil, m)
	if m.Sign() != 0 || st.GetBalance(other.Coinbase).Cmp(uint256.MustFromBig(share)) != 0 {
		t.Fatal("an unregistered sealer is not paid publicly")
	}
	if _, ok := pool.Open(st, addr, 7405, pool.Poseidon2{}).MintAt(12); ok {
		t.Fatal("an unregistered sealer's block minted")
	}
	// block 13 clears the registration (its own share still minted), block 14 is public again
	h = header(13)
	m = e.paySealer(chain, h, st, share)
	e.settlePool(chain, h, st, []*types.Transaction{clearTx}, m)
	if m.Cmp(share) != 0 {
		t.Fatal("the clearing block's share is not minted")
	}
	h = header(14)
	m = e.paySealer(chain, h, st, share)
	e.settlePool(chain, h, st, nil, m)
	if m.Sign() != 0 || st.GetBalance(sealer).Cmp(uint256.MustFromBig(new(big.Int).Mul(share, big.NewInt(2)))) != 0 {
		t.Fatal("the cleared sealer is not paid publicly")
	}
	p = pool.Open(st, addr, 7405, pool.Poseidon2{})
	if p.Count() != 2 || p.Minted().Cmp(new(big.Int).Mul(share, big.NewInt(2))) != 0 || p.Turnstile().Cmp(st.GetBalance(addr).ToBig()) != 0 {
		t.Fatalf("after the clear: count %d minted %v turnstile %v balance %v", p.Count(), p.Minted(), p.Turnstile(), st.GetBalance(addr))
	}
}

// TestCoinbaseGateOffEveryChainOfRecord: the lab chain alone has the
// gate; every chain of record is at zero, so no live chain's rules change.
func TestCoinbaseGateOffEveryChainOfRecord(t *testing.T) {
	for id, from := range map[uint64]uint64{7310: 0, 7322: 0, 7358: 0, 7396: 0, 7398: 0, 7399: 0, 7402: 1, 7405: 1} {
		if got := params.IgneumPoolFromForChainID(id); got != from {
			t.Fatalf("chain %d: gate %d, want %d", id, got, from)
		}
	}
}

// TestCoinbaseMintSurvivesStraySweep (F-792, the panel's interaction check
// of F-775 against the coinbase cut): a block whose sealer share is minted
// credits the pool's balance and the turnstile together, so the stray
// sweep at the block's end (pool.BurnStray) burns nothing of it; stray
// value that reached the pool by another path in the same block is burned
// and the mint stands; a block with no pool transaction and no mint still
// runs the sweep.
func TestCoinbaseMintSurvivesStraySweep(t *testing.T) {
	key, _ := crypto.GenerateKey()
	sealer := crypto.PubkeyToAddress(key.PublicKey)
	chainID := big.NewInt(7405)
	addr := pool.Address(7405)
	signer := types.LatestSignerForChainID(chainID)
	dk, err := mlkem.GenerateKey768()
	if err != nil {
		t.Fatal(err)
	}
	reg := pool.Registration{PK: pool.WordBytes(pool.Word{9, 8, 7, 6}), EK: dk.EncapsulationKey().Bytes()}
	regTx := types.MustSignNewTx(key, signer, &types.DynamicFeeTx{ChainID: chainID, Nonce: 0, To: &addr, Gas: 100000, GasFeeCap: big.NewInt(1), Data: reg.Encode()})
	share := big.NewInt(4763716106026549823)
	chain := poolTestChain(7405)
	header := func(n uint64) *types.Header {
		return &types.Header{Number: new(big.Int).SetUint64(n), Coinbase: sealer, ParentHash: common.Hash{byte(n)}}
	}
	st := sponsoredTestState(t)
	e := &Engine{log: log.Root()}
	e.SetPoolFrom(1)
	h := header(10)
	e.settlePool(chain, h, st, []*types.Transaction{regTx}, e.paySealer(chain, h, st, share))
	// block 11: the mint, with 9 wei of stray value arriving the same block
	h = header(11)
	st.AddBalance(addr, uint256.NewInt(9))
	m := e.paySealer(chain, h, st, share)
	if m.Cmp(share) != 0 {
		t.Fatalf("withheld %v", m)
	}
	e.settlePool(chain, h, st, nil, m)
	p := pool.Open(st, addr, 7405, pool.Poseidon2{})
	if st.GetBalance(addr).Cmp(uint256.MustFromBig(share)) != 0 || p.Turnstile().Cmp(share) != 0 || p.Minted().Cmp(share) != 0 || p.Count() != 1 {
		t.Fatalf("after the mint and the sweep: balance %v turnstile %v minted %v count %d", st.GetBalance(addr), p.Turnstile(), p.Minted(), p.Count())
	}
	if pool.StrayValue(st, addr).Sign() != 0 {
		t.Fatal("stray value left after the block")
	}
	// block 12: nothing to the pool, no mint (another sealer); stray value still swept
	other := header(12)
	other.Coinbase = common.Address{0xee}
	st.AddBalance(addr, uint256.NewInt(5))
	e.settlePool(chain, other, st, nil, e.paySealer(chain, other, st, share))
	if st.GetBalance(addr).Cmp(uint256.MustFromBig(share)) != 0 {
		t.Fatalf("an empty block did not sweep: balance %v", st.GetBalance(addr))
	}
	// block 13: the mint again; the note count two, the turnstile the pool's balance
	h = header(13)
	e.settlePool(chain, h, st, nil, e.paySealer(chain, h, st, share))
	p = pool.Open(st, addr, 7405, pool.Poseidon2{})
	if p.Count() != 2 || p.Turnstile().Cmp(st.GetBalance(addr).ToBig()) != 0 || p.Turnstile().Cmp(new(big.Int).Mul(share, big.NewInt(2))) != 0 {
		t.Fatalf("the second mint: count %d turnstile %v balance %v", p.Count(), p.Turnstile(), st.GetBalance(addr))
	}
}
