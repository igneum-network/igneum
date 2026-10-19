package difficulty

import (
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"testing"
)

func bi(x int64) *big.Int { return big.NewInt(x) }

func TestReferenceDifficulty(t *testing.T) {
	var parents []*big.Int
	for i := int64(1); i <= 300; i++ {
		parents = append(parents, bi(i))
	}
	if ReferenceDifficulty(parents).Cmp(bi(300)) != 0 {
		t.Fatal("the maximum over the window")
	}
	// A high value older than the window is forgotten.
	parents = append([]*big.Int{bi(1 << 40)}, parents...)
	if ReferenceDifficulty(parents).Cmp(bi(300)) != 0 {
		t.Fatal("a value outside the window counted")
	}
	if ReferenceDifficulty(nil).Sign() != 0 {
		t.Fatal("no parents")
	}
}

func TestRecoveryFraction(t *testing.T) {
	ref := PricedReference(bi(1000000)) // 7/8 of work(T(10^6)) = 7/8 x 999999 = 874999
	if ref.Cmp(bi(874999)) != 0 {
		t.Fatalf("priced reference %s", ref)
	}
	cases := []struct {
		d        int64
		num, den int64
	}{{1000000, 1, 1}, {875000, 1, 1}, {874999, 874998, 874999}, {500000, 499999, 874999}, {250000, 249999, 874999}, {2000000, 1, 1}}
	for _, c := range cases {
		n, d := RecoveryFraction(Work(bi(c.d)), ref)
		if n.Cmp(bi(c.num)) != 0 || d.Cmp(bi(c.den)) != 0 {
			t.Fatalf("difficulty %d: %s/%s, want %d/%d", c.d, n, d, c.num, c.den)
		}
	}
	n, d := RecoveryFraction(bi(5), bi(0))
	if n.Cmp(bi(1)) != 0 || d.Cmp(bi(1)) != 0 {
		t.Fatal("a zero reference must not price")
	}
	if Work(bi(1024)).Cmp(bi(1024)) != 0 || Work(bi(1000)).Cmp(bi(999)) != 0 {
		t.Fatal("work from difficulty")
	}
	sub := new(big.Int).Mul(bi(596), new(big.Int).Exp(bi(10), bi(16), nil)) // 5.96 IGN
	if PricedSubsidy(sub, bi(1), bi(2)).Cmp(new(big.Int).Div(sub, bi(2))) != 0 {
		t.Fatal("half")
	}
}

type recoveryVector struct {
	Name       string   `json:"name"`
	Parents    []string `json:"parents"`
	Difficulty string   `json:"difficulty"`
	Work       string   `json:"work_D"`
	Reference  string   `json:"reference_difficulty"`
	PricedRef  string   `json:"reference_work_D_REF"`
	Num        string   `json:"fraction_num"`
	Den        string   `json:"fraction_den"`
	Subsidy    string   `json:"subsidy"`
	Paid       string   `json:"paid"`
	Unpaid     string   `json:"unpaid"`
}

// TestRecoveryVectors checks the published vectors for protocol B's second
// implementation; WRITE_TEST_FILES=1 regenerates them.
func TestRecoveryVectors(t *testing.T) {
	path := filepath.Clean("../../../../pow/vectors/recovery/vectors.json")
	sub := new(big.Int).Mul(bi(596), new(big.Int).Exp(bi(10), bi(16), nil))
	if os.Getenv("WRITE_TEST_FILES") == "1" {
		var vs []recoveryVector
		steady := make([]*big.Int, 300)
		for i := range steady {
			steady[i] = bi(1000000 + int64(i%7)*1000) // the rule's noise, within the band
		}
		eased := append(append([]*big.Int{}, steady...), bi(500000), bi(250000))
		cases := []struct {
			name    string
			parents []*big.Int
			d       *big.Int
		}{
			{"genesis, no parents", nil, bi(1 << 20)},
			{"steady chain, within the band", steady, bi(990000)},
			{"steady chain, at the band's edge", steady, bi(880250)},
			{"one halving after a stall", steady, bi(503000)},
			{"two halvings, the second block", eased, bi(250000)},
			{"the return: still below the band", eased, bi(800000)},
			{"the return: back inside the band", eased, bi(900000)},
			{"an old high difficulty outside the window", append([]*big.Int{bi(1 << 40)}, steady...), bi(990000)},
		}
		for _, c := range cases {
			ref := ReferenceDifficulty(c.parents)
			dref := PricedReference(ref)
			w := Work(c.d)
			n, d := RecoveryFraction(w, dref)
			paid := PricedSubsidy(sub, n, d)
			var ps []string
			for _, p := range c.parents {
				ps = append(ps, p.String())
			}
			vs = append(vs, recoveryVector{c.name, ps, c.d.String(), w.String(), ref.String(), dref.String(), n.String(), d.String(), sub.String(), paid.String(), new(big.Int).Sub(sub, paid).String()})
		}
		b, _ := json.MarshalIndent(map[string]any{"rule": "work-priced recovery, single lane (docs/protocol-a/RECOVERY-PRICING.md): reference difficulty = max over the last 240 parents; D = work(T(difficulty)); D_REF = floor(work(T(reference)) x 7 / 8); fraction = 1 if D >= D_REF else D / D_REF; paid = floor(subsidy x fraction); unpaid = subsidy - paid", "window": ReferenceWindow, "band": "7/8", "vectors": vs}, "", "  ")
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Vectors []recoveryVector `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	for _, v := range f.Vectors {
		var parents []*big.Int
		for _, p := range v.Parents {
			x, _ := new(big.Int).SetString(p, 10)
			parents = append(parents, x)
		}
		d, _ := new(big.Int).SetString(v.Difficulty, 10)
		ref := ReferenceDifficulty(parents)
		dref := PricedReference(ref)
		w := Work(d)
		n, den := RecoveryFraction(w, dref)
		paid := PricedSubsidy(sub, n, den)
		if ref.String() != v.Reference || dref.String() != v.PricedRef || w.String() != v.Work || n.String() != v.Num || den.String() != v.Den || paid.String() != v.Paid || new(big.Int).Sub(sub, paid).String() != v.Unpaid {
			t.Errorf("%s differs", v.Name)
		}
	}
}
