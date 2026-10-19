package mosaic

import (
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

const vectorsPath = "../../../../pow/vectors/mosaic/vectors.json"

func bi(s string) *big.Int {
	x, ok := new(big.Int).SetString(s, 10)
	if !ok {
		panic(s)
	}
	return x
}

func TestWorkBounds(t *testing.T) {
	for _, s := range []string{"0", "-1", "115792089237316195423570985008687907853269984665640564039457584007913129639936"} {
		if _, err := Work(bi(s)); err == nil {
			t.Errorf("target %s accepted", s)
		}
	}
	w, _ := Work(maxTarget)
	if w.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("work at the maximum target: %s", w)
	}
	w, _ = Work(big.NewInt(1))
	if w.Cmp(new(big.Int).Lsh(big.NewInt(1), 255)) != 0 {
		t.Fatalf("work at target 1: %s", w)
	}
}

// Work256 equals Work on every target class (M10: the 256-bit form).
func TestWork256(t *testing.T) {
	targets := []*big.Int{big.NewInt(1), big.NewInt(2), big.NewInt(3), Target(big.NewInt(1000000)), new(big.Int).Lsh(big.NewInt(1), 255), new(big.Int).Sub(maxTarget, big.NewInt(1)), maxTarget}
	for i := 0; i < 64; i++ {
		targets = append(targets, new(big.Int).Lsh(big.NewInt(int64(i+1)), uint(4*i)))
	}
	for _, T := range targets {
		a, _ := Work(T)
		b, _ := Work256(T)
		if a.Cmp(b) != 0 {
			t.Fatalf("target %s: %s against %s", T, a, b)
		}
	}
}

func TestNormalise(t *testing.T) {
	w, _ := Work(Target(big.NewInt(1000)))
	n, _ := Normalise(w, w)
	if n.Cmp(NormScale) != 0 {
		t.Fatal("a block at the reference difficulty is not one unit")
	}
	w2, _ := Work(Target(big.NewInt(2000)))
	n2, _ := Normalise(w2, w)
	// work(T(D)) is D - 1 at small D by the two floors (the document's
	// section 4), so the ratio is 1999/999, within a part in 256 of two.
	diff := new(big.Int).Sub(n2, new(big.Int).Lsh(NormScale, 1))
	if diff.Abs(diff).Cmp(new(big.Int).Rsh(NormScale, 8)) > 0 {
		t.Fatalf("double difficulty is not about two units: %s", n2)
	}
	if _, err := Normalise(w, new(big.Int)); err == nil {
		t.Fatal("zero reference accepted")
	}
}

func units(xs ...int64) []*big.Int {
	out := make([]*big.Int, len(xs))
	for i, x := range xs {
		out[i] = new(big.Int).Mul(big.NewInt(x), NormScale)
	}
	return out
}

// The lemmas of MOSAIC-VF-SPEC.md section 4 on the integer score.
func TestScoreLemmas(t *testing.T) {
	w := Research
	s := Score(units(1, 1, 1, 1, 1), w)
	// Lemma 1: monotone.
	if Score(units(2, 1, 1, 1, 1), w).Cmp(s) < 0 {
		t.Fatal("not monotone")
	}
	// Lemma 2: one non-minimum lane grows the score by e x only.
	d := new(big.Int).Sub(Score(units(3, 1, 1, 1, 1), w), Score(units(2, 1, 1, 1, 1), w))
	if d.Cmp(new(big.Int).Mul(new(big.Int).SetUint64(w.ENum*w.ADen), NormScale)) != 0 {
		t.Fatalf("single-lane influence: %s", d)
	}
	// Lemma 3: a one-lane attacker needs 255x to match five honest lanes at 1x.
	honest := Score(units(1, 1, 1, 1, 1), w)
	if Score(units(254, 0, 0, 0, 0), w).Cmp(honest) >= 0 || Score(units(255, 0, 0, 0, 0), w).Cmp(honest) < 0 {
		t.Fatal("the one-lane threshold is not 255x")
	}
	// Lemma 4: e = 0 and an empty lane score zero.
	if Score(units(9, 9, 9, 9, 0), Weights{5, 1, 0, 1}).Sign() != 0 {
		t.Fatal("hard balance")
	}
	// Lemma 5: e > 0 and an empty lane still score.
	if Score(units(9, 9, 9, 9, 0), w).Sign() <= 0 {
		t.Fatal("degraded liveness")
	}
}

