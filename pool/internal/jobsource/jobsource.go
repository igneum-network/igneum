// Package jobsource fetches work from the node's mining RPC (protocol
// engineer A's PA-15: eth_getWork returns the four ethash strings, header
// hash, seed hash, target and block number; igneum_getWork returns the
// work package as a named object including the algorithm;
// eth_submitWork(nonce, headerHash, mixDigest) returns a bool) and
// submits block solutions. The pool is a getwork client; the node offers
// no stratum.
package jobsource

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"
)

var (
	ErrRPC      = errors.New("jobsource: rpc error")
	ErrShape    = errors.New("jobsource: unexpected work shape")
	ErrRejected = errors.New("jobsource: solution rejected by the node")
)

// BoundedText is a node's free text as the pool keeps it for its log (0.1.83, the security review's read of 0.1.81): the
// whitespace one space, so a line break inside it cannot forge a log line, and at most 200 bytes.
func BoundedText(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 200 {
		s = s[:200] + ".."
	}
	return s
}

// Work is one work package as the pool reads it.
type Work struct {
	Header           [32]byte
	Boundary         [32]byte
	Height           uint64
	Epoch            uint64
	Algorithm        string
	AlgorithmVersion uint32
	// Params is the algorithm's document as igneum_getWork serves it
	// (verbatim JSON) and ParamsDigest its digest; empty on eth_getWork.
	Params       json.RawMessage
	ParamsDigest string
	// Payouts is the chain-native payout list the template names (the
	// devnet4 sealer's igneum_getWork "payouts": [{"address", "weight"}],
	// the list the sealed block pays pro rata by Finalize); empty where the
	// node carries none. A pool checks it against the list it set before
	// hashing the template.
	Payouts json.RawMessage
	// The share lane's fields (LAUNCH-GUARDS 2.9; the protocol team's node shape
	// of 11 October 2026): ShareTarget the one network-wide share target
	// (decimal or hex), SharesPerBlock the operating count, ShareHeight the
	// template's height, ShareParent its parent hash, ShareParams and
	// ShareParamsDigest the share's document (the unkeyed twin). Empty on a
	// node without the lane.
	ShareTarget       json.RawMessage
	SharesPerBlock    uint64
	ShareHeight       uint64
	ShareParent       [32]byte
	ShareParams       json.RawMessage
	ShareParamsDigest string
	// VectorWork (the project's order of 00:5x UK, 13 October 2026): a template is one coordinate's. Coord is the
	// coordinate (0..3), Difficulty the coordinate's difficulty as the node states it (a decimal string) and
	// RewardWei the coordinate's reward per block (decimal wei); HasCoord is true when the package carried a coord.
	Coord      uint8
	HasCoord   bool
	Difficulty string
	RewardWei  string
	// LastHeight is the height of the coordinate's last block on the chain (the product team's router restores an emptied
	// coordinate on it: no block for more than twelve blocks); 0 when the node does not serve it.
	LastHeight uint64
	// CoordName is the coordinate's name as the node states it (vectorwork1: tensor, bandwidth, fabric).
	CoordName string

	// ParentHash is the block template's parent as the node states it (parentHash, the public test network's node
	// from 0.10.51, F-420), HasParent true when carried; StaleTemplate is the node's own word that its template is
	// not on the head (staleTemplate, served with a zero headerHash). The pool issues no block job on either (0.1.81).
	ParentHash    [32]byte
	HasParent     bool
	StaleTemplate string
	// ProgramAnchor is the period's programme anchor as the node serves it (programAnchor on igneum_getWork), with
	// AnchorHeight the height it was taken at; zero when the node serves none (the pool then resolves it by the
	// chain form).
	ProgramAnchor [32]byte
	HasAnchor     bool
	AnchorHeight  uint64
	// ShareOnly is a keyed sealer's package under the share lane: the block
	// fields zero and the target null, the share fields the whole work (a
	// block job is never issued on it).
	ShareOnly bool
	// ShareStepMax is the largest step the node accepts on igneum_submitShare
	// (PA-53's step form; 0 on a count-form chain).
	ShareStepMax uint64
}

// HasShareLane reports whether the node served the share lane's target.
func (w Work) HasShareLane() bool { return len(w.ShareTarget) > 0 }

