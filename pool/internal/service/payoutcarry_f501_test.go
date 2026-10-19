//go:build igneumhash

package service

import "testing"

// F-501 (0.1.88): the carry restored from a record is the member's last row's state, never a sum over the rows; the
// record of 17 October 2026 (a failed send every window, each due holding the carry-in) restores to the last due.
func TestCarryFromRecordIsTheLastRowNotASum(t *testing.T) {
	m := "0xaaaa"
	windows := []PayoutWindow{
		{From: 3756, To: 3819, Rows: []PayoutRow{{Member: m, DueWei: "85898930000000000000000", Status: "failed"}}},
		{From: 3820, To: 3883, Rows: []PayoutRow{{Member: m, DueWei: "86273154000000000000000", Status: "failed"}}},
		{From: 3884, To: 3947, Rows: []PayoutRow{{Member: m, DueWei: "86562432000000000000000", Status: "failed"}}},
	}
	c := carryFromRecord(windows)
	if c[m] == nil || c[m].String() != "86562432000000000000000" {
		t.Fatalf("the carry must be the last failed row's due, got %v", c[m])
	}
	// a paid row clears it; a later carried row sets it again
	windows = append(windows, PayoutWindow{From: 3948, To: 4011, Rows: []PayoutRow{{Member: m, DueWei: "90000000000000000000000", Status: "paid"}}})
	if c := carryFromRecord(windows); c[m] != nil {
		t.Fatalf("a paid row clears the carry, got %v", c[m])
	}
	windows = append(windows, PayoutWindow{From: 4012, To: 4075, Rows: []PayoutRow{{Member: m, DueWei: "12", Status: "carried"}}})
	if c := carryFromRecord(windows); c[m] == nil || c[m].Int64() != 12 {
		t.Fatalf("a carried row sets the carry to its due, got %v", c[m])
	}
	// the old sum over the three failed rows would have been 258,734,516: the compounding this version removes
	if c := carryFromRecord(windows[:3]); c[m].String() == "258734516000000000000000" {
		t.Fatalf("the restore must never sum the rows")
	}
}
