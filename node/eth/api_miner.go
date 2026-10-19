// Copyright 2023 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package eth

import (
	"fmt"
	"math/big"
	"runtime"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
)

// PayoutEntry is one entry of the sortition prototype's payout list.
type PayoutEntry struct {
	Address common.Address `json:"address"`
	Weight  hexutil.Uint64 `json:"weight"`
}

// SetPayoutList sets the payout list the next templates carry (the
// sortition prototype: the list travels in the block body, its root in the
// header, the subsidy paid pro rata by weight); an empty list clears it.
func (api *MinerAPI) SetPayoutList(entries []PayoutEntry) (bool, error) {
	var list []*types.Withdrawal
	for i, e := range entries {
		list = append(list, &types.Withdrawal{Index: uint64(i), Address: e.Address, Amount: uint64(e.Weight)})
	}
	if err := api.e.Miner().SetPayoutList(list); err != nil {
		return false, err
	}
	return true, nil
}

// MinerAPI provides an API to control the miner.
type MinerAPI struct {
	e *Ethereum
}

// NewMinerAPI creates a new MinerAPI instance.
func NewMinerAPI(e *Ethereum) *MinerAPI {
	return &MinerAPI{e}
}

// Start starts the miner with the given number of threads. If threads is nil,
// the number of workers started is equal to the number of logical CPUs that are
// usable by this process. If mining is already running, this method adjust the
// number of threads allowed to use and updates the minimum price required by the
// transaction pool.
func (api *MinerAPI) Start(threads *int) error {
	if threads == nil {
		return api.e.StartMining(runtime.NumCPU())
	}
	return api.e.StartMining(*threads)
}

// Stop terminates the miner, both at the consensus engine level as well as at
// the block creation level.
func (api *MinerAPI) Stop() {
	api.e.StopMining()
}

// SetExtra sets the extra data string that is included when this miner mines a block.
func (api *MinerAPI) SetExtra(extra string) (bool, error) {
	// A 0x-prefixed hex string is decoded to its bytes before the length
	// check (a pool setting a member's 32-byte key as the template's Extra
	// was refused as 66 ASCII bytes, 11 October 2026); any other string is
	// taken as its bytes, as before.
	data := []byte(extra)
	if len(extra) >= 2 && (extra[:2] == "0x" || extra[:2] == "0X") {
		b, err := hexutil.Decode(extra)
		if err != nil {
			return false, fmt.Errorf("extra: a 0x-prefixed value must be hex: %w", err)
		}
		data = b
	}
	if err := api.e.Miner().SetExtra(data); err != nil {
		return false, err
	}
	return true, nil
}

// SetGasPrice sets the minimum accepted gas price for the miner.
func (api *MinerAPI) SetGasPrice(gasPrice hexutil.Big) bool {
	api.e.lock.Lock()
	api.e.gasPrice = (*big.Int)(&gasPrice)
	api.e.lock.Unlock()

	api.e.txPool.SetGasTip((*big.Int)(&gasPrice))
	api.e.Miner().SetGasTip((*big.Int)(&gasPrice))
	return true
}

// SetGasLimit sets the gaslimit to target towards during mining.
func (api *MinerAPI) SetGasLimit(gasLimit hexutil.Uint64) bool {
	api.e.Miner().SetGasCeil(uint64(gasLimit))
	return true
}

// SetEtherbase sets the etherbase of the miner.
func (api *MinerAPI) SetEtherbase(etherbase common.Address) bool {
	api.e.SetEtherbase(etherbase)
	return true
}

// SetRecommitInterval updates the interval for miner sealing work recommitting.
func (api *MinerAPI) SetRecommitInterval(interval int) {
	api.e.Miner().SetRecommitInterval(time.Duration(interval) * time.Millisecond)
}
