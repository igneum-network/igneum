package shares

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The share-lane form's settlement (the settlement team's finding of 12 October 2026: the pool's recency expiry
// named paid shares as expired): the journal's carried and expired lines come from the node's account by share
// height, a share the node never names stays outstanding until twice the recency, and an expired line the node's
// account contradicts is corrected by a reconciled line, never rewritten.
func TestSettleFromNodeAccount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shares.journal")
	j, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	acc := "0x00000000000000000000000000000000000c7c31"
	for i := 0; i < 5; i++ {
		h := uint64(100)
		if i >= 3 {
			h = 101
		}
		if _, err := j.Accepted(Line{Account: acc, Height: h, Nonce: uint64(i), Weight: 1}); err != nil {
			t.Fatal(err)
		}
	}
	// the node carried two of the three at 100 and one of the two at 101 expired by reorg
	nc, nr, nw, err := j.Settle(acc, "http://node/", map[uint64]uint64{100: 2}, map[uint64]uint64{101: 1}, nil)
	if err != nil || nc != 2 || nr != 1 || nw != 0 {
		t.Fatalf("settle: %d %d %d %v", nc, nr, nw, err)
	}
	if n := len(j.Outstanding()); n != 2 {
		t.Fatalf("outstanding after the settlement: %d", n)
	}
	// the same read again writes nothing more; a rise of one at 100 writes one more
	nc, nr, nw, _ = j.Settle(acc, "http://node/", map[uint64]uint64{100: 2}, map[uint64]uint64{101: 1}, nil)
	if nc+nr+nw != 0 {
		t.Fatalf("a repeated read wrote %d %d %d", nc, nr, nw)
	}
	// (the node's maps are passed whole at every read: an absent height is a count of zero, and a fall mirrors
	// back, TestSettleMovesBack; so the reorg count at 101 stays in this read)
	nc, _, _, _ = j.Settle(acc, "http://node/", map[uint64]uint64{100: 3}, map[uint64]uint64{101: 1}, nil)
	if nc != 1 || len(j.Outstanding()) != 1 {
		t.Fatalf("the rise: carried %d, outstanding %d", nc, len(j.Outstanding()))
	}
	// the one the node never names expires only below twice the recency
	if n, _ := j.ExpireUnnamed(101, "http://node/"); n != 0 {
		t.Fatalf("expired above the bound: %d", n)
	}
	if n, _ := j.ExpireUnnamed(102, "http://node/"); n != 1 || len(j.Outstanding()) != 0 {
		t.Fatalf("the unnamed share: %d, outstanding %d", n, len(j.Outstanding()))
	}
	accounts, err := j.Accounts()
	if err != nil || len(accounts) != 1 || accounts[0] != acc {
		t.Fatalf("accounts %v %v", accounts, err)
	}
	// a replayed journal that expired a paid share by the recency: the node's account carries it, the reconciled
	// line names it and the file is appended, never rewritten
	j2, _ := OpenJournal(filepath.Join(t.TempDir(), "old.journal"))
	seq, _ := j2.Accepted(Line{Account: acc, Height: 50, Nonce: 9, Weight: 1})
	if n, _ := j2.Expire(60); n != 1 {
		t.Fatalf("the old expiry: %d", n)
	}
	n, err := j2.Reconcile(acc, "http://node/", map[uint64]uint64{50: 1})
	if err != nil || n != 1 {
		t.Fatalf("reconcile: %d %v", n, err)
	}
	if n, _ := j2.Reconcile(acc, "http://node/", map[uint64]uint64{50: 1}); n != 0 {
		t.Fatalf("a second reconcile wrote %d", n)
	}
	f, _ := os.Open(j2.path)
	defer f.Close()
	kinds := []string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var l Line
		json.Unmarshal(sc.Bytes(), &l)
		kinds = append(kinds, l.Kind)
		if l.Kind == "reconciled" && l.Seq != seq {
			t.Fatalf("reconciled the wrong share: %+v", l)
		}
	}
	if len(kinds) != 3 || kinds[0] != "accepted" || kinds[1] != "expired" || kinds[2] != "reconciled" {
		t.Fatalf("the file's lines: %v", kinds)
	}
}

