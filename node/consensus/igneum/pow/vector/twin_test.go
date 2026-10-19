package vector

import (
	"encoding/json"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/null"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// twinStub is a document-carrying algorithm over the null one for the
// menu's twin cell: its params and digest are the document bytes, and it
// records the donor it is given.
type twinStub struct {
	pow.Algorithm
	doc   []byte
	donor any
	full  bool
}

func (s *twinStub) WorkParams() any            { return json.RawMessage(s.doc) }
func (s *twinStub) ParamsDigest() common.Hash  { return common.BytesToHash(s.doc) }
func (s *twinStub) SetDonor(d any)             { s.donor = d }
func (s *twinStub) SetFullDataset(full bool)   { s.full = full }

// TestKeyedCoordinateTwin (0.10.54, the specification of record cb73f3b33
// section 4 item 8): a keyed coordinate's unkeyed twin is built beside its
// form from the document without bit 10 and keyed_rounds, the keyed form
// rides the twin's context, and the menu answers the twin's document and
// digest for the sealing coordinate (UnkeyedWorkParamsAt) and per
// coordinate (UnkeyedByCoordinateAt); an unkeyed coordinate answers false;
// an in-place change to a keyed document carries its own twin from its
// height; a re-registration reuses the built twin.
func TestKeyedCoordinateTwin(t *testing.T) {
	build := func(name string, params []byte) (pow.Algorithm, error) {
		base, err := pow.NewDev(null.Name, nil)
		if err != nil {
			return nil, err
		}
		return &twinStub{Algorithm: base, doc: append([]byte(nil), params...)}, nil
	}
	keyedDoc := json.RawMessage(`{"version":8,"dataset_mib":16,"features":5152,"keyed_rounds":2}`)
	plainDoc := json.RawMessage(`{"version":8,"dataset_mib":16,"features":4128}`)
	v := &ctypes.IgneumVector{
		Coordinates: []ctypes.IgneumCoordinate{
			{Name: "plain", Algorithm: "stub", AlgorithmParams: plainDoc, Target: 80, DRef: 1000, DMin: 10},
			{Name: "keyed", Algorithm: "stub", AlgorithmParams: keyedDoc, Target: 80, DRef: 2000, DMin: 10,
				DocumentChanges: []ctypes.IgneumDocumentChange{{Height: 50, AlgorithmParams: plainDoc, DRef: 2000, DMin: 10, LagCap: DerivedLagCap(60, 2000, 10)}}},
		},
		HalfLife: 60, DtMax: 60,
	}
	v.Coordinates[0].LagCap = DerivedLagCap(60, 1000, 10)
	v.Coordinates[1].LagCap = DerivedLagCap(60, 2000, 10)
	m, err := New(v, build)
	if err != nil {
		t.Fatal(err)
	}
	inner := m.p.Load()
	if inner.coords[0].Unkeyed != nil {
		t.Fatal("the unkeyed coordinate built a twin")
	}
	k := inner.coords[1]
	if k.Unkeyed == nil {
		t.Fatal("the keyed coordinate built no twin")
	}
	var f map[string]any
	if err := json.Unmarshal(k.UnkeyedParams, &f); err != nil {
		t.Fatal(err)
	}
	if _, has := f["keyed_rounds"]; has || f["features"].(float64) != 4128 {
		t.Fatalf("the twin's document: %s", k.UnkeyedParams)
	}
	if twin, err := pow.UnkeyedTwin(nil); err != nil || twin != nil {
		t.Fatalf("an empty document derived a twin: %s %v", twin, err)
	}
	// The numbers round-trip as written (the security review's guard): a field past
	// 2^53 and every non-features field of the twin equal the source's.
	big := []byte(`{"version":8,"dataset_mib":16,"features":5152,"keyed_rounds":2,"program_anchor":"00ff","wide":18446744073709551615,"ratio":1.25}`)
	bt, err := pow.UnkeyedTwin(big)
	if err != nil {
		t.Fatal(err)
	}
	var src, tw map[string]json.RawMessage
	if err := json.Unmarshal(big, &src); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(bt, &tw); err != nil {
		t.Fatal(err)
	}
	if string(tw["wide"]) != "18446744073709551615" || string(tw["features"]) != "4128" {
		t.Fatalf("the twin's numbers: wide %s features %s", tw["wide"], tw["features"])
	}
	if _, has := tw["keyed_rounds"]; has {
		t.Fatal("keyed_rounds left on the twin")
	}
	for k, v := range src {
		if k == "features" || k == "keyed_rounds" {
			continue
		}
		if string(tw[k]) != string(v) {
			t.Fatalf("the twin's %s is %s, the source's %s", k, tw[k], v)
		}
	}
	if k.Algorithm.(*twinStub).donor != k.Unkeyed {
		t.Fatal("the keyed form does not ride its twin")
	}
	// The menu's answers: per coordinate, and for the sealing coordinate.
	if _, _, keyed := m.UnkeyedByCoordinateAt(0, 10); keyed {
		t.Fatal("the unkeyed coordinate answered a twin")
	}
	params, digest, keyed := m.UnkeyedByCoordinateAt(1, 10)
	if !keyed || digest != k.Unkeyed.(*twinStub).ParamsDigest() || string(params.(json.RawMessage)) != string(k.UnkeyedParams) {
		t.Fatalf("the keyed coordinate's twin answer: %v %s %v", keyed, params, digest)
	}
	if _, _, keyed := m.UnkeyedWorkParamsAt(10); keyed {
		t.Fatal("the sealing coordinate defaulted to the keyed one")
	}
	m.SetSealCoordinate(1)
	if _, d2, keyed := m.UnkeyedWorkParamsAt(10); !keyed || d2 != digest {
		t.Fatalf("the sealing coordinate's twin answer: %v %v", keyed, d2)
	}
	// From the in-place change to the plain document the form is unkeyed.
	if _, _, keyed := m.UnkeyedByCoordinateAt(1, 50); keyed {
		t.Fatal("the changed form (unkeyed) answered a twin")
	}
	// A re-registration reuses the built twin.
	m2, err := newMenuReusing(v, build, inner)
	if err != nil {
		t.Fatal(err)
	}
	if m2.coords[1].Unkeyed != k.Unkeyed {
		t.Fatal("the re-registration rebuilt the twin")
	}
}

// TestUnkeyedAlgorithmAt (0.10.58, F-494): the twin algorithm is the one an
// unkeyed block or share of a keyed coordinate is hashed with: nil for an
// unkeyed coordinate and for a keyed coordinate's form changed back to the
// plain document; the twin itself for the keyed form in force.
func TestUnkeyedAlgorithmAt(t *testing.T) {
	build := func(name string, params []byte) (pow.Algorithm, error) {
		base, err := pow.NewDev(null.Name, nil)
		if err != nil {
			return nil, err
		}
		return &twinStub{Algorithm: base, doc: append([]byte(nil), params...)}, nil
	}
	keyedDoc := json.RawMessage(`{"version":8,"dataset_mib":16,"features":5152,"keyed_rounds":2}`)
	plainDoc := json.RawMessage(`{"version":8,"dataset_mib":16,"features":4128}`)
	v := &ctypes.IgneumVector{
		Coordinates: []ctypes.IgneumCoordinate{
			{Name: "plain", Algorithm: "stub", AlgorithmParams: plainDoc, Target: 80, DRef: 1000, DMin: 10, LagCap: DerivedLagCap(60, 1000, 10)},
			{Name: "keyed", Algorithm: "stub", AlgorithmParams: keyedDoc, Target: 80, DRef: 2000, DMin: 10, LagCap: DerivedLagCap(60, 2000, 10),
				DocumentChanges: []ctypes.IgneumDocumentChange{{Height: 50, AlgorithmParams: plainDoc, DRef: 2000, DMin: 10, LagCap: DerivedLagCap(60, 2000, 10)}}},
		},
		HalfLife: 60, DtMax: 60,
	}
	m, err := New(v, build)
	if err != nil {
		t.Fatal(err)
	}
	if m.UnkeyedAlgorithmAt(0, 10) != nil {
		t.Fatal("the unkeyed coordinate has a twin algorithm")
	}
	tw := m.UnkeyedAlgorithmAt(1, 10)
	if tw == nil || tw != m.p.Load().coords[1].Unkeyed {
		t.Fatal("the keyed coordinate's twin algorithm is not its twin")
	}
	if m.UnkeyedAlgorithmAt(1, 50) != nil {
		t.Fatal("the form changed back to the plain document has a twin")
	}
	if m.UnkeyedAlgorithmAt(9, 10) != nil {
		t.Fatal("a coordinate out of range has a twin")
	}
	// The donor pass: the twin rides the context its form holds or rides,
	// and carries its form's full flag (the adapter rides a donor only
	// under the flag).
	m.SetFullDataset(true)
	m.SetSealCoordinate(1)
	form := m.p.Load().coords[1].Algorithm.(*twinStub)
	twin := m.p.Load().coords[1].Unkeyed.(*twinStub)
	if twin.donor != form {
		t.Fatal("the sealing coordinate's twin does not ride its form")
	}
	if !form.full || twin.full != form.full {
		t.Fatalf("the twin's full flag %v differs from its form's %v", twin.full, form.full)
	}
	if other := m.p.Load().coords[0].Algorithm.(*twinStub); other.full {
		t.Fatal("the unkeyed coordinate's form is full on a node sealing another")
	}
}
