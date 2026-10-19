// Copyright 2019 The multi-geth Authors
// This file is part of the multi-geth library.
//
// The multi-geth library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The multi-geth library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the multi-geth library. If not, see <http://www.gnu.org/licenses/>.

package ctypes

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/holiman/uint256"
)

type UnsupportedConfigErr error

var (
	ErrUnsupportedConfigNoop  UnsupportedConfigErr = errors.New("unsupported config value (noop)")
	ErrUnsupportedConfigFatal UnsupportedConfigErr = errors.New("unsupported config value (fatal)")
)

type ErrUnsupportedConfig struct {
	Err    error
	Method string
	Value  interface{}
}

func (e ErrUnsupportedConfig) Error() string {
	return fmt.Sprintf("%v, field: %s, value: %v", e.Err, e.Method, e.Value)
}

func IsFatalUnsupportedErr(err error) bool {
	if err == nil {
		return false
	}
	return err == ErrUnsupportedConfigFatal
}

func UnsupportedConfigError(err error, method string, value interface{}) ErrUnsupportedConfig {
	return ErrUnsupportedConfig{
		Err:    err,
		Method: method,
		Value:  value,
	}
}

// Uint64Uint256ValOrMapHex is an encoding type for Parity's chain config,
// used for their 'blockReward' field.
// When only an initial value, eg 0:0x42 is set, the type is a hex-encoded string.
// When multiple values are set, eg modified block rewards, the type is a map of hex-encoded strings.
type Uint64Uint256ValOrMapHex map[uint64]*uint256.Int

// UnmarshalJSON implements the json Unmarshaler interface.
func (m *Uint64Uint256ValOrMapHex) UnmarshalJSON(input []byte) error {
	mm := make(map[math.HexOrDecimal64]math.HexOrDecimalUint256)
	err := json.Unmarshal(input, &mm)
	if err == nil {
		mp := Uint64Uint256ValOrMapHex{}
		for k, v := range mm {
			u := uint64(k)
			mp[u] = v.ToInt()
		}
		*m = mp
		return nil
	}
	uq, err := strconv.Unquote(string(input))
	if err != nil {
		return err
	}
	input = []byte(uq)
	var b = new(math.HexOrDecimalUint256)
	err = b.UnmarshalText(input)
	if err != nil {
		return err
	}
	*m = Uint64Uint256ValOrMapHex{0: b.ToInt()}
	return nil
}

// MarshalJSON implements the json Marshaler interface.
func (m Uint64Uint256ValOrMapHex) MarshalJSON() (output []byte, err error) {
	mm := make(map[math.HexOrDecimal64]*math.HexOrDecimalUint256)
	for k, v := range m {
		if v == nil {
			continue // should never happen
		}
		d := math.HexOrDecimalUint256(*v)
		mm[math.HexOrDecimal64(k)] = &d
	}
	return json.Marshal(mm)
}

// Uint64Uint256MapEncodesHex is a map that encodes and decodes w/ JSON hex format.
type Uint64Uint256MapEncodesHex map[uint64]*uint256.Int

// UnmarshalJSON implements the json Unmarshaler interface.
func (bb *Uint64Uint256MapEncodesHex) UnmarshalJSON(input []byte) error {
	// HACK: Parity uses raw numbers here...
	// It would be better to use a consistent format... instead of having to do interface{}-ing
	// and switch on types.
	m := make(map[math.HexOrDecimal64]interface{})
	err := json.Unmarshal(input, &m)
	if err != nil {
		return err
	}
	b := make(map[uint64]*uint256.Int)
	for k, v := range m {
		var vv *uint256.Int
		switch v := v.(type) {
		case string:
			var b = new(math.HexOrDecimalUint256)
			err = b.UnmarshalText([]byte(v))
			if err != nil {
				return err
			}
			vv = b.ToInt()
		case int, int64:
			vv = uint256.NewInt(uint64(v.(int64)))
		case float64:
			i, err := strconv.ParseUint(fmt.Sprintf("%.0f", v), 10, 64)
			if err != nil {
				panic(err)
			}
			vv = uint256.NewInt(i)
		default:
			panic(fmt.Sprintf("unknown type: %t %v", v, v))
		}
		if vv != nil {
			b[uint64(k)] = vv
		}
	}
	*bb = b
	return nil
}

// MarshalJSON implements the json Marshaler interface.
func (b Uint64Uint256MapEncodesHex) MarshalJSON() ([]byte, error) {
	mm := make(map[math.HexOrDecimal64]*math.HexOrDecimalUint256)
	for k, v := range b {
		if v == nil {
			continue // should never happen
		}
		d := math.HexOrDecimalUint256(*v)
		mm[math.HexOrDecimal64(k)] = &d
	}
	return json.Marshal(mm)
}

func (b Uint64Uint256MapEncodesHex) SetValueTotalForHeight(n *uint64, val *uint256.Int) {
	if n == nil || val == nil {
		return
	}

	sums := make(map[uint64]*uint256.Int)
	for k := range b {
		sums[k] = new(uint256.Int).SetUint64(b.SumValues(&k))
	}
	if sums[*n] != nil {
		if sums[*n].Cmp(val) < 0 {
			sums[*n] = val
		}
	} else {
		sums[*n] = val
	}

	sumR := uint256.NewInt(0)
	sl := []uint64{}
	for k := range sums {
		sl = append(sl, k)
	}
	sort.Slice(sl, func(i, j int) bool {
		return sl[i] < sl[j]
	})
	for _, s := range sl {
		d := new(uint256.Int).Sub(sums[s], sumR)
		b[s] = d
		sumR.Add(sumR, d)
	}
}

func (b Uint64Uint256MapEncodesHex) SumValues(n *uint64) uint64 {
	var sumB = uint256.NewInt(0)
	var sl = []uint64{}

	for k := range b {
		sl = append(sl, k)
	}
	sort.Slice(sl, func(i, j int) bool {
		return sl[i] < sl[j]
	})

	for _, s := range sl {
		if s > *n {
			// break because we're sorted chronologically,
			// all following indexes will be greater than limit n.
			break
		}
		sumB.Add(sumB, b[s])
	}

	return sumB.Uint64()
}

// MapMeetsSpecification returns the block number at which a difficulty/+reward map meet specifications, eg. EIP649 and/or EIP1234, or EIP2384.
// This is a reverse lookup to extract EIP-spec'd parameters from difficulty and reward maps implementations.
func MapMeetsSpecification(difficulties Uint64Uint256MapEncodesHex, rewards Uint64Uint256MapEncodesHex, difficultySum, wantedReward *uint256.Int) *uint64 {
	var diffN *uint64
	var sl = []uint64{}

	// difficulty
	for k := range difficulties {
		sl = append(sl, k)
	}
	sort.Slice(sl, func(i, j int) bool {
		return sl[i] < sl[j]
	})

	var total = new(uint256.Int)
	for _, s := range sl {
		d := difficulties[s]
		if d == nil {
			panic(fmt.Sprintf("dnil difficulties: %v, sl: %v", difficulties, sl))
		}
		total.Add(total, d)
		if total.Cmp(difficultySum) >= 0 {
			diffN = &s //nolint:gosec,exportloopref
			break
		}
	}
	if diffN == nil {
		// difficulty bomb delay not configured,
		// then does not meet eip649/eip1234 spec
		return nil
	}

	if wantedReward == nil || rewards == nil {
		return diffN
	}

	reward, ok := rewards[*diffN]
	if !ok {
		return nil
	}
	if reward.Cmp(wantedReward) != 0 {
		return nil
	}

	return diffN
}

