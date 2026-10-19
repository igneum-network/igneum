package stratum

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"testing"

	"igneum.network/pool/internal/validate"
)

// TestOneServerServesTwoListeners pins 0.1.67's form (16 October 2026): one stratum server accepting on two listeners is
// one pool: a session on each counts in the same session set, both get the same pool-wide job on SetJob, and the
// subscribe on either names the same session space.
func TestOneServerServesTwoListeners(t *testing.T) {
	sink := &recordingSink{}
	srv := NewServer(validate.Null{}, sink)
	la, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	lb, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx, la)
	go srv.Serve(ctx, lb)
	defer la.Close()
	defer lb.Close()
	dial := func(l net.Listener) *client {
		c, err := net.Dial("tcp", l.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		return &client{t: t, conn: c, r: bufio.NewReader(c)}
	}
	ca, cb := dial(la), dial(lb)
	for i, c := range []*client{ca, cb} {
		res, e := c.call(MethodSubscribe, []string{"test/0"})
		if e != nil {
			t.Fatalf("subscribe %d: %+v", i, e)
		}
		var sub SubscribeResult
		if err := json.Unmarshal(res, &sub); err != nil || sub.Protocol != "IS/1" {
			t.Fatalf("subscribe %d: %s %v", i, res, err)
		}
		if _, e := c.call(MethodAuthorize, []string{addr, "w"}); e != nil {
			t.Fatalf("authorize %d: %+v", i, e)
		}
	}
	if n := srv.MinersConnected(); n != 2 {
		t.Fatalf("one server, two listeners: %d session(s), want 2", n)
	}
	job := Job{ID: "00000000000000d2", Height: 12, Algorithm: validate.NullAlgorithm, ShareTarget: validate.TargetForDifficulty(16), Boundary: validate.TargetForDifficulty(1 << 20), Clean: true}
	job.Header[0] = 0xd2
	srv.SetJob(job)
	for i, c := range []*client{ca, cb} {
		if n := c.readNotify(); n.JobID != job.ID {
			t.Fatalf("listener %d did not get the pool-wide job: %+v", i, n)
		}
	}
}