// The lane table's key reproduces the hash team's register (LANES.md
// section 2): lane (1, 1), igneumhash at the proposed point.
func TestLanePID(t *testing.T) {
	p := common.HexToHash("0x1a86ebf11cc1a06ae58de541438a484e12bc7a3e30f0d8b4f2de275112328357")
	got := LanePID(1, 1, "igneumhash", p)
	if got.Hex()[:10] != "0x46e45320" {
		t.Fatalf("lane (1, 1): %s", got.Hex())
	}
	if LanePID(1, 2, "igneumhash", p) == got || LanePID(2, 1, "igneumhash", p) == got || LanePID(1, 1, "rxe1", p) == got {
		t.Fatal("the key ignores a field")
	}
	W, err := Accumulate(nil, 5, 5, NormScale)
	if err != nil || len(W) != 5 || W[4].Cmp(NormScale) != 0 {
		t.Fatal("accumulate lane 5")
	}
	if _, err := Accumulate(nil, 0, 5, NormScale); err == nil {
		t.Fatal("lane 0 accepted")
	}
}

// The hardened score (v0.2 section 3): equal quotas rescale v0.1; a
// one-lane attacker at a 20 percent quota needs 255 units, at the 5 percent
// sidecar 1,020; a missing sidecar leaves about 1.86 percent of balanced
// growth, a missing core lane about 1.50 percent.
func TestHardenedScore(t *testing.T) {
	if err := ValidateQuotas(HardenedQuotas); err != nil {
		t.Fatal(err)
	}
	if err := ValidateQuotas([]uint32{5000, 5000, 1}); err == nil {
		t.Fatal("a bad sum accepted")
	}
	equal := []uint32{2000, 2000, 2000, 2000, 2000}
	// Equal quotas: ScoreQuota at A = 1 equals Score at A = 5 (U = 5 W).
	if ScoreQuota(units(3, 1, 1, 1, 1), equal, Hardened).Cmp(Score(units(3, 1, 1, 1, 1), Research)) != 0 {
		t.Fatal("equal quotas do not rescale v0.1")
	}
	// Balanced honest service at the quotas: W_i = q_i units, U_i = 1 unit each.
	bal := []*big.Int{}
	for _, q := range HardenedQuotas {
		bal = append(bal, new(big.Int).Div(new(big.Int).Mul(NormScale, big.NewInt(int64(q))), big.NewInt(QuotaScale)))
	}
	honest := ScoreQuota(bal, HardenedQuotas, Hardened)
	// A sidecar-only attacker: r x q_5 units in lane 5; matches at r = 1020.
	side := func(r int64) []*big.Int {
		W := units(0, 0, 0, 0, 0)
		W[4] = new(big.Int).Div(new(big.Int).Mul(NormScale, big.NewInt(r*500)), big.NewInt(QuotaScale))
		return W
	}
	if ScoreQuota(side(1019), HardenedQuotas, Hardened).Cmp(honest) >= 0 || ScoreQuota(side(1021), HardenedQuotas, Hardened).Cmp(honest) < 0 {
		t.Fatalf("the sidecar threshold is not 1020x: %s %s %s", ScoreQuota(side(1019), HardenedQuotas, Hardened), honest, ScoreQuota(side(1021), HardenedQuotas, Hardened))
	}
	// A missing sidecar: growth e x (1 - q5) / (A + e) of balanced.
	noSide := append([]*big.Int{}, bal...)
	noSide[4] = new(big.Int)
	ratio := new(big.Rat).SetFrac(ScoreQuota(noSide, HardenedQuotas, Hardened), honest)
	if f, _ := ratio.Float64(); f < 0.0185 || f > 0.0188 {
		t.Fatalf("a missing sidecar leaves %.4f of growth, the text says 0.0186", f)
	}
	noCore := append([]*big.Int{}, bal...)
	noCore[0] = new(big.Int)
	ratio = new(big.Rat).SetFrac(ScoreQuota(noCore, HardenedQuotas, Hardened), honest)
	if f, _ := ratio.Float64(); f < 0.0149 || f > 0.0151 {
		t.Fatalf("a missing core lane leaves %.4f of growth, the text says 0.0150", f)
	}
}

