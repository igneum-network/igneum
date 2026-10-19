// Package service wires the reference pool together: the getwork poll
// that turns the node's work into stratum jobs, the sink that puts
// verified shares into the PPLNS window and submits block solutions,
// the found-block tracker that credits a block once it is canonical,
// the reconciler that matures or reverses it, and the rewards refresh
// that keeps the earnings fields current (docs/ecosystem/DESIGN.md,
// section 2).
package service

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"igneum.network/pool/internal/jobsource"
	"igneum.network/pool/internal/ledger"
	"igneum.network/pool/internal/limits"
	"igneum.network/pool/internal/reconcile"
	"igneum.network/pool/internal/rewards"
	"igneum.network/pool/internal/shares"
	"igneum.network/pool/internal/stratum"
	"igneum.network/pool/internal/validate"
)

// Config is what the operator sets; every figure here is published in
// the disclosure document.
type Config struct {
	// RPCURL is the pool's node's RPC as given (-rpc), named on the share journal's forwarded lines as the sealer
	// when the pool forwards to its own node (the settlement team's join, 12 October 2026).
	RPCURL string
	// BlockSeconds is the chain's block clock and ShareCapQuarter the per-session target of accepted shares a
	// height (a quarter of the node's per-address cap) the step rule aims at; from the disclosure document.
	BlockSeconds    float64
	ShareCapQuarter float64
	// ShareCap is the node's per-address cap of accepted shares a height (the disclosure's share_cap_per_height; the
	// mid-height raise keeps a session's count at the current height under it, the project's GO of 03:20 UK, 12 October 2026).
	ShareCap float64
	// ShareStepRaise turns the mid-height raise on (the default) or off (-share-step-raise=false: no job change
	// within a height; the five-minute rule alone).
	ShareStepRaise bool
	// ShareStepMaxPool caps a session's step under the node's shareStepMax (0 = the node's alone).
	ShareStepMaxPool uint8
	// ControlDir is the operator's directory beside the disclosure: a file raise.request there, one member address a
	// line, has that member's current job re-issued once at the next step within the height (the install proof of
	// an app through a mid-height re-issue, the product team's ask of 12 October 2026); the file is removed when served.
	ControlDir string
	// Coordinates is the list of VectorWork coordinates the pool serves templates for (the project's order of 00:5x UK,
	// 13 October 2026); empty = the one-form chain as before (nothing changes on the public pool until the public test network, chain 7322).
	Coordinates []uint8
	// ArchiveRPC is a node that serves old state (eth_getBalance at a past height) for the per-address page's balance
	// steps; empty = the pool's node.
	ArchiveRPC string
	// ProgramRevealBlocks is D of the chain form (the registered reveal delay): a programmed document's period
	// anchor is the canonical block hash at period x period_blocks - D (the genesis hash below height 1); 0 = no
	// resolution by the pool (the node's own programAnchor on igneum_getWork, or none).
	ProgramRevealBlocks uint64
	// ShareForwardOne forwards each share to one sealer (the coordinate's source, then the fan-out in order on a
	// refusal or an error, never all at once): the form for a chain with share gossip (the old public test network (chain 7320): its ledger of
	// 14:04 UK, 13 October 2026: 150,160 of the mini's 338,394 submissions refused "a share carried twice" were
	// the three-way fan-out's second and third copies under gossip; every such share had one accepted copy).
	// Off = the fan-out form, every sealer at once (a chain without gossip: a share pays only in the blocks of the
	// sealer whose pool holds it).
	ShareForwardOne bool
	// BlockCoords lists the coordinates block sessions are served on (chain 7321: 1 and 2; coordinate 0's keyed
	// version 14 seal is the sealer's secret key's, so no member seals it); empty = every coordinate served.
	BlockCoords []uint8
	// CoordinateRPCs maps a coordinate to the RPC URLs of the sealers that seal it (vectorwork1, 13 October 2026: one
	// sealer per coordinate, each answering its own coordinate's igneum_getWork alone); the first is that
	// coordinate's template source, every one its fan-out; a coordinate without an entry uses the pool's node.
	CoordinateRPCs map[uint8][]string
	FeeBps         uint32
	// the verifiable fee waiver (the founder's word of 18:2x UK, 14 October 2026): FeePercent the fee Ember
	// declares (1), EmberFeeAddress the address its fee session mines under, PoolFeeAddress the pool's own
	// address for the charge (one member height in a hundred), FeeVerifyMinShares the shares a verdict needs
	// ForwardAs (the pooled form, the project's order of 14:3x UK, 15 October 2026): the pool key's address every
	// member's share is forwarded under, so the chain's breadth payment lands on the key and the pool pays its
	// members by their carried work (the journal's per-member ledger); empty keeps each member's own address.
	ForwardAs string
	// PoolKeyFile is the pool key's secp256k1 key file for the payout run (made on first start, never printed);
	// its address is the forwarding address when ForwardAs is empty. PayoutWindow the blocks a window spans (the
	// address window when 0), MinPayoutWei the due under which a member's pay carries to the next window.
	PoolKeyFile        string
	PayoutWindow       uint64
	MinPayoutWei       *big.Int
	FeePercent         uint32
	EmberFeeAddress    string
	PoolFeeAddress     string
	FeeVerifyMinShares uint64
	ChainID            uint64 // bound into every job identity
	WindowN            uint64 // PPLNS window as a multiple of the network difficulty
	ShareDifficulty    uint64 // the share target's difficulty (vardiff later)
	Maturity           uint64 // SD-15
	Poll               time.Duration
	RewardFallback     *big.Int // used when igneum_blockReward is absent; nil means no credit until it is
	// PayoutList is the chain-native payout list the pool set on its node
	// (miner_setPayoutList; the devnet4 sealer's form): when set, a work
	// package whose "payouts" field is not this list is refused and no job
	// is issued on it, so the pool's hashers never work a template that
	// pays anyone but the members the pool named (POOL-NONCUSTODIAL.md
	// section 3). Nil means no check (a node without the list).
	PayoutList json.RawMessage
	// MemberNodes is the member-node form (GUARD-C-CLOSURE 3.1, the
	// required form: every member receives a unique signed template its
	// OWN node built): the lower-cased member address to its node's RPC
	// URL. The pool polls each member's node for ITS template, issues it
	// only to that member's sessions, sets the pool's payout list on each
	// node (the pool's one statement, which the member's node signs over
	// and the member checks) and submits a member's solution to the
	// member's own node. The pool holds no template, no key and no sealed
	// block. Empty means the single-node form.
	MemberNodes map[string]string
	// Withhold is a TEST FIXTURE (GUARD-C-CLOSURE 3.3 P3): the lower-cased
	// member address whose block solutions the coordinator drops instead of
	// submitting, so the row can show that the member's own direct
	// submission lands the block (the P2Pool recovery) and that without it
	// the withholding is only detected. Never set outside a test.
	Withhold string
	// ShareLane is the share lane's pool form (LAUNCH-GUARDS 2.9; protocol
	// A's node shape of 11 October 2026): when the node serves shareTarget,
	// every authorised member gets its own job whose header is the share
	// header H_s = KECCAK(RLP(shareParent, shareHeight, address, extraNonce))
	// under the member's payout address with the session's extranonce, and
	// whose boundary is the one network-wide share target; a member's share
	// that meets it is forwarded to the node as igneum_submitShare under
	// the member's address (the chain pays the member by its list, the pool
	// is a relay and an accounting of the members' shares), never as a
	// block (the seal-only rule: a share never becomes a block).
	ShareLane bool
	// ShareNodes is the relay fan-out on the share lane (the finding of the
	// share-rate record, 11 October 2026: a share lives only in the pool of
	// the node it was submitted to and pays only when that sealer wins, so
	// a share sent to one sealer is paid in that sealer's blocks alone): the
	// RPC URLs of every sealer a member's share is forwarded to, beside the
	// chain reader; the node's answers are counted per node and the share
	// counts accepted when any sealer accepts it. The interim until share
	// gossip (PA-46) carries every valid share to every sealer's pool.
	ShareNodes []string

	// NodeTokenFiles names, per node URL (exactly as the URL is named in RPCURL, ShareNodes, CoordinateRPCs,
	// ArchiveRPC or MemberNodes), a file whose bytes go on every request to that node as the Authorization:
	// Bearer header (0.1.75, 16 October 2026); read once at start (a handover is the reload); a file that cannot
	// be read refuses that node's poll with a line naming the path, never the bytes.
	NodeTokenFiles map[string]string
	// ShareJournal is the path of the pool's share journal (the project's order
	// of 11 October 2026, 14:3x UK): the members' accepted shares
	// journalled as accepted with what re-verification needs, replayed and
	// re-verified at the pool's start into the window in their order,
	// pruned as carried (the block that paid them) or expired (older than
	// ShareRecency blocks below the head); empty keeps none, the window
	// then lost at a restart.
	ShareJournal string
	// ShareRecency is the number of blocks below the head past which an
	// outstanding share is expired (zero: 64).
	ShareRecency uint64
}

// Node is the RPC surface the service needs.
type Node interface {
	GetWork(ctx context.Context) (jobsource.Work, error)
	SubmitWork(ctx context.Context, nonce uint64, header, mix [32]byte) error
	Call(ctx context.Context, method string, params interface{}, out interface{}) error
}

// Service is the running pool.
type Service struct {
	cfg       Config
	validator validate.Validator
	node      Node
	book      *ledger.Ledger
	window    *shares.Window
	recon     *reconcile.Reconciler
	Server    *stratum.Server

	chainID uint64
	mu      sync.Mutex
	current *stratum.Job
	// the chain-native list's re-set state (the fault of 11 October 2026: the list was set once at start, so a node
	// restart left the pool refusing every template, silently dead for its members): the last re-set, the node's
	// last answer state (a re-set follows the node answering again) and the count for the health line
	listSetAt  time.Time
	nodeLost   bool
	ListResets uint64
	found      []foundBlock
	carry      *big.Int
	earnings   rewards.Summary
	Stats      Stats
	now        func() time.Time
	// members is the member-node form's state, one entry per member
	// address (nil on the single-node form).
	members map[string]*member
	// shareJobs is the share lane's state, one entry per member address
	// (nil off the lane).
	shareJobs map[string]shareJob
	// journal is the pool's share journal (nil when Config.ShareJournal is
	// empty); lastExpire the head the last expiry pass ran at.
	journal         *shares.Journal
	journalVerifier shares.Verifier
	lastExpire      uint64
	// shareNodes are the share lane's fan-out targets (Config.ShareNodes
	// opened as clients; nil without the fan-out) and their per-node counts.
	shareNodes []shareNode
	// coordNodes: VectorWork's per-coordinate sealers (CoordinateRPCs), the template source first
	coordNodes map[uint8][]shareNode
	coordErrAt map[uint8]time.Time // the last minute a coordinate's poll failure was named
	// the chain's pay list at the tip (the project's order of 10:3x UK, 15 October 2026: the pool offers only coordinates
	// whose payShares is above 0, read from igneum_vectorWork and re-read every height, since the registry changes
	// by transaction now; shares on an unpaid coordinate piled at the sealer's bound and were dropped)
	payShares     []uint64
	payReadHeight uint64
	payNames      []string
	payUnpaidSaid map[uint8]bool
	// the chain of record's per-poll read (0.1.61, 15 October 2026): the source's eth_chainId re-read every
	// ChainCheckEvery, the poll refused while it differs from the bound chain id, said once a minute
	chainCheckedAt time.Time
	chainMismatch  uint64
	chainSaidAt    time.Time
	// the chain's best known head (0.1.63, 16 October 2026, the project's rule): eth_blockNumber read from EVERY configured
	// node (the source, the fan-out, every coordinate's sealers) every ChainCheckEvery, the highest kept; a template
	// more than three blocks under it is never issued
	bestHeadVal  uint64
	bestHeadFrom string
	bestHeadAt   time.Time
	bestHeadSaid time.Time
	// the sealers found for paid coordinates the configuration names none for (0.1.63: the configured nodes asked
	// igneum_getWork [c], the one answering a package carrying c taken as the coordinate's sealer; probed at most
	// once per ChainCheckEvery per coordinate)
	findAt map[uint8]time.Time
	// payUnnamedSaid: the paid coordinates the configuration names no sealer for, said once per pay-list change (laneSet)
	payUnnamedSaid map[uint8]bool

	// refusals: the refused-submit journal's collapse (0.1.74)
	refusals *refusalLog

	// nodeTokens: the per-node credentials read from NodeTokenFiles at start; nodeRefusals: the nodes whose file
	// could not be read, each with the error naming the path (0.1.75)
	nodeTokens   map[string]string
	nodeRefusals map[string]error
	laneHead     uint64       // the highest share height any lane's template served (a reader behind it is not issued)
	payout       *payoutState // the pooled form's payout run (payout.go), nil without a pool key file
	// revealOf is each coordinate document's reveal D as its named templates taught it (the period's start minus
	// the node's AnchorHeight), the pool's own resolution reading the anchor at start minus D
	revealOf  map[string]uint64
	archive   Node         // the archive RPC's client when ArchiveRPC is set
	guardNode atomic.Value // the guard's current node (a Node) for every read of the pool's node; nil = the source
	// staleAnchorSaid is the last height whose template named an anchor height off the period's, said once a height
	staleAnchorSaid uint64
	// revealReg is each coordinate's registered reveal D from the registry view (igneum_vectorRegistry's
	// revealBlocks, 0 for the chain's default), read every height with the pay list; revealRegHeight the height
	// read; nil while no registry has answered (a chain without the vector registry)
	revealReg       map[uint8]uint64
	revealRegDigest map[string]uint64 // the same by the coordinate's document digest, for a read with no coordinate (the replay)
	revealRegHeight uint64
	// anchors: the resolved period anchors by anchor height (the chain form), read once each from the coordinate's
	// sealer; a reorg below an anchor height is a re-read on the next period
	anchors map[uint64][32]byte
	// seqOf: the journal sequence of each accepted share by its key (account/job/nonce), so the forwarded lines
	// carry the share's sequence (the per-sealer hold of the admission keys on it); pruned with the retained jobs
	seqOf map[string]uint64
	// accountReads: per address (lower-cased), the chain's share account as the settle last read it from the
	// sealers, the public per-address page's "as the chain carried them"
	accountReads map[string]accountRead
	// coordState: VectorWork's per-coordinate chain state as the latest template stated it (the difficulty, the reward
	// per block, the coordinate's last block, the template height), the accounting pages' inputs
	coordState map[uint8]coordState
	// sealedAt is the highest height this pool sealed per coordinate (one block a height a coordinate)
	sealedAt map[uint8]uint64

	// submittedAt: the height a block solution is in flight to the sealer at, per coordinate (0.1.82: the duplicate
	// guard's mark, the seconds around a submit; sealedAt is the chain's hash alone)
	submittedAt map[uint8]uint64

	// blockWorks: the last block work seen per coordinate (0.1.81, F-420's pool half: a header standing while the
	// parent moves names a sealer's work that is not on the head); blockStaleSaid the height the fence last spoke at
	blockWorks     map[uint8]blockWork
	blockStaleSaid map[uint8]uint64
	graphNamed     bool                  // the proof-carried coordinate named once on the log
	feeAcct        map[string]*feeWindow // the verifiable fee waiver's accounting by member and worker
	tickets        map[string][]byte     // proof-carried shares' tickets by job and nonce, until their forward
	// forwardQ carries the share lane's forwards to the sealers off the session's reply path (the mini's stall of
	// 07:2xZ, 12 October 2026: a hung sealer's 10 s timeout per share stood between a miner's submit and its reply,
	// 77 submissions unanswered for 120 s); forwardWorkers drain it under the service's own context.
	forwardQ chan forwardItem
	// the admission bound's state (the project's order of 08:5x UK, 12 October 2026; PA-53's admission bound as the pool
	// applies it): the node's share limits, its pooled count at the last read, and the pool's accepted shares per
	// address and height (pruned past twice the recency)
	limits    shareLimits
	limitsAt  time.Time
	pooledNow uint64
	// the aggregate admission bound (the security review's path 2, 12 October 2026, F-50: sixteen sessions were accepted
	// and receipted at 16,500 shares in six minutes against a chain carrying 256 a block, the excess expiring
	// named and unpaid after a receipt): the pool's shares outstanding within the recency across EVERY address,
	// read from the journal each poll, plus the accepted since that read
	outstandingRecent uint64
	acceptedSinceRead uint64
	// laneLastAccepted (0.1.96): when a share was last accepted per coordinate, for the paid lanes without a worker
	// (named on /shares and the log: on a minimum-rule chain the pool earns nothing while any paid lane has none)
	laneLastAccepted map[uint8]time.Time
	// laneWorkerAt: per coordinate, each worker's last accepted share time (0.1.96, the notify's lane_workers)
	laneWorkerAt   map[uint8]map[string]time.Time
	startedAt      time.Time
	laneSilentSaid map[uint8]bool
	// acceptTimes is a ring of the last accepted shares' times for the live figure accepted_last_60s (the network
	// page's ask of 14:42 UK, 12 October 2026)
	acceptTimes []time.Time
	// acceptWeights rides beside acceptTimes: each accepted share's expected hashes (2^256 over its job's share
	// target, the step inside it), for the pool's share-weighted hash rate (the project's order of 11:3x UK, 15 October
	// 2026: the feed's difficulty estimate is the sealing puzzle's, not the miners' work)
	acceptWeights []uint64
	acceptHead    int
	// the forward's clock (F-50's lag read of 14:0x UK, 12 October 2026: under a flood the shares waited 30 to 120 s
	// in a 2,048-deep queue against the sealers' submit latency, longer than a template's life): the wait of the
	// share last taken off the queue and the sealers' recent call latency, so admission refuses in TIME, before the
	// receipt, when a share accepted now would reach the sealers after its template
	forwardWait   time.Duration // the queue wait of the share last dequeued
	forwardWaitAt time.Time
	sealerCall    time.Duration // the sealers' recent submit latency (an average over the last calls)
	// sharesPerBlock is the lane's operating rate from the template (igneum_getWork's sharesPerBlock): the
	// carriage bound is sharesPerBlock x recency (the project's ruling of 11:0x UK, 12 October 2026, so the pool's
	// bound and the node's agree on every network), never maxShares (the block's ceiling) and never a literal
	sharesPerBlock uint64
	acceptedAt     map[string]map[uint64]uint64
	workerSteps    map[string]*workerStep
	// the template source's failover (the stall of 12:17Z to 12:39Z, 12 October 2026: pod a's templates stood at
	// 1,829 after a rolled-back release, every address held at the cap at that one height for twenty minutes):
	// when the source's share height stands still past three block clocks and a fan-out sealer serves a share
	// height ahead, the pool takes its template from that sealer and names the move; back to the source when it
	// is at the head again for ten minutes
	templateFrom     int       // the index into shareNodes the template comes from (0 = the pool's node, the source)
	lastHeight       uint64    // the last share height issued
	lastHeightAt     time.Time // when it first appeared
	failoverLagSince time.Time
	sourceErrAt      time.Time // when the source last stopped answering getWork (zero while it answers)
	failoverSince    time.Time // when the source last served a template at the head (for the return)
}

