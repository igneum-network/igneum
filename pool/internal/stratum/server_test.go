package stratum

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"igneum.network/pool/internal/limits"
	"igneum.network/pool/internal/validate"
)

const addr = "0x00112233445566778899aabbccddeeff00112233"

type recordingSink struct {
	mu     sync.Mutex
	shares []Share
	blocks int
}

func (r *recordingSink) Block(ctx context.Context, job Job, nonce uint64, mix, hash [32]byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.blocks++
	return nil
}

func (r *recordingSink) Share(s Share) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.shares = append(r.shares, s)
}

func (r *recordingSink) count() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.shares), r.blocks
}

type client struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
	id   int
}

func (c *client) call(method string, params interface{}) (json.RawMessage, *Error) {
	c.t.Helper()
	c.id++
	p, _ := json.Marshal(params)
	line, _ := json.Marshal(map[string]interface{}{"id": c.id, "method": method, "params": json.RawMessage(p)})
	if _, err := c.conn.Write(append(line, '\n')); err != nil {
		c.t.Fatal(err)
	}
	for {
		frame, err := ReadFrame(c.r)
		if err != nil {
			c.t.Fatal(err)
		}
		var resp struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  *Error          `json:"error"`
		}
		if err := json.Unmarshal(frame, &resp); err != nil {
			c.t.Fatal(err)
		}
		if resp.Method != "" { // a notification; skip
			continue
		}
		return resp.Result, resp.Error
	}
}

func (c *client) readNotify() NotifyParams {
	c.t.Helper()
	for {
		frame, err := ReadFrame(c.r)
		if err != nil {
			c.t.Fatal(err)
		}
		var n struct {
			Method string       `json:"method"`
			Params NotifyParams `json:"params"`
		}
		if err := json.Unmarshal(frame, &n); err != nil {
			c.t.Fatal(err)
		}
		if n.Method == MethodNotify {
			return n.Params
		}
	}
}

func setup(t *testing.T) (*Server, *recordingSink, *client) {
	t.Helper()
	sink := &recordingSink{}
	srv := NewServer(validate.Null{}, sink)
	srv.now = func() time.Time { return time.Unix(1_700_000_000, 0) }
	a, b := net.Pipe()
	go srv.ServeConn(context.Background(), a)
	t.Cleanup(func() { b.Close() })
	return srv, sink, &client{t: t, conn: b, r: bufio.NewReader(b)}
}

// findNonce searches the session's range for a nonce meeting target.
func findNonce(t *testing.T, j Job, start uint64, target [32]byte) (uint64, [32]byte, [32]byte) {
	t.Helper()
	for n := start; n < start+1_000_000; n++ {
		mix, hash, err := validate.Null{}.Verify(j.Algorithm, j.Header, j.Height, n)
		if err != nil {
			t.Fatal(err)
		}
		if validate.MeetsTarget(hash, target) {
			return n, mix, hash
		}
	}
	t.Fatal("no nonce found")
	return 0, [32]byte{}, [32]byte{}
}

func TestSubscribeAuthoriseSubmit(t *testing.T) {
	srv, sink, c := setup(t)
	var sub SubscribeResult
	res, e := c.call(MethodSubscribe, []string{"test/0"})
	if e != nil {
		t.Fatalf("subscribe: %+v", e)
	}
	if err := json.Unmarshal(res, &sub); err != nil {
		t.Fatal(err)
	}
	start, _ := strconv.ParseUint(sub.NonceStart, 10, 64)
	if sub.Protocol != "IS/1" || len(sub.SessionID) != 32 {
		t.Fatalf("subscribe result %+v", sub)
	}
	if _, e := c.call(MethodAuthorize, []string{addr, "worker1"}); e != nil {
		t.Fatalf("authorize: %+v", e)
	}

	job := Job{ID: "00000000000000a1", Height: 10, Algorithm: validate.NullAlgorithm,
		ShareTarget: validate.TargetForDifficulty(16), Boundary: validate.TargetForDifficulty(1 << 20), Clean: true}
	job.Header[0] = 0xab
	srv.SetJob(job)
	n := c.readNotify()
	if n.JobID != job.ID || n.Height != 10 || n.Header != hex.EncodeToString(job.Header[:]) {
		t.Fatalf("notify %+v", n)
	}

	nonce, mix, hash := findNonce(t, job, start, job.ShareTarget)
	params := []string{job.ID, strconv.FormatUint(nonce, 10), hex.EncodeToString(mix[:]), hex.EncodeToString(hash[:])}
	if _, e := c.call(MethodSubmit, params); e != nil {
		t.Fatalf("submit: %+v", e)
	}
	if shares, blocks := sink.count(); shares != 1 || blocks != 0 {
		t.Fatalf("shares %d blocks %d", shares, blocks)
	}
	if sink.shares[0].Account != addr || sink.shares[0].Weight < 16 {
		t.Fatalf("share %+v", sink.shares[0])
	}
	if _, e := c.call(MethodSubmit, params); e == nil || e.Reason != ReasonDuplicate {
		t.Fatalf("duplicate: %+v", e)
	}
	// A nonce outside the session's range is refused before verification.
	bad := []string{job.ID, strconv.FormatUint(start+(1<<limits.NonceRangeBits)+5, 10), params[2], params[3]}
	if _, e := c.call(MethodSubmit, bad); e == nil || e.Reason != ReasonBadNonce {
		t.Fatalf("out of range: %+v", e)
	}
	// A claimed hash that the reference does not reproduce is invalid.
	lie := []string{job.ID, strconv.FormatUint(nonce+1, 10), params[2], params[3]}
	if _, e := c.call(MethodSubmit, lie); e == nil || e.Reason != ReasonInvalid {
		t.Fatalf("lie: %+v", e)
	}
	// A nonce whose hash misses the share target is low difficulty (nonce+1
	// was consumed by the lie above: a refused submission still takes its
	// duplicate slot).
	for k := nonce + 2; ; k++ {
		m, h, _ := validate.Null{}.Verify(job.Algorithm, job.Header, job.Height, k)
		if !validate.MeetsTarget(h, job.ShareTarget) {
			low := []string{job.ID, strconv.FormatUint(k, 10), hex.EncodeToString(m[:]), hex.EncodeToString(h[:])}
			if _, e := c.call(MethodSubmit, low); e == nil || e.Reason != ReasonLowDifficulty {
				t.Fatalf("low: %+v", e)
			}
			break
		}
	}
	// A stale job id.
	stale := []string{"00000000000000ff", params[1], params[2], params[3]}
	if _, e := c.call(MethodSubmit, stale); e == nil || e.Reason != ReasonStale {
		t.Fatalf("stale: %+v", e)
	}
}

