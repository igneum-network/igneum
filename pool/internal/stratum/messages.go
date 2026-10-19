package stratum

import (
	"encoding/json"
	"strconv"
	"strings"
)

// IS/1 message set (docs/ecosystem/DESIGN.md, section 2.3). The job and
// submission fields mirror GPU engineer A's worker protocol version 0:
// 32-byte values are lowercase hex without prefix, nonces are decimal
// strings, the epoch is derived from the height and never declared.

// Method names.
const (
	MethodSubscribe = "mining.subscribe"
	MethodAuthorize = "mining.authorize"
	MethodSubmit    = "mining.submit"
	MethodNotify    = "mining.notify"
	// MethodReconnect is the pool's notice before a generation stops (the handover form, the project's order of 05:5x UK,
	// 14 October 2026): [host, port, wait_seconds]; a miner re-dials the same endpoint at once
	MethodReconnect = "client.reconnect"
	MethodSetTarget = "mining.set_target"
)

// SubscribeResult answers mining.subscribe: the fresh session and the
// nonce range this session owns and no other.
type SubscribeResult struct {
	SessionID string `json:"session_id"`
	// KeyedTo is the member address a fee session is keyed to by its for block (absent otherwise).
	KeyedTo string `json:"keyed_to,omitempty"`
	// ForwardAs is the pool key every session's shares are forwarded under on the pooled form (the share header
	// built for it, the chain paying it, the pool paying the member from it: /payouts); absent on the direct form.
	ForwardAs   string `json:"forward_as,omitempty"`
	ExtraNonce1 string `json:"extranonce1"`
	NonceStart  string `json:"nonce_start"`
	NonceCount  string `json:"nonce_count"`
	Protocol    string `json:"protocol"`
	// Params and ParamsDigest: the current job's parameter document and
	// digest (see NotifyParams), so a self-verifying worker has the document
	// before its first job.
	Params       json.RawMessage `json:"params,omitempty"`
	ParamsDigest string          `json:"params_digest,omitempty"`
	// SignedShares is true on a non-custodial listener: the miner binds
	// its Ed25519 key at authorize and signs every share; PoolID is the
	// 32-byte pool identity the share's message covers.
	SignedShares bool   `json:"signed_shares,omitempty"`
	PoolID       string `json:"pool_id,omitempty"`
	// PoolList is the pool's chain-native payout list, its one statement
	// ([{"address", "weight"}]), on the member-node form: the member's own
	// client checks that it names the member and sets it on the member's
	// own node itself, over the node's local IPC, so the coordinator never
	// needs the member node's write namespaces (GUARD-C-CLOSURE 3.2, the
	// list refused before signing; the C3 row of 11 October 2026).
	PoolList json.RawMessage `json:"pool_list,omitempty"`
	// ReceiptKey is the pool's Ed25519 receipt public key (64 hex) when the
	// pool signs a mining.receipt per accepted signed share.
	ReceiptKey string `json:"receipt_key,omitempty"`
	// PoolFee is the pool's fee for THIS session (the fee-waiver handshake,
	// ED-08 and ED-17, the founder's overnight order of 11 October 2026):
	// the company's pool declares 100 bps of the miner's side, the cap
	// itself, and waives it for a session whose miner reports its dev fee
	// on under a signature the pool verified, so every company-controlled
	// charge together stays at most 1 percent and never stacks; any other
	// pool's fee is its own declared charge. Absent on a pool with no
	// declared fee.
	PoolFee *PoolFee `json:"pool_fee,omitempty"`
}

// DevFee is the miner's dev-fee state as the subscribe carries it (product
// A's Ember form, 11 October 2026): the percent (1 on, 0 off), the dev-fee
// address, the manifest's sha256, the worker name the session will
// authorize under, and an Ed25519 signature by the session key over the
// bytes "igneum-dev-fee\n<percent>\n<address>\n<manifest_sha256>\n<worker>\n"
// (no pre-hash). The session key must be the key the session later binds at
// authorize, or the waiver is void.
type DevFee struct {
	Percent        uint32 `json:"percent"`
	Address        string `json:"address"`
	ManifestSHA256 string `json:"manifest_sha256"`
	Worker         string `json:"worker"`
	Signature      string `json:"signature"`
	SessionKey     string `json:"session_key"`
}

