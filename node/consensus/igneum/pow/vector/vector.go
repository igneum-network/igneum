// Package vector is VectorWork's relation menu: one relation per work
// coordinate (docs/consensus/VECTORWORK.md), each built from its own
// algorithm name and document, dispatched by the block's coordinate tag.
// The menu itself implements pow.Algorithm by its first coordinate for the
// engine's coordinate-free reads (the name, the vectors); every hash of a
// header or a share runs on the coordinate's own algorithm through
// ByCoordinate.
package vector

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"math/big"

	"encoding/json"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// Name is the registry's view of the menu.
const Name = "vectorwork"

// MaxCoordinates is the registration's bound: the four of the design,
// raised to eight for the fifth coordinate (VECTORWORK.md revision 16,
// the founder's order of 17:5x UK, 13 October 2026: version 16 as
// coordinate 4), to sixteen for FORM-FORGE as vectorwork8's ninth
// coordinate (the founder's order of 22:3x UK, 14 October 2026); the
// rules read the registered count n everywhere (the n-th root, the
// coverage over n, the shares over n) and the header's tag is one byte.
const MaxCoordinates = 32

var (
	ErrCoordinate = errors.New("vectorwork: coordinate out of range")
	ErrShape      = errors.New("vectorwork: the registration's shape")
)

// Coordinate is one built coordinate.
type Coordinate struct {
	Name      string
	Algorithm pow.Algorithm
	// Unkeyed is a keyed document's UNKEYED TWIN (pow.UnkeyedTwin: the same
	// document without bit 10 and keyed_rounds), built beside the keyed
	// form, which rides the twin's dataset (one dataset for the pair); nil
	// for an unkeyed document. The work package's open-seat template and
	// the share lane are hashed with it (0.10.54).
	Unkeyed       pow.Algorithm
	UnkeyedParams []byte
	Target    uint64
	DRef      *big.Int
	// Activation is the height the coordinate is active from (0 genesis);
	// RetiredFrom the height its form is retired from (0 never);
	// Successor the index of its successor form (nil none).
	Activation  uint64
	RetiredFrom uint64
	Successor   *uint64
	// RevealBlocks is the coordinate's own programme reveal depth (0 the
	// chain's).
	RevealBlocks uint64
	DMin         *big.Int // nil: the chain's minimum
	WorkScale    uint64   // 0 or 1: the plain unit
	LagCap       uint64   // 0: off (the anchored lag)
	// Changes are the coordinate's in-place document changes
	// (LANE-MODIFICATION.md section 1), ascending by height: from each
	// entry's height the coordinate's algorithm, reference, floor and
	// cap are the entry's (CoordinateAt, ByCoordinateAt); the fields
	// above are the base form's (the registered document, in force from
	// the activation to the first change).
	Changes []CoordinateChange
}

// CoordinateChange is one in-force document of a coordinate from a height:
// the coordinate's own algorithm built from the entry's document, with the
// entry's reference, floor and cap. The coordinate's index, name, weight,
// routing and state are untouched by it.
type CoordinateChange struct {
	From      uint64
	Doc       []byte // the entry's document bytes (a landing reuses a form built for the same entry)
	Algorithm pow.Algorithm
	// Unkeyed is the change's document's unkeyed twin (nil for an unkeyed
	// document), as Coordinate.Unkeyed.
	Unkeyed       pow.Algorithm
	UnkeyedParams []byte
	DRef      *big.Int
	DMin      *big.Int // nil: the chain's minimum
	LagCap    uint64
}

// StepDownBlocks is how far past a change's height a coordinate's
// superseded form keeps its full context on a sealing node (the finality
// depth: a reorganisation below it is refused, so a block under the old
// form is verified on a light context past it); then the form steps down
// to light (the security review's read of the entry, 16 October 2026: every
// in-place change on the sealing coordinate would otherwise hold a full
// dataset beside the base's for the chain's life, F-215's shape again).
const StepDownBlocks = 64

// docVersion is the "version" a document names (the family of an
// IgneumHash document), and whether it names one.
func docVersion(params []byte) (float64, bool) {
	var m map[string]any
	if len(bytes.TrimSpace(params)) == 0 || json.Unmarshal(params, &m) != nil {
		return 0, false
	}
	v, ok := m["version"].(float64)
	return v, ok
}

// liveChanges are a coordinate's document changes in force or to come:
// the non-void entries whose height no void entry names, ascending.
func liveChanges(c ctypes.IgneumCoordinate) []ctypes.IgneumDocumentChange {
	voided := map[uint64]bool{}
	for _, d := range c.DocumentChanges {
		if d.Void {
			voided[d.Height] = true
		}
	}
	var out []ctypes.IgneumDocumentChange
	for _, d := range c.DocumentChanges {
		if !d.Void && !voided[d.Height] {
			out = append(out, d)
		}
	}
	return out
}

// ReanchorLag is the folded lag re-anchored where a coordinate's reference
// changes between a block and its child (an in-place document change at
// the child's height; the security review's read of 16 October 2026: a change's
// reference is not a difficulty reset): the controller's difficulty is
// dRef x 2^(-lag / H), so the lag L' = L + H x log2(dRef' / dRef) gives the
// child the difficulty this block's would have, continuous at A; only the
// controller's target moves. The integer form floors as DerivedLagCap does.
func ReanchorLag(halfLife uint64, lag int64, oldDRef, newDRef uint64) int64 {
	if halfLife == 0 || oldDRef == 0 || newDRef == 0 || oldDRef == newDRef {
		return lag
	}
	return lag + int64(math.Floor(float64(halfLife)*math.Log2(float64(newDRef)/float64(oldDRef))))
}

// at is the coordinate's form in force at height: the base form, or the
// last change at or under the height.
func (c Coordinate) at(height uint64) Coordinate {
	f := c
	for _, ch := range c.Changes {
		if ch.From > height {
			break
		}
		f.Algorithm, f.DRef, f.DMin, f.LagCap = ch.Algorithm, ch.DRef, ch.DMin, ch.LagCap
		f.Unkeyed, f.UnkeyedParams = ch.Unkeyed, ch.UnkeyedParams
	}
	return f
}

// buildTwin builds a keyed document's unkeyed twin beside its form: the
// twin's algorithm from the same constructor, the keyed form riding the
// twin's context (SetDonor), so the pair holds one dataset; nil, nil for
// an unkeyed document.
func buildTwin(name string, doc []byte, keyed pow.Algorithm, build func(name string, params []byte) (pow.Algorithm, error)) (pow.Algorithm, []byte, error) {
	twin, err := pow.UnkeyedTwin(doc)
	if err != nil || twin == nil {
		return nil, nil, err
	}
	u, err := build(name, twin)
	if err != nil {
		return nil, nil, fmt.Errorf("the unkeyed twin: %w", err)
	}
	if r, ok := keyed.(interface{ SetDonor(any) }); ok {
		r.SetDonor(u)
	}
	return u, twin, nil
}

