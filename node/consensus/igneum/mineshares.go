package igneum

import (
	"errors"
	"fmt"
	"math/big"
	"sort"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/consensus/igneum/shares"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rlp"
	"golang.org/x/crypto/sha3"
)

// The share lane's node side (LAUNCH-GUARDS.md 2.9, the project's order of 13:4x UK,
// 11 October 2026; the node team's shares package holds the target, the list's
// build with the carry-forward balance in state, the payout and the settle):
//
//   - a SHARE is unkeyed IgneumHash work on the twin document at height h over the
//     share header H_s = KECCAK(RLP(parentHash, h, address, extraNonce)): the
//     finder's payout address in the hashed header (design G), extraNonce the
//     worker's own u64; result <= ShareTarget(D(h)) = pow.Target(max(1, D(h)
//     / sharesPerBlock)); a share never becomes a block (2.1a);
//   - a block CARRIES the accepted shares its payout list is built from, as
//     69-byte entries in the body (types.MinedShare: address || LE64(extra)
//     || LE64(nonce) || mix || u8(k)) committed by the header's SharesRoot
//     INSIDE the seal hash; k names the share's parent among the carrying
//     block's ancestors (0 = the parent, up to 7: design G's recency 8);
//     at most maxShares entries (256, the consensus ceiling);
//   - the PAYOUT LIST is DERIVED: ShareList(parentState, h, accepted, sealer)
//     = the carried credits first, then the shares' addresses sorted with
//     weight = the share count, then the sealer's own entry LAST at
//     max(1, ceil(n / 3)) (design G's quarter); the block's withdrawals list
//     must EQUAL it byte for byte (row 2's independent verification), and a
//     block carrying shares without a list, or a list without shares when
//     the credits are empty, is invalid;
//   - the VALIDATION on import (VerifyMinedShares, the state processor at the
//     parent's state): the bound; each entry's ancestor at k within 8 and its
//     height; H_s recomputed; ONE IgneumHash hash on the twin at h; the result
//     against ShareTarget(D(h)) with D(h) the block at h's difficulty (the
//     carrying block's own when k = 0); no duplicate (address, extraNonce,
//     nonce, k); then the list against the derivation;
//   - the SEALER keeps an accepted-share pool keyed by the share's parent
//     (igneum_submitShare verifies as import does, at the parent's state and
//     the next difficulty), carries entries oldest first up to maxShares,
//     holds the rest for the next block within recency 8, counts the expired
//     (never silent: igneum_shareStats), and the template's list is the
//     derivation over what it carries;
//   - Finalize under the lane settles through SettleShares (the pro rata
//     over the whole derived list, the dust and the overflow to the carry).

var (
	errMinedSharesOff = errors.New("igneum shares: carried shares on a chain without the lane")
	errSharesBound    = errors.New("igneum shares: more carried shares than the ceiling")
	errSharesMalform  = errors.New("igneum shares: malformed entries")
	errShareAncestor  = errors.New("igneum shares: a share's parent is not an ancestor within the recency")
	errShareWork      = errors.New("igneum shares: the share's work does not verify")
	errShareDuplicate = errors.New("igneum shares: a share carried twice")
	// errShareOtherCoordinate (F-93, 13 October 2026): a share whose work
	// verifies under ANOTHER coordinate's relation is not this sealer's to
	// carry: a block carries its own coordinate's shares alone, and a
	// fresh node verifying the block by its tag refuses any other (every
	// fresh node on vectorwork1 stalled at 74 with "invalid mix digest" on
	// a block that carried a gossiped share of another coordinate).
	// Refused without the gossip peer's penalty: the share is good work,
	// another sealer's.
	errShareOtherCoordinate = errors.New("igneum shares: a share of another coordinate (good work, not this sealer's to carry)")
	// PA-53: a declared target step over the chain's bound, or any step
	// on a chain in the count form.
	errShareStep = errors.New("igneum shares: the share's declared step is over the chain's bound")
	// PA-46 (the share gossip): one share, two canonical blocks.
	errShareCarriedBefore = errors.New("igneum shares: a share an earlier block within the recency carried")
	errMinedShareList     = errors.New("igneum shares: the block's list is not the derivation over its shares")
	errShareNoList        = errors.New("igneum shares: carried shares without a payout list")
	errShareStale         = errors.New("igneum shares: the share's parent is not the head or within the recency window before it")
	errShareCap           = errors.New("igneum shares: address at cap")
	errSharePoolFull      = errors.New("igneum shares: pool full")
	// errShareAdmission is PA-53's admission bound before acceptance (the
	// design seat's ruling of 06:1x UK, 12 October 2026, the project's order of
	// 09:0x): a share from an address that already holds more unpaid
	// shares in this pool than the chain can pay it within the recency is
	// refused BY NAME at submit, never accepted to expire; the worker
	// paces by address.
	errShareAdmission = errors.New("igneum shares: refused before acceptance: the address holds more unpaid shares than the chain pays within the recency")
	// errShareLaneAtCarriage is F-50's second half (the security review's path 2,
	// the project's order of 11:1x UK, 12 October 2026): the per-address, per-
	// height and per-pool bounds left the AGGREGATE unbounded against the
	// chain's carriage, so sixteen sessions were accepted and receipted at
	// 16,500 shares in six minutes against a chain carrying its operating
	// target a block, the excess expiring named and unpaid after a receipt.
	// A share is refused by name, before any receipt, when the shares
	// outstanding across every address already reach what the chain
	// carries within the recency (sharesPerBlock x recency, the lane's
	// operating carriage; MaxShares is a block's ceiling, not its rate).
	errShareLaneAtCarriage = errors.New("igneum shares: refused before acceptance: lane at carriage")
	// errShareLaneUnpaid (the project's ruling of 10:3x UK, 15 October 2026): a
	// lane whose pay weight is 0 at the share's height takes no share, so
	// a lane retired from the pay by a registry transaction clears at the
	// recency instead of piling to the pool's bound.
	errShareLaneUnpaid  = errors.New("igneum shares: refused before acceptance: the lane pays 0 at this height")
	errShareStaleParent = errors.New("igneum shares: stale parent")
	errGraphShareOff    = errors.New("igneum shares: graph shares are not registered at this height (graphSharesFrom)")
	errGraphShareTicket = errors.New("igneum shares: a graph share carries its 168-byte ticket and a hash share none")
	errGraphShareMix    = errors.New("igneum shares: the graph share's mix is not its ticket hash")
	errGraphShareTarget = errors.New("igneum shares: the ticket hash is over the share target")
	// errShareCarriedElsewhere: a canonical block within the window already
	// carried the share (another sealer's, under the gossip); paid once.
	errShareCarriedElsewhere = errors.New("igneum shares: carried elsewhere")
	errShareNoTemplate       = errors.New("igneum shares: no template for that header hash")
)

// ShareRecency is design G's recency F: a share's parent may be the head or
// one of the seven blocks before it.
const ShareRecency = 8

// ShareHeaderHash is H_s, the share's hashed header: KECCAK(RLP(parentHash,
// height, address, extraNonce)).
func ShareHeaderHash(parent common.Hash, height uint64, address common.Address, extraNonce uint64) common.Hash {
	hasher := sha3.NewLegacyKeccak256()
	rlp.Encode(hasher, []interface{}{parent, height, address, extraNonce})
	var out common.Hash
	hasher.Sum(out[:0])
	return out
}

// shareDifficulty is max(1, D / sharesPerBlock): the difficulty the twin's
// verifier is given so that pow.Target(it) is the share target.
func (e *Engine) shareDifficulty(d *big.Int) *big.Int {
	n := e.config.Parts.Sortition.SharesPerBlock
	q := new(big.Int).Div(d, new(big.Int).SetUint64(n))
	if q.Sign() <= 0 {
		q.SetInt64(1)
	}
	return q
}

// verifyShareWork is one IgneumHash hash on the twin at height against the share
// target of difficulty d (the count form's step 0).
func (e *Engine) verifyShareWork(hs common.Hash, height uint64, nonce uint64, mix common.Hash, d *big.Int) error {
	return e.verifyShareWorkAt(hs, height, nonce, mix, d, 0)
}

// verifyShareWorkAt is verifyShareWork at a declared target step (PA-53):
// the share target over 2^step, the difficulty max(1, D / n) << step.
func (e *Engine) verifyShareWorkAt(hs common.Hash, height uint64, nonce uint64, mix common.Hash, d *big.Int, step uint8) error {
	return e.algo.Verify(hs, height, nonce, mix, e.shareDifficultyAt(d, step), false)
}

// verifyShareWorkAny is verifyShareWorkOn for a share whose coordinate the
// wire does not name (a submitted or gossiped share): this node's own
// coordinate first (the pool sends a coordinate's shares to its sealer),
// then every other coordinate's relation, the first pass accepted; a
// single-relation chain checks once. The receiving node's coordinate
// alone (7d1daf0f8) made every gossiped share from another coordinate's
// sealer fail, the peers were dropped at the gossip threshold and the
// three sealers of vectorwork1 churned peers and sealed nothing from
// height 405 (04:20Z, 13 October 2026).
func (e *Engine) verifyShareWorkAny(parentHash common.Hash, hs common.Hash, height uint64, nonce uint64, mix common.Hash, d *big.Int, step uint8, ticket []byte) error {
	m := e.vectorMenu()
	if m == nil {
		return e.verifyShareWorkOn(parentHash, hs, height, nonce, mix, d, step, nil, ticket)
	}
	own := e.Coordinate()
	err := e.verifyShareWorkOn(parentHash, hs, height, nonce, mix, d, step, &own, ticket)
	if err == nil {
		return nil
	}
	for c := 0; c < m.Count(); c++ {
		cc := uint8(c)
		if cc == own {
			continue
		}
		// F-142 (15 October 2026): only a coordinate ACTIVE at the share's
		// height is tried; a retired or not-yet-active coordinate is no
		// relation of the chain's there, and trying it built its
		// verifier context (a gigabyte a document) on every node for
		// every gossiped share, the node's memory growing with each
		// registered document (the OOM kills of 05:24 and 05:53 UK). A
		// retired coordinate's context is built only when a block of its
		// own below its retirement is imported (verifyShareWorkOn under
		// the block's tag).
		if !m.ActiveAt(cc, height) {
			continue
		}
		if e.verifyShareWorkOn(parentHash, hs, height, nonce, mix, d, step, &cc, ticket) == nil {
			// Good work under another coordinate: not pooled here (F-93),
			// and not held against the peer that sent it.
			return errShareOtherCoordinate
		}
	}
	return err
}

