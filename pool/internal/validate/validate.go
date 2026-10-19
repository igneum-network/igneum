// Package validate is the pool's view of the algorithm: a Validator
// recomputes a submission on the CPU reference (the plan, page 8:
// affordable verification without a mining GPU) behind the interface GPU
// engineer A publishes. The pool never trusts a worker's claimed hash.
// Null is the deterministic stand-in for the devnet's `null` algorithm
// (protocol engineer A's D-A10): no candidate is wired until the founder
// ratifies P-I02.
package validate

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math/big"
)

// ErrAlgorithm is returned for an algorithm the validator does not carry.
var ErrAlgorithm = errors.New("validate: unknown algorithm")

// Validator recomputes mix and hash for a header, height and nonce.
type Validator interface {
	Verify(algorithm string, header [32]byte, height uint64, nonce uint64) (mix [32]byte, hash [32]byte, err error)
}

// DocumentValidator is a Validator that keeps one built algorithm per document digest and verifies a share on
// the document its job names (VectorWork: one document per coordinate).
type DocumentValidator interface {
	Validator
	VerifyOn(algorithm, digest string, header [32]byte, height uint64, nonce uint64) (mix [32]byte, hash [32]byte, err error)
}

// AnchoredValidator is a DocumentValidator that verifies a programmed document's share under the period's anchor
// (the chain form: period = height div period_blocks, the anchor the canonical block hash at period x P - D).
type AnchoredValidator interface {
	DocumentValidator
	PeriodBlocks(digest string) uint64
	VerifyUnder(algorithm, digest string, anchor [32]byte, header [32]byte, height uint64, nonce uint64) (mix [32]byte, hash [32]byte, err error)
}

// Null is the development chain's stand-in as the node defines it
// (node/consensus/igneum/pow/null):
//
//	mix    = keccak256(headerHash || be64(height) || le64(nonce))
//	result = keccak256(mix || headerHash)
//
// It is not a proof of work; it is the fixed, cheap function the dev
// chain seals with, so the pool is proven end to end against a real
// node before the selection. The frozen HW-01 vectors are in the test.
type Null struct{}

// NullAlgorithm is the id Null answers to.
const NullAlgorithm = "null"

// Verify implements Validator.
func (Null) Verify(algorithm string, header [32]byte, height uint64, nonce uint64) ([32]byte, [32]byte, error) {
	if algorithm != NullAlgorithm {
		return [32]byte{}, [32]byte{}, ErrAlgorithm
	}
	var buf [48]byte
	copy(buf[:32], header[:])
	binary.BigEndian.PutUint64(buf[32:40], height)
	binary.LittleEndian.PutUint64(buf[40:], nonce)
	mix := Keccak256(buf[:])
	hash := Keccak256(append(mix[:], header[:]...))
	return mix, hash, nil
}

// Preparer is implemented by a validator that builds its algorithm from
// the work package's params document: the service calls Prepare on every
// new work package (the digest keys the built algorithm; height anchors
// the epoch cache) before shares arrive for it.
type Preparer interface {
	Prepare(algorithm string, params []byte, digest string, height uint64) error
}

// Registry: validators by name, filled by build-tagged files (the
// igneumhash validator imports the node's consensus packages and is built
// with -tags igneumhash); Null is always present.
var registry = map[string]func() Validator{NullAlgorithm: func() Validator { return Null{} }}

// Register adds a validator constructor under a name.
func Register(name string, c func() Validator) { registry[name] = c }

// Lookup returns a validator by name.
func Lookup(name string) (Validator, bool) {
	c, ok := registry[name]
	if !ok {
		return nil, false
	}
	return c(), true
}

// MeetsTarget reports whether hash, read as a big-endian 256-bit number,
// is at or below target.
func MeetsTarget(hash, target [32]byte) bool {
	return bytes.Compare(hash[:], target[:]) <= 0
}

var two256 = new(big.Int).Lsh(big.NewInt(1), 256)

// Difficulty is 2^256 / (target + 1), clamped to uint64. It is the share
// weight the accounting uses (docs/ecosystem/DESIGN.md, section 2.7).
func Difficulty(target [32]byte) uint64 {
	t := new(big.Int).SetBytes(target[:])
	t.Add(t, big.NewInt(1))
	d := new(big.Int).Div(two256, t)
	if !d.IsUint64() {
		return ^uint64(0)
	}
	if d.Uint64() == 0 {
		return 1
	}
	return d.Uint64()
}

// TargetForDifficulty is the inverse: the largest target whose
// difficulty is at least d.
func TargetForDifficulty(d uint64) [32]byte {
	var out [32]byte
	if d == 0 {
		d = 1
	}
	t := new(big.Int).Div(two256, new(big.Int).SetUint64(d))
	t.Sub(t, big.NewInt(1))
	if t.Sign() < 0 {
		t.SetInt64(0)
	}
	b := t.Bytes()
	copy(out[32-len(b):], b)
	return out
}

// GraphAlgorithm is the graph coordinate's relation as the node names it (VectorWork coordinate 3): proof-carrying,
// so the pool names the template and forwards the share as claimed, the node verifying the proof (ProofCarried).
const GraphAlgorithm = "igneum-graph"

// ProofCarried reports an algorithm whose share carries a proof the pool cannot recompute from the nonce.
func ProofCarried(algorithm string) bool { return algorithm == GraphAlgorithm }

// TicketVerifier verifies a proof-carried share's ticket on the document named by digest under the share header
// and the nonce it was found with, returning the ticket hash (the figure checked against the share target).
type TicketVerifier interface {
	VerifyTicket(digest string, header [32]byte, height uint64, nonce uint64, ticket []byte) ([32]byte, error)
}
