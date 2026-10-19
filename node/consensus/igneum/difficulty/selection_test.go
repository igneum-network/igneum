package difficulty

import (
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"testing"
)

func TestSelectionSignal(t *testing.T) {
	s := Selection{Lookback: 100, Sustain: 20, GrowthNum: 4, GrowthDen: 1, Period: 1000}
	flat := make([]*big.Int, 120)
	for i := range flat {
		flat[i] = bi(1000 + int64(i%3))
	}
	if s.SignalOpens(flat) {
		t.Fatal("a flat chain opens")
	}
	if s.SignalOpens(flat[:119]) {
		t.Fatal("too few parents opens")
	}
	// Four times the reference sustained over the span: opens.
	grown := append([]*big.Int{}, flat[:100]...)
	for i := 0; i < 20; i++ {
		grown = append(grown, bi(4004))
	}
	if !s.SignalOpens(grown) {
		t.Fatal("sustained fourfold growth does not open")
	}
	// A burst that drops below the line for one block inside the span: no.
	burst := append([]*big.Int{}, grown...)
	burst[110] = bi(3999)
	if s.SignalOpens(burst) {
		t.Fatal("an interrupted burst opens")
	}
	// Growth that only reached threefold: no.
	three := append([]*big.Int{}, flat[:100]...)
	for i := 0; i < 20; i++ {
		three = append(three, bi(3003))
	}
	if s.SignalOpens(three) {
		t.Fatal("threefold opens at a fourfold line")
	}
	// The reference is the median, so a spike inside the lookback does not raise it.
	spiked := append([]*big.Int{}, grown...)
	spiked[50] = bi(1 << 40)
	if !s.SignalOpens(spiked) {
		t.Fatal("one spike in the lookback moved the median")
	}
	if !s.PeriodicOpens(2000) || s.PeriodicOpens(2001) || s.PeriodicOpens(0) {
		t.Fatal("the periodic floor")
	}
	if !s.Opens(2000, flat) || s.Opens(1999, flat) || !s.Opens(1999, grown) || !s.Opens(0, nil) {
		t.Fatal("the combined condition")
	}
	if ResearchSelection.Lookback != 6*30*5760 || ResearchSelection.Sustain != 3*30*5760 || ResearchSelection.Period != 24*30*5760 {
		t.Fatal("the research point in blocks")
	}
}

type selectionVector struct {
	Name     string   `json:"name"`
	Lookback uint64   `json:"lookback"`
	Sustain  uint64   `json:"sustain"`
	GrowthN  uint64   `json:"growth_num"`
	GrowthD  uint64   `json:"growth_den"`
	Period   uint64   `json:"period"`
	Parents  []string `json:"parents_oldest_first"`
	Height   uint64   `json:"height"`
	Median   string   `json:"reference_median"`
	Signal   bool     `json:"signal_fires"`
	Floor    bool     `json:"floor_fires"`
	Opens    bool     `json:"opens"`
}

// TestSelectionVectors publishes the signal's statements as vectors for the
// second reading (pow/vectors/selection/vectors.json) at small windows that
// keep the files readable; the research constants are stated in the file's
// header. WRITE_TEST_FILES=1 regenerates, otherwise the file is checked.
func TestSelectionVectors(t *testing.T) {
	path := "../../../../pow/vectors/selection/vectors.json"
	s := Selection{Lookback: 8, Sustain: 4, GrowthNum: 4, GrowthDen: 1, Period: 100}
	seq := func(vals ...int64) []*big.Int {
		out := make([]*big.Int, len(vals))
		for i, v := range vals {
			out[i] = bi(v)
		}
		return out
	}
	rep := func(v int64, n int) []int64 {
		out := make([]int64, n)
		for i := range out {
			out[i] = v
		}
		return out
	}
	cat := func(parts ...[]int64) []int64 {
		var out []int64
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	cases := []struct {
		name    string
		parents []int64
		height  uint64
	}{
		{"flat chain, no signal", cat(rep(1000, 8), rep(1000, 4)), 12},
		{"too few parents: the signal is undefined", cat(rep(1000, 8), rep(4000, 3)), 11},
		{"fourfold sustained over the whole span: fires", cat(rep(1000, 8), rep(4000, 4)), 12},
		{"exactly fourfold on every block: fires (the comparison is at least)", cat(rep(1001, 8), rep(4004, 4)), 12},
		{"one block of the span below the line: does not fire", cat(rep(1000, 8), []int64{4000, 4000, 3999, 4000}), 12},
		{"threefold: does not fire", cat(rep(1000, 8), rep(3000, 4)), 12},
		{"a spike inside the lookback does not move the median", cat([]int64{1000, 1000, 1 << 40, 1000, 1000, 1000, 1000, 1000}, rep(4000, 4)), 12},
		{"the median on an even count is the lower middle: lookback 1000 x4, 2000 x4, reference 1000", cat(rep(1000, 4), rep(2000, 4), rep(4000, 4)), 12},
		{"the lookback ends before the sustain: growth inside the lookback is not the reference", cat(rep(1000, 4), rep(4000, 4), rep(4000, 4)), 12},
		{"the floor at a multiple of the period with a flat chain", cat(rep(1000, 96), rep(1000, 4)), 100},
		{"the floor at a non-multiple with a flat chain", cat(rep(1000, 96), rep(1000, 3)), 99},
		{"genesis opens by rule", nil, 0},
	}
	gen := func() []selectionVector {
		var vs []selectionVector
		for _, c := range cases {
			parents := seq(c.parents...)
			var ps []string
			for _, p := range parents {
				ps = append(ps, p.String())
			}
			med := ""
			if uint64(len(parents)) >= s.Lookback+s.Sustain {
				n := uint64(len(parents))
				med = median(parents[n-s.Lookback-s.Sustain : n-s.Sustain]).String()
			}
			vs = append(vs, selectionVector{c.name, s.Lookback, s.Sustain, s.GrowthNum, s.GrowthDen, s.Period, ps, c.height, med, s.SignalOpens(parents), s.PeriodicOpens(c.height), s.Opens(c.height, parents)})
		}
		return vs
	}
	if os.Getenv("WRITE_TEST_FILES") == "1" {
		b, _ := json.MarshalIndent(map[string]any{
			"rule":     "ENVELOPE-R0A-v0.md section 9.3 item 1, the opening condition: the reference is the median (lower middle on an even count) of the lookback span ending sustain blocks before the height; the signal fires when every difficulty of the last sustain blocks times growth_den is at least the reference times growth_num; the floor fires at a positive multiple of the period; the condition opens on either, and at height 0 by rule; undefined (no fire) with fewer than lookback plus sustain parents",
			"research": map[string]uint64{"lookback": ResearchSelection.Lookback, "sustain": ResearchSelection.Sustain, "growth_num": ResearchSelection.GrowthNum, "growth_den": ResearchSelection.GrowthDen, "period": ResearchSelection.Period},
			"vectors":  gen(),
		}, "", "  ")
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
		Vectors []selectionVector `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	want := gen()
	if len(f.Vectors) != len(want) {
		t.Fatalf("%d vectors, want %d", len(f.Vectors), len(want))
	}
	for i, v := range want {
		g := f.Vectors[i]
		if g.Name != v.Name || g.Median != v.Median || g.Signal != v.Signal || g.Floor != v.Floor || g.Opens != v.Opens {
			t.Errorf("%s differs", v.Name)
		}
	}
}
