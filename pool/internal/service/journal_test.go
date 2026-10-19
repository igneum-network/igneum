package service

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"igneum.network/pool/internal/ledger"
	"igneum.network/pool/internal/shares"
	"igneum.network/pool/internal/stratum"
	"igneum.network/pool/internal/validate"
)

// The pool's share journal across a restart (the project's order of 11 October
// 2026): shares accepted before the restart are journalled; a new pool on
// the same journal replays them, each re-verified, into its window; the
// block found after the restart pays them; the journal marks them carried.
// A journalled share whose recorded hash is not what the recomputation
// gives is dropped by name at replay and never paid.
func TestShareJournalRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "shares.journal")
	node := newFakeNode(true)
	now := time.Unix(1_700_000_000, 0)

	// the pool before the restart: two members' shares accepted and journalled, no block yet
	book1 := ledger.New(ledger.NewMemory())
	svc1 := New(Config{FeeBps: 100, WindowN: 2, ShareDifficulty: 2, Maturity: 3, ShareJournal: path}, node, validate.Null{}, book1)
	svc1.now = func() time.Time { return now }
	if err := svc1.OpenShareJournal(); err != nil {
		t.Fatal(err)
	}
	if err := svc1.pollWork(ctx); err != nil {
		t.Fatal(err)
	}
	job := *svc1.current
	// real shares: the mix and hash the validator gives for each nonce, as a submission records them
	mk := func(account string, nonce uint64) stratum.Share {
		mix, hash, err := validate.Null{}.Verify(job.Algorithm, job.Header, job.Height, nonce)
		if err != nil {
			t.Fatal(err)
		}
		return stratum.Share{Account: account, Worker: "w", JobID: job.ID, Height: job.Height, Nonce: nonce, Mix: mix, Hash: hash, Weight: 2}
	}
	svc1.Share(mk("0xAAAA", 11))
	svc1.Share(mk("0xBBBB", 12))
	if st := svc1.ShareJournalStats(); st == nil || st.Appended != 2 || st.Outstanding != 2 {
		t.Fatalf("journal before the restart: %+v", st)
	}
	svc1.journal.Close()

	// a forged line appended to the file behind the pool's back: its hash is not the recomputation's
	forged := shares.Line{Kind: "accepted", Seq: 3, Algorithm: job.Algorithm, Account: "0xcccc", Worker: "w", JobID: job.ID, Header: hex.EncodeToString(job.Header[:]), Height: job.Height,
		Nonce: 13, Mix: strings.Repeat("00", 32), Hash: strings.Repeat("ff", 32), Weight: 2}
	fb, _ := json.Marshal(forged)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.Write(append(fb, '\n'))
	f.Close()

	// the pool restarted on the same journal: the two real shares replay, the forged one drops by name
	book2 := ledger.New(ledger.NewMemory())
	svc2 := New(Config{FeeBps: 100, WindowN: 2, ShareDifficulty: 2, Maturity: 3, ShareJournal: path}, node, validate.Null{}, book2)
	svc2.now = func() time.Time { return now }
	if err := svc2.OpenShareJournal(); err != nil {
		t.Fatal(err)
	}
	st := svc2.ShareJournalStats()
	if st == nil || st.Replayed != 2 || st.Dropped != 1 || st.Outstanding != 2 {
		t.Fatalf("journal after the restart: %+v", st)
	}
	if svc2.window.Len() != 2 || svc2.window.Total() != 4 {
		t.Fatalf("window after the replay: %d shares, weight %d", svc2.window.Len(), svc2.window.Total())
	}
	for _, sh := range svc2.window.Snapshot() {
		if sh.Journal == 0 || sh.Account == "0xcccc" {
			t.Fatalf("replayed share %+v", sh)
		}
	}

	// the block found after the restart pays the replayed shares
	if err := svc2.pollWork(ctx); err != nil {
		t.Fatal(err)
	}
	job2 := *svc2.current
	nonce, mix, hash := solve(t, job2, job2.Boundary)
	if err := svc2.Block(ctx, job2, nonce, mix, hash); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := svc2.creditFound(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if svc2.Stats.Credited != 1 {
		t.Fatalf("credited %d", svc2.Stats.Credited)
	}
	a, b := book2.Balances("0xaaaa"), book2.Balances("0xbbbb")
	if a.Immature.Int64() != 496 || b.Immature.Int64() != 496 {
		t.Fatalf("the replayed shares were not paid: a=%s b=%s", a.Immature, b.Immature)
	}
	if book2.Balances("0xcccc").Immature.Sign() != 0 {
		t.Fatal("the forged share was paid")
	}
	st = svc2.ShareJournalStats()
	if st.Carried != 2 || st.Outstanding != 0 {
		t.Fatalf("journal after the block: %+v", st)
	}
	// a third pool on the journal finds nothing outstanding: the carried shares never replay again
	svc3 := New(Config{FeeBps: 100, WindowN: 2, ShareDifficulty: 2, Maturity: 3, ShareJournal: path}, node, validate.Null{}, ledger.New(ledger.NewMemory()))
	if err := svc3.OpenShareJournal(); err != nil {
		t.Fatal(err)
	}
	if svc3.window.Len() != 0 || svc3.ShareJournalStats().Outstanding != 0 {
		t.Fatalf("a carried share replayed: window %d, journal %+v", svc3.window.Len(), svc3.ShareJournalStats())
	}
}

// Expiry: an outstanding share older than the recency below the head is
// pruned by name and never replays.
func TestShareJournalExpiry(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "shares.journal")
	node := newFakeNode(true)
	svc := New(Config{FeeBps: 100, WindowN: 2, ShareDifficulty: 2, Maturity: 3, ShareJournal: path, ShareRecency: 2}, node, validate.Null{}, ledger.New(ledger.NewMemory()))
	if err := svc.OpenShareJournal(); err != nil {
		t.Fatal(err)
	}
	if err := svc.pollWork(ctx); err != nil {
		t.Fatal(err)
	}
	job := *svc.current
	mix, hash, _ := validate.Null{}.Verify(job.Algorithm, job.Header, job.Height, 21)
	svc.Share(stratum.Share{Account: "0xAAAA", Worker: "w", JobID: job.ID, Height: job.Height, Nonce: 21, Mix: mix, Hash: hash, Weight: 2})
	// the chain moves four blocks on without the pool's block: the share at height 1 is below head - 2
	node.mu.Lock()
	node.head = 5
	node.mu.Unlock()
	svc.expireJournal(5) // takes the lock for its state alone (since 14 October 2026)
	st := svc.ShareJournalStats()
	if st.Expired != 1 || st.Outstanding != 0 {
		t.Fatalf("expiry: %+v", st)
	}
}
