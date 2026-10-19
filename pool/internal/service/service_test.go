package service

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"igneum.network/pool/internal/jobsource"
	"igneum.network/pool/internal/ledger"
	"igneum.network/pool/internal/stratum"
	"igneum.network/pool/internal/validate"
)

// fakeNode is a one-block-at-a-time chain: GetWork serves the next
// height; SubmitWork seals it with the given nonce and mix; the chain
// then advances by empty blocks on Advance.
type fakeNode struct {
	mu       sync.Mutex
	head     uint64
	blocks   map[uint64]map[string]interface{}
	header   [32]byte
	boundary [32]byte
	rewards  bool
	submits  int
	// the chain-native form: the list the node's templates name (nil = none, as after a restart), the list the pool
	// set on it by miner_setPayoutList, how many times, and whether GetWork fails (the node down)
	payouts  json.RawMessage
	listSet  json.RawMessage
	listSets int
	down     bool
	// the share lane: when shareLane is set GetWork serves the share fields (the share target the boundary, 160 a
	// block, the template's height and parent) and igneum_submitShare answers accepted, or refused under refuseShares,
	// counting the submissions
	shareLane    bool
	refuseShares bool
	shareSubmits int
}

func newFakeNode(rewards bool) *fakeNode {
	n := &fakeNode{blocks: map[uint64]map[string]interface{}{}, rewards: rewards}
	n.boundary = validate.TargetForDifficulty(4)
	n.header[0] = 0x11
	n.blocks[0] = map[string]interface{}{"hash": "0xgenesis", "nonce": "0x0000000000000000", "mixHash": "0x" + strings.Repeat("00", 32)}
	return n
}

func (n *fakeNode) GetWork(ctx context.Context) (jobsource.Work, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.down {
		return jobsource.Work{}, fmt.Errorf("connection refused")
	}
	w := jobsource.Work{Header: n.header, Boundary: n.boundary, Height: n.head + 1, Algorithm: validate.NullAlgorithm, Payouts: n.payouts}
	if n.shareLane {
		w.ShareTarget = json.RawMessage(`"` + new(big.Int).SetBytes(n.boundary[:]).String() + `"`)
		w.SharesPerBlock, w.ShareHeight, w.ShareParent = 160, n.head+1, n.header
	}
	return w, nil
}

func (n *fakeNode) SubmitWork(ctx context.Context, nonce uint64, header, mix [32]byte) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.submits++
	n.head++
	n.blocks[n.head] = map[string]interface{}{
		"hash": fmt.Sprintf("0xblock%d", n.head), "nonce": fmt.Sprintf("0x%016x", nonce),
		"mixHash": "0x" + hex.EncodeToString(mix[:]),
	}
	n.header[1]++ // new work after a sealed block
	return nil
}

// Advance appends empty canonical blocks.
func (n *fakeNode) Advance(k uint64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for i := uint64(0); i < k; i++ {
		n.head++
		n.blocks[n.head] = map[string]interface{}{"hash": fmt.Sprintf("0xblock%d", n.head), "nonce": "0x0000000000000001", "mixHash": "0x" + strings.Repeat("ab", 32)}
	}
	n.header[2]++
}

func (n *fakeNode) Call(ctx context.Context, method string, params interface{}, out interface{}) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	var res interface{}
	ps := params.([]interface{})
	switch method {
	case "igneum_submitShare":
		n.shareSubmits++
		res = map[string]interface{}{"accepted": !n.refuseShares, "reason": map[bool]string{true: "igneum shares: refused by this sealer (the test)", false: ""}[n.refuseShares]}
	case "miner_setPayoutList":
		raw, _ := json.Marshal(ps[0])
		n.listSet = raw
		n.listSets++
		res = true
	case "eth_blockNumber":
		res = fmt.Sprintf("0x%x", n.head)
	case "igneum_getWork":
		// the failover guard's cheap read: the share height is the head
		if n.down {
			return fmt.Errorf("dial tcp: connect: connection refused")
		}
		res = map[string]interface{}{"shareHeight": fmt.Sprintf("0x%x", n.head)}
	case "eth_getBlockByNumber":
		var h uint64
		fmt.Sscanf(strings.TrimPrefix(ps[0].(string), "0x"), "%x", &h) //nolint:errcheck
		b, ok := n.blocks[h]
		if !ok {
			res = nil
		} else {
			res = b
		}
	case "igneum_blockReward":
		if !n.rewards {
			return fmt.Errorf("the method igneum_blockReward does not exist")
		}
		var from, to uint64
		fmt.Sscanf(strings.TrimPrefix(ps[0].(string), "0x"), "%x", &from) //nolint:errcheck
		fmt.Sscanf(strings.TrimPrefix(ps[1].(string), "0x"), "%x", &to)   //nolint:errcheck
		var rs []map[string]interface{}
		for h := from; h <= to; h++ {
			rs = append(rs, map[string]interface{}{"height": fmt.Sprintf("0x%x", h), "miner": "0x3e8", "fund": "0xa", "priorityFees": "0x2"})
		}
		res = rs
	default:
		return fmt.Errorf("unexpected method %s", method)
	}
	b, _ := json.Marshal(res)
	return json.Unmarshal(b, out)
}

