package igneum

import (
	"sort"

	"github.com/ethereum/go-ethereum/common"
)

// shareAccount is one address's share ledger on this node (G-23, "accepted
// means paid or credited", ecosystem's solo-row accounting of 19:2x UK, 11
// October 2026: 180 of a worker's 3,185 accepted shares were neither
// carried, pooled nor refused BY NAME, because the pool's counters were
// pool-wide and reset at every restart). Every accepted share of the
// address is in exactly one of carried, pooled, expiredReorg or
// expiredWindow, each by the share's height, so
//
//	accepted = carried + pooled + expiredReorg + expiredWindow
//
// holds at every moment and across restarts: the accounts are rebuilt from
// the share journal's full ledger (sharejournal.go, append-only since this
// change). Refusals are counted by name beside them and are not credit.
type shareAccount struct {
	accepted      map[uint64]uint64 // by height
	carried       map[uint64]uint64 // by height: a block within the recency carried it (this sealer's or another's)
	expiredReorg  map[uint64]uint64 // by height: the share's parent lost a reorg (a sibling's price)
	expiredWindow map[uint64]uint64 // by height: the recency passed with no carriage
	refused       map[string]uint64 // by reason
	pooled        uint64
}

func newShareAccount() *shareAccount {
	return &shareAccount{
		accepted:      make(map[uint64]uint64),
		carried:       make(map[uint64]uint64),
		expiredReorg:  make(map[uint64]uint64),
		expiredWindow: make(map[uint64]uint64),
		refused:       make(map[string]uint64),
	}
}

// carriedWeight is the address's accumulated carried weight: the shares a
// block carried for it, summed over the heights the ledger holds (the
// selection rule's key, PA-53's (4); the pool's lock is held).
func (a *shareAccount) carriedWeight() uint64 {
	var w uint64
	for _, n := range a.carried {
		w += n
	}
	return w
}

// account returns the address's ledger, made on first use (the pool's lock
// is held).
func (p *sharePool) account(a common.Address) *shareAccount {
	if p.accounts == nil {
		p.accounts = make(map[common.Address]*shareAccount)
	}
	acc := p.accounts[a]
	if acc == nil {
		acc = newShareAccount()
		p.accounts[a] = acc
	}
	return acc
}

// shareExpiry is the reason a pooled share left the pool uncarried.
type shareExpiry uint8

const (
	expiryWindow shareExpiry = 1 // the recency passed with no carriage
	expiryReorg  shareExpiry = 2 // the parent lost a reorg within the window
)

// ShareAccount is igneum_shareAccount's answer: an address's ledger with
// its totals, the counts by height, and the refusals by name.
type ShareAccount struct {
	Address       common.Address `json:"address"`
	Accepted      uint64         `json:"accepted"`
	Carried       uint64         `json:"carried"`
	Pooled        uint64         `json:"pooled"`
	ExpiredReorg  uint64         `json:"expiredReorg"`
	ExpiredWindow uint64         `json:"expiredWindow"`
	Refused       uint64         `json:"refused"`
	// Closes says accepted == carried + pooled + expiredReorg +
	// expiredWindow on this node's ledger.
	Closes          bool              `json:"closes"`
	AcceptedBy      map[uint64]uint64 `json:"acceptedByHeight,omitempty"`
	CarriedBy       map[uint64]uint64 `json:"carriedByHeight,omitempty"`
	ExpiredReorgBy  map[uint64]uint64 `json:"expiredReorgByHeight,omitempty"`
	ExpiredWindowBy map[uint64]uint64 `json:"expiredWindowByHeight,omitempty"`
	RefusedBy       map[string]uint64 `json:"refusedBy,omitempty"`
	// Heights is the ascending list of heights with any count, for a
	// reader that walks the row.
	Heights []uint64 `json:"heights,omitempty"`
}

func sumBy(m map[uint64]uint64) uint64 {
	var n uint64
	for _, c := range m {
		n += c
	}
	return n
}

func copyBy(m map[uint64]uint64) map[uint64]uint64 {
	if len(m) == 0 {
		return nil
	}
	out := make(map[uint64]uint64, len(m))
	for h, c := range m {
		out[h] = c
	}
	return out
}

// ShareAccountOf reads an address's ledger on this node.
func (e *Engine) ShareAccountOf(a common.Address) ShareAccount {
	p := &e.sharePool
	p.mu.Lock()
	defer p.mu.Unlock()
	out := ShareAccount{Address: a}
	acc := p.accounts[a]
	if acc == nil {
		out.Closes = true
		return out
	}
	out.Accepted = sumBy(acc.accepted)
	out.Carried = sumBy(acc.carried)
	out.Pooled = acc.pooled
	out.ExpiredReorg = sumBy(acc.expiredReorg)
	out.ExpiredWindow = sumBy(acc.expiredWindow)
	for _, c := range acc.refused {
		out.Refused += c
	}
	out.Closes = out.Accepted == out.Carried+out.Pooled+out.ExpiredReorg+out.ExpiredWindow
	out.AcceptedBy = copyBy(acc.accepted)
	out.CarriedBy = copyBy(acc.carried)
	out.ExpiredReorgBy = copyBy(acc.expiredReorg)
	out.ExpiredWindowBy = copyBy(acc.expiredWindow)
	if len(acc.refused) > 0 {
		out.RefusedBy = make(map[string]uint64, len(acc.refused))
		for r, c := range acc.refused {
			out.RefusedBy[r] = c
		}
	}
	seen := make(map[uint64]bool)
	for _, m := range []map[uint64]uint64{acc.accepted, acc.carried, acc.expiredReorg, acc.expiredWindow} {
		for h := range m {
			if !seen[h] {
				seen[h] = true
				out.Heights = append(out.Heights, h)
			}
		}
	}
	sort.Slice(out.Heights, func(i, k int) bool { return out.Heights[i] < out.Heights[k] })
	return out
}

// ShareLimits is igneum_shareLimits' answer: the bounds a worker paces by.
type ShareLimits struct {
	// ShareCapPerAddress is the accepted shares one address may hold at one
	// height (0 = off); PoolCapacity the pool-full bound (maxShares x the
	// recency); MaxShares the ceiling one block carries; Recency the
	// window in blocks a share's parent may trail the head by.
	ShareCapPerAddress uint64 `json:"shareCapPerAddress"`
	PoolCapacity       uint64 `json:"poolCapacity"`
	MaxShares          uint64 `json:"maxShares"`
	Recency            uint64 `json:"recency"`
}

func (e *Engine) ShareLimitsNow() ShareLimits {
	p := &e.sharePool
	p.mu.Lock()
	cap := p.cap
	p.mu.Unlock()
	return ShareLimits{ShareCapPerAddress: cap, PoolCapacity: e.SharePoolBound(), MaxShares: uint64(e.config.Parts.Sortition.MaxShares), Recency: e.ShareRecencyBlocks()}
}
