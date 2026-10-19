//go:build ignpi

package pi

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// The landed pi sets (pow/vectors/pi/<tag>/pi.json, receipts beside it) through Verify exactly as the node will call
// it: every row's code against the reason it was built for, and the valid block rows' verify time (budget 20 ms on a
// desktop-class core, 25 ms on the node host's; PI-NOTES.md section 3).
//   go test -tags ignpi ./consensus/igneum/pow/pi/ -run . -v     (from node/, the staticlib built first)

type row struct {
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

type set struct {
	ImageID string `json:"image_id"`
	NoClaim bool   `json:"no_claim"`
	Rows    []row  `json:"rows"`
}

func hex32(t *testing.T, s string) [32]byte {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 32 {
		t.Fatalf("hex32 %q: %v", s, err)
	}
	var a [32]byte
	copy(a[:], b)
	return a
}

func expectedCode(t *testing.T, name, expect string) int32 {
	if expect == "ok" {
		return OK
	}
	switch name {
	case "wrong_sk", "wrong_share", "wrong_pk", "replayed", "claim_wrong_to":
		return EJournal
	case "bad_receipt":
		return EProof
	case "oversize", "claim_composite":
		return EBound
	case "claim_as_block":
		return EKind
	}
	t.Fatalf("a row this test does not know: %s", name)
	return -1
}

func TestConstants(t *testing.T) {
	if ABIVersion() != 1 {
		t.Fatalf("ABI version %d, want 1", ABIVersion())
	}
	if LibraryImageIDHex() != ImageIDHex {
		t.Fatalf("the library's PI_IMAGE_ID %s is not the package's %s", LibraryImageIDHex(), ImageIDHex)
	}
	var z [32]byte
	if c := Verify(2, z, [][32]byte{z}, [][32]byte{z}, []byte{2, 0}); c != EArg {
		t.Fatalf("domain 2: code %d", c)
	}
	if c := Verify(0, z, nil, nil, []byte{2, 0}); c != EArg {
		t.Fatalf("k 0: code %d", c)
	}
	if c := Verify(0, z, [][32]byte{z}, [][32]byte{z}, nil); c != ESystem {
		t.Fatalf("empty pi: code %d", c)
	}
	if c := Verify(0, z, [][32]byte{z}, [][32]byte{z}, []byte{1, 0, 0}); c != ESystem {
		t.Fatalf("identifier 1: code %d", c)
	}
	if c := Verify(0, z, [][32]byte{z}, [][32]byte{z}, []byte{2, 0, 0}); c != EDecode {
		t.Fatalf("garbage: code %d", c)
	}
	if c := Verify(1, z, [][32]byte{z}, [][32]byte{z}, make([]byte, MaxBytesClaim+1)); c != EBound {
		t.Fatalf("over the claim bound: code %d", c)
	}
}

func TestLandedSets(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "..", "..", "pow", "vectors", "pi")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	total := 0
	var okMs []float64
	for _, e := range entries {
		tag := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(filepath.Join(tag, "pi.json"))
		if err != nil {
			continue
		}
		var st set
		if err := json.Unmarshal(raw, &st); err != nil {
			t.Fatalf("%s: %v", tag, err)
		}
		if st.ImageID != ImageIDHex {
			t.Fatalf("%s: image id %s is not the pinned %s", e.Name(), st.ImageID, ImageIDHex)
		}
		for _, r := range st.Rows {
			var pi []byte
			if r.Name == "oversize" {
				pi = make([]byte, MaxBytes(r.Domain)+1)
				for i := range pi {
					pi[i] = 2
				}
			} else {
				pi, err = os.ReadFile(filepath.Join(tag, r.Receipt))
				if err != nil {
					t.Fatalf("%s: %v", r.Receipt, err)
				}
				if r.Flip != nil {
					pi[*r.Flip] ^= 1
				}
			}
			pk := hex32(t, r.PK)
			m := make([][32]byte, len(r.M))
			s := make([][32]byte, len(r.Shares))
			for i := range r.M {
				m[i] = hex32(t, r.M[i])
			}
			for i := range r.Shares {
				s[i] = hex32(t, r.Shares[i])
			}
			t0 := time.Now()
			code := Verify(r.Domain, pk, m, s, pi)
			ms := float64(time.Since(t0).Microseconds()) / 1000
			want := expectedCode(t, r.Name, r.Expect)
			status := "PASS"
			if code != want {
				status = "FAIL"
			}
			t.Logf("%s %-14s v%d domain %d K %2d %6.2f ms code %d (%s)", status, r.Name, r.Vector, r.Domain, r.K, ms, code, CodeName(code))
			if code != want {
				t.Errorf("%s row %s v%d: code %d, want %d", e.Name(), r.Name, r.Vector, code, want)
			}
			if r.Expect == "ok" && r.Domain == DomainSeal {
				okMs = append(okMs, ms)
			}
			total++
		}
	}
	if total < 36 {
		t.Fatalf("%d rows, the first two landed sets alone carry 36", total)
	}
	sort.Float64s(okMs)
	median := okMs[len(okMs)/2]
	t.Logf("%d rows; the block's valid rows verify in median %.2f ms, max %.2f ms", total, median, okMs[len(okMs)-1])
	if median >= 25 {
		t.Errorf("median verify %.2f ms over the node host's 25 ms line", median)
	}
}
