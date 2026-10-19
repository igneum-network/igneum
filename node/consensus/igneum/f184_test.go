package igneum

import (
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/vector"
	"github.com/ethereum/go-ethereum/consensus/igneum/shares"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/trie"
)

// TestFoldListOnTheWire (F-184, the research chain's stall at 6665): under
// the first-form fold the list's last entry is the sealer's name at weight
// 0 (F-180), which the list's validity rule refused ("payout list invalid:
// zero weight") at assembly and on import, so no sealer could seal the
// fold's first block. The rule admits the one zero weight, last, under the
// fold alone; a zero weight anywhere else, or below the height, is refused
// as before; the importer's pro rata (payProRata) and the sealer's
// (shares.Pay) credit the same wei, the remainder to the last weighted
// entry and nothing to the name.
func TestFoldListOnTheWire(t *testing.T) {
	e := sortitionEngine(t)
	v := vectorConfig()
	v.BalancedMinFrom = 1
	v.SealerFoldListFrom = 13
	m, err := vector.New(v, pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	e.algo = m
	list := []*types.Withdrawal{{Index: 0, Address: testHolder, Amount: 1}, {Index: 1, Address: testOther, Amount: 3}, {Index: 2, Address: testFund, Amount: 0}}
	root := types.DeriveSha(types.Withdrawals(list), trie.NewStackTrie(nil))
	bound := func(height int64) *types.Header {
		h := keyedHeader(height, &testKey, testHolder)
		h.WithdrawalsHash = &root
		h.Coinbase = PayoutCoinbase(root)
		return h
	}
	if err := e.verifyPayoutList(bound(12), list); !errors.Is(err, errPayoutList) {
		t.Fatalf("the name at 0 admitted below the fold: %v", err)
	}
	if err := e.verifyPayoutList(bound(13), list); err != nil {
		t.Fatalf("the fold's list refused at its height: %v", err)
	}
	wrong := []*types.Withdrawal{{Index: 0, Address: testHolder, Amount: 0}, {Index: 1, Address: testOther, Amount: 3}, {Index: 2, Address: testFund, Amount: 1}}
	wroot := types.DeriveSha(types.Withdrawals(wrong), trie.NewStackTrie(nil))
	wh := keyedHeader(13, &testKey, testHolder)
	wh.WithdrawalsHash, wh.Coinbase = &wroot, PayoutCoinbase(wroot)
	if err := e.verifyPayoutList(wh, wrong); !errors.Is(err, errPayoutList) {
		t.Fatalf("a zero weight elsewhere admitted under the fold: %v", err)
	}
	// The importer's split and the sealer's agree to the wei: 1,000,001
	// over 1 and 3, the remainder to the last weighted entry, nothing to
	// the name.
	st := registryStateV2(t, testCap)
	amount := big.NewInt(1_000_001)
	payProRata(st, amount, list)
	a := new(big.Int).Sub(st.GetBalance(testHolder).ToBig(), big.NewInt(10_000_000))
	b := new(big.Int).Sub(st.GetBalance(testOther).ToBig(), big.NewInt(10_000_000))
	if a.Cmp(big.NewInt(250_000)) != 0 || b.Cmp(big.NewInt(750_001)) != 0 || st.GetBalance(testFund).Sign() != 0 {
		t.Fatalf("the importer's split: %v %v, the name %v", a, b, st.GetBalance(testFund))
	}
	paid, dust := shares.Pay(amount, shares.FromWithdrawals(list))
	if len(dust) != 0 || len(paid) != 2 || paid[0].Amount.Cmp(a) != 0 || paid[1].Amount.Cmp(b) != 0 {
		t.Fatalf("the sealer's split differs: %+v dust %d", paid, len(dust))
	}
}
