package stratum

import (
	"testing"

	"igneum.network/pool/internal/limits"
)

// TestJobsRetainedPerMember pins the project's order of 07:3x UK, 12 October 2026
// (the mini's eight percent stale): a member's job stays retained while
// the other members' templates arrive, up to limits.MaxJobsRetained of its
// own, so a slow card's share on the template that was the head when its
// search began is not stale at the pool.
func TestJobsRetainedPerMember(t *testing.T) {
	s := &Server{jobs: map[string]Job{}, members: map[string]*Job{}, sessions: map[*conn]struct{}{}}
	mk := func(member string, h uint64) Job {
		j := Job{Member: member, Height: h}
		j.Header[0] = byte(h)
		copy(j.Header[1:], []byte(member))
		j.ID = j.Identity(1)
		return j
	}
	slow := mk("0xaaaa", 1)
	s.SetJob(slow)
	// twenty templates for three other members
	for h := uint64(2); h < 22; h++ {
		for _, m := range []string{"0xbbbb", "0xcccc", "0xdddd"} {
			s.SetJob(mk(m, h))
		}
	}
	if _, ok := s.JobByID(slow.ID); !ok {
		t.Fatalf("the slow member's only job was evicted by the other members' templates")
	}
	// the member's own retention bound
	for h := uint64(2); h < uint64(2+limits.MaxJobsRetained); h++ {
		s.SetJob(mk("0xaaaa", h))
	}
	if _, ok := s.JobByID(slow.ID); ok {
		t.Fatalf("a job beyond the member's own %d retained is still retained", limits.MaxJobsRetained)
	}
	latest := mk("0xaaaa", uint64(1+limits.MaxJobsRetained))
	if _, ok := s.JobByID(latest.ID); !ok {
		t.Fatalf("the member's latest job is not retained")
	}
	// the age read
	s.countAge("0xAAAA", latest)
	older := mk("0xaaaa", uint64(1+limits.MaxJobsRetained-2))
	s.countAge("0xaaaa", older)
	st := s.ShareAgeStats()["by_address"].(map[string]interface{})["0xaaaa"].(map[string]interface{})
	by := st["by_age"].([]uint64)
	if len(by) < 3 || by[0] != 1 || by[2] != 1 {
		t.Fatalf("by_age = %v, want [1 0 1]", by)
	}
	s.countStale("0xaaaa")
	if n := s.ShareAgeStats()["by_address"].(map[string]interface{})["0xaaaa"].(map[string]interface{})["stale_refused"].(uint64); n != 1 {
		t.Fatalf("stale_refused = %d, want 1", n)
	}
}
