// Copyright 2026 The Igneum Authors
// This file is part of go-ethereum (Igneum fork).

// money-coinbase-lab drives the shielded coinbase's lab row (Project Money
// stage two, cut two; docs/money/STAGE2-CUT2-COINBASE-2026-10-18.md): two
// sealers on a throwaway chain, one of which registers a shielded payout
// address (its wallet's address, the Rust igneum-money tool) by a record to
// the pool address, after which the sealer share of every block it seals is
// minted as a coinbase note; the other is paid publicly as before. The
// driver waits the asked number of blocks, streams every block with its
// mints (igneum_poolBlock), scans the sealer's wallet (the notes rebuilt
// from the public record alone), spends one minted note privately to a
// second wallet and unshields another (the pinned prover proves, the node
// verifies), runs the refusal rows of the registration record, reads the
// second node's agreement, and reconciles the chain's figures to the wei:
// the mint records against the blocks' sealer shares, the turnstile against
// the pool's balance and the wallets, the supply against the fund, the
// public sealer and the minted sum, in the settlement ledger's identity
// form (identity.txt beside the result). Nothing here is a secret: the
// carrier is the development chain's published faucet key; the sealer's
// etherbase key and the wallets' files are the lab's, read from 0600 files
// and quarantined after the run. With -keygen the tool writes a fresh
// secp256k1 key to a 0600 file and prints its address (the stage script's
// etherbase form), nothing else.
package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
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
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus/igneum/pool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/params"
)

type result struct {
	Chain                uint64   `json:"chain"`
	Pool                 string   `json:"pool"`
	Verifier             string   `json:"verifier"`
	Hash                 string   `json:"hash"`
	ShieldedSealer       string   `json:"shieldedSealer"`
	PublicSealer         string   `json:"publicSealer"`
	RegistrationBlock    uint64   `json:"registrationBlock"`
	RegistrationBytes    int      `json:"registrationBytes"`
	Head                 uint64   `json:"head"`
	Blocks               uint64   `json:"blocks"`
	ShieldedSealerBlocks int      `json:"shieldedSealerBlocks"`
	MintedBlocks         int      `json:"mintedBlocks"`
	PublicSealerBlocks   int      `json:"publicSealerBlocks"`
	Notes                uint64   `json:"notes"`
	Minted               string   `json:"minted"`
	Turnstile            string   `json:"turnstile"`
	PoolBalance          string   `json:"poolBalance"`
	UnshieldedOut        string   `json:"unshieldedOut"`
	FeesBurned           string   `json:"feesBurned"`
	WalletWUnspent       string   `json:"walletWUnspent"`
	WalletBUnspent       string   `json:"walletBUnspent"`
	SupplyIssued         string   `json:"supplyIssued"`
	SupplyFund           string   `json:"supplyFund"`
	SupplyMintedShielded string   `json:"supplyMintedShielded"`
	PublicCredited       string   `json:"publicCredited"`
	Identities           []string `json:"identities"`
	Failing              []string `json:"failing"`
	Reconciled           bool     `json:"reconciled"`
	SecondNodeAgrees     bool     `json:"secondNodeAgrees"`
	RefusalRows          []string `json:"refusalRows"`
	Watcher              []string `json:"watcher"`
	MintStateBytes       int      `json:"mintStateBytesPerNote"`
	MintTookMsMedian     float64  `json:"mintTookMsMedian"`
	MintTookMsMax        float64  `json:"mintTookMsMax"`
	SpendProofBytes      int      `json:"spendProofBytes"`
	SpendProveMs         float64  `json:"spendProveMs"`
	WallSeconds          float64  `json:"wallSeconds"`
}

type wallet struct {
	name, file, notes, addr string
	birthday                uint64
	unspent                 *big.Int
}

type noteJSON struct {
	Value    string  `json:"value"`
	Position uint64  `json:"position"`
	Height   uint64  `json:"height"`
	CM       string  `json:"cm"`
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

func hb(b *hexutil.Big) *big.Int {
	if b == nil {
		return new(big.Int)
	}
	return (*big.Int)(b)
}

var step int

func fresh(prefix string) string {
	step++
	return filepath.Join(work, fmt.Sprintf("%s-%04d.json", prefix, step))
}

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
	os.Remove(witness)
	return proof, ms, n
}

func readKey(path string) *ecdsa.PrivateKey {
	st, err := os.Stat(path)
	must(err)
	if st.Mode().Perm()&0o077 != 0 {
		fail("%s: mode %o, not 0600", path, st.Mode().Perm())
	}
	text, err := os.ReadFile(path)
	must(err)
	key, err := crypto.HexToECDSA(strings.TrimSpace(strings.TrimPrefix(string(text), "0x")))
	must(err)
	return key
}