// shareNode is one sealer a member's share is forwarded to, with its counts.
type shareNode struct {
	url      string
	node     Node
	accepted uint64
	refused  uint64
	lastErr  string
}

// ShareNodeState is the health line's view of one fan-out target.
type ShareNodeState struct {
	RPC       string `json:"rpc"`
	Accepted  uint64 `json:"accepted"`
	Refused   uint64 `json:"refused"`
	LastError string `json:"last_error,omitempty"`
}

// ShareNodes is the health line's view of the fan-out (nil without one).
func (s *Service) ShareNodes() []ShareNodeState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shareNodes == nil {
		return nil
	}
	out := make([]ShareNodeState, 0, len(s.shareNodes))
	for _, n := range s.shareNodes {
		out = append(out, ShareNodeState{RPC: n.url, Accepted: n.accepted, Refused: n.refused, LastError: n.lastErr})
	}
	return out
}

// ShareJournalStats is the health line's view of the journal (nil without one).
func (s *Service) ShareJournalStats() *shares.JournalStats {
	if s.journal == nil {
		return nil
	}
	st := s.journal.Stats
	return &st
}

// shareJob is the per-member state of the share lane: the member's
// extranonce and the template the job was built on (sharelane.go, the
// igneumhash build; the default build carries the stub).
// KeepaliveSilence is the silence after which a session's standing job is sent again (F-164, 15 October 2026): under a
// minute, since Ember 0.2.16's no-work clock re-dials at 60 s; a test shortens it.
var KeepaliveSilence = 30 * time.Second

type shareJob struct {
	extraNonce uint64
	parent     [32]byte
	height     uint64
	// the period's program anchor the job was keyed on (versions 9 to 16): a reorg at the period's anchor height
	// changes the anchor without changing the share height's parent (the old public test network (chain 7320) heights 3368 to 3385, 17:32Z to
	// 17:38Z 13 October 2026: 146 shares keyed on the losing twin of block 3358, refused "invalid mix digest"
	// by every sealer), so the job is re-issued on an anchor change as on a parent change
	anchor [32]byte
	// the session's step (PA-53's declared step: the share mined at the share target over 2^step and weighing
	// 2^step), chosen from the session's measured rate so its accepted shares a height sit at a quarter of the
	// node's per-address cap (the project's order of 02:36 UK, 12 October 2026: a fast card at step 0 had two thirds of
	// its shares refused "address at cap"); re-read every five minutes, applied at a new height, never mid-height
	step uint8
	// the mid-height RAISE (the project's GO of 03:20 UK, 12 October 2026): a session whose accepted SHARES at the current
	// height reach the quarter is re-issued at the smallest higher step that keeps the height's remainder under the
	// cap (the step is never lowered mid-height; the next template reads the five-minute rule again)
	heightAt       time.Time // when the job for this height was issued
	sharesAtHeight uint64    // accepted shares (by count, as the cap counts) at the current height
	raiseAt        uint64    // the count at which the raise is considered
	raised         bool      // the one raise of this height spent
	ruleStep       uint8     // the five-minute rule's step (the step a new template starts at; a raise holds for its height only)
	worker         string    // the job's worker name (the share lane's per-worker jobs; "" = the address's one job)
	// the keepalive (F-164, 15 October 2026): the job last sent to the session and when; on a standing template the
	// same job is sent again once KeepaliveSilence has passed, so a miner's no-work clock reads a live pool through
	// the chain's own block gaps (the research chain's ran to 400 s; a miner re-dialled at every gap over a minute)
	jobID    string
	sentAt   time.Time
	coord    uint8 // VectorWork: the job's coordinate (hasCoord), one job per (address, worker, coord)
	hasCoord bool
}

// workerStep is the five-minute step rule's state per WORKER NAME across the worker's sessions (the project's ruling of
// 09:37 UK, 12 October 2026, on G-33: an install's fee session, under the fee address with the same worker name,
// carries its member session's step, bounded by the worker's measured rate); a session with no worker name is
// keyed by its address.
type workerStep struct {
	ruleStep  uint8
	stepAt    time.Time
	accepted  uint64 // step-0-equivalent shares accepted since countFrom, across the worker's sessions
	countFrom time.Time
	// firstRead: the first re-read of a worker's step comes inside its first height (the block clock after its
	// first job), the five-minute clock after (the project's ruling of 12:1x UK, 12 October 2026, G-34); a known worker
	// starts at its persisted step
	firstRead bool
	// pace is the worker's count a height as first measured (a card that paces its own submissions shows the same
	// count at any step; economics' G-34 measure of 18:45Z, 12 October 2026: the pods paced under half the quarter
	// never climbed); the probe reads the worker's count against its OWN pace, not the quarter
	pace float64
	// lastCount and lastStep: the count a height at the step of the previous full-window read, so a count that
	// does not move when the step moves names a stale pace
	lastCount float64
	lastStep  uint8
	// absentAt: when the worker was last seen leaving (the project's order of 04:2x UK, 14 October 2026: a same-worker
	// re-dial within the grace resumes the window where it stood; Ember's hourly dev-fee session must never cost
	// a miner its window)
	absentAt time.Time
	// present: the worker had an authorised session at the last poll; a worker re-dialling after an absence has
	// its count window restarted (the mini's evening of 13 October 2026: three re-installs and two pool switches,
	// each absence counted as zero rate inside the five-minute window, the step lowered to 0 on the re-dial)
	present bool

	// lastAcceptedAt: when a VERIFIED share of the worker's was last accepted (F-392, 17 October 2026: the silence
	// decay reads this clock alone; a submit refused for rate is evidence of pace, never of a share found)
	lastAcceptedAt time.Time

	// presentSince: when the worker's key was first seen or restored (the decay's clock before any share; a re-dial
	// does not move it: the decay counts wall time since the last share found)
	presentSince time.Time

	// decayAt: the last notch of the silence decay (one notch a silenceNotch)
	decayAt time.Time
}

// workerStepsFile is the persisted step per worker key beside the disclosure (G-34: a known worker name starts
// at its last step across sessions and rolls, bounded by its measured rate at the first re-read).
const workerStepsFile = "worker-steps.json"

// loadWorkerSteps reads the persisted steps (nothing when the file is absent).
func (s *Service) loadWorkerSteps() {
	if s.cfg.ControlDir == "" {
		return
	}
	b, err := os.ReadFile(filepath.Join(s.cfg.ControlDir, workerStepsFile))
	if err != nil {
		return
	}
	// both forms: 0.1.15's plain number (the step) and the {step, pace} object (the 0.1.20 roll of 20:47Z,
	// 12 October 2026, read the object form only and every worker restarted at step 0, the fleet's address at the
	// chain's cap at its next height)
	var raw map[string]json.RawMessage
	if json.Unmarshal(b, &raw) != nil {
		return
	}
	type entry struct {
		Step uint8   `json:"step"`
		Pace float64 `json:"pace"`
	}
	m := map[string]entry{}
	for k, v := range raw {
		var e entry
		var n uint8
		switch {
		case json.Unmarshal(v, &e) == nil:
			m[k] = e
		case json.Unmarshal(v, &n) == nil:
			m[k] = entry{Step: n}
		}
	}
	now := s.now()
	s.mu.Lock()
	if s.workerSteps == nil {
		s.workerSteps = map[string]*workerStep{}
	}
	for k, st := range m {
		// firstRead true: a restored worker's first re-read is on the five-minute clock, not inside the first height
		// (the 0.1.31 public handover of 01:46:27Z, 14 October 2026: a Windows card read "no share in 20 s" before its session
		// had re-dialled and fell from 18 to 0 for 21 s)
		s.workerSteps[k] = &workerStep{ruleStep: st.Step, pace: st.Pace, stepAt: now, countFrom: now, firstRead: true, presentSince: now}
	}
	s.mu.Unlock()
	log.Printf("share lane: %d worker step(s) restored from %s", len(m), workerStepsFile)
}

// saveWorkerSteps writes the steps (the caller holds s.mu).
func (s *Service) saveWorkerSteps() {
	if s.cfg.ControlDir == "" {
		return
	}
	m := map[string]map[string]interface{}{}
	for k, ws := range s.workerSteps {
		m[k] = map[string]interface{}{"step": ws.ruleStep, "pace": ws.pace}
	}
	b, _ := json.Marshal(m)
	p := filepath.Join(s.cfg.ControlDir, workerStepsFile)
	if err := os.WriteFile(p+".tmp", b, 0o644); err == nil {
		_ = os.Rename(p+".tmp", p)
	}
}

// RaiseStep is the mid-height rule: with sharesAtHeight accepted at the
// current height over elapsedSeconds at step cur, the smallest step above
// cur at which a stretch as long again brings at most a quarter of the
// height's REMAINING budget (cap minus the shares already accepted), so a
// stalled height converges under the cap (each raise spends a quarter of
// what is left, the stretches doubling); at most stepMax; cur when no raise
// is possible, stepMax when the budget is gone.
func RaiseStep(sharesAtHeight uint64, elapsedSeconds float64, cur, stepMax uint8, cap float64) uint8 {
	if cur >= stepMax || cap <= 0 || sharesAtHeight == 0 || elapsedSeconds <= 0 {
		return cur
	}
	budget := cap - float64(sharesAtHeight)
	if budget <= 0 {
		return stepMax
	}
	allowed := budget / 4
	// the session's step-0 rate at this height, from what it accepted at the current step
	rate0 := float64(sharesAtHeight) * float64(uint64(1)<<cur) / elapsedSeconds
	s := cur + 1
	for s < stepMax && rate0*elapsedSeconds/float64(uint64(1)<<s) > allowed {
		s++
	}
	return s
}

// RetargetStep is the per-session difficulty's retarget (the project's order of 12:0x UK, 15 October 2026: the share
// difficulty follows the session until it submits at the pool's share rate, so a fast card submits fewer, heavier
// shares and every hash counts; the cap stays a flood guard on shares a second, not on work): from the shares the
// session submitted at the current step over the stretch, its step-0 rate, and the smallest step at or above the
// current one at which a block clock brings at most the session's quota; many steps at once when the rate says so,
// bounded by the node's stepMax.
func RetargetStep(sharesAtStep uint64, elapsedSeconds float64, cur, stepMax uint8, quota, blockSeconds float64) uint8 {
	if cur >= stepMax || quota <= 0 || sharesAtStep == 0 || elapsedSeconds <= 0 || blockSeconds <= 0 {
		return cur
	}
	rate0 := float64(sharesAtStep) * float64(uint64(1)<<cur) / elapsedSeconds
	perHeight := rate0 * blockSeconds
	s := cur
	for s < stepMax && perHeight/float64(uint64(1)<<s) > quota {
		s++
	}
	return s
}

// silenceAfter and silenceNotch: the silence decay (F-392, 17 October 2026): a present worker with no verified share
// accepted for silenceAfter comes down one step every silenceNotch toward its declared rate's step (Service.silenceDecay)
const (
	silenceAfter = 2 * time.Minute
	silenceNotch = time.Minute
)

// blockWork is the last block template the pool saw on a coordinate (0.1.81): its header, its parent and the height.
type blockWork struct {
	header [32]byte
	parent [32]byte
	height uint64
}

// blockWorkStale is F-420's pool half (0.1.81, 17 October 2026): the sealer's block work is not on the head when its
// headerHash is zero (the node's own word from 0.10.51, staleTemplate beside it), when its parentHash is carried and is
// not the share parent (the head), or when its header stands unchanged while the parent moved (the night of 16
// October: igneum_vectorWork served the last committed sealhash under a fresh parent and height, the pool's solution
// was sealed two blocks behind and dropped). The reason names the fact; "" when the work is on the head.
func blockWorkStale(w jobsource.Work, last blockWork, seen bool) string {
	switch {
	case w.Header == ([32]byte{}):
		if w.StaleTemplate != "" {
			return "its headerHash is zero (the node says: " + w.StaleTemplate + ")"
		}
		return "its headerHash is zero"
	case w.HasParent && w.ParentHash != w.ShareParent:
		return fmt.Sprintf("its parent %x.. is not the head %x..", w.ParentHash[:6], w.ShareParent[:6])
	case seen && w.Header == last.header && w.ShareParent != last.parent:
		return fmt.Sprintf("its header %x.. is the previous parent's (%x.. at %d, the head now %x..)", w.Header[:6], last.parent[:6], last.height, w.ShareParent[:6])
	}
	return ""
}

// rawAnswer is a node's JSON-RPC result as the node sent it, for the log beside the pool's reading of it (0.1.80;
// bounded so a node cannot flood the log with one answer; its whitespace one space, so a line break inside an
// answer cannot forge a log line: the security review's read of 0.1.80).
func rawAnswer(raw json.RawMessage) string {
	a := strings.Join(strings.Fields(string(raw)), " ")
	if a == "" {
		return "(none)"
	}
	if len(a) > 200 {
		a = a[:200] + ".."
	}
	return a
}

// blockVerdict reads the chain's word on a submitted block solution (0.1.82): the block at its height is the pool's
// when its hash is the sealer's answer (the node from 0.10.52) or, without one, when its nonce is the solution's;
// another's once the head has moved two past the height with another block standing there; pending while the
// height is empty or the head is not yet two past it.
type blockVerdict uint8

const (
	verdictPending blockVerdict = iota
	verdictCanonical
	verdictLost
)

func blockVerdictOf(head uint64, b *rpcBlock, height uint64, answerHash string, nonce uint64) blockVerdict {
	if b == nil || b.Hash == "" {
		return verdictPending
	}
	// either fact counts: a keyed seal's answer hash is pre-proof until the node's cure (the security review's note on
	// 0.10.52), so the nonce stands beside it
	if answerHash != "" && strings.ToLower(b.Hash) == answerHash {
		return verdictCanonical
	}
	if n, err := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(b.Nonce), "0x"), 16, 64); err == nil && n == nonce {
		return verdictCanonical
	}
	if head >= height+2 {
		return verdictLost
	}
	return verdictPending
}

// confirmBlock is 0.1.82's read after an accepted submit: the block at the solution's height from the pool's source,
// read every two seconds until the chain's word is in or ninety seconds pass; Stats.Blocks and sealedAt move on the
// chain's hash alone, the log names "canonical at H (hash)" or "not canonical (H is another's)".
func (s *Service) confirmBlock(job stratum.Job, nonce uint64, answerHash string) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			log.Printf("block: coordinate %d height %d: the chain's word did not come in 90 s (the sealer's RPC accepted nonce %d); not counted", job.Coord, job.Height, nonce)
			return
		case <-time.After(2 * time.Second):
		}
		var headHex string
		if err := s.sourceNode().Call(ctx, "eth_blockNumber", []interface{}{}, &headHex); err != nil {
			continue
		}
		head, err := strconv.ParseUint(strings.TrimPrefix(headHex, "0x"), 16, 64)
		if err != nil {
			continue
		}
		var b *rpcBlock
		if err := s.sourceNode().Call(ctx, "eth_getBlockByNumber", []interface{}{fmt.Sprintf("0x%x", job.Height), false}, &b); err != nil {
			continue
		}
		switch blockVerdictOf(head, b, job.Height, answerHash, nonce) {
		case verdictCanonical:
			s.mu.Lock()
			s.Stats.Blocks++
			if job.HasCoord {
				if s.sealedAt == nil {
					s.sealedAt = map[uint8]uint64{}
				}
				if job.Height > s.sealedAt[job.Coord] {
					s.sealedAt[job.Coord] = job.Height
				}
			}
			s.mu.Unlock()
			log.Printf("BLOCK: member %s worker %q's solution on coordinate %d is canonical at %d (%s)", job.Member, job.Worker, job.Coord, job.Height, b.Hash)
			return
		case verdictLost:
			s.mu.Lock()
			s.Stats.BlocksLost++
			s.mu.Unlock()
			log.Printf("block: member %s worker %q's solution on coordinate %d is not canonical (%d is another's: %s, bond key %s, nonce %s)", job.Member, job.Worker, job.Coord, job.Height, b.Hash, shortKey(b.BondKey), b.Nonce)
			return
		}
	}
}

// shortKey is a bond key's first eight hex digits for the log, or "(none)".
func shortKey(k string) string {
	k = strings.TrimPrefix(strings.ToLower(k), "0x")
	if len(k) < 8 {
		return "(none)"
	}
	return "0x" + k[:8] + ".."
}

// silenceDecay is F-392's pool half (17 October 2026): a present worker with no verified share accepted for
// silenceAfter comes down one step every silenceNotch toward floor (the step its declared rate chooses, 0 without
// one), the log naming it; it returns whether a notch was taken. The five-minute read could not reach a worker
// cycling absent and present faster than its clock (a one-card row: a re-dial every 140 s restarted the window at the
// standing step), nor one whose rate-refused bursts counted as evidence at a step it never found a share at (an
// eight-card rig): this clock reads accepted shares alone.
func (s *Service) silenceDecay(wk string, ws *workerStep, now time.Time, floor uint8) bool {
	if ws.ruleStep == 0 || ws.ruleStep <= floor {
		return false
	}
	since := ws.lastAcceptedAt
	if since.IsZero() {
		since = ws.presentSince
	}
	if since.IsZero() || now.Sub(since) < silenceAfter {
		return false
	}
	if !ws.decayAt.IsZero() && now.Sub(ws.decayAt) < silenceNotch {
		return false
	}
	ws.ruleStep--
	ws.decayAt = now
	log.Printf("share lane: worker %q found no share for %.0f s at step %d: step %d by the silence decay (one a minute toward the declared rate's step %d)", wk, now.Sub(since).Seconds(), ws.ruleStep+1, ws.ruleStep, floor)
	s.saveWorkerSteps()
	return true
}