// twins are every form's unkeyed twin, in the forms' order (nil where the
// form is unkeyed).
func (c Coordinate) twins() []pow.Algorithm {
	out := []pow.Algorithm{c.Unkeyed}
	for _, ch := range c.Changes {
		out = append(out, ch.Unkeyed)
	}
	return out
}

// forms are every algorithm of the coordinate (the base and every change),
// in order.
func (c Coordinate) forms() []pow.Algorithm {
	out := []pow.Algorithm{c.Algorithm}
	for _, ch := range c.Changes {
		out = append(out, ch.Algorithm)
	}
	return out
}

// Menu is the built vector form.
type menu struct {
	v        *ctypes.IgneumVector
	coords   []Coordinate
	halfLife uint64
	dtMax    uint64
	minimum  bool
	// seal is the node's sealing coordinate (SetSealCoordinate) and full
	// the full-dataset choice: the choice reaches the sealing coordinate's
	// relation alone, the others hold light contexts (a box node with
	// three coordinates resident held 6.4 GB, 13 October 2026, 04:0x UK).
	seal *uint8
	full bool
	// single is the one-resident-context form (SetSingleDataset): recorded
	// here so a Replace carries it to the adapters a landing builds anew
	// (F-215, 16 October 2026: Replace carried the full choice and the
	// seal coordinate and not the single form, so every coordinate a
	// landing changed came back in the lookahead form, two epochs' full
	// datasets resident beside a third held, 18 to 26 GiB a node).
	single bool
	// head is the chain head the engine last anchored (Anchor); liveSig the
	// coordinates' forms in force and live at it, re-applied through
	// applyFull when it changes (a change coming into force moves the
	// donor to its form; StepDownBlocks past it the superseded form steps
	// down to light).
	head    uint64
	liveSig string
	seed    bool
	// coverage is revision 13's rule (section 2): the branch's coverage
	// count in its trailing window of `window` blocks since the common
	// ancestor, then the geometric mean of the window's work over the
	// covered coordinates; branch-relative, never from genesis.
	coverage bool
	window   uint64
	// payChanges are the height-gated pay weight changes, ascending.
	payChanges []ctypes.IgneumPayChange
	// constantsChanges are the height-gated clock constant changes
	// (F-151), ascending; the readers resolve them cumulatively.
	constantsChanges []ctypes.IgneumConstantsChange
	// balancedFrom is the balanced reward's activation height (0 off);
	// balancedV2From the breadth form's (section 9); phiDen 1/phi.
	balancedFrom       uint64
	balancedV2From     uint64
	balancedSealerFrom uint64
	balancedLanesFrom  uint64
	balancedLanes      []uint8
	lanesChanges       []ctypes.IgneumLanesChange
	graphSharesFrom    uint64
	graphSharesPerBlk  uint64
	phiDen             uint64
	phiFrom            uint64
	phiDenV2           uint64
	balancedV3From     uint64
	balancedMinFrom    uint64
	sealerFoldFrom     uint64
	sealerFoldListFrom uint64
	sealingBreadthFrom uint64
	noticeBlocks       uint64
	maxLiveBlocks      uint64
	// windowBranch is revision 16's window form: the window over each
	// branch with the common prefix (the suffix alone otherwise).
	windowBranch bool
	payCoord     bool
	shares       []uint64
}

// PayShares is the registration's issuance weights per coordinate under
// the coordinate Pay rule (nil: equal shares).
func (m *menu) PayShares() []uint64 { return m.shares }

// PaySharesAt is the pay weights in force at a height: the last PayChanges
// entry at or below the height, else PayShares.
func (m *menu) PaySharesAt(height uint64) []uint64 {
	w := m.shares
	for _, c := range m.payChanges {
		if height >= c.Height {
			w = c.PayShares
		}
	}
	return w
}

// BalancedFrom is the balanced reward's activation height (0 off).
func (m *menu) BalancedFrom() uint64 { return m.balancedFrom }

// BalancedV2From is the breadth form's activation height (0 off) and
// PhiDen the soft floor's denominator.
func (m *menu) BalancedV2From() uint64 { return m.balancedV2From }
func (m *menu) PhiDen() uint64         { return m.phiDen }

// BalancedV3At reports F-124's form at a height (no fallback weight, the
// unserved lanes outside the breadth).
func (m *menu) BalancedV3At(height uint64) bool {
	return m.balancedV3From > 0 && height >= m.balancedV3From
}

// BalancedMinAt reports the minimum pay rule at a height (the key's
// balanced work the minimum over the breadth lanes).
func (m *menu) BalancedMinAt(height uint64) bool {
	return m.balancedMinFrom > 0 && height >= m.balancedMinFrom
}

// SealerFoldAt reports the fold at a height (F-165): the sealer's quarter
// into the members' pot, from SealerFoldFrom and only while the minimum
// rule is in force (the fold is the minimum rule's complement; inert
// elsewhere, so a chain without the rule keeps the quarter).
func (m *menu) SealerFoldAt(height uint64) bool {
	return m.sealerFoldFrom > 0 && height >= m.sealerFoldFrom && m.BalancedMinAt(height)
}

// SealerFoldListAt reports the first pay form's fold at a height (F-180):
// from SealerFoldListFrom and only while the minimum rule is in force.
func (m *menu) SealerFoldListAt(height uint64) bool {
	return m.sealerFoldListFrom > 0 && height >= m.sealerFoldListFrom && m.BalancedMinAt(height)
}

// SealableAt reports whether coordinate c may seal a block at a height
// (F-166): active there and, from SealingBreadthFrom, with a pay weight
// above zero at the height (the registered weight, PaySharesAt; absent
// weights read equal, so every active coordinate seals).
func (m *menu) SealableAt(c uint8, height uint64) bool {
	if !m.ActiveAt(c, height) {
		return false
	}
	if m.sealingBreadthFrom == 0 || height < m.sealingBreadthFrom {
		return true
	}
	w := m.PaySharesAt(height)
	if len(w) == 0 {
		return true
	}
	return int(c) < len(w) && w[c] > 0
}

// SealingBreadthFrom is F-166's activation height (0 off).
func (m *menu) SealingBreadthFrom() uint64 { return m.sealingBreadthFrom }

// PhiDenAt is 1/phi in force at a height: BalancedPhiDenV2 from
// BalancedPhiFrom (0 there means no phi floor), else PhiDen.
func (m *menu) PhiDenAt(height uint64) uint64 {
	if m.phiFrom > 0 && height >= m.phiFrom {
		return m.phiDenV2
	}
	return m.phiDen
}

// BalancedSealerFrom is the third cut's activation height (0 off): sealed
// blocks as the sealer's lane work.
func (m *menu) BalancedSealerFrom() uint64 { return m.balancedSealerFrom }

// GraphSharesFrom is the typed list's activation height (0 off) and
// GraphSharesPerBlock a block's cap on graph entries (0 none).
func (m *menu) GraphSharesFrom() uint64     { return m.graphSharesFrom }
func (m *menu) GraphSharesPerBlock() uint64 { return m.graphSharesPerBlk }