// PoolFee is the pool's answer: the fee this session pays in basis points
// of the miner's side, whether the declared fee is waived, and why.
type PoolFee struct {
	Bps    uint32 `json:"bps"`
	Waived bool   `json:"waived"`
	Reason string `json:"reason"`
}

// DevFeeFromParams finds the dev_fee object in a subscribe's params: as a
// field of an object, or as a field of any object element of the params
// array ([{"dev_fee": {...}}] or ["agent", {"dev_fee": {...}}]). Nil when
// absent or unreadable.
func DevFeeFromParams(raw json.RawMessage) *DevFee {
	type holder struct {
		DevFee *DevFee `json:"dev_fee"`
	}
	var h holder
	if json.Unmarshal(raw, &h) == nil && h.DevFee != nil {
		return h.DevFee
	}
	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) != nil {
		return nil
	}
	for _, el := range arr {
		var eh holder
		if json.Unmarshal(el, &eh) == nil && eh.DevFee != nil {
			return eh.DevFee
		}
	}
	return nil
}

// DevFeeMessage is the bytes the session key signs over the dev-fee state.
func DevFeeMessage(d DevFee) []byte {
	return []byte("igneum-dev-fee\n" + strconvUint(d.Percent) + "\n" + d.Address + "\n" + d.ManifestSHA256 + "\n" + d.Worker + "\n")
}

func strconvUint(v uint32) string {
	if v == 0 {
		return "0"
	}
	var b [10]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}

