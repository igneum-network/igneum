package service

import (
	"testing"
	"time"

	"igneum.network/pool/internal/stratum"
)

// the pool's share-weighted hash rate: each accepted share at its job's expected hashes over the window
func TestHashRateFromShares(t *testing.T) {
	t0 := time.Date(2026, 10, 15, 11, 0, 0, 0, time.UTC)
	now := t0
	s := &Service{acceptTimes: make([]time.Time, 16), acceptWeights: make([]uint64, 16)}
	s.now = func() time.Time { return now }
	for i := 0; i < 6; i++ {
		s.acceptTimes[i], s.acceptWeights[i] = now.Add(-time.Duration(i)*10*time.Second), 1_000_000
	}
	s.acceptTimes[6], s.acceptWeights[6] = now.Add(-10*time.Minute), 1_000_000_000 // outside the window
	hps, n := s.HashRate(60 * time.Second)
	if n != 6 || hps != 100_000 {
		t.Fatalf("hash rate %v from %d shares, want 100000 from 6", hps, n)
	}
	_ = stratum.Share{}
}
