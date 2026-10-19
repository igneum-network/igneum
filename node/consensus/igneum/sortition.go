// Copyright 2026 Igneum. Research prototype of the sortition-gated rule
// (DRAFT-SORTITION-GATED-POW.md sections 1 to 4 and 7, protocol B, main
// d12e4fc6; the outside review's widened-scope ruling section 2). A PROPOSAL and a
// PROTOTYPE, labelled hybrid proof of work and bonded capital: nothing here
// is adopted, served or part of any launch genesis.
//
// The rule, as the node carries it: from Height every block names a bond
// (BondKey, the authority rule's field), a Round and a Sig; Sig is the
// deterministic Ed25519 signature of the bond's signer over the signing
// hash (every header field but Sig, MixDigest and Nonce: the whole template,
// so a hash provider that changes anything has a template the key did not
// sign). A bond of B_b holds n_b = floor(B_b / u) units, u = B_total / 2^16,
// read at the snapshot (the state at the last block of lottery epoch e - 2,
// e = height / EpochBlocks; genesis for e < 2). Unit j is eligible at height
// h in round r iff lot(V(e), key, j, h) < theta_r, theta_r = 2^256 x
// min(1, E x 2^r / 2^16), V(e) the BLAKE3 of the last SeedBlocks canonical
// hashes of epoch e - 2. A block at round r needs header.Time >= parent.Time
// + r x Delta and a key eligible at some round <= r. Round UnbondedRound
// admits any signer with no bond (liveness). Fork weight is work / 2^r. The
// payout list (section 7) rides the block body's withdrawals list: address
// and weight, at most MaxPayouts entries, its root in withdrawalsRoot with
// the root's first 20 bytes in Coinbase, the subsidy paid pro rata in
// Finalize beside the fund's credit. The signer is separate from the bond:
// the registry's signer slot (rotated by the bond's account; zero means the
// bond key itself is the signer), so a stolen signing key is rotated under
// the bond without losing the bond.

package igneum

import (
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/igneum/shares"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm/igneumreg"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/holiman/uint256"
	"golang.org/x/crypto/sha3"
	"lukechampine.com/blake3"
)

// SortitionParams are the rule's constants from the engine block.
type SortitionParams struct {
	Height        uint64
	EpochBlocks   uint64
	SeedBlocks    uint64
	Expected      uint64
	Delta         uint64
	MaxRound      uint64
	UnbondedRound uint64
	MaxPayouts    uint64
	// EpochChanges are the epoch length's changes by height, ascending
	// (development chains; F-33).
	EpochChanges []EpochChange
	// OpenSeat selects the genesis form (sortition_genesis.go).
	OpenSeat bool
	// EndorseExpected is E_att of the settlement rule (endorse.go; 0 = off)
	// and MaxEndorsements the bound on a block's carried list.
	EndorseExpected uint64
	MaxEndorsements uint64
	// SharesPerBlock is the share lane's operating target n (shares_engine.go;
	// 0 = off) and MaxShares the consensus ceiling on a block's carried
	// shares (at most 256, the list's bound).
	SharesPerBlock uint64
	MaxShares      uint64
	// ShareRecency is the carriage window in blocks (G-23; shares_engine.go
	// ShareRecencyBlocks): at most mineshares.go's entry bound.
	ShareRecency uint64
	// ShareStepMax is PA-53's bound on a share's declared target step
	// (mineshares.go; 0 the count form, 32 at most).
	ShareStepMax uint64
	// The round clock amendment (clock.go): SealWindowDiv (0 = off), T_PI
	// in milliseconds, the seal difficulty's floor, the time floor.
	SealWindowDiv uint64
	TPiMs         uint64
	DMin          *big.Int
	TMin          uint64
	// SealDriftMs is a bonded block's future drift against the node's
	// clock, a chain constant (clock.go verifyDeclaredTime; 500 at launch).
	SealDriftMs uint64
	// TailDelta is the length of every round past round 0 (0: Delta);
	// TailFrom the height the bounded tail applies from on a running chain
	// (the TailV1 fork; 0: from genesis when TailDelta is set), TailTMin
	// the clock floor from then (0: TMin).
	TailDelta uint64
	TailFrom  uint64
	TailTMin  uint64
	// ConstantsAt is the chain-held registration's clock constants at a
	// height (F-151; nil without the vector form): a tMin or tailDelta the
	// registration's constantsChanges name from a height overrides the
	// chain's own and the rule fork's from there, read by every round
	// floor, clock floor and seal measure through TMinAt and RoundStart.
	ConstantsAt func(height uint64) (tMin, tailDelta uint64) `json:"-"`
	// DeltaAt is the engine's hook for the round clock's Delta at a height
	// (the chain's ClockV1 forks entry naming ClockDelta re-pins it from
	// its height, 0.10.49); nil, or zero from it, keeps Delta.
	DeltaAt func(height uint64) uint64 `json:"-"`
	// Rank0FallbackBlocks is K of the seal clock's rank-0 fallback, a
	// chain constant (clock.go; PA-48b; 16 at launch).
	Rank0FallbackBlocks uint64
	// SealStaggerMs is PA-52's stagger as a chain constant the fallback's
	// read subtracts per rank (clock.go; 2,000 at launch).
	SealStaggerMs uint64
}

// Active reports whether the rule binds a block at this height.
func (s *SortitionParams) Active(height uint64) bool { return s != nil && height >= s.Height }

