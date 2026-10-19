// The Igneum block-subsidy schedule and the 99/1 split
// (the plan, pages 19 and 20). It is replayable from code with integer
// arithmetic only; tools/issuance/issuance.py is the same rule in Python and
// writes this package's golden vectors.
//
// The rule, with the candidate constants in docs/protocol-b/DESIGN.md:
//
//	epoch e      covers heights e*Epoch+1 .. (e+1)*Epoch; genesis issues nothing
//	S_e          gross issued supply at the start of epoch e (sum of every
//	             subsidy paid before it; fees and burns play no part)
//	subsidy(e)   max(Tail, (M - S_e) >> K), constant inside the epoch
//	fund         subsidy * FundBps / 10000 (floor); miner = subsidy - fund
//
// Tail > 0 is the declining schedule into the small fixed tail. Tail == 0
// is the hard-cap branch: the same code, the gross supply never reaches M
// (it stops within 2^K base units of it). The engine (consensus/igneum,
// protocol engineer A) calls Reward(height) in Finalize and credits the two
// amounts to the block's coinbase and the maintenance fund address. Uncles
// receive nothing under this schedule (DESIGN.md, PB-02), so the gross
// supply at any height is exactly Issued(height).
package issuance

import (
	"fmt"
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum/common"
)

// Params are consensus constants (IgneumConfig, PA-08); never changed after genesis.
type Params struct {
	Epoch   uint64   // blocks per issuance epoch
	M       *big.Int // asymptote of the declining phase, base units
	K       uint     // shift: each epoch issues (M - S_e) / 2^K per block (the shift form; EpochDiv 0)
	Tail    *big.Int // fixed tail subsidy per block, base units; 0 selects the hard cap
	FundBps uint64   // maintenance fund share of the subsidy, basis points
	// FundAddress is the address the fund's part is paid to (the first
	// form's; the fund is one across every gate). Check refuses the zero
	// address with a fund share set on a chain of record (F-734); a lab
	// chain id may run without one (the fund's part paid to the zero
	// address, a throwaway chain's).
	FundAddress common.Address
	// EpochDiv selects the re-cut schedule's form (the launch schedule;
	// the project's ruling of 14:0x UK, 11 October 2026): each epoch issues the
	// AMOUNT A_e = (M - S_e) / EpochDiv (floor division), paid as
	// floor(A_e / Epoch) per block with the remainder A_e mod Epoch in the
	// epoch's LAST block; the epoch's subsidy is max(TailPerEpoch, A_e),
	// so the tail is the calendar quantity in integer form (2,048 IGN an
	// epoch at launch: 525,600 IGN a year at 20 s, one third of an IGN a
	// block on average, the remainder 2,048 wei in the last block; the project's
	// correction of 14:2x UK) and Tail (per block) is not read. Zero keeps
	// the shift form above, where K is the per-block shift, Tail the
	// per-block tail and no remainder exists.
	EpochDiv     uint64
	TailPerEpoch *big.Int
	// NextFrom and Next are the height-gated re-cut (the founder's "re cut"
	// for the 12-second target, 16 October 2026; the economics team's
	// ISSUANCE-12S-2026-10-16.md): from the block above NextFrom the
	// schedule is Next, whose first epoch starts with the gross issued at
	// NextFrom carried whole (S carried across the gate), so the time
	// curve holds and no block is issued twice or not at all. Zero or nil:
	// this form for ever. The gate is any height under the shift form (a
	// block's subsidy is whole, nothing is cut) and an epoch's last height
	// under the re-cut form (EpochDiv > 0: the epoch's remainder sits in
	// its last block, so a gate inside an epoch would lose it); Check
	// refuses a gate elsewhere.
	NextFrom uint64
	Next     *Params
}