func main() {
	rpcURL := flag.String("rpc", "http://127.0.0.1:8741", "the shielded sealer's HTTP endpoint")
	rpc2URL := flag.String("rpc2", "http://127.0.0.1:8742", "the public sealer's HTTP endpoint (the second node's agreement)")
	blocks := flag.Uint64("blocks", 100, "blocks to wait after the registration lands")
	keygen := flag.String("keygen", "", "write a fresh secp256k1 key to this 0600 file, print its address, and exit")
	sealerKeyFile := flag.String("sealer-key", "", "the shielded sealer's etherbase key (hex, a 0600 file)")
	flag.StringVar(&walletBn, "wallet", "", "the igneum-money binary")
	flag.StringVar(&proverBn, "prover", "", "the money-prove binary")
	flag.StringVar(&work, "work", "", "the work directory (wallet files, witnesses, proofs; quarantined after)")
	flag.StringVar(&threads, "threads", "8", "RAYON_NUM_THREADS for the prover")
	out := flag.String("out", "", "the result JSON's path (identity.txt beside it)")
	flag.Parse()
	if *keygen != "" {
		key, err := crypto.GenerateKey()
		must(err)
		must(os.WriteFile(*keygen, []byte(hex.EncodeToString(crypto.FromECDSA(key))+"\n"), 0o600))
		fmt.Println(crypto.PubkeyToAddress(key.PublicKey).Hex())
		return
	}
	if walletBn == "" || proverBn == "" || work == "" || *sealerKeyFile == "" {
		fail("-wallet, -prover, -work and -sealer-key are needed")
	}
	must(os.MkdirAll(work, 0o700))
	started := time.Now()
	ec, err := ethclient.Dial(*rpcURL)
	must(err)
	ec2, err := ethclient.Dial(*rpc2URL)
	must(err)
	chainID, err := ec.ChainID(ctx)
	must(err)
	cid := chainID.Uint64()
	poolAddr := pool.Address(cid)
	signer := types.LatestSignerForChainID(chainID)
	faucet := params.IgneumDevFaucetKey()
	faucetAddr := crypto.PubkeyToAddress(faucet.PublicKey)
	sealerKey := readKey(*sealerKeyFile)
	sealerAddr := crypto.PubkeyToAddress(sealerKey.PublicKey)
	res := result{Chain: cid, Pool: poolAddr.Hex(), ShieldedSealer: sealerAddr.Hex()}
	publicOut := common.HexToAddress("0x00000000000000000000000000000000ae915002")
	fee := big.NewInt(1000)
	unshieldedOut, feesBurned := new(big.Int), new(big.Int)

	head := func() uint64 {
		h, err := ec.BlockNumber(ctx)
		must(err)
		return h
	}
	nonces := map[common.Address]uint64{}
	send := func(key *ecdsa.PrivateKey, to common.Address, value *big.Int, data []byte) *types.Transaction {
		from := crypto.PubkeyToAddress(key.PublicKey)
		if _, ok := nonces[from]; !ok {
			n, err := ec.PendingNonceAt(ctx, from)
			must(err)
			nonces[from] = n
		}
		tip, err := ec.SuggestGasTipCap(ctx)
		must(err)
		hd, err := ec.HeaderByNumber(ctx, nil)
		must(err)
		feeCap := new(big.Int).Add(new(big.Int).Mul(hd.BaseFee, big.NewInt(4)), tip)
		gas := uint64(21000 + 16*len(data) + 4*32 + 50000)
		tx := types.MustSignNewTx(key, signer, &types.DynamicFeeTx{ChainID: chainID, Nonce: nonces[from], To: &to, Value: value, Gas: gas, GasTipCap: tip, GasFeeCap: feeCap, Data: data})
		must(ec.SendTransaction(ctx, tx))
		nonces[from]++
		return tx
	}
	wait := func(tx *types.Transaction) *types.Receipt {
		for {
			r, err := ec.TransactionReceipt(ctx, tx.Hash())
			if err == nil && r != nil {
				if r.Status != 1 {
					fail("transaction %s failed in the EVM", tx.Hash())
				}
				for {
					h2, err := ec2.BlockNumber(ctx)
					must(err)
					if h2 >= r.BlockNumber.Uint64() {
						return r
					}
					time.Sleep(200 * time.Millisecond)
				}
			}
			time.Sleep(200 * time.Millisecond)
		}
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
	type poolMint struct {
		Position   uint64       `json:"position"`
		Sealer     string       `json:"sealer"`
		Value      *hexutil.Big `json:"value"`
		Randomness string       `json:"randomness"`
		Commitment string       `json:"commitment"`
	}
	type poolBlock struct {
		Height     uint64 `json:"height"`
		Position   uint64 `json:"position"`
		Outputs    []struct{ Commitment, Ciphertext string }
		Nullifiers []string   `json:"nullifiers"`
		Records    int        `json:"records"`
		ProofBytes int        `json:"proofBytes"`
		Mints      []poolMint `json:"mints"`
	}
	poolBlockAt := func(c *ethclient.Client, h uint64) poolBlock {
		var b poolBlock
		must(c.Client().CallContext(ctx, &b, "igneum_poolBlock", fmt.Sprintf("0x%x", h)))
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
	type payout struct {
		Account string `json:"account"`
		PK      string `json:"pk"`
		EK      string `json:"ek"`
	}
	payoutOf := func(c *ethclient.Client, a common.Address) *payout {
		var p *payout
		must(c.Client().CallContext(ctx, &p, "igneum_poolPayoutAddress", a, "latest"))
		return p
	}
	type issuance struct {
		Number       hexutil.Uint64 `json:"number"`
		Form         string         `json:"form"`
		Fund         *hexutil.Big   `json:"fund"`
		Miner        *hexutil.Big   `json:"miner"`
		PriorityFees *hexutil.Big   `json:"priorityFees"`
		Sealer       common.Address `json:"sealer"`
		Credited     *hexutil.Big   `json:"credited"`
		Entries      hexutil.Uint64 `json:"entries"`
	}
	issuanceAt := func(h uint64) issuance {
		var i issuance
		must(ec.Client().CallContext(ctx, &i, "igneum_blockIssuance", fmt.Sprintf("0x%x", h)))
		return i
	}
	type supply struct {
		Issued         *hexutil.Big `json:"issued"`
		Scheduled      *hexutil.Big `json:"scheduled"`
		FundIssued     *hexutil.Big `json:"fundIssued"`
		Burned         *hexutil.Big `json:"burned"`
		MintedShielded *hexutil.Big `json:"mintedShielded"`
	}
	supplyAt := func(c *ethclient.Client, h uint64) supply {
		var s supply
		must(c.Client().CallContext(ctx, &s, "igneum_supply", fmt.Sprintf("0x%x", h)))
		return s
	}
	balanceAt := func(c *ethclient.Client, a common.Address, h uint64) *big.Int {
		b, err := c.BalanceAt(ctx, a, new(big.Int).SetUint64(h))
		must(err)
		return b
	}

	// The block stream for the wallets, with the mints.
	streamFile := filepath.Join(work, "blocks.jsonl")
	streamed := uint64(0)
	var streamBuf bytes.Buffer
	stream := func() {
		h := head()
		for b := streamed; b <= h; b++ {
			pb := poolBlockAt(ec, b)
			outs := make([]string, 0, len(pb.Outputs))
			for _, o := range pb.Outputs {
				outs = append(outs, strings.TrimPrefix(o.Commitment, "0x")+strings.TrimPrefix(o.Ciphertext, "0x"))
			}
			nfs := make([]string, 0, len(pb.Nullifiers))
			for _, nf := range pb.Nullifiers {
				nfs = append(nfs, strings.TrimPrefix(nf, "0x"))
			}
			mints := make([]map[string]interface{}, 0, len(pb.Mints))
			for _, m := range pb.Mints {
				pk := payoutOf(ec, common.HexToAddress(m.Sealer))
				if pk == nil {
					fail("block %d mints to %s, which has no registered address", b, m.Sealer)
				}
				mints = append(mints, map[string]interface{}{"position": m.Position, "value": hb(m.Value).String(), "pk": strings.TrimPrefix(pk.PK, "0x"), "r": strings.TrimPrefix(m.Randomness, "0x"), "cm": strings.TrimPrefix(m.Commitment, "0x")})
			}
			line, _ := json.Marshal(map[string]interface{}{"height": pb.Height, "position": pb.Position, "outputs": outs, "nullifiers": nfs, "mints": mints})
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
	log := func(f string, a ...interface{}) {
		fmt.Fprintf(os.Stderr, time.Now().UTC().Format("15:04:05Z ")+f+"\n", a...)
	}

	// The sealer's wallet W and the recipient wallet B, born at the head.
	birth := head()
	W := &wallet{name: "W", file: filepath.Join(work, "wallet-w.json"), notes: filepath.Join(work, "notes-w.json"), birthday: birth}
	B := &wallet{name: "B", file: filepath.Join(work, "wallet-b.json"), notes: filepath.Join(work, "notes-b.json"), birthday: birth}
	for _, w := range []*wallet{W, B} {
		w.addr = run(walletBn, "--wallet", w.file, "keygen", "--birthday", strconv.FormatUint(w.birthday, 10))
		w.unspent = new(big.Int)
	}
	rawAddr, err := hex.DecodeString(run(walletBn, "--wallet", W.file, "address", "--raw"))
	must(err)
	if len(rawAddr) != pool.AddressBytes {
		fail("the wallet's raw address is %d bytes", len(rawAddr))
	}
	reg, ok := pool.ParseRegistration(append([]byte{pool.RegisterTag}, rawAddr...))
	if !ok {
		fail("the wallet's address does not parse as a registration")
	}
	log("wallets W and B born at %d; W's pk %x", birth, reg.PK[:8])

	// The sealer's account funded from the faucet (gas for the registration).
	funded := new(big.Int).Mul(big.NewInt(1), big.NewInt(1e18))
	wait(send(faucet, sealerAddr, funded, nil))

	// The refusal rows first (so the registration of record is the last
	// registration at every later height): a malformed encapsulation key, a
	// registration carrying value, a record of the wrong length.
	bad := pool.Registration{PK: reg.PK, EK: append([]byte(nil), reg.EK...), D: reg.D}
	bad.EK[0], bad.EK[1] = 0xff, 0xff
	r1 := wait(send(faucet, poolAddr, nil, bad.Encode()))
	if p := payoutOf(ec, faucetAddr); p != nil {
		fail("a malformed encapsulation key registered")
	}
	res.RefusalRows = append(res.RefusalRows, fmt.Sprintf("a registration whose encapsulation key fails FIPS 203's check: refused at %d, no address stored", r1.BlockNumber.Uint64()))
	r2 := wait(send(faucet, poolAddr, big.NewInt(12345), reg.Encode()))
	time.Sleep(3 * time.Second) // a competing block may replace the receipt's on a two-sealer chain
	if p := payoutOf(ec, faucetAddr); p != nil {
		fail("a registration carrying value registered")
	}
	if info := poolInfo(ec, nil); info.Balance.Cmp(info.Turnstile) != 0 {
		fail("the value of a refused registration is stuck in the pool: balance %v, turnstile %v", info.Balance, info.Turnstile)
	}
	res.RefusalRows = append(res.RefusalRows, fmt.Sprintf("a registration carrying 12,345 wei: refused at %d, the value returned in the block (the pool's balance stays its turnstile), no address stored", r2.BlockNumber.Uint64()))
	r3 := wait(send(faucet, poolAddr, nil, reg.Encode()[:100]))
	if p := payoutOf(ec, faucetAddr); p != nil {
		fail("a short record registered")
	}
	res.RefusalRows = append(res.RefusalRows, fmt.Sprintf("a record of 100 bytes: not a registration, refused at %d", r3.BlockNumber.Uint64()))

	// The registration of record, from the sealer's own account.
	regData := reg.Encode()
	res.RegistrationBytes = len(regData)
	regTx := send(sealerKey, poolAddr, nil, regData)
	rr := wait(regTx)
	// A two-sealer chain at one difficulty forks often; the registration's
	// block of record is the one the transaction sits in once both heads
	// have moved on, so the receipt is read again after a settle.
	var p *payout
	for i := 0; i < 60; i++ {
		if p = payoutOf(ec, sealerAddr); p != nil {
			if p2 := payoutOf(ec2, sealerAddr); p2 != nil && p2.PK == p.PK {
				break
			}
		}
		p = nil
		time.Sleep(time.Second)
	}
	if p == nil {
		fail("the sealer's registration is not held by both nodes a minute after its receipt")
	}
	rr, err = ec.TransactionReceipt(ctx, regTx.Hash())
	must(err)
	res.RegistrationBlock = rr.BlockNumber.Uint64()
	regGas := new(big.Int).Mul(new(big.Int).SetUint64(rr.GasUsed), rr.EffectiveGasPrice)
	if h32(p.PK) != reg.PK {
		fail("the stored recipient key %s is not the wallet's %x", p.PK, reg.PK)
	}
	if p.EK != "0x"+hex.EncodeToString(reg.EK) {
		fail("the stored encapsulation key (%d hex characters) is not the wallet's (%d bytes)", len(p.EK), len(reg.EK))
	}
	log("sealer %s registered W's address at block %d (%d bytes)", sealerAddr.Hex(), res.RegistrationBlock, len(regData))

	// The blocks.
	target := res.RegistrationBlock + *blocks
	for head() < target {
		time.Sleep(2 * time.Second)
	}
	log("head %d: the %d blocks after the registration sealed", head(), *blocks)

	// W scans: the notes from the public record alone.
	scan(W)
	notes := unspentNotes(W)
	log("W holds %d coinbase notes, unspent %v", len(notes), W.unspent)
	if len(notes) < 2 {
		fail("W holds fewer than two notes; the shielded sealer sealed too few blocks after the registration")
	}

	// A private transfer of one minted note to B (no public value), and an
	// unshield of another to a public address: the spend records name
	// neither the sealer nor the note's commitment.
	type spendRecord struct {
		Anchor       string
		Nullifiers   []string
		Outputs      []struct{ Commitment, Ciphertext string }
		PublicValue  string `json:"public_value"`
		Fee          string
		PublicOutput string `json:"public_output"`
	}
	spendOf := func(from, to *wallet, value *big.Int, vpub *big.Int, atBlock uint64, nt noteJSON) ([]byte, spendRecord) {
		pth := pathAt(nt.Position, atBlock)
		sib := make([]string, 0, 32)
		for _, s := range pth.Siblings {
			sib = append(sib, strings.TrimPrefix(s, "0x"))
		}
		pathsFile := fresh("paths")
		pj, _ := json.Marshal(map[string]interface{}{"anchor": strings.TrimPrefix(pth.Anchor, "0x"), "paths": map[string][]string{strconv.FormatUint(nt.Position, 10): sib}})
		must(os.WriteFile(pathsFile, pj, 0o600))
		wit, rec := fresh("spend-w"), fresh("spend-r")
		runJSON(walletBn, "--wallet", from.file, "spend-prepare", "--notes", from.notes, "--positions", strconv.FormatUint(nt.Position, 10), "--paths", pathsFile,
			"--to", to.addr, "--value", value.String(), "--memo", "lab", "--fee", fee.String(), "--public-value", vpub.String(), "--public-output", hex.EncodeToString(publicOut[:]),
			"--chain-id", strconv.FormatUint(cid, 10), "--witness-out", wit, "--record-out", rec)
		proof, ms, nb := prove(wit)
		res.SpendProveMs, res.SpendProofBytes = ms, nb
		var r spendRecord
		text, err := os.ReadFile(rec)
		must(err)
		must(json.Unmarshal(text, &r))
		sp := pool.Spend{Anchor: h32(r.Anchor), PublicValue: bigOf(r.PublicValue), Fee: bigOf(r.Fee), PublicOutput: common.HexToAddress(r.PublicOutput), Proof: proof}
		for _, nf := range r.Nullifiers {
			sp.Nullifiers = append(sp.Nullifiers, h32(nf))
		}
		for _, o := range r.Outputs {
			ct, err := hex.DecodeString(o.Ciphertext)
			must(err)
			sp.Outputs = append(sp.Outputs, pool.Output{Commitment: h32(o.Commitment), Ciphertext: ct})
		}
		return sp.Encode(), r
	}
	first := notes[0]
	half := new(big.Int).Div(bigOf(first.Value), big.NewInt(2))
	data, rec1 := spendOf(W, B, half, new(big.Int), head(), first)
	infoBefore := poolInfo(ec, nil)
	tx1 := send(faucet, poolAddr, nil, data)
	rc1 := wait(tx1)
	feesBurned.Add(feesBurned, fee)
	second := notes[1]
	whole := new(big.Int).Sub(bigOf(second.Value), fee)
	data, rec2 := spendOf(W, W, new(big.Int), whole, head(), second)
	tx2 := send(faucet, poolAddr, nil, data)
	rc2 := wait(tx2)
	feesBurned.Add(feesBurned, fee)
	unshieldedOut.Add(unshieldedOut, whole)
	infoAfter := poolInfo(ec, nil)
	if infoAfter.Count != infoBefore.Count+4 && infoAfter.Count < infoBefore.Count+4 {
		fail("the two spends were not admitted: count %d before, %d after", infoBefore.Count, infoAfter.Count)
	}
	for _, nf := range append(rec1.Nullifiers, rec2.Nullifiers...) {
		if nf == first.CM || nf == second.CM {
			fail("a nullifier equals the spent note's commitment")
		}
	}
	if balanceAt(ec, publicOut, rc2.BlockNumber.Uint64()).Cmp(whole) != 0 {
		fail("the unshield did not pay the public output %v", whole)
	}
	scan(W)
	scan(B)
	log("transfer admitted at %d (W to B, %v), unshield admitted at %d (%v to %s); W %v B %v", rc1.BlockNumber.Uint64(), half, rc2.BlockNumber.Uint64(), whole, publicOut.Hex(), W.unspent, B.unspent)

	// What a watcher sees of the two spends, exactly.
	res.Watcher = append(res.Watcher,
		fmt.Sprintf("the registration at block %d: a transaction from %s to the pool address carrying the 1,227-byte address (pk %s..., the encapsulation key, the diversifier): the watcher learns that this sealer pays to this pk", res.RegistrationBlock, sealerAddr.Hex(), hex.EncodeToString(reg.PK[:4])),
		fmt.Sprintf("every block the sealer seals after %d: the coinbase %s, the mint record (sealer, value, leaf position, r, cm) under the pool address's storage and on igneum_poolBlock: the watcher learns that the sealer earned the value and that leaf cm is the sealer's", res.RegistrationBlock, sealerAddr.Hex()),
		fmt.Sprintf("the transfer at block %d: a transaction from the carrier %s (not the sealer) to the pool address with anchor %s..., nullifiers %s... and %s..., two output commitments with ciphertexts, public value 0, fee %s: nothing in it is the sealer's address, a minted commitment, a leaf position or a value", rc1.BlockNumber.Uint64(), faucetAddr.Hex(), rec1.Anchor[:8], rec1.Nullifiers[0][:8], rec1.Nullifiers[1][:8], rec1.Fee),
		fmt.Sprintf("the unshield at block %d: the same form with public value %s to %s: the watcher learns that some note worth at least that left the pool to that address, not which note, not whose", rc2.BlockNumber.Uint64(), whole, publicOut.Hex()),
		"the nullifier of a minted note is H(H(nk, cm), position) under the sealer's nullifier key nk, which the registration does not carry (the address is pk, ek, d; nk is not derivable from pk): the watcher holding every mint record cannot compute any minted note's nullifier, so no spend is tied to a block",
		"what stays public: that the sealer mines, every block it sealed, every amount it earned, and that it pays to pk; what the amount correlation gives: the unshield's public value equals one note's value less the fee, so a watcher comparing it against the mint records' values finds the match (the caveat of the spec's section 8 on amounts and timing; the private transfer gives nothing)",
	)

	// The reconciliation to the wei, at one height on both nodes.
	H := head()
	for {
		h2, err := ec2.BlockNumber(ctx)
		must(err)
		if h2 >= H {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	res.Head = H
	info := poolInfo(ec, &H)
	info2 := poolInfo(ec2, &H)
	res.Verifier, res.Hash = info.Verifier, info.Hash
	res.SecondNodeAgrees = info2.Root == info.Root && info2.Count == info.Count && info2.Turnstile.Cmp(info.Turnstile) == 0 && info2.Minted.Cmp(info.Minted) == 0
	var ids, failing []string
	id := func(ok bool, f string, a ...interface{}) {
		line := fmt.Sprintf(f, a...)
		if ok {
			line += " PASS"
		} else {
			line += " FAIL"
			failing = append(failing, line)
		}
		ids = append(ids, line)
	}
	// identity M1: per block, the mint record against the sealer share.
	mintSum, publicCredited, fundSum, tipsToPublic := new(big.Int), new(big.Int), new(big.Int), new(big.Int)
	shieldedPublic := new(big.Int) // the shielded sealer's credits before its registration
	var mintedBlocks, shieldedBlocks, publicBlocks, m1Fail int
	var publicSealer common.Address
	for h := uint64(1); h <= H; h++ {
		is := issuanceAt(h)
		pb := poolBlockAt(ec, h)
		fundSum.Add(fundSum, hb(is.Fund))
		if is.Sealer == sealerAddr {
			shieldedBlocks++
			if h > res.RegistrationBlock {
				if len(pb.Mints) != 1 || common.HexToAddress(pb.Mints[0].Sealer) != sealerAddr || hb(pb.Mints[0].Value).Cmp(hb(is.Credited)) != 0 {
					m1Fail++
				} else {
					mintedBlocks++
					mintSum.Add(mintSum, hb(pb.Mints[0].Value))
				}
				continue
			}
			if len(pb.Mints) != 0 {
				m1Fail++
			}
			shieldedPublic.Add(shieldedPublic, hb(is.Credited))
			continue
		}
		if len(pb.Mints) != 0 {
			m1Fail++
		}
		if publicSealer == (common.Address{}) {
			publicSealer = is.Sealer
		} else if publicSealer != is.Sealer {
			fail("a third sealer %s at %d", is.Sealer.Hex(), h)
		}
		publicBlocks++
		publicCredited.Add(publicCredited, hb(is.Credited))
		tipsToPublic.Add(tipsToPublic, hb(is.PriorityFees))
	}
	res.PublicSealer = publicSealer.Hex()
	res.ShieldedSealerBlocks, res.MintedBlocks, res.PublicSealerBlocks = shieldedBlocks, mintedBlocks, publicBlocks
	id(m1Fail == 0 && mintSum.Cmp(info.Minted) == 0 && uint64(mintedBlocks) == info.Count-4,
		"identity M1 (per block to the wei: a block of the shielded sealer after its registration carries one mint record whose value is the block's sealer share, igneum_blockIssuance's credited; every other block carries none; the records' sum is the pool's minted): %d blocks, %d failing; %d minted blocks, minted %v wei; the pool's minted %v, notes %d (%d minted, 4 from the two spends)",
		H, m1Fail, mintedBlocks, mintSum, info.Minted, info.Count, mintedBlocks)
	// identity M2: the turnstile.
	want := new(big.Int).Sub(new(big.Int).Add(info.Minted, new(big.Int)), new(big.Int).Add(unshieldedOut, feesBurned))
	id(info.Turnstile.Cmp(want) == 0 && info.Turnstile.Cmp(info.Balance) == 0,
		"identity M2 (the turnstile: shielded in + minted in - unshielded out - fees burned = turnstile = the pool address's balance): 0 + %v - %v - %v = %v; turnstile %v, balance %v",
		info.Minted, unshieldedOut, feesBurned, want, info.Turnstile, info.Balance)
	// identity M3: the supply.
	sup := supplyAt(ec, H)
	fundAddr := common.HexToAddress("0x0000000000000000000000000000000000007310")
	fundBal := new(big.Int).Sub(balanceAt(ec, fundAddr, H), balanceAt(ec, fundAddr, 0))
	issuedWant := new(big.Int).Add(new(big.Int).Add(hb(sup.FundIssued), publicCredited), new(big.Int).Add(shieldedPublic, info.Minted))
	id(hb(sup.MintedShielded).Cmp(info.Minted) == 0 && hb(sup.Issued).Cmp(issuedWant) == 0 && fundBal.Cmp(hb(sup.FundIssued)) == 0 && fundBal.Cmp(fundSum) == 0,
		"identity M3 (the supply: issued = fund + credited public + minted; igneum_supply's mintedShielded = the pool's minted; the fund's balance = fundIssued = the blocks' fund parts): issued %v = fund %v + public sealer %v + shielded sealer before registration %v + minted %v (%v); mintedShielded %v; fund balance %v, blocks' fund parts %v",
		hb(sup.Issued), hb(sup.FundIssued), publicCredited, shieldedPublic, info.Minted, issuedWant, hb(sup.MintedShielded), fundBal, fundSum)
	// identity M4: the sealers' public balances.
	pubBal := balanceAt(ec, publicSealer, H)
	pubWant := new(big.Int).Add(publicCredited, tipsToPublic)
	sealerBal := balanceAt(ec, sealerAddr, H)
	sealerWant := new(big.Int).Add(new(big.Int).Sub(funded, regGas), shieldedPublic)
	// tips of the shielded sealer's blocks before its registration (its own registration's block rides among them)
	tipsToShielded := new(big.Int)
	for h := uint64(1); h <= H; h++ {
		if is := issuanceAt(h); is.Sealer == sealerAddr {
			tipsToShielded.Add(tipsToShielded, hb(is.PriorityFees))
		}
	}
	sealerWant.Add(sealerWant, tipsToShielded)
	id(pubBal.Cmp(pubWant) == 0 && sealerBal.Cmp(sealerWant) == 0,
		"identity M4 (the public sealer's balance = its blocks' credited shares + the tips its blocks carried; the shielded sealer's balance = its funding - its registration's gas + its shares before the registration + the tips of every block it sealed (the EVM's credit to the coinbase, the users' money, public before and after): public %v = %v + %v; shielded %v = %v - %v + %v + %v",
		pubBal, publicCredited, tipsToPublic, sealerBal, funded, regGas, shieldedPublic, tipsToShielded)
	// identity M5: the wallets against the turnstile.
	wallets := new(big.Int).Add(W.unspent, B.unspent)
	id(wallets.Cmp(info.Turnstile) == 0, "identity M5 (the wallets' unspent notes = the turnstile: W %v + B %v = %v against %v)", W.unspent, B.unspent, wallets, info.Turnstile)
	// identity M6: the second node.
	id(res.SecondNodeAgrees, "identity M6 (the second node at %d: root %s, count %d, turnstile %v, minted %v; the public sealer's node reads the same root %s, count %d, turnstile %v, minted %v)", H, info.Root.Hex()[:10], info.Count, info.Turnstile, info.Minted, info2.Root.Hex()[:10], info2.Count, info2.Turnstile, info2.Minted)
	// identity M7: the mint record recomputed from the header and the registration, byte for byte.
	var m7Fail int
	for h := res.RegistrationBlock + 1; h <= H; h++ {
		pb := poolBlockAt(ec, h)
		if len(pb.Mints) == 0 {
			continue
		}
		hd, err := ec.HeaderByNumber(ctx, new(big.Int).SetUint64(h))
		must(err)
		r := pool.MintRandomness(cid, h, hd.ParentHash, sealerAddr)
		cm := pool.Poseidon2{}.Commitment(hb(pb.Mints[0].Value), reg.PK, r)
		if hex.EncodeToString(r[:]) != strings.TrimPrefix(pb.Mints[0].Randomness, "0x") || hex.EncodeToString(cm[:]) != strings.TrimPrefix(pb.Mints[0].Commitment, "0x") {
			m7Fail++
		}
		var leaf common.Hash
		must(ec2.Client().CallContext(ctx, &leaf, "igneum_poolLeaf", pb.Mints[0].Position, fmt.Sprintf("0x%x", h)))
		if leaf != common.Hash(cm) {
			m7Fail++
		}
	}
	id(m7Fail == 0, "identity M7 (every mint record recomputed by the driver from the header, the chain id and the registration alone: r and cm byte for byte, and the second node's leaf at the position): %d records, %d failing", mintedBlocks, m7Fail)
	res.Identities, res.Failing = ids, failing
	res.Reconciled = len(failing) == 0
	res.Notes, res.Minted, res.Turnstile, res.PoolBalance = info.Count, info.Minted.String(), info.Turnstile.String(), info.Balance.String()
	res.UnshieldedOut, res.FeesBurned = unshieldedOut.String(), feesBurned.String()
	res.WalletWUnspent, res.WalletBUnspent = W.unspent.String(), B.unspent.String()
	res.SupplyIssued, res.SupplyFund, res.SupplyMintedShielded, res.PublicCredited = hb(sup.Issued).String(), hb(sup.FundIssued).String(), hb(sup.MintedShielded).String(), publicCredited.String()
	// The state a coinbase note costs: the leaf, the key, the value, the
	// height record (four 32-byte slots, keys hashed), plus the frontier's
	// amortised two; counted as slot values written.
	res.MintStateBytes = 4*32 + 2*32
	res.WallSeconds = time.Since(started).Seconds()

	lines := []string{
		fmt.Sprintf("The shielded coinbase ledger, chain %d, blocks 1 to %d, head %d, read %s", cid, H, H, time.Now().UTC().Format(time.RFC3339)),
		fmt.Sprintf("sealers: %s shielded from block %d (registration %d bytes), %s public; blocks %d by the shielded sealer (%d minted, %d before the registration), %d by the public sealer", sealerAddr.Hex(), res.RegistrationBlock+1, len(regData), publicSealer.Hex(), shieldedBlocks, mintedBlocks, shieldedBlocks-mintedBlocks, publicBlocks),
		fmt.Sprintf("the pool at %d: verifier %s, hash %s, notes %d, root %s, seq %d, turnstile %v, minted %v, balance %v", H, info.Verifier, info.Hash, info.Count, info.Root.Hex(), info.Seq, info.Turnstile, info.Minted, info.Balance),
	}
	lines = append(lines, ids...)
	lines = append(lines, "refusal rows:")
	for _, r := range res.RefusalRows {
		lines = append(lines, "  "+r)
	}
	lines = append(lines, "what a watcher sees:")
	for _, w := range res.Watcher {
		lines = append(lines, "  "+w)
	}
	verdict := "PASS"
	if !res.Reconciled {
		verdict = "FAIL"
	}
	lines = append(lines, fmt.Sprintf("reconciled to the wei: %s (%d identities, %d failing); seconds %.0f", verdict, len(ids), len(failing), res.WallSeconds), "")
	if *out != "" {
		text, _ := json.MarshalIndent(res, "", "  ")
		must(os.WriteFile(*out, text, 0o644))
		must(os.WriteFile(filepath.Join(filepath.Dir(*out), "identity.txt"), []byte(strings.Join(lines, "\n")), 0o644))
	}
	fmt.Println(strings.Join(lines, "\n"))
	if !res.Reconciled {
		os.Exit(2)
	}
}
