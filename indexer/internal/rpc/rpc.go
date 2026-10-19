// Package rpc is the light indexer's JSON-RPC client: the few calls the
// dashboard's chain-derived measures need (docs/ecosystem/DESIGN.md,
// sections 3 and 7). Standard library only; bounded request bodies.
package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"
)

var ErrRPC = errors.New("rpc: error")

// Client talks to one node over HTTP.
type Client struct {
	URL  string
	HTTP *http.Client
	id   uint64
}

// New creates a client with a bounded timeout.
func New(url string) *Client {
	return &Client{URL: url, HTTP: &http.Client{Timeout: 15 * time.Second}}
}

// Call performs one JSON-RPC call.
func (c *Client) Call(ctx context.Context, method string, params interface{}, out interface{}) error {
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
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("%w: %v", ErrRPC, err)
	}
	if env.Error != nil {
		return fmt.Errorf("%w: %d %s", ErrRPC, env.Error.Code, env.Error.Message)
	}
	if out == nil || string(env.Result) == "null" {
		if out != nil {
			return fmt.Errorf("%w: null result", ErrRPC)
		}
		return nil
	}
	return json.Unmarshal(env.Result, out)
}

// Quantity is a hex quantity as the node encodes it.
type Quantity struct{ Int *big.Int }

// UnmarshalJSON accepts 0x-hex strings and bare numbers.
func (q *Quantity) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		q.Int = new(big.Int)
		return nil
	}
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		v, ok := new(big.Int).SetString(s[2:], 16)
		if !ok {
			return fmt.Errorf("rpc: bad quantity %q", s)
		}
		q.Int = v
		return nil
	}
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return fmt.Errorf("rpc: bad quantity %q", s)
	}
	q.Int = v
	return nil
}

// Uint64 is the quantity as uint64 (saturating).
func (q Quantity) Uint64() uint64 {
	if q.Int == nil || !q.Int.IsUint64() {
		if q.Int != nil && q.Int.Sign() > 0 {
			return ^uint64(0)
		}
		return 0
	}
	return q.Int.Uint64()
}

// Block is the header subset the indexer keeps plus the transaction list.
type Block struct {
	Number       Quantity `json:"number"`
	Hash         string   `json:"hash"`
	ParentHash   string   `json:"parentHash"`
	Miner        string   `json:"miner"`
	Timestamp    Quantity `json:"timestamp"`
	GasUsed      Quantity `json:"gasUsed"`
	GasLimit     Quantity `json:"gasLimit"`
	BaseFee      Quantity `json:"baseFeePerGas"`
	Difficulty   Quantity `json:"difficulty"`
	Transactions []Tx     `json:"transactions"`
}

// Tx is the transaction subset the adoption measures need.
type Tx struct {
	Hash string `json:"hash"`
	From string `json:"from"`
	To   string `json:"to"`
}

// Receipt is the status subset plus the logs (address and topics only;
// data is not kept: the dashboard counts events, it does not decode
// amounts).
type Receipt struct {
	TransactionHash string   `json:"transactionHash"`
	Status          Quantity `json:"status"`
	Logs            []Log    `json:"logs"`
}

// Log is one emitted event: the emitting contract and its topics.
type Log struct {
	Address string   `json:"address"`
	Topics  []string `json:"topics"`
}

// Supply is the node's igneum_supply record (protocol engineer B).
type Supply struct {
	Height     Quantity `json:"height"`
	Hash       string   `json:"hash"`
	Issued     Quantity `json:"issued"`
	FundIssued Quantity `json:"fundIssued"`
	Burned     Quantity `json:"burned"`
	Net        Quantity `json:"net"`
}

// Head returns the chain height.
func (c *Client) Head(ctx context.Context) (uint64, error) {
	var q Quantity
	if err := c.Call(ctx, "eth_blockNumber", []interface{}{}, &q); err != nil {
		return 0, err
	}
	return q.Uint64(), nil
}

// BlockByNumber fetches a block with full transactions.
func (c *Client) BlockByNumber(ctx context.Context, n uint64) (*Block, error) {
	var b Block
	if err := c.Call(ctx, "eth_getBlockByNumber", []interface{}{fmt.Sprintf("0x%x", n), true}, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

// Receipts fetches a block's receipts: eth_getBlockReceipts when the
// node has it, one eth_getTransactionReceipt per transaction otherwise.
func (c *Client) Receipts(ctx context.Context, b *Block) ([]Receipt, error) {
	if len(b.Transactions) == 0 {
		return nil, nil
	}
	var rs []Receipt
	if err := c.Call(ctx, "eth_getBlockReceipts", []interface{}{fmt.Sprintf("0x%x", b.Number.Uint64())}, &rs); err == nil && len(rs) == len(b.Transactions) {
		return rs, nil
	}
	rs = rs[:0]
	for _, tx := range b.Transactions {
		var r Receipt
		if err := c.Call(ctx, "eth_getTransactionReceipt", []interface{}{tx.Hash}, &r); err != nil {
			return nil, err
		}
		rs = append(rs, r)
	}
	return rs, nil
}

// SupplyAt reads igneum_supply at a block tag or number.
func (c *Client) SupplyAt(ctx context.Context, tag string) (*Supply, error) {
	var s Supply
	if err := c.Call(ctx, "igneum_supply", []interface{}{tag}, &s); err != nil {
		return nil, err
	}
	return &s, nil
}