// BalancedLanesAt is the lanes the breadth runs over at a height: the
// registration's balancedLanes from balancedLanesFrom, else every
// coordinate.
func (m *menu) BalancedLanesAt(height uint64) []uint8 {
	for i := len(m.lanesChanges) - 1; i >= 0; i-- {
		if height >= m.lanesChanges[i].Height {
			return m.lanesChanges[i].Lanes
		}
	}
	if m.balancedLanesFrom > 0 && height >= m.balancedLanesFrom && len(m.balancedLanes) > 0 {
		return m.balancedLanes
	}
	out := make([]uint8, len(m.coords))
	for i := range out {
		out[i] = uint8(i)
	}
	return out
}

// PayChanges is the registration's height-gated pay weight changes.
func (m *menu) PayChanges() []ctypes.IgneumPayChange { return m.payChanges }

// PayByCoordinate reports the registration's coordinate Pay rule
// (revision 13, section 9).
func (m *menu) PayByCoordinate() bool { return m.payCoord }

// DefaultWindow is K_w when the registration names none: 16 target
// intervals at four coordinates.
const DefaultWindow = 64

// ActiveAt reports whether coordinate c is a consensus coordinate at the
// height (its activation height reached; genesis coordinates always).
func (m *menu) ActiveAt(c uint8, height uint64) bool {
	if int(c) >= len(m.coords) {
		return false
	}
	co := m.coords[c]
	if height < co.Activation {
		return false
	}
	// REVISION-CADENCE.md cut A: a retired form is no coordinate from
	// its height, and a form past maxLiveBlocks with no successor named
	// is invalid for sealing from there.
	if co.RetiredFrom > 0 && height >= co.RetiredFrom {
		return false
	}
	if m.maxLiveBlocks > 0 && co.Successor == nil && height >= co.Activation+m.maxLiveBlocks {
		return false
	}
	return true
}

// RevealOf is the programme reveal depth registered for the coordinate
// whose BASE algorithm is a (0 where none is, or the chain's applies): the
// form before F-730's gate, kept as it was so every block sealed under it
// verifies as it was sealed. It matches the base instance alone: an
// in-place document change (CoordinateChange) builds a second instance of
// the lane's document that this match never sees, so from the change's
// height the engine fell back to the relation's default of two in place of
// the document's registered depth (F-730, the public test network from
// 13,800, 18 October 2026: the sealers built with two, the pool's shares
// built with the document's 64 were refused as an invalid mix digest).
// From the chain's gate the engine reads DocumentRevealOf instead.
func (m *menu) RevealOf(a pow.Algorithm) uint64 {
	// The deepest reveal among the coordinates sharing the algorithm (one
	// built object can serve two coordinates with one document; the null
	// relation is one object for all).
	var r uint64
	for i := range m.coords {
		if m.coords[i].Algorithm == a && m.coords[i].RevealBlocks > r {
			r = m.coords[i].RevealBlocks
		}
	}
	return r
}

// DocumentRevealOf is the programme reveal depth registered for the
// coordinate ANY instance of whose document is a: the base form, every
// in-place document change's form (the form in force at a height,
// ByCoordinateAt) and their unkeyed twins (UnkeyedAlgorithmAt). The reveal
// is the coordinate's registration (revealBlocks), not a document's, so
// every instance of the lane's document answers the same depth (F-730's
// cure). The deepest among the coordinates sharing the object, as
// RevealOf; 0 where none names one.
func (m *menu) DocumentRevealOf(a pow.Algorithm) uint64 {
	if a == nil {
		return 0
	}
	var r uint64
	for i := range m.coords {
		if m.coords[i].RevealBlocks > r && m.coords[i].instanceOf(a) {
			r = m.coords[i].RevealBlocks
		}
	}
	return r
}

// instanceOf reports whether a is one of the coordinate's built instances:
// the base form, a change's form, or either's unkeyed twin.
func (c Coordinate) instanceOf(a pow.Algorithm) bool {
	if c.Algorithm == a || (c.Unkeyed != nil && c.Unkeyed == a) {
		return true
	}
	for _, ch := range c.Changes {
		if ch.Algorithm == a || (ch.Unkeyed != nil && ch.Unkeyed == a) {
			return true
		}
	}
	return false
}

// NoticeBlocks and MaxLiveBlocks are the cadence's registration constants.
func (m *menu) NoticeBlocks() uint64  { return m.noticeBlocks }
func (m *menu) MaxLiveBlocks() uint64 { return m.maxLiveBlocks }

// Coverage reports whether the registration's fork rule is revision 13's
// coverage rule, and Window its K_w.
func (m *menu) Coverage() bool { return m.coverage }
func (m *menu) Window() uint64 { return m.window }

// WindowBranch reports revision 16's window form (the coverage window over
// each branch with the common prefix, not the suffix alone).
func (m *menu) WindowBranch() bool { return m.windowBranch }

// SeedGenesis reports the registration's genesis seed (revision 9).
func (m *menu) SeedGenesis() bool { return m.seed }

