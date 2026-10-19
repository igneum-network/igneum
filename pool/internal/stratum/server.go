package stratum

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"igneum.network/pool/internal/limits"
	"igneum.network/pool/internal/session"
	"igneum.network/pool/internal/validate"
)

// Job is what the pool mines: the node's work package with the pool's
// share target. Algorithm names the validator and AlgorithmVersion its
// version (the IgneumHash plan, chapters 15 and 32: a canonical job id
// commits to parent, template, height, epoch, target and algorithm
// version, so a fourth candidate is one row in the job, not a fork);
// Header is the sealing hash as the node gives it.
type Job struct {
	// Payee, when set, is the address the share header is built for instead of Member (the pool's own fee
	// address on one member height in a hundred while the member's session is charged); the job still routes to
	// Member's sessions and the share's account is Payee.
	Payee string
	// ForwardAs, when set, is the pool key's address every member's share is forwarded under (the pooled form,
	// the project's order of 14:3x UK, 15 October 2026): the share header is built for it, the share's account is it and
	// Member keeps the session's own address for the pool's pay.
	ForwardAs        string
	ID               string
	Height           uint64
	Algorithm        string
	AlgorithmVersion uint32
	Epoch            uint64
	Parent           [32]byte
	Header           [32]byte
	Boundary         [32]byte
	ShareTarget      [32]byte
	Clean            bool
	// SubsidyWei and PriorityFeeMeanWei are decimal wei strings for the
	// miner's earnings display; empty when unknown.
	SubsidyWei         string
	PriorityFeeMeanWei string
	// Params and ParamsDigest: the node's parameter document and digest for
	// the job (see NotifyParams).
	Params       json.RawMessage
	ParamsDigest string
	// Payouts is the chain-native payout list the template names (the
	// node's igneum_getWork "payouts" field, verbatim), so a member can
	// refuse to work a template whose list does not name it before it
	// hashes (GUARD-C-CLOSURE 3.2 "change the payout list": the member
	// refuses before signing). Empty on a chain without lists.
	Payouts json.RawMessage
	// Member is the lower-cased address of the member whose OWN node built
	// this template (the required form of GUARD-C-CLOSURE 3.1: a unique
	// signed template per member; the pool holds no template of its own).
	// Empty on the single-node form, where one template serves every
	// session. A member job is notified only to that member's sessions.
	Member string
	// Worker names the sessions of the member this job is for (the share lane's per-worker jobs, the project's ruling of
	// 09:37 UK, 12 October 2026, on G-33: a worker's step is one across its sessions, so an install's fee session
	// mines at its member session's step); empty = every session of the member.
	Worker string
	// ShareStep is the session's declared step on the share lane (PA-53's
	// step form): the job's share target is the node's over 2^step and an
	// accepted share weighs 2^step; 0 is the count form.
	ShareStep uint8
	// VectorWork (the project's order of 00:5x UK, 13 October 2026): the job's coordinate (0..3) when the chain serves
	// templates per coordinate (HasCoord), its difficulty as the node states it and its reward per block in wei
	// (decimal strings, for the miner's display); a share names the job, so the coordinate travels with the id.
	Coord      uint8
	HasCoord   bool
	Difficulty string
	RewardWei  string
	LastHeight uint64 // the coordinate's last block on the chain (0 = not served)
	CoordName  string // the coordinate's name as the node states it
	// PaidCoords is the chain's pay list at the job's height, the coordinates it pays by index (0.1.76, the
	// 7322 succession of 16 October 2026: a retired coordinate gets no further job, and a router that read the
	// silence as an emptied lane restored it; now every job names what the chain pays, so a coordinate not in the
	// list is retired or unpaid and is dropped at once); nil on a one-form chain.
	PaidCoords []uint8

	// LaneWorth is the coordinate's marginal pay per unit of share weight (0.1.86, F-496): the coordinate's income
	// over the pool's carried share weight on it over the last closed pay window, wei per unit of share weight, a
	// decimal string in scientific form; empty while undefined.
	LaneWorth string
	// LaneWorkers is the count of workers hashing the coordinate (0.1.96); nil on a one-form chain
	LaneWorkers *int
	// Kind is "block" on a BLOCK job (vectorwork6, 13 October 2026: coordinate 1's sealer runs no CPU sealer, so every
	// bandwidth block is the pool's: a block session, its worker name ending ".block", mines the template's header
	// at the block target and the pool submits its solution as igneum_submitWork on the coordinate's sealer); empty
	// on a share job. A block session receives block jobs alone, every other session share jobs alone.
	Kind string
	// ProgramAnchor is the period's programme anchor the share is computed under (HasAnchor), with AnchorHeight
	// the height it was taken at: the chain's canonical block hash at period x period_blocks - D (the chain form),
	// as the node serves it or the pool resolves it; a document without a programme carries none.
	ProgramAnchor [32]byte
	HasAnchor     bool
	AnchorHeight  uint64
}