// countingVerifier counts the prepares a replay makes: a line of another chain must never reach it (the public
// pool's devnet8 to devnet9 switch anchored the validator at devnet8's heights through the replay).
type countingVerifier struct{ prepared int }

func (c *countingVerifier) Prepare(algorithm string, params []byte, digest string, height uint64) error {
	c.prepared++
	return nil
}
func (c *countingVerifier) Verify(algorithm string, header [32]byte, height uint64, nonce uint64) ([32]byte, [32]byte, error) {
	return [32]byte{}, [32]byte{}, nil
}

func TestReplaySkipsAnotherChain(t *testing.T) {
	j, err := OpenJournal(filepath.Join(t.TempDir(), "shares.journal"))
	if err != nil {
		t.Fatal(err)
	}
	acc := "0x00000000000000000000000000000000000c7c31"
	z := make([]byte, 32)
	hz := ""
	for range z {
		hz += "00"
	}
	j.Accepted(Line{ChainID: 7318, Account: acc, Height: 300, Nonce: 1, Weight: 1, Header: hz, Mix: hz, Hash: hz})
	j.Accepted(Line{ChainID: 7319, Account: acc, Height: 100, Nonce: 2, Weight: 1, Header: hz, Mix: hz, Hash: hz})
	j.Accepted(Line{Account: acc, Height: 101, Nonce: 3, Weight: 1, Header: hz, Mix: hz, Hash: hz}) // the older form, no chain id
	v := &countingVerifier{}
	added := 0
	replayed, dropped, err := j.ReplayFor(7319, v, func(Line) { added++ })
	if err != nil || replayed != 2 || dropped != 1 || added != 2 || v.prepared != 2 {
		t.Fatalf("replay for 7319: replayed %d dropped %d added %d prepared %d err %v", replayed, dropped, added, v.prepared, err)
	}
	if n := len(j.Outstanding()); n != 2 {
		t.Fatalf("outstanding after the replay: %d", n)
	}
	for _, l := range j.Outstanding() {
		if l.ChainID == 7318 {
			t.Fatalf("another chain's share stayed outstanding: %+v", l)
		}
	}
}

