// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package igneum

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/shares"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"
)

// The share lane's rule as the engine binds it (consensus/igneum/shares;
// LAUNCH-GUARDS.md 2.9; the seventh development chain). The engine's part
// is three calls the protocol team's assembly and import make (the share's wire
// form, its submission and the body's list are the protocol team's):
//
//   - ShareTarget(header): the target a share of this template must meet,
//     served beside the block target (igneum_getWork's shareTarget);
//   - ShareList(parentState, accepted): the canonical payout list of a
//     block from the carry at the parent's state and the block's accepted
//     shares, for the template (assembly) and for the check on import
//     (the block's list must equal it: a signer cannot omit, replace,
//     reorder or reweight a share the body carries, gate rows 2 and 3);
//   - SettleShares(state, header, amount, list): in Finalize under the
//     lane, the amount (the subsidy's miner share plus the tips, F-35)
//     paid pro rata over the list with dust carried, and the new carry
//     (the overflow of Build plus the dust) written under the registry
//     account. Without the lane (sharesPerBlock 0) nothing here runs and
//     Finalize's payProRata is the rule as before.
//
// The carry lives under the registry account beside the units table (the
// consensus-carried state of the genesis form), in shares.carrySlot's
// words; every node computes it the same way from the same list.

var (
	errSharesOff  = errors.New("igneum shares: the lane is off on this chain")
	errShareList  = errors.New("igneum shares: the block's payout list is not the canonical list")
	errShareCarry = errors.New("igneum shares: the carry balance could not be read")
)

// SharesOn reports whether the share lane binds a block at this height.
func (e *Engine) SharesOn(height uint64) bool {
	s := e.config.Parts.Sortition
	return s != nil && s.Active(height) && s.SharesPerBlock > 0
}

// MaxShareRecency is the lane's bound on the carriage window: 64 blocks
// (devnet8's value from genesis, 12 October 2026; the project's order of 19:4x
// UK on 11 October), a constant of the lane and not of the carried
// entry's width (the one-byte parent index holds 0 to 255, the height
// form any), so the validation holds whichever entry the chain carries;
// the pool's bound is maxShares x the window (16,384 at the ceiling), and
// the duplicate set and the carried-key cache are sized for it.
const MaxShareRecency = 64

// MaxShareStep is PA-53's bound on a share's declared target step: 32, so
// an entry weighs at most 2^32 and a 256-entry address at a height sums
// inside a uint64 (the step's range bounds the weight, the per-address
// cap the count).
const MaxShareStep = 32

// ShareRecencyBlocks is the carriage window (G-23; the sortition parameter
// shareRecency, the entry's bound when unset): a share mined at height h
// rides a block at or before h + ShareRecencyBlocks() - 1; the sealer's
// pool is bounded at MaxShares x this and refuses beyond it ("pool
// full"), so an accepted share is one that fits the window. Off the lane
// it reads the entry's bound.
func (e *Engine) ShareRecencyBlocks() uint64 {
	s := e.config.Parts.Sortition
	if s == nil || s.ShareRecency == 0 {
		return ShareRecency
	}
	return s.ShareRecency
}

// SharePoolBound is the most shares a sealer's pool holds for carriage
// (G-23 rule 1): MaxShares x the recency; an accepted share beyond it is
// refused at acceptance, never expired silently.
func (e *Engine) SharePoolBound() uint64 {
	s := e.config.Parts.Sortition
	if s == nil || s.SharesPerBlock == 0 {
		return 0
	}
	return s.MaxShares * e.ShareRecencyBlocks()
}

// ShareTarget is the share target of a template: the block target times
// the operating shares per block; nil when the lane is off.
func (e *Engine) ShareTarget(header *types.Header) *big.Int {
	if header == nil || header.Number == nil || !e.SharesOn(header.Number.Uint64()) {
		return nil
	}
	return shares.Target(header.Difficulty, e.config.Parts.Sortition.SharesPerBlock)
}

// shareAccount is where the carry lives: the registry account.
func (e *Engine) shareAccount() (common.Address, error) {
	a := e.config.Parts.Authority
	if a == nil {
		return common.Address{}, errSharesOff
	}
	return a.Registry, nil
}

// SealerWeight is the sealer's own entry's weight in its FIRST form, over
// the accepted entries' count n (one an address): max(1, ceil(n / 3)), the
// rule's quarter only when no address carries two shares in a block;
// kept for the chains below their SealerSharesV1 gate (devnet7) and the
// tests that read it. SealerWeightOf is the rule.
func SealerWeight(n int) uint64 {
	if n <= 0 {
		return 1
	}
	return uint64((n + 2) / 3)
}

