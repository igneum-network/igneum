// Package mosaicv2 is protocol B's independent implementation of the Mosaic-VF
// v0.2 gate 0 objects (docs/igneumhash/mosaic/v0.2/GATE0-VECTORS.md, text
// revision 1, the hash team's authority text over MOSAIC-VF-V0.2-SPEC.md
// sections 3, 6.1, 7, 8, 12 and 13), written from that text alone, never from
// the C tool or the Python port: the quota score, the quota transition, the
// continuity reservoir and the fee reservoir with its overflow. The launch
// chain's single-lane arithmetic (PLAN-TO-WIN.md point 7) is the same objects
// at one lane with the whole quota and a zero immediate fee fraction.
// Research objects, not production consensus; the research values are the
// text's and are named as such.
package mosaicv2

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
)

// Q is the quota unit (one ten-thousandth); the research weights A = 1, e = 0.02.
const (
	Q    uint64  = 10000
	ANum uint64  = 1
	ENum uint64  = 2
	EDen uint64  = 100
	AF   float64 = 1.0
	EF   float64 = 0.02
)

var ErrDocument = errors.New("mosaicv2: quota document refused")

// Document is the quota document of section 1.
type Document struct {
	Q []uint64 `json:"q"`
}

// V02 is the v0.2 document: four execution lanes at 23.75 percent, the sidecar at 5.
var V02 = Document{Q: []uint64{2375, 2375, 2375, 2375, 500}}

// SingleLane is the launch chain's document: one lane with the whole quota.
var SingleLane = Document{Q: []uint64{Q}}

// Validate refuses a document with a zero quota, fewer than one or more than
// eight lanes (the text's 2 to 8 for Mosaic; one admitted for the single lane), or a sum other than Q.
func (d Document) Validate() error {
	if len(d.Q) < 1 || len(d.Q) > 8 {
		return fmt.Errorf("%w: %d lanes", ErrDocument, len(d.Q))
	}
	var sum uint64
	for i, q := range d.Q {
		if q == 0 {
			return fmt.Errorf("%w: lane %d has quota 0", ErrDocument, i)
		}
		sum += q
	}
	if sum != Q {
		return fmt.Errorf("%w: quotas sum to %d, not %d", ErrDocument, sum, Q)
	}
	return nil
}

func u(v uint64) *big.Int { return new(big.Int).SetUint64(v) }

// MinLane is the smallest index i such that W_i * q_j <= W_j * q_i for every j (ties to the smallest index).
func MinLane(d Document, w []uint64) int {
	m := 0
	for i := 1; i < len(w); i++ {
		// i replaces m only when strictly smaller: W_i * q_m < W_m * q_i
		lhs := new(big.Int).Mul(u(w[i]), u(d.Q[m]))
		rhs := new(big.Int).Mul(u(w[m]), u(d.Q[i]))
		if lhs.Cmp(rhs) < 0 {
			m = i
		}
	}
	return m
}

// Score is the rational quota score: num = A_NUM * E_DEN * Q * W_m + E_NUM *
// q_m * sum(W), den = E_DEN * q_m, unreduced (section 2).
func Score(d Document, w []uint64) (m int, num, den *big.Int, err error) {
	if err = d.Validate(); err != nil {
		return
	}
	if len(w) != len(d.Q) {
		err = fmt.Errorf("%w: %d lanes of work against %d quotas", ErrDocument, len(w), len(d.Q))
		return
	}
	m = MinLane(d, w)
	sum := new(big.Int)
	for _, x := range w {
		sum.Add(sum, u(x))
	}
	num = new(big.Int).Mul(u(ANum*EDen*Q), u(w[m]))
	num.Add(num, new(big.Int).Mul(u(ENum*d.Q[m]), sum))
	den = u(EDen * d.Q[m])
	if num.BitLen() > 128 {
		err = fmt.Errorf("%w: the score's numerator does not fit 128 bits", ErrDocument)
	}
	return
}

// CompareScores compares two rationals by cross-multiplication: 1, 0 or -1.
func CompareScores(num1, den1, num2, den2 *big.Int) int {
	return new(big.Int).Mul(num1, den2).Cmp(new(big.Int).Mul(num2, den1))
}