// Check refuses a schedule whose gate falls where an epoch's issuance would
// be cut, a gate not above its form's start, and (F-734, the review lead's reading
// of 18 October 2026) a fund share paid to NO address: FundBps above zero
// with the zero fund address on a chain of record, where the fund's part
// would be burned to the zero address for ever (the launch placeholder's
// shape until the launch genesis names the fund). `lab` is the caller's
// word that the chain id is a lab chain (params.IsIgneumResearchChainID:
// a throwaway chain from a fresh genesis), which stays allowed without a
// fund address. Nil for a schedule with no fault.
func Check(p Params, lab bool) error {
	if p.FundBps > 0 && p.FundAddress == (common.Address{}) && !lab {
		return fmt.Errorf("issuance: a fund share of %d bps is paid to no address (the zero fund address) on a chain of record", p.FundBps)
	}
	q, base := &p, uint64(0)
	for q.Next != nil {
		if q.NextFrom <= base {
			return fmt.Errorf("issuance: the next form's gate %d is not above its form's start %d", q.NextFrom, base)
		}
		if q.EpochDiv > 0 && (q.NextFrom-base)%q.Epoch != 0 {
			return fmt.Errorf("issuance: the next form's gate %d is inside an epoch of the re-cut form (epochs of %d from %d): the epoch's remainder would be lost", q.NextFrom, q.Epoch, base)
		}
		if q.Next.Epoch == 0 {
			return fmt.Errorf("issuance: the next form from %d has no epoch length", q.NextFrom)
		}
		q, base = q.Next, q.NextFrom
	}
	return nil
}

// epochAmount is A_e of the re-cut form: the whole epoch's issuance when
// it starts at `issued` gross supply.
func (p Params) epochAmount(issued *big.Int) *big.Int {
	a := new(big.Int)
	if issued.Cmp(p.M) < 0 {
		a.Sub(p.M, issued)
		a.Div(a, new(big.Int).SetUint64(p.EpochDiv))
	}
	return a
}

// epochBlocks is the per-block subsidy and the last block's remainder of
// an epoch under the re-cut form: the epoch's subsidy max(TailPerEpoch,
// A_e), floor-divided, the remainder in the last block (the tail's epoch
// too).
func (p Params) epochBlocks(issued *big.Int) (perBlock, remainder *big.Int) {
	a := p.epochAmount(issued)
	if p.TailPerEpoch != nil && a.Cmp(p.TailPerEpoch) < 0 {
		a = new(big.Int).Set(p.TailPerEpoch)
	}
	perBlock = new(big.Int).Div(a, new(big.Int).SetUint64(p.Epoch))
	remainder = new(big.Int).Sub(a, new(big.Int).Mul(perBlock, new(big.Int).SetUint64(p.Epoch)))
	return perBlock, remainder
}

var unit = new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)

func ign(whole int64) *big.Int { return new(big.Int).Mul(big.NewInt(whole), unit) }

// Candidate is the tail branch proposed in docs/protocol-b/DESIGN.md:
// epoch 8192 blocks, M = 100,000,000 IGN, K = 24, tail 0.25 IGN, fund 1 percent.
var Candidate = Params{
	Epoch:   8192,
	M:       ign(100_000_000),
	K:       24,
	Tail:    new(big.Int).Div(unit, big.NewInt(4)),
	FundBps: 100,
}

// HardCap is the founder's alternative branch: the same schedule with no tail.
var HardCap = Params{Epoch: 8192, M: ign(100_000_000), K: 24, Tail: new(big.Int), FundBps: 100}

// SubsidyForSupply is the per-block subsidy of an epoch that starts with
// `issued` gross supply.
func (p Params) SubsidyForSupply(issued *big.Int) *big.Int {
	if p.EpochDiv > 0 {
		r, _ := p.epochBlocks(issued)
		return r
	}
	r := new(big.Int)
	if issued.Cmp(p.M) < 0 {
		r.Sub(p.M, issued)
		r.Rsh(r, p.K)
	}
	if r.Cmp(p.Tail) < 0 {
		r.Set(p.Tail)
	}
	return r
}

// Split divides a subsidy into the miner's and the fund's parts; they sum
// to the subsidy exactly.
func (p Params) Split(subsidy *big.Int) (miner, fund *big.Int) {
	fund = new(big.Int).Mul(subsidy, new(big.Int).SetUint64(p.FundBps))
	fund.Div(fund, big.NewInt(10_000))
	miner = new(big.Int).Sub(subsidy, fund)
	return miner, fund
}

