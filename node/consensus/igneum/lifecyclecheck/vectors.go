package lifecyclecheck

import (
	"encoding/json"
	"fmt"
	"os"
)

// Case is one vector: a schedule's validity, its At and Next at heights, and
// upgrade verdicts against a held schedule at a head. The expected fields
// are filled by this side (Fill) or read from another implementation's file
// (Check); verdicts are booleans so the two implementations compare on the
// rule, not on wording.
type Case struct {
	Name     string    `json:"name"`
	Schedule Schedule  `json:"schedule"`
	Valid    *bool     `json:"valid,omitempty"`
	Heights  []uint64  `json:"heights,omitempty"`
	At       []int     `json:"at,omitempty"`
	Next     []int     `json:"next,omitempty"`
	Upgrades []Upgrade `json:"upgrades,omitempty"`
}

type Upgrade struct {
	Name     string   `json:"name"`
	Head     uint64   `json:"head"`
	Given    Schedule `json:"given"`
	Admitted *bool    `json:"admitted,omitempty"`
}

type VectorFile struct {
	Relation string `json:"relation"`
	Cases    []Case `json:"cases"`
}

func (vf *VectorFile) Fill() {
	vf.Relation = "ENVELOPE-R0A-v0.md section 9.2 (the lifecycle menu), protocol B's independent implementation"
	for i := range vf.Cases {
		c := &vf.Cases[i]
		v := c.Schedule.Validate() == nil
		c.Valid = &v
		c.At, c.Next = nil, nil
		for _, h := range c.Heights {
			c.At = append(c.At, c.Schedule.At(h))
			c.Next = append(c.Next, c.Schedule.Next(h))
		}
		for j := range c.Upgrades {
			a := ValidateUpgrade(c.Schedule, c.Upgrades[j].Given, c.Upgrades[j].Head) == nil
			c.Upgrades[j].Admitted = &a
		}
	}
}

type Divergence struct{ Vector, Stage, Have, Want string }

func (d Divergence) String() string {
	return fmt.Sprintf("%s: %s: have %s want %s", d.Vector, d.Stage, d.Have, d.Want)
}

func (vf *VectorFile) Check() []Divergence {
	var divs []Divergence
	d := func(v, stage string, have, want any) {
		divs = append(divs, Divergence{v, stage, fmt.Sprint(have), fmt.Sprint(want)})
	}
	for _, c := range vf.Cases {
		v := c.Schedule.Validate() == nil
		if c.Valid != nil && v != *c.Valid {
			d(c.Name, "valid", v, *c.Valid)
			continue
		}
		for k, h := range c.Heights {
			if k < len(c.At) && c.Schedule.At(h) != c.At[k] {
				d(c.Name, fmt.Sprintf("at(%d)", h), c.Schedule.At(h), c.At[k])
				break
			}
			if k < len(c.Next) && c.Schedule.Next(h) != c.Next[k] {
				d(c.Name, fmt.Sprintf("next(%d)", h), c.Schedule.Next(h), c.Next[k])
				break
			}
		}
		for _, u := range c.Upgrades {
			a := ValidateUpgrade(c.Schedule, u.Given, u.Head) == nil
			if u.Admitted != nil && a != *u.Admitted {
				d(c.Name+" / "+u.Name, fmt.Sprintf("upgrade admitted at head %d", u.Head), a, *u.Admitted)
			}
		}
	}
	return divs
}

func ReadFile(path string) (*VectorFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var vf VectorFile
	if err := json.Unmarshal(raw, &vf); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &vf, nil
}

func bp(b bool) *bool     { return &b }
func up(u uint64) *uint64 { return &u }

