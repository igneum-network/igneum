package igneum

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"lukechampine.com/blake3"
)

// The settlement rule by endorsements (GUARD-C-CLOSURE.md 4.6 candidate 2,
// RULED the launch rule; DEVNET6-RULES.md section 2; the project's order of 11:2x
// UK, 11 October 2026), behind the sortition parameter endorseExpected
// (E_att; 0 = off) for the consensus half and the flag --igneum.endorse for
// this node's own endorsing. PROTOTYPE on the sixth development chain.
//
//   - the lot: unit j of key k is an ENDORSER at height h iff
//     BLAKE3(tag("IgneumHash/R0A/endorse") || V(e) || k || LE64(j) || LE64(h))
//     < 2^256 x E_att / 65,536, over epoch e's table (the F-32 table) at the
//     state of block h itself (the state the validator of h + 1 holds as its
//     parent's); the open seat holds no unit and endorses nothing;
//   - the endorsement: the key's registered Ed25519 signer signs
//     M = tag("IgneumHash/R0A/endorse-msg") || u64be(chain id) || LE64(h) || blockHash(h)
//     for the block it holds at h when it first sees one (publish first);
//     the entry is k (32) || LE32(j) || sig (64), 100 bytes;
//   - the carriage: block h + 1 carries the entries it holds for its parent
//     as the body's list, committed by the header's EndorsementsHash and
//     counted by Endorsed, both inside the seal hash; at most MaxEndorsements;
//   - the validation on import of h + 1, at the parent's state: every entry
//     well-formed, distinct by (k, j), its key bonded with j under its units,
//     its lot a hit at h, its signature valid over M with this block's parent
//     hash; a failing entry makes the block invalid (the sealer vouches for
//     what it carries); the count equals the header's;
//   - the weight: BlockWeight(header) = (D >> r) + Endorsed x D, one
//     endorsement weighing one round-0 block at the block's own difficulty
//     (the design's "2^-r plus the count" in the node's units; the block's
//     own D stands for its parent's, which it equals or nearly equals);
//   - the equivocation (one unit endorsing two blocks at one height): the
//     evidence is recorded (igneum_equivocations) and handed to the penalty
//     entry when it is built (protocol-a/penalties), NOT BUILT here;
//   - the transport on the lab chain: this node's endorsements go to a pool
//     and are PUSHED to the sealers it is configured with
//     (--igneum.endorse-to; igneum_submitEndorsements); a sealer's template
//     carries what reached it by its recommit. The launch form is a p2p
//     message (stated; not built).

var (
	endorseLotDomain = tagBytes("IgneumHash/R0A/endorse")
	endorseMsgDomain = tagBytes("IgneumHash/R0A/endorse-msg")

	errEndorseMalformed  = errors.New("igneum endorse: malformed entry list")
	errEndorseDuplicate  = errors.New("igneum endorse: a unit endorses twice in one block")
	errEndorseNotBonded  = errors.New("igneum endorse: the key holds no such unit")
	errEndorseNotDrawn   = errors.New("igneum endorse: the unit's lot does not hit at the parent's height")
	errEndorseBadSig     = errors.New("igneum endorse: the signature does not verify under the key's signer")
	errEndorseCount      = errors.New("igneum endorse: the header's count is not the list's")
	errEndorseOff        = errors.New("igneum endorse: endorsements on a chain without the rule")
	errEndorseOverBound  = errors.New("igneum endorse: more entries than the bound")
	errEndorseNoChainID  = errors.New("igneum endorse: the chain id is not known yet")
	errEndorseNoSigner   = errors.New("igneum endorse: this node has no signer or no bond key")
	errEndorseWrongHash  = errors.New("igneum endorse: the entry's signature is not over this block")
	errEndorseWrongHeigh = errors.New("igneum endorse: height mismatch")
)

func tagBytes(t string) []byte { return append([]byte{byte(len(t))}, t...) }

// EndorseLot is the endorsement lot of unit j of key k at height h under
// seed V(e).
func EndorseLot(seed, key common.Hash, unit, height uint64) common.Hash {
	var buf [8]byte
	hs := blake3.New(32, nil)
	hs.Write(endorseLotDomain)
	hs.Write(seed[:])
	hs.Write(key[:])
	binary.LittleEndian.PutUint64(buf[:], unit)
	hs.Write(buf[:])
	binary.LittleEndian.PutUint64(buf[:], height)
	hs.Write(buf[:])
	var out common.Hash
	hs.Sum(out[:0])
	return out
}

// EndorseThreshold is 2^256 x E_att / 65,536 (2^256 when E_att is 65,536).
func EndorseThreshold(expected uint64) *big.Int {
	t := new(big.Int).Lsh(big.NewInt(1), 256)
	t.Mul(t, new(big.Int).SetUint64(expected))
	return t.Rsh(t, 16)
}