// SortitionFor reads the rule's constants from the engine block; nil when
// the chain carries no rule.
func SortitionFor(cfg *ctypes.IgneumConfig) (*SortitionParams, error) {
	if cfg == nil || cfg.Sortition == nil {
		return nil, nil
	}
	s := cfg.Sortition
	if cfg.Authority == nil {
		return nil, errors.New("igneum: the sortition rule needs the authority rule (the bond registry)")
	}
	if s.Height < cfg.Authority.Height {
		return nil, errors.New("igneum: the sortition rule cannot start before the authority rule")
	}
	if s.EpochBlocks == 0 || s.SeedBlocks == 0 || s.Expected == 0 || s.Delta == 0 || s.MaxRound == 0 || s.UnbondedRound <= s.MaxRound || s.MaxRound > 60 {
		return nil, fmt.Errorf("igneum: sortition parameters incomplete: %+v", *s)
	}
	if s.MaxPayouts == 0 || s.MaxPayouts > 256 {
		return nil, errors.New("igneum: sortition payout list bound must be 1 to 256")
	}
	p := &SortitionParams{Height: s.Height, EpochBlocks: s.EpochBlocks, SeedBlocks: s.SeedBlocks, Expected: s.Expected, Delta: s.Delta, MaxRound: s.MaxRound, UnbondedRound: s.UnbondedRound, MaxPayouts: s.MaxPayouts, OpenSeat: s.OpenSeat}
	// The settlement rule's parameters (endorse.go): E_att and the list's
	// bound, 4 x E_att when unset, at most 1,024 entries.
	p.EndorseExpected = s.EndorseExpected
	p.MaxEndorsements = s.MaxEndorsements
	// The share lane's operating target (shares_engine.go): the list's
	// bound must be the lane's ceiling when the lane is on.
	p.SharesPerBlock = s.SharesPerBlock
	p.MaxShares = s.MaxShares
	if p.SharesPerBlock > 0 {
		// 1 <= sharesPerBlock <= maxShares <= 256 = maxPayouts (protocol
		// A's validation line; maxShares unset reads the ceiling).
		if p.MaxShares == 0 {
			p.MaxShares = shares.Ceiling
		}
		if p.MaxShares > shares.Ceiling || p.SharesPerBlock > p.MaxShares {
			return nil, fmt.Errorf("igneum: the share lane needs 1 <= sharesPerBlock (%d) <= maxShares (%d) <= %d", p.SharesPerBlock, p.MaxShares, shares.Ceiling)
		}
		if p.MaxPayouts != shares.Ceiling {
			return nil, fmt.Errorf("igneum: the share lane needs maxPayouts %d (the ceiling), not %d", shares.Ceiling, p.MaxPayouts)
		}
		// The carriage window (G-23): unset reads the lane's default
		// (ShareRecency, 8); the cap is the lane's own constant
		// MaxShareRecency, 64 (devnet8's value), independent of the
		// carried entry's width: the one-byte parent index holds 0 to 255
		// and the height form any, so either carries a window of 64.
		p.ShareRecency = s.ShareRecency
		if p.ShareRecency == 0 {
			p.ShareRecency = ShareRecency
		}
		if p.ShareRecency > MaxShareRecency {
			return nil, fmt.Errorf("igneum: the share lane's shareRecency (%d) is over the lane's bound (%d)", p.ShareRecency, MaxShareRecency)
		}
		// PA-53's step bound: a declared step's weight is 2^j, so the
		// bound keeps a 256-entry address's weight inside a uint64.
		p.ShareStepMax = s.ShareStepMax
		if p.ShareStepMax > MaxShareStep {
			return nil, fmt.Errorf("igneum: the share lane's shareStepMax (%d) is over the bound (%d)", p.ShareStepMax, MaxShareStep)
		}
	}
	// The round clock amendment (clock.go): the seal window a fraction of
	// the round less the proving allowance; a floor for the seal
	// difficulty; the time floor inside the round.
	p.SealWindowDiv, p.TPiMs, p.TMin, p.SealDriftMs = s.SealWindowDiv, s.TPiMs, s.TMin, s.SealDriftMs
	p.TailDelta = s.TailDelta
	p.Rank0FallbackBlocks, p.SealStaggerMs = s.Rank0FallbackBlocks, s.SealStaggerMs
	if s.DMin != nil {
		p.DMin = new(big.Int).Set(s.DMin)
	}
	if p.SealWindowDiv > 0 {
		if p.TPiMs == 0 {
			p.TPiMs = 250
		}
		if p.SealDriftMs == 0 {
			p.SealDriftMs = DefaultSealDriftMs
		}
		if p.Rank0FallbackBlocks == 0 {
			p.Rank0FallbackBlocks = DefaultRank0FallbackBlocks
		}
		if p.SealStaggerMs == 0 {
			p.SealStaggerMs = DefaultSealStaggerMs
		}
		if p.DMin == nil || p.DMin.Sign() <= 0 {
			return nil, errors.New("igneum: the clock amendment needs dMin above zero")
		}
		// The seal window (Delta - tMin) x 1000 - tPiMs must be positive:
		// the search starts at the time floor and must end before the
		// round-1 floor (PA-48's target term).
		if p.TMin*1000+p.TPiMs >= p.Delta*1000 {
			return nil, fmt.Errorf("igneum: the clock amendment needs tMin (%d s) plus tPiMs (%d ms) under Delta (%d s): the seal window", p.TMin, p.TPiMs, p.Delta)
		}
		if p.TMin > p.Delta {
			return nil, fmt.Errorf("igneum: the clock amendment needs tMin (%d) at or under Delta (%d)", p.TMin, p.Delta)
		}
		// The bounded tail: a round past round 0 must hold the proof
		// budget and a search (TailDelta x 1000 over tPiMs), and be at
		// most Delta (the tail comes faster, never slower).
		if p.TailDelta > 0 && (p.TailDelta*1000 <= p.TPiMs || p.TailDelta > p.Delta) {
			return nil, fmt.Errorf("igneum: the bounded tail needs tailDelta (%d s) over tPiMs (%d ms) and at most Delta (%d s)", p.TailDelta, p.TPiMs, p.Delta)
		}
		if !s.OpenSeat {
			return nil, errors.New("igneum: the clock amendment needs the genesis form (openSeat)")
		}
	}
	if p.EndorseExpected > 0 {
		if p.MaxEndorsements == 0 {
			p.MaxEndorsements = 4 * p.EndorseExpected
		}
		if p.MaxEndorsements > 1024 || p.EndorseExpected > 65536 {
			return nil, errors.New("igneum: the endorsement bound must be 1 to 1,024 entries and E_att at most 65,536")
		}
		if !s.OpenSeat {
			return nil, errors.New("igneum: the settlement rule (endorseExpected) needs the genesis form (openSeat)")
		}
	}
	if s.OpenSeat && !igneumreg.Native(cfg.Authority.RegistryVersion) {
		return nil, errors.New("igneum: the genesis form (openSeat) needs the native registry (registryVersion 3 or 4)")
	}
	if s.OpenSeat && len(s.EpochChanges) > 0 {
		return nil, errors.New("igneum: the genesis form carries one epoch length (no epochChanges)")
	}
	var last uint64
	for _, c := range s.EpochChanges {
		if c.EpochBlocks == 0 || c.Height <= last || c.Height < s.Height {
			return nil, fmt.Errorf("igneum: sortition epoch changes must be ascending, above the rule's height, with a positive length: %+v", s.EpochChanges)
		}
		last = c.Height
		p.EpochChanges = append(p.EpochChanges, EpochChange{Height: c.Height, EpochBlocks: c.EpochBlocks})
	}
	return p, nil
}

