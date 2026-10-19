package forge

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"lukechampine.com/blake3"
)

const vectorsPath = "../../../../../pow/vectors/forge/f0-r2/vectors.json"

func h32(s string) [32]byte { return blake3.Sum256([]byte(s)) }

func targetOf(difficulty uint64) (out [32]byte) {
	pow.Target(new(big.Int).SetUint64(difficulty)).FillBytes(out[:])
	return
}

var sampleCoinbase = func() (c [20]byte) { c[19] = 1; return }()

func sample() Job {
	return Job{
		ChainID:      7310,
		Version:      Version,
		ParentID:     h32("parent-1"),
		Height:       8192,
		EpochID:      2,
		ParamsID:     h32("params-1"),
		Target:       targetOf(1_000_000),
		TxCommit:     h32("txs-1"),
		RewardCommit: RewardCommitment(sampleCoinbase, []byte("extra-1")),
	}
}

func TestJobLength(t *testing.T) {
	if JobLen != 208 {
		t.Fatalf("JobLen %d, the text says 208", JobLen)
	}
	b := Encode(sample())
	if len(b) != JobLen || b[0] != 19 || string(b[1:20]) != JobDomain {
		t.Fatal("job length or domain")
	}
}

func TestDecodeRoundTrip(t *testing.T) {
	j := sample()
	got, err := DecodeJob(Encode(j))
	if err != nil || got != j {
		t.Fatalf("round trip: %v %+v", err, got)
	}
}

