// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).
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

package igneum

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus/igneum/shares"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
)

// BlockReward is one block's payout as the chain paid it (PA-15; for the
// miner's earnings display, the plan page 17: estimated receipts, fees and
// costs shown separately).
//
// Encoding, fixed for every client: the amounts (subsidy, miner, fund,
// priorityFees, burned) are wei as hex quantities, the way geth encodes
// balances ("0x" followed by the hex digits, no leading zeros; "0x0" for
// zero); number and transactions are hex quantities too; hash and coinbase are
// the usual 0x-prefixed hex. No decimal strings.
type BlockReward struct {
	Number       hexutil.Uint64 `json:"number"`
	Hash         common.Hash    `json:"hash"`
	Coinbase     common.Address `json:"coinbase"`
	Subsidy      *hexutil.Big   `json:"subsidy"`      // the schedule's block subsidy
	Miner        *hexutil.Big   `json:"miner"`        // the coinbase's share of the subsidy
	Fund         *hexutil.Big   `json:"fund"`         // the maintenance fund's share
	PriorityFees *hexutil.Big   `json:"priorityFees"` // sum over transactions of gasUsed * effective tip, paid to the coinbase
	Burned       *hexutil.Big   `json:"burned"`       // gasUsed * baseFee, paid to nobody
	Transactions hexutil.Uint64 `json:"transactions"`
}

// maxBlockRewardRange bounds one igneum_blockReward call.
const maxBlockRewardRange = 1024

// BlockIssuance is one block's issuance as the chain settled it (the
// auditor's accounting row, "recorded as unissued": the node's own record of
// the Pay rule's figures, recomputed from the block's body, receipts and
// list by the same arithmetic Finalize credited from, so a restart answers
// the same by construction). The amounts are wei as hex quantities.
type BlockIssuance struct {
	Number       hexutil.Uint64 `json:"number"`
	Hash         common.Hash    `json:"hash"`
	Form         string         `json:"form"`         // "v1" (pro rata, the sealer's term a weight) or "v2" (the fixed quarter, the rest unissued)
	Subsidy      *hexutil.Big   `json:"subsidy"`      // the schedule's block subsidy
	Fund         *hexutil.Big   `json:"fund"`         // the fund's part of the subsidy
	Miner        *hexutil.Big   `json:"miner"`        // the subsidy less the fund
	PriorityFees *hexutil.Big   `json:"priorityFees"` // the block's fees (the coinbase's under the plain form, the amount's under a list)
	Amount       *hexutil.Big   `json:"amount"`       // what the Pay rule split: miner plus fees under a list, miner alone under the plain form
	Sealer       common.Address `json:"sealer"`       // the sealer's payout address (the list's last entry, or the coinbase)
	SealerPaid   *hexutil.Big   `json:"sealerPaid"`   // the sealer's term (zero under the fold, "v2-fold": the sealer is paid as a member on its standing)
	Folded       *hexutil.Big   `json:"folded,omitempty"` // F-165: the quarter folded into the pot (the moved part)
	ListedWeight hexutil.Uint64 `json:"listedWeight"` // W, the listed entries' summed weight (the sealer's entry excluded)
	Quorum       hexutil.Uint64 `json:"quorum"`       // Q under v2 (the lane's sharesPerBlock); 0 under v1
	Pot          *hexutil.Big   `json:"pot"`          // the listed entries' part before the per-entry floors
	Entries      hexutil.Uint64 `json:"entries"`      // the listed entries (the sealer's excluded)
	Credited     *hexutil.Big   `json:"credited"`     // every credit of the amount, the sealer's and the listed entries'
	Unissued     *hexutil.Big   `json:"unissued"`     // amount less credited: minted by the schedule and credited to nobody
}

