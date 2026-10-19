package rpc

import (
	"errors"
	"testing"
	"time"
)

// TestSourceGuardExemptSources (F-209, 16 October 2026): the public
// guard never charges an exempt source (a host, or a CIDR prefix: the mesh's
// own callers, whose calls are not the public allowance's), while a client
// the trusted edge forwards is charged per its own address and a
// client-supplied X-Forwarded-For from a source that is not a trusted edge
// is ignored (the spoof), the caller charged as itself. The deny set holds
// for an exempt source as for any.
func TestSourceGuardExemptSources(t *testing.T) {
	g := newSourceGuard(20, 40, []string{"igneum_setWithhold"}, []string{"admin"}, []string{"192.0.2.1"}, []string{"192.0.2.1", "198.51.100.0/24"})
	now := time.Unix(1_700_000_000, 0)
	g.now = func() time.Time { return now }
	isLimited := func(err error) bool {
		var rl *rateLimitedError
		return errors.As(err, &rl)
	}

	// The trusted edge's own calls, no forwarded client (the box's own reads):
	// the exempt host (the addresses are the documentation ranges, RFC 5737; the
	// mesh's own are the flag's business), never charged, 1,000 calls in one second admitted.
	for i := 0; i < 1000; i++ {
		if err := g.check(g.sourceOf("192.0.2.1:40660", ""), "eth_getBlockByNumber"); err != nil {
			t.Fatalf("call %d from the exempt edge refused: %v", i, err)
		}
	}
	// A mesh host inside the exempt prefix, the same.
	for i := 0; i < 1000; i++ {
		if err := g.check(g.sourceOf("198.51.100.9:5000", ""), "eth_blockNumber"); err != nil {
			t.Fatalf("call %d from the exempt prefix refused: %v", i, err)
		}
	}
	// The deny set still holds for an exempt source.
	if err := g.check(g.sourceOf("192.0.2.1:40661", ""), "admin_peers"); err == nil || isLimited(err) {
		t.Fatalf("an admin call from the exempt edge: %v, want the not-public refusal", err)
	}
	if err := g.check(g.sourceOf("192.0.2.1:40661", ""), "igneum_setWithhold"); err == nil || isLimited(err) {
		t.Fatalf("a denied method from the exempt edge: %v, want the not-public refusal", err)
	}

	// An outside client forwarded by the trusted edge is charged per its own
	// address: the burst of 40, then refused, and a second client untouched.
	admitted := 0
	for i := 0; i < 100; i++ {
		if err := g.check(g.sourceOf("192.0.2.1:40662", "203.0.113.7"), "eth_blockNumber"); err == nil {
			admitted++
		} else if !isLimited(err) {
			t.Fatalf("forwarded client, call %d: %v", i, err)
		}
	}
	if admitted != 40 {
		t.Fatalf("a forwarded outside client admitted %d of 100 at burst 40", admitted)
	}
	if err := g.check(g.sourceOf("192.0.2.1:40663", "203.0.113.8"), "eth_blockNumber"); err != nil {
		t.Fatalf("a second forwarded client refused: %v", err)
	}
	// The edge itself stays exempt after forwarding clients.
	if err := g.check(g.sourceOf("192.0.2.1:40664", ""), "eth_blockNumber"); err != nil {
		t.Fatalf("the exempt edge refused after its forwarded clients spent theirs: %v", err)
	}

	// The spoof through an appending edge (the security review's read of the cut): a
	// client sends X-Forwarded-For naming the exempt edge and the trusted
	// edge APPENDS the client's own address; the source is the last entry,
	// the client's, charged as itself: 40 of 100, never the exempt host.
	appendAdmitted := 0
	for i := 0; i < 100; i++ {
		if err := g.check(g.sourceOf("192.0.2.1:40665", "192.0.2.1, 203.0.113.9"), "eth_blockNumber"); err == nil {
			appendAdmitted++
		} else if !isLimited(err) {
			t.Fatalf("the appended spoof, call %d: %v", i, err)
		}
	}
	if appendAdmitted != 40 {
		t.Fatalf("a client naming the exempt edge first in X-Forwarded-For through an appending trusted edge admitted %d of 100, want 40 (its own bucket)", appendAdmitted)
	}
	if key := g.sourceOf("192.0.2.1:40665", "192.0.2.1, 203.0.113.9"); key != "203.0.113.9" {
		t.Fatalf("the appended spoof's source key %q, want the client's own address (the last entry)", key)
	}
	if key := g.sourceOf("192.0.2.1:40665", "198.51.100.9, 192.0.2.1, 203.0.113.9"); key != "203.0.113.9" {
		t.Fatalf("a two-entry spoof's source key %q, want the client's own address (the last entry)", key)
	}

	// The spoof from a source that is not a trusted edge: X-Forwarded-For
	// naming the exempt edge is ignored and the caller is charged as itself,
	// refused past its burst.
	spoofAdmitted := 0
	for i := 0; i < 100; i++ {
		if err := g.check(g.sourceOf("192.0.2.33:7000", "192.0.2.1"), "eth_blockNumber"); err == nil {
			spoofAdmitted++
		} else if !isLimited(err) {
			t.Fatalf("the spoofing source, call %d: %v", i, err)
		}
	}
	if spoofAdmitted != 40 {
		t.Fatalf("a source spoofing the exempt edge's address in X-Forwarded-For admitted %d of 100, want 40 (its own bucket)", spoofAdmitted)
	}
	if key := g.sourceOf("192.0.2.33:7000", "192.0.2.1"); key != "192.0.2.33:7000" {
		t.Fatalf("the spoof's source key %q, want the caller's own address", key)
	}
}