// ScoreFloat is the floating form S_f = A * min_i(W_i * Q / q_i) + e * sum(W)
// in doubles, computed in the text's order: the minimum over the doubles
// W_i * Q / q_i in index order, then + 0.02 * sum(W).
func ScoreFloat(d Document, w []uint64) float64 {
	min := math.Inf(1)
	var sum float64
	for i, x := range w {
		v := float64(x) * float64(Q) / float64(d.Q[i])
		if v < min {
			min = v
		}
		sum += float64(x)
	}
	return AF*min + EF*sum
}

// ConcentrationMultiplier is (A + e) / (e * q_i / Q) in doubles in the package's order.
func ConcentrationMultiplier(q uint64) float64 { return (1.0 + 0.02) / (0.02 * (float64(q) / 10000.0)) }

// OutageRatio is e * (1 - q_j / Q) / (A + e) in doubles.
func OutageRatio(q uint64) float64 { return 0.02 * (1.0 - float64(q)/10000.0) / (1.0 + 0.02) }

// PortfolioAdvantage is 1 / sum_i(q_i / Q / a_i) over doubles in index order (spec section 4).
func PortfolioAdvantage(d Document, a []float64) float64 {
	var s float64
	for i, q := range d.Q {
		s += float64(q) / 10000.0 / a[i]
	}
	return 1 / s
}

// ---------------------------------------------------------------------------
// Section 3: the quota transition.

// Window is the activation window's research value.
const Window uint64 = 1000

// Entry is a menu entry (ratified, activation, document).
type Entry struct {
	Ratified   uint64   `json:"ratified"`
	Activation uint64   `json:"activation"`
	Document   Document `json:"document"`
}

// Validity processes the menu in list order and reports each entry's validity
// (entry 0 is genesis with activation 0 and is valid when its document is).
func Validity(menu []Entry, window uint64) []bool {
	out := make([]bool, len(menu))
	var latest uint64
	haveLatest := false
	for i, e := range menu {
		if i == 0 {
			out[0] = e.Activation == 0 && e.Document.Validate() == nil
			if out[0] {
				latest, haveLatest = 0, true
			}
			continue
		}
		ok := e.Activation%window == 0 && e.Activation >= e.Ratified+2*window && e.Document.Validate() == nil && haveLatest && e.Activation > latest
		out[i] = ok
		if ok {
			latest = e.Activation
		}
	}
	return out
}

// Active is the valid entry with the largest activation <= h (its index), or -1.
func Active(menu []Entry, valid []bool, h uint64) int {
	best := -1
	for i, e := range menu {
		if valid[i] && e.Activation <= h && (best < 0 || e.Activation >= menu[best].Activation) {
			best = i
		}
	}
	return best
}

// ---------------------------------------------------------------------------
// Section 4: the continuity reservoir.

// ReservoirParams are the text's research values by default.
type ReservoirParams struct {
	Emission   uint64 `json:"emission"`    // the block subsidy, research 1,000,000
	DRef       uint64 `json:"d_ref"`       // the reference intrinsic work, research 1,000,000
	Stable     uint64 `json:"stable"`      // the stable-cadence window in full-credit blocks, research 4
	ReleaseCap uint64 `json:"release_cap"` // per block, research 50,000
	Timeout    uint64 `json:"timeout"`     // chain blocks, research 200
}

var ResearchReservoir = ReservoirParams{Emission: 1_000_000, DRef: 1_000_000, Stable: 4, ReleaseCap: 50_000, Timeout: 200}

// LaneState is one lane's continuity state.
type LaneState struct {
	Balance uint64
	Weak    bool
	Table   map[uint64]uint64 // miner id to weak-interval work
	Stable  uint64
	Expiry  *uint64 // height, or none
}

// Reservoir is the continuity reservoir over a document.
type Reservoir struct {
	P     ReservoirParams
	D     Document
	Lanes []LaneState
}

