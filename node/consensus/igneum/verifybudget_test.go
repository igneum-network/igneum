package igneum

import (
	"errors"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// The verifier's global CPU budget (verifybudget.go): the bucket's
// arithmetic on a fake clock, a header deferred (never refused) while the
// bucket is in debt, a share refused by name at the limit and admitted
// after the refill, the reading, and OFF at zero.
func TestVerifyBudgetBucket(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	var slept time.Duration
	b := newVerifyBudget(100) // 100 ms a second
	b.now = func() time.Time { return now }
	b.sleep = func(d time.Duration) { slept += d; now = now.Add(d) }
	b.last = now
	if !b.on() || b.balance != 100*time.Millisecond {
		t.Fatalf("a 100 ms budget starts full: %v", b.balance)
	}
	// Three 40 ms hashes: each reserves the floor (100 us; no mean yet)
	// at admission and reconciles to its measured time; the third puts the
	// bucket in debt.
	for i := 0; i < 3; i++ {
		r, ok := b.takeShare()
		if !ok {
			t.Fatalf("share %d refused with %v left", i, b.balance)
		}
		if i == 0 && r != shareReserveFloor {
			t.Fatalf("the first reservation %v, want the floor", r)
		}
		b.chargeShare(r, 40*time.Millisecond)
	}
	if b.balance != -20*time.Millisecond {
		t.Fatalf("balance after 120 ms charged: %v", b.balance)
	}
	if b.meanShare <= 10*time.Millisecond || b.meanShare > 40*time.Millisecond {
		t.Fatalf("the running mean after three 40 ms shares: %v", b.meanShare)
	}
	// A share at the limit is refused by name, counted; a header is
	// deferred the time the debt takes to refill (20 ms at 100 ms/s =
	// 200 ms), never refused.
	if _, ok := b.takeShare(); ok {
		t.Fatal("a share admitted in debt")
	}
	b.waitHeader()
	if slept != 200*time.Millisecond || b.deferred != 1 || b.refused != 1 {
		t.Fatalf("deferral %v (deferred %d, refused %d), want 200 ms, 1, 1", slept, b.deferred, b.refused)
	}
	// After the wait the bucket is at zero: a share is still refused (the
	// bound is strict); 10 ms later 1 ms is back and it passes, reserving
	// the running mean (above the floor now).
	if _, ok := b.takeShare(); ok {
		t.Fatal("a share admitted at zero")
	}
	now = now.Add(10 * time.Millisecond)
	r, ok := b.takeShare()
	if !ok || r != b.meanShare {
		t.Fatalf("after the refill: ok %v reserved %v mean %v", ok, r, b.meanShare)
	}
	b.chargeShare(r, r) // exact: no reconciliation
	// The bucket caps at one second's budget.
	now = now.Add(time.Hour)
	b.refill()
	if b.balance != 100*time.Millisecond {
		t.Fatalf("the cap: %v", b.balance)
	}
	st := b.state()
	if st.BudgetMs != 100 || st.Deferred != 1 || st.Refused != 2 || st.ChargedMs < 120 || st.Deferring || st.MeanShareUs <= 0 {
		t.Fatalf("state %+v", st)
	}
	// OFF at zero: every share passes, no header waits, nothing counted.
	off := newVerifyBudget(0)
	off.sleep = func(time.Duration) { t.Fatal("an off budget slept") }
	off.charge(time.Hour)
	off.chargeShare(0, time.Hour)
	off.waitHeader()
	if _, ok := off.takeShare(); !ok || off.state().BudgetMs != 0 || off.on() {
		t.Fatal("an off budget refused or counted")
	}
}

// TestVerifyBudgetConcurrentFlood is F-70's row (the security review): a thousand
// shares admitted concurrently at a balance of ONE share's cost admit
// about one, not every share in flight, because each reserves its expected
// cost at admission; the reconciliation after the hashes leaves the bucket
// where the measured time puts it.
func TestVerifyBudgetConcurrentFlood(t *testing.T) {
	now := time.Unix(3_000_000, 0)
	b := newVerifyBudget(1000)
	b.now = func() time.Time { return now }
	b.sleep = func(time.Duration) {}
	b.last = now
	// Teach the mean: shares cost 3.4 ms (the scalar verifier at 2,048).
	const cost = 3400 * time.Microsecond
	for i := 0; i < 16; i++ {
		r, ok := b.takeShare()
		if !ok {
			t.Fatalf("teaching share %d refused", i)
		}
		b.chargeShare(r, cost)
	}
	// Set the balance to one share's cost and a hair.
	b.mu.Lock()
	b.balance = cost + time.Microsecond
	b.mu.Unlock()
	var (
		admitted int64
		wg       sync.WaitGroup
		mu       sync.Mutex
		reserved []time.Duration
	)
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r, ok := b.takeShare(); ok {
				mu.Lock()
				admitted++
				reserved = append(reserved, r)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if admitted < 1 || admitted > 2 {
		t.Fatalf("a concurrent flood at one share's balance admitted %d, want about one", admitted)
	}
	for _, r := range reserved {
		b.chargeShare(r, cost)
	}
	st := b.state()
	if st.Refused != uint64(1000-admitted) || st.BalanceUs > int64(time.Microsecond*2/time.Microsecond)+int64(cost/time.Microsecond) {
		t.Fatalf("after the flood: %+v (admitted %d)", st, admitted)
	}
}

// TestVerifyBudgetShareRefusal: through the engine, a share's work check
// is refused by errShareBudget while the budget is spent and runs no hash,
// and runs after the refill.
func TestVerifyBudgetShareRefusal(t *testing.T) {
	stub := newProgrammedStub(t, 10)
	parts := testParts()
	parts.Program = &ProgramParams{RevealBlocks: 2}
	parts.Sortition = &SortitionParams{Height: 1, EpochBlocks: 100, SeedBlocks: 16, Expected: 1, Delta: 6, MaxRound: 16, SharesPerBlock: 4, MaxShares: 8, MaxPayouts: 256}
	e, err := New(Config{Algorithm: stub, Parts: parts, VerifyBudgetMs: 50})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	c := newForkChain()
	g := genesisHeader()
	c.setCanonical(g)
	canon := chainOf(c, g, 12, 'a')
	for _, h := range canon {
		c.setCanonical(h)
	}
	e.anchor(c)
	now := time.Unix(2_000_000, 0)
	e.budget.now = func() time.Time { return now }
	e.budget.last = now
	d := big.NewInt(1)
	// Spend the budget.
	e.budget.charge(60 * time.Millisecond)
	if err := e.verifyShareWorkOn(canon[10].Hash(), common.HexToHash("0xaa"), 12, 1, common.Hash{}, d, 0, nil, nil); !errors.Is(err, errShareBudget) {
		t.Fatalf("share in debt: %v", err)
	}
	if n := len(stub.anchorsAt(12)); n != 0 {
		t.Fatalf("a refused share ran %d hashes", n)
	}
	// A second later the budget is back and the share runs.
	now = now.Add(time.Second)
	if err := e.verifyShareWorkOn(canon[10].Hash(), common.HexToHash("0xaa"), 12, 1, common.Hash{}, d, 0, nil, nil); err != nil {
		t.Fatal(err)
	}
	if n := len(stub.anchorsAt(12)); n != 1 {
		t.Fatalf("the admitted share ran %d hashes", n)
	}
	if st := e.VerifyBudget(); st.Refused != 1 || st.BudgetMs != 50 {
		t.Fatalf("state %+v", st)
	}
}

// TestVerifyStageOf: the refusal classes by stage, in the order verifyHeader
// asserts them.
func TestVerifyStageOf(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{nil, "ok"},
		{errOlderBlockTime, "height"},
		{errInvalidDifficulty, "height"},
		{errPayoutList, "template"},
		{errBondKeyMissing, "signature"},
		{errBondKeyUnbonded, "eligibility"},
		{errSortitionEarlyTime, "round"},
		{errUnderTimeFloor, "round"},
		{errors.New("igneum sortition: not eligible at the round"), "eligibility"},
		{errors.New("igneum pi: proof missing"), "proof"},
		{errInvalidPoW, "programme"},
		{errProgramAncestor, "programme"},
		{errKeyedReplay, "programme"},
	} {
		if got := stageOf(c.err); got != c.want {
			t.Fatalf("%v: stage %q, want %q", c.err, got, c.want)
		}
	}
}
