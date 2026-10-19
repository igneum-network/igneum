package shares

import (
	"errors"
	"math/big"
	"testing"
)

func wei(s string) *big.Int {
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		panic("bad wei literal " + s)
	}
	return v
}

func window(weights ...uint64) []Share {
	accounts := []string{"0xaaaa", "0xbbbb", "0xcccc"}
	out := make([]Share, 0, len(weights))
	for i, w := range weights {
		out = append(out, Share{Seq: uint64(i + 1), Account: accounts[i%len(accounts)], Weight: w, JobID: "job"})
	}
	return out
}

func TestDistributeSumsExactly(t *testing.T) {
	// 3 accounts, weights a=2, b=2, c=1 (shares cycle a,b,c,a,b).
	shares := window(1, 1, 1, 1, 1)
	block := Block{Hash: "0x01", Height: 100, Reward: wei("1000000000000000000")}
	d, err := Distribute(block, shares, 100, nil) // 1 percent
	if err != nil {
		t.Fatal(err)
	}
	if d.Fee.Cmp(wei("10000000000000000")) != 0 {
		t.Fatalf("fee: got %s", d.Fee)
	}
	sum := new(big.Int).Set(d.Fee)
	sum.Add(sum, d.Remainder)
	for _, c := range d.Credits {
		sum.Add(sum, c)
	}
	if sum.Cmp(block.Reward) != 0 {
		t.Fatalf("fee + credits + remainder = %s, want %s", sum, block.Reward)
	}
	// a has 2 of 5 weight.
	want := new(big.Int).Mul(wei("990000000000000000"), big.NewInt(2))
	want.Div(want, big.NewInt(5))
	if d.Credits["0xaaaa"].Cmp(want) != 0 {
		t.Fatalf("a: got %s want %s", d.Credits["0xaaaa"], want)
	}
	if d.Remainder.Sign() != 0 {
		t.Fatalf("remainder: got %s", d.Remainder)
	}
}

func TestDistributeRemainderIsRecorded(t *testing.T) {
	// 7 wei across weights 1,1,1: each gets 2, remainder 1.
	d, err := Distribute(Block{Hash: "0x02", Height: 1, Reward: big.NewInt(7)}, window(1, 1, 1), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d.Fee.Sign() != 0 {
		t.Fatalf("zero fee expected, got %s", d.Fee)
	}
	for a, c := range d.Credits {
		if c.Cmp(big.NewInt(2)) != 0 {
			t.Fatalf("%s: got %s", a, c)
		}
	}
	if d.Remainder.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("remainder: got %s", d.Remainder)
	}
	// Carry in joins the next distribution: 1 carried + 8 reward = 9 across 3.
	d2, err := Distribute(Block{Hash: "0x03", Height: 2, Reward: big.NewInt(8)}, window(1, 1, 1), 0, d.Remainder)
	if err != nil {
		t.Fatal(err)
	}
	for a, c := range d2.Credits {
		if c.Cmp(big.NewInt(3)) != 0 {
			t.Fatalf("%s: got %s", a, c)
		}
	}
	if d2.Remainder.Sign() != 0 {
		t.Fatalf("remainder after carry: got %s", d2.Remainder)
	}
}

func TestDistributeEmptyWindow(t *testing.T) {
	d, err := Distribute(Block{Hash: "0x04", Height: 1, Reward: big.NewInt(100)}, nil, 250, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Credits) != 0 {
		t.Fatalf("credits on empty window: %v", d.Credits)
	}
	if d.Fee.Cmp(big.NewInt(2)) != 0 { // floor(100 * 250 / 10000)
		t.Fatalf("fee: got %s", d.Fee)
	}
	if d.Remainder.Cmp(big.NewInt(98)) != 0 {
		t.Fatalf("remainder: got %s", d.Remainder)
	}
}

func TestFeeBounds(t *testing.T) {
	if _, err := Distribute(Block{Hash: "0x05", Height: 1, Reward: big.NewInt(1)}, window(1), 10001, nil); !errors.Is(err, ErrFee) {
		t.Fatalf("got %v", err)
	}
	if _, err := Distribute(Block{Hash: "0x06", Height: 1}, window(1), 0, nil); !errors.Is(err, ErrReward) {
		t.Fatalf("got %v", err)
	}
}

func TestInputsHashDeterministic(t *testing.T) {
	b := Block{Hash: "0x07", Height: 9, Reward: big.NewInt(1000)}
	d1, _ := Distribute(b, window(2, 3), 50, nil)
	d2, _ := Distribute(b, window(2, 3), 50, nil)
	if d1.InputsHash != d2.InputsHash {
		t.Fatal("same inputs, different hash")
	}
	d3, _ := Distribute(b, window(2, 4), 50, nil)
	if d1.InputsHash == d3.InputsHash {
		t.Fatal("different weights, same hash")
	}
	d4, _ := Distribute(b, window(2, 3), 51, nil)
	if d1.InputsHash == d4.InputsHash {
		t.Fatal("different fee, same hash")
	}
}

func TestWindowKeepsMinimalSuffix(t *testing.T) {
	w := NewWindow(10)
	for i := 0; i < 4; i++ {
		w.Add(Share{Account: "0xaaaa", Weight: 4})
	}
	if w.Len() != 3 || w.Total() != 12 {
		t.Fatalf("len %d total %d, want 3 and 12", w.Len(), w.Total())
	}
	snap := w.Snapshot()
	if snap[0].Seq != 2 || snap[2].Seq != 4 {
		t.Fatalf("suffix sequence: %+v", snap)
	}
	// A single share heavier than the target stands alone.
	w.Add(Share{Account: "0xbbbb", Weight: 100})
	if w.Len() != 1 || w.Total() != 100 {
		t.Fatalf("heavy share: len %d total %d", w.Len(), w.Total())
	}
}
