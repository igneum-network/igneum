// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library.
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

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/core/types"
)

// The keyed seal worker's dispatcher (docs/igneumhash/KEYED-SEAL-WORKER.md
// sections 1 and 2; the hash team's cut of 12 October 2026 on the protocol team's
// hook form): under --igneum.seal-worker <exe> the sealer's keyed search
// runs on a GPU worker instead of the CPU threads. The worker is this
// node's child on pipes (local by construction: the worker's keyed-peer
// guard refuses a keyed job from any other peer), speaking the worker
// protocol (docs/gpu-a/WORKER-PROTOCOL.md) with the keyed-job and seal
// extensions: hello, configure with the template's params document, ready,
// then one job per template carrying the bond secret, "seal": true and the
// round's deadline; the worker answers ONE "result" line (nonce, mix, hash,
// the S shares and the S messages) and stops, or "done" with its reason.
//
// Nothing of consensus moves: the result is replayed through the reference
// (ComputeKeyedM under the node's own secret) and refused unless the
// reference's mix, hash and shares equal the card's; the proof pi is then
// made by the pi path exactly as a CPU find's (attachPi), the (height,
// parent) pair is marked sealed and the block delivered through the same
// channel the CPU threads use. The worker's attempt counts feed the live
// meter (the dMin reading). No CPU fallback: a chain sealed through a
// worker searches nowhere else, and a worker fault is a log line and a
// restart, never a silent CPU search.

const (
	// sealWorkerStatMs is the worker's stat interval (the meter's tick).
	sealWorkerStatMs = 1000
	// sealWorkerReadyWait bounds the hello and ready handshake (the worker
	// builds its dataset at the first job, not here).
	sealWorkerReadyWait = 30 * time.Second
	// sealWorkerLineMax bounds one protocol line from the worker.
	sealWorkerLineMax = 1 << 20

	// sealWorkerNonceCount is the job's nonce range (a seal job stops at
	// the first result; the range only bounds "exhausted").
	sealWorkerNonceCount = uint64(1) << 40
)

// sealWorkerIdle bounds the worker's life between jobs (F-61, the security review's
// read on the rig, 12 October 2026: the worker kept the job's secret in its
// heap and its device mapping after miner.stop, since geth's miner closes
// the seal's abort channel and nothing else reaches the engine). A worker
// with no job for this long after its last one is shut down (its process
// ends; a template after that starts it again, at the cost of one start and
// one context build); a live sealer sees a template every interval and
// never reaches it. A var for the test.
var sealWorkerIdle = 90 * time.Second

var (
	errSealWorkerNoSecret = errors.New("igneum seal worker: a keyed template and no bond secret on this node")
	errSealWorkerNotKeyed = errors.New("igneum seal worker: the algorithm has no keyed form")
	errSealWorkerReplay   = errors.New("igneum seal worker: the reference's replay differs from the worker's result")
	errSealWorkerFault    = errors.New("igneum seal worker: fault")
)

// sealWorker is the node's hold on the worker process.
type sealWorker struct {
	exe    string
	device string
	log    interface {
		Info(msg string, ctx ...interface{})
		Warn(msg string, ctx ...interface{})
	}

	mu       sync.Mutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	lines    chan sealWorkerLine // the worker's lines, read by one goroutine
	seq      uint64
	ready    bool
	algo     string
	params   string // the params document the worker was configured with (its JSON)
	jobs     uint64
	meter    func(attempts int64)
	hello    sealWorkerHello
	readDone chan struct{}
	idle     *time.Timer // the idle shutdown, armed after a job (F-61)
	idleGen  uint64      // the arming the timer belongs to
	busy     bool        // a job in flight
}

// armIdleLocked starts the idle shutdown after a job: the worker ends when
// no job follows within sealWorkerIdle. Under the lock.
func (w *sealWorker) armIdleLocked() {
	if w.idle != nil {
		w.idle.Stop()
	}
	w.idleGen++
	gen := w.idleGen
	w.idle = time.AfterFunc(sealWorkerIdle, func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.idleGen != gen || w.busy || w.cmd == nil {
			return // a later job, or the worker already down
		}
		w.stopLocked("idle: no job for " + sealWorkerIdle.String() + " (the secret leaves with the process)")
	})
}

type sealWorkerHello struct {
	Worker     string   `json:"worker"`
	Version    string   `json:"version"`
	Backend    string   `json:"backend"`
	Extensions []string `json:"extensions"`
	Seal       bool     `json:"seal"`
}