type ConsensusEngineT int

const (
	ConsensusEngineT_Unknown = iota
	ConsensusEngineT_Clique
	ConsensusEngineT_Lyra2
	ConsensusEngineT_Igneum
)

func (c ConsensusEngineT) String() string {
	switch c {
	case ConsensusEngineT_Clique:
		return "clique"
	case ConsensusEngineT_Lyra2:
		return "lyra2"
	case ConsensusEngineT_Igneum:
		return "igneum"
	default:
		return "unknown"
	}
}

func (c ConsensusEngineT) IsClique() bool {
	return c == ConsensusEngineT_Clique
}

func (c ConsensusEngineT) IsLyra2() bool {
	return c == ConsensusEngineT_Lyra2
}

func (c ConsensusEngineT) IsIgneum() bool {
	return c == ConsensusEngineT_Igneum
}

func (c ConsensusEngineT) IsUnknown() bool {
	return c == ConsensusEngineT_Unknown
}

type BlockSealingT int

const (
	BlockSealing_Unknown = iota
	BlockSealing_Ethereum
)

func (b BlockSealingT) String() string {
	switch b {
	case BlockSealing_Ethereum:
		return "ethereum"
	default:
		return "unknown"
	}
}

// CliqueConfig is the consensus engine configs for proof-of-authority based sealing.
type CliqueConfig struct {
	Period uint64 `json:"period"` // Number of seconds between blocks to enforce
	Epoch  uint64 `json:"epoch"`  // Epoch length to reset votes and checkpoint
}

// String implements the stringer interface, returning the consensus engine details.
func (c *CliqueConfig) String() string {
	return "clique"
}

// Lyra2Config is the consensus engine configs for MINTME network.
type Lyra2Config struct{}

// String implements the stringer interface, returning the consensus engine details.
func (c *Lyra2Config) String() string {
	return "lyra2"
}

// IgneumConfig is the consensus engine config of the Igneum proof-of-work
// engine (consensus/igneum). Algorithm names the registered pow.Algorithm the
// chain runs on; Phase Zero decides the launch name and "null" is the devnet
// and test stand-in. AlgorithmParams is the algorithm's own parameter object,
// passed through verbatim. Protocol engineer B adds the difficulty and
// issuance parameters here (PB-01, PB-02).
type IgneumConfig struct {
	Algorithm       string            `json:"algorithm"`
	AlgorithmParams json.RawMessage   `json:"algorithmParams,omitempty"`
	Difficulty      *IgneumDifficulty `json:"difficulty,omitempty"`
	Issuance        *IgneumIssuance   `json:"issuance,omitempty"`
	Fees            *IgneumFees       `json:"fees,omitempty"`
	// Schedule is the bounded parameter menu (the research test plan's
	// section 6): the parameter documents in force from predetermined
	// heights, chain-bound and deterministic, with an explicit terminal
	// entry. Absent: AlgorithmParams holds for every height.
	Schedule *IgneumSchedule `json:"schedule,omitempty"`
	// Authority is the miner-bound work authority rule of the research
	// prototype (consensus/igneum/authority.go; a PROPOSAL, nothing adopted):
	// from Height every block carries a bond key in its header, the key is
	// bonded in the registry contract, and the coinbase is the bond's
	// account. Absent: no rule, no header field.
	Authority *IgneumAuthority `json:"authority,omitempty"`
	// Sortition is the sortition-gated rule of the same prototype
	// (consensus/igneum/sortition.go; a PROPOSAL, hybrid proof of work and
	// bonded capital, nothing adopted): from Height every block is signed by
	// its bond's signer at a round its key is eligible at. Needs Authority.
	Sortition *IgneumSortition `json:"sortition,omitempty"`
	// Program is the chain-side form of the per-period programme (R0E,
	// docs/igneumhash/schedule/DEVNET11-R0E-CHAIN-FORM.md): the reveal
	// delay in blocks behind which the period's anchor block sits. The
	// period itself is the algorithm document's PERIOD_BLOCKS. Absent on a
	// chain whose algorithm draws no programme; on one that does, absent
	// means the relation's own default of two blocks.
	Program *IgneumProgram `json:"program,omitempty"`
	// Vector is VectorWork, the four-coordinate proof of work
	// (docs/consensus/VECTORWORK.md): one relation document, one target
	// interval and one registered reference difficulty per coordinate;
	// a block carries exactly one coordinate (the header's coord), its
	// difficulty is that coordinate's controller output at the parent, its
	// seal verifies under that coordinate's relation, and the fork choice
	// weighs a chain by the geometric mean of its per-coordinate
	// normalised work cumulative from genesis. Nil on a single-relation
	// chain (Algorithm and Schedule carry the relation then).
	Vector *IgneumVector `json:"vector,omitempty"`
}

// IgneumPayChange is one height-gated change of the pay weights
// (IgneumVector.PayChanges).
type IgneumPayChange struct {
	Height    uint64   `json:"height"`
	PayShares []uint64 `json:"payShares"`
}

// IgneumDocumentChange is one height-gated change of a coordinate's
// document IN PLACE (LANE-MODIFICATION.md section 1, the founder's order of
// 15 October 2026: a lane modified to strand a chip without spending a
// coordinate index): from Height the coordinate's blocks and shares are
// verified under AlgorithmParams with DRef, DMin and LagCap as given; the
// coordinate keeps its index, name, hardware class, pay weight, routing
// and pool sessions, and its controller, pay pot and window state carry
// over. The entries ascend by height and are immutable once stored; a
// landing may append one above the head and above its own height plus the
// notice.
type IgneumDocumentChange struct {
	Height          uint64          `json:"height"`
	AlgorithmParams json.RawMessage `json:"algorithmParams,omitempty"`
	DRef            uint64          `json:"dRef,omitempty"`
	DMin            uint64          `json:"dMin,omitempty"`
	LagCap          uint64          `json:"lagCap,omitempty"`
	// Void (LANE-MODIFICATION.md section 1b, the security review's read of 16
	// October 2026) is the append-only entry that voids a PENDING change
	// of this coordinate: {"height": X, "void": true} names the change at
	// X (its id is its height), carries no document, and is admitted only
	// while X is above the landing; the voided change never comes into
	// force and a retirement the change locked admits again. A void entry
	// is immutable once stored like every entry.
	Void bool `json:"void,omitempty"`
}

// IgneumLanesChange is one height-gated change of the breadth's lanes
// (IgneumVector.BalancedLanesChanges).
type IgneumLanesChange struct {
	Height uint64  `json:"height"`
	Lanes  []uint8 `json:"lanes"`
}

