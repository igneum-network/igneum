// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).

package eth

import (
	"encoding/binary"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pool"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/log"
)

// The shielded pool's proof pruner (--pool.prune; Project Money, the
// pruned-sync cut: docs/money/PRUNED-SYNC-DESIGN-2026-10-18.md). Two
// steps on every block the node imports and executes:
//
//  1. at import, the block's retained set (pool.Extract against the
//     block's state and its parent's: the admitted records without their
//     proofs, each proof's keccak and length) is written to the store
//     under the block's number and hash; this is what igneum_poolBlock
//     serves wallets and igneum_poolRetained serves syncing nodes;
//  2. PoolPruneKeep blocks later, the block's stored body is rewritten
//     with each pool transaction's data cut to its retained bytes plus
//     the proof's keccak (pool.Prune): the proof bytes leave the disk.
//     The body no longer matches the header's transaction root, so this
//     node serves no full sync of that block (a full-syncing peer drops
//     it as a bad body, by design: pruned history is for pruned peers;
//     the archive serves full syncs); the headers, receipts and public
//     transactions stay.
//
// Keep is at least 256: a snap-syncing peer takes the bodies of the last
// 64 blocks whole (the downloader's pivot distance) and its pivot may
// advance by 120 before those are fetched; 256 leaves a margin, and is
// far inside the freezer's threshold (90,000), so a body reaches the
// ancient store already pruned.
type poolPruner struct {
	chain   *core.BlockChain
	db      ethdb.Database
	chainID uint64
	addr    common.Address
	gate    uint64
	keep    uint64
	cursor  uint64 // the last block whose body was pruned
	quit    chan struct{}
	done    chan struct{}
}

var poolPruneCursorKey = []byte("money-retained-prune-cursor")

func newPoolPruner(chain *core.BlockChain, db ethdb.Database, chainID, gate, keep uint64) *poolPruner {
	pp := &poolPruner{chain: chain, db: db, chainID: chainID, addr: pool.Address(chainID), gate: gate, keep: keep, quit: make(chan struct{}), done: make(chan struct{})}
	if enc, _ := db.Get(poolPruneCursorKey); len(enc) == 8 {
		pp.cursor = binary.BigEndian.Uint64(enc)
	}
	return pp
}

func (pp *poolPruner) start() {
	go pp.loop()
}

func (pp *poolPruner) stop() {
	close(pp.quit)
	<-pp.done
}

func (pp *poolPruner) loop() {
	defer close(pp.done)
	ch := make(chan core.ChainEvent, 64)
	sub := pp.chain.SubscribeChainEvent(ch)
	defer sub.Unsubscribe()
	// The blocks already on disk at start (a restart, or a node that
	// imported before the flag): their retained sets are extracted while
	// their states are still held; the bodies behind the cursor pruned.
	if head := pp.chain.CurrentBlock(); head != nil && head.Number.Uint64() >= pp.gate {
		pp.catchUp(head.Number.Uint64())
	}
	for {
		select {
		case ev := <-ch:
			pp.onBlock(ev.Block)
		case <-sub.Err():
			return
		case <-pp.quit:
			return
		}
	}
}

// catchUp extracts the retained sets of the recent blocks whose states
// are still held (the trie database keeps about 128 layers) and prunes
// the bodies behind the cursor up to head - keep.
func (pp *poolPruner) catchUp(head uint64) {
	from := pp.gate
	if head > 128 && from < head-128 {
		from = head - 128
	}
	for n := from; n <= head; n++ {
		if block := pp.chain.GetBlockByNumber(n); block != nil {
			pp.extract(block)
		}
	}
	if head > pp.keep {
		pp.pruneUpTo(head - pp.keep)
	}
}

func (pp *poolPruner) onBlock(block *types.Block) {
	n := block.NumberU64()
	if n < pp.gate {
		return
	}
	pp.extract(block)
	if n > pp.keep {
		pp.pruneUpTo(n - pp.keep)
	}
}