// NotifyParams is the job a session mines.
type NotifyParams struct {
	JobID            string `json:"job_id"`
	Height           uint64 `json:"height"`
	Algorithm        string `json:"algorithm"`
	AlgorithmVersion uint32 `json:"algorithm_version"`
	Epoch            uint64 `json:"epoch"`
	Parent           string `json:"parent"`
	Header           string `json:"header"`
	Boundary         string `json:"boundary"`
	ShareTarget      string `json:"share_target"`
	Clean            bool   `json:"clean"`
	// ChainID is the chain the job is on (the old public test network (chain 7320) switch of 22:58Z, 12 October 2026: an installed app signed
	// its shares over the chain it installed on); a miner signs its shares over this, and refuses the job when it is
	// not the chain it is set up for.
	ChainID uint64 `json:"chain_id,omitempty"`
	// VectorWork (the product team's router form, 13 October 2026): the job's coordinate (absent on a one-form chain), the
	// coordinate's difficulty as the node states it and its reward per block in wei (decimal strings); a share
	// names its job id, so the coordinate travels with it; a worker that lacks the coordinate's relation refuses
	// the job by the algorithm it names.
	// (clean is false on a coordinate job: it replaces the session's job of the same coord alone)
	Coord      *uint8 `json:"coord,omitempty"`
	Difficulty string `json:"difficulty,omitempty"`
	RewardWei  string `json:"reward_wei,omitempty"`
	// LastHeight is the height of the coordinate's last block on the chain (the product team's router restores an emptied
	// coordinate when none comes for more than twelve blocks); absent when the node does not serve it.
	LastHeight uint64 `json:"last_height,omitempty"`
	CoordName  string `json:"coord_name,omitempty"`
	// PaidCoords is the chain's pay list at the job's height: the coordinates it pays, by index (0.1.76, 16 October
	// 2026). A coordinate absent from it is retired or unpaid: a router drops it and never restores it; a job's
	// silence on a coordinate while jobs come on others says the same. Absent on a one-form chain.
	// (a []uint16, never a []uint8: encoding/json writes a byte slice as base64, not as a list of numbers.)
	PaidCoords []uint16 `json:"paid_coords,omitempty"`
	// LaneWorth is the coordinate's marginal pay per unit of share weight (0.1.86, F-496 under the per-coordinate
	// payout of 0.1.85, 17 October 2026): the coordinate's income over the pool's carried share weight on it over the
	// last closed pay window, in wei per unit of share weight, a decimal string in scientific form (printf %.4e),
	// refreshed at each window's close. A router ranks a card's lanes by its share-weight rate on each times
	// lane_worth. Absent on a one-form chain, on a coordinate the pool does not serve, before the pool's first window
	// closes, and on a coordinate the last window carried no work on.
	LaneWorth string `json:"lane_worth,omitempty"`
	// LaneWorkers (0.1.96) is the count of the pool's workers with an accepted share on this coordinate in the last
	// 64 blocks, always present on a coordinate job: 0 names a paid lane without a worker (on a minimum-rule chain
	// the pool earns nothing while any paid lane has none; a card keeps such a lane covered before moving for reward)
	LaneWorkers *int `json:"lane_workers,omitempty"`
	// Kind is "block" on a block job (a block session, its worker name ending ".block", mines the template's header
	// at the block target; the pool submits the solution as the block); absent on a share job.
	Kind string `json:"kind,omitempty"`
	// ProgramAnchor is the period's programme anchor (0x + 64 hex) the share is computed under on a programmed
	// document (versions 9 to 14: the chain's block hash at period x period_blocks - D), with AnchorHeight the
	// height it was taken at; absent on a document without a programme. A worker hands it to its hash as the
	// anchor (the r0d_anchor knob); the document's own program_anchor is never the chain's.
	ProgramAnchor string `json:"program_anchor,omitempty"`
	// HeaderFor is the address the job's share header was built for when it is not the session's own: the pool
	// key under the pooled form (the project's order of 14:3x UK, 15 October 2026), so an engine that rebuilds the share
	// header to check the job does so for this address, never its own; absent on the direct form.
	HeaderFor    string `json:"header_for,omitempty"`
	AnchorHeight uint64 `json:"anchor_height,omitempty"`
	// SubsidyWei is the miner's subsidy per block at this height and
	// PriorityFeeMeanWei the pool's mean priority fees per block over its
	// recent window, both wei as decimal strings (product engineer A's
	// Earnings page reads them where a pool miner has no node). Empty
	// when the pool's node does not serve igneum_blockReward.
	SubsidyWei         string `json:"subsidy_wei,omitempty"`
	PriorityFeeMeanWei string `json:"priority_fee_mean_wei,omitempty"`
	// Params is the algorithm's parameter document as the node serves it on
	// igneum_getWork (verbatim JSON object) and ParamsDigest the node's
	// paramsDigest hex; a worker that verifies its own shares (Ember) reads
	// both from the subscribe result and from every notify, and a change of
	// digest between jobs is the announced-change path. Absent on a chain
	// whose node serves no document (eth_getWork).
	Params       json.RawMessage `json:"params,omitempty"`
	ParamsDigest string          `json:"params_digest,omitempty"`
	// Payouts is the chain-native payout list the template names, verbatim
	// from the node's igneum_getWork, so a member checks that the list
	// names it before it works the template; Member is the address whose
	// own node built the template on the member-node form (empty on the
	// single-node form).
	Payouts json.RawMessage `json:"payouts,omitempty"`
	Member  string          `json:"member,omitempty"`
	// ShareStep is the session's declared step on the share lane (PA-53's
	// step form, 12 October 2026): the job's share_target is the node's over
	// 2^share_step and an accepted share weighs 2^share_step on the chain;
	// absent or 0 is the count form. The miner mines the job's share_target
	// as always; the step tells it the weight.
	ShareStep uint8 `json:"share_step,omitempty"`
}

// SubmitParams is one share: [job_id, nonce, mix, hash] as the worker
// protocol's share line carries them.
type SubmitParams struct {
	JobID string
	Nonce string
	Mix   string
	Hash  string
	// The non-custodial form's fifth element: the miner's Ed25519 public
	// key (64 hex) and its signature (128 hex) over the share's message
	// (docs/ecosystem/POOL-NONCUSTODIAL.md section 2). Empty on the
	// four-element form.
	Key string
	Sig string
	// Ticket is a proof-carried share's proof (the graph coordinate: the 168-byte cycle ticket, hex) in the fifth
	// element beside the key and the signature (WHOLEWORK.md section 10; the project's same-day order of 13:0x UK,
	// 14 October 2026); mix and hash then carry the ticket hash.
	Ticket string
}

