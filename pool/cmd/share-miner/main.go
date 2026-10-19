//go:build igneumhash

// Command share-miner is the share lane's CPU worker (LAUNCH-GUARDS 2.9, the
// solo-mining row; the project's order of 11 October 2026 for devnet7): a supported
// GPU submits shares directly to a node, no pool and no bond, and accepted
// work receives its pro-rata portion of the block payout under the one
// network-wide share target. This reference worker does on the CPU what a
// GPU worker does, against the node's shape as the protocol team stated it (its
// 12:3x UK line; the node side on protocol-a/shares):
//
//   - igneum_getWork carries shareTarget (a decimal big integer like target:
//     pow.Target(max(1, D / sharesPerBlock)), from chain work), sharesPerBlock
//     (160 accepted shares per 20 s block, the outside review's number of 12:4x UK;
//     256 the consensus ceiling with the deterministic carry-forward; the
//     worker reads whatever the node serves), shareHeight (the template's
//     height) and shareParent (the template's parent hash), and the share's
//     document in shareParams / shareParamsDigest (the unkeyed twin at the
//     height, so the worker never guesses);
//   - the worker builds the share header itself, H_s = KECCAK(RLP(shareParent,
//     shareHeight, address, extraNonce)) with the payout address (20 bytes)
//     and an extraNonce of its own choosing (a u64 job id), and searches the
//     nonce on the twin until the result is at or under shareTarget;
//   - it submits igneum_submitShare(address, extraNonce, nonce, mixHash,
//     height) (the hex forms the protocol team named) and the node answers
//     {"accepted": bool, "reason": string}: a replay (the same address,
//     extraNonce, nonce, parent) and a stale share (its parent not the current
//     parent or one of the seven before it) are refused by name; a share
//     whose result also meets the block target is still only a share (the
//     seal-only rule: a share never becomes a block).
//
// The sealers (the finding of 11 October 2026, the economics team's G-24): a
// share lives only in the pool of the node it was submitted to and pays only
// in that sealer's blocks, so until share gossip (PA-46) carries shares
// between sealers a solo worker names every sealer it can reach (-nodes).
// Two forms, both measured on devnet7 the same afternoon:
//
//   - per-sealer work (the default): every sealer's own template is polled
//     and H_s built on it, the hashing time split equally between the
//     sealers, and a share submitted only to the sealer whose template it was
//     found on. Each sealer accepts every share it is sent (its own parent);
//     the price is the split: a worker naming N sealers puts 1/N of its rate
//     in each sealer's pool;
//   - the same share to every sealer (-same-share): one header on the first
//     node's template, the share submitted to all. A sealer on another fork
//     refuses it by name ("the share's parent is not the head or one of the
//     seven before it"): on devnet7's two clocked sealers that was three
//     shares in four at the second sealer in the run's first minute (run 4,
//     15:4x UK: 360 of 477, B7 on another head for that minute) and none in
//     the minutes after; the split form never depends on the heads agreeing.
//
// Unsigned on devnet7 (the address alone). Every accepted and refused share is
// counted by reason, per sealer, and the rate per minute printed, the
// share-rate row for the floor card (gate L7, row 6: payout-frequency rows
// for the supported card floor). Nothing here is a measurement of energy.
package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"

	"igneum.network/pool/internal/validate"
)

// work is igneum_getWork's share fields (the block fields beside them are
// the sealer's and are read for the record only).
type work struct {
	HeaderHash        string          `json:"headerHash"`
	Height            uint64          `json:"height"`
	Target            json.RawMessage `json:"target"`
	ShareTarget       json.RawMessage `json:"shareTarget"`
	SharesPerBlock    uint64          `json:"sharesPerBlock"`
	ShareHeight       uint64          `json:"shareHeight"`
	ShareParent       string          `json:"shareParent"`
	Params            json.RawMessage `json:"params"`
	ParamsDigest      string          `json:"paramsDigest"`
	ShareParams       json.RawMessage `json:"shareParams"`
	ShareParamsDigest string          `json:"shareParamsDigest"`
	Payouts           json.RawMessage `json:"payouts"`
	Carry             json.RawMessage `json:"carry"`
}

type rpcClient struct {
	url  string
	http *http.Client
	id   uint64
}

