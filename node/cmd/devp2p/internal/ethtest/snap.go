// Copyright 2022 The go-ethereum Authors
// This file is part of go-ethereum.
//
// go-ethereum is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// go-ethereum is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with go-ethereum. If not, see <http://www.gnu.org/licenses/>.

package ethtest

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"
	"math/rand"
	"reflect"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth/protocols/snap"
	"github.com/ethereum/go-ethereum/internal/utesting"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/trie/trienode"
	"github.com/ethereum/go-ethereum/triedb"
	"github.com/holiman/uint256"
	"golang.org/x/crypto/sha3"
)

func (c *Conn) snapRequest(code uint64, msg any) (any, error) {
	if err := c.Write(snapProto, code, msg); err != nil {
		return nil, fmt.Errorf("could not write to connection: %v", err)
	}
	return c.ReadSnap()
}

func (s *Suite) TestSnapStatus(t *utesting.T) {
	conn, err := s.dialSnap()
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer conn.Close()
	if err := conn.peer(s.chain, nil); err != nil {
		t.Fatalf("peering failed: %v", err)
	}
}

type accRangeTest struct {
	nBytes       uint64
	root         common.Hash
	startingHash common.Hash
	limitHash    common.Hash

	expAccounts int
	expFirst    common.Hash
	expLast     common.Hash

	desc string
}

