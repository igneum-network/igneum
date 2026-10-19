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
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// piServeStub is a stand-in for pi-prove serve speaking the hash team's wire
// (tools/hash/pi-serve-client.py's counterpart): the ready line on stderr,
// per request a JSON line and N bytes, a refusal for a bad request, the quit,
// and an exit on the request {"die": true} (the crash the client restarts
// after). The "receipt" is u8(2) followed by the messages' bytes, so each
// seal's bytes are checkable.
const piServeStub = `#!/usr/bin/env python3
import json, os, socket, sys
sk, path = sys.argv[2], sys.argv[3]
assert len(open(sk, "rb").read()) == 32
try: os.remove(path)
except FileNotFoundError: pass
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM); s.bind(path); os.chmod(path, 0o600); s.listen(1)
sys.stderr.write("serve: " + path + " (mode 0600), image id 006bf405\n"); sys.stderr.flush()
c, _ = s.accept(); f = c.makefile("rb")
while True:
    line = f.readline()
    if not line: break
    try: req = json.loads(line)
    except Exception: c.sendall(b'{"refused": "json"}\n'); continue
    if req.get("quit"): c.sendall(b'{"quit": true}\n'); break
    if req.get("die"): os._exit(3)
    if req.get("domain") != 0: c.sendall(b'{"refused": "domain"}\n'); continue
    m = req.get("m") or []
    if not m: c.sendall(b'{"refused": "k"}\n'); continue
    body = b"\x02" + b"".join(bytes.fromhex(x) for x in m)
    c.sendall((json.dumps({"pk": "00"*32, "shares": ["11"*32]*len(m), "bytes": len(body), "prove_ms": 1.5}) + "\n").encode()); c.sendall(body)
c.close(); os.remove(path)
`

func TestResidentProverClient(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on this box")
	}
	dir := t.TempDir()
	stub := filepath.Join(dir, "pi-prove-stub")
	if err := os.WriteFile(stub, []byte(piServeStub), 0o700); err != nil {
		t.Fatal(err)
	}
	e := newTestEngine(t)
	e.config.PiProver, e.config.PiWorkDir, e.config.PiResident = stub, dir, true
	var sk [32]byte
	sk[0], sk[31] = 7, 9
	pk := common.Hash{0xaa}
	e.lock.Lock()
	e.bondSK, e.keyedBond = &sk, &pk
	e.lock.Unlock()
	m1 := bytes.Repeat([]byte{0x11}, 64)
	m2 := bytes.Repeat([]byte{0x22}, 32)
	// 1. Two seals through one process: the receipt is the stub's u8(2)
	// plus the messages, in memory.
	p1, err := e.provePiResident(&sk, m1, common.Hash{1})
	if err != nil {
		t.Fatalf("seal 1: %v", err)
	}
	if !bytes.Equal(p1, append([]byte{2}, m1...)) {
		t.Fatalf("seal 1's receipt differs: %x", p1)
	}
	p2, err := e.provePiResident(&sk, m2, common.Hash{2})
	if err != nil {
		t.Fatalf("seal 2: %v", err)
	}
	if !bytes.Equal(p2, append([]byte{2}, m2...)) {
		t.Fatalf("seal 2's receipt differs: %x", p2)
	}
	if e.piSrv.starts != 1 {
		t.Fatalf("the process started %d times for two seals", e.piSrv.starts)
	}
	// 2. A refusal by the client (K zero, never sent) and one by the server
	// (a bad domain, sent through the session): the seal errors naming it,
	// the session stays up and the next seal proves through it.
	if _, err := e.provePiResident(&sk, nil, common.Hash{3}); err == nil || !strings.Contains(err.Error(), "32") {
		t.Fatalf("an empty message set was not refused by the client: %v", err)
	}
	line := func() []byte {
		e.piSrv.mu.Lock()
		defer e.piSrv.mu.Unlock()
		if e.piSrv.conn == nil {
			t.Fatal("the client's own refusal dropped the session")
		}
		e.piSrv.conn.Write([]byte("{\"domain\": 2, \"m\": [\"00\"]}\n"))
		l, _ := e.piSrv.rd.ReadBytes('\n')
		return l
	}()
	if !strings.Contains(string(line), "refused") {
		t.Fatalf("the stub did not refuse the bad domain: %s", line)
	}
	if p, err := e.provePiResident(&sk, m2, common.Hash{6}); err != nil || !bytes.Equal(p, p2) || e.piSrv.starts != 1 {
		t.Fatalf("the seal after a server refusal: err %v, receipt equal %v, starts %d", err, err == nil && bytes.Equal(p, p2), e.piSrv.starts)
	}
	// 3. The process dies under a seal: the error, then the restart.
	func() {
		e.piSrv.mu.Lock()
		defer e.piSrv.mu.Unlock()
		if e.piSrv.conn == nil {
			t.Fatal("no session before the die")
		}
		e.piSrv.conn.Write([]byte("{\"die\": true}\n"))
	}()
	if _, err := e.provePiResident(&sk, m1, common.Hash{4}); err == nil {
		t.Fatal("a seal over a dead process did not error")
	}
	p3, err := e.provePiResident(&sk, m1, common.Hash{5})
	if err != nil {
		t.Fatalf("the seal after the restart: %v", err)
	}
	if !bytes.Equal(p3, p1) || e.piSrv.starts != 2 {
		t.Fatalf("after the restart: receipt equal %v, starts %d", bytes.Equal(p3, p1), e.piSrv.starts)
	}
	// 4. The own-receipt record: a proof this node made is trusted at its
	// own import (the hash equal to the header's commitment), another is not
	// recorded.
	e.own.add(types.PiHash(p3))
	if !e.own.has(types.PiHash(p3)) || e.own.has(types.PiHash(p2)) {
		t.Fatal("the own-receipt record does not read as written")
	}
	// 5. The quit at close removes the socket and ends the process.
	sock := e.piSrv.sock
	e.ClosePiServer()
	if _, err := os.Stat(sock); err == nil {
		t.Fatal("the socket stands after the quit")
	}
	if e.piSrv.cmd != nil {
		t.Fatal("the process handle stands after the quit")
	}
}