// The rejected encodings of section 2.4.
func TestDecodeRejects(t *testing.T) {
	b := Encode(sample())
	cases := map[string][]byte{"trailing byte": append(append([]byte{}, b...), 0), "missing byte": b[:len(b)-1]}
	bad := append([]byte{}, b...)
	bad[1] ^= 0x20
	cases["domain case"] = bad
	bad = append([]byte{}, b...)
	bad[0] = 18
	cases["domain length"] = bad
	bad = append([]byte{}, b...)
	bad[1+19+8] = 3
	cases["version 3"] = bad
	for name, c := range cases {
		if _, err := DecodeJob(c); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestAnchorHeight(t *testing.T) {
	if AnchorHeight(0, 4096, 4096) != 0 || AnchorHeight(1, 4096, 4096) != 0 || AnchorHeight(2, 4096, 4096) != 4096 || AnchorHeight(5, 4096, 1000) != 19480 {
		t.Fatal("anchor height")
	}
}

// Every field of the job, the anchor, the batch nonce, j, B and the root
// move the values they enter; the ticket index is bounded by B (section
// 9.3, the degree-of-freedom table).
func TestEveryFieldBinds(t *testing.T) {
	base := sample()
	anchor := h32("anchor-1")
	job := Encode(base)
	id := JobID(job)
	es := EpochSeed(anchor, base.EpochID, base.ParamsID)
	fs := FillSeed(es, id, 5)
	root := h32("root-1")
	t0, _ := Ticket(id, root, 5, 0, 4, base.ParamsID)
	variants := map[string]func(*Job){
		"chain id":  func(j *Job) { j.ChainID++ },
		"parent id": func(j *Job) { j.ParentID[0] ^= 1 },
		"height":    func(j *Job) { j.Height++ },
		"epoch id":  func(j *Job) { j.EpochID++ },
		"params id": func(j *Job) { j.ParamsID[0] ^= 1 },
		"target":    func(j *Job) { j.Target[31] ^= 1 },
		"tx commit": func(j *Job) { j.TxCommit[0] ^= 1 },
		"reward":    func(j *Job) { j.RewardCommit = RewardCommitment(sampleCoinbase, []byte("extra-2")) },
	}
	for name, mutate := range variants {
		j := base
		mutate(&j)
		id2 := JobID(Encode(j))
		if id2 == id {
			t.Errorf("%s: the job id did not move", name)
		}
		if FillSeed(es, id2, 5) == fs {
			t.Errorf("%s: the fill seed did not move", name)
		}
		if tk, _ := Ticket(id2, root, 5, 0, 4, base.ParamsID); tk == t0 {
			t.Errorf("%s: the ticket did not move", name)
		}
	}
	if EpochSeed(h32("anchor-2"), base.EpochID, base.ParamsID) == es || EpochSeed(anchor, base.EpochID+1, base.ParamsID) == es {
		t.Error("the anchor or the epoch id did not move the epoch seed")
	}
	if FillSeed(es, id, 6) == fs {
		t.Error("the batch nonce did not move the fill seed")
	}
	for _, c := range []struct {
		name  string
		nonce uint64
		j, B  uint32
		root  [32]byte
	}{{"batch nonce", 6, 0, 4, root}, {"j", 5, 1, 4, root}, {"B", 5, 0, 8, root}, {"root", 5, 0, 4, h32("root-2")}} {
		if tk, _ := Ticket(id, c.root, c.nonce, c.j, c.B, base.ParamsID); tk == t0 {
			t.Errorf("%s: the ticket did not move", c.name)
		}
	}
	if _, err := Ticket(id, root, 5, 4, 4, base.ParamsID); err == nil {
		t.Error("j = B accepted")
	}
	if RewardCommitment(sampleCoinbase, []byte("extra-1")) == RewardCommitment(sampleCoinbase, []byte("extra-1x")) {
		t.Error("the extra nonce did not move the reward commitment")
	}
	// The challenge and the public digest bind the whole transcript.
	t1, _ := Ticket(id, root, 5, 1, 4, base.ParamsID)
	c0 := Challenges(t0, 4, 1<<21)
	c1 := Challenges(t1, 4, 1<<21)
	if c0[0] == c1[0] && c0[1] == c1[1] {
		t.Error("the challenges ignore j")
	}
	if c0[0] == c0[1] && c0[1] == c0[2] {
		t.Error("the challenge stream repeats")
	}
	for _, c := range c0 {
		if c >= 1<<21 {
			t.Error("a challenge beyond N")
		}
	}
	p0 := Public(h32("program-1"), base.ChainID, Version, id, root, 5, 0, 4, base.ParamsID)
	if Public(h32("program-2"), base.ChainID, Version, id, root, 5, 0, 4, base.ParamsID) == p0 || Public(h32("program-1"), base.ChainID, Version, id, root, 5, 1, 4, base.ParamsID) == p0 {
		t.Error("the public digest ignores the program id or j")
	}
}

func TestQualifies(t *testing.T) {
	var target [32]byte
	target[0] = 0x0f
	for i := 1; i < 32; i++ {
		target[i] = 0xff
	}
	above, below := target, target
	above[0] = 0x10
	below[31] = 0xfe
	if !Qualifies(target, target) || !Qualifies(below, target) || Qualifies(above, target) {
		t.Fatal("the inclusive big-endian rule does not hold")
	}
}

func TestRootShape(t *testing.T) {
	labels := [][]byte{[]byte("a"), []byte("b"), []byte("c"), []byte("d")}
	root, err := Root(labels)
	if err != nil {
		t.Fatal(err)
	}
	want := Node(Node(Leaf(0, labels[0]), Leaf(1, labels[1])), Node(Leaf(2, labels[2]), Leaf(3, labels[3])))
	if root != want {
		t.Fatal("the root is not the pairwise tree over indexed leaves")
	}
	if _, err := Root(labels[:3]); err == nil {
		t.Fatal("three leaves accepted")
	}
}

type vector struct {
	Name         string   `json:"name"`
	ChainID      uint64   `json:"chain_id"`
	Version      uint32   `json:"version"`
	ParentID     string   `json:"parent_id"`
	Height       uint64   `json:"height"`
	EpochID      uint64   `json:"epoch_id"`
	ParamsID     string   `json:"params_id"`
	Target       string   `json:"target"`
	TxCommit     string   `json:"tx_commitment"`
	Coinbase     string   `json:"coinbase"`
	Extra        string   `json:"extra"`
	RewardCommit string   `json:"reward_commitment"`
	Job          string   `json:"job"`
	JobID        string   `json:"job_id"`
	Anchor       string   `json:"delayed_anchor"`
	EpochSeed    string   `json:"epoch_seed"`
	BatchNonce   uint64   `json:"batch_nonce"`
	FillSeed     string   `json:"fill_seed"`
	Labels       []string `json:"labels"`
	Root         string   `json:"root"`
	B            uint32   `json:"B"`
	Tickets      []string `json:"tickets"`
	Qualify      []bool   `json:"qualifies"`
	N            uint64   `json:"N"`
	Challenges   []uint64 `json:"challenges_j0_k0_to_k3"`
	ProgramID    string   `json:"program_id"`
	Public       string   `json:"public_j0"`
}

type vectorFile struct {
	Relation string   `json:"relation"`
	Hash     string   `json:"hash"`
	JobLen   int      `json:"job_len"`
	Domains  []string `json:"domains"`
	Vectors  []vector `json:"vectors"`
	Rejected []string `json:"rejected_encodings"`
}

func hx(b []byte) string { return hex.EncodeToString(b) }

func mustHex(t *testing.T, s string, n int) []byte {
	b, err := hex.DecodeString(s)
	if err != nil || (n >= 0 && len(b) != n) {
		t.Fatalf("vector field %q: want %d bytes", s, n)
	}
	return b
}

func makeVectors() vectorFile {
	f := vectorFile{
		Relation: "Igneum Forge F0 relation revision 2 on the Bounded Memory Tickets objects (docs/igneumhash/forge/F0-RELATION.md)",
		Hash:     "BLAKE3-256 (one-byte domain length, domain bytes, then the fields); integers unsigned little-endian; targets and tickets compared as 256-bit big-endian",
		JobLen:   JobLen,
		Domains:  []string{JobDomain, RewardDomain, EpochDomain, FillDomain, LeafDomain, NodeDomain, TicketDomain, ChallengeDomain, PublicDomain},
		Rejected: []string{"a job of any length other than 208", "a domain length other than 19 or domain bytes other than the job domain", "a version other than 2", "a ticket index j at or above B", "a leaf count that is not a power of two", "extra data longer than 32 bytes"},
	}
	var ff [32]byte
	for i := range ff {
		ff[i] = 0xff
	}
	var ffc [20]byte
	for i := range ffc {
		ffc[i] = 0xff
	}
	s := sample()
	type in struct {
		name       string
		job        Job
		coinbase   [20]byte
		extra      []byte
		anchor     [32]byte
		batchNonce uint64
		labels     [][]byte
		B          uint32
		N          uint64
		program    [32]byte
	}
	zero := Job{Version: Version, RewardCommit: RewardCommitment([20]byte{}, nil)}
	maxExtra := bytes.Repeat([]byte{0xff}, 32)
	maxJob := Job{ChainID: ^uint64(0), Version: Version, ParentID: ff, Height: ^uint64(0), EpochID: ^uint64(0), ParamsID: ff, Target: ff, TxCommit: ff, RewardCommit: RewardCommitment(ffc, maxExtra)}
	next := s
	next.Height++
	cases := []in{
		{"zero fields", zero, [20]byte{}, nil, [32]byte{}, 0, [][]byte{{0}, {0}}, 1, 8, [32]byte{}},
		{"devnet-shaped", s, sampleCoinbase, []byte("extra-1"), h32("anchor-1"), 5, [][]byte{[]byte("label-0"), []byte("label-1"), []byte("label-2"), []byte("label-3")}, 4, 1 << 21, h32("program-1")},
		{"next height, same batch nonce", next, sampleCoinbase, []byte("extra-1"), h32("anchor-1"), 5, [][]byte{[]byte("label-0"), []byte("label-1"), []byte("label-2"), []byte("label-3")}, 4, 1 << 21, h32("program-1")},
		{"maximum fields", maxJob, ffc, maxExtra, ff, ^uint64(0), [][]byte{maxExtra, maxExtra}, 64, 1 << 26, ff},
		{"target zero, eight leaves", Job{ChainID: 1, Version: Version, Height: 1, RewardCommit: RewardCommitment([20]byte{}, []byte{1})}, [20]byte{}, []byte{1}, h32("anchor-2"), 1, [][]byte{{1}, {2}, {3}, {4}, {5}, {6}, {7}, {8}}, 2, 8, h32("program-1")},
	}
	for _, c := range cases {
		job := Encode(c.job)
		id := JobID(job)
		es := EpochSeed(c.anchor, c.job.EpochID, c.job.ParamsID)
		fs := FillSeed(es, id, c.batchNonce)
		root, _ := Root(c.labels)
		v := vector{
			Name: c.name, ChainID: c.job.ChainID, Version: c.job.Version, ParentID: hx(c.job.ParentID[:]), Height: c.job.Height, EpochID: c.job.EpochID,
			ParamsID: hx(c.job.ParamsID[:]), Target: hx(c.job.Target[:]), TxCommit: hx(c.job.TxCommit[:]), Coinbase: hx(c.coinbase[:]), Extra: hx(c.extra),
			RewardCommit: hx(c.job.RewardCommit[:]), Job: hx(job), JobID: hx(id[:]), Anchor: hx(c.anchor[:]), EpochSeed: hx(es[:]), BatchNonce: c.batchNonce,
			FillSeed: hx(fs[:]), Root: hx(root[:]), B: c.B, N: c.N, ProgramID: hx(c.program[:]),
		}
		for _, l := range c.labels {
			v.Labels = append(v.Labels, hx(l))
		}
		for j := uint32(0); j < c.B; j++ {
			tk, _ := Ticket(id, root, c.batchNonce, j, c.B, c.job.ParamsID)
			v.Tickets = append(v.Tickets, hx(tk[:]))
			v.Qualify = append(v.Qualify, Qualifies(tk, c.job.Target))
		}
		tk0, _ := Ticket(id, root, c.batchNonce, 0, c.B, c.job.ParamsID)
		v.Challenges = Challenges(tk0, 4, c.N)
		pub := Public(c.program, c.job.ChainID, c.job.Version, id, root, c.batchNonce, 0, c.B, c.job.ParamsID)
		v.Public = hx(pub[:])
		f.Vectors = append(f.Vectors, v)
	}
	return f
}

// TestVectors checks the published vectors against this implementation;
// WRITE_TEST_FILES=1 regenerates them.
func TestVectors(t *testing.T) {
	path := filepath.Clean(vectorsPath)
	if os.Getenv("WRITE_TEST_FILES") == "1" {
		b, _ := json.MarshalIndent(makeVectors(), "", "  ")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f vectorFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if f.JobLen != JobLen || len(f.Vectors) == 0 {
		t.Fatal("vector file header")
	}
	for _, v := range f.Vectors {
		var j Job
		j.ChainID, j.Version, j.Height, j.EpochID = v.ChainID, v.Version, v.Height, v.EpochID
		copy(j.ParentID[:], mustHex(t, v.ParentID, 32))
		copy(j.ParamsID[:], mustHex(t, v.ParamsID, 32))
		copy(j.Target[:], mustHex(t, v.Target, 32))
		copy(j.TxCommit[:], mustHex(t, v.TxCommit, 32))
		var coinbase [20]byte
		copy(coinbase[:], mustHex(t, v.Coinbase, 20))
		j.RewardCommit = RewardCommitment(coinbase, mustHex(t, v.Extra, -1))
		if hx(j.RewardCommit[:]) != v.RewardCommit {
			t.Errorf("%s: reward commitment differs", v.Name)
		}
		job := Encode(j)
		if !bytes.Equal(job, mustHex(t, v.Job, JobLen)) {
			t.Errorf("%s: job differs", v.Name)
		}
		if _, err := DecodeJob(job); err != nil {
			t.Errorf("%s: decode: %v", v.Name, err)
		}
		id := JobID(job)
		if hx(id[:]) != v.JobID {
			t.Errorf("%s: job id differs", v.Name)
		}
		var anchor [32]byte
		copy(anchor[:], mustHex(t, v.Anchor, 32))
		es := EpochSeed(anchor, j.EpochID, j.ParamsID)
		if hx(es[:]) != v.EpochSeed {
			t.Errorf("%s: epoch seed differs", v.Name)
		}
		if fs := FillSeed(es, id, v.BatchNonce); hx(fs[:]) != v.FillSeed {
			t.Errorf("%s: fill seed differs", v.Name)
		}
		var labels [][]byte
		for _, l := range v.Labels {
			labels = append(labels, mustHex(t, l, -1))
		}
		root, err := Root(labels)
		if err != nil || hx(root[:]) != v.Root {
			t.Errorf("%s: root differs (%v)", v.Name, err)
		}
		for jj := uint32(0); jj < v.B; jj++ {
			tk, err := Ticket(id, root, v.BatchNonce, jj, v.B, j.ParamsID)
			if err != nil || hx(tk[:]) != v.Tickets[jj] || Qualifies(tk, j.Target) != v.Qualify[jj] {
				t.Errorf("%s: ticket %d differs", v.Name, jj)
			}
		}
		tk0, _ := Ticket(id, root, v.BatchNonce, 0, v.B, j.ParamsID)
		got := Challenges(tk0, 4, v.N)
		for k := range got {
			if got[k] != v.Challenges[k] {
				t.Errorf("%s: challenge %d differs", v.Name, k)
			}
		}
		var program [32]byte
		copy(program[:], mustHex(t, v.ProgramID, 32))
		if pub := Public(program, j.ChainID, j.Version, id, root, v.BatchNonce, 0, v.B, j.ParamsID); hx(pub[:]) != v.Public {
			t.Errorf("%s: public digest differs", v.Name)
		}
	}
}
