package stratum

import (
	"testing"

	"igneum.network/pool/internal/validate"
)

// TestRenotifyIsNeverClean pins the keepalive's wire form (the security review's residual on 0.1.58, 15 October 2026): a job
// first issued clean (a plain share job, no coordinate) is sent again by Renotify with the same id and Clean false, so a
// miner honouring clean_jobs keeps the search it holds; a job the server no longer retains is not sent.
func TestRenotifyIsNeverClean(t *testing.T) {
	srv, _, c := setup(t)
	if _, e := c.call(MethodSubscribe, []string{"test/0"}); e != nil {
		t.Fatalf("subscribe: %+v", e)
	}
	if _, e := c.call(MethodAuthorize, []string{addr, "worker1"}); e != nil {
		t.Fatalf("authorize: %+v", e)
	}
	job := Job{ID: "00000000000000c1", Height: 10, Algorithm: validate.NullAlgorithm,
		ShareTarget: validate.TargetForDifficulty(16), Boundary: validate.TargetForDifficulty(1 << 20), Clean: true}
	job.Header[0] = 0xcd
	srv.SetJob(job)
	first := c.readNotify()
	if first.JobID != job.ID || !first.Clean {
		t.Fatalf("the first notify is the clean job: %+v", first)
	}
	if n := srv.Renotify(job.ID); n != 1 {
		t.Fatalf("Renotify reached %d session(s), want 1", n)
	}
	again := c.readNotify()
	if again.JobID != job.ID || again.Header != first.Header || again.Clean {
		t.Fatalf("the keepalive must carry the same job, never clean: %+v", again)
	}
	if n := srv.Renotify("0000000000000000"); n != 0 {
		t.Fatalf("a job not retained was sent to %d session(s)", n)
	}
}
