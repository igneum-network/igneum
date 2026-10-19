package igneum

import (
	"crypto/ed25519"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/asm"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm/runtime"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/holiman/uint256"
)

func TestRegistryCodeV2Pinned(t *testing.T) {
	c := asm.NewCompiler(false)
	c.Feed(asm.Lex([]byte(RegistryAsmV2), false))
	hex, errs := c.Compile()
	if len(errs) > 0 {
		t.Fatalf("registry v2 asm: %v", errs)
	}
	if hex != common.Bytes2Hex(RegistryCodeV2) {
		t.Fatalf("registry v2 code differs from the pinned bytes: compiled %s", hex)
	}
}

func registryStateV2(t *testing.T, cap *big.Int) *state.StateDB {
	t.Helper()
	st := registryState(t, cap)
	st.SetCode(testRegistry, RegistryAlloc(cap, true).Code)
	return st
}

// TestRegistryRotation: a bond's account rotates its signer; anyone else,
// an unbonded key, or a call with value is refused; the read shows it.
func TestRegistryRotation(t *testing.T) {
	st := registryStateV2(t, testCap)
	a := &AuthorityParams{Height: 1, Registry: testRegistry, Cap: testCap}
	if _, err := callRegistry(st, testHolder, testKey, big.NewInt(500_000)); err != nil {
		t.Fatalf("bond: %v", err)
	}
	if got := a.SignerOf(st, testKey); got != testKey {
		t.Fatalf("default signer %x", got)
	}
	newSigner := common.HexToHash("0x9999")
	rotate := func(from common.Address, key, signer common.Hash, value int64) error {
		cfg := &runtime.Config{State: st, Origin: from, Value: big.NewInt(value), GasLimit: 1_000_000}
		_, _, err := runtime.Call(testRegistry, append(key[:], signer[:]...), cfg)
		return err
	}
	if err := rotate(testOther, testKey, newSigner, 0); err == nil {
		t.Fatal("another account rotated the signer")
	}
	if err := rotate(testHolder, common.HexToHash("0x22"), newSigner, 0); err == nil {
		t.Fatal("an unbonded key took a signer")
	}
	if err := rotate(testHolder, testKey, newSigner, 1); err == nil {
		t.Fatal("a rotation with value was accepted")
	}
	if err := rotate(testHolder, testKey, newSigner, 0); err != nil {
		t.Fatalf("rotation: %v", err)
	}
	if got := a.SignerOf(st, testKey); got != newSigner {
		t.Fatalf("signer after rotation %x", got)
	}
	out, err := callRegistry(st, testOther, testKey, new(big.Int))
	if err != nil || len(out) != 96 || common.BytesToHash(out[64:]) != newSigner {
		t.Fatalf("read: %v %x", err, out)
	}
	// The amount and account are untouched by the rotation.
	if b := a.BondOf(st, testKey); b.Amount.Cmp(big.NewInt(500_000)) != 0 || b.Account != testHolder {
		t.Fatalf("bond after rotation: %+v", b)
	}
}

func sortitionEngine(t *testing.T) *Engine {
	t.Helper()
	e := authorityEngine(t, false)
	e.config.Parts.Sortition = &SortitionParams{Height: 10, EpochBlocks: 100, SeedBlocks: 16, Expected: 1, Delta: 6, MaxRound: 16, UnbondedRound: 17, MaxPayouts: 256}
	return e
}