// EndorseHits reports whether unit j of key k endorses at h.
func EndorseHits(seed, key common.Hash, unit, height, expected uint64) bool {
	x := EndorseLot(seed, key, unit, height)
	return new(big.Int).SetBytes(x[:]).Cmp(EndorseThreshold(expected)) < 0
}

// EndorseMessage is M for a block at height h with hash blockHash on chain
// chainID.
func EndorseMessage(chainID uint64, height uint64, blockHash common.Hash) []byte {
	var b8 [8]byte
	m := make([]byte, 0, len(endorseMsgDomain)+16+32)
	m = append(m, endorseMsgDomain...)
	binary.BigEndian.PutUint64(b8[:], chainID)
	m = append(m, b8[:]...)
	binary.LittleEndian.PutUint64(b8[:], height)
	m = append(m, b8[:]...)
	return append(m, blockHash[:]...)
}

// Endorsement is one decoded entry.
type Endorsement struct {
	Key  common.Hash
	Unit uint32
	Sig  []byte
}

// Bytes is the entry's wire form, key || LE32(unit) || sig.
func (en *Endorsement) Bytes() []byte {
	out := make([]byte, 0, types.EndorsementBytes)
	out = append(out, en.Key[:]...)
	var b4 [4]byte
	binary.LittleEndian.PutUint32(b4[:], en.Unit)
	out = append(out, b4[:]...)
	return append(out, en.Sig...)
}

// ParseEndorsements decodes a concatenated list (an error on a length that
// is not whole entries).
func ParseEndorsements(entries []byte) ([]Endorsement, error) {
	n := types.EndorsementCount(entries)
	if n < 0 {
		return nil, errEndorseMalformed
	}
	out := make([]Endorsement, n)
	for i := 0; i < n; i++ {
		e := entries[i*types.EndorsementBytes : (i+1)*types.EndorsementBytes]
		copy(out[i].Key[:], e[:32])
		out[i].Unit = binary.LittleEndian.Uint32(e[32:36])
		out[i].Sig = append([]byte{}, e[36:]...)
	}
	return out, nil
}

// endorseState is the engine's endorsement state: the chain id, the pool
// of entries by height and block hash, the equivocation evidence, the push
// targets and the endorser loop.
type endorseState struct {
	mu      sync.Mutex
	chainID uint64
	pool    map[uint64]map[common.Hash]map[endorseUnit][]byte // height -> block hash -> (key, unit) -> entry bytes
	seen    map[uint64]map[endorseUnit]common.Hash            // height -> (key, unit) -> the first block hash endorsed
	equiv   []EquivocationEvidence
	pushTo  []string
	// announcer is the share mesh's endorsement gossip (eth/protocols/ishr,
	// ISHR3): the endorser's own list for a block, handed beside the push
	// so every sealer's pool holds every endorser's set, an open-seat
	// sealer on no push list among them (the ninth development chain's
	// third carry class, 12 October 2026). Nil without the protocol.
	announcer func(height uint64, blockHash common.Hash, entries []byte)
	on        bool
	lastHead  common.Hash
	// lastHeight is the last height this node endorsed at: never a second
	// endorsement at one height (PA-47; the fork choice's tie-break moves
	// the head between blocks at one height).
	lastHeight uint64
	// firstSeen is the FIRST valid block this node imported at each height
	// (canonical or side; the backend's head and side events): the block
	// the endorser endorses at that height (F-44, the design seat's ruling
	// of 01:3x UK, 12 October 2026, on devnet8's depth-2 races: the rule
	// as written endorsed the HEAD at the tick, so a withheld block
	// revealed before the tick flipped the head and harvested the honest
	// endorsements the public block had earned; now the endorsement goes
	// to the first valid block seen, whatever the head does after; the
	// once-per-height guard above is kept and there is no re-endorsement
	// clause). Bounded to the last firstSeenKeep heights.
	firstSeen map[uint64]common.Hash
	// arrivedAt is each live-announced block's local arrival (NoteArrival;
	// policy part 4's record), bounded with firstSeen.
	arrivedAt map[common.Hash]arrival
	// The arrival record's warm-up and the node's own blocks (F-51).
	arrivalLast   uint64
	arrivalStreak uint64
	ownBlocks     map[common.Hash]uint64
	quit          chan struct{}
}

// firstSeenKeep bounds the first-seen record (heights kept behind the
// newest one noted).
const firstSeenKeep = 128

// NoteSeen records the first valid block imported at a height (the backend
// calls it on every head and side event, in import order).
func (e *Engine) NoteSeen(header *types.Header) {
	if header == nil || header.Number == nil {
		return
	}
	h := header.Number.Uint64()
	es := &e.endorse
	es.mu.Lock()
	defer es.mu.Unlock()
	if es.firstSeen == nil {
		es.firstSeen = make(map[uint64]common.Hash)
	}
	if _, ok := es.firstSeen[h]; ok {
		return
	}
	es.firstSeen[h] = header.Hash()
	for k := range es.firstSeen {
		if k+firstSeenKeep < h {
			delete(es.firstSeen, k)
		}
	}
}

