// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).
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

// Package rxe1 is the independent (second) implementation of the RXE1
// experimental family, written from the text of the founder's engineering
// specification v0.1 alone (docs/igneumhash/rxe1/SPEC-TEXT-v0.1.md sections
// 6 to 8 and 12; the programme's IH 05) and never from the hash team's
// reference (IH 04). Every divergence between the two is resolved as a
// correction to the text through the hash team.
//
// RXE1 is an engineering hypothesis, not an established construction and
// not a patch to R0A consensus; it has its own algorithm id, vectors and
// devnet namespace (section 5). Nothing here activates on a network.
//
// Section numbers in comments are the specification's.
package rxe1

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"
	"sync"

	"lukechampine.com/blake3"
)

// ID is the algorithm id of the family.
const ID = "rxe1"

// Lanes is the fixed count of logical lanes per nonce (section 7).
const Lanes = 16

// ItemBytes is one dataset lookup (section 6): 128 bytes, 32 little-endian u32 words.
const (
	ItemBytes = 128
	ItemWords = ItemBytes / 4
)

// Mode is the schedule mode (section 6): M = 0 (no instruction blocks),
// F = 1 (a fixed public schedule), E = 2 (the schedule from the epoch seed).
type Mode uint32

const (
	ModeM Mode = 0
	ModeF Mode = 1
	ModeE Mode = 2
	ModeP Mode = 3 // a fresh program per nonce (A2/A3 text, section 5; schema 3 only)
	ModeS Mode = 4 // state-selected compiled blocks (A2/A3 text, section 9; schema 4 only)
)

func (m Mode) String() string {
	switch m {
	case ModeM:
		return "M"
	case ModeF:
		return "F"
	case ModeE:
		return "E"
	case ModeP:
		return "P"
	case ModeS:
		return "S"
	}
	return fmt.Sprintf("mode(%d)", uint32(m))
}

// Schema versions of the record. Schema 1 is the text verbatim; schema 2 is
// the project's fix for the security review's finding (opcode 0 is a no-op when src1 =
// src2): its one changed line is the src2 rule of GenerateSchedule, so that
// src2 never equals src1 or d; everything else is schema 1's. The test
// schemas are the test-only forms the hash team adopted (REFERENCE-NOTES.md):
// a small-dataset record carries one in its bytes, so its pid, seed and out
// never equal a production record's; the production validator refuses them.
const (
	Schema      uint32 = 1
	Schema2     uint32 = 2
	Schema3     uint32 = 3          // the additive cells A2 and A3 (SPEC-TEXT-v0.2-A2A3.md): GROUP, ROUTE, CHUNKS, PAYLOAD appended, 88 bytes
	TestSchema  uint32 = 0x54455354 // "TEST": schema 1 semantics, test-only
	TestSchema2 uint32 = 0x54455355 // schema 2 semantics, test-only
	TestSchema3 uint32 = 0x54455356 // schema 3 semantics, test-only
	Schema4     uint32 = 4          // mode S (A2/A3 text, section 9): BLOCK_LEN and FAMILY appended, 96 bytes
	TestSchema4 uint32 = 0x54455357 // schema 4 semantics, test-only
	Schema5     uint32 = 5          // the memory-free execution lane (A2/A3 text, section 11): schema 3's record with no dataset
	TestSchema5 uint32 = 0x54455358 // schema 5 semantics, test-only
)

// semantics is 1, 2 or 3 for a schema value, 0 for an unknown one.
func semantics(schema uint32) uint32 {
	switch schema {
	case Schema, TestSchema:
		return 1
	case Schema2, TestSchema2:
		return 2
	case Schema3, TestSchema3:
		return 3
	case Schema4, TestSchema4:
		return 4
	case Schema5, TestSchema5:
		return 5
	}
	return 0
}

// Semantics is the semantics of the record (1, 2 or 3). Schema 3 keeps
// schema 2's src2 rule and adds the group, route, chunk and payload levers.
func (p Params) Semantics() uint32 { return semantics(p.Schema) }

// IsTestSchema reports a test-only schema value.
func (p Params) IsTestSchema() bool {
	return p.Schema == TestSchema || p.Schema == TestSchema2 || p.Schema == TestSchema3 || p.Schema == TestSchema4 || p.Schema == TestSchema5
}

// MemoryFree reports schema 5: no dataset, the chunk operand a register read (section 11).
func (p Params) MemoryFree() bool {
	return p.Semantics() == 5
}

// The routes of schema 3 (A2, section 3).
const (
	RouteLocal uint32 = 0 // no exchange
	RouteFixed uint32 = 1 // the v0.1 exchange
	RouteEpoch uint32 = 2 // per-round, per-register descriptors from the epoch's routing seed
	RouteState uint32 = 3 // each lane's partner from its own live state
)

// G is the group (logical lanes per nonce): the record's GROUP under schema
// 3, 16 otherwise (section 7 of v0.1).
func (p Params) G() uint32 {
	if p.Semantics() >= 3 {
		return p.Group
	}
	return Lanes
}

// C, PayloadBytes and ChunkBytes are the memory step's shape: the record's
// under schema 3, the v0.1 point (one 128-byte item) otherwise.
func (p Params) C() uint32 {
	if p.Semantics() >= 3 {
		return p.Chunks
	}
	return 1
}

func (p Params) PayloadBytes() uint32 {
	if p.Semantics() >= 3 {
		return p.Payload
	}
	return ItemBytes
}

