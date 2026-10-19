package dashboard

import (
	"encoding/json"
	"math/big"
	"testing"
	"time"

	"igneum.network/indexer/internal/chain"
	"igneum.network/indexer/internal/rpc"
)

func rows(n int, miners []string) []chain.Row {
	out := make([]chain.Row, 0, n)
	for i := 0; i < n; i++ {
		r := chain.Row{Number: uint64(i), Hash: "0xh", Miner: miners[i%len(miners)], Timestamp: uint64(1000 + 15*i)}
		if i%5 == 0 {
			r.TxCount, r.TxOK = 2, 1
			r.From = []string{"0xA", "0xB"}
			r.To = []string{"0xC"}
			paid := chain.Topic("Paid(bytes32,address,address,address,uint256,bytes32)")
			// one Paid from the external sender (counted), one from the Labs sender (excluded)
			r.Events = []chain.Event{{Address: "0xC", Topic0: paid, Tx: 0}, {Address: "0xC", Topic0: paid, Tx: 1}}
		}
		out = append(out, r)
	}
	return out
}

func TestBuildMeasures(t *testing.T) {
	// 20 blocks: first window (10) by three miners, second window by two of them plus a new one.
	first := rows(10, []string{"0xm1", "0xm2", "0xm3"})
	second := rows(10, []string{"0xm1", "0xm2", "0xm4"})
	for i := range second {
		second[i].Number = uint64(10 + i)
		second[i].Timestamp = uint64(1000 + 15*(10+i))
	}
	all := append(first, second...)
	sup := &rpc.Supply{Hash: "0xs"}
	sup.Height = rpc.Quantity{Int: big.NewInt(19)}
	sup.Issued = rpc.Quantity{Int: big.NewInt(1000)}
	sup.FundIssued = rpc.Quantity{Int: big.NewInt(10)}
	sup.Burned = rpc.Quantity{Int: big.NewInt(1)}
	sup.Net = rpc.Quantity{Int: big.NewInt(999)}
	d := Build(Input{Network: "devnet", ChainID: 7310, Rows: all, Window: 10, Reorgs: 2, Supply: sup, Now: time.Unix(0, 0),
		Contracts: map[string]string{"IgneumPay": "0xC"}, Labs: []string{"0xB"}})
	if d.Head != 19 || d.WindowSize != 10 {
		t.Fatalf("head %d window %d", d.Head, d.WindowSize)
	}
	m := d.Dimensions["mining"]
	if m["independent_participation"].Value != 3 || m["independent_participation"].State != Measured {
		t.Fatalf("participation %+v", m["independent_participation"])
	}
	ee := m["entry_exit"].Value.(map[string]int)
	if ee["entered"] != 1 || ee["departed"] != 1 {
		t.Fatalf("entry/exit %+v", ee)
	}
	if m["accepted_work_per_wall_kwh"].State != NotYetMeasured {
		t.Fatal("wall-kWh must not be derived from the chain")
	}
	rel := d.Dimensions["reliability"]
	if rel["reorganisations_observed"].Value != 2 || rel["block_interval_mean_s"].Value != 15.0 {
		t.Fatalf("reliability %+v", rel)
	}
	ts := d.Dimensions["ownership"]["transaction_success"].Value.(map[string]interface{})
	if ts["transactions"] != 4 || ts["succeeded"] != 2 || ts["ratio"] != 0.5 {
		t.Fatalf("tx success %+v", ts)
	}
	ad := d.Dimensions["adoption"]
	if ad["active_addresses"].Value != 3 || ad["retained_users"].Value != 3 {
		t.Fatalf("adoption %+v", ad)
	}
	// Each tx-bearing row sends A -> C and B -> (none); only A's call to the Pay contract counts, B is Labs anyway.
	ue := ad["useful_external_transactions"].Value.(map[string]interface{})
	if ue["events"] != 2 || ue["events_by_name"].(map[string]int)["IgneumPay.Paid"] != 2 || ue["events_by_contract"].(map[string]int)["IgneumPay"] != 2 {
		t.Fatalf("events %+v", ue)
	}
	if ue["total"] != 2 || ue["by_contract"].(map[string]int)["IgneumPay"] != 2 {
		t.Fatalf("useful external %+v", ue)
	}
	if ad["seat_test_use"].Value != 0 {
		t.Fatalf("seat test use %+v", ad["seat_test_use"])
	}
	cc := d.Dimensions["independence"]["control_concentration"].Value.(map[string]interface{})
	if cc["producers"] != 3 || cc["largest"].(float64) < 0.3 || cc["top_three"] != 1.0 {
		t.Fatalf("concentration %+v", cc)
	}
	sp := d.Dimensions["supply"]["supply"].Value.(map[string]string)
	if sp["issued_wei"] != "1000" || sp["net_wei"] != "999" {
		t.Fatalf("supply %+v", sp)
	}
	if _, err := json.Marshal(d); err != nil {
		t.Fatal(err)
	}
}

func TestBuildEmpty(t *testing.T) {
	d := Build(Input{Network: "devnet", Window: 10, Now: time.Unix(0, 0)})
	if d.Dimensions["mining"]["independent_participation"].State != NotYetMeasured {
		t.Fatal("empty chain must not measure")
	}
	if d.Dimensions["supply"]["supply"].State != NotYetMeasured {
		t.Fatal("no supply must be not_yet_measured")
	}
	for dim, ms := range d.Dimensions {
		for name, m := range ms {
			if m.Source == "" {
				t.Fatalf("%s.%s has no source", dim, name)
			}
		}
	}
}
