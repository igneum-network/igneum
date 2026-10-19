// Copyright 2021 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package eth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p/tracker"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
)

func handleGetBlockHeaders(backend Backend, msg Decoder, peer *Peer) error {
	// Decode the complex header query
	var query GetBlockHeadersPacket
	if err := msg.Decode(&query); err != nil {
		return fmt.Errorf("%w: message %v: %v", errDecode, msg, err)
	}
	response := ServiceGetBlockHeadersQuery(backend.Chain(), query.GetBlockHeadersRequest, peer)
	return peer.ReplyBlockHeadersRLP(query.RequestId, response)
}

// ServiceGetBlockHeadersQuery assembles the response to a header query. It is
// exposed to allow external packages to test protocol behavior.
func ServiceGetBlockHeadersQuery(chain *core.BlockChain, query *GetBlockHeadersRequest, peer *Peer) []rlp.RawValue {
	if query.Skip == 0 {
		// The fast path: when the request is for a contiguous segment of headers.
		return serviceContiguousBlockHeaderQuery(chain, query)
	} else {
		return serviceNonContiguousBlockHeaderQuery(chain, query, peer)
	}
}

func serviceNonContiguousBlockHeaderQuery(chain *core.BlockChain, query *GetBlockHeadersRequest, peer *Peer) []rlp.RawValue {
	hashMode := query.Origin.Hash != (common.Hash{})
	first := true
	maxNonCanonical := uint64(100)

	// Gather headers until the fetch or network limits is reached
	var (
		bytes   common.StorageSize
		headers []rlp.RawValue
		unknown bool
		lookups int
	)
	for !unknown && len(headers) < int(query.Amount) && bytes < softResponseLimit &&
		len(headers) < maxHeadersServe && lookups < 2*maxHeadersServe {
		lookups++
		// Retrieve the next header satisfying the query
		var origin *types.Header
		if hashMode {
			if first {
				first = false
				origin = chain.GetHeaderByHash(query.Origin.Hash)
				if origin != nil {
					query.Origin.Number = origin.Number.Uint64()
				}
			} else {
				origin = chain.GetHeader(query.Origin.Hash, query.Origin.Number)
			}
		} else {
			origin = chain.GetHeaderByNumber(query.Origin.Number)
		}
		if origin == nil {
			break
		}
		if rlpData, err := rlp.EncodeToBytes(origin); err != nil {
			log.Crit("Unable to encode our own headers", "err", err)
		} else {
			headers = append(headers, rlp.RawValue(rlpData))
			bytes += common.StorageSize(len(rlpData))
		}
		// Advance to the next header of the query
		switch {
		case hashMode && query.Reverse:
			// Hash based traversal towards the genesis block
			ancestor := query.Skip + 1
			if ancestor == 0 {
				unknown = true
			} else {
				query.Origin.Hash, query.Origin.Number = chain.GetAncestor(query.Origin.Hash, query.Origin.Number, ancestor, &maxNonCanonical)
				unknown = (query.Origin.Hash == common.Hash{})
			}
		case hashMode && !query.Reverse:
			// Hash based traversal towards the leaf block
			var (
				current = origin.Number.Uint64()
				next    = current + query.Skip + 1
			)
			if next <= current {
				infos, _ := json.MarshalIndent(peer.Peer.Info(), "", "  ")
				peer.Log().Warn("GetBlockHeaders skip overflow attack", "current", current, "skip", query.Skip, "next", next, "attacker", infos)
				unknown = true
			} else {
				if header := chain.GetHeaderByNumber(next); header != nil {
					nextHash := header.Hash()
					expOldHash, _ := chain.GetAncestor(nextHash, next, query.Skip+1, &maxNonCanonical)
					if expOldHash == query.Origin.Hash {
						query.Origin.Hash, query.Origin.Number = nextHash, next
					} else {
						unknown = true
					}
				} else {
					unknown = true
				}
			}
		case query.Reverse:
			// Number based traversal towards the genesis block
			if query.Origin.Number >= query.Skip+1 {
				query.Origin.Number -= query.Skip + 1
			} else {
				unknown = true
			}

		case !query.Reverse:
			// Number based traversal towards the leaf block
			query.Origin.Number += query.Skip + 1
		}
	}
	return headers
}

