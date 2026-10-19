package igneum

import (
	"encoding/json"
	"math/big"
	"os"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/null"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/vector"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// TestVectorPropsCampaign is the node's side of the work-authority design
// seat's VectorWork property campaign (tools/consensus/vectorwork-props/
// props.py, mode 4): with VW_PROPS_CASES naming props.py's --export file,
// every case's prefix and two suffixes are built as headers in the engine
// (the oracle's difficulties and times as given, the registration's four
// references, the case's rule, the campaign's floor D_ref/8 as each
// coordinate's dMin, the campaign's half-life), the node's raw work per
// coordinate at each tip and its fork choice between the tips are written
// to VW_PROPS_ANSWERS in the comparator's form ({"answers": [{"id", "N_a",
// "N_b", "choice"}]}), and props.py --compare names every divergence by
// case. Skipped without the env.
func TestVectorPropsCampaign(t *testing.T) {
	in, out := os.Getenv("VW_PROPS_CASES"), os.Getenv("VW_PROPS_ANSWERS")
	if in == "" || out == "" {
		t.Skip("VW_PROPS_CASES and VW_PROPS_ANSWERS name the campaign's files")
	}
	type block struct {
		Coord uint8  `json:"coord"`
		D     uint64 `json:"d"`
		T     uint64 `json:"t"`
		Key   string `json:"key,omitempty"` // the sealer's key (props.py's draw-value export, main 32e135d0)
	}
	type oracle struct {
		NA     []uint64 `json:"N_a"`
		NB     []uint64 `json:"N_b"`
		Choice string   `json:"choice"`
	}
	type cse struct {
		ID      int      `json:"id"`
		Rule    string   `json:"rule"`
		DRef    []uint64 `json:"d_ref"` // the revision-5 export
		Reg     struct {        // the revision-10 export (main 32e135d0): the registration per case
			DRef []uint64 `json:"d_ref"`
			DMin []uint64 `json:"d_min"`
			Caps []uint64 `json:"caps"`
		} `json:"reg"`
		T0      uint64   `json:"t0"`
		Prefix  []block  `json:"prefix"`
		SuffixA []block  `json:"suffix_a"`
		SuffixB []block  `json:"suffix_b"`
		Oracle  oracle   `json:"oracle"`
	}
	var file struct {
		Constants struct {
			TCoord   uint64 `json:"T_COORD"`
			HalfLife uint64 `json:"HALF_LIFE"`
			FloorDiv uint64 `json:"FLOOR_DIV"`
		} `json:"constants"`
		Cases []cse `json:"cases"`
	}
	raw, err := os.ReadFile(in)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	type answer struct {
		ID     int      `json:"id"`
		NA     []uint64 `json:"N_a"`
		NB     []uint64 `json:"N_b"`
		Choice string   `json:"choice"`
		Tie    bool     `json:"tie,omitempty"`
		// The node's draw-value terms (props.py's export of 13 October
		// 2026): the seed is the common ancestor's header hash as the
		// node builds it (the harness's chains have no epoch table), the
		// key each block's bond key from the case, so a comparator reads
		// the node's lot from these and not from its own synthetic hash.
		DrawSeed string `json:"draw_seed,omitempty"`
		XA       string `json:"x_a,omitempty"`
		XB       string `json:"x_b,omitempty"`
	}
	var answers []answer
	ties, mism := 0, 0
	for _, c := range file.Cases {
		// props.py names the rule GEO or MIN; the registration's names
		// are geometric and minimum (the project's exporter, 13 October 2026).
		rule := map[string]string{"GEO": "geometric", "MIN": "minimum"}[c.Rule]
		if rule == "" {
			rule = c.Rule
		}
		// The revision-10 export registers per case: the references, the
		// floors, the lag caps per coordinate and the genesis seed on;
		// the revision-5 export carried d_ref alone (floor by FLOOR_DIV,
		// no cap, unseeded).
		seeded := len(c.Reg.DRef) > 0
		if seeded {
			c.DRef = c.Reg.DRef
		}
		v := &ctypes.IgneumVector{HalfLife: file.Constants.HalfLife, DtMax: file.Constants.HalfLife, Rule: rule, SeedGenesis: seeded}
		for i, d := range c.DRef {
			cc := ctypes.IgneumCoordinate{Name: []string{"tensor", "bandwidth", "fabric", "graph"}[i], Algorithm: null.Name, Target: file.Constants.TCoord, DRef: d}
			if seeded {
				cc.DMin, cc.LagCap = c.Reg.DMin[i], c.Reg.Caps[i]
			} else {
				cc.DMin = d / file.Constants.FloorDiv
			}
			v.Coordinates = append(v.Coordinates, cc)
		}
		m, err := vector.New(v, pow.NewDev)
		if err != nil {
			t.Fatalf("case %d: %v", c.ID, err)
		}
		parts := TestParts()
		parts.Difficulty.Min = big.NewInt(1)
		e, err := New(Config{Algorithm: m, Parts: parts})
		if err != nil {
			t.Fatalf("case %d: %v", c.ID, err)
		}
		chain := newForkChain()
		g := genesisHeader()
		g.Time = c.T0
		g.Difficulty = new(big.Int).SetUint64(c.DRef[0])
		chain.setCanonical(g)
		e.anchor(chain)
		mk := func(parent *types.Header, b block, tag string) *types.Header {
			h := &types.Header{ParentHash: parent.Hash(), UncleHash: types.EmptyUncleHash, Number: new(big.Int).Add(parent.Number, big.NewInt(1)), Time: b.T, GasLimit: parent.GasLimit, Difficulty: new(big.Int).SetUint64(b.D), Extra: []byte(tag)}
			h.SetCoordTag(b.Coord)
			if b.Key != "" {
				k := common.HexToHash(b.Key)
				h.BondKey = &k
				h.Coinbase = common.BytesToAddress(k[:20])
			}
			return h
		}
		p := g
		for _, b := range c.Prefix {
			h := mk(p, b, "p")
			chain.setCanonical(h)
			p = h
		}
		a, bb := p, p
		for _, b := range c.SuffixA {
			h := mk(a, b, "a")
			chain.add(h)
			a = h
		}
		for _, b := range c.SuffixB {
			h := mk(bb, b, "b")
			chain.add(h)
			bb = h
		}
		sa, err := e.vectorStateOf(chain, a)
		if err != nil {
			t.Fatalf("case %d A: %v", c.ID, err)
		}
		sb, err := e.vectorStateOf(chain, bb)
		if err != nil {
			t.Fatalf("case %d B: %v", c.ID, err)
		}
		na, nb := make([]uint64, len(c.DRef)), make([]uint64, len(c.DRef))
		for i := range c.DRef {
			na[i], nb[i] = sa.work[i].Uint64(), sb.work[i].Uint64()
		}
		wa, sumA, _ := e.vectorWeight(chain, a)
		wb, sumB, _ := e.vectorWeight(chain, bb)
		// The choice is the node's fork rule whole (PreferBranch: the
		// weight, the second key, the draw value, the block hash); the
		// tie flag reads whether the first and second keys were equal.
		tie := wb.Cmp(wa) == 0 && (sumA == nil || sumB == nil || sumA.Cmp(sumB) == 0)
		if tie {
			ties++
		}
		// The forker's order (core/forkchoice.go): PreferByWeight first
		// (the weight, the second key, then ForkTieBreak on the draw
		// value and the block hash); PreferBranch alone is the
		// single-chain order with the vector tie and never the weight.
		preferB, decided, err := e.PreferByWeight(chain, a, bb)
		if err != nil {
			t.Fatalf("case %d: fork rule: %v", c.ID, err)
		}
		if !decided {
			t.Fatalf("case %d: the vector fork rule did not decide", c.ID)
		}
		choice := "A"
		if preferB {
			choice = "B"
		}
		if choice != c.Oracle.Choice {
			mism++
		}
		ans := answer{ID: c.ID, NA: na, NB: nb, Choice: choice, Tie: tie}
		if len(c.SuffixA) > 0 && len(c.SuffixB) > 0 {
			ans.DrawSeed = p.Hash().Hex()
			ans.XA = e.drawValue(chain, p, a).Text(16)
			ans.XB = e.drawValue(chain, p, bb).Text(16)
		}
		answers = append(answers, ans)
		e.Close()
		_ = common.Hash{}
	}
	outBytes, _ := json.Marshal(map[string]any{"answers": answers, "node": "consensus/igneum vectorWeight + PreferByWeight's order", "ties": ties})
	if err := os.WriteFile(out, outBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("campaign: %d cases answered, %d ties by the first and second keys, %d choices differ from the oracle (named by props.py --compare)", len(answers), ties, mism)
}