// Identity is the canonical job id: the first 8 bytes of a SHA-256 over
// the domain tag, chain id, parent, header, height, epoch, boundary,
// share target, algorithm name and version, and on a coordinate job the
// coordinate and the document digest. Two jobs that differ in any of
// these have different ids; a share names the whole commitment.
func (j Job) Identity(chainID uint64) string {
	h := sha256.New()
	var buf [8]byte
	put := func(v uint64) {
		binary.BigEndian.PutUint64(buf[:], v)
		h.Write(buf[:])
	}
	h.Write([]byte("igneum-pool-job-v1"))
	put(chainID)
	h.Write(j.Parent[:])
	h.Write(j.Header[:])
	put(j.Height)
	put(j.Epoch)
	h.Write(j.Boundary[:])
	h.Write(j.ShareTarget[:])
	put(uint64(len(j.Algorithm)))
	h.Write([]byte(j.Algorithm))
	put(uint64(j.AlgorithmVersion))
	if j.HasCoord || j.ParamsDigest != "" {
		// VectorWork (the box soak of 02:05Z, 13 October 2026): three coordinates' jobs for one worker shared one
		// header (the same share parent on one chain), height, targets and algorithm name, so they shared one id and
		// every share was verified on the last coordinate's document; the coordinate and the document are part of
		// the commitment a share names
		h.Write([]byte("coord"))
		put(uint64(j.Coord))
		put(uint64(len(j.ParamsDigest)))
		h.Write([]byte(j.ParamsDigest))
	}
	if j.HasAnchor {
		h.Write([]byte("anchor"))
		h.Write(j.ProgramAnchor[:])
	}
	if j.Kind != "" {
		h.Write([]byte("kind:" + j.Kind))
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

// Share is an accepted share handed to the sink after the reference has
// verified it. Block is true when the hash also met the boundary.
type Share struct {
	SessionID [16]byte
	// Key is the miner's Ed25519 public key on a signed share (zero on the
	// plain form); the journal row carries it.
	Key     [32]byte
	Signed  bool
	Account string
	Worker  string
	JobID   string
	Height  uint64
	Nonce   uint64
	Mix     [32]byte
	Hash    [32]byte
	Weight  uint64
	Block   bool
	At      time.Time
	// FeeBps and FeeWaived are the session's fee terms at acceptance (the fee-waiver handshake, the project's order of
	// 20:4x UK, 13 October 2026: a waived session's shares are paid without the pool's fee, the ledger's fee row
	// per address shows which applied), carried into the journal line.
	FeeBps    uint32
	FeeWaived bool
	// Ticket is a proof-carried share's proof (the graph coordinate's 168-byte cycle ticket), nil otherwise.
	Ticket []byte
	// Member is the session's own address (Account is the pool key under the pooled form, the member's otherwise).
	Member string
	// FeeFor is the member address a fee session's share counts for (the for block's keying), empty on a member's own share.
	FeeFor string
}

// Sink receives verified shares and block solutions. Block is called
// before Share for a block-meeting submission, so the node sees the
// solution before the accounting runs.
// ShareVerifier verifies the non-custodial form's signatures (an
// Ed25519 key over BLAKE3 messages; the implementation is build-tagged
// with the node's BLAKE3, see signed_igneumhash.go).
type ShareVerifier interface {
	// Authorize checks sig over BLAKE3("ember-authorize" || session id || address)
	// under key and returns the key on success.
	Authorize(sessionID [16]byte, address, keyHex, sigHex string) ([32]byte, bool)
	// Share checks sig over BLAKE3("IgneumPool/nc-share/v0" || u64be(chainID) ||
	// poolID || header || u64be(nonce) || mix || hash) under key.
	Share(chainID uint64, poolID [32]byte, header [32]byte, nonce uint64, mix, hash [32]byte, keyHex, sigHex string) ([32]byte, bool)
}

type Sink interface {
	Block(ctx context.Context, job Job, nonce uint64, mix, hash [32]byte) error
	Share(s Share)
}

// TicketSink is a Sink that forwards proof-carried shares: Ticket hands the verified ticket of a graph share
// before Block runs for it, so the forward carries it (WHOLEWORK.md section 10).
type TicketSink interface {
	Ticket(job Job, nonce uint64, ticket []byte)
}

// Server terminates IS/1 sessions. One job at a time is current; the
// last limits.MaxJobsRetained jobs are retained for stale detection.
// paidCoordsParam is the job's paid coordinates for the wire: a list of numbers (a []uint8 would travel as base64),
// nil when the job carries none.
func paidCoordsParam(j Job) []uint16 {
	if len(j.PaidCoords) == 0 {
		return nil
	}
	out := make([]uint16, len(j.PaidCoords))
	for i, c := range j.PaidCoords {
		out[i] = uint16(c)
	}
	return out
}

// Refusal is a refused submit as the server replied it: the session's address and worker, the job id and nonce
// named, the reason, and the retained job when the id is known (nil for a stale or unknown id).
type Refusal struct {
	Address string
	Worker  string
	JobID   string
	Nonce   string // the submit's nonce text as sent
	Reason  string
	Job     *Job
}

type Server struct {
	validator validate.Validator
	sink      Sink
	now       func() time.Time
	// The non-custodial form (docs/ecosystem/POOL-NONCUSTODIAL.md): when
	// SignedShares is set, authorize carries the miner's key and a signature
	// and every share carries a signature over the share's message; PoolID
	// and ChainID enter that message. Verification is the Verifier's (a
	// build-tagged implementation; nil refuses the form).
	SignedShares bool
	// TrustClaimed skips the reference recompute and takes the share's claimed mix and hash as submitted, checked
	// against the share target alone (the scripted chain state of the product team's VectorWork router soak, 13 October
	// 2026: no node and no dataset behind the pool); never on a pool that forwards to a chain.
	TrustClaimed bool
	PoolID       [32]byte
	ChainID      uint64
	// ChainIDPrev, when set, is the chain id a share signature is ALSO accepted over, as a named transition (the
	// old public test network (chain 7320) switch of 22:58Z, 12 October 2026: the installed app signs over the chain it installed on, 7319,
	// and every share read "invalid" on 7320; the project's word of 00:4x UK, 13 October 2026): the share itself still
	// verifies against the current chain's job, so nothing replays; named once per session on the log.
	ChainIDPrev uint64
	Verifier    ShareVerifier
	// OnAuthorized, when set, is called after a signed authorize binds a
	// member's key to its session (the finder-in-Extra form: the pool sets
	// the key as the sealing node's template Extra so a block sealed by the
	// member's share names its finder on the chain; one template per finder
	// in turn on a single sealer).
	OnAuthorized func(key [32]byte, address string)
	// Receipts, when set, signs an acknowledgement per accepted signed share
	// (the share's hash, the member's key, the weight, the member's running
	// total and a running digest over every receipt) under the pool's receipt
	// key and sends it as a mining.receipt notification after the reply; the
	// member keeps it as its proof of credit (POOL-NONCUSTODIAL.md section 4:
	// a changed weight or a dropped share is then detectable by the member).
	Receipts ReceiptSigner
	rmu      sync.Mutex
	totals   map[[32]byte]uint64
	// digests is the running digest PER MEMBER KEY (security's F-34: a
	// pool-wide chain made a member's receipt verifiable only when the
	// pool's previous receipt was its own); the previous digest is carried
	// in the receipt, zero at the member's first receipt of the session.
	digests       map[[32]byte][32]byte
	seq           uint64
	receiptKeyHex string
	// PoolList is the pool's chain-native payout list, carried on the
	// subscribe result for the member-node form (the member's own client
	// sets it on its own node after checking it names the member).
	PoolList json.RawMessage
	// FeeBps is the pool's declared fee in basis points of the miner's side
	// and FeeWaiver whether it is waived for a session whose miner reports
	// its dev fee on under a verified signature (the company pool's rule:
	// 100 bps, the cap itself, waived; never stacking). DevFeeVerifier
	// checks the dev-fee signature (Ed25519 over the dev-fee message; a
	// build-tagged implementation, nil verifies nothing and no waiver is
	// granted). A session's fee is answered on the subscribe result and
	// carried in every receipt.
	FeeBps         uint32
	FeeWaiver      bool
	DevFeeVerifier DevFeeVerifier
	// Gate, when set, holds every mining.submit until it is closed (the handover form, the project's order of 21:2x UK,
	// 13 October 2026: the new generation listens beside the old and accepts shares once the old has let the
	// journal go and the journal is caught up); subscribe, authorize and the jobs run meanwhile. A submit held
	// past GateWait is answered as the pool's own fault, not the miner's.
	Gate     chan struct{}
	GateWait time.Duration
	// the drain state (Drain)
	draining                     bool
	reconnectHost, reconnectPort string
	// OnAdmissionRefused, when set, hears every share verified at the step and refused before acceptance by the
	// admission (the address at the chain's cap or the sealer's hold): the worker found it, so it is evidence
	// of its rate for the step rule (the PCs' evening of 13 October 2026: four workers on one address at step 0,
	// seven of ten shares refused at the sealer's hold, the rule reading the three as the rate and holding step 0)
	OnAdmissionRefused func(job Job)
	// OnRateRefused, when set, hears every submit the session's bucket refused (ReasonRate) with the job it named,
	// for the step rule's measured rate alone (the project's order of 12:0x UK, 15 October 2026).
	OnRateRefused func(job Job)
	// OnRefused, when set, hears EVERY refused submit (the reason as replied, the job when it is retained) so the
	// service can journal it by worker (F-271, 16 October 2026)
	OnRefused func(r Refusal)
	// BlockSessions, when false (the default), refuses a block-named worker (IsBlockWorker) at authorize with the
	// reason on the wire and a line on the log (0.1.79, 16 October 2026, the review lead's rule from the security review's 75bh:
	// no keyed seal on a mixed set; the pool's block session stays off from a node version's first roll to its
	// last DONE); -block-coords names where block sessions are served once they are on.
	BlockSessions bool
	// ForwardAs is the pool key on the pooled form, named on every subscribe result (forward_as) and on every
	// job's notify (header_for) so a session knows the address its share header is built for.
	ForwardAs string
	// FeeDecider answers a member session's fee from the verifiable accounting (nil: the declared fee on every session)
	FeeDecider FeeDecider
	// Admission, when set, is asked before a verified share is accepted (the share lane's admission bound: the
	// address at the chain's per-height cap, the node's pool at capacity, or the list bound reached); a refusal
	// goes on the wire as ReasonAdmission + ": " + reason, on the log, and in AdmissionStats; never accepted, never
	// receipted, never counted toward the bad-share threshold.
	Admission func(address string, height uint64) (ok bool, reason string)
	// AdmissionFor, when set, is Admission with the job (VectorWork: the bound is per sealer, and a job's sealers are
	// its coordinate's); it takes precedence over Admission.
	AdmissionFor     func(address string, job Job) (ok bool, reason string)
	admissionRefused map[string]uint64 // by reason
	admissionByAddr  map[string]uint64
	fmu              sync.Mutex
	feeWaived        uint64
	feeCharged       uint64
	// The bad-share threshold at the edge (DESIGN 2.21, G-33; the disclosure's
	// bad_share_limit, bad_share_window_s, bad_share_cooling_s): a session
	// whose refused shares reach BadShareLimit within BadShareWindow is
	// closed with ReasonBadShares on its last reply and its address refused
	// at authorize for BadShareCooling; zero limit means no threshold.
	BadShareLimit   uint32
	BadShareWindow  time.Duration
	BadShareCooling time.Duration
	bmu             sync.Mutex
	cooling         map[string]time.Time // the cooled key (address/worker, lower-cased) -> cooled until
	sessionsClosed  uint64
	authRefused     uint64

	mu    sync.Mutex
	jobs  map[string]Job
	order []string
	// memberOrder is the retained job ids PER MEMBER (lower-cased address; "" for the pool's own jobs), oldest
	// first, each list bounded by limits.MaxJobsRetained; a share on a job still in its member's list is not
	// stale, whatever the other members issued since (the retention was four jobs across every member before
	// the project's order of 07:3x UK, 12 October 2026).
	memberOrder map[string][]string
	// the share-age reads per address (the measure the project asked for): accepted shares by the job's age in templates
	// behind the member's latest at acceptance, and the stale refusals (a job no longer retained)
	ageAccepted  map[string][]uint64
	staleRefused map[string]uint64
	current      *Job
	// members holds the current job per member address (lower-cased) on
	// the member-node form: each member's own node's template.
	members map[string]*Job
	// paid is the chain's pay list as the newest job carried it (PaidCoords, 0.1.76); nil until a job carries one
	// (a one-form chain, or before the pool's first read). 0.1.77 (F-290's replay half, 16 October 2026): a job on
	// a coordinate the list does not pay is never replayed to a session at authorize, and leaves the retained set
	// the moment its coordinate leaves the list, so a fresh engine never sees a retired coordinate's job.
	paid     []uint8
	sessions map[*conn]struct{}
	// the share lane's document for a session's first subscribe (SetLaneParams)
	laneParams json.RawMessage
	// docReady closes the first time the server holds a parameter document (SetLaneParams or a job with one): a
	// subscribe that arrives before it waits up to SubscribeWait for it (0.1.62, 15 October 2026: a fresh generation
	// listened before its first template and answered a subscribe with no document; Ember stops itself on one)
	docReady         chan struct{}
	docOnce          sync.Once
	docSaid          time.Time
	subscribesNoDoc  uint64 // the subscribes answered with no parameter document (SubscribesWithoutDocument)
	laneParamsDigest string
}

// NewServer creates a server over a validator and a sink.
func NewServer(v validate.Validator, sink Sink) *Server {
	return &Server{
		validator: v,
		sink:      sink,
		now:       time.Now,
		jobs:      map[string]Job{},
		sessions:  map[*conn]struct{}{},
		members:   map[string]*Job{},
		docReady:  make(chan struct{}),
	}
}

// SubscribeWait is how long a subscribe arriving before the server holds a parameter document waits for one
// (a test shortens it); 0 answers at once as before.
var SubscribeWait = 10 * time.Second

// docArrived closes docReady once (the caller holds no lock requirement; it is idempotent).
func (s *Server) docArrived() {
	s.docOnce.Do(func() {
		if s.docReady != nil {
			close(s.docReady)
		}
	})
}

// waitForDocument blocks until the server holds a parameter document, the wait bound passes or ctx ends; true when a
// document is held at the end.
func (s *Server) waitForDocument(ctx context.Context) bool {
	if s.docReady == nil || SubscribeWait <= 0 {
		return false
	}
	t := time.NewTimer(SubscribeWait)
	defer t.Stop()
	select {
	case <-s.docReady:
		return true
	case <-t.C:
		return false
	case <-ctx.Done():
		return false
	}
}

// SetJob makes j the current job and notifies every session; a member
// job (j.Member set: the template the member's own node built) becomes
// that member's current job and is notified only to the sessions
// authorised under that address, so no member ever works another's
// template and the pool holds no template of its own.
// jobKey is the retention and current-job key of a job: the member's address, with the worker when the job is
// one worker's.
func jobKey(member, worker string) string {
	if worker == "" {
		return strings.ToLower(member)
	}
	return strings.ToLower(member) + "/" + worker
}

// anchorParam is the notify's program_anchor: 0x + 64 hex on an anchored job, empty otherwise.
func anchorParam(j Job) string {
	if !j.HasAnchor {
		return ""
	}
	return "0x" + hex.EncodeToString(j.ProgramAnchor[:])
}

// IsBlockWorker reports a block session: the worker name ends ".block" (the pool's own block miners on a
// coordinate whose sealer seals nothing itself).
func IsBlockWorker(worker string) bool {
	// ".block" or "-block" (Ember's worker name takes letters, digits, - and _ only: the GPU team's read of 15:50 UK,
	// 13 October 2026) or "_block"
	return strings.HasSuffix(worker, ".block") || strings.HasSuffix(worker, "-block") || strings.HasSuffix(worker, "_block")
}

// IsGraphWorker reports a graph session (WHOLEWORK.md section 10): the worker name ends ".graph", "-graph" or
// "_graph"; it alone receives a proof-carried coordinate's share jobs and submits tickets.
func IsGraphWorker(worker string) bool {
	return strings.HasSuffix(worker, ".graph") || strings.HasSuffix(worker, "-graph") || strings.HasSuffix(worker, "_graph")
}

// coordParam is the notify's coord field: the job's coordinate, or nil on a one-form job (absent on the wire).
func coordParam(j Job) *uint8 {
	if !j.HasCoord {
		return nil
	}
	c := j.Coord
	return &c
}

// jobKeyOf is the retention key of a job: the member and worker, with the coordinate when the job has one
// (VectorWork: one job per (address, worker, coord), so a session holds one job per coordinate it serves).
func jobKeyOf(j Job) string {
	k := jobKey(j.Member, j.Worker)
	if j.HasCoord {
		return k + "#" + strconv.Itoa(int(j.Coord))
	}
	return k
}

func (s *Server) SetJob(j Job) {
	s.mu.Lock()
	s.jobs[j.ID] = j
	if s.memberOrder == nil {
		s.memberOrder = map[string][]string{}
	}
	key := jobKeyOf(j)
	lst := append(s.memberOrder[key], j.ID)
	for len(lst) > limits.MaxJobsRetained {
		delete(s.jobs, lst[0])
		lst = lst[1:]
	}
	s.memberOrder[key] = lst
	// a bound across every member too (a member that left keeps its last jobs until the bound)
	s.order = append(s.order, j.ID)
	for len(s.order) > 64*limits.MaxJobsRetained {
		delete(s.jobs, s.order[0])
		s.order = s.order[1:]
	}
	if j.Member != "" {
		s.members[key] = &j
	} else {
		s.current = &j
	}
	if len(j.PaidCoords) > 0 {
		s.paid = append(s.paid[:0], j.PaidCoords...)
		s.dropUnpaidLocked()
	}
	if len(j.Params) > 0 {
		s.docArrived()
	}
	conns := make([]*conn, 0, len(s.sessions))
	for c := range s.sessions {
		if j.Member == "" || (strings.EqualFold(c.sess.Address, j.Member) && (j.Worker == "" || c.sess.Worker == j.Worker)) {
			if IsBlockWorker(c.sess.Worker) != (j.Kind == "block") {
				continue // block jobs to block sessions, share jobs to the rest
			}
			conns = append(conns, c)
		}
	}
	s.mu.Unlock()
	for _, c := range conns {
		c.notify(j)
	}
}

// Renotify sends a retained job again to the sessions it was issued to (the share lane's keepalive on a standing
// template, F-164, 15 October 2026); no bookkeeping changes: the job's identity, its place among the retained jobs
// and the session's current job are as they were. Returns the sessions notified (0 when the job is no longer
// retained or no session holds it).
func (s *Server) Renotify(id string) int {
	s.mu.Lock()
	j, ok := s.jobs[id]
	if !ok {
		s.mu.Unlock()
		return 0
	}
	conns := make([]*conn, 0, 1)
	for c := range s.sessions {
		if j.Member == "" || (strings.EqualFold(c.sess.Address, j.Member) && (j.Worker == "" || c.sess.Worker == j.Worker)) {
			if IsBlockWorker(c.sess.Worker) != (j.Kind == "block") {
				continue
			}
			conns = append(conns, c)
		}
	}
	s.mu.Unlock()
	// never clean: the job is the one the session holds, so a miner honouring clean_jobs keeps its search (the security review's
	// residual on 0.1.58, 15 October 2026: a plain share job carries Clean true and a keepalive re-sent it, restarting the
	// search every 30 s on a plain lane; every coordinate job is Clean false and was unaffected)
	j.Clean = false
	for _, c := range conns {
		c.notify(j)
	}
	return len(conns)
}

// admit is the admission bound before acceptance: AdmissionFor with the job when set, else Admission.
func (s *Server) admit(address string, job Job) (bool, string) {
	if s.AdmissionFor != nil {
		return s.AdmissionFor(address, job)
	}
	return s.Admission(address, job.Height)
}

// nameInvalid names an invalid share's cause on the log, once a minute per session (the VectorWork soak of 02:0xZ,
// 13 October 2026: every share of three sessions read "invalid" and nothing said why; on the public pool the
// fleet's hour of chain-id refusals of 22:58Z was likewise unnamed).
func (s *Server) nameInvalid(c *conn, job Job, format string, args ...interface{}) {
	now := s.now()
	s.mu.Lock()
	say := now.Sub(c.invalidNamedAt) >= time.Minute
	if say {
		c.invalidNamedAt = now
	}
	s.mu.Unlock()
	if say {
		log.Printf("share invalid for %s worker %q (job %s): %s", strings.ToLower(c.sess.Address), c.sess.Worker, job.ID, fmt.Sprintf(format, args...))
	}
}

// countStale counts a stale refusal for the address (the share-age read).
func (s *Server) countStale(address string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.staleRefused == nil {
		s.staleRefused = map[string]uint64{}
	}
	s.staleRefused[strings.ToLower(address)]++
}

// countAge counts an accepted share by its job's age: how many jobs the
// member has been issued since this one (0 = the member's latest).
func (s *Server) countAge(address string, job Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lst := s.memberOrder[jobKeyOf(job)]
	age := 0
	for i := len(lst) - 1; i >= 0; i-- {
		if lst[i] == job.ID {
			age = len(lst) - 1 - i
			break
		}
	}
	if s.ageAccepted == nil {
		s.ageAccepted = map[string][]uint64{}
	}
	a := strings.ToLower(address)
	for len(s.ageAccepted[a]) <= age {
		s.ageAccepted[a] = append(s.ageAccepted[a], 0)
	}
	s.ageAccepted[a][age]++
}

// AdmissionStats is the admission bound's read: refusals before acceptance by reason and by address.
func (s *Server) AdmissionStats() map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	by := map[string]uint64{}
	for k, v := range s.admissionRefused {
		by[k] = v
	}
	ba := map[string]uint64{}
	for k, v := range s.admissionByAddr {
		ba[k] = v
	}
	return map[string]interface{}{"on": s.Admission != nil, "refused_by_reason": by, "refused_by_address": ba,
		"rule": "a verified share is refused before acceptance, by name on the wire and the log, when its address has reached the chain's per-height cap at its height, when the node's pool is at capacity, when the pool's shares outstanding within the recency across every address reach what the chain carries over the recency (pool at carriage: F-50), or when the addresses with shares outstanding reach what the chain's list can pay (PA-53's admission bound as the pool applies it); a refused share is never accepted, receipted or counted against the session, so a receipt means the chain will pay"}
}

// ShareAgeStats is the share-age read per address: accepted shares by the
// job's age in templates behind the member's latest, and the stale
// refusals (a job no longer retained), since the pool started.
func (s *Server) ShareAgeStats() map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]interface{}{"retained_per_member": limits.MaxJobsRetained,
		"rule": "a share is accepted on any of the member's last retained jobs (by_age[n] = accepted shares on the job n templates behind the member's latest at acceptance); stale_refused counts shares on a job no longer retained; the sealer still refuses a share whose parent fell out of the chain, named on the journal"}
	by := map[string]interface{}{}
	for a, counts := range s.ageAccepted {
		by[a] = map[string]interface{}{"by_age": append([]uint64(nil), counts...), "stale_refused": s.staleRefused[a]}
	}
	for a, n := range s.staleRefused {
		if _, has := by[a]; !has {
			by[a] = map[string]interface{}{"by_age": []uint64{}, "stale_refused": n}
		}
	}
	out["by_address"] = by
	return out
}

