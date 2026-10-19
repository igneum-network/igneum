// Command nc-miner is a CPU worker for the development chain's `null`
// algorithm speaking IS/1 to the reference pool. It exists to prove the
// pool end to end on a dev chain (subscribe, authorise, jobs, shares,
// block solutions, credits); it is not a miner anyone runs for reward
// and it carries no other algorithm.
//go:build igneumhash

// nc-miner: the non-custodial form's reference client (docs/ecosystem/POOL-NONCUSTODIAL.md): an Ed25519 key generated at
// start (never written anywhere), the signed authorize binding it to the payout address for the session, every share
// signed over (tag, chain id, pool id, header, nonce, mix, hash), the work on the node's own igneumhash verifier through
// the pool's validator (the CPU path, for the dev chain). -refusal-rows sends the four bad forms once and reports the
// pool's reasons. Built with go.ih.mod and -tags igneumhash; not a miner for reward.
package main

import (
	"bytes"
	"net/http"
	"strings"
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"

	"igneum.network/pool/internal/stratum"
	"igneum.network/pool/internal/validate"
	"lukechampine.com/blake3"
)

var (
	chainID  = flag.Uint64("chain-id", 7310, "the chain id the share's message covers")
	coordFlag = flag.Int("coord", -1, "VectorWork: mine only the jobs of this coordinate (the notify's coord; -1 = every job, as on a one-form pool)")
	refusals = flag.Bool("refusal-rows", false, "send the four bad forms once (another key's signature, a bad signature, a key not the session's, unsigned) and report the reasons, then exit")
	// receiptsOut keeps every mining.receipt's params as one JSON line (the member's proof of credit, which the
	// wallet's receipt view verifies offline: the product team's `igneum-wallet receipts`, 11 October 2026)
	receiptsOut = flag.String("receipts-out", "", "append every receipt's params as a JSON line to this file")
	receiptsFile *os.File
	// the fee-waiver handshake's miner side (ED-08, ED-17; the product team's Ember form): -dev-fee 1 reports the dev fee on
	// (0 off; -1 reports nothing), signed by the session key over "igneum-dev-fee\n<percent>\n<address>\n<manifest>\n<worker>\n"
	devFeeOn       = flag.Int("dev-fee", -1, "the miner's dev-fee percent to report on subscribe (1 on, 0 off; -1 reports nothing)")
	devFeeAddr     = flag.String("dev-fee-address", "", "the dev-fee address the report names")
	devFeeManifest = flag.String("dev-fee-manifest", "", "the dev-fee manifest's sha256 the report names")
	poolFeeBps     int64 = -1 // the pool's answered fee for this session (-1 until the subscribe result)
	pub      ed25519.PublicKey
	priv     ed25519.PrivateKey
	poolID   [32]byte
	sessID   [16]byte
	sessHex  string
	val      validate.Validator
	otherPub ed25519.PublicKey
	otherPriv ed25519.PrivateKey
)

var (
	receiptKey      ed25519.PublicKey
	receiptsOK      uint64
	receiptsBad     uint64
	receiptTotal    uint64
	receiptDigest   [32]byte
	receiptMu       sync.Mutex
)