// Client talks JSON-RPC over HTTP to one node.
type Client struct {
	URL  string
	HTTP *http.Client
	id   uint64

	// Token, when set, goes on every request as "Authorization: Bearer <Token>": a per-node credential the
	// operator keeps in a file (0.1.75, 16 October 2026: the new sealer pods' edge admits a bearer alone, the
	// source address being the port map's for every client); never logged, never part of the URL, so the
	// journal's node URLs and the error strings carry nothing.
	Token string
}

// WithToken sets the per-node credential and returns the client.
func (c *Client) WithToken(token string) *Client {
	c.Token = token
	return c
}

// New creates a client with a bounded request timeout.
func New(url string) *Client {
	return &Client{URL: url, HTTP: &http.Client{Timeout: RPCTimeout, CheckRedirect: noRedirect}}
}

// noRedirect refuses to follow any redirect: a JSON-RPC client has no business following one, and Go would copy the
// Authorization header onto a redirect to the same host or a subdomain of it (the security review's read of 0.1.75, 19:09 UK
// 16 October 2026: a pod answering 302 to another port on its address would receive the credential). The redirect
// response is returned as the answer and fails the decode as any non-JSON answer does.
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// RPCTimeout is the client's call timeout (ten seconds; a node whose template build holds a call longer answers
// nothing inside it: vectorwork6's 8802 of 14:19Z, 13 October 2026, with its 4 GiB version 15 context, -32002
// after 30 s); -rpc-timeout raises it.
var RPCTimeout = 10 * time.Second

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Call performs one JSON-RPC call and decodes the result into out.
func (c *Client) Call(ctx context.Context, method string, params interface{}, out interface{}) error {
	return c.call(ctx, method, params, out)
}

func (c *Client) call(ctx context.Context, method string, params interface{}, out interface{}) error {
	c.id++
	body, err := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": c.id, "method": method, "params": params})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("%w: %v", ErrRPC, err)
	}
	if env.Error != nil {
		return fmt.Errorf("%w: %d %s", ErrRPC, env.Error.Code, env.Error.Message)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(env.Result, out)
}

// GetWork calls igneum_getWork and falls back to eth_getWork when the
// node does not carry the Igneum namespace.
func (c *Client) GetWork(ctx context.Context) (Work, error) {
	var obj map[string]json.RawMessage
	if err := c.call(ctx, "igneum_getWork", []interface{}{}, &obj); err == nil {
		return workFromObject(obj)
	}

	var arr []string
	if err := c.call(ctx, "eth_getWork", []interface{}{}, &arr); err != nil {
		return Work{}, err
	}
	return workFromEthash(arr)
}

// GetWorkCoord calls igneum_getWork for one coordinate (VectorWork: igneum_getWork [coord], the interface as
// the product team's router form and the protocol team's default name it, 13 October 2026) and refuses a package that does not
// carry that coordinate.
func (c *Client) GetWorkCoord(ctx context.Context, coord uint8) (Work, error) {
	var obj map[string]json.RawMessage
	if err := c.call(ctx, "igneum_getWork", []interface{}{coord}, &obj); err != nil {
		return Work{}, err
	}
	w, err := workFromObject(obj)
	if err != nil {
		return Work{}, err
	}
	if !w.HasCoord || w.Coord != coord {
		return Work{}, fmt.Errorf("igneum_getWork [%d]: the package carries coordinate %d (has %v)", coord, w.Coord, w.HasCoord)
	}
	return w, nil
}

// SubmitWork calls eth_submitWork with the nonce as 8 hex bytes.
func (c *Client) SubmitWork(ctx context.Context, nonce uint64, header, mix [32]byte) error {
	var ok bool
	n := fmt.Sprintf("0x%016x", nonce)
	if err := c.call(ctx, "eth_submitWork", []interface{}{n, "0x" + hex.EncodeToString(header[:]), "0x" + hex.EncodeToString(mix[:])}, &ok); err != nil {
		return err
	}
	if !ok {
		return ErrRejected
	}
	return nil
}

func workFromEthash(arr []string) (Work, error) {
	if len(arr) < 4 {
		return Work{}, ErrShape
	}
	var w Work
	var err error
	if w.Header, err = hex32(arr[0]); err != nil {
		return Work{}, err
	}
	if w.Boundary, err = hex32(arr[2]); err != nil {
		return Work{}, err
	}
	if w.Height, err = hexUint(arr[3]); err != nil {
		return Work{}, err
	}
	return w, nil
}