// UnmarshalJSON accepts the positional array form: four strings, or four
// strings and an object {"key", "sig", "ticket"}.
func (p *SubmitParams) UnmarshalJSON(b []byte) error {
	var arr []json.RawMessage
	if err := json.Unmarshal(b, &arr); err != nil {
		return err
	}
	if len(arr) != 4 && len(arr) != 5 {
		return ErrBadFrame
	}
	strs := make([]string, 4)
	for i := 0; i < 4; i++ {
		if err := json.Unmarshal(arr[i], &strs[i]); err != nil {
			return ErrBadFrame
		}
	}
	p.JobID, p.Nonce, p.Mix, p.Hash = strs[0], strs[1], strs[2], strs[3]
	if len(arr) == 5 {
		var extra struct {
			Key    string `json:"key"`
			Sig    string `json:"sig"`
			Ticket string `json:"ticket"`
		}
		if err := json.Unmarshal(arr[4], &extra); err != nil {
			return ErrBadFrame
		}
		p.Key, p.Sig, p.Ticket = extra.Key, extra.Sig, extra.Ticket
	}
	return nil
}

// Notification is a server-push frame (no id).
type Notification struct {
	Method string      `json:"method"`
	Params interface{} `json:"params"`
}

// RateFromParams finds a declared hash rate in a subscribe's params (the product team's ask of 17:1x UK, 14 October 2026:
// a fresh session of two 4090s held back 99 percent of its results while the share target ramped from step 0):
// {"hashrate": <hashes a second>} as an object or inside the positional array. Zero when none.
func RateFromParams(raw json.RawMessage) float64 {
	type holder struct {
		Rate float64 `json:"hashrate"`
	}
	var h holder
	if json.Unmarshal(raw, &h) == nil && h.Rate > 0 {
		return h.Rate
	}
	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) != nil {
		return 0
	}
	for _, el := range arr {
		var eh holder
		if json.Unmarshal(el, &eh) == nil && eh.Rate > 0 {
			return eh.Rate
		}
	}
	return 0
}

// ForBlock keys a fee session to the member it mines the fee for (the product team's form of 18:3x UK, 14 October 2026,
// the founder's "Ember on our pool: 1 percent total"): in the fee session's subscribe params, beside dev_fee,
// {"for": {address, session_key (the member's session public key, hex), fee_session_key (this session's public
// key, hex), percent, window_shares, signature}}, the signature Ed25519 by the MEMBER's session key over the bytes
// "igneum-fee-for\n<address>\n<fee session public key hex>\n<percent>\n<window_shares>\n", so a third client
// holding neither key can claim nothing.
type ForBlock struct {
	Address       string `json:"address"`
	SessionKey    string `json:"session_key"`
	FeeSessionKey string `json:"fee_session_key"`
	Percent       uint32 `json:"percent"`
	WindowShares  uint64 `json:"window_shares"`
	Signature     string `json:"signature"`
}

// Message is the signed bytes of the block.
func (f ForBlock) Message() []byte {
	return []byte("igneum-fee-for\n" + strings.ToLower(f.Address) + "\n" + strings.ToLower(f.FeeSessionKey) + "\n" + strconvUint(f.Percent) + "\n" + strconv.FormatUint(f.WindowShares, 10) + "\n")
}

// ForFromParams finds the for block in a subscribe's params (an object, or inside the positional array).
func ForFromParams(raw json.RawMessage) *ForBlock {
	type holder struct {
		For *ForBlock `json:"for"`
	}
	var h holder
	if json.Unmarshal(raw, &h) == nil && h.For != nil {
		return h.For
	}
	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) != nil {
		return nil
	}
	for _, el := range arr {
		var eh holder
		if json.Unmarshal(el, &eh) == nil && eh.For != nil {
			return eh.For
		}
	}
	return nil
}
