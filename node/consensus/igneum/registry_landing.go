// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).

package igneum

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/vector"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// REVISION-CADENCE.md cut B (15 October 2026): the vector registration is
// CHAIN-HELD. A re-registration is a transaction from the registration's
// registry key to VectorRegistryAddress whose data is the selector followed
// by the registration document (JSON); the signature binds the chain id and
// the document bytes. A node landing the block applies the admission
// (Reregistration) against the registration in force with the LANDING
// height as the head, so the rule reads the same on every node, and the
// notice: every activation and retirement the document adds must sit at
// least noticeBlocks above the landing block. The active set at a height
// is the last landing at or below it on the canonical chain, layered on the
// stored (genesis or bootstrap) registration; a block dropped by a
// reorganisation takes its landing with it. Landings persist in the node's
// registry journal and are checked against the canonical chain at start.
var (
	// VectorRegistryAddress is the registry transaction's recipient: an
	// account with no code, the data ignored by the EVM and read by the
	// engine.
	VectorRegistryAddress = common.HexToAddress("0x0000000000000000000000000000000000000b0e")
	// VectorRegistrySelector heads the registry transaction's data.
	VectorRegistrySelector = []byte("IGNV")

	errRegistryNoKey    = errors.New("igneum registry: the registration names no registry key; re-register by configuration")
	errRegistryNotKey   = errors.New("igneum registry: the transaction's sender is not the registry key")
	errRegistryNotice   = errors.New("igneum registry: under the notice")
	errRegistryDocument = errors.New("igneum registry: the document")
	errRegistryChain    = errors.New("igneum registry: the transaction is not protected for this chain")
)

// vectorLanding is one landed re-registration.
type vectorLanding struct {
	Height uint64               `json:"height"`
	Hash   common.Hash          `json:"hash"`
	TxHash common.Hash          `json:"txHash"`
	Doc    *ctypes.IgneumVector `json:"doc"`
}

// vectorRegistry is the engine's chain-held registration state.
type vectorRegistry struct {
	mu       sync.Mutex
	path     string
	base     *ctypes.IgneumVector // the stored registration the node started with
	landings []vectorLanding      // in landing order, every one on the canonical chain
}

// RegistryTxData is a registry transaction's data for a document.
func RegistryTxData(doc []byte) []byte {
	return append(append([]byte(nil), VectorRegistrySelector...), doc...)
}

// SetVectorRegistryJournal names the registry journal and replays its
// landings that stand on the canonical chain (the engine's menu replaced
// with the last).
func (e *Engine) SetVectorRegistryJournal(path string, chain consensus.ChainHeaderReader) error {
	m := e.vectorMenu()
	if m == nil {
		return nil
	}
	r := &e.registry
	r.mu.Lock()
	defer r.mu.Unlock()
	r.path = path
	if r.base == nil {
		r.base = m.Registration()
	}
	data, err := os.ReadFile(path)
	if err == nil && len(data) > 0 {
		var ls []vectorLanding
		if err := json.Unmarshal(data, &ls); err != nil {
			return fmt.Errorf("igneum registry journal %s: %v", path, err)
		}
		for _, l := range ls {
			h := chain.GetHeaderByNumber(l.Height)
			if h == nil || h.Hash() != l.Hash {
				e.log.Warn("Vector registry landing not on the canonical chain at start; dropped with those after it", "height", l.Height, "hash", l.Hash)
				break
			}
			r.landings = append(r.landings, l)
		}
	}
	if n := len(r.landings); n > 0 {
		if err := m.Replace(r.landings[n-1].Doc); err != nil {
			return fmt.Errorf("igneum registry journal: the last landing's document: %v", err)
		}
		e.log.Info("Vector registry landings replayed", "count", n, "last", r.landings[n-1].Height)
	}
	// F-146: a seal coordinate deferred at SetCoordinate is set here when
	// the replayed registration holds it (else by the start-up rescan or
	// a later landing).
	e.sealFromRegistry(m)
	return nil
}

// current is the registration in force (the last landing, else the base).
func (r *vectorRegistry) current() *ctypes.IgneumVector {
	if n := len(r.landings); n > 0 {
		return r.landings[n-1].Doc
	}
	return r.base
}

// VectorRegistration is the registration in force.
func (e *Engine) VectorRegistration() *ctypes.IgneumVector {
	if m := e.vectorMenu(); m != nil {
		return m.Registration()
	}
	return nil
}

// VectorLandingHeight is the height of the last journaled landing (0 none):
// the start-up rescan reads the canonical chain above it for registry
// transactions (the journal a cache, the chain the truth).
func (e *Engine) VectorLandingHeight() uint64 {
	r := &e.registry
	r.mu.Lock()
	defer r.mu.Unlock()
	if n := len(r.landings); n > 0 {
		return r.landings[n-1].Height
	}
	return 0
}

