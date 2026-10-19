package jobsource

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 0.1.75: a client with a credential sends it as the Authorization: Bearer header on every request; one without sends
// no such header; the URL carries nothing either way.
func TestClientSendsItsCredentialAsABearerHeader(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x1c9a"}`))
	}))
	defer srv.Close()
	var out string
	if err := New(srv.URL).WithToken("the-token-bytes").Call(context.Background(), "eth_chainId", []interface{}{}, &out); err != nil {
		t.Fatal(err)
	}
	if err := New(srv.URL).Call(context.Background(), "eth_chainId", []interface{}{}, &out); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "Bearer the-token-bytes" || got[1] != "" {
		t.Fatalf("the headers seen: %q", got)
	}
}

// A redirect is never followed: the 302 is the answer (a decode error), no second request is made, and the credential
// never travels to the redirect's target.
func TestClientNeverFollowsARedirect(t *testing.T) {
	var target []string
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target = append(target, r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x1"}`))
	}))
	defer sink.Close()
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.Redirect(w, r, sink.URL, http.StatusFound)
	}))
	defer srv.Close()
	var out string
	err := New(srv.URL).WithToken("the-token-bytes").Call(context.Background(), "eth_chainId", []interface{}{}, &out)
	if err == nil {
		t.Fatal("a redirect answered as a result")
	}
	if hits != 1 || len(target) != 0 {
		t.Fatalf("the redirect was followed: %d hits, the target saw %q", hits, target)
	}
}
