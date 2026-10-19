// Package mosaic is the research reference of Mosaic-VF's consensus objects
// (docs/igneumhash/mosaic/PHASE-A-CONSENSUS.md; MOSAIC-VF-SPEC.md section
// 3): the lane table and the per-lane target state, the canonical intrinsic
// work of a block from its lane's target, the normalisation from consensus
// difficulty state alone, the height-based scoring epochs, the epoch score
// S = A x min + e x sum and the chain score with its deterministic tie
// break, and the succession rules on the bounded menu's machinery.
//
// Research only: nothing here is reached by the engine, the sealer, the
// fork choice or the RPC; the package is pure functions over big integers
// so that a second implementation in any language reproduces the vectors
// (M10, M15). No production code path exists until Phase D decides one.
package mosaic

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
	"lukechampine.com/blake3"
)

// ResearchOnly marks the package: no consensus path reads it.
const ResearchOnly = true

// NormScale is the fixed-point scale of normalised work: one unit of a
// lane's reference work is NormScale (2^64).
var NormScale = new(big.Int).Lsh(big.NewInt(1), 64)

var (
	two256    = new(big.Int).Lsh(big.NewInt(1), 256)
	maxTarget = new(big.Int).Sub(two256, big.NewInt(1))
)

var (
	ErrTarget   = errors.New("mosaic: target out of range")
	ErrLane     = errors.New("mosaic: lane out of range")
	ErrNoRef    = errors.New("mosaic: the reference work is zero")
	ErrEpochLen = errors.New("mosaic: the epoch length is zero")
)

// Lane is one row of the chain's lane table: the lane's algorithm and
// parameter document by version, under the bounded menu's schedule rules
// (section 7: one lane changes at a time, ratified two activations ahead).
type Lane struct {
	ID        uint8                  // the lane identifier a block declares
	Algorithm string                 // the registered proof-of-work algorithm
	Versions  []LaneVersion          // the parameter versions in activation order
	Bootstrap *big.Int               // the difficulty at the lane's activation
	Schedule  *ctypes.IgneumSchedule // the parameter documents by height (the menu machinery)
}

// LaneVersion is one parameter version of a lane and its activation height.
// Version 0 is the Phase D control slot; 1 and above are candidate slots.
type LaneVersion struct {
	Version     uint32
	Height      uint64
	CandidateID string      // the candidate's registered name (igneumhash, rxe1, forgeb1, kawpow-0.9.4, ...)
	ParamsID    common.Hash // the candidate's own parameter id (P, pid; zero for a candidate without a document)
	ProofBytes  uint64      // MaxProofBytes for the version
}

// LaneDomain frames the lane table's key (the hash team's register,
// docs/igneumhash/mosaic/LANES.md): one length byte then the string.
const LaneDomain = "Mosaic/lane/v0"

// LanePID is the lane table's key for a (lane, version) row: BLAKE3 over
// the lane domain, LE32 lane, LE32 version, the candidate id as a tagged
// string and the candidate's parameter id. A block's (lane, version) pair
// resolves to this key and the row's algorithm and document.
func LanePID(lane, version uint32, candidateID string, paramsID common.Hash) common.Hash {
	b := make([]byte, 0, 1+len(LaneDomain)+4+4+1+len(candidateID)+32)
	b = append(b, byte(len(LaneDomain)))
	b = append(b, LaneDomain...)
	b = binary.LittleEndian.AppendUint32(b, lane)
	b = binary.LittleEndian.AppendUint32(b, version)
	b = append(b, byte(len(candidateID)))
	b = append(b, candidateID...)
	b = append(b, paramsID[:]...)
	return common.Hash(blake3.Sum256(b))
}

// VersionAt is the lane's parameter version in force at a height: the last
// activated at or below it, or the first when none has activated.
func (l *Lane) VersionAt(height uint64) LaneVersion {
	v := l.Versions[0]
	for _, c := range l.Versions {
		if c.Height <= height {
			v = c
		}
	}
	return v
}