// TestLotsAndRounds: the lot is deterministic, theta grows with the round
// to everything at 2^16 / E, and the eligible round is monotone in units.
func TestLotsAndRounds(t *testing.T) {
	seed := common.HexToHash("0xabcd")
	if Lot(seed, testKey, 3, 7) != Lot(seed, testKey, 3, 7) || Lot(seed, testKey, 3, 7) == Lot(seed, testKey, 4, 7) || Lot(seed, testKey, 3, 7) == Lot(seed, testKey, 3, 8) {
		t.Fatal("lot not a function of (seed, key, unit, height)")
	}
	all := new(big.Int).Lsh(big.NewInt(1), 256)
	if Theta(1, 16).Cmp(all) != 0 || Theta(1, 15).Cmp(all) >= 0 || Theta(1, 0).Cmp(Theta(1, 1)) >= 0 {
		t.Fatal("theta ladder wrong")
	}
	s := sortitionEngine(t).config.Parts.Sortition
	if s.EligibleRound(seed, testKey, 0, 12) != s.MaxRound+1 {
		t.Fatal("no units but eligible")
	}
	if r := s.EligibleRound(seed, testKey, 1<<16, 12); r > 1 {
		t.Fatalf("every unit held but first eligible at round %d", r)
	}
	// More units never delays the draw.
	prev := s.EligibleRound(seed, testKey, 1, 12)
	for _, n := range []uint64{2, 16, 256, 4096} {
		r := s.EligibleRound(seed, testKey, n, 12)
		if r > prev {
			t.Fatalf("units %d eligible later (%d) than fewer (%d)", n, r, prev)
		}
		prev = r
	}
}

// TestSigningRoundTrip: the engine signs the template under its seed, the
// signature verifies under the bond's signer, and a changed template or
// another signer fails.
func TestSigningRoundTrip(t *testing.T) {
	e := sortitionEngine(t)
	seed := make([]byte, 32)
	seed[0] = 7
	if err := e.SetSigner(seed); err != nil {
		t.Fatal(err)
	}
	pub := *e.SignerKey()
	h := keyedHeader(12, &pub, testHolder)
	h.Round = 2
	h.Extra = []byte{}
	if err := e.signHeader(h); err != nil {
		t.Fatal(err)
	}
	if len(h.Sig) != 64 || !ed25519.Verify(ed25519.PublicKey(pub[:]), e.SigningHash(h).Bytes(), h.Sig) {
		t.Fatal("signature does not verify")
	}
	// The seal hash carries the round and the signature; the signing hash does not carry the signature.
	h2 := types.CopyHeader(h)
	h2.Round = 3
	if e.SealHash(h) == e.SealHash(h2) || e.SigningHash(h) == e.SigningHash(h2) {
		t.Fatal("round not in the hashes")
	}
	h3 := types.CopyHeader(h)
	h3.Sig = append([]byte{}, h.Sig...)
	h3.Sig[0] ^= 1
	if e.SealHash(h) == e.SealHash(h3) || e.SigningHash(h) != e.SigningHash(h3) {
		t.Fatal("signature in the wrong hash")
	}
	// Stateful: the bond's signer at the parent's state.
	st := registryStateV2(t, testCap)
	if _, err := callRegistry(st, testHolder, pub, big.NewInt(500_000)); err != nil {
		t.Fatal(err)
	}
	// With no chain anchored, the draw cannot be read; the unbonded round needs only the signature.
	h.Round = 17
	if err := e.signHeader(h); err != nil {
		t.Fatal(err)
	}
	if err := e.verifySortition(h, st); err != nil {
		t.Fatalf("unbonded round: %v", err)
	}
	h.Sig[5] ^= 1
	if err := e.verifySortition(h, st); !errors.Is(err, errSortitionBadSig) {
		t.Fatalf("tampered signature: %v", err)
	}
	// A rotated signer: the old seed's signature fails, the new one verifies.
	seed2 := make([]byte, 32)
	seed2[0] = 9
	e2 := sortitionEngine(t)
	e2.SetSigner(seed2)
	newSigner := *e2.SignerKey()
	cfg := &runtime.Config{State: st, Origin: testHolder, Value: new(big.Int), GasLimit: 1_000_000}
	if _, _, err := runtime.Call(testRegistry, append(pub[:], newSigner[:]...), cfg); err != nil {
		t.Fatal(err)
	}
	h.Round = 0
	e.signHeader(h)
	if err := e.verifySortition(h, st); !errors.Is(err, errSortitionBadSig) {
		t.Fatalf("old signer after rotation: %v", err)
	}
	e2.signHeader(h)
	if err := e2.verifySortition(h, st); errors.Is(err, errSortitionBadSig) {
		t.Fatalf("new signer after rotation: %v", err)
	}
	// Stateless: fields by height, the round bound, the time floor.
	s := e.config.Parts.Sortition
	parent := keyedHeader(11, nil, testHolder)
	parent.Time = 1000
	h.Time = 1000 + 2*6 - 1
	h.Round = 2
	if err := s.verifySortitionHeader(h, parent); !errors.Is(err, errSortitionEarlyTime) {
		t.Fatalf("time floor: %v", err)
	}
	h.Time = 1000 + 2*6
	if err := s.verifySortitionHeader(h, parent); err != nil {
		t.Fatalf("at the floor: %v", err)
	}
	h.Round = 18
	if err := s.verifySortitionHeader(h, parent); !errors.Is(err, errSortitionRound) {
		t.Fatalf("round bound: %v", err)
	}
	early := keyedHeader(9, &pub, testHolder)
	early.Sig = h.Sig
	if err := s.verifySortitionHeader(early, nil); !errors.Is(err, errSortitionEarly) {
		t.Fatalf("early signature: %v", err)
	}
}

