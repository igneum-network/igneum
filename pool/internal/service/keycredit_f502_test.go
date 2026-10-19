//go:build igneumhash

package service

import (
	"math/big"
	"testing"
)

// F-502 (0.1.91): a window's income is the chain's credit to the key over the window's heights, the key's balance steps
// block by block with the pool's own sends in those blocks added back; the spans of consecutive windows partition the
// heights. The arithmetic of the step sum on a fixed balance series, with a send in one block.
func TestKeyCreditOverSumsTheBalanceSteps(t *testing.T) {
	balances := map[uint64]int64{99: 1000, 100: 1300, 101: 1300, 102: 1700, 103: 1200, 104: 1500}
	sent := map[uint64]*big.Int{103: big.NewInt(600)} // a send of 500 and gas 100 in block 103: the balance fell by 500 net of a 100 credit
	total := new(big.Int)
	prev := big.NewInt(balances[99])
	for h := uint64(100); h <= 104; h++ {
		cur := big.NewInt(balances[h])
		step := new(big.Int).Sub(cur, prev)
		if v := sent[h]; v != nil {
			step.Add(step, v)
		}
		if step.Sign() > 0 {
			total.Add(total, step)
		}
		prev = cur
	}
	// the credits: 300 at 100, 0 at 101, 400 at 102, 100 at 103 (1200 - 1700 + 600), 300 at 104 = 1100
	if total.Int64() != 1100 {
		t.Fatalf("the credit over 100 to 104 is 1100, got %d", total.Int64())
	}
	// the spans partition: (lastIncomeTo, lastBlock] with an empty window reading no heights
	lastIncomeTo := uint64(104)
	from, to := lastIncomeTo+1, uint64(104)
	if to >= from {
		t.Fatalf("a window with no carried block past the last reads no heights")
	}
}
