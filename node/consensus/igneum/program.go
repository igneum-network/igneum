package igneum

import (
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/core/types"
)

// The per-period programme's chain-side form (R0E; the page of record is
// docs/igneumhash/schedule/DEVNET11-R0E-CHAIN-FORM.md). An algorithm that
// draws a programme per period (pow.Programmed: IgneumHash versions 9 and
// 10) seeds it from the chain id, the period p = height / P and an ANCHOR,
// the hash of the block at height p x P - D, where P is the document's
// PERIOD_BLOCKS and D the chain's registered reveal delay
// (ctypes.IgneumProgram.RevealBlocks, the relation's default of two when the
// chain names none). The anchor is a header hash, never state, so every
// node derives every period from headers it holds, and it is resolved
// ALONG THE HEADER'S OWN BRANCH: a block or a share is valid under the
// programme its ancestry names, so two branches that share the anchor share
// the programme and two that do not each carry their own (the page's
// section 6). The resolution walks from the parent to the anchor height,
// taking the canonical hash as soon as an ancestor is found canonical (one
// lookup in the common case).

// ProgramRevealDefault is the reveal delay of a chain whose configuration
// names a programmed algorithm and no program block: R0D's "anchor two
// blocks back".
const ProgramRevealDefault = 2

var (
	errProgramAncestor = errors.New("igneum program: the period's anchor is not in this node's header chain")
	errProgramNoChain  = errors.New("igneum program: no chain to resolve the period's anchor from")
)

// setChain keeps the chain reader the engine resolves anchors from (the
// engine's own chain, handed to it at every import and seal; never a
// peer's).
func (e *Engine) setChain(chain consensus.ChainHeaderReader) {
	if chain != nil {
		e.headers.Store(&chainBox{chain})
	}
}

type chainBox struct{ chain consensus.ChainHeaderReader }

func (e *Engine) chainReader() consensus.ChainHeaderReader {
	if b, ok := e.headers.Load().(*chainBox); ok && b != nil {
		return b.chain
	}
	return nil
}

// programReveal is the chain's reveal delay D.
func (e *Engine) programReveal() uint64 {
	if p := e.config.Parts.Program; p != nil && p.RevealBlocks > 0 {
		return p.RevealBlocks
	}
	return ProgramRevealDefault
}

// programAnchorHeight is the height of the anchor block of the period a
// height falls in, and false where the anchor is the genesis hash (the
// period's anchor height below one): p x P - D for p = height / P.
func programAnchorHeight(height, periodBlocks, reveal uint64) (uint64, bool) {
	start := (height / periodBlocks) * periodBlocks
	if start < reveal+1 {
		return 0, false
	}
	return start - reveal, true
}

// programAnchor resolves the anchor of the period a height falls in along
// the branch whose tip is the parent (parentHash at height - 1): the
// canonical hash at the anchor height once an ancestor is found canonical,
// else the hash reached by walking the parents. The genesis hash where the
// anchor height is below one.
func (e *Engine) programAnchor(chain consensus.ChainHeaderReader, parentHash common.Hash, height, periodBlocks uint64) (common.Hash, error) {
	return e.programAnchorAt(chain, parentHash, height, periodBlocks, e.programReveal())
}

// programRevealOf is the reveal depth a base algorithm's programme uses at
// a height: the coordinate's own where the registration names one
// (RevealBlocks), else the chain's. Under the chain's document-reveal gate
// (DocumentRevealFrom; 0 off) the match is the form before F-730: the
// base instance alone (vector.Menu.RevealOf), so a lane whose document was
// changed in place resolved the chain's default from the change's height
// (the public test network from 13,800, 18 October 2026: the sealers
// built and verified with two, the pool's shares built with the
// document's 64 were refused as an invalid mix digest; no split, the
// chain consistent). From the landing check every instance of the lane's document
// (the base, each change's form, their twins) resolves the registered
// depth (vector.Menu.DocumentRevealOf), so the node builds and verifies
// with the document's reveal. The gate keeps every block sealed before it
// valid as it was sealed: a verifier reads the resolver at the block's
// own height.
func (e *Engine) programRevealOf(base pow.Algorithm, height uint64) uint64 {
	if m := e.vectorMenu(); m != nil {
		if from := e.DocumentRevealFrom(); from > 0 && height >= from {
			if r := m.DocumentRevealOf(base); r > 0 {
				return r
			}
		} else if r := m.RevealOf(base); r > 0 {
			return r
		}
	}
	return e.programReveal()
}