// SealerWeightOf is the sealer's own entry's weight: max(1, ceil(W / 3))
// over the SUMMED weight W of the block's accepted entries, the carried
// shares' count under the count form and the weight under PA-53's step
// form (the design seat's line and the project's rulings of 20:5x UK, 11 October
// 2026: the sealer's quarter is a share of the PAYMENT, so its term scales
// with the payment's denominator; over the entries' count it would
// collapse as soon as any card paced, 0.16 percent at ten shares of step
// 8). Exactly a quarter at every step; W at most 256 x 2^32 an address
// and the term a third of the block's total, inside a uint64.
func SealerWeightOf(total uint64) uint64 {
	if total == 0 {
		return 1
	}
	return (total + 2) / 3
}

// TotalWeight is the summed weight of a block's accepted entries
// (saturating, as shares.Build sums).
func TotalWeight(accepted []shares.Entry) uint64 {
	var total uint64
	for _, en := range accepted {
		if total > ^uint64(0)-en.Weight {
			return ^uint64(0)
		}
		total += en.Weight
	}
	return total
}

// ShareList builds the whole derived payout list of a block at height:
// the credits at the parent's state and the block's accepted shares
// through shares.Build (the credits first, then the shares by address,
// at most MaxPayouts minus one entries), then the sealer's own entry
// LAST at SealerWeight(len(accepted)) to the address the block names
// (the sealer's payout address is readable only there, since the header's
// coinbase under a list is the list root's prefix, F-35); and the credits
// the block leaves before dust. A sealer address already in the list
// (the sealer's own shares) still gets its own last entry: the two are
// paid apart and sum, so the rule's weight is readable on every block.
func (e *Engine) ShareList(parentState *state.StateDB, height uint64, accepted []shares.Entry, sealer common.Address) (list, carry []shares.Entry, err error) {
	if !e.SharesOn(height) {
		return nil, nil, errSharesOff
	}
	acct, err := e.shareAccount()
	if err != nil {
		return nil, nil, err
	}
	held, err := shares.ReadCarry(parentState, acct)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", errShareCarry, err)
	}
	list, carry, err = shares.Build(held, accepted, int(e.config.Parts.Sortition.MaxPayouts)-1)
	if err != nil {
		return nil, nil, err
	}
	// Build's list and credits are two windows on one slice: the sealer's
	// entry goes on a copy, never on the list's spare capacity (an append
	// there overwrites the first credit; the engine test caught it).
	whole := make([]shares.Entry, 0, len(list)+1)
	whole = append(whole, list...)
	// The sealer's term over the SUMMED weight of the accepted entries
	// (the project's ruling of 20:5x UK, 11 October 2026): under the count form
	// the sum is the carried SHARES' count (every share weighs 1), which
	// is the rule's n; the form before it took len(accepted), the accepted
	// ENTRIES' count, one an address, the share count only when no address
	// carried two in a block (a bug with a one-line fix; devnet8 runs the
	// fix from block one, devnet7 below the SealerSharesV1 gate keeps the
	// first form, the standing rule on a swap); under the step form the
	// sum is the weight, so the sealer's quarter holds at every step.
	w := SealerWeightOf(TotalWeight(accepted))
	if e.sealerTermFirstForm(height) {
		w = SealerWeight(len(accepted))
	}
	// F-165 under the first form (the coordinate rule's pro rata over the
	// list, the research chain: its first fold block 6550 read the
	// sealer's term paid as before, the fold cut in payV2Amounts alone):
	// from sealerFoldFrom the sealer's own entry carries weight 0, a name
	// and nothing else (the list's last entry still names the sealer for
	// the readers and the third cut's block credit), so the amount is
	// paid over the members by their balanced weight alone and the sealer
	// through its own key's entry or not at all; shares.Pay pays a
	// zero-weight entry nothing and never carries it as dust.
	if e.sealerFoldListAt(height) {
		w = 0
	}
	whole = append(whole, shares.Entry{Address: sealer, Weight: w})
	return whole, carry, nil
}

// sealerTermFirstForm says whether the block at height takes the sealer's
// term in its first form (over the accepted entries' count): true below
// the chain's SealerSharesV1 fork height, never without one.
func (e *Engine) sealerTermFirstForm(height uint64) bool {
	e.lock.Lock()
	f := e.forks
	e.lock.Unlock()
	return f != nil && f.SealerSharesV1 > 0 && height < f.SealerSharesV1
}

