// Package dashboard computes the chain-derived measures of the outcomes
// dashboard (the plan, page 29; docs/ecosystem/DESIGN.md, section 7)
// from the indexer's rows and the node's supply record. Every measure
// states its source and its state; a measure the chain cannot give is
// "not_yet_measured", never a guess.
package dashboard

import (
	"math"
	"math/big"
	"sort"
	"strings"
	"time"

	"igneum.network/indexer/internal/chain"
	"igneum.network/indexer/internal/rpc"
)

// States a measure can be in.
const (
	Measured       = "measured"
	NotYetMeasured = "not_yet_measured"
	Curated        = "curated"
)

// Measure is one cell of the dashboard.
type Measure struct {
	Value  interface{} `json:"value"`
	State  string      `json:"state"`
	Source string      `json:"source"`
	Window string      `json:"window,omitempty"`
	Note   string      `json:"note,omitempty"`
}

// Document is the dashboard JSON, version 0.
type Document struct {
	Version    int                           `json:"version"`
	Network    string                        `json:"network"`
	ChainID    uint64                        `json:"chain_id"`
	Updated    string                        `json:"updated"`
	Head       uint64                        `json:"head"`
	WindowSize int                           `json:"window_blocks"`
	Claims     string                        `json:"claims"`
	Dimensions map[string]map[string]Measure `json:"dimensions"`
}

// Input is what Build needs.
type Input struct {
	Network string
	ChainID uint64
	Rows    []chain.Row // canonical rows, oldest first
	Window  int         // blocks per window
	Reorgs  int
	Supply  *rpc.Supply // nil when the node lacks igneum_supply
	Now     time.Time
	// Contracts are the published application contracts (name to
	// address), Labs the addresses whose activity is excluded from the
	// adoption measures; both curated from dev/chain/igneum-chains.json.
	Contracts map[string]string
	Labs      []string
}

func nym(state, source string) Measure { return Measure{State: state, Source: source} }

func notYet(source, note string) Measure {
	return Measure{State: NotYetMeasured, Source: source, Note: note}
}

