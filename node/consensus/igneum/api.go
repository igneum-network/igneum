// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package igneum

import (
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm/igneumreg"
	"github.com/ethereum/go-ethereum/eth/protocols/ishr"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/rpc"
)

var errEngineStopped = errors.New("igneum engine stopped")

// API is the ethash-compatible mining RPC under the eth namespace
// (DESIGN.md 4.5): existing getwork tooling keeps working.
type API struct {
	engine *Engine
}

// GetWork returns the current work as the four ethash strings: header hash,
// seed hash, target (2^256/difficulty) and block number.
func (api *API) GetWork() ([4]string, error) {
	w, err := api.engine.fetchWork()
	if err != nil {
		return [4]string{}, err
	}
	return legacyWork(w), nil
}

// SubmitWork submits a solution. It returns true when accepted; false for an
// invalid, stale or unknown solution (igneum_submitWork gives the reason).
func (api *API) SubmitWork(nonce types.BlockNonce, hash, digest common.Hash, shares *hexutil.Bytes) bool {
	var s []byte
	if shares != nil {
		s = *shares
	}
	return api.engine.submitWork(nonce, hash, digest, s) == nil
}

// SubmitHashrate records a remote worker's hashrate under its id.
func (api *API) SubmitHashrate(rate hexutil.Uint64, id common.Hash) bool {
	return api.engine.submitHashrate(uint64(rate), id)
}

// GetHashrate returns the combined local and reported hashrate.
func (api *API) GetHashrate() uint64 {
	return uint64(api.engine.Hashrate())
}

// SearchRate is igneum_searchRate: this node's last finished keyed search
// (attempts, milliseconds, attempts a second) and the one in flight; the
// live keyed-search rate dMin is pinned from (searchrate.go).
func (api *IgneumAPI) SearchRate() SearchRate {
	return api.engine.SearchRateNow()
}

// Program is igneum_program: the per-period programme's chain-side reading
// at a height on the canonical chain (program.go): the period, the reveal,
// the anchor height and the anchor hash every node draws the period's
// programme from. Null on a chain whose algorithm draws no programme at
// that height.
// VectorWork is igneum_vectorWork(number): a canonical block's coordinate
// and difficulty, and the chain's cumulative per-coordinate state after it
// (vector.go): the counts, the work (the sum of the coordinate's
// difficulties), the reference difficulties the work is normalised by, the
// geometric rule's product, the minimum rule's two keys, and every
// coordinate's controller output for the block's child. Null on a chain
// without the vector form.
// A block hash reads any block the node holds, canonical or not (a
// displaced twin's figures beside the canonical one's; the security review's
// re-read of the height-31 race, 13 October 2026).
func (api *IgneumAPI) VectorWork(at rpc.BlockNumberOrHash) (*VectorWorkInfo, error) {
	if api.chain == nil {
		return nil, errors.New("igneum: no chain behind the API")
	}
	api.engine.setChain(api.chain)
	var header *types.Header
	if hash, ok := at.Hash(); ok {
		header = api.chain.GetHeaderByHash(hash)
		if header == nil {
			return nil, errors.New("igneum: no such header")
		}
		return api.engine.vectorInfo(api.chain, header)
	}
	number, _ := at.Number()
	switch {
	case number == rpc.LatestBlockNumber:
		header = api.chain.CurrentHeader()
	case number >= 0:
		header = api.chain.GetHeaderByNumber(uint64(number))
	default:
		return nil, fmt.Errorf("igneum: block tag %s is not supported by igneum_vectorWork", number.String())
	}
	if header == nil {
		return nil, errors.New("igneum: no such canonical header")
	}
	return api.engine.vectorInfo(api.chain, header)
}

// WholeWork is igneum_wholeWork(at): the balanced reward's settlement at
// a canonical block (consensus/igneum/balanced.go WholeWorkInfo): the
// rule's state, the carried shares as the list row reads them (key,
// coordinate, step, work) and every key's accrued work per coordinate
// over the pay window, its floored terms, its balanced work and the
// amount the block paid it.
func (api *IgneumAPI) WholeWork(at rpc.BlockNumberOrHash) (*WholeWorkInfo, error) {
	if api.chain == nil {
		return nil, errors.New("igneum: no chain behind the API")
	}
	api.engine.setChain(api.chain)
	var header *types.Header
	if hash, ok := at.Hash(); ok {
		header = api.chain.GetHeaderByHash(hash)
	} else {
		number, _ := at.Number()
		switch {
		case number == rpc.LatestBlockNumber:
			header = api.chain.CurrentHeader()
		case number >= 0:
			header = api.chain.GetHeaderByNumber(uint64(number))
		default:
			return nil, fmt.Errorf("igneum: block tag %s is not supported by igneum_wholeWork", number.String())
		}
	}
	if header == nil {
		return nil, errors.New("igneum: no such header")
	}
	var blk *types.Block
	if br, ok := api.chain.(blockReader); ok {
		blk = br.GetBlock(header.Hash(), header.Number.Uint64())
	}
	return api.engine.wholeWorkInfo(api.chain, header, blk)
}

// SubmitGraphShare is igneum_submitGraphShare(address, extraNonce, nonce,
// ticket, height, parentHash, step): a graph coordinate's share as its
// 168-byte ticket under the member's share header (WHOLEWORK.md section
// 10 (b)), verified by the coordinate's ticket verifier, its ticket hash
// against the share target at the step; answered as igneum_submitShare.
func (api *IgneumAPI) SubmitGraphShare(address common.Address, extraNonce, nonce hexutil.Uint64, ticket hexutil.Bytes, height hexutil.Uint64, parentHash *common.Hash, step *hexutil.Uint64) ShareSubmitResult {
	var st uint8
	if step != nil {
		if uint64(*step) > MaxShareStep {
			return ShareSubmitResult{Accepted: false, Reason: errShareStep.Error()}
		}
		st = uint8(*step)
	}
	if api.chain == nil {
		return ShareSubmitResult{Accepted: false, Reason: "igneum: no chain behind the API"}
	}
	api.engine.setChain(api.chain)
	ok, reason := api.engine.SubmitGraphShareAt(api.chain, address, uint64(extraNonce), uint64(nonce), []byte(ticket), uint64(height), parentHash, st)
	return ShareSubmitResult{Accepted: ok, Reason: reason}
}