func (p Params) ChunkBytes() uint32 { return p.PayloadBytes() / p.C() }

// RouteOf is the exchange route: the record's under schema 3, the fixed exchange otherwise.
func (p Params) RouteOf() uint32 {
	if p.Semantics() >= 3 {
		return p.Route
	}
	return RouteFixed
}

// DatasetSpecDigest is the constant of the dataset binding (REFERENCE-NOTES.md
// section 2, dependencies.lock.json): BLAKE3 of the ASCII
// "IgneumHash-R0A/v1/dataset-builder/SPEC-R0A-v0.md/revision-0.6/section-5.3".
// A record's first field must equal it.
var DatasetSpecDigest = blake3.Sum256([]byte("IgneumHash-R0A/v1/dataset-builder/SPEC-R0A-v0.md/revision-0.6/section-5.3"))

// ProductionCacheBytes is the pinned dependency's cache (section 7, the notes' section 2).
const ProductionCacheBytes = 32 << 20

// Harness limits on the exact inputs (section 6).
const (
	MinChainIDBytes  = 1
	MaxChainIDBytes  = 64
	MaxHeaderBytes   = 4096
	EpochSeedBytes   = 32
	MinDatasetBytes  = 2 << 30 // the permitted sizes are 2 and 4 GiB
	MaxDatasetBytes  = 4 << 30
	MinTestDatasetBy = ItemBytes * 16 // this side's floor for the bounded campaign (the reference's harness floor is 16 MiB)
)

// Params is the canonical parameter record (section 6), in the record's
// order: baseline dataset-spec digest, dataset size, parent count, cache
// size, round count R, register count K, instruction-block count B, schedule
// mode and schema version. Numeric fields are u32 except the two byte sizes,
// which are u64; the digest is 32 bytes.
type Params struct {
	DatasetSpecDigest [32]byte `json:"datasetSpecDigest"` // the pinned R0A dataset-spec binding (IH 01, IH 04)
	DatasetBytes      uint64   `json:"datasetBytes"`
	Parents           uint32   `json:"parents"`
	CacheBytes        uint64   `json:"cacheBytes"`
	R                 uint32   `json:"rounds"`
	K                 uint32   `json:"registers"`
	B                 uint32   `json:"blocks"`
	Mode              Mode     `json:"mode"`
	Schema            uint32   `json:"schema"`
	// schema 3 (SPEC-TEXT-v0.2-A2A3.md section 1), in the record's order after schema
	Group   uint32 `json:"group,omitempty"`         // G: 8, 16, 32
	Route   uint32 `json:"route,omitempty"`         // 0 local, 1 fixed, 2 epoch, 3 state-dependent
	Chunks  uint32 `json:"chunks,omitempty"`        // C: 1, 2, 4
	Payload uint32 `json:"payload_bytes,omitempty"` // 128, 256
	// schema 4 (section 9.1), after payload
	BlockLen uint32 `json:"block_len,omitempty"` // L: operations per compiled block (8, 16, 32)
	Family   uint32 `json:"family,omitempty"`    // NB: blocks in the precompiled family (16, 32, 64)
}

// Record is the parameter record: the fields in order, each at its width,
// little-endian (section 6): 72 bytes; 88 under schema 3 with GROUP,
// ROUTE, CHUNKS and PAYLOAD appended.
func (p Params) Record() []byte {
	out := make([]byte, 0, 72)
	out = append(out, p.DatasetSpecDigest[:]...)
	out = binary.LittleEndian.AppendUint64(out, p.DatasetBytes)
	out = binary.LittleEndian.AppendUint32(out, p.Parents)
	out = binary.LittleEndian.AppendUint64(out, p.CacheBytes)
	out = binary.LittleEndian.AppendUint32(out, p.R)
	out = binary.LittleEndian.AppendUint32(out, p.K)
	out = binary.LittleEndian.AppendUint32(out, p.B)
	out = binary.LittleEndian.AppendUint32(out, uint32(p.Mode))
	out = binary.LittleEndian.AppendUint32(out, p.Schema)
	if p.Semantics() >= 3 {
		out = binary.LittleEndian.AppendUint32(out, p.Group)
		out = binary.LittleEndian.AppendUint32(out, p.Route)
		out = binary.LittleEndian.AppendUint32(out, p.Chunks)
		out = binary.LittleEndian.AppendUint32(out, p.Payload)
	}
	if p.Semantics() == 4 {
		out = binary.LittleEndian.AppendUint32(out, p.BlockLen)
		out = binary.LittleEndian.AppendUint32(out, p.Family)
	}
	return out
}

// PID is pid = H("RXE1/params", [parameter_record]).
func (p Params) PID() [32]byte { return H("RXE1/params", p.Record()) }

// N is the dataset item count, dataset_bytes / 128 (a power of two).
func (p Params) N() uint64 { return p.DatasetBytes / ItemBytes }

