package forgecheckr2

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
)

// Vector is one tuple of the protocol team's revision 2 vector form
// (pow/vectors/forge/f0-r2/vectors.json): the inputs, then every object.
type Vector struct {
	Name         string   `json:"name"`
	ChainID      uint64   `json:"chain_id"`
	Version      uint32   `json:"version"`
	ParentID     string   `json:"parent_id"`
	Height       uint64   `json:"height"`
	EpochID      uint64   `json:"epoch_id"`
	ParamsID     string   `json:"params_id"`
	Target       string   `json:"target"`
	TxCommitment string   `json:"tx_commitment"`
	Coinbase     string   `json:"coinbase"`
	Extra        string   `json:"extra"`
	Reward       string   `json:"reward_commitment"`
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
	Qualifies    []bool   `json:"qualifies"`
	N            uint64   `json:"N"`
	Challenges   []uint64 `json:"challenges_j0_k0_to_k3"`
	ProgramID    string   `json:"program_id"`
	PublicJ0     string   `json:"public_j0"`
}

type VectorFile struct {
	Relation string   `json:"relation"`
	Hash     string   `json:"hash"`
	JobLen   int      `json:"job_len"`
	Domains  []string `json:"domains"`
	Vectors  []Vector `json:"vectors"`
	Rejected []string `json:"rejected_encodings"`
}

type Divergence struct {
	Vector, Stage, Have, Want string
}

func (d Divergence) String() string {
	return fmt.Sprintf("%s: %s: have %s want %s", d.Vector, d.Stage, d.Have, d.Want)
}

func fixed(name, s string, out []byte) error {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != len(out) {
		return fmt.Errorf("%s: not %d hex bytes", name, len(out))
	}
	copy(out, b)
	return nil
}

func hx(b [32]byte) string { return hex.EncodeToString(b[:]) }

