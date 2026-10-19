package fees

import (
	"math/big"
	"testing"
)

const gwei = 1_000_000_000

func TestBaseFeeAtTargetIsUnchanged(t *testing.T) {
	p := Candidate
	got := p.CalcBaseFee(16_000_000, 8_000_000, big.NewInt(7*gwei))
	if got.Int64() != 7*gwei {
		t.Fatalf("got %s", got)
	}
}

func TestBaseFeeFullBlockRisesTwelveAndAHalfPercent(t *testing.T) {
	p := Candidate
	got := p.CalcBaseFee(16_000_000, 16_000_000, big.NewInt(8*gwei))
	if got.Int64() != 9*gwei {
		t.Fatalf("got %s want 9 gwei", got)
	}
}

func TestBaseFeeEmptyBlockFallsTwelveAndAHalfPercent(t *testing.T) {
	p := Candidate
	got := p.CalcBaseFee(16_000_000, 0, big.NewInt(8*gwei))
	if got.Int64() != 7*gwei {
		t.Fatalf("got %s want 7 gwei", got)
	}
}

func TestBaseFeeFloor(t *testing.T) {
	p := Candidate
	// a run of empty blocks from the initial fee stops at the floor, never below
	fee := new(big.Int).Set(p.InitialBaseFee)
	for i := 0; i < 100; i++ {
		fee = p.CalcBaseFee(16_000_000, 0, fee)
		if fee.Cmp(p.MinBaseFee) < 0 {
			t.Fatalf("block %d: below the floor: %s", i, fee)
		}
	}
	if fee.Cmp(p.MinBaseFee) != 0 {
		t.Fatalf("floor not reached: %s", fee)
	}
	// without a floor the fee decays toward zero (EIP-1559 as written)
	q := p
	q.MinBaseFee = nil
	fee = big.NewInt(1000)
	for i := 0; i < 200; i++ {
		fee = q.CalcBaseFee(16_000_000, 0, fee)
	}
	if fee.Int64() > 10 {
		t.Fatalf("unfloored decay stalled at %s", fee)
	}
}

func TestBaseFeeMinimumRise(t *testing.T) {
	p := Candidate
	q := p
	q.MinBaseFee = nil
	// tiny base fee, slightly over target: the delta rounds to 0 and is lifted to 1
	got := q.CalcBaseFee(16_000_000, 8_000_001, big.NewInt(5))
	if got.Int64() != 6 {
		t.Fatalf("got %s want 6", got)
	}
}

func TestEffectiveTip(t *testing.T) {
	base := big.NewInt(10 * gwei)
	tip, ok := EffectiveTip(big.NewInt(12*gwei), big.NewInt(1*gwei), base)
	if !ok || tip.Int64() != 1*gwei {
		t.Fatalf("tip-limited: %s %v", tip, ok)
	}
	tip, ok = EffectiveTip(big.NewInt(12*gwei), big.NewInt(5*gwei), base)
	if !ok || tip.Int64() != 2*gwei {
		t.Fatalf("cap-limited: %s %v", tip, ok)
	}
	if _, ok = EffectiveTip(big.NewInt(9*gwei), big.NewInt(0), base); ok {
		t.Fatal("fee cap below base fee must not be includable")
	}
	if _, ok = EffectiveTip(big.NewInt(12*gwei), big.NewInt(13*gwei), base); ok {
		t.Fatal("tip above fee cap must not be includable")
	}
	// legacy: gas price as both
	tip, ok = EffectiveTip(big.NewInt(11*gwei), big.NewInt(11*gwei), base)
	if !ok || tip.Int64() != 1*gwei {
		t.Fatalf("legacy: %s %v", tip, ok)
	}
}

func TestBurned(t *testing.T) {
	if Burned(21_000, big.NewInt(gwei)).Int64() != 21_000*gwei {
		t.Fatal("burned")
	}
}