// Variant is the reference's tag <m|f|e>-b<B>-r<R>-k<K>-d<MiB>-p<parents>-c<cache MiB>,
// prefixed "test-" for a small (test-only) dataset.
func (p Params) Variant() string {
	tag := ""
	if p.Semantics() == 2 {
		tag = "s2-"
	}
	if p.IsTestSchema() || p.DatasetBytes < MinDatasetBytes || p.CacheBytes != ProductionCacheBytes {
		tag += "test-"
	}
	m := map[Mode]string{ModeM: "m", ModeF: "f", ModeE: "e", ModeP: "p", ModeS: "s"}[p.Mode]
	if p.Semantics() == 5 {
		t := ""
		if p.IsTestSchema() {
			t = "test-"
		}
		return fmt.Sprintf("s5-%s%s-b%d-r%d-k%d-g%d-u%d-x%d-y%d", t, m, p.B, p.R, p.K, p.Group, p.Route, p.Chunks, p.Payload)
	}
	if p.Semantics() == 4 {
		return fmt.Sprintf("s4-%s%s-b%d-r%d-k%d-g%d-u%d-x%d-y%d-l%d-n%d-d%d-p%d-c%d", tag, m, p.B, p.R, p.K, p.Group, p.Route, p.Chunks, p.Payload, p.BlockLen, p.Family, p.DatasetBytes>>20, p.Parents, p.CacheBytes>>20)
	}
	if p.Semantics() == 3 {
		return fmt.Sprintf("s3-%s%s-b%d-r%d-k%d-g%d-u%d-x%d-y%d-d%d-p%d-c%d", tag, m, p.B, p.R, p.K, p.Group, p.Route, p.Chunks, p.Payload, p.DatasetBytes>>20, p.Parents, p.CacheBytes>>20)
	}
	return fmt.Sprintf("%s%s-b%d-r%d-k%d-d%d-p%d-c%d", tag, m, p.B, p.R, p.K, p.DatasetBytes>>20, p.Parents, p.CacheBytes>>20)
}

var (
	ErrParams = errors.New("rxe1: parameter record rejected")
	ErrInput  = errors.New("rxe1: input rejected")
)

func isPow2(x uint64) bool { return x != 0 && x&(x-1) == 0 }

// validateShape checks everything but the sizes.
func (p Params) validateShape() error {
	switch {
	case p.Semantics() == 0:
		return fmt.Errorf("%w: schema %d (1, 2 or a test schema)", ErrParams, p.Schema)
	case p.MemoryFree() && (p.DatasetSpecDigest != [32]byte{} || p.DatasetBytes != 0 || p.Parents != 0 || p.CacheBytes != 0):
		return fmt.Errorf("%w: schema 5 binds no dataset: the digest field, dataset_bytes, parents and cache_bytes are zero", ErrParams)
	case p.MemoryFree() && p.Mode != ModeF && p.Mode != ModeE && p.Mode != ModeP:
		return fmt.Errorf("%w: schema 5 admits modes F, E and P only", ErrParams)
	case !p.MemoryFree() && p.DatasetSpecDigest != DatasetSpecDigest:
		return fmt.Errorf("%w: dataset_spec_digest is not the binding's constant", ErrParams)
	case p.R != 32 && p.R != 64:
		return fmt.Errorf("%w: R %d (32 or 64)", ErrParams, p.R)
	case p.K != 8 && p.K != 16 && !(p.K == 4 && p.Semantics() >= 3):
		return fmt.Errorf("%w: K %d (8 or 16; 4 under schema 3)", ErrParams, p.K)
	case p.R < p.K:
		return fmt.Errorf("%w: R %d below K %d", ErrParams, p.R, p.K)
	case !p.MemoryFree() && p.Parents != 256 && p.Parents != 512:
		return fmt.Errorf("%w: parents %d (256 or 512)", ErrParams, p.Parents)
	case p.Mode == ModeM && p.B != 0:
		return fmt.Errorf("%w: mode M requires B = 0, got %d", ErrParams, p.B)
	case p.Mode != ModeM && p.B != 1 && p.B != 2 && p.B != 4:
		return fmt.Errorf("%w: mode %s requires B of 1, 2 or 4, got %d", ErrParams, p.Mode, p.B)
	case p.Mode > ModeS:
		return fmt.Errorf("%w: mode %d", ErrParams, uint32(p.Mode))
	case p.Mode == ModeP && p.Semantics() != 3 && p.Semantics() != 5:
		return fmt.Errorf("%w: mode P is a mode of schemas 3 and 5 only (sections 5 and 11)", ErrParams)
	case p.Mode == ModeS && p.Semantics() != 4:
		return fmt.Errorf("%w: mode S is a schema 4 mode only (section 9)", ErrParams)
	case p.Semantics() == 4 && p.Mode != ModeS:
		return fmt.Errorf("%w: schema 4 is valid with mode S only (section 9.1)", ErrParams)
	case !p.MemoryFree() && (!isPow2(p.DatasetBytes) || p.DatasetBytes%ItemBytes != 0):
		return fmt.Errorf("%w: dataset %d bytes is not a power of two of items", ErrParams, p.DatasetBytes)
	case !p.MemoryFree() && (!isPow2(p.CacheBytes) || p.CacheBytes%ItemBytes != 0 || p.CacheBytes > p.DatasetBytes):
		return fmt.Errorf("%w: cache %d bytes", ErrParams, p.CacheBytes)
	}
	if p.Semantics() >= 3 {
		switch {
		case p.Group != 8 && p.Group != 16 && p.Group != 32:
			return fmt.Errorf("%w: group %d (8, 16 or 32)", ErrParams, p.Group)
		case (p.Group*p.K)%8 != 0:
			return fmt.Errorf("%w: G * K = %d is not a multiple of 8", ErrParams, p.Group*p.K)
		case p.Route > RouteState:
			return fmt.Errorf("%w: route %d (0 to 3)", ErrParams, p.Route)
		case p.Chunks != 1 && p.Chunks != 2 && p.Chunks != 4:
			return fmt.Errorf("%w: chunks %d (1, 2 or 4)", ErrParams, p.Chunks)
		case p.Payload != 128 && p.Payload != 256:
			return fmt.Errorf("%w: payload %d (128 or 256)", ErrParams, p.Payload)
		case p.Payload/p.Chunks != 64 && p.Payload/p.Chunks != 128 && p.Payload/p.Chunks != 256:
			return fmt.Errorf("%w: chunk of %d bytes (64, 128 or 256)", ErrParams, p.Payload/p.Chunks)
		case (p.Payload/4)%p.Group != 0:
			return fmt.Errorf("%w: payload words %d not a multiple of the group %d", ErrParams, p.Payload/4, p.Group)
		}
	} else if p.Group != 0 || p.Route != 0 || p.Chunks != 0 || p.Payload != 0 {
		return fmt.Errorf("%w: group, route, chunks and payload are schema 3 fields", ErrParams)
	}
	if p.Semantics() == 4 {
		ops := p.B * p.K
		switch {
		case p.BlockLen != 8 && p.BlockLen != 16 && p.BlockLen != 32:
			return fmt.Errorf("%w: block_len %d (8, 16 or 32)", ErrParams, p.BlockLen)
		case p.Family != 16 && p.Family != 32 && p.Family != 64:
			return fmt.Errorf("%w: family %d (16, 32 or 64)", ErrParams, p.Family)
		case p.BlockLen%ops != 0:
			return fmt.Errorf("%w: block_len %d is not a multiple of B * K = %d", ErrParams, p.BlockLen, ops)
		case (p.R*ops)%p.BlockLen != 0:
			return fmt.Errorf("%w: R * B * K = %d is not a multiple of block_len %d", ErrParams, p.R*ops, p.BlockLen)
		}
	} else if p.BlockLen != 0 || p.Family != 0 {
		return fmt.Errorf("%w: block_len and family are schema 4 fields", ErrParams)
	}
	return nil
}

