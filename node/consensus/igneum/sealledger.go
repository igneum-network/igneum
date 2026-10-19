package igneum

import (
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
)

// The node-local half of "one block per key per height" (the project's ruling of
// 11:5x UK, 11 October 2026; the deterministic half is the registry's mark
// from the equivocation transaction, the node team's penalties.go): a RELAY
// AND FORK-CHOICE rule, never a validity rule. The first sealed block a node
// sees under a key at a height is the one it imports and relays; a second,
// different seal under the same key at the same height is kept as EVIDENCE
// (the pair, RLP([headerA, headerB]) = the registry's 0x10 payload), not
// imported into this node's canonical chain and not relayed. The sync path
// (the downloader) is untouched: a node that syncs later takes whatever the
// chain it syncs from holds canonical, so two nodes may disagree on the
// first seen, which is why this is not consensus. The evidence is served by
// igneum_sealEquivocations as the ready payload; the first node holding
// both posts it (the lab form: the operator's transaction through
// devnet5-tx.sh's equivocate form; an automatic report from the node is
// named, not built).

type sealEntry struct {
	sealHash common.Hash
	header   *types.Header
}

// SealEquivocation is one pair of seals by one key at one height.
type SealEquivocation struct {
	Height  uint64      `json:"height"`
	Key     common.Hash `json:"key"`
	First   common.Hash `json:"first"`   // the first seen block's hash
	Second  common.Hash `json:"second"`  // the second's
	Payload []byte      `json:"payload"` // RLP([headerA, headerB]): the registry's 0x10 payload
}

type sealLedger struct {
	mu       sync.Mutex
	seen     map[uint64]map[common.Hash]sealEntry // height -> key -> first seen
	evidence []SealEquivocation
	top      uint64
}

const sealLedgerKeep = 256

// RecordSeal notes a sealed header by (height, BondKey) and reports whether
// it is a SECOND, different seal under a key already seen at that height
// (true: the caller keeps it out of its chain and its relay; the pair is on
// the evidence list). A header without a key, or the same block (by hash)
// seen again, is never a second.
func (e *Engine) RecordSeal(header *types.Header) bool {
	if header == nil || header.Number == nil {
		return false
	}
	key, ok := e.ledgerKey(header)
	if !ok {
		return false
	}
	h := header.Number.Uint64()
	// The block hash, not the seal preimage: two blocks by one key at a
	// height that differ only in the nonce (one preimage sealed twice) are
	// two blocks to the fork rule and tie on the draw value, so the second
	// is a second.
	seal := header.Hash()
	l := &e.seals
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seen == nil {
		l.seen = make(map[uint64]map[common.Hash]sealEntry)
	}
	if l.seen[h] == nil {
		l.seen[h] = make(map[common.Hash]sealEntry)
	}
	first, ok := l.seen[h][key]
	if !ok {
		l.seen[h][key] = sealEntry{sealHash: seal, header: types.CopyHeader(header)}
		if h > l.top {
			l.top = h
			for k := range l.seen {
				if k+sealLedgerKeep < h {
					delete(l.seen, k)
				}
			}
		}
		return false
	}
	if first.sealHash == seal {
		return false
	}
	payload, _ := rlp.EncodeToBytes([]*types.Header{first.header, types.CopyHeader(header)})
	l.evidence = append(l.evidence, SealEquivocation{Height: h, Key: key, First: first.header.Hash(), Second: header.Hash(), Payload: payload})
	if len(l.evidence) > 1024 {
		l.evidence = l.evidence[len(l.evidence)-1024:]
	}
	e.log.Warn("Second seal under one key at one height (kept as evidence, not imported, not relayed)", "number", h, "key", key.Hex()[:12], "first", first.header.Hash(), "second", header.Hash())
	return true
}

// ledgerKey is the key a block is counted under: the sealer's bond key,
// and under the vector form an open-seat block's coinbase key (the key
// its draw value x(h) is computed with, forkchoice.go drawValue), so the
// fork rule's tie-break is total: the formal seat's revision-10 campaign
// (main 04e66e34) showed the registered draw value separates siblings only
// while one key seals at most one block at a height, and the vector chains
// seal open-seat. Outside the vector form a block without a bond key is
// not counted (the ledger's original scope, 11 October 2026).
func (e *Engine) ledgerKey(header *types.Header) (common.Hash, bool) {
	if header.BondKey != nil {
		return *header.BondKey, true
	}
	if e.vectorMenu() != nil {
		return crypto.Keccak256Hash(header.Coinbase[:]), true
	}
	return common.Hash{}, false
}

// SealEquivocations is the evidence this node holds (igneum_sealEquivocations).
func (e *Engine) SealEquivocations() []SealEquivocation {
	e.seals.mu.Lock()
	defer e.seals.mu.Unlock()
	return append([]SealEquivocation{}, e.seals.evidence...)
}
