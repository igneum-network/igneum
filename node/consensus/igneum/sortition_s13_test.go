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

package igneum

// S13, the Sybil property campaign (GUARD-C-CLOSURE.md 2.4, the project's order of
// 17:0x UK, 11 October 2026; the security review's harness). The oracle is
// this package's own functions, never a re-implementation: unitPrice and
// unitsOf (sortition_genesis.go) for u and the units; Lot and Theta
// (sortition.go) for the round-0 lot, with EligibleRound cross-checked on
// a subset; the issuance table's Issued for issued(h); B_floor as writeFloor
// computes it (BRefBps x max(Issued(h), SRef) / 10000); and, on a subset,
// the state form (the registry's bonds in a StateDB read through liveTable
// and unitsAt, as Prepare reads them).
//
// Two tests, both gated by environment so the ordinary go test skips them:
//
//	S13_GENERATE=<n> S13_DATE=YYYYMMDD S13_RUN=<index> S13_OUT=<dir>
//	    go test -run TestSplitInvarianceCampaignGenerate   writes cases.json
//	S13_CASES=<dir>/cases.json S13_OUT=<dir> [S13_WORKERS=<n>]
//	    go test -run TestSplitInvarianceCampaign -timeout 0  reads it, writes
//	    results.jsonl as it goes (one line per case) and summary.json
//
// The seed rule: the campaign seed is SHA-256("GUARD-C-S13" || date || run
// index); case i's seed is BLAKE3(campaign seed || LE64(i)); the lot seed
// per case is BLAKE3("lot" || case seed). H = 4,096 heights per case.

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	mrand "math/rand"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/issuance"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/vm/igneumreg"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
	"lukechampine.com/blake3"
)

const (
	s13Heights     = 4096 // H
	s13EpochBlocks = 100  // the lottery epoch of the launch-equivalent chain (devnet7)
	s13Expected    = 1    // E
	s13BRefBps     = 1600 // the floor's share of max(issued, SRef)
)

// s13SRef is devnet7's SRef (11,780,607 IGN, the first year's issuance).
var s13SRef = new(big.Int).Mul(big.NewInt(11_780_607), big.NewInt(1e18))

// s13Epochs are the posting epochs of the case space; the last is the year
// boundary at 20 s a block and 100 blocks a lottery epoch.
var s13Epochs = []uint64{0, 1, 2, 10, 100, 1000, 365 * 86400 / 20 / s13EpochBlocks}

type s13Case struct {
	Index         uint64   `json:"i"`
	Seed          string   `json:"seed"`
	Capital       string   `json:"C"`
	N             int      `json:"n"`
	Form          string   `json:"form"`
	Parts         []string `json:"parts"`
	Beneficiaries string   `json:"beneficiaries"`
	PayoutList    int      `json:"payoutList"`
	Epoch         uint64   `json:"e"`
	Height        uint64   `json:"h"`
	Issued        string   `json:"issued"`
	Floor         string   `json:"floor"`
	Background    []string `json:"background"`
	Cap           uint64   `json:"cap"`
	StateForm     bool     `json:"stateForm"`
}

type s13Result struct {
	Index      uint64   `json:"i"`
	UW         string   `json:"u_W"`
	US         string   `json:"u_S"`
	UnitsW     uint64   `json:"units_W"`
	UnitsWQuot uint64   `json:"units_W_uncapped"`
	UnitsS     []uint64 `json:"units_S"`
	UnitsSSum  uint64   `json:"units_S_sum"`
	UnitsSQuot uint64   `json:"units_S_sum_uncapped"`
	Loss       int64    `json:"loss"`
	EligW      int      `json:"eligible_W"`
	EligS      int      `json:"eligible_S_union"`
	EligSKeys  int      `json:"eligible_S_by_key"`
	Heights    uint64   `json:"H"`
	Sigma      float64  `json:"sigma"`
	Dev        float64  `json:"dev_sigma"`
	UNext      string   `json:"u_e_plus_1"`
	FloorNext  string   `json:"floor_e_plus_1"`
	UnitsWNext uint64   `json:"units_W_e_plus_1"`
	UnitsSNext uint64   `json:"units_S_sum_e_plus_1"`
	P1, P2, P3 bool
	P4         bool
	P4Note     string  `json:"p4_note,omitempty"`
	P5Flag     bool    `json:"p5_cap_relaxation"`
	P5Ratio    float64 `json:"p5_ratio"`
	P5Elig     float64 `json:"p5_eligibility_ratio_capped,omitempty"`
	P3Units    string  `json:"p3_units,omitempty"`
	StateOK    *bool   `json:"state_form_agrees,omitempty"`
	StateNote  string  `json:"state_form_note,omitempty"`
	EligCross  *bool   `json:"eligibleround_agrees,omitempty"`
	Fail       string  `json:"fail,omitempty"`
}