// Validate accepts a production record only: dataset 2 or 4 GiB, the
// pinned 32 MiB cache, R 32 or 64, K 8 or 16, parents 256 or 512, schema 1,
// the mode's B (section 7; the notes' section 2). A small record is refused
// here, so the bounded campaign's datasets cannot be a production record;
// the node adapter refuses them likewise (section 12).
func (p Params) Validate() error {
	if p.IsTestSchema() {
		return fmt.Errorf("%w: schema %#x is a test-only schema", ErrParams, p.Schema)
	}
	if p.MemoryFree() {
		return p.validateShape()
	}
	if p.DatasetBytes != MinDatasetBytes && p.DatasetBytes != MaxDatasetBytes {
		return fmt.Errorf("%w: dataset %d bytes (2 or 4 GiB)", ErrParams, p.DatasetBytes)
	}
	if p.CacheBytes != ProductionCacheBytes {
		return fmt.Errorf("%w: cache %d bytes (the pinned dependency's 32 MiB)", ErrParams, p.CacheBytes)
	}
	return p.validateShape()
}

// ValidateTestOnly accepts a record of the bounded small-dataset campaign
// (section 12): a test schema in the record, or the reference's test_only
// document form on a production schema; the same shape with any
// power-of-two dataset of at least 16 items and any power-of-two cache not
// above it.
func (p Params) ValidateTestOnly() error {
	if p.MemoryFree() {
		return p.validateShape()
	}
	if p.DatasetBytes < MinTestDatasetBy {
		return fmt.Errorf("%w: test dataset %d bytes too small", ErrParams, p.DatasetBytes)
	}
	return p.validateShape()
}

// ---------------------------------------------------------------------------
// Section 6: the encoding and the hashes.

// LP is the 32-bit little-endian byte length of x followed by x.
func LP(x []byte) []byte {
	out := make([]byte, 4, 4+len(x))
	binary.LittleEndian.PutUint32(out, uint32(len(x)))
	return append(out, x...)
}

// Enc is LP(ASCII tag) followed by LP(field) for each field in order; an
// integer field is first encoded at its width (little-endian) by the caller.
func Enc(tag string, fields ...[]byte) []byte {
	out := LP([]byte(tag))
	for _, f := range fields {
		out = append(out, LP(f)...)
	}
	return out
}

// H is standard unkeyed BLAKE3 over Enc, 32 bytes.
func H(tag string, fields ...[]byte) [32]byte {
	return blake3.Sum256(Enc(tag, fields...))
}

// X is the first n bytes of BLAKE3's extendable output over Enc.
func X(tag string, n int, fields ...[]byte) []byte {
	h := blake3.New(32, nil)
	h.Write(Enc(tag, fields...))
	out := make([]byte, n)
	if _, err := h.XOF().Read(out); err != nil {
		panic("rxe1: blake3 xof: " + err.Error())
	}
	return out
}

func le32(v uint32) []byte { return binary.LittleEndian.AppendUint32(nil, v) }
func le64(v uint64) []byte { return binary.LittleEndian.AppendUint64(nil, v) }

// Input is one nonce's exact inputs (section 6).
type Input struct {
	ChainID     []byte   // 1 to 64 bytes
	HeaderBlob  []byte   // 0 to 4096 bytes: the header preimage excluding the solution
	EpochSeed   [32]byte // exactly 32 bytes
	EpochNumber uint64
	Nonce       uint64
}

