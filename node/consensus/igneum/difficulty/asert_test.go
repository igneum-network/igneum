package difficulty

import (
	"encoding/json"
	"math/big"
	"os"
	"testing"
)

type vectors struct {
	Params struct {
		Target   uint64 `json:"target"`
		HalfLife uint64 `json:"halflife"`
		DtMax    uint64 `json:"dt_max"`
		DMin     int64  `json:"d_min"`
	} `json:"params"`
	Absolute []struct {
		DGenesis string `json:"d_genesis"`
		TGenesis uint64 `json:"t_genesis"`
		TParent  uint64 `json:"t_parent"`
		HParent  uint64 `json:"h_parent"`
		Expect   string `json:"expect"`
	} `json:"absolute"`
	Relative []struct {
		DParent string `json:"d_parent"`
		Dt      uint64 `json:"dt"`
		Expect  string `json:"expect"`
	} `json:"relative"`
	Tapes []struct {
		Variant      string   `json:"variant"`
		Timestamps   []uint64 `json:"timestamps"`
		Difficulties []string `json:"difficulties"`
	} `json:"tapes"`
}

func loadVectors(t *testing.T) (vectors, Params) {
	t.Helper()
	raw, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatalf("mandatory fixture missing: %v", err) // never skip (the plan, page 24)
	}
	var v vectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	p := Params{Target: v.Params.Target, HalfLife: v.Params.HalfLife, DtMax: v.Params.DtMax, Min: big.NewInt(v.Params.DMin)}
	return v, p
}

func mustBig(t *testing.T, s string) *big.Int {
	t.Helper()
	b, ok := new(big.Int).SetString(s, 10)
	if !ok {
		t.Fatalf("bad integer %q", s)
	}
	return b
}

// The simulator and the node must agree bit for bit on both rules.
func TestVectorsAbsolute(t *testing.T) {
	v, p := loadVectors(t)
	if len(v.Absolute) == 0 {
		t.Fatal("no absolute vectors")
	}
	for i, c := range v.Absolute {
		got := p.NextAbsolute(mustBig(t, c.DGenesis), c.TGenesis, c.TParent, c.HParent)
		if got.Cmp(mustBig(t, c.Expect)) != 0 {
			t.Errorf("absolute[%d]: got %s want %s", i, got, c.Expect)
		}
	}
}

func TestVectorsRelative(t *testing.T) {
	v, p := loadVectors(t)
	if len(v.Relative) == 0 {
		t.Fatal("no relative vectors")
	}
	for i, c := range v.Relative {
		got := p.NextRelative(mustBig(t, c.DParent), c.Dt)
		if got.Cmp(mustBig(t, c.Expect)) != 0 {
			t.Errorf("relative[%d]: got %s want %s", i, got, c.Expect)
		}
	}
}

// Replay the simulator's timestamp tapes block by block through the engine
// call shape and compare every difficulty: the consensus differential test
// for this package.
func TestTapes(t *testing.T) {
	v, p := loadVectors(t)
	if len(v.Tapes) == 0 {
		t.Fatal("no tapes")
	}
	for _, tape := range v.Tapes {
		ts, ds := tape.Timestamps, tape.Difficulties
		if len(ts) != len(ds) || len(ts) < 3 {
			t.Fatalf("tape %s malformed", tape.Variant)
		}
		genesisD := mustBig(t, ds[0])
		for i := 1; i < len(ts); i++ {
			var got *big.Int
			parentNumber := uint64(i - 1)
			switch tape.Variant {
			case "A":
				got = p.NextAbsolute(genesisD, ts[0], ts[i-1], parentNumber)
			case "R":
				var gp uint64
				if i >= 2 {
					gp = ts[i-2]
				}
				got = p.Next(parentNumber, ts[i-1], gp, mustBig(t, ds[i-1]))
			default:
				t.Fatalf("unknown variant %q", tape.Variant)
			}
			if got.Cmp(mustBig(t, ds[i])) != 0 {
				t.Fatalf("tape %s block %d: got %s want %s", tape.Variant, i, got, ds[i])
			}
		}
	}
}

func TestSteadyStateIsFixedPoint(t *testing.T) {
	p := Candidate
	d := big.NewInt(1 << 40)
	for i := 0; i < 1000; i++ {
		d = p.NextRelative(d, p.Target)
	}
	if d.Cmp(big.NewInt(1<<40)) != 0 {
		t.Fatalf("an on-target interval must leave the difficulty unchanged, got %s", d)
	}
}