func serviceContiguousBlockHeaderQuery(chain *core.BlockChain, query *GetBlockHeadersRequest) []rlp.RawValue {
	count := query.Amount
	if count > maxHeadersServe {
		count = maxHeadersServe
	}
	if query.Origin.Hash == (common.Hash{}) {
		// Number mode, just return the canon chain segment. The backend
		// delivers in [N, N-1, N-2..] descending order, so we need to
		// accommodate for that.
		from := query.Origin.Number
		if !query.Reverse {
			from = from + count - 1
		}
		headers := chain.GetHeadersFrom(from, count)
		if !query.Reverse {
			for i, j := 0, len(headers)-1; i < j; i, j = i+1, j-1 {
				headers[i], headers[j] = headers[j], headers[i]
			}
		}
		return headers
	}
	// Hash mode.
	var (
		headers []rlp.RawValue
		hash    = query.Origin.Hash
		header  = chain.GetHeaderByHash(hash)
	)
	if header != nil {
		rlpData, _ := rlp.EncodeToBytes(header)
		headers = append(headers, rlpData)
	} else {
		// We don't even have the origin header
		return headers
	}
	num := header.Number.Uint64()
	if !query.Reverse {
		// Theoretically, we are tasked to deliver header by hash H, and onwards.
		// However, if H is not canon, we will be unable to deliver any descendants of
		// H.
		if canonHash := chain.GetCanonicalHash(num); canonHash != hash {
			// Not canon, we can't deliver descendants
			return headers
		}
		descendants := chain.GetHeadersFrom(num+count-1, count-1)
		for i, j := 0, len(descendants)-1; i < j; i, j = i+1, j-1 {
			descendants[i], descendants[j] = descendants[j], descendants[i]
		}
		headers = append(headers, descendants...)
		return headers
	}
	{ // Last mode: deliver ancestors of H
		for i := uint64(1); header != nil && i < count; i++ {
			header = chain.GetHeaderByHash(header.ParentHash)
			if header == nil {
				break
			}
			rlpData, _ := rlp.EncodeToBytes(header)
			headers = append(headers, rlpData)
		}
		return headers
	}
}

func handleGetBlockBodies(backend Backend, msg Decoder, peer *Peer) error {
	// Decode the block body retrieval message
	var query GetBlockBodiesPacket
	if err := msg.Decode(&query); err != nil {
		return fmt.Errorf("%w: message %v: %v", errDecode, msg, err)
	}
	response := ServiceGetBlockBodiesQuery(backend.Chain(), query.GetBlockBodiesRequest)
	return peer.ReplyBlockBodiesRLP(query.RequestId, response)
}

// ServiceGetBlockBodiesQuery assembles the response to a body query. It is
// exposed to allow external packages to test protocol behavior.
func ServiceGetBlockBodiesQuery(chain *core.BlockChain, query GetBlockBodiesRequest) []rlp.RawValue {
	// Gather blocks until the fetch or network limits is reached
	var (
		bytes  int
		bodies []rlp.RawValue
	)
	for lookups, hash := range query {
		if bytes >= softResponseLimit || len(bodies) >= maxBodiesServe ||
			lookups >= 2*maxBodiesServe {
			break
		}
		data := chain.GetBodyRLP(hash)
		if len(data) == 0 {
			break // If we don't have this block's body, stop serving.
		}
		bodies = append(bodies, data)
		bytes += len(data)
	}
	return bodies
}

