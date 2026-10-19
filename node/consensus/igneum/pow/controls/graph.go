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

//go:build controls && cgo

package controls

/*
#cgo CFLAGS: -I${SRCDIR}/../../../../../pow/ref/include
#cgo LDFLAGS: -L${SRCDIR}/../../../../../build/ref -lignpow -lrvn_ethash -lblake3 -lstdc++ -lm
#include <stdint.h>
#include <stddef.h>
#include "ignpow.h"
// The Graph coordinate's exported calls (pow/ref/src/cuckaroo29.cpp, the
// cgo path of GRAPH-REFERENCE.md section 2).
int igneum_graph_verify_ticket(uint64_t chain_id, uint64_t height, const uint8_t seed[32], uint64_t nonce, const uint32_t edges[42], uint8_t ticket_hash_out[32]);
int igneum_graph_solve(uint64_t chain_id, uint64_t height, const uint8_t seed[32], uint64_t nonce, uint32_t* tickets, size_t max_tickets, size_t* n_out);
*/
import "C"

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"unsafe"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/crypto"
)

// GraphName is VectorWork's Graph coordinate: Cuckaroo29 under Igneum's
// graph key and ticket hash (docs/igneumhash/vectorwork/GRAPH-REFERENCE.md).
const GraphName = "igneum-graph"

const (
	graphEdges       = 42
	graphTicketBytes = 4 * graphEdges
	graphMaxTickets  = 8
)

var errGraphHashForm = errors.New("igneum-graph: the node seals and verifies through the ticket (pow.Ticketed), not the hash form")

// Graph is the Graph coordinate on the node: no dataset, no cache, no
// period; the document {"chain_id": N} alone. The seal is a 168-byte
// ticket (42 edge indices of one 42-cycle, ascending, LE32) found under
// the seal hash, the height and the nonce, carried in the header outside
// the seal hash, checked by the reference's exported verifier; the mix
// digest is the ticket hash. The hash form of the base interface (the ABI's
// hash for the vectors tool) is refused here: the node's path is the
// ticket's.
type Graph struct {
	chainID uint64
	doc     graphDoc
}

type graphDoc struct {
	ChainID uint64 `json:"chain_id"`
}

func init() {
	pow.RegisterDevOnly(GraphName, func(params []byte) (pow.Algorithm, error) {
		var doc graphDoc
		if len(params) != 0 && string(params) != "null" {
			dec := json.NewDecoder(bytes.NewReader(params))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&doc); err != nil {
				return nil, fmt.Errorf("igneum-graph: the document is {\"chain_id\": N} and nothing else: %v", err)
			}
		}
		if !inBuild(GraphName) {
			return nil, fmt.Errorf("%w: %s (build string: %s)", errNotInBuild, GraphName, Build())
		}
		return &Graph{chainID: doc.ChainID, doc: doc}, nil
	})
}

func (g *Graph) Name() string                       { return GraphName }
func (g *Graph) Epoch(height uint64) uint64         { return 0 }
func (g *Graph) SeedHash(height uint64) common.Hash { return common.Hash{} }
func (g *Graph) Vectors() []pow.Vector              { return nil }

// Compute is the hash form, refused: a zero pair, which the generic search
// reads as a refusal and makes no attempt on.
func (g *Graph) Compute(headerHash common.Hash, height uint64, nonce uint64) (common.Hash, common.Hash) {
	return common.Hash{}, common.Hash{}
}

// Verify is the hash form, refused: a block of this coordinate carries a
// ticket and is checked through VerifyTicket.
func (g *Graph) Verify(headerHash common.Hash, height uint64, nonce uint64, mixDigest common.Hash, difficulty *big.Int, fullDataset bool) error {
	return errGraphHashForm
}

// TicketSize is 168 bytes.
func (g *Graph) TicketSize() int { return graphTicketBytes }

// VerifyTicket runs the exported verifier: an edge at or above 2^29, an
// unsorted or duplicated list (IGNPOW_E_ARG) and a list that is not one
// 42-cycle under the key (IGNPOW_E_MIX) are refused; otherwise the ticket
// hash.
func (g *Graph) VerifyTicket(headerHash common.Hash, height uint64, nonce uint64, ticket []byte) (common.Hash, error) {
	if len(ticket) != graphTicketBytes {
		return common.Hash{}, fmt.Errorf("igneum-graph: ticket of %d bytes, want %d", len(ticket), graphTicketBytes)
	}
	var edges [graphEdges]C.uint32_t
	for i := range edges {
		edges[i] = C.uint32_t(binary.LittleEndian.Uint32(ticket[4*i:]))
	}
	var out common.Hash
	st := C.igneum_graph_verify_ticket(C.uint64_t(g.chainID), C.uint64_t(height), (*C.uint8_t)(unsafe.Pointer(&headerHash[0])), C.uint64_t(nonce), &edges[0], (*C.uint8_t)(unsafe.Pointer(&out[0])))
	if st != C.IGNPOW_OK {
		return common.Hash{}, fmt.Errorf("igneum-graph: ticket refused: %s", statusName(int(st)))
	}
	return out, nil
}

// SolveTicket runs the reference solver over the attempt's graph (about a
// second per 2^29 graph on 16 threads; a 42-cycle in about one graph in 45)
// and returns every ticket found in canonical bytes.
func (g *Graph) SolveTicket(headerHash common.Hash, height uint64, nonce uint64) ([][]byte, error) {
	var (
		buf [graphMaxTickets * graphEdges]C.uint32_t
		n   C.size_t
	)
	st := C.igneum_graph_solve(C.uint64_t(g.chainID), C.uint64_t(height), (*C.uint8_t)(unsafe.Pointer(&headerHash[0])), C.uint64_t(nonce), &buf[0], C.size_t(graphMaxTickets), &n)
	if st != C.IGNPOW_OK {
		return nil, fmt.Errorf("igneum-graph: the solver: %s", statusName(int(st)))
	}
	count := int(n)
	if count > graphMaxTickets {
		count = graphMaxTickets
	}
	tickets := make([][]byte, 0, count)
	for s := 0; s < count; s++ {
		t := make([]byte, graphTicketBytes)
		for i := 0; i < graphEdges; i++ {
			binary.LittleEndian.PutUint32(t[4*i:], uint32(buf[s*graphEdges+i]))
		}
		tickets = append(tickets, t)
	}
	return tickets, nil
}

// WorkParams is the document a template carries for the miner.
func (g *Graph) WorkParams() any { return g.doc }

// ParamsDigest is the keccak of the canonical document bytes.
func (g *Graph) ParamsDigest() common.Hash {
	b, _ := json.Marshal(g.doc)
	return crypto.Keccak256Hash(b)
}