// TestResidentProverStartsAtNodeStart (F-458): with the resident form on
// and a bond secret set, StartPiResident brings the process up and connects
// before any seal, so the first seal proves through the session already up
// (one start, not two); without a secret nothing starts; a second call
// while the session is up starts nothing more.
func TestResidentProverStartsAtNodeStart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on this box")
	}
	dir := t.TempDir()
	stub := filepath.Join(dir, "pi-prove-stub")
	if err := os.WriteFile(stub, []byte(piServeStub), 0o700); err != nil {
		t.Fatal(err)
	}
	up := func(e *Engine) bool {
		e.piSrv.mu.Lock()
		defer e.piSrv.mu.Unlock()
		return e.piSrv.conn != nil
	}
	wait := func(e *Engine) {
		t.Helper()
		for i := 0; i < 200; i++ {
			if up(e) {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatal("the resident prover is not up ten seconds after the node's start")
	}

	// 1. No secret (a reader): nothing starts.
	reader := newTestEngine(t)
	reader.config.PiProver, reader.config.PiWorkDir, reader.config.PiResident = stub, dir, true
	reader.StartPiResident()
	time.Sleep(200 * time.Millisecond)
	if up(reader) || reader.piSrv.starts != 0 {
		t.Fatalf("a node without a bond secret started the prover (starts %d)", reader.piSrv.starts)
	}

	// 2. The resident form off: nothing starts even with a secret.
	var sk [32]byte
	sk[0], sk[31] = 7, 9
	pk := common.Hash{0xaa}
	off := newTestEngine(t)
	off.config.PiProver, off.config.PiWorkDir, off.config.PiResident = stub, dir, false
	off.lock.Lock()
	off.bondSK, off.keyedBond = &sk, &pk
	off.lock.Unlock()
	off.StartPiResident()
	time.Sleep(200 * time.Millisecond)
	if up(off) || off.piSrv.starts != 0 {
		t.Fatalf("a node without the resident form started the prover (starts %d)", off.piSrv.starts)
	}

	// 3. The sealer: up before any seal; the first seal through the one
	// session; a second start call starts nothing more.
	e := newTestEngine(t)
	e.config.PiProver, e.config.PiWorkDir, e.config.PiResident = stub, dir, true
	e.lock.Lock()
	e.bondSK, e.keyedBond = &sk, &pk
	e.lock.Unlock()
	e.StartPiResident()
	wait(e)
	if e.piSrv.starts != 1 {
		t.Fatalf("the node's start ran %d prover starts, want one", e.piSrv.starts)
	}
	m := bytes.Repeat([]byte{0x33}, 32)
	p, err := e.provePiResident(&sk, m, common.Hash{1})
	if err != nil {
		t.Fatalf("the first seal: %v", err)
	}
	if !bytes.Equal(p, append([]byte{2}, m...)) {
		t.Fatalf("the first seal's receipt differs: %x", p)
	}
	if e.piSrv.starts != 1 {
		t.Fatalf("the first seal started the prover again (starts %d): it was up at the node's start", e.piSrv.starts)
	}
	e.StartPiResident()
	time.Sleep(200 * time.Millisecond)
	if e.piSrv.starts != 1 {
		t.Fatalf("a second start call started the prover again (starts %d)", e.piSrv.starts)
	}
	e.ClosePiServer()
}