func handleGetReceipts(backend Backend, msg Decoder, peer *Peer) error {
	// Decode the block receipts retrieval message
	var query GetReceiptsPacket
	if err := msg.Decode(&query); err != nil {
		return fmt.Errorf("%w: message %v: %v", errDecode, msg, err)
	}
	response := ServiceGetReceiptsQuery(backend.Chain(), query.GetReceiptsRequest)
	return peer.ReplyReceiptsRLP(query.RequestId, response)
}

// ServiceGetReceiptsQuery assembles the response to a receipt query. It is
// exposed to allow external packages to test protocol behavior.
func ServiceGetReceiptsQuery(chain *core.BlockChain, query GetReceiptsRequest) []rlp.RawValue {
	// Gather state data until the fetch or network limits is reached
	var (
		bytes    int
		receipts []rlp.RawValue
	)
	for lookups, hash := range query {
		if bytes >= softResponseLimit || len(receipts) >= maxReceiptsServe ||
			lookups >= 2*maxReceiptsServe {
			break
		}
		// Retrieve the requested block's receipts
		results := chain.GetReceiptsByHash(hash)
		if results == nil {
			if header := chain.GetHeaderByHash(hash); header == nil || header.ReceiptHash != types.EmptyRootHash {
				break // Don't have this block's receipts, stop serving.
			}
		}
		// If known, encode and queue for response packet
		if encoded, err := rlp.EncodeToBytes(results); err != nil {
			log.Error("Failed to encode receipt", "err", err)
		} else {
			receipts = append(receipts, encoded)
			bytes += len(encoded)
		}
	}
	return receipts
}

func handleNewBlockhashes(backend Backend, msg Decoder, peer *Peer) error {
	// A batch of new block announcements just arrived
	ann := new(NewBlockHashesPacket)
	if err := msg.Decode(ann); err != nil {
		return fmt.Errorf("%w: message %v: %v", errDecode, msg, err)
	}
	// Mark the hashes as present at the remote node
	for _, block := range *ann {
		peer.markBlock(block.Hash)
	}
	// Deliver them all to the backend for queuing
	return backend.Handle(peer, ann)
}

// wholeMinedShares reports whether a propagated block's shares are whole
// entries under one of the carried-list forms: the count form (69 bytes an
// entry), PA-53's step form (70) or the typed form (a kind byte, the step
// entry and a graph share's 168-byte ticket; WHOLEWORK.md section 10 (b)).
// F-191 (16 October 2026): the research chain carries the typed form, and a
// check that knew the first two forms only refused every whole block pushed
// to it and dropped the sender as a protocol error, so no pushed block had
// imported since the typed form and every push cost the sender its peer;
// the fetch path never had the check. The chain's form at the height is the
// import's check (core/block_validator_shares.go), not the wire's.
func wholeMinedShares(entries []byte) bool {
	if len(entries) == 0 {
		return true
	}
	if types.MinedShareCountOf(entries, false) > 0 || types.MinedShareCountOf(entries, true) > 0 {
		return true
	}
	return types.MinedShareCountTyped(entries) > 0
}

// sharesWholeAt reports whether a propagated block's shares are whole under
// the chain's carried-list form at the block's height, read from the backend
// (core.ShareFormAt through the engine: the form the import checks, so the
// wire and the import name one form); a backend naming no form admits a list
// whole under any form (wholeMinedShares).
func sharesWholeAt(backend Backend, header *types.Header, entries []byte) bool {
	if len(entries) == 0 {
		return true
	}
	if fb, ok := backend.(interface {
		ShareFormAt(uint64) (types.ShareListForm, bool)
	}); ok && header.Number != nil {
		if form, known := fb.ShareFormAt(header.Number.Uint64()); known {
			return types.MinedShareCountForm(entries, form) > 0
		}
	}
	return wholeMinedShares(entries)
}

