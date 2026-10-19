// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).

package igneum

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus/igneum/pool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
)

// Pool is igneum_pool: the shielded pool's figures as the state at the
// asked block holds them (the address, the hash and the verifier in use,
// the gate, the note count, the root, the root sequence, the turnstile
// and the pool address's balance).
func (api *IgneumAPI) Pool(number *rpc.BlockNumber) (*pool.Info, error) {
	if api.chain == nil || api.chain.Config() == nil || api.chain.Config().GetChainID() == nil {
		return nil, errors.New("no chain id")
	}
	st, err := api.stateAt(number)
	if err != nil {
		return nil, err
	}
	chainID := api.chain.Config().GetChainID().Uint64()
	info := pool.Open(st, pool.Address(chainID), chainID, pool.Poseidon2{}).View()
	info.Verifier = api.engine.PoolVerifier().Name()
	info.Gate = api.engine.PoolFrom()
	return &info, nil
}

// PoolLeaf is igneum_poolLeaf: the commitment at an index of the pool's
// tree at the asked block (zero past the count).
func (api *IgneumAPI) PoolLeaf(index uint64, number *rpc.BlockNumber) (common.Hash, error) {
	if api.chain == nil || api.chain.Config() == nil || api.chain.Config().GetChainID() == nil {
		return common.Hash{}, errors.New("no chain id")
	}
	st, err := api.stateAt(number)
	if err != nil {
		return common.Hash{}, err
	}
	return common.Hash(pool.Open(st, pool.Address(api.chain.Config().GetChainID().Uint64()), api.chain.Config().GetChainID().Uint64(), pool.Poseidon2{}).Leaf(index)), nil
}

// PoolOutput is one note as a wallet scans it: its commitment and its
// ciphertext.
type PoolOutput struct {
	Commitment common.Hash   `json:"commitment"`
	Ciphertext hexutil.Bytes `json:"ciphertext"`
}

// PoolBlock is what a block gives a wallet (igneum_poolBlock): the height,
// the position of the block's first output in the tree, the admitted
// outputs in order (a shield's note, a spend's two notes) and the
// nullifiers the block's admitted spends published. This is the retained
// set of the proof-pruning rule (docs/money/STAGE1-JOIN-2026-10-18.md): a
// wallet needs nothing of the proofs, and a node serving scans needs only
// these fields of each record.
type PoolBlock struct {
	Height     uint64        `json:"height"`
	Position   uint64        `json:"position"`
	Outputs    []PoolOutput  `json:"outputs"`
	Nullifiers []common.Hash `json:"nullifiers"`
	// Records and ProofBytes count the admitted records and the proof
	// bytes they carried (the figure the pruning rule drops).
	Records    int `json:"records"`
	ProofBytes int `json:"proofBytes"`
	// Mints are the block's coinbase notes (stage two, cut two: one at
	// most, the sealer's share, after the records), every field public;
	// the sealer's wallet rebuilds the note from them and its own key.
	Mints []PoolMint `json:"mints"`
	// Statements names the admitted vault statements, in order (the count
	// of each statement kind per block is public: the spec's section 6).
	Statements []string `json:"statements,omitempty"`
}

// PoolBlock is igneum_poolBlock: the pool's admitted records of a block.
// From the retained store when the block's retained set is there (every
// block a pruning node imported, and every block a syncing node backfilled
// from a peer and checked); else read from the block's transactions to
// the pool address against the block's state (a shield is admitted when
// its commitment is the leaf at the next position; a spend when its first
// nullifier is in the set at this block and was not at the parent).
func (api *IgneumAPI) PoolBlock(number rpc.BlockNumber) (*PoolBlock, error) {
	rb, err := api.retainedAt(number)
	if err != nil {
		return nil, err
	}
	out := &PoolBlock{Height: rb.Number, Position: rb.Position, Outputs: []PoolOutput{}, Nullifiers: []common.Hash{}, Mints: api.mintsAt(rb.Number, rb.Hash)}
	for _, r := range rb.Records {
		outs, nfs, ok := r.Outputs()
		if !ok {
			return nil, fmt.Errorf("record %s of block %d does not parse", r.TxHash, rb.Number)
		}
		for _, o := range outs {
			out.Outputs = append(out.Outputs, PoolOutput{Commitment: common.Hash(o.Commitment), Ciphertext: o.Ciphertext})
		}
		for _, nf := range nfs {
			out.Nullifiers = append(out.Nullifiers, common.Hash(nf))
		}
		if pool.IsVaultTag(r.Kind) {
			out.Statements = append(out.Statements, pool.VaultName(r.Kind))
		}
		out.Records++
		out.ProofBytes += int(r.ProofSize)
	}
	return out, nil
}