// TestSnapGetAccountRange various forms of GetAccountRange requests.
func (s *Suite) TestSnapGetAccountRange(t *utesting.T) {
	var (
		ffHash = common.MaxHash
		zero   = common.Hash{}

		// test values derived from the fixture's state dumps
		root        = s.chain.Head().Root()
		headstate   = s.chain.AccountsInHashOrder()
		head        = accountItems(headstate)
		firstKey    = common.BytesToHash(headstate[0].AddressHash)
		secondKey   = common.BytesToHash(headstate[1].AddressHash)
		storageRoot = findNonEmptyStorageRoot(headstate)
		oldHeight   = int(s.chain.Head().Number().Uint64()) - igneumFixtureOldDepth
		oldstate    = s.chain.OldAccountsInHashOrder()
	)
	if oldstate == nil {
		t.Fatalf("the fixture set carries no oldstate.json (the state %d blocks behind the head)", igneumFixtureOldDepth)
	}
	old := accountItems(oldstate)
	if got := buildTrie(head, true).Hash(); got != root {
		t.Fatalf("the head state dump rebuilds to root %x, the chain's head root is %x", got, root)
	}
	if got, want := buildTrie(old, true).Hash(), s.chain.RootAt(oldHeight); got != want {
		t.Fatalf("the old state dump rebuilds to root %x, the chain's root at %d is %x", got, oldHeight, want)
	}
	withExp := func(tc accRangeTest, exp rangeExpectation) accRangeTest {
		tc.expAccounts, tc.expFirst, tc.expLast = exp.n, exp.first, exp.last
		return tc
	}

	tests := []accRangeTest{
		// Tests decreasing the number of bytes
		withExp(accRangeTest{nBytes: 4000, root: root, startingHash: zero, limitHash: ffHash,
			desc: `In this test, we request the entire state range, but limit the response to 4000 bytes.`,
		}, expectRange(head, zero, ffHash, 4000)),
		withExp(accRangeTest{nBytes: 3000, root: root, startingHash: zero, limitHash: ffHash,
			desc: `In this test, we request the entire state range, but limit the response to 3000 bytes.`,
		}, expectRange(head, zero, ffHash, 3000)),
		withExp(accRangeTest{nBytes: 2000, root: root, startingHash: zero, limitHash: ffHash,
			desc: `In this test, we request the entire state range, but limit the response to 2000 bytes.`,
		}, expectRange(head, zero, ffHash, 2000)),
		withExp(accRangeTest{nBytes: 1, root: root, startingHash: zero, limitHash: ffHash,
			desc: `In this test, we request the entire state range, but limit the response to 1 byte.
The server should return the first account of the state.`,
		}, expectRange(head, zero, ffHash, 1)),
		withExp(accRangeTest{nBytes: 0, root: root, startingHash: zero, limitHash: ffHash,
			desc: `Here we request with a responseBytes limit of zero.
The server should return one account.`,
		}, expectRange(head, zero, ffHash, 0)),

		// Tests variations of the range
		withExp(accRangeTest{nBytes: 4000, root: root, startingHash: zero, limitHash: firstKey,
			desc: `In this test, the range is [zero, firstKey], which is a single account.
The server should return the first account.`,
		}, expectRange(head, zero, firstKey, 4000)),
		withExp(accRangeTest{nBytes: 4000, root: root, startingHash: zero, limitHash: hashAdd(firstKey, -1),
			desc: `In this test, the range ends one before the first available key.
The server should still return the first account (the next available key).`,
		}, expectRange(head, zero, hashAdd(firstKey, -1), 4000)),
		withExp(accRangeTest{nBytes: 4000, root: root, startingHash: zero, limitHash: secondKey,
			desc: `In this test, the range is [zero, secondKey]: the first two accounts.`,
		}, expectRange(head, zero, secondKey, 4000)),
		withExp(accRangeTest{nBytes: 4000, root: root, startingHash: firstKey, limitHash: ffHash,
			desc: `In this test, startingHash is exactly the first available account key.
The server should return the first available account of the state as the first item.`,
		}, expectRange(head, firstKey, ffHash, 4000)),
		withExp(accRangeTest{nBytes: 4000, root: root, startingHash: hashAdd(firstKey, 1), limitHash: ffHash,
			desc: `In this test, startingHash is after the first available key.
The server should return the second account of the state as the first item.`,
		}, expectRange(head, hashAdd(firstKey, 1), ffHash, 4000)),

		// Roots
		{
			nBytes: 4000, root: common.Hash{0x13, 0x37}, startingHash: zero, limitHash: ffHash,
			expAccounts: 0, expFirst: zero, expLast: zero,
			desc: `This test requests a non-existent state root.`,
		},
		{
			nBytes: 4000, root: s.chain.RootAt(0), startingHash: zero, limitHash: ffHash,
			expAccounts: 0, expFirst: zero, expLast: zero,
			desc: `This test requests data at the state root of the genesis block. We expect the
server to return no data because genesis is older than 127 blocks.`,
		},
		withExp(accRangeTest{nBytes: 4000, root: s.chain.RootAt(oldHeight), startingHash: zero, limitHash: ffHash,
			desc: `This test requests data at a state root that is 127 blocks old.
We expect the server to have this state available.`,
		}, expectRange(old, zero, ffHash, 4000)),
		{
			nBytes: 4000, root: storageRoot, startingHash: zero, limitHash: ffHash,
			expAccounts: 0, expFirst: zero, expLast: zero,
			desc: `This test requests data at a state root that is actually the storage root of
an existing account. The server is supposed to ignore this request.`,
		},

		// Wrong order
		withExp(accRangeTest{nBytes: 4000, root: root, startingHash: ffHash, limitHash: zero,
			desc: `In this test, the startingHash is after limitHash (wrong order). The server
should ignore this invalid request.`,
		}, rangeExpectation{}),
		withExp(accRangeTest{nBytes: 4000, root: root, startingHash: firstKey, limitHash: hashAdd(firstKey, -1),
			desc: `In this test, the startingHash is the first available key, and limitHash is
a key before startingHash (wrong order). The server should return the first available key.`,
		}, expectRange(head, firstKey, hashAdd(firstKey, -1), 4000)),
		withExp(accRangeTest{nBytes: 4000, root: root, startingHash: firstKey, limitHash: zero,
			desc: `In this test, the startingHash is the first available key and limitHash is zero.
(wrong order). The server should return the first available key.`,
		}, expectRange(head, firstKey, zero, 4000)),
	}
	for i, tc := range tests {
		tc := tc
		if i > 0 {
			t.Log("\n")
		}
		t.Logf("-- Test %d", i)
		t.Log(tc.desc)
		t.Log("  request:")
		t.Logf("      root: %x", tc.root)
		t.Logf("      range: %#x - %#x", tc.startingHash, tc.limitHash)
		t.Logf("      responseBytes: %d", tc.nBytes)
		if err := s.snapGetAccountRange(t, &tc); err != nil {
			t.Errorf("test %d failed: %v", i, err)
		}
	}
}

func hashAdd(h common.Hash, n int64) common.Hash {
	hb := h.Big()
	return common.BigToHash(hb.Add(hb, big.NewInt(n)))
}

func findNonEmptyStorageRoot(accounts []state.DumpAccount) common.Hash {
	for i := range accounts {
		if len(accounts[i].Storage) != 0 {
			return common.BytesToHash(accounts[i].Root)
		}
	}
	panic("can't find account with non-empty storage")
}

// The snap suite's expectations are derived from the fixture set's own state
// dumps (PA-28b), by the server's accounting in eth/protocols/snap/handler.go:
// a range reply takes items from the origin in hash order, counts each as 32
// bytes plus its body, and stops after the item that reaches the limit or
// pushes the size past the byte budget; a trie-node reply resolves each
// compact path in the trie and stops once the bytes served pass the budget.
// The tries are rebuilt here from the dumps and must hash to the chain's
// roots, which ties the expectations to the fixture rather than to the node.

type rangeItem struct {
	hash common.Hash
	body []byte
}

type rangeExpectation struct {
	n           int
	first, last common.Hash
}

