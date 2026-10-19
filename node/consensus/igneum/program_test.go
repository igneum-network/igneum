package igneum

import (
	"encoding/json"
	"errors"
	"math/big"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/null"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/vector"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// The per-period programme's chain-side form (program.go; the page of
// record DEVNET11-R0E-CHAIN-FORM.md, sections 1, 4 and 6): the anchor
// height's arithmetic, the anchor resolved along a header's own branch
// across a fork that crosses a period's anchor, the refusal of an unknown
// ancestor, and the sealer's template under its parent's anchor. TP-248's
// programme half; its ledger half (the by-height accounts after the reorg)
// is the share lane's own test.

// programmedStub is a pow.Programmed over the null algorithm: a fixed
// period, and every view records the anchor it was bound to against the
// heights it verified, so a test reads which programme each hash ran under.
type programmedStub struct {
	pow.Algorithm
	period uint64
	mu     sync.Mutex
	ran    map[uint64][]common.Hash // height -> the anchors Verify ran under
}

func newProgrammedStub(t *testing.T, period uint64) *programmedStub {
	t.Helper()
	base, err := pow.NewDev(null.Name, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &programmedStub{Algorithm: base, period: period, ran: make(map[uint64][]common.Hash)}
}

func (s *programmedStub) PeriodBlocks(height uint64) uint64 { return s.period }
func (s *programmedStub) Under(anchor common.Hash) pow.Algorithm {
	return &programmedView{stub: s, anchor: anchor}
}
func (s *programmedStub) record(height uint64, anchor common.Hash) {
	s.mu.Lock()
	s.ran[height] = append(s.ran[height], anchor)
	s.mu.Unlock()
}
func (s *programmedStub) anchorsAt(height uint64) []common.Hash {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]common.Hash(nil), s.ran[height]...)
}

type programmedView struct {
	stub   *programmedStub
	anchor common.Hash
}

func (v *programmedView) Name() string                      { return v.stub.Name() }
func (v *programmedView) Epoch(height uint64) uint64        { return v.stub.Epoch(height) }
func (v *programmedView) SeedHash(height uint64) common.Hash { return v.stub.SeedHash(height) }
func (v *programmedView) Vectors() []pow.Vector             { return v.stub.Vectors() }
func (v *programmedView) Compute(headerHash common.Hash, height uint64, nonce uint64) (common.Hash, common.Hash) {
	v.stub.record(height, v.anchor)
	return v.stub.Compute(headerHash, height, nonce)
}
func (v *programmedView) Verify(headerHash common.Hash, height uint64, nonce uint64, mixDigest common.Hash, difficulty *big.Int, fullDataset bool) error {
	// The view records the anchor the work ran under and passes: what the
	// test reads is WHICH programme each hash ran under, not the null
	// algorithm's mix.
	v.stub.record(height, v.anchor)
	return nil
}

// forkChain is a ChainHeaderReader with a canonical branch by number and
// every header of every branch by hash.
type forkChain struct {
	*fakeChain
	canonical map[uint64]*types.Header
	reads     int // header reads by hash (the walk's cost)
}

func newForkChain() *forkChain {
	return &forkChain{fakeChain: newFakeChain(), canonical: make(map[uint64]*types.Header)}
}
func (c *forkChain) GetHeaderByNumber(number uint64) *types.Header { return c.canonical[number] }
func (c *forkChain) GetHeader(hash common.Hash, number uint64) *types.Header {
	c.reads++
	return c.fakeChain.GetHeader(hash, number)
}
func (c *forkChain) CurrentHeader() *types.Header {
	var best *types.Header
	for _, h := range c.canonical {
		if best == nil || h.Number.Uint64() > best.Number.Uint64() {
			best = h
		}
	}
	return best
}
func (c *forkChain) setCanonical(h *types.Header) { c.add(h); c.canonical[h.Number.Uint64()] = h }

// chainOf builds n headers above the parent with a distinguishing extra, on
// the chain's hash map only (canonical or not is the caller's).
func chainOf(c *forkChain, parent *types.Header, n int, extra byte) []*types.Header {
	out := make([]*types.Header, 0, n)
	for i := 0; i < n; i++ {
		h := &types.Header{
			ParentHash: parent.Hash(),
			Number:     new(big.Int).Add(parent.Number, big.NewInt(1)),
			Time:       parent.Time + 20,
			Difficulty: new(big.Int).Set(parent.Difficulty),
			GasLimit:   parent.GasLimit,
			UncleHash:  types.EmptyUncleHash,
			Extra:      []byte{extra},
		}
		c.add(h)
		out = append(out, h)
		parent = h
	}
	return out
}