func TestValidateMenu(t *testing.T) {
	acts := []Activation{{Height: 20000, Lane: 1}, {Height: 30000, Quotas: HardenedQuotas}}
	if err := ValidateMenu(acts, 10000, 4096); err != nil {
		t.Fatal(err)
	}
	if err := ValidateMenu([]Activation{{Height: 20000, Lane: 1}, {Height: 20000, Lane: 2}}, 10000, 4096); err == nil {
		t.Fatal("two changes at one height accepted")
	}
	if err := ValidateMenu([]Activation{{Height: 15000, Lane: 1}}, 10000, 4096); err == nil {
		t.Fatal("an activation under two windows accepted")
	}
	if err := ValidateMenu([]Activation{{Height: 30000, Quotas: []uint32{5000, 5000, 5000}}}, 10000, 4096); err == nil {
		t.Fatal("a bad quota schedule accepted")
	}
}

func TestCompare(t *testing.T) {
	a := Tip{Score: big.NewInt(10), Height: 5, Hash: common.HexToHash("0x02")}
	b := Tip{Score: big.NewInt(10), Height: 5, Hash: common.HexToHash("0x01")}
	if Compare(a, b) != -1 || Compare(b, a) != 1 {
		t.Fatal("the lower hash does not win the tie")
	}
	c := Tip{Score: big.NewInt(10), Height: 6, Hash: common.HexToHash("0xff")}
	if Compare(c, a) != 1 {
		t.Fatal("height does not break the score tie")
	}
	d := Tip{Score: big.NewInt(11), Height: 1, Hash: common.HexToHash("0xff")}
	if Compare(d, c) != 1 {
		t.Fatal("score does not come first")
	}
	if Compare(a, a) != 0 {
		t.Fatal("same tip")
	}
}

func TestSuccession(t *testing.T) {
	lanes := []Lane{
		{ID: 1, Versions: []LaneVersion{{Version: 1, Height: 0}, {Version: 2, Height: 20000}}},
		{ID: 2, Versions: []LaneVersion{{Version: 1, Height: 0}, {Version: 2, Height: 30000}}},
	}
	if err := ValidateSuccession(lanes, 10000, 4096); err != nil {
		t.Fatal(err)
	}
	lanes[1].Versions[1].Height = 20000
	if err := ValidateSuccession(lanes, 10000, 4096); err == nil {
		t.Fatal("two lanes at one height accepted")
	}
	lanes[1].Versions[1].Height = 15000
	if err := ValidateSuccession(lanes, 10000, 4096); err == nil {
		t.Fatal("an activation under two intervals ahead accepted")
	}
	lanes[1].Versions[1].Height = 0
	if err := ValidateSuccession(lanes, 10000, 4096); err == nil {
		t.Fatal("a non-ascending version accepted")
	}
}

