// Package ishr is the Igneum share sub-protocol (PA-46, the launch form of
// "accepted means paid"; the founder's order of 16:0x UK, 12 October 2026 by
// the project's clock): a valid share propagates to every sealer's pool like a
// transaction, on the existing peer links beside eth, so a solo miner may
// submit to ANY node (sealer or not) and the first carrying block pays it.
//
// A node announces the hashes of the shares it holds (NewShareHashes, with
// the heights so a peer skips what it holds or what is past its recency),
// a peer fetches what it lacks (GetShares, at most 256 a request), and the
// shares travel as the 109-byte wire form (the carried entry's fields plus
// the parent hash and the height). Every received share is verified exactly
// as a direct submission (the parent against this node's canonical block at
// its height first, the work under the share target, the duplicate, the
// per-address cap, the pool bound) and then pooled and forwarded ONCE
// (hash-based, a per-peer known set, so no loops); a peer sending a share
// whose work fails, or a malformed message, is dropped as a bad peer; a
// stale parent, a duplicate, a cap or a full pool is not a fault of the peer.
// Peers without the capability are simply not sent shares.
package ishr

import (
	"encoding/binary"
	"errors"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// Constants to match up protocol versions and messages
const (
	ISHR1 = 1
	// ISHR2 carries the share's declared target step (PA-53) as the
	// Share's last field; a node of either version speaks only its own,
	// so a chain's nodes change together (the ninth development chain
	// from genesis).
	ISHR2 = 2
	// ISHR3 adds the endorsement gossip (EndorsementsMsg): an endorser's
	// signed entries for a block spread over the share mesh beside the
	// one-shot HTTP push, so every sealer's pool holds every endorser's
	// set and an open-seat sealer on no push list carries like a bonded
	// one (the ninth development chain's third carry class, 12 October
	// 2026). ISHR2 stays offered, so a mixed fleet keeps the share gossip
	// through a roll window; the endorsements reach ISHR3 peers only.
	ISHR3 = 3
)

// ProtocolName is the official short name of the `ishr` protocol used during
// devp2p capability negotiation.
const ProtocolName = "ishr"

// ProtocolVersions are the supported versions of the `ishr` protocol (first
// is primary).
var ProtocolVersions = []uint{ISHR3, ISHR2}

// protocolLengths are the number of implemented message corresponding to
// different protocol versions.
var protocolLengths = map[uint]uint64{ISHR3: 4, ISHR2: 3}

// maxMessageSize is the maximum cap on the size of a protocol message.
const maxMessageSize = 2 * 1024 * 1024

const (
	NewShareHashesMsg = 0x00
	GetSharesMsg      = 0x01
	SharesMsg         = 0x02
	// EndorsementsMsg (ISHR3) carries one endorser's entries for one block.
	EndorsementsMsg = 0x03
)

// The DoS bounds (the eth transaction fetcher's, scaled to the share lane).
const (
	// maxAnnounces is the most hashes one announcement carries (and the
	// most a node keeps outstanding from one peer).
	maxAnnounces = 4096
	// maxRetrieval is the most shares one fetch asks for or one reply
	// carries.
	maxRetrieval = 256
	// maxKnownShares is the per-peer known set (the forward-once bound).
	maxKnownShares = 65536
	// maxEndorsementBytes bounds one endorsement list on the wire: 4,096
	// entries of 100 bytes (key, LE32 unit, signature), above any chain's
	// MaxEndorsements.
	maxEndorsementBytes = 4096 * 100
	// maxSeenEndorsements is the handler's spread-once set of lists.
	maxSeenEndorsements = 4096
)

var (
	errMsgTooLarge    = errors.New("message too long")
	errDecode         = errors.New("invalid message")
	errInvalidMsgCode = errors.New("invalid message code")
	errBadShare       = errors.New("the peer sent a share whose work does not verify")
	errTooMany        = errors.New("the message carries more items than the bound")
	errBanned         = errors.New("the peer is held out for a share whose work failed")
)

// Share is the wire form of a share (109 bytes of fields): the carried
// entry's address, extra nonce, nonce and mix, plus the parent the miner
// mined on and the height, so a receiving node checks the parent against
// its own canonical block at that height before any work.
type Share struct {
	Address    common.Address
	ExtraNonce uint64
	Nonce      uint64
	MixDigest  common.Hash
	ParentHash common.Hash
	Height     uint64
	// Step is PA-53's declared target step (0 under the count form), the
	// second wire form (ISHR2): a receiving node verifies the hash under
	// the share target over 2^Step and the entry weighs 2^Step. The share's
	// identity (Hash) is without it: one share at two steps is one share.
	Step uint8
}

// Hash is the share's identity on the wire and in the known sets:
// KECCAK(address || LE64(extraNonce) || LE64(nonce) || parentHash ||
// LE64(height)).
func (s *Share) Hash() common.Hash {
	var b [20 + 8 + 8 + 32 + 8]byte
	copy(b[0:], s.Address[:])
	binary.LittleEndian.PutUint64(b[20:], s.ExtraNonce)
	binary.LittleEndian.PutUint64(b[28:], s.Nonce)
	copy(b[36:], s.ParentHash[:])
	binary.LittleEndian.PutUint64(b[68:], s.Height)
	return crypto.Keccak256Hash(b[:])
}

// Announce is one announced share: its hash and height.
type Announce struct {
	Hash   common.Hash
	Height uint64
}

// NewShareHashesPacket is the announcement of shares a node holds.
type NewShareHashesPacket []Announce

// GetSharesPacket asks for shares by hash.
type GetSharesPacket struct {
	RequestId uint64
	Hashes    []common.Hash
}

// SharesPacket answers a fetch, or carries shares unasked.
type SharesPacket struct {
	RequestId uint64
	Shares    []Share
}

// EndorsementsPacket carries one endorser's signed entries for one block
// (the bytes igneum_submitEndorsements takes: key || LE32(unit) ||
// signature, 100 bytes each), as the endorser's tick pushed them.
type EndorsementsPacket struct {
	Height    uint64
	BlockHash common.Hash
	Entries   []byte
}

// Hash is the list's identity in the spread-once sets:
// KECCAK(LE64(height) || blockHash || entries).
func (p *EndorsementsPacket) Hash() common.Hash {
	b := make([]byte, 8+32+len(p.Entries))
	binary.LittleEndian.PutUint64(b[0:], p.Height)
	copy(b[8:], p.BlockHash[:])
	copy(b[40:], p.Entries)
	return crypto.Keccak256Hash(b)
}