// IgneumConstantsChange is one height-gated change of the clock constants
// (IgneumVector.ConstantsChanges; F-151, the 12-second form, 15 October
// 2026): the coordinates' controller half-life and interval clamp
// (HalfLife, DtMax, seconds) and the seal clock's time floor and tail
// round length (TMin, TailDelta, seconds). A zero field leaves that
// constant as it was before the entry; the entry is in force from its
// height, the block at Height the first read under it.
type IgneumConstantsChange struct {
	Height    uint64 `json:"height"`
	HalfLife  uint64 `json:"halfLife,omitempty"`
	DtMax     uint64 `json:"dtMax,omitempty"`
	TMin      uint64 `json:"tMin,omitempty"`
	TailDelta uint64 `json:"tailDelta,omitempty"`
}

// IgneumVector is the vector form's registration: the coordinates in
// order (two to four), the controller's half-life in seconds (the chain's
// registered ASERT form, one constant for every coordinate) and its
// maximum interval, and the fork rule (geometric, the default, or minimum
// behind the registration for the cells).
type IgneumVector struct {
	Coordinates []IgneumCoordinate `json:"coordinates"`
	HalfLife    uint64             `json:"halfLife"`
	DtMax       uint64             `json:"dtMax"`
	// Rule is "geometric" (the default when empty), "minimum", or
	// "coverage" (VECTORWORK.md revision 13, section 2: the coverage
	// count in the branch's trailing window since the common ancestor,
	// then the geometric mean of the window's work over the covered
	// coordinates; branch-relative, never from genesis).
	Rule string `json:"rule,omitempty"`
	// Window is K_w, the recency window in blocks of the coverage rule
	// (revision 13); zero or absent reads 64 (16 target intervals).
	Window uint64 `json:"window,omitempty"`
	// WindowForm (VECTORWORK.md revision 16 at b22d69be2, 14 October 2026)
	// names what the coverage window is taken over: "" or "suffix", the
	// branch's suffix from the common ancestor (revisions 13 and 15, the
	// research chains vectorwork5 to 7); "branch", the last K headers of
	// each branch WITH the common prefix, so a depth-one race compares the
	// two siblings on the window each completes rather than on their own
	// block alone (the security review's vectorwork7 slide cell: the scarce
	// coordinate's block near its floor lost every depth-one race to a
	// near-target sibling under the suffix form).
	WindowForm string `json:"windowForm,omitempty"`
	// PayByCoordinate is the Pay rule's coordinate dimension (VECTORWORK.md
	// revision 13, section 9; C-34, docs/economics/ISSUANCE-SHARE-2026-10-13.md):
	// each block's miner subsidy is split in four equal integer shares,
	// the wei remainder to the lowest-numbered coordinates, and a block of
	// coordinate c is paid the whole of c's share accrued since c's last
	// block (this block's included), so a starved coordinate's share is
	// paid in full to the work it does seal, at whatever difficulty, and
	// nothing is left unissued by it.
	PayByCoordinate bool `json:"payByCoordinate,omitempty"`
	// PayShares are the coordinates' issuance weights under the coordinate
	// Pay rule (the founder's order of 14:17 UK, 13 October 2026: the
	// governing coordinate carries the governing share): coordinate c's
	// share of a block's miner subsidy is M x w_c / sum(w), the wei
	// remainder to the lowest-numbered coordinates; absent or empty, the
	// four equal shares of C-34. One weight per coordinate, each positive.
	PayShares []uint64 `json:"payShares,omitempty"`
	// PayChanges are height-gated changes of the pay weights on a running
	// chain (the founder's word of 09:03 UK, 14 October 2026: the static
	// fabric's weight from 4 of 10 to 2 of 8, a research chain first by
	// its next sequenced release, then the eleventh chain (chain 7321), then the public chains;
	// the weight returning by the same step): from each entry's height the
	// entry's weights govern, in ascending height order; the weights
	// before the first entry are PayShares. A re-registration may append
	// an entry above the node's head and change nothing else.
	PayChanges []IgneumPayChange `json:"payChanges,omitempty"`
	// BalancedFrom (C-38, docs/consensus/BALANCED-REWARD.md; the founder's
	// word of 09:5x UK, 14 October 2026) is the first height at which the
	// share lane's payout list is the balanced reward's (consensus/igneum/
	// balanced.go): each key's claim its balanced work over the pay window
	// (the coverage window K, 64 default), the weighted geometric mean of
	// its normalised work per coordinate under the pay weights, with the
	// minimum unit and no soft floor in the first cut. Zero is off. A
	// re-registration may set it once, above the node's head.
	BalancedFrom uint64 `json:"balancedFrom,omitempty"`
	// BalancedV2From (WHOLEWORK.md section 9, protocol B at a7e2b5e48; the
	// second cut) is the first height at which the balanced work is the
	// breadth form: B_w = T_w x beta_w, T_w the key's total normalised
	// work on the pay window and beta_w = min(1, product over c of
	// (x'_c / s_c)^{s_c}) its breadth on lane shares x_c = W_c / T_w
	// floored inside as x'_c = max(x_c, phi x s_c, y_c), y_c the key's
	// lane share on the 256-block floor account; phi = BalancedPhiDen's
	// reciprocal (16 default). Zero is off (the first cut's form). Set
	// once by a re-registration, above the node's head.
	BalancedV2From uint64 `json:"balancedV2From,omitempty"`
	// BalancedPhiDen is 1/phi, the soft floor's denominator (16 default).
	BalancedPhiDen uint64 `json:"balancedPhiDen,omitempty"`
	// BalancedPhiFrom and BalancedPhiDenV2 change phi on a running chain
	// (the project's word of 17:2x UK, 14 October 2026: phi 0, no floor, a key
	// paid nothing on a lane it holds no work on): from BalancedPhiFrom
	// the denominator is BalancedPhiDenV2, where 0 means NO phi floor;
	// set once by a re-registration above the head.
	BalancedPhiFrom  uint64 `json:"balancedPhiFrom,omitempty"`
	BalancedPhiDenV2 uint64 `json:"balancedPhiDenV2,omitempty"`
	// BalancedV3From (F-124, the project's default of 18:4x UK, 14 October 2026)
	// is the first height at which (1) a key whose balanced work is zero
	// is paid nothing (no fallback weight of one: under phi 0 every key
	// with a lane at zero read 0 and the pot was split equally by key,
	// a split into N keys paid N times) and (2) a lane with no accepted
	// share chain-wide in the window is excluded from the breadth for
	// that window (an unserved lane cannot zero the chain; a key's own
	// missing lane still zeroes that key). Zero is off.
	BalancedV3From uint64 `json:"balancedV3From,omitempty"`
	// BalancedMinFrom (the founder's direction of record, 15 October 2026:
	// "a key paid on its weakest lane") is the height from which a key's
	// balanced work is the MINIMUM over the breadth lanes of its scaled
	// lane work relative to the lane's share, in place of the weighted
	// geometric mean; the over-share cap and F-124 kept. 0 off.
	BalancedMinFrom uint64 `json:"balancedMinFrom,omitempty"`
	// SealerFoldFrom (F-165, the fold, the review's form of 19:3x UK, 15
	// October 2026) is the first height at which the sealer's quarter of a
	// block's pot is folded into the members' pot: every key, the sealer
	// included through its own balanced standing (the third cut's block
	// credit), is paid through the pot by weight and no fixed term goes to
	// the sealer's last entry; the tip stays the sealer's under the burn.
	// Inert where the minimum rule is not in force (BalancedMinFrom 0 or
	// above the height), so a chain without the rule keeps the quarter.
	// Set once by a re-registration above the head.
	SealerFoldFrom uint64 `json:"sealerFoldFrom,omitempty"`
	// SealerFoldListFrom (F-180, 15 October 2026) is the fold's height for
	// the FIRST pay form (the coordinate rule's pro rata over the derived
	// list, the research chain): from it the list's sealer entry carries
	// weight 0 and the amount is paid over the members alone.
	// SealerFoldFrom binds the second form (payV2Amounts) and reports
	// the fold; a chain on the first form sets this field too. Its own
	// field because the research chain's sealerFoldFrom (6550) landed
	// before the first form carried the fold: the blocks from 6550 to
	// this field's height are sealed with the sealer's term and verify
	// as such on every node, a fresh sync included. Set once above the
	// head; inert where the minimum rule is not in force.
	SealerFoldListFrom uint64 `json:"sealerFoldListFrom,omitempty"`
	// SealingBreadthFrom (F-166, the security review's finding 57c on lane 21:
	// an unpaid coordinate sealed a quarter of the research chain's blocks
	// for no pot, a free sealing authority) is the first height at which
	// a block whose coordinate has pay weight 0 at the block's own height
	// (PaySharesAt, cumulative over payChanges) is INVALID: refused on
	// import, never assembled, the sealworker idle on such a coordinate;
	// the coordinate keeps its registration and its controller (its lag
	// capped, its difficulty at the floor, so a later payChanges entry
	// restores it from that height at the floor); the coverage window and
	// the fork vector exclude it as they exclude a retired one. A
	// validity rule: a fork by height on every chain that takes it. Set
	// once by a re-registration above the head.
	SealingBreadthFrom uint64 `json:"sealingBreadthFrom,omitempty"`
	// NoticeBlocks and MaxLiveBlocks are REVISION-CADENCE.md's constants
	// in blocks (cut A): a coordinate's activation or retirement by
	// re-registration must sit at least NoticeBlocks above the head (the
	// 60-day publication window, 259,200 at 20 s; 0 on the research
	// chains), and a coordinate with no successor named is invalid for
	// sealing from activation + MaxLiveBlocks (the 365-day rule,
	// 1,576,800; 0 off). Registration constants, immutable once stored.
	NoticeBlocks  uint64 `json:"noticeBlocks,omitempty"`
	MaxLiveBlocks uint64 `json:"maxLiveBlocks,omitempty"`
	// RegistryKey (REVISION-CADENCE.md cut B, 15 October 2026) is the
	// address whose signed transaction to the vector registry address
	// lands a re-registration on the chain; immutable once set, and from
	// then the registration is chain-held: a node derives the active set
	// at a height from the chain alone and a config re-registration is
	// refused.
	RegistryKey common.Address `json:"registryKey,omitempty"`
	// RegistryKeyNext and RegistryKeyFrom (cut B's public-chain residual,
	// 15 October 2026): the key rotation under the notice. A document
	// signed by the current key may name the next key and the height it
	// takes over (at least noticeBlocks above the landing); from that
	// height a re-registration is signed by the next key and the
	// registration in force reads it as RegistryKey.
	RegistryKeyNext common.Address `json:"registryKeyNext,omitempty"`
	RegistryKeyFrom uint64         `json:"registryKeyFrom,omitempty"`
	// Research marks a research chain: a registry key with noticeBlocks
	// under 64 (the finality depth) is admitted only with it set; a public
	// chain never sets it.
	Research bool `json:"research,omitempty"`
	// BalancedSealerFrom (WHOLEWORK.md section 9, protocol B at 2ac296768;
	// the third cut) is the first height at which a sealed block counts
	// for its sealer's key as lane work of the block's coordinate equal to
	// the block's difficulty (the unit N_c counts), so a solo sealer's
	// total and lane shares read its blocks' mix. Zero is off.
	BalancedSealerFrom uint64 `json:"balancedSealerFrom,omitempty"`
	// BalancedLanes and BalancedLanesFrom (WHOLEWORK.md section 10 (a),
	// protocol B at 2ed55d8e4): from the height, the breadth runs over
	// the named lanes only (the coordinates with a share form), their
	// discipline shares renormalised over them; a lane left out (the
	// graph, whose solutions are tickets, not shares) pays by the sealer
	// term alone, outside the keys' split. Absent: every coordinate.
	BalancedLanes     []uint8 `json:"balancedLanes,omitempty"`
	BalancedLanesFrom uint64  `json:"balancedLanesFrom,omitempty"`
	// BalancedLanesChanges are height-gated changes of the breadth's lanes
	// on a running chain (the succession of 14 October 2026: the fabric
	// lane's place carried to its successor and Forge joining it), the
	// last entry at or under a height in force; stored entries immutable,
	// an appended entry above the head.
	BalancedLanesChanges []IgneumLanesChange `json:"balancedLanesChanges,omitempty"`
	// ConstantsChanges are height-gated changes of the clock constants
	// (F-151, the 12-second form): halfLife and dtMax for every
	// coordinate's controller, tMin and tailDelta for the seal clock; the
	// entries ascending by height, each in force from its height, a zero
	// field unchanged; stored entries immutable, an appended entry above
	// the head and above the landing. A coordinate's lag cap, derived
	// from the half-life, follows the half-life in force.
	ConstantsChanges []IgneumConstantsChange `json:"constantsChanges,omitempty"`
	// GraphSharesFrom (WHOLEWORK.md section 10 (b), protocol B at
	// 2ed55d8e4; the project's same-day order of 13:0x UK, 14 October 2026) is the
	// first height at which the carried list takes the TYPED form (a kind
	// byte per entry) and a graph coordinate's shares ride it as tickets
	// (the 168-byte witness beside the key and nonce, verified against the
	// coordinate's share target); GraphSharesPerBlock caps a block's graph
	// entries (0: no cap), the rest staying pooled. Zero is off.
	GraphSharesFrom     uint64 `json:"graphSharesFrom,omitempty"`
	GraphSharesPerBlock uint64 `json:"graphSharesPerBlock,omitempty"`
	// SeedGenesis (VECTORWORK.md revision 9, section 1): every
	// coordinate's cumulative work N_c includes D_ref_c from the genesis,
	// so the geometric product is never zero before a coordinate's first
	// block (vectorwork3 ran seven hours at product zero on the tie-break
	// alone). Off keeps the zero phase's second-key order.
	SeedGenesis bool `json:"seedGenesis,omitempty"`
}