func TestProgramAnchorHeight(t *testing.T) {
	// P 10, D 2: period 0 (heights 0 to 9) anchors on genesis (0 x 10 - 2
	// below one); period 1 (10 to 19) on height 8; period 3 on 28.
	for _, c := range []struct {
		height, period, reveal uint64
		want                   uint64
		genesis                bool
	}{
		{0, 10, 2, 0, true}, {9, 10, 2, 0, true}, {10, 10, 2, 8, false}, {19, 10, 2, 8, false},
		{20, 10, 2, 18, false}, {35, 10, 2, 28, false}, {1, 1, 2, 0, true}, {2, 1, 2, 0, true},
		{3, 1, 2, 1, false}, {300, 300, 6, 294, false}, {599, 300, 6, 294, false}, {600, 300, 6, 594, false},
	} {
		got, ok := programAnchorHeight(c.height, c.period, c.reveal)
		if ok == c.genesis || (ok && got != c.want) {
			t.Fatalf("height %d P %d D %d: got %d (genesis %v), want %d (genesis %v)", c.height, c.period, c.reveal, got, !ok, c.want, c.genesis)
		}
	}
}

// TestProgramAnchorPerBranch: a canonical chain of 16 under P 10 and D 2,
// and a fork from height 6 that reaches 17 (the longer branch). Period 1's
// anchor is height 8: the canonical branch's 8 and the fork's 8 differ, so
// a header at 12 on each branch resolves its own anchor; a header at 9 on
// either resolves genesis; the fork's branch is found by the walk (none of
// its headers are canonical) and the canonical branch by the fast path.
func TestProgramAnchorPerBranch(t *testing.T) {
	stub := newProgrammedStub(t, 10)
	parts := testParts()
	parts.Program = &ProgramParams{RevealBlocks: 2}
	// The share work check reads the lane's quorum for the share target.
	parts.Sortition = &SortitionParams{Height: 1, EpochBlocks: 100, SeedBlocks: 16, Expected: 1, Delta: 6, MaxRound: 16, SharesPerBlock: 4, MaxShares: 8, MaxPayouts: 256}
	e, err := New(Config{Algorithm: stub, Parts: parts})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	c := newForkChain()
	g := genesisHeader()
	c.setCanonical(g)
	canon := chainOf(c, g, 16, 'a')
	for _, h := range canon {
		c.setCanonical(h)
	}
	fork := chainOf(c, canon[5], 11, 'b') // heights 7 to 17 on the fork, parent height 6 canonical
	e.anchor(c)

	// The canonical branch at 12: the fast path (its parent at 11 is
	// canonical) gives the canonical hash at 8.
	a, err := e.programAnchor(c, canon[10].Hash(), 12, 10)
	if err != nil {
		t.Fatal(err)
	}
	if a != canon[7].Hash() {
		t.Fatalf("canonical branch at 12 anchors on %x, want the canonical 8 %x", a[:4], canon[7].Hash().Bytes()[:4])
	}
	// The fork at 12: the walk from its parent at 11 reaches the fork's
	// own 8 (fork[1]), which is not the canonical 8.
	f, err := e.programAnchor(c, fork[4].Hash(), 12, 10)
	if err != nil {
		t.Fatal(err)
	}
	if f != fork[1].Hash() || f == a {
		t.Fatalf("fork at 12 anchors on %x, want the fork's 8 %x", f[:4], fork[1].Hash().Bytes()[:4])
	}
	// Both at 9 (period 0): the genesis hash.
	for _, p := range []common.Hash{canon[7].Hash(), fork[1].Hash()} {
		z, err := e.programAnchor(c, p, 9, 10)
		if err != nil || z != g.Hash() {
			t.Fatalf("period 0 anchors on %x (%v), want genesis", z[:4], err)
		}
	}
	// The fork at 17 whose ancestry crosses two period anchors (8 and
	// 18 is not reached: period 1 still) resolves the fork's 8 too.
	f2, err := e.programAnchor(c, fork[9].Hash(), 17, 10)
	if err != nil || f2 != fork[1].Hash() {
		t.Fatalf("fork at 17 anchors on %x (%v), want the fork's 8", f2[:4], err)
	}
	// An unknown parent is refused, never hashed under the document's
	// anchor.
	if _, err := e.programAnchor(c, common.HexToHash("0x01"), 12, 10); !errors.Is(err, errProgramAncestor) {
		t.Fatalf("unknown ancestor: %v", err)
	}

	// Through the engine: the share work at 12 on each branch runs under
	// its branch's anchor (verifyShareWorkOn), and the stub records both.
	d := big.NewInt(1)
	if err := e.verifyShareWorkOn(canon[10].Hash(), common.HexToHash("0xaa"), 12, 1, common.Hash{}, d, 0, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.verifyShareWorkOn(fork[4].Hash(), common.HexToHash("0xbb"), 12, 1, common.Hash{}, d, 0, nil, nil); err != nil {
		t.Fatal(err)
	}
	ran := stub.anchorsAt(12)
	if len(ran) != 2 || ran[0] != canon[7].Hash() || ran[1] != fork[1].Hash() {
		t.Fatalf("the hashes at 12 ran under %v, want the canonical 8 then the fork's 8", ran)
	}
	// A share naming a parent this node does not hold is refused before
	// any hash.
	if err := e.verifyShareWorkOn(common.HexToHash("0x02"), common.HexToHash("0xcc"), 12, 1, common.Hash{}, d, 0, nil, nil); !errors.Is(err, errProgramAncestor) {
		t.Fatalf("unknown parent's share: %v", err)
	}
	if n := len(stub.anchorsAt(12)); n != 2 {
		t.Fatalf("the refused share ran a hash (%d recorded)", n)
	}

	// igneum_program's reading at a canonical height.
	info, err := e.programAt(12)
	if err != nil || info == nil || info.Period != 1 || info.AnchorHeight != 8 || info.Anchor != canon[7].Hash() || info.Reveal != 2 || info.PeriodBlocks != 10 {
		t.Fatalf("programAt(12) = %+v (%v)", info, err)
	}

	// The resolution cache (TP-250 condition (d)): a second share on the
	// same side-branch parent resolves with no header read; a flood of
	// shares on one tip walks the parents once.
	before := c.reads
	if err := e.verifyShareWorkOn(fork[4].Hash(), common.HexToHash("0xdd"), 12, 2, common.Hash{}, d, 0, nil, nil); err != nil {
		t.Fatal(err)
	}
	if c.reads != before {
		t.Fatalf("the cached parent walked %d headers again", c.reads-before)
	}
	// A different tip on the same branch walks again (its own entry), once.
	before = c.reads
	for i := 0; i < 3; i++ {
		if err := e.verifyShareWorkOn(fork[5].Hash(), common.HexToHash("0xee"), 13, uint64(i), common.Hash{}, d, 0, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if walked := c.reads - before; walked == 0 || walked > 5 {
		t.Fatalf("the new tip walked %d headers over three shares, want one walk of at most 5", walked)
	}

	// F-67: a sealer whose template's parent this node does not hold
	// makes no attempt (nil, never the unbound algorithm).
	orphan := &types.Header{ParentHash: common.HexToHash("0x03"), Number: big.NewInt(12), Difficulty: big.NewInt(1)}
	if a := e.sealAlgoFor(orphan); a != nil {
		t.Fatalf("a sealer with an unresolvable parent got an algorithm: %T", a)
	}
	if n := len(stub.anchorsAt(12)); n != 3 {
		t.Fatalf("hashes at 12: %d, want the three verified shares and none from the sealer", n)
	}
}

// TestProgramReveal: the chain's reveal when named, the relation's default
// of two when not, and a reveal of zero refused by the parts.
func TestProgramReveal(t *testing.T) {
	stub := newProgrammedStub(t, 10)
	e, err := New(Config{Algorithm: stub, Parts: testParts()})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if d := e.programReveal(); d != ProgramRevealDefault {
		t.Fatalf("default reveal %d", d)
	}
	parts := testParts()
	parts.Program = &ProgramParams{RevealBlocks: 6}
	e2, err := New(Config{Algorithm: stub, Parts: parts})
	if err != nil {
		t.Fatal(err)
	}
	defer e2.Close()
	if d := e2.programReveal(); d != 6 {
		t.Fatalf("named reveal %d", d)
	}
	// An algorithm without a programme resolves nothing and is itself.
	plain := newTestEngine(t)
	if a, err := plain.algoUnder(common.Hash{}, 12); err != nil || a != plain.algo {
		t.Fatalf("a plain algorithm bound: %v %v", a, err)
	}
}

// TestProgramAnchorHeightFinality: F-141's chain forms at period 64 with the
// anchor at the finality depth 64, the previous period's first block.
func TestProgramAnchorHeightFinality(t *testing.T) {
	if h, ok := programAnchorHeight(3300, 64, 64); !ok || h != 3200 {
		t.Fatalf("anchor of 3300 at period 64 depth 64: %d %v", h, ok)
	}
	if h, ok := programAnchorHeight(3263, 64, 64); !ok || h != 3136 {
		t.Fatalf("anchor of 3263: %d %v", h, ok)
	}
	if _, ok := programAnchorHeight(100, 64, 64); ok {
		t.Fatal("the first period's anchor is the genesis hash")
	}
}

// TestProgramRevealOfDocumentGate (F-730): a lane whose document was changed
// in place resolves the chain's default from the change's height under
// the form before the landing check (the base instance alone matched), and the
// registered depth for every instance from the chain's document-reveal
// gate; the base instance resolves the registered depth throughout; the
// gate unset is the form before at every height.
func TestProgramRevealOfDocumentGate(t *testing.T) {
	build := func(name string, params []byte) (pow.Algorithm, error) {
		return newProgrammedStub(t, 64), nil
	}
	v := vectorConfig()
	v.Rule = "coverage"
	v.Coordinates[1].AlgorithmParams = json.RawMessage(`{"form":"base"}`)
	v.Coordinates[1].DMin, v.Coordinates[1].LagCap = 10, vector.DerivedLagCap(3600, 2000, 10)
	v.Coordinates[1].RevealBlocks = 64
	v.Coordinates[1].DocumentChanges = []ctypes.IgneumDocumentChange{{Height: 100, AlgorithmParams: json.RawMessage(`{"form":"one"}`), DRef: 2000, DMin: 10, LagCap: vector.DerivedLagCap(3600, 2000, 10)}}
	m, err := vector.New(v, build)
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(Config{Algorithm: m, Parts: testParts()})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	base, changed := m.ByCoordinateAt(1, 99), m.ByCoordinateAt(1, 100)
	if base == changed {
		t.Fatal("the change built no instance of its own")
	}
	// The gate unset: the form before at every height.
	if e.DocumentRevealFrom() != 0 {
		t.Fatal("the gate is set at build")
	}
	for _, h := range []uint64{50, 100, 150, 1000} {
		if r := e.programRevealOf(base, h); r != 64 {
			t.Fatalf("the base at %d, gate unset: %d", h, r)
		}
		if r := e.programRevealOf(changed, h); r != ProgramRevealDefault {
			t.Fatalf("the change's instance at %d, gate unset: %d (the form before falls back to the default)", h, r)
		}
	}
	// The gate at 200: the form before under it, the document's depth for
	// every instance from it.
	e.SetDocumentRevealFrom(200)
	if r := e.programRevealOf(changed, 199); r != ProgramRevealDefault {
		t.Fatalf("the change's instance at 199 under the landing check: %d", r)
	}
	if r := e.programRevealOf(changed, 200); r != 64 {
		t.Fatalf("the change's instance at the landing check: %d", r)
	}
	if r := e.programRevealOf(changed, 5000); r != 64 {
		t.Fatalf("the change's instance past the landing check: %d", r)
	}
	if r := e.programRevealOf(base, 200); r != 64 {
		t.Fatalf("the base at the landing check: %d", r)
	}
	// A coordinate without a reveal resolves the chain's at every height,
	// gated or not.
	if r := e.programRevealOf(m.ByCoordinateAt(0, 200), 200); r != ProgramRevealDefault {
		t.Fatalf("a coordinate without a reveal: %d", r)
	}
	// The anchor the share path resolves follows: at a height past the
	// gate the period's anchor sits the document's 64 under the period's
	// start, under it the default's two.
	if ah, ok := programAnchorHeight(640, 64, e.programRevealOf(changed, 640)); !ok || ah != 576 {
		t.Fatalf("the anchor height at 640 past the landing check: %d %v", ah, ok)
	}
	if ah, ok := programAnchorHeight(192, 64, e.programRevealOf(changed, 192)); !ok || ah != 190 {
		t.Fatalf("the anchor height at 192 under the landing check: %d %v", ah, ok)
	}
}