// firstSeenAt is the first valid block seen at a height, or false.
func (es *endorseState) firstSeenAt(height uint64) (common.Hash, bool) {
	h, ok := es.firstSeen[height]
	return h, ok
}

// FirstSeenAt is firstSeenAt under the lock: the block this node recorded
// first at a height, the one its endorser endorsed (policy part 5's read
// in the sealer's parent choice, miner/worker.go prepareWork).
func (e *Engine) FirstSeenAt(height uint64) (common.Hash, bool) {
	e.endorse.mu.Lock()
	defer e.endorse.mu.Unlock()
	return e.endorse.firstSeenAt(height)
}

// NoteArrival records a block's arrival on the LIVE announcement path (the
// backend's call for a head event one height above the last head and for
// a side event, never for a sync's segment or a branch received whole when
// a partition heals: policy part 4's bound 1, the design seat's ruling of
// 02:4x UK, 12 October 2026, so a minority side's sealers never walk back
// to the fork point). The record is read by PastDrifted. Bounded with the
// first-seen record.
func (e *Engine) NoteArrival(header *types.Header) {
	if header == nil || header.Number == nil {
		return
	}
	es := &e.endorse
	es.mu.Lock()
	defer es.mu.Unlock()
	h := header.Number.Uint64()
	// The record warms up (F-51, 12 October 2026): a node's own lateness
	// (its first blocks after a restart, a sync's tail, the minutes of a
	// dataset context build) made honest heads read past-drifted on the
	// ninth development chain (pod d at 1021 three minutes after its
	// restart, pod e2 at 1024 and 1025 inside its build), so an arrival
	// counts only after arrivalWarmup consecutive heights reached this
	// node one by one; a gap in the heights (a segment) starts the count
	// again.
	switch {
	case es.arrivalLast != 0 && h == es.arrivalLast+1:
		es.arrivalStreak++
	case es.arrivalLast != 0 && h <= es.arrivalLast:
		// a twin at a held height, or a reorganisation back to one, is
		// the live path still: the count stands
	default:
		es.arrivalStreak = 1
	}
	if h > es.arrivalLast {
		es.arrivalLast = h
	}
	if es.arrivalStreak < arrivalWarmup {
		return
	}
	if es.arrivedAt == nil {
		es.arrivedAt = make(map[common.Hash]arrival)
	}
	if _, ok := es.arrivedAt[header.Hash()]; ok {
		return
	}
	es.arrivedAt[header.Hash()] = arrival{height: h, at: uint64(time.Now().Unix())}
	for k, a := range es.arrivedAt {
		if a.height+firstSeenKeep < h {
			delete(es.arrivedAt, k)
		}
	}
}

// arrivalWarmup is how many consecutive live heads a node imports one by
// one before its arrival records count (F-51).
const arrivalWarmup = 8

// NoteOwnBlock records a block this node sealed: its arrival is its own
// seal, its stamp the template's assembly, the gap between them the
// node's own search and proof, so it is never past-drifted here (F-51: on
// the scratch pair two sealers refused their own slow-searched heads,
// walked back to a pair each had sealed and stood for good). The worker's
// call before the block's insert.
func (e *Engine) NoteOwnBlock(hash common.Hash, height uint64) {
	es := &e.endorse
	es.mu.Lock()
	defer es.mu.Unlock()
	if es.ownBlocks == nil {
		es.ownBlocks = make(map[common.Hash]uint64)
	}
	es.ownBlocks[hash] = height
	for k, h := range es.ownBlocks {
		if h+firstSeenKeep < height {
			delete(es.ownBlocks, k)
		}
	}
}

// arrival is a block's local arrival on the live path.
type arrival struct {
	height uint64
	at     uint64
}

// PastDrifted says whether a block's stamp is older than its local arrival
// by more than one round (policy part 4, the past-drift refusal, the mirror
// of the future-drift check: a sealer does not build on it and an endorser
// does not endorse it, so an honest majority denies a past-stamped branch
// its children and endorsements; F-45's cause (i)). A block without an
// arrival record (a sync, a healed partition, a node started at it) is not
// past-drifted: the refusal is policy on the live path only, never a rule
// at import. A block this node sealed itself is never past-drifted (its
// arrival is its own seal, the gap its own search; F-51), and a block whose
// stamp is within the seal window (a round) plus Delta of its arrival is
// honest however late it reached this node: an honest sealer's search may
// run the round out and its proof and the wire follow (the design seat's
// bound (i), 12 October 2026).
func (e *Engine) PastDrifted(header *types.Header) bool {
	if header == nil {
		return false
	}
	s := e.config.Parts.Sortition
	if s == nil || s.Delta == 0 {
		return false
	}
	e.endorse.mu.Lock()
	defer e.endorse.mu.Unlock()
	return e.pastDriftedLocked(header, s.DeltaAt_(header.Number.Uint64()))
}