func solve(t *testing.T, job stratum.Job, target [32]byte) (uint64, [32]byte, [32]byte) {
	t.Helper()
	for nonce := uint64(0); nonce < 1_000_000; nonce++ {
		mix, hash, _ := validate.Null{}.Verify(job.Algorithm, job.Header, job.Height, nonce)
		if validate.MeetsTarget(hash, target) {
			return nonce, mix, hash
		}
	}
	t.Fatal("no solution")
	return 0, [32]byte{}, [32]byte{}
}

func TestShareBlockCreditMature(t *testing.T) {
	ctx := context.Background()
	node := newFakeNode(true)
	book := ledger.New(ledger.NewMemory())
	svc := New(Config{FeeBps: 100, WindowN: 2, ShareDifficulty: 2, Maturity: 3, RewardFallback: nil}, node, validate.Null{}, book)
	now := time.Unix(1_700_000_000, 0)
	svc.now = func() time.Time { return now }

	if err := svc.pollWork(ctx); err != nil {
		t.Fatal(err)
	}
	job := *svc.current
	if job.Height != 1 || validate.Difficulty(job.ShareTarget) < 2 {
		t.Fatalf("job %+v", job)
	}
	// Two shares from two accounts, then a block solution from the first.
	svc.Share(stratum.Share{Account: "0xAAAA", Worker: "w", JobID: job.ID, Height: 1, Weight: 2})
	svc.Share(stratum.Share{Account: "0xBBBB", Worker: "w", JobID: job.ID, Height: 1, Weight: 2})
	nonce, mix, hash := solve(t, job, job.Boundary)
	if err := svc.Block(ctx, job, nonce, mix, hash); err != nil {
		t.Fatal(err)
	}
	svc.Share(stratum.Share{Account: "0xAAAA", Worker: "w", JobID: job.ID, Height: 1, Weight: 2, Block: true})
	if node.submits != 1 {
		t.Fatalf("submits %d", node.submits)
	}
	// The block is canonical at height 1 with our nonce: credited once.
	if err := svc.creditFound(ctx); err != nil {
		t.Fatal(err)
	}
	if err := svc.creditFound(ctx); err != nil {
		t.Fatal(err)
	}
	if svc.Stats.Credited != 1 {
		t.Fatalf("credited %d", svc.Stats.Credited)
	}
	// reward 1000 + 2 fees = 1002; fee 1 percent = 10; 992 across weights a=2, b=2 (window snapshot at the block).
	a, b := book.Balances("0xaaaa"), book.Balances("0xbbbb")
	if a.Immature.Int64() != 496 || b.Immature.Int64() != 496 {
		t.Fatalf("immature a=%s b=%s", a.Immature, b.Immature)
	}
	if book.Liability().Int64() != 992 {
		t.Fatalf("liability %s", book.Liability())
	}
	// Below maturity nothing is payable; at depth 3 it is.
	if err := svc.recon.Run(ctx, now); err != nil {
		t.Fatal(err)
	}
	if book.Balances("0xaaaa").Payable.Sign() != 0 {
		t.Fatal("payable before maturity")
	}
	node.Advance(3)
	if err := svc.recon.Run(ctx, now); err != nil {
		t.Fatal(err)
	}
	if book.Balances("0xaaaa").Payable.Int64() != 496 || book.BlockState("0xblock1") != ledger.StateMature {
		t.Fatalf("after maturity %+v %s", book.Balances("0xaaaa"), book.BlockState("0xblock1"))
	}
	// New work after the sealed block is a new job.
	if err := svc.pollWork(ctx); err != nil {
		t.Fatal(err)
	}
	if svc.current.ID == job.ID || svc.current.Height != 5 {
		t.Fatalf("next job %+v", svc.current)
	}
}