// IgneumCoordinate is one work coordinate: its name, its relation (the
// algorithm name and the document the node builds it from), its target
// block interval in seconds (80 for four coordinates, so the chain's
// combined interval is 20), and DRef, the coordinate's reference
// difficulty: its genesis target and the unit its work is normalised by
// (W_c = N_c / DRef_c), a registration constant never recomputed.
type IgneumCoordinate struct {
	Name            string          `json:"name"`
	Algorithm       string          `json:"algorithm"`
	AlgorithmParams json.RawMessage `json:"algorithmParams,omitempty"`
	Target          uint64          `json:"target"`
	DRef            uint64          `json:"dRef"`
	// DMin is the coordinate's own difficulty floor (a witness relation
	// whose samples are rare runs under the hash relations' floor); zero
	// is the chain's minimum.
	DMin uint64 `json:"dMin,omitempty"`
	// Activation is the height from which the coordinate is a consensus
	// coordinate (VECTORWORK.md revision 16, section 1's height-gated
	// re-registration: a coordinate added to a running chain, its
	// controller anchored at the activation and the shares and the
	// coverage count over the active set); zero or absent is genesis.
	Activation uint64 `json:"activation,omitempty"`
	// RetiredFrom (REVISION-CADENCE.md, cut A, 14 October 2026) is the
	// height from which the coordinate's form is retired: its tag refused
	// for sealing and import, its lane outside every window; zero is
	// never. Successor names the coordinate index whose form succeeds
	// this one on the schedule (nil: none named); with the registration's
	// maxLiveBlocks, a coordinate with no successor named is invalid for
	// sealing from activation + maxLiveBlocks.
	RetiredFrom uint64  `json:"retiredFrom,omitempty"`
	Successor   *uint64 `json:"successor,omitempty"`
	// RevealBlocks is the coordinate's own programme reveal depth (the
	// period's anchor at p x PERIOD_BLOCKS - RevealBlocks on the node's
	// own chain), 0 the chain's Program part (F-141's chain forms at the
	// finality depth 64, the consensus chair's condition, 15 October 2026).
	RevealBlocks uint64 `json:"revealBlocks,omitempty"`
	// DocumentChanges are the coordinate's in-place document changes
	// (LANE-MODIFICATION.md section 1), ascending by height: from each
	// entry's height the coordinate's blocks and shares are verified under
	// the entry's document with its reference, floor and cap, the index,
	// name, weight and state unchanged. Immutable once stored; a landing
	// appends above the head and the notice.
	DocumentChanges []IgneumDocumentChange `json:"documentChanges,omitempty"`
	// WorkScale is the coordinate's scaled unit (VECTORWORK.md revision 8,
	// section 4): the controller, the weight and the reference run in
	// units of 1/WorkScale of a seal's difficulty, so a cheap relation's
	// reference lands at or above the controller's resolution (2^16)
	// while its seal target stays where its samples are. The seal is
	// checked against D / WorkScale (at least 1); zero or one is the
	// plain unit.
	WorkScale uint64 `json:"workScale,omitempty"`
	// Sponsored (SPONSORED-LANE-NODE-FORM.md section 1, 18 October 2026)
	// marks a lane whose work a sponsor pays for through the lane's escrow:
	// its pay share is zero at every height and it is outside the breadth
	// (the registration's shape refuses otherwise), the chain's own subsidy,
	// base lanes and security untouched; the escrow is derived from the
	// chain id and the coordinate (consensus/igneum.SponsoredEscrow).
	Sponsored bool `json:"sponsored,omitempty"`
	// LagCap (VECTORWORK.md revision 10, section 2) is L_c in seconds, the
	// bound on the coordinate's folded lag: lag_c after a block is
	// min(L_c, lag_c before + the block interval - T if the block is this
	// coordinate's), from 0 at the chain's start, and the controller reads
	// it in place of the anchored lag; L_c = floor(H x log2(D_ref_c /
	// Dmin_c)) is registered as an integer (no logarithm in the rule), so
	// an absent coordinate's lag cannot exceed what brings it to its
	// floor (vectorwork3's graph coordinate carried 9,700 s of lag out of
	// a 2.7 hour absence). Zero is off: the anchored formula, which the
	// fold telescopes to wherever the cap was never reached.
	LagCap uint64 `json:"lagCap,omitempty"`
}

