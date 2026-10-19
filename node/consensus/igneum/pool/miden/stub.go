// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).

//go:build !money_miden

package miden

import (
	"errors"

	"github.com/ethereum/go-ethereum/consensus/igneum/pool"
)

// Verifier is absent in a build without the money_miden tag; New never
// returns one, so the methods are never reached.
type Verifier struct{}

// New reports the verifier absent: the node keeps pool.NoVerifier and
// refuses every shield and spend where the gate is on.
func New(minBits uint32) (*Verifier, error) {
	return nil, errors.New("built without the Miden verifier (go build -tags money_miden with lib/libmoney_verifier.a)")
}

// Name implements pool.Verifier.
func (*Verifier) Name() string { return "absent" }

// Verify implements pool.Verifier.
func (*Verifier) Verify(pool.Statement, []byte) error {
	return errors.New("built without the Miden verifier")
}

// Bits is zero without the library.
func (*Verifier) Bits() uint32 { return 0 }

// Verified is zero without the library.
func (*Verifier) Verified() uint64 { return 0 }
