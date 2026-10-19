// Copyright 2026 The core-geth Authors
// This file is part of the core-geth library.
//
// The core-geth library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The core-geth library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the core-geth library. If not, see <http://www.gnu.org/licenses/>.

package ethtest

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus/igneum"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	_ "github.com/ethereum/go-ethereum/consensus/igneum/pow/null" // the development algorithm, for the sealer and the verifier
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/params/types/genesisT"
	"github.com/ethereum/go-ethereum/params/vars"
	"github.com/ethereum/go-ethereum/rlp"
)

// The devp2p fixture set for the Igneum rule set (PA-28). It replaces the
// upstream hivechain output, which drove a post-merge node through the engine
// API. Everything here is deterministic: the sender keys are keccak256 of a
// public phrase and are dev-only, like the development faucet; the chain is
// generated on the development genesis and sealed block by block with the
// null algorithm, so the node under test verifies every seal; the head state dump is built from the generator's own record of
// every account it touched and checked against the state trie's leaf count.
//
// Regenerate with
//
//	IGNEUM_WRITE_FIXTURES=1 go test -run TestWriteIgneumFixtures ./cmd/devp2p/internal/ethtest/
//
// which writes testdata/{genesis.json,chain.rlp,headstate.json,oldstate.json,
// accounts.json,headblock.json,IGNEUM-FIXTURE,IGNEUM-SNAP-EXPECTATIONS}. The
// markers record the genesis and head hashes and the older dump's height;
// TestEthSuite requires the first, TestSnapSuite both.
const (
	igneumFixtureLength  = 300
	igneumFixtureSenders = 20
	igneumFixtureMarker  = "IGNEUM-FIXTURE"
)

var (
	igneumFixtureSenderPhrase = "igneum devp2p fixture sender %d: dev-only, public key material, never fund on any other chain"
	igneumFixtureSenderFunds  = new(big.Int).Mul(big.NewInt(1_000_000), big.NewInt(vars.Ether))
)

func igneumFixtureSenderKey(i int) *ecdsa.PrivateKey {
	key, err := crypto.ToECDSA(crypto.Keccak256([]byte(fmt.Sprintf(igneumFixtureSenderPhrase, i))))
	if err != nil {
		panic(err)
	}
	return key
}

// igneumFixtureGenesis is the development genesis plus the fixture accounts:
// the senders and one storage contract with code and eight slots.
func igneumFixtureGenesis() *genesisT.Genesis {
	g := params.DefaultIgneumDevGenesisBlock()
	g.ExtraData = []byte("igneum-devp2p-fixture")
	for i := 0; i < igneumFixtureSenders; i++ {
		addr := crypto.PubkeyToAddress(igneumFixtureSenderKey(i).PublicKey)
		g.Alloc[addr] = genesisT.GenesisAccount{Balance: new(big.Int).Set(igneumFixtureSenderFunds)}
	}
	storage := make(map[common.Hash]common.Hash)
	for s := 1; s <= 8; s++ {
		storage[common.BigToHash(big.NewInt(int64(s)))] = common.BigToHash(big.NewInt(int64(0x1000 + s)))
	}
	g.Alloc[igneumFixtureStorageAcct] = genesisT.GenesisAccount{
		Balance: big.NewInt(1),
		Code:    igneumFixtureRuntime(0xff),
		Storage: storage,
	}
	return g
}

// igneumFixtureRuntime is a four-byte runtime (PUSH1 n, POP, STOP); n varies
// the code hash from contract to contract.
func igneumFixtureRuntime(n byte) []byte { return []byte{0x60, n, 0x50, 0x00} }

// igneumFixtureInitCode stores slots 1..k, then returns the runtime.
func igneumFixtureInitCode(n byte, k int) []byte {
	var code []byte
	for s := 1; s <= k; s++ {
		code = append(code, 0x61, byte(0x20), byte(s), 0x60, byte(s), 0x55) // PUSH2 0x20ss, PUSH1 s, SSTORE
	}
	code = append(code, 0x63, 0x60, n, 0x50, 0x00) // PUSH4 runtime
	code = append(code, 0x60, 0x00, 0x52)          // PUSH1 0, MSTORE
	code = append(code, 0x60, 0x04, 0x60, 0x1c, 0xf3) // PUSH1 4, PUSH1 28, RETURN
	return code
}

// igneumFixtureSlots is the storage a contract created at generator index i
// receives: slots 1..k hold 0x20ss.
func igneumFixtureSlots(k int) map[common.Hash]common.Hash {
	m := make(map[common.Hash]common.Hash, k)
	for s := 1; s <= k; s++ {
		m[common.BigToHash(big.NewInt(int64(s)))] = common.BigToHash(big.NewInt(int64(0x2000 + s)))
	}
	return m
}

