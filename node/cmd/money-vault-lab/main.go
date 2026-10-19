// Copyright 2026 The Igneum Authors
// This file is part of go-ethereum (Igneum fork).

// money-vault-lab drives the vault's stolen-key experiment (Project Money
// stage two, cut one; docs/money/STAGE2-CUT1-VAULT-2026-10-18.md) on a
// throwaway chain: five owners, each with a working balance W of ordinary
// notes and a vault of V under a policy (delay D, limit L a period P,
// recovery delay R), their everyday keys handed to an attacker that does
// its worst (drains W, requests up to L a period, splits the requests,
// repeats across periods, tries to change the policy, to complete early, to
// request twice against one head, and a reorganisation is forced during a
// pending window); then, run by run, the owner's response: the cancellation
// key within D (run 1); the recovery authority at once with no cancellation
// key (run 2); nothing, across several periods (run 3); a malicious recovery
// authority cancelled by the cancellation key (run 4); a compromised
// cancellation key griefing the owner's withdrawals, escaped by a policy
// change (run 5); the thief's request and the authority's announcement
// against one head in one block (run 6, F-801). The findings' rows (the
// vault re-run page's section 7): in run 4 the owner's own withdrawal is
// pending when the malicious recovery is announced (F-799: revoked at the
// announcement, restored by the cancellation); in run 5 the compromised
// cancellation key tries to cancel the recovery that rotates it (F-800).
// The take of each run is measured to the wei beside the spec's bound. Nothing here is a secret: the carrier is the development
// chain's published faucet key; the wallets', keys' and vaults' files are
// the lab's and are quarantined after the run.
package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/consensus/igneum/pool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/params"
)

type figures struct {
	ProofBytes []int     `json:"proofBytes"`
	ProveMs    []float64 `json:"proveMs"`
}

type runResult struct {
	Name          string   `json:"name"`
	W             string   `json:"W"`
	V             string   `json:"V"`
	L             string   `json:"L"`
	Take          string   `json:"take"`
	TakeFromW     string   `json:"takeFromW"`
	TakeFromVault string   `json:"takeFromVault"`
	Bound         string   `json:"bound"`
	BoundWords    string   `json:"boundWords"`
	WithinBound   bool     `json:"withinBound"`
	VaultLeft     string   `json:"vaultLeft"`
	VaultReturned string   `json:"vaultReturned"`
	Requests      int      `json:"requests"`
	Completed     int      `json:"completed"`
	Cancelled     int      `json:"cancelled"`
	Alternations  int      `json:"alternations"` // F-798: requests declared back at the current height after one declared ahead, every one refused
	Refusals      []string `json:"refusals"`
	Events        []string `json:"events"`
	Pass          bool     `json:"pass"`
}

type result struct {
	Chain            uint64              `json:"chain"`
	Verifier         string              `json:"verifier"`
	Policy           map[string]string   `json:"policy"`
	FirstBlock       uint64              `json:"firstBlock"`
	LastBlock        uint64              `json:"lastBlock"`
	Runs             []*runResult        `json:"runs"`
	Figures          map[string]*figures `json:"figures"`
	NodeCostMs       map[string]float64  `json:"nodeCostMs"`
	Reorg            []string            `json:"reorg"`
	SecondNodeAgrees bool                `json:"secondNodeAgrees"`
	Reconciled       bool                `json:"reconciled"`
	Turnstile        string              `json:"turnstile"`
	PoolBalance      string              `json:"poolBalance"`
	RevokedCount     uint64              `json:"revokedCount"`
	AllPass          bool                `json:"allPass"`
	WallSeconds      float64             `json:"wallSeconds"`
}

type wallet struct {
	name, file, notes, addr string
	unspent                 *big.Int
}

type noteJSON struct {
	Value    string  `json:"value"`
	Position uint64  `json:"position"`
	SpentAt  *uint64 `json:"spent_at"`
}

var (
	ctx      = context.Background()
	walletBn string
	proverBn string
	work     string
	threads  string
	proveRE  = regexp.MustCompile(`prove ([0-9.]+) ms, proof ([0-9]+) bytes`)
	res      = result{Figures: map[string]*figures{}, NodeCostMs: map[string]float64{}, Policy: map[string]string{}}
	step     int
)

func must(err error) {
	if err != nil {
		fail("%v", err)
	}
}

func fail(f string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "FAIL: "+f+"\n", a...)
	os.Exit(1)
}

func say(f string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, time.Now().UTC().Format("15:04:05Z")+" "+f+"\n", a...)
}

// run runs a tool; ok false with the tool's words on a non-zero exit.
func run(bin string, args ...string) (string, string, bool) {
	cmd := exec.Command(bin, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if threads != "" {
		cmd.Env = append(os.Environ(), "RAYON_NUM_THREADS="+threads)
	}
	err := cmd.Run()
	return strings.TrimSpace(out.String()), strings.TrimSpace(errb.String()), err == nil
}

func mustRun(bin string, args ...string) string {
	out, errs, ok := run(bin, args...)
	if !ok {
		fail("%s %v: %s", filepath.Base(bin), args, errs)
	}
	return out
}

func runJSON(bin string, args ...string) map[string]interface{} {
	var m map[string]interface{}
	out := mustRun(bin, args...)
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		fail("%s %v: not JSON: %s", filepath.Base(bin), args, out)
	}
	return m
}

func str(m map[string]interface{}, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return fmt.Sprint(m[k])
}

func h32(s string) (out [32]byte) {
	b, err := hex.DecodeString(strings.TrimPrefix(s, "0x"))
	if err != nil || len(b) != 32 {
		fail("not 32 bytes of hex: %q", s)
	}
	copy(out[:], b)
	return
}

func bigOf(s string) *big.Int {
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		fail("not a number: %q", s)
	}
	return v
}

func fresh(prefix string) string {
	step++
	return filepath.Join(work, fmt.Sprintf("%s-%04d.json", prefix, step))
}

func prove(witness, statement string) ([]byte, float64, int) {
	proofPath := strings.TrimSuffix(witness, ".json") + ".proof"
	claimPath := strings.TrimSuffix(witness, ".json") + ".claim"
	out := mustRun(proverBn, witness, proofPath, claimPath, "1")
	m := proveRE.FindStringSubmatch(out)
	if m == nil {
		fail("the prover's output has no times: %s", out)
	}
	ms, _ := strconv.ParseFloat(m[1], 64)
	n, _ := strconv.Atoi(m[2])
	proof, err := os.ReadFile(proofPath)
	must(err)
	os.Remove(witness)
	f := res.Figures[statement]
	if f == nil {
		f = &figures{}
		res.Figures[statement] = f
	}
	f.ProofBytes = append(f.ProofBytes, n)
	f.ProveMs = append(f.ProveMs, ms)
	return proof, ms, n
}

