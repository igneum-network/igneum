package service

import (
	"fmt"
	"math/big"
	"sort"
)

// The payout per coordinate (0.1.85, F-497, 17 October 2026; the review lead's ruling (A), economics' form of record): the
// balanced reward is homogeneous of degree one in the pool key's work, so the window's income from the chain splits
// EXACTLY into p_c times the income per coordinate, p_c the chain's pay share of coordinate c over the paid
// coordinates' sum, whatever lanes the blocks landed on; within a coordinate each member is paid its carried share
// weight on that coordinate less its fee there over the pool's carried weight on it. A member's incentive then
// equals the pool's and the chain's: a diverse-hardware chain pays each lane's hardware for its lane. On a chain with
// one coordinate (or none named) p = 1 and the rule is the pool-wide payout as before.

// CoordWindowRow is one coordinate's figures over a closed window on the record (0.1.86): its pay share, the pool's
// carried weight and fee work on it, and its part of the income; lane_worth is IncomeWei over Work.
type CoordWindowRow struct {
	Coord     uint8  `json:"coord"`
	PayShare  uint64 `json:"pay_share"`
	Work      uint64 `json:"paid_work"`
	FeeWork   uint64 `json:"fee_work"`
	IncomeWei string `json:"income_wei"`
}

// CoordDue is one coordinate's part of a member's due.
type CoordDue struct {
	Coord   uint8  `json:"coord"`
	Work    uint64 `json:"paid_work"`
	FeeWork uint64 `json:"fee_work"`
	DueWei  string `json:"due_wei"`
}

// coordWindow is one coordinate's figures over a pay window: its pay share, the pool-wide carried weight and fee
// work, and each member's.
type coordWindow struct {
	coord         uint8
	payShare      uint64
	work, feeWork uint64
	members       map[string]memberWork
}

// memberWork is a member's carried weight and fee work on one coordinate over the window.
type memberWork struct {
	Work, FeeWork uint64
}

// splitByCoord divides the window's income: income_c = income x p_c / sum(p) over the coordinates with carried
// work; a member's due on c = income_c x (its work less its fee work) / the coordinate's work; the fee part on c =
// income_c x the coordinate's fee work / its work; every division floors in wei and the remainder (income less the
// dues less the fee) stays in the key, named on the window (the rounding rule of record: the remainder is the
// pool's, never a member's). Returns the dues by member (with the per-coordinate rows), the fee part and the
// remainder. With one coordinate (or none: the pool-wide form passes a single coordWindow with payShare 1) the
// result is today's rule exactly.
func splitByCoord(income *big.Int, windows []coordWindow) (dues map[string]*big.Int, byCoord map[string][]CoordDue, fee, remainder *big.Int) {
	dues, byCoord, fee, remainder, _ = splitByCoordRows(income, windows)
	return dues, byCoord, fee, remainder
}

// splitByCoordRows is splitByCoord with each coordinate's window row (its part of the income) for the record.
func splitByCoordRows(income *big.Int, windows []coordWindow) (dues map[string]*big.Int, byCoord map[string][]CoordDue, fee, remainder *big.Int, rows []CoordWindowRow) {
	dues = map[string]*big.Int{}
	byCoord = map[string][]CoordDue{}
	fee = new(big.Int)
	paid := new(big.Int)
	var sumP uint64
	for _, w := range windows {
		if w.work > 0 && w.payShare > 0 {
			sumP += w.payShare
		}
	}
	for _, w := range windows {
		if w.work == 0 || w.payShare == 0 || sumP == 0 {
			continue
		}
		incomeC := new(big.Int).Mul(income, new(big.Int).SetUint64(w.payShare))
		incomeC.Div(incomeC, new(big.Int).SetUint64(sumP))
		rows = append(rows, CoordWindowRow{Coord: w.coord, PayShare: w.payShare, Work: w.work, FeeWork: w.feeWork, IncomeWei: incomeC.String()})
		feeC := new(big.Int).Mul(incomeC, new(big.Int).SetUint64(w.feeWork))
		feeC.Div(feeC, new(big.Int).SetUint64(w.work))
		fee.Add(fee, feeC)
		members := make([]string, 0, len(w.members))
		for m := range w.members {
			members = append(members, m)
		}
		sort.Strings(members)
		for _, m := range members {
			mw := w.members[m]
			if mw.Work == 0 || mw.Work < mw.FeeWork {
				continue
			}
			due := new(big.Int).Mul(incomeC, new(big.Int).SetUint64(mw.Work-mw.FeeWork))
			due.Div(due, new(big.Int).SetUint64(w.work))
			if dues[m] == nil {
				dues[m] = new(big.Int)
			}
			dues[m].Add(dues[m], due)
			paid.Add(paid, due)
			byCoord[m] = append(byCoord[m], CoordDue{Coord: w.coord, Work: mw.Work, FeeWork: mw.FeeWork, DueWei: due.String()})
		}
	}
	remainder = new(big.Int).Sub(income, paid)
	remainder.Sub(remainder, fee)
	if remainder.Sign() < 0 {
		remainder.SetInt64(0)
	}
	return dues, byCoord, fee, remainder, rows
}

// LaneWorthWei is 0.1.86's lane_worth (F-496 under F-497's rule): a coordinate's income over the pool's carried share
// weight on it over the last closed pay window, wei per unit of share weight, the member's marginal pay there; ""
// when the window carried no work on the coordinate.
func LaneWorthWei(incomeWei string, work uint64) string {
	inc, ok := new(big.Int).SetString(incomeWei, 10)
	if !ok || work == 0 || inc.Sign() <= 0 {
		return ""
	}
	f, _ := new(big.Float).Quo(new(big.Float).SetInt(inc), new(big.Float).SetUint64(work)).Float64()
	return fmt.Sprintf("%.4e", f)
}
