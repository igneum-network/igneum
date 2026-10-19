package recoverycheck

import (
	"math/big"
	"testing"
)

func bi(s string) *big.Int { v, _ := new(big.Int).SetString(s, 10); return v }

func TestRule(t *testing.T) {
	// no parents: reference 0, D_REF 0, every block full-credit
	b := Evaluate(nil, bi("1048576"), bi("5960000000000000000"))
	if b.D.String() != "1048576" || b.DRef.Sign() != 0 || b.Num.Int64() != 1 || b.Unpaid.Sign() != 0 {
		t.Fatalf("%+v", b)
	}
	// the window: an old high difficulty beyond 240 parents is forgotten
	parents := []*big.Int{bi("1099511627776")}
	for i := 0; i < 240; i++ {
		parents = append(parents, bi("1000000"))
	}
	if ReferenceDifficulty(parents).String() != "1000000" {
		t.Fatal("window")
	}
	if ReferenceDifficulty(parents[:241]).String() != "1000000" || ReferenceDifficulty(parents[:240]).String() != "1099511627776" {
		t.Fatal("window edge")
	}
	// the band: 7/8 of the maximum's work; a block at the band's edge is full-credit, one below is priced
	ref := bi("1006000")
	dref := DRef(ref)
	if dref.String() != "880249" {
		t.Fatal(dref)
	}
	b = Evaluate([]*big.Int{ref}, bi("880250"), bi("1000"))
	if b.Num.Int64() != 1 || b.Paid.Int64() != 1000 {
		t.Fatalf("edge: %+v", b)
	}
	b = Evaluate([]*big.Int{ref}, bi("503000"), bi("5960000000000000000"))
	if b.Num.String() != "502999" || b.Den.String() != "880249" || b.Paid.String() != "3405711383937953919" || b.Unpaid.String() != "2554288616062046081" {
		t.Fatalf("halving: %+v", b)
	}
	// paid + unpaid = subsidy always; the fraction never exceeds one
	if new(big.Int).Add(b.Paid, b.Unpaid).String() != "5960000000000000000" {
		t.Fatal("conservation")
	}
}