// ShareCredit is an address's share credit at a state (igneum_shareCredit).
func (e *Engine) ShareCredit(st *state.StateDB, addr common.Address) (uint64, error) {
	acct, err := e.shareAccount()
	if err != nil {
		return 0, err
	}
	return shares.CreditOf(st, acct, addr)
}

// VerifyShareList checks a block's payout list against the derived one
// (gate rows 2 and 3): the list the body carries must be ShareList's,
// entry for entry, in order, the sealer's entry last at the rule's weight
// to whatever address the block names there (no further check on it).
func (e *Engine) VerifyShareList(parentState *state.StateDB, header *types.Header, accepted []shares.Entry, withdrawals []*types.Withdrawal) error {
	if len(withdrawals) == 0 {
		return fmt.Errorf("%w: no list (the sealer's entry is owed)", errShareList)
	}
	sealer := withdrawals[len(withdrawals)-1].Address
	list, _, err := e.ShareList(parentState, header.Number.Uint64(), accepted, sealer)
	if err != nil {
		return err
	}
	if !shares.Equal(list, shares.FromWithdrawals(withdrawals)) {
		// The two lists by name, for the fault's reading (F-121, 14
		// October 2026: the second cut's activation refused every
		// sealer's own block with the counts equal).
		return fmt.Errorf("%w: %d entries carried, %d derived; carried %s; derived %s", errShareList, len(withdrawals), len(list), shares.Describe(shares.FromWithdrawals(withdrawals)), shares.Describe(list))
	}
	if err := e.verifyWithdrawalIndices(parentState, header.Number.Uint64(), withdrawals); err != nil {
		return fmt.Errorf("%w: %v", errShareList, err)
	}
	if err := e.verifyWithdrawalIndices(parentState, header.Number.Uint64(), withdrawals); err != nil {
		return fmt.Errorf("%w: %v", errShareList, err)
	}
	return nil
}

// SettleShares pays amount over the block's list under the lane and
// writes the new carry: Build's overflow first (the parent's carry minus
// what this list paid, plus the block's overflow, which the caller passes
// as carry), then the dust of this payment. Returns the dust so the
// caller can log it.
func (e *Engine) SettleShares(st *state.StateDB, amount *big.Int, list, carry []shares.Entry) ([]shares.Entry, error) {
	acct, err := e.shareAccount()
	if err != nil {
		return nil, err
	}
	paid, dust := shares.Pay(amount, list)
	for _, p := range paid {
		creditReward(st, p.Address, uint256.MustFromBig(p.Amount))
	}
	next := make([]shares.Entry, 0, len(carry)+len(dust))
	next = append(next, carry...)
	next = append(next, dust...)
	shares.WriteCarry(st, acct, next)
	return dust, nil
}

// payV2On says whether the block at height pays under the Pay rule's second
// form (ctypes.IgneumRuleForks.PayV2; zero is off).
func (e *Engine) payV2On(height uint64) bool {
	e.lock.Lock()
	f := e.forks
	e.lock.Unlock()
	return f != nil && f.PayV2 != 0 && height >= f.PayV2
}

// payQuorumWeight is Q of the second form: the lane's operating target
// sharesPerBlock at step weight 1 (the design seat's ruling: below Q the
// members' pay per unit of weight is a constant, three quarters of the
// amount over Q, so leaving a share out gains the sealer nothing; the
// dilution starts only above Q, in the saturation regime the admission
// bound owns). Read from the sortition document, never a second constant.
func (e *Engine) payQuorumWeight() uint64 {
	if s := e.config.Parts.Sortition; s != nil && s.SharesPerBlock > 0 {
		return s.SharesPerBlock
	}
	return 1
}

// PayV2Amounts is the Pay rule's second form as arithmetic (PayV2; F-63's
// fix (b)), the one computation Finalize credits from and igneum_blockIssuance
// reports from: the sealer's quarter of amount to the list's LAST entry's
// address (the sealer's own entry, appended last by ShareList, its weight
// ignored here); the pot, three quarters of amount scaled by min(W, Q) / Q
// over the summed weight W of the other entries and the quorum weight Q,
// paid pro rata by weight over them by shares.Pay (the rounding remainder
// to the last paid entry, a zero floor as dust); the rest of amount
// credited to nobody: unissued. A list of the sealer's entry alone (the
// plain form's twin) pays the quarter and nothing else.
type PayV2Amounts struct {
	Sealer   common.Address
	Quarter  *big.Int // the sealer's fixed term (zero under the fold)
	// Fold (F-165): the sealer's quarter folded into the members' pot;
	// Folded the quarter's amount so moved (the report's "moved part").
	Fold   bool
	Folded *big.Int
	Weight   uint64   // W, the listed entries' summed weight
	Quorum   uint64   // Q
	Pot      *big.Int // the listed entries' part before Pay's floors
	Paid     []shares.Payment
	Dust     []shares.Entry
	Credited *big.Int // the quarter plus every payment
	Unissued *big.Int // amount less Credited
}

