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

package igneum

import (
	"testing"
	"time"
)

// TestSearchRateCountsOneSearch: the counter reads the attempts every
// thread ticked between a search's open and its close, with the wall time
// and the rate, and the search in flight while it runs; a close without an
// open changes nothing.
func TestSearchRateCountsOneSearch(t *testing.T) {
	var c searchCounter
	c.close()
	if r := c.read(); r.Attempts != 0 || r.Running {
		t.Fatalf("a close without an open: %+v", r)
	}
	c.open(77)
	c.add(64)
	c.add(64)
	c.add(32)
	live := c.read()
	if !live.Running || live.LiveAt != 77 || live.LiveCount != 160 {
		t.Fatalf("the search in flight: %+v", live)
	}
	time.Sleep(20 * time.Millisecond)
	c.close()
	r := c.read()
	if r.Running || r.Height != 77 || r.Attempts != 160 || r.Millis < 20 || r.Rate <= 0 || r.Rate > 160*1000/20.0 {
		t.Fatalf("the finished search: %+v", r)
	}
	// A second search starts from zero.
	c.open(78)
	if l := c.read(); l.LiveCount != 0 || l.Attempts != 160 {
		t.Fatalf("the second search's start: %+v", l)
	}
}