// SetDocumentRevealFrom sets the chain's document-reveal gate (F-730): the
// first height at which a programme's reveal depth is resolved from the
// coordinate's registration for every instance of the lane's document; set
// beside the other gates where the engine is built, from the chain id's
// entry params.IgneumChainDocumentRevealFrom; zero is the form before.
func (e *Engine) SetDocumentRevealFrom(from uint64) {
	e.documentRevealFrom.Store(from)
}

// DocumentRevealFrom is the chain's document-reveal gate (0: the form
// before F-730).
func (e *Engine) DocumentRevealFrom() uint64 {
	return e.documentRevealFrom.Load()
}

// programAnchorAt is programAnchor at a given reveal depth.
func (e *Engine) programAnchorAt(chain consensus.ChainHeaderReader, parentHash common.Hash, height, periodBlocks, reveal uint64) (common.Hash, error) {
	if chain == nil {
		return common.Hash{}, errProgramNoChain
	}
	ah, ok := programAnchorHeight(height, periodBlocks, reveal)
	if !ok {
		g := chain.GetHeaderByNumber(0)
		if g == nil {
			return common.Hash{}, errProgramAncestor
		}
		return g.Hash(), nil
	}
	if height == 0 {
		return common.Hash{}, errProgramAncestor
	}
	// The resolution cache by (parent, anchor height): one entry per
	// branch tip the node hashes on, so a share flood on a side branch
	// walks the parents once, not once per share (the security review's TP-250
	// condition (d)). The anchor of a parent at an anchor height never
	// changes (the header chain is immutable), so an entry is never stale;
	// the map is cleared when it reaches its bound.
	key := programKey{parent: parentHash, anchorHeight: ah}
	e.programMu.Lock()
	if a, hit := e.programAnchors[key]; hit {
		e.programMu.Unlock()
		return a, nil
	}
	e.programMu.Unlock()
	a, err := e.programWalk(chain, parentHash, height, ah)
	if err != nil {
		return common.Hash{}, err
	}
	e.programMu.Lock()
	if e.programAnchors == nil || len(e.programAnchors) >= programCacheBound {
		e.programAnchors = make(map[programKey]common.Hash)
	}
	e.programAnchors[key] = a
	e.programMu.Unlock()
	return a, nil
}

// programKey is the resolution cache's key: the branch tip and the anchor
// height the period asks for.
type programKey struct {
	parent       common.Hash
	anchorHeight uint64
}

// programCacheBound is the resolution cache's size, past which it starts
// over: the branch tips a node hashes on in a period are a handful.
const programCacheBound = 1024

// programWalk is the walk from the parent to the anchor height along the
// parent's branch, the canonical hash as soon as an ancestor is canonical.
func (e *Engine) programWalk(chain consensus.ChainHeaderReader, parentHash common.Hash, height, ah uint64) (common.Hash, error) {
	hash, number := parentHash, height-1
	for number > ah {
		if c := chain.GetHeaderByNumber(number); c != nil && c.Hash() == hash {
			// The ancestor is canonical, so the canonical hash at the
			// anchor height is on this branch.
			if a := chain.GetHeaderByNumber(ah); a != nil {
				return a.Hash(), nil
			}
			return common.Hash{}, errProgramAncestor
		}
		h := chain.GetHeader(hash, number)
		if h == nil {
			return common.Hash{}, fmt.Errorf("%w: height %d hash %x", errProgramAncestor, number, hash[:4])
		}
		hash, number = h.ParentHash, number-1
	}
	if number != ah {
		return common.Hash{}, fmt.Errorf("%w: the parent at %d is below the anchor height %d", errProgramAncestor, number, ah)
	}
	return hash, nil
}

// algoUnder is the engine's algorithm bound to the programme of the period
// a height falls in on the branch whose tip is the parent: the algorithm
// itself where it draws no programme at that height, else its view under
// the resolved anchor. Every hash of a header or a share runs on what this
// returns, so a verifier on two branches and a sealer on its parent's each
// run the programme their branch names.
func (e *Engine) algoUnder(parentHash common.Hash, height uint64) (pow.Algorithm, error) {
	return e.algoViewOf(e.algo, parentHash, height)
}

// algoViewOf is an algorithm bound to the programme of a height's period on
// the branch whose tip is the parent: the algorithm itself where it draws
// no programme, else its view under the resolved anchor.
func (e *Engine) algoViewOf(base pow.Algorithm, parentHash common.Hash, height uint64) (pow.Algorithm, error) {
	return e.algoViewOn(e.chainReader(), base, parentHash, height)
}

