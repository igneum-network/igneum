package lifecyclecheck

import "testing"

func TestOwnVectors(t *testing.T) {
	vf := Own()
	if divs := vf.Check(); len(divs) != 0 {
		t.Fatal(divs)
	}
	c := vf.Cases[0]
	if !*c.Valid {
		t.Fatal("the launch schedule is valid")
	}
	// At holds the launch row through both disabled rows; Next names no active row
	for k, want := range []int{0, 0, 0, 0, 0, 0} {
		if c.At[k] != want {
			t.Fatalf("at(%d) = %d", c.Heights[k], c.At[k])
		}
		if c.Next[k] != -1 {
			t.Fatalf("next(%d) = %d, a disabled row named", c.Heights[k], c.Next[k])
		}
	}
	want := []bool{true, true, false, false, true, false, false, true}
	for j, u := range c.Upgrades {
		if *u.Admitted != want[j] {
			t.Fatalf("%s: admitted %v, want %v", u.Name, *u.Admitted, want[j])
		}
	}
	for _, name := range []string{"entry 0 above height 0", "rows do not ascend", "minLifetime binds the next row", "terminal both hold and endHeight", "end height below the final row"} {
		for _, cc := range vf.Cases {
			if cc.Name == name && *cc.Valid {
				t.Fatalf("%s accepted", name)
			}
		}
	}
	for _, cc := range vf.Cases {
		switch cc.Name {
		case "minLifetime met exactly":
			if !*cc.Valid || cc.At[0] != 0 || cc.At[1] != 1 {
				t.Fatal(cc.Name, cc.At)
			}
		case "end height: no row above it":
			if cc.At[0] != 0 || cc.At[1] != -1 {
				t.Fatal(cc.Name, cc.At)
			}
		case "a disabled row between active rows is held through; Next skips it":
			if want := []int{0, 0, 0, 0, 2, 2}; !equalInts(cc.At, want) {
				t.Fatal(cc.Name, cc.At)
			}
			if want := []int{2, 2, 2, 2, -1, -1}; !equalInts(cc.Next, want) {
				t.Fatal(cc.Name, "next", cc.Next)
			}
		case "activation window zero disables the upgrade rule":
			if !*cc.Upgrades[0].Admitted {
				t.Fatal("window zero refused")
			}
		}
	}
	// a refused schedule leaves the held one in force
	held := vf.Cases[0].Schedule
	got, err := Apply(held, vf.Cases[0].Upgrades[3].Given, 100)
	if err == nil || got.Entries[0].Params[0] != held.Entries[0].Params[0] || string(got.Entries[0].Params) != string(held.Entries[0].Params) {
		t.Fatal("refusal did not hold the schedule")
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