// EpochOf is the epoch of a height >= 1 under this form counted from the
// chain's start (the first form's epochs; a table's own epochs past a gate
// count from the landing check, Table.epochOf).
func (p Params) EpochOf(height uint64) uint64 {
	return (height - 1) / p.Epoch
}

// Table is the schedule as a replayable, memoised epoch table. It is safe
// for concurrent use. A schedule with a next form is a chain of tables: the
// first form's up to its gate, the next form's above it (base the gate
// height, starts[0] the gross issued at the landing check, fundBase the fund issued
// at the landing check), every public read dispatching on the height.
type Table struct {
	p        Params
	base     uint64 // the height this form's epoch 0 starts above (0 for the chain's first form)
	mu       sync.Mutex
	starts   []*big.Int // starts[e] = gross issued at the start of this form's epoch e
	fundBase *big.Int   // the fund issued by the forms before this one (zero for the first)
	next     *Table     // the form in force above p.NextFrom (nil: this form for ever)
}

// New returns a schedule for the parameters (Check first where the
// parameters come from a chain's config: a misplaced gate is refused there,
// never silently moved).
func New(p Params) *Table {
	return newFrom(p, 0, new(big.Int), new(big.Int))
}

func newFrom(p Params, base uint64, issued, fund *big.Int) *Table {
	s := &Table{p: p, base: base, starts: []*big.Int{new(big.Int).Set(issued)}, fundBase: new(big.Int).Set(fund)}
	if p.Next != nil && p.NextFrom > base {
		// The next form carries the gross issued and the fund issued at
		// the gate whole: its epoch 0 starts above the gate at S(gate).
		s.next = newFrom(*p.Next, p.NextFrom, s.issuedOwn(p.NextFrom), new(big.Int).Add(fund, s.fundIssuedOwn(p.NextFrom)))
	}
	return s
}

// Params returns the first form's constants.
func (s *Table) Params() Params { return s.p }

// ParamsAt returns the constants of the form in force at height.
func (s *Table) ParamsAt(height uint64) Params { return s.at(height).p }

// at is the table of the form in force at height.
func (s *Table) at(height uint64) *Table {
	if s.next != nil && height > s.p.NextFrom {
		return s.next.at(height)
	}
	return s
}

// epochOf is this form's epoch of a height above its base.
func (s *Table) epochOf(height uint64) uint64 {
	return (height - s.base - 1) / s.p.Epoch
}

func (s *Table) extend(e uint64) {
	for uint64(len(s.starts)) <= e {
		last := s.starts[len(s.starts)-1]
		r := s.p.SubsidyForSupply(last)
		next := new(big.Int).Mul(r, new(big.Int).SetUint64(s.p.Epoch))
		if s.p.EpochDiv > 0 {
			// The re-cut form: the epoch issues A_e exactly (the
			// remainder in its last block).
			_, rem := s.p.epochBlocks(last)
			next.Add(next, rem)
		}
		next.Add(next, last)
		s.starts = append(s.starts, next)
	}
}

// EpochState returns the gross issued supply at the start of epoch e and
// the per-block subsidy inside it.
func (s *Table) EpochState(e uint64) (issuedAtStart, subsidy *big.Int) {
	s.mu.Lock()
	s.extend(e)
	start := new(big.Int).Set(s.starts[e])
	s.mu.Unlock()
	return start, s.p.SubsidyForSupply(start)
}

// Subsidy is the total subsidy of the block at height (0 for genesis), under
// the form in force at the height.
func (s *Table) Subsidy(height uint64) *big.Int {
	if height == 0 {
		return new(big.Int)
	}
	t := s.at(height)
	e := t.epochOf(height)
	start, r := t.EpochState(e)
	if t.p.EpochDiv > 0 && height == t.base+(e+1)*t.p.Epoch {
		// The epoch's last block carries the remainder.
		_, rem := t.p.epochBlocks(start)
		r = new(big.Int).Add(r, rem)
	}
	return r
}

