package service

import "testing"

// 0.1.76: every job names the chain's paid coordinates at its height, from the pay list as the pool last read it.
func TestPaidCoordsFromThePayList(t *testing.T) {
	s := &Service{}
	if got := s.paidCoordsLocked(); got != nil {
		t.Fatalf("before the first read: %v", got)
	}
	s.payShares = []uint64{0, 0, 0, 0, 0, 1, 1, 1}
	got := s.paidCoordsLocked()
	if len(got) != 3 || got[0] != 5 || got[1] != 6 || got[2] != 7 {
		t.Fatalf("the 7322 succession's list: %v", got)
	}
	s.payShares = []uint64{0, 0, 1, 1, 1}
	if got := s.paidCoordsLocked(); len(got) != 3 || got[0] != 2 || got[2] != 4 {
		t.Fatalf("the list before the succession: %v", got)
	}
}
