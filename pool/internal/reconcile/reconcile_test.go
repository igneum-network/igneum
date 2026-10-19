package reconcile

import (
	"context"
	"math/big"
	"testing"
	"time"

	"igneum.network/pool/internal/ledger"
	"igneum.network/pool/internal/shares"
)

type fakeChain struct {
	head   uint64
	hashes map[uint64]string
}

func (c *fakeChain) Head(ctx context.Context) (uint64, error) { return c.head, nil }
func (c *fakeChain) HashAt(ctx context.Context, h uint64) (string, error) {
	return c.hashes[h], nil
}

func credit(t *testing.T, l *ledger.Ledger, hash string, height uint64) {
	t.Helper()
	d, err := shares.Distribute(shares.Block{Hash: hash, Height: height, Reward: big.NewInt(1_000)},
		[]shares.Share{{Seq: 1, Account: "0xaaaa", Weight: 1}}, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.ApplyDistribution(context.Background(), d, time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
}

func TestMaturesAtDepthAndOrphansOnMismatch(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	l := ledger.New(ledger.NewMemory())
	chain := &fakeChain{head: 100, hashes: map[uint64]string{90: "0xgood", 95: "0xother"}}
	r := New(chain, l, 10)

	credit(t, l, "0xgood", 90)
	credit(t, l, "0xbad", 95) // the chain has a different block at 95
	r.Track(Found{Hash: "0xgood", Height: 90})
	r.Track(Found{Hash: "0xbad", Height: 95})

	if err := r.Run(ctx, now); err != nil {
		t.Fatal(err)
	}
	if got := l.BlockState("0xgood"); got != ledger.StateMature {
		t.Fatalf("good block at depth 10: %q", got)
	}
	if got := l.BlockState("0xbad"); got != ledger.StateOrphaned {
		t.Fatalf("mismatched block: %q", got)
	}
	if r.Pending() != 0 {
		t.Fatalf("pending %d", r.Pending())
	}
	if b := l.Balances("0xaaaa"); b.Payable.Int64() != 1_000 || b.Immature.Sign() != 0 {
		t.Fatalf("balances %+v", b)
	}
	if len(r.Incidents) != 0 {
		t.Fatalf("incidents %v", r.Incidents)
	}
}

func TestStaysPendingBelowDepth(t *testing.T) {
	ctx := context.Background()
	l := ledger.New(ledger.NewMemory())
	chain := &fakeChain{head: 100, hashes: map[uint64]string{95: "0xnew"}}
	r := New(chain, l, 10)
	credit(t, l, "0xnew", 95)
	r.Track(Found{Hash: "0xnew", Height: 95})
	if err := r.Run(ctx, time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	if r.Pending() != 1 || l.BlockState("0xnew") != ledger.StateImmature {
		t.Fatalf("pending %d state %q", r.Pending(), l.BlockState("0xnew"))
	}
	chain.head = 105
	if err := r.Run(ctx, time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	if r.Pending() != 0 || l.BlockState("0xnew") != ledger.StateMature {
		t.Fatalf("after depth: pending %d state %q", r.Pending(), l.BlockState("0xnew"))
	}
}

func TestOrphanAfterMaturityIsAnIncident(t *testing.T) {
	ctx := context.Background()
	l := ledger.New(ledger.NewMemory())
	chain := &fakeChain{head: 200, hashes: map[uint64]string{90: "0xmine"}}
	r := New(chain, l, 10)
	credit(t, l, "0xmine", 90)
	r.Track(Found{Hash: "0xmine", Height: 90})
	if err := r.Run(ctx, time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	if l.BlockState("0xmine") != ledger.StateMature {
		t.Fatal("expected mature")
	}
	// A deep reorganisation past maturity: the block is tracked again by a
	// restart replay and now mismatches.
	chain.hashes[90] = "0xtheirs"
	r.Track(Found{Hash: "0xmine", Height: 90})
	if err := r.Run(ctx, time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	if len(r.Incidents) != 1 {
		t.Fatalf("incidents %v", r.Incidents)
	}
	if b := l.Balances("0xaaaa"); b.Payable.Int64() != 1_000 {
		t.Fatalf("balance silently changed: %+v", b)
	}
}
