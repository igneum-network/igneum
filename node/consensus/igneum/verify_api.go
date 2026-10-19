package igneum

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
)

// The verifier's measurement entries (the outside review's verifier ruling of 20:17
// UK, 12 October 2026, through main; the node team's measure on the minimum
// node): igneum_verifyHeader admits ONE header through the engine's own
// header rule set against this node's chain and answers the verdict, the
// refusal's name and STAGE, and the verifier's time in microseconds;
// igneum_verifyBench runs the seal verification of a canonical block n
// times and answers the per-header time's p50, p99 and max; the budget's
// reading is igneum_verifyBudget. The stages are the order verifyHeader
// asserts them, which is the ruling's: the HEIGHT (the header's fixed
// fields against the parent: number, time, difficulty, gas, the fork
// fields), the TEMPLATE (the payout list and the coinbase form), the
// SIGNATURE (the bond key and the authority header), the ROUND (the
// sortition header: the round, the time floor, the clock), the ELIGIBILITY
// (the authority rule at the parent's state and the key's draw), the PROOF
// (the proof pi's header part), and only then the PROGRAMME (the seal's
// hash under the period's programme on the header's branch, and the
// anchor's resolution before it). A malformed header is refused at the
// first stage it fails, by that stage's name, with no hash run.

// VerifyReport is igneum_verifyHeader's answer.
type VerifyReport struct {
	Number  uint64      `json:"number"`
	Hash    common.Hash `json:"hash"`
	OK      bool        `json:"ok"`
	Stage   string      `json:"stage"`   // the stage reached: "ok" when every stage passed, else the refusing stage
	Refusal string      `json:"refusal"` // the refusal's text; empty when ok
	Micros  int64       `json:"micros"`  // the whole rule set's wall time
	// Program is the programme the seal was (or would be) checked under on
	// the header's branch; null on an algorithm without one.
	Program *programInfo `json:"program,omitempty"`
}

// stageOf names the stage a verifyHeader error belongs to.
func stageOf(err error) string {
	if err == nil {
		return "ok"
	}
	s := err.Error()
	switch {
	case errors.Is(err, errInvalidPoW), errors.Is(err, errProgramAncestor), errors.Is(err, errProgramNoChain),
		strings.HasPrefix(s, "igneum keyed"), strings.HasPrefix(s, "igneum program"):
		return "programme"
	case strings.HasPrefix(s, "igneum pi"):
		return "proof"
	case errors.Is(err, errPayoutList), strings.Contains(s, "payout list"), strings.Contains(s, "coinbase"):
		return "template"
	case strings.HasPrefix(s, "igneum authority"):
		// The header form (the bond key's presence and shape) and the state
		// form (bonded, the account) share the prefix; the state form is
		// the eligibility stage.
		if errors.Is(err, errBondKeyUnbonded) || errors.Is(err, errBondAccount) {
			return "eligibility"
		}
		return "signature"
	case strings.HasPrefix(s, "igneum sortition"), strings.HasPrefix(s, "igneum clock"), errors.Is(err, errUnderTimeFloor):
		if strings.Contains(s, "eligib") || strings.Contains(s, "not eligible") {
			return "eligibility"
		}
		return "round"
	default:
		return "height"
	}
}

// VerifyHeader is igneum_verifyHeader: one header admitted through the
// engine's header rule set against this node's chain (its parent must be
// held), timed.
func (api *IgneumAPI) VerifyHeader(header *types.Header) (*VerifyReport, error) {
	if api.chain == nil {
		return nil, errors.New("igneum: no chain behind the API")
	}
	if header == nil || header.Number == nil || !header.Number.IsUint64() {
		return nil, errors.New("igneum: a header with a number is needed")
	}
	e := api.engine
	e.setChain(api.chain)
	rep := &VerifyReport{Number: header.Number.Uint64(), Hash: header.Hash()}
	n := header.Number.Uint64()
	if n == 0 {
		return nil, errors.New("igneum: genesis is not verified")
	}
	parent := api.chain.GetHeader(header.ParentHash, n-1)
	if parent == nil {
		rep.Stage, rep.Refusal = "height", "parent unknown to this node"
		return rep, nil
	}
	gp, err := e.grandparent(api.chain, parent)
	if err != nil {
		rep.Stage, rep.Refusal = "height", err.Error()
		return rep, nil
	}
	if info, perr := e.programInfoFor(header); perr == nil {
		rep.Program = info
	}
	start := time.Now()
	err = e.verifyHeader(api.chain, header, parent, gp, true, time.Now().Unix())
	rep.Micros = time.Since(start).Microseconds()
	rep.Stage = stageOf(err)
	if err != nil {
		rep.Refusal = err.Error()
	} else {
		rep.OK = true
	}
	return rep, nil
}

