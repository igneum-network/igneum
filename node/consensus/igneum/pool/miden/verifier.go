// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).

//go:build money_miden

// Package miden is the shielded pool's real proof verifier: the pinned
// Miden VM 0.36.1 verifier (tools/money/stage1-prover/verifier-ffi, a C-ABI
// static library over miden-verifier) linked into the node by cgo and
// called in-process from Finalize through pool.Verifier. The choice over
// a sidecar process: no process to die or lag between a block and its
// verdict, one binary to release, and the verification's cost measured
// as the node pays it (the lab's figure, docs/money/STAGE1-JOIN-2026-10-18.md).
// The build needs the library at lib/libmoney_verifier.a and the tag
// money_miden; without the tag the package's New reports the verifier
// absent (stub.go) and the node keeps pool.NoVerifier.
package miden

/*
#cgo LDFLAGS: -L${SRCDIR}/lib -lmoney_verifier -lm -ldl -lpthread
#include <stdint.h>
#include <stddef.h>
int32_t money_program_hash(uint32_t kind, uint64_t* out);
int32_t money_verify(uint32_t kind, const uint8_t* proof, size_t proof_len, const uint64_t* sh, uint32_t min_bits, uint32_t* bits_out);
uint32_t money_verifier_version(void);
*/
import "C"

import (
	"errors"
	"fmt"
	"sync/atomic"
	"unsafe"

	"github.com/ethereum/go-ethereum/consensus/igneum/pool"
)

// Verifier implements pool.Verifier over the library.
type Verifier struct {
	minBits uint32
	// bits is the conjectured security level the last verified proof
	// carried (the library's reading of the proof's parameters).
	bits atomic.Uint32
	// verified counts the proofs accepted.
	verified atomic.Uint64
}

// New links the library's two programs against the pinned hashes and
// returns the verifier; minBits is the least conjectured the security review proof
// may carry (pool.MinSecurityBits).
func New(minBits uint32) (*Verifier, error) {
	kinds := []struct {
		tag  byte
		want [4]uint64
		name string
	}{{pool.ShieldTag, pinnedShield, "money-shield-v1"}, {pool.SpendTag, pinnedSpend, "money-spend-v1"}}
	for tag := byte(pool.VaultCreateTag); tag <= pool.VaultRecCancelTag; tag++ {
		kinds = append(kinds, struct {
			tag  byte
			want [4]uint64
			name string
		}{tag, pinnedVault[tag-pool.VaultCreateTag], pool.VaultName(tag)})
	}
	for _, kind := range kinds {
		var got [4]uint64
		if rc := C.money_program_hash(C.uint32_t(kind.tag), (*C.uint64_t)(unsafe.Pointer(&got[0]))); rc != 0 {
			return nil, fmt.Errorf("the verifier library has no program of kind %d (code %d)", kind.tag, rc)
		}
		if got != kind.want {
			return nil, fmt.Errorf("the verifier library's %s is not the pinned program: %x, pinned %x", kind.name, got, kind.want)
		}
	}
	return &Verifier{minBits: minBits}, nil
}

// Name implements pool.Verifier.
func (v *Verifier) Name() string {
	return fmt.Sprintf("miden-vm-%d-blake3-min%dbits", uint32(C.money_verifier_version()), v.minBits)
}

// Bits is the conjectured security level of the last proof verified (0
// before the first).
func (v *Verifier) Bits() uint32 { return v.bits.Load() }

// Verified is the count of proofs accepted since start.
func (v *Verifier) Verified() uint64 { return v.verified.Load() }

var codes = map[int32]string{
	1: "the proof bytes do not parse",
	2: "the proof does not verify",
	3: "the proof's commitment hash is not BLAKE3",
	4: "no program of that kind",
	5: "the proof's conjectured security is below the minimum",
	6: "the statement hash is not a word",
}

// Verify implements pool.Verifier.
func (v *Verifier) Verify(s pool.Statement, proof []byte) error {
	if len(proof) == 0 {
		return errors.New("no proof")
	}
	if s.Kind != pool.ShieldTag && s.Kind != pool.SpendTag && !pool.IsVaultTag(s.Kind) {
		return errors.New("no program of that kind")
	}
	sh := s.Word()
	var bits C.uint32_t
	rc := int32(C.money_verify(C.uint32_t(s.Kind), (*C.uint8_t)(unsafe.Pointer(&proof[0])), C.size_t(len(proof)), (*C.uint64_t)(unsafe.Pointer(&sh[0])), C.uint32_t(v.minBits), &bits))
	if bits != 0 {
		v.bits.Store(uint32(bits))
	}
	if rc != 0 {
		if why, ok := codes[rc]; ok {
			return errors.New(why)
		}
		return fmt.Errorf("the verifier returned %d", rc)
	}
	v.verified.Add(1)
	return nil
}
