package rpc

import (
	"errors"
	"testing"
	"time"
)

// TestSourceGuardRefusesAfterTheBurst (SECURITY-GAPS.md; the project's order of
// 21:4x UK, 11 October 2026): a hundred-call loop from one source at the
// edge's rate (20 a second, burst 40) is admitted for the burst and refused
// after it; a second source has its own bucket; the bucket refills at the
// rate; a connection with no remote address (IPC) is never limited; the
// denied methods and namespaces are refused without spending a token.
// TestSourceGuardReportsRefusalsOnceAMinute: a source's first refusal is
// reported, the next within the minute are counted and not reported, the
// first past the minute is reported with the count since the last report;
// another source reports on its own.
func TestSourceGuardReportsRefusalsOnceAMinute(t *testing.T) {
	g := newSourceGuard(20, 40, nil, nil, nil, nil)
	now := time.Unix(1_700_000_000, 0)
	g.now = func() time.Time { return now }
	if report, n := g.noteRefusal("203.0.113.7:51000", "eth_getBalance"); !report || n != 1 {
		t.Fatalf("the first refusal: report %v count %d, want reported with 1", report, n)
	}
	for i := 0; i < 999; i++ {
		now = now.Add(50 * time.Millisecond)
		if report, _ := g.noteRefusal("203.0.113.7:51000", "eth_getBalance"); report {
			t.Fatalf("refusal %d inside the minute was reported", i+2)
		}
	}
	now = now.Add(sourceGuardReportEvery)
	if report, n := g.noteRefusal("203.0.113.7:51002", "debug_traceTransaction"); !report || n != 1000 {
		t.Fatalf("past the minute: report %v count %d, want reported with 1000 (999 counted plus this one)", report, n)
	}
	if report, n := g.noteRefusal("198.51.100.9:4000", "eth_getBalance"); !report || n != 1 {
		t.Fatalf("another source's first refusal: report %v count %d", report, n)
	}
	var nilGuard *sourceGuard
	if report, _ := nilGuard.noteRefusal("x", "y"); !report {
		t.Fatal("a nil guard must report")
	}
}

func TestSourceGuardRefusesAfterTheBurst(t *testing.T) {
	g := newSourceGuard(20, 40, []string{"igneum_verifyBlock", "igneum_setWithhold"}, []string{"admin", "miner"}, nil, nil)
	now := time.Unix(1_700_000_000, 0)
	g.now = func() time.Time { return now }
	admitted, refused := 0, 0
	for i := 0; i < 100; i++ {
		if err := g.check("203.0.113.7:51000", "eth_blockNumber"); err == nil {
			admitted++
		} else {
			var rl *rateLimitedError
			if !errors.As(err, &rl) {
				t.Fatalf("call %d: %v (want the rate-limited error)", i, err)
			}
			refused++
		}
	}
	if admitted != 40 || refused != 60 {
		t.Fatalf("one source, 100 calls at burst 40: admitted %d refused %d", admitted, refused)
	}
	// One host's connections share the bucket (the port differs).
	if err := g.check("203.0.113.7:51001", "eth_blockNumber"); err == nil {
		t.Fatal("a second port of the same host was admitted past the burst")
	}
	// Another source has its own bucket.
	if err := g.check("198.51.100.9:4000", "eth_blockNumber"); err != nil {
		t.Fatalf("a second source refused: %v", err)
	}
	// Half a second on: ten tokens back at 20 a second.
	now = now.Add(500 * time.Millisecond)
	back := 0
	for i := 0; i < 20; i++ {
		if g.check("203.0.113.7:51000", "eth_blockNumber") == nil {
			back++
		}
	}
	if back != 10 {
		t.Fatalf("refill at 20 a second over 0.5 s: %d admitted, want 10", back)
	}
	// IPC (no remote address) is never limited.
	for i := 0; i < 200; i++ {
		if err := g.check("", "eth_blockNumber"); err != nil {
			t.Fatalf("IPC limited: %v", err)
		}
	}
	// The deny set: refused outright, no token spent.
	now = now.Add(10 * time.Second) // the bucket is full again
	for _, m := range []string{"igneum_verifyBlock", "igneum_setWithhold", "admin_addPeer", "miner_start"} {
		var np *notPublicError
		if err := g.check("203.0.113.7:51000", m); !errors.As(err, &np) {
			t.Fatalf("%s on the public listener: %v (want the not-public error)", m, err)
		}
	}
	for i := 0; i < 40; i++ {
		if err := g.check("203.0.113.7:51000", "igneum_shareStats"); err != nil {
			t.Fatalf("the deny set spent tokens: call %d refused: %v", i, err)
		}
	}
	// A guard with the rate off limits nothing but still denies.
	off := newSourceGuard(0, 0, []string{"igneum_verifyBlock"}, nil, nil, nil)
	for i := 0; i < 500; i++ {
		if err := off.check("203.0.113.7:1", "eth_chainId"); err != nil {
			t.Fatalf("rate off limited: %v", err)
		}
	}
	if err := off.check("203.0.113.7:1", "igneum_verifyBlock"); err == nil {
		t.Fatal("rate off served a denied method")
	}
	// No guard: nothing checked.
	var none *sourceGuard
	if err := none.check("203.0.113.7:1", "igneum_verifyBlock"); err != nil {
		t.Fatalf("nil guard: %v", err)
	}
}

