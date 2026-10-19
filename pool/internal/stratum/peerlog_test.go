package stratum

import (
	"bytes"
	"context"
	"log"
	"net"
	"strings"
	"testing"
	"time"

	"igneum.network/pool/internal/validate"
)

// 0.1.78: a session's authorize is named on the log with its peer address and port; the journal and the records
// carry no address (nothing of theirs changes here).
func TestAuthorizeNamesThePeerOnTheLog(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)
	srv := NewServer(validate.Null{}, nil)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		srv.ServeConn(context.Background(), c)
	}()
	cl, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	local := cl.LocalAddr().String()
	_, _ = cl.Write([]byte(`{"id":1,"method":"mining.subscribe","params":["peer-test/0.1"]}` + "\n"))
	_, _ = cl.Write([]byte(`{"id":2,"method":"mining.authorize","params":["0xABCDEF0123456789abcdef0123456789ABCDEF01","own-worker-1"]}` + "\n"))
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), "authorised: 0xabcdef0123456789abcdef0123456789abcdef01 worker \"own-worker-1\" (plain) from "+local) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no authorised line naming the peer %s; the log: %s", local, buf.String())
}
