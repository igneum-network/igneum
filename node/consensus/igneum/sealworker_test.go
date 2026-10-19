package igneum

import (
	"crypto/sha256"
	"encoding/binary"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/core/types"
)

// keyedStub is a keyed algorithm over the null one for the dispatcher's
// test: S shares and the messages derived with sha256 (so the stub worker
// below, a python script, reproduces them), the result always under the
// target. It is not a hash function of any chain.
type keyedStub struct {
	pow.Algorithm
	s uint64
}

func (k *keyedStub) KeyedRounds(height uint64) uint64 { return k.s }

func (k *keyedStub) ComputeKeyed(headerHash common.Hash, height uint64, nonce uint64, sk *[32]byte, shares []byte) (common.Hash, common.Hash, []byte) {
	mix, result, out, _ := k.ComputeKeyedM(headerHash, height, nonce, sk, shares)
	return mix, result, out
}

func (k *keyedStub) ComputeKeyedM(headerHash common.Hash, height uint64, nonce uint64, sk *[32]byte, shares []byte) (common.Hash, common.Hash, []byte, []byte) {
	var n [8]byte
	binary.LittleEndian.PutUint64(n[:], nonce)
	if sk != nil {
		shares = nil
		for i := uint64(0); i < k.s; i++ {
			h := sha256.Sum256(append(append(append(append([]byte{}, sk[:]...), headerHash[:]...), n[:]...), byte(i)))
			shares = append(shares, h[:]...)
		}
	} else if uint64(len(shares)) != k.s*32 {
		return common.Hash{}, common.Hash{}, nil, nil
	}
	mix := sha256.Sum256(append(append(append([]byte("mix"), headerHash[:]...), n[:]...), shares...))
	m1 := sha256.Sum256(append([]byte("m1"), shares...))
	m2 := sha256.Sum256(append([]byte("m2"), shares...))
	return common.Hash(mix), common.Hash{}, shares, append(m1[:], m2[:]...)
}

// The stub worker: the worker protocol's seal verb as the CUDA worker
// speaks it (hello with the extensions, ready on configure, one result
// line per seal job), its behaviour chosen by the --device-name argument
// (result, badmix, fault, silent). Its facts go to a file beside it: which
// lines it took (never the secret's value).
const sealWorkerStub = `#!/usr/bin/env python3
import sys, json, hashlib, struct, os, time
mode = "result"
a = sys.argv[1:]
for i in range(len(a) - 1):
    if a[i] == "--device-name": mode = a[i + 1]
facts = open(os.path.join(os.path.dirname(os.path.abspath(sys.argv[0])), "facts-" + mode + ".txt"), "a")
def send(t, **kw):
    kw["t"] = t; kw["seq"] = 1
    sys.stdout.write(json.dumps(kw) + "\n"); sys.stdout.flush()
send("hello", worker="stub-seal", version="0", backend="stub", extensions=["keyed-job", "seal"], seal=True, algos=[])
for line in sys.stdin:
    j = json.loads(line)
    t = j.get("t")
    if t == "configure":
        facts.write("configure algo=%s\n" % j.get("algo")); facts.flush()
        send("ready", device="stub", algo=j.get("algo"), budget={})
    elif t == "job":
        facts.write("job seal=%s has_sk=%s deadline=%s clean=%s\n" % (j.get("seal"), "sk" in j, j.get("deadline_ms", ""), j.get("clean"))); facts.flush()
        if mode == "fault":
            send("fault", code="keyed-peer", message="a keyed job is taken only from a loopback or Unix-socket peer", recoverable=False)
            continue
        if mode == "replayfault":
            send("fault", code="seal-replay", message="the reference's replay differs on one attempt; the search goes on", recoverable=True)
        send("stat", job_id=j["job_id"], hashes="4096", elapsed_ms=10, state="running")
        if mode == "silent":
            continue
        sk = bytes.fromhex(j["sk"]); hdr = bytes.fromhex(j["header"]); nonce = 5
        n = struct.pack("<Q", nonce)
        shares = b"".join(hashlib.sha256(sk + hdr + n + bytes([i])).digest() for i in range(2))
        mix = hashlib.sha256(b"mix" + hdr + n + shares).digest()
        if mode == "badmix":
            mix = bytes([mix[0] ^ 1]) + mix[1:]
        m = [hashlib.sha256(b"m1" + shares).hexdigest(), hashlib.sha256(b"m2" + shares).hexdigest()]
        send("result", job_id=j["job_id"], nonce=str(nonce), mix=mix.hex(), hash="00" * 32, shares=[shares[:32].hex(), shares[32:].hex()], m=m)
    elif t == "cancel":
        facts.write("cancel %s\n" % j.get("job_id")); facts.flush()
    elif t == "shutdown":
        facts.write("shutdown\n"); facts.flush()
        send("bye")
        break
`