// IgneumProgram are the per-period programme's chain-side constants: the
// anchor of period p is the canonical hash at p x PERIOD_BLOCKS -
// RevealBlocks (the genesis hash while that is below one), from which every
// node derives the period's programme from headers alone.
type IgneumProgram struct {
	RevealBlocks uint64 `json:"revealBlocks"`
}

// IgneumSortition are the sortition-gated rule's constants (research values
// on a development chain): the first height, the lottery epoch in blocks,
// the seed's block count, the expected eligible units at round 0, the round
// interval in seconds, the last bonded round, and the unbonded round that
// admits any signer with no bond (liveness).
type IgneumSortition struct {
	Height        uint64 `json:"height"`
	EpochBlocks   uint64 `json:"epochBlocks"`
	SeedBlocks    uint64 `json:"seedBlocks"`
	Expected      uint64 `json:"expected"`
	Delta         uint64 `json:"delta"`
	MaxRound      uint64 `json:"maxRound"`
	UnbondedRound uint64 `json:"unbondedRound"`
	MaxPayouts    uint64 `json:"maxPayouts"`
	// EpochChanges are changes of the epoch length at heights (ascending;
	// a consensus parameter changes only at a height, so a fresh node
	// replays both regimes).
	EpochChanges []IgneumEpochChange `json:"epochChanges,omitempty"`
	// OpenSeat selects the GENESIS FORM (GUARD-C-CLOSURE.md section 4,
	// route 2; PROTOTYPE): the units table and seed of each epoch written
	// into state by consensus (F-32), the empty units drawing as one open
	// seat at which any signer with no bond seals by plain proof of work,
	// the unit priced off the floor, the quarter cap in units, and the
	// keyed seal required of a drawn bonded key only. False: the drafts'
	// form as prototyped on devnet4 (snapshot state read by root; every
	// round below the unbonded one needs a bond).
	OpenSeat bool `json:"openSeat,omitempty"`
	// EndorseExpected is E_att, the settlement rule's second lot per height
	// (GUARD-C-CLOSURE.md 4.6 candidate 2; DEVNET6-RULES.md section 2):
	// the expected endorsing units per height at a full table; 0 = the rule
	// off (the prototype's round weight alone). MaxEndorsements bounds the
	// list a block carries (0 = 4 x EndorseExpected).
	EndorseExpected uint64 `json:"endorseExpected,omitempty"`
	MaxEndorsements uint64 `json:"maxEndorsements,omitempty"`
	// SharesPerBlock is the share lane's operating target n (LAUNCH-
	// GUARDS.md 2.9; consensus/igneum/shares): the share target is the
	// block target times n, so chain work yields about n accepted shares a
	// block; the list's ceiling is 256 with the carry-forward balance. The
	// launch value 160; 0 turns the lane off (the payout list stays the
	// signer's own, the form before the lane).
	SharesPerBlock uint64 `json:"sharesPerBlock,omitempty"`
	// MaxShares is the consensus ceiling on the shares a block carries
	// (at most 256 = maxPayouts; unset reads 256 when the lane is on).
	MaxShares uint64 `json:"maxShares,omitempty"`
	// ShareRecency is the carriage window in blocks: a share mined at
	// height h rides a block at or before h + ShareRecency - 1 (design G's
	// recency F; G-23's ruling of 16:0x UK, 11 October 2026: an accepted
	// share is one that fits the window, so the sealer's pool is bounded
	// at maxShares x shareRecency and refuses beyond it, and the share
	// itself is carried later, never a credit without it). At most the
	// carried entry's index bound (8 while the entry carries a one-byte
	// parent index; wider with the height form); unset reads that bound.
	ShareRecency uint64 `json:"shareRecency,omitempty"`
	// ShareStepMax turns on PA-53, the share's declared target step (the
	// auditor's third reading, 20:15 UK, 11 October 2026; the project's ruling
	// of 20:5x UK: a consensus change for the ninth development chain):
	// a carried entry names a step j, 0 to ShareStepMax, meaning the
	// share was mined at the chain's share target over 2^j, every node
	// verifies its hash under that target, and the entry weighs 2^j in
	// the payout list, so a paced card's pay is proportional to its work
	// in expectation at any step. 0 is the count form (every entry at
	// step 0, weight 1, the entry one byte shorter); at most 32.
	ShareStepMax uint64 `json:"shareStepMax,omitempty"`
	// The round clock amendment (consensus/igneum/clock.go; DEVNET6-RULES
	// section 3): SealWindowDiv turns it on (4 at launch: the seal's ASERT
	// targets (Delta - T_PI) / SealWindowDiv over the bonded blocks' seal
	// intervals), TPiMs is T_PI in milliseconds (250 at launch; 0 reads
	// 250), DMin the seal difficulty's floor (the floor card's rate over
	// the seal window; a research chain labels its value), TMin the
	// per-block time floor in seconds (10 at launch; 0 = none).
	SealWindowDiv uint64   `json:"sealWindowDiv,omitempty"`
	TPiMs         uint64   `json:"tPiMs,omitempty"`
	DMin          *big.Int `json:"dMin,omitempty"`
	TMin          uint64   `json:"tMin,omitempty"`
	// SealDriftMs is the future drift a BONDED block's declared time may
	// carry against the receiving node's clock, a CHAIN constant under
	// the clock amendment (the outside review's condition on PA-48, the project's
	// ruling of 18:1x UK, 11 October 2026: 500 ms, a fifth of the seal
	// target; unset reads 500 when the amendment is on); a block ahead by
	// more is held to its time as today's 15 s node rule holds one, never
	// refused for good; the node's clock reads in whole seconds, so the
	// whole-second drift at 500 ms is zero and a bonded block declared in
	// the next second is held up to one second. Open-seat and liveness
	// blocks keep the node's 15 s parameter.
	SealDriftMs uint64 `json:"sealDriftMs,omitempty"`
	// TailDelta (the bounded tail, 15 October 2026): the length in seconds
	// of every round past round 0 (0: Delta, the single round length); a
	// genesis form's own setting, the running chain's by the TailV1 fork.
	TailDelta uint64 `json:"tailDelta,omitempty"`
	// Rank0FallbackBlocks is K of the seal clock's rank-0 fallback (PA-48b,
	// the outside review's condition on PA-48a, 20:15 UK, 11 October 2026): the
	// consecutive heights with no leader's round-0 seal after which the
	// seal ASERT reads every bonded round-0 seal as PA-48 did, until a
	// leader's seal next appears; 16 at launch (0 reads 16 under the
	// amendment).
	Rank0FallbackBlocks uint64 `json:"rank0FallbackBlocks,omitempty"`
	// SealStaggerMs is PA-52's sealer stagger as a CHAIN constant (the
	// design seat's amendment to PA-48b, 20:4x UK, 11 October 2026): under
	// the fallback a rank-k round-0 seal is read net of k x SealStaggerMs
	// as well as tPiMs, so a staggered seal reads its search and not its
	// wait; 2,000 at launch (0 reads 2,000 under the amendment). A sealer
	// that does not stagger reads short by k staggers and raises D_seal by
	// that much against everyone, bounded by its share of round-0 seals
	// and self-limiting.
	SealStaggerMs uint64 `json:"sealStaggerMs,omitempty"`
}

