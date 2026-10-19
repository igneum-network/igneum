// Package lanes is the Mosaic-VF research rule for issuance over lanes
// (docs/igneumhash/mosaic/MOSAIC-VF-SPEC.md section 8): a separate emission
// reservoir per lane, a block in lane i drawing only from lane i's reservoir,
// unused issuance never redistributed immediately, with the four options for
// unused issuance and the three fee-capture controls as selectable rules.
// Research only, behind Policy.Enabled: the engine's reward path does not call
// it; ecosystem's simulator and the tests replay it over a lane sequence.
//
// The block subsidy of a height comes from the issuance schedule (the miner's
// part after the 99/1 split); the lanes share it by LaneBps. Every height
// credits every lane's reservoir with its share whether or not that lane
// produced the block; the block's lane draws from its own reservoir by the
// unused-issuance rule; the other lanes' credits stay in their reservoirs.
package lanes

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/consensus/igneum/issuance"
)

// Rule is the treatment of unused issuance in a lane (section 8's options).
type Rule uint8

const (
	// CarryForward: a lane's reservoir accumulates; its next block draws the whole reservoir.
	CarryForward Rule = iota
	// NeverIssue: a block draws its lane's share of its own height only; the rest of the reservoir is never issued.
	NeverIssue
	// SlowRelease: a block draws its share of its own height plus ReleaseBps of the carried balance (the Phase B default).
	SlowRelease
	// Divert: carry forward, and a lane silent for TimeoutBlocks has its reservoir diverted by the
	// precommitted rule: split equally into the other lanes' reservoirs.
	Divert
)

func (r Rule) String() string {
	switch r {
	case CarryForward:
		return "carry-forward"
	case NeverIssue:
		return "never-issue"
	case SlowRelease:
		return "slow-release"
	case Divert:
		return "divert-after-timeout"
	}
	return fmt.Sprintf("rule(%d)", uint8(r))
}

// FeeRule is a fee-capture control (section 8's candidate controls).
type FeeRule uint8

const (
	// FeesToAssembler: the block's fees go to its assembler in full (the control's baseline).
	FeesToAssembler FeeRule = iota
	// FeesFixedAssembler: AssemblerBps of the fees to the assembler, the rest into the common smoothing pool.
	FeesFixedAssembler
	// FeesSmoothed: all fees into the common pool; every block pays out pool / SmoothingBlocks.
	FeesSmoothed
	// FeesLaneReservoirs: all fees into the block's lane fee reservoir; the lane's blocks draw by the issuance rule.
	FeesLaneReservoirs
)

func (f FeeRule) String() string {
	switch f {
	case FeesToAssembler:
		return "fees-to-assembler"
	case FeesFixedAssembler:
		return "fees-fixed-assembler"
	case FeesSmoothed:
		return "fees-smoothed"
	case FeesLaneReservoirs:
		return "fees-lane-reservoirs"
	}
	return fmt.Sprintf("fee-rule(%d)", uint8(f))
}

// Policy is the selectable research policy.
type Policy struct {
	Enabled         bool     `json:"enabled"`
	LaneBps         []uint32 `json:"lane_bps"`         // one share per lane, summing to 10,000
	Rule            Rule     `json:"rule"`             // unused issuance
	ReleaseBps      uint32   `json:"release_bps"`      // SlowRelease: the carried balance's part released per block
	TimeoutBlocks   uint64   `json:"timeout_blocks"`   // Divert: heights of silence before a lane's reservoir is diverted
	FeeRule         FeeRule  `json:"fee_rule"`         // fee capture
	AssemblerBps    uint32   `json:"assembler_bps"`    // FeesFixedAssembler: the assembler's fixed part
	SmoothingBlocks uint64   `json:"smoothing_blocks"` // FeesSmoothed: the pool's payout divisor
	// AllowRejectedControls admits the rules the founder rejected (divert-after-timeout) for tests that
	// keep them as controls; never set outside a test.
	AllowRejectedControls bool `json:"allow_rejected_controls,omitempty"`
}

var ErrPolicy = errors.New("lanes: policy rejected")

