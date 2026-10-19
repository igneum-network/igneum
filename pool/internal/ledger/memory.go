package ledger

import (
	"context"
	"math/big"
	"sync"
)

// Memory is an in-process Store for tests. It is not a balance ledger:
// it is the same append-only journal shape as PostgreSQL, without the
// durability.
type Memory struct {
	mu      sync.Mutex
	entries []Entry
	keys    map[string]struct{}
}

// NewMemory creates an empty journal.
func NewMemory() *Memory { return &Memory{keys: map[string]struct{}{}} }

// Append implements Store: all or nothing, unique (Kind, Account, Ref).
func (m *Memory) Append(ctx context.Context, entries []Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := map[string]struct{}{}
	for _, e := range entries {
		k := key(e)
		if _, dup := m.keys[k]; dup {
			return ErrDuplicate
		}
		if _, dup := seen[k]; dup {
			return ErrDuplicate
		}
		seen[k] = struct{}{}
	}
	for _, e := range entries {
		e.ID = uint64(len(m.entries) + 1)
		e.Amount = new(big.Int).Set(e.Amount)
		m.entries = append(m.entries, e)
		m.keys[key(e)] = struct{}{}
	}
	return nil
}

// Replay implements Store.
func (m *Memory) Replay(ctx context.Context, fn func(Entry) error) error {
	m.mu.Lock()
	rows := make([]Entry, len(m.entries))
	copy(rows, m.entries)
	m.mu.Unlock()
	for _, e := range rows {
		e.Amount = new(big.Int).Set(e.Amount)
		if err := fn(e); err != nil {
			return err
		}
	}
	return nil
}

// Len is the number of journal rows.
func (m *Memory) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}