// Validate checks a registration's shape: two to MaxCoordinates, every
// one named with an algorithm, a target and a reference difficulty above
// zero, the half-life above zero, the rule geometric or minimum.
func Validate(v *ctypes.IgneumVector) error {
	if v == nil {
		return fmt.Errorf("%w: no vector block", ErrShape)
	}
	if n := len(v.Coordinates); n < 2 || n > MaxCoordinates {
		return fmt.Errorf("%w: %d coordinates (2 to %d)", ErrShape, n, MaxCoordinates)
	}
	for i, c := range v.Coordinates {
		if c.Name == "" || c.Algorithm == "" {
			return fmt.Errorf("%w: coordinate %d without a name or an algorithm", ErrShape, i)
		}
		if c.Target == 0 || c.DRef == 0 {
			return fmt.Errorf("%w: coordinate %d (%s) needs a target and a reference difficulty above zero", ErrShape, i, c.Name)
		}
	}
	if v.HalfLife == 0 {
		return fmt.Errorf("%w: the half-life is zero", ErrShape)
	}
	switch v.Rule {
	case "", "geometric", "minimum", "coverage":
	default:
		return fmt.Errorf("%w: rule %q (geometric, minimum or coverage)", ErrShape, v.Rule)
	}
	switch v.WindowForm {
	case "", "suffix", "branch":
	default:
		return fmt.Errorf("%w: windowForm %q (suffix or branch)", ErrShape, v.WindowForm)
	}
	if v.RegistryKeyNext != (common.Address{}) && v.RegistryKeyFrom == 0 {
		return fmt.Errorf("%w: registryKeyNext without registryKeyFrom", ErrShape)
	}
	for i, c := range v.Coordinates {
		if c.RetiredFrom > 0 && c.RetiredFrom <= c.Activation {
			return fmt.Errorf("%w: coordinate %d (%s) retires at %d, not above its activation %d", ErrShape, i, c.Name, c.RetiredFrom, c.Activation)
		}
		if c.Successor != nil && (int(*c.Successor) >= len(v.Coordinates) || int(*c.Successor) == i) {
			return fmt.Errorf("%w: coordinate %d (%s) names successor %d", ErrShape, i, c.Name, *c.Successor)
		}
		// The in-place document changes (LANE-MODIFICATION.md section 1a):
		// the changes ascending, above the activation, under the
		// retirement where one is set, each a document of the
		// coordinate's own family (the version the coordinate's document
		// names: an in-place entry changes an INSTANCE, never the class;
		// the security review's read), a reference above zero, a floor above zero
		// at or under the reference (F-166's floor condition, as the
		// registration check runs it) and the cap the reference and floor
		// derive (F-151's cap, no open default); a void entry (section 1b)
		// names a change of this list by its height, carries nothing else
		// and voids it once.
		baseVersion, baseHasVersion := docVersion(c.AlgorithmParams)
		lastChange := uint64(0)
		voided := map[uint64]bool{}
		changes := map[uint64]bool{}
		for j, d := range c.DocumentChanges {
			if d.Height == 0 {
				return fmt.Errorf("%w: coordinate %d (%s) documentChanges[%d] at height 0", ErrShape, i, c.Name, j)
			}
			if d.Void {
				if len(bytes.TrimSpace(d.AlgorithmParams)) != 0 || d.DRef != 0 || d.DMin != 0 || d.LagCap != 0 {
					return fmt.Errorf("%w: coordinate %d (%s) documentChanges[%d] is a void entry carrying a document or figures", ErrShape, i, c.Name, j)
				}
				if !changes[d.Height] || voided[d.Height] {
					return fmt.Errorf("%w: coordinate %d (%s) documentChanges[%d] voids no change of this list at %d (or one voided already)", ErrShape, i, c.Name, j, d.Height)
				}
				voided[d.Height] = true
				continue
			}
			if d.Height <= c.Activation || d.Height <= lastChange {
				return fmt.Errorf("%w: coordinate %d (%s) documentChanges[%d] at %d is not above the activation and the change before it", ErrShape, i, c.Name, j, d.Height)
			}
			if c.RetiredFrom > 0 && d.Height >= c.RetiredFrom {
				return fmt.Errorf("%w: coordinate %d (%s) documentChanges[%d] at %d is not under the retirement %d", ErrShape, i, c.Name, j, d.Height, c.RetiredFrom)
			}
			if len(bytes.TrimSpace(d.AlgorithmParams)) == 0 || d.DRef == 0 {
				return fmt.Errorf("%w: coordinate %d (%s) documentChanges[%d] needs a document and a reference difficulty above zero", ErrShape, i, c.Name, j)
			}
			if v, has := docVersion(d.AlgorithmParams); has != baseHasVersion || v != baseVersion {
				return fmt.Errorf("%w: coordinate %d (%s) documentChanges[%d] names another family (the document's version %v, the coordinate's %v): an in-place change keeps the class, a change of class is a succession", ErrShape, i, c.Name, j, v, baseVersion)
			}
			if d.DMin == 0 || d.DRef < d.DMin {
				return fmt.Errorf("%w: coordinate %d (%s) documentChanges[%d] needs a floor above zero at or under its reference (dMin %d, dRef %d)", ErrShape, i, c.Name, j, d.DMin, d.DRef)
			}
			if want := DerivedLagCap(v.HalfLife, d.DRef, d.DMin); d.LagCap != want {
				return fmt.Errorf("%w: coordinate %d (%s) documentChanges[%d] carries lagCap %d, the reference and floor derive %d", ErrShape, i, c.Name, j, d.LagCap, want)
			}
			changes[d.Height] = true
			lastChange = d.Height
		}
	}
	for _, l := range v.BalancedLanes {
		if int(l) >= len(v.Coordinates) {
			return fmt.Errorf("%w: balancedLanes names coordinate %d of %d", ErrShape, l, len(v.Coordinates))
		}
		if v.Coordinates[l].Sponsored {
			return fmt.Errorf("%w: balancedLanes names coordinate %d (%s): a sponsored lane is outside the breadth", ErrShape, l, v.Coordinates[l].Name)
		}
	}
	if v.WindowForm == "branch" && v.Rule != "coverage" {
		return fmt.Errorf("%w: windowForm branch needs the coverage rule", ErrShape)
	}
	for j, c := range v.BalancedLanesChanges {
		if c.Height == 0 || (j > 0 && c.Height <= v.BalancedLanesChanges[j-1].Height) {
			return fmt.Errorf("%w: balancedLanesChanges[%d] at %d is not above the entry before it", ErrShape, j, c.Height)
		}
		if len(c.Lanes) == 0 {
			return fmt.Errorf("%w: balancedLanesChanges[%d] names no lane", ErrShape, j)
		}
		seen := map[uint8]bool{}
		for _, l := range c.Lanes {
			if int(l) >= len(v.Coordinates) || seen[l] {
				return fmt.Errorf("%w: balancedLanesChanges[%d] names coordinate %d of %d (or twice)", ErrShape, j, l, len(v.Coordinates))
			}
			if v.Coordinates[l].Sponsored {
				return fmt.Errorf("%w: balancedLanesChanges[%d] names coordinate %d (%s): a sponsored lane is outside the breadth", ErrShape, j, l, v.Coordinates[l].Name)
			}
			seen[l] = true
		}
	}
	for j, c := range v.PayChanges {
		if c.Height == 0 || (j > 0 && c.Height <= v.PayChanges[j-1].Height) {
			return fmt.Errorf("%w: payChanges[%d] at height %d is not above the previous entry", ErrShape, j, c.Height)
		}
		// An entry written before a coordinate was added names fewer
		// weights (the stored entries are immutable on admission); the
		// missing weights read 0, the added coordinate unpaid until a
		// later entry names it (the Forge/Anvil landing, 15 October 2026).
		if len(c.PayShares) == 0 || len(c.PayShares) > len(v.Coordinates) {
			return fmt.Errorf("%w: payChanges[%d] names %d weights for %d coordinates", ErrShape, j, len(c.PayShares), len(v.Coordinates))
		}
		// A zero weight in a payChanges entry RETIRES the coordinate from
		// that height (the project's word of 21:2x UK, 14 October 2026:
		// vectorwork8's version-17 texture retired at 17b's height, its
		// pay share 0, unserved and outside the breadth); at least one
		// weight stays positive.
		var sum uint64
		for i, w := range c.PayShares {
			sum += w
			if w != 0 && v.Coordinates[i].Sponsored {
				return fmt.Errorf("%w: payChanges[%d] pays coordinate %d (%s): a sponsored lane takes no share of the subsidy", ErrShape, j, i, v.Coordinates[i].Name)
			}
		}
		if sum == 0 {
			return fmt.Errorf("%w: payChanges[%d] retires every coordinate", ErrShape, j)
		}
	}
	for j, c := range v.ConstantsChanges {
		if c.Height == 0 || (j > 0 && c.Height <= v.ConstantsChanges[j-1].Height) {
			return fmt.Errorf("%w: constantsChanges[%d] at height %d is not above the previous entry", ErrShape, j, c.Height)
		}
		if c.HalfLife == 0 && c.DtMax == 0 && c.TMin == 0 && c.TailDelta == 0 {
			return fmt.Errorf("%w: constantsChanges[%d] changes nothing", ErrShape, j)
		}
	}
	if len(v.PayShares) > 0 {
		if len(v.PayShares) != len(v.Coordinates) {
			return fmt.Errorf("%w: payShares names %d weights for %d coordinates", ErrShape, len(v.PayShares), len(v.Coordinates))
		}
		for i, w := range v.PayShares {
			// A sponsored lane's share is zero at every height (the
			// sponsor pays its keys through the escrow; the subsidy's
			// split is the base lanes' alone); every other zero is a
			// shape fault.
			if w == 0 && !v.Coordinates[i].Sponsored {
				return fmt.Errorf("%w: payShares[%d] is zero", ErrShape, i)
			}
			if w != 0 && v.Coordinates[i].Sponsored {
				return fmt.Errorf("%w: payShares[%d] pays %s: a sponsored lane takes no share of the subsidy", ErrShape, i, v.Coordinates[i].Name)
			}
		}
	}
	if v.Rule == "coverage" && v.SeedGenesis {
		// Revision 13 (the project's ruling, C-28 row 50): the seed is read by
		// nothing under the coverage rule and is not carried.
		return fmt.Errorf("%w: seedGenesis beside rule coverage (revision 13 registers no seed)", ErrShape)
	}
	return nil
}

