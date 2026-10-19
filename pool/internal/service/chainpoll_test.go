package service

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// chainNode answers eth_chainId with whatever chain it is set to, and nothing else.
type chainNode struct {
	Node
	chain uint64
	calls int
}

func (n *chainNode) Call(_ context.Context, method string, _ interface{}, out interface{}) error {
	if method != "eth_chainId" {
		return fmt.Errorf("unexpected %s", method)
	}
	n.calls++
	*(out.(*string)) = fmt.Sprintf("0x%x", n.chain)
	return nil
}

// TestChainStillOfRecordRefusesThePollOnAnotherChain pins 0.1.61's one order (the security review's residual on 0.1.60,
// 15 October 2026): the source's chain id is re-read every ChainCheckEvery; while it differs from the bound chain
// the poll is refused and no job issued; once it answers the chain of record again the lane resumes; between
// reads the last verdict stands, so the source is not asked every poll.
func TestChainStillOfRecordRefusesThePollOnAnotherChain(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	now := start
	s := &Service{chainID: 7320}
	s.now = func() time.Time { return now }
	n := &chainNode{chain: 7320}
	if err := s.chainStillOfRecord(context.Background(), n); err != nil {
		t.Fatalf("the chain of record refused: %v", err)
	}
	if n.calls != 1 {
		t.Fatalf("the first poll reads the chain once: %d", n.calls)
	}
	// inside ChainCheckEvery: no read, the verdict stands
	now = start.Add(ChainCheckEvery / 2)
	if err := s.chainStillOfRecord(context.Background(), n); err != nil || n.calls != 1 {
		t.Fatalf("a poll inside the check interval re-read the chain (%d) or refused (%v)", n.calls, err)
	}
	// the source moves to 7322 (a sealer re-pointed under a standing generation): the next read refuses
	n.chain = 7322
	now = start.Add(ChainCheckEvery + time.Second)
	err := s.chainStillOfRecord(context.Background(), n)
	if err == nil || n.calls != 2 {
		t.Fatalf("a source on 7322 against 7320 was not refused (calls %d, err %v)", n.calls, err)
	}
	for _, want := range []string{"chain 7322", "chain 7320", "no job issued"} {
		if !contains(err.Error(), want) {
			t.Fatalf("the refusal must name %q: %v", want, err)
		}
	}
	// and every poll until the next read, without asking again
	now = now.Add(time.Second)
	if err := s.chainStillOfRecord(context.Background(), n); err == nil || n.calls != 2 {
		t.Fatalf("the mismatch must stand between reads (calls %d, err %v)", n.calls, err)
	}
	// the source back on 7320: the next read resumes the lane
	n.chain = 7320
	now = start.Add(2*ChainCheckEvery + 2*time.Second)
	if err := s.chainStillOfRecord(context.Background(), n); err != nil || n.calls != 3 {
		t.Fatalf("the lane did not resume on the chain of record (calls %d, err %v)", n.calls, err)
	}
	// a chain id of 0 is never compared
	z := &Service{}
	z.now = func() time.Time { return now }
	if err := z.chainStillOfRecord(context.Background(), &chainNode{chain: 7322}); err != nil {
		t.Fatalf("a pool with no bound chain refused: %v", err)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
