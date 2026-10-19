package stratum

import (
	"testing"

	"igneum.network/pool/internal/validate"
)

// 0.1.77 (F-290's replay half, the 7322 succession of 16 October 2026 at 2,860): after the pay list moves to the
// successors, a session that authorises receives the successors' jobs alone; the retired coordinates' jobs leave
// the member set and the retained set.
func TestReplayCarriesPaidCoordinatesAlone(t *testing.T) {
	srv := NewServer(validate.Null{}, nil)
	member, worker := "0xABCDEF0123456789abcdef0123456789ABCDEF01", "own-worker-1"
	mk := func(id string, coord uint8, height uint64, paid []uint8) Job {
		return Job{ID: id, Height: height, Member: member, Worker: worker, Coord: coord, HasCoord: true, PaidCoords: paid,
			ShareTarget: validate.TargetForDifficulty(16), Boundary: validate.TargetForDifficulty(1 << 20)}
	}
	srv.SetJob(mk("00000000000000a2", 2, 2860, []uint8{2, 3, 4}))
	srv.SetJob(mk("00000000000000a3", 3, 2860, []uint8{2, 3, 4}))
	srv.SetJob(mk("00000000000000a4", 4, 2860, []uint8{2, 3, 4}))
	if got := srv.MemberJobs(member, worker); len(got) != 3 {
		t.Fatalf("before the switch: %d jobs", len(got))
	}
	srv.SetJob(mk("00000000000000b5", 5, 2861, []uint8{5, 6, 7}))
	got := srv.MemberJobs(member, worker)
	if len(got) != 1 || got[0].Coord != 5 {
		t.Fatalf("after the switch, before 6 and 7 are served: %+v", got)
	}
	srv.SetJob(mk("00000000000000b6", 6, 2863, []uint8{5, 6, 7}))
	srv.SetJob(mk("00000000000000b7", 7, 2875, []uint8{5, 6, 7}))
	got = srv.MemberJobs(member, worker)
	if len(got) != 3 || got[0].Coord != 5 || got[2].Coord != 7 {
		t.Fatalf("the successors alone: %+v", got)
	}
	for _, id := range []string{"00000000000000a2", "00000000000000a3", "00000000000000a4"} {
		if _, ok := srv.JobByID(id); ok {
			t.Fatalf("the retired job %s is still retained", id)
		}
	}
	if _, ok := srv.JobByID("00000000000000b5"); !ok {
		t.Fatal("the successor's job left the retained set")
	}
	// a one-form job (no coordinate) and a server that never saw a pay list are untouched
	plain := NewServer(validate.Null{}, nil)
	plain.SetJob(Job{ID: "00000000000000c1", Height: 10, Member: member, Worker: worker, ShareTarget: validate.TargetForDifficulty(16)})
	if got := plain.MemberJobs(member, worker); len(got) != 1 {
		t.Fatalf("the one-form job: %+v", got)
	}
}
