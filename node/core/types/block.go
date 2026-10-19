// Copyright 2014 The go-ethereum Authors
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

// Package types contains data types related to Ethereum consensus.
package types

import (
	"encoding/binary"
	"fmt"
	"io"
	"math/big"
	"reflect"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/rlp"
)

// A BlockNonce is a 64-bit hash which proves (combined with the
// mix-hash) that a sufficient amount of computation has been carried
// out on a block.
type BlockNonce [8]byte

// EncodeNonce converts the given integer to a block nonce.
func EncodeNonce(i uint64) BlockNonce {
	var n BlockNonce
	binary.BigEndian.PutUint64(n[:], i)
	return n
}

// Uint64 returns the integer value of a block nonce.
func (n BlockNonce) Uint64() uint64 {
	return binary.BigEndian.Uint64(n[:])
}

// MarshalText encodes n as a hex string with 0x prefix.
func (n BlockNonce) MarshalText() ([]byte, error) {
	return hexutil.Bytes(n[:]).MarshalText()
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (n *BlockNonce) UnmarshalText(input []byte) error {
	return hexutil.UnmarshalFixedText("BlockNonce", input, n[:])
}

//go:generate go run github.com/fjl/gencodec -type Header -field-override headerMarshaling -out gen_header_json.go
//go:generate go run ../../rlp/rlpgen -type Header -out gen_header_rlp.go

// Header represents a block header in the Ethereum blockchain.
type Header struct {
	ParentHash  common.Hash    `json:"parentHash"       gencodec:"required"`
	UncleHash   common.Hash    `json:"sha3Uncles"       gencodec:"required"`
	Coinbase    common.Address `json:"miner"`
	Root        common.Hash    `json:"stateRoot"        gencodec:"required"`
	TxHash      common.Hash    `json:"transactionsRoot" gencodec:"required"`
	ReceiptHash common.Hash    `json:"receiptsRoot"     gencodec:"required"`
	Bloom       Bloom          `json:"logsBloom"        gencodec:"required"`
	Difficulty  *big.Int       `json:"difficulty"       gencodec:"required"`
	Number      *big.Int       `json:"number"           gencodec:"required"`
	GasLimit    uint64         `json:"gasLimit"         gencodec:"required"`
	GasUsed     uint64         `json:"gasUsed"          gencodec:"required"`
	Time        uint64         `json:"timestamp"        gencodec:"required"`
	Extra       []byte         `json:"extraData"        gencodec:"required"`
	MixDigest   common.Hash    `json:"mixHash"`
	Nonce       BlockNonce     `json:"nonce"`

	// BaseFee was added by EIP-1559 and is ignored in legacy headers.
	BaseFee *big.Int `json:"baseFeePerGas" rlp:"optional"`

	// WithdrawalsHash was added by EIP-4895 and is ignored in legacy headers.
	// Igneum never sets it; "nil" lets a header with a later optional field
	// (BondKey) carry it as an empty string and decode it as nil.
	WithdrawalsHash *common.Hash `json:"withdrawalsRoot" rlp:"optional,nil"`

	// BondKey is the miner-bound work authority prototype's key field
	// (consensus/igneum/authority.go; a research prototype, not adopted):
	// the bond key the block is sealed under, inside the seal hash and so
	// inside the proof-of-work pre-image. Nil on every header before the
	// rule's height and on every chain without the rule.
	BondKey *common.Hash `json:"bondKey" rlp:"optional,nil"`

	// Round and Sig are the sortition-gated rule's fields (the same
	// prototype, consensus/igneum/sortition.go): the round the block is
	// sealed at and the deterministic Ed25519 signature under the bond's
	// signer over the signing hash (every header field but Sig, MixDigest
	// and Nonce). Zero and empty before the rule's height.
	Round uint64 `json:"round" rlp:"optional"`
	Sig   []byte `json:"sig" rlp:"optional"`

	// Shares are the bond-keyed rounds' shares (the specification's section
	// 0.9, version 8 bit 10; the hostile-key rule, PROTOTYPE on the lab
	// build): S shares of 32 bytes, written by the attempt under the bond's
	// secret, OUTSIDE the seal hash and the signing hash (like the nonce and
	// the mix digest: fixed by the proof of work, not by the template) and
	// inside the block hash. Empty on every unkeyed document.
	Shares []byte `json:"shares" rlp:"optional"`

	// PiHash is the keyed seal's proof commitment (SPEC-R0A-v0.md 0.9.1):
	// BLAKE3(tag("IgneumHash/R0A/pi") || pi) over the receipt bytes carried
	// in the block body (Body.Pi), a tail field outside the seal hash and
	// fixed by the proof of work through the shares; nil without a proof.
	PiHash *common.Hash `json:"piHash" rlp:"optional,nil"`

	// EndorsementsHash and Endorsed are the settlement rule's fields
	// (DEVNET6-RULES.md section 2): the commitment to the body's list of
	// endorsements of the PARENT (BLAKE3 over the entries) and the count of
	// distinct endorsing units the list carries, both INSIDE the seal hash
	// (the sealer chooses them before its search); absent without the rule.
	EndorsementsHash *common.Hash `json:"endorsementsHash" rlp:"optional,nil"`
	Endorsed         uint64       `json:"endorsed" rlp:"optional"`

	// SharesRoot is the share lane's commitment (LAUNCH-GUARDS.md 2.9;
	// consensus/igneum/mineshares.go): BLAKE3 over the body's carried
	// shares (Body.MinedShares), INSIDE the seal hash; nil without shares.
	SharesRoot *common.Hash `json:"sharesRoot" rlp:"optional,nil"`

	// Coord is VectorWork's coordinate tag (docs/consensus/VECTORWORK.md
	// section 5): which of the chain's registered work coordinates the
	// block's seal was found under, 0 to 3, INSIDE the seal hash (a seal
	// for one coordinate is no seal for another); the block's difficulty
	// is that coordinate's controller output at the parent and its seal
	// is verified under that coordinate's relation. Nil on every chain
	// without the vector form. One byte on the wire (a byte string, since
	// RLP's integer zero is the empty string, which a nil pointer form
	// would read as absent: coordinate 0 must survive the round trip).
	Coord []byte `json:"coord" rlp:"optional"`

	// Ticket is the Graph coordinate's witness (docs/igneumhash/vectorwork/
	// GRAPH-REFERENCE.md): the 42 edge indices of one 42-cycle of the
	// attempt's Cuckaroo29 graph, ascending, LE32, 168 bytes, OUTSIDE the
	// seal hash (the seal hash is the graph's seed; the ticket is found
	// under it and verified against it, as the nonce and the mix are).
	// MixDigest carries the ticket hash. Nil on every block of a hash
	// coordinate.
	Ticket []byte `json:"ticket" rlp:"optional"`

	// Parents are the block's MERGED parents under the block DAG ordering
	// (docs/money/ORDERING-DESIGN-2026-10-18.md; consensus/igneum/dagform.go,
	// under params.IgneumChainDAGFrom, zero everywhere of record): the
	// hashes of every tip the block references beside its selected parent
	// (ParentHash), 32 bytes each, concatenated, INSIDE the seal hash. Empty
	// on every block of every chain without the ordering, where a header
	// carrying it is refused; an empty field encodes as absent, so the
	// headers of the chains of record are byte for byte what they were.
	Parents []byte `json:"parents" rlp:"optional"`
}

// MergedParents is the header's merged parents (the DAG form), nil without.
func (h *Header) MergedParents() ([]common.Hash, error) {
	if len(h.Parents) == 0 {
		return nil, nil
	}
	if len(h.Parents)%common.HashLength != 0 {
		return nil, fmt.Errorf("merged parents: %d bytes is not a whole number of hashes", len(h.Parents))
	}
	out := make([]common.Hash, len(h.Parents)/common.HashLength)
	for i := range out {
		copy(out[i][:], h.Parents[i*common.HashLength:(i+1)*common.HashLength])
	}
	return out, nil
}

// SetMergedParents writes the merged parents (nil clears the field).
func (h *Header) SetMergedParents(parents []common.Hash) {
	if len(parents) == 0 {
		h.Parents = nil
		return
	}
	h.Parents = make([]byte, 0, len(parents)*common.HashLength)
	for _, p := range parents {
		h.Parents = append(h.Parents, p[:]...)
	}
}

// TicketBytes is the Graph coordinate's ticket size: 42 edges x LE32.
const TicketBytes = 168

// CoordTag is the header's VectorWork coordinate: ok false without one.
func (h *Header) CoordTag() (uint8, bool) {
	if len(h.Coord) != 1 {
		return 0, false
	}
	return h.Coord[0], true
}

// SetCoordTag writes the coordinate tag.
func (h *Header) SetCoordTag(c uint8) { h.Coord = []byte{c} }

// field type overrides for gencodec
type headerMarshaling struct {
	Difficulty *hexutil.Big
	Number     *hexutil.Big
	GasLimit   hexutil.Uint64
	GasUsed    hexutil.Uint64
	Time       hexutil.Uint64
	Extra      hexutil.Bytes
	BaseFee    *hexutil.Big
	Round      hexutil.Uint64
	Sig        hexutil.Bytes
	Shares     hexutil.Bytes
	Endorsed   hexutil.Uint64
	Hash       common.Hash `json:"hash"` // adds call to Hash() in MarshalJSON
}

// Hash returns the block hash of the header, which is simply the keccak256 hash of its
// RLP encoding.
func (h *Header) Hash() common.Hash {
	return rlpHash(h)
}

var headerSize = common.StorageSize(reflect.TypeOf(Header{}).Size())

// Size returns the approximate memory used by all internal contents. It is used
// to approximate and limit the memory consumption of various caches.
func (h *Header) Size() common.StorageSize {
	var baseFeeBits int
	if h.BaseFee != nil {
		baseFeeBits = h.BaseFee.BitLen()
	}
	return headerSize + common.StorageSize(len(h.Extra)+(h.Difficulty.BitLen()+h.Number.BitLen()+baseFeeBits)/8)
}

// SanityCheck checks a few basic things -- these checks are way beyond what
// any 'sane' production values should hold, and can mainly be used to prevent
// that the unbounded fields are stuffed with junk data to add processing
// overhead
func (h *Header) SanityCheck() error {
	if h.Number != nil && !h.Number.IsUint64() {
		return fmt.Errorf("too large block number: bitlen %d", h.Number.BitLen())
	}
	if h.Difficulty != nil {
		if diffLen := h.Difficulty.BitLen(); diffLen > 80 {
			return fmt.Errorf("too large block difficulty: bitlen %d", diffLen)
		}
	}
	if eLen := len(h.Extra); eLen > 100*1024 {
		return fmt.Errorf("too large block extradata: size %d", eLen)
	}
	if h.BaseFee != nil {
		if bfLen := h.BaseFee.BitLen(); bfLen > 256 {
			return fmt.Errorf("too large base fee: bitlen %d", bfLen)
		}
	}
	return nil
}

// EmptyBody returns true if there is no additional 'body' to complete the header
// that is: no transactions, no uncles and no withdrawals.
func (h *Header) EmptyBody() bool {
	if h.PiHash != nil || h.EndorsementsHash != nil || h.SharesRoot != nil {
		// A keyed seal's proof, the parent's endorsements and the carried
		// shares live in the body: never empty.
		return false
	}
	if h.WithdrawalsHash != nil {
		return h.TxHash == EmptyTxsHash && *h.WithdrawalsHash == EmptyWithdrawalsHash
	}
	return h.TxHash == EmptyTxsHash && h.UncleHash == EmptyUncleHash
}

// EmptyReceipts returns true if there are no receipts for this header/block.
func (h *Header) EmptyReceipts() bool {
	return h.ReceiptHash == EmptyReceiptsHash
}

// Body is a simple (mutable, non-safe) data container for storing and moving
// a block's data contents (transactions and uncles) together.
type Body struct {
	Transactions []*Transaction
	Uncles       []*Header
	Withdrawals  []*Withdrawal `rlp:"optional"`
	// Pi is the keyed seal's proof (the wire form u8(2) || receipt,
	// SPEC-R0A-v0.md 0.9.1), committed by the header's PiHash; empty
	// without a proof.
	Pi []byte `rlp:"optional"`
	// Endorsements is the settlement rule's list of the PARENT's
	// endorsements, 100-byte entries key || LE32(unit) || Ed25519 signature
	// concatenated, committed by the header's EndorsementsHash; empty
	// without the rule.
	Endorsements []byte `rlp:"optional"`
	// MinedShares is the share lane's carried shares (69-byte entries,
	// consensus/igneum/mineshares.go), committed by the header's SharesRoot;
	// empty without shares.
	MinedShares []byte `rlp:"optional"`
}

// Block represents an Ethereum block.
//
// Note the Block type tries to be 'immutable', and contains certain caches that rely
// on that. The rules around block immutability are as follows:
//
//   - We copy all data when the block is constructed. This makes references held inside
//     the block independent of whatever value was passed in.
//
//   - We copy all header data on access. This is because any change to the header would mess
//     up the cached hash and size values in the block. Calling code is expected to take
//     advantage of this to avoid over-allocating!
//
//   - When new body data is attached to the block, a shallow copy of the block is returned.
//     This ensures block modifications are race-free.
//
//   - We do not copy body data on access because it does not affect the caches, and also
//     because it would be too expensive.
type Block struct {
	header       *Header
	uncles       []*Header
	transactions Transactions
	withdrawals  Withdrawals
	pi           []byte // the keyed seal's proof, committed by header.PiHash
	endorsements []byte // the parent's endorsements, committed by header.EndorsementsHash
	minedShares  []byte // the share lane's carried shares, committed by header.SharesRoot

	// caches
	hash atomic.Value
	size atomic.Value

	// These fields are used by package eth to track
	// inter-peer block relay.
	ReceivedAt   time.Time
	ReceivedFrom interface{}
}

// "external" block encoding. used for eth protocol, etc.
type extblock struct {
	Header       *Header
	Txs          []*Transaction
	Uncles       []*Header
	Withdrawals  []*Withdrawal `rlp:"optional"`
	Pi           []byte        `rlp:"optional"`
	Endorsements []byte        `rlp:"optional"`
	MinedShares  []byte        `rlp:"optional"`
}

// NewBlock creates a new block. The input data is copied, changes to header and to the
// field values will not affect the block.
//
// The values of TxHash, ParentUncles, ReceiptHash and Bloom in header
// are ignored and set to values derived from the given txs, uncles
// and receipts.
func NewBlock(header *Header, txs []*Transaction, uncles []*Header, receipts []*Receipt, hasher TrieHasher) *Block {
	b := &Block{header: CopyHeader(header)}

	// TODO: panic if len(txs) != len(receipts)
	if len(txs) == 0 {
		b.header.TxHash = EmptyTxsHash
	} else {
		b.header.TxHash = DeriveSha(Transactions(txs), hasher)
		b.transactions = make(Transactions, len(txs))
		copy(b.transactions, txs)
	}

	if len(receipts) == 0 {
		b.header.ReceiptHash = EmptyReceiptsHash
	} else {
		b.header.ReceiptHash = DeriveSha(Receipts(receipts), hasher)
		b.header.Bloom = CreateBloom(receipts)
	}

	if len(uncles) == 0 {
		b.header.UncleHash = EmptyUncleHash
	} else {
		b.header.UncleHash = CalcUncleHash(uncles)
		b.uncles = make([]*Header, len(uncles))
		for i := range uncles {
			b.uncles[i] = CopyHeader(uncles[i])
		}
	}

	return b
}

// NewBlockWithWithdrawals creates a new block with withdrawals. The input data is copied,
// changes to header and to the field values will not affect the block.
//
// The values of TxHash, UncleHash, ReceiptHash and Bloom in header are ignored and set to
// values derived from the given txs, uncles and receipts.
func NewBlockWithWithdrawals(header *Header, txs []*Transaction, uncles []*Header, receipts []*Receipt, withdrawals []*Withdrawal, hasher TrieHasher) *Block {
	b := NewBlock(header, txs, uncles, receipts, hasher)

	if withdrawals == nil {
		b.header.WithdrawalsHash = nil
	} else if len(withdrawals) == 0 {
		b.header.WithdrawalsHash = &EmptyWithdrawalsHash
	} else {
		h := DeriveSha(Withdrawals(withdrawals), hasher)
		b.header.WithdrawalsHash = &h
	}

	return b.WithWithdrawals(withdrawals)
}

// CopyHeader creates a deep copy of a block header.
func CopyHeader(h *Header) *Header {
	cpy := *h
	if cpy.Difficulty = new(big.Int); h.Difficulty != nil {
		cpy.Difficulty.Set(h.Difficulty)
	}
	if cpy.Number = new(big.Int); h.Number != nil {
		cpy.Number.Set(h.Number)
	}
	if h.BaseFee != nil {
		cpy.BaseFee = new(big.Int).Set(h.BaseFee)
	}
	if len(h.Extra) > 0 {
		cpy.Extra = make([]byte, len(h.Extra))
		copy(cpy.Extra, h.Extra)
	}
	if h.WithdrawalsHash != nil {
		cpy.WithdrawalsHash = new(common.Hash)
		*cpy.WithdrawalsHash = *h.WithdrawalsHash
	}
	if h.BondKey != nil {
		cpy.BondKey = new(common.Hash)
		*cpy.BondKey = *h.BondKey
	}
	if h.PiHash != nil {
		cpy.PiHash = new(common.Hash)
		*cpy.PiHash = *h.PiHash
	}
	if h.EndorsementsHash != nil {
		cpy.EndorsementsHash = new(common.Hash)
		*cpy.EndorsementsHash = *h.EndorsementsHash
	}
	if h.SharesRoot != nil {
		cpy.SharesRoot = new(common.Hash)
		*cpy.SharesRoot = *h.SharesRoot
	}
	if len(h.Shares) > 0 {
		cpy.Shares = make([]byte, len(h.Shares))
		copy(cpy.Shares, h.Shares)
	}
	if len(h.Sig) > 0 {
		cpy.Sig = make([]byte, len(h.Sig))
		copy(cpy.Sig, h.Sig)
	}
	if len(h.Coord) > 0 {
		cpy.Coord = make([]byte, len(h.Coord))
		copy(cpy.Coord, h.Coord)
	}
	if len(h.Ticket) > 0 {
		cpy.Ticket = make([]byte, len(h.Ticket))
		copy(cpy.Ticket, h.Ticket)
	}
	if len(h.Parents) > 0 {
		cpy.Parents = make([]byte, len(h.Parents))
		copy(cpy.Parents, h.Parents)
	}
	return &cpy
}

// DecodeRLP decodes a block from RLP.
func (b *Block) DecodeRLP(s *rlp.Stream) error {
	var eb extblock
	_, size, _ := s.Kind()
	if err := s.Decode(&eb); err != nil {
		return err
	}
	b.header, b.uncles, b.transactions, b.withdrawals, b.pi, b.endorsements, b.minedShares = eb.Header, eb.Uncles, eb.Txs, eb.Withdrawals, eb.Pi, eb.Endorsements, eb.MinedShares
	// An empty withdrawals list is an ABSENT one on Igneum (a list root is
	// set only with entries): the encoder writes an absent list as an empty
	// one whenever a later optional field (the proof, the endorsements) is
	// set, and a receiver reading it as "a list present under a header with
	// no root" refused every proven block (the sixth development chain's
	// peers held at 19 while the sealer ran on, 11 October 2026).
	if len(b.withdrawals) == 0 {
		b.withdrawals = nil
	}
	b.size.Store(rlp.ListSize(size))
	return nil
}

// EncodeRLP serializes a block as RLP.
func (b *Block) EncodeRLP(w io.Writer) error {
	return rlp.Encode(w, &extblock{
		Header:       b.header,
		Txs:          b.transactions,
		Uncles:       b.uncles,
		Withdrawals:  b.withdrawals,
		Pi:           b.pi,
		Endorsements: b.endorsements,
		MinedShares:  b.minedShares,
	})
}

// Body returns the non-header content of the block.
// Note the returned data is not an independent copy.
func (b *Block) Body() *Body {
	return &Body{b.transactions, b.uncles, b.withdrawals, b.pi, b.endorsements, b.minedShares}
}

// MinedShares returns the share lane's carried shares in the body (nil
// without any): 69-byte entries concatenated.
func (b *Block) MinedShares() []byte { return b.minedShares }

// Pi returns the keyed seal's proof carried in the body (nil without one).
func (b *Block) Pi() []byte { return b.pi }

// Endorsements returns the parent's endorsements carried in the body (nil
// without any): the settlement rule's 100-byte entries concatenated.
func (b *Block) Endorsements() []byte { return b.endorsements }

// Accessors for body data. These do not return a copy because the content
// of the body slices does not affect the cached hash/size in block.

func (b *Block) Uncles() []*Header          { return b.uncles }
func (b *Block) Transactions() Transactions { return b.transactions }
func (b *Block) Withdrawals() Withdrawals   { return b.withdrawals }

func (b *Block) Transaction(hash common.Hash) *Transaction {
	for _, transaction := range b.transactions {
		if transaction.Hash() == hash {
			return transaction
		}
	}
	return nil
}

// Header returns the block header (as a copy).
func (b *Block) Header() *Header {
	return CopyHeader(b.header)
}

// Header value accessors. These do copy!

func (b *Block) Number() *big.Int     { return new(big.Int).Set(b.header.Number) }
func (b *Block) GasLimit() uint64     { return b.header.GasLimit }
func (b *Block) GasUsed() uint64      { return b.header.GasUsed }
func (b *Block) Difficulty() *big.Int { return new(big.Int).Set(b.header.Difficulty) }
func (b *Block) Time() uint64         { return b.header.Time }

func (b *Block) NumberU64() uint64        { return b.header.Number.Uint64() }
func (b *Block) MixDigest() common.Hash   { return b.header.MixDigest }
func (b *Block) Nonce() uint64            { return binary.BigEndian.Uint64(b.header.Nonce[:]) }
func (b *Block) Bloom() Bloom             { return b.header.Bloom }
func (b *Block) Coinbase() common.Address { return b.header.Coinbase }
func (b *Block) Root() common.Hash        { return b.header.Root }
func (b *Block) ParentHash() common.Hash  { return b.header.ParentHash }
func (b *Block) TxHash() common.Hash      { return b.header.TxHash }
func (b *Block) ReceiptHash() common.Hash { return b.header.ReceiptHash }
func (b *Block) UncleHash() common.Hash   { return b.header.UncleHash }
func (b *Block) Extra() []byte            { return common.CopyBytes(b.header.Extra) }

func (b *Block) BaseFee() *big.Int {
	if b.header.BaseFee == nil {
		return nil
	}
	return new(big.Int).Set(b.header.BaseFee)
}

// Size returns the true RLP encoded storage size of the block, either by encoding
// and returning it, or returning a previously cached value.
func (b *Block) Size() uint64 {
	if size := b.size.Load(); size != nil {
		return size.(uint64)
	}
	c := writeCounter(0)
	rlp.Encode(&c, b)
	b.size.Store(uint64(c))
	return uint64(c)
}

// SanityCheck can be used to prevent that unbounded fields are
// stuffed with junk data to add processing overhead
func (b *Block) SanityCheck() error {
	return b.header.SanityCheck()
}

type writeCounter uint64

func (c *writeCounter) Write(b []byte) (int, error) {
	*c += writeCounter(len(b))
	return len(b), nil
}

func CalcUncleHash(uncles []*Header) common.Hash {
	if len(uncles) == 0 {
		return EmptyUncleHash
	}
	return rlpHash(uncles)
}

// NewBlockWithHeader creates a block with the given header data. The
// header data is copied, changes to header and to the field values
// will not affect the block.
func NewBlockWithHeader(header *Header) *Block {
	return &Block{header: CopyHeader(header)}
}

// WithSeal returns a new block with the data from b but the header replaced with
// the sealed one.
func (b *Block) WithSeal(header *Header) *Block {
	return &Block{
		header:       CopyHeader(header),
		transactions: b.transactions,
		uncles:       b.uncles,
		withdrawals:  b.withdrawals,
		pi:           b.pi,
		endorsements: b.endorsements,
		minedShares:  b.minedShares,
	}
}

// WithPi returns a copy of the block carrying the keyed seal's proof in its
// body (the header's PiHash is the caller's to set; nil removes the proof).
func (b *Block) WithPi(pi []byte) *Block {
	block := &Block{
		header:       b.header,
		transactions: b.transactions,
		uncles:       b.uncles,
		withdrawals:  b.withdrawals,
		endorsements: b.endorsements,
		minedShares:  b.minedShares,
	}
	if pi != nil {
		block.pi = make([]byte, len(pi))
		copy(block.pi, pi)
	}
	return block
}

// WithEndorsements returns a copy of the block carrying the parent's
// endorsements in its body (the header's EndorsementsHash and Endorsed are
// the caller's to set; nil removes the list).
func (b *Block) WithEndorsements(endorsements []byte) *Block {
	block := &Block{
		header:       b.header,
		transactions: b.transactions,
		uncles:       b.uncles,
		withdrawals:  b.withdrawals,
		pi:           b.pi,
		minedShares:  b.minedShares,
	}
	if endorsements != nil {
		block.endorsements = make([]byte, len(endorsements))
		copy(block.endorsements, endorsements)
	}
	return block
}

// WithMinedShares returns a copy of the block carrying the share lane's
// shares in its body (the header's SharesRoot is the caller's to set; nil
// removes the list).
func (b *Block) WithMinedShares(entries []byte) *Block {
	block := &Block{
		header:       b.header,
		transactions: b.transactions,
		uncles:       b.uncles,
		withdrawals:  b.withdrawals,
		pi:           b.pi,
		endorsements: b.endorsements,
	}
	if entries != nil {
		block.minedShares = make([]byte, len(entries))
		copy(block.minedShares, entries)
	}
	return block
}

// WithBody returns a copy of the block with the given transaction and uncle contents.
func (b *Block) WithBody(transactions []*Transaction, uncles []*Header) *Block {
	block := &Block{
		header:       b.header,
		transactions: make([]*Transaction, len(transactions)),
		uncles:       make([]*Header, len(uncles)),
		withdrawals:  b.withdrawals,
		pi:           b.pi,
		endorsements: b.endorsements,
		minedShares:  b.minedShares,
	}
	copy(block.transactions, transactions)
	for i := range uncles {
		block.uncles[i] = CopyHeader(uncles[i])
	}
	return block
}

// WithWithdrawals returns a copy of the block containing the given withdrawals.
func (b *Block) WithWithdrawals(withdrawals []*Withdrawal) *Block {
	block := &Block{
		header:       b.header,
		transactions: b.transactions,
		uncles:       b.uncles,
		pi:           b.pi,
		endorsements: b.endorsements,
		minedShares:  b.minedShares,
	}
	if len(withdrawals) > 0 {
		// an empty list is an absent one on Igneum (DecodeRLP's note)
		block.withdrawals = make([]*Withdrawal, len(withdrawals))
		copy(block.withdrawals, withdrawals)
	}
	return block
}

// Hash returns the keccak256 hash of b's header.
// The hash is computed on the first call and cached thereafter.
func (b *Block) Hash() common.Hash {
	if hash := b.hash.Load(); hash != nil {
		return hash.(common.Hash)
	}
	v := b.header.Hash()
	b.hash.Store(v)
	return v
}

type Blocks []*Block

// HeaderParentHashFromRLP returns the parentHash of an RLP-encoded
// header. If 'header' is invalid, the zero hash is returned.
func HeaderParentHashFromRLP(header []byte) common.Hash {
	// parentHash is the first list element.
	listContent, _, err := rlp.SplitList(header)
	if err != nil {
		return common.Hash{}
	}
	parentHash, _, err := rlp.SplitString(listContent)
	if err != nil {
		return common.Hash{}
	}
	if len(parentHash) != 32 {
		return common.Hash{}
	}
	return common.BytesToHash(parentHash)
}
