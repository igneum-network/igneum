// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).

package pool

import (
	"crypto/subtle"
	"errors"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"lukechampine.com/blake3"
)

// Statement is a record's public inputs: for a spend (Kind SpendTag) the
// anchor, the two nullifiers, the two new commitments, the public value,
// the fee and the public output; for a shield (Kind ShieldTag) the new
// commitment and the shielded value; for a vault statement (Kind 3 to 9,
// vault.go) the record's public words in its order (Words). Word is their
// binding, the one value a proof is made against and checked against (the
// statement hash of tools/money/stage1-prover, the program's public input),
// so a proof moved to another record, another kind or another chain fails.
type Statement struct {
	Kind         byte
	ChainID      uint64
	Anchor       [32]byte
	Nullifiers   [][32]byte
	Commitments  [][32]byte
	PublicValue  *big.Int
	Fee          *big.Int
	PublicOutput common.Address
	Words        []Word
}

func wordAt(list [][32]byte, i int) Word {
	if i < len(list) {
		return wordOf(list[i])
	}
	return Word{}
}

// Word is the statement hash: for a shield H(H(domain, cm), value); for a
// spend H folded over [domain, anchor, nf1, nf2, cm1, cm2, vpub, fee,
// public output] (the vectors file names the encodings); for a vault
// statement H folded over the domain and its Words.
func (s Statement) Word() Word {
	if IsVaultTag(s.Kind) {
		return Chain(append([]Word{DomainWord(s.ChainID, s.Kind)}, s.Words...)...)
	}
	if s.Kind == ShieldTag {
		vw, _ := ValueWord(s.PublicValue)
		return Chain(DomainWord(s.ChainID, ShieldTag), wordAt(s.Commitments, 0), vw)
	}
	vw, _ := ValueWord(s.PublicValue)
	fw, _ := ValueWord(s.Fee)
	return Chain(DomainWord(s.ChainID, SpendTag), wordOf(s.Anchor), wordAt(s.Nullifiers, 0), wordAt(s.Nullifiers, 1),
		wordAt(s.Commitments, 0), wordAt(s.Commitments, 1), vw, fw, AddressWord(s.PublicOutput))
}

// Hash is the statement hash's 32 bytes.
func (s Statement) Hash() [32]byte {
	return WordBytes(s.Word())
}

// Verifier checks a record's proof against its statement. The real
// verifier (consensus/igneum/pool/miden: the pinned Miden VM 0.36.1
// verifier over the two statements' programs, through cgo) implements
// it; the node holds one value of it, set where the engine is built, and
// nothing else in the pool's rules changes with the value.
type Verifier interface {
	// Name is the verifier's name on the record and the pool view.
	Name() string
	// Verify returns nil for a proof that proves the statement, else the
	// refusal's reason.
	Verify(s Statement, proof []byte) error
}

// NoVerifier is the node's default: every proof refused, so a chain with
// the gate on and no verifier admits no shield and no spend.
type NoVerifier struct{}

// Name implements Verifier.
func (NoVerifier) Name() string { return "none" }

// Verify implements Verifier.
func (NoVerifier) Verify(Statement, []byte) error {
	return errors.New("no proof verifier on this node")
}

// UnsafeDigestDouble is the LAB'S TEST DOUBLE, NOT A VERIFIER: it accepts
// a proof whose first 32 bytes equal the BLAKE3 derive-key hash of the
// statement's hash and the proof's remaining bytes. Anyone can make such a
// proof for any statement, so it proves nothing; it exists so the pool's
// rules (the root, the nullifiers, the turnstile, the statement binding)
// are testable without a prover. The backend installs it only under --dev
// with --dev.pool.unsafe-verifier on a research-range chain id, never on a
// chain of record, and the node's log says so at start.
type UnsafeDigestDouble struct{}

const tagDouble = "igneum money pool UNSAFE digest double v1"

func derive(tag string, parts ...[]byte) (out [32]byte) {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	buf := make([]byte, 0, n)
	for _, p := range parts {
		buf = append(buf, p...)
	}
	blake3.DeriveKey(out[:], tag, buf)
	return out
}

// Name implements Verifier.
func (UnsafeDigestDouble) Name() string { return "UNSAFE-digest-double" }

// Verify implements Verifier.
func (UnsafeDigestDouble) Verify(s Statement, proof []byte) error {
	if len(proof) < 32 {
		return errors.New("the proof is shorter than its digest")
	}
	h := s.Hash()
	want := derive(tagDouble, h[:], proof[32:])
	if subtle.ConstantTimeCompare(want[:], proof[:32]) != 1 {
		return errors.New("the proof's digest does not match the statement")
	}
	return nil
}

// DoubleProof makes a proof the double accepts for the statement (the lab
// tool's and the tests' maker): the digest then the payload.
func DoubleProof(s Statement, payload []byte) []byte {
	h := s.Hash()
	d := derive(tagDouble, h[:], payload)
	return append(d[:], payload...)
}

// MinSecurityBits is the least conjectured security level a proof may
// carry to be accepted by the real verifier: the pinned release's
// measured setting (docs/money/STAGE1-JOIN-2026-10-18.md; the ADR's 128
// is not reachable in Miden VM 0.36.1's quadratic extension, the page
// says why), so a proof made under a weaker setting is refused.
const MinSecurityBits = 108