// BlockIssuance answers igneum_blockIssuance(number): one block's issuance
// figures as the chain settled them. "latest" is accepted.
func (api *IgneumAPI) BlockIssuance(number rpc.BlockNumber) (*BlockIssuance, error) {
	if api.chain == nil {
		return nil, errors.New("igneum: no chain behind the API")
	}
	rc, ok := api.chain.(receiptChain)
	if !ok {
		return nil, errors.New("igneum: the chain behind the API serves headers only")
	}
	sched := api.engine.config.Parts.Issuance
	if sched == nil {
		return nil, errors.New("igneum: engine has no issuance schedule")
	}
	var n uint64
	switch {
	case number == rpc.LatestBlockNumber:
		n = api.chain.CurrentHeader().Number.Uint64()
	case number >= 0:
		n = uint64(number)
	default:
		return nil, fmt.Errorf("igneum: block tag %s is not supported by igneum_blockIssuance", number.String())
	}
	header := api.chain.GetHeaderByNumber(n)
	if header == nil {
		return nil, fmt.Errorf("igneum: block %d not found", n)
	}
	block := rc.GetBlock(header.Hash(), n)
	if block == nil {
		return nil, fmt.Errorf("igneum: block %d body not found", n)
	}
	receipts := rc.GetReceiptsByHash(header.Hash())
	if len(block.Transactions()) > 0 && len(receipts) != len(block.Transactions()) {
		return nil, fmt.Errorf("igneum: block %d has %d transactions and %d receipts", n, len(block.Transactions()), len(receipts))
	}
	r := blockReward(sched, block, receipts)
	out := &BlockIssuance{Number: r.Number, Hash: r.Hash, Subsidy: r.Subsidy, Fund: r.Fund, Miner: r.Miner, PriorityFees: r.PriorityFees, Sealer: block.Coinbase()}
	miner := (*big.Int)(r.Miner)
	fees := (*big.Int)(r.PriorityFees)
	v2 := api.engine.SharesOn(n) && api.engine.payV2On(n)
	list := shares.FromWithdrawals(block.Withdrawals())
	if pot, ok := api.engine.coordinatePay(api.chain, header); ok {
		// The Pay rule's coordinate dimension (revision 13, section 9):
		// the block's sealer takes its coordinate's accrued share in
		// place of the miner subsidy; a block's "unissued" is then the
		// subsidy's part left accruing to the other coordinates (positive)
		// or the accrued shares this block drew (negative), and nothing
		// is lost by the schedule.
		miner = pot
		out.Form = "coordinate"
	}
	if len(list) == 0 || !api.engine.AllowsPayoutList(header) {
		// The plain form: the subsidy's miner part to the coinbase (the
		// whole under v1, a quarter under v2, the coordinate's accrued
		// share under the coordinate rule), the fees the coinbase's.
		out.Amount = (*hexutil.Big)(new(big.Int).Set(miner))
		paid := new(big.Int).Set(miner)
		if out.Form == "coordinate" {
			out.Unissued = (*hexutil.Big)(new(big.Int).Sub((*big.Int)(r.Miner), paid))
		} else if v2 {
			out.Form = "v2"
			paid = new(big.Int).Div(miner, big.NewInt(4))
		} else {
			out.Form = "v1"
		}
		out.SealerPaid, out.Pot = (*hexutil.Big)(paid), (*hexutil.Big)(new(big.Int))
		out.Credited = (*hexutil.Big)(new(big.Int).Set(paid))
		if out.Form != "coordinate" {
			out.Unissued = (*hexutil.Big)(new(big.Int).Sub(miner, paid))
		}
		return out, nil
	}
	amount := new(big.Int).Add(miner, fees)
	out.Amount = (*hexutil.Big)(amount)
	out.Sealer = list[len(list)-1].Address
	out.Entries = hexutil.Uint64(len(list) - 1)
	out.ListedWeight = hexutil.Uint64(TotalWeight(list[:len(list)-1]))
	if v2 {
		a := payV2Amounts(amount, list, api.engine.payQuorumWeight(), api.engine.sealerFoldAt(uint64(r.Number)))
		out.Form = "v2"
		if a.Fold {
			out.Form = "v2-fold"
			out.Folded = (*hexutil.Big)(a.Folded)
		}
		out.Quorum = hexutil.Uint64(a.Quorum)
		out.SealerPaid, out.Pot = (*hexutil.Big)(a.Quarter), (*hexutil.Big)(a.Pot)
		out.Credited, out.Unissued = (*hexutil.Big)(a.Credited), (*hexutil.Big)(a.Unissued)
		return out, nil
	}
	// The first form: pro rata over the whole list, the sealer's term its
	// last entry's share, nothing unissued (the dust is carried). Under
	// the coordinate rule the list divides the coordinate's accrued share
	// and the form keeps its name, its unissued the subsidy's part left
	// accruing (positive) or the shares the block drew (negative), as the
	// plain form reads it (the security review's TP-256 at vectorwork6's 36 and 44).
	coord := out.Form == "coordinate"
	out.Form = "v1"
	if api.engine.sealerFoldListAt(uint64(r.Number)) && len(list) > 0 && list[len(list)-1].Weight == 0 {
		// F-165 under the first form: the sealer's term retired (its entry
		// a name at weight 0); the term the rule would have paid, over
		// the listed weight plus SealerWeightOf, reported as the moved part.
		if coord {
			out.Form = "coordinate-fold"
		} else {
			out.Form = "v1-fold"
		}
		wl := TotalWeight(list[:len(list)-1])
		ws := SealerWeightOf(wl)
		folded := new(big.Int).Mul(amount, new(big.Int).SetUint64(ws))
		folded.Div(folded, new(big.Int).SetUint64(wl+ws))
		out.Folded = (*hexutil.Big)(folded)
	}
	paid, _ := shares.Pay(amount, list)
	credited := new(big.Int)
	sealerPaid := new(big.Int)
	for _, p := range paid {
		credited.Add(credited, p.Amount)
		if p.Address == out.Sealer {
			sealerPaid.Add(sealerPaid, p.Amount)
		}
	}
	out.SealerPaid, out.Pot = (*hexutil.Big)(sealerPaid), (*hexutil.Big)(new(big.Int).Sub(amount, sealerPaid))
	out.Credited = (*hexutil.Big)(credited)
	out.Unissued = (*hexutil.Big)(new(big.Int).Sub(amount, credited))
	if coord {
		out.Form = "coordinate"
		out.Unissued = (*hexutil.Big)(new(big.Int).Sub((*big.Int)(r.Miner), miner))
	}
	return out, nil
}