// restartStep is the step a worker's window restarts at on a re-dial after the grace (F-392): the step its declared
// rate chooses when it declares one, as a fresh session's, else the standing step.
func restartStep(cur uint8, declaredPerSec, blockSeconds, quarter float64, stepMax uint8) uint8 {
	if declaredPerSec > 0 && stepMax > 0 {
		return ChooseStep(declaredPerSec, blockSeconds, quarter, stepMax)
	}
	return cur
}

// refused is the server's OnRefused hook: the server calls it from every connection's own goroutine, so the
// collapse's state is read and written under the service lock, as the other hooks' is (the security review's read of 0.1.74's
// first cut, 18:57 UK 16 October 2026: a concurrent map write is a runtime fatal, a remote crash lever).
func (s *Service) refused(r stratum.Refusal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refusals == nil {
		return
	}
	line, write := s.refusals.line(s.now(), r)
	if write && s.journal != nil {
		_ = s.journal.Note(line)
	}
}

// sweepRefusalsLocked flushes and drops the collapse's idle keys on the share lane's poll, which holds s.mu (a key
// idle past refusalKeyIdle writes its trailing suppressed count as a last line, so no count is lost when a session
// ends; the map never grows without bound under a stranger cycling worker names). The caller holds the service lock.
func (s *Service) sweepRefusalsLocked(now time.Time) {
	if s.refusals == nil {
		return
	}
	s.refusals.sweep(now, refusalKeyIdle, func(line shares.Line) {
		if s.journal != nil {
			_ = s.journal.Note(line)
		}
	})
}

// countsForRaise is whether a submit counts toward the mid-height raise's count at the session's current height: the
// same height and the job AT the session's current step alone (F-271, 16 October 2026: a card's backlog of results
// found at the step before, still arriving after the raise, was read as the rate at the new step and climbed a
// session five raises in five seconds to the node's maximum; a result on an older job is the old step's evidence,
// which the step rule's window already scales by its own step, never a raise at the new one).
func countsForRaise(jobStep, curStep uint8, jobHeight, curHeight uint64) bool {
	return jobHeight == curHeight && jobStep == curStep
}

// raiseAllowed is whether a mid-height raise may be considered now: always for the first of the height; after a
// raise, only once a block clock has passed at the new step, so the rate read is the session's at that step.
func raiseAllowed(raised bool, atStepSeconds, blockSeconds float64) bool {
	return !raised || blockSeconds <= 0 || atStepSeconds >= blockSeconds
}

// RaiseAllowance is the share count a raised session may add at the
// current height before the raise is considered again: a quarter of the
// remaining budget, at least one.
func RaiseAllowance(sharesAtHeight uint64, cap float64) uint64 {
	budget := cap - float64(sharesAtHeight)
	if budget <= 4 {
		return 1
	}
	return uint64(budget / 4)
}

// ChooseStep is the step rule: the smallest s, at most stepMax, such that the
// session's expected accepted shares a height at step s (rate x blockSeconds
// over 2^s, the rate in step-0 shares a second) is at or under capQuarter (a
// quarter of the node's per-address cap). A session with no rate yet stays at
// step 0.
func ChooseStep(ratePerSecond float64, blockSeconds, capQuarter float64, stepMax uint8) uint8 {
	if ratePerSecond <= 0 || blockSeconds <= 0 || capQuarter <= 0 {
		return 0
	}
	perHeight := ratePerSecond * blockSeconds
	var s uint8
	for s < stepMax && perHeight/float64(uint64(1)<<s) > capQuarter {
		s++
	}
	return s
}

// Stats is what the health endpoint reports.
type Stats struct {
	Shares          uint64
	Blocks          uint64
	BlocksStale     uint64 // block-session solutions for a height the coordinate's template had left, not submitted
	BlocksDuplicate uint64 // block-session solutions for a height and coordinate this pool had already sealed, not submitted
	BlocksLost      uint64 // block-session solutions the sealer's RPC accepted that are not on the chain at their height (0.1.82)
	Credited        uint64
	Jobs            uint64
	LastJobHeight   uint64
	// Withheld counts the solutions the TEST FIXTURE dropped (Config.Withhold).
	Withheld uint64
	// StepRaises counts the share lane's mid-height raises (a session re-issued at a higher step within a height).
	StepRaises uint64
	// Keepalives counts the share lane's re-sent standing jobs (F-164: one per session per KeepaliveSilence of silence).
	Keepalives uint64
	// The share lane's counters: the members' shares forwarded to the node
	// under their own addresses, and the node's accepted and refused answers.
	SharesForwarded uint64
	SharesAccepted  uint64
	SharesRefused   uint64
}

type foundBlock struct {
	Job      stratum.Job
	Nonce    uint64
	Mix      [32]byte
	Window   []shares.Share
	Credited bool
	Hash     string
}

// New creates the service over a validator and a ledger.
func New(cfg Config, node Node, v validate.Validator, book *ledger.Ledger) *Service {
	if cfg.Poll == 0 {
		cfg.Poll = 2 * time.Second
	}
	if cfg.Maturity == 0 {
		cfg.Maturity = limits.DefaultMaturityBlocks
	}
	if cfg.ShareDifficulty == 0 {
		cfg.ShareDifficulty = 1
	}
	if cfg.WindowN == 0 {
		cfg.WindowN = 2
	}
	s := &Service{cfg: cfg, node: node, book: book, carry: new(big.Int), now: time.Now, chainID: cfg.ChainID}
	s.nodeTokens, s.nodeRefusals = readNodeTokens(cfg.NodeTokenFiles)
	s.window = shares.NewWindow(cfg.ShareDifficulty) // retargeted on every job
	s.recon = reconcile.New(chainView{node}, book, cfg.Maturity)
	s.validator = v
	s.Server = stratum.NewServer(v, s)
	// a share verified at the step and refused before acceptance by the admission is evidence of the worker's
	// rate (the step rule's window), as an accepted one is
	if cfg.FeePercent > 0 && cfg.EmberFeeAddress != "" {
		s.Server.FeeDecider = s.FeeDecide
	}
	s.Server.OnAdmissionRefused = func(job stratum.Job) {
		s.mu.Lock()
		if ws := s.workerSteps[stepKeyCoord(job.Member, job.Worker, job.Coord, job.HasCoord)]; ws != nil {
			ws.accepted += uint64(1) << job.ShareStep
		}
		s.mu.Unlock()
	}
	// a submit the session's bucket refused (the rate guard) is the session's work too, for the step alone (the project's
	// order of 12:0x UK, 15 October 2026): it counts toward the measured rate and the height's count so the
	// retarget lifts the session's difficulty off the bucket; it is never verified, never paid, and counting it
	// can only raise the submitter's own difficulty
	s.Server.OnRateRefused = func(job stratum.Job) {
		s.mu.Lock()
		if ws := s.workerSteps[stepKeyCoord(job.Member, job.Worker, job.Coord, job.HasCoord)]; ws != nil {
			ws.accepted += uint64(1) << job.ShareStep
		}
		jk := jobKeyCoord(job.Member, job.Worker, job.Coord, job.HasCoord)
		if cur, ok := s.shareJobs[jk]; ok && countsForRaise(job.ShareStep, cur.step, job.Height, cur.height) {
			cur.sharesAtHeight++
			s.shareJobs[jk] = cur
		}
		s.mu.Unlock()
	}
	// every refused submit is of record by worker with its reason (F-271, 16 October 2026: a burst's refusals by the
	// bucket and the stale rule were silent); collapsed to refusalLinesASecond lines a second per worker and reason,
	// the rest counted on the next line as suppressed
	s.refusals = newRefusalLog(refusalLinesASecond)
	s.Server.OnRefused = s.refused
	if len(cfg.MemberNodes) > 0 {
		s.members = map[string]*member{}
		for addr, url := range cfg.MemberNodes {
			a := strings.ToLower(addr)
			s.members[a] = &member{addr: a, url: url, node: s.nodeFor(url)}
		}
	}
	if cfg.ShareLane {
		// the fan-out: the chain reader's node first, then every other sealer named (a URL equal to the reader's is
		// not opened twice)
		s.shareNodes = []shareNode{{url: "the pool's node", node: node}}
		for _, u := range cfg.ShareNodes {
			u = strings.TrimSpace(u)
			if u == "" {
				continue
			}
			s.shareNodes = append(s.shareNodes, shareNode{url: u, node: s.nodeFor(u)})
		}
		if cfg.ArchiveRPC != "" {
			s.archive = s.nodeFor(cfg.ArchiveRPC)
		}
		for c, urls := range cfg.CoordinateRPCs {
			for _, u := range urls {
				u = strings.TrimSpace(u)
				if u == "" {
					continue
				}
				if s.coordNodes == nil {
					s.coordNodes = map[uint8][]shareNode{}
				}
				s.coordNodes[c] = append(s.coordNodes[c], shareNode{url: u, node: s.nodeFor(u)})
			}
		}
	}
	return s
}