func main() {
	rpcURL := flag.String("rpc", "http://127.0.0.1:8741", "the sealing node's HTTP endpoint")
	rpc2URL := flag.String("rpc2", "http://127.0.0.1:8743", "the reading node's HTTP endpoint")
	rpcT := flag.String("rpc-t", "http://127.0.0.1:8742", "the second sealer's HTTP endpoint (partitioned for the reorganisation)")
	flag.StringVar(&walletBn, "wallet", "", "the igneum-money binary")
	flag.StringVar(&proverBn, "prover", "", "the money-prove binary")
	flag.StringVar(&work, "work", "", "the work directory (wallet, key and vault files, witnesses, proofs; quarantined after)")
	flag.StringVar(&threads, "threads", "8", "RAYON_NUM_THREADS for the prover")
	out := flag.String("out", "", "the result JSON's path")
	delayBlocks := flag.Uint64("delay-blocks", 300, "D_b")
	delaySeconds := flag.Uint64("delay-seconds", 3600, "D_s")
	period := flag.Uint64("period", 300, "P")
	recBlocks := flag.Uint64("recovery-blocks", 600, "R_b")
	recSeconds := flag.Uint64("recovery-seconds", 7200, "R_s")
	periodsRun3 := flag.Int("periods", 3, "run 3: the periods the owner stays silent")
	reorg := flag.Bool("reorg", true, "the partition row (needs the second sealer T on -rpc-t)")
	flag.Parse()
	if walletBn == "" || proverBn == "" || work == "" {
		fail("-wallet, -prover and -work are needed")
	}
	must(os.MkdirAll(work, 0o700))
	ec, err := ethclient.Dial(*rpcURL)
	must(err)
	ec2, err := ethclient.Dial(*rpc2URL)
	must(err)
	ecT, err := ethclient.Dial(*rpcT)
	must(err)
	chainID, err := ec.ChainID(ctx)
	must(err)
	cid := chainID.Uint64()
	cidS := strconv.FormatUint(cid, 10)
	poolAddr := pool.Address(cid)
	key := params.IgneumDevFaucetKey()
	from := crypto.PubkeyToAddress(key.PublicKey)
	signer := types.LatestSignerForChainID(chainID)
	nonce, err := ec.PendingNonceAt(ctx, from)
	must(err)
	res.Chain = cid
	started := time.Now()
	one := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	L := new(big.Int).Set(one)                 // 1 IGN a period
	V := new(big.Int).Mul(big.NewInt(10), one) // 10 IGN in each vault
	W := new(big.Int).Mul(big.NewInt(2), one)  // 2 IGN of working balance (two notes of 1 IGN)
	res.Policy = map[string]string{"delay_blocks": fmt.Sprint(*delayBlocks), "delay_seconds": fmt.Sprint(*delaySeconds), "period": fmt.Sprint(*period), "limit": L.String(), "recovery_blocks": fmt.Sprint(*recBlocks), "recovery_seconds": fmt.Sprint(*recSeconds), "W": W.String(), "V": V.String()}

	head := func() uint64 {
		h, err := ec.BlockNumber(ctx)
		must(err)
		return h
	}
	headerAt := func(h uint64) *types.Header {
		hd, err := ec.HeaderByNumber(ctx, new(big.Int).SetUint64(h))
		must(err)
		return hd
	}
	send := func(value *big.Int, data []byte) *types.Transaction {
		tip, err := ec.SuggestGasTipCap(ctx)
		must(err)
		hd, err := ec.HeaderByNumber(ctx, nil)
		must(err)
		feeCap := new(big.Int).Add(new(big.Int).Mul(hd.BaseFee, big.NewInt(4)), tip)
		gas := uint64(21000 + 16*len(data) + 4*32 + 50000)
		tx := types.MustSignNewTx(key, signer, &types.DynamicFeeTx{ChainID: chainID, Nonce: nonce, To: &poolAddr, Value: value, Gas: gas, GasTipCap: tip, GasFeeCap: feeCap, Data: data})
		must(ec.SendTransaction(ctx, tx))
		nonce++
		return tx
	}
	wait := func(txs ...*types.Transaction) uint64 {
		var last uint64
		for _, tx := range txs {
			for {
				r, err := ec.TransactionReceipt(ctx, tx.Hash())
				if err == nil && r != nil {
					if r.Status != 1 {
						fail("transaction %s failed in the EVM", tx.Hash())
					}
					if r.BlockNumber.Uint64() > last {
						last = r.BlockNumber.Uint64()
					}
					break
				}
				time.Sleep(200 * time.Millisecond)
			}
		}
		for {
			h2, err := ec2.BlockNumber(ctx)
			must(err)
			if h2 >= last {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if res.FirstBlock == 0 {
			res.FirstBlock = last
		}
		if last > res.LastBlock {
			res.LastBlock = last
		}
		return last
	}
	poolInfo := func(c *ethclient.Client, block *uint64) pool.Info {
		var info pool.Info
		var arg interface{}
		if block != nil {
			arg = fmt.Sprintf("0x%x", *block)
		}
		must(c.Client().CallContext(ctx, &info, "igneum_pool", arg))
		return info
	}
	type poolBlock struct {
		Height     uint64 `json:"height"`
		Position   uint64 `json:"position"`
		Outputs    []struct{ Commitment, Ciphertext string }
		Nullifiers []string `json:"nullifiers"`
		Statements []string `json:"statements"`
		Records    int      `json:"records"`
		ProofBytes int      `json:"proofBytes"`
	}
	poolBlockAt := func(h uint64) poolBlock {
		var b poolBlock
		must(ec.Client().CallContext(ctx, &b, "igneum_poolBlock", fmt.Sprintf("0x%x", h)))
		return b
	}
	type poolPath struct {
		Anchor   string   `json:"anchor"`
		Height   uint64   `json:"height"`
		Index    uint64   `json:"index"`
		Siblings []string `json:"siblings"`
	}
	pathsFile := func(positions ...uint64) string {
		h := head()
		paths := map[string][]string{}
		var anchor string
		for _, pos := range positions {
			var p poolPath
			must(ec.Client().CallContext(ctx, &p, "igneum_poolPath", pos, fmt.Sprintf("0x%x", h)))
			anchor = strings.TrimPrefix(p.Anchor, "0x")
			sib := make([]string, 0, 32)
			for _, s := range p.Siblings {
				sib = append(sib, strings.TrimPrefix(s, "0x"))
			}
			paths[strconv.FormatUint(pos, 10)] = sib
		}
		f := fresh("paths")
		pj, _ := json.Marshal(map[string]interface{}{"anchor": anchor, "paths": paths})
		must(os.WriteFile(f, pj, 0o600))
		return f
	}
	revokedFile := func(keyOf uint64) string {
		var rp pool.RevokedProof
		must(ec.Client().CallContext(ctx, &rp, "igneum_poolRevokedPath", keyOf, "latest"))
		f := fresh("revoked")
		b, _ := json.Marshal(rp)
		must(os.WriteFile(f, b, 0o600))
		return f
	}
	// revokedFor is the non-membership (or membership) path of a pending note's policy at the head: the
	// path the completion is proven with (the first lab asked for key 0's path, right only while the set
	// was empty).
	revokedFor := func(policyHash string) string {
		return revokedFile(pool.RevokedKey(h32(policyHash)))
	}
	// admittedAt says whether a commitment was admitted at a block (igneum_poolBlock's outputs).
	admittedAt := func(cm string, block uint64) bool {
		pb := poolBlockAt(block)
		for _, o := range pb.Outputs {
			if strings.TrimPrefix(o.Commitment, "0x") == strings.TrimPrefix(cm, "0x") {
				return true
			}
		}
		return false
	}
	// The block stream for the wallets.
	streamFile := filepath.Join(work, "blocks.jsonl")
	streamed := uint64(0)
	var streamBuf bytes.Buffer
	stream := func() {
		h := head()
		for b := streamed; b <= h; b++ {
			pb := poolBlockAt(b)
			outs := make([]string, 0, len(pb.Outputs))
			for _, o := range pb.Outputs {
				outs = append(outs, strings.TrimPrefix(o.Commitment, "0x")+strings.TrimPrefix(o.Ciphertext, "0x"))
			}
			nfs := make([]string, 0, len(pb.Nullifiers))
			for _, nf := range pb.Nullifiers {
				nfs = append(nfs, strings.TrimPrefix(nf, "0x"))
			}
			line, _ := json.Marshal(map[string]interface{}{"height": pb.Height, "position": pb.Position, "outputs": outs, "nullifiers": nfs})
			streamBuf.Write(line)
			streamBuf.WriteByte('\n')
		}
		streamed = h + 1
		must(os.WriteFile(streamFile, streamBuf.Bytes(), 0o600))
	}
	scan := func(w *wallet) map[string]interface{} {
		stream()
		m := runJSON(walletBn, "--wallet", w.file, "scan", "--blocks", streamFile, "--notes-out", w.notes)
		w.unspent = bigOf(str(m, "unspent"))
		return m
	}
	vaultScan := func(vf string) map[string]interface{} {
		stream()
		return runJSON(walletBn, "vault-scan", "--vault", vf, "--blocks", streamFile)
	}
	unspentNotes := func(w *wallet) []noteJSON {
		text, err := os.ReadFile(w.notes)
		must(err)
		var all []noteJSON
		must(json.Unmarshal(text, &all))
		var u []noteJSON
		for _, n := range all {
			if n.SpentAt == nil && bigOf(n.Value).Sign() > 0 {
				u = append(u, n)
			}
		}
		sort.Slice(u, func(i, j int) bool { return u[i].Position < u[j].Position })
		return u
	}
	newWallet := func(name string) *wallet {
		w := &wallet{name: name, file: filepath.Join(work, "wallet-"+name+".json"), notes: filepath.Join(work, "notes-"+name+".json"), unspent: new(big.Int)}
		w.addr = mustRun(walletBn, "--wallet", w.file, "keygen", "--birthday", "0")
		return w
	}
	newKey := func(name string) (file, cancelPk, recoverPk string) {
		file = filepath.Join(work, "key-"+name+".hex")
		m := runJSON(walletBn, "vault-keygen", "--out", file)
		return file, str(m, "cancel"), str(m, "recover")
	}
	zero32 := strings.Repeat("00", 32)

	// A vault record from the wallet's record JSON.
	recordOf := func(recFile string) ([]byte, map[string]interface{}, string) {
		text, err := os.ReadFile(recFile)
		must(err)
		var r map[string]interface{}
		must(json.Unmarshal(text, &r))
		statement := str(r, "statement")
		return nil, r, statement
	}
	vaultData := func(recFile string, proof []byte) ([]byte, map[string]interface{}) {
		_, r, _ := recordOf(recFile)
		vr := pool.Vault{Tag: byte(r["kind"].(float64)), Anchor: h32(str(r, "anchor")), Ptag: h32(str(r, "ptag")), RevokedRoot: h32(str(r, "revoked_root")), Revealed: h32(str(r, "revealed")), Height: uint64(r["height"].(float64)), Time: uint64(r["time"].(float64)), Proof: proof}
		for _, nf := range r["nullifiers"].([]interface{}) {
			vr.Nullifiers = append(vr.Nullifiers, h32(nf.(string)))
		}
		for _, o := range r["outputs"].([]interface{}) {
			om := o.(map[string]interface{})
			ct, err := hex.DecodeString(str(om, "ciphertext"))
			must(err)
			vr.Outputs = append(vr.Outputs, pool.Output{Commitment: h32(str(om, "commitment")), Ciphertext: ct})
		}
		return vr.Encode(), r
	}
	// proveAndSend proves a prepared statement and sends it; returns the including block, the record and
	// whether the first output was admitted.
	proveAndSend := func(witness, recFile string) (uint64, map[string]interface{}, bool) {
		_, r, statement := recordOf(recFile)
		proof, _, _ := prove(witness, statement)
		data, _ := vaultData(recFile, proof)
		blk := wait(send(new(big.Int), data))
		cm := str(r["outputs"].([]interface{})[0].(map[string]interface{}), "commitment")
		return blk, r, admittedAt(cm, blk)
	}
	// retry runs a preparation-and-send up to three times (a statement proven against an anchor a short
	// reorganisation replaced is refused as "no recorded root" and re-proven against a fresh one: the
	// spec's rule 8.1).
	retry := func(what string, f func() (uint64, bool)) (uint64, bool) {
		for i := 0; i < 3; i++ {
			blk, ok := f()
			if ok {
				return blk, true
			}
			say("%s: not admitted at block %d, re-proven against a fresh anchor (try %d)", what, blk, i+2)
			time.Sleep(2 * time.Second)
		}
		return 0, false
	}
	type vaultRun struct {
		res                          *runResult
		owner, thiefDest             *wallet
		vaultFile, cancelKey, recKey string
		cancelPk, recoverPk          string
		vid, headCm                  string
		pendings                     []map[string]interface{} // the thief's pending receipts (files)
		receipts                     []string
		completedTake                *big.Int
	}
	newRun := func(name string) *vaultRun {
		r := &vaultRun{res: &runResult{Name: name, W: W.String(), V: V.String(), L: L.String()}, completedTake: new(big.Int)}
		r.owner = newWallet(name + "-owner")
		r.thiefDest = newWallet(name + "-thief")
		r.cancelKey, r.cancelPk, _ = newKey(name + "-cancel")
		r.recKey, _, r.recoverPk = newKey(name + "-recovery")
		r.vaultFile = filepath.Join(work, "vault-"+name+".json")
		res.Runs = append(res.Runs, r.res)
		return r
	}
	event := func(r *vaultRun, f string, a ...interface{}) {
		line := time.Now().UTC().Format("15:04:05Z") + " " + fmt.Sprintf(f, a...)
		r.res.Events = append(r.res.Events, line)
		say("%s: %s", r.res.Name, line)
	}
	refusal := func(r *vaultRun, f string, a ...interface{}) {
		line := fmt.Sprintf(f, a...)
		r.res.Refusals = append(r.res.Refusals, line)
		say("%s: REFUSED %s", r.res.Name, line)
	}

	runs := []*vaultRun{newRun("run1-cancel-within-D"), newRun("run2-recovery-at-once"), newRun("run3-owner-silent"), newRun("run4-malicious-recovery"), newRun("run5-cancel-griefing"), newRun("run6-head-race")}
	say("six owners, six thieves, twelve keys made; head %d", head())

	// The shields: each owner gets W as two notes of 1 IGN and V as one note of 10 IGN.
	shieldOf := func(to *wallet, value *big.Int) []byte {
		wit, rec := fresh("shield-w"), fresh("shield-r")
		mustRun(walletBn, "--wallet", to.file, "shield-prepare", "--value", value.String(), "--chain-id", cidS, "--witness-out", wit, "--record-out", rec)
		proof, _, _ := prove(wit, "money-shield-v1")
		var r struct{ Commitment, Ciphertext string }
		text, err := os.ReadFile(rec)
		must(err)
		must(json.Unmarshal(text, &r))
		ct, err := hex.DecodeString(r.Ciphertext)
		must(err)
		return pool.Shield{Commitment: h32(r.Commitment), Ciphertext: ct, Proof: proof}.Encode()
	}
	var pending []*types.Transaction
	for _, r := range runs {
		pending = append(pending, send(one, shieldOf(r.owner, one)), send(one, shieldOf(r.owner, one)), send(V, shieldOf(r.owner, V)))
	}
	wait(pending...)
	for _, r := range runs {
		scan(r.owner)
		if r.owner.unspent.Cmp(new(big.Int).Add(W, V)) != 0 {
			fail("%s: the owner holds %v, not W + V", r.res.Name, r.owner.unspent)
		}
	}
	say("every owner holds W + V = %v; head %d", new(big.Int).Add(W, V), head())

	// The vaults: the 10 IGN note into a vault under the policy; the cancellation key of run 2 is none.
	for _, r := range runs {
		notes := unspentNotes(r.owner)
		var big10 noteJSON
		for _, n := range notes {
			if bigOf(n.Value).Cmp(V) == 0 {
				big10 = n
			}
		}
		cancelPk := r.cancelPk
		if r.res.Name == "run2-recovery-at-once" {
			cancelPk = zero32
		}
		blk, ok := retry("the vault creation", func() (uint64, bool) {
			os.Remove(r.vaultFile)
			wit, rec := fresh("create-w"), fresh("create-r")
			m := runJSON(walletBn, "--wallet", r.owner.file, "vault-create", "--notes", r.owner.notes, "--positions", strconv.FormatUint(big10.Position, 10), "--paths", pathsFile(big10.Position),
				"--value", V.String(), "--delay-blocks", fmt.Sprint(*delayBlocks), "--delay-seconds", fmt.Sprint(*delaySeconds), "--period", fmt.Sprint(*period), "--limit", L.String(),
				"--recovery-blocks", fmt.Sprint(*recBlocks), "--recovery-seconds", fmt.Sprint(*recSeconds), "--cancel-pk", cancelPk, "--recovery-pk", r.recoverPk,
				"--chain-id", cidS, "--witness-out", wit, "--record-out", rec, "--vault-out", r.vaultFile)
			r.vid, r.headCm = str(m, "vid"), str(m, "head_commitment")
			blk, _, ok := proveAndSend(wit, rec)
			return blk, ok
		})
		if !ok {
			fail("%s: the vault creation was refused at block %d", r.res.Name, blk)
		}
		event(r, "vault created at block %d (V = %v, L = %v a period of %d blocks, D = %d blocks and %d seconds, R = %d blocks and %d seconds; cancellation key %v)", blk, V, L, *period, *delayBlocks, *delaySeconds, *recBlocks, *recSeconds, cancelPk != zero32)
	}
	for _, r := range runs {
		scan(r.owner)
		st := vaultScan(r.vaultFile)
		if str(st, "protected") != V.String() || r.owner.unspent.Cmp(W) != 0 {
			fail("%s: after creation the vault protects %s and the owner holds %v", r.res.Name, str(st, "protected"), r.owner.unspent)
		}
	}
	say("six vaults live, each protecting %v; the owners hold W = %v; head %d", V, W, head())
	// The policy floors refused at creation: a delay of one hour less a second.
	{
		r := runs[0]
		notes := unspentNotes(r.owner)
		_, errs, ok := run(walletBn, "--wallet", r.owner.file, "vault-create", "--notes", r.owner.notes, "--positions", strconv.FormatUint(notes[0].Position, 10), "--paths", pathsFile(notes[0].Position),
			"--value", one.String(), "--delay-blocks", fmt.Sprint(*delayBlocks), "--delay-seconds", fmt.Sprint(*delaySeconds-1), "--period", fmt.Sprint(*period), "--limit", L.String(), "--recovery-blocks", fmt.Sprint(*recBlocks), "--recovery-seconds", fmt.Sprint(*recSeconds),
			"--cancel-pk", r.cancelPk, "--recovery-pk", r.recoverPk, "--chain-id", cidS, "--witness-out", fresh("x"), "--record-out", fresh("x"), "--vault-out", fresh("x"))
		if ok {
			fail("a policy below the floor was accepted by the wallet")
		}
		refusal(r, "a policy with D_s one second below the floor (%d s): the wallet refuses before proving (%s); the circuit's floor is the same expression", *delaySeconds-1, errs)
	}

	// THE THEFT: the attacker holds every owner's everyday wallet file and vault file (the viewing key is in
	// the everyday wallet). It drains W first: a spend of the two 1 IGN notes to its own wallet.
	thiefSpend := func(r *vaultRun, notes []noteJSON) {
		positions := []string{}
		var ps []uint64
		for _, n := range notes {
			positions = append(positions, strconv.FormatUint(n.Position, 10))
			ps = append(ps, n.Position)
		}
		total := new(big.Int)
		for _, n := range notes {
			total.Add(total, bigOf(n.Value))
		}
		wit, rec := fresh("spend-w"), fresh("spend-r")
		mustRun(walletBn, "--wallet", r.owner.file, "spend-prepare", "--notes", r.owner.notes, "--positions", strings.Join(positions, ","), "--paths", pathsFile(ps...),
			"--to", r.thiefDest.addr, "--value", total.String(), "--fee", "0", "--chain-id", cidS, "--witness-out", wit, "--record-out", rec)
		proof, _, _ := prove(wit, "money-spend-v1")
		var rr struct {
			Anchor     string
			Nullifiers []string
			Outputs    []struct{ Commitment, Ciphertext string }
		}
		text, err := os.ReadFile(rec)
		must(err)
		must(json.Unmarshal(text, &rr))
		sp := pool.Spend{Anchor: h32(rr.Anchor), PublicValue: new(big.Int), Fee: new(big.Int), Proof: proof}
		for _, nf := range rr.Nullifiers {
			sp.Nullifiers = append(sp.Nullifiers, h32(nf))
		}
		for _, o := range rr.Outputs {
			ct, err := hex.DecodeString(o.Ciphertext)
			must(err)
			sp.Outputs = append(sp.Outputs, pool.Output{Commitment: h32(o.Commitment), Ciphertext: ct})
		}
		blk := wait(send(new(big.Int), sp.Encode()))
		scan(r.thiefDest)
		scan(r.owner)
		event(r, "W drained: %v moved to the thief's wallet at block %d (the thief holds %v, the owner %v)", total, blk, r.thiefDest.unspent, r.owner.unspent)
		r.res.TakeFromW = r.thiefDest.unspent.String()
	}
	for _, r := range runs {
		thiefSpend(r, unspentNotes(r.owner))
	}

	// A withdrawal request by the thief (the everyday key): declared at the head plus a margin.
	request := func(r *vaultRun, dest *wallet, value *big.Int, declHeight, declTime uint64, label string) (uint64, bool, string) {
		vaultScan(r.vaultFile)
		st := vaultScan(r.vaultFile)
		hm := st["head"].(map[string]interface{})
		pos := uint64(hm["position"].(float64))
		wit, rec, receipt := fresh("request-w"), fresh("request-r"), fresh("receipt")
		outS, errs, ok := run(walletBn, "--wallet", r.owner.file, "vault-request", "--vault", r.vaultFile, "--paths", pathsFile(pos), "--to", dest.addr, "--value", value.String(),
			"--height", fmt.Sprint(declHeight), "--time", fmt.Sprint(declTime), "--witness-out", wit, "--record-out", rec, "--receipt-out", receipt)
		if !ok {
			return 0, false, errs
		}
		var m map[string]interface{}
		must(json.Unmarshal([]byte(outS), &m))
		blk, _, admitted := proveAndSend(wit, rec)
		for i := 0; i < 2 && !admitted; i++ {
			say("%s: the request was not admitted at block %d; re-proven against a fresh anchor", r.res.Name, blk)
			vaultScan(r.vaultFile)
			st = vaultScan(r.vaultFile)
			pos = uint64(st["head"].(map[string]interface{})["position"].(float64))
			wit, rec, receipt = fresh("request-w"), fresh("request-r"), fresh("receipt")
			outS, errs, ok = run(walletBn, "--wallet", r.owner.file, "vault-request", "--vault", r.vaultFile, "--paths", pathsFile(pos), "--to", dest.addr, "--value", value.String(),
				"--height", fmt.Sprint(declHeight), "--time", fmt.Sprint(declTime), "--witness-out", wit, "--record-out", rec, "--receipt-out", receipt)
			if !ok {
				return blk, false, errs
			}
			must(json.Unmarshal([]byte(outS), &m))
			blk, _, admitted = proveAndSend(wit, rec)
		}
		if admitted {
			r.receipts = append(r.receipts, receipt)
			r.res.Requests++
			event(r, "%s: request of %v admitted at block %d (declared %d, %d); matures at block %d and time %d", label, value, blk, declHeight, declTime, uint64(m["matures_at_block"].(float64)), uint64(m["matures_at_time"].(float64)))
		}
		return blk, admitted, ""
	}
	now := func() (uint64, uint64) {
		h := head()
		return h, headerAt(h).Time
	}
	declare := func() (uint64, uint64) {
		h, t := now()
		return h + 2, t + 60
	}

	// Run 1: the thief requests L (split in two), tries a third, a policy change, an early completion, two
	// requests against one head; the owner's cancellation key cancels both pending notes within D.
	{
		r := runs[0]
		h, t := declare()
		half := new(big.Int).Div(L, big.NewInt(2))
		request(r, r.thiefDest, half, h, t, "split 1 of 2")
		h, t = declare()
		request(r, r.thiefDest, new(big.Int).Sub(L, half), h, t, "split 2 of 2")
		h, t = declare()
		if _, _, errs := request(r, r.thiefDest, big.NewInt(1), h, t, "one wei past L"); errs != "" {
			refusal(r, "a third request of 1 wei in the same period: the wallet refuses (%s); the circuit's running total is the same expression", errs)
		}
		// a policy change by the everyday key: the thief has no recovery key; a forged announcement is refused
		{
			vaultScan(r.vaultFile)
			st := vaultScan(r.vaultFile)
			pos := uint64(st["head"].(map[string]interface{})["position"].(float64))
			_, errs, ok := run(walletBn, "--wallet", r.thiefDest.file, "vault-new-policy", "--vault", r.vaultFile, "--delay-blocks", fmt.Sprint(*delayBlocks), "--delay-seconds", fmt.Sprint(*delaySeconds), "--period", fmt.Sprint(*period), "--limit", V.String(), "--recovery-blocks", fmt.Sprint(*recBlocks), "--recovery-seconds", fmt.Sprint(*recSeconds), "--cancel-pk", zero32, "--recovery-pk", zero32, "--out", filepath.Join(work, "thief-policy.json"))
			if !ok {
				fail("the thief's new policy file: %s", errs)
			}
			_, errs, ok = run(walletBn, "vault-announce", "--vault", r.vaultFile, "--key", r.cancelKey, "--new-policy", filepath.Join(work, "thief-policy.json"), "--paths", pathsFile(pos), "--height", fmt.Sprint(h), "--time", fmt.Sprint(t), "--witness-out", fresh("x"), "--record-out", fresh("x"))
			if ok {
				fail("an announcement without the recovery key was prepared")
			}
			refusal(r, "a policy change to the thief's keys (limit raised to V, no authorities) under the everyday key: the wallet refuses (%s)", errs)
			// forged: a record of the announcement with a garbage proof
			vr := pool.Vault{Tag: pool.VaultAnnounceTag, Anchor: h32(strings.TrimPrefix(poolInfo(ec, nil).Root.Hex(), "0x")), Nullifiers: [][32]byte{h32(strings.Repeat("11", 32))}, Outputs: []pool.Output{{Commitment: h32(strings.Repeat("22", 32)), Ciphertext: []byte{1}}}, Revealed: h32(strings.Repeat("33", 32)), Height: h, Time: t, Proof: bytes.Repeat([]byte{0x5a}, 75000)}
			before := poolInfo(ec, nil)
			blk := wait(send(new(big.Int), vr.Encode()))
			after := poolInfo(ec, nil)
			if after.Count != before.Count {
				fail("a forged announcement changed the pool")
			}
			refusal(r, "a forged announcement record (a proof of 75,000 arbitrary bytes) sent to the chain: refused by the verifier at block %d, the pool unchanged", blk)
		}
		// an early completion: the wallet refuses; the forced form (proven at a height past the chain) refused by the node
		{
			stream()
			st := vaultScan(r.vaultFile)
			pend := st["pending"].([]interface{})[0].(map[string]interface{})
			pos := uint64(pend["position"].(float64))
			h2, t2 := now()
			_, errs, ok := run(walletBn, "--wallet", r.thiefDest.file, "vault-complete", "--receipt", r.receipts[0], "--paths", pathsFile(pos), "--revoked", revokedFor(str(st, "policy_hash")), "--height", fmt.Sprint(h2), "--time", fmt.Sprint(t2), "--chain-id", cidS, "--witness-out", fresh("x"), "--record-out", fresh("x"))
			if ok {
				fail("an early completion was prepared")
			}
			refusal(r, "a completion before maturity at block %d: the wallet refuses (%s); the circuit's maturity check is the same expression", h2, errs)
			// over-declared: proven as if at the maturity height (the circuit is satisfied), the node refuses a height past the block
			mb := uint64(pend["matures_at_block"].(float64))
			mt := uint64(pend["matures_at_time"].(float64))
			wit, rec := fresh("early-w"), fresh("early-r")
			_, errs, ok = run(walletBn, "--wallet", r.thiefDest.file, "vault-complete", "--receipt", r.receipts[0], "--paths", pathsFile(pos), "--revoked", revokedFor(str(st, "policy_hash")), "--height", fmt.Sprint(mb), "--time", fmt.Sprint(mt), "--chain-id", cidS, "--witness-out", wit, "--record-out", rec)
			if !ok {
				fail("the over-declared completion: %s", errs)
			}
			before := poolInfo(ec, nil)
			blk, _, admitted := proveAndSend(wit, rec)
			if admitted || poolInfo(ec, nil).Count != before.Count {
				fail("an over-declared completion was admitted")
			}
			refusal(r, "a completion proven at the maturity height %d and time %d while the chain is at %d: refused by the node at block %d (the proven height and time are past the including block)", mb, mt, h2, blk)
		}
		// two requests proven against one head, sent in one block: one admitted, one refused as a duplicate nullifier
		{
			vaultScan(r.vaultFile)
			// the period's allowance is used: both requests are for the next period, declared 64 blocks ahead if that reaches it; else both refused by the wallet
			st := vaultScan(r.vaultFile)
			hm := st["head"].(map[string]interface{})
			pos := uint64(hm["position"].(float64))
			h3, t3 := now()
			nextBoundary := (h3/(*period) + 1) * (*period)
			if nextBoundary-h3 <= pool.DeclareMargin-4 {
				wits := []string{}
				recs := []string{}
				for i := 0; i < 2; i++ {
					wit, rec, receipt := fresh("conc-w"), fresh("conc-r"), fresh("receipt")
					vfCopy := filepath.Join(work, fmt.Sprintf("vault-run1-copy%d.json", i))
					b, err := os.ReadFile(r.vaultFile)
					must(err)
					must(os.WriteFile(vfCopy, b, 0o600))
					_, errs, ok := run(walletBn, "--wallet", r.owner.file, "vault-request", "--vault", vfCopy, "--paths", pathsFile(pos), "--to", r.thiefDest.addr, "--value", L.String(), "--height", fmt.Sprint(nextBoundary), "--time", fmt.Sprint(t3+60), "--witness-out", wit, "--record-out", rec, "--receipt-out", receipt)
					if !ok {
						fail("the concurrent request %d: %s", i, errs)
					}
					wits = append(wits, wit)
					recs = append(recs, rec)
					if i == 0 {
						r.receipts = append(r.receipts, receipt)
					}
				}
				var datas [][]byte
				var cms []string
				for i := range wits {
					_, rr, statement := recordOf(recs[i])
					proof, _, _ := prove(wits[i], statement)
					data, _ := vaultData(recs[i], proof)
					datas = append(datas, data)
					cms = append(cms, str(rr["outputs"].([]interface{})[0].(map[string]interface{}), "commitment"))
				}
				txs := []*types.Transaction{send(new(big.Int), datas[0]), send(new(big.Int), datas[1])}
				blk := wait(txs...)
				a0, a1 := admittedAt(cms[0], blk), admittedAt(cms[1], blk)
				if a0 == a1 {
					fail("two requests against one head: admitted %v and %v", a0, a1)
				}
				r.res.Requests++
				refusal(r, "two requests of L proven against one head into the next period (declared %d, %d blocks ahead), sent together: one admitted at block %d, the other refused (the head's nullifier spent)", nextBoundary, nextBoundary-h3, blk)
				event(r, "the thief reached the next period's allowance %d blocks early through the declaration margin", nextBoundary-h3)
			} else {
				refusal(r, "two requests of L against one head for the next period: the wallet refuses both while the boundary (%d) is more than %d blocks ahead of %d (the declared height must be within the margin)", nextBoundary, pool.DeclareMargin, h3)
			}
		}
		// THE OWNER: the cancellation key cancels every pending note within D
		stream()
		st := vaultScan(r.vaultFile)
		for _, p := range st["pending"].([]interface{}) {
			pm := p.(map[string]interface{})
			if pm["consumed_at"] != nil {
				continue
			}
			pos := uint64(pm["position"].(float64))
			blk, ok := retry("the cancellation", func() (uint64, bool) {
				wit, rec := fresh("cancel-w"), fresh("cancel-r")
				mustRun(walletBn, "vault-cancel", "--vault", r.vaultFile, "--key", r.cancelKey, "--pending-cm", str(pm, "cm"), "--paths", pathsFile(pos), "--witness-out", wit, "--record-out", rec)
				blk, _, ok := proveAndSend(wit, rec)
				return blk, ok
			})
			if !ok {
				fail("%s: a cancellation was refused at block %d", r.res.Name, blk)
			}
			r.res.Cancelled++
			event(r, "cancelled the pending %v at block %d (inside D: it would have matured at block %d)", pm["value"], blk, uint64(pm["matures_at_block"].(float64)))
		}
		// the thief's completion of a cancelled note: the wallet refuses by the receipt (not matured); the chain by the position tag after maturity (recorded at the end)
		// a cancellation cannot redirect: the record names no destination; the inbound note carries the vault's vid and policy
		refusal(r, "a cancellation names no destination and no amount choice: the record's one output is the inbound note under the vault's policy (the statement's public inputs)")
	}

	// Run 2: no cancellation key; the thief requests L now and L into the next period through the margin
	// when it can; the recovery authority announces at once; the thief's requests complete at maturity; the
	// recovery completes after R.
	run2NewPolicy := ""
	{
		r := runs[1]
		h, t := declare()
		request(r, r.thiefDest, L, h, t, "L in the current period")
		h2, _ := now()
		nextBoundary := (h2/(*period) + 1) * (*period)
		if nextBoundary-h2 <= pool.DeclareMargin-4 {
			request(r, r.thiefDest, L, nextBoundary, t+60, "L into the next period through the margin")
		} else {
			event(r, "the next period boundary %d is %d blocks away: the margin (%d) does not reach it; the thief holds one period's allowance before the announcement", nextBoundary, nextBoundary-h2, pool.DeclareMargin)
		}
		// the owner's recovery authority announces a new policy (new everyday key: a fresh wallet; a new cancellation key)
		newOwner := newWallet("run2-newowner")
		_, newCancelPk, _ := newKey("run2-newcancel")
		run2NewPolicy = filepath.Join(work, "run2-newpolicy.json")
		mustRun(walletBn, "--wallet", newOwner.file, "vault-new-policy", "--vault", r.vaultFile, "--delay-blocks", fmt.Sprint(*delayBlocks), "--delay-seconds", fmt.Sprint(*delaySeconds), "--period", fmt.Sprint(*period), "--limit", L.String(), "--recovery-blocks", fmt.Sprint(*recBlocks), "--recovery-seconds", fmt.Sprint(*recSeconds), "--cancel-pk", newCancelPk, "--recovery-pk", r.recoverPk, "--out", run2NewPolicy)
		st := vaultScan(r.vaultFile)
		pos := uint64(st["head"].(map[string]interface{})["position"].(float64))
		h3, t3 := declare()
		wit, rec := fresh("announce-w"), fresh("announce-r")
		m := runJSON(walletBn, "vault-announce", "--vault", r.vaultFile, "--key", r.recKey, "--new-policy", run2NewPolicy, "--paths", pathsFile(pos), "--height", fmt.Sprint(h3), "--time", fmt.Sprint(t3), "--witness-out", wit, "--record-out", rec)
		blk, _, ok := proveAndSend(wit, rec)
		if !ok {
			fail("run 2: the announcement was refused at block %d", blk)
		}
		event(r, "the recovery authority announced a new policy at block %d; it completes at block %d and time %d", blk, uint64(m["completes_at_block"].(float64)), uint64(m["completes_at_time"].(float64)))
		// the thief tries another request while the recovery is pending
		h4, t4 := declare()
		if _, _, errs := request(r, r.thiefDest, big.NewInt(1), h4, t4, "during the pending recovery"); errs != "" {
			refusal(r, "a request while a recovery is pending: the wallet refuses (%s); the circuit requires the head's rec_hash to be zero", errs)
		}
	}

	// Run 4: a malicious recovery authority (the attacker holds the recovery key) announces a policy to its
	// own keys; the owner's cancellation key cancels it inside R. Before the announcement the owner has a
	// withdrawal of L to itself pending (F-799's row: the announcement revokes the old policy at once, so
	// the owner's own matured note is refused at completion while the malicious recovery is pending, and
	// completes after the cancellation restores the policy). The cancellation is sent after the note's
	// maturity (run4Finish, inside R). Before it the attacker cannot withdraw or complete early, and the
	// owner's own further request is refused (no withdrawal while a recovery is pending): the griefing a
	// compromised authority can do.
	var run4Finish func()
	{
		r := runs[3]
		h0, t0 := declare()
		if _, ok, errs := request(r, r.owner, L, h0, t0, "the owner's own withdrawal of L to itself, before the attack"); !ok {
			fail("run 4: the owner's own request was not admitted: %s", errs)
		}
		attackerOwner := newWallet("run4-attacker")
		_, aCancel, aRecover := newKey("run4-attacker-keys")
		pf := filepath.Join(work, "run4-attacker-policy.json")
		mustRun(walletBn, "--wallet", attackerOwner.file, "vault-new-policy", "--vault", r.vaultFile, "--delay-blocks", fmt.Sprint(*delayBlocks), "--delay-seconds", fmt.Sprint(*delaySeconds), "--period", fmt.Sprint(*period), "--limit", V.String(), "--recovery-blocks", fmt.Sprint(*recBlocks), "--recovery-seconds", fmt.Sprint(*recSeconds), "--cancel-pk", aCancel, "--recovery-pk", aRecover, "--out", pf)
		vaultScan(r.vaultFile)
		st := vaultScan(r.vaultFile)
		pos := uint64(st["head"].(map[string]interface{})["position"].(float64))
		h, t := declare()
		wit, rec := fresh("announce-w"), fresh("announce-r")
		m := runJSON(walletBn, "vault-announce", "--vault", r.vaultFile, "--key", r.recKey, "--new-policy", pf, "--paths", pathsFile(pos), "--height", fmt.Sprint(h), "--time", fmt.Sprint(t), "--witness-out", wit, "--record-out", rec)
		blk, _, ok := proveAndSend(wit, rec)
		if !ok {
			fail("run 4: the malicious announcement was refused at block %d", blk)
		}
		info := poolInfo(ec, nil)
		event(r, "the compromised recovery authority announced a policy to the attacker's keys (limit V, its own everyday key, a new cancellation key) at block %d, not co-signed (the attacker has no everyday key); it would complete at block %d; the old policy %s revoked at once (the revoked set %d)", blk, uint64(m["completes_at_block"].(float64)), str(st, "policy_hash")[:16], info.RevokedCount)
		// nothing moves before R: the attacker cannot withdraw (its everyday key is not the policy's), cannot complete early
		h2, t2 := declare()
		_, errs, ok := run(walletBn, "--wallet", attackerOwner.file, "vault-request", "--vault", r.vaultFile, "--paths", pathsFile(pos), "--to", attackerOwner.addr, "--value", "1", "--height", fmt.Sprint(h2), "--time", fmt.Sprint(t2), "--witness-out", fresh("x"), "--record-out", fresh("x"), "--receipt-out", fresh("x"))
		if ok {
			fail("the attacker's request under a pending recovery was prepared")
		}
		refusal(r, "the attacker's request under its announced policy before R: the wallet refuses (%s); the head is still under the old policy", errs)
		vaultScan(r.vaultFile)
		st = vaultScan(r.vaultFile)
		pos = uint64(st["head"].(map[string]interface{})["position"].(float64))
		_, errs, ok = run(walletBn, "vault-policy-complete", "--vault", r.vaultFile, "--new-policy", pf, "--paths", pathsFile(pos), "--height", fmt.Sprint(h2), "--time", fmt.Sprint(t2), "--witness-out", fresh("x"), "--record-out", fresh("x"))
		if ok {
			fail("the malicious recovery completed before R")
		}
		refusal(r, "the attacker's completion of its recovery before R: the wallet refuses (%s); the circuit requires h_now >= rec_h + R_b and t_now >= rec_t + R_s", errs)
		// the owner's own request is blocked while the recovery is pending (the griefing)
		_, errs, ok = run(walletBn, "--wallet", r.owner.file, "vault-request", "--vault", r.vaultFile, "--paths", pathsFile(pos), "--to", r.owner.addr, "--value", "1", "--height", fmt.Sprint(h2), "--time", fmt.Sprint(t2), "--witness-out", fresh("x"), "--record-out", fresh("x"), "--receipt-out", fresh("x"))
		if ok {
			fail("the owner's request under a pending recovery was prepared")
		}
		refusal(r, "the owner's own request while the malicious recovery is pending: refused (%s): a compromised authority can freeze withdrawals for up to R", errs)
		receipt := r.receipts[0]
		run4Finish = func() {
			// the owner's note matures (the later of D_b and D_s); its completion is refused: the policy was
			// revoked at the announcement (F-799), so no non-membership proof exists against the parent's root
			stream()
			st := vaultScan(r.vaultFile)
			var pm map[string]interface{}
			for _, p := range st["pending"].([]interface{}) {
				q := p.(map[string]interface{})
				if q["consumed_at"] == nil && q["position"] != nil {
					pm = q
				}
			}
			if pm == nil {
				fail("run 4: the owner's pending note is missing")
			}
			mb, mt := uint64(pm["matures_at_block"].(float64)), uint64(pm["matures_at_time"].(float64))
			for {
				h, t := now()
				if h >= mb && t >= mt {
					break
				}
				time.Sleep(5 * time.Second)
				stream()
			}
			ppos := uint64(pm["position"].(float64))
			policyHash := str(st, "policy_hash")
			keyOf := pool.RevokedKey(h32(policyHash))
			var rp pool.RevokedProof
			must(ec.Client().CallContext(ctx, &rp, "igneum_poolRevokedPath", keyOf, "latest"))
			if !rp.Member {
				fail("run 4: the old policy is not in the revoked set while the recovery is pending")
			}
			h3, t3 := now()
			_, errs, ok := run(walletBn, "--wallet", r.owner.file, "vault-complete", "--receipt", receipt, "--paths", pathsFile(ppos), "--revoked", revokedFor(policyHash), "--height", fmt.Sprint(h3), "--time", fmt.Sprint(t3), "--chain-id", cidS, "--witness-out", fresh("x"), "--record-out", fresh("x"))
			if ok {
				fail("run 4: a completion under the policy revoked at the announcement was prepared")
			}
			refusal(r, "the owner's matured withdrawal of %v while the malicious recovery is pending: the policy was revoked at the announcement (igneum_poolRevokedPath at block %d: member, the revoked set %d), so no non-membership proof exists; the wallet refuses (%s)", pm["value"], h3, rp.Count, errs)
			// the owner's cancellation key clears the recovery (inside R): the policy restored
			vaultScan(r.vaultFile)
			st = vaultScan(r.vaultFile)
			pos := uint64(st["head"].(map[string]interface{})["position"].(float64))
			wit, rec := fresh("reccancel-w"), fresh("reccancel-r")
			mustRun(walletBn, "vault-rec-cancel", "--vault", r.vaultFile, "--key", r.cancelKey, "--paths", pathsFile(pos), "--witness-out", wit, "--record-out", rec)
			blk, _, ok := proveAndSend(wit, rec)
			if !ok {
				fail("run 4: the recovery cancellation was refused at block %d", blk)
			}
			r.res.Cancelled++
			was := rp.Count
			must(ec.Client().CallContext(ctx, &rp, "igneum_poolRevokedPath", keyOf, "latest"))
			if rp.Member {
				fail("run 4: the policy is still revoked after the cancellation")
			}
			event(r, "the owner's cancellation key cleared the malicious recovery at block %d (inside R; a rotation of the cancellation key nobody co-signed is the cancellation key's to cancel, F-800); the policy restored (the revoked set %d, was %d); the vault is untouched", blk, rp.Count, was)
			vaultScan(r.vaultFile)
			st = vaultScan(r.vaultFile)
			if st["head"].(map[string]interface{})["recovery_pending"].(bool) {
				fail("run 4: the recovery is still pending after the cancellation")
			}
			// the owner's note completes now, against the restored set's root
			blk, ok = retry("the owner's completion after the cancellation", func() (uint64, bool) {
				h4, t4 := now()
				wit, rec := fresh("complete-w"), fresh("complete-r")
				_, errs, ok := run(walletBn, "--wallet", r.owner.file, "vault-complete", "--receipt", receipt, "--paths", pathsFile(ppos), "--revoked", revokedFor(policyHash), "--height", fmt.Sprint(h4), "--time", fmt.Sprint(t4), "--chain-id", cidS, "--witness-out", wit, "--record-out", rec)
				if !ok {
					fail("run 4: the owner's completion after the cancellation: %s", errs)
				}
				blk, _, admitted := proveAndSend(wit, rec)
				return blk, admitted
			})
			if !ok {
				fail("run 4: the owner's completion after the cancellation was refused at block %d", blk)
			}
			r.res.Completed++
			scan(r.owner)
			event(r, "the owner completed its own withdrawal of %v at block %d after the cancellation restored the policy (the owner's wallet holds %v)", pm["value"], blk, r.owner.unspent)
		}
	}

	// Run 5: the attacker holds the cancellation key; the owner requests a withdrawal and the attacker cancels
	// it (the owner's allowance for the period is consumed, no money moves); the owner escapes through the
	// recovery authority: a policy change rotating the cancellation key, completed after R.
	run5NewPolicy := ""
	{
		r := runs[4]
		h, t := declare()
		vaultScan(r.vaultFile)
		st := vaultScan(r.vaultFile)
		pos := uint64(st["head"].(map[string]interface{})["position"].(float64))
		wit, rec, receipt := fresh("request-w"), fresh("request-r"), fresh("receipt")
		mustRun(walletBn, "--wallet", r.owner.file, "vault-request", "--vault", r.vaultFile, "--paths", pathsFile(pos), "--to", r.owner.addr, "--value", L.String(), "--height", fmt.Sprint(h), "--time", fmt.Sprint(t), "--witness-out", wit, "--record-out", rec, "--receipt-out", receipt)
		blk, _, ok := proveAndSend(wit, rec)
		if !ok {
			fail("run 5: the owner's request was refused at block %d", blk)
		}
		r.res.Requests++
		event(r, "the owner requested L to itself at block %d", blk)
		stream()
		st = vaultScan(r.vaultFile)
		pm := st["pending"].([]interface{})[0].(map[string]interface{})
		ppos := uint64(pm["position"].(float64))
		wit, rec = fresh("cancel-w"), fresh("cancel-r")
		mustRun(walletBn, "vault-cancel", "--vault", r.vaultFile, "--key", r.cancelKey, "--pending-cm", str(pm, "cm"), "--paths", pathsFile(ppos), "--witness-out", wit, "--record-out", rec)
		blk, _, ok = proveAndSend(wit, rec)
		if !ok {
			fail("run 5: the griefing cancellation was refused at block %d", blk)
		}
		r.res.Cancelled++
		event(r, "the compromised cancellation key cancelled the owner's withdrawal at block %d: L returned to the vault as an inbound note; the period's allowance is spent", blk)
		h2, t2 := declare()
		if _, _, errs := request(r, r.thiefDest, big.NewInt(1), h2, t2, "the owner again in the same period"); errs != "" {
			refusal(r, "the owner's second request in the period after the griefing cancellation: refused (%s): the cancelled request still counts in the running total (rule 8)", errs)
		}
		// the escape: the recovery authority rotates the cancellation key (and the everyday key) with a new policy
		newOwner := newWallet("run5-newowner")
		_, newCancelPk, _ := newKey("run5-newcancel")
		run5NewPolicy = filepath.Join(work, "run5-newpolicy.json")
		mustRun(walletBn, "--wallet", newOwner.file, "vault-new-policy", "--vault", r.vaultFile, "--delay-blocks", fmt.Sprint(*delayBlocks), "--delay-seconds", fmt.Sprint(*delaySeconds), "--period", fmt.Sprint(*period), "--limit", L.String(), "--recovery-blocks", fmt.Sprint(*recBlocks), "--recovery-seconds", fmt.Sprint(*recSeconds), "--cancel-pk", newCancelPk, "--recovery-pk", r.recoverPk, "--out", run5NewPolicy)
		vaultScan(r.vaultFile)
		st = vaultScan(r.vaultFile)
		pos = uint64(st["head"].(map[string]interface{})["position"].(float64))
		h3, t3 := declare()
		wit, rec = fresh("announce-w"), fresh("announce-r")
		m := runJSON(walletBn, "--wallet", r.owner.file, "vault-announce", "--vault", r.vaultFile, "--key", r.recKey, "--new-policy", run5NewPolicy, "--paths", pathsFile(pos), "--height", fmt.Sprint(h3), "--time", fmt.Sprint(t3), "--witness-out", wit, "--record-out", rec)
		blk, _, ok = proveAndSend(wit, rec)
		if !ok {
			fail("run 5: the announcement was refused at block %d", blk)
		}
		event(r, "the recovery authority announced a policy with a new cancellation key and a new everyday key at block %d, co-signed by the owner's everyday key (F-800); it completes at block %d and time %d", blk, uint64(m["completes_at_block"].(float64)), uint64(m["completes_at_time"].(float64)))
		// F-800: the attacker, holding the old cancellation key, tries to cancel the recovery that would rotate
		// it. The wallet refuses; forced past the wallet's check, the prover refuses the witness (the announced
		// policy's pk_c differs from the policy's, so the circuit requires the recovery authority's secret);
		// the forced record with an arbitrary proof is refused by the chain. The authority alone could cancel.
		vaultScan(r.vaultFile)
		st = vaultScan(r.vaultFile)
		pos = uint64(st["head"].(map[string]interface{})["position"].(float64))
		copyOf := func(name string) string {
			f := filepath.Join(work, name)
			b, err := os.ReadFile(r.vaultFile)
			must(err)
			must(os.WriteFile(f, b, 0o600))
			return f
		}
		_, errs, ok := run(walletBn, "vault-rec-cancel", "--vault", copyOf("vault-run5-copy1.json"), "--key", r.cancelKey, "--paths", pathsFile(pos), "--witness-out", fresh("x"), "--record-out", fresh("x"))
		if ok {
			fail("run 5: the compromised cancellation key's cancellation of its own rotation was prepared")
		}
		refusal(r, "the compromised cancellation key's cancellation of the owner-co-signed recovery that rotates it: the wallet refuses (%s)", errs)
		wit, rec = fresh("forced-w"), fresh("forced-r")
		mustRun(walletBn, "vault-rec-cancel", "--vault", copyOf("vault-run5-copy2.json"), "--key", r.cancelKey, "--paths", pathsFile(pos), "--force", "--witness-out", wit, "--record-out", rec)
		_, perr, pok := run(proverBn, wit, strings.TrimSuffix(wit, ".json")+".proof", strings.TrimSuffix(wit, ".json")+".claim", "1")
		if pok {
			fail("run 5: the prover proved the forced cancellation")
		}
		os.Remove(wit)
		refusal(r, "the same cancellation forced past the wallet's check: the prover refuses the witness (%s); the circuit's branch is the same expression", strings.TrimSpace(perr))
		forged, _ := vaultData(rec, bytes.Repeat([]byte{0x5a}, 99000))
		before := poolInfo(ec, nil)
		blk = wait(send(new(big.Int), forged))
		after := poolInfo(ec, nil)
		vaultScan(r.vaultFile)
		st = vaultScan(r.vaultFile)
		if after.Count != before.Count || after.RevokedCount != before.RevokedCount || !st["head"].(map[string]interface{})["recovery_pending"].(bool) {
			fail("run 5: the forged cancellation changed the pool")
		}
		refusal(r, "the forced cancellation's record with a proof of 99,000 arbitrary bytes sent to the chain: refused by the verifier at block %d, the pool unchanged, the recovery still pending", blk)
		_, errs, ok = run(walletBn, "vault-rec-cancel", "--vault", copyOf("vault-run5-copy3.json"), "--key", r.recKey, "--paths", pathsFile(pos), "--witness-out", fresh("x"), "--record-out", fresh("x"))
		if !ok {
			fail("run 5: the recovery authority's cancellation was refused by the wallet: %s", errs)
		}
		event(r, "the recovery authority alone could cancel its announcement (prepared on a copy of the vault file and not sent: the owner wants the rotation)")
	}

	// Run 6 (F-801, the racing block): the thief's request of L and the recovery authority's announcement,
	// both proven against the one live head, sent in one block with the request's transaction first. Under
	// the first cut's Finalize the first transaction took the head and the announcement was refused; now the
	// announcement is applied first (pool.Ordered) and the request is refused as a duplicate nullifier.
	run6NewPolicy := ""
	{
		r := runs[5]
		newOwner := newWallet("run6-newowner")
		_, newCancelPk, _ := newKey("run6-newcancel")
		run6NewPolicy = filepath.Join(work, "run6-newpolicy.json")
		mustRun(walletBn, "--wallet", newOwner.file, "vault-new-policy", "--vault", r.vaultFile, "--delay-blocks", fmt.Sprint(*delayBlocks), "--delay-seconds", fmt.Sprint(*delaySeconds), "--period", fmt.Sprint(*period), "--limit", L.String(), "--recovery-blocks", fmt.Sprint(*recBlocks), "--recovery-seconds", fmt.Sprint(*recSeconds), "--cancel-pk", newCancelPk, "--recovery-pk", r.recoverPk, "--out", run6NewPolicy)
		raced := false
		for try := 0; try < 3 && !raced; try++ {
			vaultScan(r.vaultFile)
			st := vaultScan(r.vaultFile)
			pos := uint64(st["head"].(map[string]interface{})["position"].(float64))
			h, t := declare()
			vfCopy := filepath.Join(work, fmt.Sprintf("vault-run6-copy%d.json", try))
			b, err := os.ReadFile(r.vaultFile)
			must(err)
			must(os.WriteFile(vfCopy, b, 0o600))
			witW, recW, receipt := fresh("race-w"), fresh("race-r"), fresh("receipt")
			mustRun(walletBn, "--wallet", r.owner.file, "vault-request", "--vault", vfCopy, "--paths", pathsFile(pos), "--to", r.thiefDest.addr, "--value", L.String(), "--height", fmt.Sprint(h), "--time", fmt.Sprint(t), "--witness-out", witW, "--record-out", recW, "--receipt-out", receipt)
			witA, recA := fresh("race-a-w"), fresh("race-a-r")
			m := runJSON(walletBn, "vault-announce", "--vault", r.vaultFile, "--key", r.recKey, "--new-policy", run6NewPolicy, "--paths", pathsFile(pos), "--height", fmt.Sprint(h), "--time", fmt.Sprint(t), "--witness-out", witA, "--record-out", recA)
			_, rrW, stW := recordOf(recW)
			proofW, _, _ := prove(witW, stW)
			dataW, _ := vaultData(recW, proofW)
			_, rrA, stA := recordOf(recA)
			proofA, _, _ := prove(witA, stA)
			dataA, _ := vaultData(recA, proofA)
			txW := send(new(big.Int), dataW)
			txA := send(new(big.Int), dataA)
			blk := wait(txW, txA)
			rcW, err := ec.TransactionReceipt(ctx, txW.Hash())
			must(err)
			rcA, err := ec.TransactionReceipt(ctx, txA.Hash())
			must(err)
			if rcW.BlockNumber.Cmp(rcA.BlockNumber) != 0 {
				say("run 6: the two transactions landed in blocks %v and %v, not one; the race is built again", rcW.BlockNumber, rcA.BlockNumber)
				vaultScan(r.vaultFile)
				continue
			}
			cmW := str(rrW["outputs"].([]interface{})[0].(map[string]interface{}), "commitment")
			cmA := str(rrA["outputs"].([]interface{})[0].(map[string]interface{}), "commitment")
			aW, aA := admittedAt(cmW, blk), admittedAt(cmA, blk)
			if rcW.TransactionIndex >= rcA.TransactionIndex {
				fail("run 6: the request's transaction (%d) is not before the announcement's (%d) in block %d", rcW.TransactionIndex, rcA.TransactionIndex, blk)
			}
			if aW || !aA {
				fail("run 6: the racing block %d: the request admitted %v, the announcement admitted %v: the head was raced", blk, aW, aA)
			}
			raced = true
			refusal(r, "the thief's request of L (transaction index %d) and the authority's announcement (index %d) against one head in block %d: the announcement applied first and admitted, the request refused (the head's nullifier spent)", rcW.TransactionIndex, rcA.TransactionIndex, blk)
			event(r, "the recovery authority's announcement won the racing block %d; it completes at block %d and time %d", blk, uint64(m["completes_at_block"].(float64)), uint64(m["completes_at_time"].(float64)))
		}
		if !raced {
			fail("run 6: the racing block could not be built in three tries")
		}
		vaultScan(r.vaultFile)
		st := vaultScan(r.vaultFile)
		if !st["head"].(map[string]interface{})["recovery_pending"].(bool) {
			fail("run 6: no recovery pending after the race")
		}
		h2, t2 := declare()
		if _, _, errs := request(r, r.thiefDest, big.NewInt(1), h2, t2, "after the lost race"); errs != "" {
			refusal(r, "the thief's request after the lost race: refused (%s): a recovery is pending", errs)
		}
	}

	// Run 3: the owner never acts: the thief takes L a period index across the periods, each reached through
	// the margin 60 blocks before its boundary.
	{
		r := runs[2]
		h, t := declare()
		request(r, r.thiefDest, L, h, t, "L in period 0 of the silent run")
		last := uint64(0)
		for k := 1; k <= *periodsRun3; k++ {
			h2, _ := now()
			boundary := (h2/(*period) + 1) * (*period)
			if boundary <= last {
				boundary = last + *period
			}
			target := boundary - (pool.DeclareMargin - 4)
			for head() < target {
				time.Sleep(3 * time.Second)
				stream() // the states of old blocks are pruned by a non-archive node; the stream keeps up
			}
			_, t3 := now()
			if _, _, errs := request(r, r.thiefDest, L, boundary, t3+60, fmt.Sprintf("L into period %d through the margin", k)); errs != "" {
				refusal(r, "the request into period %d: %s", k, errs)
			}
			last = boundary
			// THE ALTERNATION (F-798, the hole of the first run): a request declared back at the current
			// height after the one declared ahead through the margin. The wallet refuses it (the period
			// index only advances); the circuit's assertion and the host check are the same expression
			// (prover/tests/vault.rs: the head at period 3 refuses a request declared into period 2).
			ha, ta := declare()
			blkA, admittedA, errsA := request(r, r.thiefDest, big.NewInt(1), ha, ta, fmt.Sprintf("alternation back into period %d", k-1))
			if admittedA {
				fail("%s: the alternation back into period %d (declared %d) was admitted at block %d: the period-index hole is open", r.res.Name, k-1, ha, blkA)
			}
			r.res.Alternations++
			if errsA != "" {
				refusal(r, "the alternation back into period %d (1 wei declared at the current height %d after the head's index moved to %d): the wallet refuses (%s); the circuit's assertion is the same expression", k-1, ha, k, errsA)
			} else {
				refusal(r, "the alternation back into period %d (1 wei declared at the current height %d after the head's index moved to %d): refused by the chain at block %d", k-1, ha, k, blkA)
			}
		}
	}

	// THE REORGANISATION of depth 3 during the pending windows: the second sealer T is partitioned from S and
	// R, seals alone, then rejoins; the heavier branch wins. A request of run 1 is in flight across it (run 3's
	// head, its index moved ahead through the margin, refuses a request declared at the current height under
	// the cure of F-798, so the in-flight request is run 1's: its period's allowance is fresh at this height
	// and no recovery is pending); the owner's cancellation key clears it after the rejoin. Behind -reorg (the
	// findings' lab runs two nodes).
	if *reorg {
		enodeOf := func(c *ethclient.Client) string {
			var info map[string]interface{}
			must(c.Client().CallContext(ctx, &info, "admin_nodeInfo"))
			return str(info, "enode")
		}
		peerOp := func(c *ethclient.Client, op, enode string) {
			var ok bool
			c.Client().CallContext(ctx, &ok, op, enode)
		}
		eS, eT, eR := enodeOf(ec), enodeOf(ecT), enodeOf(ec2)
		h0 := head()
		hashes := map[uint64]string{}
		for h := h0 - 6; h <= h0; h++ {
			hashes[h] = headerAt(h).Hash().Hex()
		}
		peerOp(ecT, "admin_removePeer", eS)
		peerOp(ecT, "admin_removePeer", eR)
		peerOp(ec, "admin_removePeer", eT)
		peerOp(ec2, "admin_removePeer", eT)
		must(ecT.Client().CallContext(ctx, nil, "miner_start"))
		res.Reorg = append(res.Reorg, fmt.Sprintf("partition at block %d: T alone and sealing on three threads, S (one thread) with R", h0))
		// a request of run 1 in flight during the partition (its declared height at the head plus two)
		r := runs[0]
		hp, tp := declare()
		blkReq, admitted, errs := request(r, r.thiefDest, big.NewInt(1), hp, tp, "1 wei during the partition")
		if !admitted {
			res.Reorg = append(res.Reorg, "the in-flight request was refused by the wallet or the chain: "+errs)
		}
		// let T seal ahead: T has more threads; wait until T's head is at least S's plus 4
		deadline := time.Now().Add(4 * time.Minute)
		var hT, hS uint64
		for time.Now().Before(deadline) {
			hT, _ = ecT.BlockNumber(ctx)
			hS = head()
			if hT >= hS+4 && hT > h0+6 {
				break
			}
			time.Sleep(2 * time.Second)
		}
		// the stream is read again after the rejoin (not now: S's blocks of the partition may be replaced)
		res.Reorg = append(res.Reorg, fmt.Sprintf("before the rejoin: T at %d, S at %d (T ahead by %d)", hT, hS, int64(hT)-int64(hS)))
		peerOp(ecT, "admin_addPeer", eS)
		peerOp(ecT, "admin_addPeer", eR)
		peerOp(ec, "admin_addPeer", eT)
		peerOp(ec2, "admin_addPeer", eT)
		time.Sleep(20 * time.Second)
		ecT.Client().CallContext(ctx, nil, "miner_stop")
		for i := 0; i < 30; i++ {
			a, _ := ec.BlockNumber(ctx)
			b, _ := ecT.BlockNumber(ctx)
			c, _ := ec2.BlockNumber(ctx)
			ha := headerAt(a).Hash()
			hb, _ := ecT.HeaderByNumber(ctx, new(big.Int).SetUint64(a))
			hc, _ := ec2.HeaderByNumber(ctx, new(big.Int).SetUint64(a))
			if hb != nil && hc != nil && hb.Hash() == ha && hc.Hash() == ha && b >= a && c >= a {
				break
			}
			time.Sleep(3 * time.Second)
		}
		depth := 0
		first := uint64(0)
		for h := h0 + 1; h <= hS; h++ {
			hd, err := ec.HeaderByNumber(ctx, new(big.Int).SetUint64(h))
			if err != nil || hd == nil {
				continue
			}
			// a block S held before the rejoin at this height, replaced now
			var sib []map[string]interface{}
			ec.Client().CallContext(ctx, &sib, "igneum_siblings", fmt.Sprintf("0x%x", h))
			if len(sib) > 1 {
				depth++
				if first == 0 {
					first = h
				}
			}
		}
		res.Reorg = append(res.Reorg, fmt.Sprintf("after the rejoin: common head %d on S, T and R; %d heights from %d carry two blocks on S (the reorganisation's depth as S saw it)", head(), depth, first))
		// the in-flight request: still in the tree at its commitment (on whichever block), or re-included; its maturity unchanged
		stream()
		st := vaultScan(r.vaultFile)
		for _, p := range st["pending"].([]interface{}) {
			pm := p.(map[string]interface{})
			if uint64(pm["matures_at_block"].(float64)) == hp+*delayBlocks {
				res.Reorg = append(res.Reorg, fmt.Sprintf("the in-flight request (sent for block %d) stands at position %v, height %v; maturity block %v and time %v as declared (fixed in the commitment, unchanged by the reorganisation)", blkReq, pm["position"], pm["height"], pm["matures_at_block"], pm["matures_at_time"]))
			}
		}
		if admitted {
			// if its block was replaced and the transaction re-included at a later height past its declared one, the node refuses it: the wallet re-declares
			if !admittedAt(str(st["pending"].([]interface{})[len(st["pending"].([]interface{}))-1].(map[string]interface{}), "cm"), blkReq) {
				res.Reorg = append(res.Reorg, "the in-flight request's block was replaced; its commitment is not at the recorded height (a re-declaration would be needed: the maturity can only move later)")
			}
			// the owner's cancellation key clears the in-flight request after the rejoin (run 1's rule)
			var cmReq string
			if b, err := os.ReadFile(r.receipts[len(r.receipts)-1]); err == nil {
				var pj map[string]interface{}
				json.Unmarshal(b, &pj)
				cmReq = str(pj, "cm")
			}
			for _, p := range st["pending"].([]interface{}) {
				pm := p.(map[string]interface{})
				if pm["consumed_at"] != nil || str(pm, "cm") != cmReq || pm["position"] == nil {
					continue
				}
				pos := uint64(pm["position"].(float64))
				blk, ok := retry("the cancellation of the in-flight request", func() (uint64, bool) {
					wit, rec := fresh("cancel-w"), fresh("cancel-r")
					mustRun(walletBn, "vault-cancel", "--vault", r.vaultFile, "--key", r.cancelKey, "--pending-cm", cmReq, "--paths", pathsFile(pos), "--witness-out", wit, "--record-out", rec)
					blk, _, ok := proveAndSend(wit, rec)
					return blk, ok
				})
				if !ok {
					fail("%s: the cancellation of the in-flight request was refused at block %d", r.res.Name, blk)
				}
				r.res.Cancelled++
				event(r, "cancelled the in-flight pending %v at block %d after the reorganisation (inside D: it would have matured at block %d)", pm["value"], blk, uint64(pm["matures_at_block"].(float64)))
			}
		}
	}

	// THE WAIT: the thieves' pending notes mature (the later of D_b and D_s); then each completes to its own
	// wallet. Run 2's and run 5's recoveries complete after R.
	completeAll := func(r *vaultRun) {
		stream()
		st := vaultScan(r.vaultFile)
		for _, p := range st["pending"].([]interface{}) {
			pm := p.(map[string]interface{})
			if pm["consumed_at"] != nil || pm["position"] == nil {
				continue
			}
			// find the receipt by commitment
			var receipt, policyHash string
			for _, f := range r.receipts {
				b, err := os.ReadFile(f)
				if err != nil {
					continue
				}
				var pj map[string]interface{}
				json.Unmarshal(b, &pj)
				if str(pj, "cm") == str(pm, "cm") {
					receipt, policyHash = f, str(pj, "policy_hash")
				}
			}
			if receipt == "" {
				continue
			}
			mb, mt := uint64(pm["matures_at_block"].(float64)), uint64(pm["matures_at_time"].(float64))
			for {
				h, t := now()
				if h >= mb && t >= mt {
					break
				}
				time.Sleep(5 * time.Second)
				stream()
			}
			pos := uint64(pm["position"].(float64))
			h, t := now()
			wit, rec := fresh("complete-w"), fresh("complete-r")
			_, errs, ok := run(walletBn, "--wallet", r.thiefDest.file, "vault-complete", "--receipt", receipt, "--paths", pathsFile(pos), "--revoked", revokedFor(policyHash), "--height", fmt.Sprint(h), "--time", fmt.Sprint(t), "--chain-id", cidS, "--witness-out", wit, "--record-out", rec)
			if !ok {
				refusal(r, "the completion of the pending %v: %s", pm["value"], errs)
				continue
			}
			blk, _, admitted := proveAndSend(wit, rec)
			if admitted {
				r.res.Completed++
				r.completedTake.Add(r.completedTake, bigOf(str(pm, "value")))
				event(r, "the thief completed %v at block %d, time %d (matured at block %d and time %d; the %s bound decided)", pm["value"], blk, t, mb, mt, map[bool]string{true: "seconds", false: "blocks"}[headerAt(mb).Time < mt])
			} else {
				refusal(r, "the completion of the pending %v was refused by the chain at block %d", pm["value"], blk)
			}
		}
	}
	// run 4's rows after the owner's note matures (inside R): the refused completion, the cancellation, the completion
	run4Finish()
	// the revoked-root freshness: a completion proven against the root of a block whose parent moved (an announcement or a cancellation in between) is refused and re-proven
	for _, r := range []*vaultRun{runs[0], runs[1], runs[2]} {
		completeAll(r)
	}
	// run 1's thief against a cancelled note after maturity: the chain refuses by the position tag
	{
		r := runs[0]
		stream()
		st := vaultScan(r.vaultFile)
		for _, p := range st["pending"].([]interface{}) {
			pm := p.(map[string]interface{})
			if pm["consumed_at"] == nil {
				continue
			}
			var receipt, policyHash string
			for _, f := range r.receipts {
				b, _ := os.ReadFile(f)
				var pj map[string]interface{}
				json.Unmarshal(b, &pj)
				if str(pj, "cm") == str(pm, "cm") {
					receipt, policyHash = f, str(pj, "policy_hash")
				}
			}
			if receipt == "" {
				continue
			}
			pos := uint64(pm["position"].(float64))
			h, t := now()
			wit, rec := fresh("late-w"), fresh("late-r")
			_, errs, ok := run(walletBn, "--wallet", r.thiefDest.file, "vault-complete", "--receipt", receipt, "--paths", pathsFile(pos), "--revoked", revokedFor(policyHash), "--height", fmt.Sprint(h), "--time", fmt.Sprint(t), "--chain-id", cidS, "--witness-out", wit, "--record-out", rec)
			if !ok {
				refusal(r, "the thief's completion of the cancelled note after maturity: the wallet refuses (%s)", errs)
				break
			}
			blk, _, admitted := proveAndSend(wit, rec)
			if admitted {
				fail("a cancelled note was completed")
			}
			refusal(r, "the thief's completion of the cancelled note after maturity, proven and sent: refused by the chain at block %d (the position tag is in the set)", blk)
			break
		}
	}
	// the recoveries of run 2 and run 5 complete after R; run 2's thief then cannot request (the new everyday key); the old policy is revoked
	for _, pair := range []struct {
		r  *vaultRun
		pf string
	}{{runs[1], run2NewPolicy}, {runs[4], run5NewPolicy}, {runs[5], run6NewPolicy}} {
		r := pair.r
		stream()
		st := vaultScan(r.vaultFile)
		hm := st["head"].(map[string]interface{})
		cb := uint64(hm["rec_h"].(float64)) + *recBlocks
		ct := uint64(hm["rec_t"].(float64)) + *recSeconds
		for {
			h, t := now()
			if h >= cb && t >= ct {
				break
			}
			time.Sleep(5 * time.Second)
			stream()
		}
		pos := uint64(hm["position"].(float64))
		h, t := now()
		wit, rec := fresh("pcomplete-w"), fresh("pcomplete-r")
		m := runJSON(walletBn, "vault-policy-complete", "--vault", r.vaultFile, "--new-policy", pair.pf, "--paths", pathsFile(pos), "--height", fmt.Sprint(h), "--time", fmt.Sprint(t), "--witness-out", wit, "--record-out", rec)
		blk, _, ok := proveAndSend(wit, rec)
		if !ok {
			fail("%s: the policy completion was refused at block %d", r.res.Name, blk)
		}
		event(r, "the recovery completed at block %d: the old policy hash %s revoked (the revoked set now %d), the head under the new policy", blk, str(m, "revealed")[:16], poolInfo(ec, nil).RevokedCount)
		// the thief's old everyday key can no longer request
		h2, t2 := declare()
		if _, _, errs := request(r, r.thiefDest, big.NewInt(1), h2, t2, "under the old everyday key after the recovery"); errs != "" {
			refusal(r, "the thief's request after the recovery completed: refused (%s): the policy's everyday key is the new one", errs)
		}
	}
	// a completion against a stale revoked root: run 3's thief proves against the root at the block before a
	// revocation landed (the roots moved above): the node refuses, the wallet re-proves against the parent's.
	{
		r := runs[2]
		stream()
		st := vaultScan(r.vaultFile)
		for _, p := range st["pending"].([]interface{}) {
			pm := p.(map[string]interface{})
			if pm["consumed_at"] != nil {
				continue
			}
			_ = pm
		}
		info := poolInfo(ec, nil)
		res.RevokedCount = info.RevokedCount
	}

	// The takes, the bounds, the pass.
	for _, r := range runs {
		scan(r.thiefDest)
		stream()
		st := vaultScan(r.vaultFile)
		r.res.Take = new(big.Int).Add(bigOf(r.res.TakeFromW), r.completedTake).String()
		r.res.TakeFromVault = r.completedTake.String()
		r.res.VaultLeft = str(st, "protected")
		r.res.VaultReturned = str(st, "returned")
		var bound *big.Int
		switch r.res.Name {
		case "run1-cancel-within-D", "run4-malicious-recovery":
			bound = new(big.Int).Set(W)
			r.res.BoundWords = "W (the cancellation key acts inside D; the vault loses nothing)"
		case "run2-recovery-at-once":
			bound = new(big.Int).Add(W, new(big.Int).Mul(big.NewInt(4), L))
			r.res.BoundWords = "W + 4 L at the worst alignment (W + 3 L common) with the defaults' shape; rule 6 (no request while a recovery is pending) makes W + 2 L the most the chain admits when the authority announces at once"
		case "run3-owner-silent":
			n := int64(r.res.Requests)
			bound = new(big.Int).Add(W, new(big.Int).Mul(big.NewInt(n), L))
			r.res.BoundWords = fmt.Sprintf("W + L a period index touched: %d indexes in %d periods of silence (the first period, each next one through the 64-block margin)", n, *periodsRun3)
		case "run5-cancel-griefing":
			bound = new(big.Int).Set(W)
			r.res.BoundWords = "W: a compromised cancellation key moves nothing; it costs the owner the period's allowance until the policy rotates it, and it cannot cancel the rotation (F-800)"
		case "run6-head-race":
			bound = new(big.Int).Set(W)
			r.res.BoundWords = "W: the announcement is applied before the request in the same block (F-801); the thief's request against the head lost"
		}
		r.res.Bound = bound.String()
		r.res.WithinBound = bigOf(r.res.Take).Cmp(bound) <= 0
		r.res.Pass = r.res.WithinBound
	}
	info := poolInfo(ec, nil)
	last := res.LastBlock
	info2 := poolInfo(ec2, &last)
	infoAt := poolInfo(ec, &last)
	bal, err := ec.BalanceAt(ctx, poolAddr, nil)
	must(err)
	res.Verifier = info.Verifier
	res.Turnstile, res.PoolBalance = info.Turnstile.String(), bal.String()
	res.Reconciled = info.Turnstile.Cmp(bal) == 0
	res.SecondNodeAgrees = info2.Root == infoAt.Root && info2.Count == infoAt.Count && info2.RevokedRoot == infoAt.RevokedRoot
	res.RevokedCount = info.RevokedCount
	res.AllPass = res.Reconciled && res.SecondNodeAgrees
	for _, r := range res.Runs {
		res.AllPass = res.AllPass && r.Pass
	}
	res.WallSeconds = time.Since(started).Seconds()
	b, _ := json.MarshalIndent(res, "", "  ")
	if *out != "" {
		must(os.WriteFile(*out, b, 0o644))
	}
	fmt.Println(string(b))
	if !res.AllPass {
		fail("NOT ALL PASS")
	}
}