// Validate applies the harness limits before any hashing.
func (in Input) Validate() error {
	switch {
	case len(in.ChainID) < MinChainIDBytes || len(in.ChainID) > MaxChainIDBytes:
		return fmt.Errorf("%w: chain_id %d bytes", ErrInput, len(in.ChainID))
	case len(in.HeaderBlob) > MaxHeaderBytes:
		return fmt.Errorf("%w: header_blob %d bytes", ErrInput, len(in.HeaderBlob))
	}
	return nil
}

// Seed is seed = H("RXE1/nonce", [chain_id, pid, epoch_number, epoch_seed, header_blob, nonce]).
func Seed(in Input, pid [32]byte) [32]byte {
	return H("RXE1/nonce", in.ChainID, pid[:], le64(in.EpochNumber), in.EpochSeed[:], in.HeaderBlob, le64(in.Nonce))
}

// State is the 16 lanes of K registers.
type State [][]uint32

func newState(g, k uint32) State {
	s := make(State, g)
	for l := range s {
		s[l] = make([]uint32, k)
	}
	return s
}

func (s State) clone() State {
	t := make(State, len(s))
	for l := range s {
		t[l] = append([]uint32(nil), s[l]...)
	}
	return t
}

// bytesLaneMajor is LE32_lane_major(S): every word, lane first, register second.
func (s State) bytesLaneMajor() []byte {
	out := make([]byte, 0, 4*len(s)*len(s[0]))
	for l := range s {
		for _, w := range s[l] {
			out = binary.LittleEndian.AppendUint32(out, w)
		}
	}
	return out
}

// InitialState is G*K words from X("RXE1/state", [seed], 4*G*K), lane
// first, register second (v0.1: 16*K words from 64*K bytes; at a group other
// than 16 the same rule with G lanes, reading R-1 of DIFF-RXE1.md).
func InitialState(seed [32]byte, g, k uint32) State {
	raw := X("RXE1/state", int(4*g*k), seed[:])
	s := newState(g, k)
	for l := uint32(0); l < g; l++ {
		for r := uint32(0); r < k; r++ {
			s[l][r] = binary.LittleEndian.Uint32(raw[4*(l*k+r):])
		}
	}
	return s
}

// ---------------------------------------------------------------------------
// Section 8: the instruction schedule.

// Op is one descriptor: destination d, sources, rotate and opcode.
type Op struct {
	Dst, Src1, Src2 uint32
	Rotate          uint32 // 1 to 31
	Opcode          uint32 // 0 to 3
}

// Schedule is the public schedule of a (mode, epoch): [round][block][destination].
type Schedule [][][]Op

// ProgramSeed is H("RXE1/fixed", [pid]) for F, H("RXE1/epoch", [chain_id,
// pid, epoch_number, epoch_seed]) for E and, for P, the nonce's
// NonceProgramSeed; M and S have none (S selects from a family, FamilySeed).
func ProgramSeed(p Params, in Input, pid [32]byte) ([32]byte, bool) {
	switch p.Mode {
	case ModeF:
		return H("RXE1/fixed", pid[:]), true
	case ModeE:
		return H("RXE1/epoch", in.ChainID, pid[:], le64(in.EpochNumber), in.EpochSeed[:]), true
	case ModeP:
		return NonceProgramSeed(Seed(in, pid)), true
	}
	return [32]byte{}, false
}

// NonceProgramSeed is mode P's program_seed = H("RXE1/nonce-program", [seed])
// (A2/A3 text, section 5): one program per nonce, from the work seed alone.
func NonceProgramSeed(seed [32]byte) [32]byte { return H("RXE1/nonce-program", seed[:]) }

// Family is mode S's precompiled family: NB straight-line blocks of L
// descriptors (A2/A3 text, section 9.3), a function of the record alone.
type Family [][]Op

// FamilySeed is family_seed = H("RXE1/family", [pid]).
func FamilySeed(pid [32]byte) [32]byte { return H("RXE1/family", pid[:]) }

// GenerateFamily derives block b, operation i as q = X("RXE1/family-op",
// [family_seed, LE32(b), LE32(i)], 4): d = i mod K, opcode = (i + b) mod 4,
// src1 = (d + 1 + q[0] mod (K-1)) mod K, src2 by schema 2's draw, rotate =
// 1 + q[2] mod 31; q[3] unused. Every operation is valid by construction.
func GenerateFamily(p Params, familySeed [32]byte) Family {
	k := p.K
	fam := make(Family, p.Family)
	for b := uint32(0); b < p.Family; b++ {
		fam[b] = make([]Op, p.BlockLen)
		for i := uint32(0); i < p.BlockLen; i++ {
			q := X("RXE1/family-op", 4, familySeed[:], le32(b), le32(i))
			d := i % k
			src1 := (d + 1 + uint32(q[0])%(k-1)) % k
			r1 := (src1 + k - d - 1) % k
			t := uint32(q[1]) % (k - 2)
			if t >= r1 {
				t++
			}
			fam[b][i] = Op{Dst: d, Src1: src1, Src2: (d + 1 + t) % k, Rotate: 1 + uint32(q[2])%31, Opcode: (i + b) % 4}
		}
	}
	return fam
}

// families caches one family per record (pid): generated once per record,
// as section 9.5 names the work.
var families sync.Map

func familyFor(p Params, pid [32]byte) Family {
	if f, ok := families.Load(pid); ok {
		return f.(Family)
	}
	f := GenerateFamily(p, FamilySeed(pid))
	families.Store(pid, f)
	return f
}

