// Package fees is Igneum's reference for the EIP-1559 base-fee rule as the
// chain runs it (the plan, page 20): the fee market of EIP-1559 with Igneum's
// constants, active from genesis, with one deliberate deviation, a floor on
// the base fee (MinBaseFee) so an idle chain cannot price execution at zero.
//
// Core-Geth's consensus/misc/eip1559.CalcBaseFee is the production code
// path (PB-03 wires the constants through the configurator and adds the
// floor there); this package is the standalone statement of the rule with
// its tests, so the fee rule is replayable from code the same way the
// schedule is. Burn accounting: the base fee is never credited to anyone
// (core/state_transition.go credits gasUsed * effectiveTip to the coinbase
// and nothing else), so burned(block) = sum over its transactions of
// gasUsed * baseFee.
package fees

import "math/big"

// Params are consensus constants (IgneumConfig, PA-08).
type Params struct {
	ElasticityMultiplier     uint64   // gas limit / target
	BaseFeeChangeDenominator uint64   // bounds the per-block base-fee change
	InitialBaseFee           *big.Int // base fee of block 1 (genesis carries it too)
	MinBaseFee               *big.Int // floor; nil or zero means no floor
}

// Candidate is proposed in docs/protocol-b/DESIGN.md: Ethereum's elasticity
// and denominator (the per-block bound of 12.5 percent is right for a
// 15-second block), 1 gwei initial and 1 gwei floor.
var Candidate = Params{
	ElasticityMultiplier:     2,
	BaseFeeChangeDenominator: 8,
	InitialBaseFee:           big.NewInt(1_000_000_000),
	MinBaseFee:               big.NewInt(1_000_000_000),
}

// CalcBaseFee is the base fee of the block after a parent with the given gas
// limit, gas used and base fee. It is EIP-1559's rule with the floor applied
// last. The parent's gas target is gasLimit / ElasticityMultiplier.
func (p Params) CalcBaseFee(parentGasLimit, parentGasUsed uint64, parentBaseFee *big.Int) *big.Int {
	target := parentGasLimit / p.ElasticityMultiplier
	out := new(big.Int)
	switch {
	case parentGasUsed == target:
		out.Set(parentBaseFee)
	case parentGasUsed > target:
		// parentBaseFee * (gasUsed - target) / target / denominator, at least 1
		delta := new(big.Int).SetUint64(parentGasUsed - target)
		delta.Mul(delta, parentBaseFee)
		delta.Div(delta, new(big.Int).SetUint64(target))
		delta.Div(delta, new(big.Int).SetUint64(p.BaseFeeChangeDenominator))
		if delta.Sign() == 0 {
			delta.SetInt64(1)
		}
		out.Add(parentBaseFee, delta)
	default:
		delta := new(big.Int).SetUint64(target - parentGasUsed)
		delta.Mul(delta, parentBaseFee)
		delta.Div(delta, new(big.Int).SetUint64(target))
		delta.Div(delta, new(big.Int).SetUint64(p.BaseFeeChangeDenominator))
		out.Sub(parentBaseFee, delta)
		if out.Sign() < 0 {
			out.SetInt64(0)
		}
	}
	if p.MinBaseFee != nil && out.Cmp(p.MinBaseFee) < 0 {
		out.Set(p.MinBaseFee)
	}
	return out
}

// EffectiveTip is what the producer earns per gas from a transaction with
// the given fee cap and tip under baseFee: min(tip, feeCap - baseFee). A
// legacy transaction passes its gas price as both. ok is false when the
// transaction is not includable (feeCap below the base fee or tip above the
// fee cap), which core/state_transition.go rejects in preCheck.
func EffectiveTip(feeCap, tip, baseFee *big.Int) (*big.Int, bool) {
	if feeCap.Cmp(baseFee) < 0 || tip.Cmp(feeCap) > 0 {
		return nil, false
	}
	room := new(big.Int).Sub(feeCap, baseFee)
	if tip.Cmp(room) < 0 {
		return new(big.Int).Set(tip), true
	}
	return room, true
}

// Burned is the amount removed from supply by a transaction: gasUsed * baseFee.
func Burned(gasUsed uint64, baseFee *big.Int) *big.Int {
	return new(big.Int).Mul(new(big.Int).SetUint64(gasUsed), baseFee)
}
