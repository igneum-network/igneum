package stratum

import (
	"bufio"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"
)

// TestDrainSendsReconnect pins the drain (the handover form, the project's order of 05:5x UK, 14 October 2026): every
// open session is sent client.reconnect [host, port, 0] naming the public endpoint, and Drain returns once the
// sessions have gone.
func TestDrainSendsReconnect(t *testing.T) {
	srv := NewServer(nil, nil)
	server, client := net.Pipe()
	go srv.ServeConn(t.Context(), server)
	r := bufio.NewReader(client)
	done := make(chan string, 1)
	go func() {
		line, _ := r.ReadString('\n')
		done <- line
	}()
	time.Sleep(50 * time.Millisecond)
	go func() {
		srv.Drain("pool.igneum.network:3341", 2*time.Second)
	}()
	select {
	case line := <-done:
		var n struct {
			Method string        `json:"method"`
			Params []interface{} `json:"params"`
		}
		if err := json.Unmarshal([]byte(line), &n); err != nil {
			t.Fatal(err)
		}
		if n.Method != MethodReconnect || len(n.Params) != 3 || n.Params[0] != "pool.igneum.network" || n.Params[1] != float64(3341) {
			t.Fatalf("the reconnect notice: %s", strings.TrimSpace(line))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no client.reconnect within the drain")
	}
	client.Close()
}
