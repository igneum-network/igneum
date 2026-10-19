package rewards

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type fakeCaller struct {
	body string
	err  error
	last []interface{}
}

func (f *fakeCaller) Call(ctx context.Context, method string, params interface{}, out interface{}) error {
	if f.err != nil {
		return f.err
	}
	f.last = params.([]interface{})
	return json.Unmarshal([]byte(f.body), out)
}

func TestFetchAndSummarise(t *testing.T) {
	c := &fakeCaller{body: `[
		{"height":"0x64","miner":"0x52b7d2dcc80cd2e4000000","fund":"0xd8d726b7177a80000","priorityFees":"0x10"},
		{"height":"0x65","miner":"0x52b7d2dcc80cd2e4000000","fund":"0xd8d726b7177a80000","priorityFees":"0x20"},
		{"height":102,"miner":"100000000000000000000","priorityFees":"30"}
	]`}
	records, err := Fetch(context.Background(), c, 100, 102)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 || records[2].Height != 102 || records[2].Miner.String() != "100000000000000000000" {
		t.Fatalf("records %+v", records)
	}
	if c.last[0] != "0x64" || c.last[1] != "0x66" {
		t.Fatalf("params %v", c.last)
	}
	s, err := Summarise(records)
	if err != nil {
		t.Fatal(err)
	}
	// newest is height 102; mean fees = (16 + 32 + 30) / 3 = 26
	if s.Height != 102 || s.SubsidyWei != "100000000000000000000" || s.PriorityFeeMeanWei != "26" || s.Blocks != 3 {
		t.Fatalf("summary %+v", s)
	}
}

func TestShapeAndErrors(t *testing.T) {
	if _, err := Fetch(context.Background(), &fakeCaller{body: `[{"height":"0x1"}]`}, 1, 1); !errors.Is(err, ErrShape) {
		t.Fatalf("missing miner: %v", err)
	}
	if _, err := Fetch(context.Background(), &fakeCaller{err: errors.New("no method")}, 1, 1); err == nil {
		t.Fatal("rpc error swallowed")
	}
	if _, err := Summarise(nil); !errors.Is(err, ErrShape) {
		t.Fatalf("empty: %v", err)
	}
}

func TestCurrentWindow(t *testing.T) {
	c := &fakeCaller{body: `[{"miner":"0x1","priorityFees":"0x0"}]`}
	if _, err := Current(context.Background(), c, 5); err != nil {
		t.Fatal(err)
	}
	if c.last[0] != "0x0" || c.last[1] != "0x5" {
		t.Fatalf("short chain window %v", c.last)
	}
	if _, err := Current(context.Background(), c, 1000); err != nil {
		t.Fatal(err)
	}
	if c.last[0] != "0x385" || c.last[1] != "0x3e8" { // 901..1000
		t.Fatalf("window %v", c.last)
	}
}