// JobByID returns a retained job by its id (the share journal records the
// job's header, document and digest beside an accepted share).
func (s *Server) JobByID(id string) (Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	return j, ok
}

// Authorised lists the authorised sessions' addresses (lower-cased) with
// each session's extranonce1 as a u64 (the share lane's extraNonce for the
// member's share header); one entry per address, the first session's
// extranonce when an address has several.
func (s *Server) Authorised() map[string]uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]uint64{}
	for c := range s.sessions {
		if !c.sess.Authorised() || c.sess.Address == "" {
			continue
		}
		a := strings.ToLower(c.sess.Address)
		if _, has := out[a]; has {
			continue
		}
		var e uint64
		for _, b := range c.sess.ExtraNonce1[:] {
			e = e<<8 | uint64(b)
		}
		out[a] = e
	}
	return out
}

// MemberJob is the current job of the member's own node, nil when the
// address has none (the single-node form, or a member without a node).
func (s *Server) MemberJob(address, worker string) *Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j := s.members[jobKey(address, worker)]; j != nil && s.paysLocked(*j) {
		return j
	}
	if j := s.members[strings.ToLower(address)]; j != nil && s.paysLocked(*j) {
		return j
	}
	// no job for this worker yet (or no worker named): any job of the address, so a new session has the
	// document and a template at once; its own worker's job follows on the next poll
	prefix := strings.ToLower(address) + "/"
	for k, j := range s.members {
		if strings.HasPrefix(k, prefix) && s.paysLocked(*j) {
			return j
		}
	}
	return nil
}

