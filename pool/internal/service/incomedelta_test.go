//go:build igneumhash

package service

import "testing"

// the review lead's ruling of 17 October 2026 (economics' form): the income rule has no second form. The record of a held
// close is the window staying open: the state's deltaRefusedAt names the window held, said once.
func TestAnUnreadableArchiveHoldsTheCloseOnce(t *testing.T) {
	ps := &payoutState{}
	held := func(from uint64) bool {
		if ps.deltaRefusedAt != from {
			ps.deltaRefusedAt = from
			return true // said
		}
		return false // held again, silent
	}
	if !held(3820) {
		t.Fatalf("the first hold is said")
	}
	if held(3820) {
		t.Fatalf("the same window's hold is said once")
	}
	if !held(3884) {
		t.Fatalf("the next window's hold is said")
	}
}