// Validate checks the policy's shape.
func (p Policy) Validate() error {
	if len(p.LaneBps) == 0 || len(p.LaneBps) > 64 {
		return fmt.Errorf("%w: %d lanes (1 to 64)", ErrPolicy, len(p.LaneBps))
	}
	var sum uint32
	for _, b := range p.LaneBps {
		sum += b
	}
	if sum != 10_000 {
		return fmt.Errorf("%w: lane shares sum to %d basis points, not 10,000", ErrPolicy, sum)
	}
	switch {
	case p.Rule > Divert:
		return fmt.Errorf("%w: rule %d", ErrPolicy, p.Rule)
	case p.Rule == SlowRelease && (p.ReleaseBps == 0 || p.ReleaseBps > 10_000):
		return fmt.Errorf("%w: release_bps %d (1 to 10,000)", ErrPolicy, p.ReleaseBps)
	case p.Rule == Divert && !p.AllowRejectedControls:
		// the founder's Phase B decision (lead/mosaic-3) and v0.2 section 7 rule 7: unused issuance is burned
		// or stays unissued after the timeout, never diverted; the option stays only as a rejected control in tests
		return fmt.Errorf("%w: divert-after-timeout is a rejected control (Phase B decision), not a selectable rule", ErrPolicy)
	case p.Rule == Divert && p.TimeoutBlocks == 0:
		return fmt.Errorf("%w: timeout_blocks 0", ErrPolicy)
	case p.Rule == Divert && len(p.LaneBps) < 2:
		return fmt.Errorf("%w: divert needs two lanes", ErrPolicy)
	case p.FeeRule > FeesLaneReservoirs:
		return fmt.Errorf("%w: fee rule %d", ErrPolicy, p.FeeRule)
	case p.FeeRule == FeesFixedAssembler && p.AssemblerBps > 10_000:
		return fmt.Errorf("%w: assembler_bps %d", ErrPolicy, p.AssemblerBps)
	case (p.FeeRule == FeesFixedAssembler || p.FeeRule == FeesSmoothed) && p.SmoothingBlocks == 0:
		return fmt.Errorf("%w: smoothing_blocks 0", ErrPolicy)
	}
	return nil
}

// Lanes is the lane count.
func (p Policy) Lanes() int { return len(p.LaneBps) }

// State is the replayable state after a height: the reservoirs, the fee
// reservoirs and pool, the last height each lane produced, and the totals.
type State struct {
	Height       uint64
	Reservoir    []*big.Int // issuance per lane
	FeeReservoir []*big.Int // FeesLaneReservoirs per lane
	FeePool      *big.Int   // FeesFixedAssembler and FeesSmoothed
	LastBlock    []uint64   // the last height of each lane's block (0: none yet)
	Issued       *big.Int   // paid to miners so far (subsidy only)
	NeverIssued  *big.Int   // burned under NeverIssue
	Diverted     *big.Int   // moved between lanes under Divert
	FeesPaid     *big.Int
}

// Payout is one block's result.
type Payout struct {
	Height    uint64
	Lane      int
	Subsidy   *big.Int // from the lane's reservoir
	Fees      *big.Int // to the assembler after the fee rule
	Reservoir *big.Int // the lane's reservoir after the draw
}

// Replay applies the policy over a schedule; Next is the engine-shaped step.
type Replay struct {
	P     Policy
	S     *issuance.Table
	State State
}

// New starts a replay at genesis (height 0, every reservoir empty).
func New(p Policy, s *issuance.Table) (*Replay, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	n := p.Lanes()
	st := State{Reservoir: make([]*big.Int, n), FeeReservoir: make([]*big.Int, n), FeePool: new(big.Int), LastBlock: make([]uint64, n),
		Issued: new(big.Int), NeverIssued: new(big.Int), Diverted: new(big.Int), FeesPaid: new(big.Int)}
	for i := range st.Reservoir {
		st.Reservoir[i], st.FeeReservoir[i] = new(big.Int), new(big.Int)
	}
	return &Replay{P: p, S: s, State: st}, nil
}

func bps(x *big.Int, b uint32) *big.Int {
	return new(big.Int).Div(new(big.Int).Mul(x, big.NewInt(int64(b))), big.NewInt(10_000))
}