// paysLocked is whether a job may be handed to a session: always on a job without a coordinate or before any job
// carried the pay list; else only when the list pays the job's coordinate (the caller holds the lock).
func (s *Server) paysLocked(j Job) bool {
	if !j.HasCoord || s.paid == nil {
		return true
	}
	for _, c := range s.paid {
		if c == j.Coord {
			return true
		}
	}
	return false
}

// dropUnpaidLocked removes every member job on a coordinate the pay list no longer pays from the member set and
// from the retained set (a share on it answers stale from then), so a session that subscribes later never
// receives it and a session holding it finds no refresh (the caller holds the lock).
func (s *Server) dropUnpaidLocked() {
	for k, j := range s.members {
		if j.HasCoord && !s.paysLocked(*j) {
			delete(s.members, k)
			delete(s.jobs, j.ID)
			mk := jobKeyOf(*j)
			kept := s.memberOrder[mk][:0]
			for _, id := range s.memberOrder[mk] {
				if id != j.ID {
					kept = append(kept, id)
				}
			}
			s.memberOrder[mk] = kept
		}
	}
}

// MemberJobs is every current job of the member for the worker: one on a one-form chain (MemberJob), one per
// coordinate under VectorWork (the single-session form of the product team's router, 13 October 2026: a session
// subscribes once and holds one job per coordinate the pool serves, each notified with its coord).
func (s *Server) MemberJobs(address, worker string) []Job {
	s.mu.Lock()
	prefix := jobKey(address, worker) + "#"
	var out []Job
	for k, j := range s.members {
		if strings.HasPrefix(k, prefix) && s.paysLocked(*j) {
			out = append(out, *j)
		}
	}
	s.mu.Unlock()
	if len(out) > 0 {
		sort.Slice(out, func(x, y int) bool { return out[x].Coord < out[y].Coord })
		return out
	}
	if mj := s.MemberJob(address, worker); mj != nil {
		return []Job{*mj}
	}
	return nil
}

// MinersConnected is the count of authorised sessions (the network page's live figure).
func (s *Server) MinersConnected() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for c := range s.sessions {
		if c.sess.Authorised() && c.sess.Address != "" {
			n++
		}
	}
	return n
}

// MinersSplit counts the authorised sessions as MinersConnected does, split by whether own says the session's
// address or worker name is the operator's (the public page's "N connected: X outside, Y ours").
func (s *Server) MinersSplit(own func(address, worker string) bool) (outside, ours int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.sessions {
		if c.sess.Authorised() && c.sess.Address != "" {
			if own != nil && own(c.sess.Address, c.sess.Worker) {
				ours++
			} else {
				outside++
			}
		}
	}
	return outside, ours
}

// AuthorisedWorkers is the authorised sessions by address and worker name: for each, the extranonce of the first
// session seen (the share lane issues one job per address and worker, the H_s on that extranonce).
// DeclaredRates lists the authorised workers' declared hash rates (hashes a second) by address and worker,
// those that declared one.
func (s *Server) DeclaredRates() map[string]map[string]float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]map[string]float64{}
	for c := range s.sessions {
		if !c.sess.Authorised() || c.sess.Address == "" || c.declaredRate <= 0 {
			continue
		}
		a := strings.ToLower(c.sess.Address)
		if out[a] == nil {
			out[a] = map[string]float64{}
		}
		if out[a][c.sess.Worker] < c.declaredRate {
			out[a][c.sess.Worker] = c.declaredRate
		}
	}
	return out
}

func (s *Server) AuthorisedWorkers() map[string]map[string]uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]map[string]uint64{}
	for c := range s.sessions {
		if !c.sess.Authorised() || c.sess.Address == "" {
			continue
		}
		a := strings.ToLower(c.sess.Address)
		if out[a] == nil {
			out[a] = map[string]uint64{}
		}
		if _, has := out[a][c.sess.Worker]; has {
			continue
		}
		var e uint64
		for _, b := range c.sess.ExtraNonce1[:] {
			e = e<<8 | uint64(b)
		}
		out[a][c.sess.Worker] = e
	}
	return out
}

// anyParams is a parameter document for the subscribe result when the
// server has no pool-wide job: the first member job's (every member node
// serves the same chain and document).
func (s *Server) anyParams() (json.RawMessage, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current != nil && len(s.current.Params) > 0 {
		return s.current.Params, s.current.ParamsDigest
	}
	// the share lane's document first (0.1.65, 16 October 2026): the last template polled, one answer for every
	// subscribe rather than a member job drawn at random from the retained set (on a multi-lane pool the random draw
	// gave each subscribe a different lane's document, and a retained job without one an EMPTY answer the miner
	// stops on by F-14); a member job's document only when the lane holds none, and never an empty one
	if len(s.laneParams) > 0 {
		return s.laneParams, s.laneParamsDigest
	}
	for _, j := range s.members {
		if len(j.Params) > 0 {
			return j.Params, j.ParamsDigest
		}
	}
	if s.current != nil {
		return s.current.Params, s.current.ParamsDigest
	}
	return nil, ""
}

// SubscribesWithoutDocument counts the subscribes answered with no parameter document (the once-a-minute line names
// the latest); zero on a pool of record.
func (s *Server) SubscribesWithoutDocument() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.subscribesNoDoc
}

// SetLaneParams holds the share lane's parameter document (the node's share
// document verbatim with its digest) for the subscribe result of a session
// that has no job yet.
func (s *Server) SetLaneParams(params json.RawMessage, digest string) {
	s.mu.Lock()
	if len(params) > 0 {
		s.laneParams, s.laneParamsDigest = params, digest
	}
	s.mu.Unlock()
	if len(params) > 0 {
		s.docArrived()
	}
}

// Serve accepts connections until the listener closes.
func (s *Server) Serve(ctx context.Context, l net.Listener) error {
	for {
		c, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if s.isDraining() {
			// the drain (the handover form): a connection that still lands on this generation's listener is told
			// to re-dial at once and closed, so the queued SYNs behind a closing SO_REUSEPORT listener are not
			// reset unanswered (one far reader lost a 5 s slot at the 0.1.31 hand-off, 01:45:52Z 14 October 2026)
			go func(c net.Conn) {
				defer c.Close()
				b, _ := json.Marshal(Notification{Method: MethodReconnect, Params: s.reconnectParams()})
				c.SetWriteDeadline(time.Now().Add(2 * time.Second)) //nolint:errcheck
				c.Write(append(b, '\n'))                            //nolint:errcheck
				time.Sleep(200 * time.Millisecond)
			}(c)
			continue
		}
		go s.ServeConn(ctx, c)
	}
}

// Drain is the old generation's last act before it stops (the project's order of 05:5x UK, 14 October 2026: a roll must
// not cost a connected miner its backoff): every session is sent client.reconnect [host, port, 0] naming the
// public endpoint, new connections are told the same and closed, and Drain returns when every session has gone
// or wait has passed. The listener is closed by the caller after it.
func (s *Server) Drain(endpoint string, wait time.Duration) int {
	host, port := endpoint, ""
	if h, p, err := net.SplitHostPort(endpoint); err == nil {
		host, port = h, p
	}
	s.mu.Lock()
	s.draining = true
	s.reconnectHost, s.reconnectPort = host, port
	conns := make([]*conn, 0, len(s.sessions))
	for c := range s.sessions {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	params := s.reconnectParams()
	for _, c := range conns {
		_ = c.write(Notification{Method: MethodReconnect, Params: params})
	}
	log.Printf("drain: client.reconnect sent to %d session(s) naming %s; waiting up to %s for them to go", len(conns), endpoint, wait)
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		n := len(s.sessions)
		s.mu.Unlock()
		if n == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	s.mu.Lock()
	left := len(s.sessions)
	s.mu.Unlock()
	log.Printf("drain: %d session(s) still open at the end of the wait", left)
	return left
}

func (s *Server) isDraining() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.draining
}

func (s *Server) reconnectParams() []interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	port := 0
	if n, err := strconv.Atoi(s.reconnectPort); err == nil {
		port = n
	}
	return []interface{}{s.reconnectHost, port, 0}
}

// ServeConn runs one session on c and closes c when it ends.
func (s *Server) ServeConn(ctx context.Context, c net.Conn) {
	defer c.Close()
	sess, err := session.New(nil, s.now())
	if err != nil {
		return
	}
	cn := &conn{srv: s, net: c, sess: sess, out: make(chan []byte, outboundQueue), done: make(chan struct{})}
	go cn.writer()
	s.mu.Lock()
	s.sessions[cn] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.sessions, cn)
		s.mu.Unlock()
		cn.closeOnce.Do(func() { close(cn.done) })
	}()
	r := bufio.NewReaderSize(c, limits.MaxFrameBytes)
	for {
		frame, err := ReadFrame(r)
		if err != nil {
			return
		}
		req, err := Decode(frame)
		if err != nil {
			cn.reply(nil, nil, &Error{Code: 20, Reason: ReasonBound})
			return
		}
		if err := cn.handle(ctx, req); err != nil {
			return
		}
	}
}

// outboundQueue bounds the frames queued to one session. A session that
// does not drain its queue is closed; a slow client never blocks SetJob
// for the others.
const outboundQueue = 64

type conn struct {
	srv       *Server
	net       net.Conn
	sess      *session.Session
	out       chan []byte
	done      chan struct{}
	closeOnce sync.Once
	// the session's fee terms (the fee-waiver handshake): the miner's dev-fee
	// state as subscribed, whether its signature verified, the key it named,
	// and the fee this session pays
	devFee   *DevFee
	devFeeOK bool
	// prevChainNamed: the transition (a share signed over the previous chain id) named once on the log
	prevChainNamed bool
	invalidNamedAt time.Time // the last minute an invalid share's cause was named for this session
	devFeeKey      [32]byte
	poolFee        PoolFee
	ticket         []byte    // the last verified ticket of a proof-carried submit, handed to the share
	declaredRate   float64   // the hash rate the subscribe declared (hashes a second), zero when none
	feeFor         string    // a fee session: the member address it is keyed to by its for block (lower-cased)
	feeForBlock    *ForBlock // the for block as received
	feeCounted     bool
	// the bad-share threshold: the session's refused shares in the window
	badAt   []time.Time
	address string
	// frames queued and frames written to the socket (drain waits for the last reply before a close)
	queued uint64
	wrote  uint64
}