// VectorLandings is the landed re-registrations on the canonical chain, in
// order (height, hash, transaction).
func (e *Engine) VectorLandings() []vectorLanding {
	r := &e.registry
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]vectorLanding(nil), r.landings...)
}

// registryTx reports whether a transaction is a registry transaction and
// the document it carries.
func registryTx(tx *types.Transaction) ([]byte, bool) {
	if tx.To() == nil || *tx.To() != VectorRegistryAddress {
		return nil, false
	}
	d := tx.Data()
	if len(d) <= len(VectorRegistrySelector) || !bytes.Equal(d[:len(VectorRegistrySelector)], VectorRegistrySelector) {
		return nil, false
	}
	return d[len(VectorRegistrySelector):], true
}

// DocumentChangeNotice is the least notice of an in-place document change
// (LANE-MODIFICATION.md section 1): a change lands at least this many
// blocks before its height whatever the registration's noticeBlocks, so
// every node has the new document's references built before the first
// block verified under it (the project's rule, 16 October 2026).
const DocumentChangeNotice = 100

// landingNotice is cut B's notice rule: every coordinate the document adds
// activates, and every retirement it sets, at least noticeBlocks above the
// landing block; checked against the landing height, the same on every
// node.
func landingNotice(current, given *ctypes.IgneumVector, landing uint64) error {
	notice := given.NoticeBlocks
	for i := range given.Coordinates {
		c := given.Coordinates[i]
		if i >= len(current.Coordinates) {
			if c.Activation < landing+notice+1 {
				return fmt.Errorf("%w: coordinate %d (%s) activates at %d, the landing %d plus the notice %d", errRegistryNotice, i, c.Name, c.Activation, landing, notice)
			}
			continue
		}
		if c.RetiredFrom != 0 && c.RetiredFrom != current.Coordinates[i].RetiredFrom && c.RetiredFrom < landing+notice+1 {
			return fmt.Errorf("%w: coordinate %d (%s) retires at %d, the landing %d plus the notice %d", errRegistryNotice, i, c.Name, c.RetiredFrom, landing, notice)
		}
		// An in-place document change the document appends takes effect
		// at least the notice above the landing, and never under
		// DocumentChangeNotice blocks (LANE-MODIFICATION.md section 1:
		// every node builds the new document's references ahead of the
		// change; the single form's build is minutes on the 4 GiB rows).
		dn := notice
		if dn < DocumentChangeNotice {
			dn = DocumentChangeNotice
		}
		for j := len(current.Coordinates[i].DocumentChanges); j < len(c.DocumentChanges); j++ {
			d := c.DocumentChanges[j]
			if d.Void {
				// A void lands BEFORE the change it names comes into
				// force (section 1b): every node drops the change at
				// the landing, none has verified a block under it.
				if d.Height <= landing {
					return fmt.Errorf("%w: coordinate %d (%s) documentChanges[%d] voids the change at %d at a landing of %d, not before it", errRegistryNotice, i, c.Name, j, d.Height, landing)
				}
				continue
			}
			if d.Height < landing+dn+1 {
				return fmt.Errorf("%w: coordinate %d (%s) documentChanges[%d] at %d, the landing %d plus the notice %d", errRegistryNotice, i, c.Name, j, d.Height, landing, dn)
			}
		}
	}
	for _, p := range given.PayChanges {
		if p.Height > 0 && p.Height <= landing && !hasPayChange(current, p.Height) {
			return fmt.Errorf("%w: payChanges at %d at or under the landing %d", errRegistryNotice, p.Height, landing)
		}
	}
	// A clock constant change (F-151) the document adds takes effect
	// above the landing, never at or under it.
	for i := len(current.ConstantsChanges); i < len(given.ConstantsChanges); i++ {
		if h := given.ConstantsChanges[i].Height; h <= landing {
			return fmt.Errorf("%w: constantsChanges at %d at or under the landing %d", errRegistryNotice, h, landing)
		}
	}
	return nil
}

func hasPayChange(v *ctypes.IgneumVector, h uint64) bool {
	for _, p := range v.PayChanges {
		if p.Height == h {
			return true
		}
	}
	return false
}

// admitLanding applies a registry transaction's document against the
// registration in force at the landing height.
func admitLanding(current *ctypes.IgneumVector, doc []byte, sender common.Address, landing uint64) (*ctypes.IgneumVector, error) {
	return admitLandingFrom(current, doc, sender, landing, 0)
}

