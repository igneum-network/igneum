// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).

// igneum-regtx signs registry transactions (cut B) for a test row: the
// document file as the data behind the selector, signed by a keystore file,
// once unprotected (Homestead) and once for a named chain id, printed as raw
// transaction hex for eth_sendRawTransaction (the security review's F-144 replay row,
// 15 October 2026).
//
// Usage: igneum-regtx <keystore file> <password> <document.json> <nonce> <chainid...>
//    or: igneum-regtx --keyfile <hex secp256k1 key file> <document.json> <nonce> <chainid...>
// A chain id of 0 signs unprotected. The --keyfile form is the custody's
// (the signer's host runs it as the key's user: the key
// never leaves the file, the raw transaction comes out for
// eth_sendRawTransaction on any node of the chain).
package main

import (
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"os"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/keystore"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus/igneum"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

func main() {
	var priv *ecdsa.PrivateKey
	var addr common.Address
	args := os.Args[1:]
	if len(args) >= 5 && args[0] == "--keyfile" {
		raw, err := os.ReadFile(args[1])
		if err != nil {
			panic(err)
		}
		k, err := crypto.HexToECDSA(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(raw)), "0x")))
		if err != nil {
			panic(err)
		}
		priv, addr = k, crypto.PubkeyToAddress(k.PublicKey)
		args = args[2:]
	} else {
		if len(args) < 5 {
			fmt.Fprintln(os.Stderr, "usage: igneum-regtx <keystore file> <password> <document.json> <nonce> <chainid...> | --keyfile <key file> <document.json> <nonce> <chainid...>")
			os.Exit(2)
		}
		ks, err := os.ReadFile(args[0])
		if err != nil {
			panic(err)
		}
		key, err := keystore.DecryptKey(ks, args[1])
		if err != nil {
			panic(err)
		}
		priv, addr = key.PrivateKey, key.Address
		args = args[2:]
	}
	doc, err := os.ReadFile(args[0])
	if err != nil {
		panic(err)
	}
	nonce, err := strconv.ParseUint(args[1], 10, 64)
	if err != nil {
		panic(err)
	}
	for _, arg := range args[2:] {
		cid, err := strconv.ParseInt(arg, 10, 64)
		if err != nil {
			panic(err)
		}
		// the gas price: 2 gwei by default (the public test network's blocks carry a base fee of 1 gwei under the burn
		// rule, and a 1-wei transaction is never included: the references succession of 15 October 2026, 19:52 UK,
		// sat pending for 16 empty blocks, and one at exactly the base fee carries no tip for the sealer, whose floor
		// is above zero: 2 gwei by default); IGNEUM_REGTX_GAS_PRICE_WEI overrides it for a chain with another floor
		gasPrice := big.NewInt(2_000_000_000)
		if v := os.Getenv("IGNEUM_REGTX_GAS_PRICE_WEI"); v != "" {
			g, ok := new(big.Int).SetString(v, 10)
			if !ok || g.Sign() <= 0 {
				fmt.Fprintln(os.Stderr, "IGNEUM_REGTX_GAS_PRICE_WEI must be a positive decimal number of wei")
				os.Exit(2)
			}
			gasPrice = g
		}
		inner := &types.LegacyTx{Nonce: nonce, To: &igneum.VectorRegistryAddress, Gas: 3000000, GasPrice: gasPrice, Data: igneum.RegistryTxData(doc)}
		var signer types.Signer = types.HomesteadSigner{}
		if cid != 0 {
			signer = types.LatestSignerForChainID(big.NewInt(cid))
		}
		tx := types.MustSignNewTx(priv, signer, inner)
		raw, err := tx.MarshalBinary()
		if err != nil {
			panic(err)
		}
		fmt.Printf("chainid %d protected %v from %s hash %s\n%s\n", cid, tx.Protected(), addr.Hex(), tx.Hash().Hex(), hexutil.Encode(raw))
	}
}
