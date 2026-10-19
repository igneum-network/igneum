// Package rewards reads the node's igneum_blockReward(from, to) records
// (product engineer A's Earnings page reads the same call from a solo
// node) and reduces them to the two figures a pool miner without a node
// needs: the subsidy the miner keeps at the newest block and the mean
// priority fees per block over the window. Both are wei as decimal
// strings in the disclosure document and on every notify.
package rewards

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
)

// Window is the number of recent blocks the priority-fee mean covers.
const Window = 100

var ErrShape = errors.New("rewards: unexpected igneum_blockReward shape")

// Caller is the JSON-RPC surface the reader needs.
type Caller interface {
	Call(ctx context.Context, method string, params interface{}, out interface{}) error
}

// Record is one block's reward split as the node reports it.
type Record struct {
	Height       uint64
	Miner        *big.Int // the subsidy the miner keeps
	Fund         *big.Int // the maintenance allocation
	PriorityFees *big.Int // the priority fees the producer received
}

// Summary is what the pool publishes.
type Summary struct {
	Height             uint64
	SubsidyWei         string
	PriorityFeeMeanWei string
	Blocks             int
}

// Fetch reads the records for [from, to]. Field names are read leniently
// (miner or minerReward, priorityFees or fees, height or number); values
// may be hex quantities or decimal strings.
func Fetch(ctx context.Context, c Caller, from, to uint64) ([]Record, error) {
	var raw []map[string]json.RawMessage
	if err := c.Call(ctx, "igneum_blockReward", []interface{}{fmt.Sprintf("0x%x", from), fmt.Sprintf("0x%x", to)}, &raw); err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(raw))
	for i, m := range raw {
		r := Record{Height: from + uint64(i)}
		if h, ok := quantity(m, "height", "number"); ok {
			r.Height = h.Uint64()
		}
		var ok bool
		if r.Miner, ok = quantity(m, "miner", "minerReward", "subsidy"); !ok {
			return nil, ErrShape
		}
		r.Fund, _ = quantity(m, "fund", "fundReward")
		if r.PriorityFees, ok = quantity(m, "priorityFees", "fees", "priorityFee"); !ok {
			r.PriorityFees = new(big.Int)
		}
		out = append(out, r)
	}
	return out, nil
}

// Summarise reduces records to the published figures: the newest
// block's miner subsidy and the mean priority fees over the records.
func Summarise(records []Record) (Summary, error) {
	if len(records) == 0 {
		return Summary{}, ErrShape
	}
	newest := records[0]
	sum := new(big.Int)
	for _, r := range records {
		if r.Height > newest.Height {
			newest = r
		}
		sum.Add(sum, r.PriorityFees)
	}
	mean := new(big.Int).Div(sum, big.NewInt(int64(len(records))))
	return Summary{
		Height:             newest.Height,
		SubsidyWei:         newest.Miner.String(),
		PriorityFeeMeanWei: mean.String(),
		Blocks:             len(records),
	}, nil
}

// Current fetches the last Window blocks up to head and summarises them.
func Current(ctx context.Context, c Caller, head uint64) (Summary, error) {
	from := uint64(0)
	if head+1 > Window {
		from = head + 1 - Window
	}
	records, err := Fetch(ctx, c, from, head)
	if err != nil {
		return Summary{}, err
	}
	return Summarise(records)
}

// quantity reads the first present key as a hex quantity, a decimal
// string or a bare JSON number.
func quantity(m map[string]json.RawMessage, keys ...string) (*big.Int, bool) {
	for _, k := range keys {
		raw, ok := m[k]
		if !ok {
			continue
		}
		var s string
		if json.Unmarshal(raw, &s) != nil {
			s = strings.TrimSpace(string(raw))
		}
		if s == "" || s == "null" {
			continue
		}
		if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
			if v, ok := new(big.Int).SetString(s[2:], 16); ok {
				return v, true
			}
			continue
		}
		if v, ok := new(big.Int).SetString(s, 10); ok {
			return v, true
		}
	}
	return nil, false
}