type workVec struct {
	Name       string `json:"name"`
	Difficulty string `json:"difficulty,omitempty"`
	Target     string `json:"target"`
	Work       string `json:"work"`
	Work256    string `json:"work_256_form"`
}
type normVec struct {
	Name       string `json:"name"`
	Work       string `json:"work"`
	Ref        string `json:"ref_work"`
	Normalised string `json:"normalised"`
}
type scoreVec struct {
	Name    string   `json:"name"`
	W       []string `json:"W"`
	Score   string   `json:"score"`
	Weights Weights  `json:"weights"`
}
type chainVec struct {
	Name   string     `json:"name"`
	Epochs [][]string `json:"epochs"`
	Score  string     `json:"chain_score"`
}
type hardenedVec struct {
	Name   string   `json:"name"`
	Quotas []uint32 `json:"quotas_bps"`
	W      []string `json:"W"`
	U      []string `json:"U"`
	Score  string   `json:"score"`
}
type recoveryVec struct {
	Name          string   `json:"name"`
	RefDifficulty string   `json:"reference_difficulty"`
	RefWork       string   `json:"reference_work"`
	Floor         string   `json:"floor"`
	FloorDiff     string   `json:"floor_difficulty"`
	EasedWork     string   `json:"eased_block_work"`
	EasedNorm     string   `json:"eased_block_normalised"`
	Before        []string `json:"W_before"`
	AfterEased    []string `json:"W_after_four_eased"`
	AfterFull     []string `json:"W_after_four_at_reference"`
	ScoreBefore   string   `json:"score_before"`
	ScoreEased    string   `json:"score_after_four_eased"`
	ScoreFull     string   `json:"score_after_four_at_reference"`
}
type compareVec struct {
	Name   string `json:"name"`
	A      Tip    `json:"-"`
	AScore string `json:"a_score"`
	AH     uint64 `json:"a_height"`
	AHash  string `json:"a_hash"`
	BScore string `json:"b_score"`
	BH     uint64 `json:"b_height"`
	BHash  string `json:"b_hash"`
	Result int    `json:"result"`
}
type vectorFile struct {
	Relation   string        `json:"relation"`
	NormScale  string        `json:"norm_scale"`
	Weights    Weights       `json:"research_weights"`
	ScoreScale string        `json:"score_scale"`
	EpochLen   uint64        `json:"epoch_length_screen"`
	Work       []workVec     `json:"work"`
	Norm       []normVec     `json:"normalise"`
	Scores     []scoreVec    `json:"score"`
	Chains     []chainVec    `json:"chain_score"`
	Recovery   []recoveryVec `json:"recovery"`
	Hardened   []hardenedVec `json:"hardened_score"`
	HardenedW  Weights       `json:"hardened_weights"`
	Compares   []compareVec  `json:"compare"`
}

