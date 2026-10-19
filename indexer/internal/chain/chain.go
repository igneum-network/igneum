// Package chain walks the canonical chain from a durable cursor, keeps
// one row per block in an append-only file, and rolls back on a parent
// mismatch (a reorganisation) to the common ancestor before continuing.
// It never trusts memory across a restart: the cursor is the file.
package chain

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"

	"igneum.network/indexer/internal/rpc"
)

// Row is one block as the dashboard needs it.
type Row struct {
	Number    uint64   `json:"n"`
	Hash      string   `json:"h"`
	Parent    string   `json:"p"`
	Miner     string   `json:"m"`
	Timestamp uint64   `json:"t"`
	GasUsed   uint64   `json:"g"`
	GasLimit  uint64   `json:"gl"`
	BaseFee   string   `json:"bf"`
	TxCount   int      `json:"tx"`
	TxOK      int      `json:"ok"`
	From      []string `json:"f,omitempty"`
	To        []string `json:"to,omitempty"`
	Removed   bool     `json:"rm,omitempty"`
	// Events: one entry per log of a successful transaction: the emitting
	// contract, the first topic (the event's signature hash) and the index
	// of the transaction in From and To (so the sender is known).
	Events []Event `json:"ev,omitempty"`
}

// Event is one emitted log, kept as address, signature hash and the
// transaction's index in the row.
type Event struct {
	Address string `json:"a"`
	Topic0  string `json:"t"`
	Tx      int    `json:"k"`
}

// Source is the node view the walker needs.
type Source interface {
	Head(ctx context.Context) (uint64, error)
	BlockByNumber(ctx context.Context, n uint64) (*rpc.Block, error)
	Receipts(ctx context.Context, b *rpc.Block) ([]rpc.Receipt, error)
}

// Store is the append-only row file plus the in-memory canonical view
// rebuilt from it. A removal is a row with Removed set, so the file is
// the whole history and the canonical view is derived.
type Store struct {
	mu        sync.Mutex
	path      string
	canonical []Row // index i holds block i (from 0)
	Reorgs    int
	Removed   int
}

// Open loads the file (or starts empty).
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	fh, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	r := bufio.NewReaderSize(fh, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		if len(line) > 0 && line[len(line)-1] == '\n' {
			var row Row
			if jerr := json.Unmarshal(line, &row); jerr != nil {
				return nil, errors.New("chain: corrupt row")
			}
			s.applyRow(row)
		}
		if err != nil {
			break
		}
	}
	return s, nil
}

func (s *Store) applyRow(row Row) {
	if row.Removed {
		if len(s.canonical) > 0 && s.canonical[len(s.canonical)-1].Number == row.Number {
			s.canonical = s.canonical[:len(s.canonical)-1]
			s.Removed++
		}
		return
	}
	if row.Number == uint64(len(s.canonical)) {
		s.canonical = append(s.canonical, row)
	}
}

func (s *Store) append(row Row) error {
	b, err := json.Marshal(row)
	if err != nil {
		return err
	}
	fh, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer fh.Close()
	if _, err := fh.Write(append(b, '\n')); err != nil {
		return err
	}
	if err := fh.Sync(); err != nil {
		return err
	}
	s.applyRow(row)
	return nil
}

// Height is the number of canonical rows (next block to fetch).
func (s *Store) Height() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return uint64(len(s.canonical))
}

// Rows copies the canonical rows.
func (s *Store) Rows() []Row {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Row, len(s.canonical))
	copy(out, s.canonical)
	return out
}

// Tail copies the last n canonical rows.
func (s *Store) Tail(n int) []Row {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n > len(s.canonical) {
		n = len(s.canonical)
	}
	out := make([]Row, n)
	copy(out, s.canonical[len(s.canonical)-n:])
	return out
}

// Step fetches up to limit new blocks. On a parent mismatch it removes
// the last row and returns so the caller re-enters; each removal is a
// reorganisation observed.
func (s *Store) Step(ctx context.Context, src Source, limit int) (added int, err error) {
	head, err := src.Head(ctx)
	if err != nil {
		return 0, err
	}
	for added < limit {
		s.mu.Lock()
		next := uint64(len(s.canonical))
		var prev *Row
		if next > 0 {
			p := s.canonical[next-1]
			prev = &p
		}
		s.mu.Unlock()
		if next > head {
			return added, nil
		}
		b, err := src.BlockByNumber(ctx, next)
		if err != nil {
			return added, err
		}
		if prev != nil && b.ParentHash != prev.Hash {
			s.mu.Lock()
			err := s.append(Row{Number: prev.Number, Hash: prev.Hash, Removed: true})
			s.Reorgs++
			s.mu.Unlock()
			if err != nil {
				return added, err
			}
			continue
		}
		rs, err := src.Receipts(ctx, b)
		if err != nil {
			return added, err
		}
		row := Row{
			Number: b.Number.Uint64(), Hash: b.Hash, Parent: b.ParentHash, Miner: b.Miner,
			Timestamp: b.Timestamp.Uint64(), GasUsed: b.GasUsed.Uint64(), GasLimit: b.GasLimit.Uint64(),
			BaseFee: b.BaseFee.Int.String(), TxCount: len(b.Transactions),
		}
		for _, tx := range b.Transactions {
			row.From = append(row.From, tx.From)
			row.To = append(row.To, tx.To) // empty for a contract creation; kept aligned with From
		}
		for k, r := range rs {
			if r.Status.Uint64() != 1 {
				continue
			}
			row.TxOK++
			for _, l := range r.Logs {
				if len(l.Topics) == 0 {
					continue
				}
				row.Events = append(row.Events, Event{Address: l.Address, Topic0: l.Topics[0], Tx: k})
			}
		}
		s.mu.Lock()
		err = s.append(row)
		s.mu.Unlock()
		if err != nil {
			return added, err
		}
		added++
	}
	return added, nil
}