// blockCredited is the sum a block's issuance put into addresses (the fund's
// share plus the miner part credited: the whole under v1, the quarter or the
// list's settled amount under v2, the coordinate's accrued share under the
// coordinate rule), for the supply's credited index (F-98: igneum_supply
// counted the schedule's gross, which under v2 includes three quarters
// credited to nobody).
func (api *IgneumAPI) blockCredited(header *types.Header) (*big.Int, error) {
	bi, err := api.BlockIssuance(rpc.BlockNumber(header.Number.Int64()))
	if err != nil {
		return nil, err
	}
	sum := new(big.Int).Set((*big.Int)(bi.Fund))
	sum.Add(sum, (*big.Int)(bi.Credited))
	// Under a payout list the credited amount carries the block's priority
	// fees (moved money, the users'), which are not issuance (the security review's
	// F-102 on the old public test network's (chain 7320) six fee-carrying blocks: the supply stepped by
	// the fees and the cumulative unissued by minus the fees).
	if bi.Entries > 0 && bi.PriorityFees != nil {
		sum.Sub(sum, (*big.Int)(bi.PriorityFees))
	}
	return sum, nil
}

// receiptChain is what the API needs beyond headers: the concrete chain
// behind the engine's API is *core.BlockChain, which has both.
type receiptChain interface {
	GetBlock(hash common.Hash, number uint64) *types.Block
	GetReceiptsByHash(hash common.Hash) types.Receipts
}

