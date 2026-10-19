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

import "runtime"

// The admission and resource bounds of the engine's verification paths (the
// engineering specification section 11, IH 09), by name so that a saturation
// test asserts the declared numbers. The adapter's epoch bounds are the
// algorithm packages' (igneumhash.EpochCacheEntries and the three beside it).
const (
	// ReservedCPUShare is the share of the CPUs kept free of proof
	// verification for chain progress (block execution, state, networking):
	// one in four, at least one CPU.
	ReservedCPUShare = 4
	// SubmitQueuePerWorker is the depth of the mining RPC's verification
	// queue per verifier: a submission that finds the queue full is refused
	// at once with errVerifierBusy, before any work, and the worker retries.
	SubmitQueuePerWorker = 4
	// MaxSeenPerWork is how many of the last distinct submissions are
	// remembered per work package for duplicate detection (a ring): a
	// duplicate within them is refused before any work; an older duplicate
	// costs one verification like any other submission, bounded by the queue.
	// The bound never refuses an honest submission (security's F-19).
	MaxSeenPerWork = 4096
)

// VerifyWorkers is the number of proofs verified at once, on the header
// batch path and on the mining RPC's submit path alike: the CPUs available
// to the process less the reserved share (never fewer than one). It is
// measured from GOMAXPROCS, not from the number of connected peers or
// workers.
func VerifyWorkers() int {
	n := runtime.GOMAXPROCS(0)
	reserved := n / ReservedCPUShare
	if reserved < 1 {
		reserved = 1
	}
	if w := n - reserved; w >= 1 {
		return w
	}
	return 1
}

// SubmitQueueDepth is the bound of the mining RPC's verification queue.
func SubmitQueueDepth() int { return SubmitQueuePerWorker * VerifyWorkers() }