// IgneumEpochChange is one change of the sortition rule's epoch length.
type IgneumEpochChange struct {
	Height      uint64 `json:"height"`
	EpochBlocks uint64 `json:"epochBlocks"`
}

// IgneumAuthority are the miner-bound work authority prototype's constants:
// the first height a bond is mandatory at, the bond registry's address (a
// genesis system contract), and the per-key cap the registry enforces, base
// units. Research prototype, 10 October 2026; never changed after genesis on
// a chain that carries it.
type IgneumAuthority struct {
	Height   uint64         `json:"height"`
	Registry common.Address `json:"registry"`
	Cap      *big.Int       `json:"cap"`
	// RegistryVersion names the registry's code: 0 or 1 the bonded rule's
	// bytecode, 2 the sortition stage's (the signer operation), 3 the
	// NATIVE registry (core/vm/igneumreg) with the hostile-key claim, the
	// exit address, the key list and the genesis form's floor-priced cap
	// (LAUNCH-GUARDS.md sections 2 and 2.4; PROTOTYPE on the lab build),
	// 4 the same with the four penalty and exit operations (the
	// equivocation, the endorsement equivocation, the release after the
	// lock, the hostile claim on the proof pi; LAUNCH-GUARDS.md 2.7) and
	// the secret-reveal claim refused (the sixth development chain).
	RegistryVersion uint64 `json:"registryVersion,omitempty"`
	// LockBlocks is the exit rule's lock in blocks at registry version 4:
	// a bond posted at height h releases, or changes its exit address, at
	// h + LockBlocks and never before (LAUNCH-GUARDS.md 2.7 rule 4). The
	// launch value is the entry's retirement height plus the activation
	// delay D, written as the blocks from the post; a research chain sets
	// a reachable figure and labels it. Zero: no lock (version 3's form).
	LockBlocks uint64 `json:"lockBlocks,omitempty"`
	// BRefBps is the genesis form's bond reference level in basis points
	// of the issuance line (frozen 1600: sixteen percent): B_floor(h) =
	// max(Issued(h), SRef) x BRefBps / 10000, written to the registry's
	// floor slot each block in Finalize (LAUNCH-GUARDS.md 2.2 and 2.4;
	// GUARD-C-CLOSURE.md 4.2). Zero means the drafts' form (units priced
	// off B_total alone, no floor).
	BRefBps uint64 `json:"bRefBps,omitempty"`
	// SRef is the issuance line the floor never falls under, base units
	// (the design seat's twelve-month issuance S_ref on a launch chain; a
	// research chain sets a reachable figure and labels it).
	SRef *big.Int `json:"sRef,omitempty"`
}

// IgneumSchedule is the bounded menu's activation table. Entry 0 is the
// document of AlgorithmParams at height 0 (implicit); each row names the
// document in force from its height (ascending, each above 0 and above the
// previous), all for the same algorithm; Terminal states what follows the
// final row. Nothing here is selected by a benchmark, a peer or a miner.
type IgneumSchedule struct {
	Entries  []IgneumScheduleEntry `json:"entries"`
	Terminal IgneumTerminal        `json:"terminal"`
	// ActivationWindow is the lifecycle menu's ratification window in blocks
	// (PLAN-TO-WIN.md point 8): a change to the schedule given to a node at
	// head height h may add or alter entries only at heights at or above
	// h + 2 x ActivationWindow, and never an entry at or below h (historical
	// verification is permanent). Zero (a development chain) disables the
	// check.
	ActivationWindow uint64 `json:"activationWindow,omitempty"`
}

