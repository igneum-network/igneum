package discovery

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func valid() Disclosure {
	return Disclosure{
		Name:               "Example pool",
		Operator:           "Example operator",
		Contact:            "ops@example.invalid",
		Endpoints:          []Endpoint{{Host: "stratum.example.invalid", Port: 3333, TLS: true}},
		FeeBps:             50,
		Scheme:             "pplns",
		WindowN:            2,
		MaturityBlocks:     120,
		MinPayoutWei:       "100000000000000000",
		PayoutSchedule:     "hourly",
		PayoutGas:          PayoutGasOperator,
		SessionIdleSeconds: 600,
		ShareRateLimit:     50,
		Regions:            []string{"eu"},
		RecordsURL:         "https://example.invalid/records",
		SharesURL:          "https://example.invalid/shares",
	}
}

func TestValidate(t *testing.T) {
	if err := valid().Validate(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*Disclosure){
		"fee over whole":  func(d *Disclosure) { d.FeeBps = 10001 },
		"free gas word":   func(d *Disclosure) { d.PayoutGas = "free" },
		"no endpoints":    func(d *Disclosure) { d.Endpoints = nil },
		"no contact":      func(d *Disclosure) { d.Contact = "" },
		"no min payout":   func(d *Disclosure) { d.MinPayoutWei = "" },
		"no records url":  func(d *Disclosure) { d.RecordsURL = "" },
		"endpoint port 0": func(d *Disclosure) { d.Endpoints[0].Port = 0 },
	}
	for name, mutate := range cases {
		d := valid()
		mutate(&d)
		if err := d.Validate(); !errors.Is(err, ErrDisclosure) {
			t.Errorf("%s: got %v", name, err)
		}
	}
}

func TestHandlerServesJSON(t *testing.T) {
	h, err := Handler(valid())
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, WellKnownPath, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var got Disclosure
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.FeeBps != 50 || got.PayoutGas != PayoutGasOperator || len(got.Endpoints) != 1 {
		t.Fatalf("round trip: %+v", got)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, WellKnownPath, nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("post status %d", rec.Code)
	}
	if _, err := Handler(Disclosure{}); !errors.Is(err, ErrDisclosure) {
		t.Fatalf("empty document: %v", err)
	}
}
