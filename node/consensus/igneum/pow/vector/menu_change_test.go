package vector

import (
	"encoding/json"
	"testing"

	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/null"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// docStub is an adapter that remembers the document it was built from.
type docStub struct {
	pow.Algorithm
	doc    string
	single bool
	full   bool
	donor  any
}

func (s *docStub) SetSingleDataset(on bool) { s.single = on }
func (s *docStub) SetFullDataset(on bool)   { s.full = on }
func (s *docStub) SetDonor(d any)           { s.donor = d } // an IgneumHash adapter's surface: the donor logic applies

func docBuild(made *[]*docStub) func(name string, params []byte) (pow.Algorithm, error) {
	return func(name string, params []byte) (pow.Algorithm, error) {
		a, err := pow.NewDev(name, params)
		if err != nil {
			return nil, err
		}
		s := &docStub{Algorithm: a, doc: string(params)}
		*made = append(*made, s)
		return s, nil
	}
}

// change is a well-formed in-place change of the test coordinate: the
// base's family (no version on the null form's documents), a floor and the
// cap the reference and floor derive under the half-life 900.
func change(height uint64, form string, dRef uint64) ctypes.IgneumDocumentChange {
	return ctypes.IgneumDocumentChange{Height: height, AlgorithmParams: json.RawMessage(`{"form":"` + form + `"}`), DRef: dRef, DMin: 10, LagCap: DerivedLagCap(900, dRef, 10)}
}

func changedVector(changes ...ctypes.IgneumDocumentChange) *ctypes.IgneumVector {
	return &ctypes.IgneumVector{HalfLife: 900, Rule: "coverage", MaxLiveBlocks: 100000, Coordinates: []ctypes.IgneumCoordinate{
		{Name: "a", Algorithm: null.Name, Target: 80, DRef: 100},
		{Name: "b", Algorithm: null.Name, AlgorithmParams: json.RawMessage(`{"form":"base"}`), Target: 80, DRef: 100, DMin: 10, LagCap: 50, DocumentChanges: changes},
	}}
}

// TestSealingFormsStepDownAfterAChange (the security review's read): the sealing
// coordinate's forms hold full contexts while live; StepDownBlocks past a
// change the superseded form steps down to light, the form in force and
// the forms to come staying full.
func TestSealingFormsStepDownAfterAChange(t *testing.T) {
	var made []*docStub
	v := changedVector(change(100, "one", 200), change(300, "two", 400))
	m, err := New(v, docBuild(&made))
	if err != nil {
		t.Fatal(err)
	}
	m.SetSealCoordinate(1)
	m.SetFullDataset(true)
	base, one, two := made[1], made[2], made[3]
	full := func(h uint64, wb, w1, w2 bool) {
		t.Helper()
		m.Anchor(h)
		if base.full != wb || one.full != w1 || two.full != w2 {
			t.Fatalf("head %d: full base %v one %v two %v, want %v %v %v", h, base.full, one.full, two.full, wb, w1, w2)
		}
	}
	full(50, true, true, true)
	full(100, true, true, true)
	full(100+StepDownBlocks-1, true, true, true)
	full(100+StepDownBlocks, false, true, true)
	full(300, false, true, true)
	full(300+StepDownBlocks, false, false, true)
	full(1000, false, false, true)
	if made[0].full {
		t.Fatal("the other coordinate's form is full")
	}
}

// TestVoidEntryDropsAPendingChange (section 1b): a void entry names a
// change by its height; the menu builds no form for it and serves the base
// form across the voided height.
func TestVoidEntryDropsAPendingChange(t *testing.T) {
	var made []*docStub
	v := changedVector(change(100, "one", 200), ctypes.IgneumDocumentChange{Height: 100, Void: true}, change(300, "two", 400))
	if err := Validate(v); err != nil {
		t.Fatal(err)
	}
	m, err := New(v, docBuild(&made))
	if err != nil {
		t.Fatal(err)
	}
	if len(made) != 3 {
		t.Fatalf("adapters built: %d, want 3 (the voided change builds none)", len(made))
	}
	if m.ByCoordinateAt(1, 200).(*docStub).doc != `{"form":"base"}` {
		t.Fatal("the base form is not in force across the voided height")
	}
	if m.ByCoordinateAt(1, 300).(*docStub).doc != `{"form":"two"}` {
		t.Fatal("the change after the voided one is not in force")
	}
	if n := len(m.Changes(1)); n != 1 {
		t.Fatalf("live changes %d, want 1", n)
	}
	bad := [][]ctypes.IgneumDocumentChange{
		{{Height: 100, Void: true}}, // voids nothing
		{change(100, "one", 200), {Height: 100, Void: true}, {Height: 100, Void: true}}, // voided twice
		{change(100, "one", 200), {Height: 100, Void: true, DRef: 5}},                   // a void carrying figures
		{change(100, "one", 200), {Height: 100, Void: true}, change(100, "again", 200)}, // the voided height reused
	}
	for i, changes := range bad {
		if err := Validate(changedVector(changes...)); err == nil {
			t.Fatalf("bad void list %d admitted", i)
		}
	}
}

// TestValidateChangeFloorCapAndFamily (the security review's read): a change needs a
// floor above zero at or under its reference, the cap the reference and
// floor derive, and the coordinate's family.
func TestValidateChangeFloorCapAndFamily(t *testing.T) {
	if err := Validate(changedVector(change(100, "one", 200))); err != nil {
		t.Fatalf("a well-formed change refused: %v", err)
	}
	noFloor := change(100, "one", 200)
	noFloor.DMin = 0
	if err := Validate(changedVector(noFloor)); err == nil {
		t.Fatal("a change without a floor admitted")
	}
	under := change(100, "one", 5)
	under.LagCap = DerivedLagCap(900, 5, 10)
	if err := Validate(changedVector(under)); err == nil {
		t.Fatal("a reference under its floor admitted")
	}
	wrongCap := change(100, "one", 200)
	wrongCap.LagCap++
	if err := Validate(changedVector(wrongCap)); err == nil {
		t.Fatal("a cap the reference and floor do not derive admitted")
	}
	family := change(100, "one", 200)
	family.AlgorithmParams = json.RawMessage(`{"version":21,"form":"one"}`)
	if err := Validate(changedVector(family)); err == nil {
		t.Fatal("a change naming another family admitted")
	}
	v := changedVector(change(100, "one", 200))
	v.Coordinates[1].AlgorithmParams = json.RawMessage(`{"version":15}`)
	v.Coordinates[1].DocumentChanges[0].AlgorithmParams = json.RawMessage(`{"version":15,"dataset_mib":2048}`)
	if err := Validate(v); err != nil {
		t.Fatalf("a change of the coordinate's version refused: %v", err)
	}
	v.Coordinates[1].DocumentChanges[0].AlgorithmParams = json.RawMessage(`{"version":18}`)
	if err := Validate(v); err == nil {
		t.Fatal("a change of another version admitted")
	}
}

// TestReanchorLag: the re-anchored lag gives the new reference the
// difficulty the old had (D = dRef x 2^(-lag / H)).
func TestReanchorLag(t *testing.T) {
	if got := ReanchorLag(900, 300, 1000, 1000); got != 300 {
		t.Fatalf("the same reference moved the lag to %d", got)
	}
	// Doubling the reference adds one half-life of lag (D halves per
	// half-life of lag, so the lag grows by H to keep D).
	if got := ReanchorLag(900, 300, 1000, 2000); got != 1200 {
		t.Fatalf("a doubled reference: lag %d, want 1200", got)
	}
	if got := ReanchorLag(900, 300, 2000, 1000); got != -600 {
		t.Fatalf("a halved reference: lag %d, want -600", got)
	}
}

// TestDocumentChangeDispatchByHeight (LANE-MODIFICATION.md section 1): a
// coordinate with an in-place document change serves its base form under
// the change's height and the change's form from it: the algorithm, the
// reference, the floor and the cap; the index, name and weight stay.
func TestDocumentChangeDispatchByHeight(t *testing.T) {
	var made []*docStub
	one := ctypes.IgneumDocumentChange{Height: 100, AlgorithmParams: json.RawMessage(`{"form":"one"}`), DRef: 200, DMin: 20, LagCap: DerivedLagCap(900, 200, 20)}
	two := ctypes.IgneumDocumentChange{Height: 300, AlgorithmParams: json.RawMessage(`{"form":"two"}`), DRef: 400, DMin: 40, LagCap: DerivedLagCap(900, 400, 40)}
	v := changedVector(one, two)
	m, err := New(v, docBuild(&made))
	if err != nil {
		t.Fatal(err)
	}
	if len(made) != 4 {
		t.Fatalf("adapters built: %d, want 4 (two base forms and two changes)", len(made))
	}
	want := []struct {
		height       uint64
		doc          string
		dRef, lagCap uint64
		dMin         int64
	}{
		{0, `{"form":"base"}`, 100, 50, 10},
		{99, `{"form":"base"}`, 100, 50, 10},
		{100, `{"form":"one"}`, 200, one.LagCap, 20},
		{299, `{"form":"one"}`, 200, one.LagCap, 20},
		{300, `{"form":"two"}`, 400, two.LagCap, 40},
		{1 << 40, `{"form":"two"}`, 400, two.LagCap, 40},
	}
	for _, w := range want {
		c, ok := m.CoordinateAt(1, w.height)
		if !ok {
			t.Fatalf("height %d: no coordinate", w.height)
		}
		if got := c.Algorithm.(*docStub).doc; got != w.doc {
			t.Fatalf("height %d: the form %s, want %s", w.height, got, w.doc)
		}
		if c.DRef.Uint64() != w.dRef || c.LagCap != w.lagCap || c.Name != "b" {
			t.Fatalf("height %d: dRef %d lagCap %d name %s, want %d %d b", w.height, c.DRef.Uint64(), c.LagCap, c.Name, w.dRef, w.lagCap)
		}
		if (w.dMin < 0) != (c.DMin == nil) || (c.DMin != nil && c.DMin.Int64() != w.dMin) {
			t.Fatalf("height %d: dMin %v, want %d (-1 nil)", w.height, c.DMin, w.dMin)
		}
		if m.ByCoordinateAt(1, w.height) != c.Algorithm {
			t.Fatalf("height %d: ByCoordinateAt differs from CoordinateAt's algorithm", w.height)
		}
		if lc := m.LagCapAt(1, w.height); lc != w.lagCap {
			t.Fatalf("height %d: LagCapAt %d, want %d", w.height, lc, w.lagCap)
		}
	}
	// The base reads are the registered document's.
	if m.ByCoordinate(1).(*docStub).doc != `{"form":"base"}` {
		t.Fatal("ByCoordinate is not the base form")
	}
	if a, _ := m.CoordinateAt(0, 500); a.Algorithm != m.ByCoordinate(0) {
		t.Fatal("a coordinate without changes does not serve its base form at every height")
	}
	// The node's choices reach every form.
	m.SetSingleDataset(true)
	m.SetSealCoordinate(1)
	m.SetFullDataset(true)
	for i, s := range made {
		if !s.single {
			t.Fatalf("form %d not in the single form", i)
		}
	}
	if !made[1].full || !made[2].full || !made[3].full {
		t.Fatal("the sealing coordinate's forms (the base and both changes) are not full")
	}
	if made[0].full {
		t.Fatal("the other coordinate's form is full")
	}
}

// TestDocumentChangeReplaceReusesBuiltForms: a landing that appends a change
// builds the one form; the forms already built are reused.
func TestDocumentChangeReplaceReusesBuiltForms(t *testing.T) {
	var made []*docStub
	v := changedVector(change(100, "one", 200))
	m, err := New(v, docBuild(&made))
	if err != nil {
		t.Fatal(err)
	}
	if len(made) != 3 {
		t.Fatalf("adapters built: %d, want 3", len(made))
	}
	first := m.ByCoordinateAt(1, 100)
	v2 := changedVector(change(100, "one", 200), change(300, "two", 400))
	if err := m.Replace(v2); err != nil {
		t.Fatal(err)
	}
	if len(made) != 4 {
		t.Fatalf("adapters built after the replace: %d, want 4 (the appended change alone)", len(made))
	}
	if m.ByCoordinateAt(1, 100) != first {
		t.Fatal("the change already built was not reused")
	}
	if m.ByCoordinateAt(1, 300).(*docStub).doc != `{"form":"two"}` {
		t.Fatal("the appended change is not in force from its height")
	}
}

// TestValidateDocumentChanges: the entries ascend above the activation and
// under the retirement, each a document with a reference above zero.
func TestValidateDocumentChanges(t *testing.T) {
	ok := func(changes ...ctypes.IgneumDocumentChange) error {
		return Validate(changedVector(changes...))
	}
	if err := ok(change(100, "one", 200)); err != nil {
		t.Fatalf("one change refused: %v", err)
	}
	zero := change(0, "one", 200)
	noDoc := change(100, "one", 200)
	noDoc.AlgorithmParams = nil
	noRef := change(100, "one", 200)
	noRef.DRef, noRef.LagCap = 0, 0
	bad := map[string][]ctypes.IgneumDocumentChange{
		"zero height":    {zero},
		"not ascending":  {change(100, "one", 200), change(100, "two", 200)},
		"no document":    {noDoc},
		"zero reference": {noRef},
	}
	for name, changes := range bad {
		if err := ok(changes...); err == nil {
			t.Fatalf("%s admitted", name)
		}
	}
	v := changedVector(change(100, "one", 200))
	v.Coordinates[1].Activation = 100
	if err := Validate(v); err == nil {
		t.Fatal("a change at the activation admitted")
	}
	v.Coordinates[1].Activation = 0
	v.Coordinates[1].RetiredFrom = 100
	if err := Validate(v); err == nil {
		t.Fatal("a change at the retirement admitted")
	}
}