// pastDriftedLocked is PastDrifted under the endorse lock (the tick's call
// and PastDrifted's).
func (e *Engine) pastDriftedLocked(header *types.Header, delta uint64) bool {
	if _, own := e.endorse.ownBlocks[header.Hash()]; own {
		return false
	}
	a, ok := e.endorse.arrivedAt[header.Hash()]
	if !ok {
		return false
	}
	return header.Time+2*delta < a.at
}

type endorseUnit struct {
	key  common.Hash
	unit uint32
}

// EquivocationEvidence is one unit's two endorsements at one height over
// different blocks (the penalty entry's input; recorded, not penalised here).
type EquivocationEvidence struct {
	Height uint64      `json:"height"`
	Key    common.Hash `json:"key"`
	Unit   uint32      `json:"unit"`
	First  common.Hash `json:"first"`
	Second common.Hash `json:"second"`
}

const endorsePoolKeep = 64

func (es *endorseState) setChainID(id uint64) {
	es.mu.Lock()
	defer es.mu.Unlock()
	if es.chainID == 0 {
		es.chainID = id
	}
}

// add keeps an entry for (height, block hash); a second block by one unit
// at the height is recorded as evidence and the entry kept too (the sealer
// chooses what it carries; the validator refuses nothing on it).
func (es *endorseState) add(height uint64, blockHash common.Hash, en Endorsement) {
	es.mu.Lock()
	defer es.mu.Unlock()
	if es.pool == nil {
		es.pool = make(map[uint64]map[common.Hash]map[endorseUnit][]byte)
		es.seen = make(map[uint64]map[endorseUnit]common.Hash)
	}
	u := endorseUnit{en.Key, en.Unit}
	if es.seen[height] == nil {
		es.seen[height] = make(map[endorseUnit]common.Hash)
	}
	if first, ok := es.seen[height][u]; ok && first != blockHash {
		es.equiv = append(es.equiv, EquivocationEvidence{Height: height, Key: en.Key, Unit: en.Unit, First: first, Second: blockHash})
		if len(es.equiv) > 1024 {
			es.equiv = es.equiv[len(es.equiv)-1024:]
		}
	} else if !ok {
		es.seen[height][u] = blockHash
	}
	if es.pool[height] == nil {
		es.pool[height] = make(map[common.Hash]map[endorseUnit][]byte)
	}
	if es.pool[height][blockHash] == nil {
		es.pool[height][blockHash] = make(map[endorseUnit][]byte)
	}
	es.pool[height][blockHash][u] = en.Bytes()
	// prune far below
	for h := range es.pool {
		if h+endorsePoolKeep < height {
			delete(es.pool, h)
			delete(es.seen, h)
		}
	}
}

// entriesFor is the concatenated, deterministically ordered list for a block
// (by key then unit), at most limit entries.
func (es *endorseState) entriesFor(height uint64, blockHash common.Hash, limit uint64) []byte {
	es.mu.Lock()
	defer es.mu.Unlock()
	m := es.pool[height][blockHash]
	if len(m) == 0 {
		return nil
	}
	units := make([]endorseUnit, 0, len(m))
	for u := range m {
		units = append(units, u)
	}
	sort.Slice(units, func(i, j int) bool {
		if c := bytes.Compare(units[i].key[:], units[j].key[:]); c != 0 {
			return c < 0
		}
		return units[i].unit < units[j].unit
	})
	if limit > 0 && uint64(len(units)) > limit {
		units = units[:limit]
	}
	out := make([]byte, 0, len(units)*types.EndorsementBytes)
	for _, u := range units {
		out = append(out, m[u]...)
	}
	return out
}

// Equivocations is the evidence this node holds (igneum_equivocations).
func (e *Engine) Equivocations() []EquivocationEvidence {
	e.endorse.mu.Lock()
	defer e.endorse.mu.Unlock()
	return append([]EquivocationEvidence{}, e.endorse.equiv...)
}

// EndorsementsFor serves the pool's list for a height and block
// (igneum_endorsements).
func (e *Engine) EndorsementsFor(height uint64, blockHash common.Hash) []byte {
	return e.endorse.entriesFor(height, blockHash, 0)
}

// carryQuorum is the carry's threshold for the child of a block (F-44
// part 2a): ceil(2 x E_att / 3) endorsements of the parent held in the
// pool before the sealer assembles; zero (no hold) where the chain carries
// no endorsement rule at the height.
func (e *Engine) carryQuorum(height uint64) uint64 {
	if !e.endorseOn(height) {
		return 0
	}
	return (2*e.config.Parts.Sortition.EndorseExpected + 2) / 3
}

