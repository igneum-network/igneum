package igneum

import (
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/consensus/igneum/shares"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// TestWithdrawalIndexGlobal (F-87): under the landing check a block's indices run
// from the chain's count at the parent, the count advances by the list,
// a list restarting at zero is refused; below the gate the per-block form.
func TestWithdrawalIndexGlobal(t *testing.T) {
	e := shareEngine(t)
	defer e.Close()
	e.SetRuleForks(&ctypes.IgneumRuleForks{WithdrawalIndexV1: 10})
	st := nativeState(t, big.NewInt(1_000_000))
	var accepted []shares.Entry
	for i := 0; i < 4; i++ {
		accepted = append(accepted, shares.Entry{Address: shareAddr(i), Weight: 1})
	}
	sealer := shareAddr(9000)
	// Below the gate: the per-block index, the count untouched.
	list5, _, err := e.ShareList(st, 5, accepted, sealer)
	if err != nil {
		t.Fatal(err)
	}
	ws5 := shares.Withdrawals(list5)
	e.indexWithdrawals(st, 5, ws5)
	if ws5[0].Index != 0 || ws5[len(ws5)-1].Index != uint64(len(ws5)-1) {
		t.Fatalf("below the gate: indices %d..%d", ws5[0].Index, ws5[len(ws5)-1].Index)
	}
	e.advanceWithdrawalCount(st, 5, len(ws5))
	if c := e.WithdrawalCount(st); c != 0 {
		t.Fatalf("the count advanced below the gate: %d", c)
	}
	// At the landing check: the first list from zero, the count then its length.
	h10 := &types.Header{Number: big.NewInt(10), Difficulty: big.NewInt(1)}
	list10, _, _ := e.ShareList(st, 10, accepted, sealer)
	ws10 := shares.Withdrawals(list10)
	e.indexWithdrawals(st, 10, ws10)
	if err := e.VerifyShareList(st, h10, accepted, ws10); err != nil {
		t.Fatalf("the first indexed list refused: %v", err)
	}
	e.advanceWithdrawalCount(st, 10, len(ws10))
	n := uint64(len(ws10))
	if c := e.WithdrawalCount(st); c != n {
		t.Fatalf("the count after block 10: %d, want %d", c, n)
	}
	// The next block: indices from n; a list restarting at zero refused.
	h11 := &types.Header{Number: big.NewInt(11), Difficulty: big.NewInt(1)}
	list11, _, _ := e.ShareList(st, 11, accepted, sealer)
	ws11 := shares.Withdrawals(list11)
	e.indexWithdrawals(st, 11, ws11)
	if ws11[0].Index != n || ws11[len(ws11)-1].Index != n+uint64(len(ws11))-1 {
		t.Fatalf("block 11's indices %d..%d, want from %d", ws11[0].Index, ws11[len(ws11)-1].Index, n)
	}
	if err := e.VerifyShareList(st, h11, accepted, ws11); err != nil {
		t.Fatalf("block 11's list refused: %v", err)
	}
	restart := shares.Withdrawals(list11)
	if err := e.VerifyShareList(st, h11, accepted, restart); !errors.Is(err, errShareList) {
		t.Fatalf("a list restarting at zero accepted under the landing check: %v", err)
	}
	e.advanceWithdrawalCount(st, 11, len(ws11))
	if c := e.WithdrawalCount(st); c != n+uint64(len(ws11)) {
		t.Fatalf("the count after block 11: %d", c)
	}
}