// New builds the menu: every coordinate's algorithm through build (the
// registry's constructor), in order.
func New(v *ctypes.IgneumVector, build func(name string, params []byte) (pow.Algorithm, error)) (*Menu, error) {
	inner, err := newMenu(v, build)
	if err != nil {
		return nil, err
	}
	h := &Menu{build: build}
	h.p.Store(inner)
	return h, nil
}

func newMenu(v *ctypes.IgneumVector, build func(name string, params []byte) (pow.Algorithm, error)) (*menu, error) {
	return newMenuReusing(v, build, nil)
}

// newMenuReusing is newMenu with the algorithms of old reused for every
// coordinate whose name, algorithm and document are the old's at the same
// index (a landed re-registration appends and retires; the verifier
// contexts of the coordinates already built are not built twice).
func newMenuReusing(v *ctypes.IgneumVector, build func(name string, params []byte) (pow.Algorithm, error), old *menu) (*menu, error) {
	if err := Validate(v); err != nil {
		return nil, err
	}
	m := &menu{v: v, halfLife: v.HalfLife, dtMax: v.DtMax, minimum: v.Rule == "minimum"}
	if m.dtMax == 0 {
		m.dtMax = v.HalfLife
	}
	m.seed = v.SeedGenesis
	m.payCoord = v.PayByCoordinate
	m.shares = append([]uint64(nil), v.PayShares...)
	m.balancedFrom = v.BalancedFrom
	m.balancedV2From = v.BalancedV2From
	m.balancedSealerFrom = v.BalancedSealerFrom
	m.balancedLanesFrom = v.BalancedLanesFrom
	m.balancedLanes = append([]uint8(nil), v.BalancedLanes...)
	for _, c := range v.BalancedLanesChanges {
		m.lanesChanges = append(m.lanesChanges, ctypes.IgneumLanesChange{Height: c.Height, Lanes: append([]uint8(nil), c.Lanes...)})
	}
	m.graphSharesFrom = v.GraphSharesFrom
	m.graphSharesPerBlk = v.GraphSharesPerBlock
	m.phiDen = v.BalancedPhiDen
	if m.phiDen == 0 {
		m.phiDen = 16
	}
	m.phiFrom = v.BalancedPhiFrom
	m.phiDenV2 = v.BalancedPhiDenV2
	m.balancedV3From = v.BalancedV3From
	m.balancedMinFrom = v.BalancedMinFrom
	m.sealerFoldFrom = v.SealerFoldFrom
	m.sealerFoldListFrom = v.SealerFoldListFrom
	m.sealingBreadthFrom = v.SealingBreadthFrom
	m.noticeBlocks = v.NoticeBlocks
	m.maxLiveBlocks = v.MaxLiveBlocks
	for _, c := range v.PayChanges {
		w := make([]uint64, len(v.Coordinates))
		copy(w, c.PayShares)
		m.payChanges = append(m.payChanges, ctypes.IgneumPayChange{Height: c.Height, PayShares: w})
	}
	m.constantsChanges = append([]ctypes.IgneumConstantsChange(nil), v.ConstantsChanges...)
	m.coverage = v.Rule == "coverage"
	m.window = v.Window
	m.windowBranch = v.WindowForm == "branch"
	if m.coverage && m.window == 0 {
		m.window = DefaultWindow
	}
	for i, c := range v.Coordinates {
		var a pow.Algorithm
		var err error
		if old != nil && i < len(old.coords) && old.v != nil && i < len(old.v.Coordinates) && sameCoordinateDocument(old.v.Coordinates[i], c) {
			a = old.coords[i].Algorithm
		} else {
			a, err = build(c.Algorithm, c.AlgorithmParams)
		}
		if err != nil {
			return nil, fmt.Errorf("vectorwork: coordinate %d (%s): %w", i, c.Name, err)
		}
		coord := Coordinate{Name: c.Name, Algorithm: a, Target: c.Target, DRef: new(big.Int).SetUint64(c.DRef)}
		if old != nil && i < len(old.coords) && old.coords[i].Algorithm == a {
			coord.Unkeyed, coord.UnkeyedParams = old.coords[i].Unkeyed, old.coords[i].UnkeyedParams
		} else if coord.Unkeyed, coord.UnkeyedParams, err = buildTwin(c.Algorithm, c.AlgorithmParams, a, build); err != nil {
			return nil, fmt.Errorf("vectorwork: coordinate %d (%s): %w", i, c.Name, err)
		}
		if c.DMin > 0 {
			coord.DMin = new(big.Int).SetUint64(c.DMin)
		}
		coord.WorkScale = c.WorkScale
		coord.Activation = c.Activation
		coord.RetiredFrom = c.RetiredFrom
		coord.RevealBlocks = c.RevealBlocks
		if c.Successor != nil {
			sc := *c.Successor
			coord.Successor = &sc
		}
		coord.LagCap = c.LagCap
		// The in-place document changes in force or to come (a voided
		// entry builds nothing): one algorithm per entry, the old menu's
		// reused where it built the same entry (its height and document)
		// for the coordinate (a landing that appends one change builds the
		// one, not the forms already built).
		for j, d := range liveChanges(c) {
			var fa pow.Algorithm
			if old != nil && i < len(old.coords) && old.v != nil && i < len(old.v.Coordinates) && old.v.Coordinates[i].Algorithm == c.Algorithm {
				for _, oc := range old.coords[i].Changes {
					if oc.From == d.Height && bytes.Equal(bytes.TrimSpace(oc.Doc), bytes.TrimSpace(d.AlgorithmParams)) {
						fa = oc.Algorithm
						break
					}
				}
			}
			if fa == nil {
				fa, err = build(c.Algorithm, d.AlgorithmParams)
				if err != nil {
					return nil, fmt.Errorf("vectorwork: coordinate %d (%s) document change %d at %d: %w", i, c.Name, j, d.Height, err)
				}
			}
			ch := CoordinateChange{From: d.Height, Doc: append([]byte(nil), d.AlgorithmParams...), Algorithm: fa, DRef: new(big.Int).SetUint64(d.DRef), LagCap: d.LagCap}
			if old != nil && i < len(old.coords) {
				for _, oc := range old.coords[i].Changes {
					if oc.Algorithm == fa {
						ch.Unkeyed, ch.UnkeyedParams = oc.Unkeyed, oc.UnkeyedParams
					}
				}
			}
			if ch.Unkeyed == nil {
				if ch.Unkeyed, ch.UnkeyedParams, err = buildTwin(c.Algorithm, d.AlgorithmParams, fa, build); err != nil {
					return nil, fmt.Errorf("vectorwork: coordinate %d (%s) document change %d at %d: %w", i, c.Name, j, d.Height, err)
				}
			}
			if d.DMin > 0 {
				ch.DMin = new(big.Int).SetUint64(d.DMin)
			}
			coord.Changes = append(coord.Changes, ch)
		}
		m.coords = append(m.coords, coord)
	}
	return m, nil
}

