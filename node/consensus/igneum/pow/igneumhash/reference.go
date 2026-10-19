// Copyright 2026 The Igneum Authors
// This file is part of the Igneum node.
//
// The Igneum node is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The Igneum node is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the Igneum node. If not, see <http://www.gnu.org/licenses/>.

package igneumhash

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
)

// Version8 is the blended relation (SPEC-R0A-v0.md section 0.8, revision 2
// "IgneumHash"): the RXE1 block schedule in the round with the FP feature bits.
// The node's own WORK does not carry it; a version 8 document is served by
// the hash team's C reference through the lab build (package controls,
// built with the "controls" tag against the built reference), which
// registers a ReferenceFactory here. The public build refuses such a
// document with ErrReferenceBuild. Development chains only (the transition
// proof of the gate's form on the second development chain, 11 October
// 2026); nothing here selects a launch algorithm.
const Version8 uint32 = 8

// ReferenceFactory builds the algorithm for a document the Go WORK does not
// carry, from the chain id and the document as given.
type ReferenceFactory func(chainID uint64, document []byte) (pow.Algorithm, error)

var (
	refMu      sync.Mutex
	refFactory ReferenceFactory

	// ErrReferenceBuild is the public build's answer to a reference-only
	// document.
	ErrReferenceBuild = errors.New("igneumhash: this document's version is carried only by the lab build (the controls tag against the built C reference)")
)

// RegisterReference names the factory for reference-only documents (the
// controls package at init).
func RegisterReference(f ReferenceFactory) {
	refMu.Lock()
	defer refMu.Unlock()
	refFactory = f
}

// ReferenceAvailable reports whether this build carries the reference.
func ReferenceAvailable() bool {
	refMu.Lock()
	defer refMu.Unlock()
	return refFactory != nil
}

// referenceOnly reports whether a document names a version only the
// reference carries, with its chain id (the document's, or the development
// chain's when absent, as the reference defaults it).
func referenceOnly(params []byte) (bool, uint64) {
	var d struct {
		Version *uint32 `json:"version"`
		ChainID *uint64 `json:"chain_id"`
	}
	trimmed := bytes.TrimSpace(params)
	if len(trimmed) == 0 {
		return false, 0
	}
	if err := json.Unmarshal(trimmed, &d); err != nil || d.Version == nil {
		return false, 0
	}
	// Version 8 (the blended relation) and every version above it (9 and
	// 10, the per-period programme; 11, the hash team's memory read) are
	// the C reference's alone; the Go port knows up to version 6. A
	// version the linked reference does not carry is refused by the
	// reference's own parser.
	if *d.Version < Version8 {
		return false, 0
	}
	chainID := uint64(7310)
	if d.ChainID != nil {
		chainID = *d.ChainID
	}
	return true, chainID
}

// newReference builds the reference's algorithm for a reference-only
// document, or refuses it on a build without the reference.
func newReference(chainID uint64, params []byte) (pow.Algorithm, error) {
	refMu.Lock()
	f := refFactory
	refMu.Unlock()
	if f == nil {
		return nil, ErrReferenceBuild
	}
	a, err := f(chainID, params)
	if err != nil {
		return nil, fmt.Errorf("igneumhash reference: %w", err)
	}
	return a, nil
}