// checkReceipt verifies the pool's receipt for one of this miner's shares: the signature under receipt_key over
// BLAKE3("IgneumPool/receipt/v0" || seq || share hash || key || weight || total || previous digest), the key ours, the
// running total equal to our own count, and the digest chain continuing from the last receipt.
func (m *miner) checkReceipt(r stratum.Receipt) {
	receiptMu.Lock()
	defer receiptMu.Unlock()
	sh, _ := hex.DecodeString(r.ShareHash)
	kb, _ := hex.DecodeString(r.Key)
	sig, _ := hex.DecodeString(r.Sig)
	dg, _ := hex.DecodeString(r.Digest)
	pv, _ := hex.DecodeString(r.Prev)
	if len(sh) != 32 || len(kb) != 32 || len(sig) != 64 || len(dg) != 32 || len(pv) != 32 || len(receiptKey) != 32 {
		receiptsBad++
		return
	}
	// the chain is per member (F-34): the receipt carries the previous digest it continues from, which must be this
	// member's own last receipt's digest (zero at the first receipt of the session)
	var zero [32]byte
	linked := string(pv) == string(receiptDigest[:]) || (receiptsOK == 0 && receiptsBad == 0 && string(pv) == string(zero[:]))
	if !linked {
		receiptsBad++
		log.Printf("receipt %d NOT linked: prev %s is not our last digest %x", r.Seq, r.Prev[:16], receiptDigest[:8])
		return
	}
	var u [8]byte
	h := blake3.New(32, nil)
	switch r.Version {
	case 2:
		h.Write([]byte("IgneumPool/receipt/v2"))
	case 1:
		h.Write([]byte("IgneumPool/receipt/v1"))
	default:
		h.Write([]byte("IgneumPool/receipt/v0"))
	}
	binary.BigEndian.PutUint64(u[:], r.Seq)
	h.Write(u[:])
	h.Write(sh)
	h.Write(kb)
	binary.BigEndian.PutUint64(u[:], r.Weight)
	h.Write(u[:])
	binary.BigEndian.PutUint64(u[:], r.Total)
	h.Write(u[:])
	h.Write(pv)
	if r.Version >= 1 {
		// the session's fee terms inside the signed message (the fee-waiver handshake): the member's proof of
		// what it was charged; the pool's fee on the receipt must be the fee the subscribe result answered
		var u4 [4]byte
		binary.BigEndian.PutUint32(u4[:], r.DevFeePercent)
		h.Write(u4[:])
		binary.BigEndian.PutUint32(u4[:], r.PoolFeeBps)
		h.Write(u4[:])
		if poolFeeBps >= 0 && int64(r.PoolFeeBps) != poolFeeBps {
			receiptsBad++
			log.Printf("receipt %d NOT verified: pool fee %d bps on the receipt against %d answered at subscribe", r.Seq, r.PoolFeeBps, poolFeeBps)
			return
		}
	}
	if r.Version >= 2 {
		// the share's chain identity inside the signed message (version 2): the session's extranonce, the job's
		// share height and parent, so the receipt joins to the carried entry on the chain without the coordinator
		pr, _ := hex.DecodeString(r.Parent)
		if len(pr) != 32 {
			receiptsBad++
			log.Printf("receipt %d NOT verified: a v2 receipt without a 32-byte parent", r.Seq)
			return
		}
		binary.BigEndian.PutUint64(u[:], r.ExtraNonce)
		h.Write(u[:])
		binary.BigEndian.PutUint64(u[:], r.Height)
		h.Write(u[:])
		h.Write(pr)
	}
	msg := h.Sum(nil)
	ours := hex.EncodeToString(pub) == r.Key
	receiptTotal += r.Weight
	if !ed25519.Verify(receiptKey, msg, sig) || hex.EncodeToString(msg) != r.Digest || !ours || r.Total != receiptTotal {
		receiptsBad++
		log.Printf("receipt %d NOT verified (sig %v, digest chain %v, our key %v, total %d against our %d)", r.Seq, ed25519.Verify(receiptKey, msg, sig), hex.EncodeToString(msg) == r.Digest, ours, r.Total, receiptTotal)
		copy(receiptDigest[:], dg)
		return
	}
	copy(receiptDigest[:], msg)
	receiptsOK++
	if receiptsOK == 1 || receiptsOK%50 == 0 {
		log.Printf("receipt %d verified: share %s weight %d total %d digest %s pool key %s", r.Seq, r.ShareHash[:16], r.Weight, r.Total, r.Digest[:16], r.PoolKey[:16])
	}
}