func s13CampaignSeed(date, run string) [32]byte {
	return sha256.Sum256([]byte("GUARD-C-S13" + date + run))
}

func s13CaseSeed(campaign [32]byte, i uint64) [32]byte {
	var le [8]byte
	binary.LittleEndian.PutUint64(le[:], i)
	return blake3.Sum256(append(campaign[:], le[:]...))
}

func s13LotSeed(caseSeed [32]byte) common.Hash {
	return common.Hash(blake3.Sum256(append([]byte("lot"), caseSeed[:]...)))
}

// s13Key derives a world's key from the case seed and an index.
func s13Key(caseSeed [32]byte, world string, i int) common.Hash {
	var le [8]byte
	binary.LittleEndian.PutUint64(le[:], uint64(i))
	return common.Hash(blake3.Sum256(append(append([]byte("key-"+world), caseSeed[:]...), le[:]...)))
}

// s13Issuance is the launch schedule (params.IgneumLaunchIssuance), the
// one devnet7 carries by chain id, as PartsFor builds the table.
func s13Issuance() *issuance.Table {
	i := params.IgneumLaunchIssuance
	return issuance.New(issuance.Params{Epoch: i.Epoch, M: new(big.Int).Set(i.M), K: i.K, Tail: new(big.Int).Set(i.Tail), FundBps: i.FundBps, EpochDiv: i.EpochDiv, TailPerEpoch: new(big.Int).Set(i.TailPerEpoch)})
}

// s13Floor is B_floor as writeFloor computes it for the block at height.
func s13Floor(tab *issuance.Table, height uint64) (issued, floor *big.Int) {
	issued = tab.Issued(height)
	m := issued
	if s13SRef.Cmp(m) > 0 {
		m = s13SRef
	}
	floor = new(big.Int).Mul(m, big.NewInt(s13BRefBps))
	floor.Div(floor, big.NewInt(10000))
	return issued, floor
}

// s13Height is the snapshot height whose floor prices epoch e's table: the
// last block of epoch e - 2 (F-32) for e >= 2, the epoch's own first block
// for the live table of epochs 0 and 1.
func s13Height(e uint64) uint64 {
	if e < 2 {
		return e * s13EpochBlocks
	}
	return (e-1)*s13EpochBlocks - 1
}

func s13Rand(seed [32]byte, tag byte) *rand.Rand {
	h := blake3.Sum256(append(seed[:], tag))
	return rand.New(rand.NewPCG(binary.LittleEndian.Uint64(h[:8]), binary.LittleEndian.Uint64(h[8:16])))
}

// s13Dirichlet splits total into n positive parts by Dirichlet(1, ..., 1)
// (exponential weights, normalised; integer parts, the remainder on the
// last part).
func s13Dirichlet(r *rand.Rand, total *big.Int, n int) []*big.Int {
	w := make([]float64, n)
	sum := 0.0
	for i := range w {
		w[i] = -math.Log(1 - r.Float64())
		sum += w[i]
	}
	parts := make([]*big.Int, n)
	rest := new(big.Int).Set(total)
	for i := 0; i < n-1; i++ {
		f := new(big.Float).SetPrec(128).SetInt(total)
		f.Mul(f, big.NewFloat(w[i]/sum))
		p, _ := f.Int(nil)
		if p.Sign() <= 0 {
			p = big.NewInt(1)
		}
		if p.Cmp(rest) >= 0 {
			p = new(big.Int).Sub(rest, big.NewInt(int64(n-1-i)))
			if p.Sign() <= 0 {
				p = big.NewInt(1)
			}
		}
		parts[i] = p
		rest.Sub(rest, p)
	}
	parts[n-1] = rest
	return parts
}

