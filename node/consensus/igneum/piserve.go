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

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/pi"
	"github.com/ethereum/go-ethereum/log"
)

// The resident prover (the hash team's wire of 12 October 2026, main
// d7578293, KEYED-SEAL-WORKER.md 2.1): one pi-prove PROCESS for the
// sealer's life, started once with the secret's file and a Unix socket
// under the work directory, holding its context warm, answering each
// seal's messages with the receipt bytes in memory. The per-seal process of
// provePi paid the prover's start on every seal (445 to 742 ms on the
// ninth chain's cards against 147 to 205 ms of proof, the profile of 02:15
// UK), so the clock's tPiMs measured the process and not the proof. Chosen
// by --igneum.pi-resident; the one-shot form stays for the lab and the
// chains that run it.
//
// The wire, per seal: the client writes one line {"domain": 0, "m": ["<32
// hex>", ...]} and reads one JSON line {"pk", "shares", "bytes": N,
// "prove_ms", ...} followed by exactly N raw bytes (u8(2) || receipt); a
// refusal is {"refused": "<code>"} with the loop up; {"quit": true} answers
// {"quit": true}, wipes the secret and exits removing the socket. The
// prover holds ONE secret, this sealer's; a process that exits is restarted
// on the next seal (a crash costs one seal, never the secret).

// piServer is the client's hold on the resident process.
type piServer struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	conn   net.Conn
	rd     *bufio.Reader
	sock   string
	skFile string
	done   chan struct{} // closed when the process exits
	starts int
}

// piServeReply is the server's line per seal.
type piServeReply struct {
	PK      string   `json:"pk"`
	Shares  []string `json:"shares"`
	Bytes   int      `json:"bytes"`
	ProveMs float64  `json:"prove_ms"`
	Refused string   `json:"refused"`
	Quit    bool     `json:"quit"`
}

// piServeStartTimeout bounds the wait for the process's ready line and the
// first connection (the warm-up proof is inside it: 1.7 s on a CPU, under a
// second on a card).
const piServeStartTimeout = 60 * time.Second

// piServeSealTimeout bounds one seal's round trip.
const piServeSealTimeout = 30 * time.Second

var errPiServeDown = errors.New("igneum pi: the resident prover is not up")

// ownReceipts is the record of the receipts this node made (by the proof's
// hash), for the sealer's own import: the receipt is the prover's output
// over the node's own messages, so VerifyPi trusts it (the hash team's
// own-receipt skip) and every other node verifies as before. Bounded.
type ownReceipts struct {
	mu    sync.Mutex
	set   map[common.Hash]bool
	order []common.Hash
}

const ownReceiptsKeep = 256

func (o *ownReceipts) add(h common.Hash) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.set == nil {
		o.set = make(map[common.Hash]bool)
	}
	if o.set[h] {
		return
	}
	if len(o.order) >= ownReceiptsKeep {
		delete(o.set, o.order[0])
		o.order = o.order[1:]
	}
	o.set[h] = true
	o.order = append(o.order, h)
}

func (o *ownReceipts) has(h common.Hash) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.set[h]
}

// provePiResident is provePi's form through the resident prover: the
// process started on the first seal (and again after an exit), one request
// per seal over the socket, the receipt in memory.
func (e *Engine) provePiResident(sk *[32]byte, m []byte, sealHash common.Hash) ([]byte, error) {
	if len(m) == 0 || len(m)%32 != 0 {
		// The client's own refusal: nothing is sent, the session stays.
		return nil, fmt.Errorf("%w: %d message bytes are not K x 32", errPiServeRefused, len(m))
	}
	srv := &e.piSrv
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if err := e.piServeEnsure(srv, sk); err != nil {
		return nil, err
	}
	proof, err := e.piServeRequest(srv, m)
	if err != nil {
		if errors.Is(err, errPiServeRefused) {
			// The server refused this request and its loop is up (the
			// hash lead's wire): the session stays, this seal is lost.
			e.log.Warn("Resident prover refused the seal; the session stays", "err", err, "sealhash", sealHash)
			return nil, err
		}
		// The session is gone: drop the process and let the next seal
		// start it again; this seal is lost.
		e.log.Warn("Resident prover: the seal's request failed; the prover restarts on the next seal", "err", err, "sealhash", sealHash)
		e.piServeStop(srv, false)
		return nil, err
	}
	return proof, nil
}

var errPiServeRefused = errors.New("igneum pi: the resident prover refused the seal")

// StartPiResident starts the resident prover at node start (F-458, 17
// October 2026): before it the process started on the sealer's FIRST keyed
// seal (piServeEnsure from provePiResident), so that seal paid the start
// and the warm-up proof inside its ready line (1.7 s on a CPU, under a
// second on a card) on the seal's clock, and a sealer restarted in a roll
// window lost its first draw to it. Now the eth backend's Start calls this
// once the flags are applied: with the resident form on and a bond secret
// set (the sealer's), the process is started and connected on its own
// goroutine under the session's lock, so a seal arriving meanwhile waits
// for the ready line instead of starting a second process, and the first
// seal after finds the prover up. A node without a secret (a reader) or
// without the resident form starts nothing. A start that fails here is
// logged and nothing else: the first seal's provePiResident starts it
// again by the same path, with the F-89 fallback behind it as before.
func (e *Engine) StartPiResident() {
	if !e.config.PiResident {
		return
	}
	sk, _ := e.bondSecret()
	if sk == nil {
		e.log.Info("Resident prover not started at node start: no bond secret on this node")
		return
	}
	go func() {
		srv := &e.piSrv
		srv.mu.Lock()
		defer srv.mu.Unlock()
		t0 := time.Now()
		if err := e.piServeEnsure(srv, sk); err != nil {
			e.log.Warn("Resident prover did not start at node start; the first seal starts it again", "err", err)
			return
		}
		e.log.Info("Resident prover up at node start (F-458): the first keyed seal pays no warm-up", "elapsed", time.Since(t0), "start", srv.starts)
	}()
}

