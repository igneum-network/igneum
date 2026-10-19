package service

import (
	"context"
	"testing"
	"time"
)

// TestFailoverFromZero pins the guard's from-zero case (pool-b2 on the old public test network (chain 7320), 23:59 UK, 12 October 2026: started
// while its template source rolled, it never took a template from the fan-out because the stall was measured
// from a first template it never had). The guard's clock runs from the start when no template has come: after
// three block clocks with the source down and a fan-out sealer serving a height, templates come from that sealer.
func TestFailoverFromZero(t *testing.T) {
	src := newFakeNode(false)
	src.down = true
	fan := newFakeNode(false)
	fan.Advance(5)
	now := time.Now()
	s := &Service{now: func() time.Time { return now }, node: src}
	s.cfg.BlockSeconds = 20
	s.shareNodes = []shareNode{{url: "http://source/", node: src}, {url: "http://fan/", node: fan}}
	// before Run sets the clock: no template, no stall known, the source
	if got := s.templateNode(context.Background()); got != Node(src) {
		t.Fatalf("with no clock the source is the template node")
	}
	// Run's clock from the start, under three block clocks: still the source
	s.lastHeightAt = now.Add(-50 * time.Second)
	if got := s.templateNode(context.Background()); got != Node(src) {
		t.Fatalf("under three block clocks the source is the template node")
	}
	// past three block clocks with no template ever: the fan-out sealer at height 5
	s.lastHeightAt = now.Add(-61 * time.Second)
	if got := s.templateNode(context.Background()); got != Node(fan) {
		t.Fatalf("past three block clocks from the start the fan-out sealer is the template node")
	}
	if s.templateFrom != 1 {
		t.Fatalf("templateFrom %d, want 1", s.templateFrom)
	}
}

// TestSourceNodeUnderLock pins that sourceNode takes no lock (its callers in the credit and settle paths hold s.mu;
// the 0.1.18 hang class): the guard's choice is read from an atomic, and it follows the guard's moves.
func TestSourceNodeUnderLock(t *testing.T) {
	src := newFakeNode(false)
	fan := newFakeNode(false)
	s := &Service{now: time.Now, node: src}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sourceNode() != Node(src) {
		t.Fatalf("the source before any move")
	}
	s.publishSource(fan)
	if s.sourceNode() != Node(fan) {
		t.Fatalf("the guard's node after a move")
	}
	s.publishSource(nil)
	if s.sourceNode() != Node(src) {
		t.Fatalf("the source after the return")
	}
}