func handleNewBlock(backend Backend, msg Decoder, peer *Peer) error {
	// Retrieve the propagated block, leaving its body encoded for now
	ann := new(rawNewBlockPacket)
	if err := msg.Decode(ann); err != nil {
		return fmt.Errorf("%w: message %v: %v", errDecode, msg, err)
	}
	if err := ann.sanityCheck(); err != nil {
		return err
	}
	// An empty withdrawals list is an absent one (core/types Block.DecodeRLP's
	// note): the encoder writes an absent list as empty ahead of a set proof
	// or endorsements field.
	if ann.Block.Withdrawals != nil && ann.Block.Withdrawals.Len() == 0 {
		ann.Block.Withdrawals = nil
	}
	// Bound what the body may expand into. Decoding into rlp.RawList only counts
	// the items, so this runs before any transaction or uncle is materialized.
	if txs := ann.Block.Transactions.Len(); txs > maxBlockTransactions {
		return fmt.Errorf("too many transactions in propagated block: %d > %d", txs, maxBlockTransactions)
	}
	if uncles := ann.Block.Uncles.Len(); uncles > maxBlockUncles {
		return fmt.Errorf("too many uncles in propagated block: %d > %d", uncles, maxBlockUncles)
	}
	if ann.Block.Withdrawals != nil {
		if n := ann.Block.Withdrawals.Len(); n > maxBlockWithdrawals {
			return fmt.Errorf("too many withdrawals in propagated block: %d > %d (the payout list's bound)", n, maxBlockWithdrawals)
		}
	}
	if n := len(ann.Block.Pi); n > maxBlockPiBytes {
		return fmt.Errorf("propagated block's proof is %d bytes, over the bound %d", n, maxBlockPiBytes)
	}
	if n := len(ann.Block.Endorsements); n > maxBlockEndorsementBytes || n%types.EndorsementBytes != 0 {
		return fmt.Errorf("propagated block's endorsements are %d bytes: over the bound %d or not whole entries", n, maxBlockEndorsementBytes)
	}
	if n := len(ann.Block.MinedShares); n > maxBlockMinedShareBytes {
		return fmt.Errorf("propagated block's shares are %d bytes: over the bound %d", n, maxBlockMinedShareBytes)
	}
	if !sharesWholeAt(backend, ann.Block.Header, ann.Block.MinedShares) {
		// F-191's residual (the security review, 16 October 2026): a list that is not
		// whole under the chain's form at the block's height is as much this
		// node's miss as the sender's (a registration landing the receiver has
		// not imported, a form it does not know), so the block is dropped and
		// the peer kept; the hard drop stays with the byte bound and the
		// body-against-header checks, which no form change can trip.
		log.Debug("Propagated block's shares not whole under the chain's form at its height, dropped", "peer", peer.ID(), "number", ann.Block.Header.Number, "bytes", len(ann.Block.MinedShares))
		return nil
	}
	// Check the body against the header on the encoded form, so a block that is
	// not what it claims to be is dropped without being materialized either.
	header := ann.Block.Header
	roots := hashBodyParts([]BlockBody{ann.Block.body()})
	if hash := roots.UncleHashes[0]; hash != header.UncleHash {
		log.Warn("Propagated block has invalid uncles", "have", hash, "exp", header.UncleHash)
		return nil // TODO(karalabe): return error eventually, but wait a few releases
	}
	if hash := roots.TransactionRoots[0]; hash != header.TxHash {
		log.Warn("Propagated block has invalid body", "have", hash, "exp", header.TxHash)
		return nil // TODO(karalabe): return error eventually, but wait a few releases
	}
	// The withdrawals list is the sortition rule's payout list, Igneum's own
	// rule, so a mismatch is an error: a header with a list root takes exactly
	// the list hashing to it, a header without one takes no list.
	switch {
	case header.WithdrawalsHash == nil && ann.Block.Withdrawals != nil:
		return fmt.Errorf("propagated block carries a withdrawals list and its header has no list root")
	case header.WithdrawalsHash != nil && ann.Block.Withdrawals == nil:
		return fmt.Errorf("propagated block has a list root %x and no withdrawals list", *header.WithdrawalsHash)
	case header.WithdrawalsHash != nil && roots.WithdrawalRoots[0] != *header.WithdrawalsHash:
		return fmt.Errorf("propagated block's withdrawals list does not hash to its root: have %x, exp %x", roots.WithdrawalRoots[0], *header.WithdrawalsHash)
	}
	// The keyed seal's proof: a header committing to one takes exactly the
	// bytes hashing to its commitment; a header without one takes none.
	switch {
	case header.PiHash == nil && len(ann.Block.Pi) > 0:
		return fmt.Errorf("propagated block carries a proof and its header has no proof commitment")
	case header.PiHash != nil && len(ann.Block.Pi) == 0:
		return fmt.Errorf("propagated block has a proof commitment %x and no proof", *header.PiHash)
	case header.PiHash != nil && roots.PiHashes[0] != *header.PiHash:
		return fmt.Errorf("propagated block's proof does not hash to its commitment: have %x, exp %x", roots.PiHashes[0], *header.PiHash)
	}
	// The parent's endorsements: a header committing to a list takes exactly
	// the entries hashing to its commitment, whose count is the header's
	// Endorsed; a header without one takes none.
	switch {
	case header.EndorsementsHash == nil && (len(ann.Block.Endorsements) > 0 || header.Endorsed != 0):
		return fmt.Errorf("propagated block carries endorsements and its header has no commitment")
	case header.EndorsementsHash != nil && len(ann.Block.Endorsements) == 0:
		return fmt.Errorf("propagated block has an endorsements commitment %x and no list", *header.EndorsementsHash)
	case header.EndorsementsHash != nil && roots.EndorsementsHash[0] != *header.EndorsementsHash:
		return fmt.Errorf("propagated block's endorsements do not hash to their commitment: have %x, exp %x", roots.EndorsementsHash[0], *header.EndorsementsHash)
	case header.EndorsementsHash != nil && uint64(types.EndorsementCount(ann.Block.Endorsements)) != header.Endorsed:
		return fmt.Errorf("propagated block carries %d endorsements, its header counts %d", types.EndorsementCount(ann.Block.Endorsements), header.Endorsed)
	}
	// The share lane's carried shares: a header committing to them takes
	// exactly the entries hashing to its commitment; a header without one
	// takes none.
	switch {
	case header.SharesRoot == nil && len(ann.Block.MinedShares) > 0:
		return fmt.Errorf("propagated block carries shares and its header has no shares commitment")
	case header.SharesRoot != nil && len(ann.Block.MinedShares) == 0:
		return fmt.Errorf("propagated block has a shares commitment %x and no shares", *header.SharesRoot)
	case header.SharesRoot != nil && roots.SharesRoots[0] != *header.SharesRoot:
		return fmt.Errorf("propagated block's shares do not hash to their commitment: have %x, exp %x", roots.SharesRoots[0], *header.SharesRoot)
	}
	// Bounded and self-consistent, so it is safe to materialize now.
	txs, err := ann.Block.Transactions.Items()
	if err != nil {
		return fmt.Errorf("NewBlock: %w", err)
	}
	uncles, err := ann.Block.Uncles.Items()
	if err != nil {
		return fmt.Errorf("NewBlock: %w", err)
	}
	block := types.NewBlockWithHeader(header).WithBody(txs, uncles)
	if ann.Block.Withdrawals != nil {
		// Bounded and checked against the header's root above.
		withdrawals, err := ann.Block.Withdrawals.Items()
		if err != nil {
			return fmt.Errorf("NewBlock: %w", err)
		}
		block = block.WithWithdrawals(withdrawals)
	}
	if len(ann.Block.Pi) > 0 {
		block = block.WithPi(ann.Block.Pi)
	}
	if len(ann.Block.Endorsements) > 0 {
		block = block.WithEndorsements(ann.Block.Endorsements)
	}
	if len(ann.Block.MinedShares) > 0 {
		block = block.WithMinedShares(ann.Block.MinedShares)
	}
	block.ReceivedAt = msg.Time()
	block.ReceivedFrom = peer

	// Mark the peer as owning the block
	peer.markBlock(block.Hash())

	return backend.Handle(peer, &NewBlockPacket{Block: block, TD: ann.TD})
}

