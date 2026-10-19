//go:build igneumhash

package service

import (
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"igneum.network/pool/internal/shares"
)

// F-510 (0.1.92): a handover with a close in flight. The old generation holds the journal's lock while its close
// finishes (the sends made, the record written); the new generation reads the record only once the lock is its, so
// it reads the window as closed and never closes it again. The fixture replays it on one journal file with two
// generations' handles: the record is empty while the old holds the lock, and holds the window when the new reads.
func TestAHandoverWithACloseInFlightReadsTheRecordAfterTheLock(t *testing.T) {
	dir := t.TempDir()
	journal := filepath.Join(dir, "shares.journal")
	record := filepath.Join(dir, "pool.key.payouts.jsonl")
	old, err := shares.OpenJournal(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Lock(); err != nil {
		t.Fatal(err)
	}
	read := make(chan int, 1)
	go func() {
		next, err := shares.OpenJournal(journal)
		if err != nil {
			read <- -1
			return
		}
		if err := next.Lock(); err != nil { // waits for the old generation
			read <- -2
			return
		}
		windows, _, err := readPayoutRecord(record)
		if err != nil {
			read <- -3
			return
		}
		read <- len(windows)
	}()
	// the old generation's close is in flight: the new generation must not have read yet
	select {
	case n := <-read:
		t.Fatalf("the new generation read the record (%d window(s)) while the old held the lock", n)
	case <-time.After(300 * time.Millisecond):
	}
	// the close finishes: the window with its sends written, then the old lets go (its exit)
	f, err := os.OpenFile(record, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	w := PayoutWindow{From: 3308, To: 3371, Rows: []PayoutRow{{Member: "0xcceeadd0", DueWei: "78440000000000000000", Status: "sent", Tx: "0xaa"}}}
	if err := json.NewEncoder(f).Encode(w); err != nil {
		t.Fatal(err)
	}
	f.Close()
	old.Close()
	select {
	case n := <-read:
		if n != 1 {
			t.Fatalf("the new generation reads the closed window once the lock is its: %d", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("the new generation never took the lock after the old let go")
	}
}

// F-510 (0.1.92): the start's reconciliation. A send the chain carries from the key that no row named is an advance to
// the member it went to; a send a row names is not; an advance already named is not named again (the restart's
// replay); the advance is taken off the member's next due as far as the due holds, the rest stays owed and restores
// from the record by assignment.
func TestAChainSendNoRowNamedIsAnAdvanceTakenOffTheNextDue(t *testing.T) {
	wei := func(n int64) *big.Int { return new(big.Int).Mul(big.NewInt(n), big.NewInt(1e18)) }
	windows := []PayoutWindow{{From: 3308, To: 3371, Rows: []PayoutRow{
		{Member: "0xcceeadd0", DueWei: wei(78).String(), Status: "paid", Tx: "0xAA", Block: 3372},
	}}}
	sends := []keySend{
		{Hash: "0xaa", To: "0xcceeadd0", Block: 3372, Value: wei(78), Gas: big.NewInt(1), OK: true},
		{Hash: "0xbb", To: "0xcceeadd0", Block: 3373, Value: wei(78), Gas: big.NewInt(1), OK: true}, // the double send
		{Hash: "0xcc", To: "0xf4b0ae5d", Block: 3373, Value: wei(3), Gas: big.NewInt(1), OK: false}, // failed: no value moved
	}
	fresh := reconcileAdvances(windows, sends)
	if len(fresh) != 1 || fresh[0].Tx != "0xbb" || fresh[0].Member != "0xcceeadd0" || fresh[0].ValueWei != wei(78).String() {
		t.Fatalf("one advance, the double send: %+v", fresh)
	}
	windows[0].Advances = append(windows[0].Advances, fresh...)
	if again := reconcileAdvances(windows, sends); len(again) != 0 {
		t.Fatalf("an advance named is not named again at a restart: %+v", again)
	}
	owed := advanceFromRecord(windows)
	if owed["0xcceeadd0"] == nil || owed["0xcceeadd0"].Cmp(wei(78)) != 0 {
		t.Fatalf("the advance is owed after the record's last window: %v", owed)
	}
	// the next window: a due of 50 pays 50 of it, 28 stays owed
	applied, rest := applyAdvance(owed["0xcceeadd0"], wei(50))
	if applied.Cmp(wei(50)) != 0 || rest.Cmp(wei(28)) != 0 {
		t.Fatalf("applied %s rest %s", applied, rest)
	}
	windows = append(windows, PayoutWindow{From: 3372, To: 3435, Rows: []PayoutRow{
		{Member: "0xcceeadd0", DueWei: "0", AdvanceWei: applied.String(), AdvanceOut: rest.String(), Status: "nothing due"},
	}})
	owed = advanceFromRecord(windows)
	if owed["0xcceeadd0"] == nil || owed["0xcceeadd0"].Cmp(wei(28)) != 0 {
		t.Fatalf("the rest restores from the last row by assignment: %v", owed)
	}
	// the window after pays the rest off; nothing owed
	applied, rest = applyAdvance(owed["0xcceeadd0"], wei(40))
	if applied.Cmp(wei(28)) != 0 || rest.Sign() != 0 {
		t.Fatalf("applied %s rest %s", applied, rest)
	}
	windows = append(windows, PayoutWindow{From: 3436, To: 3499, Rows: []PayoutRow{
		{Member: "0xcceeadd0", DueWei: wei(12).String(), AdvanceWei: applied.String(), Status: "sent", Tx: "0xdd"},
	}})
	if owed = advanceFromRecord(windows); len(owed) != 0 {
		t.Fatalf("nothing owed: %v", owed)
	}
	// 0.1.93: the advance row names its send's gas; the outstanding sum is a decimal string
	fresh = reconcileAdvances(nil, []keySend{{Hash: "0xee", To: "0xcceeadd0", Block: 1, Value: wei(1), Gas: big.NewInt(21000), OK: true}})
	if len(fresh) != 1 || fresh[0].GasWei != "21000" {
		t.Fatalf("the advance names its gas: %+v", fresh)
	}
	if sumWei(map[string]*big.Int{"a": wei(2), "b": wei(3)}) != wei(5).String() {
		t.Fatalf("the outstanding sum")
	}
	// a due of nothing applies nothing: the whole advance stays owed
	applied, rest = applyAdvance(wei(5), new(big.Int))
	if applied.Sign() != 0 || rest.Cmp(wei(5)) != 0 {
		t.Fatalf("no due, nothing applied: %s %s", applied, rest)
	}
}