// SelectBlock is section 9.4's selection for execution e from the live
// state: the address fold over register e mod K of every lane with e in
// place of the round number, masked by NB - 1. It returns h and sel.
func SelectBlock(s State, e, k, nb uint32) (h, sel uint32) {
	h = e * 0x9E3779B9
	for l := range s {
		h ^= rotl32(s[l][e%k], uint32(l))
	}
	h = (h ^ (h >> 16)) * 0x85EBCA6B
	h = (h ^ (h >> 13)) * 0xC2B2AE35
	return h, (h ^ (h >> 16)) & (nb - 1)
}

// executeSequential applies one compiled block to every lane as straight-line
// code: operation i reads the registers as left by operation i - 1 (a true
// dependency chain, not a snapshot; section 9.3).
func executeSequential(s State, block []Op) {
	for l := range s {
		r := s[l]
		for _, op := range block {
			r[op.Dst] = op.Apply(r[op.Dst], r[op.Src1], r[op.Src2])
		}
	}
}

// GenerateSchedule derives every descriptor: for round j, block b and
// destination d in ascending order, q = X("RXE1/op", [program_seed, LE32(j),
// LE32(b), LE32(d)], 4); src1 = (d + 1 + q[0] % (K-1)) % K, src2 = (d + 1 +
// q[1] % (K-1)) % K, rotate = 1 + q[2] % 31, opcode = (d + q[3] % 4) % 4.
// Generation happens outside the nonce hot loop.
func GenerateSchedule(p Params, programSeed [32]byte) Schedule {
	if p.B == 0 {
		return nil
	}
	k := p.K
	sched := make(Schedule, p.R)
	for j := uint32(0); j < p.R; j++ {
		sched[j] = make([][]Op, p.B)
		for b := uint32(0); b < p.B; b++ {
			sched[j][b] = make([]Op, k)
			for d := uint32(0); d < k; d++ {
				q := X("RXE1/op", 4, programSeed[:], le32(j), le32(b), le32(d))
				src1 := (d + 1 + uint32(q[0])%(k-1)) % k
				var src2 uint32
				if p.Semantics() >= 2 {
					// schemas 2 and 3: with r1 = (src1 - d - 1) mod K and t = q[1] mod (K - 2),
					// if t >= r1 then t = t + 1; src2 = (d + 1 + t) mod K
					r1 := (src1 + k - d - 1) % k
					t := uint32(q[1]) % (k - 2)
					if t >= r1 {
						t++
					}
					src2 = (d + 1 + t) % k
				} else {
					src2 = (d + 1 + uint32(q[1])%(k-1)) % k
				}
				sched[j][b][d] = Op{
					Dst:    d,
					Src1:   src1,
					Src2:   src2,
					Rotate: 1 + uint32(q[2])%31,
					Opcode: (d + uint32(q[3])%4) % 4,
				}
			}
		}
	}
	return sched
}

func rotl32(x uint32, n uint32) uint32 { return bits.RotateLeft32(x, int(n&31)) }

// Apply is the opcode table of section 8 on x = T[l][d], y = T[l][src1], z = T[l][src2].
func (op Op) Apply(x, y, z uint32) uint32 {
	switch op.Opcode {
	case 0:
		return x + rotl32(y^z, op.Rotate)
	case 1:
		return x ^ rotl32(y+z, op.Rotate)
	case 2:
		return x + y*(z|1)
	default:
		return rotl32(x^y, op.Rotate) + z
	}
}

// executeBlock applies one block to every lane: each block reads a snapshot
// T of the entire state; writes become visible together at the block end.
func executeBlock(s State, block []Op) {
	t := s.clone()
	for l := range s {
		for _, op := range block {
			s[l][op.Dst] = op.Apply(t[l][op.Dst], t[l][op.Src1], t[l][op.Src2])
		}
	}
}

// RouteOp is one routing descriptor of route 2 (A2, section 3): for round
// j and register k, q = X("RXE1/route-op", [rseed, LE32(j), LE32(k)], 4),
// off = 1 + q[0] mod (G - 1), src = q[1] mod K, rot = 1 + q[2] mod 31, op = q[3] mod 2.
type RouteOp struct {
	Off, Src, Rot, Op uint32
}

// Routing is the epoch's routing descriptors, [round][register].
type Routing [][]RouteOp

// RouteSeed is rseed = H("RXE1/route", [chain_id, pid, LE64(epoch_number), epoch_seed]),
// every mode; ok is false unless the record's route is 2.
func RouteSeed(p Params, in Input, pid [32]byte) ([32]byte, bool) {
	if p.RouteOf() != RouteEpoch {
		return [32]byte{}, false
	}
	return H("RXE1/route", in.ChainID, pid[:], le64(in.EpochNumber), in.EpochSeed[:]), true
}

// GenerateRouting derives the route 2 descriptors for an epoch, outside the hot loop.
func GenerateRouting(p Params, rseed [32]byte) Routing {
	g, k := p.G(), p.K
	out := make(Routing, p.R)
	for j := uint32(0); j < p.R; j++ {
		out[j] = make([]RouteOp, k)
		for r := uint32(0); r < k; r++ {
			q := X("RXE1/route-op", 4, rseed[:], le32(j), le32(r))
			out[j][r] = RouteOp{Off: 1 + uint32(q[0])%(g-1), Src: uint32(q[1]) % k, Rot: 1 + uint32(q[2])%31, Op: uint32(q[3]) % 2}
		}
	}
	return out
}