// TestPayoutsAndWeight: the pro-rata split sums to the amount, the list's
// bounds hold, and the fork weight halves per round.
func TestPayoutsAndWeight(t *testing.T) {
	e := sortitionEngine(t)
	st := registryStateV2(t, testCap)
	list := []*types.Withdrawal{{Address: testHolder, Amount: 1}, {Address: testOther, Amount: 3}}
	amount := big.NewInt(1_000_001)
	payProRata(st, amount, list)
	a, b := st.GetBalance(testHolder).ToBig(), st.GetBalance(testOther).ToBig()
	a.Sub(a, big.NewInt(10_000_000))
	b.Sub(b, big.NewInt(10_000_000))
	if new(big.Int).Add(a, b).Cmp(amount) != 0 || a.Cmp(big.NewInt(250_000)) != 0 {
		t.Fatalf("split %v %v", a, b)
	}
	h := keyedHeader(12, &testKey, testHolder)
	if err := e.verifyPayoutList(h, list); !errors.Is(err, errPayoutList) {
		t.Fatalf("list without root: %v", err)
	}
	root := types.DeriveSha(types.Withdrawals(list), trie.NewStackTrie(nil))
	h.WithdrawalsHash = &root
	if err := e.verifyPayoutList(h, list); !errors.Is(err, errPayoutList) {
		t.Fatalf("coinbase not bound to the root: %v", err)
	}
	h.Coinbase = PayoutCoinbase(root)
	if err := e.verifyPayoutList(h, list); err != nil {
		t.Fatalf("bound list: %v", err)
	}
	long := make([]*types.Withdrawal, 257)
	for i := range long {
		long[i] = &types.Withdrawal{Address: testHolder, Amount: 1}
	}
	if err := e.verifyPayoutList(h, long); !errors.Is(err, errPayoutList) {
		t.Fatalf("257 entries: %v", err)
	}
	h.Difficulty = big.NewInt(1024)
	h.Round = 3
	h.Sig = make([]byte, 64)
	if e.BlockWeight(h).Int64() != 128 {
		t.Fatalf("weight %v", e.BlockWeight(h))
	}
	h.Round = 0
	if e.BlockWeight(h).Int64() != 1024 {
		t.Fatalf("weight at round 0 %v", e.BlockWeight(h))
	}
	before := keyedHeader(5, nil, testHolder)
	before.Difficulty = big.NewInt(1024)
	before.Round = 3
	if e.BlockWeight(before).Int64() != 1024 {
		t.Fatal("weight before the rule")
	}
	_ = uint256.NewInt
}

