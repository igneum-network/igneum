// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).

package igneum

import (
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/igneum/pool"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/holiman/uint256"
)

// The shielded pool (Project Money stage one, cut two; consensus/igneum/pool):
// a chain-id gate, IgneumPoolFrom, like the sponsored lane's: from the
// height the pool address exists and Finalize admits or refuses the
// shields and spends sent to it; before it, or on a chain with no entry
// (ZERO IS OFF, every chain of record), nothing of the pool writes state,
// so a node on this build and one on the build before write the same
// state root everywhere the gate is zero.
//
// Stage two, cut two (the shielded coinbase; pool/coinbase.go): under the
// same gate a sealer registers a shielded payout address by a record to
// the pool address, and the sealer share of every later block it seals is
// minted as a coinbase note after the block's records, in place of the
// public credit. Below the gate the registration record is an ordinary
// transaction to an address with no code (nothing written, as a shield
// or a spend is) and the sealer is paid as before.

// poolOn says whether the pool is in force at height on this chain.
func (e *Engine) poolOn(height uint64) bool {
	e.lock.Lock()
	from := e.poolFrom
	e.lock.Unlock()
	return from > 0 && height >= from
}

// SetPoolFrom sets the chain id's gate (params.IgneumChainPoolFrom; 0 off).
func (e *Engine) SetPoolFrom(from uint64) {
	e.lock.Lock()
	e.poolFrom = from
	e.lock.Unlock()
}

// PoolFrom is the gate (0: off).
func (e *Engine) PoolFrom() uint64 {
	e.lock.Lock()
	defer e.lock.Unlock()
	return e.poolFrom
}

// SetPoolVerifier sets the proof verifier the spends are checked through
// (nil: pool.NoVerifier, every spend refused). The real verifier is set
// here when cut one lands; the lab double only by the backend under --dev
// on a research-range chain id.
func (e *Engine) SetPoolVerifier(v pool.Verifier) {
	e.lock.Lock()
	e.poolVerifier = v
	e.lock.Unlock()
}

// SetPoolStore gives the engine the database the retained records of the
// pruned blocks live in (pool.RetainedBlock; the backend's chain database),
// read by igneum_poolBlock and igneum_poolRetained and written by the
// pruner (eth/pool_prune.go) and the backfill (igneum_poolBackfill).
func (e *Engine) SetPoolStore(db ethdb.KeyValueStore) {
	e.lock.Lock()
	e.poolStore = db
	e.lock.Unlock()
}

// PoolStore is the retained records' database (nil: none).
func (e *Engine) PoolStore() ethdb.KeyValueStore {
	e.lock.Lock()
	defer e.lock.Unlock()
	return e.poolStore
}

// PoolVerifier is the verifier in use.
func (e *Engine) PoolVerifier() pool.Verifier {
	e.lock.Lock()
	defer e.lock.Unlock()
	if e.poolVerifier == nil {
		return pool.NoVerifier{}
	}
	return e.poolVerifier
}

// poolChainID is the chain id the pool is keyed by (false: no chain, no
// id, or the pool off at the height).
func (e *Engine) poolChainID(chain consensus.ChainHeaderReader, header *types.Header) (uint64, bool) {
	if chain == nil || chain.Config() == nil || chain.Config().GetChainID() == nil || header == nil || !e.poolOn(header.Number.Uint64()) {
		return 0, false
	}
	return chain.Config().GetChainID().Uint64(), true
}

// sealerMints says whether the block's sealer share is minted as a coinbase
// note rather than credited: the pool in force and the coinbase account
// holding a registered payout key in the state as it stands before this
// block's pool records (a registration carried by this very block counts
// from the next, on assembly and on import alike, since Finalize reads
// the key before settlePool applies the block's records).
func (e *Engine) sealerMints(chain consensus.ChainHeaderReader, header *types.Header, st *state.StateDB) bool {
	chainID, on := e.poolChainID(chain, header)
	if !on {
		return false
	}
	_, has := pool.Open(st, pool.Address(chainID), chainID, pool.Poseidon2{}).PayoutKey(header.Coinbase)
	return has
}

// paySealer credits the sealer share to the coinbase, or returns it as the
// amount to mint when the sealer registered a payout key (settlePool mints
// it after the block's records, so the coinbase note sits after them in
// the tree, the spec's order). Zero: paid publicly, nothing to mint.
func (e *Engine) paySealer(chain consensus.ChainHeaderReader, header *types.Header, st *state.StateDB, amount *big.Int) *big.Int {
	if amount == nil || amount.Sign() <= 0 {
		return new(big.Int)
	}
	if amount.BitLen() <= 128 && e.sealerMints(chain, header, st) {
		return amount
	}
	creditReward(st, header.Coinbase, uint256.MustFromBig(amount))
	return new(big.Int)
}