// Build computes the document.
func Build(in Input) Document {
	d := Document{
		Version: 0, Network: in.Network, ChainID: in.ChainID,
		Updated: in.Now.UTC().Format(time.RFC3339), WindowSize: in.Window,
		Claims:     "Measures, not rankings. Nothing here is a claim of rank, profit or support; see the plan, pages 28 to 30.",
		Dimensions: map[string]map[string]Measure{},
	}
	rows := in.Rows
	if len(rows) > 0 {
		d.Head = rows[len(rows)-1].Number
	}
	w := in.Window
	if w <= 0 {
		w = 1000
	}
	last := tail(rows, w)
	prev := tail(rows[:len(rows)-len(last)], w)
	winLabel := "last " + itoa(w) + " blocks"

	// Mining
	mining := map[string]Measure{}
	miners := distinct(last, func(r chain.Row) []string { return []string{r.Miner} })
	m := nym(Measured, "light indexer: distinct coinbase addresses over the window")
	m.Value, m.Window = len(miners), winLabel
	if len(last) == 0 {
		m = notYet("light indexer", "no blocks indexed")
	}
	mining["independent_participation"] = m
	entry, exit := entryExit(prev, last)
	ee := nym(Measured, "light indexer: coinbase addresses new in this window and gone since the previous")
	ee.Value, ee.Window = map[string]int{"entered": entry, "departed": exit}, winLabel
	if len(prev) == 0 {
		ee = notYet("light indexer", "needs two full windows")
	}
	mining["entry_exit"] = ee
	mining["replacement_economics"] = notYet("Phase Zero economics model, harness wall measurements, published tariffs", "not derivable from the chain")
	mining["accepted_work_per_wall_kwh"] = notYet("the harness (external wall measurement), HW-03 runs", "not derivable from the chain")
	mining["realised_fee_burden"] = notYet("pool disclosure documents weighted by concentration; chain payouts", "pool directory not yet aggregated")
	d.Dimensions["mining"] = mining

	// Reliability
	rel := map[string]Measure{}
	rr := nym(Measured, "light indexer: parent-hash mismatches seen while walking the chain")
	rr.Value = in.Reorgs
	rel["reorganisations_observed"] = rr
	if iv, ok := meanInterval(last); ok {
		bi := nym(Measured, "light indexer: mean block interval over the window")
		bi.Value, bi.Window = round2(iv), winLabel
		rel["block_interval_mean_s"] = bi
	} else {
		rel["block_interval_mean_s"] = notYet("light indexer", "fewer than two blocks")
	}
	rel["incidents"] = notYet("the reliability team's incident log", "")
	rel["payout_integrity"] = notYet("pool ledger replay versus on-chain payouts", "no pool payouts yet")
	rel["rejected_and_stale_work"] = notYet("pool-reported rates; opt-in miner telemetry", "")
	rel["independently_observed_availability"] = notYet("probes by unaffiliated operators", "")
	d.Dimensions["reliability"] = rel

	// Ownership
	own := map[string]Measure{}
	txs, ok := 0, 0
	for _, r := range last {
		txs += r.TxCount
		ok += r.TxOK
	}
	ts := nym(Measured, "light indexer: receipts with status 1 over transactions in the window")
	ts.Window = winLabel
	if txs == 0 {
		ts = notYet("light indexer", "no transactions in the window")
	} else {
		ts.Value = map[string]interface{}{"transactions": txs, "succeeded": ok, "ratio": round2(float64(ok) / float64(txs))}
	}
	own["transaction_success"] = ts
	own["safe_custody"] = notYet("product engineer B's wallet gate results", "")
	own["independent_wallet_access"] = notYet("curated with evidence links", "")
	own["exit_liquidity"] = notYet("venue data with the published method", "no venues")
	d.Dimensions["ownership"] = own

	// Adoption
	ad := map[string]Measure{}
	active := distinct(last, func(r chain.Row) []string { return append(append([]string{}, r.From...), r.To...) })
	activePrev := distinct(prev, func(r chain.Row) []string { return append(append([]string{}, r.From...), r.To...) })
	aa := nym(Measured, "light indexer: distinct transaction senders and recipients over the window")
	aa.Value, aa.Window = len(active), winLabel
	ad["active_addresses"] = aa
	retained := 0
	for a := range active {
		if _, was := activePrev[a]; was {
			retained++
		}
	}
	ra := nym(Measured, "light indexer: addresses active in this window and the previous one; Labs addresses and subsidised activity not yet excluded")
	ra.Value, ra.Window = retained, "two consecutive windows of "+itoa(w)+" blocks"
	if len(prev) == 0 {
		ra = notYet("light indexer", "needs two full windows")
	}
	ad["retained_users"] = ra
	if len(in.Contracts) > 0 {
		contractSet := map[string]string{}
		for name, addr := range in.Contracts {
			contractSet[strings.ToLower(addr)] = name
		}
		labs := map[string]struct{}{}
		for _, a := range in.Labs {
			labs[strings.ToLower(a)] = struct{}{}
		}
		byContract := map[string]int{}
		total := 0
		for _, r := range last {
			for k, to := range r.To {
				name, ok := contractSet[strings.ToLower(to)]
				if !ok {
					continue
				}
				if k < len(r.From) {
					if _, isLabs := labs[strings.ToLower(r.From[k])]; isLabs {
						continue
					}
				}
				byContract[name]++
				total++
			}
		}
		// Events emitted by the published contracts in transactions from
		// external senders, named by their signature hash (Pay and the
		// ownership standards); an unknown signature is counted under its
		// hash so nothing is hidden.
		eventsByContract := map[string]int{}
		eventsByName := map[string]int{}
		eventTotal := 0
		for _, r := range last {
			for _, e := range r.Events {
				name, ok := contractSet[strings.ToLower(e.Address)]
				if !ok {
					continue
				}
				if e.Tx < len(r.From) {
					if _, isLabs := labs[strings.ToLower(r.From[e.Tx])]; isLabs {
						continue
					}
				}
				eventsByContract[name]++
				eventsByName[name+"."+chain.EventName(e.Topic0)]++
				eventTotal++
			}
		}
		ue := nym(Measured, "light indexer: transactions to the published application contracts from senders that are neither Labs nor seat test keys, and the events those contracts emitted in them (Pay: invoices, payments, credits, claims; ownership: transfers)")
		ue.Value, ue.Window = map[string]interface{}{"total": total, "by_contract": byContract, "events": eventTotal, "events_by_contract": eventsByContract, "events_by_name": eventsByName}, winLabel
		ad["useful_external_transactions"] = ue
		// The seats' own test use is shown apart, never inside the external count (the plan, page 29).
		seatTotal := 0
		for _, r := range last {
			for k, to := range r.To {
				if _, ok := contractSet[strings.ToLower(to)]; !ok || k >= len(r.From) {
					continue
				}
				if _, isLabs := labs[strings.ToLower(r.From[k])]; isLabs {
					seatTotal++
				}
			}
		}
		st := nym(Measured, "light indexer: transactions to the published contracts from Labs and seat test addresses (deployment checks and smoke runs); excluded from the external count")
		st.Value, st.Window = seatTotal, winLabel
		ad["seat_test_use"] = st
	} else {
		ad["useful_external_transactions"] = notYet("light indexer: Pay and ownership contract events", "no contracts curated")
	}
	ad["independent_applications"] = notYet("curated (DESIGN.md section 4)", "")
	d.Dimensions["adoption"] = ad

	// Independence
	ind := map[string]Measure{}
	if len(last) > 0 {
		shares := producerShares(last)
		cc := nym(Measured, "light indexer: share of blocks by producer address over the window")
		cc.Value, cc.Window = map[string]interface{}{"largest": round2(shares[0]), "top_three": round2(sum(shares, 3)), "producers": len(shares)}, winLabel
		ind["control_concentration"] = cc
	} else {
		ind["control_concentration"] = notYet("light indexer", "no blocks indexed")
	}
	ind["replaceable_services"] = notYet("curated with probes", "")
	ind["maintainer_diversity"] = notYet("the mirrors' history", "")
	ind["funded_continuity"] = notYet("the fund's published accounts (P-I04)", "")
	d.Dimensions["independence"] = ind

	// Market position
	mk := map[string]Measure{}
	mk["valuation_and_liquidity_comparison"] = notYet("a published script against public venue data within the frozen peer set (P-I06)", "peer set not frozen")
	d.Dimensions["market_position"] = mk

	// Supply (the public supply accounting of DESIGN.md section 5)
	sp := map[string]Measure{}
	if in.Supply != nil {
		s := nym(Measured, "node igneum_supply at the indexed head; replayable from the issuance table")
		s.Value = map[string]string{
			"height": in.Supply.Height.Int.String(), "hash": in.Supply.Hash,
			"issued_wei": str(in.Supply.Issued.Int), "fund_issued_wei": str(in.Supply.FundIssued.Int),
			"burned_wei": str(in.Supply.Burned.Int), "net_wei": str(in.Supply.Net.Int),
		}
		sp["supply"] = s
	} else {
		sp["supply"] = notYet("node igneum_supply", "the node did not answer igneum_supply")
	}
	d.Dimensions["supply"] = sp
	return d
}

