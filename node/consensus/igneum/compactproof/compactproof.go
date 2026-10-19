// Package compactproof is the second implementation of the compact-proof
// research reference (candidate "compactproof": the MTP proof relation over
// the DRSample graph under Forge's F0 envelope, the 2025 modifications
// unread), written from the hash team's text alone
// (docs/igneumhash/research/COMPACTPROOF-NOTES.md section 2 at 9989de2),
// never from the reference's code. Forge F1: the faithful small reference at
// 64 MiB; exact intermediate and final vectors; the stop is any mismatch or
// ambiguity. Research only: nothing here activates on a network.
//
// The relation (section 2.3): y = WorkF(seed; params). The seed is the work
// function's only input (one fill per attempt, no nonce step): the labels
// over the seed, the Merkle root R, Y_0 = BLAKE3(tag("CP/y0") || seed ||
// program_id || R), the L-step chain, y = Y_L; the proof is R then the L
// openings; the verifier takes (params, seed, program_id, y, proof).
//
// Readings where the text is silent or at odds with itself (each a text
// correction if the differential says otherwise):
//
//	R-1 tag(T) is the project's u8(len(T)) || T (SPEC-R0A-v0.md); XOF(m) is
//	    BLAKE3's extendable output over m, its first label_bytes bytes.
//	R-2 pid = BLAKE3 of the 20-byte little-endian record with no tag.
//	R-3 for i_j < 2 the two parent labels and their two paths are zero bytes
//	    and the verifier requires them zero (one valid encoding per proof).
//	R-4 the parent draw's v - r < 1 is clamped to vertex 1, as the text notes.
//	R-5 section 2.2 still writes the labels over the challenge I and section
//	    5 the nonce form, while section 2.3 makes the seed the only input;
//	    F0-RELATION.md section 8 says I is replaced by the seed, so the labels
//	    are X[0] = XOF(tag("CP/label0") || seed), X[1] = XOF(tag("CP/label")
//	    || seed || LE64(1) || X[0]), X[i] = XOF(tag("CP/label") || seed ||
//	    LE64(i) || X[i-1] || X[p(i)]).
//	R-6 the program id's "compactproof/v1" is the raw ASCII bytes with no
//	    length byte: BLAKE3(tag("CP/program") || "compactproof/v1" || pid).
package compactproof

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"

	"lukechampine.com/blake3"
)

// ID is the candidate id.
const ID = "compactproof"

const (
	Schema     uint32 = 1
	TestSchema uint32 = 0x54455354 // "TEST": a test-only record (any memory size)
)

// Params is the parameter document (section 2.3).
type Params struct {
	Schema      uint32 `json:"schema"`
	MemoryBytes uint64 `json:"memory_bytes"`
	LabelBytes  uint32 `json:"label_bytes"`
	Challenges  uint32 `json:"challenges"`
}

var ErrParams = errors.New("compactproof: parameter record rejected")
var ErrProof = errors.New("compactproof: proof rejected")

// T is the label count memory_bytes / label_bytes.
func (p Params) T() uint64 { return p.MemoryBytes / uint64(p.LabelBytes) }

// LogT is log2 T.
func (p Params) LogT() uint32 { return uint32(bits.TrailingZeros64(p.T())) }

// Record is schema u32 || memory_bytes u64 || label_bytes u32 || challenges u32, little-endian, 20 bytes.
func (p Params) Record() []byte {
	out := make([]byte, 0, 20)
	out = binary.LittleEndian.AppendUint32(out, p.Schema)
	out = binary.LittleEndian.AppendUint64(out, p.MemoryBytes)
	out = binary.LittleEndian.AppendUint32(out, p.LabelBytes)
	out = binary.LittleEndian.AppendUint32(out, p.Challenges)
	return out
}

// PID hashes the record (reading R-2: no tag).
func (p Params) PID() [32]byte { return blake3.Sum256(p.Record()) }

// Variant is the tag cp-m<mib>-l<label_bytes>-c<L>, prefixed "test-" for a test-only record.
func (p Params) Variant() string {
	t := ""
	if p.Schema == TestSchema {
		t = "test-"
	}
	return fmt.Sprintf("cp-%sm%d-l%d-c%d", t, p.MemoryBytes>>20, p.LabelBytes, p.Challenges)
}

