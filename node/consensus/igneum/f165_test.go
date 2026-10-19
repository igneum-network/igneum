package igneum

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/vector"
	"github.com/ethereum/go-ethereum/consensus/igneum/shares"
)

// TestSealerFold (F-165, the review's form of 19:3x UK, 15 October 2026):
// under the fold the sealer's quarter goes into the members' pot, every
// key is paid through the pot by its balanced weight (the sealer through
// its own key's entry, or not at all), the identity to the wei holds, and
// the form without the fold is unchanged; the fold is inert where the
// minimum rule is not in force; the admission sets it once above the head.
func TestSealerFold(t *testing.T) {
	amount := big.NewInt(1_000_000)
	sealer := shareAddr(9000)
	// Three members at weights 10, 30 and the sealer's own key at 20 (its
	// balanced standing), then the sealer's last entry (the quarter's
	// term) at any weight; the quorum 60 so W = Q.
	list := []shares.Entry{{Address: shareAddr(1), Weight: 10}, {Address: shareAddr(2), Weight: 30}, {Address: sealer, Weight: 20}, {Address: sealer, Weight: 7}}
	plain := payV2Amounts(amount, list, 60, false)
	if plain.Quarter.Cmp(big.NewInt(250_000)) != 0 || plain.Pot.Cmp(big.NewInt(750_000)) != 0 || plain.Fold || plain.Folded.Sign() != 0 {
		t.Fatalf("the form without the fold moved: quarter %v pot %v", plain.Quarter, plain.Pot)
	}
	fold := payV2Amounts(amount, list, 60, true)
	if !fold.Fold || fold.Quarter.Sign() != 0 || fold.Folded.Cmp(big.NewInt(250_000)) != 0 || fold.Pot.Cmp(amount) != 0 {
		t.Fatalf("the fold: quarter %v folded %v pot %v", fold.Quarter, fold.Folded, fold.Pot)
	}
	paid := map[string]*big.Int{}
	for _, p := range fold.Paid {
		paid[p.Address.Hex()] = p.Amount
	}
	// By weight over the whole amount: 10/60, 30/60, 20/60 (the
	// remainder, if any, to the last paid entry by shares.Pay).
	want := map[string]int64{shareAddr(1).Hex(): 166_666, shareAddr(2).Hex(): 500_000, sealer.Hex(): 333_333}
	for a, w := range want {
		got := paid[a]
		if got == nil || new(big.Int).Sub(got, big.NewInt(w)).CmpAbs(big.NewInt(1)) > 0 {
			t.Fatalf("%s paid %v, want about %d", a, got, w)
		}
	}
	if sum := new(big.Int).Add(fold.Credited, fold.Unissued); sum.Cmp(amount) != 0 {
		t.Fatalf("the identity: credited %v + unissued %v != %v", fold.Credited, fold.Unissued, amount)
	}
	// A sealer with no standing (no key entry): paid nothing under the
	// fold, its quarter in the members' pot; the quarter whole without it.
	lone := []shares.Entry{{Address: shareAddr(1), Weight: 10}, {Address: shareAddr(2), Weight: 30}, {Address: sealer, Weight: 7}}
	lf := payV2Amounts(amount, lone, 40, true)
	for _, p := range lf.Paid {
		if p.Address == sealer {
			t.Fatalf("a lone sealer paid %v under the fold", p.Amount)
		}
	}
	if lf.Pot.Cmp(amount) != 0 || lf.Quarter.Sign() != 0 {
		t.Fatalf("the lone sealer's block: pot %v quarter %v", lf.Pot, lf.Quarter)
	}
	if lp := payV2Amounts(amount, lone, 40, false); lp.Quarter.Cmp(big.NewInt(250_000)) != 0 {
		t.Fatalf("without the fold the lone sealer's quarter %v", lp.Quarter)
	}
	// A list lighter than the quorum scales the whole amount by W / Q.
	if light := payV2Amounts(amount, lone, 80, true); light.Pot.Cmp(big.NewInt(500_000)) != 0 {
		t.Fatalf("the light list under the fold: pot %v, want 500000", light.Pot)
	}
	// The registration: inert without the minimum rule; from the height
	// with it; set once above the head.
	v := vectorConfig()
	v.SealerFoldFrom = 100
	m, err := vector.New(v, pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	if m.SealerFoldAt(100) {
		t.Fatal("the fold in force without the minimum rule")
	}
	v.BalancedMinFrom = 50
	if err := m.Replace(v); err != nil {
		t.Fatal(err)
	}
	if m.SealerFoldAt(99) || !m.SealerFoldAt(100) || !m.SealerFoldAt(101) {
		t.Fatalf("the fold's height: %v %v %v", m.SealerFoldAt(99), m.SealerFoldAt(100), m.SealerFoldAt(101))
	}
	stored := vectorConfig()
	given := *stored
	given.SealerFoldFrom = 151
	if _, err := Reregistration(stored, &given, 150); err != nil {
		t.Fatalf("sealerFoldFrom above the head refused: %v", err)
	}
	if _, err := Reregistration(stored, &given, 151); err == nil {
		t.Fatal("sealerFoldFrom at the head admitted")
	}
	stored.SealerFoldFrom = 100
	given.SealerFoldFrom = 200
	if _, err := Reregistration(stored, &given, 150); err == nil {
		t.Fatal("a changed sealerFoldFrom admitted")
	}
}