// OpenShareJournal opens the pool's share journal and replays its
// outstanding shares into the window, each re-verified by the validator
// (the same recomputation a submission gets) in its original order; a
// share that no longer verifies is dropped by name. Called by main after
// New, before the listener opens, so the window a found block pays is
// whole across the restart.
func (s *Service) OpenShareJournal() error {
	if s.cfg.ShareJournal == "" {
		return nil
	}
	j, err := shares.OpenJournal(s.cfg.ShareJournal)
	if err != nil {
		return err
	}
	var v shares.Verifier
	if pv, ok := s.validator.(validate.Preparer); ok {
		v = &replayVerifier{s: s, prep: pv, inner: s.validator}
	} else {
		v = nullVerifier{s.validator}
	}
	// the window's target before the first template is the share difficulty alone (the first poll sets WindowN
	// times the network difficulty), so a replay into it would evict what it is meant to keep: the target is held
	// at the outstanding weights' sum through the replay, and the first template's rule applies from then on
	var total uint64
	for _, l := range j.Outstanding() {
		total += l.Weight
	}
	s.mu.Lock()
	if total > s.window.Target {
		s.window.Target = total
	}
	s.mu.Unlock()
	replayed, dropped, err := j.ReplayFor(s.chainID, v, func(l shares.Line) {
		s.mu.Lock()
		s.window.Add(shares.Share{Account: l.Account, Worker: l.Worker, Key: l.Key, JobID: l.JobID, Weight: l.Weight, Height: l.Height, Journal: l.Seq})
		s.mu.Unlock()
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.journal = j
	s.journalVerifier = v
	s.mu.Unlock()
	log.Printf("share journal %s: %d outstanding share(s) replayed into the window after re-verification, %d dropped by name", s.cfg.ShareJournal, replayed, dropped)
	return nil
}

// HandoverJournal is the new generation's side of the handover form (the project's order of 21:2x UK, 13 October 2026):
// it waits for the previous generation to let the journal go (its exclusive lock, released when that process
// exits; and the pid named, when the previous generation predates the lock), reads the lines that generation
// appended since this index was built, replays the newly outstanding ones into the window, and returns so the
// caller opens the accept gate. Nothing without a journal.
func (s *Service) HandoverJournal(waitPid int) error {
	s.mu.Lock()
	j, v := s.journal, s.journalVerifier
	s.mu.Unlock()
	if j == nil {
		return nil
	}
	if waitPid > 0 {
		for i := 0; i < 600; i++ {
			if err := syscall.Kill(waitPid, 0); err != nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	if err := j.Lock(); err != nil {
		return fmt.Errorf("handover: the journal's lock: %w", err)
	}
	// 0.1.92 (F-510): the record is read now, under the lock: the previous generation wrote its close in flight
	// before it let go, so this generation never closes that window again
	s.payoutLoadRecord()
	lines, replayed, dropped, err := j.CatchUp(s.chainID, v, func(l shares.Line) {
		s.mu.Lock()
		s.window.Add(shares.Share{Account: l.Account, Worker: l.Worker, Key: l.Key, JobID: l.JobID, Weight: l.Weight, Height: l.Height, Journal: l.Seq})
		s.mu.Unlock()
	})
	if err != nil {
		return fmt.Errorf("handover: catch up the journal: %w", err)
	}
	log.Printf("handover: the journal's lock acquired, %d line(s) appended by the previous generation caught up (%d outstanding share(s) replayed into the window, %d dropped), accepting shares", lines, replayed, dropped)
	return nil
}

// nullVerifier adapts a validator without a Preparer (the null algorithm)
// to the journal's Verifier.
type nullVerifier struct{ validate.Validator }

func (nullVerifier) Prepare(string, []byte, string, uint64) error { return nil }

// replayVerifier re-verifies journalled shares as the lane verified them (pool-b2's handover of 11:14:52Z, 14 October
// 2026: 407 outstanding shares on the keyed coordinates dropped at the catch-up because the replay verified
// without the period's anchor): a programmed document's share is checked under the anchor the chain resolves for
// its height, and a proof-carried share (the graph) is taken as journalled, the node being its verifier.
type replayVerifier struct {
	s      *Service
	prep   validate.Preparer
	inner  validate.Validator
	digest string
	algo   string
	params []byte
}

func (r *replayVerifier) Prepare(algorithm string, params []byte, digest string, height uint64) error {
	r.digest, r.algo, r.params = digest, algorithm, params
	return r.prep.Prepare(algorithm, params, digest, height)
}

func (r *replayVerifier) Verify(algorithm string, header [32]byte, height uint64, nonce uint64) ([32]byte, [32]byte, error) {
	if validate.ProofCarried(algorithm) {
		return [32]byte{}, [32]byte{}, errProofCarried
	}
	if av, ok := r.inner.(validate.AnchoredValidator); ok {
		if pb := av.PeriodBlocks(r.digest); pb > 0 && r.s.cfg.ProgramRevealBlocks > 0 {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			anchor, _, err := r.s.periodAnchor(ctx, r.s.sourceNode(), 0, false, r.digest, height, pb)
			cancel()
			if err != nil {
				return [32]byte{}, [32]byte{}, fmt.Errorf("%w: the period anchor at height %d: %v", shares.ErrUnverifiable, height, err)
			}
			return av.VerifyUnder(algorithm, r.digest, anchor, header, height, nonce)
		}
	}
	if dv, ok := r.inner.(validate.DocumentValidator); ok {
		return dv.VerifyOn(algorithm, r.digest, header, height, nonce)
	}
	return r.inner.Verify(algorithm, header, height, nonce)
}

// VerifyUnderAnchor verifies a replayed share under the anchor its line recorded (shares.AnchoredVerifier).
func (r *replayVerifier) VerifyUnderAnchor(algorithm string, anchor [32]byte, header [32]byte, height uint64, nonce uint64) ([32]byte, [32]byte, error) {
	if validate.ProofCarried(algorithm) {
		return [32]byte{}, [32]byte{}, errProofCarried
	}
	if av, ok := r.inner.(validate.AnchoredValidator); ok {
		return av.VerifyUnder(algorithm, r.digest, anchor, header, height, nonce)
	}
	return r.Verify(algorithm, header, height, nonce)
}

// anchorHex is the job's period anchor as the accepted line records it, empty on an unanchored job.
func anchorHex(j stratum.Job) string {
	if !j.HasAnchor {
		return ""
	}
	return hex.EncodeToString(j.ProgramAnchor[:])
}

// errProofCarried marks a share the pool cannot recompute (the node verifies it); the replay keeps it.
var errProofCarried = errors.New("proof-carried share: kept as journalled")

// journalShare appends an accepted share to the journal with the job's
// header, document and digest, returning the journal sequence (zero
// without a journal or when the job is no longer retained).
func (s *Service) journalShare(sh stratum.Share, key string) uint64 {
	if s.journal == nil {
		return 0
	}
	job, ok := s.Server.JobByID(sh.JobID)
	if !ok {
		return 0
	}
	seq, err := s.journal.Accepted(shares.Line{
		ChainID:   s.chainID,
		Algorithm: job.Algorithm, Account: strings.ToLower(sh.Account), Member: memberOf(sh), Worker: sh.Worker, Key: key, JobID: sh.JobID,
		Header: hex.EncodeToString(job.Header[:]), Height: sh.Height, Digest: job.ParamsDigest, Params: string(job.Params),
		Nonce: sh.Nonce, Mix: hex.EncodeToString(sh.Mix[:]), Hash: hex.EncodeToString(sh.Hash[:]), Weight: sh.Weight,
		Coord: job.Coord, HasCoord: job.HasCoord, FeeBps: sh.FeeBps, FeeWaived: sh.FeeWaived,
		Ticket: hex.EncodeToString(sh.Ticket),
		Anchor: anchorHex(job),
	})
	if err != nil {
		log.Printf("share journal: append failed: %v (the share is in the window, not in the journal)", err)
		return 0
	}
	return seq
}

// expireJournal prunes the journal's outstanding shares older than the
// recency below the head, once per head.
func (s *Service) expireJournal(head uint64) {
	s.mu.Lock()
	if s.journal == nil || head == s.lastExpire {
		s.mu.Unlock()
		return
	}
	s.lastExpire = head
	s.mu.Unlock()
	recency := s.cfg.ShareRecency
	if recency == 0 {
		recency = 64
	}
	if s.cfg.ShareLane {
		// the share-lane form: the chain pays and the pool never finds a block, so a share's carriage or expiry
		// is the NODE's to name (igneum_shareAccount per address), never the pool's recency alone (the settlement
		// seat's finding of 12 October 2026: the recency expiry named 452 paid shares as expired); the pool's
		// recency expires only a share the node's account never named within twice the recency
		s.settleFromNode(head)
		if head > 2*recency {
			if n, err := s.journal.ExpireUnnamed(head-2*recency, s.cfg.RPCURL); err != nil {
				log.Printf("share journal: expiry failed: %v", err)
			} else if n > 0 {
				log.Printf("share journal: %d share(s) below height %d not in the node's account, expired by name", n, head-2*recency)
			}
		}
		return
	}
	if head <= recency {
		return
	}
	if n, err := s.journal.Expire(head - recency); err != nil {
		log.Printf("share journal: expiry failed: %v", err)
	} else if n > 0 {
		log.Printf("share journal: %d share(s) below height %d expired by name", n, head-recency)
	}
}

// shareAccount is igneum_shareAccount's by-height maps for one address.
type shareAccount struct {
	Carried map[string]uint64 `json:"carriedByHeight"`
	Reorg   map[string]uint64 `json:"expiredReorgByHeight"`
	Window  map[string]uint64 `json:"expiredWindowByHeight"`
}

func byHeight(m map[string]uint64) map[uint64]uint64 {
	out := map[uint64]uint64{}
	for k, v := range m {
		var h uint64
		if _, err := fmt.Sscanf(k, "%d", &h); err == nil {
			out[h] = v
		}
	}
	return out
}

// settleFromNode reads the node's account for every address with an
// outstanding share and writes its carriage and expiry onto the journal
// (called under s.mu, once per head; the RPC runs with the lock held for
// the few addresses a pool carries).
func (s *Service) settleFromNode(head uint64) {
	accounts := map[string]struct{}{}
	for _, l := range s.journal.Outstanding() {
		if l.Account != "" {
			accounts[l.Account] = struct{}{}
		}
	}
	// the deadline grows with the accounts (the protocol team's read of 07:3x UK, 14 October 2026: 1c answers a heavy
	// account in 0.17 s and refuses nothing; the pool's own twenty-second cap expired mid-settle once fifty
	// addresses were outstanding and every remaining read "timed out"), and each sealer is read at no more than
	// ten calls a second (1c's public listener limits a source to twenty a second, a burst of forty)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second+time.Duration(len(accounts))*time.Second)
	defer cancel()
	lastCall := map[string]time.Time{}
	// every sealer's account, not the source's alone (the re-point of 14:17Z, 12 October 2026, on pool-b: a new
	// source that never saw the shares the old one carried expired 1,333 of them by name): per height the
	// highest count any node names, so a share carried by any sealer's block is settled carried, and a share is
	// expired by name only when NO node's account names it within twice the recency
	// the lock for the state alone, never across the sealers' calls (the 0.1.18 roll of 20:04Z, 12 October 2026,
	// hung the pool on a lock taken twice; the 04:44Z gap of 14 October held it twenty seconds a settle)
	s.mu.Lock()
	nodes := append([]shareNode(nil), s.shareNodes...)
	coordNodes := make([][]shareNode, 0, len(s.coordNodes))
	for _, cns := range s.coordNodes {
		coordNodes = append(coordNodes, cns)
	}
	s.mu.Unlock()
	if len(nodes) == 0 {
		nodes = []shareNode{{url: s.cfg.RPCURL, node: s.node}}
	}
	// VectorWork: every coordinate's sealers too (the box soak of 04:30Z, 13 October 2026: 871 shares carried by
	// the coordinate sealers and none settled, the settle read the pool's node alone)
	seen := map[string]bool{}
	for _, n := range nodes {
		seen[n.url] = true
	}
	for _, cns := range coordNodes {
		for _, n := range cns {
			if !seen[n.url] {
				seen[n.url] = true
				nodes = append(nodes, n)
			}
		}
	}
	for a := range accounts {
		carried, reorg, window := map[uint64]uint64{}, map[uint64]uint64{}, map[uint64]uint64{}
		answered := 0
		var named []string
		for i := range nodes {
			var acc shareAccount
			if since := time.Since(lastCall[nodes[i].url]); since < 100*time.Millisecond {
				time.Sleep(100*time.Millisecond - since)
			}
			lastCall[nodes[i].url] = time.Now()
			cctx, ccancel := context.WithTimeout(ctx, 5*time.Second)
			err := nodes[i].node.Call(cctx, "igneum_shareAccount", []interface{}{a}, &acc)
			ccancel()
			if err != nil {
				if i == 0 {
					log.Printf("share journal: igneum_shareAccount(%s) on the source: %v", a, err)
				}
				continue
			}
			answered++
			named = append(named, nodes[i].url)
			for h, n := range byHeight(acc.Carried) {
				if n > carried[h] {
					carried[h] = n
				}
			}
			for h, n := range byHeight(acc.Reorg) {
				if n > reorg[h] {
					reorg[h] = n
				}
			}
			for h, n := range byHeight(acc.Window) {
				if n > window[h] {
					window[h] = n
				}
			}
		}
		if answered == 0 {
			continue
		}
		// the chain's account as last read, for the per-address page (what the sealers carry, by height)
		s.mu.Lock()
		if s.accountReads == nil {
			s.accountReads = map[string]accountRead{}
		}
		s.accountReads[strings.ToLower(a)] = accountRead{at: s.now(), head: head, sealers: named, carried: carried, reorg: reorg, window: window}
		s.mu.Unlock()
		sealer := s.cfg.RPCURL
		if answered > 1 {
			sealer = fmt.Sprintf("%s (+%d sealer(s) read: the highest count any names)", s.cfg.RPCURL, answered-1)
		}
		nc, nr, nw, err := s.journal.Settle(a, sealer, carried, reorg, window)
		if err != nil {
			log.Printf("share journal: settle %s: %v", a, err)
		} else if nc+nr+nw > 0 {
			log.Printf("share journal: %s at head %d: %d carried, %d expired by reorg, %d at the window's end, from %d sealer(s)' accounts", a, head, nc, nr, nw, answered)
		}
	}
}

// ReconcileFromNode writes a reconciled line for every share the journal
// expired that the node's account carries (the replayed journals of the
// restarts that expired paid shares by the recency), once at the start.
func (s *Service) ReconcileFromNode() {
	if s.journal == nil || !s.cfg.ShareLane {
		return
	}
	accounts, err := s.journal.Accounts()
	if err != nil {
		log.Printf("share journal: reconcile: %v", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	total := 0
	for _, a := range accounts {
		var acc shareAccount
		if err := s.sourceNode().Call(ctx, "igneum_shareAccount", []interface{}{a}, &acc); err != nil {
			log.Printf("share journal: reconcile %s: %v", a, err)
			continue
		}
		n, err := s.journal.Reconcile(a, s.cfg.RPCURL, byHeight(acc.Carried))
		if err != nil {
			log.Printf("share journal: reconcile %s: %v", a, err)
			continue
		}
		total += n
	}
	if total > 0 {
		log.Printf("share journal: %d expired share(s) the node's account carries, reconciled by a line each (the record corrected, never rewritten)", total)
	}
}

// Earnings is the latest rewards summary (zero when unknown).
func (s *Service) Earnings() rewards.Summary {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.earnings
}

// shareLimits is igneum_shareLimits as the pool reads it (zero when the node serves none).
type shareLimits struct {
	CapPerAddress uint64 `json:"shareCapPerAddress"`
	PoolCapacity  uint64 `json:"poolCapacity"`
	MaxShares     uint64 `json:"maxShares"`
	Recency       uint64 `json:"recency"`
}

// readLimits refreshes the node's share limits (every five minutes) and its pooled count (every call).
func (s *Service) readLimits(ctx context.Context) {
	now := s.now()
	s.mu.Lock()
	stale := now.Sub(s.limitsAt) >= 5*time.Minute
	s.mu.Unlock()
	if stale {
		var lim shareLimits
		if err := s.sourceNode().Call(ctx, "igneum_shareLimits", []interface{}{}, &lim); err == nil {
			s.mu.Lock()
			s.limits, s.limitsAt = lim, now
			s.mu.Unlock()
		}
	}
	var st struct {
		Pooled uint64 `json:"pooled"`
	}
	if err := s.sourceNode().Call(ctx, "igneum_shareStats", []interface{}{}, &st); err == nil {
		s.mu.Lock()
		s.pooledNow = st.Pooled
		s.mu.Unlock()
	}
	// the pool's own outstanding within the recency, across every address (the aggregate bound's count)
	if s.journal != nil {
		s.mu.Lock()
		head, rec := s.Stats.LastJobHeight, s.limits.Recency
		s.mu.Unlock()
		var n uint64
		for _, l := range s.journal.Outstanding() {
			if rec == 0 || l.Height+rec >= head {
				n++
			}
		}
		s.mu.Lock()
		s.outstandingRecent, s.acceptedSinceRead = n, 0
		s.mu.Unlock()
	}
}

// Admit is the admission bound before acceptance (stratum.Server.Admission): false with the reason when the
// address has reached the chain's per-height cap at the share's height, when the node's pool is at capacity, or
// when the addresses with shares outstanding reach what the list can pay within the recency.
func (s *Service) Admit(address string, height uint64) (bool, string) {
	return s.AdmitFor(address, stratum.Job{Height: height})
}

// AdmitFor is Admit with the job: under VectorWork the per-sealer bound is checked against the job's coordinate
// sealers (PA-53 as the sealer applies it, the protocol team's form of 06:1x UK, 13 October 2026: per address and per
// sealer, the shares that sealer accepted and still holds must be under the sortition's MaxShares; a share past it
// would be receipted and refused by the sealer, so it is refused here first).
func (s *Service) AdmitFor(address string, job stratum.Job) (bool, string) {
	a := strings.ToLower(address)
	height := job.Height
	if s.journal != nil {
		s.mu.Lock()
		maxShares := s.limits.MaxShares
		targets := append([]shareNode(nil), s.shareNodes...)
		if job.HasCoord {
			if ct := s.coordNodes[job.Coord]; len(ct) > 0 {
				targets = ct
			}
		}
		if len(targets) == 0 {
			targets = []shareNode{{url: s.cfg.RPCURL}}
		}
		s.mu.Unlock()
		if maxShares > 0 {
			// every sealer the share would go to must have room for the address; the pool's own count of what each
			// holds (the forwarded lines it accepted, still outstanding) stands in for the sealer's pooled count
			for _, t := range targets {
				if held := s.journal.HeldAt(a, t.url); uint64(held) >= maxShares {
					return false, fmt.Sprintf("address at the sealer's bound: %d shares held unpaid for the address at %s of the chain's %d (MaxShares); the share is dropped until the sealer carries or expires them", held, t.url, maxShares)
				}
			}
		}
	}
	s.mu.Lock()
	lim, pooled := s.limits, s.pooledNow
	n := uint64(0)
	if m := s.acceptedAt[a]; m != nil {
		n = m[height]
	}
	s.mu.Unlock()
	if lim.CapPerAddress > 0 && n >= lim.CapPerAddress {
		return false, fmt.Sprintf("address at cap: %d accepted at height %d of the chain's cap %d", n, height, lim.CapPerAddress)
	}
	if lim.PoolCapacity > 0 && pooled >= lim.PoolCapacity {
		return false, fmt.Sprintf("the node's pool is at capacity: %d pooled of %d", pooled, lim.PoolCapacity)
	}
	s.mu.Lock()
	spb := s.sharesPerBlock
	out := s.outstandingRecent + s.acceptedSinceRead
	q := s.forwardQ
	s.mu.Unlock()
	if q != nil && len(q) >= forwardQueueAdmit {
		// the forward queue is behind the sealers' pace: a share accepted now would be receipted and reach the
		// sealers minutes later with a stale parent (F-50's lag class)
		return false, fmt.Sprintf("pool at carriage: the forward queue holds %d shares waiting for the sealers (the bound %d); the share is dropped, the queue drains in seconds", len(q), forwardQueueAdmit)
	}
	// the bound in TIME: the share leaving the queue now waited longer than half a block clock, or the queue's
	// depth at the sealers' recent latency puts the expected wait past it; a share accepted now would reach the
	// sealers after its template (the read of 14:0x UK, 12 October 2026: median 24 to 123 s in the flood's minutes)
	s.mu.Lock()
	wait, waitAt, call, block := s.forwardWait, s.forwardWaitAt, s.sealerCall, s.cfg.BlockSeconds
	s.mu.Unlock()
	if q != nil && block > 0 {
		half := time.Duration(block / 2 * float64(time.Second))
		recent := s.now().Sub(waitAt) < half
		if recent && wait > half {
			return false, fmt.Sprintf("pool at carriage: shares wait %.0f s for the sealers (past half a block clock, %.0f s); the share is dropped, the queue drains in seconds", wait.Seconds(), half.Seconds())
		}
		if call > 0 && len(q) > 0 {
			if expected := time.Duration(float64(len(q)) / float64(forwardWorkers) * float64(call)); expected > half {
				return false, fmt.Sprintf("pool at carriage: %d shares queued at the sealers' %.0f ms a call would wait %.0f s (past half a block clock, %.0f s); the share is dropped", len(q), float64(call.Milliseconds()), expected.Seconds(), half.Seconds())
			}
		}
	}
	if spb > 0 && lim.Recency > 0 && s.journal != nil {
		// the AGGREGATE bound first: shares outstanding at the pool within the recency, across every address, at
		// most what the chain carries over the recency (sharesPerBlock, the lane's operating rate from the
		// template, times the recency: the node's own figure); a share past it would be receipted and expire
		// unpaid, so it is refused by name before the receipt
		if bound := spb * lim.Recency; out >= bound {
			return false, fmt.Sprintf("pool at carriage: %d shares outstanding within the recency against the %d the chain carries over it (%d a block for %d blocks)", out, bound, spb, lim.Recency)
		}
	}
	if lim.MaxShares > 0 && lim.Recency > 0 && s.journal != nil {
		bound := lim.MaxShares * lim.Recency
		seen := map[string]struct{}{}
		for _, l := range s.journal.Outstanding() {
			seen[strings.ToLower(l.Account)] = struct{}{}
		}
		if _, has := seen[a]; !has && uint64(len(seen)) >= bound {
			return false, fmt.Sprintf("the list bound: %d addresses with shares outstanding, the %d the chain's list can pay within the recency", len(seen), bound)
		}
	}
	return true, ""
}

// AcceptedLast60s counts the shares accepted in the last minute (the ring's bound is 8,192; past it the figure
// reads the ring's size, which the page shows as "8,192 or more").
func (s *Service) AcceptedLast60s() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	since := s.now().Add(-60 * time.Second)
	n := 0
	for _, t := range s.acceptTimes {
		if !t.IsZero() && t.After(since) {
			n++
		}
	}
	return n
}

// HashRate is the pool's share-weighted hash rate over the last window seconds: the sum of each accepted share's
// expected hashes (2^256 over its job's share target, the step inside it) over the window, in hashes a second, with
// the shares counted; the figure the feed serves as "from the pool's shares".
func (s *Service) HashRate(window time.Duration) (hps float64, shares int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	since := s.now().Add(-window)
	var sum float64
	for i, t := range s.acceptTimes {
		if !t.IsZero() && t.After(since) {
			sum += float64(s.acceptWeights[i])
			shares++
		}
	}
	return sum / window.Seconds(), shares
}

// AdmissionLimits is the health line's view of the bound's inputs.
func (s *Service) AdmissionLimits() map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]interface{}{"cap_per_address": s.limits.CapPerAddress, "pool_capacity": s.limits.PoolCapacity, "max_shares": s.limits.MaxShares, "recency": s.limits.Recency, "pooled_now": s.pooledNow, "read_at": s.limitsAt.UTC().Format(time.RFC3339),
		"outstanding_within_recency": s.outstandingRecent + s.acceptedSinceRead, "forward_wait_s": s.forwardWait.Seconds(), "sealer_call_ms": s.sealerCall.Milliseconds(), "forward_queue": func() int {
			if s.forwardQ == nil {
				return 0
			}
			return len(s.forwardQ)
		}(), "forward_queue_bound": forwardQueueAdmit,
		"carriage_bound": s.sharesPerBlock * s.limits.Recency, "carriage_bound_factors": map[string]uint64{"shares_per_block": s.sharesPerBlock, "recency": s.limits.Recency}}
}

// jobKeyCoord is the share-lane job's key under VectorWork: the address and worker, with the coordinate when
// the chain serves templates per coordinate (one job per (address, worker, coord)).
func jobKeyCoord(addr, worker string, coord uint8, hasCoord bool) string {
	k := jobKeyOf(addr, worker)
	if hasCoord {
		return k + "#" + fmt.Sprint(coord)
	}
	return k
}

// stepKeyCoord is the five-minute rule's key under VectorWork: the worker's step is per coordinate (a card
// paces each relation differently), per worker name across addresses as before.
func stepKeyCoord(addr, worker string, coord uint8, hasCoord bool) string {
	k := stepKey(addr, worker)
	if hasCoord {
		return k + "#" + fmt.Sprint(coord)
	}
	return k
}

// jobKeyOf is the share-lane job's key: the address, with the worker name when the session has one.
func jobKeyOf(addr, worker string) string {
	if worker == "" {
		return strings.ToLower(addr)
	}
	return strings.ToLower(addr) + "/" + worker
}

// stepKey is the five-minute rule's key: the worker name across addresses (the member's and the fee session's
// shares one rate), or the address for a session with no worker name.
func stepKey(addr, worker string) string {
	if worker == "" {
		return "addr:" + strings.ToLower(addr)
	}
	return "worker:" + worker
}

// periodAnchor resolves a programmed document's anchor for a template height by the chain form (the hash team's
// rule of 03:3x UK, 13 October 2026): period = height div P, anchor height = period x P - D, the canonical block
// hash there (the genesis hash while the anchor height is below 1), read from the node that serves the template.
func (s *Service) periodAnchor(ctx context.Context, node Node, coord uint8, hasCoord bool, digest string, height, periodBlocks uint64) ([32]byte, uint64, error) {
	if periodBlocks == 0 {
		return [32]byte{}, 0, nil
	}
	// the node's rule (program.go programAnchorHeight): the anchor of the period a height falls in is the block at
	// p x P minus D, D the coordinate's reveal (its registration's RevealBlocks, else the chain's), the genesis
	// hash where that is below one; D is learned from the coordinate's own named templates (start minus the
	// node's AnchorHeight) and the pool's -program-reveal-blocks stands only until one is seen (the review's reading
	// of 18:19 UK, 15 October 2026, on 2e6d8204: a lookup at the period's first block sat 64 blocks off on a
	// coordinate whose D is 64)
	s.mu.Lock()
	reveal, _ := s.revealLocked(coord, hasCoord, digest)
	s.mu.Unlock()
	return s.periodAnchorWith(ctx, node, height, periodBlocks, reveal)
}

// revealLocked is a coordinate's reveal D and whether the registry stated it: the registry's revealBlocks for the
// coordinate when above 0; the chain's default (-program-reveal-blocks, the node's ProgramRevealDefault) when the
// registry answered 0 for it; else the document's taught figure, else the default. s.mu is held.
func (s *Service) revealLocked(coord uint8, hasCoord bool, digest string) (uint64, bool) {
	if s.revealReg != nil {
		d, ok := s.revealReg[coord]
		if !hasCoord || !ok {
			d, ok = s.revealRegDigest[digest]
		}
		if ok {
			if d > 0 {
				return d, true
			}
			return s.cfg.ProgramRevealBlocks, true
		}
	}
	if d, ok := s.revealOf[digest]; ok {
		return d, false
	}
	return s.cfg.ProgramRevealBlocks, false
}

// readRegistryReveals reads each coordinate's registered reveal (igneum_vectorRegistry "latest": revealBlocks per
// coordinate) once per height from the template node, beside the pay list; a read that fails keeps the last; a
// node that does not serve the view leaves revealReg nil (the node's own named anchors teach D then).
func (s *Service) readRegistryReveals(ctx context.Context, node Node) {
	var out struct {
		Height      uint64 `json:"height"`
		Coordinates []struct {
			Index        uint8  `json:"index"`
			RevealBlocks uint64 `json:"revealBlocks"`
			ParamsDigest string `json:"paramsDigest"`
		} `json:"coordinates"`
	}
	s.mu.Lock()
	last, had := s.revealRegHeight, s.revealReg != nil
	s.mu.Unlock()
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := node.Call(cctx, "igneum_vectorRegistry", []interface{}{"latest"}, &out); err != nil || len(out.Coordinates) == 0 {
		return
	}
	if had && out.Height == last && last != 0 {
		return
	}
	reg := map[uint8]uint64{}
	byDigest := map[string]uint64{}
	disagree := map[string]bool{}
	for _, c := range out.Coordinates {
		reg[c.Index] = c.RevealBlocks
		if d := strings.ToLower(c.ParamsDigest); d != "" {
			if prev, seen := byDigest[d]; seen && prev != c.RevealBlocks {
				disagree[d] = true // two coordinates on one document with different reveals: no figure by digest
			}
			byDigest[d] = c.RevealBlocks
		}
	}
	for d := range disagree {
		delete(byDigest, d)
	}
	s.mu.Lock()
	changed := !had || len(reg) != len(s.revealReg)
	for i, d := range reg {
		if !changed && s.revealReg[i] != d {
			changed = true
		}
	}
	s.revealReg, s.revealRegDigest, s.revealRegHeight = reg, byDigest, out.Height
	s.mu.Unlock()
	if changed {
		log.Printf("share lane: the registry's reveal per coordinate at height %d: %v (0 the chain's default of %d)", out.Height, reg, s.cfg.ProgramRevealBlocks)
	}
}

// periodAnchorWith resolves the period's anchor for a height under the reveal D given: the canonical block hash at
// the period's start minus D (the genesis hash while that is below 1), cached by anchor height.
func (s *Service) periodAnchorWith(ctx context.Context, node Node, height, periodBlocks, reveal uint64) ([32]byte, uint64, error) {
	var zero [32]byte
	if periodBlocks == 0 {
		return zero, 0, nil
	}
	start := (height / periodBlocks) * periodBlocks
	var at uint64
	if start >= reveal+1 {
		at = start - reveal
	}
	s.mu.Lock()
	if a, ok := s.anchors[at]; ok {
		s.mu.Unlock()
		return a, at, nil
	}
	s.mu.Unlock()
	var blk struct {
		Hash string `json:"hash"`
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := node.Call(cctx, "eth_getBlockByNumber", []interface{}{fmt.Sprintf("0x%x", at), false}, &blk); err != nil {
		return zero, at, err
	}
	b, err := hex.DecodeString(strings.TrimPrefix(blk.Hash, "0x"))
	if err != nil || len(b) != 32 {
		return zero, at, fmt.Errorf("the block at %d has no hash", at)
	}
	var a [32]byte
	copy(a[:], b)
	s.mu.Lock()
	if s.anchors == nil {
		s.anchors = map[uint64][32]byte{}
	}
	s.anchors[at] = a
	if len(s.anchors) > 64 {
		for k := range s.anchors {
			if k+64*periodBlocks < at {
				delete(s.anchors, k)
			}
		}
	}
	s.mu.Unlock()
	return a, at, nil
}

// coordSource is the coordinate's template source under VectorWork (its first sealer), or nil when the coordinate
// has no sealers of its own (the pool's node then, with [coord]).
func (s *Service) coordSource(coord uint8) Node {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ns := s.coordNodes[coord]; len(ns) > 0 {
		return ns[0].node
	}
	return nil
}

// coordTargets is the coordinate's fan-out under VectorWork (every sealer that seals it), or nil.
func (s *Service) coordTargets(coord uint8) []shareNode {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]shareNode(nil), s.coordNodes[coord]...)
}

// templateNode is the node the share lane takes its template from this poll: the source, or a fan-out sealer at
// the head when the source's share height has stood still past three block clocks (the failover guard); the
// move and the return are named on the log and /shares (TemplateSource).
// sourceNode is the node the pool's reads go to now: the fan-out sealer the guard moved templates to while the
// source is still, else the source (the public pool's hour of 06:46Z, 13 October 2026: the source was a destroyed
// pod, the guard fed templates from a fan-out sealer, and the limits, stats, reconcile and settle still asked the
// dead one; the source follows the guard).
// It takes NO lock (several of its callers hold s.mu: the credit path, the settle; the 0.1.18 hang of 20:04Z,
// 12 October 2026, was a lock taken twice on this path): the guard's choice is published in an atomic.
func (s *Service) sourceNode() Node {
	if h, ok := s.guardNode.Load().(guardHolder); ok && h.node != nil {
		return h.node
	}
	return s.node
}

// guardHolder wraps the guard's node for the atomic (an atomic.Value refuses a nil interface; the holder's nil
// node means the source).
type guardHolder struct{ node Node }

// publishSource records the guard's current node for sourceNode (called with s.mu held or not).
func (s *Service) publishSource(n Node) {
	s.guardNode.Store(guardHolder{node: n})
}

func (s *Service) templateNode(ctx context.Context) Node {
	s.mu.Lock()
	cur := s.templateFrom
	nodes := append([]shareNode(nil), s.shareNodes...)
	last, lastAt := s.lastHeight, s.lastHeightAt
	block := s.cfg.BlockSeconds
	s.mu.Unlock()
	if len(nodes) < 2 || block <= 0 {
		return s.node
	}
	now := s.now()
	// a stall is the share height standing still past three block clocks: after a template, or from the start
	// when none has come (last == 0 with lastAt set by Run); a source that ANSWERS NOTHING (a dial error, a
	// timeout: d10-sealer-1c's roll of 04:34:25Z, 14 October 2026, two minutes without a job while every miner's
	// no-work rule re-dialled) is a stall after one block clock
	s.mu.Lock()
	errAt := s.sourceErrAt
	s.mu.Unlock()
	stalled := !lastAt.IsZero() && (now.Sub(lastAt).Seconds() > 3*block || (!errAt.IsZero() && now.Sub(lastAt).Seconds() > block))
	if cur == 0 && !stalled {
		return s.node
	}
	// the stall, or a failover in force: read every sealer's share height cheaply and take the one furthest ahead
	best, bestH := cur, uint64(0)
	heights := make([]uint64, len(nodes))
	for i := range nodes {
		var obj map[string]interface{}
		cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
		err := nodes[i].node.Call(cctx, "igneum_getWork", []interface{}{}, &obj)
		cancel()
		if err != nil {
			continue
		}
		if v, ok := obj["shareHeight"]; ok {
			switch t := v.(type) {
			case string:
				heights[i], _ = anyHeight(t)
			case float64:
				heights[i] = uint64(t)
			}
		}
		if heights[i] > bestH {
			best, bestH = i, heights[i]
		}
	}
	// the return: the source at the head (within one block of the best) for ten minutes; a single lagging read
	// does not start the ten minutes over (the old public test network (chain 7320): its one-block races put the source two behind at some read in
	// every ten minutes, so the pool never went home after 1c's roll of 04:34Z, 14 October 2026): the source has
	// to lag for a whole minute to reset the clock
	if cur != 0 && heights[0] > 0 && heights[0]+1 >= bestH {
		s.mu.Lock()
		if s.failoverSince.IsZero() {
			s.failoverSince = now
		}
		s.failoverLagSince = time.Time{}
		back := now.Sub(s.failoverSince) >= 10*time.Minute
		s.mu.Unlock()
		if back {
			s.mu.Lock()
			s.templateFrom, s.failoverSince = 0, time.Time{}
			s.mu.Unlock()
			s.publishSource(nil)
			log.Printf("share lane: the template source is back at the head (%d) for ten minutes: templates from the source again", heights[0])
			return s.node
		}
	} else if cur != 0 {
		s.mu.Lock()
		if s.failoverLagSince.IsZero() {
			s.failoverLagSince = now
		}
		if now.Sub(s.failoverLagSince) >= time.Minute {
			s.failoverSince = time.Time{}
		}
		s.mu.Unlock()
	}
	if best != cur && (cur == 0 || bestH > heights[cur]+1) {
		s.mu.Lock()
		s.templateFrom = best
		// the return clock starts afresh with each failover (1c's 0.10.11 roll of 09:04Z, 14 October 2026: a clock
		// left from an earlier failover sent the pool home three seconds after it left)
		s.failoverSince, s.failoverLagSince = time.Time{}, time.Time{}
		s.mu.Unlock()
		if best > 0 {
			s.publishSource(nodes[best].node)
		} else {
			s.publishSource(nil)
		}
		log.Printf("share lane: the template source's share height stood at %d for %.0f s (over three block clocks) while %s serves %d: templates from %s until the source is at the head for ten minutes", last, now.Sub(lastAt).Seconds(), nodes[best].url, bestH, nodes[best].url)
		cur = best
	}
	return nodes[cur].node
}

// anyHeight reads a height as 0x-hex or decimal.
func anyHeight(t string) (uint64, bool) {
	t = strings.TrimSpace(t)
	if strings.HasPrefix(t, "0x") {
		var v uint64
		for _, c := range t[2:] {
			var d uint64
			switch {
			case c >= '0' && c <= '9':
				d = uint64(c - '0')
			case c >= 'a' && c <= 'f':
				d = uint64(c-'a') + 10
			case c >= 'A' && c <= 'F':
				d = uint64(c-'A') + 10
			default:
				return 0, false
			}
			v = v<<4 | d
		}
		return v, true
	}
	var v uint64
	for _, c := range t {
		if c < '0' || c > '9' {
			return 0, false
		}
		v = v*10 + uint64(c-'0')
	}
	return v, true
}

// TemplateSource is the health line's view of the failover guard.
func (s *Service) TemplateSource() map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	url := "the pool's node"
	if s.templateFrom < len(s.shareNodes) && s.templateFrom > 0 {
		url = s.shareNodes[s.templateFrom].url
	}
	return map[string]interface{}{"from": url, "failover": s.templateFrom != 0, "last_share_height": s.lastHeight, "last_height_at": s.lastHeightAt.UTC().Format(time.RFC3339),
		"rule": "when the template source's share height stands still past three block clocks while a fan-out sealer serves a height ahead, templates come from that sealer; back to the source when it is at the head for ten minutes; named on the log"}
}

// Ticket hands a proof-carried share's verified ticket ahead of its forward (stratum.TicketSink): kept by job and
// nonce until the forward takes it.
func (s *Service) Ticket(job stratum.Job, nonce uint64, ticket []byte) {
	s.mu.Lock()
	if s.tickets == nil {
		s.tickets = map[string][]byte{}
	}
	s.tickets[job.ID+":"+fmt.Sprint(nonce)] = ticket
	s.mu.Unlock()
}

func (s *Service) takeTicket(job stratum.Job, nonce uint64) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := job.ID + ":" + fmt.Sprint(nonce)
	t := s.tickets[k]
	delete(s.tickets, k)
	return t
}