func (c *rpcClient) call(ctx context.Context, method string, params interface{}, out interface{}) error {
	c.id++
	body, _ := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": c.id, "method": method, "params": params})
	req, err := http.NewRequestWithContext(ctx, "POST", c.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("%s: %w (%s)", method, err, string(raw[:min(len(raw), 120)]))
	}
	if env.Error != nil {
		return fmt.Errorf("%s: %d %s", method, env.Error.Code, env.Error.Message)
	}
	if out != nil && len(env.Result) > 0 {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

// bigFromRaw reads a decimal or 0x-hex JSON number or string.
func bigFromRaw(r json.RawMessage) (*big.Int, error) {
	s := strings.Trim(strings.TrimSpace(string(r)), "\"")
	if s == "" || s == "null" {
		return nil, errors.New("absent")
	}
	v := new(big.Int)
	if strings.HasPrefix(s, "0x") {
		if _, ok := v.SetString(s[2:], 16); !ok {
			return nil, fmt.Errorf("not hex: %s", s)
		}
		return v, nil
	}
	if _, ok := v.SetString(s, 10); !ok {
		return nil, fmt.Errorf("not a number: %s", s)
	}
	return v, nil
}

func target32(v *big.Int) [32]byte {
	var t [32]byte
	b := v.Bytes()
	if len(b) > 32 {
		b = b[len(b)-32:]
	}
	copy(t[32-len(b):], b)
	return t
}

// shareHeader is H_s = KECCAK(RLP(shareParent, shareHeight, address, extraNonce)).
func shareHeader(parent common.Hash, height uint64, address common.Address, extraNonce uint64) ([32]byte, error) {
	enc, err := rlp.EncodeToBytes([]interface{}{parent, height, address, extraNonce})
	if err != nil {
		return [32]byte{}, err
	}
	var h [32]byte
	copy(h[:], crypto.Keccak256(enc))
	return h, nil
}

// sealer is one node the worker serves: its template, the header built on it
// and its counts. The first sealer is -node; the rest are -nodes.
type sealer struct {
	rpc        *rpcClient
	tag        string // "" for the first node, "node i: " for the others (the record's reason keys)
	cur        *work
	header     [32]byte
	target     [32]byte
	params     json.RawMessage
	digest     string // the share document this sealer serves
	shareTgt   string
	accepted   uint64
	refused    uint64
	hashes     uint64
	lastHeight uint64
}

// poll reads the sealer's template and rebuilds the header when the template
// moved. It returns false when the sealer serves no share lane.
func (s *sealer) poll(ctx context.Context, addr common.Address, extraNonce uint64) bool {
	var w work
	if err := s.rpc.call(ctx, "igneum_getWork", []interface{}{}, &w); err != nil {
		log.Printf("%sgetwork: %v", s.tag, err)
		return s.cur != nil
	}
	if len(w.ShareTarget) == 0 {
		log.Printf("%sgetwork at height %d carries no shareTarget: the node is not the share lane's (protocol-a/shares); waiting", s.tag, w.Height)
		s.cur = nil
		return false
	}
	st, err := bigFromRaw(w.ShareTarget)
	if err != nil {
		log.Printf("%sshareTarget: %v", s.tag, err)
		return s.cur != nil
	}
	if w.ShareHeight == 0 {
		w.ShareHeight = w.Height
	}
	params, digest := w.ShareParams, w.ShareParamsDigest
	if len(params) == 0 {
		params, digest = w.Params, w.ParamsDigest
	}
	if s.cur != nil && s.cur.ShareParent == w.ShareParent && s.cur.ShareHeight == w.ShareHeight && digest == s.digest {
		return true
	}
	h, err := shareHeader(common.HexToHash(w.ShareParent), w.ShareHeight, addr, extraNonce)
	if err != nil {
		log.Printf("%sshare header: %v", s.tag, err)
		return s.cur != nil
	}
	s.header, s.target, s.cur, s.params, s.digest, s.shareTgt = h, target32(st), &w, params, digest, st.String()
	log.Printf("%stemplate: share height %d parent %s shareTarget %s sharesPerBlock %d; H_s %x", s.tag, w.ShareHeight, w.ShareParent[:12], st.String()[:min(len(st.String()), 20)], w.SharesPerBlock, h[:8])
	return true
}

func main() {
	var (
		nodeURL   = flag.String("node", "http://127.0.0.1:8545/", "the node's HTTP RPC (igneum_getWork, igneum_submitShare)")
		nodeList  = flag.String("nodes", "", "every OTHER sealer's RPC the worker also serves, comma-separated (a share lives only in the pool of the node it was sent to and pays only when that sealer wins, so a solo worker names every sealer it can reach until share gossip carries them); each sealer's own template is mined in turn and a share goes to the sealer whose template it was found on; the answers counted per node")
		sameShare = flag.Bool("same-share", false, "the first form of the fan-out: one header on -node's template and every share submitted to all the sealers named (a sealer on another fork refuses it by name)")
		address   = flag.String("address", "", "the payout address (0x + 40 hex; required): the share's identity and where the pro-rata payout goes")
		extra     = flag.Uint64("extra-nonce", 0, "the worker's extraNonce (0 = derived from the start time)")
		maxTime   = flag.Duration("for", 0, "stop after this long (0 = until interrupted)")
		poll      = flag.Duration("poll", 2*time.Second, "work poll interval")
		pace      = flag.Int("pace", 0, "hashes per second cap (0 = unbounded; the CPU reference is slow enough)")
		record    = flag.String("record", "", "append one JSON line per minute (accepted, refused by reason, hashes, the share target, the per-sealer counts) to this file: the share-rate row's record")
		sharesOut = flag.String("shares-out", "", "append one JSON line per submitted share (address, extraNonce, nonce, mix, height, parent, the sealer, accepted, reason) to this file: the C3 cell's replay material (a coordinator's acts replay a real share with another parent or another address)")
		label     = flag.String("label", "", "a label for the record (the card, the host)")
		dryShare  = flag.Bool("show-header", false, "print H_s for the current template and exit (a shape check against the node)")
	)
	flag.Parse()
	if *address == "" || !common.IsHexAddress(*address) {
		fmt.Fprintln(os.Stderr, "share-miner: -address (0x + 40 hex) is required")
		os.Exit(2)
	}
	addr := common.HexToAddress(*address)
	extraNonce := *extra
	if extraNonce == 0 {
		extraNonce = uint64(time.Now().UnixNano())
	}
	v, ok := validate.Lookup("igneumhash")
	if !ok {
		log.Fatal("the igneumhash validator is not in this build")
	}
	pr, _ := v.(validate.Preparer)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *maxTime > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *maxTime)
		defer cancel()
	}
	sealers := []*sealer{{rpc: &rpcClient{url: *nodeURL, http: &http.Client{Timeout: 15 * time.Second}}}}
	if *nodeList != "" {
		for _, u := range strings.Split(*nodeList, ",") {
			if u = strings.TrimSpace(u); u != "" && u != *nodeURL {
				sealers = append(sealers, &sealer{rpc: &rpcClient{url: u, http: &http.Client{Timeout: 15 * time.Second}}, tag: fmt.Sprintf("node %d: ", len(sealers))})
			}
		}
	}
	form := "per-sealer work"
	if *sameShare {
		form = "the same share to every sealer"
	}
	var (
		accepted, refused, hashes, submitted uint64
		reasons                               = map[string]uint64{}
	)
	var rec, sharesRec *os.File
	if *record != "" {
		f, err := os.OpenFile(*record, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			log.Fatal(err)
		}
		rec = f
		defer f.Close()
	}
	if *sharesOut != "" {
		f, err := os.OpenFile(*sharesOut, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			log.Fatal(err)
		}
		sharesRec = f
		defer f.Close()
	}
	log.Printf("share-miner: node %s (+%d sealer(s), %s), address %s, extraNonce %d, reference available %v", *nodeURL, len(sealers)-1, form, addr.Hex(), extraNonce, validate.ReferenceAvailable())
	ticker := time.NewTicker(*poll)
	defer ticker.Stop()
	report := time.NewTicker(time.Minute)
	defer report.Stop()
	var nonce uint64 = extraNonce // the nonce walk starts somewhere the worker alone chooses
	prepared := ""                // the share document the validator holds
	var minuteAccepted, minuteRefused, minuteHashes uint64
	// prepare puts the sealer's share document in the validator when it differs from the one held.
	prepare := func(s *sealer) bool {
		if pr == nil || len(s.params) == 0 || s.digest == prepared {
			return true
		}
		if err := pr.Prepare("igneumhash", s.params, s.digest, s.cur.ShareHeight); err != nil {
			log.Printf("%sprepare the share's document (%s): %v", s.tag, s.digest, err)
			return false
		}
		prepared = s.digest
		return true
	}
	// submit sends one share to the sealers named (the sealer it was found on, or all of them under -same-share).
	submit := func(found *sealer, height uint64, mix [32]byte, hash [32]byte) {
		submitted++
		// the sixth parameter, the parent the share was mined on (share gossip, main 39d13865): a share whose parent
		// is not the node's canonical block at its height is refused as "stale parent" before any work check
		params := []interface{}{addr.Hex(), fmt.Sprintf("0x%x", extraNonce), fmt.Sprintf("0x%x", nonce), "0x" + hex.EncodeToString(mix[:]), fmt.Sprintf("0x%x", height), found.cur.ShareParent}
		targets := []*sealer{found}
		if *sameShare {
			targets = sealers
		}
		acceptedBy, firstReason := 0, ""
		for _, t := range targets {
			var ans struct {
				Accepted bool   `json:"accepted"`
				Reason   string `json:"reason"`
			}
			err := t.rpc.call(ctx, "igneum_submitShare", params, &ans)
			if sharesRec != nil {
				line := map[string]interface{}{"utc": time.Now().UTC().Format(time.RFC3339Nano), "address": addr.Hex(), "extraNonce": fmt.Sprintf("0x%x", extraNonce), "nonce": fmt.Sprintf("0x%x", nonce),
					"mix": "0x" + hex.EncodeToString(mix[:]), "height": fmt.Sprintf("0x%x", height), "parent": found.cur.ShareParent, "sealer": t.rpc.url, "accepted": ans.Accepted, "reason": ans.Reason}
				if err != nil {
					line["rpc_error"] = trim(err.Error())
				}
				b, _ := json.Marshal(line)
				sharesRec.Write(append(b, '\n'))
			}
			if err != nil {
				reasons[t.tag+"rpc: "+trim(err.Error())]++
				t.refused++
				continue
			}
			if ans.Accepted {
				acceptedBy++
				t.accepted++
				if t.tag != "" {
					reasons[t.tag+"accepted"]++
				}
			} else {
				reasons[t.tag+trim(ans.Reason)]++
				t.refused++
				if firstReason == "" {
					firstReason = ans.Reason
				}
			}
		}
		if acceptedBy > 0 {
			accepted++
			minuteAccepted++
			log.Printf("%sshare ACCEPTED at height %d by %d of %d sealer(s) (nonce %d, result %x...)", found.tag, height, acceptedBy, len(targets), nonce, hash[:6])
		} else {
			refused++
			minuteRefused++
			log.Printf("%sshare refused at height %d by every sealer sent: %s", found.tag, height, firstReason)
		}
	}
	for {
		select {
		case <-ctx.Done():
			log.Printf("stopping: accepted %d refused %d (%v) submitted %d hashes %d", accepted, refused, reasons, submitted, hashes)
			return
		case <-report.C:
			line := map[string]interface{}{"utc": time.Now().UTC().Format(time.RFC3339), "label": *label, "address": addr.Hex(), "form": form, "accepted": accepted, "refused": refused, "reasons": reasons,
				"hashes": hashes, "minute": map[string]uint64{"accepted": minuteAccepted, "refused": minuteRefused, "hashes": minuteHashes}, "share_target": "absent", "height": 0}
			perSealer := make([]map[string]interface{}, 0, len(sealers))
			for i, s := range sealers {
				row := map[string]interface{}{"node": s.rpc.url, "accepted": s.accepted, "refused": s.refused, "hashes": s.hashes, "height": s.lastHeight, "share_target": s.shareTgt}
				if s.cur != nil {
					row["shares_per_block"] = s.cur.SharesPerBlock
					if i == 0 {
						line["height"] = s.cur.ShareHeight
						line["shares_per_block"] = s.cur.SharesPerBlock
						line["share_target"] = s.shareTgt
					}
				}
				perSealer = append(perSealer, row)
			}
			line["sealers"] = perSealer
			if rec != nil {
				b, _ := json.Marshal(line)
				rec.Write(append(b, '\n'))
			}
			log.Printf("minute: accepted %d refused %d hashes %d (total accepted %d refused %d hashes %d; %.1f kH/s)", minuteAccepted, minuteRefused, minuteHashes, accepted, refused, hashes, float64(minuteHashes)/60000.0)
			minuteAccepted, minuteRefused, minuteHashes = 0, 0, 0
		case <-ticker.C:
			live := make([]*sealer, 0, len(sealers))
			for i, s := range sealers {
				if i > 0 && *sameShare {
					break // one template, the first node's
				}
				if s.poll(ctx, addr, extraNonce) {
					live = append(live, s)
				}
			}
			if *dryShare {
				if len(live) > 0 {
					fmt.Printf("%x\n", live[0].header)
					return
				}
				continue
			}
			if len(live) == 0 {
				continue
			}
			// hash until the next poll tick, the time split equally between the sealers with a template: the
			// reference's CPU rate is the pace
			slice := (*poll - 50*time.Millisecond) / time.Duration(len(live))
			for _, s := range live {
				if !prepare(s) {
					continue
				}
				deadline := time.Now().Add(slice)
				for time.Now().Before(deadline) {
					if ctx.Err() != nil {
						break
					}
					nonce++
					mix, hash, err := v.Verify("igneumhash", s.header, s.cur.ShareHeight, nonce)
					if err != nil {
						log.Printf("%shash: %v", s.tag, err)
						break
					}
					hashes++
					minuteHashes++
					s.hashes++
					s.lastHeight = s.cur.ShareHeight
					if *pace > 0 {
						time.Sleep(time.Second / time.Duration(*pace))
					}
					if !validate.MeetsTarget(hash, s.target) {
						continue
					}
					submit(s, s.cur.ShareHeight, mix, hash)
				}
			}
		}
	}
}

func trim(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// sortedReasons is the record's stable order (unused at the log, used by the row's reader).
func sortedReasons(m map[string]uint64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
