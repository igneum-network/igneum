package mosaic

import (
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"testing"
)

func TestFinalistsEase(t *testing.T) {
	c := ResearchLaneDAA
	d0 := new(big.Int).Lsh(big.NewInt(1), 40)
	// asert-lane: a silence of one clamp window halves at most.
	half := c.AsertLaneNext(d0, 3600, d0)
	r, _ := new(big.Rat).SetFrac(half, d0).Float64()
	if r < 0.49 || r > 0.52 {
		t.Fatalf("asert-lane after one clamp window: %.4f", r)
	}
	// A longer silence clamps: the same.
	if c.AsertLaneNext(d0, 100000, d0).Cmp(half) != 0 {
		t.Fatal("the clamp does not bound the easing")
	}
	// The floor binds: two silences in a row cannot pass 1/4.
	q := c.AsertLaneNext(half, 3600, d0)
	q = c.AsertLaneNext(q, 3600, d0)
	if f, _ := new(big.Rat).SetFrac(q, d0).Float64(); f < 0.25 {
		t.Fatalf("below the floor: %.4f", f)
	}
	// lwma-lane: a pending silence eases; at the target it does not.
	var diffs []*big.Int
	var ints []uint64
	for i := 0; i < 60; i++ {
		diffs = append(diffs, d0)
		ints = append(ints, 75)
	}
	if c.LwmaLaneNext(diffs, ints, 75, d0).Cmp(d0) != 0 {
		t.Fatal("lwma-lane moves at the target")
	}
	if c.LwmaLaneNext(diffs, ints, 2400, d0).Cmp(d0) >= 0 {
		t.Fatal("lwma-lane does not ease on a pending silence")
	}
	for _, rule := range []string{"asert", "lwma"} {
		p := c.RecoveryPath(rule, 160, 15)
		first, _ := p[0].Float64()
		last, _ := p[len(p)-1].Float64()
		if first >= 1 || last < 0.999 || last > 1.001 {
			t.Fatalf("%s: path %d blocks, first %.4f last %.4f", rule, len(p), first, last)
		}
	}
}

// TestWritePaths writes security's TP-115 paths file under WRITE_TEST_FILES=1
// and checks it otherwise: per finalist, suppression length S in {10, 40,
// 160} chain blocks and floor in {1/2, 1/3, 1/4}, the difficulty of each
// block mined after the lane returns as a fraction of the pre-silence
// difficulty until it is back at 1.0.
func TestWritePaths(t *testing.T) {
	path := filepath.Clean("../../../../tests/igneumhash/protocol-a/out/tp115-paths.json")
	gen := func() map[string]any {
		out := map[string]any{
			"record":    "Phase A section 6.3's per-lane difficulty finalists, their exact recovery paths (the protocol team; security's TP-115 through TP50_PATHS)",
			"model":     "a 5-lane chain at 15 s (75 s per lane), HL 3600 s, DT_MAX 3600 s, LWMA window 60; the lane returns after S chain blocks of silence at its honest hash rate, so each block takes the lane's target interval scaled by its difficulty over the pre-silence one (the expected interval, no randomness); the path is the rule's own output. Protocol B's readings, fixed here: the expected interval is floor(T_i x D / D_pre) whole seconds; the printed ratio is D / D_pre rounded to nearest at six decimals; the pre-silence difficulty D_pre is 2^40 (large enough that the fixed-point step's rounding is below the sixth decimal; at 2^20 or 10^6 scattered sixth decimals move by one); S covers 10, 40, 160 and one, two and five clamp windows (300, 600, 1200 chain blocks)",
			"paths":     map[string][]string{},
			"generator": "node/consensus/igneum/mosaic/daa.go RecoveryPath",
		}
		for _, rule := range []string{"asert", "lwma"} {
			for _, S := range []uint64{10, 40, 160, 300, 600, 1200} {
				for _, f := range [][2]uint64{{1, 2}, {1, 3}, {1, 4}} {
					c := ResearchLaneDAA
					c.FloorNum, c.FloorDen = f[0], f[1]
					var ps []string
					for _, r := range c.RecoveryPath(rule, S, 15) {
						ps = append(ps, r.FloatString(6))
					}
					key := fmt.Sprintf("%s_S%d_f%s", rule, S, map[uint64]string{2: "0.5", 3: "0.333", 4: "0.25"}[f[1]])
					out["paths"].(map[string][]string)[key] = ps
				}
			}
		}
		return out
	}
	if os.Getenv("WRITE_TEST_FILES") == "1" {
		b, _ := json.MarshalIndent(gen(), "", "  ")
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
		Paths map[string][]string `json:"paths"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	want := gen()["paths"].(map[string][]string)
	if len(f.Paths) != 36 {
		t.Fatalf("%d paths, want 36", len(f.Paths))
	}
	for k, p := range want {
		if got := f.Paths[k]; len(got) != len(p) || (len(p) > 0 && (got[0] != p[0] || got[len(p)-1] != p[len(p)-1])) {
			t.Errorf("%s: path differs", k)
		}
	}
}
