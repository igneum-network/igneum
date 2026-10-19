// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).

// money-retained-proxy is the pruned-sync lab's lying peer: a JSON-RPC
// relay to a node that passes every call through unchanged except
// igneum_poolRetained for one block, whose reply it corrupts in one named
// way (a ciphertext byte flipped, a commitment byte flipped, a record
// dropped) before relaying it. A syncing node backfilling through it must
// refuse that block (pool.Check against the state its headers commit to)
// and store nothing of it; the lab's fourth test.
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/ethereum/go-ethereum/common/hexutil"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8739", "the address to serve")
	target := flag.String("target", "http://127.0.0.1:8731", "the honest node's HTTP endpoint")
	block := flag.Uint64("block", 0, "the block whose igneum_poolRetained reply is corrupted")
	way := flag.String("way", "ciphertext", "how: ciphertext (a byte of the first record's ciphertext flipped), commitment (a byte of the first record's commitment flipped), drop (the last record dropped), none")
	flag.Parse()
	var served, corrupted int
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		resp, err := http.Post(*target, "application/json", bytes.NewReader(body))
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		defer resp.Body.Close()
		out, err := io.ReadAll(resp.Body)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		served++
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if json.Unmarshal(body, &req) == nil && req.Method == "igneum_poolRetained" && len(req.Params) == 1 && *way != "none" {
			var num string
			if json.Unmarshal(req.Params[0], &num) == nil {
				if n, err := hexutil.DecodeUint64(num); err == nil && n == *block {
					if c, ok := corrupt(out, *way); ok {
						out = c
						corrupted++
						log.Printf("corrupted igneum_poolRetained for block %d (%s)", n, *way)
					}
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(out)
	})
	log.Printf("money-retained-proxy on %s to %s, corrupting block %d by %s", *listen, *target, *block, *way)
	log.Fatal(http.ListenAndServe(*listen, nil))
}

// corrupt rewrites the reply's result: the retained block as JSON (the
// node's RetainedBlock: Records with Record hex strings).
func corrupt(reply []byte, way string) ([]byte, bool) {
	var env struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  map[string]json.RawMessage
	}
	if err := json.Unmarshal(reply, &env); err != nil || env.Result == nil {
		return nil, false
	}
	var records []map[string]json.RawMessage
	if err := json.Unmarshal(env.Result["Records"], &records); err != nil || len(records) == 0 {
		return nil, false
	}
	switch way {
	case "drop":
		records = records[:len(records)-1]
	case "ciphertext", "commitment":
		var rec string
		if err := json.Unmarshal(records[0]["Record"], &rec); err != nil {
			return nil, false
		}
		b, err := base64.StdEncoding.DecodeString(rec) // encoding/json's form of []byte
		if err != nil || len(b) < 40 {
			return nil, false
		}
		if way == "commitment" {
			b[1+5] ^= 1 // a byte of the commitment (shield) or the anchor (spend: then the first nullifier is at 34)
			if b[0] == 0x02 {
				b[34+5] ^= 1
				b[1+5] ^= 1
			}
		} else {
			// the last byte of the record's bytes before the values (a
			// shield: the ciphertext's last byte; a spend: the second
			// output's ciphertext's last byte, 84 bytes before the end)
			if b[0] == 0x01 {
				b[len(b)-1] ^= 1
			} else {
				b[len(b)-85] ^= 1
			}
		}
		enc, _ := json.Marshal(base64.StdEncoding.EncodeToString(b))
		records[0]["Record"] = enc
	default:
		return nil, false
	}
	rs, _ := json.Marshal(records)
	env.Result["Records"] = rs
	res, _ := json.Marshal(env.Result)
	out := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":%s}`, strings.TrimSpace(string(env.ID)), res)
	return []byte(out), true
}
