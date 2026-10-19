package service

import (
	"testing"

	"igneum.network/pool/internal/shares"
)

// the pooled form (the project's order of 14:3x UK, 15 October 2026): shares forwarded under the pool key keep the member
// on the journal, and the per-member ledger counts each member's accepted and carried work with the fee's part
func TestMemberLedgerUnderThePoolKey(t *testing.T) {
	j, err := shares.OpenJournal(t.TempDir() + "/shares.journal")
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	key := "0xbd5cb7fb1f4e9bea822d6c81acdb76a999f95fef"
	a, b := "0x2222222222222222222222222222222222222222", "0x3333333333333333333333333333333333333333"
	var seqs []uint64
	for i, m := range []string{a, a, b} {
		seq, err := j.Accepted(shares.Line{Kind: "accepted", Account: key, Member: m, Worker: "w", Height: uint64(100 + i), Nonce: uint64(i), Weight: 1000, FeeBps: 100, FeeWaived: m == a})
		if err != nil {
			t.Fatal(err)
		}
		seqs = append(seqs, seq)
	}
	if err := j.Carried(seqs[:2], "0xabc", 103); err != nil {
		t.Fatal(err)
	}
	rows := j.MemberLedgers(key)
	if len(rows) != 2 {
		t.Fatalf("members: %v", rows)
	}
	ra, rb := rows[0], rows[1]
	if ra.Member != a || ra.Accepted != 2 || ra.AcceptedWork != 2000 || ra.Carried != 2 || ra.CarriedWork != 2000 || ra.FeeWork != 0 || ra.CarriedWaived != 2 {
		t.Fatalf("member a: %+v", ra)
	}
	if rb.Member != b || rb.Accepted != 1 || rb.Carried != 0 || rb.CarriedWork != 0 {
		t.Fatalf("member b: %+v", rb)
	}
	if rows := j.MemberLedgers(a); len(rows) != 0 {
		t.Fatalf("a member's own address has no ledger under it: %v", rows)
	}
}