type igneumFixtureAccount struct {
	storage map[common.Hash]common.Hash
}

func TestWriteIgneumFixtures(t *testing.T) {
	out := os.Getenv("IGNEUM_WRITE_FIXTURES")
	if out == "" {
		t.Skip("set IGNEUM_WRITE_FIXTURES=1 (or to a directory) to regenerate the devp2p fixture set")
	}
	if out == "1" {
		out = "./testdata"
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}

	genesis := igneumFixtureGenesis()
	parts, err := igneum.PartsFor(genesis.Config.GetIgneumConfig())
	if err != nil {
		t.Fatal(err)
	}
	engine := igneum.NewFaker(parts)

	var (
		keys     = make([]*ecdsa.PrivateKey, igneumFixtureSenders)
		senders  = make([]common.Address, igneumFixtureSenders)
		touched  = map[common.Address]*igneumFixtureAccount{}
		touch    = func(a common.Address) {
			if touched[a] == nil {
				touched[a] = &igneumFixtureAccount{}
			}
		}
		creations int
	)
	for i := range keys {
		keys[i] = igneumFixtureSenderKey(i)
		senders[i] = crypto.PubkeyToAddress(keys[i].PublicKey)
		touch(senders[i])
	}
	// The suite signs its own transactions from the two lowest sender
	// addresses (Chain.GetSender(0) and (1)) at nonce 0, so the chain never
	// spends from them: spenders are the other senders, in address order.
	sorted := make(Addresses, len(senders))
	copy(sorted, senders)
	sort.Sort(sorted)
	var spenders []int
	for i, a := range senders {
		if a != sorted[0] && a != sorted[1] {
			spenders = append(spenders, i)
		}
	}
	spender := func(k int) int { return spenders[k%len(spenders)] }
	for addr := range genesis.Alloc {
		touch(addr)
	}
	touched[igneumFixtureStorageAcct].storage = genesis.Alloc[igneumFixtureStorageAcct].Storage
	touch(parts.FundAddress)

	sign := func(i int, tx *types.Transaction, signer types.Signer) *types.Transaction {
		signed, err := types.SignTx(tx, signer, keys[i])
		if err != nil {
			t.Fatal(err)
		}
		return signed
	}
	gen := func(i int, b *core.BlockGen) {
		const n = igneumFixtureSenders
		b.SetCoinbase(senders[i%n])
		feeCap := new(big.Int).Mul(b.BaseFee(), big.NewInt(2))
		tip := big.NewInt(vars.GWei)

		// Every block: a legacy transfer.
		from := spender(i)
		to := senders[(i+7)%n]
		b.AddTx(sign(from, types.NewTx(&types.LegacyTx{
			Nonce: b.TxNonce(senders[from]), To: &to, Value: big.NewInt(vars.Ether / 1000),
			Gas: 21000, GasPrice: feeCap,
		}), b.Signer()))

		// Odd blocks: a dynamic-fee transfer.
		if i%2 == 1 {
			from = spender(i + 3)
			to = senders[(i+11)%n]
			b.AddTx(sign(from, types.NewTx(&types.DynamicFeeTx{
				ChainID: genesis.Config.GetChainID(), Nonce: b.TxNonce(senders[from]), To: &to,
				Value: big.NewInt(vars.Ether / 1000), Gas: 21000, GasFeeCap: feeCap, GasTipCap: tip,
			}), b.Signer()))
		}

		// Every fifth block: an access-list transfer naming the storage contract.
		if i%5 == 2 {
			from = spender(i + 5)
			to = senders[(i+13)%n]
			b.AddTx(sign(from, types.NewTx(&types.AccessListTx{
				ChainID: genesis.Config.GetChainID(), Nonce: b.TxNonce(senders[from]), To: &to,
				Value: big.NewInt(vars.Ether / 1000), Gas: 30000, GasPrice: feeCap,
				AccessList: types.AccessList{{Address: igneumFixtureStorageAcct, StorageKeys: []common.Hash{common.BigToHash(big.NewInt(1))}}},
			}), b.Signer()))
		}

		// Every tenth block: a contract creation with one to six storage slots.
		if i%10 == 4 {
			from = spender(i + 1)
			k := (i/10)%6 + 1
			nonce := b.TxNonce(senders[from])
			b.AddTx(sign(from, types.NewTx(&types.LegacyTx{
				Nonce: nonce, Value: big.NewInt(0), Gas: 400_000, GasPrice: feeCap,
				Data: igneumFixtureInitCode(byte(i), k),
			}), b.Signer()))
			created := crypto.CreateAddress(senders[from], nonce)
			touch(created)
			touched[created].storage = igneumFixtureSlots(k)
			creations++
		}
	}

	// Generate the whole chain unsealed (the chain maker knows every ancestor,
	// which the difficulty rule needs), then seal it for real in order with
	// the null algorithm, re-linking each block to its sealed parent: the seal
	// changes a block's hash and nothing else, so bodies, state roots and
	// difficulties stand. The node under test verifies every seal.
	algo, err := pow.NewDev(genesis.Config.GetIgneumConfig().Algorithm, genesis.Config.GetIgneumConfig().AlgorithmParams)
	if err != nil {
		t.Fatal(err)
	}
	db, unsealed, receipts := core.GenerateChainWithGenesis(genesis, engine, igneumFixtureLength, gen)
	blocks := make([]*types.Block, 0, len(unsealed))
	parentHash := unsealed[0].ParentHash()
	for i, b := range unsealed {
		header := b.Header()
		header.ParentHash = parentHash
		nonce, mix, ok := pow.Solve(algo, engine.SealHash(header), header.Number.Uint64(), header.Difficulty, 0, nil)
		if !ok {
			t.Fatalf("block %d: no seal found", i+1)
		}
		header.Nonce, header.MixDigest = types.EncodeNonce(nonce), mix
		if err := pow.Verify(algo, engine.SealHash(header), header.Number.Uint64(), nonce, mix, header.Difficulty); err != nil {
			t.Fatalf("block %d: the seal does not verify: %v", i+1, err)
		}
		sealed := b.WithSeal(header)
		blocks = append(blocks, sealed)
		parentHash = sealed.Hash()
	}

	// The sealed chain imports into a node with the real development engine
	// (seals verified), which is what the suite's node will do.
	verifier, err := igneum.NewForChain(genesis.Config.GetIgneumConfig(), nil, false, true)
	if err != nil {
		t.Fatal(err)
	}
	vchain, err := core.NewBlockChain(rawdb.NewMemoryDatabase(), nil, genesis, nil, verifier, vm.Config{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := vchain.InsertChain(blocks); err != nil {
		t.Fatalf("the sealed chain does not import at block %d: %v", n+1, err)
	}
	if vchain.CurrentBlock().Hash() != blocks[len(blocks)-1].Hash() {
		t.Fatal("the verifying chain did not reach the fixture head")
	}
	vchain.Stop()

	if creations != igneumFixtureLength/10 {
		t.Fatalf("created %d contracts, want %d", creations, igneumFixtureLength/10)
	}
	for i, rs := range receipts {
		for _, r := range rs {
			if r.Status != types.ReceiptStatusSuccessful {
				t.Fatalf("block %d: transaction %x failed", i+1, r.TxHash)
			}
		}
	}
	head := blocks[len(blocks)-1]

	// The head state, from the generator's record, checked against the trie;
	// and the state igneumFixtureOldDepth blocks behind it, for the snap
	// suite's old-root requests. Storage is keyed by hashed slot, as the snap
	// protocol and the trie see it, with the stripped value in hex.
	dumpAt := func(root common.Hash, check bool) state.Dump {
		statedb, err := state.New(root, state.NewDatabase(db), nil)
		if err != nil {
			t.Fatal(err)
		}
		dump := state.Dump{Root: fmt.Sprintf("%x", root), Accounts: map[string]state.DumpAccount{}}
		for addr, rec := range touched {
			if !statedb.Exist(addr) {
				if check {
					t.Fatalf("recorded account %x does not exist at the head", addr)
				}
				continue
			}
			acct := state.DumpAccount{
				Balance:     statedb.GetBalance(addr).String(),
				Nonce:       statedb.GetNonce(addr),
				Root:        statedb.GetStorageRoot(addr).Bytes(),
				CodeHash:    statedb.GetCodeHash(addr).Bytes(),
				AddressHash: crypto.Keccak256(addr.Bytes()),
			}
			if code := statedb.GetCode(addr); len(code) > 0 {
				acct.Code = code
			}
			for slot, want := range rec.storage {
				got := statedb.GetState(addr, slot)
				if check && got != want {
					t.Fatalf("account %x slot %x holds %x, want %x", addr, slot, got, want)
				}
				if got == (common.Hash{}) {
					continue
				}
				if acct.Storage == nil {
					acct.Storage = map[common.Hash]string{}
				}
				acct.Storage[crypto.Keccak256Hash(slot.Bytes())] = common.Bytes2Hex(bytes.TrimLeft(got.Bytes(), "\x00"))
			}
			dump.Accounts[addr.Hex()] = acct
		}
		leaves := statedb.RawDump(&state.DumpConfig{SkipCode: true, SkipStorage: true})
		if len(leaves.Accounts) != len(dump.Accounts) {
			t.Fatalf("the state trie at %x has %d accounts, the generator recorded %d", root, len(leaves.Accounts), len(dump.Accounts))
		}
		// Every recorded account agrees with the trie's leaf, field by field,
		// and every recorded storage rebuilds to the account's storage root;
		// then the whole dump rebuilds to the state root. This is exactly how
		// the snap suite reads the dump, so a fixture that passes here serves.
		byHash := map[common.Hash]state.DumpAccount{}
		for _, leaf := range leaves.Accounts {
			byHash[common.BytesToHash(leaf.AddressHash)] = leaf
		}
		for addrHex, acct := range dump.Accounts {
			leaf, ok := byHash[common.BytesToHash(acct.AddressHash)]
			if !ok {
				t.Fatalf("%s: recorded but not a leaf of the trie at %x", addrHex, root)
			}
			if leaf.Balance != acct.Balance || leaf.Nonce != acct.Nonce || !bytes.Equal(leaf.Root, acct.Root) || !bytes.Equal(leaf.CodeHash, acct.CodeHash) {
				t.Fatalf("%s at %x: recorded {%s %d %x %x}, the trie says {%s %d %x %x}", addrHex, root, acct.Balance, acct.Nonce, acct.Root, acct.CodeHash, leaf.Balance, leaf.Nonce, leaf.Root, leaf.CodeHash)
			}
			if len(acct.Storage) > 0 || common.BytesToHash(acct.Root) != types.EmptyRootHash {
				if got, want := buildTrie(storageItems(acct.Storage), false).Hash(), common.BytesToHash(acct.Root); got != want {
					t.Fatalf("%s at %x: %d recorded slots rebuild to storage root %x, the account's is %x", addrHex, root, len(acct.Storage), got, want)
				}
			}
		}
		if got := buildTrie(accountItems(accountsInHashOrder(dumpAccounts(dump))), true).Hash(); got != root {
			t.Fatalf("the dump rebuilds to root %x, want %x", got, root)
		}
		return dump
	}
	dump := dumpAt(head.Root(), true)
	oldHeight := head.NumberU64() - igneumFixtureOldDepth
	oldDump := dumpAt(blocks[oldHeight-1].Root(), false)

	// Write the set.
	write := func(name string, data []byte) {
		if err := os.WriteFile(filepath.Join(out, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gj, err := json.MarshalIndent(genesis, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	write("genesis.json", gj)

	var chainRLP []byte
	for _, b := range blocks {
		enc, err := rlp.EncodeToBytes(b)
		if err != nil {
			t.Fatal(err)
		}
		chainRLP = append(chainRLP, enc...)
	}
	write("chain.rlp", chainRLP)

	sj, err := json.MarshalIndent(dump, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	write("headstate.json", sj)
	oj, err := json.MarshalIndent(oldDump, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	write("oldstate.json", oj)
	write("IGNEUM-SNAP-EXPECTATIONS", []byte(fmt.Sprintf("the snap suite derives its expectations from headstate.json and oldstate.json (height %d, %d accounts)\n", oldHeight, len(oldDump.Accounts))))

	accounts := map[string]map[string]hexutil.Bytes{}
	for i, k := range keys {
		accounts[senders[i].Hex()] = map[string]hexutil.Bytes{"key": crypto.FromECDSA(k)}
	}
	aj, err := json.MarshalIndent(accounts, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	write("accounts.json", aj)

	hj, err := json.MarshalIndent(head.Header(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	write("headblock.json", hj)

	addrs := make([]string, 0, len(dump.Accounts))
	for a := range dump.Accounts {
		addrs = append(addrs, a)
	}
	sort.Strings(addrs)
	marker := fmt.Sprintf("Igneum devp2p fixture set (PA-28)\ngenesis %x\nhead %x at %d\naccounts %d\nchain id %d\n",
		blocks[0].ParentHash(), head.Hash(), head.NumberU64(), len(addrs), genesis.Config.GetChainID())
	write(igneumFixtureMarker, []byte(marker))
	t.Logf("wrote %d blocks, %d accounts to %s; genesis %x head %x", len(blocks), len(addrs), out, blocks[0].ParentHash(), head.Hash())
}

// dumpAccounts keys a dump by address, as readState does.
func dumpAccounts(d state.Dump) map[common.Address]state.DumpAccount {
	out := make(map[common.Address]state.DumpAccount, len(d.Accounts))
	for k, a := range d.Accounts {
		out[common.HexToAddress(k)] = a
	}
	return out
}