// HeaderAt is one header the node holds at a height, for igneum_headersAt.
type HeaderAt struct {
	Hash       common.Hash  `json:"hash"`
	Canonical  bool         `json:"canonical"`
	ParentHash common.Hash  `json:"parentHash"`
	Coord      *uint8       `json:"coord,omitempty"`
	Difficulty *hexutil.Big `json:"difficulty"`
	Time       uint64       `json:"timestamp"`
}

// HeadersAt lists every header the node holds at a height, the canonical
// one and the side blocks (a race's dropped twins by full hash, so a
// reader can take each one's igneum_vectorWork by hash).
func (api *IgneumAPI) HeadersAt(number uint64) ([]HeaderAt, error) {
	if api.chain == nil {
		return nil, errors.New("igneum: no chain behind the API")
	}
	lister, ok := api.chain.(interface{ HeaderHashesAt(uint64) []common.Hash })
	if !ok {
		return nil, errors.New("igneum: the chain behind the API lists no side blocks")
	}
	canonical := api.chain.GetHeaderByNumber(number)
	out := []HeaderAt{}
	for _, h := range lister.HeaderHashesAt(number) {
		header := api.chain.GetHeader(h, number)
		if header == nil {
			continue
		}
		row := HeaderAt{Hash: h, Canonical: canonical != nil && canonical.Hash() == h, ParentHash: header.ParentHash, Difficulty: (*hexutil.Big)(header.Difficulty), Time: header.Time}
		if c, ok := header.CoordTag(); ok {
			cc := c
			row.Coord = &cc
		}
		out = append(out, row)
	}
	return out, nil
}

func (api *IgneumAPI) Program(number rpc.BlockNumber) (*programInfo, error) {
	if api.chain == nil {
		return nil, errors.New("igneum: no chain behind the API")
	}
	api.engine.setChain(api.chain)
	var height uint64
	switch {
	case number == rpc.LatestBlockNumber:
		height = api.chain.CurrentHeader().Number.Uint64()
	case number >= 0:
		height = uint64(number)
	default:
		return nil, fmt.Errorf("igneum: block tag %s is not supported by igneum_program", number.String())
	}
	return api.engine.programAt(height)
}

// IgneumAPI is the object form of the mining RPC under the igneum namespace,
// plus igneum_supply (supply.go).
type IgneumAPI struct {
	engine  *Engine
	chain   consensus.ChainHeaderReader
	burns   *burnIndex
	credits *creditIndex
	// attribution caches igneum_attribution's last report (attribution.go).
	attribution attributionCache
}

// NewIgneumAPI builds the igneum namespace service for an engine on a chain.
func NewIgneumAPI(e *Engine, chain consensus.ChainHeaderReader) *IgneumAPI {
	return &IgneumAPI{engine: e, chain: chain, burns: newBurnIndex(), credits: newCreditIndex()}
}

// SubmitResult is the answer to igneum_submitWork and igneum_submitTicket.
// Accepted is true ONLY when the solution verified against a live template
// on the chain's head and was handed to the miner's seal path, and then
// Hash and Number name the block it seals (the pool counts a solution on
// that hash's presence on the chain, never on this answer alone); false
// carries the reason (no live job, stale, duplicate, invalid, the
// verifier busy), and the node logs every outcome (F-418).
type SubmitResult struct {
	Accepted bool         `json:"accepted"`
	Reason   string       `json:"reason,omitempty"`
	Hash     *common.Hash `json:"hash,omitempty"`
	Number   uint64       `json:"number,omitempty"`
}

// Algorithm returns the registered name of the proof of work in force.
func (api *IgneumAPI) Algorithm() string {
	if api.engine.algo == nil {
		return ""
	}
	return api.engine.algo.Name()
}

// GetWork returns the current work package with named fields, including the
// algorithm name so a worker refuses work for the wrong algorithm.
func (api *IgneumAPI) GetWork(coord *uint8) (*pow.WorkPackage, error) {
	// VectorWork: a caller may name the coordinate it mines; this node's
	// templates are its own coordinate's, so another is refused by name
	// (a pool fans out to one node per coordinate).
	if coord != nil {
		if api.engine.vectorMenu() == nil {
			return nil, errVectorCoordOff
		}
		if *coord != api.engine.Coordinate() {
			return nil, fmt.Errorf("%w: this node seals coordinate %d, not %d", errVectorCoord, api.engine.Coordinate(), *coord)
		}
	}
	w, err := api.engine.fetchWork()
	if err != nil {
		// A node that seals nothing serves the SHARE work alone (the share
		// gossip's launch form, sharegossip.go ShareWork): a worker names
		// one node, mines shares on its head and submits them here.
		if errors.Is(err, errNoMiningWork) {
			if sw, serr := api.engine.ShareWork(); serr == nil {
				return sw, nil
			}
		}
		return nil, err
	}
	// A sealer's template can sit behind the head: while the sealer waits
	// on its round floor it commits no new template, so the last one's
	// share fields name a height the chain has passed; a worker reading
	// them submits at that height until the cap refuses it (ecosystem's run
	// 8, 11 October 2026: 62 percent refused "address at cap" at 7 a second).
	// The share fields are therefore the HEAD's successor's whenever the
	// template is at or behind the head (the seal fields stay the template's).
	if api.chain == nil {
		return &w, nil
	}
	if head := api.chain.CurrentHeader(); head != nil && withholdSealJob(&w, head) {
		if sw, serr := api.engine.ShareWork(); serr == nil {
			w.ShareTarget, w.SharesPerBlock, w.ShareHeight, w.ShareParent = sw.ShareTarget, sw.SharesPerBlock, sw.ShareHeight, sw.ShareParent
			w.ShareParams, w.ShareParamsDigest, w.ShareStepMax = sw.ShareParams, sw.ShareParamsDigest, sw.ShareStepMax
			// The anchor follows the share height (the period's own at a
			// boundary, ShareWork): the template's seal fields are dead
			// behind the head, so the package's one anchor is the shares'.
			if sw.ProgramAnchor != nil {
				w.ProgramAnchor, w.AnchorHeight = sw.ProgramAnchor, sw.AnchorHeight
			}
		}
	}
	return &w, nil
}

