package chain

import (
	"context"
	"fmt"
	"math/big"
	"path/filepath"
	"testing"

	"igneum.network/indexer/internal/rpc"
)

// fakeSource serves a chain of hashes that the test can rewrite.
type fakeSource struct {
	hashes []string // index = height
	miners []string
	txs    map[uint64][]rpc.Tx
	calls  int
}

func (f *fakeSource) Head(ctx context.Context) (uint64, error) { return uint64(len(f.hashes) - 1), nil }

func (f *fakeSource) BlockByNumber(ctx context.Context, n uint64) (*rpc.Block, error) {
	f.calls++
	if n >= uint64(len(f.hashes)) {
		return nil, fmt.Errorf("no block %d", n)
	}
	b := &rpc.Block{Hash: f.hashes[n], Miner: f.miners[n], Transactions: f.txs[n]}
	b.Number = rpc.Quantity{Int: big.NewInt(int64(n))}
	b.Timestamp = rpc.Quantity{Int: big.NewInt(int64(1000 + 15*n))}
	b.GasUsed = rpc.Quantity{Int: big.NewInt(0)}
	b.GasLimit = rpc.Quantity{Int: big.NewInt(16_000_000)}
	b.BaseFee = rpc.Quantity{Int: big.NewInt(1_000_000_000)}
	b.Difficulty = rpc.Quantity{Int: big.NewInt(1)}
	if n > 0 {
		b.ParentHash = f.hashes[n-1]
	}
	return b, nil
}

func (f *fakeSource) Receipts(ctx context.Context, b *rpc.Block) ([]rpc.Receipt, error) {
	var out []rpc.Receipt
	for i, tx := range b.Transactions {
		st := int64(1)
		if i%2 == 1 {
			st = 0
		}
		out = append(out, rpc.Receipt{TransactionHash: tx.Hash, Status: rpc.Quantity{Int: big.NewInt(st)}})
	}
	return out, nil
}

func newSource(n int, prefix string) *fakeSource {
	f := &fakeSource{txs: map[uint64][]rpc.Tx{}}
	for i := 0; i < n; i++ {
		f.hashes = append(f.hashes, fmt.Sprintf("0x%s%04d", prefix, i))
		f.miners = append(f.miners, fmt.Sprintf("0xminer%d", i%3))
	}
	return f
}

func TestWalkAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rows.jsonl")
	src := newSource(10, "a")
	src.txs[4] = []rpc.Tx{{Hash: "0xt1", From: "0xA", To: "0xB"}, {Hash: "0xt2", From: "0xC", To: ""}}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	added, err := s.Step(ctx, src, 100)
	if err != nil {
		t.Fatal(err)
	}
	if added != 10 || s.Height() != 10 {
		t.Fatalf("added %d height %d", added, s.Height())
	}
	rows := s.Rows()
	if rows[4].TxCount != 2 || rows[4].TxOK != 1 || len(rows[4].From) != 2 || len(rows[4].To) != 2 || rows[4].To[1] != "" {
		t.Fatalf("row 4 %+v", rows[4])
	}
	// Nothing new: no fetch beyond the head.
	if added, _ := s.Step(ctx, src, 100); added != 0 {
		t.Fatalf("re-step added %d", added)
	}
	// Restart from the file.
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if again.Height() != 10 || again.Rows()[9].Hash != "0xa0009" {
		t.Fatalf("restart height %d", again.Height())
	}
}

func TestReorgRollsBackToCommonAncestor(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rows.jsonl")
	src := newSource(8, "a")
	s, _ := Open(path)
	if _, err := s.Step(ctx, src, 100); err != nil {
		t.Fatal(err)
	}
	// The chain replaces blocks 5, 6, 7 with a longer branch 5..9.
	src.hashes = append(src.hashes[:5], "0xb0005", "0xb0006", "0xb0007", "0xb0008", "0xb0009")
	src.miners = append(src.miners[:5], "0xminerX", "0xminerX", "0xminerX", "0xminerX", "0xminerX")
	for i := 0; i < 20; i++ {
		if _, err := s.Step(ctx, src, 100); err != nil {
			t.Fatal(err)
		}
		if s.Height() == 10 {
			break
		}
	}
	rows := s.Rows()
	if len(rows) != 10 || rows[4].Hash != "0xa0004" || rows[5].Hash != "0xb0005" || rows[9].Hash != "0xb0009" {
		t.Fatalf("after reorg: %d rows, 4=%s 5=%s", len(rows), rows[4].Hash, rows[5].Hash)
	}
	if s.Reorgs != 3 {
		t.Fatalf("reorgs %d, want 3 removals", s.Reorgs)
	}
	// The file replays to the same view.
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if again.Height() != 10 || again.Rows()[5].Hash != "0xb0005" || again.Removed != 3 {
		t.Fatalf("replayed height %d removed %d", again.Height(), again.Removed)
	}
}