// mintsAt is the block's coinbase note as the chain's state records it
// (pool.MintAt by height, written at the mint and never rewritten; the
// coinbase cut, pool/coinbase.go), read from the state at the height when
// the node holds it, else from the head's, which carries every mint since
// the gate: a node that joined by the pruned sync holds no state before
// its pivot and serves those blocks' mints from its head (F-791). Empty
// when the block minted nothing or the chain has no pool.
func (api *IgneumAPI) mintsAt(h uint64, hash common.Hash) []PoolMint {
	out := []PoolMint{}
	header := api.chain.GetHeader(hash, h)
	if header == nil || !api.engine.poolOn(h) {
		return out
	}
	n := rpc.BlockNumber(h)
	st, err := api.stateAt(&n)
	if err != nil {
		if st, err = api.stateAt(nil); err != nil {
			return out
		}
	}
	chainID := api.chain.Config().GetChainID().Uint64()
	p := pool.Open(st, pool.Address(chainID), chainID, pool.Poseidon2{})
	if index, ok := p.MintAt(h); ok {
		if sealer, value, ok := p.MintRecord(index); ok {
			out = append(out, PoolMint{Position: index, Sealer: sealer, Value: (*hexutil.Big)(value), Randomness: common.Hash(pool.MintRandomness(chainID, h, header.ParentHash, sealer)), Commitment: common.Hash(p.Leaf(index))})
		}
	}
	return out
}

// PoolRetained is igneum_poolRetained: a block's retained set as a pruned
// node keeps and serves it (pool.RetainedBlock: the records without their
// proofs, each with its position, the proof's keccak and length), what a
// syncing node takes from a peer and checks against the pool state its
// headers commit to (pool.Check; igneum_poolBackfill).
func (api *IgneumAPI) PoolRetained(number rpc.BlockNumber) (*pool.RetainedBlock, error) {
	return api.retainedAt(number)
}

func (api *IgneumAPI) retainedAt(number rpc.BlockNumber) (*pool.RetainedBlock, error) {
	if api.chain == nil || api.chain.Config() == nil || api.chain.Config().GetChainID() == nil {
		return nil, errors.New("no chain id")
	}
	var header *types.Header
	if number == rpc.LatestBlockNumber || number == rpc.PendingBlockNumber {
		header = api.chain.CurrentHeader()
	} else {
		header = api.chain.GetHeaderByNumber(uint64(number.Int64()))
	}
	if header == nil {
		return nil, errors.New("unknown block")
	}
	h := header.Number.Uint64()
	if db := api.engine.PoolStore(); db != nil {
		if rb := pool.ReadRetainedBlock(db, h, header.Hash()); rb != nil {
			return rb, nil
		}
	}
	br, ok := api.chain.(interface {
		GetBlock(hash common.Hash, number uint64) *types.Block
	})
	if !ok {
		return nil, errors.New("no block access on this chain reader")
	}
	block := br.GetBlock(header.Hash(), h)
	if block == nil {
		return nil, errors.New("no block body and no retained set for this block")
	}
	chainID := api.chain.Config().GetChainID().Uint64()
	addr := pool.Address(chainID)
	st, err := api.stateAt(&number)
	if err != nil {
		return nil, err
	}
	now := pool.Open(st, addr, chainID, pool.Poseidon2{})
	var parent *pool.State
	if h > 0 {
		pn := rpc.BlockNumber(h - 1)
		pst, err := api.stateAt(&pn)
		if err != nil {
			return nil, fmt.Errorf("the parent's state: %v", err)
		}
		parent = pool.Open(pst, addr, chainID, pool.Poseidon2{})
	}
	return pool.Extract(now, parent, h, header.Hash(), block.Transactions()), nil
}

