//go:build ignpi

package igneum

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/pi"
	"github.com/ethereum/go-ethereum/core/types"
)

// the hash team's 36 rows (pow/vectors/pi/<tag>/pi.json, receipts beside)
// through the NODE's rule: every domain-0 row as a keyed block's header (pk
// as BondKey, the shares as the tail field) with the proof's bytes and the
// row's messages, through verifyPiBytes under --igneum.pi-verify, expecting
// the row's code in the refusal's text; the domain-1 rows (the claim, the
// registry's path once the precompile links) through the verifier directly.
//   go test -tags controls,ignpi ./consensus/igneum/ -run TestPiRowsThroughTheNode -v

type piRow struct {
	Name    string   `json:"name"`
	Vector  int      `json:"vector"`
	Domain  uint8    `json:"domain"`
	K       uint32   `json:"k"`
	PK      string   `json:"pk"`
	M       []string `json:"m"`
	Shares  []string `json:"shares"`
	Receipt string   `json:"receipt"`
	Expect  string   `json:"expect"`
	Flip    *int     `json:"flip,omitempty"` // bad_receipt rows of the 100-row sets: the valid file with this byte flipped
}

type piSet struct {
	ImageID string  `json:"image_id"`
	Rows    []piRow `json:"rows"`
}

func piRowCode(t *testing.T, name, expect string) int32 {
	if expect == "ok" {
		return pi.OK
	}
	switch name {
	case "wrong_sk", "wrong_share", "wrong_pk", "replayed", "claim_wrong_to":
		return pi.EJournal
	case "bad_receipt":
		return pi.EProof
	case "oversize", "claim_composite":
		return pi.EBound
	case "claim_as_block":
		return pi.EKind
	}
	t.Fatalf("a row this test does not know: %s", name)
	return -1
}

func TestPiRowsThroughTheNode(t *testing.T) {
	e := newTestEngine(t)
	if err := e.SetPi(true, "", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join("..", "..", "..", "pow", "vectors", "pi")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	total := 0
	var okMs []float64
	for _, ent := range entries {
		tag := filepath.Join(dir, ent.Name())
		raw, err := os.ReadFile(filepath.Join(tag, "pi.json"))
		if err != nil {
			continue
		}
		var st piSet
		if err := json.Unmarshal(raw, &st); err != nil {
			t.Fatalf("%s: %v", tag, err)
		}
		if st.ImageID != pi.ImageIDHex {
			t.Fatalf("%s: image id %s is not the node's %s", ent.Name(), st.ImageID, pi.ImageIDHex)
		}
		for _, r := range st.Rows {
			var proof []byte
			if r.Name == "oversize" {
				proof = make([]byte, pi.MaxBytes(r.Domain)+1)
				for i := range proof {
					proof[i] = 2
				}
			} else if proof, err = os.ReadFile(filepath.Join(tag, r.Receipt)); err != nil {
				t.Fatalf("%s: %v", r.Receipt, err)
			} else if r.Flip != nil {
				// the 100-row sets' bad_receipt rows name the valid file and
				// the byte to flip (the hash team's reader rule, 97bccec2)
				proof = append([]byte{}, proof...)
				proof[*r.Flip] ^= 1
			}
			pkb, _ := hex.DecodeString(r.PK)
			pk := common.BytesToHash(pkb)
			m := make([]byte, 0, 32*len(r.M))
			for _, x := range r.M {
				b, _ := hex.DecodeString(x)
				m = append(m, b...)
			}
			shares := make([]byte, 0, 32*len(r.Shares))
			for _, x := range r.Shares {
				b, _ := hex.DecodeString(x)
				shares = append(shares, b...)
			}
			want := piRowCode(t, r.Name, r.Expect)
			var got int32
			t0 := time.Now()
			if r.Domain == pi.DomainSeal {
				header := &types.Header{Number: common.Big1, BondKey: &pk, Shares: shares}
				err := e.verifyPiBytes(header, proof, m)
				switch {
				case err == nil:
					got = pi.OK
				case errors.Is(err, errPiRefused):
					got = -1
					for c := int32(1); c <= pi.ENotBuilt; c++ {
						if strings.Contains(err.Error(), ": "+pi.CodeName(c)+" (") {
							got = c
						}
					}
				default:
					t.Fatalf("%s row %s: %v", ent.Name(), r.Name, err)
				}
			} else {
				ms := make([][32]byte, len(r.M))
				ss := make([][32]byte, len(r.Shares))
				for i := range r.M {
					copy(ms[i][:], m[32*i:])
				}
				for i := range r.Shares {
					copy(ss[i][:], shares[32*i:])
				}
				var pk32 [32]byte
				copy(pk32[:], pk[:])
				got = pi.Verify(r.Domain, pk32, ms, ss, proof)
			}
			ms := float64(time.Since(t0).Microseconds()) / 1000
			status := "PASS"
			if got != want {
				status = "FAIL"
			}
			t.Logf("%s %-14s v%d domain %d K %2d %6.2f ms code %d (%s)", status, r.Name, r.Vector, r.Domain, r.K, ms, got, pi.CodeName(got))
			if got != want {
				t.Errorf("%s row %s v%d: code %d, want %d", ent.Name(), r.Name, r.Vector, got, want)
			}
			if r.Expect == "ok" && r.Domain == pi.DomainSeal {
				okMs = append(okMs, ms)
			}
			total++
		}
	}
	if total < 36 {
		t.Fatalf("%d rows, want at least 36 (the two first sets; the 100-row closure sets on top)", total)
	}
	t.Logf("%d rows across every landed pi set", total)
	sort.Float64s(okMs)
	t.Logf("%d rows through the node; the block's valid rows verify in median %.2f ms, max %.2f ms", total, okMs[len(okMs)/2], okMs[len(okMs)-1])
	if okMs[len(okMs)/2] >= 25 {
		t.Errorf("median verify %.2f ms over the node host's 25 ms line", okMs[len(okMs)/2])
	}
}