// DerivedLagCap is L_c = floor(H x log2(D_ref_c / Dmin_c)) (VECTORWORK.md
// section 4 at a3b8803c9: the cap is derived from the coordinate's
// reference and floor and is no free field; a registration whose lagCap
// differs is refused at the registration check beside the floor check).
// The floor is the coordinate's own dMin, else the chain's minimum.
func DerivedLagCap(halfLife, dRef, floor uint64) uint64 {
	if halfLife == 0 || floor == 0 || dRef <= floor {
		return 0
	}
	return uint64(math.Floor(float64(halfLife) * math.Log2(float64(dRef)/float64(floor))))
}

// Count is the number of coordinates.
func (m *menu) Count() int { return len(m.coords) }

// Minimum reports the minimum rule (else the geometric mean).
func (m *menu) Minimum() bool { return m.minimum }

// HalfLife and DtMax are the controllers' constants (seconds).
func (m *menu) HalfLife() uint64 { return m.halfLife }
func (m *menu) DtMax() uint64    { return m.dtMax }

// ConstantsChanges is the registration's height-gated clock constant
// changes (F-151).
func (m *menu) ConstantsChanges() []ctypes.IgneumConstantsChange { return m.constantsChanges }

// ConstantsAt is the clock constants in force at a height (F-151): the
// registration's halfLife and dtMax with the seal clock's tMin and
// tailDelta at zero (the chain's own), each overridden by the last
// constantsChanges entry at or under the height that names it. The entry
// at height A binds the block at A and every block above it, so the block
// at A - 1 reads the constants before it.
func (m *menu) ConstantsAt(height uint64) ctypes.IgneumConstantsChange {
	out := ctypes.IgneumConstantsChange{HalfLife: m.halfLife, DtMax: m.dtMax}
	for _, c := range m.constantsChanges {
		if height < c.Height {
			break
		}
		out.Height = c.Height
		if c.HalfLife > 0 {
			out.HalfLife = c.HalfLife
		}
		if c.DtMax > 0 {
			out.DtMax = c.DtMax
		}
		if c.TMin > 0 {
			out.TMin = c.TMin
		}
		if c.TailDelta > 0 {
			out.TailDelta = c.TailDelta
		}
	}
	return out
}

// HalfLifeAt and DtMaxAt are the controllers' constants at a height.
func (m *menu) HalfLifeAt(height uint64) uint64 { return m.ConstantsAt(height).HalfLife }
func (m *menu) DtMaxAt(height uint64) uint64    { return m.ConstantsAt(height).DtMax }

// LagCapAt is coordinate c's folded-lag cap at a height: the registered
// cap while the half-life is the registration's, else the cap derived
// from the half-life in force (DerivedLagCap, the same rule the
// registration's cap was checked against), so a changed half-life keeps
// the cap's meaning (the lag at which the controller reads the floor).
// Zero where the coordinate registers no cap.
func (m *menu) LagCapAt(c uint8, height uint64) uint64 {
	if int(c) >= len(m.coords) {
		return 0
	}
	co := m.coords[c].at(height) // the form in force: its cap, reference and floor
	if co.LagCap == 0 {
		return 0
	}
	hl := m.HalfLifeAt(height)
	if hl == m.halfLife {
		return co.LagCap
	}
	floor := uint64(0)
	if co.DMin != nil {
		floor = co.DMin.Uint64()
	}
	if lc := DerivedLagCap(hl, co.DRef.Uint64(), floor); lc > 0 {
		return lc
	}
	return co.LagCap
}

// Coordinate is the built coordinate c; ok false out of range.
func (m *menu) Coordinate(c uint8) (Coordinate, bool) {
	if int(c) >= len(m.coords) {
		return Coordinate{}, false
	}
	return m.coords[c], true
}

// ByCoordinate is coordinate c's BASE algorithm (the registered document);
// nil out of range. A header or share is verified under the form in force
// at its height: ByCoordinateAt.
func (m *menu) ByCoordinate(c uint8) pow.Algorithm {
	if int(c) >= len(m.coords) {
		return nil
	}
	return m.coords[c].Algorithm
}

// CoordinateAt is coordinate c's form in force at height (the base, or the
// last in-place document change at or under the height: its algorithm,
// reference, floor and cap); ok false out of range.
func (m *menu) CoordinateAt(c uint8, height uint64) (Coordinate, bool) {
	if int(c) >= len(m.coords) {
		return Coordinate{}, false
	}
	return m.coords[c].at(height), true
}

// ByCoordinateAt is coordinate c's algorithm in force at height; nil out
// of range.
func (m *menu) ByCoordinateAt(c uint8, height uint64) pow.Algorithm {
	if int(c) >= len(m.coords) {
		return nil
	}
	return m.coords[c].at(height).Algorithm
}

// Changes is coordinate c's in-place document changes (none for most).
func (m *menu) Changes(c uint8) []CoordinateChange {
	if int(c) >= len(m.coords) {
		return nil
	}
	return m.coords[c].Changes
}

// The menu as a pow.Algorithm: the first coordinate's for the engine's
// coordinate-free reads. A hash through these runs under coordinate 0; the
// engine never hashes a tagged header through them (it dispatches by the
// tag), so they serve the registry's self-test and the vectors.
func (m *menu) Name() string { return Name }

// The menu's coordinate-free reads run on the SEALING coordinate's
// relation where one is set (the template's work package: its epoch and
// seed are the template's coordinate's), else the first's.
func (m *menu) own() pow.Algorithm {
	if m.seal != nil && int(*m.seal) < len(m.coords) {
		return m.coords[*m.seal].Algorithm
	}
	return m.coords[0].Algorithm
}
func (m *menu) Epoch(height uint64) uint64         { return m.own().Epoch(height) }

