// Package forgecheckr2 is protocol B's independent implementation of the
// Forge F0 relation, revision 2 (docs/igneumhash/forge/F0-RELATION.md on the
// Bounded Memory Tickets objects, at 83d4136d), written from the protocol team's
// text alone (sections 1 to 8), never from his pow/forge. The objects: the
// job and its id, the reward commitment, the epoch seed, the fill seed, the
// commitment over N labels, the B tickets, the challenge stream, the public
// digest. Research only: nothing here is deployable.
package forgecheckr2

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"

	"lukechampine.com/blake3"
)

// The domains of section 2.1.
const (
	DomainJob       = "Igneum/Forge/job/v2"
	DomainReward    = "Igneum/Forge/reward/v2"
	DomainEpoch     = "Igneum/Forge/epoch/v2"
	DomainFill      = "Igneum/Forge/fill/v2"
	DomainLeaf      = "Igneum/Forge/leaf/v2"
	DomainNode      = "Igneum/Forge/node/v2"
	DomainTicket    = "Igneum/Forge/ticket/v2"
	DomainChallenge = "Igneum/Forge/challenge/v2"
	DomainPublic    = "Igneum/Forge/public/v2"
)

const Version uint32 = 2

// JobLen is 1 + 19 + 8 + 4 + 32 + 8 + 8 + 32 + 32 + 32 + 32.
const JobLen = 208

// MaxExtra is the header rule's bound on the extra data.
const MaxExtra = 32

var ErrEncoding = errors.New("forge f0 r2: encoding rejected")

func domainBytes(d string) []byte { return append([]byte{byte(len(d))}, d...) }

// Hd is H_d(m): BLAKE3-256 over one byte of domain length, the domain, then m.
func Hd(domain string, m ...[]byte) [32]byte {
	return blake3.Sum256(append(domainBytes(domain), bytes.Join(m, nil)...))
}

// XOFd is XOF_d(m): BLAKE3's extendable output over the same bytes, n bytes.
func XOFd(domain string, n int, m ...[]byte) []byte {
	h := blake3.New(32, nil)
	h.Write(append(domainBytes(domain), bytes.Join(m, nil)...))
	out := make([]byte, n)
	h.XOF().Read(out)
	return out
}

func le32(v uint32) []byte { return binary.LittleEndian.AppendUint32(nil, v) }
func le64(v uint64) []byte { return binary.LittleEndian.AppendUint64(nil, v) }

// Job is the tuple of section 2.2.
type Job struct {
	ChainID      uint64
	Version      uint32
	ParentID     [32]byte
	Height       uint64
	EpochID      uint64
	ParamsID     [32]byte
	Target       [32]byte
	TxCommitment [32]byte
	Reward       [32]byte
}

// RewardCommitment is H_reward(u8 20 || coinbase || u8 len || extra data); extra data over 32 bytes is rejected.
func RewardCommitment(coinbase [20]byte, extra []byte) ([32]byte, error) {
	if len(extra) > MaxExtra {
		return [32]byte{}, fmt.Errorf("%w: extra data of %d bytes, at most %d", ErrEncoding, len(extra), MaxExtra)
	}
	return Hd(DomainReward, []byte{20}, coinbase[:], []byte{byte(len(extra))}, extra), nil
}

// Encode is the 208-byte job: the fields in order, unsigned little-endian integers.
func (j Job) Encode() []byte {
	x := make([]byte, 0, JobLen)
	x = append(x, domainBytes(DomainJob)...)
	x = binary.LittleEndian.AppendUint64(x, j.ChainID)
	x = binary.LittleEndian.AppendUint32(x, j.Version)
	x = append(x, j.ParentID[:]...)
	x = binary.LittleEndian.AppendUint64(x, j.Height)
	x = binary.LittleEndian.AppendUint64(x, j.EpochID)
	x = append(x, j.ParamsID[:]...)
	x = append(x, j.Target[:]...)
	x = append(x, j.TxCommitment[:]...)
	x = append(x, j.Reward[:]...)
	return x
}

