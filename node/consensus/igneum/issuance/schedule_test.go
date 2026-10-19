package issuance

import (
	"encoding/json"
	"math/big"
	"os"
	"testing"
)

type vectors struct {
	Branch string `json:"branch"`
	Params struct {
		Unit          string `json:"unit"`
		Epoch         uint64 `json:"epoch"`
		M             string `json:"m"`
		K             uint   `json:"k"`
		Tail          string `json:"tail"`
		FundBps       uint64 `json:"fund_bps"`
		BlocksPerYear uint64 `json:"blocks_per_year"`
	} `json:"params"`
	Transition *struct {
		Epoch           uint64 `json:"epoch"`
		FirstTailHeight uint64 `json:"first_tail_height"`
		IssuedAtStart   string `json:"issued_at_start"`
	} `json:"transition"`
	Epochs []struct {
		Epoch         uint64 `json:"epoch"`
		IssuedAtStart string `json:"issued_at_start"`
		Subsidy       string `json:"subsidy"`
	} `json:"epochs"`
	Heights []struct {
		Height  uint64 `json:"height"`
		Epoch   uint64 `json:"epoch"`
		Subsidy string `json:"subsidy"`
		Miner   string `json:"miner"`
		Fund    string `json:"fund"`
		Issued  string `json:"issued"`
	} `json:"heights"`
	Years []struct {
		Year    uint64 `json:"year"`
		Height  uint64 `json:"height"`
		Subsidy string `json:"subsidy"`
		Issued  string `json:"issued"`
		FundCum string `json:"fund_cum"`
	} `json:"years"`
}

func mustBig(t *testing.T, s string) *big.Int {
	t.Helper()
	b, ok := new(big.Int).SetString(s, 10)
	if !ok {
		t.Fatalf("bad integer %q", s)
	}
	return b
}

func load(t *testing.T, name string) (vectors, Params) {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("mandatory fixture missing: %v", err) // never skip (the plan, page 24)
	}
	var v vectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	p := Params{Epoch: v.Params.Epoch, M: mustBig(t, v.Params.M), K: v.Params.K, Tail: mustBig(t, v.Params.Tail), FundBps: v.Params.FundBps}
	return v, p
}

func eq(t *testing.T, what string, got *big.Int, want string) {
	t.Helper()
	if got.Cmp(mustBig(t, want)) != 0 {
		t.Errorf("%s: got %s want %s", what, got, want)
	}
}

func replayFile(t *testing.T, name string) {
	v, p := load(t, name)
	s := New(p)
	if len(v.Epochs) == 0 || len(v.Heights) == 0 || len(v.Years) == 0 {
		t.Fatal("empty fixture")
	}
	for _, c := range v.Epochs {
		start, r := s.EpochState(c.Epoch)
		eq(t, "epoch start", start, c.IssuedAtStart)
		eq(t, "epoch subsidy", r, c.Subsidy)
	}
	for _, c := range v.Heights {
		if c.Height > 0 && s.p.EpochOf(c.Height) != c.Epoch {
			t.Errorf("height %d: epoch %d want %d", c.Height, s.p.EpochOf(c.Height), c.Epoch)
		}
		eq(t, "subsidy", s.Subsidy(c.Height), c.Subsidy)
		m, f := s.Reward(c.Height)
		eq(t, "miner", m, c.Miner)
		eq(t, "fund", f, c.Fund)
		eq(t, "issued", s.Issued(c.Height), c.Issued)
		if new(big.Int).Add(m, f).Cmp(s.Subsidy(c.Height)) != 0 {
			t.Errorf("height %d: miner + fund != subsidy", c.Height)
		}
	}
	for _, c := range v.Years {
		eq(t, "year subsidy", s.Subsidy(c.Height+1), c.Subsidy)
		eq(t, "year issued", s.Issued(c.Height), c.Issued)
		eq(t, "year fund", s.FundIssued(c.Height), c.FundCum)
	}
	e, h, ok := s.Transition()
	if v.Transition == nil {
		if ok {
			t.Errorf("hard cap must have no transition")
		}
	} else {
		if !ok || e != v.Transition.Epoch || h != v.Transition.FirstTailHeight {
			t.Errorf("transition: got (%d,%d,%v) want (%d,%d)", e, h, ok, v.Transition.Epoch, v.Transition.FirstTailHeight)
		}
		start, _ := s.EpochState(e)
		eq(t, "transition start", start, v.Transition.IssuedAtStart)
	}
}

