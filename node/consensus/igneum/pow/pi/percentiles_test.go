//go:build ignpi

package pi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"
)

// The verifier's distribution through the Go binding (the outside review's gate-14 reading, 11 Oct 2026: median and upper
// percentiles, not one figure): the block's valid K 2 rows verified IGNPI_BENCH_N times in a row (default 1000) on one
// goroutine, the times sorted, median, p95, p99 and max printed in ms. Run from node/:
//
//	IGNPI_BENCH_N=1000 go test -tags ignpi -count=1 -run TestVerifyPercentiles -v ./consensus/igneum/pow/pi/
func TestVerifyPercentiles(t *testing.T) {
	n := 1000
	if v := os.Getenv("IGNPI_BENCH_N"); v != "" {
		if x, err := strconv.Atoi(v); err == nil && x > 0 {
			n = x
		}
	}
	// IGNPI_BENCH_SET names the set (default the first K 2 set; the launch document's is v8-d2048-s0-r64-k32-p256-w128-b2-x16-f5152-k2-n100)
	setName := os.Getenv("IGNPI_BENCH_SET")
	if setName == "" {
		setName = "v8-d2048-s0-r64-k32-p256-w128-b16-x16-f1056-k2"
	}
	dir := filepath.Join("..", "..", "..", "..", "..", "pow", "vectors", "pi", setName)
	raw, err := os.ReadFile(filepath.Join(dir, "pi.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var st set
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	type input struct {
		pk   [32]byte
		m, s [][32]byte
		pi   []byte
	}
	var rows []input
	for _, r := range st.Rows {
		if r.Name != "valid" || r.Domain != DomainSeal {
			continue
		}
		pi, err := os.ReadFile(filepath.Join(dir, r.Receipt))
		if err != nil {
			t.Fatal(err)
		}
		in := input{pk: hex32(t, r.PK), pi: pi}
		for _, x := range r.M {
			in.m = append(in.m, hex32(t, x))
		}
		for _, x := range r.Shares {
			in.s = append(in.s, hex32(t, x))
		}
		rows = append(rows, in)
	}
	if len(rows) == 0 {
		t.Fatal("no valid K 2 rows")
	}
	// warm-up (the first call pays the library's page-in; the outside review's figure is the steady state, the first call stated apart)
	t0 := time.Now()
	if c := Verify(DomainSeal, rows[0].pk, rows[0].m, rows[0].s, rows[0].pi); c != OK {
		t.Fatalf("warm-up: code %d", c)
	}
	first := float64(time.Since(t0).Microseconds()) / 1000
	ms := make([]float64, 0, n)
	for i := 0; i < n; i++ {
		r := rows[i%len(rows)]
		t1 := time.Now()
		if c := Verify(DomainSeal, r.pk, r.m, r.s, r.pi); c != OK {
			t.Fatalf("iteration %d: code %d", i, c)
		}
		ms = append(ms, float64(time.Since(t1).Microseconds())/1000)
	}
	sort.Float64s(ms)
	q := func(p float64) float64 { return ms[int(float64(len(ms)-1)*p)] }
	host, _ := os.Hostname()
	t.Logf("PERCENTILES host %s set %s n %d valid rows (%d distinct proofs, %d bytes each) through pi.Verify: first %.2f ms; min %.2f median %.2f p95 %.2f p99 %.2f max %.2f ms", host, setName, n, len(rows), len(rows[0].pi), first, ms[0], q(0.5), q(0.95), q(0.99), ms[len(ms)-1])
}