// mulhi32 is the high 32 bits of the 64-bit unsigned product.
func mulhi32(x, y uint32) uint32 { return uint32((uint64(x) * uint64(y)) >> 32) }

// ---------------------------------------------------------------------------
// Section 7: the reference loop.

// ItemSource serves D(pid, epoch_seed).lookup(index): exactly 128 bytes.
type ItemSource interface {
	N() uint64
	Lookup(index uint64, out *[ItemBytes]byte)
}

// readChunk reads the chunk of size bytes (64, 128 or 256) at region
// (dataset bytes [region*size, +size)) from the 128-byte items: a 64-byte
// chunk is an aligned half of one item, a 256-byte chunk an aligned pair.
func readChunk(ds ItemSource, region uint64, size uint32, out []byte) {
	var item [ItemBytes]byte
	switch size {
	case 64:
		ds.Lookup(region/2, &item)
		copy(out, item[(region%2)*64:(region%2)*64+64])
	case 128:
		ds.Lookup(region, &item)
		copy(out, item[:])
	case 256:
		ds.Lookup(2*region, &item)
		copy(out[:128], item[:])
		ds.Lookup(2*region+1, &item)
		copy(out[128:], item[:])
	}
}

// Trace holds every intermediate the vectors must publish (section 12):
// the state after initialisation, per round the address and the state after
// memory injection, after every instruction block and after the exchange.
type Trace struct {
	Init   State
	Rounds []RoundTrace
}

type RoundTrace struct {
	A            uint32   // chunk 0: the text's a after the second multiply; index = (a ^ (a >> 16)) & (N - 1)
	Index        uint64   // chunk 0's region (the item index at the v0.1 point)
	As           []uint32 // every chunk's a (schema 3; one entry otherwise)
	Regions      []uint64 // every chunk's region
	Words        []uint32 // the PAYLOAD / 4 injected words (32 at the v0.1 point)
	AfterInject  State
	Executions   []Execution // mode S: the block executions of this round (0 or 1), each before its AfterBlocks entry
	AfterBlocks  []State     // one per block (mode S: per execution)
	AfterExchang State
}

// Execution is one mode S block execution: its number e, the fold h and the selected block.
type Execution struct {
	E, H, Sel uint32
}

// Result is one nonce's outputs.
type Result struct {
	PID         [32]byte
	Seed        [32]byte
	ProgramSeed [32]byte // zero in mode M
	Out         [32]byte
	Trace       *Trace   // nil unless kept
	TraceDigest [32]byte // BLAKE3 over every intermediate (TraceDigest); zero when no trace was computed
}

// loopFromSeed runs the loop of sections 7 and 8 (and schema 3's levers) on
// a given seed: Compute without the envelope, for the tests that compare
// two records' loops on identical inputs.
func loopFromSeed(p Params, seed [32]byte, ds ItemSource, sched Schedule, routing Routing) Result {
	res := Result{Seed: seed}
	var fam Family
	if p.Mode == ModeS {
		fam = familyFor(p, p.PID())
	}
	res.Out, res.Trace = p.loop(seed, ds, sched, routing, fam, true)
	return res
}

// Compute runs one nonce. sched is the (mode, epoch) schedule from
// GenerateSchedule (nil in mode M) and routing the route 2 descriptors from
// GenerateRouting (nil for other routes); the caller generates both once per
// epoch. withTrace records every intermediate.
func Compute(p Params, in Input, ds ItemSource, sched Schedule, routing Routing, withTrace bool) (Result, error) {
	var res Result
	if err := in.Validate(); err != nil {
		return res, err
	}
	if !p.MemoryFree() && (ds == nil || ds.N() != p.N()) {
		return res, fmt.Errorf("%w: item source has %d items, the record %d", ErrParams, dsN(ds), p.N())
	}
	switch p.Mode {
	case ModeF, ModeE:
		if sched == nil || uint32(len(sched)) != p.R {
			return res, fmt.Errorf("%w: schedule missing or not %d rounds", ErrParams, p.R)
		}
	default:
		if sched != nil {
			return res, fmt.Errorf("%w: mode %s takes no epoch schedule", ErrParams, p.Mode)
		}
	}
	if p.RouteOf() == RouteEpoch && (routing == nil || uint32(len(routing)) != p.R) {
		return res, fmt.Errorf("%w: routing missing or not %d rounds", ErrParams, p.R)
	}
	res.PID = p.PID()
	res.Seed = Seed(in, res.PID)
	if ps, ok := ProgramSeed(p, in, res.PID); ok {
		res.ProgramSeed = ps
		if p.Mode == ModeP {
			// section 5: the descriptors are generated per nonce, as part of the candidate's work
			sched = GenerateSchedule(p, ps)
		}
	}
	var fam Family
	if p.Mode == ModeS {
		fam = familyFor(p, res.PID)
	}
	res.Out, res.Trace = p.loop(res.Seed, ds, sched, routing, fam, withTrace)
	return res, nil
}

