// Package forge is the frozen Forge relation of F0, revision 2 on the
// Bounded Memory Tickets objects (docs/igneumhash/forge/F0-RELATION.md,
// FORGE-B1-SPEC.md section 3): the job and its id, the epoch seed from the
// delayed anchor, the fill seed, the commitment's leaves and nodes, the B
// tickets of a fill, the challenge derivation and the proof's public digest.
// It computes no fill (F1) and verifies no proof (F2): it fixes the bytes
// those bind to.
//
// Every integer is unsigned little-endian and fixed-width (the B1 rule);
// every variable-length field carries a one-byte length; the domain strings
// are ASCII consensus constants absorbed as one length byte then the bytes.
// A version 2 job is exactly JobLen bytes and DecodeJob accepts nothing else.
// The one exception to little-endian is a 256-bit value compared as a
// number (a target, a ticket): it is 32 bytes big-endian, the fixed
// interpretation of the target comparison.
package forge

import (
	"encoding/binary"
	"errors"
	"fmt"

	"lukechampine.com/blake3"
)

// Version is the relation version committed in every job.
const Version uint32 = 2

// The domain strings (F0-RELATION.md section 2.1).
const (
	JobDomain       = "Igneum/Forge/job/v2"
	RewardDomain    = "Igneum/Forge/reward/v2"
	EpochDomain     = "Igneum/Forge/epoch/v2"
	FillDomain      = "Igneum/Forge/fill/v2"
	LeafDomain      = "Igneum/Forge/leaf/v2"
	NodeDomain      = "Igneum/Forge/node/v2"
	TicketDomain    = "Igneum/Forge/ticket/v2"
	ChallengeDomain = "Igneum/Forge/challenge/v2"
	PublicDomain    = "Igneum/Forge/public/v2"
)

// JobLen is the length of a version 2 job: 1+19 (domain) + 8 (chain id) +
// 4 (version) + 32 (parent id) + 8 (height) + 8 (epoch id) + 32 (parameter
// id) + 32 (target) + 32 (transaction commitment) + 32 (reward commitment).
const JobLen = 1 + len(JobDomain) + 8 + 4 + 32 + 8 + 8 + 32 + 32 + 32 + 32

// Job is the field tuple of a job (section 2.2), in the order Encode
// absorbs it. Everything a miner selects that reaches a ticket is here or
// inside one of the two commitments.
type Job struct {
	ChainID      uint64   // the chain's id
	Version      uint32   // the relation version (Version)
	ParentID     [32]byte // the parent block's header hash
	Height       uint64   // the block's height
	EpochID      uint64   // Height / epoch_blocks of the parameter document
	ParamsID     [32]byte // the parameter id in force at Height (the chain's table)
	Target       [32]byte // the consensus target, big-endian
	TxCommit     [32]byte // the transactions root (the extra nonce lives in the reward commitment)
	RewardCommit [32]byte // RewardCommitment(coinbase, extra)
}

var (
	ErrLength  = errors.New("forge: job is not the version 2 length")
	ErrDomain  = errors.New("forge: job does not open with the version 2 domain")
	ErrVersion = errors.New("forge: job carries a relation version this package does not encode")
	ErrIndex   = errors.New("forge: ticket index is not below B")
)

func hasher(domain string, cap int) []byte {
	b := make([]byte, 0, 1+len(domain)+cap)
	b = append(b, byte(len(domain)))
	return append(b, domain...)
}

func sum(b []byte) [32]byte { return blake3.Sum256(b) }

// RewardCommitment binds the reward destination and the extra data (where
// an extra nonce lives): H(reward domain || u8 20 || coinbase || u8 len ||
// extra). The extra data is at most 32 bytes (the header rule), so one
// length byte delimits it.
func RewardCommitment(coinbase [20]byte, extra []byte) [32]byte {
	b := hasher(RewardDomain, 1+20+1+len(extra))
	b = append(b, 20)
	b = append(b, coinbase[:]...)
	b = append(b, byte(len(extra)))
	b = append(b, extra...)
	return sum(b)
}