func TestBlockSolutionReachesSinkFirst(t *testing.T) {
	srv, sink, c := setup(t)
	var sub SubscribeResult
	res, _ := c.call(MethodSubscribe, []string{"test/0"})
	_ = json.Unmarshal(res, &sub)
	start, _ := strconv.ParseUint(sub.NonceStart, 10, 64)
	c.call(MethodAuthorize, []string{addr, "worker1"})
	// Boundary equal to the share target: every share is a block.
	job := Job{ID: "00000000000000b2", Height: 11, Algorithm: validate.NullAlgorithm,
		ShareTarget: validate.TargetForDifficulty(8), Boundary: validate.TargetForDifficulty(8)}
	srv.SetJob(job)
	c.readNotify()
	nonce, mix, hash := findNonce(t, job, start, job.Boundary)
	if _, e := c.call(MethodSubmit, []string{job.ID, strconv.FormatUint(nonce, 10), hex.EncodeToString(mix[:]), hex.EncodeToString(hash[:])}); e != nil {
		t.Fatalf("submit: %+v", e)
	}
	if shares, blocks := sink.count(); shares != 1 || blocks != 1 || !sink.shares[0].Block {
		t.Fatalf("shares %d blocks %d", shares, blocks)
	}
}

func TestJobIdentityCommitsToEveryField(t *testing.T) {
	base := Job{Height: 10, Algorithm: validate.NullAlgorithm, AlgorithmVersion: 1, Epoch: 2,
		ShareTarget: validate.TargetForDifficulty(16), Boundary: validate.TargetForDifficulty(1 << 20)}
	base.Header[0], base.Parent[0] = 0xab, 0xcd
	id := base.Identity(7310)
	if !limits.ValidJobID(id) {
		t.Fatalf("identity %q is not a valid job id", id)
	}
	if base.Identity(7310) != id {
		t.Fatal("not deterministic")
	}
	variants := map[string]Job{}
	v := base
	v.AlgorithmVersion = 2
	variants["algorithm version"] = v
	v = base
	v.Algorithm = "igneumhash-r0a"
	variants["algorithm"] = v
	v = base
	v.Epoch = 3
	variants["epoch"] = v
	v = base
	v.Parent[1] = 1
	variants["parent"] = v
	v = base
	v.Height = 11
	variants["height"] = v
	v = base
	v.Boundary = validate.TargetForDifficulty(1 << 21)
	variants["boundary"] = v
	v = base
	v.ShareTarget = validate.TargetForDifficulty(32)
	variants["share target"] = v
	v = base
	v.Header[1] = 1
	variants["header"] = v
	for name, j := range variants {
		if j.Identity(7310) == id {
			t.Fatalf("%s change kept the same id", name)
		}
	}
	if base.Identity(7311) == id {
		t.Fatal("chain id change kept the same id")
	}
}

func TestUnauthorisedSubmit(t *testing.T) {
	srv, _, c := setup(t)
	c.call(MethodSubscribe, []string{"test/0"})
	job := Job{ID: "00000000000000c3", Height: 12, Algorithm: validate.NullAlgorithm, ShareTarget: validate.TargetForDifficulty(1)}
	srv.SetJob(job)
	c.readNotify()
	zero := hex.EncodeToString(make([]byte, 32))
	if _, e := c.call(MethodSubmit, []string{job.ID, "0", zero, zero}); e == nil || e.Reason != ReasonUnauthorised {
		t.Fatalf("got %+v", e)
	}
	if _, e := c.call("mining.unknown", []string{}); e == nil || e.Reason != ReasonInvalid {
		t.Fatalf("unknown method: %+v", e)
	}
}
