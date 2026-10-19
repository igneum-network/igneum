package session

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"igneum.network/pool/internal/limits"
)

const addr = "0x00112233445566778899aabbccddeeff00112233"

func newSession(t *testing.T) (*Session, time.Time) {
	t.Helper()
	now := time.Unix(1_700_000_000, 0)
	s, err := New(bytes.NewReader(bytes.Repeat([]byte{7}, 64)), now)
	if err != nil {
		t.Fatal(err)
	}
	return s, now
}

func TestAuthoriseBounds(t *testing.T) {
	s, _ := newSession(t)
	if err := s.Authorise("1234", "worker1"); !errors.Is(err, ErrBadAddress) {
		t.Fatalf("short address: got %v", err)
	}
	if err := s.Authorise(addr, strings.Repeat("a", limits.MaxWorkerName+1)); !errors.Is(err, ErrBadWorker) {
		t.Fatalf("long worker: got %v", err)
	}
	if err := s.Authorise(addr, "rig one"); !errors.Is(err, ErrBadWorker) {
		t.Fatalf("space in worker: got %v", err)
	}
	if err := s.Authorise(addr, "rig-1.a_b"); err != nil {
		t.Fatalf("valid: got %v", err)
	}
	if !s.Authorised() {
		t.Fatal("expected authorised")
	}
}

func TestAcceptRequiresAuthorisation(t *testing.T) {
	s, now := newSession(t)
	s.Issue("job1")
	if err := s.Accept("job1", "00000001", "0000000000000001", now); !errors.Is(err, ErrNotAuthorised) {
		t.Fatalf("got %v", err)
	}
}

func TestStaleAndDuplicate(t *testing.T) {
	s, now := newSession(t)
	if err := s.Authorise(addr, "worker1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Accept("nojob", "00", "01", now); !errors.Is(err, ErrStale) {
		t.Fatalf("unknown job: got %v", err)
	}
	s.Issue("job1")
	if err := s.Accept("job1", "00", "01", now); err != nil {
		t.Fatalf("first share: got %v", err)
	}
	if err := s.Accept("job1", "00", "01", now); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate: got %v", err)
	}
	if err := s.Accept("job1", "00", "02", now); err != nil {
		t.Fatalf("different nonce: got %v", err)
	}
	// Issue more jobs than the retained bound; job1 must become stale.
	for i := 0; i < limits.MaxJobsRetained; i++ {
		s.Issue("later" + string(rune('a'+i)))
	}
	if err := s.Accept("job1", "00", "03", now); !errors.Is(err, ErrStale) {
		t.Fatalf("evicted job: got %v", err)
	}
}

func TestRateLimit(t *testing.T) {
	s, now := newSession(t)
	if err := s.Authorise(addr, "worker1"); err != nil {
		t.Fatal(err)
	}
	s.Issue("job1")
	for i := 0; i < limits.MaxSharesPerSecond; i++ {
		if err := s.Accept("job1", "00", string(rune('a'+i%26))+string(rune('a'+i/26)), now); err != nil {
			t.Fatalf("share %d: got %v", i, err)
		}
	}
	if err := s.Accept("job1", "00", "zz", now); !errors.Is(err, ErrRate) {
		t.Fatalf("over rate: got %v", err)
	}
	if err := s.Accept("job1", "00", "zz", now.Add(time.Second)); err != nil {
		t.Fatalf("after refill: got %v", err)
	}
}

func TestBlockClaimsHaveTheirOwnBucket(t *testing.T) {
	s, now := newSession(t)
	if err := s.Authorise(addr, "worker1"); err != nil {
		t.Fatal(err)
	}
	s.Issue("job1")
	for i := 0; i < limits.MaxSharesPerSecond; i++ {
		if err := s.Accept("job1", "01", string(rune('a'+i%26))+string(rune('a'+i/26)), now); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Accept("job1", "01", "share-over", now); !errors.Is(err, ErrRate) {
		t.Fatalf("share over rate: %v", err)
	}
	// The share bucket is empty; a block claim still passes, up to its own rate.
	for i := 0; i < limits.MaxBlockClaimsPerSecond; i++ {
		if err := s.AcceptBlockClaim("job1", "02", string(rune('a'+i)), now); err != nil {
			t.Fatalf("block claim %d: %v", i, err)
		}
	}
	if err := s.AcceptBlockClaim("job1", "02", "over", now); !errors.Is(err, ErrRate) {
		t.Fatalf("block claim over rate: %v", err)
	}
}

// TestIssueAgainKeepsTheNonceMemory pins the keepalive's half in the session (F-164, 15 October 2026): a job issued
// again to a session that holds it keeps the nonces seen on it (a share accepted before the keepalive is a duplicate
// after it) and its one place among the retained jobs (a re-issue never evicts a live job's memory).
func TestIssueAgainKeepsTheNonceMemory(t *testing.T) {
	s, now := newSession(t)
	if err := s.Authorise(addr, "w"); err != nil {
		t.Fatal(err)
	}
	s.IssueOn("job1", "aa")
	if err := s.Accept("job1", "00000001", "0000000000000001", now); err != nil {
		t.Fatalf("first share: %v", err)
	}
	s.IssueOn("job1", "aa")
	if err := s.Accept("job1", "00000001", "0000000000000001", now); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("the same nonce after the re-issue: got %v, want duplicate", err)
	}
	if err := s.Accept("job1", "00000001", "0000000000000002", now); err != nil {
		t.Fatalf("a new nonce after the re-issue: %v", err)
	}
	// the retained bound counts the job once: MaxJobsRetained - 1 further jobs leave job1 live, one more evicts it
	for i := 0; i < limits.MaxJobsRetained-1; i++ {
		s.IssueOn("job1", "aa")
		s.Issue("more" + strings.Repeat("x", i))
	}
	if err := s.Accept("job1", "00000001", "0000000000000003", now); err != nil {
		t.Fatalf("job1 evicted early by its re-issues: %v", err)
	}
	s.Issue("last")
	if err := s.Accept("job1", "00000001", "0000000000000004", now); !errors.Is(err, ErrStale) {
		t.Fatalf("job1 past the retained bound: got %v, want stale", err)
	}
}