func s13Generate(t *testing.T, date, run string, count int, out string) {
	campaign := s13CampaignSeed(date, run)
	tab := s13Issuance()
	forms := []string{"equal", "random", "dust", "lossless"}
	ns := []int{1, 2, 3, 4, 8, 16, 64, 256, 1024}
	lists := []int{1, 4, 256}
	f, err := os.Create(filepath.Join(out, "cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := bufio.NewWriterSize(f, 1<<20)
	w.WriteString("[\n")
	for i := 0; i < count; i++ {
		seed := s13CaseSeed(campaign, uint64(i))
		r := s13Rand(seed, 0)
		e := s13Epochs[r.IntN(len(s13Epochs))]
		h := s13Height(e)
		issued, floor := s13Floor(tab, h)
		uFloor := new(big.Int).Rsh(floor, 16)
		// C log-uniform from u / 2 to 4 x B_floor
		lo := s13Log(uFloor) - math.Log(2)
		hi := math.Log(4) + s13Log(floor)
		cf := new(big.Float).SetPrec(128).SetFloat64(math.Exp(lo + r.Float64()*(hi-lo)))
		capital, _ := cf.Int(nil)
		if capital.Sign() <= 0 {
			capital = big.NewInt(1)
		}
		n := ns[r.IntN(len(ns))]
		form := forms[r.IntN(len(forms))]
		var parts []*big.Int
		switch form {
		case "equal":
			q := new(big.Int).Div(capital, big.NewInt(int64(n)))
			parts = make([]*big.Int, n)
			rest := new(big.Int).Set(capital)
			for j := 0; j < n-1; j++ {
				parts[j] = new(big.Int).Set(q)
				rest.Sub(rest, q)
			}
			parts[n-1] = rest
		case "random":
			parts = s13Dirichlet(r, capital, n)
		case "dust":
			// one large part and n - 1 dust parts under u (zero-unit parts)
			parts = make([]*big.Int, n)
			bound := new(big.Int).Sub(uFloor, big.NewInt(1))
			share := new(big.Int).Div(capital, big.NewInt(int64(2*n)))
			if share.Cmp(bound) < 0 {
				bound = share
			}
			if bound.Sign() <= 0 {
				bound = big.NewInt(1)
			}
			rest := new(big.Int).Set(capital)
			for j := 1; j < n; j++ {
				d := new(big.Int).Rand(s13MathRand(r), bound)
				d.Add(d, big.NewInt(1))
				parts[j] = d
				rest.Sub(rest, d)
			}
			parts[0] = rest
		case "lossless":
			// every part a multiple of u (the price at the case's B_total,
			// which the harness recomputes; the remainder under u on the
			// last part)
			parts = make([]*big.Int, n)
			rest := new(big.Int).Set(capital)
			for j := 0; j < n-1; j++ {
				parts[j] = new(big.Int)
			}
			parts[n-1] = rest
		}
		// background bonds: none, or 1 to 64 keys summing to [0, 3 x B_floor]
		var bg []*big.Int
		if r.IntN(2) == 1 {
			k := 1 + r.IntN(64)
			tot := new(big.Int).Mul(floor, big.NewInt(3))
			tot = new(big.Int).Rand(s13MathRand(r), tot)
			if tot.Sign() > 0 {
				bg = s13Dirichlet(r, tot, k)
			}
		}
		// the lossless form's multiples are of u at B_total = C + background
		if form == "lossless" {
			total := new(big.Int).Set(capital)
			for _, b := range bg {
				total.Add(total, b)
			}
			u := unitPrice(total, floor)
			units := new(big.Int).Div(capital, u).Uint64()
			rest := new(big.Int).Set(capital)
			for j := 0; j < n-1; j++ {
				var k uint64
				if units > 0 {
					k = uint64(r.Int64N(int64(units) + 1))
				}
				units -= k
				p := new(big.Int).Mul(u, new(big.Int).SetUint64(k))
				parts[j] = p
				rest.Sub(rest, p)
			}
			parts[n-1] = rest
		}
		c := s13Case{
			Index: uint64(i), Seed: common.Bytes2Hex(seed[:]), Capital: capital.String(), N: n, Form: form,
			Beneficiaries: []string{"one", "distinct"}[r.IntN(2)], PayoutList: lists[r.IntN(len(lists))],
			Epoch: e, Height: h, Issued: issued.String(), Floor: floor.String(), Cap: []uint64{65536, 16384}[r.IntN(2)],
			StateForm: i%50 == 0,
		}
		for _, p := range parts {
			c.Parts = append(c.Parts, p.String())
		}
		for _, b := range bg {
			c.Background = append(c.Background, b.String())
		}
		b, _ := json.Marshal(c)
		if i > 0 {
			w.WriteString(",\n")
		}
		w.Write(b)
	}
	w.WriteString("\n]\n")
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
}

func s13Log(x *big.Int) float64 {
	f, _ := new(big.Float).SetInt(x).Float64()
	return math.Log(f)
}

// s13MathRand adapts the PCG stream for big.Int.Rand (which takes the v1
// *math/rand.Rand): a v1 generator seeded from the v2 stream.
func s13MathRand(r *rand.Rand) *mrand.Rand {
	return mrand.New(mrand.NewSource(int64(r.Uint64() >> 1)))
}

func TestSplitInvarianceCampaignGenerate(t *testing.T) {
	n := os.Getenv("S13_GENERATE")
	if n == "" {
		t.Skip("S13_GENERATE unset")
	}
	count, err := strconv.Atoi(n)
	if err != nil || count <= 0 {
		t.Fatalf("S13_GENERATE: %q", n)
	}
	date, run, out := os.Getenv("S13_DATE"), os.Getenv("S13_RUN"), os.Getenv("S13_OUT")
	if len(date) != 8 || run == "" || out == "" {
		t.Fatal("S13_DATE (YYYYMMDD), S13_RUN and S13_OUT are required")
	}
	s13Generate(t, date, run, count, out)
	t.Logf("S13 cases written: %d under %s (campaign seed %x)", count, out, s13CampaignSeed(date, run))
}

// s13Eligible0 reads whether a key with n units is round-0 eligible at a
// height: any unit's Lot under Theta(E, 0), the node's own Lot and Theta;
// the scan stops at the first hit (EligibleRound reads every unit and is
// cross-checked on a subset).
func s13Eligible0(seed, key common.Hash, units, height uint64, theta0 *s13Theta) bool {
	for j := uint64(0); j < units; j++ {
		x := Lot(seed, key, j, height)
		if theta0.all || bytes.Compare(x[:], theta0.b[:]) < 0 {
			return true
		}
	}
	return false
}

// s13Theta is Theta(E, 0) as 32 big-endian bytes for the scan's compare
// without an allocation per lot (the lot is a 32-byte hash, so the byte
// compare is the integer compare); all marks theta 2^256 (every lot hits).
type s13Theta struct {
	b   [32]byte
	all bool
}

func s13ThetaOf(theta *big.Int) *s13Theta {
	t := &s13Theta{}
	if theta.BitLen() > 256 {
		t.all = true
		return t
	}
	theta.FillBytes(t.b[:])
	return t
}

// s13HeightsFor is S13.1's H(units) (main cb4ff718, 17:5x UK): min(4,096,
// floor(2^27 / units_W)) and never under 512, so a 65,536-unit case reads
// 2,048 heights, 32,768 and under the full 4,096; a key with no units reads
// the full H (every height a non-hit, the scan free).
func s13HeightsFor(units uint64) uint64 {
	if units == 0 {
		return s13Heights
	}
	h := uint64(1<<27) / units
	if h > s13Heights {
		h = s13Heights
	}
	if h < 512 {
		h = 512
	}
	return h
}

func s13Eval(t *testing.T, c *s13Case, tab *issuance.Table, sp *SortitionParams) s13Result {
	res := s13Result{Index: c.Index}
	var caseSeed [32]byte
	copy(caseSeed[:], common.FromHex(c.Seed))
	capital, _ := new(big.Int).SetString(c.Capital, 10)
	floor, _ := new(big.Int).SetString(c.Floor, 10)
	parts := make([]*big.Int, len(c.Parts))
	sumParts := new(big.Int)
	for i, p := range c.Parts {
		parts[i], _ = new(big.Int).SetString(p, 10)
		sumParts.Add(sumParts, parts[i])
	}
	if sumParts.Cmp(capital) != 0 {
		res.Fail = "the parts do not sum to C (generator fault)"
		return res
	}
	bg := new(big.Int)
	for _, b := range c.Background {
		x, _ := new(big.Int).SetString(b, 10)
		bg.Add(bg, x)
	}
	total := new(big.Int).Add(capital, bg)
	// P1: u in W and in S (the same B_total)
	uW := unitPrice(total, floor)
	uS := unitPrice(new(big.Int).Add(sumParts, bg), floor)
	res.UW, res.US = uW.String(), uS.String()
	res.P1 = uW.Cmp(uS) == 0 && uW.Cmp(new(big.Int).Rsh(floor, 16)) >= 0
	// the units: the node's unitsOf (capped at MaxUnitsPerKey) and the
	// uncapped quotient floor(B / u) beside it
	res.UnitsW = unitsOf(capital, uW)
	res.UnitsWQuot = new(big.Int).Div(capital, uW).Uint64()
	for _, p := range parts {
		n := unitsOf(p, uS)
		res.UnitsS = append(res.UnitsS, n)
		res.UnitsSSum += n
		res.UnitsSQuot += new(big.Int).Div(p, uS).Uint64()
	}
	// P2: the split's units in [units_W - (n - 1), units_W] with the cap
	// lifted (the quotients), and never above
	res.Loss = int64(res.UnitsWQuot) - int64(res.UnitsSQuot)
	res.P2 = res.Loss >= 0 && res.Loss <= int64(c.N-1)
	if c.Form == "lossless" && res.Loss != 0 {
		res.P2 = false
	}
	// P5: the cap's per-key relaxation (reported, never graded)
	if c.Cap == 16384 && res.UnitsSSum > res.UnitsW {
		res.P5Flag = true
		res.P5Ratio = float64(res.UnitsSSum) / float64(res.UnitsW)
	}
	// P3: round-0 eligibility over H heights, W's key against S's union
	lotSeed := s13LotSeed(caseSeed)
	theta0 := s13ThetaOf(Theta(s13Expected, 0))
	keyW := s13Key(caseSeed, "W", 0)
	keysS := make([]common.Hash, c.N)
	for i := range keysS {
		keysS[i] = s13Key(caseSeed, "S", i)
	}
	// P3 is read at the CAP-LIFTED units in both worlds (the uncapped
	// quotients floor(B / u)), whatever the case's cap: under the cap a
	// single key above 16,384 units holds fewer units than its capital
	// buys while a split past the cap holds them all, which is P5's class
	// (the cap's per-key relaxation, reported and never graded; 2.4 and
	// S12) and not a Sybil gain at the price, so P3 under the cap would
	// grade P5's relaxation as a failure (the twelve-case profile run of
	// 17:5x UK read two such: 5005 and 5010, both cap 16,384 with W above
	// it). P5 carries the capped units' expected eligibility beside the
	// unit ratio, computed from the binomial form and not scanned.
	unitsW := res.UnitsWQuot
	unitsS := make([]uint64, c.N)
	for i, p := range parts {
		unitsS[i] = new(big.Int).Div(p, uS).Uint64()
	}
	res.P3Units = "lifted"
	if res.P5Flag {
		pOne := func(n uint64) float64 { return 1 - math.Pow(1-1.0/65536, float64(n)) }
		res.P5Elig = pOne(res.UnitsSSum) / pOne(res.UnitsW)
	}
	H := s13HeightsFor(unitsW)
	res.Heights = H
	for h := uint64(1); h <= H; h++ {
		if s13Eligible0(lotSeed, keyW, unitsW, h, theta0) {
			res.EligW++
		}
		// the union over S's keys: the scan stops at the first key that hits
		// (P3 compares the union; EligSKeys counts the keys scanned up to
		// and including the hit, not every hitting key, since 18:1x UK)
		for i := range keysS {
			if unitsS[i] > 0 && s13Eligible0(lotSeed, keysS[i], unitsS[i], h, theta0) {
				res.EligSKeys += i + 1
				res.EligS++
				break
			}
		}
	}
	// P3 in the outside review's words, "no extra expected authority": the split's
	// eligible heights never exceed the single key's by more than three
	// binomial standard deviations (the difference of two binomials over H
	// heights at W's probability); the signed deviation is printed, and a
	// split whose loss of up to n - 1 units reads BELOW W is the split's
	// own cost, counted in the summary and never a failure
	pW := 1 - math.Pow(1-1.0/65536, float64(unitsW))
	res.Sigma = math.Sqrt(2 * float64(H) * pW * (1 - pW))
	if res.Sigma > 0 {
		res.Dev = (float64(res.EligS) - float64(res.EligW)) / res.Sigma
		res.P3 = res.Dev <= 3
	} else {
		res.P3 = res.EligS <= res.EligW
	}
	// EligibleRound cross-check on one height in fifty cases (the node's
	// full scan against the early-exit scan)
	if c.Index%50 == 7 && unitsW > 0 && unitsW <= 1<<16 {
		h := uint64(1 + c.Index%H)
		a := sp.EligibleRound(lotSeed, keyW, unitsW, h) == 0
		b := s13Eligible0(lotSeed, keyW, unitsW, h, theta0)
		ok := a == b
		res.EligCross = &ok
		if !ok {
			res.Fail = "EligibleRound disagrees with the Lot scan"
		}
	}
	// P4: u(e) one number for every key, never under the floor's unit;
	// the whole case at e + 1 (W at e, S at e + 1 and the reverse): at an
	// equal floor the units are the same numbers; at the year boundary
	// the floor's step is printed
	hNext := s13Height(c.Epoch + 1)
	_, floorNext := s13Floor(tab, hNext)
	uNext := unitPrice(total, floorNext)
	res.UNext, res.FloorNext = uNext.String(), floorNext.String()
	res.UnitsWNext = unitsOf(capital, uNext)
	for _, p := range parts {
		res.UnitsSNext += unitsOf(p, uNext)
	}
	res.P4 = uW.Cmp(new(big.Int).Rsh(floor, 16)) >= 0 && uNext.Cmp(new(big.Int).Rsh(floorNext, 16)) >= 0 && uNext.Cmp(uW) >= 0
	if floorNext.Cmp(floor) == 0 {
		// the swapped order: S posted at e + 1 buys what W buys at e less the
		// split's loss, and W at e + 1 what S buys at e plus it
		if res.UnitsWNext != res.UnitsW || res.UnitsSNext != res.UnitsSSum {
			res.P4 = false
			res.P4Note = "the same capital bought different units at e and e + 1 under an equal floor"
		}
	} else {
		res.P4Note = fmt.Sprintf("the floor steps at e + 1 (issued over SRef): u %s to %s; units_W %d to %d", uW, uNext, res.UnitsW, res.UnitsWNext)
	}
	// the state form on a subset: the bonds posted into a StateDB through
	// the registry, the units read as Prepare reads them (liveTable,
	// unitsAt)
	if c.StateForm {
		ok, note := s13StateForm(t, c, parts, floor, uW, res.UnitsW, res.UnitsS)
		res.StateOK = &ok
		res.StateNote = note
		if !ok {
			res.Fail = "the state form's units disagree with unitsOf: " + note
		}
	}
	if res.Fail == "" && !(res.P1 && res.P2 && res.P3 && res.P4) {
		res.Fail = fmt.Sprintf("P1 %v P2 %v P3 %v P4 %v", res.P1, res.P2, res.P3, res.P4)
	}
	return res
}

// s13StateForm posts W's bond and S's parts (and the background) into two
// native-registry states with the case's floor in the floor slot and reads
// each key's units through liveTable and unitsAt. The registry's own cap
// (CapBps, 26 percent of max(B_total, B_floor) at posting) refuses a bond
// over it, which is the chain's rule and not a harness fault: a refused W
// or part is recorded in the note (S12's reading, the cap as a per-key
// operational limit) and the comparison runs over the bonds the registry
// accepted; the result is false only when an accepted bond's units or the
// unit price disagree with the harness's unitsOf and unitPrice.
func s13StateForm(t *testing.T, c *s13Case, parts []*big.Int, floor, u *big.Int, unitsW uint64, unitsS []uint64) (bool, string) {
	e := genesisEngine(t)
	var caseSeed [32]byte
	copy(caseSeed[:], common.FromHex(c.Seed))
	capital, _ := new(big.Int).SetString(c.Capital, 10)
	bg := make([]*big.Int, len(c.Background))
	for i, b := range c.Background {
		bg[i], _ = new(big.Int).SetString(b, 10)
	}
	note := ""
	mk := func() (*state.StateDB, int) {
		st := nativeState(t, new(big.Int).Lsh(big.NewInt(1), 200))
		st.SetState(testRegistry, igneumreg.SlotFloor, common.BigToHash(floor))
		refused := 0
		for i, b := range bg {
			from := common.BytesToAddress(s13Key(caseSeed, "bg-acct", i).Bytes()[:20])
			if s13Bond(st, from, s13Key(caseSeed, "bg", i), b) != nil {
				refused++
			}
		}
		return st, refused
	}
	// W
	stW, bgRefW := mk()
	fromW := common.BytesToAddress(s13Key(caseSeed, "W-acct", 0).Bytes()[:20])
	if err := s13Bond(stW, fromW, s13Key(caseSeed, "W", 0), capital); err != nil {
		note += fmt.Sprintf("W refused (%v); ", err)
	} else if bgRefW == 0 {
		tW := e.liveTable(stW, 0)
		got := e.unitsAt(stW, tW, s13Key(caseSeed, "W", 0))
		if tW.u.Cmp(u) != 0 || got != unitsW {
			return false, note + fmt.Sprintf("W: state u %s units %d against %s %d", tW.u, got, u, unitsW)
		}
	} else {
		note += fmt.Sprintf("%d background bonds refused in W's state; ", bgRefW)
	}
	// S
	stS, bgRefS := mk()
	refS := 0
	for i, p := range parts {
		from := fromW
		if c.Beneficiaries == "distinct" {
			from = common.BytesToAddress(s13Key(caseSeed, "S-acct", i).Bytes()[:20])
		}
		if s13Bond(stS, from, s13Key(caseSeed, "S", i), p) != nil {
			refS++
		}
	}
	if refS > 0 || bgRefS > 0 {
		note += fmt.Sprintf("%d of %d parts and %d background bonds refused in S's state; ", refS, len(parts), bgRefS)
		return true, note
	}
	tS := e.liveTable(stS, 0)
	if tS.u.Cmp(u) != 0 {
		return false, note + fmt.Sprintf("S: state u %s against %s", tS.u, u)
	}
	for i := range parts {
		if got := e.unitsAt(stS, tS, s13Key(caseSeed, "S", i)); got != unitsS[i] {
			return false, note + fmt.Sprintf("S part %d: state units %d against %d", i, got, unitsS[i])
		}
	}
	return true, note
}

// s13Bond posts a bond through the registry as the EVM would (the value
// moved to the registry first, as a call carries it); a refusal (the cap,
// the key range) is returned with the value moved back.
func s13Bond(st *state.StateDB, from common.Address, key common.Hash, amount *big.Int) error {
	v := uint256.MustFromBig(amount)
	st.AddBalance(from, v)
	st.SubBalance(from, v)
	st.AddBalance(testRegistry, v)
	if _, err := igneumreg.Run(st, testRegistry, from, from, v, key[:]); err != nil {
		st.SubBalance(testRegistry, v)
		return err
	}
	return nil
}

func TestSplitInvarianceCampaign(t *testing.T) {
	path := os.Getenv("S13_CASES")
	if path == "" {
		t.Skip("S13_CASES unset")
	}
	out := os.Getenv("S13_OUT")
	if out == "" {
		out = filepath.Dir(path)
	}
	workers := runtime.NumCPU()
	if w, err := strconv.Atoi(os.Getenv("S13_WORKERS")); err == nil && w > 0 {
		workers = w
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cases []s13Case
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	// S13_FROM and S13_TO slice the campaign by case index (from inclusive,
	// to exclusive) so two hosts share one cases.json (the project's order of
	// 17:2x UK: one box the lower half, another the upper); the slices'
	// results.jsonl files are merged by index into one summary by
	// tools/security/s13_merge.py, which names both hosts.
	from, to := 0, len(cases)
	if v, err := strconv.Atoi(os.Getenv("S13_FROM")); err == nil && v >= 0 && v < len(cases) {
		from = v
	}
	if v, err := strconv.Atoi(os.Getenv("S13_TO")); err == nil && v > from && v <= len(cases) {
		to = v
	}
	cases = cases[from:to]
	// S13_DONE names results files whose cases are already read (the first
	// form's H 4,096, kept as the stricter reading, main 17:5x UK); their
	// indices are skipped here and the merge takes the stricter record.
	capByIdx := make(map[uint64]uint64, len(cases))
	for _, c := range cases {
		capByIdx[c.Index] = c.Cap
	}
	capOf := func(i uint64) uint64 { return capByIdx[i] }
	doneIdx := map[uint64]bool{}
	for _, p := range strings.Split(os.Getenv("S13_DONE"), ",") {
		if p == "" {
			continue
		}
		f, err := os.Open(p)
		if err != nil {
			t.Fatalf("S13_DONE %s: %v", p, err)
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<24)
		for sc.Scan() {
			var r struct {
				Index   uint64 `json:"i"`
				UnitsWQ uint64 `json:"units_W_uncapped"`
				P3Units string `json:"p3_units"`
			}
			if json.Unmarshal(sc.Bytes(), &r) == nil {
				// S13_REDO_CAPPED=1: a done record graded at the capped units
				// (no p3_units) whose W exceeds the cap is read again at the
				// lifted units (the regrading of 18:0x UK); the rest stay done
				if os.Getenv("S13_REDO_CAPPED") == "1" && r.P3Units == "" && r.UnitsWQ > MaxUnitsPerKey && capOf(r.Index) == 16384 {
					continue
				}
				doneIdx[r.Index] = true
			}
		}
		f.Close()
	}
	skipped := 0
	kept := cases[:0]
	for _, c := range cases {
		if doneIdx[c.Index] {
			skipped++
			continue
		}
		kept = append(kept, c)
	}
	cases = kept
	host, _ := os.Hostname()
	tab := s13Issuance()
	sp := &SortitionParams{Height: 1, EpochBlocks: s13EpochBlocks, SeedBlocks: 1024, Expected: s13Expected, Delta: 20, MaxRound: 16, UnbondedRound: 17, MaxPayouts: 256, OpenSeat: true}
	rf, err := os.Create(filepath.Join(out, "results.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer rf.Close()
	var mu sync.Mutex
	var wg sync.WaitGroup
	jobs := make(chan int)
	results := make([]s13Result, len(cases))
	done := 0
	start := time.Now()
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				r := s13Eval(t, &cases[i], tab, sp)
				b, _ := json.Marshal(r)
				mu.Lock()
				results[i] = r
				rf.Write(append(b, '\n'))
				done++
				if done%200 == 0 {
					t.Logf("S13 %d of %d cases at %s", done, len(cases), time.Since(start).Round(time.Second))
				}
				mu.Unlock()
			}
		}()
	}
	for i := range cases {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	// summary
	type cls = map[string]int
	sum := map[string]any{}
	perForm, perN, perEpoch, perCap, perList, perBen := cls{}, cls{}, cls{}, cls{}, cls{}, cls{}
	var fails []string
	maxDev, p5n, p5max := 0.0, 0, 0.0
	below3, zeroLossOver3 := 0, 0
	stateChecked, stateOK, crossChecked, crossOK := 0, 0, 0, 0
	stateRefused := 0
	for i, r := range results {
		c := cases[i]
		perForm[c.Form]++
		perN[strconv.Itoa(c.N)]++
		perEpoch[strconv.FormatUint(c.Epoch, 10)]++
		perCap[strconv.FormatUint(c.Cap, 10)]++
		perList[strconv.Itoa(c.PayoutList)]++
		perBen[c.Beneficiaries]++
		if r.Fail != "" {
			fails = append(fails, fmt.Sprintf("case %d: %s", r.Index, r.Fail))
		}
		if r.Dev > maxDev {
			maxDev = r.Dev
		}
		if r.Dev < -3 {
			below3++
		}
		if r.Loss == 0 && math.Abs(r.Dev) > 3 {
			zeroLossOver3++
		}
		if r.P5Flag {
			p5n++
			if r.P5Ratio > p5max {
				p5max = r.P5Ratio
			}
		}
		if r.StateOK != nil {
			stateChecked++
			if *r.StateOK {
				stateOK++
			}
			if r.StateNote != "" {
				stateRefused++
			}
		}
		if r.EligCross != nil {
			crossChecked++
			if *r.EligCross {
				crossOK++
			}
		}
	}
	sum["cases"] = len(cases)
	sum["range"] = []int{from, to}
	sum["host"] = host
	sum["per_form"], sum["per_n"], sum["per_epoch"], sum["per_cap"], sum["per_payout_list"], sum["per_beneficiaries"] = perForm, perN, perEpoch, perCap, perList, perBen
	sum["failures"] = fails
	sum["failure_count"] = len(fails)
	sum["p3_largest_dev_sigma"] = maxDev
	sum["p3_split_below_W_by_over_3_sigma"] = below3
	sum["p3_zero_loss_cases_outside_3_sigma_either_way"] = zeroLossOver3
	sum["p5_cases"] = p5n
	sum["p5_fraction"] = float64(p5n) / float64(len(cases))
	sum["p5_largest_ratio"] = p5max
	sum["state_form_checked"], sum["state_form_agree"] = stateChecked, stateOK
	sum["state_form_with_a_registry_refusal"] = stateRefused
	sum["eligibleround_checked"], sum["eligibleround_agree"] = crossChecked, crossOK
	sum["heights_per_case"] = "S13.1 (main cb4ff718): H(units_W) = min(4,096, floor(2^27 / units_W)), never under 512; the H used per case in results (H)"
	sum["form"] = "S13.1 cb4ff718"
	sum["skipped_as_done"] = skipped
	sum["expected"] = s13Expected
	sum["epoch_blocks"] = s13EpochBlocks
	sum["sref_wei"] = s13SRef.String()
	sum["workers"] = workers
	sum["elapsed"] = time.Since(start).String()
	sum["utc"] = time.Now().UTC().Format(time.RFC3339)
	sum["verdict"] = map[bool]string{true: "PASS (zero failures)", false: "FAIL (named)"}[len(fails) == 0]
	b, _ := json.MarshalIndent(sum, "", " ")
	if err := os.WriteFile(filepath.Join(out, "summary.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("S13 summary: %s", b)
	if len(fails) > 0 {
		t.Errorf("S13: %d failures, the first: %s", len(fails), fails[0])
	}
}
