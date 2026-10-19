//go:build igneumhash

package service

import (
	"math/big"
	"testing"
)

// F-540 (0.1.95): a member with an advance owed, a due below it (applied whole: "applied to the advance", never
// "nothing due") and a due above it (the advance paid off, the rest sent or carried), and the plain cases.
func TestARowWithDuesIsNeverNothingDue(t *testing.T) {
	wei := func(n int64) *big.Int { return new(big.Int).Mul(big.NewInt(n), big.NewInt(1e15)) } // milli-IGN
	min := wei(1)
	owed := wei(5000)
	d := decideRow(wei(188), owed, min)
	if d.status != "applied to the advance" || d.applied.Cmp(wei(188)) != 0 || d.rest.Cmp(wei(4812)) != 0 || d.due.Sign() != 0 {
		t.Fatalf("a due below the advance is applied whole: %+v", d)
	}
	d = decideRow(wei(6000), owed, min)
	if d.status != "sent" || d.applied.Cmp(wei(5000)) != 0 || d.rest.Sign() != 0 || d.due.Cmp(wei(1000)) != 0 {
		t.Fatalf("a due above the advance pays it off and sends the rest: %+v", d)
	}
	d = decideRow(new(big.Int).Add(owed, big.NewInt(1)), owed, min)
	if d.status != "carried" || d.due.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("the rest under the minimum is carried: %+v", d)
	}
	d = decideRow(new(big.Int), owed, min)
	if d.status != "nothing due" || d.applied.Sign() != 0 || d.rest.Cmp(owed) != 0 {
		t.Fatalf("no due applies nothing and owes the same: %+v", d)
	}
	d = decideRow(wei(3), nil, min)
	if d.status != "sent" || d.due.Cmp(wei(3)) != 0 {
		t.Fatalf("no advance: sent: %+v", d)
	}
	d = decideRow(big.NewInt(5), nil, min)
	if d.status != "carried" {
		t.Fatalf("no advance, under the minimum: carried: %+v", d)
	}
}

// 0.1.95: the pool sends what the balance allows.
func TestThePoolSendsWhatTheBalanceAllows(t *testing.T) {
	wei := func(n int64) *big.Int { return new(big.Int).Mul(big.NewInt(n), big.NewInt(1e15)) }
	v, c := partialSend(wei(19000), wei(18000), wei(1))
	if v.Cmp(wei(18000)) != 0 || c.Cmp(wei(1000)) != 0 {
		t.Fatalf("the available part sent, the rest carried: %s %s", v, c)
	}
	v, c = partialSend(wei(100), wei(18000), wei(1))
	if v.Cmp(wei(100)) != 0 || c.Sign() != 0 {
		t.Fatalf("a due that fits is sent whole: %s %s", v, c)
	}
	v, c = partialSend(wei(100), new(big.Int), wei(1))
	if v.Sign() != 0 || c.Cmp(wei(100)) != 0 {
		t.Fatalf("no balance: nothing sent, the whole due carried: %s %s", v, c)
	}
	v, c = partialSend(wei(100), big.NewInt(5), wei(1))
	if v.Sign() != 0 || c.Cmp(wei(100)) != 0 {
		t.Fatalf("an available part under the minimum: nothing sent: %s %s", v, c)
	}
	// the restore reads carry_out_wei by assignment: a partial row keeps its rest, a later paid row without one clears it
	ws := []PayoutWindow{{Rows: []PayoutRow{{Member: "0xm", DueWei: wei(19000).String(), Status: "partial", PaidWei: wei(18000).String(), CarryOut: wei(1000).String()}}}}
	if c := carryFromRecord(ws); c["0xm"] == nil || c["0xm"].Cmp(wei(1000)) != 0 {
		t.Fatalf("the partial row's rest restores: %v", c)
	}
	ws = append(ws, PayoutWindow{Rows: []PayoutRow{{Member: "0xm", DueWei: wei(1050).String(), Status: "paid"}}})
	if c := carryFromRecord(ws); len(c) != 0 {
		t.Fatalf("a paid row clears: %v", c)
	}
}