// programInfoFor is the programme of a header's period on its branch.
func (e *Engine) programInfoFor(header *types.Header) (*programInfo, error) {
	p, ok := e.algo.(interface{ PeriodBlocks(uint64) uint64 })
	if !ok {
		return nil, nil
	}
	period := p.PeriodBlocks(header.Number.Uint64())
	if period == 0 {
		return nil, nil
	}
	anchor, err := e.programAnchor(e.chainReader(), header.ParentHash, header.Number.Uint64(), period)
	if err != nil {
		return nil, err
	}
	info := &programInfo{PeriodBlocks: period, Reveal: e.programReveal(), Period: header.Number.Uint64() / period, Anchor: anchor}
	if ah, ok := programAnchorHeight(header.Number.Uint64(), period, info.Reveal); ok {
		info.AnchorHeight = ah
	}
	return info, nil
}

// VerifyBench is igneum_verifyBench's answer: the seal verification of one
// canonical header run n times (the programme cached after the first), the
// per-run time's distribution in microseconds.
type VerifyBench struct {
	Number   uint64 `json:"number"`
	Runs     int    `json:"runs"`
	P50Us    int64  `json:"p50Us"`
	P99Us    int64  `json:"p99Us"`
	MaxUs    int64  `json:"maxUs"`
	FirstUs  int64  `json:"firstUs"` // the first run: the programme's draw and the context's touch
	Refusal  string `json:"refusal,omitempty"`
	Program  *programInfo `json:"program,omitempty"`
	// ChargedMs is what the bench charged the node's verify budget: zero,
	// the bench runs outside the bucket (the budget's row is
	// igneum_verifyBudget's).
	ChargedMs uint64 `json:"chargedMs"`
}

// VerifyBench is igneum_verifyBench(number, runs): the seal verification of
// the canonical header at number, runs times.
func (api *IgneumAPI) VerifyBench(number rpc.BlockNumber, runs int) (*VerifyBench, error) {
	if api.chain == nil {
		return nil, errors.New("igneum: no chain behind the API")
	}
	if runs <= 0 || runs > 10000 {
		return nil, errors.New("igneum: runs must be 1 to 10,000")
	}
	e := api.engine
	e.setChain(api.chain)
	var header *types.Header
	switch {
	case number == rpc.LatestBlockNumber:
		header = api.chain.CurrentHeader()
	case number > 0:
		header = api.chain.GetHeaderByNumber(uint64(number))
	default:
		return nil, fmt.Errorf("igneum: block %s is not benchable", number.String())
	}
	if header == nil {
		return nil, errors.New("igneum: no such canonical header")
	}
	b := &VerifyBench{Number: header.Number.Uint64(), Runs: runs}
	if info, err := e.programInfoFor(header); err == nil {
		b.Program = info
	}
	// The bench runs the seal's hash on the header's view directly, OUTSIDE
	// the node's verify budget (the node team's row of 21:5x UK: a thousand
	// runs charged 38 s into a 200 ms/s bucket and the percentiles read the
	// refill), so the cost row and the budget row are separate reads; it
	// charges nothing and says so.
	algo, err := e.algoFor(header)
	if err != nil {
		b.Refusal = err.Error()
		return b, nil
	}
	sealHash := e.SealHash(header)
	times := make([]int64, 0, runs)
	for i := 0; i < runs; i++ {
		start := time.Now()
		var err error
		if t, ok := algo.(pow.Ticketed); ok {
			err = pow.VerifyTicketed(t, sealHash, header.Number.Uint64(), header.Nonce.Uint64(), header.MixDigest, header.Ticket, e.sealDifficulty(header))
		} else {
			err = algo.Verify(sealHash, header.Number.Uint64(), header.Nonce.Uint64(), header.MixDigest, e.sealDifficulty(header), false)
		}
		us := time.Since(start).Microseconds()
		if err != nil {
			b.Refusal = err.Error()
			break
		}
		if i == 0 {
			b.FirstUs = us
		}
		times = append(times, us)
	}
	if len(times) == 0 {
		return b, nil
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	b.P50Us = times[len(times)/2]
	b.P99Us = times[(len(times)*99)/100]
	b.MaxUs = times[len(times)-1]
	return b, nil
}

// VerifyBudget is igneum_verifyBudget: the global CPU budget's reading.
func (api *IgneumAPI) VerifyBudget() VerifyBudgetState { return api.engine.VerifyBudget() }
