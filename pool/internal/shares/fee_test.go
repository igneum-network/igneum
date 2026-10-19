package shares

import (
	"path/filepath"
	"testing"
)

// TestFeeRowPerSession pins the ledger's fee row per address (the project's order of 20:4x UK, 13 October 2026, the fund
// record's waiver): a share accepted on a waived session adds nothing to the fee work when paid, a charged session's
// share adds its session fee, and the counts say which applied.
func TestFeeRowPerSession(t *testing.T) {
	j, err := OpenJournal(filepath.Join(t.TempDir(), "shares.journal"))
	if err != nil {
		t.Fatal(err)
	}
	acc := "0x00000000000000000000000000000000000fee01"
	if _, err := j.Accepted(Line{Account: acc, Height: 100, Nonce: 1, Weight: 512, FeeBps: 0, FeeWaived: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Accepted(Line{Account: acc, Height: 100, Nonce: 2, Weight: 512, FeeBps: 100}); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Accepted(Line{Account: acc, Height: 100, Nonce: 3, Weight: 512}); err != nil { // the older form, no fee fields
		t.Fatal(err)
	}
	r := j.Fees(acc)
	if r.AcceptedWaived != 1 || r.AcceptedCharged != 1 || r.AcceptedUnknown != 1 {
		t.Fatalf("accepted by fee: %+v", r)
	}
	if _, _, _, err := j.Settle(acc, "http://node/", map[uint64]uint64{100: 3}, nil, nil); err != nil {
		t.Fatal(err)
	}
	r = j.Fees(acc)
	if r.PaidWaived != 1 || r.PaidCharged != 1 || r.PaidUnknown != 1 || r.FeeWork != 512*100/10000 {
		t.Fatalf("paid by fee: %+v (fee work want %d)", r, 512*100/10000)
	}
	if all := j.Fees(""); all.FeeWork != r.FeeWork || all.PaidWaived != 1 {
		t.Fatalf("pool-wide row: %+v", all)
	}
}
