package shares

import (
	"path/filepath"
	"testing"
)

// TestHandoverCatchUp pins the handover's journal side (the project's order of 21:2x UK, 13 October 2026): a new
// generation opens the journal, the previous one appends shares after that, and the new one's CatchUp folds those
// lines in, replays the outstanding ones into the window and continues the sequence past them.
func TestHandoverCatchUp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shares.journal")
	old, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	acc := "0x00000000000000000000000000000000000ab001"
	if _, err := old.Accepted(Line{Account: acc, Height: 10, Nonce: 1, Weight: 1}); err != nil {
		t.Fatal(err)
	}
	// the new generation opens beside the old (the lock is not taken by the old in this test: the first handover's form)
	next, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Outstanding()) != 1 {
		t.Fatalf("the new generation's index: %d outstanding", len(next.Outstanding()))
	}
	// the old generation appends two more shares and settles one, then exits
	if _, err := old.Accepted(Line{Account: acc, Height: 11, Nonce: 2, Weight: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Accepted(Line{Account: acc, Height: 11, Nonce: 3, Weight: 1}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := old.Settle(acc, "http://node/", map[uint64]uint64{10: 1}, nil, nil); err != nil {
		t.Fatal(err)
	}
	old.f.Close()
	if err := next.Lock(); err != nil {
		t.Fatal(err)
	}
	var added []Line
	lines, replayed, dropped, err := next.CatchUp(0, nil, func(l Line) { added = append(added, l) })
	if err != nil {
		t.Fatal(err)
	}
	if lines != 3 || replayed != 2 || dropped != 0 || len(added) != 2 {
		t.Fatalf("catch up: lines %d replayed %d dropped %d added %d", lines, replayed, dropped, len(added))
	}
	if n := len(next.Outstanding()); n != 2 {
		t.Fatalf("outstanding after the catch-up: %d (the share at 10 was carried by the old generation)", n)
	}
	seq, err := next.Accepted(Line{Account: acc, Height: 12, Nonce: 4, Weight: 1})
	if err != nil {
		t.Fatal(err)
	}
	if seq != 4 {
		t.Fatalf("the sequence must continue past the old generation's: got %d, want 4", seq)
	}
}
