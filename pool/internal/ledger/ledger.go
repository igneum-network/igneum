// Package ledger holds the pool's durable liabilities
// (docs/ecosystem/DESIGN.md, section 2.8): an append-only journal in a
// Store (PostgreSQL in production, memory in tests) and balances derived
// from it. The cache is rebuilt by replay and is never authoritative.
package ledger

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"sync"
	"time"

	"igneum.network/pool/internal/shares"
)

// Kind is a journal entry kind.
type Kind string

const (
	CreditImmature Kind = "credit_immature" // block found, not yet mature
	Mature         Kind = "mature"          // immature credit became payable
	ReverseCredit  Kind = "reverse_credit"  // block orphaned before maturity
	Debit          Kind = "debit"           // a signed payment intent
	DebitReverse   Kind = "debit_reverse"   // an intent reverted
)

// Block states as the ledger tracks them.
const (
	StateUnknown  = ""
	StateImmature = "immature"
	StateMature   = "mature"
	StateOrphaned = "orphaned"
)

// Entry is one journal row. Ref is a block hash for credits and an intent
// id for debits; (Kind, Account, Ref) is unique.
type Entry struct {
	ID      uint64
	Kind    Kind
	Account string
	Amount  *big.Int
	Ref     string
	At      time.Time
}

var (
	ErrDuplicate    = errors.New("ledger: entry already applied")
	ErrInsufficient = errors.New("ledger: debit exceeds payable")
	ErrAmount       = errors.New("ledger: amount missing or negative")
	ErrNotImmature  = errors.New("ledger: block has no immature credits")
	ErrPastMaturity = errors.New("ledger: block already matured; an orphan after maturity is an operator incident")
)

// Store is the durable journal. Append is atomic for the whole batch and
// enforces (Kind, Account, Ref) uniqueness; Replay yields rows in id order.
type Store interface {
	Append(ctx context.Context, entries []Entry) error
	Replay(ctx context.Context, fn func(Entry) error) error
}

// Balances is one account's derived position.
type Balances struct {
	Immature *big.Int
	Payable  *big.Int
	Paid     *big.Int
}

func newBalances() *Balances {
	return &Balances{Immature: new(big.Int), Payable: new(big.Int), Paid: new(big.Int)}
}

func (b *Balances) clone() *Balances {
	return &Balances{
		Immature: new(big.Int).Set(b.Immature),
		Payable:  new(big.Int).Set(b.Payable),
		Paid:     new(big.Int).Set(b.Paid),
	}
}

// Ledger is the journal plus its derived cache.
type Ledger struct {
	store Store
	mu    sync.Mutex
	bal   map[string]*Balances
	keys  map[string]struct{}
	refs  map[string][]Entry // block hash -> its credit entries
	state map[string]string  // block hash -> state
}

// New creates a ledger over a store. Call Rebuild to load the journal.
func New(store Store) *Ledger {
	return &Ledger{
		store: store,
		bal:   map[string]*Balances{},
		keys:  map[string]struct{}{},
		refs:  map[string][]Entry{},
		state: map[string]string{},
	}
}

func key(e Entry) string { return string(e.Kind) + "|" + e.Account + "|" + e.Ref }

func (l *Ledger) balances(acc string) *Balances {
	b, ok := l.bal[acc]
	if !ok {
		b = newBalances()
		l.bal[acc] = b
	}
	return b
}

// applyTo is the only place balance arithmetic lives.
func applyTo(b *Balances, e Entry) error {
	if e.Amount == nil || e.Amount.Sign() < 0 {
		return ErrAmount
	}
	switch e.Kind {
	case CreditImmature:
		b.Immature.Add(b.Immature, e.Amount)
	case Mature:
		if b.Immature.Cmp(e.Amount) < 0 {
			return fmt.Errorf("ledger: mature %s exceeds immature for %s", e.Amount, e.Account)
		}
		b.Immature.Sub(b.Immature, e.Amount)
		b.Payable.Add(b.Payable, e.Amount)
	case ReverseCredit:
		if b.Immature.Cmp(e.Amount) < 0 {
			return fmt.Errorf("ledger: reverse %s exceeds immature for %s", e.Amount, e.Account)
		}
		b.Immature.Sub(b.Immature, e.Amount)
	case Debit:
		if b.Payable.Cmp(e.Amount) < 0 {
			return ErrInsufficient
		}
		b.Payable.Sub(b.Payable, e.Amount)
		b.Paid.Add(b.Paid, e.Amount)
	case DebitReverse:
		if b.Paid.Cmp(e.Amount) < 0 {
			return fmt.Errorf("ledger: debit reverse %s exceeds paid for %s", e.Amount, e.Account)
		}
		b.Paid.Sub(b.Paid, e.Amount)
		b.Payable.Add(b.Payable, e.Amount)
	default:
		return fmt.Errorf("ledger: unknown kind %q", e.Kind)
	}
	return nil
}

// record applies a validated entry to the cache.
func (l *Ledger) record(e Entry) error {
	if err := applyTo(l.balances(e.Account), e); err != nil {
		return err
	}
	l.keys[key(e)] = struct{}{}
	switch e.Kind {
	case CreditImmature:
		l.refs[e.Ref] = append(l.refs[e.Ref], e)
		l.state[e.Ref] = StateImmature
	case Mature:
		l.state[e.Ref] = StateMature
	case ReverseCredit:
		l.state[e.Ref] = StateOrphaned
	}
	return nil
}