func handleBlockHeaders(backend Backend, msg Decoder, peer *Peer) error {
	// A batch of headers arrived to one of our previous requests
	res := new(BlockHeadersPacket)
	if err := msg.Decode(res); err != nil {
		return fmt.Errorf("%w: message %v: %v", errDecode, msg, err)
	}
	tresp := tracker.Response{ID: res.RequestId, MsgCode: BlockHeadersMsg, Size: res.List.Len()}
	if err := peer.tracker.Fulfil(tresp); err != nil {
		return fmt.Errorf("BlockHeaders: %w", err)
	}
	headers, err := res.List.Items()
	if err != nil {
		return fmt.Errorf("BlockHeaders: %w", err)
	}

	metadata := func() interface{} {
		hashes := make([]common.Hash, len(headers))
		for i, header := range headers {
			hashes[i] = header.Hash()
		}
		return hashes
	}
	return peer.dispatchResponse(&Response{
		id:   res.RequestId,
		code: BlockHeadersMsg,
		Res:  (*BlockHeadersRequest)(&headers),
	}, metadata)
}

func handleBlockBodies(backend Backend, msg Decoder, peer *Peer) error {
	// A batch of block bodies arrived to one of our previous requests
	res := new(BlockBodiesPacket)
	if err := msg.Decode(res); err != nil {
		return fmt.Errorf("%w: message %v: %v", errDecode, msg, err)
	}

	// Check against the request.
	length := res.List.Len()
	tresp := tracker.Response{ID: res.RequestId, MsgCode: BlockBodiesMsg, Size: length}
	if err := peer.tracker.Fulfil(tresp); err != nil {
		return fmt.Errorf("BlockBodies: %w", err)
	}

	// Collect items and dispatch.
	items, err := res.List.Items()
	if err != nil {
		return fmt.Errorf("BlockBodies: %w", err)
	}
	metadata := func() any { return hashBodyParts(items) }
	return peer.dispatchResponse(&Response{
		id:   res.RequestId,
		code: BlockBodiesMsg,
		Res:  (*BlockBodiesResponse)(&items),
	}, metadata)
}

