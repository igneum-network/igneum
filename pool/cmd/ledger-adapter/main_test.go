package main

import (
	"errors"
	"math/big"
	"testing"

	"igneum.network/pool/internal/ledger"
)

const (
	accA = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	accB = "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func run(t *testing.T, dir string, crashAt int, event string) (*State, error) {
	t.Helper()
	a, err := open(dir, 240, 0, 100)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	a.crashAt = crashAt
	return a.apply([]byte(event))
}

func mustRun(t *testing.T, dir string, event string) *State {
	t.Helper()
	st, err := run(t, dir, 0, event)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func n(s string) *big.Int {
	v, _ := new(big.Int).SetString(s, 10)
	return v
}

// invariant: liabilities = credits - payments, after every event.
func invariant(t *testing.T, st *State) {
	t.Helper()
	want := new(big.Int).Sub(n(st.Credits), n(st.Payments))
	if n(st.Liability).Cmp(want) != 0 {
		t.Fatalf("liability %s, credits %s - payments %s = %s", st.Liability, st.Credits, st.Payments, want)
	}
	if new(big.Int).Add(n(st.Immature), n(st.Payable)).Cmp(n(st.Liability)) != 0 {
		t.Fatalf("immature + payable != liability: %+v", st)
	}
}

func seed(t *testing.T, dir string) {
	t.Helper()
	invariant(t, mustRun(t, dir, `{"type":"share","account":"`+accA+`","weight":3}`))
	invariant(t, mustRun(t, dir, `{"type":"share","account":"`+accB+`","weight":1}`))
	st := mustRun(t, dir, `{"type":"block","hash":"0xb1","height":10,"reward":"4000"}`)
	invariant(t, st)
	if st.Accounts[accA]["immature"] != "3000" || st.Accounts[accB]["immature"] != "1000" {
		t.Fatalf("credits %+v", st.Accounts)
	}
	// Below maturity: still immature.
	st = mustRun(t, dir, `{"type":"head","height":100}`)
	invariant(t, st)
	if st.Blocks["0xb1"].State != ledger.StateImmature {
		t.Fatalf("state %q at depth 90", st.Blocks["0xb1"].State)
	}
	// At depth 240: mature, payable.
	st = mustRun(t, dir, `{"type":"head","height":250}`)
	invariant(t, st)
	if st.Blocks["0xb1"].State != ledger.StateMature || st.Accounts[accA]["payable"] != "3000" {
		t.Fatalf("after maturity %+v", st)
	}
}

func TestPayoutWithoutCrash(t *testing.T) {
	dir := t.TempDir()
	seed(t, dir)
	st := mustRun(t, dir, `{"type":"payout","min":"1"}`)
	invariant(t, st)
	if st.Payments != "4000" || st.Broadcast != "4000" || st.UnsettledDebits != 0 || st.Liability != "0" {
		t.Fatalf("after payout %+v", st)
	}
	if len(st.Intents) != 2 {
		t.Fatalf("intents %+v", st.Intents)
	}
	// A second payout pays nothing more.
	st = mustRun(t, dir, `{"type":"payout","min":"1"}`)
	invariant(t, st)
	if st.Payments != "4000" || st.Broadcast != "4000" || len(st.Intents) != 2 {
		t.Fatalf("double payout %+v", st)
	}
}

func TestCrashAtEveryPayoutStepNeverDoublePaysOrLoses(t *testing.T) {
	for step := 1; step <= 4; step++ {
		dir := t.TempDir()
		seed(t, dir)
		_, err := run(t, dir, step, `{"type":"payout","min":"1"}`)
		if !errors.Is(err, errCrash) {
			t.Fatalf("step %d: expected the injected crash, got %v", step, err)
		}
		// Restart: recovery runs before the event.
		st := mustRun(t, dir, `{"type":"restart"}`)
		invariant(t, st)
		if st.UnsettledDebits != 0 {
			t.Fatalf("step %d: unsettled debits after recovery: %+v", step, st)
		}
		if st.Payments != st.Broadcast {
			t.Fatalf("step %d: ledger debits %s but chain holds %s", step, st.Payments, st.Broadcast)
		}
		// Finish the payout: the total must come to exactly the credits, once.
		st = mustRun(t, dir, `{"type":"payout","min":"1"}`)
		invariant(t, st)
		if st.Payments != "4000" || st.Broadcast != "4000" || st.Liability != "0" {
			t.Fatalf("step %d: after resumed payout %+v", step, st)
		}
		st = mustRun(t, dir, `{"type":"payout","min":"1"}`)
		if st.Broadcast != "4000" {
			t.Fatalf("step %d: double pay %+v", step, st)
		}
	}
}

func TestOrphanReversesWithAuditRows(t *testing.T) {
	dir := t.TempDir()
	mustRun(t, dir, `{"type":"share","account":"`+accA+`","weight":1}`)
	st := mustRun(t, dir, `{"type":"block","hash":"0xb2","height":20,"reward":"500"}`)
	invariant(t, st)
	if st.Liability != "500" {
		t.Fatalf("liability %s", st.Liability)
	}
	// The chain has another block at height 20: ours is orphaned.
	st = mustRun(t, dir, `{"type":"head","height":30,"canonical":{"20":"0xtheirs"}}`)
	invariant(t, st)
	if st.Liability != "0" || st.Blocks["0xb2"].State != ledger.StateOrphaned {
		t.Fatalf("after orphan %+v", st)
	}
	if len(st.Audit) != 1 || st.Audit[0]["kind"] != string(ledger.ReverseCredit) || st.Audit[0]["amount"] != "500" {
		t.Fatalf("audit rows %+v", st.Audit)
	}
	// Survives a restart.
	st = mustRun(t, dir, `{"type":"restart"}`)
	invariant(t, st)
	if len(st.Audit) != 1 || st.Liability != "0" {
		t.Fatalf("after restart %+v", st)
	}
}

func TestUnknownEventAndDuplicateBlock(t *testing.T) {
	dir := t.TempDir()
	if _, err := run(t, dir, 0, `{"type":"bogus"}`); err == nil {
		t.Fatal("unknown event accepted")
	}
	if _, err := run(t, dir, 0, `{"type":"share","account":"`+accA+`","weight":1,"extra":1}`); err == nil {
		t.Fatal("unknown field accepted")
	}
	mustRun(t, dir, `{"type":"share","account":"`+accA+`","weight":1}`)
	mustRun(t, dir, `{"type":"block","hash":"0xb3","height":1,"reward":"10"}`)
	if _, err := run(t, dir, 0, `{"type":"block","hash":"0xb3","height":1,"reward":"10"}`); err == nil {
		t.Fatal("duplicate block accepted")
	}
}