// piServeEnsure starts the process when none is up (the secret's file as
// provePi writes it, the socket beside it) and connects.
func (e *Engine) piServeEnsure(srv *piServer, sk *[32]byte) error {
	if srv.cmd != nil && srv.conn != nil {
		select {
		case <-srv.done:
			e.piServeStop(srv, false)
		default:
			return nil
		}
	}
	dir := e.config.PiWorkDir
	if dir == "" {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("igneum pi: work directory: %w", err)
	}
	_, pk := e.bondSecret()
	name := "bond-sk"
	if pk != nil {
		name = "bond-sk-" + pk.Hex()[2:18]
	}
	skFile := filepath.Join(dir, name)
	if cur, err := os.ReadFile(skFile); err != nil || string(cur) != string(sk[:]) {
		if err := os.WriteFile(skFile, sk[:], 0o600); err != nil {
			return fmt.Errorf("igneum pi: the secret's file: %w", err)
		}
	}
	sock := filepath.Join(dir, fmt.Sprintf("pi-serve-%d.sock", os.Getpid()))
	os.Remove(sock)
	cmd := exec.Command(e.config.PiProver, "serve", skFile, sock)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("igneum pi: the resident prover did not start: %w", err)
	}
	srv.cmd, srv.sock, srv.skFile, srv.starts = cmd, sock, skFile, srv.starts+1
	srv.done = make(chan struct{})
	ready := make(chan string, 1)
	go func(done chan struct{}, c *exec.Cmd, r io.Reader) {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			line := sc.Text()
			if len(line) >= 6 && line[:6] == "serve:" {
				select {
				case ready <- line:
				default:
				}
			}
		}
		c.Wait()
		close(done)
	}(srv.done, cmd, stderr)
	t0 := time.Now()
	select {
	case line := <-ready:
		e.log.Info("Resident prover ready", "line", line, "start", srv.starts, "elapsed", time.Since(t0))
	case <-srv.done:
		e.piServeStop(srv, false)
		return errors.New("igneum pi: the resident prover exited before its ready line")
	case <-time.After(piServeStartTimeout):
		e.piServeStop(srv, true)
		return errors.New("igneum pi: the resident prover gave no ready line in time")
	}
	conn, err := net.DialTimeout("unix", sock, 5*time.Second)
	if err != nil {
		e.piServeStop(srv, true)
		return fmt.Errorf("igneum pi: the resident prover's socket: %w", err)
	}
	srv.conn, srv.rd = conn, bufio.NewReader(conn)
	return nil
}

// piServeRequest is one seal's round trip (the caller holds srv.mu).
func (e *Engine) piServeRequest(srv *piServer, m []byte) ([]byte, error) {
	if srv.conn == nil {
		return nil, errPiServeDown
	}
	if len(m)%32 != 0 || len(m) == 0 {
		return nil, fmt.Errorf("igneum pi: %d message bytes are not K x 32", len(m))
	}
	ms := make([]string, len(m)/32)
	for i := range ms {
		ms[i] = hex.EncodeToString(m[32*i : 32*i+32])
	}
	req, _ := json.Marshal(map[string]interface{}{"domain": 0, "m": ms})
	srv.conn.SetDeadline(time.Now().Add(piServeSealTimeout))
	if _, err := srv.conn.Write(append(req, '\n')); err != nil {
		return nil, err
	}
	line, err := srv.rd.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	var rep piServeReply
	if err := json.Unmarshal(line, &rep); err != nil {
		return nil, fmt.Errorf("igneum pi: the resident prover's line: %w", err)
	}
	if rep.Refused != "" {
		return nil, fmt.Errorf("%w: %s", errPiServeRefused, rep.Refused)
	}
	if rep.Bytes <= 0 || rep.Bytes > pi.MaxBytes(pi.DomainSeal) {
		return nil, fmt.Errorf("igneum pi: the resident prover's receipt size %d", rep.Bytes)
	}
	proof := make([]byte, rep.Bytes)
	if _, err := io.ReadFull(srv.rd, proof); err != nil {
		return nil, err
	}
	if proof[0] != pi.System {
		return nil, fmt.Errorf("igneum pi: the resident prover's receipt is not a domain-0 proof")
	}
	e.log.Info("Proof pi made (resident)", "bytes", len(proof), "proveMs", rep.ProveMs)
	return proof, nil
}

// piServeStop ends the session and the process: a quit over the socket when
// the session is up (the server wipes the secret and removes the socket),
// else the process killed by its own pid (never a name).
func (e *Engine) piServeStop(srv *piServer, kill bool) {
	if srv.conn != nil {
		if !kill {
			srv.conn.SetDeadline(time.Now().Add(3 * time.Second))
			srv.conn.Write([]byte("{\"quit\": true}\n"))
			srv.rd.ReadBytes('\n')
		}
		srv.conn.Close()
		srv.conn, srv.rd = nil, nil
	}
	if srv.cmd != nil && srv.cmd.Process != nil {
		select {
		case <-srv.done:
		case <-time.After(3 * time.Second):
			srv.cmd.Process.Kill()
			<-srv.done
		}
	}
	if srv.sock != "" {
		os.Remove(srv.sock)
	}
	srv.cmd, srv.sock = nil, ""
}

// ClosePiServer ends the resident prover at the engine's close.
func (e *Engine) ClosePiServer() {
	srv := &e.piSrv
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.cmd != nil {
		e.piServeStop(srv, false)
		log.Info("Resident prover stopped")
	}
}