// algoViewOn is algoViewOf through the given reader.
func (e *Engine) algoViewOn(chain consensus.ChainHeaderReader, base pow.Algorithm, parentHash common.Hash, height uint64) (pow.Algorithm, error) {
	p, ok := base.(pow.Programmed)
	if !ok {
		return base, nil
	}
	period := p.PeriodBlocks(height)
	if period == 0 {
		return base, nil
	}
	anchor, err := e.programAnchorAt(chain, parentHash, height, period, e.programRevealOf(base, height))
	if err != nil {
		return nil, err
	}
	return p.Under(anchor), nil
}

// programAnchorFor is the resolved anchor of a height's period on the
// branch whose tip is the parent, with the anchor block's height, for the
// work package; ok false where the base draws no programme.
func (e *Engine) programAnchorFor(base pow.Algorithm, parentHash common.Hash, height uint64) (anchor common.Hash, anchorHeight uint64, ok bool) {
	p, is := base.(pow.Programmed)
	if !is {
		return common.Hash{}, 0, false
	}
	period := p.PeriodBlocks(height)
	if period == 0 {
		return common.Hash{}, 0, false
	}
	reveal := e.programRevealOf(base, height)
	a, err := e.programAnchorAt(e.chainReader(), parentHash, height, period, reveal)
	if err != nil {
		return common.Hash{}, 0, false
	}
	ah, _ := programAnchorHeight(height, period, reveal)
	return a, ah, true
}

// algoFor is the algorithm a header's seal is checked and found under: on
// a VectorWork chain the header's coordinate's relation (vector.go), then
// the programme of its period on its own branch where that relation draws
// one.
func (e *Engine) algoFor(header *types.Header) (pow.Algorithm, error) {
	return e.algoForOn(nil, header)
}

// algoForOn is algoFor reading the branch through the given reader (a
// verification batch's overlay, consensus.go batchReader); nil reads the
// engine's own chain.
func (e *Engine) algoForOn(chain consensus.ChainHeaderReader, header *types.Header) (pow.Algorithm, error) {
	base := e.algo
	if a, vec := e.vectorAlgoFor(header); vec {
		if a == nil {
			return nil, errVectorCoord
		}
		base = a
		// F-494 (0.10.58): an UNKEYED block of a keyed coordinate (the open
		// seat, the liveness round) is hashed with the form's twin, as the
		// schedule's rows hash theirs with plain(): a header without
		// shares, unless it is this node's own template marked keyed at
		// assembly (its shares come with the seal). The document's S is
		// read elsewhere from the keyed form (docKeyedRounds), never from
		// the twin.
		if tw := e.vectorUnkeyedFor(header); tw != nil {
			base = tw
		}
	}
	if chain == nil {
		chain = e.chainReader()
	}
	return e.algoViewOn(chain, base, header.ParentHash, header.Number.Uint64())
}

// sealAlgoFor is algoFor for the sealer's and the prover's own templates:
// NIL when the anchor cannot be resolved on this node's own chain, logged,
// so the caller makes no attempt on that template and takes the next one
// (the security review's F-67, 12 October 2026: a sealer never hashes or proves
// under the document's anchor, which no verifier accepts; it skips the
// tick instead).
func (e *Engine) sealAlgoFor(header *types.Header) pow.Algorithm {
	a, err := e.algoFor(header)
	if err != nil {
		e.log.Warn("Programme anchor unresolved on this node's own chain: no attempt on this template", "number", header.Number, "err", err)
		return nil
	}
	return a
}

// programInfo is what igneum_program serves: the period, the anchor height
// and the anchor of a height on the canonical chain.
type programInfo struct {
	PeriodBlocks uint64      `json:"periodBlocks"`
	Reveal       uint64      `json:"revealBlocks"`
	Period       uint64      `json:"period"`
	AnchorHeight uint64      `json:"anchorHeight"`
	Anchor       common.Hash `json:"anchor"`
}

func (e *Engine) programAt(height uint64) (*programInfo, error) {
	period := pow.PeriodBlocksAt(e.algo, height)
	if period == 0 {
		return nil, nil
	}
	chain := e.chainReader()
	if chain == nil {
		return nil, errProgramNoChain
	}
	info := &programInfo{PeriodBlocks: period, Reveal: e.programReveal(), Period: height / period}
	if ah, ok := programAnchorHeight(height, period, info.Reveal); ok {
		info.AnchorHeight = ah
	}
	a := chain.GetHeaderByNumber(info.AnchorHeight)
	if a == nil {
		return nil, errProgramAncestor
	}
	info.Anchor = a.Hash()
	return info, nil
}