// IgneumScheduleEntry is one activation row: (name, height, document), and
// the lifecycle menu's two fields: Enabled (nil or true: the row takes force
// at its height; false: a fully implemented successor configuration whose
// activation is disabled, built by every node so its support is proven,
// never in force until a later ratified schedule enables it) and
// MinLifetime (the least number of blocks the row stays in force: the next
// row's height is at least Height + MinLifetime).
type IgneumScheduleEntry struct {
	Name            string          `json:"name"`
	Height          uint64          `json:"height"`
	AlgorithmParams json.RawMessage `json:"algorithmParams"`
	Enabled         *bool           `json:"enabled,omitempty"`
	MinLifetime     uint64          `json:"minLifetime,omitempty"`
}

// Active reports whether the row takes force at its height.
func (e IgneumScheduleEntry) Active() bool { return e.Enabled == nil || *e.Enabled }

// IgneumTerminal states what follows the final entry: exactly one of Hold
// (the final document stays in force for every later height, stated rather
// than assumed) or EndHeight (no block above it is valid: the development
// network's declared end).
type IgneumTerminal struct {
	Hold      bool    `json:"hold,omitempty"`
	EndHeight *uint64 `json:"endHeight,omitempty"`
}

// IgneumDifficulty are the consensus constants of the difficulty rule
// (consensus/igneum/difficulty.Params, PB-01): never changed after genesis.
type IgneumDifficulty struct {
	Target   uint64   `json:"target"`   // block-time target in seconds
	HalfLife uint64   `json:"halfLife"` // seconds of accumulated lag per halving of difficulty
	DtMax    uint64   `json:"dtMax"`    // the most one block interval can count, seconds
	Min      *big.Int `json:"min"`      // the difficulty floor; also the genesis difficulty
}

// IgneumIssuance are the consensus constants of the block-subsidy schedule
// (consensus/igneum/issuance.Params, PB-02) and the maintenance fund address.
type IgneumIssuance struct {
	Epoch       uint64         `json:"epoch"`       // blocks per issuance epoch
	M           *big.Int       `json:"m"`           // asymptote of the declining phase, base units
	K           uint           `json:"k"`           // each epoch issues (M - issued) / 2^K per block
	Tail        *big.Int       `json:"tail"`        // fixed tail subsidy per block, base units; 0 selects the hard cap
	FundBps     uint64         `json:"fundBps"`     // maintenance fund share of the subsidy, basis points
	FundAddress common.Address `json:"fundAddress"` // the fund's address; required when FundBps > 0
	// EpochDiv selects the re-cut schedule (the launch schedule; the project's
	// ruling of 14:0x UK, 11 October 2026): each epoch issues (M minus the
	// supply) / EpochDiv, floor-divided over the epoch's blocks with the
	// remainder in its last block; the epoch's subsidy max(TailPerEpoch,
	// the amount), so the tail is the calendar quantity's integer form
	// (2,048 IGN an epoch at launch) and Tail is not read. Zero: the shift
	// form by K with the per-block Tail (the development chains before
	// the seventh).
	EpochDiv     uint64   `json:"epochDiv,omitempty"`
	TailPerEpoch *big.Int `json:"tailPerEpoch,omitempty"`
	// NextFrom and Next are the height-gated re-cut (the founder's "re cut"
	// for the 12-second target, 16 October 2026; the economics team's
	// ISSUANCE-12S-2026-10-16.md): from the block above NextFrom the
	// schedule is Next, its first epoch starting with the gross issued at
	// NextFrom carried whole; the fund's address and share are this form's
	// (Next names none). Absent: this form for ever. The gate is any height
	// under the shift form and an epoch's last height under the re-cut form
	// (issuance.Check refuses one elsewhere).
	NextFrom uint64          `json:"nextFrom,omitempty"`
	Next     *IgneumIssuance `json:"next,omitempty"`
}

// IgneumRPCLimits are the node's public-RPC limits as chain-config
// defaults (SECURITY-GAPS.md 8c6289fd, the project's order of 21:4x UK on 11
// October 2026; the protocol team's protocol-a/rpc-limits reads them): a
// per-source token bucket on the HTTP and WS listeners (RPCRate calls a
// second, RPCBurst the bucket's depth, matching the edge's 20 and 40) and
// the share lane's per-address cap on the shares one payout address may
// place in one block's accepted set (ShareCapPerAddress; a quarter of the
// 256 ceiling at launch, so four addresses at the cap fill a block and
// one cannot). Zero on any field reads that limit off (a development
// chain before the seventh). Keyed by chain id (params.IgneumChainRPCLimits).
type IgneumRPCLimits struct {
	RPCRate            uint64 `json:"rpcRate,omitempty"`
	RPCBurst           uint64 `json:"rpcBurst,omitempty"`
	ShareCapPerAddress uint64 `json:"shareCapPerAddress,omitempty"`
}