// Check recomputes every object of every vector from its inputs and returns
// the first divergence per vector, in the order of the text: reward
// commitment, job bytes, the strict decode round trip, job id, epoch seed,
// fill seed, root, each ticket and its qualification, the challenges of
// ticket 0, the public digest of ticket 0.
func (vf *VectorFile) Check() ([]Divergence, error) {
	var divs []Divergence
	for n, v := range vf.Vectors {
		name := fmt.Sprintf("f0r2-%02d %s", n, v.Name)
		var j Job
		var coinbase [20]byte
		var anchor, prog [32]byte
		j.ChainID, j.Version, j.Height, j.EpochID = v.ChainID, v.Version, v.Height, v.EpochID
		for _, e := range []error{
			fixed("parent_id", v.ParentID, j.ParentID[:]), fixed("params_id", v.ParamsID, j.ParamsID[:]),
			fixed("target", v.Target, j.Target[:]), fixed("tx_commitment", v.TxCommitment, j.TxCommitment[:]),
			fixed("coinbase", v.Coinbase, coinbase[:]), fixed("delayed_anchor", v.Anchor, anchor[:]), fixed("program_id", v.ProgramID, prog[:]),
		} {
			if e != nil {
				return nil, fmt.Errorf("%s: %w", name, e)
			}
		}
		extra, err := hex.DecodeString(v.Extra)
		if err != nil {
			return nil, fmt.Errorf("%s: extra is not hex", name)
		}
		d := func(stage, have, want string) { divs = append(divs, Divergence{name, stage, have, want}) }
		reward, err := RewardCommitment(coinbase, extra)
		if err != nil {
			d("reward commitment", err.Error(), v.Reward)
			continue
		}
		if v.Reward != "" && hx(reward) != v.Reward {
			d("reward commitment", hx(reward), v.Reward)
			continue
		}
		j.Reward = reward
		job := j.Encode()
		if v.Job != "" {
			want, err := hex.DecodeString(v.Job)
			if err != nil {
				return nil, fmt.Errorf("%s: job is not hex", name)
			}
			if k := firstDiff(job, want); k >= 0 {
				d(fmt.Sprintf("job byte %d (%s)", k, jobField(k)), at(job, k), at(want, k))
				continue
			}
		}
		if g, err := Decode(job); err != nil || g != j {
			d("strict decode of the job", fmt.Sprint(err), "the tuple")
			continue
		}
		jobID := JobID(job)
		if v.JobID != "" && hx(jobID) != v.JobID {
			d("job id", hx(jobID), v.JobID)
			continue
		}
		es := EpochSeed(anchor, j.EpochID, j.ParamsID)
		if v.EpochSeed != "" && hx(es) != v.EpochSeed {
			d("epoch seed", hx(es), v.EpochSeed)
			continue
		}
		fs := FillSeed(es, jobID, v.BatchNonce)
		if v.FillSeed != "" && hx(fs) != v.FillSeed {
			d("fill seed", hx(fs), v.FillSeed)
			continue
		}
		labels := make([][]byte, len(v.Labels))
		for i, l := range v.Labels {
			if labels[i], err = hex.DecodeString(l); err != nil {
				return nil, fmt.Errorf("%s: label %d is not hex", name, i)
			}
		}
		root, err := Root(labels)
		if err != nil {
			d("root", err.Error(), v.Root)
			continue
		}
		if v.Root != "" && hx(root) != v.Root {
			d("root", hx(root), v.Root)
			continue
		}
		var t0 [32]byte
		bad := false
		for k := uint32(0); k < v.B && !bad; k++ {
			t, err := Ticket(jobID, root, v.BatchNonce, k, v.B, j.ParamsID)
			if err != nil {
				d(fmt.Sprintf("ticket %d", k), err.Error(), "a ticket")
				bad = true
				break
			}
			if k == 0 {
				t0 = t
			}
			if int(k) < len(v.Tickets) && hx(t) != v.Tickets[k] {
				d(fmt.Sprintf("ticket %d", k), hx(t), v.Tickets[k])
				bad = true
				break
			}
			if int(k) < len(v.Qualifies) && Qualifies(t, j.Target) != v.Qualifies[k] {
				d(fmt.Sprintf("ticket %d qualifies", k), fmt.Sprint(Qualifies(t, j.Target)), fmt.Sprint(v.Qualifies[k]))
				bad = true
				break
			}
		}
		if bad {
			continue
		}
		if _, err := Ticket(jobID, root, v.BatchNonce, v.B, v.B, j.ParamsID); err == nil {
			d("ticket index B", "accepted", "rejected")
			continue
		}
		if len(v.Challenges) > 0 && v.N > 0 {
			ch := Challenges(t0, len(v.Challenges), v.N)
			for k := range ch {
				if ch[k] != v.Challenges[k] {
					d(fmt.Sprintf("challenge %d of ticket 0", k), fmt.Sprint(ch[k]), fmt.Sprint(v.Challenges[k]))
					bad = true
					break
				}
			}
			if bad {
				continue
			}
		}
		pub := Public(prog, j.ChainID, j.Version, jobID, root, v.BatchNonce, 0, v.B, j.ParamsID)
		if v.PublicJ0 != "" && hx(pub) != v.PublicJ0 {
			d("public digest of ticket 0", hx(pub), v.PublicJ0)
		}
	}
	return divs, nil
}

func jobField(k int) string {
	switch {
	case k < 1:
		return "domain length"
	case k < 20:
		return "domain"
	case k < 28:
		return "chain id"
	case k < 32:
		return "version"
	case k < 64:
		return "parent id"
	case k < 72:
		return "height"
	case k < 80:
		return "epoch id"
	case k < 112:
		return "parameter id"
	case k < 144:
		return "target"
	case k < 176:
		return "transaction commitment"
	case k < 208:
		return "reward commitment"
	}
	return "beyond 208"
}

func at(b []byte, k int) string {
	if k < len(b) {
		return fmt.Sprintf("%02x", b[k])
	}
	return "(none)"
}

func firstDiff(a, b []byte) int {
	for k := 0; k < max(len(a), len(b)); k++ {
		if k >= len(a) || k >= len(b) || a[k] != b[k] {
			return k
		}
	}
	return -1
}

func ReadFile(path string) (*VectorFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var vf VectorFile
	if err := json.Unmarshal(raw, &vf); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &vf, nil
}
