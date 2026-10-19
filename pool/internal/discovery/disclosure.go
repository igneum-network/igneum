// Package discovery serves the pool's disclosure document
// (docs/ecosystem/DESIGN.md, section 2.12) at
// /.well-known/igneum-pool.json. One fee field, a mandatory payout-gas
// field, no income promise: the schema cannot express "free mining".
package discovery

import (
	"encoding/json"
	"errors"
	"net/http"

	"igneum.network/pool/internal/limits"
)

// WellKnownPath is where every pool serves its disclosure.
const WellKnownPath = "/.well-known/igneum-pool.json"

// Payout gas policies.
const (
	PayoutGasOperator = "operator"
	PayoutGasDeducted = "deducted"
)

var ErrDisclosure = errors.New("discovery: disclosure document out of bounds")

// Endpoint is one stratum listener.
type Endpoint struct {
	Host string `json:"host"`
	Port uint16 `json:"port"`
	TLS  bool   `json:"tls"`
}

// Disclosure is the published document the directory reads.
type Disclosure struct {
	Name               string     `json:"name"`
	Operator           string     `json:"operator"`
	Contact            string     `json:"contact"`
	LabsOperated       bool       `json:"labs_operated"`
	Endpoints          []Endpoint `json:"endpoints"`
	FeeBps             uint32     `json:"fee_bps"`
	Scheme             string     `json:"scheme"`
	WindowN            uint32     `json:"window_n"`
	MaturityBlocks     uint64     `json:"maturity_blocks"`
	MinPayoutWei       string     `json:"min_payout_wei"`
	PayoutSchedule     string     `json:"payout_schedule"`
	PayoutGas          string     `json:"payout_gas"`
	SessionIdleSeconds uint32     `json:"session_idle_seconds"`
	ShareRateLimit     uint32     `json:"share_rate_limit"`
	// The bad-share threshold at the pool's edge (DESIGN 2.21, G-33): a
	// session whose refused shares (invalid, low difficulty, duplicate, a
	// bad nonce) reach BadShareLimit within BadShareWindowSeconds is closed
	// with the reason on its last reply, and its address is refused at
	// authorize for BadShareCoolingSeconds; zero limit means no threshold.
	// Declared here so a miner reads the rule before connecting.
	BadShareLimit         uint32 `json:"bad_share_limit,omitempty"`
	BadShareWindowSeconds uint32 `json:"bad_share_window_s,omitempty"`
	BadShareCoolingSeconds uint32 `json:"bad_share_cooling_s,omitempty"`
	// The useful pace on the share lane, so a miner reads it from the pool
	// (the public pool's first outside session, 12 October 2026: a miner
	// submitting far above the node's cap is throttled by the pool's rate
	// and refused by the node above the cap): the node's per-address cap of
	// accepted shares per height and the chain's block clock in seconds; a
	// session gains nothing above cap over clock shares a second.
	ShareCapPerHeight uint32 `json:"share_cap_per_height,omitempty"`
	BlockSeconds      uint32 `json:"block_seconds,omitempty"`
	// Addresses is the pool's public footprint on the chain (the observatory's
	// measure of pool concentration, 12 October 2026): the payout address of
	// the pool's member node (the sealer's own entry in every block it seals)
	// and the bond keys and payout addresses of every sealer the pool
	// forwards shares to, so an observer reads the pool's share of the
	// chain's blocks and lists honestly; the facts a block already shows.
	Addresses *PoolAddresses `json:"addresses,omitempty"`
	// ChainID is the chain the pool mines (eth_chainId as the pool bound it at start); a miner reads it at dial and
	// signs its shares over it (the old public test network (chain 7320) switch of 22:58Z, 12 October 2026).
	ChainID            uint64     `json:"chain_id,omitempty"`
	Regions            []string   `json:"regions"`
	RecordsURL         string     `json:"records_url"`
	SharesURL          string     `json:"shares_url"`
	DeclaredHashrate   string     `json:"declared_hashrate,omitempty"`
	// Earnings inputs for a pool miner without a node (product engineer
	// A's Earnings page): the miner's subsidy per block at the pool's
	// current height and the pool's mean priority fees per block over
	// EarningsWindowBlocks, both wei as decimal strings; refreshed by the
	// service from igneum_blockReward, absent when the node lacks it.
	SubsidyWei           string `json:"subsidy_wei,omitempty"`
	PriorityFeeMeanWei   string `json:"priority_fee_mean_wei,omitempty"`
	EarningsWindowBlocks uint32 `json:"earnings_window_blocks,omitempty"`
	EarningsHeight       uint64 `json:"earnings_height,omitempty"`
}

// PoolAddresses is the pool's self-declared footprint on the chain.
type PoolAddresses struct {
	// MemberPayout is the payout address of the pool's member node (its own
	// entry in every block it seals).
	MemberPayout string `json:"member_payout"`
	// Sealers lists every sealer the pool forwards shares to: the RPC the
	// pool names, the sealer's bond key and its payout address.
	Sealers []PoolSealer `json:"sealers"`
}

// PoolSealer is one sealer the pool forwards shares to.
type PoolSealer struct {
	RPC     string `json:"rpc"`
	BondKey string `json:"bond_key"`
	Payout  string `json:"payout"`
}

// Validate refuses a document that could conceal a cost or a condition.
func (d Disclosure) Validate() error {
	switch {
	case d.Name == "", d.Operator == "", d.Contact == "":
		return ErrDisclosure
	case len(d.Endpoints) == 0:
		return ErrDisclosure
	case d.FeeBps > limits.MaxFeeBps:
		return ErrDisclosure
	case d.BadShareLimit > 0 && (d.BadShareWindowSeconds == 0 || d.BadShareCoolingSeconds == 0):
		// a threshold without its window or its cooling period would be a condition the miner cannot read
		return ErrDisclosure
	case d.Scheme == "":
		return ErrDisclosure
	case d.PayoutGas != PayoutGasOperator && d.PayoutGas != PayoutGasDeducted:
		return ErrDisclosure
	case d.MinPayoutWei == "", d.PayoutSchedule == "":
		return ErrDisclosure
	case d.RecordsURL == "", d.SharesURL == "":
		return ErrDisclosure
	}
	for _, e := range d.Endpoints {
		if e.Host == "" || e.Port == 0 {
			return ErrDisclosure
		}
	}
	return nil
}

// Handler serves the validated document as JSON.
func Handler(d Disclosure) (http.Handler, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	body, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.Write(body) //nolint:errcheck // nothing to do on a failed write
	}), nil
}