// payV2Amounts computes the second form over amount and a derived list.
// Under the fold (F-165, the review's form of 19:3x UK, 15 October 2026:
// the security review's section 62, the design review's and the security review's lines) the
// sealer's quarter is folded into the members' pot: the quarter is zero,
// the whole amount is the pot scaled by min(W, Q) / Q over the members by
// weight, and the sealer is paid as a member on its own balanced standing
// (its key's entry, the third cut's block credit) or not at all; the tip
// is its premium under the burn. The sealer's last entry (the quarter's
// term) is then a name and nothing else.
func payV2Amounts(amount *big.Int, list []shares.Entry, q uint64, fold bool) PayV2Amounts {
	out := PayV2Amounts{Quarter: new(big.Int), Folded: new(big.Int), Pot: new(big.Int), Credited: new(big.Int), Unissued: new(big.Int), Quorum: q, Fold: fold}
	if amount == nil || amount.Sign() <= 0 || len(list) == 0 {
		out.Dust = append([]shares.Entry(nil), list...)
		return out
	}
	sealer := list[len(list)-1]
	members := list[:len(list)-1]
	out.Sealer = sealer.Address
	quarter := new(big.Int).Div(amount, big.NewInt(4))
	if fold {
		out.Folded = quarter
	} else {
		out.Quarter = quarter
	}
	out.Credited = new(big.Int).Set(out.Quarter)
	out.Weight = TotalWeight(members)
	w := out.Weight
	if q == 0 {
		q = 1
	}
	if w > q {
		w = q
	}
	if w > 0 && len(members) > 0 {
		out.Pot = new(big.Int).Sub(amount, out.Quarter)
		out.Pot.Mul(out.Pot, new(big.Int).SetUint64(w))
		out.Pot.Div(out.Pot, new(big.Int).SetUint64(q))
		out.Paid, out.Dust = shares.Pay(out.Pot, members)
		for _, p := range out.Paid {
			out.Credited.Add(out.Credited, p.Amount)
		}
	}
	out.Unissued = new(big.Int).Sub(amount, out.Credited)
	return out
}

// SettleSharesV2 credits the second form (payV2Amounts) at a state: the
// quarter to the sealer, every payment, the dust into the carry. Returns
// the dust and the unissued amount.
func (e *Engine) SettleSharesV2(st *state.StateDB, height uint64, amount *big.Int, list, carry []shares.Entry) (dust []shares.Entry, unissued *big.Int, err error) {
	a := payV2Amounts(amount, list, e.payQuorumWeight(), e.sealerFoldAt(height))
	if amount == nil || amount.Sign() <= 0 || len(list) == 0 {
		return a.Dust, a.Unissued, nil
	}
	acct, err := e.shareAccount()
	if err != nil {
		return nil, nil, err
	}
	if a.Quarter.Sign() > 0 {
		creditReward(st, a.Sealer, uint256.MustFromBig(a.Quarter))
	}
	for _, p := range a.Paid {
		creditReward(st, p.Address, uint256.MustFromBig(p.Amount))
	}
	next := make([]shares.Entry, 0, len(carry)+len(a.Dust))
	next = append(next, carry...)
	next = append(next, a.Dust...)
	shares.WriteCarry(st, acct, next)
	return a.Dust, a.Unissued, nil
}

// sealerFoldAt reports F-165's fold at a height: the vector registration's
// SealerFoldFrom reached and the minimum rule in force; false on a chain
// without the vector form.
func (e *Engine) sealerFoldAt(height uint64) bool {
	m := e.vectorMenu()
	return m != nil && m.SealerFoldAt(height)
}

// sealerFoldListAt reports the first pay form's fold at a height (F-180):
// the registration's SealerFoldListFrom reached and the minimum rule in
// force; false on a chain without the vector form.
func (e *Engine) sealerFoldListAt(height uint64) bool {
	m := e.vectorMenu()
	return m != nil && m.SealerFoldListAt(height)
}

// ShareCarry reads the carry at a state (igneum_shares, the record's read).
func (e *Engine) ShareCarry(st *state.StateDB) ([]shares.Entry, error) {
	acct, err := e.shareAccount()
	if err != nil {
		return nil, err
	}
	return shares.ReadCarry(st, acct)
}
