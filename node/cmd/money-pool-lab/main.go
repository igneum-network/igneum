// Copyright 2026 The Igneum Authors
// This file is part of go-ethereum (Igneum fork).

// money-pool-lab drives the shielded pool's lab row (Project Money stage
// one, cut four; docs/money/STAGE1-JOIN-2026-10-18.md): two real wallets
// (the Rust igneum-money tool) shield public IGN into notes, scan them
// from the chain (igneum_poolBlock), prepare spends with paths the node
// serves (igneum_poolPath), the pinned prover (money-prove) proves each
// statement, the node verifies and admits the records, then the refusal
// rows (a replayed nullifier, a forged proof, a proof moved to another
// statement, a shield whose value is not the proven one, a stale anchor,
// a spend carrying value, an unreadable record), the second node's
// agreement, a restore from the words with and without the birthday, and
// the reconciliation of the chain's figures against both wallets to the
// wei. Nothing here is a secret: the carrier is the development chain's
// published faucet key; the wallets' files are the lab's and are
// quarantined after the run.
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

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/params"
)

type result struct {
	Chain                  uint64   `json:"chain"`
	Pool                   string   `json:"pool"`
	Verifier               string   `json:"verifier"`
	Hash                   string   `json:"hash"`
	Shields                int      `json:"shields"`
	Transfers              int      `json:"transfers"`
	TwoInputTransfers      int      `json:"twoInputTransfers"`
	Unshields              int      `json:"unshields"`
	FirstBlock             uint64   `json:"firstBlock"`
	LastBlock              uint64   `json:"lastBlock"`
	Notes                  uint64   `json:"notes"`
	Seq                    uint64   `json:"seq"`
	Turnstile              string   `json:"turnstile"`
	PoolBalance            string   `json:"poolBalance"`
	WalletAUnspent         string   `json:"walletAUnspent"`
	WalletBUnspent         string   `json:"walletBUnspent"`
	ShieldedIn             string   `json:"shieldedIn"`
	UnshieldedOut          string   `json:"unshieldedOut"`
	FeesBurned             string   `json:"feesBurned"`
	PublicOutputs          string   `json:"publicOutputs"`
	Reconciled             bool     `json:"reconciled"`
	SecondNodeAgrees       bool     `json:"secondNodeAgrees"`
	SecondNodeRoot         string   `json:"secondNodeRoot"`
	ThirdNodeAgrees        bool     `json:"thirdNodeAgrees,omitempty"`
	ThirdNodeRoot          string   `json:"thirdNodeRoot,omitempty"`
	ThirdNodeTurnstile     string   `json:"thirdNodeTurnstile,omitempty"`
	StreamFrom             string   `json:"streamFrom,omitempty"`
	RefusalRows            []string `json:"refusalRows"`
	RestoreRows            []string `json:"restoreRows"`
	SpendProofBytesMedian  int      `json:"spendProofBytesMedian"`
	SpendProofBytesMax     int      `json:"spendProofBytesMax"`
	ShieldProofBytesMedian int      `json:"shieldProofBytesMedian"`
	SpendProveMsMedian     float64  `json:"spendProveMsMedian"`
	ShieldProveMsMedian    float64  `json:"shieldProveMsMedian"`
	SpendRecordBytesMedian int      `json:"spendRecordBytesMedian"`
	RetainedBytesPerSpend  int      `json:"retainedBytesPerSpend"`
	RetainedBytesPerShield int      `json:"retainedBytesPerShield"`
	ProofBytesTotal        int      `json:"proofBytesTotal"`
	RetainedBytesTotal     int      `json:"retainedBytesTotal"`
	WallSeconds            float64  `json:"wallSeconds"`
	ProverThreads          string   `json:"proverThreads"`
}

