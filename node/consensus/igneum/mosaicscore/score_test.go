package mosaicscore

import (
	"math/big"
	"math/rand"
	"testing"
)

func bi(s string) *big.Int { v, _ := new(big.Int).SetString(s, 10); return v }

func TestWorkRangeAndForms(t *testing.T) {
	// M10: T = 0 and T at or above 2^256 are rejected, never computed
	for _, bad := range []*big.Int{big.NewInt(0), big.NewInt(-1), two256, new(big.Int).Add(two256, one)} {
		if _, err := Work(bad); err == nil {
			t.Fatalf("work accepted %s", bad)
		}
		if _, err := Work256(bad); err == nil {
			t.Fatalf("work256 accepted %s", bad)
		}
	}
	if _, err := Target(big.NewInt(0)); err == nil {
		t.Fatal("difficulty 0 accepted")
	}
	// the stated values: 1 at the maximum target, 2^255 at T = 1
	if w, _ := Work(max256); w.Cmp(one) != 0 {
		t.Fatal("work at the maximum target")
	}
	if w, _ := Work(one); w.Cmp(new(big.Int).Lsh(one, 255)) != 0 {
		t.Fatal("work at target 1")
	}
	// work(T(D)) = D - 1 at small D as the text's examples: 999 at 1000, 1999 at 2000 (not at D = 1 or 2, where it is D)
	for _, c := range []struct{ d, w int64 }{{1000, 999}, {2000, 1999}, {2, 2}, {1, 1}} {
		tt, _ := Target(big.NewInt(c.d))
		w, _ := Work(tt)
		if w.Int64() != c.w {
			t.Fatalf("work(T(%d)) = %s, want %d", c.d, w, c.w)
		}
	}
	// the two forms agree on every target class, including the edges and random 256-bit values
	rng := rand.New(rand.NewSource(1))
	cases := []*big.Int{one, big.NewInt(2), big.NewInt(3), new(big.Int).Lsh(one, 64), new(big.Int).Lsh(one, 255), new(big.Int).Sub(new(big.Int).Lsh(one, 255), one), new(big.Int).Sub(max256, one), max256}
	for i := 0; i < 2000; i++ {
		b := make([]byte, 1+rng.Intn(32))
		rng.Read(b)
		v := new(big.Int).SetBytes(b)
		if v.Sign() == 0 {
			v = one
		}
		cases = append(cases, v)
	}
	for _, c := range cases {
		a, err1 := Work(c)
		b, err2 := Work256(c)
		if err1 != nil || err2 != nil || a.Cmp(b) != 0 {
			t.Fatalf("forms differ at %s: %v %v", c, a, b)
		}
		if a.Cmp(two256) >= 0 || a.Sign() <= 0 {
			t.Fatalf("work out of 256 bits at %s", c)
		}
	}
	// finding M-1: the 256-bit form's intermediates fit below 2^256 for every target but the maximum,
	// where b = T + 1 = 2^256 (a 256-bit evaluator wraps b to 0)
	if !Fits256(new(big.Int).Sub(max256, one)) || Fits256(max256) {
		t.Fatal("the 256-bit form's range at the maximum target")
	}
}