func NewReservoir(p ReservoirParams, d Document) (*Reservoir, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	r := &Reservoir{P: p, D: d, Lanes: make([]LaneState, len(d.Q))}
	for i := range r.Lanes {
		r.Lanes[i].Table = map[uint64]uint64{}
	}
	return r, nil
}

// Pay is one attributed release.
type Pay struct {
	Miner  uint64
	Amount uint64
}

// Burn is one burned balance.
type Burn struct {
	Lane   int
	Amount uint64
}

// BlockRecord is the vector's per-block record (section 4).
type BlockRecord struct {
	H            uint64
	Lane         int
	Miner        uint64
	D            uint64
	B            uint64
	Imm          uint64
	Unpaid       uint64
	Released     []Pay
	Burned       []Burn
	BalanceAfter uint64
}

// Block applies the block at height h in lane i by miner x with intrinsic work d, in the text's order.
func (r *Reservoir) Block(h uint64, lane int, miner uint64, d uint64) (BlockRecord, error) {
	if lane < 0 || lane >= len(r.Lanes) {
		return BlockRecord{}, fmt.Errorf("%w: lane %d", ErrDocument, lane)
	}
	rec := BlockRecord{H: h, Lane: lane, Miner: miner, D: d, Released: []Pay{}, Burned: []Burn{}}
	// 1. expiry, every lane, before anything else
	for j := range r.Lanes {
		L := &r.Lanes[j]
		if L.Balance > 0 && !L.Weak && L.Expiry != nil && *L.Expiry <= h {
			rec.Burned = append(rec.Burned, Burn{j, L.Balance})
			L.Balance = 0
			L.Table = map[uint64]uint64{}
			L.Expiry = nil
		}
	}
	// 2. the lane budget and the immediate subsidy
	B := new(big.Int).Mul(u(r.P.Emission), u(r.D.Q[lane]))
	B.Div(B, u(Q))
	rec.B = B.Uint64()
	if d >= r.P.DRef {
		rec.Imm = rec.B
	} else {
		imm := new(big.Int).Mul(u(rec.B), u(d))
		imm.Div(imm, u(r.P.DRef))
		rec.Imm = imm.Uint64()
	}
	rec.Unpaid = rec.B - rec.Imm
	L := &r.Lanes[lane]
	if rec.Unpaid > 0 {
		// 3. a weak block
		if !L.Weak && L.Balance > 0 {
			// reading R-1: a new weak interval never inherits the old interval's attribution
			rec.Burned = append(rec.Burned, Burn{lane, L.Balance})
			L.Balance = 0
			L.Table = map[uint64]uint64{}
		}
		L.Weak = true
		L.Balance += rec.Unpaid
		L.Table[miner] += d
		L.Stable = 0
		L.Expiry = nil
	} else {
		// 4. a full-credit block
		if L.Weak {
			L.Weak = false
			L.Stable = 0
			e := h + r.P.Timeout
			L.Expiry = &e
		}
		if L.Balance > 0 {
			L.Stable++
			if L.Stable > r.P.Stable {
				rel := L.Balance
				if rel > r.P.ReleaseCap {
					rel = r.P.ReleaseCap
				}
				var total uint64
				ids := make([]uint64, 0, len(L.Table))
				for id, w := range L.Table {
					total += w
					ids = append(ids, id)
				}
				sort.Slice(ids, func(a, b int) bool { return ids[a] < ids[b] })
				for _, id := range ids {
					pay := new(big.Int).Mul(u(rel), u(L.Table[id]))
					pay.Div(pay, u(total))
					p := pay.Uint64()
					rec.Released = append(rec.Released, Pay{id, p})
					L.Balance -= p
				}
			}
		}
	}
	rec.BalanceAfter = L.Balance
	return rec, nil
}

// ---------------------------------------------------------------------------
// Section 5: the fee reservoir and the overflow.

type FeeParams struct {
	ImmPct     uint64 `json:"imm_pct"`     // the bounded immediate inclusion fraction, research 20 (0 on the launch chain)
	FeeCap     uint64 `json:"fee_cap"`     // per block, research 30,000
	FeeWindow  int    `json:"fee_window"`  // the rolling window in lane blocks, research 8
	FeeTimeout uint64 `json:"fee_timeout"` // chain blocks, research 40
}