func tail(rows []chain.Row, n int) []chain.Row {
	if n > len(rows) {
		n = len(rows)
	}
	return rows[len(rows)-n:]
}

func distinct(rows []chain.Row, f func(chain.Row) []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, r := range rows {
		for _, a := range f(r) {
			if a != "" {
				out[strings.ToLower(a)] = struct{}{}
			}
		}
	}
	return out
}

func entryExit(prev, last []chain.Row) (entered, departed int) {
	p := distinct(prev, func(r chain.Row) []string { return []string{r.Miner} })
	l := distinct(last, func(r chain.Row) []string { return []string{r.Miner} })
	for a := range l {
		if _, was := p[a]; !was {
			entered++
		}
	}
	for a := range p {
		if _, still := l[a]; !still {
			departed++
		}
	}
	return
}

func meanInterval(rows []chain.Row) (float64, bool) {
	if len(rows) < 2 {
		return 0, false
	}
	first, last := rows[0].Timestamp, rows[len(rows)-1].Timestamp
	if last < first {
		return 0, false
	}
	return float64(last-first) / float64(len(rows)-1), true
}

func producerShares(rows []chain.Row) []float64 {
	counts := map[string]int{}
	for _, r := range rows {
		counts[strings.ToLower(r.Miner)]++
	}
	out := make([]float64, 0, len(counts))
	for _, c := range counts {
		out = append(out, float64(c)/float64(len(rows)))
	}
	sort.Sort(sort.Reverse(sort.Float64Slice(out)))
	return out
}

func sum(v []float64, n int) float64 {
	t := 0.0
	for i := 0; i < n && i < len(v); i++ {
		t += v[i]
	}
	return t
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

func itoa(n int) string { return big.NewInt(int64(n)).String() }

func str(v *big.Int) string {
	if v == nil {
		return "0"
	}
	return v.String()
}
