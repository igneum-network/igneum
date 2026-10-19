package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"igneum.network/pool/internal/jobsource"
)

// 0.1.75: a node's credential file is read at start (trimmed); a file that cannot be read refuses that node, the error
// naming the path and never the bytes; a node with a credential carries it on its client.
func TestNodeTokensReadAtStartAndRefusedByPath(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "sealer-5.token")
	if err := os.WriteFile(good, []byte("secret-bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "sealer-6.token")
	tokens, refusals := readNodeTokens(map[string]string{"http://a:1/": good, "http://b:2/": missing})
	if tokens["http://a:1/"] != "secret-bytes" {
		t.Fatalf("the token read: %q", tokens["http://a:1/"])
	}
	err := refusals["http://b:2/"]
	if err == nil || !strings.Contains(err.Error(), missing) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("the refusal: %v", err)
	}
	s := &Service{nodeTokens: tokens, nodeRefusals: refusals}
	if c, ok := s.nodeFor("http://a:1/").(*jobsource.Client); !ok || c.Token != "secret-bytes" || c.URL != "http://a:1/" {
		t.Fatalf("the node with a credential: %#v", s.nodeFor("http://a:1/"))
	}
	n := s.nodeFor("http://b:2/")
	if e := n.Call(context.Background(), "eth_blockNumber", nil, nil); e == nil || !strings.Contains(e.Error(), missing) {
		t.Fatalf("the refused node's call: %v", e)
	}
	if _, e := n.GetWork(context.Background()); e == nil {
		t.Fatal("the refused node's GetWork answered")
	}
	if c, ok := s.nodeFor("http://c:3/").(*jobsource.Client); !ok || c.Token != "" {
		t.Fatalf("a node without a credential dials as before: %#v", s.nodeFor("http://c:3/"))
	}
}
