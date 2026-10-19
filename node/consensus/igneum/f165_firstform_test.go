package igneum

import (
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/vector"
	"github.com/ethereum/go-ethereum/consensus/igneum/shares"
	"github.com/ethereum/go-ethereum/core/types"
)

// TestSealerFoldFirstForm (F-180: F-165 under the first form, the coordinate
// rule's pro rata over the list; the research chain's first fold block 6550
// read the sealer's term paid as before): from sealerFoldListFrom (the first
// form's own field, so the heights sealed under sealerFoldFrom before the
// first form carried the fold verify as sealed on every node) the derived
// list's last entry (the sealer's) carries weight 0, a name alone, so the
// amount is paid over the members by weight, the sealer through its own
// key's entry or not at all; the verifier derives the same list; Pay pays a
// zero-weight entry nothing and never carries it as dust.
func TestSealerFoldFirstForm(t *testing.T) {
	e := shareEngine(t)
	defer e.Close()
	v := vectorConfig()
	v.BalancedMinFrom = 1
	v.SealerFoldListFrom = 10
	m, err := vector.New(v, pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	e.algo = m
	st := nativeState(t, big.NewInt(1_000_000))
	sealer := shareAddr(9000)
	accepted := []shares.Entry{{Address: shareAddr(1), Weight: 10}, {Address: shareAddr(2), Weight: 30}, {Address: sealer, Weight: 20}}
	// Below the height: the sealer's term at (W + 2) / 3 = 20.
	list, _, err := e.ShareList(st, 9, accepted, sealer)
	if err != nil || list[len(list)-1] != (shares.Entry{Address: sealer, Weight: 20}) {
		t.Fatalf("below the fold: %+v %v", list[len(list)-1], err)
	}
	// From the height: the sealer's entry a name at weight 0, the
	// verifier agreeing on the derived list.
	list, _, err = e.ShareList(st, 10, accepted, sealer)
	if err != nil || list[len(list)-1] != (shares.Entry{Address: sealer, Weight: 0}) || len(list) != 4 {
		t.Fatalf("under the fold: %+v (%d entries) %v", list[len(list)-1], len(list), err)
	}
	h := &types.Header{Number: big.NewInt(10), Difficulty: big.NewInt(1)}
	if err := e.VerifyShareList(st, h, accepted, shares.Withdrawals(list)); err != nil {
		t.Fatalf("the folded list refused by the verifier: %v", err)
	}
	old := shares.Withdrawals(list)
	old[3] = &types.Withdrawal{Index: 3, Address: sealer, Amount: 20}
	if err := e.VerifyShareList(st, h, accepted, old); !errors.Is(err, errShareList) {
		t.Fatalf("the old weight admitted under the fold: %v", err)
	}
	// The pay: 1,000,000 over 10 + 30 + 20 by weight, the sealer's key
	// paid as a member (333,333 with the remainder), the name entry
	// nothing and not in the dust.
	paid, dust := shares.Pay(big.NewInt(1_000_000), list)
	if len(dust) != 0 || len(paid) != 3 {
		t.Fatalf("paid %d dust %d", len(paid), len(dust))
	}
	sum := new(big.Int)
	for _, p := range paid {
		sum.Add(sum, p.Amount)
		if p.Address == sealer && new(big.Int).Sub(p.Amount, big.NewInt(333_333)).CmpAbs(big.NewInt(2)) > 0 {
			t.Fatalf("the sealer's key as a member: %v", p.Amount)
		}
	}
	if sum.Cmp(big.NewInt(1_000_000)) != 0 {
		t.Fatalf("the amount paid whole: %v", sum)
	}
	// A lone sealer (no key entry among the members): nothing, the whole
	// amount to the members; nothing carried for it.
	lone := []shares.Entry{{Address: shareAddr(1), Weight: 10}, {Address: sealer, Weight: 0}}
	paid, dust = shares.Pay(big.NewInt(7), lone)
	if len(paid) != 1 || paid[0].Address != shareAddr(1) || paid[0].Amount.Int64() != 7 || len(dust) != 0 {
		t.Fatalf("the lone sealer: %+v dust %+v", paid, dust)
	}
	// Nothing to pay: the weighted entries carried, the name not.
	if _, dust = shares.Pay(big.NewInt(0), lone); len(dust) != 1 || dust[0].Address != shareAddr(1) {
		t.Fatalf("the carry without an amount: %+v", dust)
	}
	// sealerFoldFrom alone (the second form's field, the research chain's
	// 6550) moves nothing on the first form: the term stands.
	v2 := vectorConfig()
	v2.BalancedMinFrom = 1
	v2.SealerFoldFrom = 10
	m2, err := vector.New(v2, pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	e.algo = m2
	list, _, err = e.ShareList(st, 10, accepted, sealer)
	if err != nil || list[len(list)-1] != (shares.Entry{Address: sealer, Weight: 20}) {
		t.Fatalf("sealerFoldFrom alone folded the first form: %+v %v", list[len(list)-1], err)
	}
	// The admission: set once, above the head.
	stored := vectorConfig()
	given := *stored
	given.SealerFoldListFrom = 151
	if _, err := Reregistration(stored, &given, 150); err != nil {
		t.Fatalf("sealerFoldListFrom above the head refused: %v", err)
	}
	if _, err := Reregistration(stored, &given, 151); err == nil {
		t.Fatal("sealerFoldListFrom at the head admitted")
	}
	stored.SealerFoldListFrom = 100
	given.SealerFoldListFrom = 200
	if _, err := Reregistration(stored, &given, 150); err == nil {
		t.Fatal("a changed sealerFoldListFrom admitted")
	}
}