func makeVectors() vectorFile {
	f := vectorFile{Relation: "Mosaic-VF Phase A consensus objects (docs/igneumhash/mosaic/PHASE-A-CONSENSUS.md)", NormScale: NormScale.String(), Weights: Research, ScoreScale: Research.Scale().String(), EpochLen: 100}
	type tc struct {
		name string
		diff *big.Int
		T    *big.Int
	}
	cases := []tc{{"difficulty 1 (maximum target)", big.NewInt(1), nil}, {"difficulty 2", big.NewInt(2), nil}, {"difficulty 1000000", big.NewInt(1000000), nil}, {"difficulty 2^64", new(big.Int).Lsh(big.NewInt(1), 64), nil}, {"target 1", nil, big.NewInt(1)}, {"target 2^255", nil, new(big.Int).Lsh(big.NewInt(1), 255)}, {"target 2^256 - 2", nil, new(big.Int).Sub(maxTarget, big.NewInt(1))}, {"target 12345678901234567890", nil, bi("12345678901234567890")}}
	for _, c := range cases {
		T := c.T
		if T == nil {
			T = Target(c.diff)
		}
		w, _ := Work(T)
		w2, _ := Work256(T)
		v := workVec{Name: c.name, Target: T.String(), Work: w.String(), Work256: w2.String()}
		if c.diff != nil {
			v.Difficulty = c.diff.String()
		}
		f.Work = append(f.Work, v)
	}
	ref, _ := Work(Target(big.NewInt(1000000)))
	for _, c := range []struct {
		name string
		diff int64
	}{{"at the reference", 1000000}, {"double the reference", 2000000}, {"half the reference", 500000}, {"one third", 333333}} {
		w, _ := Work(Target(big.NewInt(c.diff)))
		n, _ := Normalise(w, ref)
		f.Norm = append(f.Norm, normVec{c.name, w.String(), ref.String(), n.String()})
	}
	strs := func(W []*big.Int) []string {
		out := make([]string, len(W))
		for i, x := range W {
			out[i] = x.String()
		}
		return out
	}
	for _, c := range []struct {
		name string
		W    []*big.Int
	}{{"five lanes at one unit", units(1, 1, 1, 1, 1)}, {"one lane ahead", units(3, 1, 1, 1, 1)}, {"one lane empty", units(9, 9, 9, 9, 0)}, {"one-lane attacker at 255", units(255, 0, 0, 0, 0)}, {"uneven", units(7, 2, 5, 3, 11)}, {"fractional units", []*big.Int{bi("18446744073709551616"), bi("9223372036854775808"), bi("27670116110564327424"), bi("18446744073709551615"), bi("1")}}} {
		f.Scores = append(f.Scores, scoreVec{c.name, strs(c.W), Score(c.W, Research).String(), Research})
	}
	chains := []struct {
		name   string
		epochs [][]*big.Int
	}{{"three epochs, balanced", [][]*big.Int{units(100, 100, 100, 100, 100), units(100, 100, 100, 100, 100), units(40, 40, 40, 40, 40)}}, {"three epochs, one lane lost in the last", [][]*big.Int{units(100, 100, 100, 100, 100), units(100, 100, 100, 100, 100), units(40, 40, 40, 40, 0)}}, {"ancient imbalance does not reach the present", [][]*big.Int{units(1000, 0, 0, 0, 0), units(10, 10, 10, 10, 10)}}}
	for _, c := range chains {
		var es [][]string
		for _, e := range c.epochs {
			es = append(es, strs(e))
		}
		f.Chains = append(f.Chains, chainVec{c.name, es, ChainScore(c.epochs, Research).String()})
	}
	// Recovery (section 6.1): four eased blocks at the floor (a quarter of the
	// reference difficulty) against four blocks at the reference.
	{
		refD := big.NewInt(1000000)
		refW, _ := Work(Target(refD))
		st := LaneState{Difficulty: refD, RefWork: refW, FloorNum: ResearchFloor[0], FloorDen: ResearchFloor[1], SilenceDifficulty: refD}
		floorD := st.FloorDifficulty()
		easedW, _ := Work(Target(floorD))
		easedN, _ := Normalise(easedW, refW)
		before := units(10, 10, 10, 10, 0)
		eased := units(10, 10, 10, 10, 0)
		eased[4] = new(big.Int).Mul(easedN, big.NewInt(4))
		full := units(10, 10, 10, 10, 4)
		f.Recovery = append(f.Recovery, recoveryVec{"recovery blocks raise the minimum by intrinsic work only", refD.String(), refW.String(), "1/4", floorD.String(), easedW.String(), easedN.String(), strs(before), strs(eased), strs(full), Score(before, Research).String(), Score(eased, Research).String(), Score(full, Research).String()})
	}
	// The hardened score (v0.2): balanced service at the default quotas, a
	// sidecar-only attacker at 1020x, a missing sidecar, a missing core lane,
	// equal quotas (the v0.1 rescaling).
	f.HardenedW = Hardened
	{
		bal := []*big.Int{}
		for _, q := range HardenedQuotas {
			bal = append(bal, new(big.Int).Div(new(big.Int).Mul(NormScale, big.NewInt(int64(q))), big.NewInt(QuotaScale)))
		}
		side := units(0, 0, 0, 0, 0)
		side[4] = new(big.Int).Div(new(big.Int).Mul(NormScale, big.NewInt(1020*500)), big.NewInt(QuotaScale))
		noSide := append([]*big.Int{}, bal...)
		noSide[4] = new(big.Int)
		noCore := append([]*big.Int{}, bal...)
		noCore[0] = new(big.Int)
		equal := []uint32{2000, 2000, 2000, 2000, 2000}
		for _, c := range []struct {
			name string
			q    []uint32
			W    []*big.Int
		}{{"balanced service at the default quotas", HardenedQuotas, bal}, {"sidecar-only attacker at 1020x", HardenedQuotas, side}, {"missing sidecar", HardenedQuotas, noSide}, {"missing core lane", HardenedQuotas, noCore}, {"equal quotas, one lane ahead", equal, units(3, 1, 1, 1, 1)}} {
			var U []string
			for i, x := range c.W {
				U = append(U, Service(x, c.q[i]).String())
			}
			f.Hardened = append(f.Hardened, hardenedVec{c.name, c.q, strs(c.W), U, ScoreQuota(c.W, c.q, Hardened).String()})
		}
	}
	cmp := []struct {
		name string
		a, b Tip
	}{{"greater score wins", Tip{big.NewInt(11), 1, common.HexToHash("0xff")}, Tip{big.NewInt(10), 9, common.HexToHash("0x00")}}, {"equal score, greater height wins", Tip{big.NewInt(10), 6, common.HexToHash("0xff")}, Tip{big.NewInt(10), 5, common.HexToHash("0x00")}}, {"equal score and height, lower hash wins", Tip{big.NewInt(10), 5, common.HexToHash("0x02")}, Tip{big.NewInt(10), 5, common.HexToHash("0x01")}}}
	for _, c := range cmp {
		f.Compares = append(f.Compares, compareVec{Name: c.name, AScore: c.a.Score.String(), AH: c.a.Height, AHash: c.a.Hash.Hex(), BScore: c.b.Score.String(), BH: c.b.Height, BHash: c.b.Hash.Hex(), Result: Compare(c.a, c.b)})
	}
	return f
}