// Next applies the block at the next height in lane with the given fees and
// returns its payout. The miner's part of the schedule's subsidy is shared
// into every reservoir by LaneBps (the last lane takes the rounding
// remainder, so the shares sum exactly); the block's lane draws by the rule.
func (r *Replay) Next(lane int, fees *big.Int) (Payout, error) {
	if !r.P.Enabled {
		return Payout{}, fmt.Errorf("%w: policy disabled", ErrPolicy)
	}
	if lane < 0 || lane >= r.P.Lanes() {
		return Payout{}, fmt.Errorf("%w: lane %d of %d", ErrPolicy, lane, r.P.Lanes())
	}
	st := &r.State
	st.Height++
	h := st.Height
	miner, _ := r.S.Reward(h)
	// credit every lane
	rest := new(big.Int).Set(miner)
	for i := range st.Reservoir {
		var share *big.Int
		if i == len(st.Reservoir)-1 {
			share = rest
		} else {
			share = bps(miner, r.P.LaneBps[i])
			rest = new(big.Int).Sub(rest, share)
		}
		st.Reservoir[i].Add(st.Reservoir[i], share)
	}
	// the draw by the rule
	own := shareOfHeight(miner, r.P.LaneBps, lane) // the lane's credit of this height
	var draw *big.Int
	switch r.P.Rule {
	case CarryForward, Divert:
		draw = new(big.Int).Set(st.Reservoir[lane])
	case NeverIssue:
		draw = own
		// the other lanes' credits of this height are never issued either: a lane draws only its own
		// height's share, so everything else in every reservoir is burned at once
		for i := range st.Reservoir {
			burn := new(big.Int).Set(st.Reservoir[i])
			if i == lane {
				burn.Sub(burn, draw)
			}
			st.NeverIssued.Add(st.NeverIssued, burn)
			st.Reservoir[i].SetInt64(0)
		}
		st.Reservoir[lane].Set(draw)
	case SlowRelease:
		carry := new(big.Int).Sub(st.Reservoir[lane], own)
		draw = new(big.Int).Add(own, bps(carry, r.P.ReleaseBps))
	}
	if draw.Cmp(st.Reservoir[lane]) > 0 {
		draw.Set(st.Reservoir[lane])
	}
	st.Reservoir[lane].Sub(st.Reservoir[lane], draw)
	st.Issued.Add(st.Issued, draw)
	st.LastBlock[lane] = h
	// Divert: a lane silent for TimeoutBlocks loses its reservoir to the others, equally (precommitted)
	if r.P.Rule == Divert {
		for i := range st.Reservoir {
			last := st.LastBlock[i]
			if i == lane || h-last < r.P.TimeoutBlocks || st.Reservoir[i].Sign() == 0 {
				continue
			}
			amount := new(big.Int).Set(st.Reservoir[i])
			st.Reservoir[i].SetInt64(0)
			others := int64(len(st.Reservoir) - 1)
			each := new(big.Int).Div(amount, big.NewInt(others))
			remainder := new(big.Int).Sub(amount, new(big.Int).Mul(each, big.NewInt(others)))
			for j := range st.Reservoir {
				if j == i {
					continue
				}
				st.Reservoir[j].Add(st.Reservoir[j], each)
			}
			st.Reservoir[lane].Add(st.Reservoir[lane], remainder)
			st.Diverted.Add(st.Diverted, amount)
			st.LastBlock[i] = h // the timeout restarts
		}
	}
	// fees
	paid := new(big.Int)
	switch r.P.FeeRule {
	case FeesToAssembler:
		paid.Set(fees)
	case FeesFixedAssembler:
		paid = bps(fees, r.P.AssemblerBps)
		st.FeePool.Add(st.FeePool, new(big.Int).Sub(fees, paid))
		out := new(big.Int).Div(st.FeePool, new(big.Int).SetUint64(r.P.SmoothingBlocks))
		st.FeePool.Sub(st.FeePool, out)
		paid.Add(paid, out)
	case FeesSmoothed:
		st.FeePool.Add(st.FeePool, fees)
		out := new(big.Int).Div(st.FeePool, new(big.Int).SetUint64(r.P.SmoothingBlocks))
		st.FeePool.Sub(st.FeePool, out)
		paid.Set(out)
	case FeesLaneReservoirs:
		st.FeeReservoir[lane].Add(st.FeeReservoir[lane], fees)
		switch r.P.Rule {
		case SlowRelease:
			paid = bps(st.FeeReservoir[lane], r.P.ReleaseBps)
		default:
			paid.Set(st.FeeReservoir[lane])
		}
		st.FeeReservoir[lane].Sub(st.FeeReservoir[lane], paid)
	}
	st.FeesPaid.Add(st.FeesPaid, paid)
	return Payout{Height: h, Lane: lane, Subsidy: draw, Fees: paid, Reservoir: new(big.Int).Set(st.Reservoir[lane])}, nil
}

// shareOfHeight is lane i's exact credit of a height's miner part (the last lane takes the remainder).
func shareOfHeight(miner *big.Int, laneBps []uint32, i int) *big.Int {
	if i < len(laneBps)-1 {
		return bps(miner, laneBps[i])
	}
	rest := new(big.Int).Set(miner)
	for j := 0; j < len(laneBps)-1; j++ {
		rest.Sub(rest, bps(miner, laneBps[j]))
	}
	return rest
}

// Reservoirs returns copies of the issuance reservoirs.
func (r *Replay) Reservoirs() []*big.Int {
	out := make([]*big.Int, len(r.State.Reservoir))
	for i, v := range r.State.Reservoir {
		out[i] = new(big.Int).Set(v)
	}
	return out
}

// MinerTotal is the schedule's miner part summed over heights 1..h: the conservation bound.
func MinerTotal(s *issuance.Table, h uint64) *big.Int {
	total := new(big.Int)
	for i := uint64(1); i <= h; i++ {
		m, _ := s.Reward(i)
		total.Add(total, m)
	}
	return total
}