// UnkeyedWorkParamsAt is the document an UNKEYED template of the sealing
// coordinate at a height is hashed with (its keyed form's twin), and
// whether that form is keyed; the schedule's row answers the same for the
// bounded menu (sealer.go, mineshares.go read it through the engine's
// algorithm). An unkeyed coordinate answers false.
func (m *menu) UnkeyedWorkParamsAt(height uint64) (params any, digest common.Hash, keyedRow bool) {
	c := 0
	if m.seal != nil && int(*m.seal) < len(m.coords) {
		c = int(*m.seal)
	}
	return m.UnkeyedByCoordinateAt(uint8(c), height)
}

// UnkeyedAlgorithmAt is the twin algorithm of coordinate c's form in force
// at the height (nil where the form is unkeyed or c is out of range): the
// relation an UNKEYED block or share of a keyed coordinate is hashed with
// (0.10.58, F-494: the keyed adapter's plain compute on a bit-10 context is
// the reference's refusal, so the verifying side routes through the twin
// as the schedule's rows do with plain()).
func (m *menu) UnkeyedAlgorithmAt(c uint8, height uint64) pow.Algorithm {
	if int(c) >= len(m.coords) {
		return nil
	}
	return m.coords[c].at(height).Unkeyed
}

// UnkeyedByCoordinateAt is UnkeyedWorkParamsAt for a named coordinate: the
// twin of its form in force at the height.
func (m *menu) UnkeyedByCoordinateAt(c uint8, height uint64) (params any, digest common.Hash, keyedRow bool) {
	if int(c) >= len(m.coords) {
		return nil, common.Hash{}, false
	}
	f := m.coords[c].at(height)
	if f.Unkeyed == nil {
		return nil, common.Hash{}, false
	}
	if w, ok := f.Unkeyed.(interface{ WorkParams() any }); ok {
		params = w.WorkParams()
	}
	if d, ok := f.Unkeyed.(interface{ ParamsDigest() common.Hash }); ok {
		digest = d.ParamsDigest()
	}
	return params, digest, true
}
func (m *menu) SeedHash(height uint64) common.Hash { return m.own().SeedHash(height) }
func (m *menu) Vectors() []pow.Vector              { return m.coords[0].Algorithm.Vectors() }
func (m *menu) Compute(headerHash common.Hash, height uint64, nonce uint64) (common.Hash, common.Hash) {
	return m.coords[0].Algorithm.Compute(headerHash, height, nonce)
}
func (m *menu) Verify(headerHash common.Hash, height uint64, nonce uint64, mixDigest common.Hash, difficulty *big.Int, fullDataset bool) error {
	return m.coords[0].Algorithm.Verify(headerHash, height, nonce, mixDigest, difficulty, fullDataset)
}

// Anchor forwards the chain head to every coordinate with epoch state, the
// in-place forms included (a change's references are built on every node
// ahead of its height by the anchor's lookahead, LANE-MODIFICATION.md
// section 1 item 6), and re-applies the full-dataset choice when a form
// comes into force or steps down at the head.
func (m *menu) Anchor(height uint64) {
	m.head = height
	if sig := m.formsSig(height); sig != m.liveSig {
		m.liveSig = sig
		m.applyFull()
	}
	for _, c := range m.coords {
		for _, a := range c.forms() {
			if an, ok := a.(interface{ Anchor(uint64) }); ok {
				an.Anchor(height)
			}
		}
	}
}

// formLive reports whether form k of coordinate c (0 the base, k the k-th
// change) holds its full context at height on a sealing node: a form not
// yet superseded, or superseded under StepDownBlocks ago.
func formLive(c Coordinate, k int, height uint64) bool {
	if k >= len(c.Changes) {
		return true // the last form is never superseded
	}
	return height < c.Changes[k].From+StepDownBlocks
}

// inForceIndex is the index of coordinate c's form in force at height (0
// the base).
func inForceIndex(c Coordinate, height uint64) int {
	k := 0
	for j, ch := range c.Changes {
		if ch.From <= height {
			k = j + 1
		}
	}
	return k
}

// formsSig is the coordinates' forms in force and live at height, as a
// string the anchor compares (empty for a registration without changes).
func (m *menu) formsSig(height uint64) string {
	var b []byte
	// F-559: the ACTIVE set at the height is part of the signature on a
	// reader (no sealing coordinate), since the reader's holders are the
	// first active coordinate of each base: a retirement or an activation
	// passing re-runs the donor pass.
	if m.seal == nil {
		b = append(b, 'a', ':')
		for i := range m.coords {
			if m.ActiveAt(uint8(i), height) {
				b = append(b, '1')
			} else {
				b = append(b, '0')
			}
		}
		b = append(b, ';')
	}
	for i, c := range m.coords {
		if len(c.Changes) == 0 {
			continue
		}
		b = append(b, byte('0'+i), ':', byte('0'+inForceIndex(c, height)), ':')
		for k := 0; k <= len(c.Changes); k++ {
			if formLive(c, k, height) {
				b = append(b, '1')
			} else {
				b = append(b, '0')
			}
		}
		b = append(b, ';')
	}
	return string(b)
}

// SetFullDataset forwards the sealer's full-dataset choice.
func (m *menu) SetFullDataset(full bool) {
	m.full = full
	m.applyFull()
}

// SetSingleDataset forwards the one-resident-context form to every coordinate
// and records it for the registrations that replace this one.
func (m *menu) SetSingleDataset(on bool) {
	m.single = on
	for _, c := range m.coords {
		for _, a := range c.forms() {
			if s, ok := a.(interface{ SetSingleDataset(bool) }); ok {
				s.SetSingleDataset(on)
			}
		}
	}
}

// SetSealCoordinate names the node's sealing coordinate: the full-dataset
// choice follows it.
func (m *menu) SetSealCoordinate(c uint8) {
	cc := c
	m.seal = &cc
	m.applyFull()
}