// BlockReward answers igneum_blockReward(from, to): the payout of every block
// in the range, inclusive, most recent last. "latest" is accepted for either
// end; "pending", "safe" and "finalized" are not (the plan, page 10: no
// invented finality). At most 1024 blocks per call.
func (api *IgneumAPI) BlockReward(from, to rpc.BlockNumber) ([]*BlockReward, error) {
	if api.chain == nil {
		return nil, errors.New("igneum: no chain behind the API")
	}
	rc, ok := api.chain.(receiptChain)
	if !ok {
		return nil, errors.New("igneum: the chain behind the API serves headers only")
	}
	sched := api.engine.config.Parts.Issuance
	if sched == nil {
		return nil, errors.New("igneum: engine has no issuance schedule")
	}
	resolve := func(n rpc.BlockNumber) (uint64, error) {
		switch {
		case n == rpc.LatestBlockNumber:
			return api.chain.CurrentHeader().Number.Uint64(), nil
		case n >= 0:
			return uint64(n), nil
		default:
			return 0, fmt.Errorf("igneum: block tag %s is not supported by igneum_blockReward", n.String())
		}
	}
	start, err := resolve(from)
	if err != nil {
		return nil, err
	}
	end, err := resolve(to)
	if err != nil {
		return nil, err
	}
	if end < start {
		return nil, fmt.Errorf("igneum: range end %d is before start %d", end, start)
	}
	// A range above the head answers what exists: nothing for a range wholly
	// above it (a miner asking for the last blocks of a chain that has none
	// yet), the blocks up to the head otherwise. "latest" is the head.
	head := api.chain.CurrentHeader().Number.Uint64()
	if start > head {
		return []*BlockReward{}, nil
	}
	if end > head {
		end = head
	}
	if end-start+1 > maxBlockRewardRange {
		return nil, fmt.Errorf("igneum: range of %d blocks exceeds the limit of %d", end-start+1, maxBlockRewardRange)
	}
	out := make([]*BlockReward, 0, end-start+1)
	for n := start; n <= end; n++ {
		header := api.chain.GetHeaderByNumber(n)
		if header == nil {
			return nil, fmt.Errorf("igneum: block %d not found", n)
		}
		block := rc.GetBlock(header.Hash(), n)
		if block == nil {
			return nil, fmt.Errorf("igneum: block %d body not found", n)
		}
		receipts := rc.GetReceiptsByHash(header.Hash())
		if len(block.Transactions()) > 0 && len(receipts) != len(block.Transactions()) {
			return nil, fmt.Errorf("igneum: block %d has %d transactions and %d receipts", n, len(block.Transactions()), len(receipts))
		}
		out = append(out, blockReward(sched, block, receipts))
	}
	return out, nil
}

// issuer is the part of the schedule the reward view uses.
type issuer interface {
	Subsidy(height uint64) *big.Int
	Reward(height uint64) (miner, fund *big.Int)
}

// blockReward computes one block's payout from its body and receipts. The
// effective tip of a transaction is min(maxPriorityFeePerGas, maxFeePerGas -
// baseFee) (EIP-1559), or gasPrice - baseFee for a legacy transaction; a block
// without a base fee (test configurations) pays the whole gas price as tip.
func blockReward(sched issuer, block *types.Block, receipts types.Receipts) *BlockReward {
	height := block.NumberU64()
	miner, fund := sched.Reward(height)
	baseFee := block.BaseFee()
	fees := new(big.Int)
	burned := new(big.Int)
	for i, tx := range block.Transactions() {
		gasUsed := new(big.Int).SetUint64(receipts[i].GasUsed)
		var tip *big.Int
		if baseFee == nil {
			tip = tx.GasPrice()
		} else {
			tip, _ = tx.EffectiveGasTip(baseFee)
			if tip == nil || tip.Sign() < 0 {
				tip = new(big.Int)
			}
			burned.Add(burned, new(big.Int).Mul(gasUsed, baseFee))
		}
		fees.Add(fees, new(big.Int).Mul(gasUsed, tip))
	}
	return &BlockReward{
		Number:       hexutil.Uint64(height),
		Hash:         block.Hash(),
		Coinbase:     block.Coinbase(),
		Subsidy:      (*hexutil.Big)(sched.Subsidy(height)),
		Miner:        (*hexutil.Big)(miner),
		Fund:         (*hexutil.Big)(fund),
		PriorityFees: (*hexutil.Big)(fees),
		Burned:       (*hexutil.Big)(burned),
		Transactions: hexutil.Uint64(len(block.Transactions())),
	}
}