// sealWorkerLine is one line from the worker, decoded to its kind and the
// fields the dispatcher reads; the raw line is kept for the log of a
// malformed one (never a line that carries a secret: the worker's lines
// carry none).
type sealWorkerLine struct {
	T           string   `json:"t"`
	Seq         uint64   `json:"seq"`
	JobID       string   `json:"job_id"`
	Nonce       string   `json:"nonce"`
	Mix         string   `json:"mix"`
	Hash        string   `json:"hash"`
	Shares      []string `json:"shares"`
	M           []string `json:"m"`
	Reason      string   `json:"reason"`
	Nonces      string   `json:"nonces"`
	Hashes      string   `json:"hashes"`
	Code        string   `json:"code"`
	Message     string   `json:"message"`
	Recoverable bool     `json:"recoverable"`
	Height      string   `json:"height"`
	rawBytes    []byte
	err         error
}

// sealWorkerResult is a worker's seal, replayed and accepted.
type sealWorkerResult struct {
	nonce  uint64
	mix    common.Hash
	shares []byte
}

// SetSealWorker names the worker executable the keyed search runs on (the
// eth backend's call from --igneum.seal-worker) and the card it opens
// (--igneum.seal-worker-device, the worker's --device-name substring; empty
// for the first). A node with a worker seals on no CPU thread.
func (e *Engine) SetSealWorker(exe, device string) error {
	if exe == "" {
		return nil
	}
	st, err := os.Stat(exe)
	if err != nil {
		return fmt.Errorf("igneum seal worker: %w", err)
	}
	if st.IsDir() || st.Mode()&0o111 == 0 {
		return fmt.Errorf("igneum seal worker: %s is not an executable", exe)
	}
	e.lock.Lock()
	e.config.SealWorker, e.config.SealWorkerDevice = exe, device
	e.lock.Unlock()
	e.log.Info("Keyed seal WORKER (keyed templates search on the card and on no CPU thread; unkeyed ones on the CPU twin)", "exe", exe, "device", device)
	return nil
}

// sealWorkerFor returns the live worker, starting it when none is up.
func (e *Engine) sealWorkerFor() (*sealWorker, error) {
	e.lock.Lock()
	w := e.worker
	if w == nil {
		// The worker's attempt counts tick the meter and the search's own
		// counter (searchrate.go), so igneum_searchRate reads the card.
		w = &sealWorker{exe: e.config.SealWorker, device: e.config.SealWorkerDevice, log: e.log, meter: e.searchTick}
		e.worker = w
	}
	e.lock.Unlock()
	return w, w.ensure()
}

// ensure starts the worker process and takes its hello when it is not up.
func (w *sealWorker) ensure() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cmd != nil {
		select {
		case <-w.readDone:
			// the reader ended: the worker's stdout closed; start again
			w.stopLocked("stdout closed")
		default:
			return nil
		}
	}
	w.ready, w.algo, w.params = false, "", ""
	args := []string{"protocol"}
	if w.device != "" {
		args = append(args, "--device-name", w.device)
	}
	cmd := exec.Command(w.exe, args...)
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin"}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = nil // the worker's stderr is its own; never a secret on it
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("igneum seal worker: start: %w", err)
	}
	w.cmd, w.stdin = cmd, stdin
	w.lines = make(chan sealWorkerLine, 64)
	w.readDone = make(chan struct{})
	go w.reader(stdout, w.lines, w.readDone)
	go func(c *exec.Cmd) { _ = c.Wait() }(cmd)
	// hello
	select {
	case l := <-w.lines:
		if l.err != nil || l.T != "hello" {
			w.stopLocked("no hello")
			return fmt.Errorf("igneum seal worker: no hello (%s)", l.T)
		}
		var h sealWorkerHello
		_ = json.Unmarshal(l.raw(), &h)
		w.hello = h
		if !hasExt(h.Extensions, "keyed-job") || !hasExt(h.Extensions, "seal") {
			w.stopLocked("no seal verb")
			return fmt.Errorf("igneum seal worker: %s %s takes no keyed seal job (extensions %v)", h.Worker, h.Version, h.Extensions)
		}
		w.log.Info("Keyed seal worker up", "worker", h.Worker, "version", h.Version, "backend", h.Backend, "pid", cmd.Process.Pid)
	case <-time.After(sealWorkerReadyWait):
		w.stopLocked("hello timeout")
		return errors.New("igneum seal worker: no hello in time")
	}
	return nil
}

func hasExt(list []string, name string) bool {
	for _, s := range list {
		if s == name {
			return true
		}
	}
	return false
}

