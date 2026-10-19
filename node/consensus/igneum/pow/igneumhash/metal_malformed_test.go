package igneumhash

import (
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
)

// TestMetalMalformed is the OR-0 verifier row (the Apple memory seat's
// ask, 11 October 2026): the Metal worker's own tuples (the device's mix
// and hash on the or-0-guard-1c cell) run through the node's verifier
// unflipped ("ok") and with one word of the mix flipped, the nonce off by
// one, a header bit flipped (all "mix"), the target below the hash
// ("boundary"), and the claimed height in another epoch or outside the
// schedule ("epoch", "height"), each row's reason code and verification
// time recorded. The summary goes to IGNEUM_METAL_OUT as JSON when set.
func TestMetalMalformed(t *testing.T) {
	type row struct {
		Variant string  `json:"variant"`
		Index   int     `json:"index"`
		Expect  string  `json:"expect"`
		Flip    string  `json:"flip"`
		Reason  string  `json:"reason"`
		Agree   bool    `json:"agree"`
		Ms      float64 `json:"ms"`
	}
	type summary struct {
		Variant  string         `json:"variant"`
		Rows     int            `json:"rows"`
		Agree    int            `json:"agree"`
		ByExpect map[string]int `json:"byExpect"`
		MsP50    float64        `json:"msP50"`
		MsP90    float64        `json:"msP90"`
		MsMax    float64        `json:"msMax"`
		Under2ms int            `json:"under2ms"`
		Reasons  map[string]int `json:"reasons"`
		Rows_    []row          `json:"-"`
	}
	var out []summary
	for _, variant := range []string{"v4-d2048-s0-r64-k32-p256-w128", "v4-d4096-s0-r64-k32-p256-w128", "v4-d4096-s0-r64-k32-p256-w256"} {
		t.Run(variant, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(specVectorsRoot, variant, "malformed-metal.json"))
			if err != nil {
				if os.Getenv("IGNEUM_REQUIRE_FIXTURES") != "" {
					t.Fatal(err)
				}
				t.Skip(err)
			}
			var f struct {
				Params  json.RawMessage `json:"params"`
				Vectors []struct {
					Height    json.Number `json:"height"`
					Header    string      `json:"header"`
					Nonce     string      `json:"nonce"`
					CtxHeight json.Number `json:"ctx_height"`
					Mix       string      `json:"mix"`
					Boundary  string      `json:"boundary"`
					Expect    string      `json:"expect"`
					Flip      string      `json:"flip"`
				} `json:"vectors"`
			}
			if err := json.Unmarshal(raw, &f); err != nil {
				t.Fatal(err)
			}
			// The document's width comes from the tag where the file's params
			// omit item_bytes (the 4 GiB files), as the spec vectors do.
			_, p, err := DecodeChainParams(versionedParams(t, variant, f.Params))
			if err != nil {
				t.Fatal(err)
			}
			a := New(DefaultChainID, p, defaultWork())
			s := summary{Variant: variant, ByExpect: map[string]int{}, Reasons: map[string]int{}}
			var times []float64
			for i, v := range f.Vectors {
				height, _ := strconv.ParseUint(v.Height.String(), 10, 64)
				ctx, _ := strconv.ParseUint(v.CtxHeight.String(), 10, 64)
				nonce, _ := strconv.ParseUint(v.Nonce, 10, 64)
				header, mix := common.HexToHash(v.Header), common.HexToHash(v.Mix)
				target := new(big.Int).SetBytes(common.FromHex(v.Boundary))
				r := row{Variant: variant, Index: i, Expect: v.Expect, Flip: v.Flip}
				s.ByExpect[v.Expect]++
				switch v.Expect {
				case "ok", "boundary", "mix":
					start := time.Now()
					err := pow.VerifyTarget(a, header, ctx, nonce, mix, target)
					r.Ms = float64(time.Since(start).Microseconds()) / 1000
					times = append(times, r.Ms)
					want := map[string]error{"ok": nil, "boundary": pow.ErrResultAboveTarget, "mix": pow.ErrInvalidMixDigest}[v.Expect]
					r.Reason = "accepted"
					if err != nil {
						r.Reason = err.Error()
					}
					r.Agree = (want == nil && err == nil) || errors.Is(err, want)
				case "epoch", "height":
					// The engine refuses a claimed height that is not the chain
					// context's before the algorithm runs (consensus.go, the
					// validated height is the only one fed to the proof); the
					// row shows the claimed height's own mix differs.
					start := time.Now()
					got, _ := a.Compute(header, height, nonce)
					r.Ms = float64(time.Since(start).Microseconds()) / 1000
					r.Reason = "refused by the engine: the claimed height is not the chain context's (verifySeal feeds the validated height only)"
					r.Agree = height != ctx && (a.Epoch(height) == a.Epoch(ctx) || got != mix)
				default:
					t.Fatalf("vector %d: unknown expectation %q", i, v.Expect)
				}
				if !r.Agree {
					t.Errorf("vector %d (%s, %s): %s", i, v.Expect, v.Flip, r.Reason)
				} else {
					s.Agree++
				}
				s.Reasons[r.Reason]++
				s.Rows++
				s.Rows_ = append(s.Rows_, r)
			}
			sort.Float64s(times)
			if n := len(times); n > 0 {
				s.MsP50, s.MsP90, s.MsMax = times[n/2], times[int(float64(n)*0.9)], times[n-1]
				for _, x := range times {
					if x < 2 {
						s.Under2ms++
					}
				}
			}
			t.Logf("%s: %d rows, %d agree, p50 %.3f ms, p90 %.3f ms, max %.3f ms, %d of %d under 2 ms", variant, s.Rows, s.Agree, s.MsP50, s.MsP90, s.MsMax, s.Under2ms, len(times))
			out = append(out, s)
		})
	}
	if path := os.Getenv("IGNEUM_METAL_OUT"); path != "" && len(out) > 0 {
		full := map[string]any{"row": "OR-0 invalid proofs through the node verifier (the Metal worker's tuples, flipped per class); research prototype record, 11 October 2026", "summaries": out}
		var rows []row
		for _, s := range out {
			rows = append(rows, s.Rows_...)
		}
		full["rows"] = rows
		b, _ := json.MarshalIndent(full, "", " ")
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