// withholdSealJob (F-420, 17 October 2026: the pool's one keyed solution
// of the bounded run, found against the 3,871 template the node kept
// serving while its 3,872 and 3,873 templates waited on their round, was
// admitted and dropped at the parent check 0.46 s later) reports whether
// the package's template is NOT on the chain's head, and then takes the
// block job out of it: HeaderHash and Target zero, StaleTemplate naming
// the reason. The remote sealer's work is refreshed only by a commit, and
// under a round WAIT no template exists to commit, so without this the
// served job is the last commit's, behind the head, and every solution a
// worker finds against it is a dropped block. The share fields are the
// caller's to re-point (the head's successor's).
func withholdSealJob(w *pow.WorkPackage, head *types.Header) bool {
	headNumber := head.Number.Uint64()
	var why string
	switch {
	case w.Height <= headNumber:
		why = fmt.Sprintf("no block job: the node's own template (height %d) is at or behind the head %d (the sealer waits on its round, or the template is not yet re-cut); the share fields are the head's successor's", w.Height, headNumber)
	case w.ParentHash != (common.Hash{}) && w.ParentHash != head.Hash():
		why = fmt.Sprintf("no block job: the node's own template (height %d, parent %s) is not on the head %d %s; the share fields are the head's successor's", w.Height, w.ParentHash.Hex(), headNumber, head.Hash().Hex())
	default:
		return false
	}
	// The withheld job's one shape (the security review's read): Target nil
	// (null on the wire), as the node's own share-only package serves
	// it (sharegossip.go ShareWork), never the number 0, which a pool
	// that reads no staleTemplate would take as a boundary.
	w.StaleTemplate = why
	w.HeaderHash = common.Hash{}
	w.Target = nil
	return true
}

// SubmitWork submits a solution and reports acceptance with the reason on
// failure.
func (api *IgneumAPI) SubmitWork(nonce types.BlockNonce, headerHash, mixDigest common.Hash, shares *hexutil.Bytes) SubmitResult {
	// shares: the keyed document's S shares (32 bytes each, concatenated),
	// the hostile-key rule's work binding; absent or empty on an unkeyed
	// document.
	var s []byte
	if shares != nil {
		s = *shares
	}
	sealed, number, err := api.engine.submitWorkResult(nonce, headerHash, mixDigest, s, nil)
	if err != nil {
		return SubmitResult{Accepted: false, Reason: err.Error()}
	}
	return SubmitResult{Accepted: true, Hash: &sealed, Number: number}
}

// SubmitTicket is SubmitWork for a ticketed coordinate (the Graph): the
// nonce, the template's header hash, the mix digest (the ticket hash) and
// the ticket's canonical bytes (168 for cuckaroo29: 42 edges, LE32 each).
// The node verifies the ticket under (headerHash, height, nonce) and the
// coordinate's target before the block is sealed.
func (api *IgneumAPI) SubmitTicket(nonce types.BlockNonce, headerHash, mixDigest common.Hash, ticket hexutil.Bytes) SubmitResult {
	sealed, number, err := api.engine.submitWorkResult(nonce, headerHash, mixDigest, nil, ticket)
	if err != nil {
		return SubmitResult{Accepted: false, Reason: err.Error()}
	}
	return SubmitResult{Accepted: true, Hash: &sealed, Number: number}
}

// SubmitHashrate records a remote worker's hashrate under its id.
func (api *IgneumAPI) SubmitHashrate(rate hexutil.Uint64, id common.Hash) bool {
	return api.engine.submitHashrate(uint64(rate), id)
}

func (e *Engine) fetchWork() (pow.WorkPackage, error) {
	if e.remote == nil || e.algo == nil {
		return pow.WorkPackage{}, errors.New("mining RPC not available in fake mode")
	}
	var (
		workCh = make(chan pow.WorkPackage, 1)
		errc   = make(chan error, 1)
	)
	select {
	case e.remote.fetchWorkCh <- &sealWork{errc: errc, res: workCh}:
	case <-e.remote.exitCh:
		return pow.WorkPackage{}, errEngineStopped
	}
	select {
	case work := <-workCh:
		return work, nil
	case err := <-errc:
		return pow.WorkPackage{}, err
	}
}

func (e *Engine) submitWork(nonce types.BlockNonce, hash, digest common.Hash, shares []byte) error {
	return e.submitWorkTicket(nonce, hash, digest, shares, nil)
}

// submitWorkTicket is submitWork with a ticketed coordinate's witness (the
// Graph's ticket, 168 bytes): a remote cuckaroo29 solver seals the Graph
// coordinate through the work interface (the GPU team's solver on a rented pod
// against vectorwork5's graph node, the project's order of 13 October 2026).
func (e *Engine) submitWorkTicket(nonce types.BlockNonce, hash, digest common.Hash, shares, ticket []byte) error {
	_, _, err := e.submitWorkResult(nonce, hash, digest, shares, ticket)
	return err
}

// submitWorkResult is submitWorkTicket returning the sealed block's hash
// and number on acceptance (F-418): the verifier sets them on the result
// from the delivered block (after attachPi: a keyed seal's PiHash is
// inside the hash) before it answers errc, so the read after errc is
// ordered.
func (e *Engine) submitWorkResult(nonce types.BlockNonce, hash, digest common.Hash, shares, ticket []byte) (common.Hash, uint64, error) {
	if e.remote == nil {
		return common.Hash{}, 0, errEngineStopped
	}
	errc := make(chan error, 1)
	r := &mineResult{nonce: nonce, mixDigest: digest, hash: hash, shares: shares, ticket: ticket, errc: errc}
	select {
	case e.remote.submitWorkCh <- r:
	case <-e.remote.exitCh:
		return common.Hash{}, 0, errEngineStopped
	}
	if err := <-errc; err != nil {
		return common.Hash{}, 0, err
	}
	return r.sealed, r.number, nil
}