// Errors of the rule.
var (
	errSortitionEarly     = errors.New("igneum sortition: round or signature before the rule's height")
	errSortitionSig       = errors.New("igneum sortition: signature missing or malformed")
	errSortitionBadSig    = errors.New("igneum sortition: signature does not verify under the bond's signer")
	errSortitionRound     = errors.New("igneum sortition: round above the unbonded round")
	errSortitionEarlyTime = errors.New("igneum sortition: block time under the round's floor")
	errSortitionNotDrawn  = errors.New("igneum sortition: key not eligible at this round")
	errSortitionNoChain   = errors.New("igneum sortition: no chain state to read the snapshot from")
	errSortitionNoSigner  = errors.New("igneum sortition: this node has no signing key")
	errSortitionWait      = errors.New("igneum sortition: not yet eligible at a round whose time floor has passed")
)

// IsTemplateWait says whether a Prepare error is a WAIT and not a fault:
// the drawn round's floor or the carry bound not yet reached (the worker
// asks again at the next tick), or a node without a bond key, which seals
// nothing by design (A9, the readers, the probe points). The worker logs
// these once per height at Info and per tick at Debug; every other error
// stays at Error (the project's reading of 05:00 UK, 12 October 2026: 2,955
// "Failed to prepare header" lines on a sealer and one every two seconds
// on every non-sealing node, all this class).
func IsTemplateWait(err error) bool {
	return errors.Is(err, errSortitionWait) || errors.Is(err, errBondKeyUnset)
}

var (
	errPayoutList         = errors.New("igneum sortition: payout list invalid")
)

// Domains of the lottery's hashes (the node's form; the draft's tag()).
var (
	sortitionSeedDomain = []byte("IgneumHash/R0A/sortition")
	sortitionLotDomain  = []byte("IgneumHash/R0A/lot")
)

// stateAtReader is the chain's state access the rule needs (the blockchain).
type stateAtReader interface {
	StateAt(root common.Hash) (*state.StateDB, error)
}

// epochSnapshot is one lottery epoch's fixed inputs.
type epochSnapshot struct {
	epoch uint64
	seed  common.Hash
	root  common.Hash // the snapshot state's root
	block uint64      // the snapshot block
}

// sortitionState is the engine's per-epoch cache.
type sortitionState struct {
	mu    sync.Mutex
	cache map[uint64]*epochSnapshot
	chain consensus.ChainHeaderReader
	// chainD caches the chain's difficulty D(h) by block hash under the
	// round clock amendment (clock.go ChainDifficulty), where a bonded
	// block's header carries D_seal and D is derived from the headers.
	chainD map[common.Hash]*big.Int
}

func (e *Engine) sortitionAnchor(chain consensus.ChainHeaderReader) {
	if chain == nil {
		return
	}
	e.sortition.mu.Lock()
	e.sortition.chain = chain
	e.sortition.mu.Unlock()
}

// signerSlot is the registry slot of a bond's signer: KECCAK256(key || 0x01).
func signerSlot(key common.Hash) common.Hash {
	return crypto.Keccak256Hash(key[:], []byte{1})
}

// SignerOf reads a bond's signer at a state: the signer slot, or the bond
// key itself when none is set.
func (a *AuthorityParams) SignerOf(st *state.StateDB, key common.Hash) common.Hash {
	if s := st.GetState(a.Registry, signerSlot(key)); s != (common.Hash{}) {
		return s
	}
	return key
}

// SigningHash is KECCAK(RLP(header without Sig, MixDigest and Nonce)): what
// the bond's signer signs (the seal hash's fields with Round, without Sig).
func (e *Engine) SigningHash(header *types.Header) (hash common.Hash) {
	hasher := sha3.NewLegacyKeccak256()
	enc := e.sealFields(header)
	enc = append(enc, header.Round)
	rlp.Encode(hasher, enc)
	hasher.Sum(hash[:0])
	return hash
}

