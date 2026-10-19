package stratum

import (
	"encoding/json"
	"testing"
)

// 0.1.76: a job's paid coordinates travel on the notify as "paid_coords"; absent when the job carries none.
func TestNotifyCarriesPaidCoords(t *testing.T) {
	j := Job{ID: "00000000000000d1", Height: 2861, Coord: 5, HasCoord: true, CoordName: "raster-36s", PaidCoords: []uint8{5, 6, 7}}
	p := NotifyParams{Coord: coordParam(j), CoordName: j.CoordName, PaidCoords: paidCoordsParam(j)}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]json.RawMessage
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if string(back["paid_coords"]) != "[5,6,7]" {
		t.Fatalf("paid_coords on the wire: %s", back["paid_coords"])
	}
	b, _ = json.Marshal(NotifyParams{Coord: coordParam(Job{Coord: 2, HasCoord: true}), PaidCoords: paidCoordsParam(Job{})})
	var none map[string]json.RawMessage // a fresh map: Unmarshal adds keys to an existing one and never clears it
	if err := json.Unmarshal(b, &none); err != nil {
		t.Fatal(err)
	}
	if _, ok := none["paid_coords"]; ok {
		t.Fatal("paid_coords present with none")
	}
}