// TestSettleMovesBack pins the node team's finding of 07:5xZ, 12 October 2026:
// a node (0.9.4) puts carried shares back at a reorg and re-carries expired
// ones, so a FALL of the account's count is mirrored by an uncarried or
// unexpired line per share, the share outstanding again and settled anew by
// the next rise; the index survives a reopen.
// TestAccountView pins the per-address page's journal view (the E1 study's finding 5, 12 October 2026): accepted
// shares by height, those the chain carried, those still outstanding, those the pool expired by name, and the
// accounts list from the index rather than a scan of the file.
func TestAccountView(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shares.journal")
	j, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	acc := "0xABC0000000000000000000000000000000000ABC"
	for i := 0; i < 3; i++ {
		if _, err := j.Accepted(Line{Account: acc, Height: 10, Nonce: uint64(i), Weight: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := j.Accepted(Line{Account: acc, Height: 12, Nonce: 9, Weight: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Accepted(Line{Account: "0xdef0000000000000000000000000000000000def", Height: 12, Nonce: 1, Weight: 1}); err != nil {
		t.Fatal(err)
	}
	if nc, _, _, _ := j.Settle(acc, "http://node/", map[uint64]uint64{10: 2}, nil, nil); nc != 2 {
		t.Fatalf("carried %d, want 2", nc)
	}
	rows := j.Account(acc, 0)
	if len(rows) != 2 || rows[0].Height != 10 || rows[1].Height != 12 {
		t.Fatalf("rows %+v", rows)
	}
	if r := rows[0]; r.Accepted != 3 || r.Carried != 2 || r.Outstanding != 1 || r.ExpiredByName != 0 {
		t.Fatalf("height 10: %+v", r)
	}
	if r := rows[1]; r.Accepted != 1 || r.Outstanding != 1 {
		t.Fatalf("height 12: %+v", r)
	}
	if got := j.Account(acc, 11); len(got) != 1 || got[0].Height != 12 {
		t.Fatalf("from height 11: %+v", got)
	}
	accounts, err := j.Accounts()
	if err != nil || len(accounts) != 2 || accounts[0] != strings.ToLower(acc) {
		t.Fatalf("accounts %v err %v", accounts, err)
	}
	// re-opened, the index is rebuilt from the file
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j2, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if rows := j2.Account(acc, 0); len(rows) != 2 || rows[0].Carried != 2 || rows[0].Outstanding != 1 {
		t.Fatalf("after re-open: %+v", rows)
	}
}

func TestSettleMovesBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shares.journal")
	j, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	acc := "0xabc0000000000000000000000000000000000abc"
	for i := 0; i < 3; i++ {
		if _, err := j.Accepted(Line{Account: acc, Height: 10, Nonce: uint64(i), Weight: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if nc, _, _, _ := j.Settle(acc, "http://node/", map[uint64]uint64{10: 3}, nil, nil); nc != 3 {
		t.Fatalf("carried %d, want 3", nc)
	}
	// the reorg: the node now carries one and counts two expired by reorg at the height
	nc, nr, _, err := j.Settle(acc, "http://node/", map[uint64]uint64{10: 1}, map[uint64]uint64{10: 2}, nil)
	if err != nil || nc != 0 || nr != 2 {
		t.Fatalf("after the reorg: carried %d expired %d err %v, want 0 2", nc, nr, err)
	}
	if j.Stats.MovedBack != 2 || len(j.outstanding) != 0 {
		t.Fatalf("moved back %d outstanding %d, want 2 0", j.Stats.MovedBack, len(j.outstanding))
	}
	// the re-carry: the node carries all three again
	nc, nr, _, _ = j.Settle(acc, "http://node/", map[uint64]uint64{10: 3}, map[uint64]uint64{10: 0}, nil)
	if nc != 2 || nr != 0 || j.Stats.MovedBack != 4 {
		t.Fatalf("after the re-carry: carried %d expired %d moved back %d, want 2 0 4", nc, nr, j.Stats.MovedBack)
	}
	// the same read again writes nothing
	if nc, nr, _, _ = j.Settle(acc, "http://node/", map[uint64]uint64{10: 3}, map[uint64]uint64{10: 0}, nil); nc != 0 || nr != 0 {
		t.Fatalf("a repeated read wrote carried %d expired %d", nc, nr)
	}
	// a height the node no longer speaks of (pruned past its recency) is not a fall
	if nc, nr, _, _ = j.Settle(acc, "http://node/", map[uint64]uint64{}, map[uint64]uint64{}, nil); nc != 0 || nr != 0 || j.Stats.MovedBack != 4 {
		t.Fatalf("a pruned height moved shares back: carried %d expired %d moved back %d", nc, nr, j.Stats.MovedBack)
	}
	j.Close()
	// reopened: the index is rebuilt from the lines, so the node's counts write nothing new and a fall still mirrors
	j2, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if nc, nr, _, _ := j2.Settle(acc, "http://node/", map[uint64]uint64{10: 3}, nil, nil); nc != 0 || nr != 0 {
		t.Fatalf("after reopen a repeated read wrote carried %d expired %d", nc, nr)
	}
	if nc, nr, _, _ := j2.Settle(acc, "http://node/", map[uint64]uint64{10: 2}, map[uint64]uint64{10: 1}, nil); nc != 0 || nr != 1 || j2.Stats.MovedBack != 1 {
		t.Fatalf("after reopen a fall: carried %d expired %d moved back %d, want 0 1 1", nc, nr, j2.Stats.MovedBack)
	}
	kinds := map[string]int{}
	f, _ := os.Open(path)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var l Line
		if json.Unmarshal(sc.Bytes(), &l) == nil {
			kinds[l.Kind]++
		}
	}
	f.Close()
	if kinds["uncarried"] != 3 || kinds["unexpired"] != 2 || kinds["carried"] != 5 || kinds["expired"] != 3 {
		t.Fatalf("kinds %v, want uncarried 3 unexpired 2 carried 5 expired 3", kinds)
	}
}
