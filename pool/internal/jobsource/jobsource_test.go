package jobsource

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
)

const (
	hdr = "0x" + "ab" + "00000000000000000000000000000000000000000000000000000000000000"
	tgt = "0x" + "0f" + "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
)

func rpcServer(t *testing.T, handler func(method string, params []json.RawMessage) (interface{}, *rpcError)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage   `json:"id"`
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
			return
		}
		res, e := handler(req.Method, req.Params)
		out := map[string]interface{}{"jsonrpc": "2.0", "id": req.ID}
		if e != nil {
			out["error"] = e
		} else {
			out["result"] = res
		}
		json.NewEncoder(w).Encode(out) //nolint:errcheck
	}))
}

func TestGetWorkIgneumObject(t *testing.T) {
	srv := rpcServer(t, func(method string, _ []json.RawMessage) (interface{}, *rpcError) {
		if method != "igneum_getWork" {
			return nil, &rpcError{Code: -32601, Message: "unexpected " + method}
		}
		return map[string]interface{}{"headerHash": hdr, "target": tgt, "number": "0x2a", "algorithm": "null", "seedHash": "0x00"}, nil
	})
	defer srv.Close()
	w, err := New(srv.URL).GetWork(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if w.Header[0] != 0xab || w.Boundary[0] != 0x0f || w.Height != 42 || w.Algorithm != "null" {
		t.Fatalf("work %+v", w)
	}
}

func TestGetWorkNodeShape(t *testing.T) {
	// The node's own shape (9 October 2026): height and epoch as numbers,
	// the target as a decimal integer wider than float64.
	body := `{"algorithm":"null","headerHash":"` + hdr + `","height":1297,"epoch":0,"seedHash":"0x00","target":5386650671717323657644774266643836555307189607503147732681878590550905}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID json.RawMessage `json:"id"`
		}
		json.NewDecoder(r.Body).Decode(&req)                                                   //nolint:errcheck
		w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(req.ID) + `,"result":` + body + `}`)) //nolint:errcheck
	}))
	defer srv.Close()
	wk, err := New(srv.URL).GetWork(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if wk.Height != 1297 || wk.Algorithm != "null" || wk.Header[0] != 0xab {
		t.Fatalf("work %+v", wk)
	}
	want, _ := new(big.Int).SetString("5386650671717323657644774266643836555307189607503147732681878590550905", 10)
	if new(big.Int).SetBytes(wk.Boundary[:]).Cmp(want) != 0 {
		t.Fatalf("boundary %x", wk.Boundary)
	}
}

func TestGetWorkFallsBackToEthash(t *testing.T) {
	srv := rpcServer(t, func(method string, _ []json.RawMessage) (interface{}, *rpcError) {
		switch method {
		case "igneum_getWork":
			return nil, &rpcError{Code: -32601, Message: "method not found"}
		case "eth_getWork":
			return []string{hdr, "0x00", tgt, "0x10"}, nil
		}
		return nil, &rpcError{Code: -32601, Message: "unexpected " + method}
	})
	defer srv.Close()
	w, err := New(srv.URL).GetWork(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if w.Header[0] != 0xab || w.Height != 16 || w.Algorithm != "" {
		t.Fatalf("work %+v", w)
	}
}

func TestGetWorkRefusesIncompletePackage(t *testing.T) {
	srv := rpcServer(t, func(method string, _ []json.RawMessage) (interface{}, *rpcError) {
		if method == "igneum_getWork" {
			return map[string]interface{}{"headerHash": hdr, "target": tgt, "number": "0x2a"}, nil // no algorithm
		}
		return nil, &rpcError{Code: -32601, Message: "unexpected " + method}
	})
	defer srv.Close()
	if _, err := New(srv.URL).GetWork(context.Background()); !errors.Is(err, ErrShape) {
		t.Fatalf("got %v", err)
	}
}

func TestSubmitWork(t *testing.T) {
	var got []json.RawMessage
	accept := true
	srv := rpcServer(t, func(method string, params []json.RawMessage) (interface{}, *rpcError) {
		if method != "eth_submitWork" {
			return nil, &rpcError{Code: -32601, Message: "unexpected " + method}
		}
		got = params
		return accept, nil
	})
	defer srv.Close()
	c := New(srv.URL)
	var header, mix [32]byte
	header[0], mix[31] = 0xab, 0x01
	if err := c.SubmitWork(context.Background(), 0x1234, header, mix); err != nil {
		t.Fatal(err)
	}
	var nonce, h, m string
	json.Unmarshal(got[0], &nonce) //nolint:errcheck
	json.Unmarshal(got[1], &h)     //nolint:errcheck
	json.Unmarshal(got[2], &m)     //nolint:errcheck
	if nonce != "0x0000000000001234" || h != hdr || len(m) != 66 || m[65] != '1' {
		t.Fatalf("params %s %s %s", nonce, h, m)
	}
	accept = false
	if err := c.SubmitWork(context.Background(), 1, header, mix); !errors.Is(err, ErrRejected) {
		t.Fatalf("rejected: %v", err)
	}
}

func TestHexUint(t *testing.T) {
	for in, want := range map[string]uint64{"0x0": 0, "0x2a": 42, "ff": 255, "0xFFFFFFFFFFFFFFFF": ^uint64(0)} {
		got, err := hexUint(in)
		if err != nil || got != want {
			t.Fatalf("%s: %d %v", in, got, err)
		}
	}
	for _, in := range []string{"", "0x", "0xg", "0x1ffffffffffffffff"} {
		if _, err := hexUint(in); err == nil {
			t.Fatalf("%q accepted", in)
		}
	}
}
