package service

import "testing"

// the pool offers jobs only on coordinates the chain pays at the tip (the project's order of 10:3x UK, 15 October 2026)
func TestPayListOffersPaidCoordinatesOnly(t *testing.T) {
	s := &Service{cfg: Config{Coordinates: []uint8{0, 1, 2, 3, 6, 7, 15, 16}}}
	if _, known := s.coordPaid(2); known {
		t.Fatal("a pay list not yet read must not be known")
	}
	paid, unpaid := s.paidCoordinates()
	if len(paid) != 8 || len(unpaid) != 0 {
		t.Fatalf("before any read every coordinate is offered: %v %v", paid, unpaid)
	}
	s.payShares = []uint64{0, 5, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 5}
	s.payReadHeight = 4292
	paid, unpaid = s.paidCoordinates()
	if len(paid) != 2 || paid[0] != 1 || paid[1] != 16 {
		t.Fatalf("paid: %v", paid)
	}
	if len(unpaid) != 6 {
		t.Fatalf("unpaid: %v", unpaid)
	}
	if p, known := s.coordPaid(40); p || !known {
		t.Fatal("a coordinate past the list is unpaid")
	}
	pl := s.PayList()
	if pl["read_height"].(uint64) != 4292 || len(pl["unpaid"].([]int)) != 6 {
		t.Fatalf("PayList: %v", pl)
	}
}