func expectRange(items []rangeItem, origin, limit common.Hash, nBytes uint64) rangeExpectation {
	var (
		exp  rangeExpectation
		size uint64
	)
	for _, it := range items {
		if bytes.Compare(it.hash[:], origin[:]) < 0 {
			continue
		}
		if exp.n == 0 {
			exp.first = it.hash
		}
		exp.n++
		exp.last = it.hash
		size += uint64(common.HashLength + len(it.body))
		if bytes.Compare(it.hash[:], limit[:]) >= 0 {
			break
		}
		if size > nBytes {
			break
		}
	}
	return exp
}

// expectStorageRange is expectRange with the storage server's accounting
// (ServiceGetStorageRangesQuery): the size is checked at the start of each
// slot against a hard limit of the budget plus ten percent, so the reply
// holds every slot begun before the limit was reached, and the limit hash
// still ends it.
func expectStorageRange(items []rangeItem, origin, limit common.Hash, nBytes uint64) rangeExpectation {
	var (
		exp       rangeExpectation
		size      uint64
		hardLimit = uint64(float64(nBytes) * 1.1)
	)
	for _, it := range items {
		if bytes.Compare(it.hash[:], origin[:]) < 0 {
			continue
		}
		if size >= hardLimit {
			break
		}
		if exp.n == 0 {
			exp.first = it.hash
		}
		exp.n++
		exp.last = it.hash
		size += uint64(common.HashLength + len(it.body))
		if bytes.Compare(it.hash[:], limit[:]) >= 0 {
			break
		}
	}
	return exp
}

// accountItems is the snapshot's view of a dump: slim account RLP per hash.
func accountItems(accounts []state.DumpAccount) []rangeItem {
	items := make([]rangeItem, 0, len(accounts))
	for _, a := range accounts {
		bal, ok := new(big.Int).SetString(a.Balance, 10)
		if !ok {
			panic("bad balance in state dump: " + a.Balance)
		}
		slim := types.SlimAccountRLP(types.StateAccount{
			Nonce:    a.Nonce,
			Balance:  uint256.MustFromBig(bal),
			Root:     common.BytesToHash(a.Root),
			CodeHash: a.CodeHash,
		})
		items = append(items, rangeItem{hash: common.BytesToHash(a.AddressHash), body: slim})
	}
	return items
}

// storageItems is the snapshot's view of an account's storage: RLP of the
// stripped value per hashed slot, in hash order.
func storageItems(storage map[common.Hash]string) []rangeItem {
	items := make([]rangeItem, 0, len(storage))
	for h, v := range storage {
		enc, err := rlp.EncodeToBytes(common.FromHex(v))
		if err != nil {
			panic(err)
		}
		items = append(items, rangeItem{hash: h, body: enc})
	}
	sort.Slice(items, func(i, j int) bool { return bytes.Compare(items[i].hash[:], items[j].hash[:]) < 0 })
	return items
}

func storageData(items []rangeItem, exp rangeExpectation) []*snap.StorageData {
	var out []*snap.StorageData
	for _, it := range items {
		if exp.n > 0 && bytes.Compare(it.hash[:], exp.first[:]) >= 0 && bytes.Compare(it.hash[:], exp.last[:]) <= 0 {
			out = append(out, &snap.StorageData{Hash: it.hash, Body: it.body})
		}
	}
	return out
}

// buildTrie rebuilds a trie from items and commits it to a memory database,
// so that node lookups behave as the server's do on its committed trie
// (children as hash references, a missing node an error): the account trie
// takes the full account RLP, a storage trie the slot bodies.
func buildTrie(items []rangeItem, full bool) *trie.Trie {
	db := triedb.NewDatabase(rawdb.NewMemoryDatabase(), nil)
	tr := trie.NewEmpty(db)
	for _, it := range items {
		body := it.body
		if full {
			var err error
			if body, err = types.FullAccountRLP(it.body); err != nil {
				panic(err)
			}
		}
		tr.MustUpdate(it.hash[:], body)
	}
	root, nodes, err := tr.Commit(false)
	if err != nil {
		panic(err)
	}
	if nodes != nil {
		if err := db.Update(root, types.EmptyRootHash, 0, trienode.NewWithNodeSet(nodes), nil); err != nil {
			panic(err)
		}
	}
	if err := db.Commit(root, false); err != nil {
		panic(err)
	}
	committed, err := trie.New(trie.TrieID(root), db)
	if err != nil {
		panic(err)
	}
	return committed
}