func (m *menu) applyFull() {
	// One dataset for every coordinate (the hash team's fact of record,
	// main 21e743debaf0, SPEC-R0E-v0 section 5a: every registered document
	// over the one base derives the same cache and dataset, and the
	// reference's shared context accepts any as the donor for any other):
	// the sealing coordinate's adapter (else the first) holds the full
	// context, every other IgneumHash coordinate rides it through its
	// shared context (SetDonor) and builds nothing; a relation of another
	// kind (the Graph) has no dataset and no donor.
	donor := 0
	// F-146: a seal coordinate beyond this registration (named before the
	// landing that holds it, or left by a dropped landing) is no donor
	// index; the first coordinate holds the context until the seal is in
	// range (the engine's sealFromRegistry).
	if m.seal != nil && int(*m.seal) < len(m.coords) {
		donor = int(*m.seal)
	}
	if _, ok := m.coords[donor].Algorithm.(interface{ SetDonor(any) }); !ok {
		donor = -1
		for i, c := range m.coords {
			if _, ok := c.Algorithm.(interface{ SetDonor(any) }); ok {
				donor = i
				break
			}
		}
	}
	// The donor's algorithm is the donor coordinate's form IN FORCE at the
	// head (its base before the first change): a rider rides the context
	// that is live.
	var donorAlg pow.Algorithm
	var donorForm int
	if donor >= 0 {
		donorForm = inForceIndex(m.coords[donor], m.head)
		donorAlg = m.coords[donor].forms()[donorForm]
	}
	// F-559 / F-560 (17 October 2026): a node WITHOUT a sealing coordinate
	// (a reader or a point) verifies every live lane, and a lane of a base
	// other than the first coordinate's held its own LIGHT context (the
	// cache alone, a hash at a hundred milliseconds, rebuilt whenever the
	// sweep freed it): under the downloader's batch across three lanes the
	// batch never finished (the public test network's point oregon at
	// 8,084, six minutes in the batch on every start). The cure: a reader
	// holds one FULL context per base among the coordinates ACTIVE at the
	// head (the first active coordinate of each base the holder, the
	// rest of that base its riders; a retired coordinate's form rides a
	// holder of its base where one is live, else light). A sealing node is
	// as before: its own coordinate the one holder.
	var baseHolder []int // per coordinate: the holder coordinate's index, -1 for none (a sealing node: unused)
	if m.seal == nil {
		baseHolder = make([]int, len(m.coords))
		for i := range baseHolder {
			baseHolder[i] = -1
		}
		var holders []int
		for i, c := range m.coords {
			if !m.ActiveAt(uint8(i), m.head) {
				continue
			}
			a := c.forms()[inForceIndex(c, m.head)]
			if _, ok := a.(interface{ SetDonor(any) }); !ok {
				continue
			}
			found := -1
			for _, h := range holders {
				if sameBase(a, m.coords[h].forms()[inForceIndex(m.coords[h], m.head)]) {
					found = h
					break
				}
			}
			if found < 0 {
				holders = append(holders, i)
				found = i
			}
			baseHolder[i] = found
		}
		for i, c := range m.coords {
			if baseHolder[i] >= 0 {
				continue
			}
			a := c.forms()[inForceIndex(c, m.head)]
			for _, h := range holders {
				if sameBase(a, m.coords[h].forms()[inForceIndex(m.coords[h], m.head)]) {
					baseHolder[i] = h
					break
				}
			}
		}
	}
	for i, c := range m.coords {
		for k, a := range c.forms() {
			rider, canRide := a.(interface{ SetDonor(any) })
			if baseHolder != nil {
				// The reader's form (F-559): the holder of coordinate i's
				// base, or none.
				h := baseHolder[i]
				var hAlg pow.Algorithm
				if h >= 0 {
					hAlg = m.coords[h].forms()[inForceIndex(m.coords[h], m.head)]
				}
				holds := h == i && k == inForceIndex(c, m.head)
				ridesH := canRide && h >= 0 && !holds && sameBase(a, hAlg)
				if ridesH {
					rider.SetDonor(hAlg)
				} else if canRide {
					rider.SetDonor(nil)
				}
				onH := m.full && (holds || ridesH)
				if f, ok := a.(interface{ SetFullDataset(bool) }); ok {
					f.SetFullDataset(onH)
				}
				if tw := c.twins()[k]; tw != nil {
					if tr, ok := tw.(interface{ SetDonor(any) }); ok {
						switch {
						case holds:
							tr.SetDonor(a)
						case ridesH:
							tr.SetDonor(hAlg)
						default:
							tr.SetDonor(nil)
						}
					}
					if f, ok := tw.(interface{ SetFullDataset(bool) }); ok {
						f.SetFullDataset(onH)
					}
				}
				continue
			}
			// A rider only over the SAME BASE: the dataset's derivation
			// inputs equal (dataset_mib, cache_mib, parents, item_bytes).
			// Version 15's 4 GiB base is not the 2 GiB documents' (F-99,
			// vectorwork6: the shared context across bases was created and
			// refused every hash, and its creation cost the template path
			// 25 to 30 s a call on the node's own-coordinate row); a row of
			// another base holds its own context, light under the single
			// form, full where it is the sealing coordinate. The sealing
			// coordinate's forms hold their own full contexts while LIVE
			// (formLive: not yet superseded, or superseded under
			// StepDownBlocks ago), the superseded form stepping down to
			// light past that (the security review's read: not two full datasets
			// a sealing node for the chain's life per change).
			own := i == donor
			rides := canRide && donor >= 0 && !(own && k == donorForm) && sameBase(a, donorAlg)
			if rides {
				rider.SetDonor(donorAlg)
			} else if canRide {
				rider.SetDonor(nil)
			}
			on := m.full && ((own && formLive(c, k, m.head)) || rides)
			if f, ok := a.(interface{ SetFullDataset(bool) }); ok {
				f.SetFullDataset(on)
			}
			// A keyed form's twin rides the same context the form holds or
			// rides (the same base by construction: the twin is the
			// document without its two keyed fields), so an open-seat
			// search on the sealing coordinate and a share's check on any
			// node hash on a built dataset, never a context of the twin's
			// own (0.10.58, F-494). Where the form holds its own full
			// context, the twin rides the form; where the form rides the
			// donor, the twin rides the donor too. The twin's full flag is
			// its form's: the adapter rides a donor only under the flag
			// (igneumhash_ref.go withContext: "d != nil && full"), and with
			// it false the twin would fall to a light context of its own
			// (the security review's strike, 75do: shares verified at a light
			// hash's hundred milliseconds, the open-seat search the same).
			if tw := c.twins()[k]; tw != nil {
				if tr, ok := tw.(interface{ SetDonor(any) }); ok {
					switch {
					case own && k == donorForm:
						tr.SetDonor(a)
					case rides:
						tr.SetDonor(donorAlg)
					default:
						tr.SetDonor(nil)
					}
				}
				if f, ok := tw.(interface{ SetFullDataset(bool) }); ok {
					f.SetFullDataset(on)
				}
			}
		}
	}
}

// sameBase reports whether two IgneumHash documents derive the same
// dataset (the derivation inputs equal; a document without them is never
// the same base).
func sameBase(a, b pow.Algorithm) bool {
	fa, ok1 := docFields(a)
	fb, ok2 := docFields(b)
	if !ok1 || !ok2 {
		return false
	}
	for _, k := range []string{"dataset_mib", "cache_mib", "parents", "item_bytes"} {
		va, oka := fa[k]
		vb, okb := fb[k]
		if !oka || !okb || va != vb {
			return false
		}
	}
	return true
}

func docFields(a pow.Algorithm) (map[string]any, bool) {
	w, ok := a.(interface{ WorkParams() any })
	if !ok {
		return nil, false
	}
	m, ok := w.WorkParams().(map[string]any)
	return m, ok
}

// Close releases every coordinate's resources, the in-place forms' too.
func (m *menu) Close() {
	for _, c := range m.coords {
		for _, a := range c.forms() {
			if cl, ok := a.(interface{ Close() }); ok {
				cl.Close()
			}
		}
	}
}

// sameCoordinateDocument reports whether two coordinates name one algorithm
// and one document (the fields a verifier context depends on).
func sameCoordinateDocument(a, b ctypes.IgneumCoordinate) bool {
	return a.Name == b.Name && a.Algorithm == b.Algorithm && bytes.Equal(bytes.TrimSpace(a.AlgorithmParams), bytes.TrimSpace(b.AlgorithmParams))
}
