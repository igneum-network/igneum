package service

import (
	"strings"
	"testing"
	"time"

	"igneum.network/pool/internal/shares"
	"igneum.network/pool/internal/stratum"
)

// TestAdmit pins the admission bound before acceptance (the project's order of
// 08:5x UK, 12 October 2026; PA-53's admission bound as the pool applies
// it): an address at the chain's per-height cap, or the node's pool at
// capacity, is refused by name before acceptance; under the limits
// everything is admitted; without limits (a node serving none) nothing is
// refused.
func TestAdmit(t *testing.T) {
	s := &Service{now: time.Now, window: shares.NewWindow(1)}
	if ok, _ := s.Admit("0xabc", 10); !ok {
		t.Fatalf("refused with no limits")
	}
	s.limits = shareLimits{CapPerAddress: 3, PoolCapacity: 100, MaxShares: 256, Recency: 64}
	for i := 0; i < 3; i++ {
		if ok, why := s.Admit("0xABC", 10); !ok {
			t.Fatalf("share %d refused: %s", i, why)
		}
		s.Share(stratum.Share{Account: "0xabc", Height: 10, Weight: 1})
	}
	if ok, why := s.Admit("0xabc", 10); ok || !strings.HasPrefix(why, "address at cap") {
		t.Fatalf("at the cap: ok %v why %q", ok, why)
	}
	if ok, _ := s.Admit("0xabc", 11); !ok {
		t.Fatalf("the next height refused")
	}
	if ok, _ := s.Admit("0xdef", 10); !ok {
		t.Fatalf("another address refused at the cap of the first")
	}
	s.pooledNow = 100
	if ok, why := s.Admit("0xdef", 11); ok || !strings.HasPrefix(why, "the node's pool is at capacity") {
		t.Fatalf("at capacity: ok %v why %q", ok, why)
	}
}

// TestAdmitAggregate pins F-50 (the security review's path 2, 12 October 2026): the
// pool's shares outstanding within the recency across EVERY address are
// bounded by what the chain carries over the recency; past it a share is
// refused "pool at carriage" before the receipt, whatever its address.
func TestAdmitAggregate(t *testing.T) {
	s := &Service{now: time.Now, window: shares.NewWindow(1), journal: &shares.Journal{}}
	s.limits = shareLimits{CapPerAddress: 256, PoolCapacity: 16384, MaxShares: 8, Recency: 2}
	s.sharesPerBlock = 4 // the lane's operating rate: the chain carries 8 over the recency (4 x 2), under the ceiling 16
	s.outstandingRecent = 7
	if ok, _ := s.Admit("0xaaa", 10); !ok {
		t.Fatalf("refused under the bound")
	}
	s.acceptedSinceRead++ // the eighth, accepted since the poll's read
	if ok, why := s.Admit("0xbbb", 10); ok || !strings.HasPrefix(why, "pool at carriage") {
		t.Fatalf("at carriage: ok %v why %q", ok, why)
	}
	s.outstandingRecent, s.acceptedSinceRead = 3, 0 // the chain carried some (the next poll's read)
	if ok, _ := s.Admit("0xbbb", 11); !ok {
		t.Fatalf("refused after the chain carried")
	}
}

// TestSettleFromNodeUnderLock pins the lock contract of the settle: since 14 October 2026 (the 04:44Z gap: the
// sealers' calls ran under the service lock) its caller does NOT hold the lock and the settle takes it for its
// state alone, so it returns at once with no accounts outstanding and the lock free to others.
func TestSettleFromNodeUnderLock(t *testing.T) {
	s := &Service{now: time.Now, window: shares.NewWindow(1)}
	s.journal = &shares.Journal{}
	done := make(chan struct{})
	go func() {
		s.settleFromNode(1) // no accounts outstanding: returns at once
		s.mu.Lock()         // and leaves the lock free
		s.mu.Unlock()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("settleFromNode took the service lock its caller holds")
	}
}
