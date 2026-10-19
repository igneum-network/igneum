// Copyright 2026 The Igneum Authors
// This file is part of the Igneum node.
//
// The Igneum node is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The Igneum node is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the Igneum node. If not, see <http://www.gnu.org/licenses/>.

package schedule

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow/igneumhash"
	_ "github.com/ethereum/go-ethereum/consensus/igneum/pow/null"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

func sched(t *testing.T, rows string) *ctypes.IgneumSchedule {
	t.Helper()
	var s ctypes.IgneumSchedule
	if err := json.Unmarshal([]byte(rows), &s); err != nil {
		t.Fatal(err)
	}
	return &s
}

// TestValidate: ascending heights above zero, one terminal rule.
func TestValidate(t *testing.T) {
	good := sched(t, `{"entries": [{"name": "more-rounds", "height": 100, "algorithmParams": {"version": 1, "rounds": 128}}], "terminal": {"hold": true}}`)
	if err := Validate(igneumhash.Name, good); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		`{"entries": [], "terminal": {}}`,
		`{"entries": [], "terminal": {"hold": true, "endHeight": 5}}`,
		`{"entries": [{"name": "x", "height": 0, "algorithmParams": {"version": 1}}], "terminal": {"hold": true}}`,
		`{"entries": [{"name": "x", "height": 10, "algorithmParams": {"version": 1}}, {"name": "y", "height": 10, "algorithmParams": {"version": 1}}], "terminal": {"hold": true}}`,
		`{"entries": [{"name": "x", "height": 10, "algorithmParams": {"version": 1}}], "terminal": {"endHeight": 5}}`,
		`{"entries": [{"name": "", "height": 10, "algorithmParams": {"version": 1}}], "terminal": {"hold": true}}`,
	} {
		if err := Validate(igneumhash.Name, sched(t, bad)); err == nil {
			t.Errorf("accepted: %s", bad)
		}
	}
}

