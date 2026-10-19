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
	"sync"
	"sync/atomic"
	"time"
)

// searchCounter is the CPU sealer's own count of one search: every
// thread's attempts (the pow loop's tick, SolveTickEvery at a time) from
// the search's start to its abort or find, and the last finished search
// kept with its wall time. The meter beside it (a one-minute average)
// reads the duty, not the rate: under a sealer's short searches the live
// keyed-search rate, which dMin is pinned from (the project's rule of 04:00 UK,
// 12 October 2026), is attempts over the search's own seconds.
type searchCounter struct {
	attempts atomic.Int64 // the search in flight
	mu       sync.Mutex
	height   uint64
	startAt  time.Time
	running  bool
	last     SearchRate
}

// SearchRate is igneum_searchRate's answer: the last finished keyed search
// on this node (height, attempts, milliseconds, attempts a second) and the
// search in flight (its height, attempts so far and milliseconds so far;
// zero when none runs).
type SearchRate struct {
	Height    uint64  `json:"height"`
	Attempts  int64   `json:"attempts"`
	Millis    int64   `json:"ms"`
	Rate      float64 `json:"rate"`
	Running   bool    `json:"running"`
	LiveAt    uint64  `json:"liveHeight,omitempty"`
	LiveCount int64   `json:"liveAttempts,omitempty"`
	LiveMs    int64   `json:"liveMs,omitempty"`
}

func (c *searchCounter) open(height uint64) {
	c.mu.Lock()
	c.height, c.startAt, c.running = height, time.Now(), true
	c.attempts.Store(0)
	c.mu.Unlock()
}

func (c *searchCounter) add(n int64) { c.attempts.Add(n) }

func (c *searchCounter) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.running {
		return
	}
	ms := time.Since(c.startAt).Milliseconds()
	n := c.attempts.Load()
	c.last = SearchRate{Height: c.height, Attempts: n, Millis: ms}
	if ms > 0 {
		c.last.Rate = float64(n) * 1000 / float64(ms)
	}
	c.running = false
}

func (c *searchCounter) read() SearchRate {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.last
	if c.running {
		r.Running = true
		r.LiveAt = c.height
		r.LiveCount = c.attempts.Load()
		r.LiveMs = time.Since(c.startAt).Milliseconds()
	}
	return r
}

// searchTick is the pow loop's tick: the meter's mark and the search's
// count.
func (e *Engine) searchTick(attempts int64) {
	e.hashrate.Mark(attempts)
	e.search.add(attempts)
}

// SearchRateNow is the engine's reading (the API's igneum_searchRate).
func (e *Engine) SearchRateNow() SearchRate { return e.search.read() }
