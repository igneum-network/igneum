package ledger

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"igneum.network/pool/internal/shares"
)

var (
	ctx = context.Background()
	t0  = time.Unix(1_700_000_000, 0)
)

func distribution(t *testing.T, hash string, reward int64, weights ...uint64) *shares.Distribution {
	t.Helper()
	accounts := []string{"0xaaaa", "0xbbbb", "0xcccc"}
	var w []shares.Share
	for i, x := range weights {
		w = append(w, shares.Share{Seq: uint64(i + 1), Account: accounts[i%3], Weight: x})
	}
	d, err := shares.Distribute(shares.Block{Hash: hash, Height: 1, Reward: big.NewInt(reward)}, w, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestCreditMatureDebit(t *testing.T) {
	store := NewMemory()
	l := New(store)
	d := distribution(t, "0xb1", 1_000_000, 1, 1) // a and b get 495000 each; fee 10000
	if err := l.ApplyDistribution(ctx, d, t0); err != nil {
		t.Fatal(err)
	}
	if b := l.Balances("0xaaaa"); b.Immature.Int64() != 495_000 || b.Payable.Sign() != 0 {
		t.Fatalf("after credit: %+v", b)
	}
	if l.BlockState("0xb1") != StateImmature {
		t.Fatalf("state %q", l.BlockState("0xb1"))
	}
	// Applying the same block again is refused and changes nothing.
	if err := l.ApplyDistribution(ctx, d, t0); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate block: got %v", err)
	}
	if b := l.Balances("0xaaaa"); b.Immature.Int64() != 495_000 {
		t.Fatalf("duplicate changed balance: %+v", b)
	}
	if err := l.MatureBlock(ctx, "0xb1", t0); err != nil {
		t.Fatal(err)
	}
	if b := l.Balances("0xaaaa"); b.Immature.Sign() != 0 || b.Payable.Int64() != 495_000 {
		t.Fatalf("after mature: %+v", b)
	}
	if err := l.MatureBlock(ctx, "0xb1", t0); !errors.Is(err, ErrPastMaturity) {
		t.Fatalf("mature twice: got %v", err)
	}
	// A debit above payable is refused; one within it moves to paid.
	if err := l.RecordDebit(ctx, "0xaaaa", big.NewInt(495_001), "intent-1", t0); !errors.Is(err, ErrInsufficient) {
		t.Fatalf("over debit: got %v", err)
	}
	if err := l.RecordDebit(ctx, "0xaaaa", big.NewInt(495_000), "intent-1", t0); err != nil {
		t.Fatal(err)
	}
	if err := l.RecordDebit(ctx, "0xaaaa", big.NewInt(1), "intent-1", t0); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("same intent twice: got %v", err)
	}
	b := l.Balances("0xaaaa")
	if b.Payable.Sign() != 0 || b.Paid.Int64() != 495_000 {
		t.Fatalf("after debit: %+v", b)
	}
	if err := l.ReverseDebit(ctx, "0xaaaa", big.NewInt(495_000), "intent-1", t0); err != nil {
		t.Fatal(err)
	}
	if b := l.Balances("0xaaaa"); b.Payable.Int64() != 495_000 || b.Paid.Sign() != 0 {
		t.Fatalf("after debit reverse: %+v", b)
	}
}

func TestOrphanBeforeMaturity(t *testing.T) {
	l := New(NewMemory())
	d := distribution(t, "0xb2", 1_000_000, 1, 1, 1)
	if err := l.ApplyDistribution(ctx, d, t0); err != nil {
		t.Fatal(err)
	}
	before := l.Liability()
	if before.Sign() == 0 {
		t.Fatal("no liability after credit")
	}
	if err := l.OrphanBlock(ctx, "0xb2", t0); err != nil {
		t.Fatal(err)
	}
	if l.Liability().Sign() != 0 {
		t.Fatalf("liability after orphan: %s", l.Liability())
	}
	if l.BlockState("0xb2") != StateOrphaned {
		t.Fatalf("state %q", l.BlockState("0xb2"))
	}
	if err := l.OrphanBlock(ctx, "0xb2", t0); !errors.Is(err, ErrNotImmature) {
		t.Fatalf("orphan twice: got %v", err)
	}
	if err := l.OrphanBlock(ctx, "0xnone", t0); !errors.Is(err, ErrNotImmature) {
		t.Fatalf("orphan unknown: got %v", err)
	}
}