// PoolBackfill is the report of igneum_poolBackfill.
type PoolBackfill struct {
	From       uint64      `json:"from,omitempty"`
	To         uint64      `json:"to,omitempty"` // F-793: the two bounds carried one json tag ("from"), so the report's "to" was lost
	Blocks     int         `json:"blocks"`       // retained blocks taken, checked and stored
	Records    int         `json:"records"`      // records among them
	Bytes      int         `json:"bytes"`        // retained bytes among them (the records' encodings)
	ProofBytes int         `json:"proofBytes"`   // the proof bytes those records carried before pruning (counted, not taken)
	Skipped    int         `json:"skipped"`      // blocks already in the store
	Refused    string      `json:"refused,omitempty"`
	RefusedAt  uint64      `json:"refusedAt,omitempty"`
	Seconds    float64     `json:"seconds"`
	CheckedAt  common.Hash `json:"checkedAt"` // the state root the sets were checked against
	HeadNumber uint64      `json:"headNumber"`
}

// PoolBackfill is igneum_poolBackfill: a node that joined by the pruned
// sync (the headers, the state at the pivot, the full blocks after it)
// takes the retained sets of the blocks from `from` to `to` from a peer's
// igneum_poolRetained at source, checks each against the pool state of
// its own head (pool.Check: positions contiguous, every commitment the
// leaf at its position, every ciphertext hashing to the state's, every
// nullifier in the set, the end count the one the chain recorded for the
// height) and stores it; the first block that fails stops the backfill
// with the refusal's words and nothing of it stored (the peer is wrong or
// lying: the state root the chain's work commits to is the judge). The
// lab's transport is this RPC; the peer protocol's message is the next
// cut's.
func (api *IgneumAPI) PoolBackfill(ctx context.Context, source string, from, to uint64) (*PoolBackfill, error) {
	if api.chain == nil || api.chain.Config() == nil || api.chain.Config().GetChainID() == nil {
		return nil, errors.New("no chain id")
	}
	db := api.engine.PoolStore()
	if db == nil {
		return nil, errors.New("no retained store on this node")
	}
	gate := api.engine.PoolFrom()
	if gate == 0 {
		return nil, errors.New("the pool is not on this chain")
	}
	if from < gate {
		from = gate
	}
	head := api.chain.CurrentHeader()
	if to > head.Number.Uint64() {
		to = head.Number.Uint64()
	}
	if to < from {
		return nil, fmt.Errorf("nothing to backfill between %d and %d", from, to)
	}
	st, err := api.stateAt(nil)
	if err != nil {
		return nil, fmt.Errorf("the head's state: %v", err)
	}
	chainID := api.chain.Config().GetChainID().Uint64()
	p := pool.Open(st, pool.Address(chainID), chainID, pool.Poseidon2{})
	client, err := rpc.DialContext(ctx, source)
	if err != nil {
		return nil, fmt.Errorf("the source: %v", err)
	}
	defer client.Close()
	started := time.Now()
	rep := &PoolBackfill{From: from, To: to, CheckedAt: head.Root, HeadNumber: head.Number.Uint64()}
	// The count before `from`: the stored set of the block before it, or
	// the state's count-at the last appending block before it.
	expect := uint64(0)
	if from > gate {
		prev := api.chain.GetHeaderByNumber(from - 1)
		if prev == nil {
			return nil, fmt.Errorf("no header at %d", from-1)
		}
		if rb := pool.ReadRetainedBlock(db, from-1, prev.Hash()); rb != nil {
			expect = rb.End
		} else {
			for n := from - 1; n >= gate; n-- {
				if c := p.CountAt(n); c != 0 {
					expect = c
					break
				}
				if n == gate {
					break
				}
			}
		}
	}
	for n := from; n <= to; n++ {
		header := api.chain.GetHeaderByNumber(n)
		if header == nil {
			return nil, fmt.Errorf("no header at %d", n)
		}
		if rb := pool.ReadRetainedBlock(db, n, header.Hash()); rb != nil {
			rep.Skipped++
			expect = rb.End
			continue
		}
		var rb pool.RetainedBlock
		if err := client.CallContext(ctx, &rb, "igneum_poolRetained", hexutil.EncodeUint64(n)); err != nil {
			rep.Refused, rep.RefusedAt = "the source: "+err.Error(), n
			break
		}
		if rb.Number != n || rb.Hash != header.Hash() {
			rep.Refused, rep.RefusedAt = fmt.Sprintf("the source's block %d is %s, the chain's is %s", rb.Number, rb.Hash, header.Hash()), n
			break
		}
		if err := pool.Check(p, &rb, expect); err != nil {
			rep.Refused, rep.RefusedAt = err.Error(), n
			break
		}
		if err := pool.WriteRetainedBlock(db, &rb); err != nil {
			return nil, err
		}
		rep.Blocks++
		rep.Records += len(rb.Records)
		for _, r := range rb.Records {
			rep.Bytes += len(r.Record)
			rep.ProofBytes += int(r.ProofSize)
		}
		expect = rb.End
	}
	rep.Seconds = time.Since(started).Seconds()
	return rep, nil
}

