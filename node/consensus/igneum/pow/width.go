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

package pow

import "sync"

// The node's parallel verification width (F-219, 16 October 2026): the
// number of proofs the node verifies at once (consensus/igneum's
// VerifyWorkers), pushed to every reference that bounds its working memory
// by it. The raster form's reference once kept its buffers per OS thread
// (a thread's first hash allocated them, nothing freed while the thread
// lived; a node hashes from 50 and more threads), so the research chain's
// nodes grew 5 to 26 GB an hour; the reference now leases its buffers from
// a pool of at most this width per context. The engine sets the width when
// it is built; a reference registered before that is told then, one
// registered after is told at once.
var verifyWidth struct {
	sync.Mutex
	n     int
	hooks []func(int)
}

// SetVerifyWidth records the width and tells every registered reference.
// A width under one is ignored (the reference keeps its own default).
func SetVerifyWidth(n int) {
	if n < 1 {
		return
	}
	verifyWidth.Lock()
	verifyWidth.n = n
	hooks := make([]func(int), len(verifyWidth.hooks))
	copy(hooks, verifyWidth.hooks)
	verifyWidth.Unlock()
	for _, fn := range hooks {
		fn(n)
	}
}

// VerifyWidth is the width recorded, zero until the engine sets it.
func VerifyWidth() int {
	verifyWidth.Lock()
	defer verifyWidth.Unlock()
	return verifyWidth.n
}

// OnVerifyWidth registers a reference's setter: called with the width at
// every SetVerifyWidth, and at once when a width is already recorded.
func OnVerifyWidth(fn func(int)) {
	verifyWidth.Lock()
	verifyWidth.hooks = append(verifyWidth.hooks, fn)
	n := verifyWidth.n
	verifyWidth.Unlock()
	if n > 0 {
		fn(n)
	}
}
