package stratum

import (
	"bufio"
	"context"
	"encoding/hex"
	"net"
	"strings"
	"testing"
	"time"

	"igneum.network/pool/internal/validate"
)

// The bad-share threshold at the pool's edge (DESIGN 2.21, G-33): a session whose refused shares reach the limit
// within the window is closed with the reason on its last reply; its address is refused at authorize while it
// cools and accepted again after.
func TestBadShareThresholdClosesAndCools(t *testing.T) {
	sink := &recordingSink{}
	srv := NewServer(validate.Null{}, sink)
	clock := time.Unix(1_700_000_000, 0)
	srv.now = func() time.Time { return clock }
	srv.BadShareLimit = 3
	srv.BadShareWindow = 60 * time.Second
	srv.BadShareCooling = 300 * time.Second
	open := func() *client {
		a, b := net.Pipe()
		go srv.ServeConn(context.Background(), a)
		t.Cleanup(func() { b.Close() })
		return &client{t: t, conn: b, r: bufio.NewReader(b)}
	}
	c := open()
	c.call(MethodSubscribe, []string{"test/0"})
	if _, e := c.call(MethodAuthorize, []string{addr, "worker1"}); e != nil {
		t.Fatalf("authorize: %+v", e)
	}
	job := Job{ID: "00000000000000b5", Height: 20, Algorithm: validate.NullAlgorithm, ShareTarget: validate.TargetForDifficulty(1 << 20), Boundary: validate.TargetForDifficulty(1 << 30), Clean: true}
	srv.SetJob(job)
	c.readNotify()
	zero := hex.EncodeToString(make([]byte, 32))
	// two refused shares (a nonce outside the session's range, then the same again) stay under the limit
	for i := 0; i < 2; i++ {
		if _, e := c.call(MethodSubmit, []string{job.ID, "7", zero, zero}); e == nil || strings.HasPrefix(e.Reason, ReasonBadShares) {
			t.Fatalf("refusal %d: %+v", i, e)
		}
	}
	// the third reaches the limit: the reply carries the threshold's reason and the session is closed
	_, e := c.call(MethodSubmit, []string{job.ID, "9", zero, zero})
	if e == nil || !strings.HasPrefix(e.Reason, ReasonBadShares) {
		t.Fatalf("at the limit: %+v", e)
	}
	if _, err := ReadFrame(c.r); err == nil {
		t.Fatal("the session was not closed after the threshold reply")
	}
	st := srv.BadShareStats()
	if st["sessions_closed"].(uint64) != 1 || st["addresses_cooling"].(int) != 1 {
		t.Fatalf("stats %+v", st)
	}
	// a new session under the same address is refused at authorize while it cools
	c2 := open()
	c2.call(MethodSubscribe, []string{"test/0"})
	if _, e := c2.call(MethodAuthorize, []string{addr, "worker1"}); e == nil || e.Reason != ReasonCooling {
		t.Fatalf("cooling: %+v", e)
	}
	// another address is not
	other := "0xffeeddccbbaa99887766554433221100ffeeddcc"
	if _, e := c2.call(MethodAuthorize, []string{other, "worker2"}); e != nil {
		t.Fatalf("other address: %+v", e)
	}
	// after the cooling period the address is accepted again
	clock = clock.Add(301 * time.Second)
	c3 := open()
	c3.call(MethodSubscribe, []string{"test/0"})
	if _, e := c3.call(MethodAuthorize, []string{addr, "worker1"}); e != nil {
		t.Fatalf("after cooling: %+v", e)
	}
	// a stale job's refusal does not count toward the threshold (nor the pool's own rate throttle, nor an
	// unauthorised session's: a fast honest miner is never closed for the pool's throttle)
	srv.BadShareLimit = 1
	if _, e := c3.call(MethodSubmit, []string{"00000000000000ff", "1", zero, zero}); e == nil || e.Reason != ReasonStale {
		t.Fatalf("stale: %+v", e)
	}
	if _, e := c3.call(MethodAuthorize, []string{addr, "worker1"}); e != nil {
		t.Fatalf("the session stayed open after a stale refusal: %+v", e)
	}
	c5 := open()
	c5.call(MethodSubscribe, []string{"test/0"})
	if _, e := c5.call(MethodSubmit, []string{job.ID, "1", zero, zero}); e == nil || e.Reason != ReasonUnauthorised {
		t.Fatalf("unauthorised: %+v", e)
	}
	if _, e := c5.call(MethodAuthorize, []string{other, "rig5"}); e != nil {
		t.Fatalf("the session stayed open after an unauthorised refusal: %+v", e)
	}
	// no threshold when the limit is zero
	srv.BadShareLimit = 0
	c4 := open()
	c4.call(MethodSubscribe, []string{"test/0"})
	c4.call(MethodAuthorize, []string{other, "rig3"}) // the current job's notify arrives with the subscribe
	for i := 0; i < 5; i++ {
		if _, e := c4.call(MethodSubmit, []string{job.ID, "11", zero, zero}); e == nil || strings.HasPrefix(e.Reason, ReasonBadShares) {
			t.Fatalf("no threshold: %+v", e)
		}
	}
}