// PoolRevokedPath is igneum_poolRevokedPath: the revoked set's
// non-membership witness for a policy hash's key at a block (the root, the
// low leaf and its siblings), the witness a vault completion needs; Member
// true when the key is revoked.
func (api *IgneumAPI) PoolRevokedPath(key uint64, number *rpc.BlockNumber) (*pool.RevokedProof, error) {
	if api.chain == nil || api.chain.Config() == nil || api.chain.Config().GetChainID() == nil {
		return nil, errors.New("no chain id")
	}
	st, err := api.stateAt(number)
	if err != nil {
		return nil, err
	}
	chainID := api.chain.Config().GetChainID().Uint64()
	proof := pool.Open(st, pool.Address(chainID), chainID, pool.Poseidon2{}).RevokedPath(key)
	return &proof, nil
}

// PoolPath is a note's Merkle path under the pool's root at a block
// (igneum_poolPath): the anchor, the index and the 32 siblings from the
// leaf up, the witness a spend needs. Computed from the state's leaves
// level by level (every leaf read: the lab's form; a node-side tree index
// is the next cut's for a pool of millions).
type PoolPath struct {
	Anchor   common.Hash   `json:"anchor"`
	Height   uint64        `json:"height"`
	Index    uint64        `json:"index"`
	Siblings []common.Hash `json:"siblings"`
}

// PoolPath is igneum_poolPath.
func (api *IgneumAPI) PoolPath(index uint64, number *rpc.BlockNumber) (*PoolPath, error) {
	if api.chain == nil || api.chain.Config() == nil || api.chain.Config().GetChainID() == nil {
		return nil, errors.New("no chain id")
	}
	var header *types.Header
	if number == nil || *number == rpc.LatestBlockNumber || *number == rpc.PendingBlockNumber {
		header = api.chain.CurrentHeader()
	} else {
		header = api.chain.GetHeaderByNumber(uint64(number.Int64()))
	}
	if header == nil {
		return nil, errors.New("unknown block")
	}
	st, err := api.stateAt(number)
	if err != nil {
		return nil, err
	}
	chainID := api.chain.Config().GetChainID().Uint64()
	h := pool.Poseidon2{}
	p := pool.Open(st, pool.Address(chainID), chainID, h)
	count := p.Count()
	if index >= count {
		return nil, fmt.Errorf("index %d is past the count %d", index, count)
	}
	if count > 1<<22 {
		return nil, errors.New("the pool is past the lab form's size for a path read")
	}
	level := make([][32]byte, count)
	for i := uint64(0); i < count; i++ {
		level[i] = p.Leaf(i)
	}
	zeros := pool.Zeros(h)
	out := &PoolPath{Anchor: common.Hash(p.Root()), Height: header.Number.Uint64(), Index: index}
	i := index
	for d := 0; d < pool.Depth; d++ {
		sib := zeros[d]
		if j := i ^ 1; j < uint64(len(level)) {
			sib = level[j]
		}
		out.Siblings = append(out.Siblings, common.Hash(sib))
		next := make([][32]byte, (len(level)+1)/2)
		for k := range next {
			l := level[2*k]
			r := zeros[d]
			if 2*k+1 < len(level) {
				r = level[2*k+1]
			}
			next[k] = h.Node(l, r)
		}
		level = next
		i >>= 1
	}
	if len(level) != 1 || level[0] != p.Root() {
		return nil, errors.New("the path's root does not match the pool's")
	}
	return out, nil
}