// extract writes the block's retained set (nothing when it is stored, or
// when a state is not held).
func (pp *poolPruner) extract(block *types.Block) {
	n, hash := block.NumberU64(), block.Hash()
	if n < pp.gate || pool.HasRetainedBlock(pp.db, n, hash) {
		return
	}
	st, err := pp.chain.StateAt(block.Root())
	if err != nil {
		log.Warn("Shielded pool retained set not extracted (state not held)", "number", n, "err", err)
		return
	}
	now := pool.Open(st, pp.addr, pp.chainID, pool.Poseidon2{})
	var parent *pool.State
	if n > 0 {
		ph := pp.chain.GetHeader(block.ParentHash(), n-1)
		if ph == nil {
			return
		}
		pst, err := pp.chain.StateAt(ph.Root)
		if err != nil {
			log.Warn("Shielded pool retained set not extracted (parent state not held)", "number", n, "err", err)
			return
		}
		parent = pool.Open(pst, pp.addr, pp.chainID, pool.Poseidon2{})
	}
	rb := pool.Extract(now, parent, n, hash, block.Transactions())
	if err := pool.WriteRetainedBlock(pp.db, rb); err != nil {
		log.Error("Shielded pool retained set not written", "number", n, "err", err)
		return
	}
	if len(rb.Records) > 0 {
		log.Debug("Shielded pool retained set stored", "number", n, "records", len(rb.Records), "position", rb.Position, "end", rb.End)
	}
}

// pruneUpTo rewrites the canonical bodies from the cursor to n: each pool
// transaction's data cut to its retained bytes and the proof's keccak.
func (pp *poolPruner) pruneUpTo(n uint64) {
	if n <= pp.cursor {
		return
	}
	started := time.Now()
	var blocks, records, dropped int
	for i := pp.cursor + 1; i <= n; i++ {
		select {
		case <-pp.quit:
			return
		default:
		}
		if i < pp.gate {
			continue
		}
		hash := rawdb.ReadCanonicalHash(pp.db, i)
		if hash == (common.Hash{}) {
			continue
		}
		body := rawdb.ReadBody(pp.db, hash, i)
		if body == nil {
			continue
		}
		changed := false
		for j, tx := range body.Transactions {
			if tx.To() == nil || *tx.To() != pp.addr {
				continue
			}
			pruned, ok := pool.Prune(tx.Data())
			if !ok || len(pruned) == len(tx.Data()) {
				continue // not a record, or already pruned (its tail is the 32-byte hash)
			}
			if nt := withData(tx, pruned); nt != nil {
				body.Transactions[j] = nt
				changed = true
				records++
				dropped += len(tx.Data()) - len(pruned)
			}
		}
		if changed {
			rawdb.WriteBody(pp.db, hash, i, body)
			blocks++
		}
	}
	pp.cursor = n
	var enc [8]byte
	binary.BigEndian.PutUint64(enc[:], n)
	pp.db.Put(poolPruneCursorKey, enc[:])
	if blocks > 0 {
		log.Info("Shielded pool proofs pruned", "upto", n, "blocks", blocks, "records", records, "bytes", dropped, "took", time.Since(started))
	}
}

// withData is the transaction with its data replaced and every other
// field, the signature included, kept (the hash changes: the stored form
// of a pruned record is not the transaction that was mined).
func withData(tx *types.Transaction, data []byte) *types.Transaction {
	v, r, s := tx.RawSignatureValues()
	switch tx.Type() {
	case types.LegacyTxType:
		return types.NewTx(&types.LegacyTx{Nonce: tx.Nonce(), GasPrice: tx.GasPrice(), Gas: tx.Gas(), To: tx.To(), Value: tx.Value(), Data: data, V: v, R: r, S: s})
	case types.AccessListTxType:
		return types.NewTx(&types.AccessListTx{ChainID: tx.ChainId(), Nonce: tx.Nonce(), GasPrice: tx.GasPrice(), Gas: tx.Gas(), To: tx.To(), Value: tx.Value(), Data: data, AccessList: tx.AccessList(), V: v, R: r, S: s})
	case types.DynamicFeeTxType:
		return types.NewTx(&types.DynamicFeeTx{ChainID: tx.ChainId(), Nonce: tx.Nonce(), GasTipCap: tx.GasTipCap(), GasFeeCap: tx.GasFeeCap(), Gas: tx.Gas(), To: tx.To(), Value: tx.Value(), Data: data, AccessList: tx.AccessList(), V: v, R: r, S: s})
	}
	return nil
}
