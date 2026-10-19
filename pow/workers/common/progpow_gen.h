// progpow_gen.h: the host-side generator of the per-period kernel program for the
// ProgPoW/KAWPOW family, in the Metal and the OpenCL C dialect. The sequence is the one
// the vendored reference's mix_rng_state and round() produce (the RNG state is taken by
// value per round, so every round runs the same program); HW-01 proves the agreement.
// GPU engineer B, 9 October 2026.
#pragma once
#include <cstdint>
#include <string>
#include "candidates.h"

namespace igm {

enum class Dialect { msl, opencl };

// Emits the source of
//   igm_period_program(mix, c_dag, dag_entry)
// for prog_seed under the candidate's constants, unrolled with constant indices:
// the cache_accesses cache merges and math_ops math merges interleaved as round()
// interleaves them, then the dag_loads global merges.
std::string generate_period_program(const Candidate &c, uint64_t prog_seed, Dialect d);

// Splices the program at the "// IGM_PERIOD_PROGRAM" marker of the base source.
std::string splice_period_program(const std::string &base_source, const std::string &program);

}  // namespace igm