func TestNormaliseAndScore(t *testing.T) {
	// one unit at the reference; two at double; floor at a third
	n, _ := Normalise(bi("999999"), bi("999999"))
	if n.Cmp(two64) != 0 {
		t.Fatal("one unit at the reference")
	}
	n, _ = Normalise(bi("2"), bi("1"))
	if n.Cmp(new(big.Int).Lsh(one, 65)) != 0 {
		t.Fatal("two units")
	}
	n, _ = Normalise(bi("1"), bi("3"))
	if n.String() != "6148914691236517205" {
		t.Fatal("a third", n)
	}
	if _, err := Normalise(bi("1"), bi("0")); err == nil {
		t.Fatal("reference 0 accepted")
	}
	// cheap-work overflow (M10): the largest work over the smallest reference stays finite and exact
	n, _ = Normalise(new(big.Int).Lsh(one, 255), one)
	if n.Cmp(new(big.Int).Lsh(one, 319)) != 0 {
		t.Fatal("large normalisation")
	}
	// the score: lemma 1 monotone; a non-minimum lane moves the score by e x; five at one = A + 5e in scale
	u := two64
	five := []*big.Int{u, u, u, u, u}
	s, _ := Score(Research, five)
	// S' = 5 x 50 x u + 1 x 1 x 5u = 255 u
	if s.Cmp(new(big.Int).Mul(u, big.NewInt(255))) != 0 {
		t.Fatal("five at one unit", s)
	}
	more := []*big.Int{u, u, u, u, new(big.Int).Add(u, big.NewInt(100))}
	s2, _ := Score(Research, more)
	if new(big.Int).Sub(s2, s).Int64() != 100 {
		t.Fatal("a non-minimum lane moves the score by e x in scale")
	}
	// the one-lane attacker at 255 units against five honest at one: equal
	att := []*big.Int{new(big.Int).Mul(u, big.NewInt(255)), big.NewInt(0), big.NewInt(0), big.NewInt(0), big.NewInt(0)}
	sa, _ := Score(Research, att)
	if sa.Cmp(s) != 0 {
		t.Fatal("the 255-unit attacker", sa, s)
	}
	// e = 0 scores an empty-lane epoch zero
	s0, _ := Score(Weights{5, 1, 0, 1}, []*big.Int{u, u, big.NewInt(0)})
	if s0.Sign() != 0 {
		t.Fatal("e = 0 with an empty lane")
	}
	if _, err := Score(Research, nil); err == nil {
		t.Fatal("no lanes accepted")
	}
	// epochs
	if Epoch(0, 100) != 0 || Epoch(99, 100) != 0 || Epoch(100, 100) != 1 {
		t.Fatal("epoch of a height")
	}
	// chain score: an ancient one-lane imbalance adds a constant that does not change the present epoch's choice
	anc := [][]*big.Int{{u, big.NewInt(0), big.NewInt(0)}, five, five}
	bal := [][]*big.Int{five, five, five}
	ca, _ := ChainScore(Research, anc)
	cb, _ := ChainScore(Research, bal)
	if ca.Cmp(cb) >= 0 {
		t.Fatal("ancient imbalance")
	}
}

func TestOrderTotal(t *testing.T) {
	// M15: every node picks the same tip; the order is total, antisymmetric and transitive on random tips
	rng := rand.New(rand.NewSource(2))
	tips := make([]Tip, 60)
	for i := range tips {
		var h [32]byte
		rng.Read(h[:])
		tips[i] = Tip{big.NewInt(int64(rng.Intn(3))), uint64(rng.Intn(3)), h}
	}
	for _, a := range tips {
		for _, b := range tips {
			c := Compare(a, b)
			if c != -Compare(b, a) {
				t.Fatal("antisymmetry")
			}
			if c == 0 && (a.Score.Cmp(b.Score) != 0 || a.Height != b.Height || a.Hash != b.Hash) {
				t.Fatal("zero on distinct tips")
			}
			for _, d := range tips {
				if Compare(a, b) > 0 && Compare(b, d) > 0 && Compare(a, d) <= 0 {
					t.Fatal("transitivity")
				}
			}
		}
	}
	// the lower hash wins only at equal score and height
	var lo, hi [32]byte
	hi[31] = 1
	if Compare(Tip{one, 1, lo}, Tip{one, 1, hi}) != 1 || Compare(Tip{one, 1, hi}, Tip{one, 2, lo}) != -1 || Compare(Tip{big.NewInt(2), 1, hi}, Tip{one, 9, lo}) != 1 {
		t.Fatal("order")
	}
}

func TestOwnVectors(t *testing.T) {
	vf := Own()
	divs, err := vf.Check()
	if err != nil || len(divs) != 0 {
		t.Fatal(divs, err)
	}
	bad := *vf
	bad.Work = append([]WorkVector(nil), vf.Work...)
	bad.Work[2].Work = "1000"
	divs, _ = bad.Check()
	if len(divs) != 1 || divs[0].Stage != "work" {
		t.Fatal(divs)
	}
}
