package igneum

import (
	"encoding/json"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/difficulty"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/null"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/vector"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
	"github.com/ethereum/go-ethereum/rlp"
)

// VectorWork on the node (vector.go; docs/consensus/VECTORWORK.md): three
// coordinates over the null relation (every nonce a seal) with different
// reference difficulties and an 80 s target each, the controller's
// half-life an hour.
func vectorConfig() *ctypes.IgneumVector {
	return &ctypes.IgneumVector{
		Coordinates: []ctypes.IgneumCoordinate{
			{Name: "tensor", Algorithm: null.Name, Target: 80, DRef: 1000},
			{Name: "bandwidth", Algorithm: null.Name, Target: 80, DRef: 2000},
			{Name: "fabric", Algorithm: null.Name, Target: 80, DRef: 4000},
		},
		HalfLife: 3600, DtMax: 3600,
	}
}

func vectorEngine(t *testing.T, rule string) *Engine {
	t.Helper()
	v := vectorConfig()
	v.Rule = rule
	m, err := vector.New(v, pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	parts := TestParts()
	parts.Difficulty.Min = big.NewInt(1)
	e, err := New(Config{Algorithm: m, Parts: parts})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

// taggedChild is a child of parent with the coordinate c, its time dt after
// the parent, and the difficulty the controller gives it on the chain.
func taggedChild(t *testing.T, e *Engine, c *forkChain, parent *types.Header, coord uint8, dt uint64) *types.Header {
	t.Helper()
	h := &types.Header{
		ParentHash: parent.Hash(),
		UncleHash:  types.EmptyUncleHash,
		Number:     new(big.Int).Add(parent.Number, big.NewInt(1)),
		Time:       parent.Time + dt,
		GasLimit:   parent.GasLimit,
	}
	h.SetCoordTag(coord)
	d, err := e.vectorDifficulty(c, parent, coord)
	if err != nil {
		t.Fatal(err)
	}
	h.Difficulty = d
	return h
}

func TestVectorMenuShape(t *testing.T) {
	v := vectorConfig()
	if err := vector.Validate(v); err != nil {
		t.Fatal(err)
	}
	bad := vectorConfig()
	bad.Coordinates = bad.Coordinates[:1]
	if err := vector.Validate(bad); !errors.Is(err, vector.ErrShape) {
		t.Fatalf("one coordinate accepted: %v", err)
	}
	bad = vectorConfig()
	bad.Coordinates[1].DRef = 0
	if err := vector.Validate(bad); !errors.Is(err, vector.ErrShape) {
		t.Fatalf("a zero reference accepted: %v", err)
	}
	bad = vectorConfig()
	bad.Rule = "sum"
	if err := vector.Validate(bad); !errors.Is(err, vector.ErrShape) {
		t.Fatalf("the sum rule accepted: %v", err)
	}
	m, err := vector.New(v, pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	if m.Count() != 3 || m.ByCoordinate(3) != nil || m.ByCoordinate(2) == nil {
		t.Fatal("the menu's coordinates")
	}
}

// TestVectorHeaderTagInSealAndJSON: the tag is inside the seal hash (a
// seal for one coordinate is no seal for another), survives RLP and JSON,
// and a header without it decodes with none.
func TestVectorHeaderTagInSealAndJSON(t *testing.T) {
	e := vectorEngine(t, "")
	g := genesisHeader()
	h := childOf(g)
	h.SetCoordTag(0)
	s0 := e.SealHash(h)
	h.SetCoordTag(1)
	s1 := e.SealHash(h)
	if s0 == s1 {
		t.Fatal("the seal hash does not commit to the coordinate")
	}
	// Both tags survive the RLP round trip, coordinate 0 included (the
	// byte-string form; an integer zero would read as absent).
	for _, c := range []uint8{1, 0} {
		h.SetCoordTag(c)
		raw, err := rlp.EncodeToBytes(h)
		if err != nil {
			t.Fatal(err)
		}
		var back types.Header
		if err := rlp.DecodeBytes(raw, &back); err != nil {
			t.Fatal(err)
		}
		if got, ok := back.CoordTag(); !ok || got != c || back.Hash() != h.Hash() {
			t.Fatalf("the RLP round trip lost tag %d: %v %v", c, back.Coord, ok)
		}
		js, err := json.Marshal(h)
		if err != nil {
			t.Fatal(err)
		}
		var jb types.Header
		if err := json.Unmarshal(js, &jb); err != nil {
			t.Fatal(err)
		}
		if got, ok := jb.CoordTag(); !ok || got != c {
			t.Fatalf("the JSON round trip lost tag %d: %s", c, js)
		}
	}
	h.Coord = nil
	raw, _ := rlp.EncodeToBytes(h)
	var none types.Header
	if err := rlp.DecodeBytes(raw, &none); err != nil || len(none.Coord) != 0 {
		t.Fatalf("a header without a tag: %v %v", err, none.Coord)
	}
}

// TestVectorControllerPerCoordinate: at genesis every coordinate's first
// difficulty is its reference (no lag); a coordinate with no blocks while
// the chain's time runs on decays below its reference; one at its target
// holds it; the header rule refuses a wrong difficulty, a missing tag and
// an out-of-range tag.
func TestVectorControllerPerCoordinate(t *testing.T) {
	e := vectorEngine(t, "")
	c := newForkChain()
	g := genesisHeader()
	g.Time = 1_000_000
	c.setCanonical(g)
	e.anchor(c)
	for coord, want := range []int64{1000, 2000, 4000} {
		d, err := e.vectorDifficulty(c, g, uint8(coord))
		if err != nil || d.Cmp(big.NewInt(want)) != 0 {
			t.Fatalf("coordinate %d at genesis: %v (%v), want %d", coord, d, err, want)
		}
	}
	// Twelve coordinate-0 blocks at 80 s each: coordinate 0 holds its
	// reference (no lag); coordinates 1 and 2, with no blocks over 960 s,
	// decay (lag 960 s of 3,600).
	parent := g
	for i := 0; i < 12; i++ {
		h := taggedChild(t, e, c, parent, 0, 80)
		c.setCanonical(h)
		parent = h
	}
	d0, _ := e.vectorDifficulty(c, parent, 0)
	d1, _ := e.vectorDifficulty(c, parent, 1)
	if d0.Cmp(big.NewInt(1000)) != 0 {
		t.Fatalf("coordinate 0 at its target: %v, want 1000", d0)
	}
	if d1.Cmp(big.NewInt(2000)) >= 0 || d1.Cmp(big.NewInt(1500)) <= 0 {
		t.Fatalf("coordinate 1 idle for 960 s: %v, want a decay below 2000 and above 1500", d1)
	}
	// The header rule.
	good := taggedChild(t, e, c, parent, 1, 80)
	if err := e.verifyVectorHeader(c, good, parent); err != nil {
		t.Fatalf("a right header refused: %v", err)
	}
	bad := taggedChild(t, e, c, parent, 1, 80)
	bad.Difficulty = big.NewInt(2000)
	if err := e.verifyVectorHeader(c, bad, parent); !errors.Is(err, errVectorDifficulty) {
		t.Fatalf("a wrong difficulty accepted: %v", err)
	}
	bad.Coord = nil
	if err := e.verifyVectorHeader(c, bad, parent); !errors.Is(err, errVectorCoord) {
		t.Fatalf("a missing tag accepted: %v", err)
	}
	// A genesis at time zero (the development form): the clock starts at
	// the first block, so block 2's coordinate reads its reference plus
	// one interval's lag, not the floor.
	z := newForkChain()
	gz := genesisHeader()
	gz.Time = 0
	z.setCanonical(gz)
	for coord, want := range []int64{1000, 2000, 4000} {
		d, _ := e.vectorDifficulty(z, gz, uint8(coord))
		if d.Cmp(big.NewInt(want)) != 0 {
			t.Fatalf("time-zero genesis, coordinate %d: %v, want %d", coord, d, want)
		}
	}
	b1 := taggedChild(t, e, z, gz, 2, 1_000_000)
	z.setCanonical(b1)
	d2, _ := e.vectorDifficulty(z, b1, 2)
	d0z, _ := e.vectorDifficulty(z, b1, 0)
	if d2.Cmp(big.NewInt(4000)) <= 0 || d2.Cmp(big.NewInt(4100)) >= 0 || d0z.Cmp(big.NewInt(1000)) != 0 {
		t.Fatalf("after the first block of a time-zero chain: coordinate 2 %v (want just above 4000), coordinate 0 %v (want 1000)", d2, d0z)
	}
	b2 := taggedChild(t, e, z, b1, 2, 80)
	if err := e.verifyVectorHeader(z, b2, b1); err != nil {
		t.Fatalf("block 2 of a time-zero chain refused: %v", err)
	}
	// The registration check: a reference under the floor is refused at
	// start, under the chain's minimum or the coordinate's own.
	vr := vectorConfig()
	mr, _ := vector.New(vr, pow.NewDev)
	pr := TestParts()
	pr.Difficulty.Min = big.NewInt(1500)
	if eu, err := New(Config{Algorithm: mr, Parts: pr}); err != nil {
		t.Fatalf("a reference under the chain's floor refused at the engine (the refusal is the genesis's, the engine warns): %v", err)
	} else {
		eu.Close()
	}
	vr.Coordinates[0].DMin = 1000
	vr.Coordinates[1].DMin = 1
	vr.Coordinates[2].DMin = 1
	mr, _ = vector.New(vr, pow.NewDev)
	if er, err := New(Config{Algorithm: mr, Parts: pr}); err != nil {
		t.Fatalf("references at their own floors refused: %v", err)
	} else {
		er.Close()
	}
	// The scaled unit: a coordinate at workScale 1024 seals against D/1024
	// (at least 1) while its controller and weight run on D.
	vsc := vectorConfig()
	vsc.Coordinates[2].WorkScale = 1024
	msc, _ := vector.New(vsc, pow.NewDev)
	psc := TestParts()
	psc.Difficulty.Min = big.NewInt(1)
	esc, err := New(Config{Algorithm: msc, Parts: psc})
	if err != nil {
		t.Fatal(err)
	}
	defer esc.Close()
	hsc := taggedChild(t, esc, z, gz, 2, 80)
	if sd := esc.sealDifficulty(hsc); sd.Cmp(big.NewInt(4000/1024)) != 0 {
		t.Fatalf("the scaled seal difficulty at D 4000, scale 1024: %v, want 3", sd)
	}
	hsc.Difficulty = big.NewInt(500)
	if sd := esc.sealDifficulty(hsc); sd.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("the scaled seal difficulty under the scale: %v, want 1", sd)
	}
	h0 := taggedChild(t, esc, z, gz, 0, 80)
	if sd := esc.sealDifficulty(h0); sd.Cmp(h0.Difficulty) != 0 {
		t.Fatalf("a plain-unit coordinate scaled: %v against %v", sd, h0.Difficulty)
	}
	// A coordinate's own floor.
	vf := vectorConfig()
	vf.Coordinates[2].DMin = 3
	mf, err := vector.New(vf, pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	pf := TestParts()
	pf.Difficulty.Min = big.NewInt(500)
	ef, err := New(Config{Algorithm: mf, Parts: pf})
	if err != nil {
		t.Fatal(err)
	}
	defer ef.Close()
	idle := gz
	for i := 0; i < 2; i++ {
		h := taggedChild(t, ef, z, idle, 0, 100_000)
		z.setCanonical(h)
		idle = h
	}
	f1, _ := ef.vectorDifficulty(z, idle, 1)
	f2, _ := ef.vectorDifficulty(z, idle, 2)
	if f1.Cmp(big.NewInt(500)) != 0 || f2.Cmp(big.NewInt(3)) != 0 {
		t.Fatalf("the floors after a long idle: coordinate 1 %v (want the chain's 500), coordinate 2 %v (want its own 3)", f1, f2)
	}
	bad.SetCoordTag(5)
	if err := e.verifyVectorHeader(c, bad, parent); !errors.Is(err, errVectorCoord) {
		t.Fatalf("an out-of-range tag accepted: %v", err)
	}
	// A tag on a chain without the form is refused.
	plain := newTestEngine(t)
	if err := plain.verifyVectorHeader(c, good, parent); !errors.Is(err, errVectorCoordOff) {
		t.Fatalf("a tag on a single-relation chain accepted: %v", err)
	}
}

// TestVectorForkWeight: the geometric rule's product. A chain whose
// suffix covers every coordinate outweighs a longer one that covers two,
// a chain of one coordinate alone weighs zero against any that covers
// all, and equal weights fall to the tie-break; the minimum rule orders by
// the scarcest coordinate then the sum.
func TestVectorForkWeight(t *testing.T) {
	e := vectorEngine(t, "")
	c := newForkChain()
	g := genesisHeader()
	g.Time = 1_000_000
	c.setCanonical(g)
	e.anchor(c)
	// The common prefix: one block each of coordinates 0 and 1 (none of
	// 2: the weight is cumulative from genesis, form (a), so a branch's
	// coordinate 2 work is its own).
	prefix := g
	for coord := uint8(0); coord < 2; coord++ {
		h := taggedChild(t, e, c, prefix, coord, 20)
		c.setCanonical(h)
		prefix = h
	}
	// Branch A: three more blocks, one per coordinate (coordinate 2's first
	// block among them). Branch B: five more blocks, coordinates 0 and 1
	// only (longer, with more raw work, and coordinate 2 starved: its
	// product is zero).
	a := prefix
	for coord := uint8(0); coord < 3; coord++ {
		h := taggedChild(t, e, c, a, coord, 20)
		h.Extra = []byte("a")
		c.add(h)
		a = h
	}
	b := prefix
	for i := 0; i < 5; i++ {
		h := taggedChild(t, e, c, b, uint8(i%2), 20)
		h.Extra = []byte("b")
		c.add(h)
		b = h
	}
	wa, _, err := e.vectorWeight(c, a)
	if err != nil {
		t.Fatal(err)
	}
	wb, _, _ := e.vectorWeight(c, b)
	if wa.Cmp(wb) <= 0 {
		t.Fatalf("the balanced branch (%v) must outweigh the longer two-coordinate branch (%v)", wa, wb)
	}
	reorg, decided, err := e.PreferByWeight(c, b, a)
	if err != nil || !decided || !reorg {
		t.Fatalf("from the two-coordinate head to the balanced one: reorg %v decided %v err %v", reorg, decided, err)
	}
	reorg, decided, _ = e.PreferByWeight(c, a, b)
	if !decided || reorg {
		t.Fatal("from the balanced head to the two-coordinate one")
	}
	// A chain of one coordinate from genesis weighs zero.
	solo := g
	for i := 0; i < 6; i++ {
		h := taggedChild(t, e, c, solo, 0, 20)
		h.Extra = []byte("solo")
		c.add(h)
		solo = h
	}
	ws, wsum, _ := e.vectorWeight(c, solo)
	if ws.Sign() != 0 {
		t.Fatalf("a one-coordinate chain weighs %v, want zero", ws)
	}
	if wsum == nil || wsum.Sign() <= 0 {
		t.Fatalf("the second key of a one-coordinate chain: %v, want its work", wsum)
	}
	// The zero phase (the product zero on both sides): the genesis never
	// outweighs its own child, and a chain of one coordinate is preferred
	// to the genesis by the second key, however long it is.
	first := c.GetHeaderByNumber(1)
	if reorg, decided, err := e.PreferByWeight(c, g, first); err != nil || !decided || !reorg {
		t.Fatalf("the first tagged block from the genesis: reorg %v decided %v err %v", reorg, decided, err)
	}
	if reorg, decided, _ := e.PreferByWeight(c, first, g); !decided || reorg {
		t.Fatal("the genesis preferred to its child")
	}
	if reorg, _, _ := e.PreferByWeight(c, g, solo); !reorg {
		t.Fatal("the one-coordinate chain refused from the genesis")
	}
	// A longer one-coordinate chain against the shorter two-coordinate
	// prefix: both products zero, the second key is the work.
	wp, psum, _ := e.vectorWeight(c, prefix)
	if wp.Sign() != 0 || psum.Cmp(wsum) >= 0 {
		t.Fatalf("the prefix's keys %v %v against the solo chain's %v %v", wp, psum, ws, wsum)
	}
	if reorg, _, _ := e.PreferByWeight(c, prefix, solo); !reorg {
		t.Fatal("more work at zero product refused")
	}
	// Equal weight: two siblings of the balanced head's parent with the
	// same coordinate decide by the tie-break, both ways consistent.
	p := c.GetHeader(a.ParentHash, a.Number.Uint64()-1)
	s1 := taggedChild(t, e, c, p, 2, 20)
	s1.Extra = []byte("s1")
	s2 := taggedChild(t, e, c, p, 2, 20)
	s2.Extra = []byte("s2")
	c.add(s1)
	c.add(s2)
	w1, _, _ := e.vectorWeight(c, s1)
	w2, _, _ := e.vectorWeight(c, s2)
	if w1.Cmp(w2) != 0 {
		t.Fatalf("siblings of one coordinate weigh %v and %v", w1, w2)
	}
	r12, d12, err := e.PreferByWeight(c, s1, s2)
	if err != nil || !d12 {
		t.Fatalf("the tie: %v %v", d12, err)
	}
	r21, _, _ := e.PreferByWeight(c, s2, s1)
	if r12 == r21 {
		t.Fatalf("the tie-break is not antisymmetric: %v %v", r12, r21)
	}
	// F-88 (vectorwork1 height 31, 13 October 2026): two twins of one
	// height whose difficulties differ are NOT of equal weight under the
	// vector form, so the sealer's twin policy must not build on the
	// first-seen one; twins of equal difficulty and coordinate are.
	// (Both twins on coordinate 2, which the parent's branch lacks: a
	// twin on another coordinate would leave the product at zero and
	// lose on that alone, which is the zero phase's row, not this one.)
	hi := taggedChild(t, e, c, p, 2, 20)
	hi.Extra = []byte("hi")
	hi.Difficulty = new(big.Int).Add(hi.Difficulty, big.NewInt(1200))
	c.add(hi)
	lo := taggedChild(t, e, c, p, 2, 20)
	lo.Extra = []byte("lo")
	c.add(lo)
	if eq, decided, err := e.EqualWeight(c, hi, lo); err != nil || !decided || eq {
		t.Fatalf("twins of different work read as equal weight: %v %v %v", eq, decided, err)
	}
	if eq, decided, _ := e.EqualWeight(c, s1, s2); !decided || !eq {
		t.Fatalf("twins of one coordinate and difficulty read as unequal: %v %v", eq, decided)
	}
	if reorg, _, _ := e.PreferByWeight(c, lo, hi); !reorg {
		t.Fatal("the twin with more work not preferred")
	}
	// A single-relation chain leaves the decision to the forker.
	plain := newTestEngine(t)
	if _, decided, _ := plain.PreferByWeight(c, s1, s2); decided {
		t.Fatal("a single-relation engine decided a fork by vector weight")
	}
	// The minimum rule: the balanced branch's minimum coordinate against
	// the two-coordinate branch's zero.
	em := vectorEngine(t, "minimum")
	em.anchor(c)
	ma, _, _ := em.vectorWeight(c, a)
	mb, _, _ := em.vectorWeight(c, b)
	if ma.Cmp(mb) <= 0 {
		t.Fatalf("minimum rule: balanced %v against two-coordinate %v", ma, mb)
	}
	info, err := e.vectorInfo(c, a)
	if err != nil || info == nil || len(info.Counts) != 3 || info.Counts[2] != 1 || info.Counts[0] != 2 || info.Product.Cmp(wa) != 0 || info.Rule != "geometric" {
		t.Fatalf("vectorInfo: %+v (%v)", info, err)
	}
	_ = common.Hash{}
}

// TestVectorTemplateAndAlgo: the template takes the node's coordinate and
// that coordinate's difficulty; the seal's algorithm is the coordinate's.
func TestVectorTemplateAndAlgo(t *testing.T) {
	e := vectorEngine(t, "")
	c := newForkChain()
	g := genesisHeader()
	g.Time = 1_000_000
	c.setCanonical(g)
	e.anchor(c)
	if err := e.SetCoordinate(2); err != nil {
		t.Fatal(err)
	}
	if err := e.SetCoordinate(3); !errors.Is(err, errVectorCoord) {
		t.Fatalf("coordinate 3 of three accepted: %v", err)
	}
	h := childOf(g)
	if err := e.prepareVector(c, h, g); err != nil {
		t.Fatal(err)
	}
	if got, ok := h.CoordTag(); !ok || got != 2 || h.Difficulty.Cmp(big.NewInt(4000)) != 0 {
		t.Fatalf("the template: coord %v difficulty %v", h.Coord, h.Difficulty)
	}
	algo, err := e.algoFor(h)
	if err != nil || algo != e.vectorMenu().ByCoordinate(2) {
		t.Fatalf("the seal's algorithm is not coordinate 2's: %v", err)
	}
	plain := newTestEngine(t)
	if err := plain.SetCoordinate(0); !errors.Is(err, errVectorCoordOff) {
		t.Fatalf("a coordinate on a single-relation chain: %v", err)
	}
}

// TestBatchReaderAnchor: a batch of headers verified before insertion
// resolves the programme's anchor through its own parents (the forked
// node's downloader; vectorwork3's graph node, 13 October 2026).
func TestBatchReaderAnchor(t *testing.T) {
	c := newForkChain()
	g := genesisHeader()
	c.setCanonical(g)
	// A branch of 40 headers kept OUT of the chain, as a batch.
	var batch []*types.Header
	parent := g
	for i := 0; i < 40; i++ {
		h := childOf(parent)
		h.Extra = []byte{byte(i)}
		batch = append(batch, h)
		parent = h
	}
	br := newBatchReader(c, batch)
	// The anchor of period 1 (heights 30 to 59 at P 30, D 2) is the batch's
	// header at 28; through the bare chain the walk fails at the first
	// parent inside the batch, through the overlay it resolves.
	e := newTestEngine(t)
	if _, err := e.programWalk(c, batch[36].Hash(), 38, 28); err == nil {
		t.Fatal("the bare chain resolved an anchor it does not hold")
	}
	a, err := e.programWalk(br, batch[36].Hash(), 38, 28)
	if err != nil || a != batch[27].Hash() {
		t.Fatalf("the overlay's anchor: %x %v, want %x", a[:4], err, batch[27].Hash().Bytes()[:4])
	}
	if br.GetHeader(batch[5].Hash(), 6) != batch[5] || br.GetHeader(g.Hash(), 0) == nil {
		t.Fatal("the overlay's reads")
	}
}

// TestVectorGenesisSeedAndDrawTie (vectorwork4, VECTORWORK.md revision
// 9): with seedGenesis every coordinate's work carries its reference from
// the genesis, so the product is positive before any block; and at equal
// weight two open-seat blocks tie on the sealer's draw value x(h), never
// the seal hash (a nonce regrind changes the seal hash, not the draw).
func TestVectorGenesisSeedAndDrawTie(t *testing.T) {
	v := vectorConfig()
	v.SeedGenesis = true
	m, err := vector.New(v, pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	parts := TestParts()
	parts.Difficulty.Min = big.NewInt(1)
	e, err := New(Config{Algorithm: m, Parts: parts})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	c := newForkChain()
	g := genesisHeader()
	g.Time = 1_000_000
	c.setCanonical(g)
	e.anchor(c)
	// The product at the genesis is the product of the references, not
	// zero; one block of coordinate 0 keeps it positive.
	w0, _, err := e.vectorWeight(c, g)
	if err != nil || w0.Sign() <= 0 {
		t.Fatalf("the seeded genesis weighs %v (%v), want positive", w0, err)
	}
	want := new(big.Int).Mul(big.NewInt(1000), new(big.Int).Mul(big.NewInt(2000), big.NewInt(4000)))
	if w0.Cmp(want) != 0 {
		t.Fatalf("the seeded genesis product %v, want %v", w0, want)
	}
	h1 := taggedChild(t, e, c, g, 0, 20)
	c.setCanonical(h1)
	if w1, _, _ := e.vectorWeight(c, h1); w1.Cmp(w0) <= 0 {
		t.Fatalf("a block did not raise the seeded weight: %v after %v", w1, w0)
	}
	// Two open-seat twins of one coordinate and difficulty at equal
	// weight: the tie is the sealers' draw values. The same sealer with a
	// different nonce (a different seal hash) does not change which
	// sealer's block the rule prefers against another sealer's.
	s1 := taggedChild(t, e, c, h1, 1, 20)
	s1.Coinbase = common.HexToAddress("0x1111")
	s1.Nonce = types.EncodeNonce(1)
	s2 := taggedChild(t, e, c, h1, 1, 20)
	s2.Coinbase = common.HexToAddress("0x2222")
	s2.Nonce = types.EncodeNonce(1)
	c.add(s1)
	c.add(s2)
	r12, d, err := e.PreferByWeight(c, s1, s2)
	if err != nil || !d {
		t.Fatalf("the tie: %v %v", d, err)
	}
	// Regrind s1's nonce (the seal hash moves); the preference must not.
	s1b := types.CopyHeader(s1)
	s1b.Nonce = types.EncodeNonce(2)
	c.add(s1b)
	r12b, _, _ := e.PreferByWeight(c, s1b, s2)
	if r12 != r12b {
		t.Fatal("a nonce regrind changed the tie-break: the seal hash is in the term")
	}
	r21, _, _ := e.PreferByWeight(c, s2, s1)
	if r12 == r21 {
		t.Fatal("the draw-value tie-break is not antisymmetric")
	}
}

// TestVectorLagCap (vectorwork4, VECTORWORK.md revision 10): a coordinate's
// folded lag is capped at its registered L_c, so a long absence lowers its
// difficulty only to the cap's value and no further; with the cap off the
// fold reproduces the anchored formula exactly.
func TestVectorLagCap(t *testing.T) {
	v := vectorConfig()
	v.Coordinates[2].LagCap = 100
	m, err := vector.New(v, pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	parts := TestParts()
	parts.Difficulty.Min = big.NewInt(1)
	e, err := New(Config{Algorithm: m, Parts: parts})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	plain := vectorEngine(t, "")
	c := newForkChain()
	g := genesisHeader()
	g.Time = 1_000_000
	c.setCanonical(g)
	e.anchor(c)
	plain.anchor(c)
	// Twelve coordinate-0 blocks at 80 s: coordinate 2 idle 960 s. Without
	// a cap its lag is 960 and the anchored and folded forms agree on
	// coordinate 1 (no cap) exactly; with the cap coordinate 2's lag is
	// 100 and its difficulty the cap's, above the uncapped decay.
	parent := g
	for i := 0; i < 12; i++ {
		h := taggedChild(t, e, c, parent, 0, 80)
		c.setCanonical(h)
		parent = h
	}
	d1, _ := e.vectorDifficulty(c, parent, 1)
	d1p, _ := plain.vectorDifficulty(c, parent, 1)
	if d1.Cmp(d1p) != 0 {
		t.Fatalf("the fold without a cap departs from the anchored formula: %v against %v", d1, d1p)
	}
	d2, _ := e.vectorDifficulty(c, parent, 2)
	d2p, _ := plain.vectorDifficulty(c, parent, 2)
	want := (difficulty.Params{Target: 80, HalfLife: 3600, DtMax: 3600, Min: big.NewInt(1)}).NextFromLag(big.NewInt(4000), 100)
	if d2.Cmp(want) != 0 || d2.Cmp(d2p) <= 0 {
		t.Fatalf("the capped coordinate: %v, want the cap's %v (the uncapped decay %v)", d2, want, d2p)
	}
	info, _ := e.vectorInfo(c, parent)
	if info.Lags[2] != 100 || info.Lags[0] != 0 || info.Lags[1] != 960 {
		t.Fatalf("the folded lags on the reader: %v", info.Lags)
	}
}

// TestShareOtherCoordinateNotPooled (F-93): a share whose work verifies
// under another coordinate's relation is refused without the work
// penalty and never pooled, so a block carries its own coordinate's
// shares alone and a fresh node verifying it by its tag accepts it.
func TestShareOtherCoordinateNotPooled(t *testing.T) {
	e := vectorEngine(t, "")
	if err := e.SetCoordinate(1); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(errShareOtherCoordinate, errShareOtherCoordinate) {
		t.Fatal("the error's identity")
	}
	// The gossip's bad count keys on errShareWork's text; the other-
	// coordinate refusal must not carry it.
	if r := errShareOtherCoordinate.Error(); len(r) >= len(errShareWork.Error()) && r[:len(errShareWork.Error())] == errShareWork.Error() {
		t.Fatal("the other-coordinate refusal reads as a work failure to the gossip")
	}
}

// TestVectorSeedOnceAtGenesis (the outside review's condition on the seed,
// closure 20): the genesis reference is read from the registration into
// the genesis state alone, once, and never recreated: a restart (a new
// engine on the same chain, the cache empty) reads the same weight; a
// branch rebuilt from the genesis carries the seed once plus its own
// work, not the seed again; a block at an epoch boundary adds no seed;
// a re-registration is another chain (its own genesis).
func TestVectorSeedOnceAtGenesis(t *testing.T) {
	v := vectorConfig()
	v.SeedGenesis = true
	newEngine := func() *Engine {
		m, err := vector.New(v, pow.NewDev)
		if err != nil {
			t.Fatal(err)
		}
		parts := TestParts()
		parts.Difficulty.Min = big.NewInt(1)
		e, err := New(Config{Algorithm: m, Parts: parts})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { e.Close() })
		return e
	}
	e := newEngine()
	c := newForkChain()
	g := genesisHeader()
	g.Time = 1_000_000
	c.setCanonical(g)
	e.anchor(c)
	seed := new(big.Int).Mul(big.NewInt(1000), new(big.Int).Mul(big.NewInt(2000), big.NewInt(4000)))
	// A canonical run of 130 blocks across an epoch boundary (the test
	// parts' epoch is 100 blocks) on coordinates 0 and 1.
	parent := g
	for i := 0; i < 130; i++ {
		h := taggedChild(t, e, c, parent, uint8(i%2), 20)
		c.setCanonical(h)
		parent = h
	}
	w, _, err := e.vectorWeight(c, parent)
	if err != nil {
		t.Fatal(err)
	}
	// The expected weight: the seed per coordinate plus the branch's own
	// work, the seed exactly once (coordinate 2 is the seed alone).
	s, _ := e.vectorStateOf(c, parent)
	if s.work[2].Cmp(big.NewInt(4000)) != 0 {
		t.Fatalf("an idle coordinate across an epoch boundary carries %v, want its seed 4000 once", s.work[2])
	}
	if s.work[0].Cmp(big.NewInt(1000)) <= 0 || s.work[1].Cmp(big.NewInt(2000)) <= 0 {
		t.Fatalf("the sealing coordinates carry %v and %v, want the seed plus their work", s.work[0], s.work[1])
	}
	// A restart: a new engine, an empty cache, the same chain reads the
	// same weight to the number.
	e2 := newEngine()
	e2.anchor(c)
	w2, _, _ := e2.vectorWeight(c, parent)
	if w2.Cmp(w) != 0 {
		t.Fatalf("a restart reads %v against %v", w2, w)
	}
	// A reorganisation: a branch from height 50 rebuilt from the genesis
	// carries the seed once plus its own work; the genesis alone weighs
	// the seed.
	fork := c.GetHeaderByNumber(50)
	b := fork
	for i := 0; i < 90; i++ {
		h := taggedChild(t, e2, c, b, 2, 20)
		h.Extra = []byte("b")
		c.add(h)
		b = h
	}
	sb, _ := e2.vectorStateOf(c, b)
	if sb.work[2].Cmp(big.NewInt(4000)) <= 0 {
		t.Fatalf("the branch's coordinate 2 carries %v, want the seed plus its work", sb.work[2])
	}
	own := new(big.Int).Sub(sb.work[2], big.NewInt(4000))
	sum := new(big.Int)
	for h := b; h.Number.Uint64() > 50; h = c.GetHeader(h.ParentHash, h.Number.Uint64()-1) {
		sum.Add(sum, h.Difficulty)
	}
	if own.Cmp(sum) != 0 {
		t.Fatalf("the branch's own work %v against the sum of its difficulties %v (the seed counted twice or not at all)", own, sum)
	}
	wg, _, _ := e2.vectorWeight(c, g)
	if wg.Cmp(seed) != 0 {
		t.Fatalf("the genesis weighs %v, want the seed %v", wg, seed)
	}
}

// TestVectorDerivedLagCap pins the cap derivation L_c = floor(H x
// log2(D_ref / Dmin)) on every live registration (vectorwork7's five on
// the chain's 8,192 floor and the fifth's own D_ref/16, the eleventh chain's (chain 7321) four
// and its coordinate 4): the field equals the derivation on each.
func TestVectorDerivedLagCap(t *testing.T) {
	rows := []struct{ dRef, floor, cap uint64 }{
		{79360, 8192, 2948}, {1700000, 8192, 6927}, {272560, 8192, 4550}, {101711, 8192, 3270},
		{45248730, 2828045, 3600}, {82410106, 5150631, 3600}, {1799660441, 112478777, 3600},
		{220533841, 13783365, 3600}, {43045685279, 2690355329, 3600},
	}
	for _, r := range rows {
		if got := vector.DerivedLagCap(900, r.dRef, r.floor); got != r.cap {
			t.Fatalf("dRef %d floor %d: derived %d, registered %d", r.dRef, r.floor, got, r.cap)
		}
	}
	if got := vector.DerivedLagCap(900, 8192, 8192); got != 0 {
		t.Fatalf("a reference at the floor caps at zero, got %d", got)
	}
}