func (p Params) validateShape() error {
	switch {
	case p.Schema != Schema && p.Schema != TestSchema:
		return fmt.Errorf("%w: schema %d", ErrParams, p.Schema)
	case p.LabelBytes != 32 && p.LabelBytes != 64 && p.LabelBytes != 128:
		return fmt.Errorf("%w: label_bytes %d (32, 64 or 128)", ErrParams, p.LabelBytes)
	case p.MemoryBytes%uint64(p.LabelBytes) != 0 || p.T() < 4 || p.T()&(p.T()-1) != 0:
		return fmt.Errorf("%w: memory_bytes %d is not a power-of-two count of labels", ErrParams, p.MemoryBytes)
	case p.Challenges == 0:
		return fmt.Errorf("%w: challenges 0", ErrParams)
	}
	return nil
}

// Validate admits the research points: 64 MiB (F1), 256 MiB, 1 GiB, 2 GiB.
func (p Params) Validate() error {
	if err := p.validateShape(); err != nil {
		return err
	}
	if p.Schema == TestSchema {
		return fmt.Errorf("%w: test-only schema", ErrParams)
	}
	switch p.MemoryBytes {
	case 64 << 20, 256 << 20, 1 << 30, 2 << 30:
		return nil
	}
	return fmt.Errorf("%w: memory_bytes %d (64 MiB, 256 MiB, 1 GiB or 2 GiB)", ErrParams, p.MemoryBytes)
}

// ValidateTestOnly admits any shape under the test schema.
func (p Params) ValidateTestOnly() error {
	if err := p.validateShape(); err != nil {
		return err
	}
	if p.Schema != TestSchema {
		return fmt.Errorf("%w: a test-only record carries schema 0x54455354", ErrParams)
	}
	return nil
}

// tag is u8(len(T)) || T (reading R-1).
func tag(t string) []byte { return append([]byte{byte(len(t))}, t...) }

func le64(v uint64) []byte { return binary.LittleEndian.AppendUint64(nil, v) }

func h32(parts ...[]byte) [32]byte { return blake3.Sum256(bytes.Join(parts, nil)) }

func xof(n int, parts ...[]byte) []byte {
	h := blake3.New(n, nil)
	h.Write(bytes.Join(parts, nil))
	return h.Sum(nil)
}

// GetParent is DRSample's parent of the 1-based vertex v >= 3 (section 2.1):
// g' uniform in [1, floor(log2 v) + 1], g = min(v, 2^g'), r uniform in
// [max(g / 2, 2), g], parent v - r (clamped to 1 at 0); both draws by
// remainder from BLAKE3(tag("CP/parent") || pid || LE64(v)): bytes 0..8 for
// g', 8..16 for r, little-endian. Vertex 2's parent is 1; vertex 1 has none.
func GetParent(pid [32]byte, v uint64) uint64 {
	if v < 3 {
		return 1
	}
	d := h32(tag("CP/parent"), pid[:], le64(v))
	u1 := binary.LittleEndian.Uint64(d[0:8])
	u2 := binary.LittleEndian.Uint64(d[8:16])
	logv := uint64(bits.Len64(v) - 1) // floor(log2 v)
	gp := 1 + u1%(logv+1)
	g := uint64(1) << gp
	if g > v {
		g = v
	}
	lo := g / 2
	if lo < 2 {
		lo = 2
	}
	r := lo + u2%(g-lo+1)
	if r >= v {
		return 1
	}
	return v - r
}

// Parent0 is p(i) = GetParent(i + 1) - 1, the 0-based parent of label i >= 2.
func Parent0(pid [32]byte, i uint64) uint64 { return GetParent(pid, i+1) - 1 }

// HarnessSeed is the ABI harness's seed BLAKE3(tag("CP/challenge") ||
// LE64(chain_id) || header32 || LE64(nonce)) (section 2.3b); the node uses
// F0's H_seed(x) instead, and vectors carry explicit seeds.
func HarnessSeed(chainID uint64, header32 [32]byte, nonce uint64) [32]byte {
	return h32(tag("CP/challenge"), le64(chainID), header32[:], le64(nonce))
}

// ProgramID is BLAKE3(tag("CP/program") || "compactproof/v1" || pid), one per
// parameter set (section 2.3; reading R-6).
func ProgramID(pid [32]byte) [32]byte {
	return h32(tag("CP/program"), []byte("compactproof/v1"), pid[:])
}

// Labels is the filled memory: T labels of label_bytes, contiguous.
type Labels struct {
	P      Params
	PID    [32]byte
	Seed   [32]byte
	Data   []byte
	nodes  [][32]byte // the Merkle tree in heap order (node 1 the root, leaf i at T + i); nil until Commit
	root   [32]byte
	rooted bool
}

// Label i.
func (l *Labels) Label(i uint64) []byte {
	lb := uint64(l.P.LabelBytes)
	return l.Data[i*lb : (i+1)*lb]
}