// workFromObject reads the named fields leniently: protocol engineer A
// has not fixed the names, so the common spellings are accepted and the
// pool refuses a package it cannot read in full.
func workFromObject(obj map[string]json.RawMessage) (Work, error) {
	var w Work
	// A field may be a hex string, a decimal string or a bare JSON number
	// (the node's igneum_getWork sends height and epoch as numbers and the
	// target as a decimal integer wider than float64; it is read as text).
	str := func(keys ...string) (string, bool) {
		for _, k := range keys {
			if raw, ok := obj[k]; ok {
				t := strings.TrimSpace(string(raw))
				if t == "" || t == "null" {
					continue // a null field is an absent one (a share-only package's target)
				}
				var s string
				if json.Unmarshal(raw, &s) == nil {
					return s, true
				}
				if isDecimal(t) {
					return "dec:" + t, true
				}
			}
		}
		return "", false
	}
	h, ok := str("headerHash", "header", "sealHash")
	if !ok {
		return Work{}, ErrShape
	}
	if raw, ok := obj["params"]; ok && len(raw) > 0 && string(raw) != "null" {
		w.Params = append(json.RawMessage(nil), raw...)
	}
	if d, ok := str("paramsDigest", "params_digest"); ok {
		w.ParamsDigest = d
	}
	if raw, ok := obj["shareTarget"]; ok && len(raw) > 0 && string(raw) != "null" {
		w.ShareTarget = append(json.RawMessage(nil), raw...)
		if raw, ok := obj["sharesPerBlock"]; ok {
			var s string
			if json.Unmarshal(raw, &s) == nil {
				w.SharesPerBlock, _ = anyUint(s)
			} else {
				var n uint64
				if json.Unmarshal(raw, &n) == nil {
					w.SharesPerBlock = n
				}
			}
		}
		if raw, ok := obj["shareStepMax"]; ok {
			// PA-53's step form: the largest step the node accepts on igneum_submitShare (0 on a count-form chain)
			var s string
			if json.Unmarshal(raw, &s) == nil {
				w.ShareStepMax, _ = anyUint(s)
			} else {
				var n uint64
				if json.Unmarshal(raw, &n) == nil {
					w.ShareStepMax = n
				}
			}
		}
		if raw, ok := obj["shareHeight"]; ok {
			var s string
			if json.Unmarshal(raw, &s) == nil {
				w.ShareHeight, _ = anyUint(s)
			} else {
				var n uint64
				if json.Unmarshal(raw, &n) == nil {
					w.ShareHeight = n
				}
			}
		}
		if raw, ok := obj["shareParent"]; ok {
			var s string
			if json.Unmarshal(raw, &s) == nil {
				w.ShareParent, _ = hex32(s)
			}
		}
		if raw, ok := obj["shareParams"]; ok && len(raw) > 0 && string(raw) != "null" {
			w.ShareParams = append(json.RawMessage(nil), raw...)
		}
		if raw, ok := obj["shareParamsDigest"]; ok {
			var s string
			if json.Unmarshal(raw, &s) == nil {
				w.ShareParamsDigest = s
			}
		}
	}
	if raw, ok := obj["payouts"]; ok && len(raw) > 0 && string(raw) != "null" {
		w.Payouts = append(json.RawMessage(nil), raw...)
	}
	// VectorWork's per-coordinate fields (absent on a one-form chain)
	if raw, ok := obj["coord"]; ok && len(raw) > 0 && string(raw) != "null" {
		var n uint64
		var sv string
		switch {
		case json.Unmarshal(raw, &n) == nil:
			w.Coord, w.HasCoord = uint8(n), true
		case json.Unmarshal(raw, &sv) == nil:
			if v, err := anyUint(sv); err == nil {
				w.Coord, w.HasCoord = uint8(v), true
			}
		}
	}
	if d, ok := str("difficulty", "coordDifficulty"); ok {
		w.Difficulty = strings.TrimPrefix(d, "dec:")
	}
	if r, ok := str("rewardWei", "reward_wei", "coordRewardWei"); ok {
		w.RewardWei = strings.TrimPrefix(r, "dec:")
	}
	if an, ok := str("programAnchor", "program_anchor", "shareAnchor"); ok {
		if h, err := hex32(an); err == nil {
			w.ProgramAnchor, w.HasAnchor = h, true
		}
	}
	if ah, ok := str("anchorHeight", "anchor_height"); ok {
		w.AnchorHeight, _ = anyUint(ah)
	}
	if cn, ok := str("coordName", "coord_name"); ok {
		w.CoordName = cn
	}
	if ph, ok := str("parentHash", "parent_hash"); ok {
		if h, err := hex32(ph); err == nil {
			w.ParentHash, w.HasParent = h, true
		}
	}
	if raw, ok := obj["staleTemplate"]; ok && len(raw) > 0 && string(raw) != "null" {
		var sv string
		var bv bool
		switch {
		case json.Unmarshal(raw, &sv) == nil:
			w.StaleTemplate = BoundedText(sv)
		case json.Unmarshal(raw, &bv) == nil && bv:
			w.StaleTemplate = "the node's template is not on the head"
		}
	}
	if lh, ok := str("lastHeight", "last_height", "coordLastHeight"); ok {
		// anyUint reads a bare number through its "dec:" mark; stripping the mark read 128 as hex 0x128 = 296 (the
		// settlement seat's read of 16:30 UK, 13 October 2026: last_height above the chain's head on every coordinate)
		w.LastHeight, _ = anyUint(lh)
	}
	var err error
	if w.Header, err = hex32(h); err != nil {
		return Work{}, err
	}
	b, ok := str("target", "boundary")
	if !ok {
		// a share-only package (a keyed sealer's getWork under the share lane, "shareOnly": true, the block
		// fields zero and the target null): the share fields are the work, and a block job is never issued on it
		if !w.HasShareLane() {
			return Work{}, ErrShape
		}
		w.ShareOnly = true
	} else if w.Boundary, err = target32(b); err != nil {
		return Work{}, err
	}
	n, ok := str("number", "height", "blockNumber")
	if !ok {
		return Work{}, ErrShape
	}
	if w.Height, err = anyUint(n); err != nil {
		return Work{}, err
	}
	if w.Algorithm, ok = str("algorithm", "algo"); !ok {
		return Work{}, ErrShape
	}
	if e, ok := str("epoch"); ok {
		if w.Epoch, err = anyUint(e); err != nil {
			return Work{}, err
		}
	}
	if v, ok := str("algorithmVersion", "algorithm_version", "version"); ok {
		ver, err := anyUint(v)
		if err != nil {
			return Work{}, err
		}
		w.AlgorithmVersion = uint32(ver)
	}
	return w, nil
}