func TestReplayTail(t *testing.T) { replayFile(t, "vectors-tail.json") }
func TestReplayCap(t *testing.T)  { replayFile(t, "vectors-cap.json") }

// The candidate constants in code are the ones the fixture was made with.
func TestCandidateMatchesFixture(t *testing.T) {
	v, p := load(t, "vectors-tail.json")
	if p.Epoch != Candidate.Epoch || p.K != Candidate.K || p.FundBps != Candidate.FundBps ||
		p.M.Cmp(Candidate.M) != 0 || p.Tail.Cmp(Candidate.Tail) != 0 || v.Branch != "tail" {
		t.Fatal("Candidate differs from the fixture's parameters")
	}
	v, p = load(t, "vectors-cap.json")
	if p.Epoch != HardCap.Epoch || p.K != HardCap.K || p.M.Cmp(HardCap.M) != 0 || p.Tail.Sign() != 0 || v.Branch != "cap" {
		t.Fatal("HardCap differs from the fixture's parameters")
	}
}

// Block-by-block sum over the first three epochs equals Issued.
func TestIssuedIsTheSumOfSubsidies(t *testing.T) {
	s := New(Candidate)
	sum := new(big.Int)
	for h := uint64(1); h <= 3*Candidate.Epoch+5; h++ {
		m, f := s.Reward(h)
		sum.Add(sum, m)
		sum.Add(sum, f)
		if h%1000 == 0 || h%Candidate.Epoch < 2 {
			if sum.Cmp(s.Issued(h)) != 0 {
				t.Fatalf("height %d: sum %s != Issued %s", h, sum, s.Issued(h))
			}
		}
	}
}

func TestMonotoneAndTail(t *testing.T) {
	s := New(Candidate)
	e, h, ok := s.Transition()
	if !ok {
		t.Fatal("tail branch must transition")
	}
	prev := new(big.Int).Set(Candidate.M)
	for i := uint64(0); i <= e+10; i++ {
		_, r := s.EpochState(i)
		if r.Cmp(prev) > 0 {
			t.Fatalf("epoch %d: subsidy rose", i)
		}
		if i >= e && r.Cmp(Candidate.Tail) != 0 {
			t.Fatalf("epoch %d: not at the tail", i)
		}
		if i < e && r.Cmp(Candidate.Tail) <= 0 {
			t.Fatalf("epoch %d: at the tail before the transition", i)
		}
		prev = r
	}
	if s.Subsidy(h-1).Cmp(Candidate.Tail) <= 0 || s.Subsidy(h).Cmp(Candidate.Tail) != 0 {
		t.Fatal("first tail height wrong")
	}
	// the tail's annualised rate at the transition is about 0.5 percent of the gross issued supply
	blocksPerYear := uint64(365 * 86400 / 15)
	annual := new(big.Int).Mul(Candidate.Tail, new(big.Int).SetUint64(blocksPerYear))
	start, _ := s.EpochState(e)
	rate := new(big.Float).Quo(new(big.Float).SetInt(annual), new(big.Float).SetInt(start))
	f, _ := rate.Float64()
	if f < 0.0045 || f > 0.0065 {
		t.Fatalf("tail rate at transition %.4f outside the modelled band", f)
	}
}

func TestHardCapNeverExceedsM(t *testing.T) {
	s := New(HardCap)
	blocksPerYear := uint64(365 * 86400 / 15)
	for _, y := range []uint64{1, 10, 25, 50, 100, 200, 400} {
		if s.Issued(y*blocksPerYear).Cmp(HardCap.M) >= 0 {
			t.Fatalf("year %d: issued reached M", y)
		}
	}
	if s.Subsidy(400*blocksPerYear).Sign() != 0 {
		t.Fatalf("hard cap: subsidy not zero at year 400")
	}
	if _, _, ok := s.Transition(); ok {
		t.Fatal("hard cap has no tail")
	}
}

func TestSplitRounding(t *testing.T) {
	m, f := Candidate.Split(big.NewInt(199))
	if f.Int64() != 1 || m.Int64() != 198 {
		t.Fatalf("split 199: miner %s fund %s", m, f)
	}
	m, f = Candidate.Split(big.NewInt(99))
	if f.Int64() != 0 || m.Int64() != 99 {
		t.Fatalf("split 99: miner %s fund %s", m, f)
	}
}

func TestGenesisIssuesNothing(t *testing.T) {
	s := New(Candidate)
	if s.Subsidy(0).Sign() != 0 || s.Issued(0).Sign() != 0 {
		t.Fatal("genesis must issue nothing")
	}
}