// expectTrieNodes mirrors ServiceGetTrieNodesQuery over the rebuilt tries:
// one-path sets resolve in the account trie, longer sets in the storage trie
// of the account named first; a path that resolves to nothing yields the
// hash of nothing.
func expectTrieNodes(accTrie *trie.Trie, storageTries map[common.Hash]*trie.Trie, paths []snap.TrieNodePathSet, nBytes uint64) []common.Hash {
	var (
		hashes []common.Hash
		served uint64
	)
	for _, pathset := range paths {
		switch len(pathset) {
		case 0:
			return hashes
		case 1:
			blob, _, err := accTrie.GetNode(pathset[0])
			if err != nil {
				break
			}
			hashes = append(hashes, crypto.Keccak256Hash(blob))
			served += uint64(len(blob))
		default:
			stTrie := storageTries[common.BytesToHash(pathset[0])]
			if stTrie == nil {
				break
			}
			for _, path := range pathset[1:] {
				blob, _, err := stTrie.GetNode(path)
				if err != nil {
					break
				}
				hashes = append(hashes, crypto.Keccak256Hash(blob))
				served += uint64(len(blob))
				if served > nBytes {
					break
				}
			}
		}
		if served > nBytes {
			break
		}
	}
	return hashes
}

type stRangesTest struct {
	root     common.Hash
	accounts []common.Hash
	origin   []byte
	limit    []byte
	nBytes   uint64

	expSlots [][]*snap.StorageData

	desc string
}

// TestSnapGetStorageRanges various forms of GetStorageRanges requests.
func (s *Suite) TestSnapGetStorageRanges(t *utesting.T) {
	var (
		acct      = igneumFixtureStorageAcct
		acctHash  = common.BytesToHash(s.chain.state[acct].AddressHash)
		ffHash    = common.MaxHash
		zero      = common.Hash{}
		blockroot = s.chain.Head().Root()
		// The storage slots of the test account, in hash order, as snap response data.
		slots = storageItems(s.chain.state[acct].Storage)
	)
	if len(slots) < 3 {
		t.Fatalf("the storage account %x has %d slots in the dump, want at least 3", acct, len(slots))
	}
	if got, want := buildTrie(slots, false).Hash(), common.BytesToHash(s.chain.state[acct].Root); got != want {
		t.Fatalf("the storage dump of %x rebuilds to root %x, the account's root is %x", acct, got, want)
	}
	expect := func(origin, limit common.Hash, nBytes uint64) [][]*snap.StorageData {
		return [][]*snap.StorageData{storageData(slots, expectStorageRange(slots, origin, limit, nBytes))}
	}
	tests := []stRangesTest{
		{ // [:] -> every slot
			desc: `This request has a range of 00..ff.
The server should return all storage slots of the test account.`,
			root:     blockroot,
			accounts: []common.Hash{acctHash},
			origin:   zero[:],
			limit:    ffHash[:],
			nBytes:   500,
			expSlots: expect(zero, ffHash, 500),
		},
		{ // [slot1:] -> every slot
			desc: `This test requests slots starting at the first available key.
The server should return all storage slots of the test account.`,
			root:     blockroot,
			accounts: []common.Hash{acctHash},
			origin:   slots[0].hash[:],
			limit:    ffHash[:],
			nBytes:   1000,
			expSlots: expect(slots[0].hash, ffHash, 1000),
		},
		{ // [slot1+:] -> every slot but the first
			desc: `This test requests slots starting at a key one past the first available key.
The server should return the remaining slots of the test account.`,
			root:     blockroot,
			accounts: []common.Hash{acctHash},
			origin:   hashAdd(slots[0].hash, 1).Bytes(),
			limit:    ffHash[:],
			nBytes:   500,
			expSlots: expect(hashAdd(slots[0].hash, 1), ffHash, 500),
		},
		{ // [slot1:slot2] -> [slot1, slot2]
			desc:     `This test requests a range which is exactly the first and second available key.`,
			root:     blockroot,
			accounts: []common.Hash{acctHash},
			origin:   slots[0].hash[:],
			limit:    slots[1].hash[:],
			nBytes:   500,
			expSlots: expect(slots[0].hash, slots[1].hash, 500),
		},
		{ // [slot1+:slot2+] -> [slot2, slot3]
			desc: `This test requests a range where limitHash is after the second, but before the third slot
of the test account. The server should return slots [2,3] (i.e. the 'next available' needs to be returned).`,
			root:     blockroot,
			accounts: []common.Hash{acctHash},
			origin:   hashAdd(slots[0].hash, 1).Bytes(),
			limit:    hashAdd(slots[1].hash, 1).Bytes(),
			nBytes:   500,
			expSlots: expect(hashAdd(slots[0].hash, 1), hashAdd(slots[1].hash, 1), 500),
		},
		{ // a byte budget that cuts the range
			desc: `This test requests every slot with a byte budget below the size of two slots.
The server should return the slots begun before its hard limit (the budget plus ten percent) was reached.`,
			root:     blockroot,
			accounts: []common.Hash{acctHash},
			origin:   zero[:],
			limit:    ffHash[:],
			nBytes:   uint64(2*(common.HashLength+len(slots[0].body)) - 10),
			expSlots: expect(zero, ffHash, uint64(2*(common.HashLength+len(slots[0].body))-10)),
		},
	}
	for i, tc := range tests {
		tc := tc
		if i > 0 {
			t.Log("\n")
		}
		t.Logf("-- Test %d", i)
		t.Log(tc.desc)
		t.Log("  request:")
		t.Logf("      root: %x", tc.root)
		t.Logf("      accounts: %x", tc.accounts)
		t.Logf("      range: %#x - %#x", tc.origin, tc.limit)
		t.Logf("      responseBytes: %d", tc.nBytes)
		if err := s.snapGetStorageRanges(t, &tc); err != nil {
			t.Errorf("  failed: %v", err)
		}
	}
}