// verifyShareWorkOn is verifyShareWorkAt under the programme of the share's
// period on the branch of its parent (program.go): a share names the parent
// its template was assembled on, and its hash is checked under the
// programme that branch's anchor draws, so a share under another branch's
// programme is refused as wrong work rather than verified under this
// node's head.
func (e *Engine) verifyShareWorkOn(parentHash common.Hash, hs common.Hash, height uint64, nonce uint64, mix common.Hash, d *big.Int, step uint8, coord *uint8, ticket []byte) error {
	// Under the vector form the share's relation is its coordinate's: the
	// block's tag for a carried share, this node's sealing coordinate for a
	// submitted one (the pool sends a coordinate's shares to that
	// coordinate's sealer); the menu itself would answer with the first
	// coordinate's relation and no anchor (ecosystem's soak, 13 October
	// 2026: every anchored share on coordinate 1 refused "invalid mix
	// digest").
	base := e.algo
	if m := e.vectorMenu(); m != nil {
		c := e.Coordinate()
		if coord != nil {
			c = *coord
		}
		base = m.ByCoordinateAt(c, height) // the document in force at the share's height
		if base == nil {
			return errVectorCoord
		}
		// F-494 (0.10.58): a share is unkeyed work; on a keyed coordinate
		// it is hashed with the form's twin, the document the work package
		// served it under (UnkeyedWorkParamsAt).
		if tw := m.UnkeyedAlgorithmAt(c, height); tw != nil {
			base = tw
		}
	}
	// A graph coordinate (section 10 (b)): the share is a ticket under the
	// share header, verified by the coordinate's ticket verifier, its
	// ticket hash the share's mix and at or under the share target at the
	// step; a hash share on a graph coordinate, or a ticket on a hash
	// coordinate, is wrong work.
	if g, ok := base.(interface {
		VerifyTicket(common.Hash, uint64, uint64, []byte) (common.Hash, error)
	}); ok {
		if !e.shareFormAt(height).Typed {
			return errGraphShareOff
		}
		if len(ticket) != types.GraphTicketBytes {
			return errGraphShareTicket
		}
		th, err := g.VerifyTicket(hs, height, nonce, ticket)
		if err != nil {
			return err
		}
		if th != mix {
			return errGraphShareMix
		}
		target := pow.Target(e.shareDifficultyAt(d, step))
		if target == nil || new(big.Int).SetBytes(th[:]).Cmp(target) > 0 {
			return errGraphShareTarget
		}
		return nil
	}
	if len(ticket) > 0 {
		return errGraphShareTicket
	}
	algo, err := e.algoViewOf(base, parentHash, height)
	if err != nil {
		return err
	}
	// The verifier's global budget: a share is refused by name while the
	// bucket is spent, its expected cost reserved at admission and
	// reconciled with its measured time after the hash (verifybudget.go;
	// F-70).
	reserved, ok := e.budget.takeShare()
	if !ok {
		return errShareBudget
	}
	start := time.Now()
	err = algo.Verify(hs, height, nonce, mix, e.shareDifficultyAt(d, step), false)
	e.budget.chargeShare(reserved, time.Since(start))
	return err
}

// shareDifficultyAt is the difficulty whose target is the share target over
// 2^step: shareDifficulty(d) << step (the step form's hash check).
func (e *Engine) shareDifficultyAt(d *big.Int, step uint8) *big.Int {
	return new(big.Int).Lsh(e.shareDifficulty(d), uint(step))
}

// shareFormAt is the carried list's form at a height: the typed form from
// the registration's graphSharesFrom (section 10 (b)), else the step or
// count form.
func (e *Engine) shareFormAt(height uint64) types.ShareListForm {
	f := types.ShareListForm{Step: e.shareStepOn()}
	if m := e.vectorMenu(); m != nil {
		if from := m.GraphSharesFrom(); from > 0 && height >= from {
			f.Typed = true
			f.Step = true
		}
	}
	return f
}

// ShareFormTypedAt is shareFormAt's typed flag for the block validator.
func (e *Engine) ShareFormTypedAt(height uint64) bool { return e.shareFormAt(height).Typed }

// shareStepOn says whether the chain carries PA-53's step form (the
// sortition constant shareStepMax above zero): the carried entry has the
// step byte and the entries weigh 2^step; off, the count form.
func (e *Engine) shareStepOn() bool {
	s := e.config.Parts.Sortition
	return s != nil && s.ShareStepMax > 0
}

// ShareStepMax is PA-53's bound on the declared step (0: the count form).
func (e *Engine) ShareStepMax() uint64 {
	if s := e.config.Parts.Sortition; s != nil {
		return s.ShareStepMax
	}
	return 0
}

// minedStash keeps, by SharesRoot, what the derivation needs again in
// Finalize (the accepted entries and the new carry), set at assembly and on
// import.
type minedStash struct {
	mu      sync.Mutex
	by      map[common.Hash]minedEntry
	keys    []common.Hash
	entries map[common.Hash][]byte // the templates' carried shares by root
}

type minedEntry struct {
	accepted []shares.Entry
	carry    []shares.Entry
}

func (m *minedStash) put(root common.Hash, accepted, carry []shares.Entry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.by == nil {
		m.by = make(map[common.Hash]minedEntry)
	}
	if _, ok := m.by[root]; !ok {
		m.keys = append(m.keys, root)
		if len(m.keys) > 4096 {
			delete(m.by, m.keys[0])
			m.keys = m.keys[1:]
		}
	}
	m.by[root] = minedEntry{accepted, carry}
}

func (m *minedStash) get(root common.Hash) (minedEntry, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.by[root]
	return v, ok
}

// sharePool is the sealer's accepted shares: by the share's parent hash, in
// arrival order, each with its height; the templates it handed out by
// header hash; the counters.
type sharePool struct {
	mu       sync.Mutex
	byParent map[common.Hash][]pooledShare
	// The empty-list hold (the old public test network (chain 7320) 10,325, 15 October 2026: a one-block
	// reorganisation put back the shares of the dropped blocks, every one
	// naming a dropped parent, and the sealer re-sealed the height four
	// seconds later with an empty list while shares flowed, three quarters
	// of the block unissued): putBackAt is the time of the last put-back
	// and putBackHeight the lowest dropped height (the next template's);
	// acceptedAt the time of the last accepted share. A template at
	// putBackHeight with nothing to carry holds for one block interval
	// after the put-back while shares were flowing before it (sealer
	// policy: no wire change, no verifier rule).
	putBackAt     int64
	putBackHeight uint64
	acceptedAt    int64
	// seen is the duplicate index of accepted shares, address||extra||nonce||
	// parent to the share's height; pruned by the sweep below the window's
	// oldest ancestor (a share below the window is refused as stale before
	// the index is asked, so a pruned key can never be accepted again).
	// Unpruned it grew by every share for the node's life (A9 on the
	// ninth development chain, 12 October 2026: 150 bytes a share, 150 MB
	// a day at 700 shares a minute).
	seen            map[string]uint64
	seenPrunedBelow uint64 // the height the last prune ran below
	templates       map[common.Hash]shareTemplate
	tkeys           []common.Hash
	accepted        uint64
	carried         uint64
	expired         uint64
	refused         uint64
	// carriedElsewhere counts the pooled shares dropped because another
	// sealer's block within the window carried them (templateShares).
	carriedElsewhere uint64
	// flapped counts the shares refused for their work once and verified
	// on the second pass (F-224: a transient of the verifier's context,
	// never a worker's fault); each is logged at warning level.
	flapped uint64
	// refusedBy counts the refusals by their reason ("stale parent",
	// "pool full", "address at cap", the work, the duplicate, the recency),
	// for igneum_shareStats.
	refusedBy map[string]uint64
	// cap is the per-ADDRESS cap of accepted shares at one height
	// (SECURITY-GAPS.md; the project's order of 21:4x UK, 11 October 2026: no
	// single source fills the 256 ceiling), the chain config's
	// ShareCapPerAddress (the node team's params.IgneumRPCLimits; 64 at
	// launch, a quarter of the ceiling, so four addresses at the cap fill
	// a block and one cannot); zero is off. perAddr counts the accepted
	// shares by height and address; a height's counts go with its parent
	// when the parent leaves the pool (expiry or carriage).
	cap     uint64
	perAddr map[uint64]map[common.Address]uint64
	// pooled is the live count of accepted, not yet carried shares; the
	// pool is FULL at maxShares x ShareRecency (G-23, the project's ruling of 22:5x
	// UK, 11 October 2026: "accepted" means "will be paid or credited", so a
	// share is refused at acceptance when carrying it within the recency
	// is not assured; a sealer carries up to the ceiling oldest first every
	// block, so a full window's worth is what the recency can carry).
	pooled uint64
	// expiredNoBlock records the one remaining expiry: shares that left the
	// recency uncarried because THIS sealer sealed no block in the window
	// (its loss, not the miner's; the gossip of PA-46 is the launch form);
	// by the height they were accepted at, for igneum_shareStats.
	expiredNoBlock map[uint64]uint64
	// carriedBy is the window-wide duplicate set's cache (PA-46, the share
	// gossip; the node team): the keys (address, extraNonce, nonce, height)
	// of the shares each imported block carried, by block hash, so the
	// import of block h refuses a share an ancestor within the recency
	// already carried (two canonical carriages of one share, which the
	// gossip makes likely when two sealers hold it) without re-reading
	// bodies; an ancestor not in the cache is read through the chain
	// reader's GetBlock. Bounded by carriedCacheMax entries.
	carriedBy map[common.Hash][]string
	// carriedShares is what each imported block's carriage MOVED out of
	// this pool (MarkCarried), by block hash, so a reorg that drops the
	// block puts the shares back (UnmarkCarried, identity 4 of 12 October
	// 2026: a share carried by a block that lost its height was neither
	// paid nor pooled; H_s binds the share's parent, not the carrying
	// block, so the share is carriable again by the winning side within
	// its window). Bounded like carriedBy.
	carriedShares map[common.Hash][]pooledShare
	// carriedLists is each imported block's WHOLE carried list as the
	// journal recorded it (MarkCarried's journalCarried: every entry whose
	// parent the chain held), by block hash, so a reorg that drops the
	// block withdraws every one of those records (F-48's second path, 12
	// October 2026: a share already expired from this pool, carried by a
	// twin that then lost its height, kept the journal's carried record
	// for good because the uncarry covered only the shares the pool had
	// moved; 47 of a member's shares at 1034 on pod d read paid by
	// nobody). Bounded like carriedShares.
	carriedLists map[common.Hash][]carriedEntry
	// uncarried counts the shares put back by UnmarkCarried.
	uncarried uint64
	// expiredReorg is the other expiry, split from expiredNoBlock (the project's
	// order of 19:2x UK, 11 October 2026, the 180 unaccounted shares): a
	// share whose parent lost a reorg within the window (a sibling's price;
	// H_s binds the parent hash, so no block on the winning side can carry
	// it), by the share's height. expiredNoBlock keeps the window's end.
	expiredReorg map[uint64]uint64
	// accounts is the per-address ledger (shareaccount.go), rebuilt from
	// the journal at a restart.
	accounts map[common.Address]*shareAccount
	// journal is the engine's share journal once opened (SetShareJournal),
	// for the expiry records written under the pool's lock.
	journal *shareJournal
}