// loop is the body of Compute from the seed on: the state, the rounds, out.
func (p Params) loop(seed [32]byte, ds ItemSource, sched Schedule, routing Routing, fam Family, withTrace bool) ([32]byte, *Trace) {
	var tr *Trace
	g, k := p.G(), p.K
	c, payload, chunkBytes := p.C(), p.PayloadBytes(), p.ChunkBytes()
	nRegions := p.DatasetBytes / uint64(chunkBytes)
	memoryFree := p.MemoryFree()
	var snap State // schema 5: the one state snapshot before the round's injection
	half := k / 2
	wpl := payload / 4 / g
	s := InitialState(seed, g, k)
	if withTrace {
		tr = &Trace{Init: s.clone()}
	}
	chunks := make([]byte, payload)
	w := make([]uint32, payload/4)
	for j := uint32(0); j < p.R; j++ {
		var rt RoundTrace
		// the memory step: C chunk addresses, each from its own fold (A3, section 2;
		// at C = 1, PAYLOAD = 128 this is the v0.1 address and injection)
		for ci := uint32(0); ci < c; ci++ {
			a := j*0x9E3779B9 ^ ci*0x7F4A7C15
			for l := uint32(0); l < g; l++ {
				a ^= rotl32(s[l][(j+ci)%k], l)
			}
			a = (a ^ (a >> 16)) * 0x85EBCA6B
			a = (a ^ (a >> 13)) * 0xC2B2AE35
			if memoryFree {
				// section 11.2: the payload words of chunk ci are register reads keyed by a_c from the
				// pre-injection snapshot: w[q] = S[l][(j + m + K/2) mod K] XOR a_c, l = q div WPL, m = q mod WPL
				if ci == 0 {
					snap = s.clone()
				}
				perChunk := payload / 4 / c
				for q := ci * perChunk; q < (ci+1)*perChunk; q++ {
					l, m := q/wpl, q%wpl
					w[q] = snap[l][(j+m+half)%k] ^ a
				}
				if withTrace {
					rt.As = append(rt.As, a)
				}
				continue
			}
			region := uint64(a^(a>>16)) & (nRegions - 1)
			readChunk(ds, region, chunkBytes, chunks[ci*chunkBytes:(ci+1)*chunkBytes])
			if withTrace {
				rt.As = append(rt.As, a)
				rt.Regions = append(rt.Regions, region)
			}
		}
		if !memoryFree {
			for i := range w {
				w[i] = binary.LittleEndian.Uint32(chunks[4*i:])
			}
		}
		for l := uint32(0); l < g; l++ {
			for m := uint32(0); m < wpl; m++ {
				if m%2 == 0 {
					s[l][(j+m)%k] ^= w[wpl*l+m]
				} else {
					s[l][(j+m)%k] += w[wpl*l+m]
				}
			}
		}
		if withTrace {
			rt.A = rt.As[0]
			if len(rt.Regions) > 0 {
				rt.Index = rt.Regions[0]
			}
			rt.Words = append([]uint32(nil), w...)
			rt.AfterInject = s.clone()
		}
		// instruction blocks
		if p.Mode == ModeS {
			// section 9.2: one compiled block every L / (B * K) rounds, execution e
			// in round (e + 1) * (L / (B * K)) - 1, after the injection, before the exchange
			period := p.BlockLen / (p.B * k)
			if (j+1)%period == 0 {
				e := (j+1)/period - 1
				h, sel := SelectBlock(s, e, k, p.Family)
				executeSequential(s, fam[sel])
				if withTrace {
					rt.Executions = append(rt.Executions, Execution{E: e, H: h, Sel: sel})
					rt.AfterBlocks = append(rt.AfterBlocks, s.clone())
				}
			}
		} else {
			for b := uint32(0); b < p.B; b++ {
				executeBlock(s, sched[j][b])
				if withTrace {
					rt.AfterBlocks = append(rt.AfterBlocks, s.clone())
				}
			}
		}
		// the exchange step (A2, section 3), from one snapshot of the whole group
		t := s.clone()
		switch p.RouteOf() {
		case RouteLocal:
			// S = T
		case RouteFixed:
			for l := uint32(0); l < g; l++ {
				for r := uint32(0); r < k; r++ {
					s[l][r] = t[l][r] + rotl32(t[(l+1)%g][(r+1)%k], 7)
				}
			}
		case RouteEpoch:
			for r := uint32(0); r < k; r++ {
				ro := routing[j][r]
				for l := uint32(0); l < g; l++ {
					v := rotl32(t[(l+ro.Off)%g][ro.Src], ro.Rot)
					if ro.Op == 0 {
						s[l][r] = t[l][r] + v
					} else {
						// S = (T XOR mulhi32(T | 1, v)) + v: the xor first, the add last (text revision 2, section 7)
						s[l][r] = (t[l][r] ^ mulhi32(t[l][r]|1, v)) + v
					}
				}
			}
		case RouteState:
			for l := uint32(0); l < g; l++ {
				for r := uint32(0); r < k; r++ {
					off := 1 + (t[l][(r+1)%k]>>16)%(g-1)
					s[l][r] = t[l][r] + rotl32(t[(l+off)%g][(r+1)%k], 7)
				}
			}
		}
		if withTrace {
			rt.AfterExchang = s.clone()
			tr.Rounds = append(tr.Rounds, rt)
		}
	}
	return H("RXE1/result", seed[:], s.bytesLaneMajor()), tr
}

func dsN(ds ItemSource) uint64 {
	if ds == nil {
		return 0
	}
	return ds.N()
}

// Accept is the harness rule (section 7): out as an unsigned little-endian
// 256-bit integer is at most the target. No early target filter.
func Accept(out, target [32]byte) bool {
	for i := 31; i >= 0; i-- {
		if out[i] != target[i] {
			return out[i] < target[i]
		}
	}
	return true
}