type byteCodesTest struct {
	nBytes uint64
	hashes []common.Hash

	expHashes int

	desc string
}

// TestSnapGetByteCodes various forms of GetByteCodes requests.
func (s *Suite) TestSnapGetByteCodes(t *utesting.T) {
	var (
		allHashes   = s.chain.CodeHashes()
		headRoot    = s.chain.Head().Root()
		genesisRoot = s.chain.RootAt(0)
	)

	tests := []byteCodesTest{
		// A few stateroots
		{
			desc:      `Here we request state roots as code hashes. The server should deliver an empty response with no items.`,
			nBytes:    10000,
			hashes:    []common.Hash{genesisRoot, headRoot},
			expHashes: 0,
		},
		{
			desc:      `Here we request the genesis state root (which is not an existing code hash) two times. The server should deliver an empty response with no items.`,
			nBytes:    10000,
			hashes:    []common.Hash{genesisRoot, genesisRoot},
			expHashes: 0,
		},
		// Empties
		{
			desc:      `Here we request the empty state root (which is not an existing code hash). The server should deliver an empty response with no items.`,
			nBytes:    10000,
			hashes:    []common.Hash{types.EmptyRootHash},
			expHashes: 0,
		},
		{
			desc:      `Here we request the empty code hash. The server should deliver an empty response item.`,
			nBytes:    10000,
			hashes:    []common.Hash{types.EmptyCodeHash},
			expHashes: 1,
		},
		{
			desc:      `In this test, we request the empty code hash three times. The server should deliver the empty item three times.`,
			nBytes:    10000,
			hashes:    []common.Hash{types.EmptyCodeHash, types.EmptyCodeHash, types.EmptyCodeHash},
			expHashes: 3,
		},
		// The existing bytecodes
		{
			desc:      `Here we request all available contract codes. The server should deliver them all in one response.`,
			nBytes:    100000,
			hashes:    allHashes,
			expHashes: len(allHashes),
		},
		// The existing, with limited byte arg
		{
			desc:      `In this test, the request has a bytes limit of one. The server should deliver one item.`,
			nBytes:    1,
			hashes:    allHashes,
			expHashes: 1,
		},
		{
			desc:      `In this test, the request has a bytes limit of zero. The server should deliver one item.`,
			nBytes:    0,
			hashes:    allHashes,
			expHashes: 1,
		},
		// Request the same hash multiple times.
		{
			desc:      `This test requests the same code hash multiple times. The server should deliver it multiple times.`,
			nBytes:    1000,
			hashes:    []common.Hash{allHashes[0], allHashes[0], allHashes[0], allHashes[0]},
			expHashes: 4,
		},
	}

	for i, tc := range tests {
		tc := tc
		if i > 0 {
			t.Log("\n")
		}
		t.Logf("-- Test %d", i)
		t.Log(tc.desc)
		t.Log("  request:")
		t.Logf("      hashes: %x", tc.hashes)
		t.Logf("      responseBytes: %d", tc.nBytes)
		if err := s.snapGetByteCodes(t, &tc); err != nil {
			t.Errorf("failed: %v", err)
		}
	}
}

type trieNodesTest struct {
	root   common.Hash
	paths  []snap.TrieNodePathSet
	nBytes uint64

	expHashes []common.Hash // expected response
	expReject bool          // if true, request should be rejected

	desc string
}

func decodeNibbles(nibbles []byte, bytes []byte) {
	for bi, ni := 0, 0; ni < len(nibbles); bi, ni = bi+1, ni+2 {
		bytes[bi] = nibbles[ni]<<4 | nibbles[ni+1]
	}
}

// hasTerm returns whether a hex key has the terminator flag.
func hasTerm(s []byte) bool {
	return len(s) > 0 && s[len(s)-1] == 16
}

func keybytesToHex(str []byte) []byte {
	l := len(str)*2 + 1
	var nibbles = make([]byte, l)
	for i, b := range str {
		nibbles[i*2] = b / 16
		nibbles[i*2+1] = b % 16
	}
	nibbles[l-1] = 16
	return nibbles
}

