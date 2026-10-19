package session

import (
	"crypto/rand"
	"testing"
	"time"
)

// TestDuplicateAcrossJobsOnOneHeader pins the VectorWork box soak of 04:23Z, 13 October 2026: a client that restarts
// its nonce range on a re-issued job of the same header (a mid-height raise, a coordinate's re-notify) sent the same
// share twice and the sealer refused it "carried twice"; a share is a duplicate across every job issued on one header.
func TestDuplicateAcrossJobsOnOneHeader(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s, err := New(rand.Reader, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Authorise("0x00000000000000000000000000000000000d0d0d", "w"); err != nil {
		t.Fatal(err)
	}
	s.IssueOn("job-a", "header-1")
	if err := s.Accept("job-a", "", "7", now); err != nil {
		t.Fatalf("first share: %v", err)
	}
	s.IssueOn("job-b", "header-1") // the re-issue on the same header
	if err := s.Accept("job-b", "", "7", now.Add(time.Second)); err != ErrRefound {
		t.Fatalf("the same nonce on the re-issued job must be a re-find (refused, not a bad share), got %v", err)
	}
	if err := s.Accept("job-b", "", "8", now.Add(time.Second)); err != nil {
		t.Fatalf("a new nonce on the re-issued job: %v", err)
	}
	s.IssueOn("job-c", "header-2") // a new header: the space is fresh
	if err := s.Accept("job-c", "", "7", now.Add(2*time.Second)); err != nil {
		t.Fatalf("the nonce on a new header: %v", err)
	}
}