// TestDispatchByHeight: every call goes to the document in force at the
// height, each entry has its own digest, the base holds below the first row,
// the final row holds above it (hold) or the end refuses (endHeight).
func TestDispatchByHeight(t *testing.T) {
	base := []byte(`{"version": 1, "cache_mib": 1}`)
	s := sched(t, `{"entries": [{"name": "more-rounds", "height": 100, "algorithmParams": {"version": 1, "rounds": 128, "cache_mib": 1}}, {"name": "larger-dataset", "height": 200, "algorithmParams": {"version": 1, "rounds": 128, "dataset_mib": 2048, "cache_mib": 1}}], "terminal": {"hold": true}}`)
	m, err := New(igneumhash.Name, base, s, pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Entries()) != 3 || m.At(0).Name != "base" || m.At(99).Name != "base" || m.At(100).Name != "more-rounds" || m.At(199).Name != "more-rounds" || m.At(200).Name != "larger-dataset" || m.At(1<<40).Name != "larger-dataset" {
		t.Fatalf("dispatch: %v %v %v %v", m.At(99).Name, m.At(100).Name, m.At(199).Name, m.At(200).Name)
	}
	d0, d1, d2 := m.ParamsDigestAt(0), m.ParamsDigestAt(100), m.ParamsDigestAt(200)
	if d0 == d1 || d1 == d2 || d0 == d2 {
		t.Fatal("entries share a parameter digest")
	}
	h := common.HexToHash("0x42")
	// A proof made under one entry does not verify under another: the
	// document is part of the envelope.
	nonce, mix, ok := pow.Solve(m.At(100).Algorithm, h, 150, big.NewInt(1), 0, nil)
	if !ok {
		t.Fatal("no solution")
	}
	if err := m.Verify(h, 150, nonce, mix, big.NewInt(1), false); err != nil {
		t.Fatalf("own entry: %v", err)
	}
	if err := m.Verify(h, 50, nonce, mix, big.NewInt(1), false); !errors.Is(err, pow.ErrInvalidMixDigest) {
		t.Fatalf("base entry accepted a more-rounds proof: %v", err)
	}
	if err := m.Verify(h, 250, nonce, mix, big.NewInt(1), false); !errors.Is(err, pow.ErrInvalidMixDigest) {
		t.Fatalf("larger-dataset entry accepted a more-rounds proof: %v", err)
	}
	// The work package document follows the height.
	if doc := m.WorkParamsAt(150).(igneumhash.Document); doc.Rounds != 128 || doc.DatasetMiB != 1024 {
		t.Fatalf("document at 150: %+v", doc)
	}
	if doc := m.WorkParamsAt(200).(igneumhash.Document); doc.DatasetMiB != 2048 {
		t.Fatalf("document at 200: %+v", doc)
	}
	// An end height refuses above it before any work.
	e := sched(t, `{"entries": [{"name": "more-rounds", "height": 100, "algorithmParams": {"version": 1, "rounds": 128, "cache_mib": 1}}], "terminal": {"endHeight": 300}}`)
	me, err := New(igneumhash.Name, base, e, pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	if err := me.Verify(h, 301, 1, common.Hash{}, big.NewInt(1), false); !errors.Is(err, ErrBeyondEnd) {
		t.Fatalf("beyond the end: %v", err)
	}
	// At the end height the entry in force still verifies its own proof (the
	// mix binds the seal hash and the nonce, not the height, inside one entry).
	if err := me.Verify(h, 300, nonce, mix, big.NewInt(1), false); err != nil {
		t.Fatalf("at the end: %v", err)
	}
	// A bad document in a row is refused at construction, never at a height.
	if _, err := New(igneumhash.Name, base, sched(t, `{"entries": [{"name": "x", "height": 10, "algorithmParams": {"version": 1, "lanes": 4}}], "terminal": {"hold": true}}`), pow.NewDev); err == nil {
		t.Fatal("a row with an invalid document was accepted")
	}
}

// TestNoSchedule: without a schedule the base document holds for ever and
// the menu is one entry.
func TestNoSchedule(t *testing.T) {
	m, err := New("null", nil, nil, pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Entries()) != 1 || m.EndHeight() != nil || m.At(1<<62).Name != "base" {
		t.Fatal("no-schedule menu is not the base for ever")
	}
}

// TestNext: the advance notice names the first entry ahead of a height and
// nothing once the final entry holds.
func TestNext(t *testing.T) {
	base := []byte(`{"version": 1, "cache_mib": 1}`)
	s := sched(t, `{"entries": [{"name": "more-rounds", "height": 100, "algorithmParams": {"version": 1, "rounds": 128, "cache_mib": 1}}, {"name": "larger-dataset", "height": 200, "algorithmParams": {"version": 1, "rounds": 128, "dataset_mib": 2048, "cache_mib": 1}}], "terminal": {"hold": true}}`)
	m, err := New(igneumhash.Name, base, s, pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	if e := m.Next(0); e == nil || e.Name != "more-rounds" || e.Height != 100 {
		t.Fatalf("next of 0: %+v", e)
	}
	if e := m.Next(99); e == nil || e.Name != "more-rounds" {
		t.Fatalf("next of 99: %+v", e)
	}
	if e := m.Next(100); e == nil || e.Name != "larger-dataset" || e.Height != 200 {
		t.Fatalf("next of 100: %+v", e)
	}
	if e := m.Next(200); e != nil {
		t.Fatalf("next of 200: %+v", e)
	}
	params, digest, at, ok := m.NextAt(150)
	if !ok || at != 200 || params == nil || digest == (common.Hash{}) || digest != m.ParamsDigestAt(200) || digest == m.ParamsDigestAt(150) {
		t.Fatalf("next at 150: %v %x %d %v", params, digest, at, ok)
	}
	if _, _, _, ok := m.NextAt(200); ok {
		t.Fatal("an entry ahead of the final one")
	}
}

// TestLifecycleMenu: the launch schedule's shape (PLAN-TO-WIN.md point 8):
// the launch document plus two fully implemented successor configurations
// with activation disabled. A disabled row is built (node support proven)
// and never in force; At holds the previous active row through its height;
// Next names only active rows; a minimum lifetime binds the next row's
// height; the ratification rule admits a change two windows ahead and
// refuses a nearer one, a rewritten past row, and a nearer terminal change.
func TestLifecycleMenu(t *testing.T) {
	base := []byte(`{"version": 1, "cache_mib": 1}`)
	off := false
	launch := &ctypes.IgneumSchedule{
		Entries: []ctypes.IgneumScheduleEntry{
			{Name: "successor-a", Height: 100000, AlgorithmParams: []byte(`{"version": 1, "rounds": 128, "cache_mib": 1}`), Enabled: &off, MinLifetime: 50000},
			{Name: "successor-b", Height: 200000, AlgorithmParams: []byte(`{"version": 1, "rounds": 32, "cache_mib": 1}`), Enabled: &off},
		},
		Terminal:         ctypes.IgneumTerminal{Hold: true},
		ActivationWindow: 4096,
	}
	m, err := New(igneumhash.Name, base, launch, pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Entries()) != 3 || m.Entries()[1].Active || m.Entries()[2].Active {
		t.Fatal("the successors are not built as disabled rows")
	}
	for _, h := range []uint64{0, 99999, 100000, 150000, 200000, 1 << 40} {
		if m.At(h).Name != "base" {
			t.Fatalf("height %d: %q in force, the disabled successors must not take force", h, m.At(h).Name)
		}
	}
	if m.Next(0) != nil {
		t.Fatal("a disabled row announced as next")
	}
	// Enabling successor-a by a later schedule: a ratified change two windows
	// ahead passes, nearer does not; the past cannot change.
	on := true
	enabled := *launch
	enabled.Entries = append([]ctypes.IgneumScheduleEntry{}, launch.Entries...)
	enabled.Entries[0].Enabled = &on
	if err := ValidateUpgrade(launch, &enabled, 100000-2*4096); err != nil {
		t.Fatalf("two windows ahead refused: %v", err)
	}
	if err := ValidateUpgrade(launch, &enabled, 100000-2*4096+1); err == nil {
		t.Fatal("a change inside two windows accepted")
	}
	m2, err := New(igneumhash.Name, base, &enabled, pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	if m2.At(100000).Name != "successor-a" || m2.At(99999).Name != "base" || m2.At(200000).Name != "successor-a" {
		t.Fatal("the enabled successor does not take force at its height and hold through the disabled one")
	}
	if n := m2.Next(0); n == nil || n.Name != "successor-a" {
		t.Fatal("the enabled successor is not announced")
	}
	rewritten := enabled
	rewritten.Entries = append([]ctypes.IgneumScheduleEntry{}, enabled.Entries...)
	rewritten.Entries[0].AlgorithmParams = []byte(`{"version": 1, "rounds": 16, "cache_mib": 1}`)
	if err := ValidateUpgrade(&enabled, &rewritten, 150000); err == nil {
		t.Fatal("a row in force rewritten and accepted")
	}
	// The minimum lifetime binds the next row.
	tooSoon := *launch
	tooSoon.Entries = append([]ctypes.IgneumScheduleEntry{}, launch.Entries...)
	tooSoon.Entries[1].Height = 120000
	if err := Validate(igneumhash.Name, &tooSoon); err == nil {
		t.Fatal("a row inside the previous row's minimum lifetime accepted")
	}
	// The terminal cannot change nearer than two windows.
	end := uint64(150000)
	ended := enabled
	ended.Terminal = ctypes.IgneumTerminal{EndHeight: &end}
	if err := ValidateUpgrade(&enabled, &ended, 149000); err == nil {
		t.Fatal("a terminal change inside two windows accepted")
	}
	if err := ValidateUpgrade(&enabled, &ended, 100000); err != nil {
		t.Fatalf("a terminal change two windows ahead refused: %v", err)
	}
	// An end moved from beyond the margin to inside it is refused; removed
	// from beyond the margin, admitted; an end inside the margin is fixed.
	near := uint64(141000)
	moved := ended
	moved.Terminal = ctypes.IgneumTerminal{EndHeight: &near}
	if err := ValidateUpgrade(&ended, &moved, 140000); err == nil {
		t.Fatal("an end moved inside the margin accepted")
	}
	if err := ValidateUpgrade(&ended, &enabled, 100000); err != nil {
		t.Fatalf("an end removed from beyond the margin refused: %v", err)
	}
	if err := ValidateUpgrade(&moved, &ended, 140000); err == nil {
		t.Fatal("an end inside the margin moved out and accepted")
	}
	// A development schedule (no window) is never checked.
	dev := *launch
	dev.ActivationWindow = 0
	if err := ValidateUpgrade(launch, &dev, 199999); err != nil {
		t.Fatal(err)
	}
}

type lifecycleVector struct {
	Name    string                 `json:"name"`
	Held    *ctypes.IgneumSchedule `json:"held"`
	Given   *ctypes.IgneumSchedule `json:"given,omitempty"`
	Head    uint64                 `json:"head"`
	Valid   bool                   `json:"valid_schedule"`
	Upgrade *bool                  `json:"upgrade_admitted,omitempty"`
	At      map[string]string      `json:"at,omitempty"`
	Next    map[string]string      `json:"next,omitempty"`
}

// TestLifecycleVectors publishes TestLifecycleMenu's statements as vectors
// for protocol B's second reading (pow/vectors/lifecycle/vectors.json);
// WRITE_TEST_FILES=1 regenerates, otherwise the file is checked.
func TestLifecycleVectors(t *testing.T) {
	path := "../../../../../pow/vectors/lifecycle/vectors.json"
	base := []byte(`{"version": 1, "cache_mib": 1}`)
	off, on := false, true
	launch := &ctypes.IgneumSchedule{Entries: []ctypes.IgneumScheduleEntry{
		{Name: "successor-a", Height: 100000, AlgorithmParams: []byte(`{"version": 1, "rounds": 128, "cache_mib": 1}`), Enabled: &off, MinLifetime: 50000},
		{Name: "successor-b", Height: 200000, AlgorithmParams: []byte(`{"version": 1, "rounds": 32, "cache_mib": 1}`), Enabled: &off},
	}, Terminal: ctypes.IgneumTerminal{Hold: true}, ActivationWindow: 4096}
	clone := func(s *ctypes.IgneumSchedule) *ctypes.IgneumSchedule {
		c := *s
		c.Entries = append([]ctypes.IgneumScheduleEntry{}, s.Entries...)
		return &c
	}
	enabled := clone(launch)
	enabled.Entries[0].Enabled = &on
	rewritten := clone(enabled)
	rewritten.Entries[0].AlgorithmParams = []byte(`{"version": 1, "rounds": 16, "cache_mib": 1}`)
	tooSoon := clone(launch)
	tooSoon.Entries[1].Height = 120000
	end, near := uint64(260000), uint64(141000)
	ended := clone(enabled)
	ended.Terminal = ctypes.IgneumTerminal{EndHeight: &end}
	moved := clone(ended)
	moved.Terminal = ctypes.IgneumTerminal{EndHeight: &near}
	dev := clone(launch)
	dev.ActivationWindow = 0
	gen := func() []lifecycleVector {
		var vs []lifecycleVector
		add := func(name string, held, given *ctypes.IgneumSchedule, head uint64, heights []uint64) {
			v := lifecycleVector{Name: name, Held: held, Given: given, Head: head}
			v.Valid = Validate(igneumhash.Name, held) == nil
			if given != nil {
				ok := Validate(igneumhash.Name, given) == nil && ValidateUpgrade(held, given, head) == nil
				v.Upgrade = &ok
			}
			if v.Valid && len(heights) > 0 {
				m, err := New(igneumhash.Name, base, held, pow.NewDev)
				if err != nil {
					t.Fatal(err)
				}
				v.At, v.Next = map[string]string{}, map[string]string{}
				for _, h := range heights {
					v.At[fmt.Sprint(h)] = m.At(h).Name
					if n := m.Next(h); n != nil {
						v.Next[fmt.Sprint(h)] = n.Name
					} else {
						v.Next[fmt.Sprint(h)] = ""
					}
				}
			}
			vs = append(vs, v)
		}
		hs := []uint64{0, 99999, 100000, 150000, 200000, 1 << 40}
		add("launch shape: two disabled successors never in force", launch, nil, 0, hs)
		add("successor-a enabled: in force from its height, held through the disabled one", enabled, nil, 0, hs)
		add("enable two windows ahead: admitted", launch, enabled, 100000-2*4096, nil)
		add("enable inside two windows: refused", launch, enabled, 100000-2*4096+1, nil)
		add("a row in force rewritten: refused", enabled, rewritten, 150000, nil)
		add("a row inside the previous row's minimum lifetime: not a valid schedule", tooSoon, nil, 0, nil)
		add("an end introduced two windows ahead: admitted", enabled, ended, 100000, nil)
		add("an end introduced inside two windows: refused", enabled, ended, 259000, nil)
		add("an end moved from beyond the margin to inside it: refused (the given schedule also ends before its final row)", ended, moved, 140000, nil)
		add("an end removed from beyond the margin: admitted", ended, enabled, 100000, nil)
		add("an end inside the margin moved out: refused (the held schedule ends before its final row)", moved, ended, 140000, nil)
		add("a development schedule (no window): any change admitted", launch, dev, 199999, nil)
		return vs
	}
	if os.Getenv("WRITE_TEST_FILES") == "1" {
		b, _ := json.MarshalIndent(map[string]any{"rule": "the lifecycle menu (ENVELOPE-R0A-v0.md sections 9.1 and 9.2): Validate, At, Next, ValidateUpgrade; a vector gives the held schedule, optionally a given one with the head it is given at; valid_schedule is the HELD schedule's validity; upgrade_admitted is Validate(given) and ValidateUpgrade(held, given, head) both passing; At and Next are the held schedule's by height", "vectors": gen()}, "", "  ")
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Vectors []lifecycleVector `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	want := gen()
	if len(f.Vectors) != len(want) {
		t.Fatalf("%d vectors, want %d", len(f.Vectors), len(want))
	}
	for i, v := range want {
		g := f.Vectors[i]
		if g.Name != v.Name || g.Valid != v.Valid || (g.Upgrade == nil) != (v.Upgrade == nil) || (g.Upgrade != nil && *g.Upgrade != *v.Upgrade) || fmt.Sprint(g.At) != fmt.Sprint(v.At) || fmt.Sprint(g.Next) != fmt.Sprint(v.Next) {
			t.Errorf("%s differs", v.Name)
		}
	}
}

// TestUpgradeSameDocumentSpacing: a held row read back compact and the same
// row given with spacing and another key order are one row (the second
// chain's refusal of its own unchanged schedule).
func TestUpgradeSameDocumentSpacing(t *testing.T) {
	held := &ctypes.IgneumSchedule{Entries: []ctypes.IgneumScheduleEntry{{Name: "a", Height: 100, AlgorithmParams: []byte(`{"dataset_mib":2048,"version":1}`)}}, Terminal: ctypes.IgneumTerminal{Hold: true}, ActivationWindow: 20}
	given := &ctypes.IgneumSchedule{Entries: []ctypes.IgneumScheduleEntry{{Name: "a", Height: 100, AlgorithmParams: []byte(`{"version": 1, "dataset_mib": 2048}`)}}, Terminal: ctypes.IgneumTerminal{Hold: true}, ActivationWindow: 20}
	if err := ValidateUpgrade(held, given, 500); err != nil {
		t.Fatalf("an unchanged schedule refused on spacing: %v", err)
	}
	given.Entries[0].AlgorithmParams = []byte(`{"version": 1, "dataset_mib": 4096}`)
	if err := ValidateUpgrade(held, given, 500); err == nil {
		t.Fatal("a changed past document accepted")
	}
}

// TestUnkeyedTwin: a keyed document (bit 10, keyed_rounds) derives its
// twin with the bit cleared and the field removed; an unkeyed one derives
// nothing.
func TestUnkeyedTwin(t *testing.T) {
	twin, err := unkeyedTwin([]byte(`{"version": 8, "features": 5152, "keyed_rounds": 2, "dataset_mib": 2048}`))
	if err != nil || twin == nil {
		t.Fatalf("twin: %s %v", twin, err)
	}
	var f map[string]any
	if err := json.Unmarshal(twin, &f); err != nil {
		t.Fatal(err)
	}
	if _, has := f["keyed_rounds"]; has || f["features"].(float64) != 4128 || f["dataset_mib"].(float64) != 2048 {
		t.Fatalf("twin fields %v", f)
	}
	none, err := unkeyedTwin([]byte(`{"version": 4, "dataset_mib": 2048}`))
	if err != nil || none != nil {
		t.Fatalf("unkeyed document derived a twin: %s %v", none, err)
	}
}

// TestLiveEntries: the full-dataset choice and the anchor reach the live
// entries alone: the entry in force at the anchor unless its span ends
// inside the lookahead, and the successor taking force inside it (node
// seat B's 4 GB row, 13 October 2026).
func TestLiveEntries(t *testing.T) {
	base := []byte(`{"version": 1, "cache_mib": 1}`)
	s := sched(t, `{"entries": [{"name": "next", "height": 1, "algorithmParams": {"version": 1, "rounds": 128, "cache_mib": 1}}, {"name": "far", "height": 10000, "algorithmParams": {"version": 1, "rounds": 128, "dataset_mib": 2048, "cache_mib": 1}}], "terminal": {"hold": true}}`)
	m, err := New(igneumhash.Name, base, s, pow.NewDev)
	if err != nil {
		t.Fatal(err)
	}
	names := func() string {
		out := ""
		for i := range m.entries {
			if m.live(i) {
				out += m.entries[i].Name + " "
			}
		}
		return out
	}
	m.Anchor(0)
	if got := names(); got != "next " {
		t.Fatalf("at the genesis: live %q, want the successor alone (the base's span ends at 0)", got)
	}
	m.Anchor(500)
	if got := names(); got != "next " {
		t.Fatalf("at 500: live %q, want the entry in force alone", got)
	}
	m.Anchor(10000 - Lookahead)
	if got := names(); got != "far " {
		t.Fatalf("at the lookahead's edge: live %q, want the successor alone", got)
	}
	m.Anchor(10000 - Lookahead - 1)
	if got := names(); got != "next " {
		t.Fatalf("one before the lookahead: live %q", got)
	}
	m.Anchor(20000)
	if got := names(); got != "far " {
		t.Fatalf("past the last entry: live %q", got)
	}
}

// TestSingleBudgetOneFlight (the node team's seventh window): two holders
// claiming the pair's budget at once serialise, the second freeing the
// first's contexts before its own build, so one full context is ever
// held across the pair.
func TestSingleBudgetOneFlight(t *testing.T) {
	b := &singleBudget{}
	var log []string
	type holder struct{ name string }
	free := map[*holder]int{}
	type h2 struct {
		*holder
		m *map[*holder]int
	}
	ha, hb := &holder{"row"}, &holder{"twin"}
	fa := freer{func() { free[ha]++; log = append(log, "free row") }}
	fb := freer{func() { free[hb]++; log = append(log, "free twin") }}
	rel := b.Claim(fa)
	log = append(log, "row builds")
	done := make(chan struct{})
	go func() {
		r2 := b.Claim(fb)
		log = append(log, "twin builds")
		r2()
		close(done)
	}()
	// The twin's claim waits for the row's release.
	select {
	case <-done:
		t.Fatal("the twin's claim did not wait for the row's build")
	default:
	}
	rel()
	<-done
	// The interim form: no eviction between the pair (the thrash of the
	// eighth window), the builds serialised.
	if free[ha] != 0 || free[hb] != 0 {
		t.Fatalf("the pair evicted each other (%d, %d); the interim form holds one context each", free[ha], free[hb])
	}
	if want := []string{"row builds", "twin builds"}; len(log) != 2 || log[0] != want[0] || log[1] != want[1] {
		t.Fatalf("the order: %v", log)
	}
	_ = h2{}
}

type freer struct{ f func() }

func (f freer) FreeFull() { f.f() }