// snapshotFor returns the lottery epoch's seed and snapshot state root for a
// height, from the cache or the chain.
func (e *Engine) snapshotFor(height uint64) (*epochSnapshot, error) {
	s := e.config.Parts.Sortition
	regime, epochBlocks := s.regimeAt(height)
	epoch := height / epochBlocks
	cacheKey := epoch | uint64(regime)<<48
	e.sortition.mu.Lock()
	chain := e.sortition.chain
	if snap, ok := e.sortition.cache[cacheKey]; ok {
		e.sortition.mu.Unlock()
		return snap, nil
	}
	e.sortition.mu.Unlock()
	if chain == nil {
		return nil, errSortitionNoChain
	}
	var (
		block  uint64
		hashes []common.Hash
	)
	if epoch < 2 {
		// The first two epochs have no epoch e - 2: the snapshot is the
		// state just before the rule's height (bonds posted before the
		// rule count from its first block), the seed the hashes of the
		// SeedBlocks blocks ending there. Deterministic and the same on
		// every node; a research-chain convenience, not the draft's text.
		block = s.Height - 1
		lo := uint64(0)
		if block+1 > s.SeedBlocks {
			lo = block + 1 - s.SeedBlocks
		}
		for n := lo; n <= block; n++ {
			h := chain.GetHeaderByNumber(n)
			if h == nil {
				return nil, fmt.Errorf("igneum sortition: snapshot block %d unknown", n)
			}
			hashes = append(hashes, h.Hash())
		}
	} else {
		first := (epoch - 2) * epochBlocks
		block = first + epochBlocks - 1
		lo := first
		if block+1 > s.SeedBlocks && block+1-s.SeedBlocks > lo {
			lo = block + 1 - s.SeedBlocks
		}
		for n := lo; n <= block; n++ {
			h := chain.GetHeaderByNumber(n)
			if h == nil {
				return nil, fmt.Errorf("igneum sortition: snapshot block %d unknown", n)
			}
			hashes = append(hashes, h.Hash())
		}
	}
	root := chain.GetHeaderByNumber(block)
	if root == nil {
		return nil, errSortitionNoChain
	}
	hs := blake3.New(32, nil)
	hs.Write(sortitionSeedDomain)
	var le [8]byte
	binary.LittleEndian.PutUint64(le[:], epoch)
	hs.Write(le[:])
	for _, h := range hashes {
		hs.Write(h[:])
	}
	snap := &epochSnapshot{epoch: epoch, root: root.Root, block: block}
	hs.Sum(snap.seed[:0])
	e.sortition.mu.Lock()
	if e.sortition.cache == nil {
		e.sortition.cache = make(map[uint64]*epochSnapshot)
	}
	e.sortition.cache[cacheKey] = snap
	e.sortition.mu.Unlock()
	return snap, nil
}

// EpochChange is a change of the lottery's epoch length at a height
// (security's F-33: a consensus parameter changes only at a height, never
// by a restart, or a fresh node cannot replay the chain). From Height the
// epoch index is height / EpochBlocks under the new length (the first two
// indices under any length snapshot at the rule's height, as the chain that
// changed on a restart did), so the history before the height replays under
// the old length and the history after it under the new.
type EpochChange struct {
	Height      uint64
	EpochBlocks uint64
}

// regimeAt returns the index of the epoch-length regime a height is in and
// its epoch length (index 0: the document's EpochBlocks).
func (s *SortitionParams) regimeAt(height uint64) (int, uint64) {
	regime, blocks := 0, s.EpochBlocks
	for i, c := range s.EpochChanges {
		if height >= c.Height {
			regime, blocks = i+1, c.EpochBlocks
		}
	}
	return regime, blocks
}

// EpochAt is the lottery epoch index of a height under its regime.
func (s *SortitionParams) EpochAt(height uint64) uint64 {
	_, blocks := s.regimeAt(height)
	return height / blocks
}

// Lot is x(b, j, h) = BLAKE3(domain || V || K_b || LE64(j) || LE64(h)).
func Lot(seed, key common.Hash, unit, height uint64) common.Hash {
	// The one-shot form (security's S13 profile, the project's order of 18:1x UK,
	// 11 October 2026: the streaming hasher's 2 KB per call put 57 percent
	// of the campaign's CPU in allocation and the collector): the same
	// bytes in a stack buffer, blake3.Sum256 over it, the output
	// byte-identical to the streaming form (lotStreaming; TestLotOneShot
	// holds them equal over 10,000 random inputs). The domain is the plain
	// 18-byte ASCII "IgneumHash/R0A/lot" (no length prefix, as the
	// streaming form wrote it), the message 18 + 32 + 32 + 8 + 8 = 98
	// bytes.
	var msg [lotMessageLen]byte
	n := copy(msg[:], sortitionLotDomain)
	n += copy(msg[n:], seed[:])
	n += copy(msg[n:], key[:])
	binary.LittleEndian.PutUint64(msg[n:], unit)
	n += 8
	binary.LittleEndian.PutUint64(msg[n:], height)
	n += 8
	return common.Hash(blake3.Sum256(msg[:n]))
}

// lotMessageLen is the lot message's length: the domain, the seed, the
// key, LE64(unit), LE64(height).
const lotMessageLen = len("IgneumHash/R0A/lot") + 32 + 32 + 8 + 8