// BlockBodyHashes contains the lists of block body part roots for a list of block bodies.
type BlockBodyHashes struct {
	TransactionRoots []common.Hash
	WithdrawalRoots  []common.Hash
	UncleHashes      []common.Hash
	PiHashes         []common.Hash // the keyed seal's proof commitment per body (zero without a proof)
	EndorsementsHash []common.Hash // the parent's endorsements' commitment per body (zero without a list)
	SharesRoots      []common.Hash // the share lane's commitment per body (zero without shares)
}

func hashBodyParts(items []BlockBody) BlockBodyHashes {
	h := BlockBodyHashes{
		TransactionRoots: make([]common.Hash, len(items)),
		WithdrawalRoots:  make([]common.Hash, len(items)),
		UncleHashes:      make([]common.Hash, len(items)),
		PiHashes:         make([]common.Hash, len(items)),
		EndorsementsHash: make([]common.Hash, len(items)),
		SharesRoots:      make([]common.Hash, len(items)),
	}
	hasher := trie.NewStackTrie(nil)
	for i, body := range items {
		// the keyed seal's proof: its commitment when a body carries one
		if len(body.Pi) > 0 {
			h.PiHashes[i] = types.PiHash(body.Pi)
		}
		// the parent's endorsements: their commitment when a body carries any
		if len(body.Endorsements) > 0 {
			h.EndorsementsHash[i] = types.EndorsementsHash(body.Endorsements)
		}
		// the share lane's carried shares: their commitment when a body carries any
		if len(body.MinedShares) > 0 {
			h.SharesRoots[i] = types.MinedSharesRoot(body.MinedShares)
		}
		// txs
		txsList := newDerivableRawList(&body.Transactions, writeTxForHash)
		h.TransactionRoots[i] = types.DeriveSha(txsList, hasher)
		// uncles
		if body.Uncles.Len() == 0 {
			h.UncleHashes[i] = types.EmptyUncleHash
		} else {
			h.UncleHashes[i] = crypto.Keccak256Hash(body.Uncles.Bytes())
		}
		// withdrawals
		if body.Withdrawals != nil {
			wdlist := newDerivableRawList(body.Withdrawals, nil)
			h.WithdrawalRoots[i] = types.DeriveSha(wdlist, hasher)
		}
	}
	return h
}