func newSealWorkerEngine(t *testing.T, mode string) (*Engine, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("pipes and a python stub")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on this box")
	}
	dir := t.TempDir()
	stub := filepath.Join(dir, "worker-stub")
	if err := os.WriteFile(stub, []byte(sealWorkerStub), 0o700); err != nil {
		t.Fatal(err)
	}
	base, err := pow.NewDev("null", nil)
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(Config{Algorithm: &keyedStub{Algorithm: base, s: 2}, Parts: testParts()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	if err := e.SetSealWorker(stub, mode); err != nil {
		t.Fatal(err)
	}
	var sk [32]byte
	sk[0], sk[31] = 7, 9
	pk := common.Hash{0xaa}
	e.lock.Lock()
	e.bondSK, e.keyedBond = &sk, &pk
	e.lock.Unlock()
	return e, filepath.Join(dir, "facts-"+mode+".txt")
}

func sealWorkerTemplate(parent common.Hash) *types.Block {
	return types.NewBlockWithHeader(&types.Header{Number: big.NewInt(7), ParentHash: parent, Difficulty: new(big.Int).Set(testDifficulty), Time: uint64(time.Now().Unix()), GasLimit: 1})
}

func sealThrough(t *testing.T, e *Engine, b *types.Block, wait time.Duration, stopAfter time.Duration) *types.Block {
	t.Helper()
	results := make(chan *types.Block, 1)
	stop := make(chan struct{})
	if err := e.Seal(nil, b, results, stop); err != nil {
		t.Fatal(err)
	}
	if stopAfter > 0 {
		time.AfterFunc(stopAfter, func() { close(stop) })
	} else {
		defer close(stop)
	}
	select {
	case r := <-results:
		return r
	case <-time.After(wait):
		return nil
	}
}

func readFacts(t *testing.T, path string) string {
	t.Helper()
	b, _ := os.ReadFile(path)
	return string(b)
}

// A seal through the worker: the result replayed through the reference and
// delivered with the worker's nonce, mix and shares; the pair marked
// sealed; the job line carried the secret and the seal verb; the stat
// lines reached the meter.
func TestSealWorkerSealsAKeyedTemplate(t *testing.T) {
	e, facts := newSealWorkerEngine(t, "result")
	parent := common.HexToHash("0xaa")
	b := sealThrough(t, e, sealWorkerTemplate(parent), 10*time.Second, 0)
	if b == nil {
		t.Fatalf("no seal through the worker; facts:\n%s", readFacts(t, facts))
	}
	h := b.Header()
	if h.Nonce.Uint64() != 5 {
		t.Fatalf("nonce %d, want the worker's 5", h.Nonce.Uint64())
	}
	if len(h.Shares) != 64 {
		t.Fatalf("shares %d bytes, want 64", len(h.Shares))
	}
	k := e.algo.(*keyedStub)
	sk, _ := e.bondSecret()
	mix, _, shares := k.ComputeKeyed(e.SealHash(b.Header()), 7, 5, sk, nil)
	if mix != h.MixDigest || string(shares) != string(h.Shares) {
		t.Fatal("the delivered seal is not the reference's replay")
	}
	if !e.HeightSealed(7, parent) {
		t.Fatal("the pair is not marked sealed")
	}
	f := readFacts(t, facts)
	if !strings.Contains(f, "job seal=True has_sk=True") || !strings.Contains(f, "clean=True") {
		t.Fatalf("the job line lacked the seal verb or the secret:\n%s", f)
	}
	if !strings.Contains(f, "configure algo=null") {
		t.Fatalf("no configure before the job:\n%s", f)
	}
	if c := e.hashrate.Snapshot().Count(); c < 4096 {
		t.Fatalf("the meter read %d, want the worker's stat", c)
	}
	// The search's own counter (igneum_searchRate) read the card's
	// attempts at the template's height and the search is closed.
	if sr := e.SearchRateNow(); sr.Height != 7 || sr.Attempts < 4096 || sr.Running {
		t.Fatalf("the search counter read %+v, want height 7, the worker's attempts, closed", sr)
	}
	// The same pair again: no second search (the sealer's one-seal rule).
	if sealThrough(t, e, sealWorkerTemplate(parent), 500*time.Millisecond, 0) != nil {
		t.Fatal("a second seal for a sealed pair")
	}
}

// A result whose mix the reference's replay does not reproduce is refused:
// no block.
func TestSealWorkerRefusesAWrongReplay(t *testing.T) {
	e, facts := newSealWorkerEngine(t, "badmix")
	if b := sealThrough(t, e, sealWorkerTemplate(common.HexToHash("0xbb")), 3*time.Second, 0); b != nil {
		t.Fatalf("a seal delivered from a result the replay refuses; facts:\n%s", readFacts(t, facts))
	}
	if e.HeightSealed(7, common.HexToHash("0xbb")) {
		t.Fatal("the pair marked sealed without a seal")
	}
}

// An unrecoverable fault (the custody guard's keyed-peer) ends the worker:
// no block, the process stopped, the next template starts it again.
func TestSealWorkerStopsOnAnUnrecoverableFault(t *testing.T) {
	e, facts := newSealWorkerEngine(t, "fault")
	if b := sealThrough(t, e, sealWorkerTemplate(common.HexToHash("0xcc")), 3*time.Second, 0); b != nil {
		t.Fatal("a seal delivered after a fault")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		e.worker.mu.Lock()
		stopped := e.worker.cmd == nil
		e.worker.mu.Unlock()
		if stopped || time.Now().After(deadline) {
			if !stopped {
				t.Fatalf("the worker was not stopped after keyed-peer; facts:\n%s", readFacts(t, facts))
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(readFacts(t, facts), "shutdown") {
		t.Fatalf("no shutdown line reached the worker:\n%s", readFacts(t, facts))
	}
}

// A recoverable fault (the worker's seal-replay on one attempt) does not
// end the template's search: the result after it is taken.
func TestSealWorkerKeepsWaitingThroughARecoverableFault(t *testing.T) {
	e, facts := newSealWorkerEngine(t, "replayfault")
	b := sealThrough(t, e, sealWorkerTemplate(common.HexToHash("0xef")), 10*time.Second, 0)
	if b == nil {
		t.Fatalf("no seal after a recoverable fault; facts:\n%s", readFacts(t, facts))
	}
	if b.Header().Nonce.Uint64() != 5 {
		t.Fatal("the seal after the fault is not the worker's result")
	}
}

// An unkeyed template (the open seat, the liveness round) under a worker
// seals on the CPU twin: the worker is not started for it (the protocol team's
// read of 12 October 2026: the chain's floor must not go dark on a card
// sealer).
func TestSealWorkerLeavesUnkeyedTemplatesToTheCPU(t *testing.T) {
	e, facts := newSealWorkerEngine(t, "result")
	e.algo.(*keyedStub).s = 0 // the document is unkeyed from here
	e.SetThreads(1)
	b := sealThrough(t, e, sealWorkerTemplate(common.HexToHash("0xee")), 10*time.Second, 0)
	if b == nil {
		t.Fatal("an unkeyed template found no seal on the CPU twin")
	}
	if len(b.Header().Shares) != 0 {
		t.Fatal("an unkeyed seal carries shares")
	}
	if f := readFacts(t, facts); f != "" {
		t.Fatalf("the worker was started for an unkeyed template:\n%s", f)
	}
	e.lock.Lock()
	started := e.worker != nil
	e.lock.Unlock()
	if started {
		t.Fatal("the worker process was started for an unkeyed template")
	}
}

// The abort (a re-cut template or the stop) cancels the job on the worker.
func TestSealWorkerCancelsOnAbort(t *testing.T) {
	e, facts := newSealWorkerEngine(t, "silent")
	if b := sealThrough(t, e, sealWorkerTemplate(common.HexToHash("0xdd")), 3*time.Second, 400*time.Millisecond); b != nil {
		t.Fatal("a seal from a silent worker")
	}
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(readFacts(t, facts), "cancel seal-7-1") {
		if time.Now().After(deadline) {
			t.Fatalf("no cancel reached the worker after the abort:\n%s", readFacts(t, facts))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// F-61 (the security review's read on the rig): a worker with no job after its last
// one is shut down within the idle bound, so the job's secret leaves with
// the process after miner.stop (geth closes the seal's abort channel and
// nothing else reaches the engine); the next template starts it again.
func TestSealWorkerEndsWhenIdle(t *testing.T) {
	old := sealWorkerIdle
	sealWorkerIdle = 300 * time.Millisecond
	t.Cleanup(func() { sealWorkerIdle = old })
	e, facts := newSealWorkerEngine(t, "seal")
	if b := sealThrough(t, e, sealWorkerTemplate(common.HexToHash("0xee")), 5*time.Second, 0); b == nil {
		t.Fatalf("no seal through the worker:\n%s", readFacts(t, facts))
	}
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(readFacts(t, facts), "shutdown") {
		if time.Now().After(deadline) {
			t.Fatalf("the idle worker was not shut down:\n%s", readFacts(t, facts))
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.lock.Lock()
	w := e.worker
	e.lock.Unlock()
	w.mu.Lock()
	down := w.cmd == nil
	w.mu.Unlock()
	if !down {
		t.Fatal("the worker's process is still recorded after the idle shutdown")
	}
	// the next template starts the worker again and seals
	if b := sealThrough(t, e, sealWorkerTemplate(common.HexToHash("0xef")), 5*time.Second, 0); b == nil {
		t.Fatalf("no seal through the restarted worker:\n%s", readFacts(t, facts))
	}
	if strings.Count(readFacts(t, facts), "configure") < 2 {
		t.Fatalf("the worker was not started a second time:\n%s", readFacts(t, facts))
	}
}