// Own is this side's vector set: the launch shape (the launch document plus
// two disabled successors), each rule of 9.2 admitted and refused.
func Own() *VectorFile {
	doc := func(s string) json.RawMessage { return json.RawMessage(s) }
	launch := Schedule{Entries: []Entry{
		{Name: "launch", Height: 0, Params: doc(`{"version": 4, "item_bytes": 128, "dataset_mib": 2048}`), MinLifetime: 100000},
		{Name: "W256 challenger", Height: 500000, Params: doc(`{"version": 4, "item_bytes": 256, "dataset_mib": 2048}`), Enabled: bp(false), MinLifetime: 100000},
		{Name: "W128 at 4 GiB", Height: 900000, Params: doc(`{"version": 4, "item_bytes": 128, "dataset_mib": 4096}`), Enabled: bp(false)},
	}, Terminal: Terminal{Hold: true}, ActivationWindow: 10000}
	enabledAt900k := launch
	enabledAt900k.Entries = append([]Entry(nil), launch.Entries...)
	enabledAt900k.Entries[2] = Entry{Name: "W128 at 4 GiB", Height: 900000, Params: launch.Entries[2].Params, Enabled: bp(true)}
	alteredBelow := launch
	alteredBelow.Entries = append([]Entry(nil), launch.Entries...)
	alteredBelow.Entries[0] = Entry{Name: "launch", Height: 0, Params: doc(`{"version": 4, "item_bytes": 256, "dataset_mib": 2048}`), MinLifetime: 100000}
	added := launch
	added.Entries = append(append([]Entry(nil), launch.Entries...), Entry{Name: "successor", Height: 1300000, Params: doc(`{"version": 4, "item_bytes": 512, "dataset_mib": 4096}`)})
	addedNear := launch
	addedNear.Entries = append(append([]Entry(nil), launch.Entries...), Entry{Name: "successor", Height: 910000, Params: doc(`{"version": 4, "item_bytes": 512, "dataset_mib": 4096}`)})
	endNear := launch
	endNear.Terminal = Terminal{EndHeight: up(905000)}
	endFar := launch
	endFar.Terminal = Terminal{EndHeight: up(2000000)}
	cases := []Case{
		{Name: "launch schedule: disabled rows built and never in force", Schedule: launch, Heights: []uint64{0, 499999, 500000, 899999, 900000, 5000000},
			Upgrades: []Upgrade{
				{"same schedule", 450000, launch, nil},
				{"enable the 4 GiB row at head 450000 (900000 >= 450000 + 20000)", 450000, enabledAt900k, nil},
				{"enable the 4 GiB row at head 890000 (900000 < 890000 + 20000)", 890000, enabledAt900k, nil},
				{"alter the launch row (at or below the head)", 100, alteredBelow, nil},
				{"add a successor two windows ahead", 1200000, added, nil},
				{"add a successor nearer than two windows", 895000, addedNear, nil},
				{"end height within the margin", 890000, endNear, nil},
				{"end height two windows ahead", 890000, endFar, nil},
			}},
		{Name: "entry 0 above height 0", Schedule: Schedule{Entries: []Entry{{Name: "a", Height: 5, Params: doc(`{}`)}}, Terminal: Terminal{Hold: true}}},
		{Name: "rows do not ascend", Schedule: Schedule{Entries: []Entry{{Name: "a", Height: 0, Params: doc(`{}`)}, {Name: "b", Height: 10, Params: doc(`{}`)}, {Name: "c", Height: 10, Params: doc(`{}`)}}, Terminal: Terminal{Hold: true}}},
		{Name: "minLifetime binds the next row", Schedule: Schedule{Entries: []Entry{{Name: "a", Height: 0, Params: doc(`{}`), MinLifetime: 50}, {Name: "b", Height: 40, Params: doc(`{}`)}}, Terminal: Terminal{Hold: true}}},
		{Name: "minLifetime met exactly", Schedule: Schedule{Entries: []Entry{{Name: "a", Height: 0, Params: doc(`{}`), MinLifetime: 50}, {Name: "b", Height: 50, Params: doc(`{}`)}}, Terminal: Terminal{Hold: true}}, Heights: []uint64{49, 50}},
		{Name: "terminal both hold and endHeight", Schedule: Schedule{Entries: []Entry{{Name: "a", Height: 0, Params: doc(`{}`)}}, Terminal: Terminal{Hold: true, EndHeight: up(10)}}},
		{Name: "end height below the final row", Schedule: Schedule{Entries: []Entry{{Name: "a", Height: 0, Params: doc(`{}`)}, {Name: "b", Height: 100, Params: doc(`{}`)}}, Terminal: Terminal{EndHeight: up(90)}}},
		{Name: "end height: no row above it", Schedule: Schedule{Entries: []Entry{{Name: "a", Height: 0, Params: doc(`{}`)}}, Terminal: Terminal{EndHeight: up(10)}}, Heights: []uint64{10, 11}},
		{Name: "a disabled row between active rows is held through; Next skips it", Schedule: Schedule{Entries: []Entry{{Name: "a", Height: 0, Params: doc(`{}`)}, {Name: "b", Height: 10, Params: doc(`{}`), Enabled: bp(false)}, {Name: "c", Height: 20, Params: doc(`{}`)}}, Terminal: Terminal{Hold: true}}, Heights: []uint64{0, 9, 10, 19, 20, 100}},
		{Name: "activation window zero disables the upgrade rule", Schedule: Schedule{Entries: []Entry{{Name: "a", Height: 0, Params: doc(`{}`)}}, Terminal: Terminal{Hold: true}},
			Upgrades: []Upgrade{{"alter the launch row with no window", 100, Schedule{Entries: []Entry{{Name: "a", Height: 0, Params: doc(`{"x": 1}`)}}, Terminal: Terminal{Hold: true}}, nil}}},
	}
	vf := &VectorFile{Cases: cases}
	vf.Fill()
	return vf
}