// isDecimal reports whether t is a bare decimal integer.
func isDecimal(t string) bool {
	if t == "" {
		return false
	}
	for i := 0; i < len(t); i++ {
		if t[i] < '0' || t[i] > '9' {
			return false
		}
	}
	return true
}

// target32 reads a 256-bit target as 0x-hex or as a decimal integer.
func target32(s string) ([32]byte, error) {
	var out [32]byte
	if strings.HasPrefix(s, "dec:") {
		v, ok := new(big.Int).SetString(s[4:], 10)
		if !ok || v.Sign() < 0 || v.BitLen() > 256 {
			return out, ErrShape
		}
		b := v.Bytes()
		copy(out[32-len(b):], b)
		return out, nil
	}
	return hex32(s)
}

// anyUint reads a uint64 from 0x-hex or a decimal integer.
func anyUint(s string) (uint64, error) {
	if strings.HasPrefix(s, "dec:") {
		v, err := strconv.ParseUint(s[4:], 10, 64)
		if err != nil {
			return 0, ErrShape
		}
		return v, nil
	}
	return hexUint(s)
}

func hex32(s string) ([32]byte, error) {
	var out [32]byte
	s = strings.TrimPrefix(s, "0x")
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 32 {
		return out, ErrShape
	}
	copy(out[:], b)
	return out, nil
}

func hexUint(s string) (uint64, error) {
	s = strings.TrimPrefix(s, "0x")
	if s == "" || len(s) > 16 {
		return 0, ErrShape
	}
	var v uint64
	for _, c := range s {
		var d uint64
		switch {
		case c >= '0' && c <= '9':
			d = uint64(c - '0')
		case c >= 'a' && c <= 'f':
			d = uint64(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = uint64(c-'A') + 10
		default:
			return 0, ErrShape
		}
		v = v<<4 | d
	}
	return v, nil
}