// lotStreaming is the lot's former streaming form, kept for the equality
// test and the record; never called by the rule.
func lotStreaming(seed, key common.Hash, unit, height uint64) common.Hash {
	var buf [8]byte
	hs := blake3.New(32, nil)
	hs.Write(sortitionLotDomain)
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

// Theta is theta_r = floor(2^256 x min(1, E x 2^r / 2^16)) as a big integer
// (2^256 when every unit is eligible).
func Theta(expected, round uint64) *big.Int {
	n := new(big.Int).Lsh(big.NewInt(int64(expected)), uint(round))
	if n.Cmp(big.NewInt(1<<16)) >= 0 {
		return new(big.Int).Lsh(big.NewInt(1), 256)
	}
	return n.Lsh(n, 240)
}

// Units is n_b = floor(B_b / u), u = B_total / 2^16, B_total the registry's
// balance at the snapshot (every bond's value sits in the registry).
func (a *AuthorityParams) Units(st *state.StateDB, key common.Hash) (units uint64, bond, total *big.Int) {
	bond = a.BondOf(st, key).Amount
	total = st.GetBalance(a.Registry).ToBig()
	u := new(big.Int).Rsh(total, 16)
	if u.Sign() == 0 || bond.Sign() == 0 {
		return 0, bond, total
	}
	n := new(big.Int).Div(bond, u)
	if !n.IsUint64() || n.Uint64() > 1<<16 {
		return 1 << 16, bond, total
	}
	return n.Uint64(), bond, total
}

// EligibleRound is the smallest round a key with n units is eligible at for
// a height under a seed: the round whose theta exceeds the key's smallest
// lot; MaxRound + 1 when no unit is drawn by MaxRound (n = 0 included).
func (s *SortitionParams) EligibleRound(seed, key common.Hash, units, height uint64) uint64 {
	if units == 0 {
		return s.MaxRound + 1
	}
	var minLot *big.Int
	for j := uint64(0); j < units; j++ {
		x := Lot(seed, key, j, height)
		xi := new(big.Int).SetBytes(x[:])
		if minLot == nil || xi.Cmp(minLot) < 0 {
			minLot = xi
		}
	}
	for r := uint64(0); r <= s.MaxRound; r++ {
		if minLot.Cmp(Theta(s.Expected, r)) < 0 {
			return r
		}
	}
	return s.MaxRound + 1
}

// SetSigner names the Ed25519 seed this node signs under (nil: none).
func (e *Engine) SetSigner(seed []byte) error {
	if seed == nil {
		e.lock.Lock()
		e.signer = nil
		e.lock.Unlock()
		return nil
	}
	if len(seed) != ed25519.SeedSize {
		return errors.New("igneum sortition: a 32-byte seed is needed")
	}
	e.lock.Lock()
	e.signer = ed25519.NewKeyFromSeed(seed)
	// The keyed form: the seed is also the bond's secret sk, and the bond
	// key it derives is pk = G(sk) (core/vm/igneumreg.BondKeyOf). Which of
	// the two keys the header carries is the operator's --dev.bondkey
	// (the weak form names the Ed25519 public key, the keyed form names
	// pk); the owner's check in verifyKeyedSeal binds to pk.
	var sk [32]byte
	copy(sk[:], seed)
	e.bondSK = &sk
	pk := igneumreg.BondKeyOf(sk)
	e.keyedBond = &pk
	e.lock.Unlock()
	// F-55: the process holding the secret never dumps a core and the
	// secret's page never swaps (secretguard_linux.go).
	warn, err := guardSecretProcess(e.bondSK[:])
	if err != nil {
		return err
	}
	if warn != nil {
		e.log.Warn("Bond secret's process guard incomplete", "warn", warn)
	} else {
		e.log.Info("Bond secret's process guarded (core limit 0, not dumpable, the secret locked in memory)")
	}
	return nil
}

// bondSecret returns this node's bond secret and the keyed bond key it
// derives (nil without a seed).
func (e *Engine) bondSecret() (*[32]byte, *common.Hash) {
	e.lock.Lock()
	defer e.lock.Unlock()
	if e.bondSK == nil {
		return nil, nil
	}
	sk := *e.bondSK
	pk := *e.keyedBond
	return &sk, &pk
}

// KeyedBondKey is pk = G(seed) for this node's seed (nil without one): the
// bond key a keyed chain registers and the header carries.
func (e *Engine) KeyedBondKey() *common.Hash {
	_, pk := e.bondSecret()
	return pk
}

// SignerKey is the public key this node signs under (nil without one).
func (e *Engine) SignerKey() *common.Hash {
	e.lock.Lock()
	defer e.lock.Unlock()
	if e.signer == nil {
		return nil
	}
	k := common.BytesToHash(e.signer.Public().(ed25519.PublicKey))
	return &k
}

// prepareSortition chooses this node's round for a block it assembles: the
// smallest round its key is eligible at whose time floor has passed, or an
// error that makes the worker try again at its next commit.
func (e *Engine) prepareSortition(chain consensus.ChainHeaderReader, header, parent *types.Header) error {
	s := e.config.Parts.Sortition
	height := header.Number.Uint64()
	if !s.Active(height) {
		header.Round, header.Sig = 0, nil
		return nil
	}
	e.sortitionAnchor(chain)
	if e.SignerKey() == nil {
		return errSortitionNoSigner
	}
	key := header.BondKey
	if key == nil {
		return errBondKeyUnset
	}
	if e.authorityLax {
		// The TEST HARNESS's non-compliant sealer: round 0 regardless of the
		// draw and the floor, signed by whatever seed it holds, so compliant
		// nodes refuse its re-mined blocks at the signer or the draw (the
		// rejection suite's rows behind the seal).
		header.Round, header.Sig = 0, nil
		return nil
	}
	r, open, st, err := e.roundFor(chain, parent, height, *key)
	if err != nil {
		return err
	}
	if open && s.OpenSeat && st != nil && e.config.Parts.Authority != nil {
		// F-179 (the public test network's stall at 508, 15 October 2026):
		// the open seat is never sealed under a BONDED signer key. The
		// open-seat template names this node's signer key as the header's
		// key; when that key holds units in the table (the bonds posted on
		// the sealers' signer keys rather than their bond keys), the
		// verifier reads the block as the bonded key's and refuses it
		// below the key's own round, so a template passed only when the
		// open seat's round happened to be at or above it and the chain
		// stood at 508 for good. The node draws as the bonded signer key
		// and seals the keyed template at its round.
		if sk, r2, ok := e.bondedSignerDraw(st, *key, height); ok {
			r, open, key = r2, false, sk
			header.BondKey = sk
		}
	}
	if !open && r < s.UnbondedRound && st != nil && e.config.Parts.Authority != nil {
		// F-52 (the security review, 12:5x UK, 12 October 2026): a bonded key whose
		// registered signer at the parent's state is not this node's
		// signing key cannot sign the keyed template it drew (the
		// rotation is pending, or another node holds the signer), and a
		// sealer that assembles it seals nothing. Until the registry's
		// signer is this node's, the node seals as an unbonded node does:
		// the open seat under its own signer key (which has no units, so
		// the draw below is the open seat's and the template is unkeyed
		// by sealIsKeyed). No verifier rule moves: the verifier never
		// read a sealer's intent, only its block.
		if signer := e.config.Parts.Authority.SignerOf(st, *key); signer != *e.SignerKey() {
			sk := e.SignerKey()
			r2, open2, st2, err2 := e.roundFor(chain, parent, height, *sk)
			if err2 != nil {
				// The open-seat draw itself failed (no state, no table): the
				// keyed draw it cannot sign is kept and this height is dark,
				// said as its own line (the security review's note), never silent.
				e.log.Warn("Keyed draw held and the open-seat draw failed: this height seals nothing", "number", height, "bondKey", key, "registrySigner", signer, "err", err2)
				return fmt.Errorf("%w: the held draw's open seat: %v", errSortitionWait, err2)
			}
			e.heldKeyedDraw.log(e.log, height, *key, signer, *sk)
			// the key below (the stagger rank and anything else that reads it)
			// is the key the block is sealed under: the signer key, exactly
			// as an unbonded node ranks (the protocol team's read)
			r, open, st, key = r2, open2, st2, sk
		}
	}
	if open {
		// The open seat (or a key not yet in the table): the block is
		// signed under this node's Ed25519 key and names it as the
		// header's key, so the signature verifies under the header
		// (the bond key pk = G(sk) is not a signing key; a bonded key
		// registers its signer in the registry and seals under pk).
		sk := e.SignerKey()
		if sk == nil {
			return errSortitionNoSigner
		}
		header.BondKey = sk
	}
	floor := s.sealerFloor(parent, r, e.staggerRank(st, *key, height, r), e.SealStaggerMs())
	if open {
		// F-114's open-seat spread (stagger.go): the search start by the
		// slot of (parent hash, the signer key the block is sealed under).
		floor += openSpread(parent.Hash(), *header.BondKey, e.OpenSpreadMs())
	}
	if header.Time < floor {
		return fmt.Errorf("%w: round %d from time %d, template time %d", errSortitionWait, r, floor, header.Time)
	}
	// THE CARRY (F-44 part 2a, the design seat's ruling v3 of 02:4x UK,
	// 12 October 2026, main ea0bfef6): a sealer assembles the child of h
	// (and starts its keyed search) no earlier than the moment its pool
	// holds at least ceil(2 x E_att / 3) endorsements of h, so the honest
	// children carry the public block's endorsements and the branch's
	// weight (D x Endorsed, sortition.go BlockWeight) stands against a
	// withholder's reveal whose own child carries only its own endorsers'.
	// Bounded by the sealer's OWN round window's end less the proof time
	// and the search target (the design seat's correction of 01:2x UK:
	// round r's window ends at parent.Time + (r + 1) x Delta, so the
	// search always has its full target inside the window and no seat is
	// lost when the endorsements never come; a bound past the window
	// would start the round-0 search inside round 1's time and every such
	// seal would read as an overrun on the clock rows). Sealer policy: no
	// wire change, no parameter, no verifier rule. The line names held
	// and the wait past the floor, so the clock rows attribute a start
	// shift to the carry.
	// The quorum against the bonded share of the epoch's table the draw
	// above read (carryQuorumAt: 16 on devnet9's 0.75 share, 22 near one;
	// zero in an epoch whose table holds no units), scaled to the chain's
	// own supply of carried endorsements over the last window
	// (carrySupplyQuorum: no supply, no wait).
	if need := e.carrySupplyQuorum(chain, st, parent, height); need > 0 {
		held := e.endorsementsHeld(parent)
		if held < need {
			target := e.sealParamsAt(height, e.sealMeasureFirstForm(height)).Target
			tPiMs, _ := e.clockConstsAt(height)
			bound := parent.Time + s.RoundStart(height, r+1) - (tPiMs+target+999)/1000
			if header.Time < bound {
				return fmt.Errorf("%w: round %d, the pool holds %d of %d endorsements of the parent, carry bound %d, template time %d", errSortitionWait, r, held, need, bound, header.Time)
			}
			e.log.Info("Template assembled at the carry's bound", "number", height, "round", r, "held", held, "need", need, "waitSec", header.Time-floor)
		} else if header.Time > floor {
			e.log.Debug("Template assembled under the carry", "number", height, "round", r, "held", held, "need", need, "waitSec", header.Time-floor)
		}
	}
	header.Round = r
	header.Sig = nil
	return nil
}

// bondedSignerDraw (F-179) is the signer key's draw where this node's bond
// key draws the open seat but its signer key holds units in the table at
// the state: the key the template is sealed under and its round, ok true;
// ok false where the signer key is the bond key, unbonded, marked, or
// above the unbonded round (the open seat as before).
func (e *Engine) bondedSignerDraw(st *state.StateDB, bondKey common.Hash, height uint64) (*common.Hash, uint64, bool) {
	s := e.config.Parts.Sortition
	sk := e.SignerKey()
	if sk == nil || *sk == bondKey || st == nil {
		return nil, 0, false
	}
	d, err := e.drawAt(st, *sk, height)
	if err != nil || d.open || d.units == 0 {
		return nil, 0, false
	}
	r := d.round
	if r > s.MaxRound {
		r = s.UnbondedRound
	}
	e.bondedSigner.log(e.log, height, bondKey, *sk, d.units, r)
	return sk, r, true
}

// bondedSignerDraw logs the signer key's draw taken for an open bond key
// once per height (F-179).
type bondedSignerDraw struct {
	mu     sync.Mutex
	height uint64
}

func (b *bondedSignerDraw) log(logger log.Logger, height uint64, bondKey, signer common.Hash, units, round uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.height == height {
		return
	}
	b.height = height
	logger.Warn("Bond key open but the signer key is bonded: sealing as the signer key at its round (F-179)", "number", height, "bondKey", bondKey, "signer", signer, "units", units, "round", round)
}

// heldDraw logs a held keyed draw once per height (F-52).
type heldDraw struct {
	mu     sync.Mutex
	height uint64
}

func (h *heldDraw) log(logger log.Logger, height uint64, key, signer, own common.Hash) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.height == height {
		return
	}
	h.height = height
	logger.Warn("Keyed draw held: the registry's signer for this node's bond is not its signing key; sealing the open seat until the rotation lands", "number", height, "bondKey", key, "registrySigner", signer, "signingKey", own)
}