// carryQuorumAt is the carry's threshold against the BONDED SHARE of the
// epoch's units table the node draws with (the design seat's ruling of
// 03:4x UK, 12 October 2026, on devnet9's read of 140 to 199: at a 0.75
// bonded share the honest draw is Binomial(32, 0.75) and P(X >= 22) is
// 0.846, so the absolute 22 fails structurally): ceil(2/3 x E_att x
// share), 16 on devnet9's table of 49,152 units in 65,536, 22 again when
// the share is near one; at least 1 while any unit is bonded; zero (no
// hold) where the chain carries no endorsement rule, where the table
// cannot be read, or where the table holds no units (an epoch with no
// bonded key endorses nothing, so a wait would be a wait for nothing).
func (e *Engine) carryQuorumAt(st *state.StateDB, height uint64) uint64 {
	if !e.endorseOn(height) || st == nil {
		return 0
	}
	s := e.config.Parts.Sortition
	// Below PolicyV1 the absolute form (the release before 0.9.3).
	if !s.OpenSeat || !e.policyOn(height) {
		return e.carryQuorum(height)
	}
	t, err := e.tableAt(st, s.EpochAt(height))
	if err != nil || t == nil || t.nTotal == 0 {
		return 0
	}
	n := t.nTotal
	if n > NotionalUnits {
		n = NotionalUnits
	}
	// ceil(2 x E_att x n / (3 x NotionalUnits))
	q := (2*s.EndorseExpected*n + 3*NotionalUnits - 1) / (3 * NotionalUnits)
	if q < 1 {
		q = 1
	}
	return q
}

// carryWindow is the number of canonical ancestors whose carried
// endorsements (the headers' Endorsed) set the carry's quorum.
const carryWindow = 64

// carrySupplyQuorum is the carry's threshold scaled to the chain's OWN
// supply of endorsements (the security review's form of 20:46 UK, 16 October 2026,
// the review lead's ruling, on the public test network's sealers waiting to the
// carry's bound on every height since genesis with held 0 of 16: no node
// there endorses): need = min(carryQuorumAt, floor(2/3 x the mean Endorsed
// carried by the last carryWindow canonical blocks)), read from the headers
// every node holds the same, never from the local pool (a pool-read skip is
// a lever: one adversarial endorsement a window keeps every honest sealer
// waiting, and a sealer eclipsed from the gossip would skip ahead of the
// ones that wait). No supply, no wait; a growing supply raises the quorum
// with no cliff at the first endorser (the floor: a mean under 1.5 carried
// a block asks nothing); a withholder lowering the supply lowers the quorum
// and the honest child still harvests what the bound's wait allows; the
// weight contest (BlockWeight, carried against carried) is unchanged. The
// chain reader absent, the quorum stands as before.
func (e *Engine) carrySupplyQuorum(chain consensus.ChainHeaderReader, st *state.StateDB, parent *types.Header, height uint64) uint64 {
	need := e.carryQuorumAt(st, height)
	if need == 0 || chain == nil || parent == nil {
		return need
	}
	var sum, n uint64
	for h := parent; h != nil && n < carryWindow; {
		sum += h.Endorsed
		n++
		if h.Number.Uint64() == 0 {
			break
		}
		h = chain.GetHeader(h.ParentHash, h.Number.Uint64()-1)
	}
	if n == 0 {
		return need
	}
	if supply := (2 * sum) / (3 * n); supply < need {
		return supply
	}
	return need
}

// endorsementsHeld is the pool's count of entries for a block (the
// parent of the template being assembled).
func (e *Engine) endorsementsHeld(block *types.Header) uint64 {
	if block == nil {
		return 0
	}
	return uint64(len(e.endorse.entriesFor(block.Number.Uint64(), block.Hash(), 0)) / types.EndorsementBytes)
}

// endorseOn reports whether the chain carries the rule at a height.
func (e *Engine) endorseOn(height uint64) bool {
	s := e.config.Parts.Sortition
	return s != nil && s.OpenSeat && s.EndorseExpected > 0 && s.Active(height)
}

// SubmitEndorsements takes a pushed list for (height, block hash): each
// entry's signature is checked over M before it enters the pool (the lot and
// the bond are checked at the template and on import against the state).
func (e *Engine) SubmitEndorsements(height uint64, blockHash common.Hash, entries []byte) (int, error) {
	if !e.endorseOn(height) {
		return 0, errEndorseOff
	}
	chainID := e.endorse.chainIDValue()
	if chainID == 0 {
		return 0, errEndorseNoChainID
	}
	list, err := ParseEndorsements(entries)
	if err != nil {
		return 0, err
	}
	if uint64(len(list)) > e.config.Parts.Sortition.MaxEndorsements {
		return 0, errEndorseOverBound
	}
	msg := EndorseMessage(chainID, height, blockHash)
	n := 0
	for _, en := range list {
		// The signer is the key's registered one, which needs state; the pool
		// takes the entry on the key's own bytes as signer only when it is
		// an Ed25519 key (the open-seat form), else defers to the template's
		// state check. Here: accept and let the template check decide.
		if len(en.Sig) != ed25519.SignatureSize {
			continue
		}
		_ = msg
		e.endorse.add(height, blockHash, en)
		n++
	}
	return n, nil
}