type foundShare struct {
	nonceStr, mixHex, hashHex string
	header                    [32]byte
	nonce                     uint64
	mix, hash                 [32]byte
}

// The member-node form's two flags (GUARD-C-CLOSURE 3.1): the member's own node's RPC for the direct submission and the
// list check before working; the counters are on the periodic line.
var (
	selfSubmit    string
	requireListed bool
	ownNodeIPC    string
	listRefused   uint64
	selfOK        uint64
	selfBad       uint64
	ownNodeSet    bool
)

// ipcCall is one JSON-RPC call over the member's own node's IPC socket (local to the member's host; the node's HTTP
// serves the read namespaces alone, so a coordinator on the network cannot make this call).
func ipcCall(path, method string, params interface{}) (json.RawMessage, error) {
	c, err := net.DialTimeout("unix", path, 5*time.Second)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	body, _ := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if _, err := c.Write(append(body, '\n')); err != nil {
		return nil, err
	}
	var out struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(bufio.NewReader(c)).Decode(&out); err != nil {
		return nil, err
	}
	if out.Error != nil {
		return nil, fmt.Errorf("%s: %s", method, out.Error.Message)
	}
	return out.Result, nil
}

// ownNodeTakeList is the member's own act on its own node, once per session, on the pool's statement: the list must
// name the member (else the client refuses and exits with the reason, hashing nothing), then the list and the member's
// own key as the template's Extra go onto the member's node over IPC. The coordinator's hands never touch the node.
func ownNodeTakeList(list json.RawMessage, account string) {
	if ownNodeIPC == "" || ownNodeSet {
		return
	}
	if !listNames(list, account) {
		log.Fatalf("REFUSED: the pool's list %s does not name %s; nothing set on the member's node, nothing hashed", shortList(list), account)
	}
	var entries []map[string]interface{}
	if err := json.Unmarshal(list, &entries); err != nil {
		log.Fatalf("REFUSED: the pool's list is not a list of entries: %v", err)
	}
	if _, err := ipcCall(ownNodeIPC, "miner_setPayoutList", []interface{}{entries}); err != nil {
		log.Fatalf("own node: miner_setPayoutList over IPC refused: %v", err)
	}
	if _, err := ipcCall(ownNodeIPC, "miner_setExtra", []interface{}{"0x" + hex.EncodeToString(pub)}); err != nil {
		log.Printf("own node: miner_setExtra over IPC refused: %v (the list is set; the finder's key is not in Extra)", err)
	}
	ownNodeSet = true
	log.Printf("own node: the pool's list %s names %s; set on the member's own node over IPC with the member's key as Extra", shortList(list), account)
}

// listNames reports whether the chain-native payout list names the account.
func listNames(list json.RawMessage, account string) bool {
	var entries []struct {
		Address string `json:"address"`
	}
	if len(list) == 0 || json.Unmarshal(list, &entries) != nil {
		return false
	}
	for _, e := range entries {
		if strings.EqualFold(e.Address, account) {
			return true
		}
	}
	return false
}

func shortList(list json.RawMessage) string {
	if len(list) == 0 {
		return "no list"
	}
	s := string(list)
	if len(s) > 96 {
		return s[:96] + "..."
	}
	return s
}