// roundFor is the sealer's draw for key at height: the eligible round
// (the unbonded round above MaxRound), whether it is the open seat's, and
// the parent state the draw was read at (the genesis form reads the table
// in the parent's state; the snapshot form the epoch snapshot's).
func (e *Engine) roundFor(chain consensus.ChainHeaderReader, parent *types.Header, height uint64, key common.Hash) (r uint64, open bool, st *state.StateDB, err error) {
	s := e.config.Parts.Sortition
	sr, ok := chain.(stateAtReader)
	if !ok {
		return 0, false, nil, errSortitionNoChain
	}
	if s.OpenSeat {
		// The genesis form: the draw from the table in the parent's state;
		// a key without units takes the open seat's round.
		st, err = sr.StateAt(parent.Root)
		if err != nil {
			return 0, false, nil, err
		}
		d, err := e.drawAt(st, key, height)
		if err != nil {
			return 0, false, nil, err
		}
		r, open = d.round, d.open
	} else {
		snap, err := e.snapshotFor(height)
		if err != nil {
			return 0, false, nil, err
		}
		st, err = sr.StateAt(snap.root)
		if err != nil {
			return 0, false, nil, err
		}
		units, _, _ := e.config.Parts.Authority.Units(st, key)
		r = s.EligibleRound(snap.seed, key, units, height)
	}
	if r > s.MaxRound {
		r = s.UnbondedRound
	}
	return r, open, st, nil
}