func (e *Engine) submitHashrate(rate uint64, id common.Hash) bool {
	if e.remote == nil {
		return false
	}
	done := make(chan struct{}, 1)
	select {
	case e.remote.submitRateCh <- &hashrate{done: done, rate: rate, id: id}:
	case <-e.remote.exitCh:
		return false
	}
	<-done
	return true
}

// AuthorityInfo is igneum_authority: the miner-bound work authority
// prototype's rule as this node holds it (nil fields without the rule).
type AuthorityInfo struct {
	Enabled   bool           `json:"enabled"`
	Height    uint64         `json:"height,omitempty"`
	Registry  common.Address `json:"registry,omitempty"`
	Cap       *hexutil.Big   `json:"cap,omitempty"`
	Active    bool           `json:"active"`
	Head      uint64         `json:"head"`
	BondKey   *common.Hash   `json:"bondKey,omitempty"`
	Lax       bool           `json:"lax,omitempty"`
	Prototype string         `json:"prototype"`
	// The rule's other constants (the wallet-security seat's ask of 12
	// October 2026): the registry's code version, the exit lock in
	// blocks, the floor's reference level in basis points and the
	// issuance line it never falls under.
	RegistryVersion uint64       `json:"registryVersion,omitempty"`
	LockBlocks      uint64       `json:"lockBlocks,omitempty"`
	BRefBps         uint64       `json:"bRefBps,omitempty"`
	SRef            *hexutil.Big `json:"sRef,omitempty"`
}

// Authority reports the rule's constants and whether it binds the next
// block.
func (api *IgneumAPI) Authority() AuthorityInfo {
	info := AuthorityInfo{Prototype: "miner-bound work authority, weak form, research prototype; nothing adopted"}
	head := api.chain.CurrentHeader()
	if head != nil {
		info.Head = head.Number.Uint64()
	}
	a := api.engine.Authority()
	if a == nil {
		return info
	}
	info.Enabled = true
	info.Height = a.Height
	info.Registry = a.Registry
	info.Cap = (*hexutil.Big)(a.Cap)
	info.Active = a.Active(info.Head + 1)
	info.RegistryVersion = a.RegistryVersion
	info.LockBlocks = a.LockBlocks
	info.BRefBps = a.BRefBps
	if a.SRef != nil {
		info.SRef = (*hexutil.Big)(a.SRef)
	}
	info.BondKey = api.engine.BondKey()
	api.engine.lock.Lock()
	info.Lax = api.engine.authorityLax
	api.engine.lock.Unlock()
	return info
}

// BondInfo is igneum_bondOf: a key's registry record at a block's state.
type BondInfo struct {
	Key     common.Hash    `json:"key"`
	Amount  *hexutil.Big   `json:"amount"`
	Account common.Address `json:"account"`
	Bonded  bool           `json:"bonded"`
	Signer  common.Hash    `json:"signer"`
}

// SortitionInfo is igneum_sortition: the rule as this node holds it and
// this node's own draw at the next height.
type SortitionInfo struct {
	Enabled       bool         `json:"enabled"`
	Params        any          `json:"params,omitempty"`
	Active        bool         `json:"active"`
	Head          uint64       `json:"head"`
	Next          uint64       `json:"next"`
	Epoch         uint64       `json:"epoch,omitempty"`
	Seed          common.Hash  `json:"seed,omitempty"`
	SnapshotBlock uint64       `json:"snapshotBlock"`
	Signer        *common.Hash `json:"signer,omitempty"`
	BondKey       *common.Hash `json:"bondKey,omitempty"`
	Units         uint64       `json:"units"`
	Bond          *hexutil.Big `json:"bond,omitempty"`
	Total         *hexutil.Big `json:"total,omitempty"`
	EligibleRound uint64       `json:"eligibleRound"`
	Prototype     string       `json:"prototype"`
	// The genesis form (sortition_genesis.go): the epoch's table as the
	// chain carries it and this node's standing in it.
	OpenSeat      bool         `json:"openSeat,omitempty"`
	UnitPrice     *hexutil.Big `json:"unitPrice,omitempty"`
	UnitsTotal    uint64       `json:"unitsTotal,omitempty"`
	OpenShareBps  uint64       `json:"openShareBps,omitempty"`
	OpenSeatRound uint64       `json:"openSeatRound,omitempty"`
	TableLive     bool         `json:"tableLive,omitempty"` // computed from the parent state (epochs 0 and 1), not read
	Floor         *hexutil.Big `json:"floor,omitempty"`
	KeyedRounds   uint64       `json:"keyedRounds,omitempty"`
	// Clock is the round clock's state after the head (clock.go: the two
	// difficulties, the head's and its parent's times, the head's sealer's
	// rank and PA-48b's dark count), absent before the amendment's first
	// block; the security review's rows read the count per height here beside
	// the slot bytes (20:46 UK, 11 October 2026).
	Clock *ClockInfo `json:"clock,omitempty"`
}

// ClockInfo is the clock's state as igneum_sortition prints it.
type ClockInfo struct {
	Chain      *hexutil.Big `json:"chain"`
	Seal       *hexutil.Big `json:"seal"`
	Time       uint64       `json:"time"`
	ParentTime uint64       `json:"parentTime"`
	Rank       int          `json:"rank"`
	Dark       uint64       `json:"dark"`
}

