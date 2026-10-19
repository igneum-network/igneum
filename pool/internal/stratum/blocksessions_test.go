package stratum

import (
	"strings"
	"testing"
)

// 0.1.79: with block sessions off (the default) a block-named worker is refused at authorize by name; with them on it
// authorises as before; a share-named worker is untouched either way.
func TestBlockSessionsOffRefusesABlockWorkerAtAuthorize(t *testing.T) {
	addr := "0xABCDEF0123456789abcdef0123456789ABCDEF01"
	_, _, c := setup(t)
	if _, e := c.call(MethodSubscribe, []string{"test/0"}); e != nil {
		t.Fatalf("subscribe: %+v", e)
	}
	if _, e := c.call(MethodAuthorize, []string{addr, "own-worker-1.block"}); e == nil || !strings.HasPrefix(e.Reason, ReasonUnauthorised) || !strings.Contains(e.Reason, "block sessions are off") {
		t.Fatalf("a block worker with sessions off: %+v", e)
	}
	if _, e := c.call(MethodAuthorize, []string{addr, "own-worker-1"}); e != nil {
		t.Fatalf("a share worker with sessions off: %+v", e)
	}
	srv, _, c2 := setup(t)
	srv.BlockSessions = true
	if _, e := c2.call(MethodSubscribe, []string{"test/0"}); e != nil {
		t.Fatalf("subscribe: %+v", e)
	}
	if _, e := c2.call(MethodAuthorize, []string{addr, "own-worker-1.block"}); e != nil {
		t.Fatalf("a block worker with sessions on: %+v", e)
	}
}