// Decode is the strict decoder of section 2.4.
func Decode(x []byte) (Job, error) {
	var j Job
	if len(x) != JobLen {
		return j, fmt.Errorf("%w: job of %d bytes, not %d", ErrEncoding, len(x), JobLen)
	}
	if x[0] != byte(len(DomainJob)) {
		return j, fmt.Errorf("%w: domain length %d, not %d", ErrEncoding, x[0], len(DomainJob))
	}
	if string(x[1:20]) != DomainJob {
		return j, fmt.Errorf("%w: domain bytes are not the job domain", ErrEncoding)
	}
	j.ChainID = binary.LittleEndian.Uint64(x[20:28])
	j.Version = binary.LittleEndian.Uint32(x[28:32])
	if j.Version != Version {
		return j, fmt.Errorf("%w: version %d, not %d", ErrEncoding, j.Version, Version)
	}
	copy(j.ParentID[:], x[32:64])
	j.Height = binary.LittleEndian.Uint64(x[64:72])
	j.EpochID = binary.LittleEndian.Uint64(x[72:80])
	copy(j.ParamsID[:], x[80:112])
	copy(j.Target[:], x[112:144])
	copy(j.TxCommitment[:], x[144:176])
	copy(j.Reward[:], x[176:208])
	return j, nil
}

// JobID is H(job): BLAKE3-256 of the 208 bytes, no second domain.
func JobID(job []byte) [32]byte { return blake3.Sum256(job) }

// EpochSeed is H_epoch(delayed anchor || u64 epoch id || parameter id).
func EpochSeed(anchor [32]byte, epochID uint64, paramsID [32]byte) [32]byte {
	return Hd(DomainEpoch, anchor[:], le64(epochID), paramsID[:])
}

// AnchorHeight is max(0, epoch id x epoch_blocks - D).
func AnchorHeight(epochID, epochBlocks, delay uint64) uint64 {
	h := epochID * epochBlocks
	if h < delay {
		return 0
	}
	return h - delay
}

// FillSeed is H_fill(epoch_seed || job id || u64 batch nonce).
func FillSeed(epochSeed, jobID [32]byte, batchNonce uint64) [32]byte {
	return Hd(DomainFill, epochSeed[:], jobID[:], le64(batchNonce))
}

// Leaf is H_leaf(u64 i || label_i); Node is H_node(left || right).
func Leaf(i uint64, label []byte) [32]byte { return Hd(DomainLeaf, le64(i), label) }
func Node(left, right [32]byte) [32]byte   { return Hd(DomainNode, left[:], right[:]) }

// Root is the pairwise tree over the N leaves in index order; a leaf count
// that is not a power of two is not a commitment.
func Root(labels [][]byte) ([32]byte, error) {
	n := len(labels)
	if n == 0 || n&(n-1) != 0 {
		return [32]byte{}, fmt.Errorf("%w: %d leaves is not a power of two", ErrEncoding, n)
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

// Ticket is ticket_j = H_ticket(job id || root || u64 batch nonce || u32 j || u32 B || parameter id); j >= B is rejected.
func Ticket(jobID, root [32]byte, batchNonce uint64, j, b uint32, paramsID [32]byte) ([32]byte, error) {
	if j >= b {
		return [32]byte{}, fmt.Errorf("%w: ticket index %d at or above B = %d", ErrEncoding, j, b)
	}
	return Hd(DomainTicket, jobID[:], root[:], le64(batchNonce), le32(j), le32(b), paramsID[:]), nil
}

// Qualifies: the ticket as a 256-bit big-endian integer is at most the target (inclusive).
func Qualifies(ticket, target [32]byte) bool { return bytes.Compare(ticket[:], target[:]) <= 0 }

// Challenges is challenge k = LE64(stream[8k .. 8k+8)) mod N over stream = XOF_challenge(ticket_j), k in [0, q).
func Challenges(ticket [32]byte, q int, n uint64) []uint64 {
	s := XOFd(DomainChallenge, 8*q, ticket[:])
	out := make([]uint64, q)
	for k := range out {
		out[k] = binary.LittleEndian.Uint64(s[8*k:]) % n
	}
	return out
}

// Public is H_public(program id || u64 chain id || u32 version || job id || root || u64 batch nonce || u32 j || u32 B || parameter id).
func Public(programID [32]byte, chainID uint64, version uint32, jobID, root [32]byte, batchNonce uint64, j, b uint32, paramsID [32]byte) [32]byte {
	return Hd(DomainPublic, programID[:], le64(chainID), le32(version), jobID[:], root[:], le64(batchNonce), le32(j), le32(b), paramsID[:])
}