func TestOrphanAfterMaturityIsRefused(t *testing.T) {
	l := New(NewMemory())
	d := distribution(t, "0xb3", 1_000_000, 1)
	if err := l.ApplyDistribution(ctx, d, t0); err != nil {
		t.Fatal(err)
	}
	if err := l.MatureBlock(ctx, "0xb3", t0); err != nil {
		t.Fatal(err)
	}
	if err := l.OrphanBlock(ctx, "0xb3", t0); !errors.Is(err, ErrPastMaturity) {
		t.Fatalf("got %v", err)
	}
}

func TestRemainderGoesToCarry(t *testing.T) {
	l := New(NewMemory())
	// 7 wei, no fee: 2 each to three accounts, 1 to carry.
	var w []shares.Share
	for i, a := range []string{"0xaaaa", "0xbbbb", "0xcccc"} {
		w = append(w, shares.Share{Seq: uint64(i + 1), Account: a, Weight: 1})
	}
	d, err := shares.Distribute(shares.Block{Hash: "0xb4", Height: 1, Reward: big.NewInt(7)}, w, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.ApplyDistribution(ctx, d, t0); err != nil {
		t.Fatal(err)
	}
	if b := l.Balances(shares.CarryAccount); b.Immature.Int64() != 1 {
		t.Fatalf("carry: %+v", b)
	}
	if l.Liability().Int64() != 7 {
		t.Fatalf("liability %s, want 7", l.Liability())
	}
}

func TestRebuildReplaysJournal(t *testing.T) {
	store := NewMemory()
	l := New(store)
	if err := l.ApplyDistribution(ctx, distribution(t, "0xb5", 1_000_000, 1, 1), t0); err != nil {
		t.Fatal(err)
	}
	if err := l.MatureBlock(ctx, "0xb5", t0); err != nil {
		t.Fatal(err)
	}
	if err := l.RecordDebit(ctx, "0xaaaa", big.NewInt(100), "intent-9", t0); err != nil {
		t.Fatal(err)
	}
	want := l.Balances("0xaaaa")
	liability := l.Liability()

	fresh := New(store)
	if err := fresh.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	got := fresh.Balances("0xaaaa")
	if got.Immature.Cmp(want.Immature) != 0 || got.Payable.Cmp(want.Payable) != 0 || got.Paid.Cmp(want.Paid) != 0 {
		t.Fatalf("rebuilt %+v, want %+v", got, want)
	}
	if fresh.Liability().Cmp(liability) != 0 {
		t.Fatalf("rebuilt liability %s, want %s", fresh.Liability(), liability)
	}
	if fresh.BlockState("0xb5") != StateMature {
		t.Fatalf("rebuilt state %q", fresh.BlockState("0xb5"))
	}
	// The rebuilt ledger refuses the same intent again: replay is idempotent.
	if err := fresh.RecordDebit(ctx, "0xaaaa", big.NewInt(100), "intent-9", t0); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("got %v", err)
	}
	if store.Len() != 5 { // 2 credits, 2 matures, 1 debit
		t.Fatalf("journal rows %d, want 5", store.Len())
	}
}

func TestRefusedBatchLeavesCacheUntouched(t *testing.T) {
	store := NewMemory()
	l := New(store)
	if err := l.ApplyDistribution(ctx, distribution(t, "0xb6", 1_000_000, 1, 1), t0); err != nil {
		t.Fatal(err)
	}
	// A batch whose second entry is a duplicate must not apply its first.
	batch := []Entry{
		{Kind: CreditImmature, Account: "0xdddd", Amount: big.NewInt(5), Ref: "0xb7", At: t0},
		{Kind: CreditImmature, Account: "0xaaaa", Amount: big.NewInt(5), Ref: "0xb6", At: t0},
	}
	if err := l.commit(ctx, batch); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("got %v", err)
	}
	if b := l.Balances("0xdddd"); b.Immature.Sign() != 0 {
		t.Fatalf("partial batch applied: %+v", b)
	}
	if store.Len() != 2 {
		t.Fatalf("journal rows %d, want 2", store.Len())
	}
}