// Label0 and Label1 derive X[0] and X[1] from the seed alone (the verifier's path for i_j < 2).
func Label0(p Params, seed [32]byte) []byte {
	return xof(int(p.LabelBytes), tag("CP/label0"), seed[:])
}

func Label1(p Params, seed [32]byte, x0 []byte) []byte {
	return xof(int(p.LabelBytes), tag("CP/label"), seed[:], le64(1), x0)
}

// LabelFrom is X[i] = XOF(tag("CP/label") || seed || LE64(i) || X[i-1] || X[p(i)]) for i >= 2.
func LabelFrom(p Params, seed [32]byte, i uint64, prev, parent []byte) []byte {
	return xof(int(p.LabelBytes), tag("CP/label"), seed[:], le64(i), prev, parent)
}

// Fill computes every label of one attempt's seed (section 2.2 under 2.3).
func Fill(p Params, seed [32]byte) *Labels {
	l := &Labels{P: p, PID: p.PID(), Seed: seed}
	t := p.T()
	lb := uint64(p.LabelBytes)
	l.Data = make([]byte, t*lb)
	copy(l.Data[0:lb], Label0(p, seed))
	copy(l.Data[lb:2*lb], Label1(p, seed, l.Data[0:lb]))
	for i := uint64(2); i < t; i++ {
		pi := Parent0(l.PID, i)
		copy(l.Data[i*lb:(i+1)*lb], LabelFrom(p, seed, i, l.Data[(i-1)*lb:i*lb], l.Data[pi*lb:(pi+1)*lb]))
	}
	return l
}

// Leaf is BLAKE3(tag("CP/leaf") || LE64(i) || X[i]).
func Leaf(i uint64, x []byte) [32]byte { return h32(tag("CP/leaf"), le64(i), x) }

// Node is BLAKE3(tag("CP/node") || left || right).
func Node(left, right [32]byte) [32]byte { return h32(tag("CP/node"), left[:], right[:]) }

// Commit builds the binary Merkle tree over the T leaves and returns the root R.
func (l *Labels) Commit() [32]byte {
	if l.rooted {
		return l.root
	}
	t := l.P.T()
	l.nodes = make([][32]byte, 2*t)
	for i := uint64(0); i < t; i++ {
		l.nodes[t+i] = Leaf(i, l.Label(i))
	}
	for k := t - 1; k >= 1; k-- {
		l.nodes[k] = Node(l.nodes[2*k], l.nodes[2*k+1])
	}
	l.root, l.rooted = l.nodes[1], true
	return l.root
}

// Path is the log2 T sibling nodes of leaf i, from the leaf up, concatenated.
func (l *Labels) Path(i uint64) []byte {
	l.Commit()
	t := l.P.T()
	out := make([]byte, 0, 32*l.P.LogT())
	for idx := t + i; idx > 1; idx >>= 1 {
		out = append(out, l.nodes[idx^1][:]...)
	}
	return out
}

// checkPath verifies leaf i's path against the root.
func checkPath(p Params, root [32]byte, i uint64, leaf [32]byte, path []byte) bool {
	cur := leaf
	idx := p.T() + i
	for k := uint32(0); k < p.LogT(); k++ {
		var sib [32]byte
		copy(sib[:], path[32*k:32*k+32])
		if idx&1 == 0 {
			cur = Node(cur, sib)
		} else {
			cur = Node(sib, cur)
		}
		idx >>= 1
	}
	return cur == root
}

// Y0 is BLAKE3(tag("CP/y0") || seed || program_id || R) (section 2.3: no nonce step).
func Y0(seed, programID, R [32]byte) [32]byte { return h32(tag("CP/y0"), seed[:], programID[:], R[:]) }

// Step is i_j = LE64(Y_{j-1}[0..8)) mod T and Y_j = BLAKE3(tag("CP/y") || Y_{j-1} || X[i_j]).
func Step(p Params, y [32]byte, label func(i uint64) []byte) (uint64, [32]byte) {
	i := binary.LittleEndian.Uint64(y[0:8]) % p.T()
	return i, h32(tag("CP/y"), y[:], label(i))
}

// Chain runs the L steps of the attempt: the indices and the work hash y = Y_L.
func (l *Labels) Chain() ([]uint64, [32]byte) {
	R := l.Commit()
	y := Y0(l.Seed, ProgramID(l.PID), R)
	idx := make([]uint64, l.P.Challenges)
	for j := range idx {
		idx[j], y = Step(l.P, y, l.Label)
	}
	return idx, y
}

// ProofBytes is 32 + L x (8 + 2 x label_bytes + 3 x log2 T x 32): R, then the openings.
func (p Params) ProofBytes() int {
	return 32 + int(p.Challenges)*(8+2*int(p.LabelBytes)+3*int(p.LogT())*32)
}