// TestFeesUnderPayoutList (F-35's regression): the fee rule under a payout
// list, stated once and the same in assembly and on import. The EVM credits
// a block's tips to the header's coinbase, which under a list is the list
// root's prefix; Finalize moves that address's whole balance into the
// pro-rata payment beside the subsidy, so the list's addresses take the
// coinbase's share and the prefix ends at zero. Without a list the
// coinbase keeps its fees and takes the subsidy as before. The two paths
// share Finalize, so one call on one state is the rule; the worker's half
// (the sealing environment's fee recipient is header.Coinbase, never the
// etherbase) is the one-line change in miner/worker.go beside it.
func TestFeesUnderPayoutList(t *testing.T) {
	e := sortitionEngine(t)
	list := []*types.Withdrawal{{Address: testHolder, Amount: 1}, {Address: testOther, Amount: 3}}
	root := types.DeriveSha(types.Withdrawals(list), trie.NewStackTrie(nil))
	h := keyedHeader(12, &testKey, PayoutCoinbase(root))
	h.WithdrawalsHash = &root

	st := registryStateV2(t, testCap)
	// The tips the EVM credited to the prefix during the block's
	// transactions: 4,000 wei.
	st.SetBalance(h.Coinbase, uint256.NewInt(4000))
	subsidy, _ := e.config.Parts.Issuance.Reward(12)
	e.Finalize(nil, h, st, nil, nil, list)
	if got := st.GetBalance(h.Coinbase); !got.IsZero() {
		t.Fatalf("the prefix keeps %v after Finalize", got)
	}
	a := new(big.Int).Sub(st.GetBalance(testHolder).ToBig(), big.NewInt(10_000_000))
	b := new(big.Int).Sub(st.GetBalance(testOther).ToBig(), big.NewInt(10_000_000))
	want := new(big.Int).Add(subsidy, big.NewInt(4000))
	if new(big.Int).Add(a, b).Cmp(want) != 0 {
		t.Fatalf("the list received %v, want subsidy + fees = %v", new(big.Int).Add(a, b), want)
	}
	if quarter := new(big.Int).Div(want, big.NewInt(4)); a.Cmp(quarter) != 0 {
		t.Fatalf("the 1-weight entry received %v, want a quarter %v", a, quarter)
	}
	// Determinism: a second state with the same inputs reaches the same
	// balances (the import path's Finalize is this call).
	st2 := registryStateV2(t, testCap)
	st2.SetBalance(h.Coinbase, uint256.NewInt(4000))
	e.Finalize(nil, h, st2, nil, nil, list)
	if st2.GetBalance(testHolder).Cmp(st.GetBalance(testHolder)) != 0 || st2.GetBalance(testOther).Cmp(st.GetBalance(testOther)) != 0 {
		t.Fatal("two states diverge under one rule")
	}
	// Without a list: the coinbase keeps its fees and takes the subsidy.
	plain := keyedHeader(12, &testKey, testHolder)
	st3 := registryStateV2(t, testCap)
	st3.SetBalance(testHolder, uint256.NewInt(4000))
	e.Finalize(nil, plain, st3, nil, nil, nil)
	if got := st3.GetBalance(testHolder).ToBig(); got.Cmp(new(big.Int).Add(subsidy, big.NewInt(4000))) != 0 {
		t.Fatalf("no list: coinbase holds %v", got)
	}
}

// TestBoundedTailRounds: the bounded tail (TailV1, the founder's word of
// 15 October 2026): past round 0 the rounds open every TailDelta seconds
// from the height the tail applies, the clock floor reads TailTMin, and
// below the height the single round length and tMin stand.
func TestBoundedTailRounds(t *testing.T) {
	s := &SortitionParams{Delta: 20, TMin: 12, TailDelta: 10, TailTMin: 6, TailFrom: 100}
	if s.RoundStart(99, 2) != 40 || s.RoundStart(99, 0) != 0 || s.TMinAt(99) != 12 {
		t.Fatalf("below the height: %d %d %d", s.RoundStart(99, 2), s.RoundStart(99, 0), s.TMinAt(99))
	}
	if s.RoundStart(100, 0) != 0 || s.RoundStart(100, 1) != 20 || s.RoundStart(100, 2) != 30 || s.RoundStart(100, 4) != 50 || s.TMinAt(100) != 6 {
		t.Fatalf("under the tail: %d %d %d %d %d", s.RoundStart(100, 0), s.RoundStart(100, 1), s.RoundStart(100, 2), s.RoundStart(100, 4), s.TMinAt(100))
	}
	// The genesis form: the tail from genesis when TailFrom is 0.
	g := &SortitionParams{Delta: 20, TMin: 6, TailDelta: 10}
	if g.RoundStart(1, 3) != 40 || g.TMinAt(1) != 6 {
		t.Fatalf("the genesis form: %d %d", g.RoundStart(1, 3), g.TMinAt(1))
	}
	// The verify path reads the same RoundStart by height (sortition.go
	// verifySortitionHeader), the signature checked before the floor.

}