// Reward is the engine's call: the miner's and the fund's amounts for the
// block at height.
func (s *Table) Reward(height uint64) (miner, fund *big.Int) {
	return s.p.Split(s.Subsidy(height))
}

// Issued is the gross issued supply after the block at height: the sum of
// every subsidy from height 1 to height inclusive, across the forms.
func (s *Table) Issued(height uint64) *big.Int {
	if height == 0 {
		return new(big.Int)
	}
	return s.at(height).issuedOwn(height)
}

// issuedOwn is the gross issued after height under this form alone (the
// form's carried start plus its own issuance; the start itself at or under
// the base).
func (s *Table) issuedOwn(height uint64) *big.Int {
	if height <= s.base {
		s.mu.Lock()
		defer s.mu.Unlock()
		return new(big.Int).Set(s.starts[0])
	}
	e := s.epochOf(height)
	start, r := s.EpochState(e)
	inEpoch := height - s.base - e*s.p.Epoch // 1 .. Epoch
	out := new(big.Int).Mul(r, new(big.Int).SetUint64(inEpoch))
	if s.p.EpochDiv > 0 && inEpoch == s.p.Epoch {
		_, rem := s.p.epochBlocks(start)
		out.Add(out, rem)
	}
	return out.Add(out, start)
}

// FundIssued is the fund's cumulative share after the block at height,
// across the forms.
func (s *Table) FundIssued(height uint64) *big.Int {
	if height == 0 {
		return new(big.Int)
	}
	t := s.at(height)
	return new(big.Int).Add(t.fundBase, t.fundIssuedOwn(height))
}

// fundIssuedOwn is the fund's share issued by this form alone from its base
// to height inclusive.
func (s *Table) fundIssuedOwn(height uint64) *big.Int {
	total := new(big.Int)
	if height <= s.base {
		return total
	}
	e := s.epochOf(height)
	for i := uint64(0); i < e; i++ {
		_, r := s.EpochState(i)
		_, f := s.p.Split(r)
		total.Add(total, f.Mul(f, new(big.Int).SetUint64(s.p.Epoch)))
		if s.p.EpochDiv > 0 {
			// The last block's remainder splits on its own.
			total.Add(total, s.lastBlockExtraFund(i))
		}
	}
	_, r := s.EpochState(e)
	_, f := s.p.Split(r)
	total.Add(total, f.Mul(f, new(big.Int).SetUint64(height-s.base-e*s.p.Epoch)))
	if s.p.EpochDiv > 0 && height == s.base+(e+1)*s.p.Epoch {
		total.Add(total, s.lastBlockExtraFund(e))
	}
	return total
}

// lastBlockExtraFund is the fund's share of an epoch's last block beyond
// the per-block share (the remainder's own split; the per-block subsidy
// and the remainder split apart, so the sum is the block's split).
func (s *Table) lastBlockExtraFund(e uint64) *big.Int {
	start, r := s.EpochState(e)
	_, rem := s.p.epochBlocks(start)
	_, fWhole := s.p.Split(new(big.Int).Add(r, rem))
	_, fPer := s.p.Split(r)
	return new(big.Int).Sub(fWhole, fPer)
}

// Transition returns the first epoch whose subsidy is the tail (the epoch
// counted within its form) and the first height paid at the tail; ok is
// false on the hard-cap branch. A form with a next form answers from the
// next where the tail comes above the gate.
func (s *Table) Transition() (epoch, firstTailHeight uint64, ok bool) {
	if s.p.Tail.Sign() == 0 {
		if s.next != nil {
			return s.next.Transition()
		}
		return 0, 0, false
	}
	for e := uint64(0); ; e++ {
		first := s.base + e*s.p.Epoch + 1
		if s.next != nil && first > s.p.NextFrom {
			return s.next.Transition()
		}
		_, r := s.EpochState(e)
		if r.Cmp(s.p.Tail) == 0 {
			return e, first, true
		}
	}
}