func hexToCompact(hex []byte) []byte {
	terminator := byte(0)
	if hasTerm(hex) {
		terminator = 1
		hex = hex[:len(hex)-1]
	}
	buf := make([]byte, len(hex)/2+1)
	buf[0] = terminator << 5 // the flag byte
	if len(hex)&1 == 1 {
		buf[0] |= 1 << 4 // odd flag
		buf[0] |= hex[0] // first nibble is contained in the first byte
		hex = hex[1:]
	}
	decodeNibbles(hex, buf[1:])
	return buf
}

// TestSnapTrieNodes various forms of GetTrieNodes requests.
func (s *Suite) TestSnapTrieNodes(t *utesting.T) {
	var (
		headRoot  = s.chain.Head().Root()
		headstate = s.chain.AccountsInHashOrder()
		accTrie   = buildTrie(accountItems(headstate), true)
		// The fixture set's storage contract, and its storage trie.
		storageAcct     = igneumFixtureStorageAcct
		storageAcctHash = common.BytesToHash(s.chain.state[storageAcct].AddressHash)
		slots           = storageItems(s.chain.state[storageAcct].Storage)
		storageTries    = map[common.Hash]*trie.Trie{storageAcctHash: buildTrie(slots, false)}
		// An existing account, the first in hash order.
		key      = headstate[0].AddressHash
		empty    = types.EmptyCodeHash
		accPaths []snap.TrieNodePathSet
	)
	if got := accTrie.Hash(); got != headRoot {
		t.Fatalf("the head state dump rebuilds to root %x, the chain's head root is %x", got, headRoot)
	}
	for i := 1; i <= 65; i++ {
		accPaths = append(accPaths, makeSnapPath(key, i))
	}
	// A storage path one nibble long, under the first slot's hash.
	firstNibble := []byte{0x10 | slots[0].hash[0]>>4}
	expect := func(paths []snap.TrieNodePathSet, nBytes uint64) []common.Hash {
		return expectTrieNodes(accTrie, storageTries, paths, nBytes)
	}

	tests := []trieNodesTest{
		{
			desc:      `In this test, we send an empty request to the node.`,
			root:      headRoot,
			paths:     nil,
			nBytes:    500,
			expHashes: nil,
		},
		{
			desc: `In this test, we send a request containing an empty path-set.
The server should reject the request.`,
			root: headRoot,
			paths: []snap.TrieNodePathSet{
				{}, // zero-length pathset should 'abort' and kick us off
				{[]byte{0}},
			},
			nBytes:    5000,
			expHashes: []common.Hash{},
			expReject: true,
		},
		{
			desc: `Here we request the root node of the trie. The server should respond with the root node.`,
			root: s.chain.RootAt(int(s.chain.Head().NumberU64() - 1)),
			paths: []snap.TrieNodePathSet{
				{[]byte{0}},
				{[]byte{1}, []byte{0}},
			},
			nBytes:    5000,
			expHashes: []common.Hash{s.chain.RootAt(int(s.chain.Head().NumberU64() - 1))},
		},
		{ // nonsensically long path
			desc: `In this test, we request a very long trie node path. The server should respond with an empty node (keccak256("")).`,
			root: headRoot,
			paths: []snap.TrieNodePathSet{
				{[]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 0, 1, 2, 3, 4, 5, 6, 7, 8, 0, 1, 2, 3, 4, 5, 6, 7, 8,
					0, 1, 2, 3, 4, 5, 6, 7, 8, 0, 1, 2, 3, 4, 5, 6, 7, 8, 0, 1, 2, 3, 4, 5, 6, 7, 8}},
			},
			nBytes:    5000,
			expHashes: []common.Hash{empty},
		},
		{
			// The leaf is only a couple of levels down, so the continued trie traversal yields empties.
			desc:      `Here we request some known accounts from the state.`,
			root:      headRoot,
			paths:     accPaths,
			nBytes:    5000,
			expHashes: expect(accPaths, 5000),
		},
		{
			desc: `In this test, we request some known accounts in state. The requested paths are NOT in key order.`,
			root: headRoot,
			paths: []snap.TrieNodePathSet{
				accPaths[10], accPaths[1], accPaths[0],
			},
			nBytes:    5000,
			expHashes: expect([]snap.TrieNodePathSet{accPaths[10], accPaths[1], accPaths[0]}, 5000),
		},
		// Storage tests.
		// These use the fixture set's storage account.
		{
			desc: `This test requests the storage root node of a known account.`,
			root: headRoot,
			paths: []snap.TrieNodePathSet{
				{
					storageAcctHash[:],
					[]byte{0},
				},
			},
			nBytes:    5000,
			expHashes: expect([]snap.TrieNodePathSet{{storageAcctHash[:], []byte{0}}}, 5000),
		},
		{
			desc: `This test requests multiple storage nodes of a known account.`,
			root: headRoot,
			paths: []snap.TrieNodePathSet{
				{
					storageAcctHash[:],
					[]byte{0},
					firstNibble,
				},
			},
			nBytes:    5000,
			expHashes: expect([]snap.TrieNodePathSet{{storageAcctHash[:], []byte{0}, firstNibble}}, 5000),
		},
	}
	for i, tc := range tests {
		tc := tc
		if i > 0 {
			t.Log("\n")
		}
		t.Logf("-- Test %d", i)
		t.Log(tc.desc)
		t.Log("  request:")
		t.Logf("    root: %x", tc.root)
		t.Logf("    paths: %x", tc.paths)
		t.Logf("    responseBytes: %d", tc.nBytes)
		if err := s.snapGetTrieNodes(t, &tc); err != nil {
			t.Errorf("  failed: %v", err)
		}
	}
}

