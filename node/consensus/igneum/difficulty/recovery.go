// The work-priced recovery rule of the single-lane launch chain
// (PLAN-TO-WIN.md point 7; Mosaic v0.2 section 6.1 carried to one lane):
// an eased block, one whose difficulty the rule lowered after the chain ran
// behind, earns the fork-choice credit of its own difficulty (the Nakamoto
// rule already does this: total difficulty sums what each block proved) and
// only the fraction of the block subsidy its difficulty justifies against
// the reference difficulty. The unpaid part is the issuance code's to carry
// into the continuity reservoir (protocol B); nothing here reads or moves a
// balance. Every input is a header field, so the fraction is recomputed
// from headers alone on any node and any fork.
package difficulty

import "math/big"

// ReferenceWindow is the number of parents the reference difficulty is
// taken over: the half-life in blocks at the candidate (3600 s / 15 s).
const ReferenceWindow = 240

// RecoveryBandNum / RecoveryBandDen is the tolerance below the reference
// inside which a block is not eased: the greatest of 240 noisy samples sits
// about 11 percent above the typical one (0.056 log2 per block at the
// candidate half-life), so the reference the issuance reads is 7/8 of the
// window's maximum and an honest block is never priced. The fraction is
// continuous at the band's edge.
const (
	RecoveryBandNum = 7
	RecoveryBandDen = 8
)

// ReferenceDifficulty is the greatest difficulty among the block's parents
// over ReferenceWindow blocks (fewer near genesis): the difficulty the
// chain held before any easing, consensus-derived from headers alone.
func ReferenceDifficulty(parents []*big.Int) *big.Int {
	ref := new(big.Int)
	start := 0
	if len(parents) > ReferenceWindow {
		start = len(parents) - ReferenceWindow
	}
	for _, d := range parents[start:] {
		if d.Cmp(ref) > 0 {
			ref.Set(d)
		}
	}
	return ref
}

// Work is a block's intrinsic work from its difficulty: floor(2^256 /
// (T + 1)) with T = floor((2^256 - 1) / difficulty), the canonical formula
// of docs/igneumhash/mosaic/PHASE-A-CONSENSUS.md section 4 (D - 1 for most
// difficulties, D at a power of two). The issuance code reads work, never
// difficulty, so one formula serves both chains.
func Work(difficulty *big.Int) *big.Int {
	if difficulty == nil || difficulty.Sign() <= 0 {
		return new(big.Int)
	}
	target := new(big.Int).Div(maxTarget, difficulty)
	return new(big.Int).Div(two256, new(big.Int).Add(target, big.NewInt(1)))
}

var (
	two256    = new(big.Int).Lsh(big.NewInt(1), 256)
	maxTarget = new(big.Int).Sub(two256, big.NewInt(1))
)

// PricedReference is the reference intrinsic work the issuance code reads
// (D_REF): floor(Work(reference difficulty) x 7 / 8), the window's
// maximum less the band.
func PricedReference(referenceDifficulty *big.Int) *big.Int {
	w := Work(referenceDifficulty)
	w.Mul(w, big.NewInt(RecoveryBandNum))
	return w.Div(w, big.NewInt(RecoveryBandDen))
}

// RecoveryFraction is the share of the block subsidy a block earns, as an
// exact rational num / den in [0, 1]: 1 when the block's work D is at least
// D_REF, else D / D_REF (protocol B's issuance computes imm = B if D >= D_REF
// else floor(B x D / D_REF) from the same two integers). A block with no
// reference (genesis) earns the whole subsidy.
func RecoveryFraction(work, pricedReference *big.Int) (num, den *big.Int) {
	if pricedReference == nil || pricedReference.Sign() <= 0 || work == nil || work.Sign() <= 0 {
		return big.NewInt(1), big.NewInt(1)
	}
	if work.Cmp(pricedReference) >= 0 {
		return big.NewInt(1), big.NewInt(1)
	}
	return new(big.Int).Set(work), new(big.Int).Set(pricedReference)
}

// PricedSubsidy applies the fraction to a subsidy: floor(subsidy x num /
// den). The remainder, subsidy less the result, is the unpaid part.
func PricedSubsidy(subsidy, num, den *big.Int) *big.Int {
	out := new(big.Int).Mul(subsidy, num)
	return out.Div(out, den)
}
