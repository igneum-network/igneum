package shares

import (
	"path/filepath"
	"testing"
)

// TestHeldAtSealer pins the per-sealer hold (PA-53's per-address per-sealer bound as the pool models it, the
// VectorWork box soak of 05:0xZ, 13 October 2026): a forwarded line a sealer accepted holds the share for that
// sealer while it is outstanding; a carry or an expiry releases it everywhere.
func TestHeldAtSealer(t *testing.T) {
	j, err := OpenJournal(filepath.Join(t.TempDir(), "shares.journal"))
	if err != nil {
		t.Fatal(err)
	}
	acc := "0xABC0000000000000000000000000000000000ABC"
	var seqs []uint64
	for i := 0; i < 3; i++ {
		seq, err := j.Accepted(Line{Account: acc, Height: 10, Nonce: uint64(i), Weight: 1})
		if err != nil {
			t.Fatal(err)
		}
		seqs = append(seqs, seq)
		if err := j.Forwarded(Line{Seq: seq, Account: acc, Height: 10, Sealer: "http://a/", NodeOK: i < 2}); err != nil {
			t.Fatal(err)
		}
		if err := j.Forwarded(Line{Seq: seq, Account: acc, Height: 10, Sealer: "http://b/", NodeOK: true}); err != nil {
			t.Fatal(err)
		}
	}
	if j.HeldAt(acc, "http://a/") != 2 || j.HeldAt(acc, "http://b/") != 3 || j.HeldAt(acc, "http://c/") != 0 {
		t.Fatalf("held a %d b %d c %d", j.HeldAt(acc, "http://a/"), j.HeldAt(acc, "http://b/"), j.HeldAt(acc, "http://c/"))
	}
	if nc, _, _, _ := j.Settle(acc, "http://b/", map[uint64]uint64{10: 1}, nil, nil); nc != 1 {
		t.Fatalf("carried %d", nc)
	}
	if j.HeldAt(acc, "http://a/")+j.HeldAt(acc, "http://b/") != 1+2 && j.HeldAt(acc, "http://b/") != 2 {
		t.Fatalf("after one carry: a %d b %d", j.HeldAt(acc, "http://a/"), j.HeldAt(acc, "http://b/"))
	}
	if j.HeldAt(acc, "http://b/") != 2 {
		t.Fatalf("the carried share must leave the sealer's hold: b %d", j.HeldAt(acc, "http://b/"))
	}
}