func makeSnapPath(key []byte, length int) snap.TrieNodePathSet {
	hex := keybytesToHex(key)[:length]
	hex[len(hex)-1] = 0 // remove term flag
	hKey := hexToCompact(hex)
	return snap.TrieNodePathSet{hKey}
}

func (s *Suite) snapGetAccountRange(t *utesting.T, tc *accRangeTest) error {
	conn, err := s.dialSnap()
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer conn.Close()
	if err = conn.peer(s.chain, nil); err != nil {
		t.Fatalf("peering failed: %v", err)
	}
	// write request
	req := &snap.GetAccountRangePacket{
		ID:     uint64(rand.Int63()),
		Root:   tc.root,
		Origin: tc.startingHash,
		Limit:  tc.limitHash,
		Bytes:  tc.nBytes,
	}
	msg, err := conn.snapRequest(snap.GetAccountRangeMsg, req)
	if err != nil {
		return fmt.Errorf("account range request failed: %v", err)
	}
	res, ok := msg.(*snap.AccountRangePacket)
	if !ok {
		return fmt.Errorf("account range response wrong: %T %v", msg, msg)
	}
	if exp, got := tc.expAccounts, len(res.Accounts); exp != got {
		return fmt.Errorf("expected %d accounts, got %d", exp, got)
	}
	// Check that the encoding order is correct
	for i := 1; i < len(res.Accounts); i++ {
		if bytes.Compare(res.Accounts[i-1].Hash[:], res.Accounts[i].Hash[:]) >= 0 {
			return fmt.Errorf("accounts not monotonically increasing: #%d [%x] vs #%d [%x]", i-1, res.Accounts[i-1].Hash[:], i, res.Accounts[i].Hash[:])
		}
	}
	var (
		hashes   []common.Hash
		accounts [][]byte
		proof    = res.Proof
	)
	hashes, accounts, err = res.Unpack()
	if err != nil {
		return err
	}
	if len(hashes) == 0 && len(accounts) == 0 && len(proof) == 0 {
		return nil
	}
	if len(hashes) > 0 {
		if exp, got := tc.expFirst, res.Accounts[0].Hash; exp != got {
			return fmt.Errorf("expected first account %#x, got %#x", exp, got)
		}
		if exp, got := tc.expLast, res.Accounts[len(res.Accounts)-1].Hash; exp != got {
			return fmt.Errorf("expected last account %#x, got %#x", exp, got)
		}
	}
	// Reconstruct a partial trie from the response and verify it
	keys := make([][]byte, len(hashes))
	for i, key := range hashes {
		keys[i] = common.CopyBytes(key[:])
	}
	nodes := make(trienode.ProofList, len(proof))
	for i, node := range proof {
		nodes[i] = node
	}
	proofdb := nodes.Set()

	_, err = trie.VerifyRangeProof(tc.root, tc.startingHash[:], keys, accounts, proofdb)
	return err
}

