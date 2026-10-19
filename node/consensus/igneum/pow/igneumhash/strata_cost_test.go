package igneumhash

import (
	"fmt"
	"math/big"
	"os"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
)

// TestStrataVerifierCost is the Go-path verifier row per width (STRATA.md
// section 3, the brief's two rows seated with the protocol team): for each frozen
// width document the light verification of one proof (the epoch cache held,
// ROUNDS items reconstructed at width W, the whole-item round), cold and
// warm p50, p95, p99 and max, allocations and bytes per proof, the live
// heap before and after, and the invalid-proof cost by stage. Under
// IGNEUM_STRATA_COST=1 only; it prints one row per width.
func TestStrataVerifierCost(t *testing.T) {
	if os.Getenv("IGNEUM_STRATA_COST") == "" {
		t.Skip("set IGNEUM_STRATA_COST=1 to measure the Strata verifier rows")
	}
	quant := func(d []time.Duration) (p50, p95, p99, max time.Duration) {
		sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
		q := func(f float64) time.Duration { return d[int(f*float64(len(d)-1))] }
		return q(0.5), q(0.95), q(0.99), d[len(d)-1]
	}
	const proofs = 200
	// The documents: the four frozen Strata widths by default, or the
	// newline-separated list in IGNEUM_COST_DOCS (any documents this adapter
	// accepts: the Riptide cells, the 4 GiB successors, version 1 points).
	var docs []string
	if env := os.Getenv("IGNEUM_COST_DOCS"); env != "" {
		for _, d := range strings.Split(env, "\n") {
			if d = strings.TrimSpace(d); d != "" {
				docs = append(docs, d)
			}
		}
	} else {
		for _, w := range []uint32{64, 128, 256, 512} {
			docs = append(docs, fmt.Sprintf(`{"version": 4, "dataset_mib": 2048, "scratch_kib": 0, "rounds": 64, "cache_mib": 32, "parents": 256, "item_bytes": %d}`, w))
		}
	}
	for _, doc := range docs {
		_, p, err := DecodeChainParams([]byte(doc))
		if err != nil {
			t.Fatal(err)
		}
		w := p.ItemWidth()
		a := New(DefaultChainID, p, specWork{})
		a.Anchor(1)
		header := common.HexToHash("0x6468beee66bb5264f1191b196797b748220bf33cf2072630a7f1c030c520aa11")
		// Proofs at an easy difficulty: the verifier's work does not depend on
		// the target, so difficulty 1 lets every nonce be a valid proof.
		type proof struct {
			nonce uint64
			mix   common.Hash
		}
		var ps []proof
		for n := uint64(0); n < proofs; n++ {
			mix, _ := a.Compute(header, 1, 1000+n)
			ps = append(ps, proof{1000 + n, mix})
		}
		one := big.NewInt(1)
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		start := time.Now()
		if err := a.Verify(header, 1, ps[0].nonce, ps[0].mix, one, false); err != nil {
			t.Fatal(err)
		}
		cold := time.Since(start)
		var times []time.Duration
		for r := 0; r < 5*proofs; r++ {
			pr := ps[r%proofs]
			s := time.Now()
			if err := a.Verify(header, 1, pr.nonce, pr.mix, one, false); err != nil {
				t.Fatal(err)
			}
			times = append(times, time.Since(s))
		}
		runtime.ReadMemStats(&after)
		allocs := testing.AllocsPerRun(20, func() { a.Verify(header, 1, ps[0].nonce, ps[0].mix, one, false) })
		p50, p95, p99, max := quant(times)
		t.Logf("go-path\tversion %d\twidth %d\trounds %d\tdataset %d MiB\tP %x\tcold %v\twarm p50 %v\tp95 %v\tp99 %v\tmax %v\tallocs/proof %.0f\tbytes/proof %d\tlive heap before %d KB after %d KB\tcache %d MB",
			p.Version, w, p.Rounds, p.DatasetBytes>>20, p.Hash(), cold, p50, p95, p99, max, allocs, (after.TotalAlloc-before.TotalAlloc)/uint64(5*proofs+1), before.HeapAlloc/1024, after.HeapAlloc/1024, p.CacheBytes>>20)
		// Invalid classes: the stage each fails at and its cost.
		classes := []struct {
			name string
			fn   func() error
		}{
			{"difficulty zero (stage 0, before any work)", func() error { return a.Verify(header, 1, ps[0].nonce, ps[0].mix, big.NewInt(0), false) }},
			{"altered mix digest (full reconstruction, then the digest check)", func() error {
				m := ps[0].mix
				m[0] ^= 1
				return a.Verify(header, 1, ps[0].nonce, m, one, false)
			}},
			{"wrong nonce (full reconstruction, then the digest check)", func() error { return a.Verify(header, 1, ps[0].nonce+777777, ps[0].mix, one, false) }},
			{"target missed (full reconstruction, digest right, target wrong)", func() error {
				return a.Verify(header, 1, ps[0].nonce, ps[0].mix, new(big.Int).Lsh(one, 255), false)
			}},
		}
		for _, c := range classes {
			var ct []time.Duration
			ok := true
			for r := 0; r < 50; r++ {
				s := time.Now()
				err := c.fn()
				ct = append(ct, time.Since(s))
				if err == nil {
					ok = false
				}
			}
			q50, _, q99, qmax := quant(ct)
			t.Logf("invalid\twidth %d\t%s\trejected %v\tp50 %v\tp99 %v\tmax %v", w, c.name, ok, q50, q99, qmax)
		}
		_ = pow.ErrInvalidDifficulty
	}
}