func TestHalvingAndDoubling(t *testing.T) {
	p := Candidate
	p.DtMax = 1 << 30 // no clamp for the halving check
	d0 := big.NewInt(1 << 40)
	// one block counted at Target + HalfLife halves the difficulty
	d := p.NextRelative(d0, p.Target+p.HalfLife)
	want := new(big.Int).Rsh(d0, 1)
	if d.Cmp(want) != 0 {
		t.Fatalf("halving: got %s want %s", d, want)
	}
	// the clamp: a gap of a year counts as DtMax
	p = Candidate
	d = p.NextRelative(d0, 365*86400)
	if d.Cmp(p.NextRelative(d0, p.DtMax)) != 0 {
		t.Fatalf("clamp not applied")
	}
	if d.Cmp(new(big.Int).Rsh(d0, 1)) <= 0 || d.Cmp(d0) >= 0 {
		t.Fatalf("a clamped gap must lower by less than one halving: %s", d)
	}
	// an interval of 1 s raises the difficulty by about 2^((T-1)/HL)
	d = p.NextRelative(d0, 1)
	if d.Cmp(d0) <= 0 {
		t.Fatalf("fast block must raise the difficulty")
	}
	ratio := new(big.Float).Quo(new(big.Float).SetInt(d), new(big.Float).SetInt(d0))
	r, _ := ratio.Float64()
	// 2^(14/3600) = 1.002700...
	if r < 1.0026 || r > 1.0028 {
		t.Fatalf("fast-block factor %f out of range", r)
	}
}

func TestFloor(t *testing.T) {
	p := Params{Target: 15, HalfLife: 60, DtMax: 60, Min: big.NewInt(1000)}
	d := big.NewInt(1001)
	for i := 0; i < 50; i++ {
		d = p.NextRelative(d, 60)
	}
	if d.Cmp(p.Min) != 0 {
		t.Fatalf("floor: got %s want %s", d, p.Min)
	}
	if got := p.NextAbsolute(big.NewInt(1001), 0, 1_000_000, 1); got.Cmp(p.Min) != 0 {
		t.Fatalf("absolute floor: got %s", got)
	}
}

func TestFloorDiv(t *testing.T) {
	cases := [][3]int64{{7, 2, 3}, {-7, 2, -4}, {6, 3, 2}, {-6, 3, -2}, {0, 5, 0}, {-1, 65536, -1}}
	for _, c := range cases {
		if got := floorDiv(c[0], c[1]); got != c[2] {
			t.Errorf("floorDiv(%d,%d)=%d want %d", c[0], c[1], got, c[2])
		}
	}
}

func TestPow2FixedEndpoints(t *testing.T) {
	// exact at 0 and at a whole step
	f, s := pow2Fixed(0)
	if f.Int64() != radix || s != 0 {
		t.Fatalf("2^0: factor %s shifts %d", f, s)
	}
	f, s = pow2Fixed(radix)
	if f.Int64() != radix || s != 1 {
		t.Fatalf("2^1: factor %s shifts %d", f, s)
	}
	f, s = pow2Fixed(-radix)
	if f.Int64() != radix || s != -1 {
		t.Fatalf("2^-1: factor %s shifts %d", f, s)
	}
	// half step: 2^0.5 = 1.41421 -> 92682 at radix 65536 (within 0.013 percent)
	f, s = pow2Fixed(radix / 2)
	if s != 0 || f.Int64() < 92670 || f.Int64() > 92694 {
		t.Fatalf("2^0.5: factor %s shifts %d", f, s)
	}
	// the Igneum step: exact at 0, 1 and -1; 2^0.5 within 0.07 percent; the
	// slope at 0 within 1e-9 (a step of 2^-20 of a doubling)
	f, s = pow2Igneum(0)
	if f.Int64() != iradix || s != 0 {
		t.Fatalf("igneum 2^0: factor %s shifts %d", f, s)
	}
	f, s = pow2Igneum(iradix)
	if f.Int64() != iradix || s != 1 {
		t.Fatalf("igneum 2^1: factor %s shifts %d", f, s)
	}
	f, s = pow2Igneum(-iradix)
	if f.Int64() != iradix || s != -1 {
		t.Fatalf("igneum 2^-1: factor %s shifts %d", f, s)
	}
	f, s = pow2Igneum(iradix / 2)
	half := float64(f.Int64()) / iradix
	if s != 0 || half < 1.41421356*(1-0.0007) || half > 1.41421356*(1+0.0007) {
		t.Fatalf("igneum 2^0.5: %f", half)
	}
	f, _ = pow2Igneum(1 << 12) // x = 2^-20
	want := 1.0 + 0.6931471806*(1.0/(1<<20))
	got := float64(f.Int64()) / iradix
	if got < want*(1-1e-9) || got > want*(1+1e-9) {
		t.Fatalf("igneum slope at 0: got %.12f want %.12f", got, want)
	}
}