// Encode is the canonical job of a tuple: the one byte string it has.
func Encode(j Job) []byte {
	b := hasher(JobDomain, JobLen)
	b = binary.LittleEndian.AppendUint64(b, j.ChainID)
	b = binary.LittleEndian.AppendUint32(b, j.Version)
	b = append(b, j.ParentID[:]...)
	b = binary.LittleEndian.AppendUint64(b, j.Height)
	b = binary.LittleEndian.AppendUint64(b, j.EpochID)
	b = append(b, j.ParamsID[:]...)
	b = append(b, j.Target[:]...)
	b = append(b, j.TxCommit[:]...)
	b = append(b, j.RewardCommit[:]...)
	return b
}

// DecodeJob is the strict inverse of Encode: the exact length, the version
// 2 domain and the version this package encodes, or an error naming the
// first failing field.
func DecodeJob(b []byte) (Job, error) {
	var j Job
	if len(b) != JobLen {
		return j, fmt.Errorf("%w: %d bytes, want %d", ErrLength, len(b), JobLen)
	}
	p := 0
	if int(b[p]) != len(JobDomain) || string(b[p+1:p+1+len(JobDomain)]) != JobDomain {
		return j, ErrDomain
	}
	p += 1 + len(JobDomain)
	j.ChainID = binary.LittleEndian.Uint64(b[p:])
	p += 8
	j.Version = binary.LittleEndian.Uint32(b[p:])
	p += 4
	if j.Version != Version {
		return j, fmt.Errorf("%w: %d", ErrVersion, j.Version)
	}
	copy(j.ParentID[:], b[p:])
	p += 32
	j.Height = binary.LittleEndian.Uint64(b[p:])
	p += 8
	j.EpochID = binary.LittleEndian.Uint64(b[p:])
	p += 8
	copy(j.ParamsID[:], b[p:])
	p += 32
	copy(j.Target[:], b[p:])
	p += 32
	copy(j.TxCommit[:], b[p:])
	p += 32
	copy(j.RewardCommit[:], b[p:])
	return j, nil
}

// JobID is H(job): BLAKE3-256 of the canonical job bytes (the job opens
// with its own domain, so no second domain is applied).
func JobID(job []byte) [32]byte { return sum(job) }

// EpochSeed is H(epoch domain || delayed anchor || epoch id || parameter
// id): the topology's only input (section 3). The anchor is the canonical
// block hash at AnchorHeight.
func EpochSeed(anchor [32]byte, epochID uint64, paramsID [32]byte) [32]byte {
	b := hasher(EpochDomain, 32+8+32)
	b = append(b, anchor[:]...)
	b = binary.LittleEndian.AppendUint64(b, epochID)
	b = append(b, paramsID[:]...)
	return sum(b)
}

// AnchorHeight is the height of the delayed anchor of an epoch: the first
// height of the epoch less the delay, floored at the genesis. With the
// default delay of one epoch the anchor is the first block of the previous
// epoch, known a whole epoch ahead (section 3.2).
func AnchorHeight(epochID, epochBlocks, delay uint64) uint64 {
	first := epochID * epochBlocks
	if first <= delay {
		return 0
	}
	return first - delay
}

// FillSeed is H(fill domain || epoch seed || job id || batch nonce): the
// fill's only input (section 4).
func FillSeed(epochSeed, jobID [32]byte, batchNonce uint64) [32]byte {
	b := hasher(FillDomain, 32+32+8)
	b = append(b, epochSeed[:]...)
	b = append(b, jobID[:]...)
	b = binary.LittleEndian.AppendUint64(b, batchNonce)
	return sum(b)
}

// Leaf is H(leaf domain || u64 index || label) (section 5).
func Leaf(index uint64, label []byte) [32]byte {
	b := hasher(LeafDomain, 8+len(label))
	b = binary.LittleEndian.AppendUint64(b, index)
	b = append(b, label...)
	return sum(b)
}