// LaneState is the per-lane consensus state a branch carries: the lane's
// difficulty after its last block, that block's height and time, and the
// lane's work at the start of the current scoring epoch (the normalisation
// reference). It is a deterministic consequence of the branch.
type LaneState struct {
	Difficulty *big.Int
	LastHeight uint64
	LastTime   uint64
	RefWork    *big.Int // Work(Target(Difficulty)) frozen at the epoch's first height
	// Quota is the lane's target quota in basis points (v0.2 section 3),
	// fixed by scheduled upgrade only.
	Quota uint32
	// FloorNum / FloorDen is the difficulty floor of recovery (section 6.3):
	// the lane's difficulty never eases below this fraction of its
	// difficulty at the start of the silence. Research default 1/4.
	FloorNum, FloorDen uint64
	SilenceDifficulty  *big.Int // the difficulty when the silence began (the floor's base)
}

// ResearchFloor is the research default of the difficulty floor, 1/4.
var ResearchFloor = [2]uint64{1, 4}

// FloorDifficulty is the lowest difficulty recovery may reach:
// ceil(base x FloorNum / FloorDen), never below 1.
func (s *LaneState) FloorDifficulty() *big.Int {
	if s.SilenceDifficulty == nil || s.FloorDen == 0 {
		return big.NewInt(1)
	}
	f := new(big.Int).Mul(s.SilenceDifficulty, new(big.Int).SetUint64(s.FloorNum))
	den := new(big.Int).SetUint64(s.FloorDen)
	f.Add(f, new(big.Int).Sub(den, big.NewInt(1))).Div(f, den)
	if f.Sign() <= 0 {
		return big.NewInt(1)
	}
	return f
}

// Target is floor((2^256 - 1) / difficulty), the rule of pow.Target.
func Target(difficulty *big.Int) *big.Int {
	return new(big.Int).Div(maxTarget, difficulty)
}

// Work is the canonical intrinsic work of a block: floor(2^256 / (T + 1))
// for the lane's consensus target T in [1, 2^256 - 1], the expected number
// of proof evaluations that meet T. The proof value itself is not an input
// (section 3.2: credit by the target met, never by luck). The result is in
// [1, 2^255].
func Work(target *big.Int) (*big.Int, error) {
	if target.Sign() <= 0 || target.Cmp(maxTarget) > 0 {
		return nil, ErrTarget
	}
	return new(big.Int).Div(two256, new(big.Int).Add(target, big.NewInt(1))), nil
}

// Work256 is Work in 256-bit arithmetic for implementations without a wider
// integer: floor((2^256 - 1 - T) / (T + 1)) + 1, every intermediate below
// 2^256 for T up to 2^256 - 2; the maximum target (difficulty 1) is the one
// explicit case, work 1, since its divisor 2^256 does not fit (M-1). It
// equals Work exactly.
func Work256(target *big.Int) (*big.Int, error) {
	if target.Sign() <= 0 || target.Cmp(maxTarget) > 0 {
		return nil, ErrTarget
	}
	if target.Cmp(maxTarget) == 0 {
		return big.NewInt(1), nil
	}
	num := new(big.Int).Sub(maxTarget, target)
	den := new(big.Int).Add(target, big.NewInt(1))
	return num.Div(num, den).Add(num, big.NewInt(1)), nil
}

// Normalise converts a block's intrinsic work into the lane's research
// units: floor(work x 2^64 / ref), ref the lane's reference work (its own
// difficulty state at the epoch's start, section 3.3). A lane at steady
// difficulty earns exactly NormScale per block.
func Normalise(work, ref *big.Int) (*big.Int, error) {
	if ref.Sign() <= 0 {
		return nil, ErrNoRef
	}
	n := new(big.Int).Mul(work, NormScale)
	return n.Div(n, ref), nil
}

// EpochOf is the scoring epoch of a height: height / length, height-based
// and identical on every fork (section 4).
func EpochOf(height, length uint64) (uint64, error) {
	if length == 0 {
		return 0, ErrEpochLen
	}
	return height / length, nil
}

// Weights are the score's consensus constants as exact rationals: A =
// ANum / ADen on the minimum, e = ENum / EDen on the sum.
type Weights struct {
	ANum, ADen, ENum, EDen uint64
}

// Research is the v0.1 model's point: A = 5, e = 1/50 (0.02), equal quotas.
var Research = Weights{ANum: 5, ADen: 1, ENum: 1, EDen: 50}

