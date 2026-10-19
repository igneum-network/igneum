package igneum

import "math/big"

// F-192 (the node team's fresh sync of the research chain, 16 October 2026,
// 00:4x UK): the minimum pay rule's y lift bound was scaled to the pay window
// (A256_c x window / 256) in node 0.10.18 (b3a88b8f, 17:32 UK, 15 October)
// without a height gate. The unscaled bound (y at most A256_c) never bound,
// so every block sealed before the roll carries a list derived without it,
// and the scaled one binds: a fresh walk under the new rule derived the
// research chain's 4211 1.33x over its carried list and stopped, and the
// throwaway chain's 65 the same (the bisect by sync on a builder's box: 0.10.17's
// build c94bb1fde syncs through, 0.10.19's 6fdd7feb refuses). The gate is
// the chain's payBoundUnscaledUntil (params.IgneumPayBoundUnscaledUntil, by
// chain id, as the rule forks are): heights at or under it derive with the
// unscaled bound, heights above with the scaled one; a chain without one
// derives scaled from genesis, as the code stood.

// minPayBound is the minimum rule's bound on the y lift: A256_c unscaled,
// A256_c x window / 256 scaled.
func minPayBound(a256c *big.Int, window uint64, scaled bool) *big.Int {
	if !scaled {
		return new(big.Int).Set(a256c)
	}
	bound := new(big.Int).Mul(a256c, new(big.Int).SetUint64(window))
	return bound.Div(bound, big.NewInt(256))
}

// payBoundUnscaledAt reports whether a height derives with the unscaled
// bound (at or under the chain's payBoundUnscaledUntil; 0: never).
func (e *Engine) payBoundUnscaledAt(height uint64) bool {
	e.lock.Lock()
	until := e.payBoundUnscaledUntil
	e.lock.Unlock()
	return until != 0 && height <= until
}

// SetPayBoundUnscaledUntil sets the chain's gate (the backend, from the chain
// id's entry).
func (e *Engine) SetPayBoundUnscaledUntil(until uint64) {
	e.lock.Lock()
	e.payBoundUnscaledUntil = until
	e.lock.Unlock()
}

// PayBoundUnscaledUntil is the gate in force (0: none).
func (e *Engine) PayBoundUnscaledUntil() uint64 {
	e.lock.Lock()
	defer e.lock.Unlock()
	return e.payBoundUnscaledUntil
}

// SetRegistryNoticeFrom sets the chain's notice-floor gate (F-192,
// ReregistrationFrom; the backend, from the chain id's entry in
// params.IgneumChainRegistryNoticeFrom): the first landing height at which
// a chain-held registration without the research mark needs noticeBlocks
// of at least 64; landings under it are admitted as the chain admitted
// them.
func (e *Engine) SetRegistryNoticeFrom(from uint64) {
	e.lock.Lock()
	e.registryNoticeFrom = from
	e.lock.Unlock()
}

// RegistryNoticeFrom is the notice-floor gate in force (0: the floor binds
// at every landing).
func (e *Engine) RegistryNoticeFrom() uint64 {
	e.lock.Lock()
	defer e.lock.Unlock()
	return e.registryNoticeFrom
}