// Prove writes the attempt's proof: R (32), then per j LE64(i_j) ||
// X[i_j - 1] || X[p(i_j)] || path(i_j - 1) || path(p(i_j)) || path(i_j); for
// i_j < 2 the parent labels and their paths are zero (reading R-3).
func (l *Labels) Prove() ([]byte, [32]byte) {
	idx, y := l.Chain()
	R := l.Commit()
	lb := int(l.P.LabelBytes)
	pl := 32 * int(l.P.LogT())
	out := make([]byte, 0, l.P.ProofBytes())
	out = append(out, R[:]...)
	for _, i := range idx {
		out = append(out, le64(i)...)
		if i < 2 {
			out = append(out, make([]byte, 2*lb+2*pl)...)
		} else {
			pi := Parent0(l.PID, i)
			out = append(out, l.Label(i-1)...)
			out = append(out, l.Label(pi)...)
			out = append(out, l.Path(i-1)...)
			out = append(out, l.Path(pi)...)
		}
		out = append(out, l.Path(i)...)
	}
	return out, y
}

// Verify is the light verifier (section 2.3, Algorithm 2) of the statement
// "WorkF(seed; params) = y under program id": it refuses another program id
// before anything else, reads R from the proof, replays the chain through the
// openings and compares its Y_L with the claimed y. It allocates nothing
// proportional to T.
func Verify(p Params, seed, programID, y [32]byte, proof []byte) error {
	if programID != ProgramID(p.PID()) {
		return fmt.Errorf("%w: program id is not this parameter set's", ErrParams)
	}
	got, err := Replay(p, seed, programID, proof)
	if err != nil {
		return err
	}
	if got != y {
		return fmt.Errorf("%w: the replayed y differs from the claimed y", ErrProof)
	}
	return nil
}

// Replay runs the verifier's chain over the proof's openings and returns Y_L.
func Replay(p Params, seed, programID [32]byte, proof []byte) ([32]byte, error) {
	var zero [32]byte
	if len(proof) != p.ProofBytes() {
		return zero, fmt.Errorf("%w: %d bytes, the layout has %d", ErrProof, len(proof), p.ProofBytes())
	}
	pid := p.PID()
	lb := int(p.LabelBytes)
	pl := 32 * int(p.LogT())
	var R [32]byte
	copy(R[:], proof[0:32])
	y := Y0(seed, programID, R)
	off := 32
	for j := uint32(0); j < p.Challenges; j++ {
		want := binary.LittleEndian.Uint64(y[0:8]) % p.T()
		got := binary.LittleEndian.Uint64(proof[off : off+8])
		if got != want {
			return zero, fmt.Errorf("%w: step %d opens index %d, the chain says %d", ErrProof, j, got, want)
		}
		i := want
		prev := proof[off+8 : off+8+lb]
		par := proof[off+8+lb : off+8+2*lb]
		pathPrev := proof[off+8+2*lb : off+8+2*lb+pl]
		pathPar := proof[off+8+2*lb+pl : off+8+2*lb+2*pl]
		pathI := proof[off+8+2*lb+2*pl : off+8+2*lb+3*pl]
		off += 8 + 2*lb + 3*pl
		var x []byte
		if i < 2 {
			if !isZero(prev) || !isZero(par) || !isZero(pathPrev) || !isZero(pathPar) {
				return zero, fmt.Errorf("%w: step %d opens index %d with non-zero parent fields", ErrProof, j, i)
			}
			x0 := Label0(p, seed)
			x = x0
			if i == 1 {
				x = Label1(p, seed, x0)
			}
		} else {
			pi := Parent0(pid, i)
			if !checkPath(p, R, i-1, Leaf(i-1, prev), pathPrev) {
				return zero, fmt.Errorf("%w: step %d, the path of X[%d] does not reach the root", ErrProof, j, i-1)
			}
			if !checkPath(p, R, pi, Leaf(pi, par), pathPar) {
				return zero, fmt.Errorf("%w: step %d, the path of X[p(%d)] = X[%d] does not reach the root", ErrProof, j, i, pi)
			}
			x = LabelFrom(p, seed, i, prev, par)
		}
		if !checkPath(p, R, i, Leaf(i, x), pathI) {
			return zero, fmt.Errorf("%w: step %d, the path of X[%d] does not reach the root", ErrProof, j, i)
		}
		y = h32(tag("CP/y"), y[:], x)
	}
	return y, nil
}

func isZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// Qualifies compares the work hash as the ABI does: big-endian 256-bit, <= boundary.
func Qualifies(y, boundary [32]byte) bool { return bytes.Compare(y[:], boundary[:]) <= 0 }
