// refbridge.h: the two accessors the workers need beyond A's ABI, with in-tree
// fallbacks so HW-01 runs today: the program seed (ABI first, else height / period from
// the pinned candidate table, logged once) and the dataset item sample check (ABI when
// present, else reported as not sampled; the vector comparison remains the check).
// GPU engineer B, 9 October 2026.
#pragma once
#include <cstdint>
#include <cstddef>
#include "candidates.h"
#include "ignpow.h"

namespace igm {

// Returns false only when neither the ABI nor the table can give a seed.
bool ref_program_seed(const Candidate &c, uint64_t height, uint64_t &seed, bool &from_abi);

// IGNPOW_OK with the item in out; IGNPOW_E_ARG when the ABI has no accessor yet.
int ref_dataset_item(const ignpow_ctx *ctx, uint64_t index, void *out, size_t cap);

}  // namespace igm
