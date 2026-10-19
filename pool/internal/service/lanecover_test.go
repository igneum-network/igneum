package service

import (
	"testing"
	"time"
)

// 0.1.96: a paid lane with no accepted share for 64 blocks is named; a lane never hashed is "never"; a lane with a
// recent share is not listed.
func TestAPaidLaneWithoutAWorkerIsNamed(t *testing.T) {
	t0 := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	s := &Service{cfg: Config{BlockSeconds: 12}, laneLastAccepted: map[uint8]time.Time{6: t0.Add(-10 * time.Second), 5: t0.Add(-51 * time.Minute)}, startedAt: t0.Add(-2 * time.Hour)}
	s.now = func() time.Time { return t0 }
	got := s.lanesWithoutWorkerLocked([]uint8{5, 6, 7})
	if len(got) != 2 {
		t.Fatalf("lanes 5 (silent 51 min) and 7 (never): %+v", got)
	}
	if got[0].Coord != 5 || got[0].Never || got[0].SinceSeconds < 3000 {
		t.Fatalf("lane 5 silent since 51 min: %+v", got[0])
	}
	if got[1].Coord != 7 || !got[1].Never {
		t.Fatalf("lane 7 never hashed: %+v", got[1])
	}
	// the notify's lane_workers: two workers inside 64 blocks on lane 6, one silent past it forgotten; nil on a one-form chain
	s.laneWorkerAt = map[uint8]map[string]time.Time{6: {"0xa|w1": t0.Add(-10 * time.Second), "0xa|w2": t0.Add(-5 * time.Minute), "0xb|old": t0.Add(-2 * time.Hour)}}
	if n := s.laneWorkersLocked(6, true); n == nil || *n != 2 {
		t.Fatalf("two workers on lane 6: %v", n)
	}
	if n := s.laneWorkersLocked(5, true); n == nil || *n != 0 {
		t.Fatalf("lane 5 uncovered: 0, present: %v", n)
	}
	if s.laneWorkersLocked(0, false) != nil {
		t.Fatalf("nil on a one-form chain")
	}
	s.laneLastAccepted[5] = t0.Add(-5 * time.Minute)
	if got := s.lanesWithoutWorkerLocked([]uint8{5, 6}); len(got) != 0 {
		t.Fatalf("a lane with a share inside 64 blocks is not listed: %+v", got)
	}
}