// badShare records a refused share and reports whether the session has
// reached the threshold (the window's count at or past the limit).
func (c *conn) badShare(now time.Time) bool {
	srv := c.srv
	if srv.BadShareLimit == 0 {
		return false
	}
	cut := now.Add(-srv.BadShareWindow)
	kept := c.badAt[:0]
	for _, t := range c.badAt {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	c.badAt = append(kept, now)
	return uint32(len(c.badAt)) >= srv.BadShareLimit
}

// coolAddress refuses the address at authorize for the cooling period and
// counts the closed session.
// coolKey is the cooling's key: the address AND the worker name (the public pool of 08:27:04Z, 13 October 2026:
// one session of the founder's fleet address reached the threshold and the whole fleet's new sessions were
// refused at authorize for five minutes; a shared address is many miners, and the one that misbehaved is the
// worker). A session without a worker name cools its address alone, as before.
func coolKey(address, worker string) string {
	if worker == "" {
		return strings.ToLower(address)
	}
	return strings.ToLower(address) + "/" + worker
}

func (s *Server) coolAddress(address, worker string, now time.Time) {
	s.bmu.Lock()
	defer s.bmu.Unlock()
	if s.cooling == nil {
		s.cooling = map[string]time.Time{}
	}
	if address != "" && s.BadShareCooling > 0 {
		s.cooling[coolKey(address, worker)] = now.Add(s.BadShareCooling)
	}
	s.sessionsClosed++
}
func (s *Server) cooled(address, worker string, now time.Time) bool {
	s.bmu.Lock()
	defer s.bmu.Unlock()
	k := coolKey(address, worker)
	until, ok := s.cooling[k]
	if !ok {
		return false
	}
	if now.After(until) {
		delete(s.cooling, k)
		return false
	}
	s.authRefused++
	return true
}

// BadShareStats is the threshold and its counts for /shares.
func (s *Server) BadShareStats() map[string]interface{} {
	s.bmu.Lock()
	defer s.bmu.Unlock()
	return map[string]interface{}{"limit": s.BadShareLimit, "window_s": uint64(s.BadShareWindow / time.Second), "cooling_s": uint64(s.BadShareCooling / time.Second),
		"sessions_closed": s.sessionsClosed, "authorize_refused_cooling": s.authRefused, "addresses_cooling": len(s.cooling),
		"rule": "a session whose refused shares (invalid, low difficulty, duplicate, a bad nonce) reach the limit within the window is closed with the reason on its last reply, and its address is refused at authorize for the cooling period; zero limit means no threshold"}
}

// errSessionClosed ends a session from its handler after its last reply (the bad-share threshold).
var errSessionClosed = errors.New("stratum: session closed at the pool's threshold")

// DevFeeVerifier checks a miner's dev-fee signature: the session key (32
// bytes hex) over DevFeeMessage, returning the key.
type DevFeeVerifier interface {
	VerifyDevFee(d DevFee) ([32]byte, bool)
}

// sessionFee decides the session's fee from the subscribed dev-fee state:
// the declared fee, waived when the waiver is on, the miner reports a dev
// fee of at least 1 percent and its signature verified.
func (c *conn) sessionFee() PoolFee {
	srv := c.srv
	if srv.FeeBps == 0 {
		return PoolFee{Bps: 0, Waived: false, Reason: "no declared fee"}
	}
	// THE FOUNDER'S DECISION of 21:3x UK, 13 October 2026: the pool charges its declared fee on EVERY session, the
	// miner's dev fee on or off (the fund record's waiver withdrawn; the dev fee stays Ember's own 1 percent with
	// its switch). The dev-fee state is still read and named, nothing is waived on it.
	state := "dev fee not reported"
	switch {
	case c.devFee == nil:
	case c.devFee.Percent < 1:
		state = "dev fee off"
	case !c.devFeeOK:
		state = "dev fee reported, signature not verified"
	default:
		state = "dev fee " + strconvUint(c.devFee.Percent) + " percent declared and verified"
	}
	if srv.FeeDecider != nil && c.feeFor == "" {
		// the verifiable waiver (the founder's word of 18:2x UK, 14 October 2026): the member's own session is
		// waived while its fee shares, on a fee session keyed to it, meet the fraction over the settle window
		if waived, why := srv.FeeDecider(c.sess.Address, c.sess.Worker); waived {
			return PoolFee{Bps: 0, Waived: true, Reason: why + "; " + state}
		} else {
			return PoolFee{Bps: srv.FeeBps, Waived: false, Reason: why + "; " + state}
		}
	}
	if c.feeFor != "" {
		return PoolFee{Bps: 0, Waived: true, Reason: "a fee session keyed to " + c.feeFor + ": its shares are the member's fee, not charged"}
	}
	return PoolFee{Bps: srv.FeeBps, Waived: false, Reason: "the pool's declared fee applies to every session (the founder's decision of 13 October 2026; the dev fee is the miner's own): " + state}
}

// keyFeeSession verifies a fee session's for block against the member's session key the pool holds and returns
// the member's address, or the refusal's reason.
// shareAccount is the address a share credits: the job's payee when set, else the session's.
func shareAccount(j Job, session string) string {
	if j.ForwardAs != "" {
		return j.ForwardAs
	}
	if j.Payee != "" {
		return j.Payee
	}
	return session
}

func (s *Server) keyFeeSession(f *ForBlock) (member, why string) {
	mk, err := hex.DecodeString(f.SessionKey)
	if err != nil || len(mk) != ed25519.PublicKeySize {
		return "", "fee session: the for block's session_key is not a 32-byte hex key"
	}
	sig, err := hex.DecodeString(f.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return "", "fee session: the for block's signature is not a 64-byte hex signature"
	}
	if !ed25519.Verify(ed25519.PublicKey(mk), f.Message(), sig) {
		return "", "fee session: the for block's signature does not verify against the member's session key"
	}
	addr := strings.ToLower(f.Address)
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.sessions {
		if c.sess.Authorised() && strings.EqualFold(c.sess.Address, addr) && c.sess.HasKey && hex.EncodeToString(c.sess.Key[:]) == strings.ToLower(f.SessionKey) {
			return addr, ""
		}
	}
	return "", "fee session: no member session under that key"
}

// FeeDecider, when set, answers the session's fee from the verifiable fee accounting (the founder's word of
// 18:2x UK, 14 October 2026, "Ember on our pool: 1 percent total"): waived while the member's fee shares meet the
// fraction over the settle window, charged otherwise; the reason names the figures.
type FeeDecider func(address, worker string) (waived bool, reason string)

// SessionFees lists an address's live sessions' fee terms by worker (the ledger's fee row per address).
func (s *Server) SessionFees(address string) map[string]PoolFee {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]PoolFee{}
	for c := range s.sessions {
		if c.sess.Authorised() && strings.EqualFold(c.sess.Address, address) {
			out[c.sess.Worker] = c.poolFee
		}
	}
	return out
}

// countFee records the session's fee outcome once, at authorize.
// logAuthorised names every session the moment it authorises: the address and the worker as sent, the form (signed or
// plain) and the PEER (the connection's remote address and port), on the box's log alone (0.1.78, 16 October 2026:
// an Ember-shaped client re-dialled the public pool every two minutes for a day under the name of one of our rigs and
// the pool could not say from which machine; the journal and the served records carry no address, as before).
func (c *conn) logAuthorised(address, worker, form string) {
	peer := "unknown"
	if c.net != nil && c.net.RemoteAddr() != nil {
		peer = c.net.RemoteAddr().String()
	}
	log.Printf("authorised: %s worker %q (%s) from %s", strings.ToLower(address), worker, form, peer)
}

func (c *conn) countFee() {
	if c.feeCounted {
		return
	}
	c.feeCounted = true
	c.srv.fmu.Lock()
	if c.poolFee.Waived {
		c.srv.feeWaived++
	} else {
		c.srv.feeCharged++
	}
	c.srv.fmu.Unlock()
	// the session's fee answer on the log, once (the ledger's fee row per address, the project's order of 20:4x UK,
	// 13 October 2026): the address, the worker, the bps and whether waived, and the reason
	log.Printf("fee: session of %s worker %q answered pool fee %d bps, waived %t: %s", c.sess.Address, c.sess.Worker, c.poolFee.Bps, c.poolFee.Waived, c.poolFee.Reason)
}

// FeeStats is the pool's fee rule and its count of sessions for /shares.
func (s *Server) FeeStats() map[string]interface{} {
	s.fmu.Lock()
	defer s.fmu.Unlock()
	rule := "no declared fee"
	switch {
	case s.FeeBps > 0 && s.FeeDecider != nil:
		rule = "the declared fee is " + strconvUint(s.FeeBps) + " bps of the miner's side, WAIVED for a member while its Ember fee shares are on the chain at the fee's fraction over the settle window (the verifiable waiver, 14 October 2026: the verdict by member and worker on /address under fee, never by the authorisation string), charged otherwise as one member height in a hundred issued under the pool's own fee address; a fee session is never charged; a session's fee is answered on its authorize and carried in every receipt"
	case s.FeeBps > 0:
		rule = "the declared fee is " + strconvUint(s.FeeBps) + " bps of the miner's side on every session, the miner's dev fee on or off (the founder's decision of 13 October 2026: no waiver); a session's fee is answered on its subscribe result and carried in every receipt"
	}
	return map[string]interface{}{"declared_bps": s.FeeBps, "waiver": s.FeeWaiver, "rule": rule, "sessions_waived": s.feeWaived, "sessions_charged": s.feeCharged}
}

// writer drains the outbound queue to the socket in order.
func (c *conn) writer() {
	w := bufio.NewWriter(c.net)
	for {
		select {
		case <-c.done:
			return
		case b := <-c.out:
			if _, err := w.Write(b); err != nil {
				c.net.Close()
				return
			}
			if err := w.Flush(); err != nil {
				c.net.Close()
				return
			}
			atomic.AddUint64(&c.wrote, 1)
		}
	}
}