// Sortition reports the sortition rule and this node's draw at head + 1.
// SortitionParamsView is igneum_sortition's params field: the rule's
// constants as the engine holds them, with the clock constants in force at
// the height (the registration's tMin and tailDelta where a constantsChanges
// entry names them, F-151) as values beside the chain's own.
type SortitionParamsView struct {
	SortitionParams
	// TMinAt and TailDeltaAt are the clock floor and the tail round length
	// in force at Height (RoundStart and TMinAt's reads), the registration's
	// over the chain's own.
	Height      uint64 `json:"height"`
	TMinAt      uint64 `json:"tMinAt"`
	TailDeltaAt uint64 `json:"tailDeltaAt"`
}

func sortitionParamsView(s *SortitionParams, height uint64) SortitionParamsView {
	v := SortitionParamsView{SortitionParams: *s, Height: height, TMinAt: s.TMinAt(height)}
	if r := s.RoundStart(height, 2) - s.RoundStart(height, 1); r > 0 {
		v.TailDeltaAt = r
	}
	v.SortitionParams.ConstantsAt = nil
	return v
}

func (api *IgneumAPI) Sortition() (*SortitionInfo, error) {
	info := &SortitionInfo{Prototype: "sortition-gated rule, hybrid proof of work and bonded capital, research prototype; nothing adopted"}
	head := api.chain.CurrentHeader()
	if head != nil {
		info.Head = head.Number.Uint64()
	}
	info.Next = info.Head + 1
	s := api.engine.config.Parts.Sortition
	if s == nil {
		return info, nil
	}
	info.Enabled = true
	// The params view: the rule's constants with the registration's clock
	// constants at the next height as values (F-177: the F-151 hook is a
	// function the RPC cannot marshal, "json: unsupported type", which
	// left the public test network's igneum_sortition empty from 0.10.20
	// and the root feed's bond total null; the hook itself is not served).
	info.Params = sortitionParamsView(s, info.Next)
	info.Active = s.Active(info.Next)
	info.Signer = api.engine.SignerKey()
	info.BondKey = api.engine.BondKey()
	if s.OpenSeat && info.Active {
		// The genesis form: the table from the head's state.
		info.OpenSeat = true
		info.KeyedRounds = api.engine.ownDocKeyedRounds(info.Next)
		sr, ok := api.chain.(stateReader)
		if !ok || head == nil {
			return info, errors.New("no state access on this chain reader")
		}
		st, err := sr.StateAt(head.Root)
		if err != nil {
			return info, err
		}
		t, err := api.engine.tableAt(st, s.EpochAt(info.Next))
		if err != nil {
			return info, err
		}
		info.Epoch, info.Seed, info.TableLive = t.epoch, t.seed, t.live
		info.UnitPrice, info.UnitsTotal = (*hexutil.Big)(t.u), t.nTotal
		info.OpenShareBps = (NotionalUnits - t.nTotal) * 10000 / NotionalUnits
		info.OpenSeatRound = s.OpenSeatRound(t.seed, t.nTotal, info.Next)
		reg := api.engine.config.Parts.Authority.Registry
		info.Floor = (*hexutil.Big)(igneumreg.Floor(st, reg))
		info.Total = (*hexutil.Big)(igneumreg.Total(st, reg))
		if api.engine.ClockOn(info.Next) {
			if c, err := api.engine.ReadClock(st); err == nil && c != nil {
				info.Clock = &ClockInfo{Chain: (*hexutil.Big)(c.Chain), Seal: (*hexutil.Big)(c.Seal), Time: c.Time, ParentTime: c.ParentTime, Rank: c.Rank, Dark: c.Dark}
			}
		}
		if info.BondKey != nil {
			d, err := api.engine.drawAt(st, *info.BondKey, info.Next)
			if err != nil {
				return info, err
			}
			info.Units, info.EligibleRound = d.units, d.round
			info.Bond = (*hexutil.Big)(igneumreg.BondOf(st, reg, *info.BondKey).Amount)
		}
		return info, nil
	}
	if !info.Active || info.BondKey == nil {
		return info, nil
	}
	api.engine.sortitionAnchor(api.chain)
	snap, err := api.engine.snapshotFor(info.Next)
	if err != nil {
		return info, err
	}
	info.Epoch, info.Seed, info.SnapshotBlock = snap.epoch, snap.seed, snap.block
	sr, ok := api.chain.(stateReader)
	if !ok {
		return info, errors.New("no state access on this chain reader")
	}
	st, err := sr.StateAt(snap.root)
	if err != nil {
		return info, err
	}
	units, bond, total := api.engine.config.Parts.Authority.Units(st, *info.BondKey)
	info.Units, info.Bond, info.Total = units, (*hexutil.Big)(bond), (*hexutil.Big)(total)
	info.EligibleRound = s.EligibleRound(snap.seed, *info.BondKey, units, info.Next)
	return info, nil
}

// stateReader is what the chain gives the API for a state at a root.
type stateReader interface {
	StateAt(root common.Hash) (*state.StateDB, error)
}

// BondOf reads a key's bond from the registry at the given block's state
// (the latest when absent).
func (api *IgneumAPI) BondOf(key common.Hash, number *rpc.BlockNumber) (*BondInfo, error) {
	a := api.engine.Authority()
	if a == nil {
		return nil, errors.New("this chain carries no authority rule")
	}
	st, err := api.stateAt(number)
	if err != nil {
		return nil, err
	}
	b := a.BondOf(st, key)
	return &BondInfo{Key: key, Amount: (*hexutil.Big)(b.Amount), Account: b.Account, Bonded: b.Amount.Sign() > 0, Signer: a.SignerOf(st, key)}, nil
}

func (api *IgneumAPI) stateAt(number *rpc.BlockNumber) (*state.StateDB, error) {
	sr, ok := api.chain.(stateReader)
	if !ok {
		return nil, errors.New("no state access on this chain reader")
	}
	var header *types.Header
	if number == nil || *number == rpc.LatestBlockNumber || *number == rpc.PendingBlockNumber {
		header = api.chain.CurrentHeader()
	} else {
		header = api.chain.GetHeaderByNumber(uint64(number.Int64()))
	}
	if header == nil {
		return nil, errors.New("unknown block")
	}
	return sr.StateAt(header.Root)
}