// IgneumRuleForks are the heights at which a consensus rule's form changed
// on a RUNNING chain (a swap in place), keyed by chain id
// (params.IgneumChainRuleForks; the project's standing rule of 20:1x UK, 11
// October 2026: a consensus change swapped onto a running devnet carries a
// height gate from the swap height, never a bare replacement, so a fresh
// node always syncs the chain from genesis). A zero height, or no entry
// for the chain id, runs the rule's latest form from genesis.
type IgneumRuleForks struct {
	// SealMeasureV1 is the first height under the seal clock's second
	// measurement (PA-48: the seal ASERT reads the round-0 bonded seals
	// only, from the search start net of tPiMs, at the target (Delta -
	// tMin - T_PI) / div); below it the first form (every bonded parent,
	// from the round floor, at (Delta - T_PI) / div).
	SealMeasureV1 uint64 `json:"sealMeasureV1,omitempty"`
	// ClockRankV1 is the first height whose Finalize writes the sealer's
	// lot rank into the clock's fifth slot and whose seal step reads the
	// LEADER's round-0 seals only (PA-48a); below it the slot is not
	// written (a state root difference otherwise: devnet7's fresh node
	// refused block 10 on the merkle root under the swapped binary) and
	// the seal step reads every round-0 bonded seal (PA-48).
	ClockRankV1 uint64 `json:"clockRankV1,omitempty"`
	// ClockDarkV1 is the first height whose Finalize writes the dark count
	// into the clock's sixth slot and whose seal step counts it (PA-48b's
	// fallback); below it the slot is not written and the fallback is off.
	ClockDarkV1 uint64 `json:"clockDarkV1,omitempty"`
	// SealerSharesV1 is the first height at which the sealer's own payout
	// entry is max(1, ceil(W / 3)) over the carried shares' summed weight
	// (their count under the count form; the project's ruling of 20:5x UK, 11
	// October 2026, the fix of a term that read the accepted entries'
	// count, one an address); below it the first form. The seventh chain
	// names its swap height; the eighth and later run the rule from
	// genesis.
	SealerSharesV1 uint64 `json:"sealerSharesV1,omitempty"`
	// FundV2 is the first height at which the fund's share is credited to
	// FundV2Address (the fund contract of record) instead of the chain's
	// stand-in fund account, and at which Finalize moves the stand-in's
	// whole balance to it in one transfer (the founder's word of 09:03 UK,
	// 14 October 2026: the stand-in's accumulated one percent moved to the
	// contract by the recorded path). Zero is off.
	FundV2        uint64         `json:"fundV2,omitempty"`
	FundV2Address common.Address `json:"fundV2Address,omitempty"`
	// TailV1 (the founder's word of 15:1x UK, 15 October 2026, the bounded
	// tail): from its height the sortition's rounds past round 0 open every
	// TailDelta seconds instead of Delta (the seal chance, which doubles a
	// round, rises with the gap past the target), and the clock floor tMin
	// reads TailTMin (lower, so the fast side alone does not hold the
	// mean). A field left zero keeps the document's.
	TailV1    uint64 `json:"tailV1,omitempty"`
	TailDelta uint64 `json:"tailDelta,omitempty"`
	TailTMin  uint64 `json:"tailTMin,omitempty"`
	// ForkLengthV1 is the first height at which the fork choice's tie
	// order is: at equal cumulative weight the branch with MORE BLOCKS
	// wins, and the lot term (PA-47's tie-break) applies only at equal
	// weight and equal length (F-44 part 3, the design seat's ruling v3
	// of 02:4x UK, 12 October 2026: the zero-carried tie). Read at the
	// longer head's height. ZERO IS OFF (geth's order: the shorter head
	// at equal weight), unlike the clock gates: a binary swapped onto a
	// running chain changes no head choice before the named height.
	ForkLengthV1 uint64 `json:"forkLengthV1,omitempty"`
	// PolicyV1 is the first height at which the by-height parts of the
	// second fault release (node/0.9.3; the project's order of 05:3x UK, 12
	// October 2026) are in force on a chain: the carry's quorum against
	// the bonded share of the epoch's table (below it the absolute
	// ceil(2 x E_att / 3)), policy part 4 (the arrival record and the
	// past-drift refusal) and part 5 (the first-seen twin as the
	// sealer's parent at equal weight), and the carried mark keyed with
	// the share's parent (below it the height-keyed mark). Sealer and
	// endorser policy and the pool's accounting, no consensus rule; ZERO
	// IS OFF, so a binary swapped onto a running chain behaves as the
	// release before it until the named height.
	PolicyV1 uint64 `json:"policyV1,omitempty"`
	// ClockV1 is the first height at which the seal clock's two measured
	// constants are re-pinned on a running chain (the outside review's condition
	// 4, C17 at a frozen resident-path tPiMs and a live-meter dMin; the project's
	// order of 09:0x UK, 12 October 2026): from it the round target and
	// the seal window read ClockTPiMs (milliseconds, the resident proof's
	// budget under the design seat's margin rule) and the seal difficulty
	// floor reads ClockDMin (the slowest admitted sealer's live
	// keyed-search rate over the target). ZERO IS OFF (the genesis
	// document's figures throughout); a node without the entry forks at
	// the height, as under the other fields, so the entry ships in the
	// binary's built-in table and no node takes a hand on a params file.
	ClockV1    uint64 `json:"clockV1,omitempty"`
	ClockTPiMs uint64 `json:"clockTPiMs,omitempty"`
	// ClockDelta is the round clock's Delta (seconds) from ClockV1 on a
	// chain whose forks entry names it (the public test network, 0.10.49,
	// 16 October 2026: the verifier's round floor parent.Time +
	// RoundStart(r), the seal target's window and the drift bound read it
	// from the height; the genesis document's Delta below it); zero keeps
	// the document's. Every node of record is on the version before the
	// height, since the floors differ across versions from it.
	ClockDelta uint64 `json:"clockDelta,omitempty"`
	ClockDMin  uint64 `json:"clockDMin,omitempty"`
	// PiVerifyV1 is the first height from which a keyed block's proof pi is
	// the CHAIN'S rule (F-461, 17 October 2026): every node verifies the
	// commitment and the proof of a keyed block at or above it whatever its
	// --igneum.pi-verify flag, so a bad proof cannot split verifying nodes
	// from the rest. Zero: the flag alone decides (the lab). Set with a
	// chain's first keyed entry, never ahead of it; a node on a chain whose
	// entry names it must be built with the verifier.
	PiVerifyV1 uint64 `json:"piVerifyV1,omitempty"`
	// PayV2 is the first height at which the Pay rule's second form
	// applies (F-63's fix (b), the design seat's ruling of 12 October 2026
	// for the tenth development chain): the sealer's term a FIXED quarter
	// of the block's miner amount whatever the share list holds; the list's
	// three quarters paid over the accepted entries by weight scaled by
	// min(W, Q) / Q, W the block's summed accepted weight and Q the lane's
	// operating target (the sortition document's sharesPerBlock at step
	// weight 1); the remainder UNISSUED, never rolled forward and never to
	// the sealer, so an empty list never pays more than a full one. ZERO IS
	// OFF (the first form throughout, the ninth chain's); a chain under the
	// second form from genesis carries 1.
	PayV2 uint64 `json:"payV2,omitempty"`
	// DiffMinV1 is the first height whose chain difficulty floors at
	// DiffMinV1Min instead of the genesis set's minimum (the security review's
	// F-86, 13 October 2026: the old public test network (chain 7320) ran at the 12 s clock floor for hours
	// because the open seat sealed at a chain difficulty near the genesis
	// minimum of 8,192 and the controller's hour-long half-life raised it
	// nineteen a block; the floor is re-pinned at the sealers' measured
	// rate times the target so the controller binds). Zero is OFF.
	DiffMinV1    uint64 `json:"diffMinV1,omitempty"`
	DiffMinV1Min uint64 `json:"diffMinV1Min,omitempty"`
	// WithdrawalIndexV1 is the first height whose payout list carries the
	// chain's global withdrawal index (ecosystem's F-87, 13 October 2026:
	// the index restarted at zero every block, and an explorer keying
	// withdrawals by index as EIP-4895 reads it refused every catch-up
	// chunk): entry i of a block's list is indexed C(parent) + i, C the
	// running count of withdrawals from genesis, carried in the state.
	// Zero is OFF (the per-block index).
	WithdrawalIndexV1 uint64 `json:"withdrawalIndexV1,omitempty"`
}

// IgneumFees are the EIP-1559 constants as Igneum runs them
// (consensus/igneum/fees.Params, PB-03), with the base-fee floor.
type IgneumFees struct {
	ElasticityMultiplier     uint64   `json:"elasticityMultiplier"`
	BaseFeeChangeDenominator uint64   `json:"baseFeeChangeDenominator"`
	InitialBaseFee           *big.Int `json:"initialBaseFee"`
	MinBaseFee               *big.Int `json:"minBaseFee"`
}

// String implements the stringer interface, returning the consensus engine details.
func (c *IgneumConfig) String() string {
	return "igneum/" + c.Algorithm
}
