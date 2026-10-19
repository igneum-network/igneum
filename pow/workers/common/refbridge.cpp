// refbridge.cpp: see refbridge.h.
#include "refbridge.h"
#include "ignpow_ext.h"
#include <cstdio>

namespace igm {

bool ref_program_seed(const Candidate &c, uint64_t height, uint64_t &seed, bool &from_abi) {
    static bool warned = false;
    uint64_t s = 0;
    if (ignpow_program_seed(c.id, nullptr, height, &s) == IGNPOW_OK) { seed = s; from_abi = true; return true; }
    if (c.lane_model != LaneModel::cooperative || c.period == 0) return false;
    if (!warned) { fprintf(stderr, "refbridge: program seed from the pinned table (height / %u, %s); the ABI accessor is proposed to A\n", c.period, c.pin); warned = true; }
    seed = height / c.period;   // the vendored hash_mix: number = block_number / period_length
    from_abi = false;
    return true;
}

int ref_dataset_item(const ignpow_ctx *ctx, uint64_t index, void *out, size_t cap) {
    return ignpow_ctx_dataset_item(ctx, index, out, cap);
}

}  // namespace igm