// RoundStart is the offset in seconds from the parent's time at which round
// r opens: r x Delta under the single round length, and under the bounded
// tail (TailDelta > 0) Delta + (r - 1) x TailDelta for r at or above 1,
// so the rounds past the target come faster and the seal chance, which
// doubles a round (Theta), rises with the gap.
func (s *SortitionParams) RoundStart(height, r uint64) uint64 {
	if r == 0 {
		return 0
	}
	delta := s.DeltaAt_(height)
	if _, td := s.registeredAt(height); td > 0 {
		// F-151: the registration's tail round length from its height.
		return delta + (r-1)*td
	}
	if !s.tailAt(height) {
		return r * delta
	}
	return delta + (r-1)*s.TailDelta
}

// DeltaAt_ is the round clock's Delta at a height: the DeltaAt hook's
// figure where it names one (the forks entry's ClockDelta from ClockV1),
// else Delta.
func (s *SortitionParams) DeltaAt_(height uint64) uint64 {
	if s.DeltaAt != nil {
		if d := s.DeltaAt(height); d > 0 {
			return d
		}
	}
	return s.Delta
}

// registeredAt is the chain-held registration's clock constants at a
// height (F-151), zero without a registration or an entry naming them.
func (s *SortitionParams) registeredAt(height uint64) (tMin, tailDelta uint64) {
	if s.ConstantsAt == nil {
		return 0, 0
	}
	return s.ConstantsAt(height)
}

// tailAt reports whether the bounded tail is in force at a height.
func (s *SortitionParams) tailAt(height uint64) bool {
	if s.TailDelta == 0 {
		return false
	}
	return s.TailFrom == 0 || height >= s.TailFrom
}

// TMinAt is the clock floor at a height: the registration's tMin from a
// constantsChanges height (F-151), else TailTMin under the bounded tail
// where it is set, else TMin.
func (s *SortitionParams) TMinAt(height uint64) uint64 {
	if t, _ := s.registeredAt(height); t > 0 {
		return t
	}
	if s.tailAt(height) && s.TailTMin > 0 {
		return s.TailTMin
	}
	return s.TMin
}

// sealerFloor is the template's earliest time for the drawn round r: PA-48's
// sealer half (the project's ruling of 16:4x UK, 11 October 2026), the SEARCH
// START max(the round floor parent.Time + r x Delta, parent.Time + tMin),
// the same start ClockNext's seal step measures from, so a round-0 sealer
// under the clock amendment searches from the time floor and never
// submits a template the floor refuses; plus PA-52's stagger (the project's
// order of 20:2x UK, 11 October 2026): at a round where several keys are
// eligible, the least-lot key starts at the round's start and each next
// rank waits one stagger more (whole seconds, the header's unit; 0 off).
// No consensus change: the verifier's floors stay the round's and tMin's.
func (s *SortitionParams) sealerFloor(parent *types.Header, r uint64, rank int, staggerMs uint64) uint64 {
	floor := parent.Time + s.RoundStart(parent.Number.Uint64()+1, r)
	if t := parent.Time + s.TMinAt(parent.Number.Uint64()+1); t > floor {
		floor = t
	}
	if rank > 0 && staggerMs > 0 {
		floor += (uint64(rank)*staggerMs + 999) / 1000
	}
	return floor
}

// signHeader sets Sig over the signing hash (the final template).
func (e *Engine) signHeader(header *types.Header) error {
	e.lock.Lock()
	signer := e.signer
	e.lock.Unlock()
	if signer == nil {
		return errSortitionNoSigner
	}
	h := e.SigningHash(header)
	header.Sig = ed25519.Sign(signer, h[:])
	return nil
}

// verifySortitionHeader is the stateless part: fields by height, the round
// bound, the time floor, the signature's shape.
func (s *SortitionParams) verifySortitionHeader(header, parent *types.Header) error {
	height := header.Number.Uint64()
	if !s.Active(height) {
		if header.Round != 0 || len(header.Sig) != 0 {
			return errSortitionEarly
		}
		return nil
	}
	if len(header.Sig) != ed25519.SignatureSize {
		return errSortitionSig
	}
	if header.BondKey == nil || *header.BondKey == (common.Hash{}) {
		return errBondKeyZero
	}
	if header.Round > s.UnbondedRound {
		return errSortitionRound
	}
	if parent != nil && s.TMinAt(height) > 0 && header.Time < parent.Time+s.TMinAt(height) {
		// The round clock amendment's time floor (clock.go): every block at
		// least T_min after its parent, whatever its round.
		return fmt.Errorf("%w: floor %d, time %d", errUnderTimeFloor, parent.Time+s.TMinAt(height), header.Time)
	}
	if parent != nil && header.Time < parent.Time+s.RoundStart(height, header.Round) {
		return fmt.Errorf("%w: round %d, floor %d, time %d", errSortitionEarlyTime, header.Round, parent.Time+s.RoundStart(height, header.Round), header.Time)
	}
	return nil
}