// Hardened is the v0.2 Hardened point (MOSAIC-VF-V0.2-SPEC.md section 3):
// A = 1, e = 1/50, over quota-normalised service.
var Hardened = Weights{ANum: 1, ADen: 1, ENum: 1, EDen: 50}

// QuotaScale is the quota unit: quotas are basis points summing to 10,000.
const QuotaScale = 10000

// HardenedQuotas is v0.2's default lane quotas in basis points: 23.75
// percent on the four core lanes, 5 percent on the experimental sidecar.
var HardenedQuotas = []uint32{2375, 2375, 2375, 2375, 500}

// ValidateQuotas: every quota positive, the sum QuotaScale, one per lane.
func ValidateQuotas(q []uint32) error {
	if len(q) == 0 {
		return errors.New("mosaic: no quotas")
	}
	var sum uint64
	for i, x := range q {
		if x == 0 {
			return fmt.Errorf("mosaic: lane %d quota zero", i+1)
		}
		sum += uint64(x)
	}
	if sum != QuotaScale {
		return fmt.Errorf("mosaic: quotas sum to %d, want %d", sum, QuotaScale)
	}
	return nil
}

// Service is the quota-normalised service of a lane: U_i = floor(W_i x
// QuotaScale / q_i) (v0.2 section 3). A lane at its quota's share of the
// epoch's work has the same U as every other lane at its share.
func Service(W *big.Int, quota uint32) *big.Int {
	u := new(big.Int).Mul(W, big.NewInt(QuotaScale))
	return u.Div(u, new(big.Int).SetUint64(uint64(quota)))
}

// ScoreQuota is the v0.2 epoch score S = A x min_i(U_i) + e x sum_i(W_i)
// scaled by Scale: ANum x EDen x min(U) + ENum x ADen x sum(W). With equal
// quotas it is Score at the same weights times QuotaScale / q on the first
// term; len(q) must equal len(W).
func ScoreQuota(W []*big.Int, q []uint32, w Weights) *big.Int {
	if len(W) == 0 || len(W) != len(q) {
		return new(big.Int)
	}
	min := Service(W[0], q[0])
	sum := new(big.Int)
	for i, x := range W {
		if u := Service(x, q[i]); u.Cmp(min) < 0 {
			min = u
		}
		sum.Add(sum, x)
	}
	a := new(big.Int).Mul(new(big.Int).SetUint64(w.ANum), new(big.Int).SetUint64(w.EDen))
	e := new(big.Int).Mul(new(big.Int).SetUint64(w.ENum), new(big.Int).SetUint64(w.ADen))
	return a.Mul(a, min).Add(a, e.Mul(e, sum))
}

// ChainScoreQuota is the sum of ScoreQuota over the completed epochs and the
// partial one, the quotas fixed for the whole range (a quota change is an
// activation and starts a new range).
func ChainScoreQuota(epochs [][]*big.Int, q []uint32, w Weights) *big.Int {
	total := new(big.Int)
	for _, W := range epochs {
		total.Add(total, ScoreQuota(W, q, w))
	}
	return total
}

// Scale is the integer the score is multiplied by so it stays an integer:
// ADen x EDen. Scores compare only against scores of the same weights.
func (w Weights) Scale() *big.Int {
	return new(big.Int).Mul(new(big.Int).SetUint64(w.ADen), new(big.Int).SetUint64(w.EDen))
}

// Score is the epoch score S = A x min_i(W_i) + e x sum_i(W_i) over the m
// lanes' normalised work, scaled by Scale: ANum x EDen x min + ENum x ADen
// x sum. An absent lane counts as zero work.
func Score(W []*big.Int, w Weights) *big.Int {
	if len(W) == 0 {
		return new(big.Int)
	}
	min := new(big.Int).Set(W[0])
	sum := new(big.Int)
	for _, x := range W {
		if x.Cmp(min) < 0 {
			min.Set(x)
		}
		sum.Add(sum, x)
	}
	a := new(big.Int).Mul(new(big.Int).SetUint64(w.ANum), new(big.Int).SetUint64(w.EDen))
	e := new(big.Int).Mul(new(big.Int).SetUint64(w.ENum), new(big.Int).SetUint64(w.ADen))
	return a.Mul(a, min).Add(a, e.Mul(e, sum))
}

