package igneum

import (
	"errors"
	"sync"
	"time"
)

// The verifier's GLOBAL CPU budget (the outside review's verifier ruling of 20:17
// UK, 12 October 2026, through main; the security review's TP-250, the multi-peer
// share flood): one bucket of verifier CPU time across every peer and
// every path that executes the relation (a header's seal, a share's work),
// so that no set of peers can make this node spend more than the registered
// share of its time on hashes. The unit is milliseconds of verifier CPU a
// second (--igneum.verify-budget-ms; zero is OFF). The bucket holds one
// second's worth; a hash's wall time is charged after it runs (its cost is
// not known before).
//
// At the limit the two paths differ, by design: a HEADER is DEFERRED (the
// verifier waits for the bucket to refill, bounded by one budget period,
// never refused: refusing an honest block for the verifier's own load is a
// liveness fault, and a peer's bad block is refused on its own merits after
// the wait), while a SHARE is REFUSED by name (errShareBudget: the
// submitter resubmits; a share is cheap to refuse and the flood is the
// case the budget exists for). The refusal counts are served by
// igneum_verifyBudget beside the per-peer admission's.

var errShareBudget = errors.New("igneum shares: the verifier's CPU budget is spent (resubmit)")

type verifyBudget struct {
	mu        sync.Mutex
	perSec    time.Duration // the budget: verifier CPU a second; zero is OFF
	balance   time.Duration // what is left of this second's budget (may go negative: a hash is charged after it runs)
	last      time.Time     // the last refill
	deferred  uint64        // headers that waited
	refused   uint64        // shares refused
	charged   time.Duration // CPU charged in all
	meanShare time.Duration // the running mean of a share's measured time (the reservation at admission)
	now       func() time.Time
	sleep     func(time.Duration)
}

func newVerifyBudget(perSecMs uint64) *verifyBudget {
	b := &verifyBudget{perSec: time.Duration(perSecMs) * time.Millisecond, now: time.Now, sleep: time.Sleep}
	b.balance = b.perSec
	b.last = b.now()
	return b
}

func (b *verifyBudget) on() bool { return b != nil && b.perSec > 0 }

// refill adds the budget earned since the last refill, capped at one
// second's worth (the lock held).
func (b *verifyBudget) refill() {
	now := b.now()
	if elapsed := now.Sub(b.last); elapsed > 0 {
		b.balance += time.Duration(float64(b.perSec) * elapsed.Seconds())
		if b.balance > b.perSec {
			b.balance = b.perSec
		}
		b.last = now
	}
}

// waitHeader defers a header's hash until the bucket is non-negative,
// bounded by one second (a negative balance refills within one period by
// construction); counts the deferral.
func (b *verifyBudget) waitHeader() {
	if !b.on() {
		return
	}
	b.mu.Lock()
	b.refill()
	if b.balance >= 0 {
		b.mu.Unlock()
		return
	}
	// The time until the balance reaches zero at the refill rate.
	wait := time.Duration(float64(-b.balance) / float64(b.perSec) * float64(time.Second))
	if wait > time.Second {
		wait = time.Second
	}
	b.deferred++
	b.mu.Unlock()
	b.sleep(wait)
}

// shareReserveFloor is the least a share's hash is reserved at on admission
// (the security review's F-70: the admit-then-charge race; a share reserves its
// expected cost when admitted, so a concurrent flood at a small positive
// balance admits about one share, not every share in flight).
const shareReserveFloor = 100 * time.Microsecond

// takeShare says whether a share's hash may run and reserves its expected
// cost (the running mean of charged share time, floored) from the bucket
// at admission; chargeShare reconciles the reservation with the measured
// time after the hash. False, counted, when the bucket is spent.
func (b *verifyBudget) takeShare() (reserved time.Duration, ok bool) {
	if !b.on() {
		return 0, true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refill()
	if b.balance <= 0 {
		b.refused++
		return 0, false
	}
	reserved = b.meanShare
	if reserved < shareReserveFloor {
		reserved = shareReserveFloor
	}
	b.balance -= reserved
	return reserved, true
}

// chargeShare reconciles a share's reservation with its measured time and
// folds the time into the running mean (an eighth per sample).
// meanShareTime is the running mean of a share's measured verification
// time (zero before any measurement, or with the budget off): the share
// gossip's fetch pacing reads it (F-227).
func (b *verifyBudget) meanShareTime() time.Duration {
	if !b.on() {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.meanShare
}

func (b *verifyBudget) chargeShare(reserved, actual time.Duration) {
	if !b.on() {
		return
	}
	if actual < 0 {
		actual = 0
	}
	b.mu.Lock()
	b.balance += reserved - actual
	b.charged += actual
	if b.meanShare == 0 {
		b.meanShare = actual
	} else {
		b.meanShare = (b.meanShare*7 + actual) / 8
	}
	b.mu.Unlock()
}

// charge takes a hash's time off the bucket after it ran.
func (b *verifyBudget) charge(d time.Duration) {
	if !b.on() || d <= 0 {
		return
	}
	b.mu.Lock()
	b.balance -= d
	b.charged += d
	b.mu.Unlock()
}

// VerifyBudgetState is igneum_verifyBudget's reading.
type VerifyBudgetState struct {
	BudgetMs    uint64 `json:"budgetMs"`    // the flag's value; zero OFF
	BalanceUs   int64  `json:"balanceUs"`   // what is left of this second's budget (negative: in debt)
	Deferred    uint64 `json:"deferred"`    // headers that waited for the refill
	Refused     uint64 `json:"refused"`     // shares refused by name
	ChargedMs   uint64 `json:"chargedMs"`   // verifier CPU charged in all
	Deferring   bool   `json:"deferring"`   // true while the balance is negative
	MeanShareUs int64  `json:"meanShareUs"` // the running mean of a share's measured time (the reservation at admission)
}

func (b *verifyBudget) state() VerifyBudgetState {
	if b == nil {
		return VerifyBudgetState{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.on() {
		b.refill()
	}
	return VerifyBudgetState{
		BudgetMs: uint64(b.perSec / time.Millisecond), BalanceUs: int64(b.balance / time.Microsecond),
		Deferred: b.deferred, Refused: b.refused, ChargedMs: uint64(b.charged / time.Millisecond), Deferring: b.on() && b.balance < 0,
		MeanShareUs: int64(b.meanShare / time.Microsecond),
	}
}

// SetVerifyBudget sets the verifier's CPU budget in milliseconds a second
// (the flag; zero turns it off).
func (e *Engine) SetVerifyBudget(perSecMs uint64) {
	e.budget = newVerifyBudget(perSecMs)
}

// VerifyBudget is the budget's reading (igneum_verifyBudget).
func (e *Engine) VerifyBudget() VerifyBudgetState { return e.budget.state() }