// forwardItem is one share-lane forward waiting for a worker.
type forwardItem struct {
	job    stratum.Job
	nonce  uint64
	mix    [32]byte
	at     time.Time // when it was accepted (the receipt's minute): the queue wait is measured from it
	ticket []byte
}

// forwardWorkers is how many forwards run at once; forwardQueue how many wait.
const (
	forwardWorkers = 64
	forwardQueue   = 8192
	// forwardQueueAdmit is the queue depth at which a share is refused before acceptance ("pool at carriage: the
	// forward queue holds N shares"): a share accepted now would reach the sealers only after the shares ahead
	// of it, and a parent goes stale in minutes (F-50's lag)
	forwardQueueAdmit = 2048
)

// startForwarders starts the share lane's forward workers (once, from Run).
func (s *Service) startForwarders(ctx context.Context) {
	s.mu.Lock()
	if s.forwardQ != nil {
		s.mu.Unlock()
		return
	}
	s.forwardQ = make(chan forwardItem, forwardQueue)
	q := s.forwardQ
	s.mu.Unlock()
	for i := 0; i < forwardWorkers; i++ {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case it := <-q:
					t0 := time.Now()
					s.mu.Lock()
					s.forwardWait, s.forwardWaitAt = t0.Sub(it.at), t0
					s.mu.Unlock()
					_ = s.submitShareTicket(ctx, it.job, it.nonce, it.mix, it.ticket)
					took := time.Since(t0)
					s.mu.Lock()
					if s.sealerCall == 0 {
						s.sealerCall = took
					} else {
						s.sealerCall = (s.sealerCall*7 + took) / 8
					}
					s.mu.Unlock()
				}
			}
		}()
	}
}

// Block implements stratum.Sink: submit the solution to the node first.
func (s *Service) Block(ctx context.Context, job stratum.Job, nonce uint64, mix, hash [32]byte) error {
	if job.Kind == "block" {
		var lastErr error
		// a block session's solution on a coordinate whose sealer seals nothing itself (vectorwork6's bandwidth
		// coordinate, 13 October 2026): igneum_submitWork(nonce, headerHash, mix) on the coordinate's sealers in
		// order, the first acceptance the block (the node assembles its pooled shares); the share lane's pay unchanged
		// a solution for a height the coordinate's template has left is stale before it is sent (pool-b2 on
		// vectorwork6, 16:27Z to 17:02Z 13 October 2026: 346 "work submitted is too old" answers beside 56 blocks,
		// a 5090's solutions landing after the chain's seventeen-second pace moved the height): refused here, no call
		if job.HasCoord {
			s.mu.Lock()
			st, has := s.coordState[job.Coord]
			s.mu.Unlock()
			if has && st.height > job.Height {
				s.mu.Lock()
				s.Stats.BlocksStale++
				s.mu.Unlock()
				err := fmt.Errorf("stale: coordinate %d's template is at height %d", job.Coord, st.height)
				log.Printf("block: member %s worker %q's solution on coordinate %d at height %d not submitted: %v", job.Member, job.Worker, job.Coord, job.Height, err)
				return err
			}
			// one block a height a coordinate (the node team's reading of 00:17 UK, 14 October 2026: twelve "sealed"
			// answers a height on coordinate 1, the sealer accepting every valid solution while only the first can
			// stand): a second solution for a height this pool has already sealed on the coordinate is not sent
			s.mu.Lock()
			sealed := s.sealedAt[job.Coord]
			if in := s.submittedAt[job.Coord]; in > sealed {
				sealed = in
			}
			if sealed > 0 && job.Height <= sealed {
				s.Stats.BlocksDuplicate++
				s.mu.Unlock()
				return fmt.Errorf("sealed: this pool sealed coordinate %d at height %d already", job.Coord, sealed)
			}
			// marked before the call (two solutions arriving together both passed the read at 1618 on vectorwork7,
			// 01:30:32Z 14 October 2026); a refusal that is not "too old" gives the height back; the mark is the
			// in-flight one (0.1.82): sealedAt moves on the chain's hash alone
			if s.submittedAt == nil {
				s.submittedAt = map[uint8]uint64{}
			}
			prevSealed := s.submittedAt[job.Coord]
			s.submittedAt[job.Coord] = job.Height
			s.mu.Unlock()
			defer func() {
				if lastErr != nil && !strings.Contains(lastErr.Error(), "too old") {
					s.mu.Lock()
					if s.submittedAt[job.Coord] == job.Height {
						s.submittedAt[job.Coord] = prevSealed
					}
					s.mu.Unlock()
				}
			}()
		}
		targets := s.coordTargets(job.Coord)
		if len(targets) == 0 {
			targets = []shareNode{{url: s.cfg.RPCURL, node: s.node}}
		}
		params := []interface{}{fmt.Sprintf("0x%016x", nonce), "0x" + hex.EncodeToString(job.Header[:]), "0x" + hex.EncodeToString(mix[:])}
		for _, t := range targets {
			// the igneum namespace answers {"accepted", "reason"} (the protocol team's form of 15:49 UK, 13 October 2026);
			// the legacy eth_submitWork shape answers a bare bool; both read
			var raw json.RawMessage
			cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := t.node.Call(cctx, "igneum_submitWork", params, &raw)
			cancel()
			ok, reason, answerHash := false, "", ""
			if err == nil {
				var ans struct {
					Accepted bool   `json:"accepted"`
					Reason   string `json:"reason"`
					Hash     string `json:"hash"` // the sealed block's hash (the node from 0.10.52, F-418)
				}
				if json.Unmarshal(raw, &ans) == nil && (ans.Accepted || ans.Reason != "") {
					ok, reason, answerHash = ans.Accepted, ans.Reason, strings.ToLower(ans.Hash)
				} else {
					var b bool
					if json.Unmarshal(raw, &b) == nil {
						ok = b
					}
				}
			}
			if err == nil && ok {
				// 0.1.80 (17 October 2026, the review lead's wording): the pool knows the RPC's answer, never the seal; the
				// answer is logged as the node sent it (F-418: "accepted" with nothing sealed and no line on the node)
				log.Printf("BLOCK: member %s worker %q's solution on coordinate %d at height %d accepted by the sealer's RPC %s (nonce %d, hash %x..; the answer %s); the seal is the chain's to show", job.Member, job.Worker, job.Coord, job.Height, t.url, nonce, hash[:6], rawAnswer(raw))
				// 0.1.82 (F-418's pool half, the security review's rule): the block is counted on the chain's hash alone
				go s.confirmBlock(job, nonce, answerHash)
				return nil
			}
			if err != nil {
				lastErr = err
			} else if reason != "" {
				lastErr = fmt.Errorf("igneum_submitWork: %s (the answer %s)", reason, rawAnswer(raw))
			} else {
				lastErr = fmt.Errorf("igneum_submitWork answered false (the answer %s)", rawAnswer(raw))
			}
		}
		log.Printf("block: member %s worker %q's solution on coordinate %d at height %d refused: %v", job.Member, job.Worker, job.Coord, job.Height, lastErr)
		return lastErr
	}
	if s.cfg.ShareLane && job.Member != "" {
		// the share lane: a solution at the share target is a share under the member's address, never a block; the
		// forward to the sealers goes through the workers so the miner's reply never waits on a sealer (the pool's
		// acceptance is the receipt's basis; the sealers' answers go on the journal's forwarded lines as before)
		s.mu.Lock()
		q := s.forwardQ
		s.mu.Unlock()
		ticket := s.takeTicket(job, nonce)
		if q == nil {
			return s.submitShareTicket(ctx, job, nonce, mix, ticket)
		}
		q <- forwardItem{job: job, nonce: nonce, mix: mix, at: s.now(), ticket: ticket}
		return nil
	}
	node := s.node
	if job.Member != "" {
		// the member-node form: the solution goes to the member's OWN node, whose template it is
		m := s.members[job.Member]
		if m == nil {
			return fmt.Errorf("block at height %d: no node for member %s", job.Height, job.Member)
		}
		if s.cfg.Withhold != "" && s.cfg.Withhold == job.Member {
			// the TEST FIXTURE (GUARD-C-CLOSURE 3.3 P3): the coordinator drops the member's solution; the member's own
			// direct submission (nc-miner -self-submit) is the deterministic recovery the row reads
			s.mu.Lock()
			s.Stats.Withheld++
			s.mu.Unlock()
			log.Printf("WITHHELD (test fixture): the coordinator drops member %s's block solution at height %d (nonce %d); the member's own submission decides", job.Member, job.Height, nonce)
			return nil
		}
		node = m.node
	}
	err := node.SubmitWork(ctx, nonce, job.Header, mix)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Stats.Blocks++
	if err != nil {
		log.Printf("block at height %d rejected by the node: %v", job.Height, err)
		return err
	}
	s.found = append(s.found, foundBlock{Job: job, Nonce: nonce, Mix: mix, Window: s.window.Snapshot()})
	if job.Member != "" {
		log.Printf("block solution at height %d submitted (nonce %d) to member %s's own node (its template %x)", job.Height, nonce, job.Member, job.Header[:8])
	} else {
		log.Printf("block solution at height %d submitted (nonce %d)", job.Height, nonce)
	}
	return nil
}

