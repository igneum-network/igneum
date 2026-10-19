// Package recoverycheck is protocol B's independent implementation of
// work-priced recovery on the single-lane launch chain
// (docs/protocol-a/RECOVERY-PRICING.md section 2 at 29e037bd; PLAN-TO-WIN.md
// point 7), written from the protocol team's text alone, never from his
// difficulty/recovery.go. Per block, from headers alone: the reference
// difficulty is the greatest over the last 240 parents; D = work(T(difficulty))
// by PHASE-A-CONSENSUS.md section 4; D_REF = floor(work(T(reference)) x 7 / 8);
// fraction = 1 when D >= D_REF else D / D_REF; paid = floor(subsidy x
// fraction); unpaid = subsidy - paid, the continuity reservoir's input.
package recoverycheck

import (
	"math/big"

	"github.com/ethereum/go-ethereum/consensus/igneum/mosaicscore"
)

// Window is the reference window in blocks (the half-life at 3,600 s / 15 s).
const Window = 240

// BandNum and BandDen are the band below the window's maximum: 7 / 8.
const (
	BandNum = 7
	BandDen = 8
)

// ReferenceDifficulty is the greatest difficulty among the last Window
// parents (parents oldest first; fewer near genesis); zero with no parents.
func ReferenceDifficulty(parents []*big.Int) *big.Int {
	ref := new(big.Int)
	start := 0
	if len(parents) > Window {
		start = len(parents) - Window
	}
	for _, d := range parents[start:] {
		if d.Cmp(ref) > 0 {
			ref.Set(d)
		}
	}
	return ref
}

// WorkOf is work(T(difficulty)), the canonical intrinsic work; zero at difficulty zero.
func WorkOf(difficulty *big.Int) *big.Int {
	if difficulty.Sign() <= 0 {
		return new(big.Int)
	}
	t, err := mosaicscore.Target(difficulty)
	if err != nil {
		return new(big.Int)
	}
	w, err := mosaicscore.Work(t)
	if err != nil {
		return new(big.Int)
	}
	return w
}

// DRef is floor(work(T(reference)) x 7 / 8).
func DRef(reference *big.Int) *big.Int {
	w := WorkOf(reference)
	w.Mul(w, big.NewInt(BandNum))
	return w.Div(w, big.NewInt(BandDen))
}

// Fraction is the exact rational: (1, 1) when D >= D_REF, else (D, D_REF).
func Fraction(d, dRef *big.Int) (num, den *big.Int) {
	if d.Cmp(dRef) >= 0 {
		return big.NewInt(1), big.NewInt(1)
	}
	return new(big.Int).Set(d), new(big.Int).Set(dRef)
}

// Price splits the block's subsidy: paid = floor(subsidy x num / den), unpaid = subsidy - paid.
func Price(subsidy, num, den *big.Int) (paid, unpaid *big.Int) {
	paid = new(big.Int).Mul(subsidy, num)
	paid.Div(paid, den)
	return paid, new(big.Int).Sub(subsidy, paid)
}

// Block is the whole rule for one block.
type Block struct {
	D, Reference, DRef, Num, Den, Paid, Unpaid *big.Int
}

func Evaluate(parents []*big.Int, difficulty, subsidy *big.Int) Block {
	var b Block
	b.D = WorkOf(difficulty)
	b.Reference = ReferenceDifficulty(parents)
	b.DRef = DRef(b.Reference)
	b.Num, b.Den = Fraction(b.D, b.DRef)
	b.Paid, b.Unpaid = Price(subsidy, b.Num, b.Den)
	return b
}