var ResearchFees = FeeParams{ImmPct: 20, FeeCap: 30_000, FeeWindow: 8, FeeTimeout: 40}

type FeeLane struct {
	Balance uint64
	Last    *uint64
	Recent  []uint64
}

type FeeReservoir struct {
	P     FeeParams
	D     Document
	Lanes []FeeLane
}

func NewFeeReservoir(p FeeParams, d Document) (*FeeReservoir, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	if p.FeeWindow <= 0 || p.ImmPct > 100 {
		return nil, fmt.Errorf("%w: fee window %d, immediate fraction %d", ErrDocument, p.FeeWindow, p.ImmPct)
	}
	return &FeeReservoir{P: p, D: d, Lanes: make([]FeeLane, len(d.Q))}, nil
}

// FeeRecord is the vector's per-block record (section 5).
type FeeRecord struct {
	H               uint64
	Lane            int
	Assembler       uint64
	D               uint64
	F               uint64
	Imm             uint64
	Dust            uint64
	Shares          []uint64
	Due             uint64
	Rel             uint64
	Overflow        uint64
	FeeBalanceAfter uint64
	FeeBurned       []Burn
	AssemblerTotal  uint64
}

// Block applies the block at height h in lane i by assembler x with intrinsic work d and total fees f.
func (r *FeeReservoir) Block(h uint64, lane int, assembler uint64, d, f uint64) (FeeRecord, error) {
	if lane < 0 || lane >= len(r.Lanes) {
		return FeeRecord{}, fmt.Errorf("%w: lane %d", ErrDocument, lane)
	}
	rec := FeeRecord{H: h, Lane: lane, Assembler: assembler, D: d, F: f, FeeBurned: []Burn{}}
	// 1. inactivity
	for j := range r.Lanes {
		L := &r.Lanes[j]
		if L.Balance > 0 && L.Last != nil && *L.Last+r.P.FeeTimeout <= h {
			rec.FeeBurned = append(rec.FeeBurned, Burn{j, L.Balance})
			L.Balance = 0
		}
	}
	// 2. the immediate fraction, the quota shares, the dust
	imm := new(big.Int).Mul(u(f), u(r.P.ImmPct))
	imm.Div(imm, u(100))
	rec.Imm = imm.Uint64()
	R := f - rec.Imm
	var sumShares uint64
	rec.Shares = make([]uint64, len(r.Lanes))
	for j := range r.Lanes {
		s := new(big.Int).Mul(u(R), u(r.D.Q[j]))
		s.Div(s, u(Q))
		rec.Shares[j] = s.Uint64()
		r.Lanes[j].Balance += rec.Shares[j]
		sumShares += rec.Shares[j]
	}
	rec.Dust = R - sumShares
	// 3. the lane's release under the cap
	L := &r.Lanes[lane]
	L.Recent = append(L.Recent, d)
	if len(L.Recent) > r.P.FeeWindow {
		L.Recent = L.Recent[len(L.Recent)-r.P.FeeWindow:]
	}
	var w uint64
	for _, x := range L.Recent {
		w += x
	}
	due := new(big.Int).Mul(u(L.Balance), u(d))
	due.Div(due, u(w))
	rec.Due = due.Uint64()
	rec.Rel = rec.Due
	if rec.Rel > r.P.FeeCap {
		rec.Rel = r.P.FeeCap
	}
	rec.Overflow = rec.Due - rec.Rel
	L.Balance -= rec.Rel
	hh := h
	L.Last = &hh
	rec.FeeBalanceAfter = L.Balance
	rec.AssemblerTotal = rec.Imm + rec.Dust + rec.Rel
	return rec, nil
}

// ---------------------------------------------------------------------------
// Section 6: the generator (xorshift64*) and the vector chain's shape.

type Xorshift struct{ S uint64 }

func (x *Xorshift) Next() uint64 {
	x.S ^= x.S >> 12
	x.S ^= x.S << 25
	x.S ^= x.S >> 27
	return x.S * 0x2545F4914F6CDD1D
}