func (s *Suite) snapGetStorageRanges(t *utesting.T, tc *stRangesTest) error {
	conn, err := s.dialSnap()
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer conn.Close()
	if err = conn.peer(s.chain, nil); err != nil {
		t.Fatalf("peering failed: %v", err)
	}

	// write request
	req := &snap.GetStorageRangesPacket{
		ID:       uint64(rand.Int63()),
		Root:     tc.root,
		Accounts: tc.accounts,
		Origin:   tc.origin,
		Limit:    tc.limit,
		Bytes:    tc.nBytes,
	}
	msg, err := conn.snapRequest(snap.GetStorageRangesMsg, req)
	if err != nil {
		return fmt.Errorf("account range request failed: %v", err)
	}
	res, ok := msg.(*snap.StorageRangesPacket)
	if !ok {
		return fmt.Errorf("account range response wrong: %T %v", msg, msg)
	}

	// Ensure the ranges are monotonically increasing
	for i, slots := range res.Slots {
		for j := 1; j < len(slots); j++ {
			if bytes.Compare(slots[j-1].Hash[:], slots[j].Hash[:]) >= 0 {
				return fmt.Errorf("storage slots not monotonically increasing for account #%d: #%d [%x] vs #%d [%x]", i, j-1, slots[j-1].Hash[:], j, slots[j].Hash[:])
			}
		}
	}

	// Compute expected slot hashes.
	var expHashes [][]common.Hash
	for _, acct := range tc.expSlots {
		var list []common.Hash
		for _, s := range acct {
			list = append(list, s.Hash)
		}
		expHashes = append(expHashes, list)
	}

	// Check response.
	if !reflect.DeepEqual(res.Slots, tc.expSlots) {
		t.Log("  expected slot hashes:", expHashes)
		return fmt.Errorf("wrong storage slots in response: %#v", res.Slots)
	}
	return nil
}

func (s *Suite) snapGetByteCodes(t *utesting.T, tc *byteCodesTest) error {
	conn, err := s.dialSnap()
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer conn.Close()
	if err = conn.peer(s.chain, nil); err != nil {
		t.Fatalf("peering failed: %v", err)
	}
	// write request
	req := &snap.GetByteCodesPacket{
		ID:     uint64(rand.Int63()),
		Hashes: tc.hashes,
		Bytes:  tc.nBytes,
	}
	msg, err := conn.snapRequest(snap.GetByteCodesMsg, req)
	if err != nil {
		return fmt.Errorf("getBytecodes request failed: %v", err)
	}
	res, ok := msg.(*snap.ByteCodesPacket)
	if !ok {
		return fmt.Errorf("bytecodes response wrong: %T %v", msg, msg)
	}
	if exp, got := tc.expHashes, len(res.Codes); exp != got {
		for i, c := range res.Codes {
			t.Logf("%d. %#x\n", i, c)
		}
		return fmt.Errorf("expected %d bytecodes, got %d", exp, got)
	}
	// Cross reference the requested bytecodes with the response to find gaps
	// that the serving node is missing
	var (
		bytecodes = res.Codes
		hasher    = sha3.NewLegacyKeccak256().(crypto.KeccakState)
		hash      = make([]byte, 32)
		codes     = make([][]byte, len(req.Hashes))
	)

	for i, j := 0, 0; i < len(bytecodes); i++ {
		// Find the next hash that we've been served, leaving misses with nils
		hasher.Reset()
		hasher.Write(bytecodes[i])
		hasher.Read(hash)

		for j < len(req.Hashes) && !bytes.Equal(hash, req.Hashes[j][:]) {
			j++
		}
		if j < len(req.Hashes) {
			codes[j] = bytecodes[i]
			j++
			continue
		}
		// We've either ran out of hashes, or got unrequested data
		return errors.New("unexpected bytecode")
	}

	return nil
}

func (s *Suite) snapGetTrieNodes(t *utesting.T, tc *trieNodesTest) error {
	conn, err := s.dialSnap()
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer conn.Close()
	if err = conn.peer(s.chain, nil); err != nil {
		t.Fatalf("peering failed: %v", err)
	}

	// write0 request
	paths, err := rlp.EncodeToRawList(tc.paths)
	if err != nil {
		panic(err)
	}
	req := &snap.GetTrieNodesPacket{
		ID:    uint64(rand.Int63()),
		Root:  tc.root,
		Paths: paths,
		Bytes: tc.nBytes,
	}
	msg, err := conn.snapRequest(snap.GetTrieNodesMsg, req)
	if err != nil {
		if tc.expReject {
			return nil
		}
		return fmt.Errorf("trienodes  request failed: %v", err)
	}
	res, ok := msg.(*snap.TrieNodesPacket)
	if !ok {
		return fmt.Errorf("trienodes response wrong: %T %v", msg, msg)
	}

	// Check the correctness

	// Cross reference the requested trienodes with the response to find gaps
	// that the serving node is missing
	hasher := sha3.NewLegacyKeccak256().(crypto.KeccakState)
	hash := make([]byte, 32)
	trienodes := res.Nodes
	if got, want := len(trienodes), len(tc.expHashes); got != want {
		return fmt.Errorf("wrong trienode count, got %d, want %d", got, want)
	}
	for i, trienode := range trienodes {
		hasher.Reset()
		hasher.Write(trienode)
		hasher.Read(hash)
		if got, want := hash, tc.expHashes[i]; !bytes.Equal(got, want[:]) {
			t.Logf("  hash %d wrong, got %#x, want %#x\n", i, got, want)
			err = fmt.Errorf("hash %d wrong, got %#x, want %#x", i, got, want)
		}
	}
	return err
}