func TestNoRewardSourceMeansNoCreditUntilFallback(t *testing.T) {
	ctx := context.Background()
	node := newFakeNode(false)
	book := ledger.New(ledger.NewMemory())
	svc := New(Config{ShareDifficulty: 1, Maturity: 2}, node, validate.Null{}, book)
	if err := svc.pollWork(ctx); err != nil {
		t.Fatal(err)
	}
	job := *svc.current
	svc.Share(stratum.Share{Account: "0xAAAA", JobID: job.ID, Height: 1, Weight: 1})
	nonce, mix, hash := solve(t, job, job.Boundary)
	if err := svc.Block(ctx, job, nonce, mix, hash); err != nil {
		t.Fatal(err)
	}
	if err := svc.creditFound(ctx); err != nil {
		t.Fatal(err)
	}
	if svc.Stats.Credited != 0 || book.Liability().Sign() != 0 {
		t.Fatal("credited without a reward source")
	}
	svc.cfg.RewardFallback = big.NewInt(500)
	if err := svc.creditFound(ctx); err != nil {
		t.Fatal(err)
	}
	if svc.Stats.Credited != 1 || book.Liability().Int64() != 500 {
		t.Fatalf("fallback credit: %d %s", svc.Stats.Credited, book.Liability())
	}
}

func TestRefreshRewards(t *testing.T) {
	node := newFakeNode(true)
	node.Advance(5)
	svc := New(Config{}, node, validate.Null{}, ledger.New(ledger.NewMemory()))
	svc.refreshRewards(context.Background())
	e := svc.Earnings()
	if e.SubsidyWei != "1000" || e.PriorityFeeMeanWei != "2" || e.Height != 5 {
		t.Fatalf("earnings %+v", e)
	}
	if err := svc.pollWork(context.Background()); err != nil {
		t.Fatal(err)
	}
	if svc.current.SubsidyWei != "1000" {
		t.Fatalf("job carries %q", svc.current.SubsidyWei)
	}
}

// The fault of 11 October 2026 (security's TP-205 watch): the list was set once at start, so after the sealer's
// restart its templates named an empty list and the pool refused every template, silently dead for its members. The
// fix: the list is re-set on the node when a template does not name it (at most once per ListResetInterval) and
// when the node answers again after an error; a template that names it issues a job.
func TestPayoutListReset(t *testing.T) {
	ctx := context.Background()
	node := newFakeNode(true)
	list := json.RawMessage(`[{"address": "0xAAAA", "weight": "0x3"}, {"address": "0xBBBB", "weight": "0x1"}]`)
	svc := New(Config{WindowN: 2, ShareDifficulty: 2, Maturity: 3, PayoutList: list}, node, validate.Null{}, nil)
	now := time.Unix(1_700_000_000, 0)
	svc.now = func() time.Time { return now }
	// start: the list set on the node, as main does
	if err := svc.SetPayoutList(ctx); err != nil {
		t.Fatal(err)
	}
	if node.listSets != 1 || svc.ListResets != 1 {
		t.Fatalf("start set %d/%d", node.listSets, svc.ListResets)
	}
	// the node restarted: its templates name no list; the pool refuses the template and re-sets once per interval
	node.payouts = nil
	if err := svc.pollWork(ctx); err == nil || !strings.Contains(err.Error(), "no job issued") {
		t.Fatalf("expected the refusal, got %v", err)
	}
	if svc.current != nil {
		t.Fatal("a job was issued on a template without the list")
	}
	if node.listSets != 1 {
		t.Fatalf("re-set inside the interval: %d", node.listSets)
	}
	now = now.Add(ListResetInterval)
	if err := svc.pollWork(ctx); err == nil {
		t.Fatal("expected the refusal")
	}
	if node.listSets != 2 || !samePayoutList(node.listSet, list) {
		t.Fatalf("re-set after the interval: %d, %s", node.listSets, node.listSet)
	}
	// the next template names the list: a job issues
	node.payouts = list
	if err := svc.pollWork(ctx); err != nil {
		t.Fatal(err)
	}
	if svc.current == nil || svc.current.Height != 1 {
		t.Fatalf("no job on a template that names the list: %+v", svc.current)
	}
	// the node goes away and answers again: the list is re-set before the next job
	node.down = true
	if err := svc.pollWork(ctx); err == nil {
		t.Fatal("expected the node's error")
	}
	node.down = false
	now = now.Add(ListResetInterval)
	if err := svc.pollWork(ctx); err != nil {
		t.Fatal(err)
	}
	if node.listSets != 3 || svc.ListResets != 3 {
		t.Fatalf("re-set on the node answering again: %d/%d", node.listSets, svc.ListResets)
	}
}