// Share implements stratum.Sink: a verified share joins the window.
func (s *Service) Share(sh stratum.Share) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Stats.Shares++
	s.feeAccount(sh)
	key := ""
	if sh.Signed {
		key = hex.EncodeToString(sh.Key[:])
	}
	seq := s.journalShare(sh, key)
	if seq > 0 {
		if s.seqOf == nil {
			s.seqOf = map[string]uint64{}
		}
		s.seqOf[strings.ToLower(sh.Account)+"/"+sh.JobID+"/"+fmt.Sprint(sh.Nonce)] = seq
		if len(s.seqOf) > 200000 {
			s.seqOf = map[string]uint64{}
		}
	}
	// the admission bound's count per address and height, pruned past twice the recency
	if s.acceptedAt == nil {
		s.acceptedAt = map[string]map[uint64]uint64{}
	}
	a := strings.ToLower(sh.Account)
	if s.acceptedAt[a] == nil {
		s.acceptedAt[a] = map[uint64]uint64{}
	}
	s.acceptedAt[a][sh.Height]++
	s.acceptedSinceRead++
	if s.acceptTimes == nil {
		s.acceptTimes = make([]time.Time, 8192)
		s.acceptWeights = make([]uint64, 8192)
	}
	s.acceptTimes[s.acceptHead] = s.now()
	s.acceptWeights[s.acceptHead] = sh.Weight
	s.acceptHead = (s.acceptHead + 1) % len(s.acceptTimes)
	if r := s.limits.Recency; r > 0 && sh.Height > 2*r {
		for h := range s.acceptedAt[a] {
			if h < sh.Height-2*r {
				delete(s.acceptedAt[a], h)
			}
		}
	}
	s.window.Add(shares.Share{Account: strings.ToLower(sh.Account), Worker: sh.Worker, Key: key, JobID: sh.JobID, Weight: sh.Weight, Height: sh.Height, Journal: seq})
	if sh.Signed {
		log.Printf("signed share accepted: key %s account %s job %s nonce %d", key[:16], strings.ToLower(sh.Account), sh.JobID, sh.Nonce)
	}
}

// Shares is the window's current snapshot for the /shares listing
// (POOL-NONCUSTODIAL.md section 3: the published journal lets any member
// recompute its own credit): every share with its key, account, worker,
// job, height, weight and sequence, oldest first, and the window's total.
func (s *Service) Shares() map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := s.window.Snapshot()
	rows := make([]map[string]interface{}, 0, len(snap))
	for _, sh := range snap {
		rows = append(rows, map[string]interface{}{"seq": sh.Seq, "key": sh.Key, "account": sh.Account, "worker": sh.Worker, "job_id": sh.JobID, "height": sh.Height, "weight": sh.Weight})
	}
	return map[string]interface{}{"window_target": s.window.Target, "window_total": s.window.Total(), "count": len(rows), "shares": rows}
}

// coordState is one coordinate's chain state as the latest template stated it.
type coordState struct {
	difficulty *big.Int
	rewardWei  *big.Int
	lastHeight uint64
	height     uint64
	at         time.Time
}

// laneWorthLocked is the coordinate's lane_worth for a job (0.1.86, F-496 under F-497's payout rule; the caller holds
// s.mu): the coordinate's income over the pool's carried share weight on it over the LAST CLOSED pay window, wei per
// unit of share weight, the member's marginal pay there as the pool paid it; "" before the first close, on a one-form
// chain, and on a coordinate the window carried no work on. The 0.1.84 form (the lane's difficulty over the pool's
// chain-normalised work, which cancelled to one over the carried weight) never rolled.
func (s *Service) laneWorthLocked(coord uint8, hasCoord bool) string {
	if !hasCoord {
		return ""
	}
	return s.laneWorthAt(coord)
}

// CoordAccounting is the outside review's per-coordinate accounting for /shares (the project's order of 01:3x UK, 13 October
// 2026): per coordinate the issuance share (its reward per block over the sum of every coordinate's), the
// accepted work, the paid work, the difficulty-normalised chain work (the paid work times the coordinate's
// difficulty: what the chain credits), the pool's fee on the paid work (fee_bps of it), the orphans (expired by
// reorg) and the expiries, from the journal; pool-wide, or one account's when account is set.
func (s *Service) CoordAccounting(account string) []map[string]interface{} {
	if s.journal == nil || len(s.cfg.Coordinates) == 0 {
		return nil
	}
	ledgers := s.journal.CoordLedgers(account)
	s.mu.Lock()
	states := map[uint8]coordState{}
	for c, st := range s.coordState {
		states[c] = st
	}
	fee := s.cfg.FeeBps
	s.mu.Unlock()
	total := new(big.Int)
	for _, c := range s.cfg.Coordinates {
		if st, ok := states[c]; ok && st.rewardWei != nil {
			total.Add(total, st.rewardWei)
		}
	}
	rows := []map[string]interface{}{}
	for _, c := range s.cfg.Coordinates {
		l := ledgers[c]
		row := map[string]interface{}{"coord": c, "accepted": l.Accepted, "accepted_work": l.AcceptedWork, "paid": l.Carried, "paid_work": l.CarriedWork,
			"orphaned": l.ExpiredReorg, "orphaned_work": l.ExpiredReorgWork, "expired_window": l.ExpiredWindow, "expired_window_work": l.ExpiredWindowWork,
			"expired_by_name": l.ExpiredByName, "expired_by_name_work": l.ExpiredByNameWork,
			"fee_work": l.FeeWork, "paid_waived": l.CarriedWaived, "paid_charged": l.CarriedCharged, "declared_fee_bps": fee}
		if st, ok := states[c]; ok {
			if st.difficulty != nil {
				row["difficulty"] = st.difficulty.String()
				row["chain_work_normalised"] = new(big.Int).Mul(new(big.Int).SetUint64(l.CarriedWork), st.difficulty).String()
			}
			if st.rewardWei != nil {
				row["reward_wei"] = st.rewardWei.String()
				if total.Sign() > 0 {
					share := new(big.Rat).SetFrac(st.rewardWei, total)
					f, _ := share.Float64()
					row["issuance_share"] = f
				}
			}
			row["last_height"] = st.lastHeight
			row["template_height"] = st.height
			row["template_at"] = st.at.UTC().Format(time.RFC3339)
		}
		rows = append(rows, row)
	}
	return rows
}

// accountRead is the chain's share account for one address as the settle last read it from the sealers.
type accountRead struct {
	at      time.Time
	head    uint64
	sealers []string
	carried map[uint64]uint64
	reorg   map[uint64]uint64
	window  map[uint64]uint64
}

// AddressWindow is the per-address page's window in blocks (the disclosure's earnings_window_blocks).
const AddressWindow = rewards.Window

// Address is the public per-address page (the E1 study's finding 5, 12 October 2026: no served page named a
// miner's own shares or earnings): the address's accepted shares by height over the window as the journal
// holds them, what the chain carried as the sealers' accounts last read, its workers' steps, and the earnings as
// the chain paid them: the address's balance now and at the window's start, when the node serves the state.
func (s *Service) Address(ctx context.Context, address string) map[string]interface{} {
	a := strings.ToLower(address)
	s.mu.Lock()
	head := s.Stats.LastJobHeight
	var minHeight uint64
	if head > AddressWindow {
		minHeight = head - AddressWindow
	}
	workers := []map[string]interface{}{}
	for k, j := range s.shareJobs {
		if !strings.HasPrefix(k, a) {
			continue
		}
		row := map[string]interface{}{"worker": j.worker, "step": j.step, "height": j.height, "shares_at_height": j.sharesAtHeight, "raised_this_height": j.raised}
		if j.hasCoord {
			row["coord"] = j.coord
		}
		if ws := s.workerSteps[stepKeyCoord(a, j.worker, j.coord, j.hasCoord)]; ws != nil {
			row["rule_step"] = ws.ruleStep
			row["pace_a_height"] = ws.pace
			row["step_read_at"] = ws.stepAt.UTC().Format(time.RFC3339)
		}
		workers = append(workers, row)
	}
	var chain map[string]interface{}
	if r, has := s.accountReads[a]; has {
		rows := []map[string]interface{}{}
		heights := map[uint64]struct{}{}
		for h := range r.carried {
			heights[h] = struct{}{}
		}
		for h := range r.reorg {
			heights[h] = struct{}{}
		}
		for h := range r.window {
			heights[h] = struct{}{}
		}
		var carriedTotal uint64
		for h := range heights {
			if h < minHeight {
				continue
			}
			rows = append(rows, map[string]interface{}{"height": h, "carried": r.carried[h], "expired_reorg": r.reorg[h], "expired_window": r.window[h]})
			carriedTotal += r.carried[h]
		}
		sort.Slice(rows, func(x, y int) bool { return rows[x]["height"].(uint64) < rows[y]["height"].(uint64) })
		chain = map[string]interface{}{"read_at": r.at.UTC().Format(time.RFC3339), "read_at_head": r.head, "sealers": r.sealers, "by_height": rows, "carried_in_window": carriedTotal,
			"rule": "the sealers' igneum_shareAccount for the address as the pool last read it (per height the highest count any sealer names); a carried share is one the chain paid at that height"}
	}
	s.mu.Unlock()
	var journalRows []shares.AccountHeight
	if s.journal != nil {
		journalRows = s.journal.Account(a, minHeight)
	}
	var totals shares.AccountHeight
	for _, r := range journalRows {
		totals.Accepted += r.Accepted
		totals.Outstanding += r.Outstanding
		totals.Carried += r.Carried
		totals.ExpiredReorg += r.ExpiredReorg
		totals.ExpiredWindow += r.ExpiredWindow
		totals.ExpiredByName += r.ExpiredByName
	}
	earnings := map[string]interface{}{"window_blocks": AddressWindow, "from_height": minHeight, "to_height": head,
		"rule": "the chain pays the address directly at each block for the shares it carries (nothing is held by the pool); balance_wei is the address's balance on the chain now and balance_wei_at_window_start at the window's first height, earned_wei_over_window their difference, each absent when the node does not serve that state; spending from the address over the window lowers the difference"}
	cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	var now, start string
	if err := s.sourceNode().Call(cctx, "eth_getBalance", []interface{}{a, "latest"}, &now); err == nil && strings.HasPrefix(now, "0x") {
		if n, ok := new(big.Int).SetString(now[2:], 16); ok {
			earnings["balance_wei"] = n.String()
			if err := s.sourceNode().Call(cctx, "eth_getBalance", []interface{}{a, fmt.Sprintf("0x%x", minHeight)}, &start); err == nil && strings.HasPrefix(start, "0x") {
				if st, ok := new(big.Int).SetString(start[2:], 16); ok {
					earnings["balance_wei_at_window_start"] = st.String()
					earnings["earned_wei_over_window"] = new(big.Int).Sub(n, st).String()
				}
			}
		}
	}
	out := map[string]interface{}{
		"address": a, "read_at": s.now().UTC().Format(time.RFC3339), "head": head,
		"shares": map[string]interface{}{"from_height": minHeight, "to_height": head, "totals": totals, "by_height": journalRows,
			"rule": "accepted = shares the pool accepted and receipted for the address at that share height; outstanding = not yet named by a sealer's account; carried = the chain paid them; expired = by reorg, at the window's end, or by the pool's name when no sealer named the share within twice the recency"},
		"chain":    chain,
		"workers":  workers,
		"earnings": earnings,
	}
	if s.journal != nil {
		fees := s.journal.Fees(a)
		out["fee"] = map[string]interface{}{"declared_bps": s.cfg.FeeBps, "waiver": s.Server.FeeWaiver, "accepted_waived": fees.AcceptedWaived, "accepted_charged": fees.AcceptedCharged,
			"accepted_unknown": fees.AcceptedUnknown, "paid_waived": fees.PaidWaived, "paid_charged": fees.PaidCharged, "paid_unknown": fees.PaidUnknown, "fee_work": fees.FeeWork, "sessions": s.Server.SessionFees(a), "windows": s.FeeWindows(a),
			"rule": s.feeRule()}
	}
	if len(s.cfg.Coordinates) > 0 && s.journal != nil {
		// VectorWork: the split per coordinate (the outside review's accounting for this address: accepted and paid work,
		// orphans and expiries, the normalised chain work and the fee per coordinate; the accepted shares in the
		// window by coordinate) and the balance steps (the address's balance before and after its last paid heights,
		// read from the node when it serves the state)
		// the balance steps: the heights over the last 32 blocks at which the address's balance CHANGED, as the chain
		// states them (the paying block, not the share's height: the settlement team's compare of 16:28 UK,
		// 13 October 2026, pays shares at blocks 36 and 44 for shares mined earlier); read from the archive RPC when
		// one is set (a sealer serves no old state), else from the pool's node, and absent when neither serves it
		steps := []map[string]interface{}{}
		bal := s.sourceNode()
		if s.archive != nil {
			bal = s.archive
		}
		var prev *big.Int
		lo := minHeight
		if head > 32 && head-32 > lo {
			lo = head - 32
		}
		for h := lo; h <= head && h > 0; h++ {
			var cur string
			if err := bal.Call(cctx, "eth_getBalance", []interface{}{a, fmt.Sprintf("0x%x", h)}, &cur); err != nil || !strings.HasPrefix(cur, "0x") {
				break
			}
			b, ok := new(big.Int).SetString(cur[2:], 16)
			if !ok {
				break
			}
			if prev != nil && b.Cmp(prev) != 0 {
				steps = append(steps, map[string]interface{}{"height": h, "balance_before_wei": prev.String(), "balance_after_wei": b.String(), "step_wei": new(big.Int).Sub(b, prev).String()})
			}
			prev = b
		}
		out["coordinates"] = map[string]interface{}{"accounting": s.CoordAccounting(a), "accepted_in_window_by_coord": s.journal.AccountByCoord(a, minHeight), "balance_steps": steps,
			"rule": "per coordinate: the address's accepted and paid work, orphans and expiries as the journal holds them, the normalised chain work (paid work times the coordinate's difficulty) and the pool's fee on the paid work; balance_steps are the address's balance before and after its last paid heights as the chain states them; the chain pays per coordinate at that coordinate's reward per block, never additively across coordinates"}
	}
	return out
}