// drain waits until every queued frame has reached the socket (the last
// reply before a session is closed by the pool), at most about a second.
func (c *conn) drain() {
	for i := 0; i < 100 && atomic.LoadUint64(&c.wrote) < atomic.LoadUint64(&c.queued); i++ {
		time.Sleep(10 * time.Millisecond)
	}
}

// write queues one frame; a full queue closes the session.
func (c *conn) write(v interface{}) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	select {
	case c.out <- append(b, '\n'):
		atomic.AddUint64(&c.queued, 1)
		return nil
	case <-c.done:
		return net.ErrClosed
	default:
		c.closeOnce.Do(func() { close(c.done) })
		c.net.Close()
		return errors.New("stratum: outbound queue full; session closed")
	}
}

func (c *conn) reply(id json.RawMessage, result interface{}, e *Error) error {
	if id == nil {
		id = json.RawMessage("null")
	}
	return c.write(Response{ID: id, Result: result, Error: e})
}

func (c *conn) notify(j Job) {
	c.sess.IssueOn(j.ID, hex.EncodeToString(j.Header[:]))
	_ = c.write(Notification{Method: MethodNotify, Params: NotifyParams{
		JobID:            j.ID,
		Height:           j.Height,
		Algorithm:        j.Algorithm,
		AlgorithmVersion: j.AlgorithmVersion,
		Epoch:            j.Epoch,
		Parent:           hex.EncodeToString(j.Parent[:]),
		Header:           hex.EncodeToString(j.Header[:]),
		Boundary:         hex.EncodeToString(j.Boundary[:]),
		ShareTarget:      hex.EncodeToString(j.ShareTarget[:]),
		Clean:            j.Clean,
		ChainID:          c.srv.ChainID,
		Kind:             j.Kind,
		Coord:            coordParam(j),
		Difficulty:       j.Difficulty,
		RewardWei:        j.RewardWei,
		LastHeight:       j.LastHeight,
		CoordName:        j.CoordName,
		PaidCoords:       paidCoordsParam(j),
		LaneWorth:        j.LaneWorth,
		LaneWorkers:      j.LaneWorkers,
		ProgramAnchor:    anchorParam(j),
		HeaderFor:        j.ForwardAs,
		AnchorHeight:     j.AnchorHeight,

		SubsidyWei:         j.SubsidyWei,
		PriorityFeeMeanWei: j.PriorityFeeMeanWei,
		Params:             j.Params,
		ParamsDigest:       j.ParamsDigest,
		Payouts:            j.Payouts,
		Member:             j.Member,
		ShareStep:          j.ShareStep,
	}})
}

// nonceRange is the range this session owns.
func (c *conn) nonceRange() (start, count uint64) {
	return nonceRangeOf(c.sess.ExtraNonce1)
}

// nonceRangeOf is a session's nonce range from its extranonce1: the prefix the low NonceSessionBits bits of it, the
// range 2^NonceRangeBits above (limits.NonceRangeBits: sized so the fastest card cannot search it through inside a
// template's life); the range never overflows the nonce's 64 bits.
func nonceRangeOf(extra [limits.ExtraNonce1Bytes]byte) (start, count uint64) {
	e := uint64(binary.BigEndian.Uint32(extra[:])) & (1<<limits.NonceSessionBits - 1)
	return e << limits.NonceRangeBits, 1 << limits.NonceRangeBits
}

func (c *conn) handle(ctx context.Context, req Request) error {
	switch req.Method {
	case MethodSubscribe:
		start, count := c.nonceRange()
		c.srv.mu.Lock()
		cur := c.srv.current
		c.srv.mu.Unlock()
		res := SubscribeResult{
			SessionID:   c.sess.IDHex(),
			ExtraNonce1: hex.EncodeToString(c.sess.ExtraNonce1[:]),
			NonceStart:  strconv.FormatUint(start, 10),
			NonceCount:  strconv.FormatUint(count, 10),
			Protocol:    "IS/1",
		}
		if c.srv.SignedShares {
			res.SignedShares = true
			res.PoolID = hex.EncodeToString(c.srv.PoolID[:])
			if c.srv.Receipts != nil {
				pk := c.srv.Receipts.PublicKey()
				res.ReceiptKey = hex.EncodeToString(pk[:])
			}
		}
		res.Params, res.ParamsDigest = c.srv.anyParams()
		if len(res.Params) == 0 {
			// a subscribe before the server holds a document (a generation's first seconds: the listener opens
			// before the first template): wait for the first document up to SubscribeWait rather than answer a
			// document-less result Ember stops itself on; answered without one past the bound, said once a minute
			if c.srv.waitForDocument(ctx) {
				res.Params, res.ParamsDigest = c.srv.anyParams()
			}
			if len(res.Params) == 0 {
				c.srv.mu.Lock()
				c.srv.subscribesNoDoc++
				n := c.srv.subscribesNoDoc
				say := c.srv.now().Sub(c.srv.docSaid) >= time.Minute
				if say {
					c.srv.docSaid = c.srv.now()
				}
				c.srv.mu.Unlock()
				if say {
					log.Printf("subscribe answered with no parameter document after %s (%d so far): the share lane holds none (the miner will refuse to start on it)", SubscribeWait, n)
				}
			}
		}
		if len(c.srv.PoolList) > 0 {
			res.PoolList = c.srv.PoolList
		}
		// the fee-waiver handshake: the miner's dev-fee state on the subscribe, its signature verified by the
		// session key it names, the session's fee answered in the same result and carried in every receipt
		c.declaredRate = RateFromParams(req.Params)
		if d := DevFeeFromParams(req.Params); d != nil {
			c.devFee = d
			if c.srv.DevFeeVerifier != nil {
				c.devFeeKey, c.devFeeOK = c.srv.DevFeeVerifier.VerifyDevFee(*d)
			}
		}
		if f := ForFromParams(req.Params); f != nil {
			// a FEE session keyed to its member (the product team's form of 18:3x UK, 14 October 2026): the for block's
			// signature by the member's session key, which the pool holds from the member's own authorize; the
			// answer names the member keyed to, a failed signature refuses the subscribe by name
			if member, why := c.srv.keyFeeSession(f); why != "" {
				log.Printf("fee session refused at subscribe (error 24) for member %s: %s", f.Address, why)
				return c.reply(req.ID, nil, &Error{Code: 24, Reason: why})
			} else {
				log.Printf("fee session keyed to %s by its for block (fee key %s.., %d percent, %d window shares)", member, f.FeeSessionKey[:min(8, len(f.FeeSessionKey))], f.Percent, f.WindowShares)
				c.feeFor, c.feeForBlock = member, f
				res.KeyedTo = member
			}
		}
		if c.srv.FeeBps > 0 {
			c.poolFee = c.sessionFee()
			pf := c.poolFee
			res.PoolFee = &pf
		}
		res.ForwardAs = c.srv.ForwardAs
		if err := c.reply(req.ID, res, nil); err != nil {
			return err
		}
		if cur != nil {
			c.notify(*cur)
		}
		// on the member-node form the session's job arrives at authorize,
		// when the address is known
		return nil
	case MethodAuthorize:
		var params []string
		if err := json.Unmarshal(req.Params, &params); err != nil || (len(params) != 2 && len(params) != 4) {
			return c.reply(req.ID, false, &Error{Code: 20, Reason: ReasonBound})
		}
		// the bad-share threshold's cooling: an address closed for bad shares is refused at authorize until its
		// cooling period passes
		if c.srv.cooled(params[0], params[1], c.srv.now()) {
			return c.reply(req.ID, false, &Error{Code: 25, Reason: ReasonCooling})
		}
		c.address = params[0]
		if IsBlockWorker(params[1]) && !c.srv.BlockSessions {
			log.Printf("authorize refused: %s worker %q: block sessions are off on this pool", strings.ToLower(params[0]), params[1])
			return c.reply(req.ID, false, &Error{Code: 24, Reason: ReasonUnauthorised + ": block sessions are off on this pool"})
		}
		if c.srv.SignedShares {
			// the non-custodial form: [address, worker, key_hex, sig_hex], the
			// signature by the key over the authorize message binding the key to
			// the address for this session; the key never reaches the pool
			if len(params) != 4 || c.srv.Verifier == nil {
				// named (the product team's reading of 06:1x UK, 14 October 2026: a re-dial's short authorize read as the
				// pool's refusal for an hour because the answer and the log said only "unauthorised")
				log.Printf("authorize refused: %s worker %q: the authorize carries %d part(s), the signed form needs four (address, worker, key, signature)", strings.ToLower(params[0]), params[1], len(params))
				return c.reply(req.ID, false, &Error{Code: 24, Reason: ReasonUnauthorised + ": the authorize carries no signature; this pool takes the signed form (address, worker, key, signature)"})
			}
			key, ok := c.srv.Verifier.Authorize(c.sess.ID, params[0], params[2], params[3])
			if !ok {
				// named on the log (never the key): a refused re-authorise during a hang turned a pause into a stop on an
				// installed app (the product team's reading of 07:21:54Z, 12 October 2026)
				log.Printf("authorize refused: %s worker %q: the signature does not verify over this session's id", strings.ToLower(params[0]), params[1])
			}
			if !ok {
				return c.reply(req.ID, false, &Error{Code: 24, Reason: ReasonUnauthorised})
			}
			if err := c.sess.Authorise(params[0], params[1]); err != nil {
				return c.reply(req.ID, false, &Error{Code: 24, Reason: ReasonBound})
			}
			c.sess.BindKey(key)
			// the waiver binds to the key the session authorizes under: a dev-fee state signed by another
			// key, or under another worker name, is void and the session pays the declared fee
			if c.devFee != nil && c.devFeeOK && (key != c.devFeeKey || c.devFee.Worker != params[1]) {
				c.devFeeOK = false
				c.poolFee = c.sessionFee()
				c.poolFee.Reason = "dev fee not verified: the session key or the worker differs from the authorize"
			}
			if c.srv.FeeDecider != nil && c.srv.FeeBps > 0 {
				// the verifiable waiver's verdict is by address and worker, known only now (the subscribe answer
				// carried the rule with no verdict; the charge itself is by the job's payee, never by this answer)
				c.poolFee = c.sessionFee()
			}
			c.countFee()
			c.logAuthorised(params[0], params[1], "signed")
			if c.srv.OnAuthorized != nil {
				c.srv.OnAuthorized(key, params[0])
			}
			if err := c.reply(req.ID, true, nil); err != nil {
				return err
			}
			for _, mj := range c.srv.MemberJobs(params[0], params[1]) {
				c.notify(mj)
			}
			return nil
		}
		if err := c.sess.Authorise(params[0], params[1]); err != nil {
			return c.reply(req.ID, false, &Error{Code: 24, Reason: ReasonBound})
		}
		c.logAuthorised(params[0], params[1], "plain")
		if err := c.reply(req.ID, true, nil); err != nil {
			return err
		}
		if mj := c.srv.MemberJob(params[0], params[1]); mj != nil {
			c.notify(*mj)
		}
		return nil
	case MethodSubmit:
		var p SubmitParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return c.reply(req.ID, false, &Error{Code: 20, Reason: ReasonBound})
		}
		reason := c.submit(ctx, p)
		if reason != "" {
			if c.srv.OnRefused != nil {
				r := Refusal{Address: c.address, Worker: c.sess.Worker, JobID: p.JobID, Nonce: p.Nonce, Reason: reason}
				c.srv.mu.Lock()
				if j, ok := c.srv.jobs[p.JobID]; ok {
					jc := j
					r.Job = &jc
				}
				c.srv.mu.Unlock()
				c.srv.OnRefused(r)
			}
			// the bad-share threshold: a refused share counts in the session's window; at the limit the session is
			// closed with the reason on this last reply and its address cooled at authorize
			// a stale job, the pool's own rate throttle and an unauthorised session are not bad shares
			if reason != ReasonStale && reason != ReasonRate && reason != ReasonUnauthorised && reason != ReasonGateHeld && !strings.HasPrefix(reason, ReasonAdmission) && c.badShare(c.srv.now()) {
				c.srv.coolAddress(c.address, c.sess.Worker, c.srv.now())
				log.Printf("session closed at the bad-share threshold: %s worker %q, the last refusal %q; the worker cooled %s", strings.ToLower(c.address), c.sess.Worker, reason, c.srv.BadShareCooling)
				_ = c.reply(req.ID, false, &Error{Code: 23, Reason: ReasonBadShares + ": " + reason})
				// the last reply drains to the socket, then the session ends (ServeConn closes the connection)
				c.drain()
				return errSessionClosed
			}
			return c.reply(req.ID, false, &Error{Code: 23, Reason: reason})
		}
		return c.reply(req.ID, true, nil)
	default:
		return c.reply(req.ID, nil, &Error{Code: 20, Reason: ReasonInvalid})
	}
}

