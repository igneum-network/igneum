// Package shares implements transparent share accounting
// (docs/ecosystem/DESIGN.md, section 2.7): a PPLNS window by weight and a
// replayable distribution record per block found. Integer arithmetic in
// wei; one fee applied once; division remainders go to a named carry
// account and are in the record, never absorbed.
package shares

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math/big"
	"sort"

	"igneum.network/pool/internal/limits"
)

// CarryAccount receives division remainders; it joins the next
// distribution as carry in.
const CarryAccount = "carry"

var (
	ErrFee    = errors.New("shares: fee basis points out of bounds")
	ErrReward = errors.New("shares: block reward missing or negative")
)

// Share is one accepted share. Weight is the share difficulty.
type Share struct {
	Seq     uint64
	Account string
	Worker  string
	// Key is the miner's signing key on the non-custodial form (hex; empty
	// on the plain form); it rides the record for attribution.
	Key string
	JobID   string
	Weight  uint64
	Height  uint64
	// Journal is the share's sequence in the pool's share journal (zero
	// when the pool keeps none): a block's distribution names the journal
	// lines it paid, so they are pruned as carried.
	Journal uint64
}

// Window keeps the minimal suffix of accepted shares whose weights sum to
// at least Target (the operator's N times the network difficulty).
type Window struct {
	Target uint64
	shares []Share
	total  uint64
	next   uint64
}

// NewWindow creates a window with the given weight target.
func NewWindow(target uint64) *Window { return &Window{Target: target} }

// Add appends a share, assigns its sequence number and evicts the oldest
// shares while the remainder still reaches the target. The stored share
// (with Seq set) is returned.
func (w *Window) Add(s Share) Share {
	w.next++
	s.Seq = w.next
	w.shares = append(w.shares, s)
	w.total += s.Weight
	for len(w.shares) > 1 && w.total-w.shares[0].Weight >= w.Target {
		w.total -= w.shares[0].Weight
		w.shares = w.shares[1:]
	}
	return s
}

// Total is the sum of weights in the window.
func (w *Window) Total() uint64 { return w.total }

// Len is the number of shares in the window.
func (w *Window) Len() int { return len(w.shares) }

// Snapshot copies the window's shares, oldest first.
func (w *Window) Snapshot() []Share {
	out := make([]Share, len(w.shares))
	copy(out, w.shares)
	return out
}

// Block is a block the pool found, with the value its coinbase received
// (the protocol's allocation already applied in consensus) plus the
// priority fees, in wei.
type Block struct {
	Hash   string
	Height uint64
	Reward *big.Int
}

// Distribution is the replayable record of one block's split.
type Distribution struct {
	BlockHash  string
	Height     uint64
	Reward     *big.Int
	FeeBps     uint32
	Fee        *big.Int
	CarryIn    *big.Int
	Shares     []Share
	Credits    map[string]*big.Int
	Remainder  *big.Int
	InputsHash [32]byte
}

// Distribute splits a block's reward across the window: the one fee in
// basis points comes off once, carry in is added, each account receives
// floor(distributable * weight / total), and the remainder is recorded.
// An empty window sends everything distributable to the remainder.
func Distribute(b Block, window []Share, feeBps uint32, carryIn *big.Int) (*Distribution, error) {
	if feeBps > limits.MaxFeeBps {
		return nil, ErrFee
	}
	if b.Reward == nil || b.Reward.Sign() < 0 {
		return nil, ErrReward
	}
	if carryIn == nil {
		carryIn = new(big.Int)
	}
	d := &Distribution{
		BlockHash: b.Hash,
		Height:    b.Height,
		Reward:    new(big.Int).Set(b.Reward),
		FeeBps:    feeBps,
		CarryIn:   new(big.Int).Set(carryIn),
		Shares:    append([]Share(nil), window...),
		Credits:   map[string]*big.Int{},
	}
	d.Fee = new(big.Int).Mul(d.Reward, big.NewInt(int64(feeBps)))
	d.Fee.Div(d.Fee, big.NewInt(limits.MaxFeeBps))
	distributable := new(big.Int).Sub(d.Reward, d.Fee)
	distributable.Add(distributable, d.CarryIn)

	weights := map[string]*big.Int{}
	total := new(big.Int)
	for _, s := range d.Shares {
		w := new(big.Int).SetUint64(s.Weight)
		total.Add(total, w)
		if acc, ok := weights[s.Account]; ok {
			acc.Add(acc, w)
		} else {
			weights[s.Account] = w
		}
	}
	paid := new(big.Int)
	if total.Sign() > 0 {
		accounts := make([]string, 0, len(weights))
		for a := range weights {
			accounts = append(accounts, a)
		}
		sort.Strings(accounts)
		for _, a := range accounts {
			c := new(big.Int).Mul(distributable, weights[a])
			c.Div(c, total)
			d.Credits[a] = c
			paid.Add(paid, c)
		}
	}
	d.Remainder = new(big.Int).Sub(distributable, paid)
	d.InputsHash = d.hashInputs()
	return d, nil
}

// hashInputs is a SHA-256 over the canonical encoding of everything the
// split depends on, so any miner can recompute and compare.
func (d *Distribution) hashInputs() [32]byte {
	h := sha256.New()
	var buf [8]byte
	put := func(v uint64) {
		binary.BigEndian.PutUint64(buf[:], v)
		h.Write(buf[:])
	}
	str := func(s string) {
		put(uint64(len(s)))
		h.Write([]byte(s))
	}
	str("igneum-pool-distribution-v1")
	str(d.BlockHash)
	put(d.Height)
	str(d.Reward.String())
	put(uint64(d.FeeBps))
	str(d.CarryIn.String())
	put(uint64(len(d.Shares)))
	for _, s := range d.Shares {
		put(s.Seq)
		str(s.Account)
		put(s.Weight)
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}