// Node is H(node domain || left || right) (section 5).
func Node(left, right [32]byte) [32]byte {
	b := hasher(NodeDomain, 64)
	b = append(b, left[:]...)
	b = append(b, right[:]...)
	return sum(b)
}

// Root is the commitment over exactly N labels, N a power of two: leaves in
// index order, each level the Node of adjacent pairs (section 5). It is
// the specification of the root for the vectors, not a fill-sized tool.
func Root(labels [][]byte) ([32]byte, error) {
	n := len(labels)
	if n == 0 || n&(n-1) != 0 {
		return [32]byte{}, errors.New("forge: the leaf count is not a power of two")
	}
	level := make([][32]byte, n)
	for i, l := range labels {
		level[i] = Leaf(uint64(i), l)
	}
	for len(level) > 1 {
		next := make([][32]byte, len(level)/2)
		for i := range next {
			next[i] = Node(level[2*i], level[2*i+1])
		}
		level = next
	}
	return level[0], nil
}

// Ticket is ticket_j = H(ticket domain || job id || root || batch nonce ||
// u32 j || u32 B || parameter id) for j below B (section 6); no other
// field yields a ticket from the same root.
func Ticket(jobID, root [32]byte, batchNonce uint64, j, B uint32, paramsID [32]byte) ([32]byte, error) {
	if j >= B {
		return [32]byte{}, fmt.Errorf("%w: %d of %d", ErrIndex, j, B)
	}
	b := hasher(TicketDomain, 32+32+8+4+4+32)
	b = append(b, jobID[:]...)
	b = append(b, root[:]...)
	b = binary.LittleEndian.AppendUint64(b, batchNonce)
	b = binary.LittleEndian.AppendUint32(b, j)
	b = binary.LittleEndian.AppendUint32(b, B)
	b = append(b, paramsID[:]...)
	return sum(b), nil
}

// Qualifies reports whether a ticket meets the target: the ticket read as a
// 256-bit big-endian integer is at most the target (inclusive).
func Qualifies(ticket, target [32]byte) bool {
	for i := 0; i < 32; i++ {
		if ticket[i] != target[i] {
			return ticket[i] < target[i]
		}
	}
	return true
}

// Challenges are the q challenged leaf indices of a qualifying ticket: the
// BLAKE3 extendable output over the challenge domain and the ticket (the
// random-oracle digest of the complete ticket transcript), read as
// successive little-endian 64-bit words, each reduced mod N (section 7).
// The ticket binds every transcript field, so every challenge does.
func Challenges(ticket [32]byte, q uint32, N uint64) []uint64 {
	b := hasher(ChallengeDomain, 32)
	b = append(b, ticket[:]...)
	h := blake3.New(32, nil)
	h.Write(b)
	stream := make([]byte, 8*q)
	h.XOF().Read(stream)
	out := make([]uint64, q)
	for k := range out {
		out[k] = binary.LittleEndian.Uint64(stream[8*k:]) % N
	}
	return out
}

// Public is the proof's public-input digest: H(public domain || program id
// || chain id || version || job id || root || batch nonce || u32 j || u32 B
// || parameter id) (section 8). A proof verifies against this digest and
// nothing else.
func Public(programID [32]byte, chainID uint64, version uint32, jobID, root [32]byte, batchNonce uint64, j, B uint32, paramsID [32]byte) [32]byte {
	b := hasher(PublicDomain, 32+8+4+32+32+8+4+4+32)
	b = append(b, programID[:]...)
	b = binary.LittleEndian.AppendUint64(b, chainID)
	b = binary.LittleEndian.AppendUint32(b, version)
	b = append(b, jobID[:]...)
	b = append(b, root[:]...)
	b = binary.LittleEndian.AppendUint64(b, batchNonce)
	b = binary.LittleEndian.AppendUint32(b, j)
	b = binary.LittleEndian.AppendUint32(b, B)
	b = append(b, paramsID[:]...)
	return sum(b)
}