// submit validates a share in the order: bounds, session (authorised,
// known job, rate, duplicate), nonce range, reference verification,
// claimed values, share target, boundary.
func (c *conn) submit(ctx context.Context, p SubmitParams) string {
	if !limits.ValidJobID(p.JobID) || !limits.IsHex(p.Mix, 64) || !limits.IsHex(p.Hash, 64) {
		return ReasonBound
	}
	if g := c.srv.Gate; g != nil {
		wait := c.srv.GateWait
		if wait <= 0 {
			wait = 15 * time.Second
		}
		select {
		case <-g:
		case <-ctx.Done():
			return ReasonBound
		case <-time.After(wait):
			return ReasonGateHeld
		}
	}
	nonce, err := strconv.ParseUint(p.Nonce, 10, 64)
	if err != nil {
		return ReasonBadNonce
	}
	now := c.srv.now()
	// A submission that claims the block boundary goes through the
	// block-claim bucket; the claim is checked against the reference below.
	c.srv.mu.Lock()
	jobForClaim, known := c.srv.jobs[p.JobID]
	c.srv.mu.Unlock()
	var claimed [32]byte
	claimBytes, _ := hex.DecodeString(p.Hash)
	copy(claimed[:], claimBytes)
	accept := c.sess.Accept
	if known && validate.MeetsTarget(claimed, jobForClaim.Boundary) {
		accept = c.sess.AcceptBlockClaim
	}
	if err := accept(p.JobID, "", p.Nonce, now); err != nil {
		switch {
		case errors.Is(err, session.ErrNotAuthorised):
			return ReasonUnauthorised
		case errors.Is(err, session.ErrStale):
			c.srv.countStale(c.sess.Address)
			return ReasonStale
		case errors.Is(err, session.ErrDuplicate):
			return ReasonDuplicate
		case errors.Is(err, session.ErrRefound):
			// an honest re-find on a re-issued job of the same header (the VectorWork soak of 04:37Z, 13 October 2026:
			// the header dedupe turned a client's nonce restart into twenty "duplicate" bad shares and closed its
			// session): refused by name, never counted toward the threshold
			return ReasonAdmission + ": " + ReasonRefound
		case errors.Is(err, session.ErrRate):
			if known && c.srv.OnRateRefused != nil {
				c.srv.OnRateRefused(jobForClaim)
			}
			// the session's submit bucket is empty (limits.MaxSharesPerSecond a second, a burst of the same): the share
			// is not read and not accepted, so the reply carries the before-acceptance form and the bound by name
			// (the product team's reading of 11:2x UK, 12 October 2026: a bare "rate" read as a rejected share), never a
			// count toward the bad-share threshold
			return ReasonAdmission + ": " + ReasonRate + fmt.Sprintf(": the session's submit bucket is empty (%d a second, a burst of %d); the share is dropped, submit at a steadier pace", limits.MaxSharesPerSecond, limits.MaxSharesPerSecond)
		}
		return ReasonInvalid
	}
	start, count := c.nonceRange()
	if nonce < start || nonce-start >= count {
		return ReasonBadNonce
	}
	c.srv.mu.Lock()
	job, ok := c.srv.jobs[p.JobID]
	c.srv.mu.Unlock()
	if !ok {
		c.srv.countStale(c.sess.Address)
		return ReasonStale
	}
	var key [32]byte
	signed := false
	if c.srv.SignedShares {
		// the non-custodial form: the share carries the session's key and a
		// signature over (tag, chain id, pool id, header, nonce, mix, hash);
		// a share under another key, or unsigned, or badly signed, is refused.
		// The cheap checks run BEFORE the hash (security's F-31): the key
		// must be present and the session's, and the signature must verify
		// over the mix and hash as submitted, so a refused share never costs
		// the pool a reference hash.
		if p.Key == "" || p.Sig == "" || c.srv.Verifier == nil || !c.sess.HasKey {
			return ReasonUnauthorised
		}
		if hex.EncodeToString(c.sess.Key[:]) != strings.ToLower(strings.TrimPrefix(p.Key, "0x")) {
			return ReasonUnauthorised
		}
		var claimedMix, claimedHash [32]byte
		mb, _ := hex.DecodeString(p.Mix)
		hb, _ := hex.DecodeString(p.Hash)
		copy(claimedMix[:], mb)
		copy(claimedHash[:], hb)
		k, ok := c.srv.Verifier.Share(c.srv.ChainID, c.srv.PoolID, job.Header, nonce, claimedMix, claimedHash, p.Key, p.Sig)
		if (!ok || k != c.sess.Key) && c.srv.ChainIDPrev != 0 && c.srv.ChainIDPrev != c.srv.ChainID {
			// the named transition: the signature over the previous chain id
			if k2, ok2 := c.srv.Verifier.Share(c.srv.ChainIDPrev, c.srv.PoolID, job.Header, nonce, claimedMix, claimedHash, p.Key, p.Sig); ok2 && k2 == c.sess.Key {
				k, ok = k2, true
				if !c.prevChainNamed {
					c.prevChainNamed = true
					log.Printf("share signed over the previous chain id %d accepted for %s worker %q (the pool is on %d; the share verifies against the current job): the transition, named once per session", c.srv.ChainIDPrev, strings.ToLower(c.sess.Address), c.sess.Worker, c.srv.ChainID)
				}
			}
		}
		if !ok || k != c.sess.Key {
			c.srv.nameInvalid(c, job, "the signature does not verify over the share's message (chain %d, pool id %x)", c.srv.ChainID, c.srv.PoolID[:4])
			return ReasonInvalid
		}
		key, signed = k, true
	}
	var mix, hash [32]byte
	if c.srv.TrustClaimed {
		mb, _ := hex.DecodeString(p.Mix)
		hb, _ := hex.DecodeString(p.Hash)
		if len(mb) != 32 || len(hb) != 32 {
			return ReasonInvalid
		}
		copy(mix[:], mb)
		copy(hash[:], hb)
	} else if av, ok := c.srv.validator.(validate.AnchoredValidator); ok && job.HasAnchor && job.ParamsDigest != "" {
		// a programmed document: the share is checked under the period's anchor the job carries
		mix, hash, err = av.VerifyUnder(job.Algorithm, job.ParamsDigest, job.ProgramAnchor, job.Header, job.Height, nonce)
	} else if dv, ok := c.srv.validator.(validate.DocumentValidator); ok && job.HasCoord && job.ParamsDigest != "" {
		// VectorWork: the share is checked on its job's document (the coordinate's), never on the last prepared
		mix, hash, err = dv.VerifyOn(job.Algorithm, job.ParamsDigest, job.Header, job.Height, nonce)
	} else {
		mix, hash, err = c.srv.validator.Verify(job.Algorithm, job.Header, job.Height, nonce)
	}
	if err != nil {
		c.srv.nameInvalid(c, job, "the reference could not compute the share: %v", err)
		return ReasonInvalid
	}
	if validate.ProofCarried(job.Algorithm) {
		// a proof-carried relation (the graph coordinate): the share is the ticket in the submit's fifth element,
		// verified under the job's header (the member's share header) and the nonce it was found with; the ticket
		// hash stands where the mix stands and is the figure against the share target (WHOLEWORK.md section 10)
		tv, ok := c.srv.validator.(validate.TicketVerifier)
		tb, err := hex.DecodeString(p.Ticket)
		if !ok || err != nil || len(tb) == 0 {
			c.srv.nameInvalid(c, job, "a proof-carried share carries its ticket in the fifth element ({\"ticket\": hex}); none read")
			return ReasonInvalid
		}
		th, err := tv.VerifyTicket(job.ParamsDigest, job.Header, job.Height, nonce, tb)
		if err != nil {
			c.srv.nameInvalid(c, job, "the ticket does not verify under the share header: %v", err)
			return ReasonInvalid
		}
		mix, hash = th, th
		c.ticket = tb
	} else if hex.EncodeToString(mix[:]) != p.Mix || hex.EncodeToString(hash[:]) != p.Hash {
		c.srv.nameInvalid(c, job, "the claimed hash %s.. differs from the reference's %x.. on document %s (coord %d, height %d, nonce %d)", p.Hash[:12], hash[:6], job.ParamsDigest, job.Coord, job.Height, nonce)
		return ReasonInvalid
	}
	if !validate.MeetsTarget(hash, job.ShareTarget) {
		return ReasonLowDifficulty
	}
	if (c.srv.Admission != nil || c.srv.AdmissionFor != nil) && job.Kind != "block" {
		if ok, why := c.srv.admit(c.sess.Address, job); !ok {
			c.srv.mu.Lock()
			if c.srv.admissionRefused == nil {
				c.srv.admissionRefused = map[string]uint64{}
				c.srv.admissionByAddr = map[string]uint64{}
			}
			c.srv.admissionRefused[why]++
			c.srv.admissionByAddr[strings.ToLower(c.sess.Address)]++
			n := c.srv.admissionByAddr[strings.ToLower(c.sess.Address)]
			c.srv.mu.Unlock()
			if n == 1 || n%100 == 0 {
				log.Printf("share refused before acceptance: %s at height %d (%d so far): %s", strings.ToLower(c.sess.Address), job.Height, n, why)
			}
			if c.srv.OnAdmissionRefused != nil {
				c.srv.OnAdmissionRefused(job)
			}
			return ReasonAdmission + ": " + why
		}
	}
	share := Share{
		SessionID: c.sess.ID,
		Key:       key,
		Signed:    signed,
		FeeBps:    c.poolFee.Bps,
		FeeWaived: c.poolFee.Waived,
		Ticket:    c.ticket,
		FeeFor:    c.feeFor,
		Account:   shareAccount(job, c.sess.Address),
		Member:    c.sess.Address,
		Worker:    c.sess.Worker,
		JobID:     job.ID,
		Height:    job.Height,
		Nonce:     nonce,
		Mix:       mix,
		Hash:      hash,
		Weight:    validate.Difficulty(job.ShareTarget),
		At:        now,
	}
	c.ticket = nil
	if validate.MeetsTarget(hash, job.Boundary) {
		share.Block = true
		if len(share.Ticket) > 0 {
			if ts, ok := c.srv.sink.(TicketSink); ok {
				ts.Ticket(job, nonce, share.Ticket)
			}
		}
		_ = c.srv.sink.Block(ctx, job, nonce, mix, hash)
	}
	if job.Kind == "block" {
		// a block job's solution is the pool's block (submitted by the sink above), never a share: no journal, no
		// receipt, no weight on the lane; counted on the session as accepted
		return ""
	}
	c.srv.countAge(c.sess.Address, job)
	c.srv.sink.Share(share)
	if signed && c.srv.Receipts != nil {
		c.sendReceipt(share)
	}
	return ""
}