// settlePool is Finalize's read of the block's transactions to the pool
// address: each shield, spend or registration admitted by the pool's
// rules or refused with its value returned to the sender (a transaction
// to the pool with no readable record is refused the same way; nothing is
// ever stuck), in the pool's order (pool.Ordered, F-801: a vault's
// announcement, recovery cancellation and withdrawal cancellation before
// the block's other records, so a head cannot be raced by a request in
// the same block); then the sealer's coinbase note when mint is positive (the
// sealer share paySealer withheld from the public credit): the mint
// credits the pool's balance and the turnstile together, so the sweep
// below leaves it; then the root recorded once if the block appended;
// then the stray value (the balance above the turnstile that no shield
// and no mint brought) burned (F-775).
func (e *Engine) settlePool(chain consensus.ChainHeaderReader, header *types.Header, st *state.StateDB, txs []*types.Transaction, mint *big.Int) {
	minting := mint != nil && mint.Sign() > 0
	// No early return on an empty block: F-775's burn of stray value runs
	// at every block the pool is on, transactions or none.
	chainID, on := e.poolChainID(chain, header)
	if !on {
		return
	}
	height := header.Number.Uint64()
	addr := pool.Address(chainID)
	var p *pool.State
	verifier := e.PoolVerifier()
	// The key the coinbase note is minted to: the sealer's registration as
	// the block began (paySealer withheld on the same read), so a
	// registration or a clearing this block carries counts from the next.
	var mintKey [32]byte
	if minting {
		p = pool.Open(st, addr, chainID, pool.Poseidon2{})
		mintKey, _ = p.PayoutKey(header.Coinbase)
	}
	for _, tx := range pool.Ordered(txs, addr) {
		sender, err := types.Sender(types.LatestSignerForChainID(tx.ChainId()), tx)
		if err != nil {
			continue
		}
		if p == nil {
			p = pool.Open(st, addr, chainID, pool.Poseidon2{})
		}
		value := tx.Value()
		started := time.Now()
		var why, form string
		if sh, ok := pool.ParseShield(tx.Data()); ok {
			form = "shield"
			var index uint64
			index, why = p.AdmitShield(sender, value, sh, verifier)
			if why == "" {
				e.log.Info("Shielded pool shield admitted", "number", height, "index", index, "value", value, "tx", tx.Hash(), "took", time.Since(started))
				continue
			}
		} else if sp, ok := pool.ParseSpend(tx.Data()); ok {
			form = "spend"
			why = p.AdmitSpend(sender, value, sp, verifier)
			if why == "" {
				e.log.Info("Shielded pool spend admitted", "number", height, "inputs", len(sp.Nullifiers), "outputs", len(sp.Outputs), "public", sp.PublicValue, "fee", sp.Fee, "tx", tx.Hash(), "took", time.Since(started))
				continue
			}
		} else if rg, ok := pool.ParseRegistration(tx.Data()); ok {
			form = "registration"
			why = p.AdmitRegistration(sender, value, rg)
			if why == "" {
				e.log.Info("Shielded pool payout address registered", "number", height, "account", sender, "cleared", rg.Empty(), "tx", tx.Hash())
				continue
			}
		} else if vr, ok := pool.ParseVault(tx.Data()); ok {
			// A vault statement (stage two, cut one; pool/vault.go) under the
			// including block's height and header time.
			form = pool.VaultName(vr.Tag)
			why = p.AdmitVault(sender, value, vr, verifier, height, header.Time)
			if why == "" {
				e.log.Info("Shielded pool vault statement admitted", "number", height, "statement", form, "outputs", len(vr.Outputs), "declared", vr.Height, "tx", tx.Hash(), "took", time.Since(started))
				continue
			}
		} else {
			form, why = "record", "no readable shield, spend, registration or vault record"
		}
		// The refusal: the value back to the sender in this block (the pool's
		// balance above its turnstile, which the value is).
		if value != nil && value.Sign() > 0 {
			free := new(uint256.Int).Sub(st.GetBalance(addr), uint256.MustFromBig(p.Turnstile()))
			if v := uint256.MustFromBig(value); free.Cmp(v) >= 0 {
				st.SubBalance(addr, v)
				st.AddBalance(sender, v)
			}
		}
		e.log.Warn("Shielded pool "+form+" refused (the value returned)", "number", height, "tx", tx.Hash(), "why", why, "took", time.Since(started))
	}
	if minting {
		if p == nil {
			p = pool.Open(st, addr, chainID, pool.Poseidon2{})
		}
		started := time.Now()
		if index, cm, ok := p.Mint(header.Coinbase, mintKey, mint, height, header.ParentHash); ok {
			e.log.Info("Shielded coinbase minted", "number", height, "sealer", header.Coinbase, "value", mint, "index", index, "commitment", common.Hash(cm), "took", time.Since(started))
		} else {
			// Unreachable by construction (paySealer withholds only for a
			// registered key and a 128-bit amount, read as mintKey is; the
			// tree's 2^32 is not reached): the share is paid publicly so no
			// wei is lost.
			e.log.Error("Shielded coinbase not minted (paid publicly)", "number", height, "sealer", header.Coinbase, "value", mint)
			creditReward(st, header.Coinbase, uint256.MustFromBig(mint))
		}
	}
	if p != nil && p.RecordRoot() {
		p.RecordCountAt(height)
		e.log.Info("Shielded pool root recorded", "number", height, "count", p.Count(), "seq", p.Seq(), "root", p.Root())
	}
	// F-775: value that reached the pool address by any path but a shield
	// (an internal call, a selfdestruct, the coinbase) sits above the
	// turnstile with no way out; the pool's rule is that it is burned at the
	// block's end, so the balance is the turnstile and nothing is stranded.
	if burned := pool.BurnStray(st, addr); burned.Sign() > 0 {
		e.log.Warn("Shielded pool stray value burned (arrived by no shield)", "number", height, "value", burned)
	}
}

// settlePoolTxs is settlePool with no coinbase note (the tests' and the
// earlier callers' form).
func (e *Engine) settlePoolTxs(chain consensus.ChainHeaderReader, header *types.Header, st *state.StateDB, txs []*types.Transaction) {
	e.settlePool(chain, header, st, txs, nil)
}