// carriedCacheMax bounds the carried-set cache (a window needs at most
// the recency's blocks; the bound is sixteen windows at the entry's
// widest recency).
const carriedCacheMax = 4096

// carriedKey is the window-wide duplicate key of a carried share: the
// share's (address, extraNonce, nonce) at the height it was mined for (k
// resolved to the height, so the key is the same under the LE32 entry).
func carriedKey(sh types.MinedShare, height uint64, parent common.Hash) string {
	// WITH the parent hash (the settlement team's ledger-20 of 04:2x UK,
	// 12 October 2026, the lost-after-carriage class at a tenth of the
	// ninth chain's shares): H_s binds the parent, so a share mined for a
	// sibling parent at the same height by a worker whose nonces restart
	// at zero is a different share; keyed without the parent, the
	// sibling's carriage marked the honest share carried (never paid, no
	// expiry) and the duplicate set refused its resubmission.
	return shareKey(sh.Address, sh.ExtraNonce, sh.Nonce) + "@" + fmt.Sprint(height) + "/" + parent.Hex()[2:18]
}

// carriedKeyAt is the carried key in the form the chain runs at the share's
// height: the parent form from PolicyV1 (node/0.9.3's gate), the height
// form below it (the release before, so every node of the chain keys one
// way at one height whatever binary it runs).
func (e *Engine) carriedKeyAt(sh types.MinedShare, height uint64, parent common.Hash) string {
	if e.policyOn(height) {
		return carriedKey(sh, height, parent)
	}
	return shareKey(sh.Address, sh.ExtraNonce, sh.Nonce) + "@" + fmt.Sprint(height)
}

// shareParentOf is the parent of a carried entry: the carrying block's
// ancestor k + 1 back (k = ParentIdx; the entry's share height is the
// block's less k, its parent the block at that height less one). The
// block's own parent for k = 0 without a chain read.
func shareParentOf(chain consensus.ChainHeaderReader, h *types.Header, k int) (common.Hash, bool) {
	if k == 0 {
		return h.ParentHash, true
	}
	cur := h
	for i := 0; i < k; i++ {
		if cur.Number.Sign() == 0 {
			return common.Hash{}, false
		}
		cur = chain.GetHeader(cur.ParentHash, cur.Number.Uint64()-1)
		if cur == nil {
			return common.Hash{}, false
		}
	}
	return cur.ParentHash, true
}

// blockReader is the chain reader with bodies (core.BlockChain), for the
// carried set of an ancestor the cache does not hold.
type blockReader interface {
	GetBlock(hash common.Hash, number uint64) *types.Block
}

// carriedKeysOf is the carried keys of a block: from the cache, else from
// its body through the chain reader (nil, false when neither holds it:
// a header-only reader, which only the tests are).
func (e *Engine) carriedKeysOf(chain consensus.ChainHeaderReader, h *types.Header) ([]string, bool) {
	p := &e.sharePool
	p.mu.Lock()
	keys, ok := p.carriedBy[h.Hash()]
	p.mu.Unlock()
	if ok {
		return keys, true
	}
	br, ok := chain.(blockReader)
	if !ok {
		return nil, false
	}
	b := br.GetBlock(h.Hash(), h.Number.Uint64())
	if b == nil {
		return nil, false
	}
	list, ok := types.ParseMinedSharesForm(b.MinedShares(), e.shareFormAt(h.Number.Uint64()))
	if !ok {
		return nil, true
	}
	keys = e.carriedKeysFromList(chain, h, list)
	e.rememberCarried(h.Hash(), keys)
	return keys, true
}

// carriedKeysFromList resolves each entry's k to its height against the
// block's own ancestors.
func (e *Engine) carriedKeysFromList(chain consensus.ChainHeaderReader, h *types.Header, list []types.MinedShare) []string {
	keys := make([]string, 0, len(list))
	for _, sh := range list {
		parent, ok := shareParentOf(chain, h, int(sh.ParentIdx))
		if !ok {
			continue
		}
		keys = append(keys, e.carriedKeyAt(sh, h.Number.Uint64()-uint64(sh.ParentIdx), parent))
	}
	return keys
}

// rememberCarried caches a block's carried keys.
func (e *Engine) rememberCarried(hash common.Hash, keys []string) {
	p := &e.sharePool
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.carriedBy == nil || len(p.carriedBy) >= carriedCacheMax {
		p.carriedBy = make(map[common.Hash][]string)
	}
	p.carriedBy[hash] = keys
}

// carriedWithinWindow is the set of shares the ancestors of a block within
// the recency carried: a share mined for height hs rides a block in [hs,
// hs + recency - 1], so an ancestor older than recency - 1 blocks cannot
// have carried a share still carriable by this block. An ancestor whose
// body neither the cache nor the reader holds is skipped (stated: a
// header-only reader).
func (e *Engine) carriedWithinWindow(chain consensus.ChainHeaderReader, header *types.Header) map[string]bool {
	set := make(map[string]bool)
	cur := chain.GetHeader(header.ParentHash, header.Number.Uint64()-1)
	for n := uint64(1); cur != nil && n < e.ShareRecencyBlocks(); n++ {
		if keys, ok := e.carriedKeysOf(chain, cur); ok {
			for _, k := range keys {
				set[k] = true
			}
		}
		if cur.Number.Uint64() == 0 {
			break
		}
		cur = chain.GetHeader(cur.ParentHash, cur.Number.Uint64()-1)
	}
	return set
}

// carriedAtHead is the carried-within-window set a share at the head's
// successor is checked against (the canonical chain's carriages, node seat
// B's cached set): the template, the journal's replay and the gossip's
// receipt all read it, so a share any canonical block within the window
// carried is never pooled or re-carried by this node (F-42, devnet7's
// stall at 350, 11 October 2026).
func (e *Engine) carriedAtHead(chain consensus.ChainHeaderReader) map[string]bool {
	head := chain.CurrentHeader()
	if head == nil {
		return nil
	}
	return e.carriedWithinWindow(chain, &types.Header{ParentHash: head.Hash(), Number: new(big.Int).SetUint64(head.Number.Uint64() + 1)})
}

// SetShareCap sets the per-address cap of accepted shares at one height
// (zero disables it); the Ethereum service sets it from the chain config.
func (e *Engine) SetShareCap(n uint64) {
	p := &e.sharePool
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cap = n
}

type pooledShare struct {
	share  types.MinedShare
	parent common.Hash
	height uint64
}

type shareTemplate struct {
	parent  common.Hash
	height  uint64
	address common.Address
	extra   uint64
}

