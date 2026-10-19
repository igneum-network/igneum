// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).

package igneum

import (
	"errors"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus/igneum/pool"
	"github.com/ethereum/go-ethereum/rpc"
)

// PoolMint is one coinbase note as the chain records it (pool/coinbase.go;
// docs/money/PAYOUT-PRIVACY-SPEC-2026-10-18.md section 4.2): the leaf
// position, the sealer, the value, the randomness derived from public
// data and the commitment. What a watcher sees, whole; the owner's key is
// in the sealer's registration, and nothing after the mint names either.
type PoolMint struct {
	Position   uint64         `json:"position"`
	Sealer     common.Address `json:"sealer"`
	Value      *hexutil.Big   `json:"value"`
	Randomness common.Hash    `json:"randomness"`
	Commitment common.Hash    `json:"commitment"`
}

// PoolPayoutAddress is the shielded payout address an account registered
// (igneum_poolPayoutAddress): the recipient key, the encapsulation key and
// the diversifier, or nil for none. An outside pool reads it as the node
// does, to pay a member by a shielded output.
type PoolPayoutAddress struct {
	Account     common.Address `json:"account"`
	PK          common.Hash    `json:"pk"`
	EK          hexutil.Bytes  `json:"ek"`
	Diversifier hexutil.Bytes  `json:"d"`
}

// PoolPayoutAddress is igneum_poolPayoutAddress.
func (api *IgneumAPI) PoolPayoutAddress(account common.Address, number *rpc.BlockNumber) (*PoolPayoutAddress, error) {
	if api.chain == nil || api.chain.Config() == nil || api.chain.Config().GetChainID() == nil {
		return nil, errors.New("no chain id")
	}
	st, err := api.stateAt(number)
	if err != nil {
		return nil, err
	}
	chainID := api.chain.Config().GetChainID().Uint64()
	r, ok := pool.Open(st, pool.Address(chainID), chainID, pool.Poseidon2{}).PayoutAddress(account)
	if !ok {
		return nil, nil
	}
	return &PoolPayoutAddress{Account: account, PK: common.Hash(r.PK), EK: r.EK, Diversifier: r.D[:]}, nil
}