type wallet struct {
	name, file, notes, addr string
	birthday                uint64
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

func run(bin string, args ...string) string {
	cmd := exec.Command(bin, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if threads != "" {
		cmd.Env = append(os.Environ(), "RAYON_NUM_THREADS="+threads)
	}
	if err := cmd.Run(); err != nil {
		fail("%s %v: %v\n%s", filepath.Base(bin), args, err, errb.String())
	}
	return strings.TrimSpace(out.String())
}

func runJSON(bin string, args ...string) map[string]interface{} {
	var m map[string]interface{}
	out := run(bin, args...)
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

var step int

func fresh(prefix string) string {
	step++
	return filepath.Join(work, fmt.Sprintf("%s-%04d.json", prefix, step))
}

// prove runs the pinned prover on a witness; returns the proof bytes and the prove time.
func prove(witness string) ([]byte, float64, int) {
	proofPath := strings.TrimSuffix(witness, ".json") + ".proof"
	claimPath := strings.TrimSuffix(witness, ".json") + ".claim"
	out := run(proverBn, witness, proofPath, claimPath, "1")
	m := proveRE.FindStringSubmatch(out)
	if m == nil {
		fail("the prover's output has no times: %s", out)
	}
	ms, _ := strconv.ParseFloat(m[1], 64)
	n, _ := strconv.Atoi(m[2])
	proof, err := os.ReadFile(proofPath)
	must(err)
	os.Remove(witness) // the witness carries the spend authority; the proof does not
	return proof, ms, n
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return s[len(s)/2]
}

func medianInt(v []int) int {
	if len(v) == 0 {
		return 0
	}
	s := append([]int(nil), v...)
	sort.Ints(s)
	return s[len(s)/2]
}

func main() {
	rpcURL := flag.String("rpc", "http://127.0.0.1:8731", "the sealing node's HTTP endpoint")
	rpc2URL := flag.String("rpc2", "http://127.0.0.1:8732", "the reading node's HTTP endpoint (the second node's agreement)")
	n := flag.Int("n", 100, "private transfers to make (shields are extra)")
	shields := flag.Int("shields", 40, "shields before the transfers (wallet A and wallet B)")
	batch := flag.Int("batch", 4, "transactions a round (one block or more)")
	flag.StringVar(&walletBn, "wallet", "", "the igneum-money binary")
	flag.StringVar(&proverBn, "prover", "", "the money-prove binary")
	flag.StringVar(&work, "work", "", "the work directory (wallet files, witnesses, proofs; quarantined after)")
	flag.StringVar(&threads, "threads", "8", "RAYON_NUM_THREADS for the prover (the wallet's PC: eight cores)")
	stale := flag.Bool("stale", true, "run the stale-anchor row (64 roots after a proof)")
	out := flag.String("out", "", "the result JSON's path")
	// The pruned-sync lab's additions: a later run over the same wallets
	// (-reuse), a third node that must agree (-rpc3: the node that joined
	// by the pruned sync), the node whose igneum_poolBlock feeds the
	// wallets' scans (-stream-rpc; the third node's, to show its backfilled
	// retained sets serve a scan), and the refusal and restore rows off.
	reuse := flag.Bool("reuse", false, "reuse the work directory's wallets (no keygen; the shields may be zero)")
	rpc3URL := flag.String("rpc3", "", "a third node's HTTP endpoint that must hold every block and agree (the pruned-sync lab)")
	streamURL := flag.String("stream-rpc", "", "the node whose igneum_poolBlock feeds the wallets' scans (default: -rpc)")
	refusals := flag.Bool("refusals", true, "run the refusal rows")
	restore := flag.Bool("restore", true, "run the restore rows")
	flag.Parse()
	if walletBn == "" || proverBn == "" || work == "" {
		fail("-wallet, -prover and -work are needed")
	}
	must(os.MkdirAll(work, 0o700))
	ec, err := ethclient.Dial(*rpcURL)
	must(err)
	ec2, err := ethclient.Dial(*rpc2URL)
	must(err)
	var ec3 *ethclient.Client
	if *rpc3URL != "" {
		ec3, err = ethclient.Dial(*rpc3URL)
		must(err)
	}
	ecStream := ec
	if *streamURL != "" {
		ecStream, err = ethclient.Dial(*streamURL)
		must(err)
	}
	chainID, err := ec.ChainID(ctx)
	must(err)
	cid := chainID.Uint64()
	poolAddr := pool.Address(cid)
	key := params.IgneumDevFaucetKey()
	from := crypto.PubkeyToAddress(key.PublicKey)
	signer := types.LatestSignerForChainID(chainID)
	nonce, err := ec.PendingNonceAt(ctx, from)
	must(err)
	res := result{Chain: cid, Pool: poolAddr.Hex(), ProverThreads: threads}
	shieldedIn, unshieldedOut, fees := new(big.Int), new(big.Int), new(big.Int)
	publicOut := common.HexToAddress("0x00000000000000000000000000000000ae915001")
	fee := big.NewInt(1000)
	started := time.Now()
	var spendProofBytes, shieldProofBytes, spendRecordBytes []int
	var spendProveMs, shieldProveMs []float64

	head := func() uint64 {
		h, err := ec.BlockNumber(ctx)
		must(err)
		return h
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
	wait := func(txs []*types.Transaction) uint64 {
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
		// the reading node must have the block too (and the third, when there is one)
		for _, c := range []*ethclient.Client{ec2, ec3} {
			if c == nil {
				continue
			}
			for {
				h2, err := c.BlockNumber(ctx)
				must(err)
				if h2 >= last {
					break
				}
				time.Sleep(200 * time.Millisecond)
			}
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
		Records    int      `json:"records"`
		ProofBytes int      `json:"proofBytes"`
	}
	poolBlockAt := func(h uint64) poolBlock {
		var b poolBlock
		must(ecStream.Client().CallContext(ctx, &b, "igneum_poolBlock", fmt.Sprintf("0x%x", h)))
		return b
	}
	type poolPath struct {
		Anchor   string   `json:"anchor"`
		Height   uint64   `json:"height"`
		Index    uint64   `json:"index"`
		Siblings []string `json:"siblings"`
	}
	pathAt := func(index, h uint64) poolPath {
		var p poolPath
		must(ec.Client().CallContext(ctx, &p, "igneum_poolPath", index, fmt.Sprintf("0x%x", h)))
		return p
	}

	// The block stream for the wallets: every block from 0 to the head, as
	// igneum_poolBlock gives it (a wallet skips the blocks below its birthday
	// by their positions).
	streamFile := filepath.Join(work, "blocks.jsonl")
	streamed := uint64(0)
	var streamBuf bytes.Buffer
	var proofBytesTotal, retainedTotal int
	var retainedBlocks = map[uint64]bool{}
	stream := func() {
		h := head()
		for b := streamed; b <= h; b++ {
			pb := poolBlockAt(b)
			outs := make([]string, 0, len(pb.Outputs))
			for _, o := range pb.Outputs {
				outs = append(outs, strings.TrimPrefix(o.Commitment, "0x")+strings.TrimPrefix(o.Ciphertext, "0x"))
			}
			line, _ := json.Marshal(map[string]interface{}{"height": pb.Height, "position": pb.Position, "outputs": outs, "nullifiers": func() []string {
				s := make([]string, 0, len(pb.Nullifiers))
				for _, nf := range pb.Nullifiers {
					s = append(s, strings.TrimPrefix(nf, "0x"))
				}
				return s
			}()})
			streamBuf.Write(line)
			streamBuf.WriteByte('\n')
			if !retainedBlocks[b] {
				retainedBlocks[b] = true
				proofBytesTotal += pb.ProofBytes
				for _, o := range pb.Outputs {
					retainedTotal += 32 + (len(o.Ciphertext)-2)/2
				}
				retainedTotal += 32 * len(pb.Nullifiers)
				retainedTotal += pb.Records * (32 + 32 + 20 + 1) // the anchor or value, the fee, the public output, the tag
			}
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

	// The two wallets, born at the current head.
	birth := head()
	A := &wallet{name: "A", file: filepath.Join(work, "wallet-a.json"), notes: filepath.Join(work, "notes-a.json"), birthday: birth}
	B := &wallet{name: "B", file: filepath.Join(work, "wallet-b.json"), notes: filepath.Join(work, "notes-b.json"), birthday: birth}
	for _, w := range []*wallet{A, B} {
		addrFile := filepath.Join(work, "addr-"+w.name+".txt")
		if *reuse {
			text, err := os.ReadFile(addrFile)
			must(err)
			w.addr = strings.TrimSpace(string(text))
		} else {
			w.addr = run(walletBn, "--wallet", w.file, "keygen", "--birthday", strconv.FormatUint(w.birthday, 10))
			must(os.WriteFile(addrFile, []byte(w.addr+"\n"), 0o600))
		}
		w.unspent = new(big.Int)
	}
	if *reuse {
		fmt.Fprintf(os.Stderr, "%s wallets A and B reused from %s at head %d\n", time.Now().UTC().Format("15:04:05Z"), work, birth)
	} else {
		fmt.Fprintf(os.Stderr, "%s wallets A and B born at %d\n", time.Now().UTC().Format("15:04:05Z"), birth)
	}
	startInfo := poolInfo(ec, nil)
	startPub, err := ec.BalanceAt(ctx, publicOut, nil)
	must(err)
	res.StreamFrom = *streamURL

	// A shield record: the wallet's witness and record, the prover's proof.
	shieldOf := func(to *wallet, value *big.Int) ([]byte, [32]byte) {
		wit, rec := fresh("shield-w"), fresh("shield-r")
		run(walletBn, "--wallet", to.file, "shield-prepare", "--value", value.String(), "--chain-id", strconv.FormatUint(cid, 10), "--witness-out", wit, "--record-out", rec)
		proof, ms, nb := prove(wit)
		shieldProveMs = append(shieldProveMs, ms)
		shieldProofBytes = append(shieldProofBytes, nb)
		var r struct{ Commitment, Ciphertext string }
		text, err := os.ReadFile(rec)
		must(err)
		must(json.Unmarshal(text, &r))
		ct, err := hex.DecodeString(r.Ciphertext)
		must(err)
		sh := pool.Shield{Commitment: h32(r.Commitment), Ciphertext: ct, Proof: proof}
		return sh.Encode(), sh.Commitment
	}

	// The shields: alternating to A and B, batch a round.
	var pending []*types.Transaction
	flush := func() uint64 {
		if len(pending) == 0 {
			return res.LastBlock
		}
		last := wait(pending)
		pending = nil
		return last
	}
	for i := 0; i < *shields; i++ {
		to := A
		if i%3 == 2 {
			to = B
		}
		v := new(big.Int).Mul(big.NewInt(int64(1+i%7)), big.NewInt(1e18))
		data, _ := shieldOf(to, v)
		pending = append(pending, send(v, data))
		shieldedIn.Add(shieldedIn, v)
		res.Shields++
		if len(pending) >= *batch {
			flush()
		}
	}
	flush()
	scan(A)
	scan(B)
	fmt.Fprintf(os.Stderr, "%s %d shields admitted; A unspent %v B unspent %v head %d\n", time.Now().UTC().Format("15:04:05Z"), res.Shields, A.unspent, B.unspent, head())
	if info := poolInfo(ec, nil); !*reuse && info.Count != uint64(res.Shields) {
		fail("after the shields the pool holds %d notes, not %d", info.Count, res.Shields)
	}

	// A spend record from the sender's notes to the recipient.
	var lastSpendData []byte
	spendOf := func(from, to *wallet, value *big.Int, vpub *big.Int, atBlock uint64, notes []noteJSON) ([]byte, map[string]interface{}) {
		positions := make([]string, 0, 2)
		paths := map[string][]string{}
		var anchor string
		for _, nt := range notes {
			p := pathAt(nt.Position, atBlock)
			if anchor == "" {
				anchor = strings.TrimPrefix(p.Anchor, "0x")
			}
			sib := make([]string, 0, 32)
			for _, s := range p.Siblings {
				sib = append(sib, strings.TrimPrefix(s, "0x"))
			}
			paths[strconv.FormatUint(nt.Position, 10)] = sib
			positions = append(positions, strconv.FormatUint(nt.Position, 10))
		}
		pathsFile := fresh("paths")
		pj, _ := json.Marshal(map[string]interface{}{"anchor": anchor, "paths": paths})
		must(os.WriteFile(pathsFile, pj, 0o600))
		wit, rec := fresh("spend-w"), fresh("spend-r")
		m := runJSON(walletBn, "--wallet", from.file, "spend-prepare", "--notes", from.notes, "--positions", strings.Join(positions, ","), "--paths", pathsFile,
			"--to", to.addr, "--value", value.String(), "--memo", "lab", "--fee", fee.String(), "--public-value", vpub.String(), "--public-output", hex.EncodeToString(publicOut[:]),
			"--chain-id", strconv.FormatUint(cid, 10), "--witness-out", wit, "--record-out", rec)
		proof, ms, nb := prove(wit)
		spendProveMs = append(spendProveMs, ms)
		spendProofBytes = append(spendProofBytes, nb)
		var r struct {
			Anchor     string
			Nullifiers []string
			Outputs    []struct{ Commitment, Ciphertext string }
			Fee        string
		}
		text, err := os.ReadFile(rec)
		must(err)
		must(json.Unmarshal(text, &r))
		sp := pool.Spend{Anchor: h32(r.Anchor), PublicValue: vpub, Fee: bigOf(r.Fee), PublicOutput: publicOut, Proof: proof}
		for _, nf := range r.Nullifiers {
			sp.Nullifiers = append(sp.Nullifiers, h32(nf))
		}
		for _, o := range r.Outputs {
			ct, err := hex.DecodeString(o.Ciphertext)
			must(err)
			sp.Outputs = append(sp.Outputs, pool.Output{Commitment: h32(o.Commitment), Ciphertext: ct})
		}
		data := sp.Encode()
		spendRecordBytes = append(spendRecordBytes, len(data))
		lastSpendData = data
		return data, m
	}

	// The transfers: A to B and B to A, one or two inputs, every fifth with a public value.
	for res.Transfers < *n {
		inRound := 0
		var roundFrom []*wallet
		for inRound < *batch && res.Transfers < *n {
			from, to := A, B
			if res.Transfers%3 == 2 {
				from, to = B, A
			}
			notes := unspentNotes(from)
			// a note spent in this round is still unspent in the file: skip the ones already picked
			if len(notes) == 0 {
				from, to = to, from
				notes = unspentNotes(from)
			}
			if len(notes) == 0 {
				break
			}
			pick := notes[:1]
			if len(notes) >= 2 && res.Transfers%2 == 1 {
				pick = notes[:2]
			}
			total := new(big.Int)
			for _, nt := range pick {
				total.Add(total, bigOf(nt.Value))
			}
			vpub := new(big.Int)
			if res.Transfers%5 == 4 {
				vpub.Div(total, big.NewInt(4))
			}
			value := new(big.Int).Sub(total, fee)
			value.Sub(value, vpub)
			value.Div(value, big.NewInt(2)) // half to the recipient, the rest change
			if res.Transfers%7 == 6 {
				value = new(big.Int).Sub(total, fee)
				value.Sub(value, vpub) // the whole note: no change
			}
			data, _ := spendOf(from, to, value, vpub, head(), pick)
			pending = append(pending, send(new(big.Int), data))
			roundFrom = append(roundFrom, from)
			unshieldedOut.Add(unshieldedOut, vpub)
			fees.Add(fees, fee)
			res.Transfers++
			if len(pick) == 2 {
				res.TwoInputTransfers++
			}
			if vpub.Sign() > 0 {
				res.Unshields++
			}
			inRound++
			// one spend a round from each wallet keeps the picks distinct; a second from the same wallet rescans
			if inRound < *batch && res.Transfers < *n {
				flush()
				scan(from)
				scan(to)
			}
		}
		flush()
		scan(A)
		scan(B)
		fmt.Fprintf(os.Stderr, "%s transfers %d (two-input %d, unshields %d) A %v B %v head %d\n", time.Now().UTC().Format("15:04:05Z"), res.Transfers, res.TwoInputTransfers, res.Unshields, A.unspent, B.unspent, head())
	}

	// The refusal rows: each sent, mined, and refused by Finalize (the pool's
	// figures unchanged, the value returned when there was one).
	_ = lastSpendData
	refusal := func(name string, value *big.Int, data []byte) {
		before := poolInfo(ec, nil)
		balBefore, err := ec.BalanceAt(ctx, from, nil)
		must(err)
		tx := send(value, data)
		blk := wait([]*types.Transaction{tx})
		after := poolInfo(ec, nil)
		if after.Count != before.Count || after.Turnstile.Cmp(before.Turnstile) != 0 || after.Root != before.Root {
			fail("refusal row %q changed the pool: %+v -> %+v", name, before, after)
		}
		if value.Sign() > 0 {
			balAfter, err := ec.BalanceAt(ctx, from, nil)
			must(err)
			r, _ := ec.TransactionReceipt(ctx, tx.Hash())
			gasPaid := new(big.Int).Mul(new(big.Int).SetUint64(r.GasUsed), r.EffectiveGasPrice)
			if new(big.Int).Sub(balBefore, balAfter).Cmp(gasPaid) != 0 {
				fail("refusal row %q: the value was not returned", name)
			}
		}
		res.RefusalRows = append(res.RefusalRows, fmt.Sprintf("%s: refused at block %d, the pool unchanged", name, blk))
	}
	if *refusals {
		// (a) the last admitted spend replayed (its nullifiers are spent)
		refusal("replayed nullifier (an admitted spend sent again)", new(big.Int), lastSpendData)
		// (b) a forged proof: a fresh spend's proof with one byte flipped
		notesA := unspentNotes(A)
		if len(notesA) < 3 {
			fail("wallet A has fewer than three unspent notes for the refusal rows")
		}
		forged, _ := spendOf(A, B, big.NewInt(1), new(big.Int), head(), notesA[:1])
		forged[len(forged)-100] ^= 1
		refusal("forged proof (one byte of the proof flipped)", new(big.Int), forged)
		// (c) the proof moved to another statement: the same record with the fee changed
		moved, _ := spendOf(A, B, big.NewInt(1), new(big.Int), head(), notesA[1:2])
		sp, ok := pool.ParseSpend(moved)
		if !ok {
			fail("the spend record does not parse")
		}
		sp.Fee = new(big.Int).Add(sp.Fee, big.NewInt(1))
		refusal("proof moved to another statement (the fee changed under the same proof)", new(big.Int), sp.Encode())
		// (d) a shield whose value is not the proven one
		shieldData, _ := shieldOf(A, big.NewInt(1e18))
		refusal("shield carrying a value other than the proven one (2 IGN sent, 1 IGN proven)", big.NewInt(2e18), shieldData)
		// (e) a spend carrying value
		withValue, _ := spendOf(A, B, big.NewInt(1), new(big.Int), head(), notesA[2:3])
		refusal("spend carrying value", big.NewInt(1), withValue)
		// (f) an unreadable record with value
		refusal("unreadable record with value", big.NewInt(1e18), []byte{0x77, 1, 2, 3})
		// (g) a stale anchor: a proof against the root now, then RecentRoots roots recorded after it
		if *stale {
			notesB := unspentNotes(B)
			if len(notesB) == 0 {
				fail("wallet B has no unspent note for the stale row")
			}
			staleData, _ := spendOf(B, A, big.NewInt(1), new(big.Int), head(), notesB[:1])
			first := poolInfo(ec, nil)
			for poolInfo(ec, nil).Seq < first.Seq+pool.RecentRoots {
				v := big.NewInt(1e17)
				data, _ := shieldOf(A, v)
				wait([]*types.Transaction{send(v, data)})
				shieldedIn.Add(shieldedIn, v)
				res.Shields++
			}
			res.RefusalRows = append(res.RefusalRows, fmt.Sprintf("the anchor at sequence %d is outside the last %d roots at sequence %d", first.Seq, pool.RecentRoots, poolInfo(ec, nil).Seq))
			refusal("stale anchor (a proof against a root 64 recorded roots old)", new(big.Int), staleData)
			// the same note against the current root is admitted
			scan(B)
			notesB = unspentNotes(B)
			before := poolInfo(ec, nil)
			freshData, _ := spendOf(B, A, big.NewInt(1), new(big.Int), head(), notesB[:1])
			blk := wait([]*types.Transaction{send(new(big.Int), freshData)})
			if after := poolInfo(ec, nil); after.Count != before.Count+2 {
				fail("the fresh-anchor spend of the same note was not admitted")
			}
			fees.Add(fees, fee)
			res.Transfers++
			res.RefusalRows = append(res.RefusalRows, fmt.Sprintf("the same note against the current root: admitted at block %d", blk))
		}
	}
	scan(A)
	scan(B)

	// The restore rows: A from its words with the birthday, then without it.
	if *restore {
		words := run(walletBn, "--wallet", A.file, "backup")
		wordsFile := filepath.Join(work, "words-a.txt")
		must(os.WriteFile(wordsFile, []byte(words), 0o600))
		for _, withBirthday := range []bool{true, false} {
			file := filepath.Join(work, fmt.Sprintf("restored-a-%v.json", withBirthday))
			notes := filepath.Join(work, fmt.Sprintf("restored-notes-%v.json", withBirthday))
			args := []string{"--wallet", file, "restore", "--words-file", wordsFile}
			if withBirthday {
				args = append(args, "--birthday", strconv.FormatUint(A.birthday, 10))
			}
			if addr := run(walletBn, args...); addr != A.addr {
				fail("the restored wallet's address differs")
			}
			m := runJSON(walletBn, "--wallet", file, "scan", "--blocks", streamFile, "--notes-out", notes)
			if str(m, "unspent") != A.unspent.String() {
				fail("the restored wallet's balance %s differs from A's %v (birthday %v)", str(m, "unspent"), A.unspent, withBirthday)
			}
			res.RestoreRows = append(res.RestoreRows, fmt.Sprintf("restore with birthday %v: from height %v, %v blocks read, %v skipped, %v outputs tried, %.3f s, unspent %s equal to A's", withBirthday, m["from_height"], m["blocks"], m["skipped_blocks"], m["outputs_tried"], m["seconds"], str(m, "unspent")))
		}

		// The reconciliation to the wei, and the second node's agreement.
	}
	info := poolInfo(ec, nil)
	last := res.LastBlock
	info2 := poolInfo(ec2, &last)
	infoAt := poolInfo(ec, &last)
	if ec3 != nil {
		info3 := poolInfo(ec3, &last)
		res.ThirdNodeAgrees = info3.Root == infoAt.Root && info3.Count == infoAt.Count && info3.Turnstile.Cmp(infoAt.Turnstile) == 0
		res.ThirdNodeRoot, res.ThirdNodeTurnstile = info3.Root.Hex(), info3.Turnstile.String()
	}
	bal, err := ec.BalanceAt(ctx, poolAddr, nil)
	must(err)
	pubBal, err := ec.BalanceAt(ctx, publicOut, nil)
	must(err)
	expect := new(big.Int).Add(startInfo.Turnstile, shieldedIn)
	expect.Sub(expect, unshieldedOut)
	expect.Sub(expect, fees)
	pubBal.Sub(pubBal, startPub) // this run's unshields alone
	walletsUnspent := new(big.Int).Add(A.unspent, B.unspent)
	res.Notes, res.Seq, res.Verifier, res.Hash = info.Count, info.Seq, info.Verifier, info.Hash
	res.Turnstile, res.PoolBalance = info.Turnstile.String(), bal.String()
	res.WalletAUnspent, res.WalletBUnspent = A.unspent.String(), B.unspent.String()
	res.ShieldedIn, res.UnshieldedOut, res.FeesBurned, res.PublicOutputs = shieldedIn.String(), unshieldedOut.String(), fees.String(), pubBal.String()
	res.Reconciled = info.Turnstile.Cmp(bal) == 0 && bal.Cmp(walletsUnspent) == 0 && walletsUnspent.Cmp(expect) == 0 && pubBal.Cmp(unshieldedOut) == 0
	res.SecondNodeAgrees = info2.Root == infoAt.Root && info2.Count == infoAt.Count && info2.Turnstile.Cmp(infoAt.Turnstile) == 0
	res.SecondNodeRoot = info2.Root.Hex()
	res.SpendProofBytesMedian, res.ShieldProofBytesMedian = medianInt(spendProofBytes), medianInt(shieldProofBytes)
	for _, b := range spendProofBytes {
		if b > res.SpendProofBytesMax {
			res.SpendProofBytesMax = b
		}
	}
	res.SpendProveMsMedian, res.ShieldProveMsMedian = median(spendProveMs), median(shieldProveMs)
	res.SpendRecordBytesMedian = medianInt(spendRecordBytes)
	res.RetainedBytesPerSpend = 1 + 32 + 2*32 + 2*(32+2+1293) + 32 + 32 + 20
	res.RetainedBytesPerShield = 1 + 32 + 2 + 1293
	stream()
	res.ProofBytesTotal, res.RetainedBytesTotal = proofBytesTotal, retainedTotal
	res.WallSeconds = time.Since(started).Seconds()
	b, _ := json.MarshalIndent(res, "", "  ")
	if *out != "" {
		must(os.WriteFile(*out, b, 0o644))
	}
	fmt.Println(string(b))
	if !res.Reconciled {
		fail("NOT RECONCILED")
	}
	if !res.SecondNodeAgrees {
		fail("THE SECOND NODE DISAGREES")
	}
	if ec3 != nil && !res.ThirdNodeAgrees {
		fail("THE THIRD NODE DISAGREES")
	}
}