// verifySortition is the stateful part: the signer (at the parent's state,
// so a rotation takes effect at once), the signature, and the draw at the
// snapshot. An unbonded-round block needs only the signature under its key.
func (e *Engine) verifySortition(header *types.Header, parentState *state.StateDB) error {
	s := e.config.Parts.Sortition
	a := e.config.Parts.Authority
	height := header.Number.Uint64()
	if !s.Active(height) {
		return nil
	}
	key := *header.BondKey
	if s.OpenSeat {
		return e.verifySortitionGenesis(header, parentState, true)
	}
	signer := key
	if header.Round < s.UnbondedRound {
		signer = a.SignerOf(parentState, key)
	}
	h := e.SigningHash(header)
	if !ed25519.Verify(ed25519.PublicKey(signer[:]), h[:], header.Sig) {
		return errSortitionBadSig
	}
	if header.Round >= s.UnbondedRound {
		return nil
	}
	snap, err := e.snapshotFor(height)
	if err != nil {
		return err
	}
	e.sortition.mu.Lock()
	chain := e.sortition.chain
	e.sortition.mu.Unlock()
	sr, ok := chain.(stateAtReader)
	if !ok {
		return errSortitionNoChain
	}
	st, err := sr.StateAt(snap.root)
	if err != nil {
		return err
	}
	units, _, _ := a.Units(st, key)
	if r := s.EligibleRound(snap.seed, key, units, height); r > header.Round {
		return fmt.Errorf("%w: eligible from round %d, sealed at %d (%d units)", errSortitionNotDrawn, r, header.Round, units)
	}
	return nil
}

// BlockWeight is the fork weight of a block: its work divided by 2^round
// (core adds it to the parent's total in place of the difficulty).
func (e *Engine) BlockWeight(header *types.Header) *big.Int {
	s := e.config.Parts.Sortition
	if !s.Active(header.Number.Uint64()) {
		return new(big.Int).Set(header.Difficulty)
	}
	// The weight's D is the CHAIN's difficulty (the project's ruling of 14:0x UK
	// on the round clock amendment, clock.go): under the amendment a
	// bonded block's header carries D_seal, so D is derived from the
	// headers (ChainDifficulty) and a bonded block and an open-seat block
	// at one round weigh the same; without the amendment the header's
	// difficulty is D.
	d := e.ChainDifficulty(header)
	w := new(big.Int).Set(d)
	if header.Round > 0 {
		w.Rsh(w, uint(header.Round))
		if w.Sign() == 0 {
			w.SetInt64(1)
		}
	}
	// The settlement rule (endorse.go): the parent's endorsements this block
	// carries weigh one round-0 block each, at the chain's difficulty.
	if header.Endorsed > 0 {
		w.Add(w, new(big.Int).Mul(d, new(big.Int).SetUint64(header.Endorsed)))
	}
	return w
}

// AllowsPayoutList reports whether a block at this header may carry the
// payout list (the body's withdrawals list) under the rule.
func (e *Engine) AllowsPayoutList(header *types.Header) bool {
	return e.config.Parts.Sortition.Active(header.Number.Uint64())
}

// PayoutCoinbase is the coinbase a payout list binds: the first 20 bytes of
// the list's root.
func PayoutCoinbase(root common.Hash) common.Address {
	return common.BytesToAddress(root[:20])
}

// verifyPayoutList checks a block's list against the header and the bound.
func (e *Engine) verifyPayoutList(header *types.Header, withdrawals []*types.Withdrawal) error {
	s := e.config.Parts.Sortition
	if header.WithdrawalsHash == nil {
		if len(withdrawals) > 0 {
			return fmt.Errorf("%w: list without a root", errPayoutList)
		}
		return nil
	}
	if !s.Active(header.Number.Uint64()) {
		return errUnsupportedWithdraw
	}
	if len(withdrawals) == 0 || uint64(len(withdrawals)) > s.MaxPayouts {
		return fmt.Errorf("%w: %d entries, bound %d", errPayoutList, len(withdrawals), s.MaxPayouts)
	}
	// F-184 (the research chain's stall at 6665, 15 October 2026): under the
	// first-form fold (sealerFoldListFrom) the list's LAST entry is the
	// sealer's name at weight 0 (F-180's form), the one zero weight the
	// list may carry; every other entry's weight is above zero as before.
	fold := e.sealerFoldListAt(header.Number.Uint64())
	for i, w := range withdrawals {
		if w.Amount == 0 && !(fold && i == len(withdrawals)-1) {
			return fmt.Errorf("%w: zero weight", errPayoutList)
		}
	}
	if header.Coinbase != PayoutCoinbase(*header.WithdrawalsHash) {
		return fmt.Errorf("%w: coinbase is not the list root's prefix", errPayoutList)
	}
	return nil
}

// payProRata credits the miner amount to the list by weight; the remainder
// of the integer division goes to the last entry with weight (F-184: a
// zero-weight entry, the sealer's name under the first-form fold, takes
// nothing and never the remainder, as shares.Pay settles it on the sealer's
// own path, so the sealer and an importer credit the same wei).
func payProRata(st *state.StateDB, amount *big.Int, withdrawals []*types.Withdrawal) {
	total := new(big.Int)
	last := -1
	for i, w := range withdrawals {
		total.Add(total, new(big.Int).SetUint64(w.Amount))
		if w.Amount > 0 {
			last = i
		}
	}
	if total.Sign() == 0 {
		return
	}
	paid := new(big.Int)
	for i, w := range withdrawals {
		if w.Amount == 0 {
			continue
		}
		share := new(big.Int).Mul(amount, new(big.Int).SetUint64(w.Amount))
		share.Div(share, total)
		if i == last {
			share = new(big.Int).Sub(amount, paid)
		}
		paid.Add(paid, share)
		if share.Sign() > 0 {
			creditReward(st, w.Address, uint256.MustFromBig(share))
		}
	}
}