// VerifyResult is igneum_verifyBlock's answer.
type VerifyResult struct {
	Valid  bool        `json:"valid"`
	Stage  string      `json:"stage,omitempty"`
	Reason string      `json:"reason,omitempty"`
	Hash   common.Hash `json:"hash"`
	Number uint64      `json:"number"`
}

// VerifyBlock is the dry run for the rejection suite: an RLP block is
// verified as an import would verify it (the header with its seal, then the
// authority rule at the parent's state) and the verdict is returned without
// any import. The block's parent must be known to this node.
func (api *IgneumAPI) VerifyBlock(blockRLP hexutil.Bytes) (*VerifyResult, error) {
	var block types.Block
	if err := rlp.DecodeBytes(blockRLP, &block); err != nil {
		return &VerifyResult{Valid: false, Stage: "decode", Reason: err.Error()}, nil
	}
	res := &VerifyResult{Hash: block.Hash(), Number: block.NumberU64()}
	header := block.Header()
	parent := api.chain.GetHeader(header.ParentHash, header.Number.Uint64()-1)
	if parent == nil {
		res.Stage, res.Reason = "parent", "unknown parent"
		return res, nil
	}
	if err := api.engine.VerifyHeader(api.chain, header, true); err != nil {
		res.Stage, res.Reason = "header", err.Error()
		return res, nil
	}
	// The body's commitment and the proof pi under --igneum.pi-verify
	// (pi.go), as the body validator runs them on import.
	if header.PiHash != nil {
		if len(block.Pi()) == 0 {
			res.Stage, res.Reason = "body", "missing proof in block body"
			return res, nil
		}
		if h := types.PiHash(block.Pi()); h != *header.PiHash {
			res.Stage, res.Reason = "body", fmt.Sprintf("proof commitment mismatch (header value %x, calculated %x)", *header.PiHash, h)
			return res, nil
		}
	} else if len(block.Pi()) > 0 {
		res.Stage, res.Reason = "body", "proof present in block body without a commitment"
		return res, nil
	}
	if err := api.engine.VerifyPi(&block); err != nil {
		res.Stage, res.Reason = "pi", err.Error()
		return res, nil
	}
	sr, ok := api.chain.(stateReader)
	if !ok {
		res.Stage, res.Reason = "state", "no state access on this chain reader"
		return res, nil
	}
	st, err := sr.StateAt(parent.Root)
	if err != nil {
		res.Stage, res.Reason = "state", err.Error()
		return res, nil
	}
	if err := api.engine.VerifyAuthority(header, st); err != nil {
		res.Stage, res.Reason = "authority", err.Error()
		return res, nil
	}
	res.Valid = true
	return res, nil
}

// SubmitEndorsements takes a pushed list of endorsements for (height, block
// hash) into this node's pool (the settlement rule's lab transport,
// endorse.go): the entries hex-encoded, 100 bytes each; returns the count
// taken.
func (api *IgneumAPI) SubmitEndorsements(height hexutil.Uint64, blockHash common.Hash, entries hexutil.Bytes) (int, error) {
	return api.engine.SubmitEndorsements(uint64(height), blockHash, entries)
}

// Endorsements serves the pool's list for a height and block hash.
func (api *IgneumAPI) Endorsements(height hexutil.Uint64, blockHash common.Hash) hexutil.Bytes {
	return api.engine.EndorsementsFor(uint64(height), blockHash)
}

// Equivocations is the endorsement equivocation evidence this node holds
// (one unit over two blocks at one height; the penalty entry's input).
func (api *IgneumAPI) Equivocations() []EquivocationEvidence {
	return api.engine.Equivocations()
}

// ShareSubmitResult is igneum_submitShare's answer.
type ShareSubmitResult struct {
	Accepted bool   `json:"accepted"`
	Reason   string `json:"reason,omitempty"`
}

// SubmitShare takes a worker's share for the share lane (mineshares.go):
// address the payout address, extraNonce and nonce the worker's, mixDigest
// the share's mix, height the template's share height. Verified as import
// verifies it; pooled by its parent when accepted.
// The optional parentHash names the parent the worker mined on (igneum_getWork's
// shareParent): a share whose parent is not this node's canonical block at its
// height is refused as "stale parent" before any work check, so honest work on
// a sibling never reads "invalid" (the project's rule of 23:5x UK, 11 October 2026).
// The optional step is PA-53's declared target step (igneum_getWork's
// shareStepMax bounds it; absent reads 0): the share was mined at the
// share target over 2^step and weighs 2^step; a step over the bound is
// refused by name.
func (api *IgneumAPI) SubmitShare(address common.Address, extraNonce, nonce hexutil.Uint64, mixDigest common.Hash, height hexutil.Uint64, parentHash *common.Hash, step *hexutil.Uint64) ShareSubmitResult {
	var st uint8
	if step != nil {
		if uint64(*step) > MaxShareStep {
			return ShareSubmitResult{Accepted: false, Reason: errShareStep.Error()}
		}
		st = uint8(*step)
	}
	ok, reason := api.engine.SubmitShareAt(api.chain, address, uint64(extraNonce), uint64(nonce), mixDigest, uint64(height), parentHash, st)
	return ShareSubmitResult{Accepted: ok, Reason: reason}
}

// EndorsingUnits is igneum_endorsingUnits(key, height): the units of a bonded
// key whose endorsement lot hits at height over the table at the block's
// state (the harness's ok control for the endorsement classes; security's
// ask of 15:3x UK, 11 October 2026). Nil when the key holds no unit.
func (api *IgneumAPI) EndorsingUnits(key common.Hash, height hexutil.Uint64) ([]uint64, error) {
	h := uint64(height)
	n := rpc.BlockNumber(h)
	st, err := api.stateAt(&n)
	if err != nil {
		return nil, err
	}
	s := api.engine.config.Parts.Sortition
	if s == nil || !api.engine.endorseOn(h) {
		return nil, errors.New("igneum: endorsements are not in force at that height")
	}
	table, err := api.engine.tableAt(st, s.EpochAt(h))
	if err != nil {
		return nil, err
	}
	units := api.engine.unitsAt(st, table, key)
	var hits []uint64
	for j := uint64(0); j < units; j++ {
		if EndorseHits(table.seed, key, j, h, s.EndorseExpected) {
			hits = append(hits, j)
		}
	}
	return hits, nil
}