// admitLandingFrom is admitLanding under the chain's notice-floor gate
// (F-192, ReregistrationFrom): a landing under noticeFrom is admitted as the
// chain admitted it, without the floor.
func admitLandingFrom(current *ctypes.IgneumVector, doc []byte, sender common.Address, landing uint64, noticeFrom uint64) (*ctypes.IgneumVector, error) {
	if current == nil || current.RegistryKey == (common.Address{}) {
		return nil, errRegistryNoKey
	}
	// The key in force at the landing: the current key, or the next key
	// from its height (the rotation under the notice).
	keyInForce := current.RegistryKey
	if current.RegistryKeyNext != (common.Address{}) && current.RegistryKeyFrom > 0 && landing >= current.RegistryKeyFrom {
		keyInForce = current.RegistryKeyNext
	}
	if sender != keyInForce {
		return nil, fmt.Errorf("%w: %s, the key %s", errRegistryNotKey, sender.Hex(), keyInForce.Hex())
	}
	var given ctypes.IgneumVector
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&given); err != nil {
		return nil, fmt.Errorf("%w: %v", errRegistryDocument, err)
	}
	if given.RegistryKey != keyInForce {
		return nil, fmt.Errorf("%w: the registry key differs from the key in force", errRegistryDocument)
	}
	// A rotation named by this document takes over no sooner than the
	// notice after its landing.
	if given.RegistryKeyNext != (common.Address{}) && (given.RegistryKeyNext != current.RegistryKeyNext || given.RegistryKeyFrom != current.RegistryKeyFrom) && given.RegistryKeyFrom < landing+given.NoticeBlocks+1 {
		return nil, fmt.Errorf("%w: registryKeyFrom %d under the landing %d plus the notice %d", errRegistryNotice, given.RegistryKeyFrom, landing, given.NoticeBlocks)
	}
	if err := vector.Validate(&given); err != nil {
		return nil, fmt.Errorf("%w: %v", errRegistryDocument, err)
	}
	// The admission with the LANDING height as the head: a rewind can
	// never be asked (every activation is above the landing by the
	// notice), so a non-zero rewind is refused as a shape fault.
	cur := *current
	cur.RegistryKey = keyInForce
	rewind, err := ReregistrationFrom(&cur, &given, landing, noticeFrom)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errRegistryDocument, err)
	}
	if rewind != 0 {
		return nil, fmt.Errorf("%w: an activation at or under the landing %d", errRegistryNotice, landing)
	}
	if err := landingNotice(current, &given, landing); err != nil {
		return nil, err
	}
	return &given, nil
}

// LandRegistry reads a canonical block's registry transactions (in order)
// and lands the admitted documents; a refused one is logged and ignored.
// Called for every block that becomes canonical.
// RegistryLandingBlock reports whether a block carries a registry
// transaction from the key in force (or its named successor): the block
// chain splits an import batch after such a block and applies its landing
// before the blocks above it are verified (core.BlockChain's
// insertChainLanding, F-197). A chain without the vector form, or with no
// registry key stored, never lands by transaction and reads false; the
// admission itself (the key at the landing height, the document) stays
// LandRegistry's.
func (e *Engine) RegistryLandingBlock(block *types.Block) bool {
	m := e.vectorMenu()
	if m == nil || block == nil {
		return false
	}
	r := m.Registration()
	if r == nil || r.RegistryKey == (common.Address{}) {
		return false
	}
	for _, tx := range block.Transactions() {
		if _, ok := registryTx(tx); !ok || !tx.Protected() {
			continue
		}
		sender, err := types.Sender(types.LatestSignerForChainID(tx.ChainId()), tx)
		if err != nil {
			continue
		}
		if sender == r.RegistryKey || (r.RegistryKeyNext != (common.Address{}) && sender == r.RegistryKeyNext) {
			return true
		}
	}
	return false
}

