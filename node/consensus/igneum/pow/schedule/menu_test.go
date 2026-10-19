package schedule

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestLifecycleMenuAtTheLaunchClock reads docs/igneumhash/adversary/in/
// menu-lifetimes.json (ENVELOPE 9.2 and 9.3's source and the successor
// list's) and holds it to the launch clock (the project's order of 22:1x UK, 11
// October 2026): 20 s a block, 4,320 a day, the 12-month minimum 1,576,800
// blocks, the 3-month activation window 394,200, the 6-month notice
// 788,400, every entry's counts the calendar quantities, the launch
// relation's own entry present (version 8, keyed, W128 at 2 GiB, in force)
// with the two disabled successors beside it.
func TestLifecycleMenuAtTheLaunchClock(t *testing.T) {
	path := filepath.Join("..", "..", "..", "..", "..", "docs", "igneumhash", "adversary", "in", "menu-lifetimes.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("the lifecycle menu is not beside this checkout: %v", err)
	}
	var menu struct {
		Interval uint64 `json:"block_interval_s"`
		PerDay   uint64 `json:"blocks_per_day"`
		YearDays uint64 `json:"year_days"`
		Entries  map[string]struct {
			Version      uint64  `json:"version"`
			Keyed        bool    `json:"keyed"`
			ItemBytes    uint64  `json:"item_bytes"`
			DatasetMiB   uint64  `json:"dataset_mib"`
			Disabled     bool    `json:"disabled"`
			MinMonths    uint64  `json:"min_lifetime_months"`
			SuccMonths   uint64  `json:"earliest_succession_months"`
			WindowMonths uint64  `json:"activation_window_months"`
			NoticeMonths uint64  `json:"notice_months"`
			MinBlocks    uint64  `json:"min_lifetime_blocks"`
			SuccBlocks   uint64  `json:"earliest_succession_blocks"`
			WindowBlocks uint64  `json:"activation_window_blocks"`
			NoticeBlocks uint64  `json:"notice_blocks"`
			MinDays      float64 `json:"min_lifetime_days"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(data, &menu); err != nil {
		t.Fatal(err)
	}
	if menu.Interval != 20 || menu.PerDay != 86400/20 || menu.YearDays != 365 {
		t.Fatalf("clock: interval %d per day %d year %d (want 20, 4320, 365)", menu.Interval, menu.PerDay, menu.YearDays)
	}
	year := menu.PerDay * menu.YearDays // 1,576,800
	if year != 1576800 {
		t.Fatalf("a year at 20 s: %d blocks, want 1,576,800", year)
	}
	if len(menu.Entries) == 0 {
		t.Fatal("no entries")
	}
	for name, e := range menu.Entries {
		want := func(months uint64) uint64 { return year * months / 12 }
		if e.MinBlocks != want(e.MinMonths) || e.SuccBlocks != want(e.SuccMonths) || e.WindowBlocks != want(e.WindowMonths) || e.NoticeBlocks != want(e.NoticeMonths) {
			t.Fatalf("%s: blocks (%d, %d, %d, %d) are not the calendar quantities of (%d, %d, %d, %d) months at 20 s", name, e.MinBlocks, e.SuccBlocks, e.WindowBlocks, e.NoticeBlocks, e.MinMonths, e.SuccMonths, e.WindowMonths, e.NoticeMonths)
		}
		if e.MinBlocks != 1576800 || e.WindowBlocks != 394200 || e.NoticeBlocks != 788400 {
			t.Fatalf("%s: the 12-month minimum %d, the window %d, the notice %d (want 1,576,800, 394,200, 788,400)", name, e.MinBlocks, e.WindowBlocks, e.NoticeBlocks)
		}
		if e.NoticeBlocks != 2*e.WindowBlocks {
			t.Fatalf("%s: the notice is not two activation windows", name)
		}
		if uint64(e.MinDays*float64(menu.PerDay)) != e.MinBlocks {
			t.Fatalf("%s: days %v against blocks %d", name, e.MinDays, e.MinBlocks)
		}
	}
	launch, ok := menu.Entries["launch-keyed-loom-s2"]
	if !ok {
		t.Fatal("the launch relation's own entry is missing")
	}
	if launch.Version != 8 || !launch.Keyed || launch.ItemBytes != 128 || launch.DatasetMiB != 2048 || launch.Disabled {
		t.Fatalf("the launch entry: version %d keyed %v W%d %d MiB disabled %v", launch.Version, launch.Keyed, launch.ItemBytes, launch.DatasetMiB, launch.Disabled)
	}
	for _, s := range []string{"successor1-w256-2gib", "successor2-w128-4gib"} {
		e, ok := menu.Entries[s]
		if !ok || !e.Disabled {
			t.Fatalf("%s: present %v disabled %v (want a disabled successor beside the launch entry)", s, ok, e.Disabled)
		}
	}
	// The schedule's Entry carries the same count as its MinLifetime: the
	// menu's value is what a launch row is built with.
	if e := (Entry{MinLifetime: launch.MinBlocks}); e.MinLifetime != year {
		t.Fatalf("Entry.MinLifetime %d against the menu's year %d", e.MinLifetime, year)
	}
}