// ShareStats is the share pool's counters (the outside review's row 6: accepted,
// carried, expired, refused, pooled).
func (api *IgneumAPI) ShareStats() ShareStatsWithGossip {
	out := ShareStatsWithGossip{ShareStats: api.engine.ShareStatsNow(), GossipIndex: api.engine.GossipIndexSize()}
	if g := api.engine.gossipStats(); g != nil {
		out.Gossip = g
	}
	return out
}

// ShareStatsWithGossip is igneum_shareStats' answer: the pool's counters
// and, with the share gossip (PA-46), the protocol's (the peers on it, the
// shares received from peers and pooled, the announcements forwarded, the
// fetches served, the received shares dropped by reason) and the size of
// the index that answers peers' fetches.
type ShareStatsWithGossip struct {
	ShareStats
	Gossip      *ishr.Stats `json:"gossip,omitempty"`
	GossipIndex int         `json:"gossipIndex"`
}

// SetWithhold arms (n > 0) or disarms (n == 0) the withholding sealer (the
// lab build's research instrument, labmode.go), returning the number of
// blocks a disarm released. Refused off the lab build. Security drives the
// guard's branch races with it: arm N on two sealers, let each seal N
// blocks privately, which reveal at once; disarm between races.
func (api *IgneumAPI) SetWithhold(n hexutil.Uint64) (int, error) {
	released, err := api.engine.SetWithhold(uint64(n))
	if err != nil {
		return 0, err
	}
	return len(released), nil
}

// ShareCredit is an address's carry-forward balance in state at the head
// (the node team's ShareCredit).
func (api *IgneumAPI) ShareCredit(address common.Address) (uint64, error) {
	sr, ok := api.chain.(stateReader)
	if !ok {
		return 0, errors.New("no state access on this chain reader")
	}
	head := api.chain.CurrentHeader()
	if head == nil {
		return 0, errors.New("no head")
	}
	st, err := sr.StateAt(head.Root)
	if err != nil {
		return 0, err
	}
	return api.engine.ShareCredit(st, address)
}

// ShareAccount is igneum_shareAccount: an address's share ledger on this
// node (accepted, carried, pooled, expired by a reorg, expired at the
// window's end, each by height, and the refusals by name), rebuilt from
// the share journal at a restart, so accepted = carried + pooled +
// expiredReorg + expiredWindow closes without subtraction (G-23).
func (api *IgneumAPI) ShareAccount(address common.Address) ShareAccount {
	return api.engine.ShareAccountOf(address)
}

// EligibleAt is igneum_eligibleAt's answer: this node's bond key's draw at
// a height, read at the parent's state exactly as the sealer reads it
// (prepareSortition): the round (the unbonded round when none below the
// liveness round), whether it is the open seat's, the key's lot rank among
// the keys eligible at the round (PA-52; 0 the leader) and the sealer's
// search start (PA-48's floor with the stagger), in the header's seconds.
type EligibleAt struct {
	Height uint64      `json:"height"`
	Key    common.Hash `json:"key"`
	Round  uint64      `json:"round"`
	Open   bool        `json:"open"`
	Rank   int         `json:"rank"`
	Floor  uint64      `json:"floor"`
}

// EligibleAt is igneum_eligibleAt(height, key): the draw above for the
// head's child when height is nil, for this node's bond key when key is
// nil (the wallet-security seat's ask of 12 October 2026: any bonded key's
// draw, read from the parent's state). An error names a parent the node
// does not hold, a parent state it does not hold, or a node without a
// bond key when none is given.
func (api *IgneumAPI) EligibleAt(height *hexutil.Uint64, keyIn *common.Hash) (*EligibleAt, error) {
	key := keyIn
	if key == nil {
		key = api.engine.BondKey()
	}
	if key == nil {
		return nil, errors.New("no bond key on this node and none given")
	}
	head := api.chain.CurrentHeader()
	if head == nil {
		return nil, errors.New("no head")
	}
	h := head.Number.Uint64() + 1
	if height != nil {
		h = uint64(*height)
	}
	if h == 0 {
		return nil, errors.New("no draw at the genesis")
	}
	parent := api.chain.GetHeaderByNumber(h - 1)
	if parent == nil {
		return nil, fmt.Errorf("no parent at %d on this node", h-1)
	}
	r, open, st, err := api.engine.roundFor(api.chain, parent, h, *key)
	if err != nil {
		return nil, fmt.Errorf("the parent's state at %d: %w", h-1, err)
	}
	rank := api.engine.staggerRank(st, *key, h, r)
	return &EligibleAt{Height: h, Key: *key, Round: r, Open: open, Rank: rank, Floor: api.engine.config.Parts.Sortition.sealerFloor(parent, r, rank, api.engine.SealStaggerMs())}, nil
}

// ShareLimits is igneum_shareLimits: the per-address cap, the pool's
// capacity, the ceiling a block carries and the recency, as a worker paces
// by them.
func (api *IgneumAPI) ShareLimits() ShareLimits {
	return api.engine.ShareLimitsNow()
}

// SealEquivocations is the seal equivocation evidence this node holds (two
// different seals under one key at one height, the second kept out of this
// node's chain and relay; sealledger.go), each with the registry's 0x10
// payload ready to post.
func (api *IgneumAPI) SealEquivocations() []SealEquivocation {
	return api.engine.SealEquivocations()
}

