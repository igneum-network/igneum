// Copyright 2026 The Igneum Authors
// This file is part of go-ethereum (Igneum fork).

// money-vault-send carries one vault record (the wallet's record JSON and
// the prover's proof) to the pool address from the development chain's
// faucet key and says whether its first output was admitted (the lab's hand
// tool beside money-vault-lab; docs/money/STAGE2-CUT1-VAULT-2026-10-18.md).
package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/consensus/igneum/pool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/params"
)

func h32(s string) (out [32]byte) {
	b, err := hex.DecodeString(strings.TrimPrefix(s, "0x"))
	if err != nil || len(b) != 32 {
		fmt.Fprintf(os.Stderr, "not 32 bytes of hex: %q\n", s)
		os.Exit(1)
	}
	copy(out[:], b)
	return
}

func main() {
	rpcURL := flag.String("rpc", "http://127.0.0.1:8741", "the sealing node's HTTP endpoint")
	recFile := flag.String("record", "", "the wallet's record JSON")
	proofFile := flag.String("proof", "", "the proof bytes")
	flag.Parse()
	ctx := context.Background()
	text, err := os.ReadFile(*recFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var r map[string]interface{}
	if err := json.Unmarshal(text, &r); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	proof, err := os.ReadFile(*proofFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	str := func(k string) string { return fmt.Sprint(r[k]) }
	vr := pool.Vault{Tag: byte(r["kind"].(float64)), Anchor: h32(str("anchor")), Ptag: h32(str("ptag")), RevokedRoot: h32(str("revoked_root")), Revealed: h32(str("revealed")), Height: uint64(r["height"].(float64)), Time: uint64(r["time"].(float64)), Proof: proof}
	for _, nf := range r["nullifiers"].([]interface{}) {
		vr.Nullifiers = append(vr.Nullifiers, h32(nf.(string)))
	}
	for _, o := range r["outputs"].([]interface{}) {
		om := o.(map[string]interface{})
		ct, _ := hex.DecodeString(fmt.Sprint(om["ciphertext"]))
		vr.Outputs = append(vr.Outputs, pool.Output{Commitment: h32(fmt.Sprint(om["commitment"])), Ciphertext: ct})
	}
	ec, err := ethclient.Dial(*rpcURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	chainID, _ := ec.ChainID(ctx)
	poolAddr := pool.Address(chainID.Uint64())
	key := params.IgneumDevFaucetKey()
	from := crypto.PubkeyToAddress(key.PublicKey)
	nonce, _ := ec.PendingNonceAt(ctx, from)
	tip, _ := ec.SuggestGasTipCap(ctx)
	hd, _ := ec.HeaderByNumber(ctx, nil)
	data := vr.Encode()
	tx := types.MustSignNewTx(key, types.LatestSignerForChainID(chainID), &types.DynamicFeeTx{ChainID: chainID, Nonce: nonce, To: &poolAddr, Gas: uint64(21000 + 16*len(data) + 4*32 + 50000), GasTipCap: tip, GasFeeCap: new(big.Int).Add(new(big.Int).Mul(hd.BaseFee, big.NewInt(4)), tip), Data: data})
	if err := ec.SendTransaction(ctx, tx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var blk uint64
	for {
		rc, err := ec.TransactionReceipt(ctx, tx.Hash())
		if err == nil && rc != nil {
			blk = rc.BlockNumber.Uint64()
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	var pb struct {
		Outputs    []struct{ Commitment string }
		Statements []string `json:"statements"`
	}
	ec.Client().CallContext(ctx, &pb, "igneum_poolBlock", fmt.Sprintf("0x%x", blk))
	cm := fmt.Sprint(r["outputs"].([]interface{})[0].(map[string]interface{})["commitment"])
	admitted := false
	for _, o := range pb.Outputs {
		if strings.TrimPrefix(o.Commitment, "0x") == cm {
			admitted = true
		}
	}
	out, _ := json.Marshal(map[string]interface{}{"block": blk, "tx": tx.Hash().Hex(), "admitted": admitted, "statements": pb.Statements, "recordBytes": len(data)})
	fmt.Println(string(out))
}