// ReceiptSigner signs the pool's per-share receipts (a build-tagged
// implementation: Ed25519 over BLAKE3, see signed_igneumhash.go).
type ReceiptSigner interface {
	PublicKey() [32]byte
	// Sign returns the signature over BLAKE3("IgneumPool/receipt/v0" ||
	// u64be(seq) || share hash || key || u64be(weight) || u64be(total) || digest).
	Sign(seq uint64, shareHash, key [32]byte, weight, total uint64, digest [32]byte) (sig [64]byte, msg [32]byte)
	// SignV1 is the receipt with the session's fee terms inside the signed
	// message (the fee-waiver handshake): BLAKE3("IgneumPool/receipt/v1" ||
	// u64be(seq) || share hash || key || u64be(weight) || u64be(total) ||
	// digest || u32be(devFeePercent) || u32be(poolFeeBps)), so a member
	// proves what it was charged from the receipt alone.
	SignV1(seq uint64, shareHash, key [32]byte, weight, total uint64, digest [32]byte, devFeePercent, poolFeeBps uint32) (sig [64]byte, msg [32]byte)
	// SignV2 adds the share's chain identity inside the signed message:
	// ... || u32be(poolFeeBps) || u64be(extraNonce) || u64be(height) || parent,
	// under "IgneumPool/receipt/v2", so a stranger joins the receipt to its
	// carried share from the receipt alone.
	SignV2(seq uint64, shareHash, key [32]byte, weight, total uint64, digest [32]byte, devFeePercent, poolFeeBps uint32, extraNonce, height uint64, parent [32]byte) (sig [64]byte, msg [32]byte)
}

// Receipt is the pool's signed acknowledgement of one accepted share.
type Receipt struct {
	Seq       uint64 `json:"seq"`
	ShareHash string `json:"share_hash"`
	Key       string `json:"key"`
	Weight    uint64 `json:"weight"`
	Total     uint64 `json:"total"`
	Digest    string `json:"digest"`
	// Prev is the member's previous receipt digest the chain continues from
	// (zero at the member's first receipt of the session), carried so the
	// member verifies the link it is given (F-34).
	Prev    string `json:"prev"`
	PoolKey string `json:"pool_key"`
	Sig     string `json:"sig"`
	JobID   string `json:"job_id"`
	Nonce   uint64 `json:"nonce"`
	// Version 1 carries the session's fee terms inside the signed message
	// (the fee-waiver handshake): the miner's declared dev-fee percent and
	// the pool's fee for the session in basis points. Version 0 (absent) is
	// the receipt without them.
	Version       uint32 `json:"version,omitempty"`
	DevFeePercent uint32 `json:"dev_fee_percent"`
	PoolFeeBps    uint32 `json:"pool_fee_bps"`
	// Version 2 adds the share's chain identity inside the signed message
	// (the settlement team's join, 12 October 2026): the session's extranonce
	// (the share header's extraNonce), the job's share height and its parent
	// hash (64 hex), so the receipt joins to the carried entry on the chain
	// without the coordinator.
	ExtraNonce uint64 `json:"extra_nonce,omitempty"`
	Height     uint64 `json:"height,omitempty"`
	Parent     string `json:"parent,omitempty"`
}

// sendReceipt builds and signs the receipt under the server's running
// state (one sequence, one digest chain for the whole pool, a running total
// per member key) and pushes it as a mining.receipt notification.
func (c *conn) sendReceipt(sh Share) {
	c.srv.rmu.Lock()
	if c.srv.totals == nil {
		c.srv.totals = map[[32]byte]uint64{}
	}
	c.srv.seq++
	seq := c.srv.seq
	if c.srv.digests == nil {
		c.srv.digests = map[[32]byte][32]byte{}
	}
	c.srv.totals[sh.Key] += sh.Weight
	total := c.srv.totals[sh.Key]
	shareHash := shareDigest(sh)
	prev := c.srv.digests[sh.Key]
	// version 1 when the pool declares a fee: the session's fee terms inside the signed message, so the member
	// proves from the receipt alone what it was charged (the fee-waiver handshake); version 0 otherwise
	var sig [64]byte
	var msg [32]byte
	var devPct uint32
	if c.devFee != nil {
		devPct = c.devFee.Percent
	}
	version := uint32(0)
	// version 2 when the pool declares a fee: the fee terms and the share's chain identity (the session's extranonce
	// as the share header carries it, the job's share height and parent) inside the signed message
	var extra uint64
	for _, b := range c.sess.ExtraNonce1[:] {
		extra = extra<<8 | uint64(b)
	}
	c.srv.mu.Lock()
	job, hasJob := c.srv.jobs[sh.JobID]
	c.srv.mu.Unlock()
	var parent [32]byte
	if hasJob {
		parent = job.Parent
	}
	if c.srv.FeeBps > 0 {
		version = 2
		sig, msg = c.srv.Receipts.SignV2(seq, shareHash, sh.Key, sh.Weight, total, prev, devPct, c.poolFee.Bps, extra, sh.Height, parent)
	} else {
		sig, msg = c.srv.Receipts.Sign(seq, shareHash, sh.Key, sh.Weight, total, prev)
	}
	c.srv.digests[sh.Key] = msg
	c.srv.rmu.Unlock()
	pk := c.srv.Receipts.PublicKey()
	rc := Receipt{
		Seq: seq, ShareHash: hex.EncodeToString(shareHash[:]), Key: hex.EncodeToString(sh.Key[:]), Weight: sh.Weight, Total: total,
		Digest: hex.EncodeToString(msg[:]), Prev: hex.EncodeToString(prev[:]), PoolKey: hex.EncodeToString(pk[:]), Sig: hex.EncodeToString(sig[:]), JobID: sh.JobID, Nonce: sh.Nonce,
		Version: version, DevFeePercent: devPct, PoolFeeBps: c.poolFee.Bps,
	}
	if version == 2 {
		rc.ExtraNonce, rc.Height, rc.Parent = extra, sh.Height, hex.EncodeToString(parent[:])
	}
	_ = c.write(Notification{Method: "mining.receipt", Params: rc})
}

// shareDigest is the share's identity in a receipt: the job id, the nonce,
// the mix and the hash, hashed with Keccak-256 (the validator's hash, in
// every build) so a member can recompute it from what it sent.
func shareDigest(sh Share) [32]byte {
	var u [8]byte
	binary.BigEndian.PutUint64(u[:], sh.Nonce)
	b := append([]byte(sh.JobID), u[:]...)
	b = append(b, sh.Mix[:]...)
	b = append(b, sh.Hash[:]...)
	return validate.Keccak256(b)
}