// BlockRLP returns a canonical block's RLP (the rejection suite's raw
// material: mutate, re-verify).
func (api *IgneumAPI) BlockRLP(number rpc.BlockNumber) (hexutil.Bytes, error) {
	br, ok := api.chain.(interface {
		GetBlockByNumber(uint64) *types.Block
	})
	if !ok {
		return nil, errors.New("no block access on this chain reader")
	}
	var n uint64
	if number == rpc.LatestBlockNumber || number == rpc.PendingBlockNumber {
		n = api.chain.CurrentHeader().Number.Uint64()
	} else {
		n = uint64(number.Int64())
	}
	block := br.GetBlockByNumber(n)
	if block == nil {
		return nil, errors.New("unknown block")
	}
	return rlp.EncodeToBytes(block)
}

// ShareJournal is igneum_shareJournal(address, from, to): this node's
// share journal read as records (accepted with the parent, mix and step;
// carried with the share's height; uncarried; expired with the reason),
// for one address (the zero address: every address) and a height range
// (to 0: open). the node team's read with the uncarried record (the project's
// split of 00:2x UK, 12 October 2026).
func (api *IgneumAPI) ShareJournal(address common.Address, from, to hexutil.Uint64) ([]ShareJournalRecord, error) {
	return api.engine.ShareJournal(address, uint64(from), uint64(to))
}

// Sibling is one block this node holds at a height, for igneum_siblings.
type Sibling struct {
	Hash             common.Hash  `json:"hash"`
	Canonical        bool         `json:"canonical"`
	Parent           common.Hash  `json:"parentHash"`
	Time             uint64       `json:"timestamp"`
	Round            uint64       `json:"round"`
	BondKey          *common.Hash `json:"bondKey,omitempty"`
	EndorsementsHeld uint64       `json:"endorsementsHeld"`
	FirstSeen        bool         `json:"firstSeen"`
}

// Siblings is igneum_siblings(height): every block this node holds at the
// height (canonical or not; rawdb's hashes at the number) with the pool's
// endorsements held for each, so a race's two twins are read together
// (the design seat's measure (c) of 04:3x UK, 12 October 2026: the
// winner's carried count plus the loser's entries in the pools).
func (api *IgneumAPI) Siblings(height hexutil.Uint64) ([]Sibling, error) {
	hr, ok := api.chain.(interface{ HashesAt(uint64) []common.Hash })
	if !ok {
		return nil, errors.New("no block-hash enumeration on this chain reader")
	}
	n := uint64(height)
	var canonical common.Hash
	if c := api.chain.GetHeaderByNumber(n); c != nil {
		canonical = c.Hash()
	}
	api.engine.endorse.mu.Lock()
	first, hasFirst := api.engine.endorse.firstSeenAt(n)
	api.engine.endorse.mu.Unlock()
	out := []Sibling{}
	for _, h := range hr.HashesAt(n) {
		hd := api.chain.GetHeader(h, n)
		if hd == nil {
			continue
		}
		out = append(out, Sibling{Hash: h, Canonical: h == canonical, Parent: hd.ParentHash, Time: hd.Time, Round: hd.Round, BondKey: hd.BondKey, EndorsementsHeld: api.engine.endorsementsHeld(hd), FirstSeen: hasFirst && first == h})
	}
	return out, nil
}

// SideBlock is igneum_sideBlock(hash)'s answer: a block this node holds
// by hash, canonical or not, with what the fork rule and the share lane
// read of it.
type SideBlock struct {
	Hash       common.Hash  `json:"hash"`
	Number     uint64       `json:"number"`
	Parent     common.Hash  `json:"parentHash"`
	Time       uint64       `json:"timestamp"`
	Difficulty *hexutil.Big `json:"difficulty"`
	Round      uint64       `json:"round"`
	BondKey    *common.Hash `json:"bondKey,omitempty"`
	// Endorsed is the header's count of the PARENT's endorsements it
	// carries; EndorsementsHeld the pool's count of endorsements OF this
	// block (what its child could carry); FirstSeen whether this block is
	// the first valid one this node saw at its height (the endorser's
	// target, F-44 part 1).
	Endorsed         uint64 `json:"endorsed"`
	EndorsementsHeld uint64 `json:"endorsementsHeld"`
	FirstSeen        bool   `json:"firstSeen"`
	Canonical        bool   `json:"canonical"`
	// CanonicalAtHeight is the hash the chain holds at this height.
	CanonicalAtHeight common.Hash `json:"canonicalAtHeight"`
	// MinedShares counts the carried shares in the body (when the node
	// holds the body).
	MinedShares *uint64 `json:"minedShares,omitempty"`
}

// SideBlock is igneum_sideBlock(hash): the block by hash (the project's order of
// 00:4x UK, 12 October 2026, for the race reads: a side block's time,
// round, key, the endorsements it carries and the ones held for it).
func (api *IgneumAPI) SideBlock(hash common.Hash) (*SideBlock, error) {
	h := api.chain.GetHeaderByHash(hash)
	if h == nil {
		return nil, errors.New("unknown block")
	}
	n := h.Number.Uint64()
	out := &SideBlock{Hash: hash, Number: n, Parent: h.ParentHash, Time: h.Time, Difficulty: (*hexutil.Big)(h.Difficulty), Round: h.Round, BondKey: h.BondKey, Endorsed: h.Endorsed}
	if c := api.chain.GetHeaderByNumber(n); c != nil {
		out.CanonicalAtHeight = c.Hash()
		out.Canonical = c.Hash() == hash
	}
	out.EndorsementsHeld = api.engine.endorsementsHeld(h)
	api.engine.endorse.mu.Lock()
	first, ok := api.engine.endorse.firstSeenAt(n)
	api.engine.endorse.mu.Unlock()
	out.FirstSeen = ok && first == hash
	if br, ok := api.chain.(interface {
		GetBlock(common.Hash, uint64) *types.Block
	}); ok {
		if b := br.GetBlock(hash, n); b != nil {
			if list, ok := types.ParseMinedSharesForm(b.MinedShares(), api.engine.shareFormAt(b.NumberU64())); ok {
				c := uint64(len(list))
				out.MinedShares = &c
			}
		}
	}
	return out, nil
}