// TestVectors checks the published vectors; WRITE_TEST_FILES=1 regenerates.
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
	if f.NormScale != NormScale.String() || f.Weights != Research {
		t.Fatal("vector file header")
	}
	for _, v := range f.Work {
		T := bi(v.Target)
		if v.Difficulty != "" && Target(bi(v.Difficulty)).Cmp(T) != 0 {
			t.Errorf("%s: target", v.Name)
		}
		w, _ := Work(T)
		w2, _ := Work256(T)
		if w.String() != v.Work || w2.String() != v.Work256 {
			t.Errorf("%s: work", v.Name)
		}
	}
	for _, v := range f.Norm {
		n, _ := Normalise(bi(v.Work), bi(v.Ref))
		if n.String() != v.Normalised {
			t.Errorf("%s: normalised", v.Name)
		}
	}
	parse := func(ss []string) []*big.Int {
		out := make([]*big.Int, len(ss))
		for i, s := range ss {
			out[i] = bi(s)
		}
		return out
	}
	for _, v := range f.Scores {
		if Score(parse(v.W), v.Weights).String() != v.Score {
			t.Errorf("%s: score", v.Name)
		}
	}
	for _, v := range f.Chains {
		var es [][]*big.Int
		for _, e := range v.Epochs {
			es = append(es, parse(e))
		}
		if ChainScore(es, Research).String() != v.Score {
			t.Errorf("%s: chain score", v.Name)
		}
	}
	for _, v := range f.Recovery {
		st := LaneState{Difficulty: bi(v.RefDifficulty), FloorNum: ResearchFloor[0], FloorDen: ResearchFloor[1], SilenceDifficulty: bi(v.RefDifficulty)}
		fd := st.FloorDifficulty()
		ew, _ := Work(Target(fd))
		en, _ := Normalise(ew, bi(v.RefWork))
		if fd.String() != v.FloorDiff || ew.String() != v.EasedWork || en.String() != v.EasedNorm {
			t.Errorf("%s: eased block", v.Name)
		}
		if Score(parse(v.Before), Research).String() != v.ScoreBefore || Score(parse(v.AfterEased), Research).String() != v.ScoreEased || Score(parse(v.AfterFull), Research).String() != v.ScoreFull {
			t.Errorf("%s: scores", v.Name)
		}
		// Four eased blocks lift the empty lane to about one unit, not four.
		eased := parse(v.AfterEased)[4]
		if eased.Cmp(new(big.Int).Rsh(NormScale, 1)) < 0 || eased.Cmp(new(big.Int).Lsh(NormScale, 1)) > 0 {
			t.Errorf("%s: eased component %s", v.Name, eased)
		}
	}
	for _, v := range f.Hardened {
		W := parse(v.W)
		for i, x := range W {
			if Service(x, v.Quotas[i]).String() != v.U[i] {
				t.Errorf("%s: U %d", v.Name, i)
			}
		}
		if ScoreQuota(W, v.Quotas, f.HardenedW).String() != v.Score {
			t.Errorf("%s: hardened score", v.Name)
		}
	}
	for _, v := range f.Compares {
		a := Tip{bi(v.AScore), v.AH, common.HexToHash(v.AHash)}
		b := Tip{bi(v.BScore), v.BH, common.HexToHash(v.BHash)}
		if Compare(a, b) != v.Result || Compare(b, a) != -v.Result {
			t.Errorf("%s: compare", v.Name)
		}
	}
}