func (e *Engine) LandRegistry(chain consensus.ChainHeaderReader, block *types.Block) {
	m := e.vectorMenu()
	if m == nil || block == nil {
		return
	}
	var cands []*types.Transaction
	for _, tx := range block.Transactions() {
		if _, ok := registryTx(tx); ok {
			cands = append(cands, tx)
		}
	}
	if len(cands) == 0 {
		return
	}
	r := &e.registry
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.base == nil {
		r.base = m.Registration()
	}
	for _, l := range r.landings {
		if l.Hash == block.Hash() {
			return // landed already
		}
	}
	height := block.NumberU64()
	// The chain's id: a registry transaction must be EIP-155 protected
	// for THIS chain (the consensus chair's read, 15 October 2026: the
	// research chains share the funded key, so an unprotected or
	// other-chain transaction signed by it would replay onto every
	// chain naming the key; the landing refuses it itself, never leaning
	// on the block's own transaction validation).
	var want *big.Int
	if chain != nil && chain.Config() != nil {
		want = chain.Config().GetChainID()
	}
	for _, tx := range cands {
		doc, _ := registryTx(tx)
		if !tx.Protected() || (want != nil && (tx.ChainId() == nil || tx.ChainId().Cmp(want) != 0)) {
			e.log.Warn("Vector registry transaction refused; the registration in force stands", "number", height, "tx", tx.Hash(), "err", errRegistryChain, "txChainId", tx.ChainId(), "chainId", want)
			continue
		}
		signer := types.LatestSignerForChainID(tx.ChainId())
		sender, err := types.Sender(signer, tx)
		if err != nil {
			e.log.Warn("Vector registry transaction unsigned; ignored", "number", height, "tx", tx.Hash(), "err", err)
			continue
		}
		given, err := admitLandingFrom(r.current(), doc, sender, height, e.RegistryNoticeFrom())
		if err == nil {
			// F-151: the clock constants an entry names bounded as the
			// parser bounds the chain's own.
			if err = e.constantsWithinClock(given); err != nil {
				err = fmt.Errorf("%w: %v", errRegistryDocument, err)
			}
		}
		if err == nil && want != nil {
			// The sponsored mark's gate (sponsored.go SponsoredGateRefusal).
			if err = SponsoredGateRefusal(want.Uint64(), height, given); err != nil {
				err = fmt.Errorf("%w: %v", errRegistryDocument, err)
			}
		}
		if err != nil {
			e.log.Warn("Vector registry transaction refused; the registration in force stands", "number", height, "tx", tx.Hash(), "from", sender, "err", err)
			continue
		}
		if err := m.Replace(given); err != nil {
			e.log.Error("Vector registry document admitted but not buildable; the registration in force stands", "number", height, "tx", tx.Hash(), "err", err)
			continue
		}
		r.landings = append(r.landings, vectorLanding{Height: height, Hash: block.Hash(), TxHash: tx.Hash(), Doc: given})
		e.log.Info("Vector registration LANDED (cut B): the chain holds the registration from here", "number", height, "hash", block.Hash(), "tx", tx.Hash(), "coordinates", len(given.Coordinates), "notice", given.NoticeBlocks)
		e.sealFromRegistry(m) // F-146: the landing may hold the deferred seal coordinate
	}
	r.persist(e)
}

// UnlandRegistry drops the landings a block dropped by a reorganisation
// carried (and every later one), the menu returning to the one before.
func (e *Engine) UnlandRegistry(block *types.Block) {
	m := e.vectorMenu()
	if m == nil || block == nil {
		return
	}
	r := &e.registry
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, l := range r.landings {
		if l.Hash == block.Hash() {
			r.landings = r.landings[:i]
			if err := m.Replace(r.current()); err != nil {
				e.log.Error("Vector registry: the registration before a dropped landing not buildable", "err", err)
			}
			e.sealFromRegistry(m) // F-146: the dropped landing may have held the seal coordinate
			e.log.Warn("Vector registration landing dropped with its block by a reorganisation", "number", block.NumberU64(), "hash", block.Hash(), "landingsLeft", len(r.landings))
			r.persist(e)
			return
		}
	}
}

func (r *vectorRegistry) persist(e *Engine) {
	if r.path == "" {
		return
	}
	data, err := json.Marshal(r.landings)
	if err == nil {
		err = os.WriteFile(r.path, data, 0o644)
	}
	if err != nil {
		e.log.Error("Vector registry journal not written", "path", r.path, "err", err)
	}
}

// constantsWithinClock bounds a document's constantsChanges (F-151) as the
// parser bounds the chain's own clock constants (SortitionFor): a tMin
// with the proof under Delta and at most Delta, a tailDelta over tPiMs
// and at most Delta; nothing to bound without the clock amendment.
func (e *Engine) constantsWithinClock(v *ctypes.IgneumVector) error {
	s := e.config.Parts.Sortition
	if s == nil || s.SealWindowDiv == 0 || v == nil {
		return nil
	}
	for j, c := range v.ConstantsChanges {
		// The bounds against the clock in force at the entry's own height
		// (0.10.49: a ClockV1 entry re-pins Delta and tPiMs from its
		// height, so an entry past it is read against the live window).
		delta := s.DeltaAt_(c.Height)
		tPiMs, _ := e.clockConstsAt(c.Height)
		if c.TMin > 0 && (c.TMin*1000+tPiMs >= delta*1000 || c.TMin > delta) {
			return fmt.Errorf("constantsChanges[%d]: tMin %d s plus tPiMs %d ms is not under Delta %d s", j, c.TMin, tPiMs, delta)
		}
		if c.TailDelta > 0 && (c.TailDelta*1000 <= tPiMs || c.TailDelta > delta) {
			return fmt.Errorf("constantsChanges[%d]: tailDelta %d s is not over tPiMs %d ms and at most Delta %d s", j, c.TailDelta, tPiMs, delta)
		}
	}
	return nil
}