// derivableRawList implements types.DerivableList for a serialized RLP list.
type derivableRawList struct {
	data    []byte
	offsets []uint32
	write   func([]byte, *bytes.Buffer)
}

func newDerivableRawList[T any](list *rlp.RawList[T], write func([]byte, *bytes.Buffer)) *derivableRawList {
	dl := derivableRawList{data: list.Content(), write: write}
	if dl.write == nil {
		// default transform is identity
		dl.write = func(b []byte, buf *bytes.Buffer) { buf.Write(b) }
	}
	// Assert to ensure 32-bit offsets are valid. This can never trigger
	// unless a block body component or p2p receipt list is larger than 4GB.
	if uint(len(dl.data)) > math.MaxUint32 {
		panic("list data too big for derivableRawList")
	}
	it := list.ContentIterator()
	dl.offsets = make([]uint32, list.Len())
	for i := 0; it.Next(); i++ {
		dl.offsets[i] = uint32(it.Offset())
	}
	return &dl
}

// Len returns the number of items in the list.
func (dl *derivableRawList) Len() int {
	return len(dl.offsets)
}

// EncodeIndex writes the i'th item to the buffer.
func (dl *derivableRawList) EncodeIndex(i int, buf *bytes.Buffer) {
	start := dl.offsets[i]
	end := uint32(len(dl.data))
	if i != len(dl.offsets)-1 {
		end = dl.offsets[i+1]
	}
	dl.write(dl.data[start:end], buf)
}

// writeTxForHash changes a transaction in 'network encoding' into the format used for
// the transactions MPT.
func writeTxForHash(tx []byte, buf *bytes.Buffer) {
	k, content, _, _ := rlp.Split(tx)
	if k == rlp.List {
		buf.Write(tx) // legacy tx
	} else {
		buf.Write(content) // typed tx
	}
}

func handleReceipts(backend Backend, msg Decoder, peer *Peer) error {
	// A batch of receipts arrived to one of our previous requests
	res := new(ReceiptsPacket)
	if err := msg.Decode(res); err != nil {
		return fmt.Errorf("%w: message %v: %v", errDecode, msg, err)
	}

	// Check against the request.
	tresp := tracker.Response{ID: res.RequestId, MsgCode: ReceiptsMsg, Size: res.List.Len()}
	if err := peer.tracker.Fulfil(tresp); err != nil {
		return fmt.Errorf("Receipts: %w", err)
	}

	receiptLists, err := res.List.Items()
	if err != nil {
		return fmt.Errorf("Receipts: %w", err)
	}
	metadata := func() interface{} {
		hasher := trie.NewStackTrie(nil)
		hashes := make([]common.Hash, len(receiptLists))
		for i := range receiptLists {
			hashes[i] = types.DeriveSha(newDerivableRawList(&receiptLists[i], writeTxForHash), hasher)
		}
		return hashes
	}
	var enc ReceiptsRLPResponse
	for i := range receiptLists {
		enc = append(enc, receiptLists[i].Bytes())
	}
	return peer.dispatchResponse(&Response{
		id:   res.RequestId,
		code: ReceiptsMsg,
		Res:  &enc,
	}, metadata)
}