// acceptedEntries folds a list of carried shares into shares.Entry weights
// (one per address, the share count).
func acceptedEntries(list []types.MinedShare) []shares.Entry {
	w := make(map[common.Address]uint64)
	for _, s := range list {
		// PA-53: a share at the declared step j weighs 2^j (1 under the
		// count form, where every entry's step is 0).
		w[s.Address] += uint64(1) << s.Step
	}
	out := make([]shares.Entry, 0, len(w))
	for a, n := range w {
		out = append(out, shares.Entry{Address: a, Weight: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Address.Hex() < out[j].Address.Hex() })
	return out
}

// VerifyMinedShares is the state processor's hook at the parent's state: a
// block's carried shares (each verified by one IgneumHash hash on the twin), the
// derived list against the block's list, and the stash for Finalize.
func (e *Engine) VerifyMinedShares(block *types.Block, st *state.StateDB, chain consensus.ChainHeaderReader) error {
	header := block.Header()
	height := header.Number.Uint64()
	entries := block.MinedShares()
	if !e.SharesOn(height) {
		if len(entries) > 0 || header.SharesRoot != nil {
			return errMinedSharesOff
		}
		return nil
	}
	s := e.config.Parts.Sortition
	list, ok := types.ParseMinedSharesForm(entries, e.shareFormAt(height))
	if !ok {
		return errSharesMalform
	}
	if uint64(len(list)) > s.MaxShares {
		return errSharesBound
	}
	// The ancestors: index 0 = the parent, up to ShareRecency-1; the
	// difficulty of the block at each share's height (the block whose
	// parent is the ancestor): the carrying block's own for k = 0, the
	// ancestor at k-1 otherwise.
	// The window is the chain's parameter (ShareRecencyBlocks: 64 on
	// devnet8), not the lane's default constant, so a share at a parent
	// index past 7 verifies under a wider window.
	window := int(e.ShareRecencyBlocks())
	anc := make([]*types.Header, 0, window)
	cur := chain.GetHeader(header.ParentHash, height-1)
	for cur != nil && len(anc) < window {
		anc = append(anc, cur)
		if cur.Number.Uint64() == 0 {
			break
		}
		cur = chain.GetHeader(cur.ParentHash, cur.Number.Uint64()-1)
	}
	// The window-wide duplicate set (PA-46): the shares the ancestors
	// within the recency carried; a share carried by any of them is
	// refused here, so one share is paid by one canonical block however
	// many sealers held it.
	earlier := e.carriedWithinWindow(chain, header)
	seen := make(map[string]bool, len(list))
	keys := make([]string, 0, len(list))
	for i, sh := range list {
		k := int(sh.ParentIdx)
		if k >= len(anc) {
			return fmt.Errorf("%w: entry %d (k %d)", errShareAncestor, i, k)
		}
		parent := anc[k]
		h := parent.Number.Uint64() + 1
		// The chain difficulty D of the block at h (under the round clock
		// amendment a bonded block's header carries D_seal, not D).
		var d *big.Int
		if k == 0 {
			d = e.ChainDifficulty(header)
		} else {
			d = e.ChainDifficulty(anc[k-1])
		}
		// F-690: from the share-lane gate the carried share's difficulty
		// is the carrying block's lane's at h (the share is verified under
		// the block's tag below), on every node alike.
		if bc, ok := header.CoordTag(); ok {
			ld, lerr := e.shareLaneDForImport(chain, parent, h, bc)
			if lerr != nil {
				return fmt.Errorf("%w: entry %d: %v", errShareWork, i, lerr)
			}
			if ld != nil {
				d = ld
			}
		}
		key := e.carriedKeyAt(sh, h, parent.Hash())
		if seen[key] {
			return fmt.Errorf("%w: entry %d", errShareDuplicate, i)
		}
		if earlier[key] {
			return fmt.Errorf("%w: entry %d (an ancestor within the recency carried it)", errShareCarriedBefore, i)
		}
		seen[key] = true
		keys = append(keys, key)
		// PA-53: the declared step within the chain's bound, the hash
		// under the share target over 2^step (the count form's entries
		// read step 0).
		if uint64(sh.Step) > s.ShareStepMax {
			return fmt.Errorf("%w: entry %d (step %d over %d)", errShareStep, i, sh.Step, s.ShareStepMax)
		}
		hs := ShareHeaderHash(parent.Hash(), h, sh.Address, sh.ExtraNonce)
		var coord *uint8
		if c, ok := header.CoordTag(); ok {
			cc := c
			coord = &cc
		}
		if err := e.verifyShareWorkOn(parent.Hash(), hs, h, sh.Nonce, sh.MixDigest, d, sh.Step, coord, sh.Ticket); err != nil {
			return fmt.Errorf("%w: entry %d: %v", errShareWork, i, err)
		}
	}
	accepted := acceptedEntries(list)
	if e.balancedOn(height) && len(anc) > 0 {
		if c, ok := e.coordOf(header); ok {
			accepted = e.balancedEntries(chain, anc[0], c, header.Difficulty, sealerOf(header, block.Withdrawals()), list)
		}
	}
	// The derived list against the block's.
	withdrawals := block.Withdrawals()
	if len(list) > 0 && len(withdrawals) == 0 {
		return errShareNoList
	}
	if len(withdrawals) == 0 {
		// no shares and no list: the plain form is allowed only when the
		// carry holds nothing to drain
		carry, err := e.ShareCarry(st)
		if err != nil {
			return err
		}
		if len(carry) > 0 {
			return fmt.Errorf("%w: credits to drain and no list", errMinedShareList)
		}
		return nil
	}
	if err := e.VerifyShareList(st, header, accepted, withdrawals); err != nil {
		return fmt.Errorf("%w: %v", errMinedShareList, err)
	}
	sealer := withdrawals[len(withdrawals)-1].Address
	derived, carry, err := e.ShareList(st, height, accepted, sealer)
	if err != nil {
		return err
	}
	_ = derived
	root := common.Hash{}
	if header.SharesRoot != nil {
		root = *header.SharesRoot
	}
	e.mined.put(stashKey(root, header), accepted, carry)
	// The block's carried set into the window cache for its descendants'
	// import (keyed by the block's hash: a sibling carrying the same share
	// is a different hash and its own set).
	e.rememberCarried(block.Hash(), keys)
	return nil
}

// stashKey keys the stash by the shares root, or by the header's parent and
// number when a block carries a list of credits alone (no shares root).
func stashKey(root common.Hash, header *types.Header) common.Hash {
	if root != (common.Hash{}) {
		return root
	}
	hasher := sha3.NewLegacyKeccak256()
	hasher.Write(header.ParentHash[:])
	hasher.Write(header.Number.Bytes())
	var out common.Hash
	hasher.Sum(out[:0])
	return out
}

// settleMinedShares is Finalize's path under the lane with a list: the
// whole derived list paid pro rata through SettleShares, the dust and the
// overflow into the carry. Returns false when the stash holds nothing for
// the block (the caller falls back to the plain pro rata).
func (e *Engine) settleMinedShares(header *types.Header, st *state.StateDB, amount *big.Int, withdrawals []*types.Withdrawal) bool {
	root := common.Hash{}
	if header.SharesRoot != nil {
		root = *header.SharesRoot
	}
	ent, ok := e.mined.get(stashKey(root, header))
	if !ok {
		return false
	}
	list := shares.FromWithdrawals(withdrawals)
	if e.payV2On(header.Number.Uint64()) {
		_, unissued, err := e.SettleSharesV2(st, header.Number.Uint64(), amount, list, ent.carry)
		if err != nil {
			e.log.Error("Share settle (second form) failed (consensus fault)", "number", header.Number, "err", err)
			return false
		}
		if unissued.Sign() > 0 {
			e.log.Debug("Pay rule second form: unissued", "number", header.Number, "unissued", unissued, "entries", len(list)-1)
		}
		return true
	}
	if _, err := e.SettleShares(st, amount, list, ent.carry); err != nil {
		e.log.Error("Share settle failed (consensus fault)", "number", header.Number, "err", err)
		return false
	}
	return true
}

// SubmitShare takes a worker's share for a template this node handed out
// (igneum_submitShare): verified as import will verify it, at the head's
// state and the next difficulty, then pooled by its parent.
func (e *Engine) SubmitShare(chain consensus.ChainHeaderReader, address common.Address, extraNonce, nonce uint64, mix common.Hash, height uint64) (bool, string) {
	return e.SubmitShareAt(chain, address, extraNonce, nonce, mix, height, nil, 0)
}

// SubmitShareAt is SubmitShare with the miner's parent (nil: unchecked, an
// older client) and PA-53's declared target step (0 under the count form;
// a step over the chain's bound, or any step on a chain in the count
// form, is refused by name before the work check).
func (e *Engine) SubmitShareAt(chain consensus.ChainHeaderReader, address common.Address, extraNonce, nonce uint64, mix common.Hash, height uint64, parent *common.Hash, step uint8) (bool, string) {
	ok, reason := e.acceptShare(chain, address, extraNonce, nonce, mix, height, parent, step, true, true, nil)
	if ok {
		e.announceAccepted(chain, address, extraNonce, nonce, mix, height, step)
	}
	return ok, reason
}

// SubmitGraphShareAt is igneum_submitGraphShare's admission (section 10
// (b)): a graph coordinate's share as its 168-byte ticket under the share
// header, the ticket hash the share's mix.
func (e *Engine) SubmitGraphShareAt(chain consensus.ChainHeaderReader, address common.Address, extraNonce, nonce uint64, ticket []byte, height uint64, parent *common.Hash, step uint8) (bool, string) {
	if len(ticket) != types.GraphTicketBytes {
		return false, errGraphShareTicket.Error()
	}
	mix, err := e.graphTicketHash(chain, address, extraNonce, nonce, ticket, height, parent)
	if err != nil {
		return false, err.Error()
	}
	return e.acceptShare(chain, address, extraNonce, nonce, mix, height, parent, step, true, true, ticket)
}

// graphTicketHash is the ticket hash of a graph share under its share
// header on the named parent (the canonical block at height-1 when none is
// named), through this node's coordinate's verifier.
func (e *Engine) graphTicketHash(chain consensus.ChainHeaderReader, address common.Address, extraNonce, nonce uint64, ticket []byte, height uint64, parent *common.Hash) (common.Hash, error) {
	ph := parent
	if ph == nil {
		if height == 0 {
			return common.Hash{}, errShareStale
		}
		h := chain.GetHeaderByNumber(height - 1)
		if h == nil {
			return common.Hash{}, errShareStale
		}
		hh := h.Hash()
		ph = &hh
	}
	m := e.vectorMenu()
	if m == nil {
		return common.Hash{}, errGraphShareOff
	}
	g, ok := m.ByCoordinateAt(e.Coordinate(), height).(interface {
		VerifyTicket(common.Hash, uint64, uint64, []byte) (common.Hash, error)
	})
	if !ok {
		return common.Hash{}, errShareOtherCoordinate
	}
	hs := ShareHeaderHash(*ph, height, address, extraNonce)
	return g.VerifyTicket(hs, height, nonce, ticket)
}

// SubmitShareOn is SubmitShare with the miner's parent named: a share whose
// parent is not this node's canonical block at its height is refused as
// "stale parent" before any work check (a sibling, never "invalid"). An
// accepted share is announced to the peers (sharegossip.go, PA-46), so a
// solo miner's share submitted to ANY node reaches every sealer's pool.
func (e *Engine) SubmitShareOn(chain consensus.ChainHeaderReader, address common.Address, extraNonce, nonce uint64, mix common.Hash, height uint64, parent common.Hash) (bool, string) {
	return e.SubmitShareAt(chain, address, extraNonce, nonce, mix, height, &parent, 0)
}

// refuse counts a refusal, in all and by its reason (the caller holds the
// pool's lock).
func (p *sharePool) refuse(reason error, address common.Address) {
	p.refused++
	if p.refusedBy == nil {
		p.refusedBy = make(map[string]uint64)
	}
	p.refusedBy[reason.Error()]++
	p.account(address).refused[reason.Error()]++
}

// acceptShare is SubmitShare's rule. journal says whether a REFUSAL is
// recorded on the miner's account (not for a gossiped share: 3df8c90dd,
// 13 October 2026); credit says whether an ACCEPTANCE is credited to the
// address's account and written to the share journal (not on a replay,
// whose accounts the ledger rebuilds first). The two were one flag until
// F-147 (15 October 2026): a reader fed by gossip alone credited no
// address while its aggregate rose, so igneum_shareAccount read flat on
// every non-sealing node of the public test network under 0.10.15 and the
// settlement seat's identity 1 failed there.
func (e *Engine) acceptShare(chain consensus.ChainHeaderReader, address common.Address, extraNonce, nonce uint64, mix common.Hash, height uint64, parentHash *common.Hash, step uint8, journal, credit bool, ticket []byte) (bool, string) {
	if !e.SharesOn(height) {
		return false, errMinedSharesOff.Error()
	}
	// A lane at pay weight 0 takes no share (the project's ruling of 10:3x UK,
	// 15 October 2026: the registry transaction's pay change clears the
	// lane's pool at the recency instead of piling it to the bound),
	// refused before any chain read.
	if m := e.vectorMenu(); m != nil {
		if w := m.PaySharesAt(height); int(e.Coordinate()) < len(w) && w[e.Coordinate()] == 0 {
			if journal {
				e.sharePool.mu.Lock()
				e.sharePool.refuse(errShareLaneUnpaid, address)
				e.sharePool.mu.Unlock()
			}
			return false, errShareLaneUnpaid.Error()
		}
	}
	// PA-53: the declared step within the chain's bound (none above zero
	// under the count form), read before any work.
	if uint64(step) > e.ShareStepMax() {
		e.sharePool.mu.Lock()
		if journal {
			e.sharePool.refuse(errShareStep, address)
		}
		e.sharePool.mu.Unlock()
		return false, errShareStep.Error()
	}
	head := chain.CurrentHeader()
	if head == nil {
		return false, "no head"
	}
	// A refusal is counted by name for a live submission only: the
	// journal's replay (journal false) re-reads shares the window may have
	// passed, which the replay counts as expired, not as refused (the 211
	// "not the head or one of the seven before it" at the 19:01 UK restart
	// of 11 October 2026 were journal entries, not a worker's). F-224 (16
	// October 2026, the public test network's third sealer refusing 27 of
	// 133 valid coordinate-3 shares as an invalid mix digest with no line
	// in its log): every live refusal is LOGGED at warning level with its
	// reason, the parent named and the canonical one, the height, the head
	// and the step, as block submits are (sealer.go), so a refusal of
	// valid work is read on the node and not from the pool's journal.
	var canonical common.Hash
	warn := func(reason, inner error) {
		if !journal {
			return
		}
		named := "none"
		if parentHash != nil {
			named = parentHash.Hex()
		}
		ctx := []interface{}{"reason", reason, "height", height, "head", head.Number.Uint64(), "parent", named, "canonical", canonical.Hex(), "address", address, "extraNonce", extraNonce, "nonce", nonce, "step", step}
		if inner != nil {
			ctx = append(ctx, "err", inner)
		}
		e.log.Warn("Share refused", ctx...)
	}
	refuse := func(reason error) {
		if !journal {
			return
		}
		e.sharePool.mu.Lock()
		e.sharePool.refuse(reason, address)
		e.sharePool.mu.Unlock()
	}
	// The share's parent: the block at height-1 on the canonical chain,
	// within the recency of the head.
	if height > head.Number.Uint64()+1 || head.Number.Uint64()+1-height >= e.ShareRecencyBlocks() {
		refuse(errShareStale)
		warn(errShareStale, nil)
		return false, errShareStale.Error()
	}
	parent := chain.GetHeaderByNumber(height - 1)
	if parent == nil {
		refuse(errShareStale)
		warn(errShareStale, nil)
		return false, errShareStale.Error()
	}
	canonical = parent.Hash()
	// The miner's parent FIRST (the project's rule of 23:5x UK, 11 October 2026,
	// from the paced 4070 row: honest work on a sibling read "invalid mix
	// digest" because the node recomputed H_s with its own parent): a share
	// that names a parent other than this node's canonical block at its
	// height is refused as "stale parent" and never reaches the work check,
	// so a miner's log never says invalid for honest work on a sibling. A
	// submission without the parent (an older client) is checked as before.
	if parentHash != nil && *parentHash != parent.Hash() {
		refuse(errShareStaleParent)
		warn(errShareStaleParent, nil)
		return false, errShareStaleParent.Error()
	}
	// The chain difficulty D of the block at height: the canonical block's
	// when it exists, else the next block's by the rule.
	d := e.shareChainDifficulty(chain, parent, chain.GetHeaderByNumber(height))
	// F-690: from the share-lane gate a submitted share is checked
	// against this node's sealing coordinate's lane difficulty at height
	// (the pool sends a coordinate's shares to that coordinate's sealer).
	if ld := e.shareLaneD(chain, parent, height, e.Coordinate()); ld != nil {
		d = ld
	}
	hs := ShareHeaderHash(parent.Hash(), height, address, extraNonce)
	err := e.verifyShareWorkAny(parent.Hash(), hs, height, nonce, mix, d, step, ticket)
	if err != nil && !errors.Is(err, errShareOtherCoordinate) && !errors.Is(err, errShareBudget) {
		// F-224: the programmed view's hash answered a ZERO mix on any
		// error of the context path (a context evicted between its two
		// locks, a failed build) or non-OK status from the reference, read
		// as an invalid mix digest in 0 ms, so valid work was refused in
		// a transient (27 of 133 shares at heights 874 to 899, every one
		// valid to the byte by the GPU team's reference). A share refused for its
		// work is verified ONCE MORE before the answer: a transient becomes
		// a pass at the cost of one hash, logged as the flap it was with
		// the first refusal's reason; a refusal that holds is real. The
		// second hash runs under the verifier's budget as the first did.
		if err2 := e.verifyShareWorkAny(parent.Hash(), hs, height, nonce, mix, d, step, ticket); err2 == nil {
			e.log.Warn("Share verification flapped (F-224): refused once, verified on the second pass", "first", err, "height", height, "parent", parent.Hash(), "address", address, "nonce", nonce, "step", step)
			e.sharePool.mu.Lock()
			e.sharePool.flapped++
			e.sharePool.mu.Unlock()
			err = nil
		} else {
			err = err2
		}
	}
	if err != nil {
		if errors.Is(err, errShareOtherCoordinate) {
			warn(errShareOtherCoordinate, nil)
			return false, errShareOtherCoordinate.Error()
		}
		refuse(errShareWork)
		warn(errShareWork, err)
		return false, errShareWork.Error() + ": " + err.Error()
	}
	key := string(address[:]) + fmt.Sprint(extraNonce, nonce) + parent.Hash().Hex()
	p := &e.sharePool
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.seen == nil {
		p.seen = make(map[string]uint64)
		p.byParent = make(map[common.Hash][]pooledShare)
	}
	if _, dup := p.seen[key]; dup {
		if journal {
			p.refuse(errShareDuplicate, address)
		}
		return false, errShareDuplicate.Error()
	}
	// The per-address cap at this height (the chain config's), read
	// BEFORE the pool's bound: it is the more specific reason, so a worker
	// at its cap paces by address and only the pool's state refuses the
	// rest as "pool full".
	if p.cap > 0 {
		if p.perAddr == nil {
			p.perAddr = make(map[uint64]map[common.Address]uint64)
		}
		if p.perAddr[height] == nil {
			p.perAddr[height] = make(map[common.Address]uint64)
		}
		if p.perAddr[height][address] >= p.cap {
			if journal {
				p.refuse(errShareCap, address)
			}
			return false, errShareCap.Error()
		}
	}
	// The admission bound before acceptance (PA-53): an address's unpaid
	// shares in this pool may not exceed what the chain pays it within the
	// recency, the constant AdmissionPooledPerAddress (one block's whole
	// list: a block carries at most MaxShares entries, and an address with
	// a whole list pooled already has nothing the next carriage could pay
	// beyond it), so the refusal is at submit and by name, never an
	// accepted share that expires by window.
	if bound := e.admissionPooledPerAddress(); bound > 0 && p.account(address).pooled >= bound {
		if journal {
			p.refuse(errShareAdmission, address)
		}
		return false, errShareAdmission.Error()
	}
	// The lane at carriage (F-50's second half): the outstanding shares
	// across every address against the chain's carriage within the
	// recency, refused by name before any receipt.
	if bound := e.laneCarriageBound(); bound > 0 && p.pooled >= bound {
		if journal {
			p.refuse(errShareLaneAtCarriage, address)
		}
		return false, errShareLaneAtCarriage.Error()
	}
	// The pool at its capacity for the recency window: carriage within the
	// recency is not assured, so the share is refused by name and the
	// worker paces (G-23).
	if bound := e.SharePoolBound(); bound > 0 && p.pooled >= bound {
		if journal {
			p.refuse(errSharePoolFull, address)
		}
		return false, errSharePoolFull.Error()
	}
	if p.cap > 0 {
		p.perAddr[height][address]++
	}
	p.seen[key] = height
	ps := pooledShare{share: types.MinedShare{Address: address, ExtraNonce: extraNonce, Nonce: nonce, MixDigest: mix, Step: step, Ticket: append([]byte(nil), ticket...)}, parent: parent.Hash(), height: height}
	p.byParent[parent.Hash()] = append(p.byParent[parent.Hash()], ps)
	p.acceptedAt = time.Now().Unix()
	p.accepted++
	p.pooled++
	acc := p.account(address)
	acc.pooled++
	if credit {
		// A replayed share's acceptance is already in the ledger and in
		// its account (the replay rebuilds the accounts first); a
		// gossiped share's is credited here as a submitted one's.
		acc.accepted[height]++
		e.shareJournal.write(acceptedRecord(ps))
	}
	return true, ""
}

// templateShares picks the pool's shares for a template whose parent is the
// head: the ancestors' shares oldest first (k by ancestor index), at most
// maxShares, the rest held; shares whose parent fell out of the recency are
// counted expired and dropped.
// shareAncestors is the carriage window seen from a template's parent:
// the parent first (k = 0), then its ancestors, ShareRecencyBlocks() in
// all (a share's parent must be one of them).
func (e *Engine) shareAncestors(chain consensus.ChainHeaderReader, parent *types.Header) []*types.Header {
	n := int(e.ShareRecencyBlocks())
	anc := make([]*types.Header, 0, n)
	cur := parent
	for cur != nil && len(anc) < n {
		anc = append(anc, cur)
		if cur.Number.Uint64() == 0 {
			break
		}
		cur = chain.GetHeader(cur.ParentHash, cur.Number.Uint64()-1)
	}
	return anc
}

// expireLocked drops from the pool the shares whose parent fell out of the
// carriage window anc (the one remaining expiry, G-23: this node carried
// no block within the window; named by the height they were accepted at)
// and the per-address counts below the window. The pool's lock is held.
func (p *sharePool) expireLocked(chain consensus.ChainHeaderReader, anc []*types.Header) {
	keep := make(map[common.Hash]int, len(anc))
	for k, a := range anc {
		keep[a.Hash()] = k
	}
	// The class by the window the sweep itself walked (the node team's read
	// of 12 October 2026, pod d at 1,721: 29 shares whose parent had just
	// lost its twin expired as "window" thirteen seconds after acceptance,
	// because expiryClass asked the chain's number index for the parent's
	// height while the reorg was still writing it, and the replay later
	// re-classed them as reorg from the settled chain). A parent whose
	// height lies inside the window and is not among its ancestors has
	// lost a reorg; only a parent below the window's oldest ancestor is
	// the window's end. Both read from anc alone, so the live class and
	// the replay's agree.
	oldest := uint64(0)
	if len(anc) > 0 {
		oldest = anc[len(anc)-1].Number.Uint64()
	}
	for ph, list := range p.byParent {
		if _, ok := keep[ph]; !ok {
			for _, ps := range list {
				why := expiryWindow
				if ps.height > 0 && ps.height-1 >= oldest {
					why = expiryReorg
				}
				p.expireOne(ps, why)
			}
			delete(p.byParent, ph)
		}
	}
	// The per-address counts hold for a height's whole recency (carriage
	// does not reset a cap); below the oldest kept ancestor's successor
	// they go.
	if len(anc) > 0 {
		oldest := anc[len(anc)-1].Number.Uint64() + 1
		for h := range p.perAddr {
			if h < oldest {
				delete(p.perAddr, h)
			}
		}
		// The duplicate index below the window goes with them, once per
		// advance of the window's oldest height (a share below the window
		// is refused as stale before the index is asked).
		if oldest > p.seenPrunedBelow {
			for k, h := range p.seen {
				if h < oldest {
					delete(p.seen, k)
				}
			}
			p.seenPrunedBelow = oldest
		}
	}
}

// expiryClass is the class of a share leaving the pool uncarried (the project's
// ruling of 20:0x UK, 11 October 2026, on ecosystem's read of the swapped
// peer, where 531's and 537's shares, whose parents 530 and 536 lost their
// siblings, were filed under the window's end because the replay read the
// class by the window's position): a share whose PARENT is not the
// canonical block at its height is expiredReorg, whatever height it
// expired at (the rule's price: "accepted means paid or credited while
// the parent stays canonical"); only a share whose parent is canonical
// and whose recency window has passed is expiredWindow, which a healthy
// chain reads as zero. A chain without the parent's height reads reorg
// (the parent is not canonical on it).
func expiryClass(chain consensus.ChainHeaderReader, ps pooledShare) shareExpiry {
	if chain == nil || ps.height == 0 {
		return expiryWindow
	}
	if h := chain.GetHeaderByNumber(ps.height - 1); h != nil && h.Hash() == ps.parent {
		return expiryWindow
	}
	return expiryReorg
}

// expireOne counts one pooled share's expiry, pool-wide and in its
// address's ledger, and journals it (type 4) so a restart rebuilds the
// same ledger. The pool's lock is held.
func (p *sharePool) expireOne(ps pooledShare, why shareExpiry) {
	p.expired++
	if p.pooled > 0 {
		p.pooled--
	}
	acc := p.account(ps.share.Address)
	if acc.pooled > 0 {
		acc.pooled--
	}
	switch why {
	case expiryReorg:
		if p.expiredReorg == nil {
			p.expiredReorg = make(map[uint64]uint64)
		}
		p.expiredReorg[ps.height]++
		acc.expiredReorg[ps.height]++
	default:
		if p.expiredNoBlock == nil {
			p.expiredNoBlock = make(map[uint64]uint64)
		}
		p.expiredNoBlock[ps.height]++
		acc.expiredWindow[ps.height]++
	}
	if p.journal != nil {
		p.journal.write(expiredRecord(ps, why))
	}
}

// ExpireShares runs the pool's expiry sweep at a new head (PA-46, the
// share gossip; the node team): a NON-sealing node never builds a template,
// so without this its pool of received and submitted shares would grow
// without bound; the gossip handler calls it on every new head with the
// head as the window's parent, the same sweep templateShares runs. It
// returns the number expired by this call.
func (e *Engine) ExpireShares(chain consensus.ChainHeaderReader, head *types.Header) uint64 {
	if head == nil || !e.SharesOn(head.Number.Uint64()+1) {
		return 0
	}
	anc := e.shareAncestors(chain, head)
	p := &e.sharePool
	p.mu.Lock()
	defer p.mu.Unlock()
	before := p.expired
	p.expireLocked(chain, anc)
	return p.expired - before
}

func (e *Engine) templateShares(chain consensus.ChainHeaderReader, parent *types.Header) ([]types.MinedShare, []byte) {
	s := e.config.Parts.Sortition
	anc := e.shareAncestors(chain, parent)
	// The canonical chain's carriages within the window (the node team's
	// set, cached per imported block), read BEFORE the pool's lock: its
	// reader takes the same lock.
	carried := e.carriedWithinWindow(chain, &types.Header{ParentHash: parent.Hash(), Number: new(big.Int).SetUint64(parent.Number.Uint64() + 1)})
	p := &e.sharePool
	p.mu.Lock()
	defer p.mu.Unlock()
	p.expireLocked(chain, anc)
	var out []types.MinedShare
	var entries []byte
	limit := int(s.MaxShares)
	// oldest ancestors first (largest k), then arrival order
	// Shares another sealer's block already carried within the window
	// (the gossip puts one share in every pool; devnet7's stall at 350, 11
	// October 2026: the other sealer's carriage left the share in this
	// pool, every template re-carried it, and the import rule refused the
	// block): dropped from the pool as carried elsewhere, never carried
	// again, never counted as expired.
	for ph, list := range p.byParent {
		kept := list[:0]
		for _, ps := range list {
			if carried[e.carriedKeyAt(ps.share, ps.height, ps.parent)] {
				p.carriedElsewhere++
				p.carryOne(ps)
				continue
			}
			kept = append(kept, ps)
		}
		if len(kept) == 0 {
			delete(p.byParent, ph)
		} else {
			p.byParent[ph] = kept
		}
	}
	// The selection rule (PA-53's (4), the design seat's ruling of 06:1x UK,
	// 12 October 2026): the list of up to MaxShares takes shares by their
	// address's ACCUMULATED CARRIED WEIGHT descending, the oldest first on
	// ties (the oldest ancestor, then arrival order), so a flood of light
	// addresses cannot displace an honest address of greater or older
	// weight; an honest address at or above the chain's minimum weight is
	// paid within the recency.
	type candidate struct {
		sh types.MinedShare
	}
	var cands []candidate
	for k := len(anc) - 1; k >= 0; k-- {
		for _, ps := range p.byParent[anc[k].Hash()] {
			sh := ps.share
			sh.ParentIdx = uint8(k)
			cands = append(cands, candidate{sh: sh})
		}
	}
	// F-122 (vectorwork8, 14 October 2026): the order above let a key
	// with no carriage yet lose every slot to a key already carried,
	// whatever the share's own weight (a 2^20-step share behind 256
	// step-0 shares at every height, never carried, expired at the
	// window and then refused at the admission bound), so the ORDER IS
	// the share's own weight 2^step first. F-174 (the design review's finding of
	// 20:1x UK, 15 October 2026, F-122's second face live on the research
	// chain's lane 18: one block in 40 to 60, each full at MaxShares, the
	// carried-weight term then starving a never-carried key at EQUAL step
	// for good, since ranking needed carriage): within a step the OLDEST
	// share first (the share's height by its ancestor, then arrival), the
	// carried-weight term retired; the step order and the per-address
	// admission bound carry the flood case that term was for, and oldest
	// first minimises expiry. The candidates are gathered oldest ancestor
	// first and in arrival order, so the stable sort by step alone is the
	// rule. The template's choice alone; any valid list verifies.
	sort.SliceStable(cands, func(i, j int) bool {
		return cands[i].sh.Step > cands[j].sh.Step
	})
	form := e.shareFormAt(parent.Number.Uint64() + 1)
	var graphCap uint64
	if m := e.vectorMenu(); m != nil && form.Typed {
		graphCap = m.GraphSharesPerBlock()
	}
	var graphN uint64
	for i := 0; i < len(cands) && len(out) < limit; i++ {
		if len(cands[i].sh.Ticket) > 0 {
			// A graph share rides the typed form alone, under the
			// block's cap (section 10 (b)); the rest stay pooled.
			if !form.Typed || (graphCap > 0 && graphN >= graphCap) {
				continue
			}
			graphN++
		}
		out = append(out, cands[i].sh)
		entries = append(entries, cands[i].sh.BytesForm(form)...)
	}
	return out, entries
}

// laneCarriageBound is the lane's carriage within the recency: the
// operating shares a block (sharesPerBlock) times the recency, the most
// shares the chain pays in a window; the outstanding count across every
// address is held under it (errShareLaneAtCarriage). Zero when the lane is
// off.
func (e *Engine) laneCarriageBound() uint64 {
	s := e.config.Parts.Sortition
	if s == nil || s.SharesPerBlock == 0 {
		return 0
	}
	return s.SharesPerBlock * e.ShareRecencyBlocks()
}

// admissionPooledPerAddress is the admission bound (errShareAdmission): a
// block's whole list, MaxShares, as the most unpaid shares one address may
// hold in this pool; zero when the lane is off.
func (e *Engine) admissionPooledPerAddress() uint64 {
	s := e.config.Parts.Sortition
	if s == nil {
		return 0
	}
	return s.MaxShares
}

// TemplateShares builds a template's carried shares and its derived payout
// list at the parent's state (the worker calls it before the template is
// signed): the entries, the list, and whether anything is to be carried.
// The sealer's address is the template's coinbase before the list replaces
// it. The stash is set for Finalize at assembly.
func (e *Engine) TemplateShares(chain consensus.ChainHeaderReader, parent *types.Header, st *state.StateDB, sealer common.Address) (entries []byte, list []*types.Withdrawal, err error) {
	height := parent.Number.Uint64() + 1
	if !e.SharesOn(height) {
		return nil, nil, nil
	}
	picked, entries := e.templateShares(chain, parent)
	accepted := acceptedEntries(picked)
	if e.balancedOn(height) {
		// The balanced reward (balanced.go): the template's coordinate
		// is this node's, its difficulty the coordinate's next.
		if d, err := e.vectorDifficulty(chain, parent, e.Coordinate()); err == nil {
			accepted = e.balancedEntries(chain, parent, e.Coordinate(), d, sealer, picked)
		}
	}
	derived, carry, err := e.ShareList(st, height, accepted, sealer)
	if err != nil {
		return nil, nil, err
	}
	if len(picked) == 0 {
		if hold, left := e.emptyListHold(height); hold {
			return nil, nil, fmt.Errorf("%w: the share lane holds the seal %d s more after a reorganisation at %d, the pool empty while shares flow", errSortitionWait, left, height)
		}
	}
	if len(picked) == 0 && len(derived) <= 1 {
		// nothing carried and nothing to drain: the plain form
		return nil, nil, nil
	}
	list = shares.Withdrawals(derived)
	e.indexWithdrawals(st, height, list)
	root := types.MinedSharesRoot(entries)
	if len(entries) == 0 {
		root = common.Hash{}
	} else {
		e.mined.putEntries(root, entries)
	}
	h := &types.Header{ParentHash: parent.Hash(), Number: new(big.Int).SetUint64(height)}
	e.mined.put(stashKey(root, h), accepted, carry)
	return entries, list, nil
}

// templateEntries is the carried shares' bytes for a template's root (set by
// TemplateShares; read by FinalizeAndAssemble).
func (e *Engine) templateEntries(root common.Hash) ([]byte, bool) {
	e.mined.mu.Lock()
	defer e.mined.mu.Unlock()
	b, ok := e.mined.entries[root]
	return b, ok
}

func (m *minedStash) putEntries(root common.Hash, entries []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.entries == nil {
		m.entries = make(map[common.Hash][]byte)
	}
	m.entries[root] = append([]byte{}, entries...)
	if len(m.entries) > 1024 {
		for k := range m.entries {
			if k != root {
				delete(m.entries, k)
				break
			}
		}
	}
}

// MarkCarried moves the carried shares out of the pool once the block that
// carries them is sealed (the sealer's own block imported), so they are not
// carried twice; the remaining shares stay for the next block.
func (e *Engine) MarkCarried(chain consensus.ChainHeaderReader, block *types.Block) {
	list, ok := types.ParseMinedSharesForm(block.MinedShares(), e.shareFormAt(block.NumberU64()))
	if !ok || len(list) == 0 {
		return
	}
	// Keyed WITH the share's height (PA-46, the node team) AND its parent
	// (the ledger-20 class of 12 October 2026): the carried entry's height
	// is the block's less its parent index, its parent the block's
	// ancestor one further back (shareParentOf); an entry whose parent the
	// chain does not hold marks nothing.
	height := block.NumberU64()
	header := block.Header()
	carriedKeys := make(map[string]bool, len(list))
	parents := make([]common.Hash, len(list))
	for i, sh := range list {
		parent, ok := shareParentOf(chain, header, int(sh.ParentIdx))
		if !ok {
			continue
		}
		parents[i] = parent
		carriedKeys[e.carriedKeyAt(sh, height-uint64(sh.ParentIdx), parent)] = true
	}
	e.journalCarried(list, parents, height)
	p := &e.sharePool
	p.mu.Lock()
	defer p.mu.Unlock()
	var moved []pooledShare
	for ph, pl := range p.byParent {
		kept := pl[:0]
		for _, ps := range pl {
			if carriedKeys[e.carriedKeyAt(ps.share, ps.height, ps.parent)] {
				p.carried++
				p.carryOne(ps)
				moved = append(moved, ps)
				continue
			}
			kept = append(kept, ps)
		}
		if len(kept) == 0 {
			delete(p.byParent, ph)
		} else {
			p.byParent[ph] = kept
		}
	}
	if len(moved) > 0 {
		if p.carriedShares == nil || len(p.carriedShares) >= carriedCacheMax {
			p.carriedShares = make(map[common.Hash][]pooledShare)
		}
		p.carriedShares[block.Hash()] = moved
	}
	var whole []carriedEntry
	for i, sh := range list {
		if parents[i] == (common.Hash{}) {
			continue
		}
		whole = append(whole, carriedEntry{address: sh.Address, extraNonce: sh.ExtraNonce, nonce: sh.Nonce, height: height - uint64(sh.ParentIdx), parent: parents[i]})
	}
	if len(whole) > 0 {
		if p.carriedLists == nil || len(p.carriedLists) >= carriedCacheMax {
			p.carriedLists = make(map[common.Hash][]carriedEntry)
		}
		p.carriedLists[block.Hash()] = whole
	}
}

// carriedEntry is one journaled carriage of a block: the share's fields,
// its height and the parent it named.
type carriedEntry struct {
	address    common.Address
	extraNonce uint64
	nonce      uint64
	height     uint64
	parent     common.Hash
}

// UnmarkCarried is the backend's call on a side event: the block lost its
// height, so the shares its carriage moved out of this pool come back
// (identity 4): pooled again under their own parent, the ledger's carried
// count at their height given back, the journal's uncarried record
// written (type 6; the replay reads it as the carriage undone). A share
// whose parent is itself off the canonical chain expires at the next
// ExpireShares by the canonicity rule; a share past its window expires
// there too. Nothing is paid or refused here.
func (e *Engine) UnmarkCarried(block *types.Block) {
	if block == nil {
		return
	}
	p := &e.sharePool
	p.mu.Lock()
	defer p.mu.Unlock()
	moved := p.carriedShares[block.Hash()]
	whole := p.carriedLists[block.Hash()]
	if len(moved) == 0 && len(whole) == 0 {
		return
	}
	delete(p.carriedShares, block.Hash())
	delete(p.carriedLists, block.Hash())
	if p.byParent == nil {
		p.byParent = make(map[common.Hash][]pooledShare)
	}
	for _, ps := range moved {
		p.byParent[ps.parent] = append(p.byParent[ps.parent], ps)
		p.pooled++
		p.uncarried++
		if p.carried > 0 {
			p.carried--
		}
		acc := p.account(ps.share.Address)
		acc.pooled++
		if acc.carried[ps.height] > 0 {
			acc.carried[ps.height]--
		}
	}
	// The journal withdraws EVERY carriage the block's record laid (the
	// whole list), the moved shares among them: a share this pool no
	// longer held when the block carried it (expired, or carried before
	// by another block) has its carried record undone too, so the replay
	// and the account read the canonical carriage alone (F-48's second
	// path). The list is the block's when it was marked; the moved shares
	// alone when the block was marked before this node held lists.
	var rec []byte
	if len(whole) > 0 {
		for _, c := range whole {
			rec = append(rec, e.uncarriedRecordAt(c.address, c.extraNonce, c.nonce, c.height, c.parent)...)
		}
	} else {
		for _, ps := range moved {
			rec = append(rec, e.uncarriedRecordAt(ps.share.Address, ps.share.ExtraNonce, ps.share.Nonce, ps.height, ps.parent)...)
		}
	}
	if p.journal != nil {
		p.journal.write(rec)
	}
	if n := block.NumberU64(); p.putBackAt == 0 || n < p.putBackHeight || time.Now().Unix()-p.putBackAt > int64(e.holdSeconds()) {
		p.putBackHeight = n
	}
	p.putBackAt = time.Now().Unix()
	e.log.Info("Shares put back by a reorg", "number", block.Number(), "hash", block.Hash(), "shares", len(moved), "recordsWithdrawn", len(whole))
}

// holdSeconds is the empty-list hold's length: one block interval (the
// difficulty rule's target), 10 where the rule names none.
func (e *Engine) holdSeconds() uint64 {
	if t := e.config.Parts.Difficulty.Target; t > 0 {
		return t
	}
	return 10
}

// emptyListHold reports whether a template at height with nothing to carry
// holds: within one block interval of a put-back at that height, with a
// share accepted within the interval before the put-back (shares were
// flowing); the worker asks again at the next tick and seals with whatever
// arrived.
func (e *Engine) emptyListHold(height uint64) (bool, int64) {
	p := &e.sharePool
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.putBackAt == 0 || p.putBackHeight != height {
		return false, 0
	}
	hold := int64(e.holdSeconds())
	now := time.Now().Unix()
	if now-p.putBackAt >= hold {
		return false, 0
	}
	if p.acceptedAt == 0 || p.putBackAt-p.acceptedAt > hold {
		return false, 0
	}
	return true, p.putBackAt + hold - now
}

// carryOne moves one pooled share to carried in the pool's live count and
// in its address's ledger (the pool's lock is held; the journal's carried
// record is the block's, written by journalCarried).
func (p *sharePool) carryOne(ps pooledShare) {
	if p.pooled > 0 {
		p.pooled--
	}
	acc := p.account(ps.share.Address)
	if acc.pooled > 0 {
		acc.pooled--
	}
	acc.carried[ps.height]++
}

// ShareStats is igneum_shareStats: the pool's counters and size.
type ShareStats struct {
	Accepted uint64 `json:"accepted"`
	Carried  uint64 `json:"carried"`
	Expired  uint64 `json:"expired"`
	Refused  uint64 `json:"refused"`
	Pooled   uint64 `json:"pooled"`
	Parents  int    `json:"parents"`
	// The share journal's last replay (sharejournal.go): the shares put
	// back into the pool at this start and the ones it refused.
	Replayed uint64 `json:"replayed"`
	Dropped  uint64 `json:"dropped"`
	// ExpiredNoBlock names the one remaining expiry (G-23): shares that
	// left the recency because this sealer carried no block in the window,
	// by the height they were accepted at. Capacity is the pool-full bound
	// (maxShares x the recency), the point at which a submission is refused
	// as "pool full".
	ExpiredNoBlock map[uint64]uint64 `json:"expiredNoBlock,omitempty"`
	// ExpiredReorg is the other expiry: shares whose parent lost a reorg
	// within the window (a sibling's price), by the share's height.
	ExpiredReorg map[uint64]uint64 `json:"expiredReorg,omitempty"`
	Capacity     uint64            `json:"capacity"`
	// RefusedBy is the refusals by reason ("stale parent" a sibling's
	// honest work, "pool full", "address at cap", the work, the duplicate,
	// the recency).
	RefusedBy map[string]uint64 `json:"refusedBy,omitempty"`
	// CarriedElsewhere is the pooled shares dropped because another
	// sealer's block within the window carried them (the gossip's pools
	// overlap; paid once, by that block).
	CarriedElsewhere uint64 `json:"carriedElsewhere"`
	// Uncarried counts the shares put back into the pool because the
	// block that carried them lost its height (UnmarkCarried).
	Uncarried uint64 `json:"uncarried"`
	// Flapped counts the shares refused for their work once and verified
	// on the second pass (F-224): a transient of the verifier's context,
	// each logged at warning level; a count above zero is the fault's
	// trace, never a worker's.
	Flapped uint64 `json:"flapped"`
}

func (e *Engine) ShareStatsNow() ShareStats {
	j := &e.shareJournal
	j.mu.Lock()
	replayed, dropped := j.replayed, j.dropped
	j.mu.Unlock()
	p := &e.sharePool
	p.mu.Lock()
	defer p.mu.Unlock()
	var n uint64
	for _, l := range p.byParent {
		n += uint64(len(l))
	}
	var enb map[uint64]uint64
	if len(p.expiredNoBlock) > 0 {
		enb = make(map[uint64]uint64, len(p.expiredNoBlock))
		for h, c := range p.expiredNoBlock {
			enb[h] = c
		}
	}
	var by map[string]uint64
	if len(p.refusedBy) > 0 {
		by = make(map[string]uint64, len(p.refusedBy))
		for r, c := range p.refusedBy {
			by[r] = c
		}
	}
	return ShareStats{Accepted: p.accepted, Carried: p.carried, Expired: p.expired, Refused: p.refused, Pooled: n, Parents: len(p.byParent), Replayed: replayed, Dropped: dropped, ExpiredNoBlock: enb, ExpiredReorg: copyBy(p.expiredReorg), Capacity: e.SharePoolBound(), RefusedBy: by, CarriedElsewhere: p.carriedElsewhere, Uncarried: p.uncarried, Flapped: p.flapped}
}

// shareChainDifficulty is the chain difficulty D(h) a share at height h is
// targeted against (LAUNCH-GUARDS 2.9: Target(D, n) on the CHAIN's
// difficulty): the block at h's when the chain holds it (ChainDifficulty:
// under the round clock amendment a bonded block's header carries D_seal,
// not D, so the header's difficulty is not read), else the chain's step
// from the parent's D over the parent's interval, the value the block at h
// will read once it exists (chainStep is ChainDifficulty's own step, and
// the chain rule's Next without the amendment). One function for the
// submission, the template and the import, so the three cannot diverge.
func (e *Engine) shareChainDifficulty(chain consensus.ChainHeaderReader, parent, at *types.Header) *big.Int {
	if at != nil {
		return e.ChainDifficulty(at)
	}
	var gpTime uint64
	if n := parent.Number.Uint64(); n > 0 {
		if gp := chain.GetHeader(parent.ParentHash, n-1); gp != nil {
			gpTime = gp.Time
		}
	}
	return e.chainStep(e.ChainDifficulty(parent), parent.Number.Uint64(), parent.Time, gpTime)
}

// shareLaneD is the share target's difficulty for height h on coordinate c
// from the share-lane gate (F-690): the lane's controller figure for the
// block at h (vectorDifficulty on the parent at h - 1), the same on every
// node and for every share of the block's lane; nil below the gate, without
// the vector form, or when the parent's state cannot be read (the caller
// then reads the clock's D(h) as before).
func (e *Engine) shareLaneD(chain consensus.ChainHeaderReader, parent *types.Header, h uint64, c uint8) *big.Int {
	if chain == nil || parent == nil || !e.shareLaneOn(h) || e.vectorMenu() == nil {
		return nil
	}
	d, err := e.vectorDifficulty(chain, parent, c)
	if err != nil || d == nil || d.Sign() <= 0 {
		return nil
	}
	return d
}

// shareLaneDForImport is shareLaneD for the IMPORT path (the security review's cell
// on 964cd234f): from the landing check, under the vector form, a lane figure the
// node cannot read is a verification failure of the block (the reason
// named), never a fallback to the clock's D(h), else two nodes would
// verify one carried list against two targets (a silent state split); the
// template and the submit paths may fall back, since they shape this
// node's own work alone. Below the gate, or without the form, (nil, nil):
// the caller keeps the clock's figure.
func (e *Engine) shareLaneDForImport(chain consensus.ChainHeaderReader, parent *types.Header, h uint64, c uint8) (*big.Int, error) {
	if !e.shareLaneOn(h) || e.vectorMenu() == nil {
		return nil, nil
	}
	if d := e.shareLaneD(chain, parent, h, c); d != nil {
		return d, nil
	}
	return nil, fmt.Errorf("igneum shares: the lane's difficulty at %d on coordinate %d could not be read (the share-lane gate)", h, c)
}

// templateChainDifficulty is D(h) of a template (an unsealed header on the
// anchored chain): the step from its parent, never the template's own
// difficulty (D_seal for a bonded template), and nothing cached under the
// template's changing hash.
func (e *Engine) templateChainDifficulty(header *types.Header) *big.Int {
	e.sortition.mu.Lock()
	chain := e.sortition.chain
	e.sortition.mu.Unlock()
	n := header.Number.Uint64()
	if chain == nil || n == 0 {
		return new(big.Int).Set(header.Difficulty)
	}
	parent := chain.GetHeader(header.ParentHash, n-1)
	if parent == nil {
		return new(big.Int).Set(header.Difficulty)
	}
	return e.shareChainDifficulty(chain, parent, nil)
}

// shareWorkFields fills a work package's share fields for a template at
// height on parent: the share target from the template's difficulty, the
// operating count, the height, the parent, and the twin document the share
// is hashed with.
func (e *Engine) shareWorkFields(wp *pow.WorkPackage, header *types.Header) {
	height := header.Number.Uint64()
	if !e.SharesOn(height) {
		return
	}
	// F-690: from the share-lane gate the served target is the lane's
	// (the template's coordinate on its parent), else the clock's D(h).
	d := e.templateChainDifficulty(header)
	if e.shareLaneOn(height) {
		e.sortition.mu.Lock()
		chain := e.sortition.chain
		e.sortition.mu.Unlock()
		if chain != nil && height > 0 {
			if parent := chain.GetHeader(header.ParentHash, height-1); parent != nil {
				if ld := e.shareLaneD(chain, parent, height, e.Coordinate()); ld != nil {
					d = ld
				}
			}
		}
	}
	wp.ShareTarget = pow.Target(e.shareDifficulty(d))
	wp.SharesPerBlock = e.config.Parts.Sortition.SharesPerBlock
	wp.ShareStepMax = e.config.Parts.Sortition.ShareStepMax
	wp.ShareHeight = height
	wp.ShareParent = header.ParentHash
	if u, ok := e.algo.(interface {
		UnkeyedWorkParamsAt(uint64) (any, common.Hash, bool)
	}); ok {
		if params, digest, keyed := u.UnkeyedWorkParamsAt(height); keyed {
			wp.ShareParams, wp.ShareParamsDigest = params, digest
			return
		}
	}
	wp.ShareParams, wp.ShareParamsDigest = wp.Params, wp.ParamsDigest
}