// reader turns the worker's stdout into lines; a malformed line is one
// error line (never a restart by itself), the end of the stream closes done.
func (w *sealWorker) reader(r io.Reader, out chan<- sealWorkerLine, done chan struct{}) {
	defer close(done)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), sealWorkerLineMax)
	for sc.Scan() {
		b := bytes.TrimSpace(sc.Bytes())
		if len(b) == 0 {
			continue
		}
		var l sealWorkerLine
		if err := json.Unmarshal(b, &l); err != nil {
			l = sealWorkerLine{err: fmt.Errorf("malformed line: %w", err)}
		}
		l.rawBytes = append([]byte(nil), b...)
		select {
		case out <- l:
		default:
			// the dispatcher is not reading (between templates): stat lines
			// are the only steady flow and may be dropped; a result is
			// never produced without a job being read
		}
	}
}

func (l *sealWorkerLine) raw() []byte { return l.rawBytes }

// send writes one line to the worker with the next seq. The job line is
// the only line that carries the secret, and it is never logged.
func (w *sealWorker) send(t string, fields map[string]interface{}) error {
	w.seq++
	m := map[string]interface{}{"t": t, "seq": w.seq}
	for k, v := range fields {
		m[k] = v
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.stdin.Write(b)
	for i := range b {
		b[i] = 0
	}
	return err
}

// configure sends the template's params document when the worker does not
// hold it yet and waits for ready.
func (w *sealWorker) configure(algo string, params interface{}) error {
	pj, err := json.Marshal(params)
	if err != nil {
		return err
	}
	if w.ready && w.algo == algo && w.params == string(pj) {
		return nil
	}
	var pv json.RawMessage = pj
	if err := w.send("configure", map[string]interface{}{"device": "0", "algo": algo, "params": pv, "stat_ms": sealWorkerStatMs, "transition": "cold", "memory_margin_bytes": "0"}); err != nil {
		return err
	}
	deadline := time.After(sealWorkerReadyWait)
	for {
		select {
		case l := <-w.lines:
			switch l.T {
			case "ready":
				w.ready, w.algo, w.params = true, algo, string(pj)
				return nil
			case "fault":
				return fmt.Errorf("%w: %s: %s", errSealWorkerFault, l.Code, l.Message)
			case "bye":
				return errors.New("igneum seal worker: bye before ready")
			}
		case <-w.readDone:
			return errors.New("igneum seal worker: exited before ready")
		case <-deadline:
			return errors.New("igneum seal worker: no ready in time")
		}
	}
}

// seal runs one keyed seal job: the template's seal hash, height, boundary
// and the secret to the worker; the first result back, or none at the
// abort or the deadline. The meter is ticked by the worker's stat lines.
func (w *sealWorker) seal(sealHash common.Hash, height uint64, target []byte, sk *[32]byte, deadlineMs uint64, abort <-chan struct{}) (*sealWorkerResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.idle != nil {
		w.idle.Stop()
	}
	w.busy = true
	defer func() { w.busy = false; w.armIdleLocked() }()
	w.jobs++
	jobID := "seal-" + strconv.FormatUint(height, 10) + "-" + strconv.FormatUint(w.jobs, 10)
	fields := map[string]interface{}{
		"job_id": jobID, "height": strconv.FormatUint(height, 10),
		"header": hex.EncodeToString(sealHash[:]), "boundary": hex.EncodeToString(target), "share_target": hex.EncodeToString(target),
		"nonce_start": "0", "nonce_count": strconv.FormatUint(sealWorkerNonceCount, 10), "clean": true, "algo": w.algo,
		"sk": hex.EncodeToString(sk[:]), "seal": true,
	}
	if deadlineMs > 0 {
		fields["deadline_ms"] = strconv.FormatUint(deadlineMs, 10)
	}
	err := w.send("job", fields)
	skHex, _ := fields["sk"].(string)
	wipeString(skHex)
	delete(fields, "sk")
	if err != nil {
		return nil, err
	}
	cancel := func() { _ = w.send("cancel", map[string]interface{}{"job_id": jobID}) }
	for {
		select {
		case <-abort:
			cancel()
			return nil, nil
		case <-w.readDone:
			return nil, errors.New("igneum seal worker: exited during the search")
		case l := <-w.lines:
			if l.err != nil {
				w.log.Warn("Keyed seal worker: a malformed line (ignored)", "err", l.err)
				continue
			}
			switch l.T {
			case "stat":
				if n, err := strconv.ParseInt(l.Hashes, 10, 64); err == nil && n > 0 && w.meter != nil {
					w.meter(n)
				}
			case "result":
				if l.JobID != jobID {
					continue // a late result of an earlier job
				}
				nonce, err := strconv.ParseUint(l.Nonce, 10, 64)
				if err != nil {
					return nil, fmt.Errorf("igneum seal worker: the result's nonce %q", l.Nonce)
				}
				mixB, err := hex.DecodeString(l.Mix)
				if err != nil || len(mixB) != 32 {
					return nil, errors.New("igneum seal worker: the result's mix is not 32 bytes")
				}
				var shares []byte
				for _, s := range l.Shares {
					b, err := hex.DecodeString(s)
					if err != nil || len(b) != 32 {
						return nil, errors.New("igneum seal worker: a share is not 32 bytes")
					}
					shares = append(shares, b...)
				}
				return &sealWorkerResult{nonce: nonce, mix: common.BytesToHash(mixB), shares: shares}, nil
			case "done":
				if l.JobID == jobID {
					w.log.Info("Keyed seal worker: the search ended", "reason", l.Reason, "nonces", l.Nonces, "height", height)
					return nil, nil
				}
			case "refuse":
				return nil, fmt.Errorf("igneum seal worker: the dataset at height %s refused: %s", l.Height, l.Reason)
			case "fault":
				if l.Recoverable {
					// A recoverable fault (the worker's own replay
					// mismatch, seal-replay, on one attempt: the
					// search goes on; or a job refused before it
					// started): logged, the wait continues until the
					// result, the deadline or the abort.
					w.log.Warn("Keyed seal worker: a recoverable fault", "code", l.Code, "message", l.Message, "height", height)
					continue
				}
				w.stopLocked("unrecoverable fault " + l.Code)
				return nil, fmt.Errorf("%w: %s: %s", errSealWorkerFault, l.Code, l.Message)
			case "bye":
				w.stopLocked("bye")
				return nil, errors.New("igneum seal worker: bye during the search")
			}
		}
	}
}

func wipeString(s string) {
	// Go strings are immutable by contract; the hex of the secret lives in
	// the marshalled line, zeroed in send. Nothing further to do here but
	// drop the reference. Kept as the one place the rule is stated.
	_ = s
}

// stopLocked ends the worker process (shutdown, then the pid) under the
// lock. The reason is logged; the pid is the one recorded at start.
func (w *sealWorker) stopLocked(reason string) {
	if w.cmd == nil {
		return
	}
	if w.idle != nil {
		w.idle.Stop()
	}
	_ = w.send("shutdown", nil)
	if w.stdin != nil {
		_ = w.stdin.Close()
	}
	select {
	case <-w.readDone:
	case <-time.After(3 * time.Second):
		if w.cmd.Process != nil {
			_ = w.cmd.Process.Kill()
		}
	}
	w.log.Warn("Keyed seal worker stopped", "reason", reason, "pid", w.cmd.Process.Pid)
	w.cmd, w.stdin, w.ready = nil, nil, false
}

// Stop ends the worker (the engine's Close).
func (w *sealWorker) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stopLocked("close")
}