func (es *endorseState) chainIDValue() uint64 {
	es.mu.Lock()
	defer es.mu.Unlock()
	return es.chainID
}

// verifyEndorsementEntries checks a block's carried list for its parent at
// the parent's state: the rule of DEVNET6-RULES.md 2.5. Returns the count.
func (e *Engine) verifyEndorsementEntries(header *types.Header, entries []byte, st *state.StateDB) (uint64, error) {
	s := e.config.Parts.Sortition
	parentHeight := header.Number.Uint64() - 1
	if !e.endorseOn(header.Number.Uint64()) {
		if len(entries) > 0 || header.EndorsementsHash != nil || header.Endorsed != 0 {
			return 0, errEndorseOff
		}
		return 0, nil
	}
	if len(entries) == 0 {
		if header.EndorsementsHash != nil || header.Endorsed != 0 {
			return 0, errEndorseCount
		}
		return 0, nil
	}
	list, err := ParseEndorsements(entries)
	if err != nil {
		return 0, err
	}
	if uint64(len(list)) > s.MaxEndorsements {
		return 0, errEndorseOverBound
	}
	if header.EndorsementsHash == nil || types.EndorsementsHash(entries) != *header.EndorsementsHash || uint64(len(list)) != header.Endorsed {
		return 0, errEndorseCount
	}
	chainID := e.endorse.chainIDValue()
	if chainID == 0 {
		return 0, errEndorseNoChainID
	}
	// Endorsements are for the PARENT: epoch(parent)'s table at the parent's
	// state, through the memo when it holds the parent's facts
	// (authoritymemo.go, F-39; the memo's table is the CHILD's epoch, so a
	// parent on an epoch boundary reads the state).
	r := e.rulesFor(st, header.ParentHash)
	table, err := r.tableAt(s.EpochAt(parentHeight))
	if err != nil {
		return 0, err
	}
	msg := EndorseMessage(chainID, parentHeight, header.ParentHash)
	seen := make(map[endorseUnit]bool, len(list))
	for i, en := range list {
		u := endorseUnit{en.Key, en.Unit}
		if seen[u] {
			return 0, fmt.Errorf("%w: entry %d", errEndorseDuplicate, i)
		}
		seen[u] = true
		// A key marked by an equivocation (registry version 4) endorses
		// nothing from the block after its mark, whatever the written
		// table still carries for it.
		marked, err := r.marked(en.Key)
		if err != nil {
			return 0, err
		}
		if marked {
			return 0, fmt.Errorf("%w: entry %d (the key is marked)", errEndorseNotBonded, i)
		}
		units, err := r.unitsAt(table, en.Key)
		if err != nil {
			return 0, err
		}
		if units == 0 || uint64(en.Unit) >= units {
			return 0, fmt.Errorf("%w: entry %d (key %s unit %d of %d)", errEndorseNotBonded, i, en.Key.Hex()[:10], en.Unit, units)
		}
		if !EndorseHits(table.seed, en.Key, uint64(en.Unit), parentHeight, s.EndorseExpected) {
			return 0, fmt.Errorf("%w: entry %d", errEndorseNotDrawn, i)
		}
		b, err := r.bondOf(en.Key)
		if err != nil {
			return 0, err
		}
		signer := b.Signer
		if signer == (common.Hash{}) {
			signer = en.Key
		}
		if len(en.Sig) != ed25519.SignatureSize || !ed25519.Verify(ed25519.PublicKey(signer[:]), msg, en.Sig) {
			return 0, fmt.Errorf("%w: entry %d", errEndorseBadSig, i)
		}
	}
	return uint64(len(list)), nil
}

// VerifyEndorsements is the state processor's hook: the block's carried list
// for its parent at the parent's state (core.StateProcessor.Process).
func (e *Engine) VerifyEndorsements(block *types.Block, st *state.StateDB) error {
	// The lax-endorsement instrument (labmode.go, the lab build only): a
	// node that attaches an unverified list is the harness's NON-COMPLIANT
	// node on import too, so its own post-seal validation writes and
	// broadcasts the block and the OTHER nodes' import refusals are reached
	// by class (security's finding of 15:3x UK, 11 October 2026: without
	// this the sealer's own validation refused every lax block and the
	// network-side half was never exercised). Never on a compliant node.
	if e.laxEndorseEntries() != nil {
		return nil
	}
	_, err := e.verifyEndorsementEntries(block.Header(), block.Endorsements(), st)
	return err
}