// commit validates a batch on copies, appends it to the store, then
// applies it to the cache. A refused batch leaves the cache untouched.
func (l *Ledger) commit(ctx context.Context, entries []Entry) error {
	if len(entries) == 0 {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	tmp := map[string]*Balances{}
	tmpKeys := map[string]struct{}{}
	for _, e := range entries {
		k := key(e)
		if _, dup := l.keys[k]; dup {
			return ErrDuplicate
		}
		if _, dup := tmpKeys[k]; dup {
			return ErrDuplicate
		}
		tmpKeys[k] = struct{}{}
		b, ok := tmp[e.Account]
		if !ok {
			b = l.balances(e.Account).clone()
			tmp[e.Account] = b
		}
		if err := applyTo(b, e); err != nil {
			return err
		}
	}
	if err := l.store.Append(ctx, entries); err != nil {
		return err
	}
	for _, e := range entries {
		if err := l.record(e); err != nil {
			// Validated above; a failure here is a programming error.
			panic("ledger: record after validation failed: " + err.Error())
		}
	}
	return nil
}

// Rebuild clears the cache and replays the store.
func (l *Ledger) Rebuild(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.bal = map[string]*Balances{}
	l.keys = map[string]struct{}{}
	l.refs = map[string][]Entry{}
	l.state = map[string]string{}
	return l.store.Replay(ctx, func(e Entry) error {
		if _, dup := l.keys[key(e)]; dup {
			return fmt.Errorf("%w: journal row %d", ErrDuplicate, e.ID)
		}
		return l.record(e)
	})
}

// ApplyDistribution credits a found block's split as immature, including
// the carry account's remainder. Accounts are credited in sorted order.
func (l *Ledger) ApplyDistribution(ctx context.Context, d *shares.Distribution, at time.Time) error {
	accounts := make([]string, 0, len(d.Credits))
	for a := range d.Credits {
		accounts = append(accounts, a)
	}
	sort.Strings(accounts)
	entries := make([]Entry, 0, len(accounts)+1)
	for _, a := range accounts {
		if d.Credits[a].Sign() <= 0 {
			continue
		}
		entries = append(entries, Entry{Kind: CreditImmature, Account: a, Amount: new(big.Int).Set(d.Credits[a]), Ref: d.BlockHash, At: at})
	}
	if d.Remainder != nil && d.Remainder.Sign() > 0 {
		entries = append(entries, Entry{Kind: CreditImmature, Account: shares.CarryAccount, Amount: new(big.Int).Set(d.Remainder), Ref: d.BlockHash, At: at})
	}
	return l.commit(ctx, entries)
}

// MatureBlock moves a block's immature credits to payable.
func (l *Ledger) MatureBlock(ctx context.Context, blockHash string, at time.Time) error {
	entries, err := l.blockEntries(blockHash, Mature, at)
	if err != nil {
		return err
	}
	return l.commit(ctx, entries)
}

// OrphanBlock reverses a block's immature credits. A block that already
// matured is refused: that is an operator incident, not a silent netting.
func (l *Ledger) OrphanBlock(ctx context.Context, blockHash string, at time.Time) error {
	entries, err := l.blockEntries(blockHash, ReverseCredit, at)
	if err != nil {
		return err
	}
	return l.commit(ctx, entries)
}

func (l *Ledger) blockEntries(blockHash string, kind Kind, at time.Time) ([]Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch l.state[blockHash] {
	case StateImmature:
	case StateMature:
		return nil, ErrPastMaturity
	default:
		return nil, ErrNotImmature
	}
	credits := l.refs[blockHash]
	entries := make([]Entry, 0, len(credits))
	for _, c := range credits {
		entries = append(entries, Entry{Kind: kind, Account: c.Account, Amount: new(big.Int).Set(c.Amount), Ref: blockHash, At: at})
	}
	return entries, nil
}

// RecordDebit records a signed payment intent against payable.
func (l *Ledger) RecordDebit(ctx context.Context, account string, amount *big.Int, intentID string, at time.Time) error {
	return l.commit(ctx, []Entry{{Kind: Debit, Account: account, Amount: amount, Ref: intentID, At: at}})
}

// ReverseDebit returns a reverted intent's amount to payable.
func (l *Ledger) ReverseDebit(ctx context.Context, account string, amount *big.Int, intentID string, at time.Time) error {
	return l.commit(ctx, []Entry{{Kind: DebitReverse, Account: account, Amount: amount, Ref: intentID, At: at}})
}

// Balances returns a copy of an account's position.
func (l *Ledger) Balances(account string) Balances {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.bal[account]
	if !ok {
		return *newBalances()
	}
	return *b.clone()
}

// BlockState reports what the ledger knows about a block hash.
func (l *Ledger) BlockState(blockHash string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state[blockHash]
}

// Liability is the sum of immature and payable across all accounts: what
// the pool owes.
func (l *Ledger) Liability() *big.Int {
	l.mu.Lock()
	defer l.mu.Unlock()
	total := new(big.Int)
	for _, b := range l.bal {
		total.Add(total, b.Immature)
		total.Add(total, b.Payable)
	}
	return total
}