func handleNewPooledTransactionHashes(backend Backend, msg Decoder, peer *Peer) error {
	// New transaction announcement arrived, make sure we have
	// a valid and fresh chain to handle them
	if !backend.AcceptTxs() {
		return nil
	}
	ann := new(NewPooledTransactionHashesPacket)
	if err := msg.Decode(ann); err != nil {
		return fmt.Errorf("%w: message %v: %v", errDecode, msg, err)
	}
	if len(ann.Hashes) != len(ann.Types) || len(ann.Hashes) != len(ann.Sizes) {
		return fmt.Errorf("%w: message %v: invalid len of fields: %v %v %v", errDecode, msg, len(ann.Hashes), len(ann.Types), len(ann.Sizes))
	}
	// Schedule all the unknown hashes for retrieval
	for _, hash := range ann.Hashes {
		peer.MarkTransaction(hash)
	}
	return backend.Handle(peer, ann)
}

func handleGetPooledTransactions(backend Backend, msg Decoder, peer *Peer) error {
	// Decode the pooled transactions retrieval message
	var query GetPooledTransactionsPacket
	if err := msg.Decode(&query); err != nil {
		return fmt.Errorf("%w: message %v: %v", errDecode, msg, err)
	}
	hashes, txs := answerGetPooledTransactions(backend, query.GetPooledTransactionsRequest)
	return peer.ReplyPooledTransactionsRLP(query.RequestId, hashes, txs)
}

func answerGetPooledTransactions(backend Backend, query GetPooledTransactionsRequest) ([]common.Hash, []rlp.RawValue) {
	// Gather transactions until the fetch or network limits is reached
	var (
		bytes  int
		hashes []common.Hash
		txs    []rlp.RawValue
	)
	for _, hash := range query {
		if bytes >= softResponseLimit {
			break
		}
		// Retrieve the requested transaction, skipping if unknown to us
		tx := backend.TxPool().Get(hash)
		if tx == nil {
			continue
		}
		// If known, encode and queue for response packet
		if encoded, err := rlp.EncodeToBytes(tx); err != nil {
			log.Error("Failed to encode transaction", "err", err)
		} else {
			hashes = append(hashes, hash)
			txs = append(txs, encoded)
			bytes += len(encoded)
		}
	}
	return hashes, txs
}

func handleTransactions(backend Backend, msg Decoder, peer *Peer) error {
	// Transactions arrived, make sure we have a valid and fresh chain to handle them
	if !backend.AcceptTxs() {
		return nil
	}
	// Transactions can be processed, parse all of them and deliver to the pool
	var txs TransactionsPacket
	if err := msg.Decode(&txs); err != nil {
		return fmt.Errorf("%w: message %v: %v", errDecode, msg, err)
	}
	if txs.Len() > maxTransactionAnnouncements {
		return fmt.Errorf("too many transactions")
	}
	return backend.Handle(peer, &txs)
}

func handlePooledTransactions(backend Backend, msg Decoder, peer *Peer) error {
	// Transactions arrived, make sure we have a valid and fresh chain to handle them
	if !backend.AcceptTxs() {
		return nil
	}

	// Check against request and decode.
	var resp PooledTransactionsPacket
	if err := msg.Decode(&resp); err != nil {
		return fmt.Errorf("%w: message %v: %v", errDecode, msg, err)
	}
	tresp := tracker.Response{
		ID:      resp.RequestId,
		MsgCode: PooledTransactionsMsg,
		Size:    resp.List.Len(),
	}
	if err := peer.tracker.Fulfil(tresp); err != nil {
		return fmt.Errorf("PooledTransactions: %w", err)
	}

	return backend.Handle(peer, &resp)
}