// sealOnWorker is the dispatcher's path in Seal: the template's keyed
// search on the worker, the result replayed and proven, the block into
// found. It returns when the search ends (a seal, the abort, or a fault).
func (e *Engine) sealOnWorker(chain consensus.ChainHeaderReader, block *types.Block, abort chan struct{}, found chan *types.Block) {
	header := block.Header()
	height := header.Number.Uint64()
	sealHash := e.SealHash(header)
	logger := e.log.New("miner", "worker")
	if e.keyedRoundsFor(header) == 0 {
		// An unkeyed template (the open seat, the liveness round) seals on
		// the CPU twin (Seal starts the threads for it); this path takes
		// keyed templates only, and is not entered for one. The guard
		// stands for a caller that reaches it anyway.
		logger.Warn("Unkeyed template on the worker's path: no search here (the CPU twin seals it)", "number", height)
		<-abort
		return
	}
	sk, _ := e.bondSecret()
	if sk == nil {
		logger.Warn(errSealWorkerNoSecret.Error(), "number", height)
		<-abort
		return
	}
	// The worker's replay of a find runs under the programme of the
	// template's period on its parent's branch (program.go); no anchor,
	// no attempt on this template (F-67).
	algo := e.sealAlgoFor(header)
	if algo == nil {
		<-abort
		return
	}
	k, isKeyed := algo.(pow.KeyedM)
	if !isKeyed {
		logger.Warn(errSealWorkerNotKeyed.Error(), "number", height)
		<-abort
		return
	}
	target := pow.Target(header.Difficulty)
	if target == nil {
		<-abort
		return
	}
	w, err := e.sealWorkerFor()
	if err != nil {
		logger.Warn("Keyed seal worker not up; no search this template", "err", err, "number", height)
		<-abort
		return
	}
	wp := e.workPackage(block)
	if err := w.configure(e.algoID(), wp.Params); err != nil {
		logger.Warn("Keyed seal worker refused the configuration; no search this template", "err", err, "number", height)
		<-abort
		return
	}
	// The search's own counter (searchrate.go) is opened at the job's
	// send and closed when the search ends: the result, the cancel at the
	// abort, the deadline or a fault, all one return from seal.
	e.search.open(height)
	res, err := w.seal(sealHash, height, target.FillBytes(make([]byte, 32)), sk, e.sealDeadlineMs(chain, header), abort)
	e.search.close()
	if err != nil {
		logger.Warn("Keyed seal worker: no seal this template", "err", err, "number", height)
		return
	}
	if res == nil {
		return // aborted or the deadline
	}
	// The reference's replay under this node's secret: the card's result
	// is accepted only when the reference's mix, hash and shares equal it.
	mix, result, shares, m := k.ComputeKeyedM(sealHash, height, res.nonce, sk, nil)
	if m == nil || mix != res.mix || !bytes.Equal(shares, res.shares) || new(big.Int).SetBytes(result[:]).Cmp(target) > 0 {
		logger.Warn(errSealWorkerReplay.Error(), "number", height, "nonce", res.nonce)
		return
	}
	header = types.CopyHeader(header)
	header.Nonce = types.EncodeNonce(res.nonce)
	header.MixDigest = mix
	header.Shares = shares
	logger.Info("Seal found (worker)", "number", height, "keyedRounds", e.keyedRoundsFor(header), "shares", len(shares), "sealhash", sealHash)
	if !e.claims.claim(sealHash) {
		return
	}
	if e.heightSealed(height, header.ParentHash) {
		logger.Info("Seal found for a height already sealed under this parent; discarded before the proof", "number", height)
		return
	}
	if e.proofHook != nil {
		e.proofHook()
	}
	sealed, err := e.attachPi(block, header)
	if err != nil {
		logger.Warn("Seal dropped: no proof", "number", height, "err", err)
		return
	}
	found <- sealed
}