// TestSourceGuardKeysByTheForwardedClientFromATrustedEdge (the ninth
// development chain, 12 October 2026: every public client behind the mesh
// host's filter was one source to the guard): a call from the trusted edge
// with a forwarded client address is charged to that client, so two clients
// behind the edge draw two buckets and the edge's own address is never a
// key; the last address of a forwarded list (the entry the edge appended) is the client; a call from an
// untrusted peer carrying the header is charged to the peer; an edge call
// without the header is charged to the edge; the denied methods stay refused
// whoever the client is; a nil guard names the peer.
func TestSourceGuardKeysByTheForwardedClientFromATrustedEdge(t *testing.T) {
	g := newSourceGuard(20, 40, []string{"igneum_verifyBlock"}, []string{"admin"}, []string{" 192.0.2.1 "}, nil)
	now := time.Unix(1_700_000_000, 0)
	g.now = func() time.Time { return now }
	edge := "192.0.2.1:40000"
	if s := g.sourceOf(edge, "203.0.113.7"); s != "203.0.113.7" {
		t.Fatalf("the forwarded client from the trusted edge: %q", s)
	}
	if s := g.sourceOf(edge, "10.0.0.5, 203.0.113.7"); s != "203.0.113.7" {
		t.Fatalf("the last of a forwarded list (the entry the edge appended): %q", s)
	}
	if s := g.sourceOf(edge, ""); s != edge {
		t.Fatalf("an edge call without the header: %q", s)
	}
	if s := g.sourceOf("198.51.100.9:51000", "203.0.113.7"); s != "198.51.100.9:51000" {
		t.Fatalf("the header from an untrusted peer: %q", s)
	}
	var none *sourceGuard
	if s := none.sourceOf(edge, "203.0.113.7"); s != edge {
		t.Fatalf("a nil guard: %q", s)
	}
	// Two clients behind the edge: each has the burst of 40, the 41st of
	// each is refused, and the edge's own address holds no bucket.
	for _, c := range []string{"203.0.113.7", "203.0.113.8"} {
		for i := 0; i < 40; i++ {
			if err := g.check(g.sourceOf(edge, c), "eth_blockNumber"); err != nil {
				t.Fatalf("client %s call %d: %v", c, i, err)
			}
		}
		var rl *rateLimitedError
		if err := g.check(g.sourceOf(edge, c), "eth_blockNumber"); !errors.As(err, &rl) {
			t.Fatalf("client %s past the burst: %v", c, err)
		}
	}
	if _, ok := g.buckets["192.0.2.1"]; ok {
		t.Fatal("the edge's address holds a bucket")
	}
	var np *notPublicError
	if err := g.check(g.sourceOf(edge, "203.0.113.9"), "igneum_verifyBlock"); !errors.As(err, &np) {
		t.Fatalf("a forwarded client's denied method: %v", err)
	}
	if err := g.check(g.sourceOf(edge, "203.0.113.9"), "admin_addPeer"); !errors.As(err, &np) {
		t.Fatalf("a forwarded client's denied namespace: %v", err)
	}
}
