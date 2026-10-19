// seams.h: Orchard OR-0's allocation correctness test (the Apple memory seat, 10 October 2026): the guard regions around every
// dataset allocation and the boundary addresses immediately below, at and above 2 GiB, on any engine that exposes the dataset
// word, trace and guard paths (Metal first; the OpenCL engine's own `boundary` step stays the ruling's form for AMD).
//
// What one run does, on an open engine at the given height with the knobs of g_vector_knobs (params, guard_mb, storage, alloc):
//   1. the build side: the device dataset's BLAKE3 digest against the ground truth (the vector set's dataset class when a
//      dataset.json is given, else the reference's own full build) and the cache digest;
//   2. the guard side: every live dataset buffer's front and back guard read back against the canary (guard_test);
//   3. the seam items: for every seam (the 2 GiB byte address when the dataset is larger than 2 GiB; the split half when the
//      allocation is split; the last item) the items immediately below, at and above it read back from the device through the
//      split-aware path and, where the reference exposes items, compared with the reference's;
//   4. the read side: `nonces` nonces traced on the device (the full trace of every round: the address and the item words the
//      kernel consumed), each round's item words compared with the device's words at that address, banded below and above 2 GiB
//      and counted when they land on a seam item;
//   5. the record field boundary_test {below_2g, at_2g, above_2g, split_item} in the letter of security's TP-146 (n/a on a
//      dataset that does not reach 2 GiB) and a seam_test summary with the verdict; the exit code is the fault count.
#pragma once
#include <cstdint>
#include <string>
#include "engine_iface.h"

namespace igm {
int seam_check(EngineIface &eng, uint64_t height, uint32_t nonces, const std::string &dataset_vectors);
}