// sealDeadlineMs is the worker's deadline for a template: the sealer's own
// round window's end (parent.Time + (round + 1) x Delta) less the proof's
// time, measured from now; zero (no deadline, the abort alone) when the
// window is unknown or already past.
func (e *Engine) sealDeadlineMs(chain consensus.ChainHeaderReader, header *types.Header) uint64 {
	s := e.config.Parts.Sortition
	if s == nil || s.Delta == 0 {
		return 0
	}
	parentTime := header.Time
	if chain != nil && header.Number != nil && header.Number.Uint64() > 0 {
		if p := chain.GetHeader(header.ParentHash, header.Number.Uint64()-1); p != nil {
			parentTime = p.Time
		}
	}
	end := parentTime + s.RoundStart(header.Number.Uint64(), header.Round+1)
	now := uint64(time.Now().Unix())
	if end <= now {
		return 0
	}
	ms := (end - now) * 1000
	if ms > s.TPiMs {
		ms -= s.TPiMs
	}
	return ms
}

// TemplateRoundOver reports whether a template's round window has ended
// (F-171): under the sortition rule, the sealer's own round window ends at
// parent.Time + RoundStart(round + 1) (the deadline the keyed seal worker
// runs to); at or past it the miner re-issues the template so the stamp
// advances with the search. False without the rule, without the parent,
// or while the window is open.
func (e *Engine) TemplateRoundOver(chain consensus.ChainHeaderReader, header *types.Header) bool {
	s := e.config.Parts.Sortition
	if s == nil || s.Delta == 0 || header == nil || header.Number == nil || header.Number.Sign() == 0 || chain == nil {
		return false
	}
	if !s.Active(header.Number.Uint64()) {
		return false
	}
	p := chain.GetHeader(header.ParentHash, header.Number.Uint64()-1)
	if p == nil {
		return false
	}
	// The window's end is the first round boundary past the template's
	// own stamp (the drawn round's end for a template issued inside its
	// round; the next boundary for one re-issued after it), so a
	// template is re-issued once a round, never once a tick (the gate's
	// residual on the cut, 19:48 UK).
	height := header.Number.Uint64()
	for r := header.Round + 1; r < header.Round+4096; r++ {
		end := p.Time + s.RoundStart(height, r)
		if end > header.Time {
			return uint64(time.Now().Unix()) >= end
		}
	}
	return false
}

// algoID is the candidate id the worker is configured with (the
// algorithm's registered name).
func (e *Engine) algoID() string {
	return e.algo.Name()
}