// selfSubmitBlock submits a block solution to the member's own node with eth_submitWork, the P2Pool recovery: the
// template is the member's node's own, so the node accepts the solution from the member as it would from the pool.
func selfSubmitBlock(height uint64, header [32]byte, nonce uint64, mix [32]byte) {
	body, _ := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": 1, "method": "eth_submitWork",
		"params": []string{fmt.Sprintf("0x%016x", nonce), "0x" + hex.EncodeToString(header[:]), "0x" + hex.EncodeToString(mix[:])}})
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(selfSubmit, "application/json", bytes.NewReader(body))
	if err != nil {
		selfBad++
		log.Printf("self-submit at height %d: %v", height, err)
		return
	}
	defer resp.Body.Close()
	var out struct {
		Result bool            `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || !out.Result {
		selfBad++
		log.Printf("self-submit at height %d: the node answered false (%s %v)", height, string(out.Error), err)
		return
	}
	selfOK++
	log.Printf("self-submit at height %d: ACCEPTED by the member's own node (nonce %d); the member landed its block without the coordinator", height, nonce)
}

func authorizeSig(session [16]byte, address string, key ed25519.PrivateKey) string {
	addr, _ := hex.DecodeString(strings.TrimPrefix(strings.ToLower(address), "0x"))
	h := blake3.New(32, nil)
	h.Write([]byte("ember-authorize"))
	h.Write(session[:])
	h.Write(addr)
	return hex.EncodeToString(ed25519.Sign(key, h.Sum(nil)))
}

func shareSig(header [32]byte, nonce uint64, mix, hash [32]byte, key ed25519.PrivateKey) string {
	var u [8]byte
	h := blake3.New(32, nil)
	h.Write([]byte("IgneumPool/nc-share/v0"))
	binary.BigEndian.PutUint64(u[:], *chainID)
	h.Write(u[:])
	h.Write(poolID[:])
	h.Write(header[:])
	binary.BigEndian.PutUint64(u[:], nonce)
	h.Write(u[:])
	h.Write(mix[:])
	h.Write(hash[:])
	return hex.EncodeToString(ed25519.Sign(key, h.Sum(nil)))
}

func main() {
	var (
		addr    = flag.String("pool", "127.0.0.1:3333", "pool stratum address")
		account = flag.String("account", "", "payout address (0x + 40 hex, required)")
		worker  = flag.String("worker", "null1", "worker name")
		maxTime = flag.Duration("for", 0, "stop after this long (0 = until interrupted)")
	)
	flag.StringVar(&selfSubmit, "self-submit", "", "the member's OWN node's RPC URL (the member-node form): every block solution is also submitted there directly with eth_submitWork, so the member lands its own block without the coordinator (the P2Pool recovery, GUARD-C-CLOSURE 3.2 'withhold a solution')")
	flag.BoolVar(&requireListed, "require-listed", false, "refuse to work a job whose payouts list does not name -account (the member checks the list before it hashes; a job without a list is refused too)")
	flag.StringVar(&ownNodeIPC, "own-node-ipc", "", "the member's OWN node's IPC socket (the datadir's geth.ipc): on the subscribe result the client checks that the pool's list (pool_list) names -account, refuses and exits if not, and otherwise sets that list (miner_setPayoutList) and its own key as the template's Extra (miner_setExtra) on its own node over IPC, so the coordinator never needs the node's write namespaces (GUARD-C-CLOSURE 3.2; the C3 row of 11 October 2026)")
	flag.Parse()
	if *account == "" {
		fmt.Fprintln(os.Stderr, "nc-miner: -account is required")
		os.Exit(2)
	}
	var err error
	pub, priv, err = ed25519.GenerateKey(rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	otherPub, otherPriv, _ = ed25519.GenerateKey(rand.Reader)
	log.Printf("miner key %x (generated at start, held in memory only)", pub)
	if *receiptsOut != "" {
		f, err := os.OpenFile(*receiptsOut, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			log.Fatal(err)
		}
		receiptsFile = f
		defer f.Close()
	}
	v, ok := validate.Lookup("igneumhash")
	if !ok {
		log.Fatal("the igneumhash validator is not in this build")
	}
	val = v
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *maxTime > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *maxTime)
		defer cancel()
	}
	// the session re-dials after a lost connection until the run time ends (a coordinator's restart, the C3 cells'
	// outage phase, must not end a member's client; each session starts its own receipt chain)
	for {
		err := run(ctx, *addr, *account, *worker)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Printf("session ended: %v; redialling %s in 2 s", err, *addr)
		} else {
			log.Printf("session closed by the pool; redialling %s in 2 s", *addr)
		}
		receiptMu.Lock()
		receiptsOK, receiptsBad, receiptTotal, receiptDigest, poolFeeBps = 0, 0, 0, [32]byte{}, -1
		receiptMu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

type miner struct {
	conn net.Conn
	w    *bufio.Writer
	wmu  sync.Mutex
	id   int

	mu      sync.Mutex
	cursor  map[string]uint64 // the nonce reached per header (continued across re-issued jobs)
	job     *stratum.NotifyParams
	start   uint64
	count   uint64
	pending map[int]string
	account string

	accepted, rejected, blocks uint64
}

func run(ctx context.Context, addr, account, worker string) error {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	m := &miner{conn: conn, w: bufio.NewWriter(conn), pending: map[int]string{}, account: account}
	jobs := make(chan stratum.NotifyParams, 8)
	go func() {
		<-ctx.Done()
		conn.Close()
	}()
	go m.reader(bufio.NewReader(conn), jobs)
	// the fee-waiver handshake's miner side (the product team's Ember form): the dev-fee state signed by the session key
	// inside the subscribe's params; the pool answers pool_fee on the result
	var subParams interface{} = []string{"nc-miner/0"}
	if *devFeeOn >= 0 {
		d := stratum.DevFee{Percent: uint32(*devFeeOn), Address: *devFeeAddr, ManifestSHA256: *devFeeManifest, Worker: worker, SessionKey: hex.EncodeToString(pub)}
		d.Signature = hex.EncodeToString(ed25519.Sign(priv, stratum.DevFeeMessage(d)))
		subParams = []interface{}{"nc-miner/0", map[string]interface{}{"dev_fee": d}}
	}
	// a NEW session every dial: the old session's id is forgotten before the subscribe goes, so the authorize below
	// signs over the id THIS connection is given, never the last one's (the node team's finding of 09:11Z,
	// 12 October 2026: on a redial to a restarted pool the authorize signed over the old id and was refused
	// "unauthorised", the member mining nothing until a hand restarted it; the no-manual-inputs class)
	sessHex = ""
	sessID = [16]byte{}
	if err := m.send(stratum.MethodSubscribe, subParams); err != nil {
		return err
	}
	// the authorize waits for the subscribe result (the session id and the pool id enter the signatures)
	for i := 0; i < 50 && sessHex == ""; i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if sessHex == "" {
		return fmt.Errorf("no subscribe result")
	}
	if err := m.send(stratum.MethodAuthorize, []string{account, worker, hex.EncodeToString(pub), authorizeSig(sessID, account, priv)}); err != nil {
		return err
	}
	report := time.NewTicker(10 * time.Second)
	defer report.Stop()
	var cancelJob context.CancelFunc
	for {
		select {
		case <-ctx.Done():
			if cancelJob != nil {
				cancelJob()
			}
			log.Printf("stopping: accepted %d rejected %d blocks %d", m.accepted, m.rejected, m.blocks)
			return nil
		case j, ok := <-jobs:
			if !ok {
				// the reader ended: the connection is lost (the pool stopped or restarted); the session ends and main
				// redials
				if cancelJob != nil {
					cancelJob()
				}
				return fmt.Errorf("connection to %s lost (accepted %d rejected %d)", addr, m.accepted, m.rejected)
			}
			if cancelJob != nil {
				cancelJob()
			}
			if requireListed && !listNames(j.Payouts, account) {
				// the member refuses before it works: a template whose list does not name it earns it nothing, and a
				// coordinator that changed the list cannot have the member's work on it (GUARD-C-CLOSURE 3.2)
				listRefused++
				log.Printf("job %s at height %d REFUSED: its payout list does not name %s (%s); refused so far %d", j.JobID, j.Height, account, shortList(j.Payouts), listRefused)
				continue
			}
			var jctx context.Context
			jctx, cancelJob = context.WithCancel(ctx)
			go m.mine(jctx, j)
		case <-report.C:
			receiptMu.Lock()
			rok, rbad := receiptsOK, receiptsBad
			receiptMu.Unlock()
			log.Printf("accepted %d rejected %d blocks %d receipts verified %d bad %d; jobs refused (list) %d; self-submit accepted %d refused %d", m.accepted, m.rejected, m.blocks, rok, rbad, listRefused, selfOK, selfBad)
		}
	}
}

func (m *miner) send(method string, params interface{}) error {
	m.wmu.Lock()
	defer m.wmu.Unlock()
	m.id++
	if method == stratum.MethodSubmit {
		m.mu.Lock()
		m.pending[m.id] = method
		m.mu.Unlock()
	}
	b, err := json.Marshal(map[string]interface{}{"id": m.id, "method": method, "params": params})
	if err != nil {
		return err
	}
	if _, err := m.w.Write(append(b, '\n')); err != nil {
		return err
	}
	return m.w.Flush()
}

func (m *miner) reader(r *bufio.Reader, jobs chan<- stratum.NotifyParams) {
	seenCoordJob := map[uint8]bool{}
	defer close(jobs) // a read error (the pool gone) ends the session's run loop, which redials
	for {
		frame, err := stratum.ReadFrame(r)
		if err != nil {
			return
		}
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  *stratum.Error  `json:"error"`
		}
		if err := json.Unmarshal(frame, &msg); err != nil {
			continue
		}
		switch {
		case msg.Method == stratum.MethodNotify:
			var p stratum.NotifyParams
			if json.Unmarshal(msg.Params, &p) == nil {
				// VectorWork: a session on one coordinate takes that coordinate's jobs alone (the pool notifies one job
				// per coordinate on one session); the job's chain_id, when carried, is the chain the share signs over
				if *coordFlag >= 0 && (p.Coord == nil || int(*p.Coord) != *coordFlag) {
					continue
				}
				if p.Coord != nil && !seenCoordJob[*p.Coord] {
					seenCoordJob[*p.Coord] = true
					log.Printf("coordinate %d (%s) job: document %s, difficulty %s, program_anchor %q at height %d", *p.Coord, p.CoordName, p.ParamsDigest, p.Difficulty, p.ProgramAnchor, p.AnchorHeight)
				}
				if p.ChainID != 0 && p.ChainID != *chainID {
					log.Printf("the job names chain %d (the flag said %d): the share signs over the job's", p.ChainID, *chainID)
					*chainID = p.ChainID
				}
				jobs <- p
			}
		case msg.Method == "mining.receipt":
			// the pool's signed receipt for an accepted share: verified against the subscribe result's receipt_key and the
			// member's own running total and digest chain (the member keeps every receipt as its proof of credit)
			var r stratum.Receipt
			if json.Unmarshal(msg.Params, &r) == nil {
				m.checkReceipt(r)
				if receiptsFile != nil {
					receiptsFile.Write(append(append([]byte{}, msg.Params...), '\n'))
				}
			}
		case msg.Method == "":
			var id int
			if json.Unmarshal(msg.ID, &id) != nil {
				continue
			}
			var sub stratum.SubscribeResult
			if json.Unmarshal(msg.Result, &sub) == nil && sub.NonceStart != "" {
				m.mu.Lock()
				m.start, _ = strconv.ParseUint(sub.NonceStart, 10, 64)
				m.count, _ = strconv.ParseUint(sub.NonceCount, 10, 64)
				m.mu.Unlock()
				sb, _ := hex.DecodeString(sub.SessionID)
				copy(sessID[:], sb)
				pb, _ := hex.DecodeString(sub.PoolID)
				copy(poolID[:], pb)
				sessHex = sub.SessionID
				if len(sub.PoolList) > 0 {
					ownNodeTakeList(sub.PoolList, m.account)
				} else if ownNodeIPC != "" {
					log.Fatalf("REFUSED: -own-node-ipc is set and the pool's subscribe result carries no pool_list; nothing set, nothing hashed")
				}
				if rk, err := hex.DecodeString(sub.ReceiptKey); err == nil && len(rk) == 32 {
					receiptMu.Lock()
					receiptKey = ed25519.PublicKey(rk)
					receiptMu.Unlock()
					log.Printf("the pool signs receipts under %s", sub.ReceiptKey)
				}
				log.Printf("session %s nonce range [%d, +%d); signed_shares %v pool_id %s", sub.SessionID, m.start, m.count, sub.SignedShares, sub.PoolID)
				// the pool's answer to the dev-fee report: the fee this session pays, carried in every receipt
				if sub.PoolFee != nil {
					receiptMu.Lock()
					poolFeeBps = int64(sub.PoolFee.Bps)
					receiptMu.Unlock()
					dev := "off"
					if *devFeeOn > 0 {
						dev = fmt.Sprintf("%d percent", *devFeeOn)
					}
					if sub.PoolFee.Waived {
						log.Printf("Dev fee %s; pool fee 0 (waived): %s", dev, sub.PoolFee.Reason)
					} else {
						log.Printf("Dev fee %s; pool fee %d bps: %s", dev, sub.PoolFee.Bps, sub.PoolFee.Reason)
					}
				} else {
					log.Printf("pool fee: not declared")
				}
				continue
			}
			m.mu.Lock()
			_, wasSubmit := m.pending[id]
			delete(m.pending, id)
			m.mu.Unlock()
			if wasSubmit {
				if msg.Error != nil {
					m.rejected++
					log.Printf("share rejected: %s", msg.Error.Reason)
				} else {
					m.accepted++
				}
			} else if msg.Error != nil {
				log.Printf("request %d refused: %s", id, msg.Error.Reason)
			}
		}
	}
}

func (m *miner) mine(ctx context.Context, j stratum.NotifyParams) {
	var header, share, boundary [32]byte
	for _, p := range []struct {
		dst *[32]byte
		src string
	}{{&header, j.Header}, {&share, j.ShareTarget}, {&boundary, j.Boundary}} {
		b, err := hex.DecodeString(p.src)
		if err != nil || len(b) != 32 {
			log.Printf("job %s: bad field", j.JobID)
			return
		}
		copy(p.dst[:], b)
	}
	m.mu.Lock()
	start, count := m.start, m.count
	m.mu.Unlock()
	if pr, ok := val.(validate.Preparer); ok {
		if err := pr.Prepare(j.Algorithm, j.Params, j.ParamsDigest, j.Height); err != nil {
			log.Printf("job %s: prepare: %v", j.JobID, err)
			return
		}
	}
	sent := 0
	var found []foundShare
	// Pace shares under the pool's published rate; block solutions are
	// never held back.
	pace := time.NewTicker(time.Second / 40)
	defer pace.Stop()
	// the nonce range continues across re-issued jobs of one header (a mid-height raise, a coordinate's re-notify):
	// a restart re-finds the same shares, which the pool refuses as re-found (the VectorWork soak of 04:37Z,
	// 13 October 2026)
	from := start
	m.mu.Lock()
	if m.cursor == nil {
		m.cursor = map[string]uint64{}
	}
	if c, ok := m.cursor[j.Header]; ok && c > from && c < start+count {
		from = c
	}
	m.mu.Unlock()
	for nonce := from; nonce < start+count; nonce++ {
		if nonce&0xfff == 0 {
			m.mu.Lock()
			m.cursor[j.Header] = nonce
			m.mu.Unlock()
		}
		if nonce&0xffff == 0 && ctx.Err() != nil {
			return
		}
		var mix, hash [32]byte
		var err error
		if av, ok := val.(validate.AnchoredValidator); ok && j.ProgramAnchor != "" {
			// a programmed document: the share is computed under the period's anchor the notify carries
			ab, aerr := hex.DecodeString(strings.TrimPrefix(j.ProgramAnchor, "0x"))
			if aerr != nil || len(ab) != 32 {
				log.Printf("job %s: bad program_anchor", j.JobID)
				return
			}
			var anchor [32]byte
			copy(anchor[:], ab)
			mix, hash, err = av.VerifyUnder(j.Algorithm, j.ParamsDigest, anchor, header, j.Height, nonce)
		} else {
			mix, hash, err = val.Verify(j.Algorithm, header, j.Height, nonce)
		}
		if err != nil {
			log.Printf("job %s: %v", j.JobID, err)
			return
		}
		if !validate.MeetsTarget(hash, share) {
			continue
		}
		if validate.MeetsTarget(hash, boundary) {
			m.blocks++
			log.Printf("block solution at height %d (nonce %d)", j.Height, nonce)
			if selfSubmit != "" {
				// the member holds its template (its own node built it): it lands its block itself, before and beside
				// the pool's submission, so a withholding coordinator changes nothing
				go selfSubmitBlock(j.Height, header, nonce, mix)
			}
		} else {
			select {
			case <-pace.C:
			case <-ctx.Done():
				return
			}
		}
		nonceStr, mixHex, hashHex := strconv.FormatUint(nonce, 10), hex.EncodeToString(mix[:]), hex.EncodeToString(hash[:])
		if *refusals {
			// each row on its own qualifying share (the session's duplicate check fires before the signature check)
			found = append(found, foundShare{nonceStr, mixHex, hashHex, header, nonce, mix, hash})
			if len(found) < 5 {
				continue
			}
			rows := []struct {
				name  string
				extra func(f foundShare) map[string]string
			}{
				{"another key's signature (key A's sig under key B)", func(f foundShare) map[string]string { return map[string]string{"key": hex.EncodeToString(otherPub), "sig": shareSig(f.header, f.nonce, f.mix, f.hash, priv)} }},
				{"a bad signature (the bytes altered)", func(f foundShare) map[string]string { return map[string]string{"key": hex.EncodeToString(pub), "sig": strings.Repeat("00", 64)} }},
				{"a key not the session's (key B signing its own share)", func(f foundShare) map[string]string { return map[string]string{"key": hex.EncodeToString(otherPub), "sig": shareSig(f.header, f.nonce, f.mix, f.hash, otherPriv)} }},
				{"unsigned (the four-element form)", nil},
			}
			for k, r := range rows {
				f := found[k]
				log.Printf("refusal row: %s (nonce %s)", r.name, f.nonceStr)
				if r.extra == nil {
					_ = m.send(stratum.MethodSubmit, []string{j.JobID, f.nonceStr, f.mixHex, f.hashHex})
				} else {
					_ = m.send(stratum.MethodSubmit, []interface{}{j.JobID, f.nonceStr, f.mixHex, f.hashHex, r.extra(f)})
				}
				time.Sleep(300 * time.Millisecond)
			}
			f := found[4]
			log.Printf("the valid signed share follows (nonce %s)", f.nonceStr)
			_ = m.send(stratum.MethodSubmit, []interface{}{j.JobID, f.nonceStr, f.mixHex, f.hashHex, map[string]string{"key": hex.EncodeToString(pub), "sig": shareSig(f.header, f.nonce, f.mix, f.hash, priv)}})
			time.Sleep(time.Second)
			log.Printf("refusal rows sent; accepted %d rejected %d", m.accepted, m.rejected)
			os.Exit(0)
		}
		if err := m.send(stratum.MethodSubmit, []interface{}{j.JobID, nonceStr, mixHex, hashHex, map[string]string{"key": hex.EncodeToString(pub), "sig": shareSig(header, nonce, mix, hash, priv)}}); err != nil {
			return
		}
		sent++
		if ctx.Err() != nil {
			return
		}
	}
}