// Run polls the node for work, credits found blocks and reconciles
// until ctx ends.
func (s *Service) Run(ctx context.Context) error {
	if s.cfg.ShareLane {
		s.startForwarders(ctx)
		s.loadWorkerSteps()
	}
	s.mu.Lock()
	if s.startedAt.IsZero() {
		s.startedAt = s.now()
	}
	if s.lastHeight == 0 && s.lastHeightAt.IsZero() {
		// no template yet: the guard's clock runs from the start (pool-b2 on the old public test network (chain 7320), 23:59 UK, 12 October 2026:
		// started while its source rolled, it never took a template from the fan-out because the stall was
		// measured from a first template it never had)
		s.lastHeightAt = s.now()
	}
	s.mu.Unlock()
	t := time.NewTicker(s.cfg.Poll)
	defer t.Stop()
	var lastRewards time.Time
	for {
		if s.cfg.ShareLane {
			s.readLimits(ctx)
		}
		if err := s.pollWork(ctx); err != nil && ctx.Err() == nil {
			log.Printf("getwork: %v", err)
		}
		if err := s.creditFound(ctx); err != nil && ctx.Err() == nil {
			log.Printf("credit: %v", err)
		}
		if err := s.recon.Run(ctx, s.now()); err != nil && ctx.Err() == nil {
			log.Printf("reconcile: %v", err)
		}
		if s.now().Sub(lastRewards) > 30*time.Second {
			s.refreshRewards(ctx)
			lastRewards = s.now()
			if s.cfg.ShareLane {
				s.laneCoverCheck()
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// samePayoutList compares two chain-native payout lists ([{"address",
// "weight"}]) entry by entry, the addresses lower-cased and the weights as
// integers (hex or decimal), order kept (the list's root depends on it).
func samePayoutList(a, b json.RawMessage) bool {
	type entry struct {
		Address string          `json:"address"`
		Weight  json.RawMessage `json:"weight"`
	}
	var la, lb []entry
	if json.Unmarshal(a, &la) != nil || json.Unmarshal(b, &lb) != nil || len(la) != len(lb) || len(la) == 0 {
		return false
	}
	weight := func(r json.RawMessage) *big.Int {
		var s string
		if json.Unmarshal(r, &s) == nil {
			v := new(big.Int)
			if strings.HasPrefix(s, "0x") {
				if _, ok := v.SetString(s[2:], 16); ok {
					return v
				}
				return nil
			}
			if _, ok := v.SetString(s, 10); ok {
				return v
			}
			return nil
		}
		var n int64
		if json.Unmarshal(r, &n) == nil {
			return big.NewInt(n)
		}
		return nil
	}
	for i := range la {
		wa, wb := weight(la[i].Weight), weight(lb[i].Weight)
		if wa == nil || wb == nil || wa.Cmp(wb) != 0 || !strings.EqualFold(la[i].Address, lb[i].Address) {
			return false
		}
	}
	return true
}

func short(r json.RawMessage) string {
	s := string(r)
	if len(s) > 96 {
		return s[:96] + "..."
	}
	return s
}

// ListResetInterval bounds how often the pool re-sets its payout list on the
// node: once per template interval (the sealer recommits every 2 s), so a
// node that refuses the call is asked once a template, not once a poll.
const ListResetInterval = 2 * time.Second

// SetPayoutList sets the pool's chain-native list on the node
// (miner_setPayoutList) and records the time; the start-up call and every
// re-set go through it. An error is the node's refusal or absence.
func (s *Service) listEntries() ([]map[string]interface{}, error) {
	var entries []map[string]interface{}
	if err := json.Unmarshal(s.cfg.PayoutList, &entries); err != nil || len(entries) == 0 || len(entries) > 256 {
		return nil, fmt.Errorf("payout list: 1 to 256 entries of {address, weight} expected")
	}
	return entries, nil
}

func (s *Service) SetPayoutList(ctx context.Context) error {
	if err := s.setListOn(ctx, s.node); err != nil {
		return err
	}
	s.mu.Lock()
	s.listSetAt = s.now()
	s.ListResets++
	s.mu.Unlock()
	return nil
}

// resetPayoutList re-sets the list when it may do so (the interval elapsed),
// naming the reason in the log line; the caller still refuses the template
// in hand, since the next one carries the list.
func (s *Service) resetPayoutList(ctx context.Context, reason string) {
	s.mu.Lock()
	due := s.now().Sub(s.listSetAt) >= ListResetInterval
	s.mu.Unlock()
	if !due {
		return
	}
	if err := s.SetPayoutList(ctx); err != nil {
		log.Printf("payout list: re-set on the node refused (%s): %v", reason, err)
		return
	}
	log.Printf("payout list: re-set on the node (%s); the next template must name it", reason)
}

// pollWork turns new work into a job. A new header means a new job.
func (s *Service) pollWork(ctx context.Context) error {
	if s.members != nil {
		return s.pollMembers(ctx)
	}
	if s.cfg.ShareLane {
		return s.pollShareLane(ctx)
	}
	w, err := s.node.GetWork(ctx)
	if err != nil {
		s.mu.Lock()
		s.nodeLost = true
		s.mu.Unlock()
		return err
	}
	if w.ShareOnly {
		return fmt.Errorf("work package at height %d is share-only (a keyed sealer under the share lane): no block job; run the pool with -share-lane", w.Height)
	}
	if len(s.cfg.PayoutList) > 0 {
		// the chain-native form: the template must name the pool's own list before any hasher works it; a node that
		// restarted or lost the list is given it again (the fault of 11 October 2026: set once at start, the pool
		// refused every template after the sealer's restart and issued no job), on the node answering again after
		// an error and on any template that does not name the list, at most once per template interval
		s.mu.Lock()
		lost := s.nodeLost
		s.nodeLost = false
		s.mu.Unlock()
		if lost {
			s.resetPayoutList(ctx, "the node answers again")
		}
		if !samePayoutList(w.Payouts, s.cfg.PayoutList) {
			s.resetPayoutList(ctx, fmt.Sprintf("the template at height %d names %s", w.Height, short(w.Payouts)))
			return fmt.Errorf("work package at height %d names a payout list that is not the pool's (%s against %s): no job issued", w.Height, short(w.Payouts), short(s.cfg.PayoutList))
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current != nil && s.current.Header == w.Header {
		return nil
	}
	job, err := s.jobFromWork(ctx, w, "")
	if err != nil {
		return err
	}
	s.current = &job
	s.Stats.Jobs++
	s.Stats.LastJobHeight = w.Height
	s.Server.SetJob(job)
	return nil
}

// jobFromWork builds the job for a work package (the parent from the
// chain reader, the algorithm prepared, the share target the easier of
// the configured difficulty and the boundary so a solo-scale chain still
// produces shares, the payout list carried verbatim); member names the
// member whose own node built the template ("" on the single-node form).
// The caller holds s.mu.
func (s *Service) jobFromWork(ctx context.Context, w jobsource.Work, member string) (stratum.Job, error) {
	job := stratum.Job{
		Height: w.Height, Algorithm: w.Algorithm, AlgorithmVersion: w.AlgorithmVersion, Epoch: w.Epoch,
		Header: w.Header, Boundary: w.Boundary, Clean: true, Payouts: w.Payouts, Member: member,
	}
	if w.Height > 0 {
		if parent, err := s.parentHash(ctx, w.Height-1); err == nil {
			job.Parent = parent
		}
	}
	if job.Algorithm == "" {
		job.Algorithm = validate.NullAlgorithm
	}
	if pr, ok := s.validator.(validate.Preparer); ok {
		if err := pr.Prepare(job.Algorithm, w.Params, w.ParamsDigest, w.Height); err != nil {
			return job, fmt.Errorf("prepare %s: %w", job.Algorithm, err)
		}
	}
	netDiff := validate.Difficulty(w.Boundary)
	shareDiff := s.cfg.ShareDifficulty
	if shareDiff > netDiff {
		shareDiff = netDiff
	}
	job.ShareTarget = validate.TargetForDifficulty(shareDiff)
	s.window.Target = s.cfg.WindowN * netDiff
	job.SubsidyWei, job.PriorityFeeMeanWei = s.earnings.SubsidyWei, s.earnings.PriorityFeeMeanWei
	job.Params, job.ParamsDigest = w.Params, w.ParamsDigest
	job.ID = job.Identity(s.chainID)
	return job, nil
}

// creditFound looks for each submitted solution in the canonical chain
// (same height, same nonce and mix) and, once seen, distributes its
// reward into the ledger as immature credits and tracks it for maturity.
func (s *Service) creditFound(ctx context.Context) error {
	if s.journal != nil {
		// the journal's expiry runs on the chain's head once per head, whatever the pool's form
		reader := chainView{s.node}
		if head, err := reader.Head(ctx); err == nil {
			// without the service lock across it (the public pool's 81 s without a job at 04:44:31Z, 14 October
			// 2026, and the HTTPS front's timed-out minutes since the 13th: the settle read every sealer's account
			// under the lock, up to twenty seconds a settle while a sealer timed out, and the share lane, the pages
			// and the health answer all waited on it); expireJournal and the settle take the lock for their state alone
			s.expireJournal(head)
		}
	}
	s.mu.Lock()
	pending := make([]*foundBlock, 0)
	for i := range s.found {
		if !s.found[i].Credited {
			pending = append(pending, &s.found[i])
		}
	}
	s.mu.Unlock()
	for _, f := range pending {
		hash, ok, err := s.canonicalMatch(ctx, f)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		reward, err := s.blockReward(ctx, f.Job.Height)
		if err != nil {
			return err
		}
		if reward == nil {
			continue // no reward source yet; try again next poll
		}
		s.mu.Lock()
		d, err := shares.Distribute(shares.Block{Hash: hash, Height: f.Job.Height, Reward: reward}, f.Window, s.cfg.FeeBps, s.carry)
		if err != nil {
			s.mu.Unlock()
			return err
		}
		if err := s.book.ApplyDistribution(ctx, d, s.now()); err != nil && !errors.Is(err, ledger.ErrDuplicate) {
			s.mu.Unlock()
			return err
		}
		s.carry = new(big.Int).Set(d.Remainder)
		f.Credited, f.Hash = true, hash
		if s.journal != nil {
			// the shares this block paid are carried: pruned from the journal by name
			seqs := make([]uint64, 0, len(f.Window))
			for _, sh := range f.Window {
				if sh.Journal != 0 {
					seqs = append(seqs, sh.Journal)
				}
			}
			if err := s.journal.Carried(seqs, hash, f.Job.Height); err != nil {
				log.Printf("share journal: carriage of %d share(s) by block %s failed: %v", len(seqs), hash, err)
			}
		}
		s.Stats.Credited++
		s.recon.Track(reconcile.Found{Hash: hash, Height: f.Job.Height})
		s.mu.Unlock()
		log.Printf("block %s at height %d credited: reward %s wei, fee %s, %d accounts, remainder %s", hash, f.Job.Height, reward, d.Fee, len(d.Credits), d.Remainder)
	}
	return nil
}

// parentHash reads the canonical hash at height (the job's parent).
func (s *Service) parentHash(ctx context.Context, height uint64) ([32]byte, error) {
	var out [32]byte
	var b *rpcBlock
	if err := s.sourceNode().Call(ctx, "eth_getBlockByNumber", []interface{}{fmt.Sprintf("0x%x", height), false}, &b); err != nil {
		return out, err
	}
	if b == nil {
		return out, errors.New("no block")
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(b.Hash, "0x"))
	if err != nil || len(raw) != 32 {
		return out, errors.New("bad hash")
	}
	copy(out[:], raw)
	return out, nil
}

type rpcBlock struct {
	Hash    string `json:"hash"`
	Nonce   string `json:"nonce"`
	MixHash string `json:"mixHash"`
	Miner   string `json:"miner"`
	BondKey string `json:"bondKey"` // the sealing lane's bond key (the public test network's header; 0.1.82's log)
}

func (s *Service) canonicalMatch(ctx context.Context, f *foundBlock) (string, bool, error) {
	var b *rpcBlock
	if err := s.sourceNode().Call(ctx, "eth_getBlockByNumber", []interface{}{fmt.Sprintf("0x%x", f.Job.Height), false}, &b); err != nil {
		return "", false, err
	}
	if b == nil {
		return "", false, nil
	}
	wantNonce := fmt.Sprintf("0x%016x", f.Nonce)
	if !strings.EqualFold(b.Nonce, wantNonce) || !strings.EqualFold(strings.TrimPrefix(b.MixHash, "0x"), hex.EncodeToString(f.Mix[:])) {
		return "", false, nil
	}
	return b.Hash, true, nil
}

// blockReward is the miner's subsidy plus priority fees at height from
// igneum_blockReward, or the configured fallback when the node lacks it.
func (s *Service) blockReward(ctx context.Context, height uint64) (*big.Int, error) {
	records, err := rewards.Fetch(ctx, s.node, height, height)
	if err == nil && len(records) == 1 {
		r := new(big.Int).Set(records[0].Miner)
		r.Add(r, records[0].PriorityFees)
		return r, nil
	}
	if s.cfg.RewardFallback != nil {
		return new(big.Int).Set(s.cfg.RewardFallback), nil
	}
	return nil, nil
}

func (s *Service) refreshRewards(ctx context.Context) {
	var head uint64
	var q json.RawMessage
	if err := s.sourceNode().Call(ctx, "eth_blockNumber", []interface{}{}, &q); err != nil {
		return
	}
	var hs string
	if json.Unmarshal(q, &hs) != nil {
		return
	}
	if _, err := fmt.Sscanf(strings.TrimPrefix(hs, "0x"), "%x", &head); err != nil {
		return
	}
	sum, err := rewards.Current(ctx, s.node, head)
	if err != nil {
		return
	}
	s.mu.Lock()
	s.earnings = sum
	s.mu.Unlock()
}

// chainView adapts the node to the reconciler.
type chainView struct{ node Node }

func (c chainView) Head(ctx context.Context) (uint64, error) {
	var hs string
	if err := c.node.Call(ctx, "eth_blockNumber", []interface{}{}, &hs); err != nil {
		return 0, err
	}
	var h uint64
	if _, err := fmt.Sscanf(strings.TrimPrefix(hs, "0x"), "%x", &h); err != nil {
		return 0, err
	}
	return h, nil
}

func (c chainView) HashAt(ctx context.Context, height uint64) (string, error) {
	var b *rpcBlock
	if err := c.node.Call(ctx, "eth_getBlockByNumber", []interface{}{fmt.Sprintf("0x%x", height), false}, &b); err != nil {
		return "", err
	}
	if b == nil {
		return "", nil
	}
	return b.Hash, nil
}

// readPayShares reads the chain's pay list at the tip (igneum_vectorWork "latest": payShares per coordinate) once
// per height from the template node; a read that fails keeps the last list (the registry changes by transaction,
// so every height is read). Nothing is read on a node that does not serve it.
func (s *Service) readPayShares(ctx context.Context, node Node) {
	var out struct {
		Number    uint64   `json:"number"`
		PayShares []uint64 `json:"payShares"`
		Names     []string `json:"names"`
	}
	s.mu.Lock()
	last := s.payReadHeight
	s.mu.Unlock()
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := node.Call(cctx, "igneum_vectorWork", []interface{}{"latest"}, &out); err != nil || len(out.PayShares) == 0 {
		return
	}
	if out.Number == last && last != 0 {
		return
	}
	s.mu.Lock()
	changed := len(out.PayShares) != len(s.payShares)
	for i := range out.PayShares {
		if !changed && out.PayShares[i] != s.payShares[i] {
			changed = true
		}
	}
	s.payShares, s.payReadHeight, s.payNames = out.PayShares, out.Number, out.Names
	if changed {
		s.payUnpaidSaid = map[uint8]bool{} // the unpaid ones are named again on a change
		s.payUnnamedSaid = nil             // and the unnamed paid ones
	}
	s.mu.Unlock()
	if changed {
		paid, unpaid := s.paidCoordinates()
		log.Printf("share lane: the chain's pay list at height %d: paid coordinates %v, unpaid %v (payShares %v)", out.Number, paid, unpaid, out.PayShares)
	}
}

// ChainCheckEvery is how often the share lane re-reads the source's chain id against the bound one (a test shortens it).
var ChainCheckEvery = 10 * time.Second

// chainStillOfRecord re-reads the source's eth_chainId every ChainCheckEvery and refuses the poll while it is not the
// chain bound at the start (the security review's residual on 0.1.60, 15 October 2026: the chain of record was checked at the
// start alone, so a source whose chain changed while the pool ran, a sealer re-pointed under a standing generation,
// would have served the other chain's templates under the start's job identities until a restart refused them); no
// job is issued on a mismatched poll, the mismatch is said once a minute with both chain ids and the source, and the
// lane resumes the poll the source answers the bound chain again. A source that does not answer is not a mismatch
// (the failover and the source-error clock read that); a chain id of 0 is never compared.
func (s *Service) chainStillOfRecord(ctx context.Context, node Node) error {
	if s.chainID == 0 || node == nil {
		return nil
	}
	now := s.now()
	s.mu.Lock()
	due := now.Sub(s.chainCheckedAt) >= ChainCheckEvery
	s.mu.Unlock()
	if due {
		var chainHex string
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err := node.Call(cctx, "eth_chainId", []interface{}{}, &chainHex)
		cancel()
		var got uint64
		if err == nil && strings.HasPrefix(chainHex, "0x") {
			fmt.Sscanf(strings.TrimPrefix(chainHex, "0x"), "%x", &got) //nolint:errcheck
		}
		s.mu.Lock()
		s.chainCheckedAt = now
		if err == nil && got != 0 {
			if got != s.chainID {
				s.chainMismatch = got
			} else if s.chainMismatch != 0 {
				s.chainMismatch = 0
				log.Printf("share lane: the source answers chain %d again, the chain of record: jobs resume", s.chainID)
			}
		}
		s.mu.Unlock()
	}
	s.mu.Lock()
	mismatch := s.chainMismatch
	say := mismatch != 0 && now.Sub(s.chainSaidAt) >= time.Minute
	if say {
		s.chainSaidAt = now
	}
	s.mu.Unlock()
	if mismatch == 0 {
		return nil
	}
	err := fmt.Errorf("the source answers chain %d, this pool serves chain %d: no job issued until it answers the chain of record (F-173's per-poll read)", mismatch, s.chainID)
	if say {
		log.Printf("share lane: REFUSED poll: %v", err)
	}
	return err
}

// configuredNodes is every node this pool is configured to read: the source, the fan-out and each coordinate's
// sealers, each once, with the url it is known by.
func (s *Service) configuredNodes() []shareNode {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	out := []shareNode{}
	add := func(n shareNode) {
		if n.node == nil || seen[n.url] {
			return
		}
		seen[n.url] = true
		out = append(out, n)
	}
	add(shareNode{url: s.cfg.RPCURL, node: s.node})
	for _, n := range s.shareNodes {
		add(n)
	}
	for _, ns := range s.coordNodes {
		for _, n := range ns {
			add(n)
		}
	}
	return out
}

// blockAt reads one block by tag or number from a node: its number and hash, false when the node does not serve it.
func blockAt(ctx context.Context, n Node, tag string) (uint64, string, bool) {
	var blk struct {
		Number string `json:"number"`
		Hash   string `json:"hash"`
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := n.Call(cctx, "eth_getBlockByNumber", []interface{}{tag, false}, &blk); err != nil || !strings.HasPrefix(blk.Hash, "0x") || len(blk.Hash) < 10 {
		return 0, "", false
	}
	var h uint64
	fmt.Sscanf(strings.TrimPrefix(blk.Number, "0x"), "%x", &h) //nolint:errcheck
	return h, strings.ToLower(blk.Hash), true
}

// readBestHead reads the chain's best known head ON THE POOL'S OWN BRANCH, once per ChainCheckEvery: the source's tip
// (number and hash) is the branch of record; every other configured node is asked, in parallel, its chain id, its
// head and its block at the source's tip height, and it raises the reference only when its chain id is the bound
// one and its block at that height IS the source's tip (its chain holds the pool's template parent); a node whose
// block there differs is on a sibling branch, longer by count or not, and contributes nothing (the security review's finding
// on 0.1.63, 16 October 2026: the highest count over every node is longest-chain reasoning on chains whose fork rule
// is coverage or the minimum, and F-143's live case, node10 alone fourteen blocks ahead by count for twenty-three
// minutes, would have held every template of the main chain); a node that does not answer contributes nothing.
func (s *Service) readBestHead(ctx context.Context) {
	now := s.now()
	s.mu.Lock()
	due := now.Sub(s.bestHeadAt) >= ChainCheckEvery
	src, srcURL, want := s.node, s.cfg.RPCURL, s.chainID
	s.mu.Unlock()
	if !due || src == nil {
		return
	}
	tipH, tipHash, ok := blockAt(ctx, src, "latest")
	if !ok {
		s.mu.Lock()
		s.bestHeadAt = now
		s.mu.Unlock()
		return // the source serves no tip this read: the reference stands as it was
	}
	nodes := s.configuredNodes()
	type ans struct {
		url string
		h   uint64
	}
	ch := make(chan ans, len(nodes))
	asked := 0
	for _, n := range nodes {
		if n.url == srcURL || n.node == src {
			continue
		}
		asked++
		go func(n shareNode) {
			out := ans{url: n.url}
			defer func() { ch <- out }()
			if want != 0 {
				var chainHex string
				cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
				err := n.node.Call(cctx, "eth_chainId", []interface{}{}, &chainHex)
				cancel()
				var got uint64
				if err == nil && strings.HasPrefix(chainHex, "0x") {
					fmt.Sscanf(strings.TrimPrefix(chainHex, "0x"), "%x", &got) //nolint:errcheck
				}
				if got != want {
					return // another chain, or no answer: nothing
				}
			}
			var headHex string
			cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			err := n.node.Call(cctx, "eth_blockNumber", []interface{}{}, &headHex)
			cancel()
			var h uint64
			if err == nil && strings.HasPrefix(headHex, "0x") {
				fmt.Sscanf(strings.TrimPrefix(headHex, "0x"), "%x", &h) //nolint:errcheck
			}
			if h <= tipH {
				return // not ahead of the source: nothing to raise
			}
			// ahead by count: on the pool's branch only if it holds the source's tip
			if _, hash, ok := blockAt(ctx, n.node, fmt.Sprintf("0x%x", tipH)); ok && hash == tipHash {
				out.h = h
			}
		}(n)
	}
	best := ans{url: srcURL, h: tipH}
	for i := 0; i < asked; i++ {
		a := <-ch
		if a.h > best.h {
			best = a
		}
	}
	s.mu.Lock()
	s.bestHeadAt = now
	s.bestHeadVal, s.bestHeadFrom = best.h, best.url
	s.mu.Unlock()
}

// behindBestHead reports whether a template's share height is more than three blocks under the chain's best known
// head (the project's rule of 16 October 2026: never issued), with the head and the node it was read from; false when no
// head has been read.
func (s *Service) behindBestHead(shareHeight uint64) (bool, uint64, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bestHeadVal == 0 {
		return false, 0, ""
	}
	return s.bestHeadVal > shareHeight+3, s.bestHeadVal, s.bestHeadFrom
}

// findSealer asks every configured node igneum_getWork [c] and takes the first that answers a package carrying c as
// the coordinate's sealer (its template source and its share target), for a paid coordinate the configuration names
// no sealer for (the research chain's succession at 6523 and the public test network's at 520, 15 October 2026: the
// successors' sealers were named by hand in a configuration generation); probed at most once per ChainCheckEvery
// per coordinate; nil when none answers. A found sealer is kept as a configured one until the generation ends.
func (s *Service) findSealer(ctx context.Context, c uint8) Node {
	now := s.now()
	s.mu.Lock()
	if s.findAt == nil {
		s.findAt = map[uint8]time.Time{}
	}
	if now.Sub(s.findAt[c]) < ChainCheckEvery {
		s.mu.Unlock()
		return nil
	}
	s.findAt[c] = now
	s.mu.Unlock()
	for _, n := range s.configuredNodes() {
		cw, ok := n.node.(interface {
			GetWorkCoord(context.Context, uint8) (jobsource.Work, error)
		})
		if !ok {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		w, err := cw.GetWorkCoord(cctx, c)
		cancel()
		if err != nil || !w.HasCoord || w.Coord != c {
			continue
		}
		s.mu.Lock()
		if s.coordNodes == nil {
			s.coordNodes = map[uint8][]shareNode{}
		}
		s.coordNodes[c] = []shareNode{{url: n.url, node: n.node}}
		s.mu.Unlock()
		log.Printf("share lane: coordinate %d (%s) is sealed by %s (found by asking the configured nodes): its templates and shares go there from this poll", c, s.coordName(c), n.url)
		return n.node
	}
	return nil
}

// coordPaid reports whether a coordinate pays at the tip and whether the pay list has been read at all.
func (s *Service) coordPaid(c uint8) (paid, known bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.payShares) == 0 {
		return true, false
	}
	if int(c) >= len(s.payShares) {
		return false, true
	}
	return s.payShares[c] > 0, true
}

// coordName is the chain's name for a coordinate, or its number.
func (s *Service) coordName(c uint8) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if int(c) < len(s.payNames) {
		return s.payNames[c]
	}
	return fmt.Sprint(c)
}

// paidCoordinates splits the pool's coordinates by the pay list at the tip.
func (s *Service) paidCoordinates() (paid, unpaid []uint8) {
	for _, c := range s.cfg.Coordinates {
		if p, known := s.coordPaid(c); !known || p {
			paid = append(paid, c)
		} else {
			unpaid = append(unpaid, c)
		}
	}
	return
}

// laneSet is the coordinates this poll serves: the configured ones (-coordinates, each with its sealer), then every
// coordinate the chain's registration PAYS at the tip that the configuration does not name (the project's rule of 20:2x UK,
// 15 October 2026, the public test network's succession at 520: a lane set follows the registration's activation
// height, read from the chain, never a timer or a later restart). A paid coordinate with no sealer named is served
// from the source when the source seals it, else its failure is named once a minute as any lane's; the operator's
// line is logged once per pay-list change: the sealer to name for it.
func (s *Service) laneSet() []uint8 {
	s.mu.Lock()
	defer s.mu.Unlock()
	set := append([]uint8(nil), s.cfg.Coordinates...)
	if len(s.cfg.Coordinates) == 0 || len(s.payShares) == 0 {
		return set
	}
	named := map[uint8]bool{}
	for _, c := range s.cfg.Coordinates {
		named[c] = true
	}
	for i, p := range s.payShares {
		if i > 255 || p == 0 || named[uint8(i)] {
			continue
		}
		c := uint8(i)
		if s.payUnnamedSaid == nil {
			s.payUnnamedSaid = map[uint8]bool{}
		}
		if !s.payUnnamedSaid[c] {
			s.payUnnamedSaid[c] = true
			log.Printf("share lane: coordinate %d (%s) pays at the tip (height %d) but this pool names no sealer for it: served from the source while the source seals it; name its sealer with -coordinate-rpcs %d=<url> at the next generation", c, coordNameOf(s.payNames, c), s.payReadHeight, c)
		}
		set = append(set, c)
	}
	return set
}

// paidCoordsLocked is the chain's pay list as the pool last read it, the coordinates paying at the tip by index (nil
// before the first read, or on a one-form chain), for every job's paid_coords (0.1.76). The caller holds the lock.
func (s *Service) paidCoordsLocked() []uint8 {
	var out []uint8
	for i, p := range s.payShares {
		if i > 255 {
			break
		}
		if p > 0 {
			out = append(out, uint8(i))
		}
	}
	return out
}

// coordNameOf is a coordinate's registered name from the pay list's names, its number when none (the caller holds the lock).
func coordNameOf(names []string, c uint8) string {
	if int(c) < len(names) {
		return names[c]
	}
	return fmt.Sprint(c)
}

// PayList is the pay list as the pool read it, for /shares: the coordinates offered and the unpaid ones with the
// note to the miner.
func (s *Service) PayList() map[string]interface{} {
	paid8, unpaid8 := s.paidCoordinates()
	// as numbers on the page (a []uint8 marshals as base64)
	paid, unpaid := make([]int, 0, len(paid8)), make([]int, 0, len(unpaid8))
	for _, c := range paid8 {
		paid = append(paid, int(c))
	}
	for _, c := range unpaid8 {
		unpaid = append(unpaid, int(c))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]interface{}{"read_height": s.payReadHeight, "pay_shares": s.payShares, "names": s.payNames, "offered": paid, "unpaid": unpaid,
		"lanes_without_worker": s.lanesWithoutWorkerLocked(paid8),
		"lanes_rule":           "on a minimum-rule chain the pool key's weight is the minimum over the paid lanes (the floor decaying over 256 blocks), so the pool earns NOTHING while any paid lane has no hashing worker; a lane is listed here once no share of the pool's has been accepted on it for 64 blocks, with the seconds since the last",
		"note":                 "the pool offers jobs only on coordinates the chain pays at the tip (payShares above 0 on igneum_vectorWork, re-read every height); a coordinate listed under unpaid is served by the chain but pays nothing now, so no job is offered on it and shares on it would be dropped at the sealer's bound"}
}

// clockNow is s.now, or the wall clock on a Service built without one (the older tests).
func (s *Service) clockNow() time.Time {
	if s.now == nil {
		return time.Now()
	}
	return s.now()
}

// noteLaneAccepted records a share the SEALER accepted on a coordinate for the lane cover (0.1.96; the caller holds
// s.mu): the lane's last accepted time and the worker's.
func (s *Service) noteLaneAccepted(coord uint8, has bool, member, worker string) {
	if !has {
		return
	}
	if s.laneLastAccepted == nil {
		s.laneLastAccepted = map[uint8]time.Time{}
		s.laneWorkerAt = map[uint8]map[string]time.Time{}
	}
	s.laneLastAccepted[coord] = s.clockNow()
	if s.laneWorkerAt[coord] == nil {
		s.laneWorkerAt[coord] = map[string]time.Time{}
	}
	s.laneWorkerAt[coord][strings.ToLower(member)+"|"+worker] = s.clockNow()
}

// laneWithoutWorker is a paid lane the pool has had no accepted share on for 64 blocks or more (0.1.96).
type laneWithoutWorker struct {
	Coord        int     `json:"coord"`
	SinceSeconds float64 `json:"since_seconds"`
	Never        bool    `json:"never,omitempty"`
}

// lanesWithoutWorkerLocked lists the offered (paid) lanes without an accepted share for 64 blocks (the caller holds
// s.mu); a lane with no accepted share since the start is "never".
func (s *Service) lanesWithoutWorkerLocked(paid []uint8) []laneWithoutWorker {
	bs := s.cfg.BlockSeconds
	if bs <= 0 {
		bs = 12
	}
	bound := time.Duration(64*bs) * time.Second
	now := s.clockNow()
	var out []laneWithoutWorker
	for _, c := range paid {
		last, ok := s.laneLastAccepted[c]
		if !ok {
			out = append(out, laneWithoutWorker{Coord: int(c), SinceSeconds: now.Sub(s.startedAt).Seconds(), Never: true})
			continue
		}
		if now.Sub(last) >= bound {
			out = append(out, laneWithoutWorker{Coord: int(c), SinceSeconds: now.Sub(last).Seconds()})
		}
	}
	return out
}

// laneWorkersLocked is the count of workers with an accepted share on the coordinate in the last 64 blocks (0.1.96;
// the caller holds s.mu); nil on a one-form chain. Workers silent past the bound are forgotten as they are counted.
func (s *Service) laneWorkersLocked(coord uint8, has bool) *int {
	if !has {
		return nil
	}
	bs := s.cfg.BlockSeconds
	if bs <= 0 {
		bs = 12
	}
	bound := time.Duration(64*bs) * time.Second
	now := s.clockNow()
	n := 0
	for k, t := range s.laneWorkerAt[coord] {
		if now.Sub(t) < bound {
			n++
		} else {
			delete(s.laneWorkerAt[coord], k)
		}
	}
	return &n
}

// laneCoverCheck says once, per lane, when a paid lane has had no accepted share for 64 blocks (the minimum rule's
// floor decays over 256) and once when it has a worker again (0.1.96).
func (s *Service) laneCoverCheck() {
	paid, _ := s.paidCoordinates()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.laneSilentSaid == nil {
		s.laneSilentSaid = map[uint8]bool{}
	}
	silent := map[uint8]bool{}
	for _, l := range s.lanesWithoutWorkerLocked(paid) {
		silent[uint8(l.Coord)] = true
		if !s.laneSilentSaid[uint8(l.Coord)] {
			s.laneSilentSaid[uint8(l.Coord)] = true
			log.Printf("LANE WITHOUT A WORKER: coordinate %d is paid by the chain and no share of the pool's has been accepted on it for %.0f s (never: %v): on a minimum-rule chain the pool key's weight is the minimum over the paid lanes, its floor decaying over 256 blocks, so the pool earns nothing until a worker hashes it", l.Coord, l.SinceSeconds, l.Never)
		}
	}
	for c, said := range s.laneSilentSaid {
		if said && !silent[c] {
			s.laneSilentSaid[c] = false
			log.Printf("lane %d has a worker again (a share accepted); the minimum rule's floor rebuilds over 256 blocks", c)
		}
	}
}

// memberOf is the member's own address for the journal when the share was forwarded under another account.
func memberOf(sh stratum.Share) string {
	if sh.Member != "" && !strings.EqualFold(sh.Member, sh.Account) {
		return strings.ToLower(sh.Member)
	}
	return ""
}

// PoolPay is the pooled form's pay table for /shares and /payouts (the project's order of 14:3x UK, 15 October 2026):
// the pool key the shares are forwarded under, the key's chain payment over the window (its balance now against the
// window's first height), each member's carried work in the window with the fee's part, and the member's due as
// its share of the payment after the fee; nothing is paid from here, the payout run reads this.
func (s *Service) PoolPay(ctx context.Context) map[string]interface{} {
	key := strings.ToLower(s.cfg.ForwardAs)
	if key == "" || s.journal == nil {
		return nil
	}
	members := s.journal.MemberLedgers(key)
	var total, fees uint64
	for _, m := range members {
		total += m.CarriedWork
		fees += m.FeeWork
	}
	s.mu.Lock()
	head := s.Stats.LastJobHeight
	s.mu.Unlock()
	var minHeight uint64
	if head > AddressWindow {
		minHeight = head - AddressWindow
	}
	out := map[string]interface{}{"pool_key": key, "members": len(members), "carried_work": total, "fee_work": fees, "window_blocks": AddressWindow, "from_height": minHeight, "to_height": head,
		"rule": "every member's share is forwarded under the pool key, so the chain pays the key its breadth payment; each member's due is the key's payment over the window times (its carried work less the fee's part on its charged shares) over all members' carried work; the fee's part is the pool's; the payout run pays the dues from the key"}
	cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	var nowHex, startHex string
	var paid *big.Int
	if err := s.sourceNode().Call(cctx, "eth_getBalance", []interface{}{key, "latest"}, &nowHex); err == nil && strings.HasPrefix(nowHex, "0x") {
		if n, ok := new(big.Int).SetString(nowHex[2:], 16); ok {
			out["balance_wei"] = n.String()
			if err := s.sourceNode().Call(cctx, "eth_getBalance", []interface{}{key, fmt.Sprintf("0x%x", minHeight)}, &startHex); err == nil && strings.HasPrefix(startHex, "0x") {
				if st, ok := new(big.Int).SetString(startHex[2:], 16); ok {
					paid = new(big.Int).Sub(n, st)
					out["paid_wei_over_window"] = paid.String()
				}
			}
		}
	}
	rows := make([]map[string]interface{}, 0, len(members))
	for _, m := range members {
		row := map[string]interface{}{"member": m.Member, "accepted": m.Accepted, "accepted_work": m.AcceptedWork, "carried": m.Carried, "paid_work": m.CarriedWork, "fee_work": m.FeeWork, "carried_waived": m.CarriedWaived, "carried_charged": m.CarriedCharged}
		if paid != nil && total > 0 && m.CarriedWork >= m.FeeWork {
			due := new(big.Int).Mul(paid, new(big.Int).SetUint64(m.CarriedWork-m.FeeWork))
			due.Div(due, new(big.Int).SetUint64(total))
			row["due_wei"] = due.String()
		}
		rows = append(rows, row)
	}
	out["rows"] = rows
	return out
}

// PayoutInit starts the pooled form's payout run when a pool key file is configured (the key's address becomes the
// forwarding address when none is named).
func (s *Service) PayoutInit() error {
	if err := s.payoutInit(); err != nil {
		return err
	}
	s.Server.ForwardAs = s.cfg.ForwardAs
	return nil
}

// ForwardAs is the pool key's address every member's share is forwarded under, empty on the direct form.
func (s *Service) ForwardAs() string { return s.cfg.ForwardAs }

// ChainID is the chain the pool serves, as read from its node at start.
func (s *Service) ChainID() uint64 { return s.chainID }

// readNodeTokens reads each node's credential file (0.1.75): the trimmed bytes per node URL, and per URL whose file
// could not be read the error naming the path alone, never the bytes, so that node's poll is refused rather than
// dialled without its credential. The paths are logged; the bytes never are.
func readNodeTokens(files map[string]string) (map[string]string, map[string]error) {
	tokens, refusals := map[string]string{}, map[string]error{}
	for url, path := range files {
		b, err := os.ReadFile(path)
		if err != nil {
			refusals[url] = fmt.Errorf("node credential: %s: its token file %s could not be read: the node is not polled", url, path)
			log.Printf("%v", refusals[url])
			continue
		}
		tok := strings.TrimSpace(string(b))
		if tok == "" {
			refusals[url] = fmt.Errorf("node credential: %s: its token file %s is empty: the node is not polled", url, path)
			log.Printf("%v", refusals[url])
			continue
		}
		tokens[url] = tok
		log.Printf("node credential: %s carries the Authorization header from %s", url, path)
	}
	return tokens, refusals
}

// nodeFor is the node client for a URL: the member-node client with its credential when one is named, a refusing
// node when the credential file could not be read (every call answers the error naming the path).
func (s *Service) nodeFor(url string) Node {
	if err := s.nodeRefusals[url]; err != nil {
		return refusedNode{url: url, err: err}
	}
	n := newMemberNode(url)
	if tok := s.nodeTokens[url]; tok != "" {
		if c, ok := n.(*jobsource.Client); ok {
			c.Token = tok
		}
	}
	return n
}

// refusedNode stands for a node whose credential file could not be read: it never dials; every call answers the error.
type refusedNode struct {
	url string
	err error
}

func (r refusedNode) GetWork(ctx context.Context) (jobsource.Work, error) {
	return jobsource.Work{}, r.err
}
func (r refusedNode) SubmitWork(ctx context.Context, nonce uint64, header, mix [32]byte) error {
	return r.err
}
func (r refusedNode) Call(ctx context.Context, method string, params interface{}, out interface{}) error {
	return r.err
}
