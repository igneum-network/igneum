package ledger

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"os"
	"sync"
	"time"
)

// File is a durable Store over one append-only JSON-lines file, fsynced
// per batch. It carries the same journal shape and uniqueness rule as
// PostgreSQL for the single-process cases (the TP-15 adapter, solo mode)
// where a database server is not wanted.
type File struct {
	mu   sync.Mutex
	path string
	keys map[string]struct{}
	next uint64
}

type fileRow struct {
	ID      uint64    `json:"id"`
	Kind    string    `json:"kind"`
	Account string    `json:"account"`
	Amount  string    `json:"amount"`
	Ref     string    `json:"ref"`
	At      time.Time `json:"at"`
}

// OpenFile opens or creates the journal and loads its keys.
func OpenFile(path string) (*File, error) {
	f := &File{path: path, keys: map[string]struct{}{}}
	err := f.Replay(context.Background(), func(e Entry) error {
		f.keys[key(e)] = struct{}{}
		if e.ID > f.next {
			f.next = e.ID
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return f, nil
}

// Append implements Store: the batch is validated for uniqueness, written
// as whole lines and fsynced before it is acknowledged. A crash before
// the fsync leaves either no line or a torn last line, which Replay
// refuses, so a partial batch never becomes a journal.
func (f *File) Append(ctx context.Context, entries []Entry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	seen := map[string]struct{}{}
	for _, e := range entries {
		if e.Amount == nil || e.Amount.Sign() < 0 {
			return ErrAmount
		}
		k := key(e)
		if _, dup := f.keys[k]; dup {
			return ErrDuplicate
		}
		if _, dup := seen[k]; dup {
			return ErrDuplicate
		}
		seen[k] = struct{}{}
	}
	fh, err := os.OpenFile(f.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer fh.Close()
	var buf []byte
	id := f.next
	for _, e := range entries {
		id++
		line, err := json.Marshal(fileRow{ID: id, Kind: string(e.Kind), Account: e.Account, Amount: e.Amount.String(), Ref: e.Ref, At: e.At.UTC()})
		if err != nil {
			return err
		}
		buf = append(buf, line...)
		buf = append(buf, '\n')
	}
	if _, err := fh.Write(buf); err != nil {
		return err
	}
	if err := fh.Sync(); err != nil {
		return err
	}
	f.next = id
	for k := range seen {
		f.keys[k] = struct{}{}
	}
	return nil
}

// Replay implements Store in file order. A torn last line (no newline)
// is an error, never silently skipped.
func (f *File) Replay(ctx context.Context, fn func(Entry) error) error {
	fh, err := os.Open(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer fh.Close()
	r := bufio.NewReader(fh)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if len(line) > 0 {
			if line[len(line)-1] != '\n' {
				return errors.New("ledger: torn journal line")
			}
			var row fileRow
			if jerr := json.Unmarshal(line, &row); jerr != nil {
				return errors.New("ledger: corrupt journal line")
			}
			amount, ok := new(big.Int).SetString(row.Amount, 10)
			if !ok {
				return errors.New("ledger: corrupt journal amount")
			}
			if ferr := fn(Entry{ID: row.ID, Kind: Kind(row.Kind), Account: row.Account, Amount: amount, Ref: row.Ref, At: row.At}); ferr != nil {
				return ferr
			}
		}
		if err != nil {
			return nil
		}
	}
}