// ChainScore is the sum of the epoch scores of the completed epochs and the
// partial one (section 4): the branch's score.
func ChainScore(epochs [][]*big.Int, w Weights) *big.Int {
	total := new(big.Int)
	for _, W := range epochs {
		total.Add(total, Score(W, w))
	}
	return total
}

// Tip is what fork choice compares: the branch's chain score, its height
// and its tip's hash.
type Tip struct {
	Score  *big.Int
	Height uint64
	Hash   common.Hash
}

// Compare orders two branches: greater chain score first; equal scores by
// greater height; equal heights by the lower tip hash (bytewise). It is a
// total order on distinct tips, so every node choosing the greatest picks
// the same branch (section 5). Returns +1 when a is preferred, -1 when b,
// 0 only for the same tip.
func Compare(a, b Tip) int {
	if c := a.Score.Cmp(b.Score); c != 0 {
		return c
	}
	if a.Height != b.Height {
		if a.Height > b.Height {
			return 1
		}
		return -1
	}
	return -bytes.Compare(a.Hash[:], b.Hash[:])
}

// Accumulate adds one block's normalised work to its lane (1 to m) in the
// epoch's vector (section 3.3), growing the vector to m lanes.
func Accumulate(W []*big.Int, lane uint32, m int, norm *big.Int) ([]*big.Int, error) {
	if lane == 0 || int(lane) > m {
		return W, fmt.Errorf("%w: %d of %d", ErrLane, lane, m)
	}
	for len(W) < m {
		W = append(W, new(big.Int))
	}
	W[lane-1] = new(big.Int).Add(W[lane-1], norm)
	return W, nil
}

// Activation is one structural change of the chain's menu (v0.2 section
// 13): a lane's new version, a lane replacement, or a quota schedule, at a
// fixed height.
type Activation struct {
	Height uint64
	Lane   uint32   // the lane changed (0 for a quota schedule)
	Quotas []uint32 // the new quotas when the change is a quota schedule
}

// ValidateMenu is the adaptation rule as a validation: every activation
// ratified at least two activation windows ahead of the height it is
// ratified at; at most one structural change at any height; quota
// schedules valid; a menu with no activation ahead is the previous valid
// schedule holding (a failed activation falls back to it, never to
// discretion: the caller keeps the last schedule that validated).
func ValidateMenu(acts []Activation, ratifiedAt, window uint64) error {
	seen := map[uint64]bool{}
	for _, a := range acts {
		if seen[a.Height] {
			return fmt.Errorf("mosaic: two structural changes at %d", a.Height)
		}
		seen[a.Height] = true
		if a.Height > ratifiedAt && a.Height < ratifiedAt+2*window {
			return fmt.Errorf("mosaic: activation at %d is under two windows after %d", a.Height, ratifiedAt)
		}
		if a.Quotas != nil {
			if err := ValidateQuotas(a.Quotas); err != nil {
				return err
			}
		}
	}
	return nil
}

// Succession rules (section 7) on a lane table: one lane changes at any
// activation height; every activation is at least two intervals ahead of
// the height it is ratified at; a lane's versions strictly ascend. Validate
// reports the first violation.
func ValidateSuccession(lanes []Lane, ratifiedAt, interval uint64) error {
	seen := map[uint64]uint8{}
	for _, l := range lanes {
		if len(l.Versions) == 0 {
			return fmt.Errorf("mosaic: lane %d has no version", l.ID)
		}
		for i, v := range l.Versions {
			if i > 0 && v.Height <= l.Versions[i-1].Height {
				return fmt.Errorf("mosaic: lane %d version %d does not ascend", l.ID, v.Version)
			}
			if i > 0 {
				if other, dup := seen[v.Height]; dup && other != l.ID {
					return fmt.Errorf("mosaic: lanes %d and %d both change at %d", other, l.ID, v.Height)
				}
				seen[v.Height] = l.ID
				if v.Height > ratifiedAt && v.Height < ratifiedAt+2*interval {
					return fmt.Errorf("mosaic: lane %d version %d activates at %d, under two intervals after %d", l.ID, v.Version, v.Height, ratifiedAt)
				}
			}
		}
	}
	return nil
}