// the protocol team's form (pow/vectors/lifecycle/vectors.json): per vector the held
// schedule (its entries without the base, which is entry 0 at height 0 named
// "base"), optionally the given one with the head, valid_schedule,
// upgrade_admitted, and At and Next by height as entry names ("" for none).

type theirSchedule struct {
	Entries          []Entry  `json:"entries"`
	Terminal         Terminal `json:"terminal"`
	ActivationWindow uint64   `json:"activationWindow"`
}

func (t theirSchedule) schedule() Schedule {
	base := Entry{Name: "base", Height: 0, Params: json.RawMessage(`{"base": true}`)}
	return Schedule{Entries: append([]Entry{base}, t.Entries...), Terminal: t.Terminal, ActivationWindow: t.ActivationWindow}
}

type theirVector struct {
	Name     string            `json:"name"`
	Held     theirSchedule     `json:"held"`
	Given    *theirSchedule    `json:"given"`
	Head     uint64            `json:"head"`
	Valid    *bool             `json:"valid_schedule"`
	Admitted *bool             `json:"upgrade_admitted"`
	At       map[string]string `json:"at"`
	Next     map[string]string `json:"next"`
}

type TheirFile struct {
	Rule    string        `json:"rule"`
	Vectors []theirVector `json:"vectors"`
}

func nameOf(s Schedule, i int) string {
	if i < 0 {
		return ""
	}
	return s.Entries[i].Name
}

// CheckTheirs runs this side's rules over the protocol team's file. valid_schedule
// is the held schedule's validity; upgrade_admitted is ValidateUpgrade's
// verdict on the given schedule at the head.
func CheckTheirs(path string) (int, []Divergence, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, nil, err
	}
	var f TheirFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return 0, nil, fmt.Errorf("%s: %w", path, err)
	}
	var divs []Divergence
	d := func(v, stage string, have, want any) {
		divs = append(divs, Divergence{v, stage, fmt.Sprint(have), fmt.Sprint(want)})
	}
	for _, v := range f.Vectors {
		held := v.Held.schedule()
		var given *Schedule
		if v.Given != nil {
			g := v.Given.schedule()
			given = &g
		}
		if v.Valid != nil {
			// valid_schedule describes the held schedule (every vector of the file reads so)
			ok := held.Validate() == nil
			if ok != *v.Valid {
				d(v.Name, "valid_schedule (the held schedule)", ok, *v.Valid)
				continue
			}
		}
		for hs, want := range v.At {
			var h uint64
			fmt.Sscanf(hs, "%d", &h)
			if got := nameOf(held, held.At(h)); got != want {
				d(v.Name, fmt.Sprintf("at(%d)", h), got, want)
			}
		}
		for hs, want := range v.Next {
			var h uint64
			fmt.Sscanf(hs, "%d", &h)
			if got := nameOf(held, held.Next(h)); got != want {
				d(v.Name, fmt.Sprintf("next(%d)", h), got, want)
			}
		}
		if v.Admitted != nil && given != nil {
			err := ValidateUpgrade(held, *given, v.Head)
			if (err == nil) != *v.Admitted {
				d(v.Name, fmt.Sprintf("upgrade_admitted at head %d", v.Head), err == nil, *v.Admitted)
				if err != nil {
					divs[len(divs)-1].Have += " (" + err.Error() + ")"
				}
			}
		}
	}
	return len(f.Vectors), divs, nil
}