// attachEndorsements puts the pool's list for the template's parent into the
// template (the header's commitment and count, the body's list), before the
// signature and the seal.
func (e *Engine) attachEndorsements(header *types.Header, block *types.Block, st *state.StateDB) (*types.Block, error) {
	if !e.endorseOn(header.Number.Uint64()) {
		return block, nil
	}
	// The lax-endorsement instrument (labmode.go, lab build only): attach the
	// armed list WITHOUT verifying its entries, so the other nodes' import
	// refusals are exercised by class (C11). The header learns the two fields
	// and the body carries the chosen bytes; the signature and seal are made
	// over them as usual, so a peer refuses at the entry check, not before.
	if lax := e.laxEndorseEntries(); len(lax) > 0 {
		h := types.CopyHeader(block.Header())
		hh := types.EndorsementsHash(lax)
		h.EndorsementsHash, h.Endorsed = &hh, uint64(types.EndorsementCount(lax))
		header.EndorsementsHash, header.Endorsed = h.EndorsementsHash, h.Endorsed
		return block.WithSeal(h).WithEndorsements(lax), nil
	}
	s := e.config.Parts.Sortition
	entries := e.endorse.entriesFor(header.Number.Uint64()-1, header.ParentHash, s.MaxEndorsements)
	if len(entries) == 0 {
		return block, nil
	}
	// Carry only what this node can verify at the parent's state: the sealer
	// vouches for every entry.
	list, _ := ParseEndorsements(entries)
	// The BLOCK's header is the one with the transaction and receipt roots
	// (types.NewBlock derives them onto its own copy, never onto the
	// caller's header): copying the caller's header here wrote zero roots
	// into every block that carried endorsements, which every peer refused
	// at the body's transaction-root check (the sixth development chain's
	// blocks from 20, 11 October 2026).
	h := types.CopyHeader(block.Header())
	keep := make([]byte, 0, len(entries))
	for _, en := range list {
		one := en.Bytes()
		trial := types.CopyHeader(header)
		hh := types.EndorsementsHash(one)
		trial.EndorsementsHash, trial.Endorsed = &hh, 1
		if _, err := e.verifyEndorsementEntries(trial, one, st); err == nil {
			keep = append(keep, one...)
		}
	}
	if len(keep) == 0 {
		return block, nil
	}
	hh := types.EndorsementsHash(keep)
	h.EndorsementsHash, h.Endorsed = &hh, uint64(types.EndorsementCount(keep))
	// the caller's header learns the two fields and nothing else of the
	// block's
	header.EndorsementsHash, header.Endorsed = h.EndorsementsHash, h.Endorsed
	return block.WithSeal(h).WithEndorsements(keep), nil
}

// StartEndorser runs this node's endorser: on every new head h it computes
// its own endorsing units at h over epoch(h)'s table at h's state, signs M
// and pushes the entries to the configured sealers (and keeps them in its
// own pool). The chain reader is the blockchain (CurrentHeader, StateAt).
func (e *Engine) StartEndorser(chain consensus.ChainHeaderReader, pushTo []string) {
	e.endorse.mu.Lock()
	e.endorse.pushTo = append([]string{}, pushTo...)
	e.endorse.on = true
	if e.endorse.quit == nil {
		e.endorse.quit = make(chan struct{})
	}
	quit := e.endorse.quit
	e.endorse.mu.Unlock()
	if id := chain.Config(); id != nil && id.GetChainID() != nil {
		e.endorse.setChainID(id.GetChainID().Uint64())
	}
	go func() {
		t := time.NewTicker(500 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-quit:
				return
			case <-t.C:
				head := chain.CurrentHeader()
				if head == nil || head.Number == nil {
					continue
				}
				e.endorse.mu.Lock()
				// The FIRST valid block seen at the head's height is the
				// one endorsed (F-44: the head at the tick harvested a
				// withholder's late reveal); the head itself when the
				// backend noted nothing at that height (a node started
				// at this head, a test).
				target := head
				if first, ok := e.endorse.firstSeenAt(head.Number.Uint64()); ok && first != head.Hash() {
					if fh := chain.GetHeader(first, head.Number.Uint64()); fh != nil {
						target = fh
					}
				}
				// Policy part 4: a block whose stamp is older than its
				// live arrival by more than one round is not endorsed
				// by this node at all (the height is left; the once-per
				// height guard holds, so no other block gets it either).
				if e.policyOn(target.Number.Uint64()+1) && e.config.Parts.Sortition != nil && e.pastDriftedLocked(target, e.config.Parts.Sortition.DeltaAt_(target.Number.Uint64())) {
					a := e.endorse.arrivedAt[target.Hash()]
					e.endorse.mu.Unlock()
					e.log.Debug("Endorsement withheld: the block's stamp is older than its arrival by more than a round", "number", target.Number, "hash", target.Hash(), "stamp", target.Time, "arrived", a.at)
					continue
				}
				same := e.endorse.lastHead == target.Hash()
				// Once a height (PA-47, the node team): the fork choice's
				// tie-break moves the head between two blocks at one
				// height, and an endorsement of the second would be this
				// node's own equivocation (two endorsements by one unit at
				// one height, the penalty of registry 0x11); the
				// endorsement goes to the block first endorsed at the
				// height and never to another block at that height; no
				// re-endorsement on a refusal or a round's passing (the
				// design seat's amendment: a reveal arrives exactly then).
				done := e.endorse.lastHeight != 0 && target.Number.Uint64() <= e.endorse.lastHeight
				if !same && !done {
					e.endorse.lastHead = target.Hash()
					e.endorse.lastHeight = target.Number.Uint64()
				}
				e.endorse.mu.Unlock()
				if same || done {
					continue
				}
				if err := e.endorseHead(chain, target); err != nil {
					e.log.Debug("Endorsement skipped", "number", target.Number, "err", err)
				}
			}
		}
	}()
}

// endorseHead makes and pushes this node's endorsements for a head.
func (e *Engine) endorseHead(chain consensus.ChainHeaderReader, head *types.Header) error {
	height := head.Number.Uint64()
	if !e.endorseOn(height + 1) {
		return nil
	}
	chainID := e.endorse.chainIDValue()
	if chainID == 0 {
		return errEndorseNoChainID
	}
	key := e.KeyedBondKey()
	e.lock.Lock()
	signer := e.signer
	e.lock.Unlock()
	if key == nil || signer == nil {
		return errEndorseNoSigner
	}
	sr, ok := chain.(stateAtReader)
	if !ok {
		return errors.New("igneum endorse: no state access")
	}
	st, err := sr.StateAt(head.Root)
	if err != nil {
		return err
	}
	s := e.config.Parts.Sortition
	table, err := e.tableAt(st, s.EpochAt(height))
	if err != nil {
		return err
	}
	units := e.unitsAt(st, table, *key)
	if units == 0 {
		return nil
	}
	msg := EndorseMessage(chainID, height, head.Hash())
	var entries []byte
	n := 0
	for j := uint64(0); j < units; j++ {
		if !EndorseHits(table.seed, *key, j, height, s.EndorseExpected) {
			continue
		}
		en := Endorsement{Key: *key, Unit: uint32(j), Sig: ed25519.Sign(signer, msg)}
		e.endorse.add(height, head.Hash(), en)
		entries = append(entries, en.Bytes()...)
		n++
	}
	if n == 0 {
		return nil
	}
	e.log.Info("Endorsed the head", "number", height, "hash", head.Hash(), "units", n, "of", units)
	e.endorse.mu.Lock()
	targets := append([]string{}, e.endorse.pushTo...)
	announce := e.endorse.announcer
	e.endorse.mu.Unlock()
	for _, url := range targets {
		go pushEndorsements(url, height, head.Hash(), entries)
	}
	if announce != nil {
		go announce(height, head.Hash(), entries)
	}
	return nil
}

// SetEndorsementAnnouncer is the share protocol's hook (ishr's
// endorsementAnnouncerSetter): the way this endorser hands its own list
// for a block to the mesh. Set once at the handler's construction.
func (e *Engine) SetEndorsementAnnouncer(fn func(height uint64, blockHash common.Hash, entries []byte)) {
	e.endorse.mu.Lock()
	e.endorse.announcer = fn
	e.endorse.mu.Unlock()
}

// pushEndorsements posts igneum_submitEndorsements to a sealer (the lab
// transport; 3 s timeout per attempt), TWICE when the first attempt fails
// or answers an error status (the design seat's ruling of 04:3x UK, 12
// October 2026, on the ninth chain's third carry class: a sealer's pool
// short of one endorser's whole set at a single height, the one-shot
// push; the gossip of endorsements is the structural form, registered
// for the release after). The second attempt follows a short pause,
// inside the tick.
func pushEndorsements(url string, height uint64, blockHash common.Hash, entries []byte) {
	body, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0", "id": 1, "method": "igneum_submitEndorsements",
		"params": []interface{}{fmt.Sprintf("0x%x", height), blockHash.Hex(), "0x" + hex.EncodeToString(entries)},
	})
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			time.Sleep(200 * time.Millisecond)
		}
		if pushEndorsementsOnce(url, body) {
			return
		}
	}
}

// pushEndorsementsOnce is one POST; true when the sealer answered 2xx.
func pushEndorsementsOnce(url string, body []byte) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return true // a malformed url is not retried
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}
